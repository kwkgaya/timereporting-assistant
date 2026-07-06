package holidays

import (
	"testing"
	"time"
)

func date(year int, m time.Month, day int) time.Time {
	return time.Date(year, m, day, 0, 0, 0, 0, time.UTC)
}

func TestNorway2026(t *testing.T) {
	cases := []struct {
		d    time.Time
		want bool
		name string
	}{
		{date(2026, time.January, 1), true, "New Year"},
		{date(2026, time.April, 2), true, "Maundy Thursday"},
		{date(2026, time.April, 3), true, "Good Friday"},
		{date(2026, time.April, 5), true, "Easter Sunday"},
		{date(2026, time.April, 6), true, "Easter Monday"},
		{date(2026, time.May, 1), true, "Labour Day"},
		{date(2026, time.May, 17), true, "Constitution Day"},
		{date(2026, time.May, 14), true, "Ascension Day"},
		{date(2026, time.May, 24), true, "Whit Sunday"},
		{date(2026, time.May, 25), true, "Whit Monday"},
		{date(2026, time.December, 25), true, "Christmas Day"},
		{date(2026, time.December, 26), true, "Boxing Day"},
		{date(2026, time.June, 1), false, "Ordinary Monday"},
		{date(2026, time.April, 4), false, "Holy Saturday (not a holiday in NO)"},
	}
	for _, c := range cases {
		if got := IsHoliday("NO", c.d); got != c.want {
			t.Errorf("NO %s (%s): got %v, want %v", c.d.Format("2006-01-02"), c.name, got, c.want)
		}
	}
}

func TestSweden2026(t *testing.T) {
	cases := []struct {
		d    time.Time
		want bool
		name string
	}{
		{date(2026, time.January, 1), true, "New Year"},
		{date(2026, time.January, 6), true, "Epiphany"},
		{date(2026, time.April, 3), true, "Good Friday"},
		{date(2026, time.April, 5), true, "Easter Sunday"},
		{date(2026, time.April, 6), true, "Easter Monday"},
		{date(2026, time.May, 1), true, "Labour Day"},
		{date(2026, time.May, 14), true, "Ascension Day"},
		{date(2026, time.May, 24), true, "Whit Sunday"},
		{date(2026, time.June, 6), true, "National Day"},
		{date(2026, time.June, 20), true, "Midsummer Day (Sat Jun 20)"},
		{date(2026, time.December, 24), true, "Christmas Eve"},
		{date(2026, time.December, 25), true, "Christmas Day"},
		{date(2026, time.December, 26), true, "Boxing Day"},
		{date(2026, time.June, 1), false, "Ordinary Monday"},
	}
	for _, c := range cases {
		if got := IsHoliday("SE", c.d); got != c.want {
			t.Errorf("SE %s (%s): got %v, want %v", c.d.Format("2006-01-02"), c.name, got, c.want)
		}
	}
}

func TestSriLanka2026(t *testing.T) {
	cases := []struct {
		d    time.Time
		want bool
		name string
	}{
		{date(2026, time.January, 1), true, "New Year"},
		{date(2026, time.February, 4), true, "National Day"},
		{date(2026, time.April, 3), true, "Good Friday"},
		{date(2026, time.April, 13), true, "New Year Eve"},
		{date(2026, time.April, 14), true, "New Year Day"},
		{date(2026, time.May, 1), true, "Labour Day"},
		{date(2026, time.May, 11), true, "Vesak"},
		{date(2026, time.December, 25), true, "Christmas"},
		{date(2026, time.June, 1), false, "Ordinary Monday"},
	}
	for _, c := range cases {
		if got := IsHoliday("LK", c.d); got != c.want {
			t.Errorf("LK %s (%s): got %v, want %v", c.d.Format("2006-01-02"), c.name, got, c.want)
		}
	}
}

func TestUnknownCountry(t *testing.T) {
	if IsHoliday("", date(2026, time.January, 1)) {
		t.Error("empty country should return false")
	}
	if IsHoliday("XX", date(2026, time.January, 1)) {
		t.Error("unknown country should return false")
	}
}
