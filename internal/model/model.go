// Package model defines the domain types shared across the application and a
// few date helpers. Dates are handled in UTC to avoid timezone ambiguity in
// this first version.
package model

import "time"

// DayStatus describes how a given day should be logged.
type DayStatus string

const (
	// StatusWorking is a normal working day.
	StatusWorking DayStatus = "working"
	// StatusHoliday is a public holiday: full workday logged to the leave task.
	StatusHoliday DayStatus = "holiday"
	// StatusFullLeave is a full-day leave: full workday logged to the leave task.
	StatusFullLeave DayStatus = "full_leave"
	// StatusHalfLeave is a half-day leave: half the workday to the leave task,
	// the other half filled by meetings/activity.
	StatusHalfLeave DayStatus = "half_leave"
)

// WorklogCategory identifies the origin of a worklog line.
type WorklogCategory string

const (
	// CategoryExisting is a worklog already present in Jira.
	CategoryExisting WorklogCategory = "existing"
	// CategoryMeeting is time derived from calendar meetings.
	CategoryMeeting WorklogCategory = "meeting"
	// CategoryActivity is time derived from GitHub / local git activity.
	CategoryActivity WorklogCategory = "activity"
	// CategoryLeave is time logged to the leave/holiday task.
	CategoryLeave WorklogCategory = "leave"
	// CategoryManual is a line the user added by hand.
	CategoryManual WorklogCategory = "manual"
)

// Activity is a single signal that the user did some work on a day.
type Activity struct {
	Date   time.Time // day at 00:00:00 UTC
	Source string    // e.g. "github-commit", "github-pr", "github-review", "local-git"
	Text   string    // human-readable description (commit subject, PR title, ...)
	Ref    string    // repo/branch/URL reference
	Keys   []string  // Jira keys extracted from Text/Ref (may be empty)
}

// Meeting is a calendar event that counts toward logged time.
type Meeting struct {
	Date    time.Time // day at 00:00:00 UTC
	Start   time.Time
	End     time.Time
	Summary string
}

// Minutes returns the meeting duration in whole minutes (never negative).
func (m Meeting) Minutes() int {
	d := m.End.Sub(m.Start)
	if d < 0 {
		return 0
	}
	return int(d.Minutes())
}

// Worklog is a single time entry for a day.
type Worklog struct {
	ID       string          // Jira worklog ID (set for existing worklogs; empty for suggested)
	IssueKey string          // may be empty when unassigned
	Minutes  int             // duration in minutes (multiples of 30 for suggestions)
	Comment  string          // worklog comment
	Category WorklogCategory // origin
	Started  time.Time       // day at 12:00:00 UTC
	Author   string          // email of the author (set for existing worklogs read from Jira)
}

// DayPlan is the full proposed timesheet for a single day.
type DayPlan struct {
	Date       time.Time  // day at 00:00:00 UTC
	Status     DayStatus  // working / holiday / leave
	Existing   []Worklog  // already logged in Jira (read-only, counted toward target)
	Suggested  []Worklog  // proposed new worklogs
	Unassigned []Activity // activity with no Jira key; needs user to pick an issue
	Notes      []string   // human-readable notes about how the plan was built
}

// ExistingMinutes returns the total minutes already logged in Jira for the day.
func (d DayPlan) ExistingMinutes() int {
	total := 0
	for _, w := range d.Existing {
		total += w.Minutes
	}
	return total
}

// SuggestedMinutes returns the total minutes of proposed new worklogs.
func (d DayPlan) SuggestedMinutes() int {
	total := 0
	for _, w := range d.Suggested {
		total += w.Minutes
	}
	return total
}

// TotalMinutes returns existing + suggested minutes.
func (d DayPlan) TotalMinutes() int {
	return d.ExistingMinutes() + d.SuggestedMinutes()
}

// Day truncates t to midnight UTC (the canonical representation of a calendar day).
func Day(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// WorklogStart returns noon UTC on the given day, used as every worklog's start time.
func WorklogStart(day time.Time) time.Time {
	d := Day(day)
	return d.Add(12 * time.Hour)
}

// Weekdays returns the list of Mon-Fri days in [start, end] inclusive (UTC).
func Weekdays(start, end time.Time) []time.Time {
	var days []time.Time
	for d := Day(start); !d.After(Day(end)); d = d.AddDate(0, 0, 1) {
		switch d.Weekday() {
		case time.Saturday, time.Sunday:
			continue
		default:
			days = append(days, d)
		}
	}
	return days
}
