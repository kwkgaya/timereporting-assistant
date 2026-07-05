package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	srv := New(plans, client, nil, "mock", 8080)
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

func TestSubmitDayDryRun(t *testing.T) {
	srv, _ := makeTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{"dryRun": true})
	resp, err := http.Post(ts.URL+"/api/days/2026-06-01/submit", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dry-run status = %d", resp.StatusCode)
	}
	var res map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&res)
	if res["dryRun"] != true {
		t.Errorf("dryRun not true in response: %+v", res)
	}
	// Should NOT be marked submitted after a dry run.
	if srv.days[0].Submitted {
		t.Error("day should not be submitted after dry run")
	}
}

func TestSubmitDayWritesToMock(t *testing.T) {
	srv, mockTS := makeTestServer(t)
	webTS := httptest.NewServer(srv.Handler())
	defer webTS.Close()
	_ = mockTS

	body, _ := json.Marshal(map[string]any{"dryRun": false})
	resp, err := http.Post(webTS.URL+"/api/days/2026-06-01/submit", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status = %d: %s", resp.StatusCode, readBody(resp))
	}
	if !srv.days[0].Submitted {
		t.Error("day should be marked submitted")
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

func readBody(r *http.Response) string {
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r.Body)
	return buf.String()
}

func TestTargetSwitchRejectsRealWithoutClient(t *testing.T) {
	srv, _ := makeTestServer(t) // real client is nil
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Switching to real must fail when no real client is configured.
	body, _ := json.Marshal(map[string]any{"target": "real"})
	resp, err := http.NewRequest(http.MethodPut, ts.URL+"/api/target", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, resp)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("switch to real without client: status = %d, want 400", rec.Code)
	}
	if srv.activeWrite != "mock" {
		t.Errorf("activeWrite = %q, want mock", srv.activeWrite)
	}
}

func TestTargetSwitchToRealWithClient(t *testing.T) {
	// Build a server with a (fake) real client pointing at a second mock.
	realMock := mockjira.NewDefault()
	realTS := httptest.NewServer(realMock.Handler())
	defer realTS.Close()
	realClient := jira.NewClient(realTS.URL, "", "")

	mockMock := mockjira.NewDefault()
	mockTS := httptest.NewServer(mockMock.Handler())
	defer mockTS.Close()
	mockClient := jira.NewClient(mockTS.URL, "", "")

	jun1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	plans := []model.DayPlan{{
		Date:   jun1,
		Status: model.StatusWorking,
		Suggested: []model.Worklog{
			{IssueKey: "EDB-100", Minutes: 420, Comment: "Work", Category: model.CategoryActivity, Started: model.WorklogStart(jun1)},
		},
	}}
	srv := New(plans, mockClient, realClient, "mock-write", 8080)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Switch to real.
	body, _ := json.Marshal(map[string]any{"target": "real"})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/target", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("switch to real: status = %d: %s", rec.Code, rec.Body.String())
	}

	// Submit → should hit the REAL mock, not the mock-write mock.
	sbody, _ := json.Marshal(map[string]any{"dryRun": false})
	resp, err := http.Post(ts.URL+"/api/days/2026-06-01/submit", "application/json", bytes.NewReader(sbody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status = %d: %s", resp.StatusCode, readBody(resp))
	}
	if realMock.WorklogCount("EDB-100") != 1 {
		t.Errorf("real target worklog count = %d, want 1", realMock.WorklogCount("EDB-100"))
	}
	if mockMock.WorklogCount("EDB-100") != 0 {
		t.Errorf("mock target should be untouched, got %d", mockMock.WorklogCount("EDB-100"))
	}
}

