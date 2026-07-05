// Package mockjira implements a tiny in-memory Jira-compatible server used to
// test the worklog write flow safely, without touching a real Jira instance.
// It implements just enough of the REST v3 surface the client needs.
package mockjira

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sync"

	"github.com/kwkgaya/timereporting-assistant/internal/adf"
)

// Author identifies who logged work.
type Author struct {
	EmailAddress string `json:"emailAddress"`
	DisplayName  string `json:"displayName"`
}

// Worklog is a single time entry as exchanged over the wire.
type Worklog struct {
	ID               string          `json:"id"`
	Author           Author          `json:"author"`
	TimeSpentSeconds int             `json:"timeSpentSeconds"`
	Started          string          `json:"started"`
	Comment          json.RawMessage `json:"comment,omitempty"`
}

type issue struct {
	Key      string
	Summary  string
	Worklogs []Worklog
}

// Server is an in-memory mock Jira.
type Server struct {
	mu         sync.Mutex
	issues     map[string]*issue
	order      []string
	nextID     int
	authorMail string
	authorName string
}

// New creates an empty mock server.
func New() *Server {
	return &Server{
		issues:     map[string]*issue{},
		nextID:     1,
		authorMail: "mock.user@example.com",
		authorName: "Mock User",
	}
}

// NewDefault creates a mock server seeded with the meeting task, the leave task,
// and a few sample development issues.
func NewDefault() *Server {
	s := New()
	s.Seed("EDB-9070", "Absence / Leave / Public holiday")
	s.Seed("EDB-9071", "Internal meetings")
	s.Seed("EDB-100", "Implement inventory widget")
	s.Seed("EDB-200", "Fix login redirect bug")
	s.Seed("EDB-300", "Refactor worklog API")
	return s
}

// Seed adds (or replaces) an issue with the given key and summary.
func (s *Server) Seed(key, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.issues[key]; !ok {
		s.order = append(s.order, key)
	}
	s.issues[key] = &issue{Key: key, Summary: summary}
}

// WorklogCount returns the number of worklogs recorded against key.
func (s *Server) WorklogCount(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if iss, ok := s.issues[key]; ok {
		return len(iss.Worklogs)
	}
	return 0
}

// Handler returns the HTTP handler implementing the mock API and inspect page.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/issue/{key}/worklog", s.handleListWorklog)
	mux.HandleFunc("POST /rest/api/3/issue/{key}/worklog", s.handleAddWorklog)
	mux.HandleFunc("GET /rest/api/3/issue/{key}", s.handleGetIssue)
	mux.HandleFunc("GET /rest/api/3/search", s.handleSearch)
	mux.HandleFunc("GET /", s.handleIndex)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"errorMessages": []string{msg}, "errors": map[string]any{}})
}

func (s *Server) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	s.mu.Lock()
	iss, ok := s.issues[key]
	summary := ""
	if ok {
		summary = iss.Summary
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "Issue does not exist: "+key)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key":    key,
		"fields": map[string]any{"summary": summary},
	})
}

func (s *Server) handleListWorklog(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	s.mu.Lock()
	iss, ok := s.issues[key]
	var logs []Worklog
	if ok {
		logs = append(logs, iss.Worklogs...)
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "Issue does not exist: "+key)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"startAt":    0,
		"maxResults": len(logs),
		"total":      len(logs),
		"worklogs":   logs,
	})
}

func (s *Server) handleAddWorklog(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var body struct {
		TimeSpentSeconds int             `json:"timeSpentSeconds"`
		Started          string          `json:"started"`
		Comment          json.RawMessage `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if body.TimeSpentSeconds <= 0 {
		writeErr(w, http.StatusBadRequest, "timeSpentSeconds must be > 0")
		return
	}
	s.mu.Lock()
	iss, ok := s.issues[key]
	if !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "Issue does not exist: "+key)
		return
	}
	wl := Worklog{
		ID:               fmt.Sprintf("%d", s.nextID),
		Author:           Author{EmailAddress: s.authorMail, DisplayName: s.authorName},
		TimeSpentSeconds: body.TimeSpentSeconds,
		Started:          body.Started,
		Comment:          body.Comment,
	}
	s.nextID++
	iss.Worklogs = append(iss.Worklogs, wl)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, wl)
}

// handleSearch ignores the JQL and returns all seeded issues. That is enough
// for the client's "find issues that may have my worklogs" step; it then reads
// each issue's worklogs and filters locally.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	issues := make([]map[string]any, 0, len(s.order))
	for _, k := range s.order {
		iss := s.issues[k]
		issues = append(issues, map[string]any{
			"key":    iss.Key,
			"fields": map[string]any{"summary": iss.Summary},
		})
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"startAt":    0,
		"maxResults": len(issues),
		"total":      len(issues),
		"issues":     issues,
	})
}

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Mock Jira</title>
<style>
body{font-family:system-ui,Arial,sans-serif;margin:2rem;color:#172b4d}
h1{font-size:1.3rem}
table{border-collapse:collapse;width:100%;margin-top:1rem}
th,td{border:1px solid #dfe1e6;padding:6px 10px;text-align:left;font-size:14px}
th{background:#f4f5f7}
.key{font-weight:600}
.empty{color:#6b778c}
</style></head><body>
<h1>Mock Jira &mdash; worklogs</h1>
{{range .}}
<h2 class="key">{{.Key}} <span class="empty">{{.Summary}}</span></h2>
{{if .Worklogs}}
<table><tr><th>Started</th><th>Hours</th><th>Author</th><th>Comment</th></tr>
{{range .Worklogs}}<tr><td>{{.Started}}</td><td>{{.Hours}}</td><td>{{.Author}}</td><td>{{.Comment}}</td></tr>{{end}}
</table>
{{else}}<p class="empty">No worklogs.</p>{{end}}
{{end}}
</body></html>`))

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	type wlView struct {
		Started string
		Hours   string
		Author  string
		Comment string
	}
	type issueView struct {
		Key      string
		Summary  string
		Worklogs []wlView
	}
	s.mu.Lock()
	views := make([]issueView, 0, len(s.order))
	for _, k := range s.order {
		iss := s.issues[k]
		iv := issueView{Key: iss.Key, Summary: iss.Summary}
		for _, wl := range iss.Worklogs {
			iv.Worklogs = append(iv.Worklogs, wlView{
				Started: wl.Started,
				Hours:   fmt.Sprintf("%.1f", float64(wl.TimeSpentSeconds)/3600),
				Author:  wl.Author.EmailAddress,
				Comment: adf.Text(wl.Comment),
			})
		}
		views = append(views, iv)
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTmpl.Execute(w, views)
}
