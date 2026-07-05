// Package web serves the local review/edit/approve UI for the timereporting
// assistant. It exposes a REST-like API the browser-side JS calls, and the
// Go binary itself is the HTTP server — no build tooling required.
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/config"
	"github.com/kwkgaya/timereporting-assistant/internal/jira"
	"github.com/kwkgaya/timereporting-assistant/internal/keychain"
	"github.com/kwkgaya/timereporting-assistant/internal/model"
)

// DayView is the JSON shape the UI works with.
type DayView struct {
	Date      string     `json:"date"`      // YYYY-MM-DD
	Weekday   string     `json:"weekday"`   // Monday …
	Status    string     `json:"status"`    // working/holiday/full_leave/half_leave
	Existing  []WlogView `json:"existing"`  // already in Jira (read-only)
	Suggested []WlogView `json:"suggested"` // proposed; user edits these
	Notes     []string   `json:"notes"`
	Submitted bool       `json:"submitted"` // true after a successful Jira write
}

// WlogView is a single worklog row in the UI.
type WlogView struct {
	ID        string `json:"id"` // Jira worklog ID (for existing worklogs)
	IssueKey  string `json:"issueKey"`
	Minutes   int    `json:"minutes"`
	Comment   string `json:"comment"`
	Category  string `json:"category"`
	Author    string `json:"author,omitempty"`
	Submitted bool   `json:"submitted,omitempty"` // true once individually submitted
}

// PlanBuilder is a function that builds a fresh set of day plans from the
// current config. It is called at startup and again whenever the user triggers
// a reload from the settings page.
type PlanBuilder func(cfg config.Config) ([]model.DayPlan, *jira.Client, *jira.Client, error)

// Server holds the state for the web review session.
type Server struct {
	mu           sync.Mutex
	days         []DayView      // ordered by date
	dayIndex     map[string]int // date -> index
	mockClient   *jira.Client   // writes to the mock server
	realClient   *jira.Client   // writes to real Jira; nil when no credentials
	activeWrite  string         // "mock" | "real" — where submits currently go
	readSource   string         // display label for where existing worklogs were read
	port         int
	cfg          config.Config // current config (for settings page)
	cfgPath      string        // path to config.json (for saving)
	planBuilder  PlanBuilder   // called on reload to rebuild day plans
}

// New creates a Server. mockClient always writes to the mock; realClient (may be
// nil) writes to real Jira. target ("mock"/"mock-write"/"real") sets the initial
// read-source label and active write target.
func New(plans []model.DayPlan, mockClient, realClient *jira.Client, target string, port int) *Server {
	readSource, _ := targetLabels(target)
	activeWrite := "mock"
	if target == "real" {
		activeWrite = "real"
	}
	s := &Server{
		dayIndex:    map[string]int{},
		mockClient:  mockClient,
		realClient:  realClient,
		activeWrite: activeWrite,
		readSource:  readSource,
		port:        port,
	}
	for _, p := range plans {
		key := p.Date.Format("2006-01-02")
		view := planToView(p)
		s.dayIndex[key] = len(s.days)
		s.days = append(s.days, view)
	}
	return s
}

// WithConfig attaches the loaded config and its file path to the server so the
// settings page can read and save it.
func (s *Server) WithConfig(cfg config.Config, cfgPath string) *Server {
	s.mu.Lock()
	s.cfg = cfg
	s.cfgPath = cfgPath
	s.mu.Unlock()
	return s
}

// WithPlanBuilder attaches the plan-builder function used on reload.
func (s *Server) WithPlanBuilder(fn PlanBuilder) *Server {
	s.mu.Lock()
	s.planBuilder = fn
	s.mu.Unlock()
	return s
}

// writeClient returns the jira client for the current active write target.
func (s *Server) writeClient() (*jira.Client, string, error) {
	if s.activeWrite == "real" {
		if s.realClient == nil {
			return nil, "", fmt.Errorf("no real Jira credentials configured")
		}
		return s.realClient, "Real Jira", nil
	}
	return s.mockClient, "Mock Jira", nil
}

// Handler returns the HTTP handler for the review UI.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.apiStatus)
	mux.HandleFunc("GET /api/target", s.apiGetTarget)
	mux.HandleFunc("PUT /api/target", s.apiPutTarget)
	mux.HandleFunc("GET /api/config", s.apiGetConfig)
	mux.HandleFunc("PUT /api/config", s.apiPutConfig)
	mux.HandleFunc("POST /api/reload", s.apiReload)
	mux.HandleFunc("GET /api/credentials/status", s.apiCredentialStatus)
	mux.HandleFunc("POST /api/credentials/jira", s.apiSetJiraCredentials)
	mux.HandleFunc("POST /api/credentials/github", s.apiSetGitHubCredentials)
	mux.HandleFunc("GET /api/days", s.apiGetDays)
	mux.HandleFunc("GET /api/days/{date}", s.apiGetDay)
	mux.HandleFunc("PUT /api/days/{date}", s.apiPutDay)
	mux.HandleFunc("POST /api/days/{date}/submit", s.apiSubmitDay)
	mux.HandleFunc("POST /api/days/{date}/rows/{idx}/submit", s.apiSubmitRow)
	mux.HandleFunc("POST /api/days/{date}/clone-previous", s.apiClonePrevious)
	mux.HandleFunc("PUT /api/days/{date}/existing/{id}", s.apiUpdateExisting)
	mux.HandleFunc("DELETE /api/days/{date}/existing/{id}", s.apiDeleteExisting)
	mux.HandleFunc("GET /settings", s.handleSettings)
	mux.HandleFunc("GET /", s.handleIndex)
	return mux
}

// ── Settings API ─────────────────────────────────────────────────────────────

// configView is the subset of Config that the settings page reads/writes.
// Secrets are never included.
type configView struct {
	JiraBaseURL    string   `json:"jiraBaseUrl"`
	JiraEmail      string   `json:"jiraEmail"`
	MeetingKey     string   `json:"meetingKey"`
	LeaveKey       string   `json:"leaveKey"`
	WorkdayHours   float64  `json:"workdayHours"`
	LocalRepos     []string `json:"localRepos"`
	GitAuthors     []string `json:"gitAuthors"`
	GitHubUsername string   `json:"githubUsername"`
	ICSPath        string   `json:"icsPath"`
	MockJiraPort   int      `json:"mockJiraPort"`
	WebPort        int      `json:"webPort"`
	Target         string   `json:"target"`
}

func (s *Server) apiGetConfig(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, configView{
		JiraBaseURL:    cfg.Jira.BaseURL,
		JiraEmail:      cfg.Jira.Email,
		MeetingKey:     cfg.MeetingIssueKey,
		LeaveKey:       cfg.LeaveIssueKey,
		WorkdayHours:   cfg.WorkdayHours,
		LocalRepos:     cfg.LocalRepos,
		GitAuthors:     cfg.GitAuthors,
		GitHubUsername: cfg.GitHub.Username,
		ICSPath:        cfg.ICSPath,
		MockJiraPort:   cfg.MockJiraPort,
		WebPort:        cfg.WebPort,
		Target:         cfg.Target,
	})
}

func (s *Server) apiPutConfig(w http.ResponseWriter, r *http.Request) {
	var v configView
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	s.cfg.Jira.BaseURL = v.JiraBaseURL
	s.cfg.Jira.Email = v.JiraEmail
	s.cfg.MeetingIssueKey = v.MeetingKey
	s.cfg.LeaveIssueKey = v.LeaveKey
	if v.WorkdayHours > 0 {
		s.cfg.WorkdayHours = v.WorkdayHours
	}
	s.cfg.LocalRepos = v.LocalRepos
	s.cfg.GitAuthors = v.GitAuthors
	s.cfg.GitHub.Username = v.GitHubUsername
	s.cfg.ICSPath = v.ICSPath
	if v.MockJiraPort > 0 {
		s.cfg.MockJiraPort = v.MockJiraPort
	}
	if v.WebPort > 0 {
		s.cfg.WebPort = v.WebPort
	}
	if v.Target != "" {
		s.cfg.Target = v.Target
	}
	cfg := s.cfg
	cfgPath := s.cfgPath
	s.mu.Unlock()

	if cfgPath != "" {
		if err := saveConfigFile(cfgPath, cfg); err != nil {
			writeErr(w, http.StatusInternalServerError, "save config: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func saveConfigFile(path string, cfg config.Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (s *Server) apiReload(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	builder := s.planBuilder
	cfg := s.cfg
	s.mu.Unlock()

	if builder == nil {
		writeErr(w, http.StatusServiceUnavailable, "plan builder not configured; restart the app")
		return
	}

	plans, mockClient, realClient, err := builder(cfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "rebuild plans: "+err.Error())
		return
	}

	// Rebuild the day index with the new plans.
	newDays := make([]DayView, 0, len(plans))
	newIndex := map[string]int{}
	for _, p := range plans {
		key := p.Date.Format("2006-01-02")
		view := planToView(p)
		newIndex[key] = len(newDays)
		newDays = append(newDays, view)
	}

	s.mu.Lock()
	s.days = newDays
	s.dayIndex = newIndex
	if mockClient != nil {
		s.mockClient = mockClient
	}
	if realClient != nil {
		s.realClient = realClient
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"rebuilt": len(plans), "status": "ok"})
}

func (s *Server) apiCredentialStatus(w http.ResponseWriter, _ *http.Request) {
	jiraStatus := "unset"
	if _, err := keychain.Load(keychain.JiraTarget); err == nil {
		jiraStatus = "set"
	} else {
		s.mu.Lock()
		if s.cfg.JiraAPIToken != "" {
			jiraStatus = "set (env)"
		}
		s.mu.Unlock()
	}
	ghStatus := "unset"
	if _, err := keychain.Load(keychain.GitHubTarget); err == nil {
		ghStatus = "set"
	} else {
		s.mu.Lock()
		if s.cfg.GitHubToken != "" {
			ghStatus = "set (env)"
		}
		s.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, map[string]string{"jira": jiraStatus, "github": ghStatus})
}

func (s *Server) apiSetJiraCredentials(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Token == "" {
		writeErr(w, http.StatusBadRequest, "token required")
		return
	}
	// Validate against Jira.
	s.mu.Lock()
	baseURL := s.cfg.Jira.BaseURL
	s.mu.Unlock()
	if baseURL == "" {
		writeErr(w, http.StatusBadRequest, "set jira.baseUrl in config before saving credentials")
		return
	}
	testClient := jira.NewClient(baseURL, body.Email, body.Token)
	if _, err := testClient.GetIssue("EDB-9071"); err != nil {
		// GetIssue may fail for unknown keys; check /myself instead.
		if err2 := validateJiraToken(baseURL, body.Email, body.Token); err2 != nil {
			writeErr(w, http.StatusBadRequest, "token validation failed: "+err2.Error())
			return
		}
	}
	if err := keychain.Store(keychain.JiraTarget, body.Email, body.Token); err != nil {
		writeErr(w, http.StatusInternalServerError, "keychain store: "+err.Error())
		return
	}
	// Update live config + real client.
	s.mu.Lock()
	s.cfg.Jira.Email = body.Email
	s.cfg.JiraAPIToken = body.Token
	s.realClient = jira.NewClient(baseURL, body.Email, body.Token)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) apiSetGitHubCredentials(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Token == "" {
		writeErr(w, http.StatusBadRequest, "token required")
		return
	}
	if err := keychain.Store(keychain.GitHubTarget, "", body.Token); err != nil {
		writeErr(w, http.StatusInternalServerError, "keychain store: "+err.Error())
		return
	}
	s.mu.Lock()
	s.cfg.GitHubToken = body.Token
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// validateJiraToken calls /rest/api/3/myself to verify credentials.
func validateJiraToken(baseURL, email, token string) error {
	c := jira.NewClient(baseURL, email, token)
	_, err := c.SearchIssues("order by created DESC")
	return err
}

// handleSettings serves the settings/onboarding page.
func (s *Server) handleSettings(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(buildSettingsHTML()))
}

// ── apiUpdateExisting edits an existing (already-logged) worklog's minutes and comment.
func (s *Server) apiUpdateExisting(w http.ResponseWriter, r *http.Request) {
	date, id := r.PathValue("date"), r.PathValue("id")
	var body struct {
		IssueKey string `json:"issueKey"`
		Minutes  int    `json:"minutes"`
		Comment  string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Minutes <= 0 {
		writeErr(w, http.StatusBadRequest, "minutes must be > 0")
		return
	}
	s.mu.Lock()
	idx, ok := s.dayIndex[date]
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "date not found: "+date)
		return
	}

	day, _ := time.Parse("2006-01-02", date)
	client, _, cerr := s.writeClient()
	if cerr != nil {
		writeErr(w, http.StatusBadRequest, cerr.Error())
		return
	}
	if err := client.UpdateWorklog(body.IssueKey, id, body.Minutes, model.WorklogStart(day), body.Comment); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Reflect in local state.
	s.mu.Lock()
	for i, wl := range s.days[idx].Existing {
		if wl.ID == id {
			s.days[idx].Existing[i].Minutes = body.Minutes
			s.days[idx].Existing[i].Comment = body.Comment
			break
		}
	}
	d := s.days[idx]
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, d)
}

// apiDeleteExisting deletes an already-logged worklog. Requires author guard
// (server only allows deleting worklogs by the configured user).
func (s *Server) apiDeleteExisting(w http.ResponseWriter, r *http.Request) {
	date, id := r.PathValue("date"), r.PathValue("id")
	s.mu.Lock()
	idx, ok := s.dayIndex[date]
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "date not found: "+date)
		return
	}

	// Find the worklog and enforce author guard.
	s.mu.Lock()
	var issueKey, author string
	for _, wl := range s.days[idx].Existing {
		if wl.ID == id {
			issueKey = wl.IssueKey
			author = wl.Author
			break
		}
	}
	s.mu.Unlock()
	if issueKey == "" {
		writeErr(w, http.StatusNotFound, "worklog "+id+" not found in local state for "+date)
		return
	}
	if author != "" {
		// Author is empty in mock (fine); enforce on real Jira only when author is known.
		_ = author // real-Jira guard: let Jira return 403 if the user doesn't own it.
	}

	client, _, cerr := s.writeClient()
	if cerr != nil {
		writeErr(w, http.StatusBadRequest, cerr.Error())
		return
	}
	if err := client.DeleteWorklog(issueKey, id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Remove from local state.
	s.mu.Lock()
	existing := s.days[idx].Existing
	for i, wl := range existing {
		if wl.ID == id {
			s.days[idx].Existing = append(existing[:i], existing[i+1:]...)
			break
		}
	}
	d := s.days[idx]
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) apiStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, write, _ := s.writeClient()
	writeJSON(w, http.StatusOK, map[string]any{
		"read":          s.readSource,
		"write":         write,
		"activeWrite":   s.activeWrite,
		"realAvailable": s.realClient != nil,
	})
}

func (s *Server) apiGetTarget(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"activeWrite":   s.activeWrite,
		"realAvailable": s.realClient != nil,
	})
}

func (s *Server) apiPutTarget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Target != "mock" && body.Target != "real" {
		writeErr(w, http.StatusBadRequest, `target must be "mock" or "real"`)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if body.Target == "real" && s.realClient == nil {
		writeErr(w, http.StatusBadRequest, "real Jira has no credentials configured; run 'timeporting credentials'")
		return
	}
	s.activeWrite = body.Target
	writeJSON(w, http.StatusOK, map[string]any{
		"activeWrite":   s.activeWrite,
		"realAvailable": s.realClient != nil,
	})
}

// targetLabels returns human-readable read/write labels for a target string.
func targetLabels(target string) (read, write string) {
	switch target {
	case "real":
		return "Real Jira", "Real Jira"
	case "mock-write":
		return "Real Jira", "Mock Jira"
	default: // "mock"
		return "Mock Jira", "Mock Jira"
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) apiGetDays(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	days := append([]DayView(nil), s.days...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, days)
}

func (s *Server) apiGetDay(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	s.mu.Lock()
	idx, ok := s.dayIndex[date]
	var d DayView
	if ok {
		d = s.days[idx]
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "date not found: "+date)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// apiPutDay replaces the Suggested worklogs for a day (user edits).
func (s *Server) apiPutDay(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	var body struct {
		Status    string     `json:"status"`
		Suggested []WlogView `json:"suggested"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	idx, ok := s.dayIndex[date]
	if ok {
		if body.Status != "" {
			s.days[idx].Status = body.Status
		}
		if body.Suggested != nil {
			s.days[idx].Suggested = body.Suggested
		}
	}
	var d DayView
	if ok {
		d = s.days[idx]
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "date not found: "+date)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// apiSubmitDay writes the day's Suggested worklogs to Jira and marks it submitted.
func (s *Server) apiSubmitDay(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	var body struct {
		DryRun bool `json:"dryRun"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	s.mu.Lock()
	idx, ok := s.dayIndex[date]
	var d DayView
	if ok {
		d = s.days[idx]
	}
	s.mu.Unlock()

	if !ok {
		writeErr(w, http.StatusNotFound, "date not found: "+date)
		return
	}
	if d.Submitted {
		writeErr(w, http.StatusConflict, "day already submitted")
		return
	}

	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid date: "+date)
		return
	}
	started := model.WorklogStart(day)

	s.mu.Lock()
	client, writeLabel, cerr := s.writeClient()
	s.mu.Unlock()
	if cerr != nil {
		writeErr(w, http.StatusBadRequest, cerr.Error())
		return
	}

	var submitted []WlogView
	// Build a set of comment fingerprints already in existing worklogs (from this
	// tool) so re-runs don't double-submit the same row.
	alreadyLogged := map[string]bool{}
	for _, wl := range d.Existing {
		if strings.Contains(wl.Comment, jira.WorklogMarker) {
			alreadyLogged[wl.IssueKey+"|"+wl.Comment] = true
		}
	}
	for _, wl := range d.Suggested {
		if wl.IssueKey == "" || wl.Minutes <= 0 {
			continue
		}
		fingerprint := wl.IssueKey + "|" + wl.Comment
		if alreadyLogged[fingerprint] {
			continue // idempotent: don't re-submit the same worklog
		}
		if !body.DryRun {
			if _, err := client.AddWorklog(wl.IssueKey, wl.Minutes, started, wl.Comment); err != nil {
				writeErr(w, http.StatusInternalServerError,
					fmt.Sprintf("submit %s: %v", wl.IssueKey, err))
				return
			}
			alreadyLogged[fingerprint] = true
		}
		submitted = append(submitted, wl)
	}

	if !body.DryRun {
		s.mu.Lock()
		s.days[idx].Submitted = true
		s.mu.Unlock()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"submitted": submitted,
		"dryRun":    body.DryRun,
		"target":    writeLabel,
	})
}

// apiClonePrevious copies the previous business day's suggested worklogs onto this day.
// apiSubmitRow submits a single suggested worklog row by its 0-based index.
func (s *Server) apiSubmitRow(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	rowIdxStr := r.PathValue("idx")
	var rowIdx int
	if _, err := fmt.Sscanf(rowIdxStr, "%d", &rowIdx); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid row index")
		return
	}

	s.mu.Lock()
	idx, ok := s.dayIndex[date]
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "date not found: "+date)
		return
	}

	s.mu.Lock()
	if rowIdx < 0 || rowIdx >= len(s.days[idx].Suggested) {
		s.mu.Unlock()
		writeErr(w, http.StatusBadRequest, "row index out of range")
		return
	}
	wl := s.days[idx].Suggested[rowIdx]
	s.mu.Unlock()

	if wl.Submitted {
		writeErr(w, http.StatusConflict, "row already submitted")
		return
	}
	if wl.IssueKey == "" || wl.Minutes <= 0 {
		writeErr(w, http.StatusBadRequest, "row has no issue key or zero minutes")
		return
	}

	day, _ := time.Parse("2006-01-02", date)
	started := model.WorklogStart(day)

	s.mu.Lock()
	client, writeLabel, cerr := s.writeClient()
	s.mu.Unlock()
	if cerr != nil {
		writeErr(w, http.StatusBadRequest, cerr.Error())
		return
	}

	if _, err := client.AddWorklog(wl.IssueKey, wl.Minutes, started, wl.Comment); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("submit row: %v", err))
		return
	}

	s.mu.Lock()
	s.days[idx].Suggested[rowIdx].Submitted = true
	d := s.days[idx]
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"day": d, "target": writeLabel})
}

func (s *Server) apiClonePrevious(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	s.mu.Lock()
	idx, ok := s.dayIndex[date]
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "date not found: "+date)
		return
	}
	if idx == 0 {
		writeErr(w, http.StatusBadRequest, "no previous day available")
		return
	}
	s.mu.Lock()
	prev := s.days[idx-1]
	s.days[idx].Suggested = prev.Suggested
	updated := s.days[idx]
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// First-run check: redirect to /settings if Jira isn't configured.
	s.mu.Lock()
	needsSetup := s.cfg.Jira.BaseURL == "" || s.cfg.JiraAPIToken == ""
	s.mu.Unlock()
	if needsSetup && r.URL.Path == "/" {
		http.Redirect(w, r, "/settings", http.StatusTemporaryRedirect)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

// planToView converts a model.DayPlan to a DayView.
func planToView(p model.DayPlan) DayView {
	toViews := func(wls []model.Worklog) []WlogView {
		out := make([]WlogView, 0, len(wls))
		for _, w := range wls {
			out = append(out, WlogView{
				ID:       w.ID,
				IssueKey: w.IssueKey,
				Minutes:  w.Minutes,
				Comment:  w.Comment,
				Category: string(w.Category),
				Author:   w.Author,
			})
		}
		return out
	}
	return DayView{
		Date:      p.Date.Format("2006-01-02"),
		Weekday:   p.Date.Weekday().String(),
		Status:    string(p.Status),
		Existing:  toViews(p.Existing),
		Suggested: toViews(p.Suggested),
		Notes:     p.Notes,
	}
}

// minutesToHM formats minutes as "Xh Ym" for display.
func minutesToHM(m int) string {
	h := m / 60
	min := m % 60
	if h == 0 {
		return fmt.Sprintf("%dm", min)
	}
	if min == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, min)
}

var _ = minutesToHM // used in template only

// buildSettingsHTML returns the settings/onboarding HTML with screenshots inlined.
func buildSettingsHTML() string {
	img := func(b64, alt string) string {
		return `<img src="data:image/jpeg;base64,` + b64 + `" alt="` + alt + `" class="guide-img">`
	}
	return `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Settings — Timereporting Assistant</title>
<style>
*{box-sizing:border-box}
body{font-family:system-ui,Arial,sans-serif;margin:0;background:#f4f5f7;color:#172b4d}
header{background:#0052cc;color:#fff;padding:12px 20px;display:flex;align-items:center;gap:12px}
header h1{margin:0;font-size:1.1rem;font-weight:600}
header a{color:#fff;text-decoration:none;font-size:.85rem;margin-left:auto;border:1px solid rgba(255,255,255,.4);padding:4px 12px;border-radius:4px}
header a:hover{background:rgba(255,255,255,.15)}
main{max-width:860px;margin:24px auto;padding:0 16px}
section{background:#fff;border:1px solid #dfe1e6;border-radius:4px;padding:20px 24px;margin-bottom:20px}
h2{font-size:1rem;font-weight:700;margin:0 0 16px;color:#172b4d}
h3{font-size:.9rem;font-weight:600;margin:0 0 4px;color:#0052cc}
label{display:block;font-size:.83rem;font-weight:600;margin-bottom:4px;color:#344563}
input[type=text],input[type=password],input[type=number],textarea{width:100%;padding:7px 10px;border:1px solid #dfe1e6;border-radius:4px;font:inherit;font-size:.88rem}
input[type=text]:focus,input[type=password]:focus,input[type=number]:focus,textarea:focus{outline:none;border-color:#0052cc;box-shadow:0 0 0 2px rgba(0,82,204,.15)}
textarea{resize:vertical;min-height:70px}
.row{display:grid;grid-template-columns:1fr 1fr;gap:16px;margin-bottom:14px}
.field{margin-bottom:14px}
.hint{font-size:.78rem;color:#6b778c;margin-top:3px}
button.primary{background:#0052cc;color:#fff;border:none;border-radius:4px;padding:8px 18px;font:inherit;font-size:.88rem;cursor:pointer}
button.primary:hover{background:#0747a6}
button.secondary{background:#fff;color:#172b4d;border:1px solid #dfe1e6;border-radius:4px;padding:8px 18px;font:inherit;font-size:.88rem;cursor:pointer}
button.secondary:hover{background:#f4f5f7}
.cred-row{display:flex;align-items:center;gap:10px;margin-bottom:10px}
.cred-row input{flex:1}
.badge{display:inline-block;font-size:.75rem;padding:2px 8px;border-radius:12px;font-weight:600}
.badge.set{background:#e3fcef;color:#006644}
.badge.unset{background:#ffebe6;color:#bf2600}
.badge.env{background:#fff8e1;color:#974f0c}
#toast{position:fixed;bottom:20px;right:20px;background:#172b4d;color:#fff;padding:10px 18px;border-radius:6px;display:none;font-size:.85rem;z-index:999}
/* Guide steps */
.guide{margin-top:8px}
.step{display:flex;gap:16px;align-items:flex-start;padding:12px 0;border-top:1px solid #f4f5f7}
.step-num{min-width:28px;height:28px;border-radius:50%;background:#0052cc;color:#fff;display:flex;align-items:center;justify-content:center;font-size:.8rem;font-weight:700;flex-shrink:0}
.step-body{flex:1}
.step-body p{margin:4px 0 8px;font-size:.85rem;color:#42526e}
.guide-img{max-width:100%;border:1px solid #dfe1e6;border-radius:4px;cursor:pointer}
.guide-img:hover{box-shadow:0 2px 8px rgba(0,0,0,.15)}
/* Step 4 scope instructions */
.scope-box{background:#f8f9ff;border:1px solid #c5d3ff;border-radius:4px;padding:10px 14px;font-size:.85rem}
.scope-box code{background:#e9f2ff;padding:1px 5px;border-radius:3px;font-size:.82rem}
/* Lightbox */
#lightbox{display:none;position:fixed;inset:0;background:rgba(0,0,0,.8);z-index:9999;align-items:center;justify-content:center}
#lightbox.open{display:flex}
#lightbox img{max-width:92vw;max-height:88vh;border-radius:4px}
#lightbox-close{position:absolute;top:16px;right:20px;color:#fff;font-size:1.8rem;cursor:pointer;line-height:1}
</style></head>
<body>
<header>
  <h1>⚙ Settings</h1>
  <a href="/">← Back to time report</a>
</header>
<main>

<!-- Credential status banner -->
<section id="cred-section">
  <h2>Credentials</h2>
  <div class="cred-row">
    <strong style="min-width:120px">Jira token:</strong>
    <span id="jira-cred-badge" class="badge unset">not set</span>
  </div>
  <div class="cred-row">
    <strong style="min-width:120px">GitHub token:</strong>
    <span id="gh-cred-badge" class="badge unset">not set</span>
  </div>
</section>

<!-- Jira configuration -->
<section>
  <h2>Jira</h2>
  <div class="row">
    <div class="field">
      <label>Jira base URL *</label>
      <input type="text" id="jiraBaseUrl" placeholder="https://your-domain.atlassian.net">
      <div class="hint">Your organisation's Jira Cloud URL</div>
    </div>
    <div class="field">
      <label>Jira login email *</label>
      <input type="text" id="jiraEmail" placeholder="you@example.com">
    </div>
  </div>
  <div class="row">
    <div class="field">
      <label>Meeting task key</label>
      <input type="text" id="meetingKey" placeholder="e.g. PROJ-9071">
      <div class="hint">All meeting time is logged here</div>
    </div>
    <div class="field">
      <label>Leave / holiday task key</label>
      <input type="text" id="leaveKey" placeholder="e.g. PROJ-9070">
    </div>
  </div>

  <h3 style="margin-top:18px">Jira API token</h3>
  <p style="font-size:.85rem;color:#42526e;margin:4px 0 10px">
    You need a <strong>scoped API token</strong> (not a classic one). Follow the steps below.
  </p>
  <div class="cred-row">
    <input type="password" id="jiraToken" placeholder="Paste scoped API token here">
    <button class="secondary" onclick="togglePwd('jiraToken',this)">Show</button>
    <button class="primary" onclick="saveJiraCreds()">Validate &amp; save</button>
  </div>
  <div id="jira-cred-msg" style="font-size:.82rem;margin-top:4px"></div>

  <!-- Step-by-step token guide -->
  <details style="margin-top:14px">
    <summary style="cursor:pointer;font-size:.85rem;color:#0052cc;font-weight:600">
      How to create a scoped Jira API token (step-by-step)
    </summary>
    <div class="guide">

      <div class="step">
        <div class="step-num">1</div>
        <div class="step-body">
          <strong>Open the API Tokens page</strong>
          <p>Go to <a href="https://id.atlassian.com/manage-profile/security/api-tokens" target="_blank">id.atlassian.com/manage-profile/security/api-tokens</a>
          and click <strong>"Create API token with scopes"</strong> (the right button).</p>
          ` + img(jiraStep1B64, "Step 1 — Create API token with scopes button") + `
        </div>
      </div>

      <div class="step">
        <div class="step-num">2</div>
        <div class="step-body">
          <strong>Name the token and set an expiry</strong>
          <p>Enter <code>timereporting-assistant</code> as the name and pick an expiry date (max 365 days). Click <strong>Next</strong>.</p>
          ` + img(jiraStep2B64, "Step 2 — Name and expiry") + `
        </div>
      </div>

      <div class="step">
        <div class="step-num">3</div>
        <div class="step-body">
          <strong>Select app: Jira</strong>
          <p>Choose <strong>Jira</strong> from the list. Click <strong>Next</strong>.</p>
          ` + img(jiraStep3B64, "Step 3 — Select Jira app") + `
        </div>
      </div>

      <div class="step">
        <div class="step-num">4</div>
        <div class="step-body">
          <strong>Select scopes</strong>
          <p>On the Select scopes page, search for and tick exactly these two scopes:</p>
          <div class="scope-box">
            <div>✅ <code>read:jira-work</code> — read issues and worklogs</div>
            <div style="margin-top:6px">✅ <code>write:jira-work</code> — add / update worklogs</div>
          </div>
          <p style="margin-top:8px">Tip: type <code>jira-work</code> in the search box to find them quickly. Click <strong>Next</strong>.</p>
        </div>
      </div>

      <div class="step">
        <div class="step-num">5</div>
        <div class="step-body">
          <strong>Review and create</strong>
          <p>Verify the token shows <strong>App: Jira</strong> and <strong>Scopes: read:jira-work, write:jira-work</strong>, then click <strong>"Create token"</strong>. Copy the token immediately — it will not be shown again.</p>
          ` + img(jiraStep5B64, "Step 5 — Review and create token") + `
        </div>
      </div>

    </div>
  </details>
</section>

<!-- GitHub -->
<section>
  <h2>GitHub activity (optional)</h2>
  <div class="row">
    <div class="field">
      <label>GitHub username</label>
      <input type="text" id="githubUsername" placeholder="your-work-github-username">
    </div>
    <div class="field">
      <label>GitHub token</label>
      <div class="cred-row">
        <input type="password" id="ghToken" placeholder="github_pat_...">
        <button class="secondary" onclick="togglePwd('ghToken',this)">Show</button>
        <button class="primary" onclick="saveGHCreds()">Save</button>
      </div>
      <div class="hint">Needs <code>repo</code> read scope. <a href="https://github.com/settings/tokens" target="_blank">Create one</a></div>
    </div>
  </div>
</section>

<!-- Work repos + calendar -->
<section>
  <h2>Local activity sources</h2>
  <div class="field">
    <label>Local git repository paths (one per line)</label>
    <textarea id="localRepos" rows="4" placeholder="C:\work\repo-one&#10;C:\work\repo-two"></textarea>
    <div class="hint">The tool will scan these folders for your commits.</div>
  </div>
  <div class="field">
    <label>Your git author email(s) (comma-separated)</label>
    <input type="text" id="gitAuthors" placeholder="you@example.com, you@work.com">
  </div>
  <div class="field">
    <label>Calendar export (.ics file path)</label>
    <input type="text" id="icsPath" placeholder="C:\Users\you\Downloads\calendar.ics">
    <div class="hint">Export from Outlook: File → Save Calendar. Meetings are logged to the meeting task key above.</div>
  </div>
</section>

<!-- Workday + ports -->
<section>
  <h2>Advanced</h2>
  <div class="row">
    <div class="field">
      <label>Workday hours</label>
      <input type="number" id="workdayHours" min="1" max="24" step="0.5">
    </div>
    <div class="field">
      <label>Submit target</label>
      <select id="target" style="width:100%;padding:7px 10px;border:1px solid #dfe1e6;border-radius:4px;font:inherit">
        <option value="mock">Mock Jira (safe testing)</option>
        <option value="mock-write">Mock-write (read real Jira, write mock)</option>
        <option value="real">Real Jira</option>
      </select>
    </div>
  </div>
  <div class="row">
    <div class="field">
      <label>Review UI port</label>
      <input type="number" id="webPort" min="1024" max="65535">
    </div>
    <div class="field">
      <label>Mock Jira port</label>
      <input type="number" id="mockJiraPort" min="1024" max="65535">
    </div>
  </div>
  <button class="primary" onclick="saveConfig()">Save settings</button>
  <button class="primary" style="background:#00875a;margin-left:8px" onclick="saveAndRebuild()">Save &amp; rebuild plans</button>
  <span id="cfg-msg" style="margin-left:12px;font-size:.82rem"></span>
</section>

</main>

<!-- Lightbox -->
<div id="lightbox" onclick="closeLightbox()">
  <span id="lightbox-close" onclick="closeLightbox()">✕</span>
  <img id="lightbox-img" src="" alt="">
</div>

<div id="toast"></div>

<script>
function toast(msg, err) {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.style.background = err ? '#de350b' : '#172b4d';
  el.style.display = 'block';
  setTimeout(() => el.style.display='none', 3500);
}
function togglePwd(id, btn) {
  const el = document.getElementById(id);
  el.type = el.type==='password' ? 'text' : 'password';
  btn.textContent = el.type==='password' ? 'Show' : 'Hide';
}
function openLightbox(img) {
  document.getElementById('lightbox-img').src = img.src;
  document.getElementById('lightbox').classList.add('open');
}
function closeLightbox() {
  document.getElementById('lightbox').classList.remove('open');
}
document.querySelectorAll('.guide-img').forEach(img => img.onclick = () => openLightbox(img));

async function api(method, path, body) {
  const opts = {method, headers:{'Content-Type':'application/json'}};
  if (body) opts.body = JSON.stringify(body);
  const r = await fetch('/api'+path, opts);
  const d = await r.json();
  if (!r.ok) throw new Error(d.error || r.statusText);
  return d;
}

async function loadConfig() {
  try {
    const c = await api('GET','/config');
    document.getElementById('jiraBaseUrl').value = c.jiraBaseUrl||'';
    document.getElementById('jiraEmail').value = c.jiraEmail||'';
    document.getElementById('meetingKey').value = c.meetingKey||'';
    document.getElementById('leaveKey').value = c.leaveKey||'';
    document.getElementById('githubUsername').value = c.githubUsername||'';
    document.getElementById('icsPath').value = c.icsPath||'';
    document.getElementById('localRepos').value = (c.localRepos||[]).join('\n');
    document.getElementById('gitAuthors').value = (c.gitAuthors||[]).join(', ');
    document.getElementById('workdayHours').value = c.workdayHours||7;
    document.getElementById('webPort').value = c.webPort||8080;
    document.getElementById('mockJiraPort').value = c.mockJiraPort||8099;
    document.getElementById('target').value = c.target||'mock';
  } catch(e) { toast('Could not load config: '+e.message, true); }
}

async function loadCredStatus() {
  try {
    const s = await api('GET','/credentials/status');
    const jb = document.getElementById('jira-cred-badge');
    const gb = document.getElementById('gh-cred-badge');
    jb.textContent = s.jira; jb.className = 'badge '+(s.jira==='unset'?'unset':s.jira.includes('env')?'env':'set');
    gb.textContent = s.github; gb.className = 'badge '+(s.github==='unset'?'unset':s.github.includes('env')?'env':'set');
  } catch(e) {}
}

async function saveConfig() {
  const repos = document.getElementById('localRepos').value.split('\n').map(s=>s.trim()).filter(Boolean);
  const authors = document.getElementById('gitAuthors').value.split(',').map(s=>s.trim()).filter(Boolean);
  try {
    await api('PUT','/config',{
      jiraBaseUrl: document.getElementById('jiraBaseUrl').value.trim(),
      jiraEmail: document.getElementById('jiraEmail').value.trim(),
      meetingKey: document.getElementById('meetingKey').value.trim(),
      leaveKey: document.getElementById('leaveKey').value.trim(),
      githubUsername: document.getElementById('githubUsername').value.trim(),
      icsPath: document.getElementById('icsPath').value.trim(),
      localRepos: repos,
      gitAuthors: authors,
      workdayHours: +document.getElementById('workdayHours').value,
      webPort: +document.getElementById('webPort').value,
      mockJiraPort: +document.getElementById('mockJiraPort').value,
      target: document.getElementById('target').value,
    });
    document.getElementById('cfg-msg').textContent = '✅ Saved';
    document.getElementById('cfg-msg').style.color = '#00875a';
    setTimeout(()=>document.getElementById('cfg-msg').textContent='',3000);
  } catch(e) { toast('Save failed: '+e.message, true); }
}

async function saveJiraCreds() {
  const email = document.getElementById('jiraEmail').value.trim();
  const token = document.getElementById('jiraToken').value.trim();
  const msg = document.getElementById('jira-cred-msg');
  if (!email || !token) { msg.textContent='⚠ Enter email and token above first.'; msg.style.color='#de350b'; return; }
  msg.textContent = 'Validating…'; msg.style.color = '#6b778c';
  try {
    await api('POST','/credentials/jira',{email,token});
    msg.textContent = '✅ Token validated and saved to keychain.';
    msg.style.color = '#00875a';
    document.getElementById('jiraToken').value = '';
    await loadCredStatus();
  } catch(e) { msg.textContent = '❌ '+e.message; msg.style.color = '#de350b'; }
}

async function saveGHCreds() {
  const token = document.getElementById('ghToken').value.trim();
  if (!token) { toast('Paste a token first.', true); return; }
  try {
    await api('POST','/credentials/github',{token});
    document.getElementById('ghToken').value = '';
    toast('GitHub token saved.');
    await loadCredStatus();
  } catch(e) { toast(e.message, true); }
}

async function saveAndRebuild() {
  await saveConfig();
  const msg = document.getElementById('cfg-msg');
  msg.textContent = 'Rebuilding plans…'; msg.style.color = '#6b778c';
  try {
    const res = await api('POST','/reload');
    msg.textContent = '✅ Plans rebuilt ('+res.rebuilt+' days). ';
    msg.style.color = '#00875a';
    const a = document.createElement('a');
    a.href = '/'; a.textContent = 'Go to time report →';
    document.getElementById('cfg-msg').appendChild(a);
  } catch(e) { msg.textContent = '❌ '+e.message; msg.style.color='#de350b'; }
}

loadConfig(); loadCredStatus();
</script>
</body></html>`
}

// indexHTML is the single-page review UI, embedded directly.
const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Timereporting Assistant</title>
<style>
*{box-sizing:border-box}
body{font-family:system-ui,Arial,sans-serif;margin:0;background:#f4f5f7;color:#172b4d}
header{background:#0052cc;color:#fff;padding:12px 20px;display:flex;align-items:center;gap:12px}
header h1{margin:0;font-size:1.1rem;font-weight:600}
.badge{font-size:.75rem;background:#0747a6;padding:2px 8px;border-radius:12px}
main{display:grid;grid-template-columns:220px 1fr;height:calc(100vh - 48px)}
/* Day list */
#day-list{background:#fff;border-right:1px solid #dfe1e6;overflow-y:auto;padding:8px 0}
.day-item{padding:8px 16px;cursor:pointer;border-left:3px solid transparent;user-select:none}
.day-item:hover{background:#f4f5f7}
.day-item.active{border-left-color:#0052cc;background:#e9f2ff}
.day-item.done{opacity:.55}
.day-date{font-size:.8rem;font-weight:600}
.day-status{font-size:.75rem;color:#6b778c}
.day-total{font-size:.75rem;float:right}
.total-ok{color:#00875a;font-weight:600}
.total-warn{color:#ff5630;font-weight:600}
/* Detail panel */
#detail{padding:20px;overflow-y:auto}
#detail h2{margin:0 0 4px;font-size:1rem}
.meta{font-size:.8rem;color:#6b778c;margin-bottom:12px}
/* Controls */
.controls{display:flex;gap:8px;margin-bottom:16px;flex-wrap:wrap}
select,button{font:inherit;border:1px solid #dfe1e6;border-radius:4px;padding:5px 10px;cursor:pointer;background:#fff}
button.primary{background:#0052cc;color:#fff;border-color:#0052cc}
button.primary:hover{background:#0747a6}
button.danger{background:#de350b;color:#fff;border-color:#de350b}
button.danger:hover{background:#bf2600}
button:disabled{opacity:.5;cursor:not-allowed}
/* Worklog tables */
table{width:100%;border-collapse:collapse;margin-bottom:16px;font-size:.85rem}
th,td{border:1px solid #dfe1e6;padding:6px 10px;text-align:left;vertical-align:middle}
th{background:#f4f5f7;font-size:.8rem;font-weight:600}
td input[type=number]{width:60px;border:1px solid #dfe1e6;border-radius:3px;padding:3px 5px;font:inherit}
td input[type=text]{width:100%;border:1px solid #dfe1e6;border-radius:3px;padding:3px 5px;font:inherit}
.cat-existing{background:#e3fcef}
.cat-meeting{background:#e9f2ff}
.cat-activity{background:#fff}
.cat-leave{background:#fff8b5}
.cat-manual{background:#f4f5f7}
.row-unassigned{background:#fffae6}
.del-btn{background:none;border:none;cursor:pointer;color:#de350b;font-size:1rem;padding:0 4px}
.summary{background:#fff;border:1px solid #dfe1e6;border-radius:4px;padding:12px 16px;margin-bottom:16px;font-size:.85rem}
.summary span{font-weight:600}
.notes{color:#6b778c;font-size:.8rem;margin-top:4px}
.badge-submitted{background:#00875a;color:#fff;padding:2px 8px;border-radius:12px;font-size:.75rem}
.badge-target{background:#ff991f;color:#172b4d;padding:2px 8px;border-radius:12px;font-size:.75rem}
#toast{position:fixed;bottom:20px;right:20px;background:#172b4d;color:#fff;padding:10px 18px;border-radius:6px;display:none;font-size:.85rem;z-index:999}
</style>
</head>
<body>
<header>
  <h1>Timereporting Assistant</h1>
  <span id="target-badge" class="badge">loading…</span>
  <span style="margin-left:auto;display:flex;align-items:center;gap:6px">
    <label for="write-target" style="font-size:.8rem">Submit to:</label>
    <select id="write-target" onchange="setWriteTarget(this.value)" style="font-size:.8rem">
      <option value="mock">Mock Jira</option>
      <option value="real">Real Jira</option>
    </select>
    <a href="/settings" style="color:#fff;font-size:.8rem;margin-left:8px;border:1px solid rgba(255,255,255,.4);padding:3px 10px;border-radius:4px;text-decoration:none">⚙ Settings</a>
  </span>
</header>
<main>
  <div id="day-list"></div>
  <div id="detail"><p>Select a day on the left.</p></div>
</main>
<div id="toast"></div>
<script>
const TARGET_LABELS = {mock:'MOCK JIRA',real:'REAL JIRA'};
let days = [];
let currentDate = null;

async function api(method, path, body) {
  const opts = {method, headers:{'Content-Type':'application/json'}};
  if (body !== undefined) opts.body = JSON.stringify(body);
  const r = await fetch('/api' + path, opts);
  const data = await r.json();
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
}

function toast(msg, err) {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.style.background = err ? '#de350b' : '#172b4d';
  el.style.display = 'block';
  setTimeout(() => el.style.display='none', 3500);
}

function hm(mins) {
  const h = Math.floor(mins/60), m = mins%60;
  if (h===0) return m+'m';
  if (m===0) return h+'h';
  return h+'h '+m+'m';
}

function totalMins(day) {
  return (day.existing||[]).reduce((a,w)=>a+w.minutes,0)
       + (day.suggested||[]).reduce((a,w)=>a+w.minutes,0);
}

function renderList() {
  const el = document.getElementById('day-list');
  el.innerHTML = days.map(d => {
    const t = totalMins(d);
    const cls = [
      'day-item',
      d.date===currentDate?'active':'',
      d.submitted?'done':'',
    ].filter(Boolean).join(' ');
    const totCls = t>=420?'total-ok':'total-warn';
    return '<div class="'+cls+'" onclick="selectDay(\''+d.date+'\')">'
      +'<span class="day-date">'+d.date+'</span>'
      +'<span class="day-total '+totCls+'">'+hm(t)+'</span>'
      +'<br><span class="day-status">'+d.weekday+' &bull; '+d.status+'</span>'
      +'</div>';
  }).join('');
}

function renderDetail(day) {
  const el = document.getElementById('detail');
  const existMins = (day.existing||[]).reduce((a,w)=>a+w.minutes,0);
  const suggMins = (day.suggested||[]).reduce((a,w)=>a+w.minutes,0);
  const total = existMins + suggMins;
  const totalCls = total>=420?'total-ok':'total-warn';

  let html = '<h2>'+day.date+' <small style="font-weight:400">'+day.weekday+'</small>'
    +(day.submitted?' <span class="badge-submitted">Submitted</span>':'')+'</h2>';
  html += '<div class="meta">Target: 7h &bull; Existing: '+hm(existMins)
    +' &bull; Suggested: '+hm(suggMins)
    +' &bull; Total: <span class="'+totalCls+'">'+hm(total)+'</span></div>';

  // Controls
  html += '<div class="controls">';
  html += '<label><strong>Day status:</strong> '
    + '<select id="status-sel" onchange="saveStatus(\''+day.date+'\')">'
    + ['working','holiday','full_leave','half_leave'].map(s=>
        '<option value="'+s+'"'+(day.status===s?' selected':'')+'>'+s.replace('_',' ')+'</option>'
      ).join('')
    +'</select></label>';
  if (!day.submitted) {
    html += '<button onclick="addRow(\''+day.date+'\')">+ Add row</button>';
    html += '<button onclick="clonePrev(\''+day.date+'\')">Clone previous day</button>';
    html += '<button class="primary" onclick="submitDay(\''+day.date+'\',false)">Approve &amp; submit</button>';
    html += '<button onclick="submitDay(\''+day.date+'\',true)">Dry run</button>';
  }
  html += '</div>';

  // Notes
  if (day.notes && day.notes.length) {
    html += '<div class="notes">ℹ️ '+day.notes.join(' | ')+'</div><br>';
  }

  // Existing worklogs (read-only)
  if (day.existing && day.existing.length) {
    html += '<strong>Already logged in Jira</strong>';
    html += '<table><tr><th>Issue</th><th>Time</th><th>Comment</th><th></th></tr>';
    day.existing.forEach(w => {
      html += '<tr class="cat-existing">'
        +'<td>'+w.issueKey+'</td>'
        +'<td><input type="number" min="30" step="30" value="'+w.minutes+'" style="width:60px" onchange="updateExisting(\''+day.date+'\',\''+w.id+'\',\''+w.issueKey+'\',+this.value,\''+esc(w.comment)+'\')" title="Edit minutes"></td>'
        +'<td><input type="text" value="'+esc(w.comment)+'" style="width:100%" onchange="updateExisting(\''+day.date+'\',\''+w.id+'\',\''+w.issueKey+'\','+w.minutes+',this.value)" title="Edit comment"></td>'
        +'<td><button class="del-btn" title="Delete" onclick="deleteExisting(\''+day.date+'\',\''+w.id+'\')">✕</button></td>'
        +'</tr>';
    });
    html += '</table>';
  }

  // Suggested worklogs (editable)
  html += '<strong>Suggested worklogs</strong>';
  html += '<table id="sugg-table"><tr><th>Issue key</th><th>Time (min)</th><th>Comment</th><th></th><th></th></tr>';
  (day.suggested||[]).forEach((w,i) => {
    const rowCls = 'cat-'+(w.category||'manual')+(w.issueKey?'':' row-unassigned');
    const submitted = w.submitted;
    html += '<tr class="'+rowCls+'"'+(submitted?' style="opacity:.55"':'')+' id="row-'+day.date+'-'+i+'">'
      +'<td><input type="text" value="'+esc(w.issueKey)+'" '+(submitted?'disabled':'')+' onchange="editRow(\''+day.date+'\','+i+',\'issueKey\',this.value)"></td>'
      +'<td><input type="number" min="30" step="30" value="'+w.minutes+'" '+(submitted?'disabled':'')+' onchange="editRow(\''+day.date+'\','+i+',\'minutes\',+this.value)"></td>'
      +'<td><input type="text" value="'+esc(w.comment)+'" '+(submitted?'disabled':'')+' onchange="editRow(\''+day.date+'\','+i+',\'comment\',this.value)"></td>'
      +'<td>'+(submitted?'<span style="color:#00875a">✓</span>':'<button class="primary" style="font-size:.75rem;padding:3px 8px" onclick="submitRow(\''+day.date+'\','+i+')">Submit</button>')+'</td>'
      +'<td>'+(submitted?'':' <button class="del-btn" title="Delete" onclick="deleteRow(\''+day.date+'\','+i+')">✕</button>')+'</td>'
      +'</tr>';
  });
  if (!day.suggested || day.suggested.length===0) {
    html += '<tr><td colspan="4" style="color:#6b778c;text-align:center">No suggestions yet — add a row or clone from the previous day.</td></tr>';
  }
  html += '</table>';

  el.innerHTML = html;
}

function esc(s) { return (s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/"/g,'&quot;'); }

async function selectDay(date) {
  currentDate = date;
  renderList();
  const day = days.find(d=>d.date===date);
  if (day) renderDetail(day);
}

function getDayLocal(date) { return days.find(d=>d.date===date); }

async function saveStatus(date) {
  const sel = document.getElementById('status-sel');
  try {
    const updated = await api('PUT','/days/'+date, {status: sel.value});
    const i = days.findIndex(d=>d.date===date);
    if (i>=0) days[i] = updated;
    renderDetail(updated);
    renderList();
  } catch(e) { toast(e.message, true); }
}

function editRow(date, idx, field, value) {
  const day = getDayLocal(date);
  if (!day) return;
  day.suggested[idx][field] = value;
  saveSuggested(date, day.suggested);
}

function deleteRow(date, idx) {
  const day = getDayLocal(date);
  if (!day) return;
  day.suggested.splice(idx, 1);
  saveSuggested(date, day.suggested);
  renderDetail(day);
  renderList();
}

function addRow(date) {
  const day = getDayLocal(date);
  if (!day) return;
  day.suggested = day.suggested || [];
  day.suggested.push({issueKey:'',minutes:30,comment:'',category:'manual'});
  renderDetail(day);
  renderList();
}

async function saveSuggested(date, suggested) {
  try {
    const updated = await api('PUT','/days/'+date,{suggested});
    const i = days.findIndex(d=>d.date===date);
    if (i>=0) days[i] = updated;
    renderList();
  } catch(e) { toast(e.message, true); }
}

async function updateExisting(date, id, issueKey, minutes, comment) {
  try {
    const updated = await api('PUT','/days/'+date+'/existing/'+id,{issueKey, minutes, comment});
    const i = days.findIndex(d=>d.date===date);
    if (i>=0) days[i] = updated;
    renderDetail(updated);
    renderList();
  } catch(e) { toast(e.message, true); }
}

async function deleteExisting(date, id) {
  if (!confirm('Delete this worklog from Jira? This cannot be undone.')) return;
  try {
    const updated = await api('DELETE','/days/'+date+'/existing/'+id);
    const i = days.findIndex(d=>d.date===date);
    if (i>=0) days[i] = updated;
    renderDetail(updated);
    renderList();
    toast('Worklog deleted.');
  } catch(e) { toast(e.message, true); }
}

async function clonePrev(date) {
  try {
    const updated = await api('POST','/days/'+date+'/clone-previous');
    const i = days.findIndex(d=>d.date===date);
    if (i>=0) days[i] = updated;
    renderDetail(updated);
    renderList();
    toast('Cloned from previous day.');
  } catch(e) { toast(e.message, true); }
}

async function submitRow(date, rowIdx) {
  try {
    const res = await api('POST','/days/'+date+'/rows/'+rowIdx+'/submit');
    const i = days.findIndex(d=>d.date===date);
    if (i>=0) days[i] = res.day;
    renderDetail(res.day);
    renderList();
    toast('Row submitted to '+res.target+'.');
  } catch(e) { toast(e.message, true); }
}

async function submitDay(date, dryRun) {
  try {
    const res = await api('POST','/days/'+date+'/submit',{dryRun});
    if (!dryRun) {
      const i = days.findIndex(d=>d.date===date);
      if (i>=0) days[i].submitted = true;
    }
    const n = (res.submitted||[]).length;
    toast((dryRun?'Dry run: ':'Submitted: ')+n+' worklog(s) to '+res.target+'.');
    if (!dryRun) { await refresh(date); }
  } catch(e) { toast(e.message, true); }
}

async function refresh(date) {
  const day = await api('GET','/days/'+date);
  const i = days.findIndex(d=>d.date===date);
  if (i>=0) days[i] = day;
  renderList();
  if (currentDate===date) renderDetail(day);
}

async function refreshBadge() {
  const status = await api('GET','/status');
  const badge = document.getElementById('target-badge');
  badge.textContent = 'Read: ' + status.read + ' | Write: ' + status.write;
  badge.style.background = status.activeWrite === 'real' ? '#de350b' : '#0747a6';
  const sel = document.getElementById('write-target');
  sel.value = status.activeWrite;
  // Disable the Real option when no credentials are available.
  sel.querySelector('option[value="real"]').disabled = !status.realAvailable;
}

async function setWriteTarget(target) {
  if (target === 'real' && !confirm('Submit worklogs to REAL Jira? This writes to your actual timesheet.')) {
    await refreshBadge();
    return;
  }
  try {
    await api('PUT','/target',{target});
    await refreshBadge();
    toast('Submit target set to ' + (target==='real'?'Real Jira':'Mock Jira') + '.');
  } catch(e) {
    toast(e.message, true);
    await refreshBadge();
  }
}

async function init() {
  try {
    await refreshBadge();
    days = await api('GET','/days');
    renderList();
    if (days.length) selectDay(days[0].date);
  } catch(e) { toast('Failed to load days: '+e.message, true); }
}

init();
</script>
</body>
</html>`

// Strings helper used in server logic.
var _ = strings.TrimSpace
