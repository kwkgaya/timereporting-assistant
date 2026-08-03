package ics

import (
	"strings"
	"testing"
	"time"
)

// buildICS wraps VEVENT bodies in a minimal VCALENDAR.
func buildICS(events ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + strings.Join(events, "") + "END:VCALENDAR\r\n"
}

func event(lines ...string) string {
	return "BEGIN:VEVENT\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VEVENT\r\n"
}

func daysWithMeetings(t *testing.T, ics string) map[string]int {
	t.Helper()
	ms, err := Parse(strings.NewReader(ics))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, m := range ms {
		out[m.Start.Format("2006-01-02")]++
	}
	return out
}

func TestExpandDaily(t *testing.T) {
	base := time.Now().UTC().AddDate(0, 0, -10).Format("20060102")
	got := daysWithMeetings(t, buildICS(event(
		"UID:daily-1",
		"SUMMARY:Standup",
		"DTSTART:"+base+"T090000Z",
		"DTEND:"+base+"T091500Z",
		"RRULE:FREQ=DAILY;COUNT=5",
	)))
	if len(got) != 5 {
		t.Fatalf("expected 5 daily occurrences, got %d: %v", len(got), got)
	}
}

func TestExpandWeeklyByDay(t *testing.T) {
	// Monday 2026-06-01.
	got := daysWithMeetings(t, buildICS(event(
		"UID:weekly-1",
		"SUMMARY:Sync",
		"DTSTART:20260601T090000Z",
		"DTEND:20260601T093000Z",
		"RRULE:FREQ=WEEKLY;BYDAY=MO,WE;UNTIL=20260615T000000Z",
	)))
	for _, d := range []string{"2026-06-01", "2026-06-03", "2026-06-08", "2026-06-10"} {
		if got[d] == 0 {
			t.Errorf("expected an occurrence on %s, got %v", d, got)
		}
	}
	if got["2026-06-02"] != 0 {
		t.Errorf("did not expect an occurrence on Tuesday 2026-06-02")
	}
}

func TestExpandMonthlyNthWeekday(t *testing.T) {
	// Second Tuesday of each month, starting 2026-06-09.
	got := daysWithMeetings(t, buildICS(event(
		"UID:monthly-1",
		"SUMMARY:Retro",
		"DTSTART:20260609T140000Z",
		"DTEND:20260609T150000Z",
		"RRULE:FREQ=MONTHLY;BYDAY=2TU;COUNT=3",
	)))
	for _, d := range []string{"2026-06-09", "2026-07-14", "2026-08-11"} {
		if got[d] == 0 {
			t.Errorf("expected an occurrence on %s, got %v", d, got)
		}
	}
}

func TestExdateIsExcluded(t *testing.T) {
	got := daysWithMeetings(t, buildICS(event(
		"UID:daily-2",
		"SUMMARY:Standup",
		"DTSTART:20260601T090000Z",
		"DTEND:20260601T091500Z",
		"RRULE:FREQ=DAILY;COUNT=3",
		"EXDATE:20260602T090000Z",
	)))
	if got["2026-06-02"] != 0 {
		t.Errorf("EXDATE occurrence should be excluded, got %v", got)
	}
	if got["2026-06-01"] == 0 || got["2026-06-03"] == 0 {
		t.Errorf("other occurrences should remain, got %v", got)
	}
}

func TestRecurrenceIDOverridesGeneratedOccurrence(t *testing.T) {
	ics := buildICS(
		event(
			"UID:series-1",
			"SUMMARY:Weekly",
			"DTSTART:20260601T090000Z",
			"DTEND:20260601T093000Z",
			"RRULE:FREQ=DAILY;COUNT=3",
		),
		event(
			"UID:series-1",
			"RECURRENCE-ID:20260602T090000Z",
			"SUMMARY:Weekly (moved)",
			"DTSTART:20260602T140000Z",
			"DTEND:20260602T143000Z",
		),
	)
	ms, err := Parse(strings.NewReader(ics))
	if err != nil {
		t.Fatal(err)
	}
	var onJun2 []string
	for _, m := range ms {
		if m.Start.Format("2006-01-02") == "2026-06-02" {
			onJun2 = append(onJun2, m.Start.Format("15:04")+" "+m.Summary)
		}
	}
	if len(onJun2) != 1 || !strings.Contains(onJun2[0], "moved") {
		t.Fatalf("expected only the rescheduled instance on 2026-06-02, got %v", onJun2)
	}
}

func TestNonRecurringEventStillParsed(t *testing.T) {
	got := daysWithMeetings(t, buildICS(event(
		"UID:one-off",
		"SUMMARY:One off",
		"DTSTART:20260601T090000Z",
		"DTEND:20260601T100000Z",
	)))
	if got["2026-06-01"] != 1 {
		t.Fatalf("expected the single event, got %v", got)
	}
}

func TestUnsupportedRRuleFallsBackToSingleOccurrence(t *testing.T) {
	got := daysWithMeetings(t, buildICS(event(
		"UID:weird",
		"SUMMARY:Odd",
		"DTSTART:20260601T090000Z",
		"DTEND:20260601T100000Z",
		"RRULE:FREQ=HOURLY;COUNT=5",
	)))
	if got["2026-06-01"] != 1 || len(got) != 1 {
		t.Fatalf("expected a single fallback occurrence, got %v", got)
	}
}
