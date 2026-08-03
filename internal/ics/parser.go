// Package ics parses iCalendar (.ics) files to extract meeting events for
// a given date. Only VEVENT components are considered. Events the user
// declined (PARTSTAT=DECLINED) are excluded. All-day events, public holidays,
// and focus-time holds are included (per user preference).
package ics

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kwkgaya/timereporting-assistant/internal/model"
)

// httpClient is used for fetching published calendar URLs.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// ParseFile opens an .ics file and returns the meetings it contains.
func ParseFile(path string) ([]model.Meeting, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// ParseURL fetches an ICS document from url and returns the meetings it contains.
// A 30-second timeout is applied to the HTTP request.
func ParseURL(rawURL string) ([]model.Meeting, error) {
	// Normalise webcal:// to https:// (common for Outlook published links).
	if strings.HasPrefix(strings.ToLower(rawURL), "webcal://") {
		rawURL = "https://" + rawURL[len("webcal://"):]
	}
	if !strings.HasPrefix(strings.ToLower(rawURL), "https://") &&
		!strings.HasPrefix(strings.ToLower(rawURL), "http://") {
		return nil, fmt.Errorf("calendar URL must start with https:// or http:// (got %q)", rawURL)
	}
	resp, err := httpClient.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("fetch calendar URL: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("calendar URL returned %d", resp.StatusCode)
	}
	return Parse(resp.Body)
}

// vevent is a raw calendar event before recurrence expansion.
type vevent struct {
	uid        string
	start, end time.Time
	summary    string
	declined   bool
	rruleRaw   string
	exdates    []time.Time
	recurID    time.Time
	hasRecurID bool
}

// Parse reads iCalendar data from r and returns meeting events. Recurring
// events are expanded into individual occurrences.
func Parse(r io.Reader) ([]model.Meeting, error) {
	lines, err := unfold(r)
	if err != nil {
		return nil, err
	}
	var events []vevent
	var cur vevent
	var inEvent bool

	for _, line := range lines {
		name, params, value := splitLine(line)
		switch {
		case name == "BEGIN" && value == "VEVENT":
			cur = vevent{}
			inEvent = true
		case name == "END" && value == "VEVENT" && inEvent:
			if !cur.declined && !cur.start.IsZero() && !cur.end.IsZero() && cur.end.After(cur.start) {
				events = append(events, cur)
			}
			inEvent = false
		case !inEvent:
			// Outside a VEVENT (VTIMEZONE, VCALENDAR headers) — ignore.
		case name == "SUMMARY":
			cur.summary = decodeValue(value)
		case name == "UID":
			cur.uid = value
		case name == "DTSTART":
			cur.start = parseDateTime(value, params)
		case name == "DTEND":
			cur.end = parseDateTime(value, params)
		case name == "RRULE":
			cur.rruleRaw = value
		case name == "EXDATE":
			for _, v := range strings.Split(value, ",") {
				if t := parseDateTime(strings.TrimSpace(v), params); !t.IsZero() {
					cur.exdates = append(cur.exdates, t)
				}
			}
		case name == "RECURRENCE-ID":
			cur.recurID = parseDateTime(value, params)
			cur.hasRecurID = !cur.recurID.IsZero()
		case name == "ATTENDEE":
			if isDeclined(params, value) {
				cur.declined = true
			}
		}
	}
	return expandEvents(events, time.Now().UTC()), nil
}

// expandEvents turns raw VEVENTs into meetings, expanding recurrence rules and
// honouring EXDATE exclusions and RECURRENCE-ID overrides.
func expandEvents(events []vevent, now time.Time) []model.Meeting {
	// A VEVENT with RECURRENCE-ID is a rescheduled instance of its series; the
	// generated occurrence for that date must be dropped in favour of it.
	overrides := map[string]bool{}
	for _, e := range events {
		if e.hasRecurID {
			overrides[e.uid+"|"+model.Day(e.recurID).Format("2006-01-02")] = true
		}
	}
	from := now.AddDate(-expandYearsBack, 0, 0)
	to := now.AddDate(expandYearsForward, 0, 0)

	var out []model.Meeting
	add := func(e vevent, start, end time.Time) {
		out = append(out, model.Meeting{
			Date:    model.Day(start),
			Start:   start,
			End:     end,
			Summary: e.summary,
		})
	}
	for _, e := range events {
		r, ok := parseRRule(e.rruleRaw)
		if e.rruleRaw == "" || e.hasRecurID || !ok {
			add(e, e.start, e.end)
			continue
		}
		dur := e.end.Sub(e.start)
		excluded := map[string]bool{}
		for _, x := range e.exdates {
			excluded[model.Day(x).Format("2006-01-02")] = true
		}
		for _, s := range r.occurrences(e.start, from, to) {
			key := model.Day(s).Format("2006-01-02")
			if excluded[key] || overrides[e.uid+"|"+key] {
				continue
			}
			add(e, s, s.Add(dur))
		}
	}
	return out
}

// MeetingsForDay returns meetings from all that fall on the given UTC day.
func MeetingsForDay(all []model.Meeting, day time.Time) []model.Meeting {
	target := model.Day(day)
	var out []model.Meeting
	for _, m := range all {
		if model.Day(m.Start).Equal(target) {
			out = append(out, m)
		}
	}
	return out
}

// TotalMinutesForDay sums the duration of all meetings on day.
func TotalMinutesForDay(all []model.Meeting, day time.Time) int {
	total := 0
	for _, m := range MeetingsForDay(all, day) {
		total += m.Minutes()
	}
	return total
}

// IsHolidayDay reports whether any all-day event on day looks like a public
// holiday. An event qualifies when its summary (case-insensitive) contains
// "holiday" or "poya day".
func IsHolidayDay(all []model.Meeting, day time.Time) bool {
	for _, m := range MeetingsForDay(all, day) {
		if m.Minutes() < 24*60 {
			continue
		}
		lower := strings.ToLower(m.Summary)
		if strings.Contains(lower, "holiday") || strings.Contains(lower, "poya day") {
			return true
		}
	}
	return false
}

// unfold joins continuation lines (RFC 5545 §3.1: lines beginning with
// SPACE or TAB are continuations of the preceding line).
func unfold(r io.Reader) ([]string, error) {
	var lines []string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		raw := scanner.Text()
		if len(raw) == 0 {
			continue
		}
		// Continuation line.
		if (raw[0] == ' ' || raw[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += raw[1:]
		} else {
			lines = append(lines, raw)
		}
	}
	return lines, scanner.Err()
}

// splitLine splits a content line into name, params string, and value.
// e.g. "DTSTART;TZID=America/New_York:20260601T090000"
//
//	-> name="DTSTART", params="TZID=America/New_York", value="20260601T090000"
func splitLine(line string) (name, params, value string) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return strings.ToUpper(line), "", ""
	}
	left := line[:colon]
	value = line[colon+1:]
	semi := strings.IndexByte(left, ';')
	if semi < 0 {
		return strings.ToUpper(left), "", value
	}
	return strings.ToUpper(left[:semi]), left[semi+1:], value
}

// parseDateTime parses a date-time or date value using the TZID param if
// present, falling back to UTC if the value ends in 'Z', or local time
// otherwise (treated as UTC in this tool).
func parseDateTime(value, params string) time.Time {
	// DATE-only (e.g. all-day events) — treat as midnight UTC.
	if len(value) == 8 {
		t, err := time.ParseInLocation("20060102", value, time.UTC)
		if err != nil {
			return time.Time{}
		}
		return t
	}

	// Strip trailing Z; it signals UTC explicitly.
	isUTC := strings.HasSuffix(value, "Z")
	v := strings.TrimSuffix(value, "Z")

	// Try to find a TZID in params.
	var loc *time.Location
	for _, param := range strings.Split(params, ";") {
		if strings.HasPrefix(strings.ToUpper(param), "TZID=") {
			raw := param[5:]
			// URL-decode in case of quoted/percent-encoded values.
			if dec, err := url.QueryUnescape(raw); err == nil {
				raw = dec
			}
			// Try IANA name first, then Windows tz name fallback.
			if l, err := time.LoadLocation(raw); err == nil {
				loc = l
			} else if iana, ok := windowsToIANA[raw]; ok {
				if l, err := time.LoadLocation(iana); err == nil {
					loc = l
				}
			}
			break
		}
	}
	if loc == nil || isUTC {
		loc = time.UTC
	}

	layouts := []string{"20060102T150405", "20060102T1504", "20060102"}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// isDeclined returns true if the ATTENDEE line indicates PARTSTAT=DECLINED.
func isDeclined(params, _ string) bool {
	for _, part := range strings.Split(params, ";") {
		if strings.EqualFold(strings.TrimSpace(part), "PARTSTAT=DECLINED") {
			return true
		}
	}
	return false
}

// decodeValue handles basic text unescaping per RFC 5545 (\\, \n, \,).
func decodeValue(v string) string {
	if !utf8.ValidString(v) {
		return v
	}
	v = strings.ReplaceAll(v, `\n`, "\n")
	v = strings.ReplaceAll(v, `\N`, "\n")
	v = strings.ReplaceAll(v, `\,`, ",")
	v = strings.ReplaceAll(v, `\\`, `\`)
	return v
}

// windowsToIANA maps Windows timezone display names (as used in Outlook .ics
// exports) to IANA time zone identifiers. Outlook embeds Windows names in
// TZID= parameters; Go's time package only understands IANA names.
var windowsToIANA = map[string]string{
	// Europe
	"W. Europe Standard Time":        "Europe/Berlin",
	"Romance Standard Time":          "Europe/Paris",
	"Central Europe Standard Time":   "Europe/Budapest",
	"Central European Standard Time": "Europe/Warsaw",
	"E. Europe Standard Time":        "Europe/Nicosia",
	"FLE Standard Time":              "Europe/Helsinki",
	"GTB Standard Time":              "Europe/Athens",
	"Turkey Standard Time":           "Europe/Istanbul",
	"Russia Time Zone 3":             "Europe/Samara",
	"Russian Standard Time":          "Europe/Moscow",
	"GMT Standard Time":              "Europe/London",
	"UTC":                            "UTC",
	// Asia
	"Sri Lanka Standard Time":  "Asia/Colombo",
	"India Standard Time":      "Asia/Calcutta",
	"Bangladesh Standard Time": "Asia/Dhaka",
	"SE Asia Standard Time":    "Asia/Bangkok",
	"Singapore Standard Time":  "Asia/Singapore",
	"China Standard Time":      "Asia/Shanghai",
	"Tokyo Standard Time":      "Asia/Tokyo",
	"Korea Standard Time":      "Asia/Seoul",
	"Arabian Standard Time":    "Asia/Dubai",
	"Pakistan Standard Time":   "Asia/Karachi",
	// Americas
	"Eastern Standard Time":    "America/New_York",
	"Central Standard Time":    "America/Chicago",
	"Mountain Standard Time":   "America/Denver",
	"Pacific Standard Time":    "America/Los_Angeles",
	"US Eastern Standard Time": "America/Indianapolis",
	"UTC-02":                   "Etc/GMT+2",
	"UTC-11":                   "Etc/GMT+11",
	"UTC+12":                   "Etc/GMT-12",
	// Australia / Pacific
	"AUS Eastern Standard Time": "Australia/Sydney",
	"AUS Central Standard Time": "Australia/Darwin",
	"New Zealand Standard Time": "Pacific/Auckland",
	// Africa
	"South Africa Standard Time": "Africa/Johannesburg",
	"Egypt Standard Time":        "Africa/Cairo",
	// Middle East
	"Israel Standard Time": "Asia/Jerusalem",
}
