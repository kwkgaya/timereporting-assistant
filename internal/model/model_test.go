package model

import (
	"testing"
	"time"
)

func TestMeetingMinutes(t *testing.T) {
	m := Meeting{
		Start: time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC),
	}
	if got := m.Minutes(); got != 30 {
		t.Errorf("Minutes() = %d, want 30", got)
	}
	// Negative duration guards to 0.
	bad := Meeting{Start: m.End, End: m.Start}
	if got := bad.Minutes(); got != 0 {
		t.Errorf("negative Minutes() = %d, want 0", got)
	}
}

func TestWorklogStartIsNoonUTC(t *testing.T) {
	day := time.Date(2026, 6, 3, 15, 4, 5, 0, time.UTC)
	start := WorklogStart(day)
	if start.Hour() != 12 || start.Minute() != 0 || start.Location() != time.UTC {
		t.Errorf("WorklogStart = %v, want noon UTC", start)
	}
	if start.Day() != 3 || start.Month() != time.June {
		t.Errorf("WorklogStart date = %v, want June 3", start)
	}
}

func TestWeekdaysSkipsWeekends(t *testing.T) {
	// June 2026: 1st is Monday. Full month has 22 weekdays.
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	days := Weekdays(start, end)
	if len(days) != 22 {
		t.Fatalf("weekday count = %d, want 22", len(days))
	}
	for _, d := range days {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			t.Errorf("weekend leaked in: %v", d)
		}
	}
}

func TestDayPlanTotals(t *testing.T) {
	d := DayPlan{
		Existing:  []Worklog{{Minutes: 60}, {Minutes: 30}},
		Suggested: []Worklog{{Minutes: 120}},
	}
	if d.ExistingMinutes() != 90 {
		t.Errorf("existing = %d, want 90", d.ExistingMinutes())
	}
	if d.SuggestedMinutes() != 120 {
		t.Errorf("suggested = %d, want 120", d.SuggestedMinutes())
	}
	if d.TotalMinutes() != 210 {
		t.Errorf("total = %d, want 210", d.TotalMinutes())
	}
}
