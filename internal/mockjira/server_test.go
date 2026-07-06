package mockjira

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kwkgaya/timereporting-assistant/internal/adf"
)

func TestSearchFiltersByTerm(t *testing.T) {
	srv := NewDefault()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	search := func(jql string) []string {
		body, _ := json.Marshal(map[string]any{"jql": jql})
		resp, err := http.Post(ts.URL+"/rest/api/3/search/jql", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Issues []struct {
				Key string `json:"key"`
			} `json:"issues"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		keys := make([]string, 0, len(out.Issues))
		for _, i := range out.Issues {
			keys = append(keys, i.Key)
		}
		return keys
	}

	// A ~ term filters by summary substring.
	if got := search(`text ~ "login*"`); len(got) != 1 || got[0] != "EDB-200" {
		t.Errorf("search login = %v, want [EDB-200]", got)
	}
	// Matching by key substring.
	if got := search(`text ~ "EDB-100*"`); len(got) != 1 || got[0] != "EDB-100" {
		t.Errorf("search EDB-100 = %v, want [EDB-100]", got)
	}
	// A JQL with no ~ clause (the worklogAuthor query) returns all issues.
	if got := search(`worklogAuthor = currentUser()`); len(got) != 5 {
		t.Errorf("worklogAuthor query returned %d issues, want 5 (all)", len(got))
	}
}

func TestAddAndListWorklog(t *testing.T) {
	srv := NewDefault()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	comment, _ := json.Marshal(adf.Doc("Worked on EDB-100"))
	body, _ := json.Marshal(map[string]any{
		"timeSpentSeconds": 3600,
		"started":          "2026-06-01T12:00:00.000+0000",
		"comment":          json.RawMessage(comment),
	})
	resp, err := http.Post(ts.URL+"/rest/api/3/issue/EDB-100/worklog", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add worklog status = %d, want 201", resp.StatusCode)
	}
	var created Worklog
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == "" || created.TimeSpentSeconds != 3600 {
		t.Fatalf("unexpected created worklog: %+v", created)
	}

	if srv.WorklogCount("EDB-100") != 1 {
		t.Fatalf("worklog count = %d, want 1", srv.WorklogCount("EDB-100"))
	}

	// List and verify.
	lr, err := http.Get(ts.URL + "/rest/api/3/issue/EDB-100/worklog")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Total    int       `json:"total"`
		Worklogs []Worklog `json:"worklogs"`
	}
	_ = json.NewDecoder(lr.Body).Decode(&list)
	lr.Body.Close()
	if list.Total != 1 || len(list.Worklogs) != 1 {
		t.Fatalf("list total = %d, len = %d, want 1/1", list.Total, len(list.Worklogs))
	}
	if got := adf.Text(list.Worklogs[0].Comment); got != "Worked on EDB-100" {
		t.Errorf("comment = %q, want %q", got, "Worked on EDB-100")
	}
}

func TestAddWorklogRejectsZeroSeconds(t *testing.T) {
	srv := NewDefault()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{"timeSpentSeconds": 0, "started": "x"})
	resp, err := http.Post(ts.URL+"/rest/api/3/issue/EDB-100/worklog", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAddWorklogUnknownIssue(t *testing.T) {
	srv := NewDefault()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{"timeSpentSeconds": 60, "started": "x"})
	resp, err := http.Post(ts.URL+"/rest/api/3/issue/NOPE-1/worklog", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestGetIssueAndSearch(t *testing.T) {
	srv := NewDefault()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ir, err := http.Get(ts.URL + "/rest/api/3/issue/EDB-9071")
	if err != nil {
		t.Fatal(err)
	}
	var iss struct {
		Key    string `json:"key"`
		Fields struct {
			Summary string `json:"summary"`
		} `json:"fields"`
	}
	_ = json.NewDecoder(ir.Body).Decode(&iss)
	ir.Body.Close()
	if iss.Key != "EDB-9071" || iss.Fields.Summary == "" {
		t.Errorf("get issue = %+v", iss)
	}

	sr, err := http.Get(ts.URL + "/rest/api/3/search?jql=worklogAuthor=currentUser()")
	if err != nil {
		t.Fatal(err)
	}
	var search struct {
		Total  int `json:"total"`
		Issues []struct {
			Key string `json:"key"`
		} `json:"issues"`
	}
	_ = json.NewDecoder(sr.Body).Decode(&search)
	sr.Body.Close()
	if search.Total < 5 {
		t.Errorf("search total = %d, want >= 5 seeded issues", search.Total)
	}
}

func TestIndexPageRenders(t *testing.T) {
	srv := NewDefault()
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
}
