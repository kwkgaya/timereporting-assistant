// Package engine builds per-day worklog plans from existing Jira worklogs,
// ICS meetings, and GitHub/git activity. It applies all the agreed-upon rules:
//   - Meetings first -> meeting task
//   - Remaining time split proportionally, rounded to 30-min blocks (sum preserved)
//   - Top-up: existing worklogs count toward the 7h target
//   - Day status: working / holiday / full-leave / half-leave
//   - No-activity day: pre-fill highlighted 7h on leave task
//   - Clone previous day's suggested worklogs onto a new day
package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/jirakey"
	"github.com/kwkgaya/timereporting-assistant/internal/model"
)

const blockMinutes = 30 // smallest allocatable unit

// Config holds the parameters the engine needs.
type Config struct {
	WorkdayMinutes  int    // normally 7*60 = 420
	MeetingIssueKey string // e.g. EDB-9071
	LeaveIssueKey   string // e.g. EDB-9070
}

// DefaultConfig builds a Config from workday hours and the two special keys.
func DefaultConfig(workdayHours float64, meetingKey, leaveKey string) Config {
	return Config{
		WorkdayMinutes:  int(workdayHours * 60),
		MeetingIssueKey: meetingKey,
		LeaveIssueKey:   leaveKey,
	}
}

// BuildDayPlan constructs a DayPlan for one weekday.
//
// Parameters:
//   - day: the date (midnight UTC)
//   - status: working / holiday / leave …
//   - existing: worklogs already in Jira for this day
//   - meetingMins: total meeting minutes from the ICS for this day
//   - activities: work signals for this day
func BuildDayPlan(cfg Config, day time.Time, status model.DayStatus,
	existing []model.Worklog, meetingMins int, activities []model.Activity,
) model.DayPlan {
	plan := model.DayPlan{
		Date:     model.Day(day),
		Status:   status,
		Existing: existing,
	}
	started := model.WorklogStart(day)
	existingMins := plan.ExistingMinutes()
	targetMins := cfg.WorkdayMinutes
	var notes []string

	switch status {
	case model.StatusHoliday, model.StatusFullLeave:
		// Entire day goes to leave task (minus what's already logged).
		remaining := targetMins - existingMins
		if remaining > 0 {
			plan.Suggested = append(plan.Suggested, model.Worklog{
				IssueKey: cfg.LeaveIssueKey,
				Minutes:  remaining,
				Comment:  leaveComment(status),
				Category: model.CategoryLeave,
				Started:  started,
			})
		}
		return plan

	case model.StatusHalfLeave:
		// Half to leave task, half available for work.
		halfMins := roundToBlock(targetMins / 2)
		leaveMins := halfMins - countExistingForKey(existing, cfg.LeaveIssueKey)
		if leaveMins > 0 {
			plan.Suggested = append(plan.Suggested, model.Worklog{
				IssueKey: cfg.LeaveIssueKey,
				Minutes:  leaveMins,
				Comment:  leaveComment(status),
				Category: model.CategoryLeave,
				Started:  started,
			})
		}
		targetMins = targetMins - halfMins
		notes = append(notes, fmt.Sprintf("half-day leave: %dmin to %s, %dmin work", halfMins, cfg.LeaveIssueKey, targetMins))
	}

	// Remaining capacity after existing non-leave worklogs.
	workExisting := existingMins - countExistingForKey(existing, cfg.LeaveIssueKey)
	remaining := targetMins - workExisting
	if remaining <= 0 {
		notes = append(notes, "already at or above target; nothing to suggest")
		plan.Notes = notes
		return plan
	}

	// --- Meetings first ---
	meetingAlloc := clamp(meetingMins, 0, remaining)
	meetingAlloc = roundToBlock(meetingAlloc)
	if meetingAlloc > 0 {
		plan.Suggested = append(plan.Suggested, model.Worklog{
			IssueKey: cfg.MeetingIssueKey,
			Minutes:  meetingAlloc,
			Comment:  fmt.Sprintf("Meetings (%s)", started.Format("2006-01-02")),
			Category: model.CategoryMeeting,
			Started:  started,
		})
		remaining -= meetingAlloc
	}

	if remaining <= 0 {
		plan.Notes = notes
		return plan
	}

	// --- Distribute remaining proportionally by Jira key ---
	grouped := jirakey.GroupByKey(activities)
	plan.Unassigned = grouped.Unassigned

	if len(grouped.Groups) == 0 {
		// No keyed activity: pre-fill full remaining on leave task (highlighted).
		notes = append(notes, "no activity found: pre-filled leave task suggestion")
		plan.Suggested = append(plan.Suggested, model.Worklog{
			IssueKey: cfg.LeaveIssueKey,
			Minutes:  remaining,
			Comment:  fmt.Sprintf("No activity found for %s — please review", started.Format("2006-01-02")),
			Category: model.CategoryLeave,
			Started:  started,
		})
		plan.Notes = notes
		return plan
	}

	allocs := allocate(grouped.Groups, remaining)
	for i, g := range grouped.Groups {
		if allocs[i] <= 0 {
			continue
		}
		desc := buildComment(g.Activities)
		plan.Suggested = append(plan.Suggested, model.Worklog{
			IssueKey: g.Key,
			Minutes:  allocs[i],
			Comment:  desc,
			Category: model.CategoryActivity,
			Started:  started,
		})
	}

	// If unassigned activity exists, note it.
	if len(grouped.Unassigned) > 0 {
		notes = append(notes, fmt.Sprintf("%d activity item(s) have no Jira key — assign in UI", len(grouped.Unassigned)))
	}

	plan.Notes = notes
	return plan
}

// ClonePreviousDay copies the Suggested worklogs from src onto dest (re-dating
// their Started field to dest's day). Existing worklogs are not copied.
func ClonePreviousDay(dest model.DayPlan, src model.DayPlan) model.DayPlan {
	started := model.WorklogStart(dest.Date)
	for _, w := range src.Suggested {
		clone := w
		clone.Started = started
		dest.Suggested = append(dest.Suggested, clone)
	}
	return dest
}

// allocate distributes totalMins across groups proportionally, rounded to
// blockMinutes, using the largest-remainder method to ensure the sum equals
// totalMins (within rounding tolerance).
func allocate(groups []jirakey.KeyGroup, totalMins int) []int {
	n := len(groups)
	if n == 0 {
		return nil
	}

	totalWeight := 0
	for _, g := range groups {
		totalWeight += g.Weight
	}

	// Compute exact quotas (in minutes).
	exact := make([]float64, n)
	for i, g := range groups {
		exact[i] = float64(g.Weight) / float64(totalWeight) * float64(totalMins)
	}

	// Floor to nearest block.
	allocs := make([]int, n)
	remainders := make([]float64, n)
	for i, e := range exact {
		blocks := int(e / float64(blockMinutes))
		allocs[i] = blocks * blockMinutes
		remainders[i] = e - float64(allocs[i])
	}

	// Distribute leftover blocks by largest remainder.
	allocated := 0
	for _, a := range allocs {
		allocated += a
	}
	leftover := totalMins - allocated
	leftoverBlocks := leftover / blockMinutes

	indices := make([]int, n)
	for i := range indices {
		indices[i] = i
	}
	sort.Slice(indices, func(a, b int) bool {
		return remainders[indices[a]] > remainders[indices[b]]
	})
	for i := 0; i < leftoverBlocks && i < n; i++ {
		allocs[indices[i]] += blockMinutes
	}

	return allocs
}

func roundToBlock(mins int) int {
	if mins <= 0 {
		return 0
	}
	blocks := (mins + blockMinutes/2) / blockMinutes
	if blocks < 1 {
		blocks = 1
	}
	return blocks * blockMinutes
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func countExistingForKey(existing []model.Worklog, key string) int {
	total := 0
	for _, w := range existing {
		if w.IssueKey == key {
			total += w.Minutes
		}
	}
	return total
}

func leaveComment(status model.DayStatus) string {
	switch status {
	case model.StatusHoliday:
		return "Public holiday [timereporting]"
	case model.StatusFullLeave:
		return "Full-day leave [timereporting]"
	case model.StatusHalfLeave:
		return "Half-day leave [timereporting]"
	default:
		return "Leave [timereporting]"
	}
}

func buildComment(acts []model.Activity) string {
	seen := map[string]bool{}
	var parts []string
	for _, a := range acts {
		t := a.Text
		if t == "" {
			t = a.Ref
		}
		if !seen[t] {
			seen[t] = true
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return "Work logged"
	}
	s := strings.Join(parts, "; ")
	const maxLen = 250
	if len(s) > maxLen {
		s = s[:maxLen-1] + "…"
	}
	return s
}
