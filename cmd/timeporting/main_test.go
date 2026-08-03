package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/kwkgaya/timereporting-assistant/internal/config"
	"github.com/kwkgaya/timereporting-assistant/internal/model"
)

func TestCalendarWarning(t *testing.T) {
	configured := config.Config{ICSUrl: "https://example.com/cal.ics"}

	tests := []struct {
		name     string
		cfg      config.Config
		meetings []model.Meeting
		err      error
		want     string // substring; "" means no warning
	}{
		{
			name: "not configured",
			cfg:  config.Config{},
			want: "No calendar configured",
		},
		{
			name: "fetch failed",
			cfg:  configured,
			err:  errors.New("calendar URL returned 404"),
			want: "Could not load your calendar: calendar URL returned 404",
		},
		{
			name: "loaded but empty",
			cfg:  configured,
			want: "contained no events",
		},
		{
			name:     "healthy",
			cfg:      configured,
			meetings: []model.Meeting{{Summary: "Standup"}},
			want:     "",
		},
		{
			name:     "local file with events",
			cfg:      config.Config{ICSPath: "data/calendar.ics"},
			meetings: []model.Meeting{{Summary: "Standup"}},
			want:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := calendarWarning(tc.cfg, tc.meetings, tc.err)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("expected no warning, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("expected warning containing %q, got %q", tc.want, got)
			}
		})
	}
}
