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
	if !strings.HasPrefix(strings.ToLower(rawURL), "https://") {
		return nil, fmt.Errorf("calendar URL must use HTTPS (got %q)", rawURL)
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

// Parse reads iCalendar data from r and returns meeting events.
func Parse(r io.Reader) ([]model.Meeting, error) {
	lines, err := unfold(r)
	if err != nil {
		return nil, err
	}
	var meetings []model.Meeting
	var inEvent bool
	var dtstart, dtend time.Time
	var summary string
	var declined bool

	reset := func() {
		inEvent = false
		dtstart = time.Time{}
		dtend = time.Time{}
		summary = ""
		declined = false
	}

	for _, line := range lines {
		name, params, value := splitLine(line)
		switch {
		case name == "BEGIN" && value == "VEVENT":
			reset()
			inEvent = true
		case name == "END" && value == "VEVENT" && inEvent:
			if !declined && !dtstart.IsZero() && !dtend.IsZero() && dtend.After(dtstart) {
				meetings = append(meetings, model.Meeting{
					Date:    model.Day(dtstart),
					Start:   dtstart,
					End:     dtend,
					Summary: summary,
				})
			}
			reset()
		case inEvent && name == "SUMMARY":
			summary = decodeValue(value)
		case inEvent && (name == "DTSTART" || strings.HasPrefix(name, "DTSTART;")):
			dtstart = parseDateTime(value, params)
		case inEvent && (name == "DTEND" || strings.HasPrefix(name, "DTEND;")):
			dtend = parseDateTime(value, params)
		case inEvent && name == "ATTENDEE":
			if isDeclined(params, value) {
				declined = true
			}
		}
	}
	return meetings, nil
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
