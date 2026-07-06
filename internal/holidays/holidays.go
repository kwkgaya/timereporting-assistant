// Package holidays provides public holiday detection for supported countries.
// Country codes follow ISO 3166-1 alpha-2 (e.g. "NO", "SE", "LK").
// An empty string means no country is configured; IsHoliday always returns false.
package holidays

import (
	"strings"
	"time"
)

// IsHoliday reports whether date is a public holiday in country.
// The date is compared in UTC; the time component is ignored.
// Supported countries: "NO" (Norway), "SE" (Sweden), "LK" (Sri Lanka).
// Returns false for unknown or empty country codes.
func IsHoliday(country string, date time.Time) bool {
	d := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	switch strings.ToUpper(country) {
	case "NO":
		return isNO(d)
	case "SE":
		return isSE(d)
	case "LK":
		return isLK(d)
	}
	return false
}

// ── Easter (Meeus/Jones/Butcher algorithm) ────────────────────────────────────

// easter returns Easter Sunday midnight UTC for the given year.
func easter(year int) time.Time {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := (h+l-7*m+114)%31 + 1
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

func addDays(t time.Time, n int) time.Time { return t.AddDate(0, 0, n) }
func isDate(t time.Time, month time.Month, day int) bool {
	return t.Month() == month && t.Day() == day
}

// ── Norway (NO) ───────────────────────────────────────────────────────────────

func isNO(d time.Time) bool {
	e := easter(d.Year())
	return isDate(d, time.January, 1) || // New Year
		d.Equal(addDays(e, -3)) || // Maundy Thursday
		d.Equal(addDays(e, -2)) || // Good Friday
		d.Equal(e) || // Easter Sunday
		d.Equal(addDays(e, 1)) || // Easter Monday
		isDate(d, time.May, 1) || // Labour Day
		isDate(d, time.May, 17) || // Constitution Day
		d.Equal(addDays(e, 39)) || // Ascension Day
		d.Equal(addDays(e, 49)) || // Whit Sunday
		d.Equal(addDays(e, 50)) || // Whit Monday
		isDate(d, time.December, 25) || // Christmas Day
		isDate(d, time.December, 26) // Boxing Day
}

// ── Sweden (SE) ───────────────────────────────────────────────────────────────

func isSE(d time.Time) bool {
	e := easter(d.Year())
	return isDate(d, time.January, 1) || // New Year
		isDate(d, time.January, 6) || // Epiphany
		d.Equal(addDays(e, -2)) || // Good Friday
		d.Equal(e) || // Easter Sunday
		d.Equal(addDays(e, 1)) || // Easter Monday
		isDate(d, time.May, 1) || // Labour Day
		d.Equal(addDays(e, 39)) || // Ascension Day
		d.Equal(addDays(e, 49)) || // Whit Sunday
		isDate(d, time.June, 6) || // National Day
		isSEMidsummer(d) || // Midsummer Day (Sat Jun 20–26)
		isSEAllSaints(d) || // All Saints' Day (Sat Oct 31–Nov 6)
		isDate(d, time.December, 24) || // Christmas Eve
		isDate(d, time.December, 25) || // Christmas Day
		isDate(d, time.December, 26) || // Boxing Day
		isDate(d, time.December, 31) // New Year's Eve
}

// isSEMidsummer returns true if d is the Saturday between June 20 and June 26.
func isSEMidsummer(d time.Time) bool {
	if d.Month() != time.June || d.Weekday() != time.Saturday {
		return false
	}
	return d.Day() >= 20 && d.Day() <= 26
}

// isSEAllSaints returns true if d is the Saturday between Oct 31 and Nov 6.
func isSEAllSaints(d time.Time) bool {
	if d.Weekday() != time.Saturday {
		return false
	}
	if d.Month() == time.October && d.Day() == 31 {
		return true
	}
	return d.Month() == time.November && d.Day() >= 1 && d.Day() <= 6
}

// ── Sri Lanka (LK) ────────────────────────────────────────────────────────────
// Sri Lanka observes many lunar-based holidays (Poya days, Sinhala/Tamil New Year,
// Islamic holidays). The dates shift each year. We provide static lists for the
// years most likely to be in the app's backfill range.
// Users should also configure an ICS calendar for accurate coverage.

func isLK(d time.Time) bool {
	y, m, day := d.Year(), d.Month(), d.Day()
	if isDate(d, time.January, 1) || // New Year
		isDate(d, time.February, 4) || // National Day
		isDate(d, time.May, 1) || // Labour Day
		isDate(d, time.April, 13) || // Sinhala & Tamil New Year's Eve
		isDate(d, time.April, 14) { // Sinhala & Tamil New Year's Day
		return true
	}
	// Good Friday (Easter-based)
	e := easter(y)
	if d.Equal(addDays(e, -2)) {
		return true
	}
	// Static per-year lists for movable holidays.
	type md struct{ m time.Month; d int }
	static := map[int][]md{
		2025: {
			{time.January, 14},  // Tamil Thai Pongal
			{time.February, 12}, // Navam Full Moon Poya
			{time.March, 14},    // Medin Full Moon Poya
			{time.April, 13},    // Bak Full Moon Poya
			{time.May, 12},      // Vesak Full Moon Poya
			{time.May, 13},      // Day following Vesak
			{time.June, 11},     // Poson Full Moon Poya
			{time.July, 10},     // Esala Full Moon Poya
			{time.August, 9},    // Nikini Full Moon Poya
			{time.September, 7}, // Binara Full Moon Poya
			{time.September, 6}, // Milad Un Nabi (approx.)
			{time.October, 6},   // Vap Full Moon Poya
			{time.October, 20},  // Deepavali
			{time.November, 5},  // Il Full Moon Poya
			{time.December, 4},  // Unduvap Full Moon Poya
			{time.December, 25}, // Christmas
		},
		2026: {
			{time.January, 14},  // Tamil Thai Pongal
			{time.February, 11}, // Navam Full Moon Poya
			{time.March, 13},    // Medin Full Moon Poya
			{time.April, 12},    // Bak Full Moon Poya
			{time.May, 11},      // Vesak Full Moon Poya
			{time.May, 12},      // Day following Vesak
			{time.June, 10},     // Poson Full Moon Poya
			{time.July, 9},      // Esala Full Moon Poya
			{time.August, 8},    // Nikini Full Moon Poya
			{time.September, 6}, // Binara Full Moon Poya
			{time.September, 14},// Milad Un Nabi (approx.)
			{time.October, 5},   // Vap Full Moon Poya
			{time.October, 20},  // Deepavali
			{time.November, 4},  // Il Full Moon Poya
			{time.December, 3},  // Unduvap Full Moon Poya
			{time.December, 25}, // Christmas
		},
	}
	for _, h := range static[y] {
		if m == h.m && day == h.d {
			return true
		}
	}
	return false
}
