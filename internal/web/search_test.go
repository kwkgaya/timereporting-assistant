package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kwkgaya/timereporting-assistant/internal/jira"
)

// Real Jira's `text ~` clause searches summary/description/comments and never
// matches an issue key, so a key typed into the picker must be resolved by a
// direct issue lookup instead.
func TestSearchIssuesResolvesIssueKey(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/EDB-11549"):
			_, _ = w.Write([]byte(`{"key":"EDB-11549","fields":{"summary":"Improve search"}}`))
		case r.URL.Path == "/rest/api/3/search/jql":
			_, _ = w.Write([]byte(`{"issues":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()

	srv, _ := makeTestServer(t)
	srv.mu.Lock()
	srv.mockClient = jira.NewClient(fake.URL, "", "")
	srv.mu.Unlock()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/issues/search?q=EDB-11549")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0]["key"] != "EDB-11549" {
		t.Fatalf("expected the issue to be found by key, got %v", out)
	}
}

// A lowercase key still resolves, and a text search failure must not hide a
// successful key lookup.
func TestSearchIssuesKeyWinsWhenTextSearchFails(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/EDB-11549") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"key":"EDB-11549","fields":{"summary":"Improve search"}}`))
			return
		}
		http.Error(w, `{"errorMessages":["bad jql"]}`, http.StatusBadRequest)
	}))
	defer fake.Close()

	srv, _ := makeTestServer(t)
	srv.mu.Lock()
	srv.mockClient = jira.NewClient(fake.URL, "", "")
	srv.mu.Unlock()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/issues/search?q=edb-11549")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out []map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0]["key"] != "EDB-11549" {
		t.Fatalf("expected the key hit to survive a failed text search, got %v", out)
	}
}
