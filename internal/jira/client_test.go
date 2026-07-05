package jira

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/mockjira"
	"github.com/kwkgaya/timereporting-assistant/internal/model"
)

func newTestClient(t *testing.T) (*Client, *mockjira.Server, func()) {
	t.Helper()
	srv := mockjira.NewDefault()
	ts := httptest.NewServer(srv.Handler())
	return NewClient(ts.URL, "", ""), srv, ts.Close
}

func TestClientAddAndListWorklog(t *testing.T) {
	c, srv, cleanup := newTestClient(t)
	defer cleanup()

	day := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	start := model.WorklogStart(day)
	wl, err := c.AddWorklog("EDB-100", 90, start, "Worked on the widget "+WorklogMarker)
	if err != nil {
		t.Fatalf("AddWorklog: %v", err)
	}
	if wl.Minutes != 90 {
		t.Errorf("added minutes = %d, want 90", wl.Minutes)
	}
	if srv.WorklogCount("EDB-100") != 1 {
		t.Errorf("server count = %d, want 1", srv.WorklogCount("EDB-100"))
	}

	logs, err := c.ListWorklogs("EDB-100")
	if err != nil {
		t.Fatalf("ListWorklogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("list len = %d, want 1", len(logs))
	}
	got := logs[0]
	if got.IssueKey != "EDB-100" || got.Minutes != 90 || got.Category != model.CategoryExisting {
		t.Errorf("unexpected worklog: %+v", got)
	}
	if got.Started.Hour() != 12 {
		t.Errorf("started hour = %d, want 12 (noon UTC)", got.Started.Hour())
	}
}

func TestClientGetIssue(t *testing.T) {
	c, _, cleanup := newTestClient(t)
	defer cleanup()

	iss, err := c.GetIssue("EDB-9071")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if iss.Key != "EDB-9071" || iss.Summary == "" {
		t.Errorf("issue = %+v", iss)
	}
}

func TestClientExistingWorklogsByDay(t *testing.T) {
	c, _, cleanup := newTestClient(t)
	defer cleanup()

	jun1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	jun2 := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	if _, err := c.AddWorklog("EDB-100", 60, model.WorklogStart(jun1), "day one"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddWorklog("EDB-200", 120, model.WorklogStart(jun2), "day two"); err != nil {
		t.Fatal(err)
	}

	byDay, err := c.ExistingWorklogsByDay("", jun1, time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ExistingWorklogsByDay: %v", err)
	}
	if len(byDay["2026-06-01"]) != 1 || byDay["2026-06-01"][0].Minutes != 60 {
		t.Errorf("2026-06-01 = %+v", byDay["2026-06-01"])
	}
	if len(byDay["2026-06-02"]) != 1 || byDay["2026-06-02"][0].Minutes != 120 {
		t.Errorf("2026-06-02 = %+v", byDay["2026-06-02"])
	}
}

func TestClientAuthorFilter(t *testing.T) {
	c, _, cleanup := newTestClient(t)
	defer cleanup()

	jun1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := c.AddWorklog("EDB-100", 60, model.WorklogStart(jun1), "mine"); err != nil {
		t.Fatal(err)
	}
	// The mock logs as mock.user@example.com; filtering by a different author yields nothing.
	byDay, err := c.ExistingWorklogsByDay("someone.else@example.com", jun1, jun1)
	if err != nil {
		t.Fatal(err)
	}
	if len(byDay) != 0 {
		t.Errorf("expected no worklogs for other author, got %+v", byDay)
	}
}
