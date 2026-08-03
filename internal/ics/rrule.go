// Recurrence expansion for VEVENTs. Outlook and Google emit a single VEVENT
// with an RRULE for repeating meetings; without expanding them most working
// days look like they have no calendar events at all.
package ics

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// expandWindow bounds recurrence expansion relative to today. The tool only
// ever reports on recent days, and an unbounded rule would never terminate.
const (
	expandYearsBack    = 2
	expandYearsForward = 1
	maxIterations      = 4000
)

type byDay struct {
	ord int // 0 = every matching weekday, 1..5 = nth, negative = from the end
	wd  time.Weekday
}

type rrule struct {
	freq       string // DAILY, WEEKLY, MONTHLY, YEARLY
	interval   int
	count      int
	until      time.Time
	byDay      []byDay
	byMonthDay []int
	bySetPos   []int
}

var weekdayCodes = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

// parseRRule parses the subset of RFC 5545 recurrence rules that calendar
// clients actually emit for meetings. It reports false for rules it cannot
// expand, so the caller can fall back to the single DTSTART occurrence.
func parseRRule(value string) (rrule, bool) {
	r := rrule{interval: 1}
	for _, part := range strings.Split(value, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "FREQ":
			r.freq = strings.ToUpper(strings.TrimSpace(v))
		case "INTERVAL":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				r.interval = n
			}
		case "COUNT":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				r.count = n
			}
		case "UNTIL":
			r.until = parseDateTime(strings.TrimSpace(v), "")
		case "BYDAY":
			for _, d := range strings.Split(v, ",") {
				if bd, ok := parseByDay(strings.TrimSpace(d)); ok {
					r.byDay = append(r.byDay, bd)
				}
			}
		case "BYMONTHDAY":
			for _, d := range strings.Split(v, ",") {
				if n, err := strconv.Atoi(strings.TrimSpace(d)); err == nil {
					r.byMonthDay = append(r.byMonthDay, n)
				}
			}
		case "BYSETPOS":
			for _, d := range strings.Split(v, ",") {
				if n, err := strconv.Atoi(strings.TrimSpace(d)); err == nil {
					r.bySetPos = append(r.bySetPos, n)
				}
			}
		}
	}
	switch r.freq {
	case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
		return r, true
	}
	return r, false
}

// parseByDay parses a BYDAY entry such as "MO", "2TU" or "-1FR".
func parseByDay(s string) (byDay, bool) {
	if len(s) < 2 {
		return byDay{}, false
	}
	code := s[len(s)-2:]
	wd, ok := weekdayCodes[strings.ToUpper(code)]
	if !ok {
		return byDay{}, false
	}
	bd := byDay{wd: wd}
	if prefix := s[:len(s)-2]; prefix != "" {
		n, err := strconv.Atoi(prefix)
		if err != nil {
			return byDay{}, false
		}
		bd.ord = n
	}
	return bd, true
}

// occurrences returns the start times of the rule's occurrences that fall
// within [from, to]. Occurrences outside the window still count towards COUNT.
func (r rrule) occurrences(dtstart, from, to time.Time) []time.Time {
	if !r.until.IsZero() && r.until.Before(to) {
		to = r.until
	}
	if to.Before(dtstart) {
		return nil
	}

	hh, mm, ss := dtstart.Clock()
	loc := dtstart.Location()
	atTime := func(d time.Time) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), hh, mm, ss, 0, loc)
	}

	var out []time.Time
	emitted := 0
	// emit reports false once the sequence has run past its end.
	emit := func(t time.Time) bool {
		if r.count > 0 && emitted >= r.count {
			return false
		}
		if t.After(to) {
			return false
		}
		emitted++
		if !t.Before(from) && !t.Before(dtstart) {
			out = append(out, t)
		}
		return true
	}

	switch r.freq {
	case "DAILY":
		for i, iter := 0, 0; iter < maxIterations; i, iter = i+r.interval, iter+1 {
			if !emit(atTime(dtstart.AddDate(0, 0, i))) {
				return out
			}
		}
	case "WEEKLY":
		days := r.byDay
		if len(days) == 0 {
			days = []byDay{{wd: dtstart.Weekday()}}
		}
		offsets := make([]int, 0, len(days))
		for _, bd := range days {
			offsets = append(offsets, mondayOffset(bd.wd))
		}
		sort.Ints(offsets)
		weekStart := dtstart.AddDate(0, 0, -mondayOffset(dtstart.Weekday()))
		for w, iter := 0, 0; iter < maxIterations; w, iter = w+r.interval, iter+1 {
			base := weekStart.AddDate(0, 0, w*7)
			for _, off := range offsets {
				if !emit(atTime(base.AddDate(0, 0, off))) {
					return out
				}
			}
		}
	case "MONTHLY":
		first := time.Date(dtstart.Year(), dtstart.Month(), 1, 0, 0, 0, 0, loc)
		for m, iter := 0, 0; iter < maxIterations; m, iter = m+r.interval, iter+1 {
			base := first.AddDate(0, m, 0)
			cands := r.monthlyCandidates(base, dtstart)
			if len(cands) == 0 && base.After(to) {
				return out
			}
			for _, d := range cands {
				if !emit(atTime(d)) {
					return out
				}
			}
		}
	case "YEARLY":
		for y, iter := 0, 0; iter < maxIterations; y, iter = y+r.interval, iter+1 {
			if !emit(atTime(dtstart.AddDate(y, 0, 0))) {
				return out
			}
		}
	}
	return out
}

// monthlyCandidates returns the sorted days in base's month that the rule
// selects, applying BYSETPOS when present.
func (r rrule) monthlyCandidates(base, dtstart time.Time) []time.Time {
	var cands []time.Time
	switch {
	case len(r.byDay) > 0:
		for _, bd := range r.byDay {
			cands = append(cands, weekdaysInMonth(base, bd)...)
		}
	case len(r.byMonthDay) > 0:
		days := daysInMonth(base)
		for _, md := range r.byMonthDay {
			d := md
			if d < 0 {
				d = days + 1 + d
			}
			if d >= 1 && d <= days {
				cands = append(cands, base.AddDate(0, 0, d-1))
			}
		}
	default:
		if dtstart.Day() <= daysInMonth(base) {
			cands = append(cands, base.AddDate(0, 0, dtstart.Day()-1))
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Before(cands[j]) })
	if len(r.bySetPos) == 0 {
		return cands
	}
	var picked []time.Time
	for _, pos := range r.bySetPos {
		i := pos - 1
		if pos < 0 {
			i = len(cands) + pos
		}
		if i >= 0 && i < len(cands) {
			picked = append(picked, cands[i])
		}
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].Before(picked[j]) })
	return picked
}

// weekdaysInMonth returns the days in base's month matching bd, honouring an
// ordinal prefix such as "2TU" (second Tuesday) or "-1FR" (last Friday).
func weekdaysInMonth(base time.Time, bd byDay) []time.Time {
	var all []time.Time
	total := daysInMonth(base)
	for d := 0; d < total; d++ {
		day := base.AddDate(0, 0, d)
		if day.Weekday() == bd.wd {
			all = append(all, day)
		}
	}
	switch {
	case bd.ord == 0:
		return all
	case bd.ord > 0 && bd.ord <= len(all):
		return all[bd.ord-1 : bd.ord]
	case bd.ord < 0 && -bd.ord <= len(all):
		return all[len(all)+bd.ord : len(all)+bd.ord+1]
	}
	return nil
}

func daysInMonth(base time.Time) int {
	return time.Date(base.Year(), base.Month()+1, 0, 0, 0, 0, 0, base.Location()).Day()
}

// mondayOffset returns the number of days from Monday to wd.
func mondayOffset(wd time.Weekday) int {
	return (int(wd) + 6) % 7
}
