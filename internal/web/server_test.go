package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/config"
	"github.com/kwkgaya/timereporting-assistant/internal/jira"
	"github.com/kwkgaya/timereporting-assistant/internal/mockjira"
	"github.com/kwkgaya/timereporting-assistant/internal/model"
)

func makeTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	mock := mockjira.NewDefault()
	ts := httptest.NewServer(mock.Handler())
	t.Cleanup(ts.Close)
	client := jira.NewClient(ts.URL, "", "")

	jun1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	jun2 := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	plans := []model.DayPlan{
		{
			Date:   jun1,
			Status: model.StatusWorking,
			Suggested: []model.Worklog{
				{IssueKey: "EDB-100", Minutes: 210, Comment: "Work", Category: model.CategoryActivity, Started: model.WorklogStart(jun1)},
				{IssueKey: "EDB-9071", Minutes: 210, Comment: "Meetings", Category: model.CategoryMeeting, Started: model.WorklogStart(jun1)},
			},
		},
		{
			Date:   jun2,
			Status: model.StatusWorking,
			Suggested: []model.Worklog{
				{IssueKey: "EDB-200", Minutes: 420, Comment: "Bug fix", Category: model.CategoryActivity, Started: model.WorklogStart(jun2)},
			},
		},
	}
	srv := New(plans, client, nil, "mock", 9080)
	return srv, ts
}

func TestGetDays(t *testing.T) {
	srv, _ := makeTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/days")
	if err != nil {
		t.Fatal(err)
	}
	var days []DayView
	if err := json.NewDecoder(resp.Body).Decode(&days); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(days) != 2 {
		t.Fatalf("days = %d, want 2", len(days))
	}
	if days[0].Date != "2026-06-01" {
		t.Errorf("first day = %q", days[0].Date)
	}
}

func TestPutDayUpdatesSuggested(t *testing.T) {
	srv, _ := makeTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"suggested": []map[string]any{
			{"issueKey": "EDB-300", "minutes": 420, "comment": "manual", "category": "manual"},
		},
	})
	resp, err := http.NewRequest(http.MethodPut, ts.URL+"/api/days/2026-06-01", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Header = http.Header{"Content-Type": []string{"application/json"}}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, resp)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", rec.Code, rec.Body.String())
	}
	var updated DayView
	_ = json.NewDecoder(rec.Body).Decode(&updated)
	if len(updated.Suggested) != 1 || updated.Suggested[0].IssueKey != "EDB-300" {
		t.Errorf("suggested = %+v", updated.Suggested)
	}
}

func TestClonePrevious(t *testing.T) {
	srv, _ := makeTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/days/2026-06-02/clone-previous", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clone status = %d", resp.StatusCode)
	}
	var updated DayView
	_ = json.NewDecoder(resp.Body).Decode(&updated)
	// Should now have same suggested as June 1.
	if len(updated.Suggested) != 2 {
		t.Errorf("cloned suggested len = %d, want 2", len(updated.Suggested))
	}
}

func TestRebuildDay(t *testing.T) {
	srv, _ := makeTestServer(t)
	called := 0
	srv.WithDayBuilder(func(cfg config.Config, day time.Time) (model.DayPlan, error) {
		called++
		return model.DayPlan{
			Date:      day,
			Status:    model.StatusWorking,
			Suggested: []model.Worklog{{IssueKey: "EDB-999", Minutes: 420, Category: model.CategoryActivity}},
		}, nil
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/days/2026-06-01/rebuild", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rebuild status = %d", resp.StatusCode)
	}
	var out struct {
		Day DayView `json:"day"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if called != 1 {
		t.Errorf("builder called %d times, want 1", called)
	}
	if len(out.Day.Suggested) != 1 || out.Day.Suggested[0].IssueKey != "EDB-999" {
		t.Errorf("rebuilt suggested = %+v", out.Day.Suggested)
	}
}

func TestRebuildDayInvalidDate(t *testing.T) {
	srv, _ := makeTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/days/not-a-date/rebuild", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("rebuild status = %d, want 400", resp.StatusCode)
	}
}

func TestEditExistingMovesWorklogToSuggested(t *testing.T) {
	mock := mockjira.NewDefault()
	mockSrv := httptest.NewServer(mock.Handler())
	defer mockSrv.Close()
	client := jira.NewClient(mockSrv.URL, "", "")

	jun1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	plans := []model.DayPlan{{
		Date:   jun1,
		Status: model.StatusWorking,
		Suggested: []model.Worklog{
			{IssueKey: "EDB-100", Minutes: 210, Comment: "Work", Category: model.CategoryActivity, Started: model.WorklogStart(jun1)},
		},
	}}
	// realClient must be set: submits go to the "real" write target by default.
	srv := New(plans, client, client, "mock", 9080)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Submit the row so there is a real worklog to edit.
	resp, err := http.Post(ts.URL+"/api/days/2026-06-01/rows/0/submit", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var subm struct {
		Day DayView `json:"day"`
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	_ = json.Unmarshal(raw, &subm)
	if len(subm.Day.Existing) != 1 {
		t.Fatalf("existing after submit = %d, want 1 (status %d, body %s)", len(subm.Day.Existing), resp.StatusCode, raw)
	}
	id := subm.Day.Existing[0].ID
	key := subm.Day.Existing[0].IssueKey

	resp, err = http.Post(ts.URL+"/api/days/2026-06-01/existing/"+id+"/edit", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit status = %d", resp.StatusCode)
	}
	var updated DayView
	_ = json.NewDecoder(resp.Body).Decode(&updated)

	for _, wl := range updated.Existing {
		if wl.ID == id {
			t.Fatal("worklog should have been deleted from Jira and removed from Existing")
		}
	}
	if len(updated.Suggested) == 0 || !updated.Suggested[0].Reedit {
		t.Fatalf("first suggested row = %+v, want a reedit row", updated.Suggested)
	}
	if updated.Suggested[0].IssueKey != key {
		t.Errorf("reedit issue key = %q, want %q", updated.Suggested[0].IssueKey, key)
	}
	if updated.Suggested[0].Submitted {
		t.Error("reedit row must not be marked submitted")
	}
}

func TestIndexRendersHTML(t *testing.T) {
	srv, _ := makeTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index status = %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !bytes.Contains([]byte(ct), []byte("text/html")) {
		t.Errorf("content-type = %q, want text/html", ct)
	}
}

func TestIndexInjectsConfiguredTarget(t *testing.T) {
	srv, _ := makeTestServer(t)
	srv.WithConfig(config.Config{
		WorkdayHours: 8,
		Jira:         config.JiraConfig{BaseURL: "https://example.atlassian.net"},
		JiraAPIToken: "token",
	}, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readBody(resp)

	// A leftover token is a JavaScript syntax error that blanks the whole page.
	if strings.Contains(body, targetMinsToken) {
		t.Error("target placeholder was not substituted")
	}
	if !strings.Contains(body, "const TARGET_MINS = 480;") {
		t.Error("8h workday was not injected as 480 minutes")
	}
}

func readBody(r *http.Response) string {
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r.Body)
	return buf.String()
}
