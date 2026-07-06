package ics

import (
	"strings"
	"testing"
	"time"
)

// minimal .ics fixture covering common real-world patterns
const sampleICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VEVENT
DTSTART;TZID=Europe/Oslo:20260601T090000
DTEND;TZID=Europe/Oslo:20260601T100000
SUMMARY:Team standup
ATTENDEE;PARTSTAT=ACCEPTED:mailto:me@example.com
END:VEVENT
BEGIN:VEVENT
DTSTART:20260601T110000Z
DTEND:20260601T120000Z
SUMMARY:Architecture review
ATTENDEE;PARTSTAT=ACCEPTED:mailto:me@example.com
END:VEVENT
BEGIN:VEVENT
DTSTART;TZID=Europe/Oslo:20260601T130000
DTEND;TZID=Europe/Oslo:20260601T140000
SUMMARY:Declined meeting
ATTENDEE;PARTSTAT=DECLINED:mailto:me@example.com
END:VEVENT
BEGIN:VEVENT
DTSTART;TZID=Europe/Oslo:20260602T140000
DTEND;TZID=Europe/Oslo:20260602T150000
SUMMARY:Next day meeting
ATTENDEE;PARTSTAT=ACCEPTED:mailto:me@example.com
END:VEVENT
BEGIN:VEVENT
DTSTART:20260603
DTEND:20260604
SUMMARY:All-day block
END:VEVENT
END:VCALENDAR`

func TestParse_DeclinedExcluded(t *testing.T) {
	meetings, err := Parse(strings.NewReader(sampleICS))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, m := range meetings {
		if m.Summary == "Declined meeting" {
			t.Error("declined meeting should have been excluded")
		}
	}
}

func TestParse_MeetingsForDay(t *testing.T) {
	meetings, err := Parse(strings.NewReader(sampleICS))
	if err != nil {
		t.Fatal(err)
	}
	jun1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	day1 := MeetingsForDay(meetings, jun1)
	// standup (UTC 07:00-08:00) + arch review (UTC 11:00-12:00) = 2 meetings
	if len(day1) != 2 {
		t.Errorf("june 1 meetings = %d, want 2; got %+v", len(day1), day1)
	}
}

func TestParse_TotalMinutesForDay(t *testing.T) {
	meetings, err := Parse(strings.NewReader(sampleICS))
	if err != nil {
		t.Fatal(err)
	}
	jun1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	total := TotalMinutesForDay(meetings, jun1)
	// standup = 60 min + arch review = 60 min = 120
	if total != 120 {
		t.Errorf("total minutes june 1 = %d, want 120", total)
	}
}

func TestParse_AllDayEvent(t *testing.T) {
	meetings, err := Parse(strings.NewReader(sampleICS))
	if err != nil {
		t.Fatal(err)
	}
	jun3 := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	day3 := MeetingsForDay(meetings, jun3)
	// All-day block is included (user chose NOT to exclude all-day events).
	if len(day3) != 1 || day3[0].Summary != "All-day block" {
		t.Errorf("june 3 = %+v, want 1 all-day event", day3)
	}
}

func TestParse_FoldedLine(t *testing.T) {
	folded := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:20260601T120000Z\r\nDTEND:20260601T130000Z\r\nSUMMARY:Long title that is\r\n  wrapped across two lines\r\nEND:VEVENT\r\nEND:VCALENDAR"
	meetings, err := Parse(strings.NewReader(folded))
	if err != nil {
		t.Fatal(err)
	}
	if len(meetings) != 1 {
		t.Fatalf("want 1 meeting, got %d", len(meetings))
	}
	if meetings[0].Summary != "Long title that is wrapped across two lines" {
		t.Errorf("summary = %q", meetings[0].Summary)
	}
}

func TestParse_EscapedSummary(t *testing.T) {
	raw := "BEGIN:VCALENDAR\nBEGIN:VEVENT\nDTSTART:20260601T100000Z\nDTEND:20260601T110000Z\nSUMMARY:Meeting\\, planning & review\nEND:VEVENT\nEND:VCALENDAR"
	meetings, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(meetings) != 1 || meetings[0].Summary != "Meeting, planning & review" {
		t.Errorf("summary = %q", meetings[0].Summary)
	}
}

func TestIsHolidayDay(t *testing.T) { // All-day event with "holiday" in the title on 2026-06-17.
	raw := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"BEGIN:VEVENT",
		"DTSTART:20260617",
		"DTEND:20260618",
		"SUMMARY:Norway Constitution Day (Public Holiday)",
		"END:VEVENT",
		// Regular meeting on the same day — should not trigger holiday.
		"BEGIN:VEVENT",
		"DTSTART:20260617T090000Z",
		"DTEND:20260617T100000Z",
		"SUMMARY:Standup",
		"END:VEVENT",
		// All-day event WITHOUT holiday in the title.
		"BEGIN:VEVENT",
		"DTSTART:20260618",
		"DTEND:20260619",
		"SUMMARY:Team off-site",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\n")

	meetings, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	day17 := time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
	day18 := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	day19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)

	if !IsHolidayDay(meetings, day17) {
		t.Error("day with all-day 'Public Holiday' event should be detected as holiday")
	}
	if IsHolidayDay(meetings, day18) {
		t.Error("day with all-day non-holiday event should NOT be detected as holiday")
	}
	if IsHolidayDay(meetings, day19) {
		t.Error("day with no events should NOT be a holiday")
	}
	// Case-insensitive check.
	raw2 := "BEGIN:VCALENDAR\nBEGIN:VEVENT\nDTSTART:20260617\nDTEND:20260618\nSUMMARY:HOLIDAY - closed\nEND:VEVENT\nEND:VCALENDAR"
	m2, _ := Parse(strings.NewReader(raw2))
	if !IsHolidayDay(m2, day17) {
		t.Error("holiday detection should be case-insensitive")
	}
}

func TestParse_WindowsTimezoneNames(t *testing.T) {
	// Outlook exports ICS with Windows timezone names like "W. Europe Standard Time".
	// These must be resolved so events land on the correct UTC day.
	raw := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"BEGIN:VEVENT",
		// W. Europe Standard Time = UTC+2 in summer: 09:00 CEST = 07:00 UTC (same day)
		"DTSTART;TZID=W. Europe Standard Time:20260602T090000",
		"DTEND;TZID=W. Europe Standard Time:20260602T093000",
		"SUMMARY:Morning standup",
		"END:VEVENT",
		"BEGIN:VEVENT",
		// Sri Lanka Standard Time = UTC+5:30: 14:00 IST = 08:30 UTC (same day)
		"DTSTART;TZID=Sri Lanka Standard Time:20260603T140000",
		"DTEND;TZID=Sri Lanka Standard Time:20260603T143000",
		"SUMMARY:Team sync",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\n")

	meetings, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(meetings) != 2 {
		t.Fatalf("expected 2 meetings, got %d", len(meetings))
	}

	jun2 := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	jun3 := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	if TotalMinutesForDay(meetings, jun2) != 30 {
		t.Errorf("jun2 total = %d, want 30 (W. Europe timezone resolved)", TotalMinutesForDay(meetings, jun2))
	}
	if TotalMinutesForDay(meetings, jun3) != 30 {
		t.Errorf("jun3 total = %d, want 30 (Sri Lanka timezone resolved)", TotalMinutesForDay(meetings, jun3))
	}
}
