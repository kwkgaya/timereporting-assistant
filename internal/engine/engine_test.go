package engine

import (
	"testing"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/model"
)

var testCfg = DefaultConfig(7, "EDB-9071", "EDB-9070", false)

var jun1 = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// makeMeetings creates a slice with a single meeting of the given duration in minutes.
func makeMeetings(mins int) []model.Meeting {
	if mins <= 0 {
		return nil
	}
	start := jun1.Add(9 * time.Hour) // 09:00 UTC
	return []model.Meeting{{
		Date:    jun1,
		Start:   start,
		End:     start.Add(time.Duration(mins) * time.Minute),
		Summary: "Test meeting",
	}}
}

func activity(text, ref string) model.Activity {
	return model.Activity{Date: jun1, Source: "test", Text: text, Ref: ref}
}

// sumSuggested returns the total minutes in the plan's Suggested worklogs.
func sumSuggested(p model.DayPlan) int {
	t := 0
	for _, w := range p.Suggested {
		t += w.Minutes
	}
	return t
}

func TestWorkingDayNoExistingNoMeetings(t *testing.T) {
	acts := []model.Activity{
		activity("EDB-100 add widget", "feat/EDB-100"),
		activity("EDB-200 fix bug", "fix/EDB-200"),
	}
	plan := BuildDayPlan(testCfg, jun1, model.StatusWorking, nil, nil, acts)

	total := plan.ExistingMinutes() + sumSuggested(plan)
	if total != 420 {
		t.Errorf("total = %d, want 420 (7h)", total)
	}
	// All suggested are multiples of 30.
	for _, w := range plan.Suggested {
		if w.Minutes%30 != 0 {
			t.Errorf("suggested %q minutes = %d, not a multiple of 30", w.IssueKey, w.Minutes)
		}
	}
}

func TestWorkingDayWithMeetings(t *testing.T) {
	acts := []model.Activity{
		activity("EDB-100 work", "feat/EDB-100"),
	}
	// 90 min of meetings
	plan := BuildDayPlan(testCfg, jun1, model.StatusWorking, nil, makeMeetings(90), acts)

	total := plan.ExistingMinutes() + sumSuggested(plan)
	if total != 420 {
		t.Errorf("total = %d, want 420", total)
	}
	hasMeeting := false
	for _, w := range plan.Suggested {
		if w.IssueKey == testCfg.MeetingIssueKey {
			hasMeeting = true
			if w.Minutes != 90 {
				t.Errorf("meeting minutes = %d, want 90", w.Minutes)
			}
		}
	}
	if !hasMeeting {
		t.Error("expected a meeting worklog")
	}
}

func TestTopUpExistingWorklogs(t *testing.T) {
	existing := []model.Worklog{
		{IssueKey: "EDB-100", Minutes: 180, Category: model.CategoryExisting, Started: model.WorklogStart(jun1)},
	}
	acts := []model.Activity{activity("EDB-200 review", "EDB-200")}
	plan := BuildDayPlan(testCfg, jun1, model.StatusWorking, existing, nil, acts)

	// Existing = 180 min. Should suggest 240 more to reach 420.
	total := plan.TotalMinutes()
	if total != 420 {
		t.Errorf("total = %d, want 420", total)
	}
	if sumSuggested(plan) != 240 {
		t.Errorf("suggested = %d, want 240", sumSuggested(plan))
	}
}

func TestFullLeaveDay(t *testing.T) {
	plan := BuildDayPlan(testCfg, jun1, model.StatusFullLeave, nil, makeMeetings(60), nil)

	if len(plan.Suggested) != 1 {
		t.Fatalf("suggested len = %d, want 1", len(plan.Suggested))
	}
	w := plan.Suggested[0]
	if w.IssueKey != testCfg.LeaveIssueKey {
		t.Errorf("issue = %q, want leave key", w.IssueKey)
	}
	if w.Minutes != 420 {
		t.Errorf("minutes = %d, want 420", w.Minutes)
	}
}

func TestHalfLeaveDay(t *testing.T) {
	acts := []model.Activity{activity("EDB-100 stuff", "EDB-100")}
	plan := BuildDayPlan(testCfg, jun1, model.StatusHalfLeave, nil, nil, acts)

	total := plan.TotalMinutes()
	if total != 420 {
		t.Errorf("total = %d, want 420", total)
	}
	leaveMins := 0
	for _, w := range plan.Suggested {
		if w.IssueKey == testCfg.LeaveIssueKey {
			leaveMins += w.Minutes
		}
	}
	if leaveMins != 210 {
		t.Errorf("leave mins = %d, want 210 (3.5h)", leaveMins)
	}
}

func TestNoActivityPreFillsLeaveTask(t *testing.T) {
	plan := BuildDayPlan(testCfg, jun1, model.StatusWorking, nil, nil, nil)

	if len(plan.Suggested) != 1 {
		t.Fatalf("suggested = %d, want 1 (leave task pre-fill)", len(plan.Suggested))
	}
	if plan.Suggested[0].IssueKey != testCfg.LeaveIssueKey {
		t.Errorf("pre-fill key = %q, want leave key", plan.Suggested[0].IssueKey)
	}
}

func TestAllocateDirect(t *testing.T) {
	cases := []struct {
		weights   []int
		totalMins int
	}{
		{[]int{3, 1}, 420},
		{[]int{1, 1, 1}, 420},
		{[]int{5, 2, 1}, 390},
		{[]int{1}, 420},
	}
	for _, tc := range cases {
		fakeGroups := make([]struct{ Weight int }, len(tc.weights))
		for i, w := range tc.weights {
			fakeGroups[i].Weight = w
		}
		// Directly test allocate via a re-implementation to avoid import cycle.
		// We use the exported BuildDayPlan as an integration test instead.
		acts := make([]model.Activity, 0)
		keys := []string{"A-1", "B-2", "C-3", "D-4"}
		for i, w := range tc.weights {
			if i >= len(keys) {
				break
			}
			for j := 0; j < w; j++ {
				acts = append(acts, activity(keys[i]+" work", "ref"))
			}
		}
		plan := BuildDayPlan(
			Config{WorkdayMinutes: tc.totalMins, MeetingIssueKey: "MTG-1", LeaveIssueKey: "LVE-1"},
jun1, model.StatusWorking, nil, nil, acts,
		)
		total := sumSuggested(plan)
		if total != tc.totalMins {
			t.Errorf("weights=%v total=%d: suggested sum=%d want %d", tc.weights, tc.totalMins, total, tc.totalMins)
		}
		for _, w := range plan.Suggested {
			if w.Minutes%30 != 0 {
				t.Errorf("weights=%v: minutes=%d not multiple of 30", tc.weights, w.Minutes)
			}
		}
	}
}

func TestWorklogStartIsNoonUTC(t *testing.T) {
	plan := BuildDayPlan(testCfg, jun1, model.StatusFullLeave, nil, nil, nil)
	if len(plan.Suggested) == 0 {
		t.Fatal("no suggested worklogs")
	}
	s := plan.Suggested[0].Started
	if s.Hour() != 12 || s.Minute() != 0 || s.Location() != time.UTC {
		t.Errorf("started = %v, want noon UTC", s)
	}
}

func TestClonePreviousDay(t *testing.T) {
	acts := []model.Activity{activity("EDB-100 work", "EDB-100")}
	src := BuildDayPlan(testCfg, jun1, model.StatusWorking, nil, makeMeetings(60), acts)

	jun2 := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	dest := model.DayPlan{Date: jun2, Status: model.StatusWorking}
	dest = ClonePreviousDay(dest, src)

	if len(dest.Suggested) == 0 {
		t.Fatal("clone produced no suggested worklogs")
	}
	for _, w := range dest.Suggested {
		if w.Started.Day() != 2 {
			t.Errorf("cloned worklog started on day %d, want 2", w.Started.Day())
		}
	}
}

