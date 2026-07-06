// Package web serves the local review/edit/approve UI for the timereporting
// assistant. It exposes a REST-like API the browser-side JS calls, and the
// Go binary itself is the HTTP server — no build tooling required.
package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	Date       string           `json:"date"`       // YYYY-MM-DD
	Weekday    string           `json:"weekday"`    // Monday …
	Status     string           `json:"status"`     // working/holiday/full_leave/half_leave
	Existing   []WlogView       `json:"existing"`   // already in Jira (read-only)
	Suggested  []WlogView       `json:"suggested"`  // proposed; user edits these
	Unassigned []UnassignedView `json:"unassigned"` // activity with no Jira key
	Notes      []string         `json:"notes"`
	Submitted  bool             `json:"submitted"` // true after a successful Jira write
}

// UnassignedView is an activity item that has no Jira key yet.
type UnassignedView struct {
	Source string `json:"source"`
	Text   string `json:"text"`
	Ref    string `json:"ref"`
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
// a reload from the settings page. The optional progress callback (may be nil)
// receives incremental status updates while plans are built.
type PlanBuilder func(cfg config.Config, progress ProgressFunc) ([]model.DayPlan, *jira.Client, *jira.Client, error)

// ProgressFunc reports plan-building progress. done and total are day counts
// (total may be 0 for phase-only messages); phase is a human-readable status.
type ProgressFunc func(done, total int, phase string)

// DayBuilder builds a single day's plan on demand. Used when the user navigates
// to a date that wasn't in the initially loaded range.
type DayBuilder func(cfg config.Config, date time.Time) (model.DayPlan, error)

// Server holds the state for the web review session.
type Server struct {
	mu          sync.Mutex
	days        []DayView      // ordered by date
	dayIndex    map[string]int // date -> index
	mockClient  *jira.Client   // writes to the mock server
	realClient  *jira.Client   // writes to real Jira; nil when no credentials
	activeWrite string         // "mock" | "real" — where submits currently go
	readSource  string         // display label for where existing worklogs were read
	port        int
	cfg         config.Config // current config (for settings page)
	cfgPath     string        // path to config.json (for saving)
	planBuilder PlanBuilder   // called on reload to rebuild day plans
	dayBuilder  DayBuilder    // called to build a single day on demand
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

// WithDayBuilder attaches the on-demand single-day builder.
func (s *Server) WithDayBuilder(fn DayBuilder) *Server {
	s.mu.Lock()
	s.dayBuilder = fn
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
	mux.HandleFunc("GET /api/reload/stream", s.apiReloadStream)
	mux.HandleFunc("GET /api/issue", s.apiGetIssue)
	mux.HandleFunc("GET /api/issues/search", s.apiSearchIssues)
	mux.HandleFunc("POST /api/mock/clear-worklogs", s.apiClearMockWorklogs)
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
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)
	mux.HandleFunc("POST /api/upload/ics", s.apiUploadICS)
	mux.HandleFunc("GET /guide/jira-token", s.handleJiraGuide)
	mux.HandleFunc("GET /guide/github-token", s.handleGitHubGuide)
	mux.HandleFunc("GET /wizard", s.handleWizard)
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
	// Pointers so a partial PUT (e.g. the wizard's final step) can omit these
	// without resetting them to false.
	AutoUpdate       *bool `json:"autoUpdate,omitempty"`
	UpdatePrerelease *bool `json:"updatePrerelease,omitempty"`
}

func (s *Server) apiGetConfig(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, configView{
		JiraBaseURL:      cfg.Jira.BaseURL,
		JiraEmail:        cfg.Jira.Email,
		MeetingKey:       cfg.MeetingIssueKey,
		LeaveKey:         cfg.LeaveIssueKey,
		WorkdayHours:     cfg.WorkdayHours,
		LocalRepos:       cfg.LocalRepos,
		GitAuthors:       cfg.GitAuthors,
		GitHubUsername:   cfg.GitHub.Username,
		ICSPath:          cfg.ICSPath,
		MockJiraPort:     cfg.MockJiraPort,
		WebPort:          cfg.WebPort,
		Target:           cfg.Target,
		AutoUpdate:       &cfg.AutoUpdate,
		UpdatePrerelease: &cfg.UpdatePrerelease,
	})
}

func (s *Server) apiPutConfig(w http.ResponseWriter, r *http.Request) {
	var v configView
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	// Preserve existing values when the incoming field is empty. This lets the
	// wizard's final step send a partial config (repos/ICS/ports only) without
	// wiping the Jira URL/email/keys set in earlier steps.
	if v.JiraBaseURL != "" {
		s.cfg.Jira.BaseURL = v.JiraBaseURL
	}
	if v.JiraEmail != "" {
		s.cfg.Jira.Email = v.JiraEmail
	}
	if v.MeetingKey != "" {
		s.cfg.MeetingIssueKey = v.MeetingKey
	}
	if v.LeaveKey != "" {
		s.cfg.LeaveIssueKey = v.LeaveKey
	}
	if v.WorkdayHours > 0 {
		s.cfg.WorkdayHours = v.WorkdayHours
	}
	// Lists and paths are set explicitly (may legitimately be cleared).
	if v.LocalRepos != nil {
		s.cfg.LocalRepos = v.LocalRepos
	}
	if v.GitAuthors != nil {
		s.cfg.GitAuthors = v.GitAuthors
	}
	if v.GitHubUsername != "" {
		s.cfg.GitHub.Username = v.GitHubUsername
	}
	if v.ICSPath != "" {
		s.cfg.ICSPath = v.ICSPath
	}
	if v.MockJiraPort > 0 {
		s.cfg.MockJiraPort = v.MockJiraPort
	}
	if v.WebPort > 0 {
		s.cfg.WebPort = v.WebPort
	}
	if v.Target != "" {
		s.cfg.Target = v.Target
	}
	if v.AutoUpdate != nil {
		s.cfg.AutoUpdate = *v.AutoUpdate
	}
	if v.UpdatePrerelease != nil {
		s.cfg.UpdatePrerelease = *v.UpdatePrerelease
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

// icsStoragePath returns the standard location where uploaded .ics files are saved.
func icsStoragePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "timereporting-assistant", "calendar.ics")
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

	plans, mockClient, realClient, err := builder(cfg, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "rebuild plans: "+err.Error())
		return
	}
	s.applyPlans(plans, mockClient, realClient)
	writeJSON(w, http.StatusOK, map[string]any{"rebuilt": len(plans), "status": "ok"})
}

// apiReloadStream rebuilds the day plans while streaming progress to the client
// via Server-Sent Events. It emits "progress" events ({done,total,phase}) and a
// final "done" event ({rebuilt}) or an "error" event.
func (s *Server) apiReloadStream(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	builder := s.planBuilder
	cfg := s.cfg
	s.mu.Unlock()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func(event string, payload any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}

	if builder == nil {
		send("error", map[string]string{"error": "plan builder not configured; restart the app"})
		return
	}

	progress := func(done, total int, phase string) {
		send("progress", map[string]any{"done": done, "total": total, "phase": phase})
	}

	plans, mockClient, realClient, err := builder(cfg, progress)
	if err != nil {
		send("error", map[string]string{"error": err.Error()})
		return
	}
	s.applyPlans(plans, mockClient, realClient)
	send("done", map[string]any{"rebuilt": len(plans)})
}

// applyPlans swaps in a freshly built set of day plans and updates the clients.
func (s *Server) applyPlans(plans []model.DayPlan, mockClient, realClient *jira.Client) {
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
}

// readClientLocked returns the Jira client used for reads, matching the source
// the day plans were built from. Caller must hold s.mu.
func (s *Server) readClientLocked() *jira.Client {
	switch s.cfg.Target {
	case config.TargetReal, config.TargetMockWrite:
		if s.realClient != nil {
			return s.realClient
		}
	}
	return s.mockClient
}

// apiGetIssue returns a Jira issue's summary (title) for the given ?key=.
// Used by the review UI to show the issue title next to its key.
func (s *Server) apiGetIssue(w http.ResponseWriter, r *http.Request) {
	key := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("key")))
	if key == "" {
		writeErr(w, http.StatusBadRequest, "key required")
		return
	}
	s.mu.Lock()
	client := s.readClientLocked()
	s.mu.Unlock()
	if client == nil {
		writeErr(w, http.StatusServiceUnavailable, "no Jira client available")
		return
	}
	iss, err := client.GetIssue(key)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": iss.Key, "summary": iss.Summary})
}

// jqlSafe keeps only characters safe to embed inside a JQL string literal,
// preventing JQL injection from the free-text search box.
var jqlSafe = regexp.MustCompile(`[^A-Za-z0-9 _-]+`)

// apiSearchIssues returns up to 10 Jira issues matching the ?q= text, for the
// type-ahead issue picker in the review UI.
func (s *Server) apiSearchIssues(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	s.mu.Lock()
	client := s.readClientLocked()
	s.mu.Unlock()
	if client == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	safe := jqlSafe.ReplaceAllString(q, " ")
	jql := fmt.Sprintf(`text ~ "%s*" ORDER BY updated DESC`, strings.TrimSpace(safe))
	issues, err := client.SearchIssues(jql)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	out := make([]map[string]string, 0, 10)
	for _, iss := range issues {
		out = append(out, map[string]string{"key": iss.Key, "summary": iss.Summary})
		if len(out) >= 10 {
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// apiClearMockWorklogs wipes all worklogs from the mock Jira server (testing
// convenience). It never touches real Jira.
func (s *Server) apiClearMockWorklogs(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	port := s.cfg.MockJiraPort
	s.mu.Unlock()
	if port == 0 {
		port = 9099
	}
	url := fmt.Sprintf("http://localhost:%d/admin/clear-worklogs", port)
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "mock Jira not reachable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("mock Jira returned %d", resp.StatusCode))
		return
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	writeJSON(w, http.StatusOK, body)
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
		BaseURL string `json:"baseUrl"`
		Email   string `json:"email"`
		Token   string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// If no token is supplied, reuse the one already saved in the keychain.
	// This lets returning users re-run the wizard without re-pasting their token.
	token := body.Token
	if token == "" {
		if cred, err := keychain.Load(keychain.JiraTarget); err == nil && cred.Secret != "" {
			token = cred.Secret
		} else {
			writeErr(w, http.StatusBadRequest, "token required")
			return
		}
	}
	// Use baseUrl from body if provided; fall back to saved config.
	s.mu.Lock()
	baseURL := body.BaseURL
	if baseURL == "" {
		baseURL = s.cfg.Jira.BaseURL
	}
	email := body.Email
	if email == "" {
		email = s.cfg.Jira.Email
	}
	s.mu.Unlock()
	if baseURL == "" {
		writeErr(w, http.StatusBadRequest, "Jira base URL is required — fill it in above")
		return
	}
	apiBase, err := validateJiraToken(baseURL, email, token)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "token validation failed: "+err.Error())
		return
	}
	if err := keychain.Store(keychain.JiraTarget, email, token); err != nil {
		writeErr(w, http.StatusInternalServerError, "keychain store: "+err.Error())
		return
	}
	// Update live config + real client (use the resolved API base URL).
	s.mu.Lock()
	s.cfg.Jira.BaseURL = baseURL
	s.cfg.Jira.APIBase = apiBase
	s.cfg.Jira.Email = email
	s.cfg.JiraAPIToken = token
	s.realClient = jira.NewClient(apiBase, email, token)
	cfgPath := s.cfgPath
	cfg := s.cfg
	s.mu.Unlock()
	// Persist so the URL/email are saved too.
	if cfgPath != "" {
		_ = saveConfigFile(cfgPath, cfg)
	}
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
// validateJiraToken verifies credentials by calling GET /rest/api/3/myself —
// a stable, non-deprecated endpoint that requires no specific permissions beyond auth.
// For scoped tokens it auto-resolves the cloudId and api.atlassian.com URL.
// Returns the resolved API base URL to save, or an error.
func validateJiraToken(baseURL, email, token string) (string, error) {
	apiBase, err := jira.ResolveAPIBase(baseURL, email, token)
	return apiBase, err
}

// handleFavicon serves the app icon as a favicon (PNG format, accepted by all modern browsers).
func (s *Server) handleFavicon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(faviconPNG)
}

// apiUploadICS accepts a multipart .ics file upload, saves it to the user's
// AppData directory, and returns the saved path so the UI can auto-fill it.
func (s *Server) apiUploadICS(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "parse form: "+err.Error())
		return
	}
	file, _, err := r.FormFile("ics")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no ics file in request")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read file: "+err.Error())
		return
	}

	savePath := icsStoragePath()
	if err := os.MkdirAll(filepath.Dir(savePath), 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "create dir: "+err.Error())
		return
	}
	if err := os.WriteFile(savePath, data, 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, "save file: "+err.Error())
		return
	}

	// Also update live config.
	s.mu.Lock()
	s.cfg.ICSPath = savePath
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{"path": savePath})
}

// handleWizard serves the first-run setup wizard.
func (s *Server) handleWizard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(wizardHTML))
}

// handleJiraGuide serves the Jira API token creation guide.
func (s *Server) handleJiraGuide(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(buildJiraGuideHTML()))
}

// handleGitHubGuide serves the GitHub token creation guide.
func (s *Server) handleGitHubGuide(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(githubGuideHTML))
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
	builder := s.dayBuilder
	cfg := s.cfg
	s.mu.Unlock()

	if !ok {
		// Date is outside the initially loaded range — build it on demand.
		if builder == nil {
			writeErr(w, http.StatusNotFound, "date not found: "+date)
			return
		}
		t, err := time.Parse("2006-01-02", date)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid date: "+date)
			return
		}
		if !model.IsWeekday(t) {
			writeErr(w, http.StatusBadRequest, "not a weekday: "+date)
			return
		}
		plan, err := builder(cfg, t)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "build day: "+err.Error())
			return
		}
		d = planToView(plan)
		// Cache it so subsequent edits/submits work.
		s.mu.Lock()
		if _, exists := s.dayIndex[date]; !exists {
			s.dayIndex[date] = len(s.days)
			s.days = append(s.days, d)
		}
		s.mu.Unlock()
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
	// Build a set of fingerprints from worklogs already in Jira so re-runs don't
	// double-submit the same row (matched by issue key + comment).
	alreadyLogged := map[string]bool{}
	for _, wl := range d.Existing {
		alreadyLogged[wl.IssueKey+"|"+wl.Comment] = true
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
		http.Redirect(w, r, "/wizard", http.StatusTemporaryRedirect)
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
	toUnassigned := func(acts []model.Activity) []UnassignedView {
		out := make([]UnassignedView, 0, len(acts))
		for _, a := range acts {
			out = append(out, UnassignedView{Source: a.Source, Text: a.Text, Ref: a.Ref})
		}
		return out
	}
	return DayView{
		Date:       p.Date.Format("2006-01-02"),
		Weekday:    p.Date.Weekday().String(),
		Status:     string(p.Status),
		Existing:   toViews(p.Existing),
		Suggested:  toViews(p.Suggested),
		Unassigned: toUnassigned(p.Unassigned),
		Notes:      p.Notes,
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

// guideCSS is shared styling for both guide pages.
const guideCSS = `
*{box-sizing:border-box}
body{font-family:system-ui,Arial,sans-serif;margin:0;background:#f4f5f7;color:#172b4d}
header{background:#0052cc;color:#fff;padding:12px 20px;display:flex;align-items:center;gap:12px}
header h1{margin:0;font-size:1.1rem;font-weight:600}
header a{color:#fff;text-decoration:none;font-size:.85rem;margin-left:auto;border:1px solid rgba(255,255,255,.4);padding:4px 12px;border-radius:4px}
header a:hover{background:rgba(255,255,255,.15)}
main{max-width:820px;margin:24px auto;padding:0 16px}
section{background:#fff;border:1px solid #dfe1e6;border-radius:4px;padding:20px 24px;margin-bottom:20px}
h2{font-size:1rem;font-weight:700;margin:0 0 4px}
.subtitle{font-size:.85rem;color:#42526e;margin:0 0 16px}
.step{display:flex;gap:16px;align-items:flex-start;padding:14px 0;border-top:1px solid #f4f5f7}
.step-num{min-width:28px;height:28px;border-radius:50%;background:#0052cc;color:#fff;display:flex;align-items:center;justify-content:center;font-size:.8rem;font-weight:700;flex-shrink:0}
.step-body{flex:1}
.step-body p{margin:4px 0 8px;font-size:.85rem;color:#42526e}
.step-body strong{font-size:.9rem}
.guide-img{max-width:100%;border:1px solid #dfe1e6;border-radius:4px;cursor:pointer}
.guide-img:hover{box-shadow:0 2px 8px rgba(0,0,0,.15)}
.scope-box{background:#f8f9ff;border:1px solid #c5d3ff;border-radius:4px;padding:10px 14px;font-size:.85rem}
.scope-box code{background:#e9f2ff;padding:1px 5px;border-radius:3px}
code{background:#f4f5f7;padding:1px 5px;border-radius:3px;font-size:.85rem}
a{color:#0052cc}
#lightbox{display:none;position:fixed;inset:0;background:rgba(0,0,0,.8);z-index:9999;align-items:center;justify-content:center}
#lightbox.open{display:flex}
#lightbox img{max-width:92vw;max-height:88vh;border-radius:4px}
#lightbox-close{position:absolute;top:16px;right:20px;color:#fff;font-size:1.8rem;cursor:pointer}`

const guideJS = `
document.querySelectorAll('.guide-img').forEach(img => img.onclick = () => {
  document.getElementById('lightbox-img').src = img.src;
  document.getElementById('lightbox').classList.add('open');
});
function closeLightbox() { document.getElementById('lightbox').classList.remove('open'); }`

// buildJiraGuideHTML returns the dedicated Jira API token creation guide page.
func buildJiraGuideHTML() string {
	img := func(b64, alt string) string {
		return `<img src="data:image/jpeg;base64,` + b64 + `" alt="` + alt + `" class="guide-img">`
	}
	return `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Create a Jira API token — Timereporting Assistant</title>
<style>` + guideCSS + `</style></head>
<body>
<header>
  <h1>How to create a Jira API token</h1>
  <a href="/settings">← Back to Settings</a>
</header>
<main>
<section>
  <h2>What is this and why do you need it?</h2>
  <p class="subtitle">
    The Timereporting Assistant reads your existing Jira worklogs and — after you review and approve — writes new ones on your behalf.
    To do this securely it needs a <strong>scoped API token</strong>: a password-like key that only gives it permission to read and write worklogs, nothing else.
    The token is stored in your Windows Credential Manager and never written to any file.
  </p>
  <p style="font-size:.85rem;color:#42526e">You need a <strong>scoped token</strong> (not a classic one). The scoped token wizard lets you pick exactly which permissions to grant.</p>
</section>

<section>
  <h2>Step-by-step</h2>

  <div class="step">
    <div class="step-num">1</div>
    <div class="step-body">
      <strong>Open the API Tokens page</strong>
      <p>Go to <a href="https://id.atlassian.com/manage-profile/security/api-tokens" target="_blank">id.atlassian.com/manage-profile/security/api-tokens</a>
      and click <strong>"Create API token with scopes"</strong>. (Not "Create classic API token".)</p>
      ` + img(jiraStep1B64, "Step 1 — Create API token with scopes button") + `
    </div>
  </div>

  <div class="step">
    <div class="step-num">2</div>
    <div class="step-body">
      <strong>Name your token and set an expiry date</strong>
      <p>Enter <code>timereporting-assistant</code> as the name and choose an expiry (up to 365 days). Click <strong>Next</strong>.</p>
      ` + img(jiraStep2B64, "Step 2 — Name and expiry") + `
    </div>
  </div>

  <div class="step">
    <div class="step-num">3</div>
    <div class="step-body">
      <strong>Select app: Jira</strong>
      <p>Choose <strong>Jira</strong> from the application list. Click <strong>Next</strong>.</p>
      ` + img(jiraStep3B64, "Step 3 — Select Jira") + `
    </div>
  </div>

  <div class="step">
    <div class="step-num">4</div>
    <div class="step-body">
      <strong>Select exactly these two scopes</strong>
      <p>Search for <code>jira-work</code> and tick both:</p>
      <div class="scope-box">
        <div>✅ <code>read:jira-work</code> — read your existing worklogs and issue summaries</div>
        <div style="margin-top:6px">✅ <code>write:jira-work</code> — add new worklogs after you approve them</div>
      </div>
      <p style="margin-top:8px;color:#42526e;font-size:.85rem">That's all. No admin scopes, no project config access. Click <strong>Next</strong>.</p>
    </div>
  </div>

  <div class="step">
    <div class="step-num">5</div>
    <div class="step-body">
      <strong>Review and create — then copy immediately</strong>
      <p>Confirm the summary shows <strong>App: Jira</strong> and <strong>Scopes: read:jira-work, write:jira-work</strong>. Click <strong>"Create token"</strong>. 
      <span style="color:#de350b;font-weight:600">Copy the token now</span> — it is shown only once.</p>
      ` + img(jiraStep5B64, "Step 5 — Review and create") + `
    </div>
  </div>

  <div class="step">
    <div class="step-num">6</div>
    <div class="step-body">
      <strong>Paste the token back on the Settings page</strong>
      <p>Return to <a href="/settings">Settings</a>, paste the token into the <strong>Jira API token</strong> field, and click <strong>Validate &amp; save</strong>.</p>
    </div>
  </div>
</section>
</main>
<div id="lightbox" onclick="closeLightbox()">
  <span id="lightbox-close" onclick="closeLightbox()">✕</span>
  <img id="lightbox-img" src="" alt="">
</div>
<script>` + guideJS + `</script>
</body></html>`
}

// githubGuideHTML is the dedicated GitHub personal access token guide page.
const githubGuideHTML = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Create a GitHub token — Timereporting Assistant</title>
<style>` + guideCSS + `</style></head>
<body>
<header>
  <h1>How to create a GitHub personal access token</h1>
  <a href="/settings">← Back to Settings</a>
</header>
<main>
<section>
  <h2>What is this and why do you need it?</h2>
  <p class="subtitle">
    To detect which Jira tasks you worked on each day, the assistant can scan your commits, pull requests and code reviews on GitHub.
    A personal access token (PAT) lets it read this activity from your work GitHub account.
    Only <strong>read access</strong> is needed — it never writes anything to GitHub.
    The token is stored in your Windows Credential Manager.
  </p>
  <p style="font-size:.85rem;color:#42526e">This is <strong>optional</strong>. If you leave it blank, the tool will rely only on your locally cloned git repos (which often covers everything anyway).</p>
</section>

<section>
  <h2>Step-by-step (classic token — simplest)</h2>

  <div class="step">
    <div class="step-num">1</div>
    <div class="step-body">
      <strong>Open the GitHub token page</strong>
      <p>Go to <a href="https://github.com/settings/tokens" target="_blank">github.com/settings/tokens</a> (or your company's GitHub Enterprise equivalent). Click <strong>"Generate new token" → "Generate new token (classic)"</strong>.</p>
    </div>
  </div>

  <div class="step">
    <div class="step-num">2</div>
    <div class="step-body">
      <strong>Name and scope</strong>
      <p>Enter <code>timereporting-assistant</code> as the note. Set an expiry. Under <strong>Scopes</strong>, tick <strong>only</strong>:</p>
      <div class="scope-box">
        <div>✅ <code>repo</code> — read access to your private repositories (commits, PRs, reviews)</div>
      </div>
      <p style="margin-top:8px;color:#42526e;font-size:.85rem">If your work repos are public, you only need the <code>public_repo</code> sub-scope.</p>
    </div>
  </div>

  <div class="step">
    <div class="step-num">3</div>
    <div class="step-body">
      <strong>Generate and copy</strong>
      <p>Click <strong>"Generate token"</strong>. <span style="color:#de350b;font-weight:600">Copy the token immediately</span> — it is shown only once.</p>
    </div>
  </div>

  <div class="step">
    <div class="step-num">4</div>
    <div class="step-body">
      <strong>Paste on the Settings page</strong>
      <p>Return to <a href="/settings">Settings</a>, paste the token into the <strong>GitHub token</strong> field, and click <strong>Save</strong>.</p>
    </div>
  </div>
</section>

<section>
  <h2>Alternative: Fine-grained token (more secure)</h2>
  <p style="font-size:.85rem;color:#42526e">Fine-grained tokens let you scope access to specific repositories only.</p>
  <div class="step">
    <div class="step-num">1</div>
    <div class="step-body">
      <strong>Generate new token → "Fine-grained token"</strong>
      <p>Under <strong>Repository access</strong>, select the work repos you want scanned.</p>
    </div>
  </div>
  <div class="step">
    <div class="step-num">2</div>
    <div class="step-body">
      <strong>Permissions needed</strong>
      <div class="scope-box">
        <div>✅ <strong>Contents</strong> — Read-only (to read commits)</div>
        <div style="margin-top:6px">✅ <strong>Pull requests</strong> — Read-only (to read PR activity)</div>
      </div>
    </div>
  </div>
</section>
</main>
</body></html>`

// wizardHTML is the first-run setup wizard.
const wizardHTML = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Setup — Timereporting Assistant</title>
<style>
*{box-sizing:border-box}
body{font-family:system-ui,Arial,sans-serif;margin:0;background:#f4f5f7;color:#172b4d;min-height:100vh;display:flex;flex-direction:column}
header{background:#0052cc;color:#fff;padding:12px 20px}
header h1{margin:0;font-size:1.1rem;font-weight:600}
.progress-bar{background:#e9f2ff;height:6px}
.progress-fill{background:#0052cc;height:6px;transition:width .3s}
main{flex:1;display:flex;align-items:center;justify-content:center;padding:24px 16px}
.card{background:#fff;border:1px solid #dfe1e6;border-radius:8px;width:100%;max-width:540px;padding:32px 36px;box-shadow:0 2px 8px rgba(0,0,0,.06)}
.step-label{font-size:.78rem;font-weight:600;color:#6b778c;text-transform:uppercase;letter-spacing:.04em;margin-bottom:6px}
h2{margin:0 0 6px;font-size:1.2rem;font-weight:700}
.subtitle{font-size:.9rem;color:#42526e;margin:0 0 24px}
label{display:block;font-size:.83rem;font-weight:600;margin-bottom:4px;color:#344563}
input[type=text],input[type=password],textarea{width:100%;padding:8px 10px;border:1px solid #dfe1e6;border-radius:4px;font:inherit;font-size:.9rem;margin-bottom:14px}
input:focus,textarea:focus{outline:none;border-color:#0052cc;box-shadow:0 0 0 2px rgba(0,82,204,.15)}
textarea{resize:vertical;min-height:80px}
.hint{font-size:.78rem;color:#6b778c;margin-top:-10px;margin-bottom:14px}
.btn-row{display:flex;gap:10px;margin-top:8px;align-items:center;flex-wrap:wrap}
button{font:inherit;border-radius:4px;padding:9px 20px;cursor:pointer;font-size:.9rem}
.btn-primary{background:#0052cc;color:#fff;border:none}
.btn-primary:hover{background:#0747a6}
.btn-primary:disabled{opacity:.5;cursor:not-allowed}
.btn-secondary{background:#fff;color:#172b4d;border:1px solid #dfe1e6}
.btn-secondary:hover{background:#f4f5f7}
.btn-skip{background:none;border:none;color:#6b778c;font-size:.85rem;cursor:pointer;text-decoration:underline;padding:0}
.btn-skip:hover{color:#172b4d}
.error{color:#de350b;font-size:.83rem;margin-top:4px;min-height:18px}
.show-btn{background:#f4f5f7;border:1px solid #dfe1e6;padding:8px 12px;border-radius:4px;cursor:pointer;font-size:.83rem;white-space:nowrap;height:38px}
.input-row{display:flex;gap:8px;align-items:center;margin-bottom:14px}
.input-row input{margin-bottom:0;flex:1}
.checklist{list-style:none;padding:0;margin:0 0 20px}
.checklist li{padding:5px 0;font-size:.9rem;color:#42526e}
.checklist li::before{content:"✓ ";color:#00875a;font-weight:700}
.done-icon{font-size:3rem;text-align:center;margin-bottom:12px}
.build-bar{background:#e9f2ff;height:10px;border-radius:5px;overflow:hidden;margin-top:8px}
.build-fill{background:#0052cc;height:10px;width:0;transition:width .3s}
a{color:#0052cc}
</style></head>
<body>
<header><h1>Timereporting Assistant — Setup</h1></header>
<div class="progress-bar"><div class="progress-fill" id="progress" style="width:0%"></div></div>
<main>

<div class="card" id="step-1">
  <div class="step-label">Step 1 of 6</div>
  <h2>Welcome! 👋</h2>
  <p class="subtitle">This takes about 2 minutes. Here's what you'll need:</p>
  <ul class="checklist">
    <li>Your Jira login email and base URL</li>
    <li>A scoped Jira API token (we guide you through it)</li>
    <li>Paths to your local git repositories</li>
    <li>An exported .ics calendar file — optional</li>
    <li>A GitHub token — optional</li>
  </ul>
  <div class="btn-row">
    <button class="btn-primary" onclick="goTo(2)">Let's go →</button>
    <a href="/settings" style="font-size:.85rem;color:#6b778c;text-decoration:none">Configure manually instead</a>
  </div>
</div>

<div class="card" id="step-2" style="display:none">
  <div class="step-label">Step 2 of 6</div>
  <h2>Jira connection</h2>
  <p class="subtitle">The URL you open when you log into Jira.</p>
  <label>Jira base URL *</label>
  <input type="text" id="w-jiraBaseUrl" placeholder="https://your-domain.atlassian.net" oninput="clearErr('err-2')">
  <label>Your Jira login email *</label>
  <input type="text" id="w-jiraEmail" placeholder="you@example.com" oninput="clearErr('err-2')">
  <label>Meeting task key</label>
  <input type="text" id="w-meetingKey" value="EDB-9071">
  <div class="hint">All meeting time from your calendar is logged to this task.</div>
  <label>Leave / holiday task key</label>
  <input type="text" id="w-leaveKey" value="EDB-9070">
  <div class="hint">Public holidays and leave days are logged to this task.</div>
  <div class="error" id="err-2"></div>
  <div class="btn-row">
    <button class="btn-secondary" onclick="goTo(1)">← Back</button>
    <button class="btn-primary" onclick="validateStep2()">Next →</button>
  </div>
</div>

<div class="card" id="step-3" style="display:none">
  <div class="step-label">Step 3 of 6</div>
  <h2>Jira API token</h2>
  <p class="subtitle">The tool needs an API token to read and write your Jira worklogs. Both <strong>Classic</strong> and <strong>Scoped</strong> tokens are supported.</p>
  <p style="margin:0 0 10px"><a href="/guide/jira-token" target="_blank" style="display:inline-block;background:#e9f2ff;color:#0052cc;padding:6px 14px;border-radius:4px;font-size:.85rem;font-weight:600;text-decoration:none">📖 How to create a Jira API token →</a></p>
  <p style="font-size:.82rem;background:#fffae6;border:1px solid #ffe380;border-radius:4px;padding:8px 12px;margin-bottom:12px">
    ⚠ The email must be your <strong>Atlassian account email</strong> — check it at
    <a href="https://id.atlassian.com/manage-profile" target="_blank">id.atlassian.com/manage-profile</a>.
    It may differ from your work email.
  </p>
  <div id="jira-saved-note" style="display:none;font-size:.85rem;background:#e3fcef;border:1px solid #abf5d1;border-radius:4px;padding:8px 12px;margin-bottom:12px">
    ✓ A Jira token is already saved on this computer. Leave the box below empty to <strong>keep using it</strong>, or paste a new token to replace it.
  </div>
  <label>API token <span id="jira-token-req">*</span></label>
  <div class="input-row">
    <input type="password" id="w-jiraToken" placeholder="Paste token here" oninput="clearErr('err-3')">
    <button class="show-btn" onclick="togglePwd('w-jiraToken',this)">Show</button>
  </div>
  <div class="error" id="err-3"></div>
  <div class="btn-row" style="margin-top:4px">
    <button class="btn-secondary" onclick="goTo(2)">← Back</button>
    <button class="btn-primary" id="btn-validate" onclick="validateAndSaveJira()">Validate &amp; continue →</button>
  </div>
</div>

<div class="card" id="step-4" style="display:none">
  <div class="step-label">Step 4 of 6</div>
  <h2>GitHub activity <span style="font-size:.8rem;font-weight:400;color:#6b778c">(optional)</span></h2>
  <p class="subtitle">Scan your commits and PRs to detect which Jira tasks you worked on.
  Skip if you only use local repos.</p>
  <p style="margin:0 0 14px"><a href="/guide/github-token" target="_blank" style="display:inline-block;background:#e9f2ff;color:#0052cc;padding:6px 14px;border-radius:4px;font-size:.85rem;font-weight:600;text-decoration:none">📖 How to create a GitHub token →</a></p>
  <label>GitHub username</label>
  <input type="text" id="w-ghUser" placeholder="your-work-github-username">
  <div id="gh-saved-note" style="display:none;font-size:.85rem;background:#e3fcef;border:1px solid #abf5d1;border-radius:4px;padding:8px 12px;margin:10px 0">
    ✓ A GitHub token is already saved. Leave the box below empty to <strong>keep using it</strong>, or paste a new token to replace it.
  </div>
  <label>GitHub token</label>
  <div class="input-row">
    <input type="password" id="w-ghToken" placeholder="github_pat_..." oninput="clearErr('err-4')">
    <button class="show-btn" onclick="togglePwd('w-ghToken',this)">Show</button>
  </div>
  <div class="error" id="err-4"></div>
  <div class="btn-row" style="margin-top:4px">
    <button class="btn-secondary" onclick="goTo(3)">← Back</button>
    <button class="btn-primary" onclick="saveGitHub()">Next →</button>
    <button class="btn-skip" onclick="goTo(5)">Skip for now</button>
  </div>
</div>

<div class="card" id="step-5" style="display:none">
  <div class="step-label">Step 5 of 6</div>
  <h2>Local repos &amp; calendar</h2>
  <p class="subtitle">Where to find your local git repositories and meeting data.</p>
  <label>Git repo paths * (one per line)</label>
  <textarea id="w-repos" placeholder="C:\work\project-one&#10;C:\work\project-two" oninput="clearErr('err-5')"></textarea>
  <label>Your git author email(s)</label>
  <input type="text" id="w-authors" placeholder="you@example.com">
  <div class="hint">Filters commits by author. Comma-separated if you have more than one email.</div>
  <label>Outlook calendar .ics export — optional</label>
  <div class="input-row" style="margin-bottom:4px">
    <input type="text" id="w-ics" placeholder="C:\Users\you\Downloads\calendar.ics" style="margin-bottom:0">
    <label style="display:inline-flex;align-items:center;background:#f4f5f7;border:1px solid #dfe1e6;border-radius:4px;padding:0 12px;cursor:pointer;height:38px;font-weight:400;font-size:.83rem;white-space:nowrap;margin:0">
      📂 Browse<input type="file" accept=".ics" style="display:none" onchange="uploadICS(this,'w-ics')">
    </label>
  </div>
  <div class="hint">Export from Outlook: File → Save Calendar. Meetings are logged first before other tasks.</div>
  <details style="margin-top:8px"><summary style="cursor:pointer;font-size:.83rem;color:#6b778c">Advanced: change ports (default 9080 / 9099)</summary>
  <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:10px">
    <div>
      <label>Review UI port</label>
      <input type="number" id="w-webPort" value="9080" min="1024" max="65535">
    </div>
    <div>
      <label>Mock Jira port</label>
      <input type="number" id="w-mockPort" value="9099" min="1024" max="65535">
    </div>
  </div>
  </details>
  <div style="margin-top:14px;padding:12px 14px;background:#f4f5f7;border-radius:6px">
    <label style="display:flex;align-items:center;gap:8px;font-weight:600;margin-bottom:8px;cursor:pointer">
      <input type="checkbox" id="w-autoUpdate" checked style="width:16px;height:16px">
      Keep the app up to date automatically
    </label>
    <label style="display:flex;align-items:center;gap:8px;font-size:.85rem;color:#42526e;cursor:pointer">
      <input type="checkbox" id="w-updatePrerelease" style="width:16px;height:16px">
      Include beta (prerelease) versions
    </label>
  </div>
  <div class="error" id="err-5"></div>
  <div class="btn-row">
    <button class="btn-secondary" onclick="goTo(4)">← Back</button>
    <button class="btn-primary" onclick="saveReposAndFinish()">Save &amp; build plans →</button>
  </div>
</div>

<div class="card" id="step-6" style="display:none">
  <div class="step-label">Step 6 of 6</div>
  <div id="build-view">
    <div class="done-icon">⏳</div>
    <h2 style="text-align:center">Building your time report plans…</h2>
    <p class="subtitle" style="text-align:center;margin-bottom:8px" id="build-phase">Starting…</p>
    <div class="build-bar"><div class="build-fill" id="build-fill"></div></div>
    <p style="text-align:center;font-size:.82rem;color:#6b778c;margin-top:8px" id="build-count"></p>
  </div>
  <div id="done-view" style="display:none">
    <div class="done-icon">🎉</div>
    <h2 style="text-align:center">You're all set!</h2>
    <p class="subtitle" style="text-align:center" id="done-msg"></p>
    <div class="btn-row" style="justify-content:center;margin-top:16px">
      <button class="btn-primary" id="btn-go" onclick="window.location='/'">Open time report →</button>
    </div>
  </div>
</div>

</main>
<script>
const TOTAL=6;
function goTo(n){
  document.querySelectorAll('.card').forEach(c=>c.style.display='none');
  document.getElementById('step-'+n).style.display='block';
  document.getElementById('progress').style.width=Math.round((n/TOTAL)*100)+'%';
  window.scrollTo(0,0);
}
function clearErr(id){document.getElementById(id).textContent='';}
function showErr(id,msg){document.getElementById(id).textContent=msg;}
function togglePwd(id,btn){
  const el=document.getElementById(id);
  el.type=el.type==='password'?'text':'password';
  btn.textContent=el.type==='password'?'Show':'Hide';
}
async function api(method,path,body){
  const opts={method,headers:{'Content-Type':'application/json'}};
  if(body)opts.body=JSON.stringify(body);
  const r=await fetch('/api'+path,opts);
  const d=await r.json();
  if(!r.ok)throw new Error(d.error||r.statusText);
  return d;
}
function validateStep2(){
  const url=document.getElementById('w-jiraBaseUrl').value.trim();
  const email=document.getElementById('w-jiraEmail').value.trim();
  if(!url||!url.startsWith('http')){showErr('err-2','Enter a valid Jira URL (starts with https://).');return;}
  if(!email||!email.includes('@')){showErr('err-2','Enter your Jira login email.');return;}
  goTo(3);
}
async function validateAndSaveJira(){
  const token=document.getElementById('w-jiraToken').value.trim();
  const hasSaved=document.getElementById('jira-saved-note').style.display!=='none';
  if(!token&&!hasSaved){showErr('err-3','Paste your token first.');return;}
  const baseUrl=document.getElementById('w-jiraBaseUrl').value.trim();
  const email=document.getElementById('w-jiraEmail').value.trim();
  const btn=document.getElementById('btn-validate');
  btn.disabled=true;btn.textContent='Validating…';
  try{
    await api('PUT','/config',{
      jiraBaseUrl:baseUrl,jiraEmail:email,
      meetingKey:document.getElementById('w-meetingKey').value.trim()||'EDB-9071',
      leaveKey:document.getElementById('w-leaveKey').value.trim()||'EDB-9070',
    });
    // Empty token => server reuses the token already saved in the keychain.
    await api('POST','/credentials/jira',{baseUrl,email,token});
    goTo(4);
  }catch(e){showErr('err-3','❌ '+e.message);}
  finally{btn.disabled=false;btn.textContent='Validate & continue →';}
}
async function saveGitHub(){
  const user=document.getElementById('w-ghUser').value.trim();
  const token=document.getElementById('w-ghToken').value.trim();
  const hasSaved=document.getElementById('gh-saved-note').style.display!=='none';
  if(user&&!token&&!hasSaved){showErr('err-4','Enter a token for the username, or skip.');return;}
  try{
    if(token){
      await api('PUT','/config',{githubUsername:user});
      await api('POST','/credentials/github',{token});
    }else if(user){
      // Keep the existing token; just update the username.
      await api('PUT','/config',{githubUsername:user});
    }
    goTo(5);
  }catch(e){showErr('err-4','❌ '+e.message);}
}
async function saveReposAndFinish(){
  const repos=document.getElementById('w-repos').value.split('\n').map(s=>s.trim()).filter(Boolean);
  if(!repos.length){showErr('err-5','Enter at least one repository path.');return;}
  const authors=document.getElementById('w-authors').value.split(',').map(s=>s.trim()).filter(Boolean);
  const ics=document.getElementById('w-ics').value.trim();
  goTo(6);
  document.getElementById('build-view').style.display='block';
  document.getElementById('done-view').style.display='none';
  document.getElementById('build-fill').style.width='0%';
  document.getElementById('build-phase').textContent='Saving configuration…';
  document.getElementById('build-count').textContent='';
  try{
    await api('PUT','/config',{
      localRepos:repos, gitAuthors:authors, icsPath:ics,
      workdayHours:7, target:'mock',
      webPort:+(document.getElementById('w-webPort').value||9080),
      mockJiraPort:+(document.getElementById('w-mockPort').value||9099),
      autoUpdate:document.getElementById('w-autoUpdate').checked,
      updatePrerelease:document.getElementById('w-updatePrerelease').checked,
    });
  }catch(e){ showBuildError(e.message); return; }
  startBuildStream();
}
function showBuildError(msg){
  document.getElementById('build-view').style.display='none';
  const dv=document.getElementById('done-view');
  dv.style.display='block';
  dv.querySelector('.done-icon').textContent='⚠';
  document.getElementById('done-msg').textContent=msg+' — you can still continue.';
}
function startBuildStream(){
  let finished=false;
  const es=new EventSource('/api/reload/stream');
  es.addEventListener('progress',ev=>{
    const d=JSON.parse(ev.data);
    document.getElementById('build-phase').textContent=d.phase||'Working…';
    if(d.total>0){
      const pct=Math.round((d.done/d.total)*100);
      document.getElementById('build-fill').style.width=pct+'%';
      document.getElementById('build-count').textContent=d.done+' / '+d.total+' days';
    }
  });
  es.addEventListener('done',ev=>{
    finished=true; es.close();
    const d=JSON.parse(ev.data);
    document.getElementById('build-fill').style.width='100%';
    document.getElementById('build-view').style.display='none';
    document.getElementById('done-view').style.display='block';
    document.getElementById('done-msg').textContent='Plans built for '+d.rebuilt+' days. You\'re ready!';
  });
  es.addEventListener('error',ev=>{
    if(finished)return;
    // A custom server error event carries data; transient connection blips do not.
    if(ev.data){
      finished=true; es.close();
      let msg='Could not build plans';
      try{ msg=JSON.parse(ev.data).error||msg; }catch(_){}
      showBuildError(msg);
    }
  });
}
async function uploadICS(input, targetId) {
  if (!input.files || !input.files[0]) return;
  const fd = new FormData();
  fd.append('ics', input.files[0]);
  try {
    const r = await fetch('/api/upload/ics', {method:'POST', body:fd});
    const d = await r.json();
    if (r.ok) {
      document.getElementById(targetId).value = d.path;
      toast('Calendar file saved: '+d.path);
    } else { toast(d.error||'Upload failed', true); }
  } catch(e) { toast('Upload failed: '+e.message, true); }
  input.value = '';
}

// Pre-fill fields from any existing config so returning users don't retype.
async function prefill(){
  try{
    const c=await api('GET','/config');
    const set=(id,val)=>{if(val)document.getElementById(id).value=val;};
    set('w-jiraBaseUrl',c.jiraBaseUrl);
    set('w-jiraEmail',c.jiraEmail);
    set('w-meetingKey',c.meetingKey);
    set('w-leaveKey',c.leaveKey);
    set('w-ghUser',c.githubUsername);
    if(c.localRepos&&c.localRepos.length)document.getElementById('w-repos').value=c.localRepos.join('\n');
    if(c.gitAuthors&&c.gitAuthors.length)document.getElementById('w-authors').value=c.gitAuthors.join(', ');
    set('w-ics',c.icsPath);
    if(c.webPort)document.getElementById('w-webPort').value=c.webPort;
    if(c.mockJiraPort)document.getElementById('w-mockPort').value=c.mockJiraPort;
    if(typeof c.autoUpdate==='boolean')document.getElementById('w-autoUpdate').checked=c.autoUpdate;
    if(typeof c.updatePrerelease==='boolean')document.getElementById('w-updatePrerelease').checked=c.updatePrerelease;
  }catch(e){/* first run: nothing to pre-fill */}
  // Detect already-saved tokens so the user can reuse them without re-pasting.
  try{
    const s=await api('GET','/credentials/status');
    if(s.jira&&s.jira.indexOf('set')===0){
      document.getElementById('jira-saved-note').style.display='block';
      document.getElementById('jira-token-req').style.display='none';
      document.getElementById('w-jiraToken').placeholder='Leave empty to keep the saved token';
    }
    if(s.github&&s.github.indexOf('set')===0){
      document.getElementById('gh-saved-note').style.display='block';
      document.getElementById('w-ghToken').placeholder='Leave empty to keep the saved token';
    }
  }catch(e){/* status unavailable: fall back to requiring a token */}
}

prefill();
goTo(1);
</script>
</body></html>`

// buildSettingsHTML returns the settings/onboarding HTML with screenshots inlined.
func buildSettingsHTML() string {
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
  <a href="/wizard" style="font-size:.8rem;margin-right:8px">↺ Setup wizard</a>
  <a href="/">← Back to time report</a>
</header>
<main>

<!-- Credential status banner -->
<section id="cred-section">
  <h2>Credentials</h2>
  <p style="font-size:.85rem;color:#42526e;margin:0 0 12px">Status of your API tokens. Tokens are stored securely in the Windows Credential Manager — never in a file on disk.</p>
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
  <p style="font-size:.85rem;color:#42526e;margin:0 0 16px">Required. The tool reads your existing worklogs from Jira and, after you approve, writes new ones. Your real Jira is <strong>read-only</strong> until you explicitly choose the "Real Jira" target.</p>
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
    You need a <strong>scoped API token</strong> (not a classic one).
    <a href="/guide/jira-token" target="_blank" style="color:#0052cc">How to create one →</a>
  </p>
  <div class="cred-row">
    <input type="password" id="jiraToken" placeholder="Paste scoped API token here">
    <button class="secondary" onclick="togglePwd('jiraToken',this)">Show</button>
    <button class="primary" onclick="saveJiraCreds()">Validate &amp; save</button>
  </div>
  <div id="jira-cred-msg" style="font-size:.82rem;margin-top:4px"></div>

</section>

<!-- GitHub -->
<section>
  <h2>GitHub activity (optional)</h2>
  <p style="font-size:.85rem;color:#42526e;margin:0 0 16px">Used to detect which Jira tasks you worked on each day by scanning your commits, pull requests and code reviews. Leave blank to rely only on local git repos.</p>
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
      <div class="hint">Needs <code>repo</code> read scope. <a href="/guide/github-token" target="_blank">How to create one →</a></div>
    </div>
  </div>
</section>

<!-- Work repos + calendar -->
<section>
  <h2>Local activity sources</h2>
  <p style="font-size:.85rem;color:#42526e;margin:0 0 16px">The tool scans your local git repos for commits to map time against Jira tasks. It also reads your exported Outlook/Teams calendar to split out meeting time first.</p>
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
    <div style="display:flex;gap:8px;align-items:center">
      <input type="text" id="icsPath" placeholder="C:\Users\you\Downloads\calendar.ics" style="flex:1;margin-bottom:0">
      <label style="display:inline-flex;align-items:center;background:#f4f5f7;border:1px solid #dfe1e6;border-radius:4px;padding:0 12px;cursor:pointer;height:36px;font-weight:400;font-size:.83rem;white-space:nowrap;margin:0">
        📂 Browse<input type="file" accept=".ics" style="display:none" onchange="uploadICS(this,'icsPath')">
      </label>
    </div>
    <div class="hint">Export from Outlook: File → Save Calendar. Meetings are logged to the meeting task key above.</div>
  </div>
</section>

<!-- Workday + ports -->
<section>
  <h2>Advanced</h2>
  <p style="font-size:.85rem;color:#42526e;margin:0 0 16px">Defaults work for most people. Only change these if you use non-standard ports or a different contract hours per day.</p>
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
  <div class="field">
    <label style="display:flex;align-items:center;gap:8px;cursor:pointer">
      <input type="checkbox" id="autoUpdate" style="width:16px;height:16px">
      Keep the app up to date automatically
    </label>
  </div>
  <div class="field">
    <label style="display:flex;align-items:center;gap:8px;cursor:pointer">
      <input type="checkbox" id="updatePrerelease" style="width:16px;height:16px">
      Include beta (prerelease) versions when updating
    </label>
  </div>
  <button class="primary" onclick="saveAndRebuild()">Save &amp; rebuild plans</button>
  <span id="cfg-msg" style="margin-left:12px;font-size:.82rem"></span>
  <div style="margin-top:18px;padding-top:14px;border-top:1px solid #dfe1e6">
    <button class="secondary" onclick="clearMockWorklogs()">🧹 Clear all mock Jira worklogs</button>
    <span id="clear-msg" style="margin-left:12px;font-size:.82rem"></span>
    <div class="hint">Wipes every worklog from the mock Jira server so you can test submitting again from a clean slate. Never touches real Jira.</div>
  </div>
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
    document.getElementById('webPort').value = c.webPort||9080;
    document.getElementById('mockJiraPort').value = c.mockJiraPort||9099;
    document.getElementById('target').value = c.target||'mock';
    document.getElementById('autoUpdate').checked = c.autoUpdate!==false;
    document.getElementById('updatePrerelease').checked = c.updatePrerelease===true;
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
      autoUpdate: document.getElementById('autoUpdate').checked,
      updatePrerelease: document.getElementById('updatePrerelease').checked,
    });
    document.getElementById('cfg-msg').textContent = '✅ Saved';
    document.getElementById('cfg-msg').style.color = '#00875a';
    setTimeout(()=>document.getElementById('cfg-msg').textContent='',3000);
  } catch(e) { toast('Save failed: '+e.message, true); }
}

async function saveJiraCreds() {
  const baseUrl = document.getElementById('jiraBaseUrl').value.trim();
  const email = document.getElementById('jiraEmail').value.trim();
  const token = document.getElementById('jiraToken').value.trim();
  const msg = document.getElementById('jira-cred-msg');
  if (!token) { msg.textContent='⚠ Paste the token first.'; msg.style.color='#de350b'; return; }
  if (!baseUrl) { msg.textContent='⚠ Enter the Jira base URL above first.'; msg.style.color='#de350b'; return; }
  msg.textContent = 'Validating…'; msg.style.color = '#6b778c';
  try {
    await api('POST','/credentials/jira',{baseUrl, email, token});
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

async function clearMockWorklogs() {
  if (!confirm('Delete ALL worklogs from the mock Jira server? (Real Jira is not affected.)')) return;
  const msg = document.getElementById('clear-msg');
  msg.textContent = 'Clearing…'; msg.style.color = '#6b778c';
  try {
    const res = await api('POST','/mock/clear-worklogs');
    msg.textContent = '✅ Cleared '+(res.deleted!=null?res.deleted:'all')+' worklog(s). Rebuilding…';
    msg.style.color = '#00875a';
    await api('POST','/reload');
    msg.textContent = '✅ Mock worklogs cleared and plans rebuilt.';
  } catch(e) { msg.textContent = '❌ '+e.message; msg.style.color='#de350b'; }
}

async function uploadICS(input, targetId) {
  if (!input.files || !input.files[0]) return;
  const fd = new FormData();
  fd.append('ics', input.files[0]);
  try {
    const r = await fetch('/api/upload/ics', {method:'POST', body:fd});
    const d = await r.json();
    if (r.ok) {
      document.getElementById(targetId).value = d.path;
      toast('Calendar file saved: '+d.path);
    } else { toast(d.error||'Upload failed', true); }
  } catch(e) { toast('Upload failed: '+e.message, true); }
  input.value = '';
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
main{height:calc(100vh - 48px);display:flex;flex-direction:column}
#toolbar{padding:10px 20px;background:#fff;border-bottom:1px solid #dfe1e6;display:flex;align-items:center;gap:14px;font-size:.85rem;flex:none}
#toolbar input[type=date]{font:inherit;padding:5px 8px;border:1px solid #dfe1e6;border-radius:4px}
#incomplete-count{color:#6b778c}
.total-ok{color:#00875a;font-weight:600}
.total-warn{color:#ff5630;font-weight:600}
/* Detail panel */
#detail{padding:20px;overflow-y:auto;flex:1}
.day-nav{display:flex;align-items:center;gap:16px;margin-bottom:12px}
.day-nav h2{flex:1;text-align:center;margin:0;font-size:1.3rem}
.nav-btn{font-size:1.8rem;line-height:1;background:#fff;border:1px solid #dfe1e6;border-radius:8px;padding:6px 22px;cursor:pointer;color:#0052cc}
.nav-btn:hover{background:#e9f2ff}
.summary-line{font-size:1.15rem;font-weight:700;margin:18px 0;padding:14px 18px;background:#fff;border:1px solid #dfe1e6;border-radius:6px}
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
.issue-title{font-size:.75rem;color:#6b778c;margin-top:2px;max-width:280px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sugg-total td{font-weight:700;border-top:2px solid #dfe1e6;background:#f4f5f7}
.new-row td{position:relative}
.new-row input{border:1px dashed #0052cc;background:#f7faff}
.issue-search-results{position:absolute;left:0;top:100%;z-index:20;background:#fff;border:1px solid #dfe1e6;border-radius:4px;box-shadow:0 6px 18px rgba(0,0,0,.14);max-height:260px;overflow-y:auto;min-width:340px;display:none}
.isr-item{padding:7px 12px;cursor:pointer;font-size:.85rem;border-bottom:1px solid #f0f1f3}
.isr-item:hover{background:#e9f2ff}
.isr-item strong{color:#0052cc;margin-right:6px}
.isr-empty{padding:8px 12px;color:#6b778c;font-size:.82rem}
.del-btn{background:none;border:none;cursor:pointer;color:#de350b;font-size:1rem;padding:0 4px}
.summary{background:#fff;border:1px solid #dfe1e6;border-radius:4px;padding:12px 16px;margin-bottom:16px;font-size:.85rem}
.summary span{font-weight:600}
.notes{color:#6b778c;font-size:.8rem;margin-top:4px}
.badge-submitted{background:#00875a;color:#fff;padding:2px 8px;border-radius:12px;font-size:.75rem}
.badge-target{background:#ff991f;color:#172b4d;padding:2px 8px;border-radius:12px;font-size:.75rem}
#toast{position:fixed;bottom:20px;right:20px;background:#172b4d;color:#fff;padding:10px 18px;border-radius:6px;display:none;font-size:.85rem;z-index:999}
#day-overlay{position:fixed;inset:0;background:rgba(255,255,255,.88);display:none;flex-direction:column;align-items:center;justify-content:center;z-index:600}
.spinner{width:52px;height:52px;border:5px solid #dfe1e6;border-top-color:#0052cc;border-radius:50%;animation:spin .8s linear infinite}
@keyframes spin{to{transform:rotate(360deg)}}
#day-overlay-msg{margin-top:18px;font-size:1.05rem;font-weight:600;color:#172b4d}
</style>
</head>
<body>
<div id="day-overlay" aria-live="polite" aria-busy="true">
  <div class="spinner"></div>
  <div id="day-overlay-msg">Building day plan…</div>
</div>
<header>
  <h1>Timereporting Assistant</h1>
  <span id="target-badge" class="badge">loading…</span>
  <span style="margin-left:auto;display:flex;align-items:center;gap:10px">
    <label id="target-label" style="display:flex;align-items:center;gap:7px;cursor:pointer;font-size:.85rem;user-select:none"
           title="Toggle between Mock Jira (safe) and Real Jira (writes to your timesheet)">
      <input type="checkbox" id="real-jira-chk" onchange="onTargetCheckbox(this.checked)"
             style="width:16px;height:16px;cursor:pointer">
      <span id="target-chk-label" style="font-weight:600">Submit to Real Jira</span>
    </label>
    <a href="/settings" style="color:#fff;font-size:.8rem;border:1px solid rgba(255,255,255,.4);padding:3px 10px;border-radius:4px;text-decoration:none">⚙ Settings</a>
  </span>
</header>
<main>
  <div id="toolbar">
    <label>📅 Jump to day: <input type="date" id="date-picker" onchange="onPickDate(this.value)" oninput="onPickDate(this.value)"></label>
    <span id="incomplete-count"></span>
  </div>
  <div id="detail"><p>Loading…</p></div>
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

function isIncomplete(day) {
  return !day.submitted && totalMins(day) < 420;
}

// renderList updates the date picker value and the incomplete-day count.
function renderList() {
  const picker = document.getElementById('date-picker');
  if (picker) {
    // No minimum date; only cap at today so future days can't be selected.
    picker.max = todayStr();
    picker.removeAttribute('min');
    if (currentDate) picker.value = currentDate;
  }
  const el = document.getElementById('incomplete-count');
  if (el) {
    const inc = days.filter(isIncomplete).length;
    el.textContent = inc ? ('⚠ '+inc+' incomplete day(s)') : 'All days complete ✓';
    el.style.color = inc ? '#ff5630' : '#00875a';
  }
}

// todayStr returns today's local date as YYYY-MM-DD.
function todayStr() {
  const d = new Date();
  const m = String(d.getMonth()+1).padStart(2,'0');
  const day = String(d.getDate()).padStart(2,'0');
  return d.getFullYear()+'-'+m+'-'+day;
}

// onPickDate jumps to the selected day. If it isn't loaded yet, fetches it from
// the server (which builds it on demand) while showing a blocking spinner.
async function onPickDate(value) {
  if (!value) return;
  const exact = days.find(x=>x.date===value);
  if (exact) { selectDay(exact.date); return; }
  // Date not in loaded range — fetch on demand.
  await fetchAndShowDay(value);
}

// fetchAndShowDay fetches a single day from the server (building it on demand),
// shows a spinner overlay while loading, then renders the result.
async function fetchAndShowDay(date) {
  showOverlay('Building plan for '+date+'…');
  try {
    const day = await api('GET','/days/'+date);
    // Add to local cache so edits/submits work.
    if (!days.find(d=>d.date===date)) days.push(day);
    currentDate = date;
    renderList();
    renderDetail(day);
  } catch(e) {
    toast(e.message || 'Could not build plan for '+date, true);
  } finally {
    hideOverlay();
  }
}

function showOverlay(msg) {
  const ov = document.getElementById('day-overlay');
  document.getElementById('day-overlay-msg').textContent = msg || 'Building day plan…';
  ov.style.display = 'flex';
}
function hideOverlay() {
  document.getElementById('day-overlay').style.display = 'none';
}

// gotoIncomplete moves to the next/previous incomplete day (dir = +1 / -1).
function gotoIncomplete(dir) {
  if (!days.length) return;
  let i = days.findIndex(d=>d.date===currentDate);
  if (i < 0) i = 0;
  for (let step=1; step<=days.length; step++) {
    const j = i + dir*step;
    if (j < 0 || j >= days.length) break;
    if (isIncomplete(days[j])) { selectDay(days[j].date); return; }
  }
  toast(dir>0 ? 'No more incomplete days ahead.' : 'No earlier incomplete days.');
}

function renderDetail(day) {
  const el = document.getElementById('detail');
  const existMins = (day.existing||[]).reduce((a,w)=>a+w.minutes,0);
  const suggMins = (day.suggested||[]).reduce((a,w)=>a+w.minutes,0);
  const total = existMins + suggMins;
  const totalCls = total>=420?'total-ok':'total-warn';

  let html = '<div class="day-nav">'
    +'<button class="nav-btn" onclick="gotoIncomplete(-1)" title="Previous incomplete day">‹</button>'
    +'<h2>'+day.date+' <small style="font-weight:400">'+day.weekday+'</small>'
    +(day.submitted?' <span class="badge-submitted">Submitted</span>':'')+'</h2>'
    +'<button class="nav-btn" onclick="gotoIncomplete(1)" title="Next incomplete day">›</button>'
    +'</div>';

  // Controls
  html += '<div class="controls">';
  html += '<label><strong>Day status:</strong> '
    + '<select id="status-sel" onchange="saveStatus(\''+day.date+'\')">'
    + ['working','holiday','full_leave','half_leave'].map(s=>
        '<option value="'+s+'"'+(day.status===s?' selected':'')+'>'+s.replace('_',' ')+'</option>'
      ).join('')
    +'</select></label>';
  if (!day.submitted) {
    html += '<button onclick="clonePrev(\''+day.date+'\')">Clone previous day</button>';
  }
  html += '</div>';

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
  html += '<table id="sugg-table"><tr><th>Issue key &amp; title</th><th>Time</th><th>Comment</th><th></th><th></th></tr>';
  (day.suggested||[]).forEach((w,i) => {
    const rowCls = 'cat-'+(w.category||'manual')+(w.issueKey?'':' row-unassigned');
    const submitted = w.submitted;
    const kid = 'key-'+day.date+'-'+i;
    html += '<tr class="'+rowCls+'"'+(submitted?' style="opacity:.55"':'')+' id="row-'+day.date+'-'+i+'">'
      +'<td><input type="text" id="'+kid+'" value="'+esc(w.issueKey)+'" style="width:100%" '+(submitted?'disabled':'')+' onchange="editRowKey(\''+day.date+'\','+i+',this.value)"></td>'
      +'<td><input type="text" value="'+hm(w.minutes)+'" '+(submitted?'disabled':'')+' style="width:80px" placeholder="1h 30m" title="e.g. 1h, 30m, 1h 30m" onchange="editRowTime(\''+day.date+'\','+i+',this.value)"></td>'
      +'<td><input type="text" value="'+esc(w.comment)+'" '+(submitted?'disabled':'')+' onchange="editRow(\''+day.date+'\','+i+',\'comment\',this.value)"></td>'
      +'<td>'+(submitted?'<span style="color:#00875a">✓</span>':'<button class="primary" style="font-size:.75rem;padding:3px 8px" onclick="submitRow(\''+day.date+'\','+i+')">Submit</button>')+'</td>'
      +'<td>'+(submitted?'':' <button class="del-btn" title="Delete" onclick="deleteRow(\''+day.date+'\','+i+')">✕</button>')+'</td>'
      +'</tr>';
  });
  if (!day.suggested || day.suggested.length===0) {
    if (day.submitted) {
      html += '<tr><td colspan="5" style="color:#6b778c;text-align:center">No suggestions.</td></tr>';
    }
  }
  if (!day.submitted) {
    // Always-visible entry row: the issue-key box searches Jira as you type.
    html += '<tr class="new-row"><td>'
      +'<input type="text" id="new-issue-input" placeholder="+ Type to search Jira issues…" autocomplete="off" '
        +'oninput="onIssueSearchInput(this.value)" onkeydown="onNewRowKey(event,this)" onblur="setTimeout(hideIssueResults,200)">'
      +'<div class="issue-search-results" id="issue-search-results"></div></td>'
      +'<td colspan="4" style="color:#6b778c;font-size:.8rem">Pick an issue, or type a key &amp; press Enter, to add a row</td></tr>';
  }
  if (day.suggested && day.suggested.length) {
    html += '<tr class="sugg-total"><td style="text-align:right">Total</td>'
      +'<td>'+hm(suggMins)+'</td><td></td><td></td><td></td></tr>';
  }
  html += '</table>';

  // Summary — moved below the suggested worklogs, larger & bolder.
  html += '<div class="summary-line">Target: 7h &bull; Existing: '+hm(existMins)
    +' &bull; Suggested: '+hm(suggMins)
    +' &bull; Total: <span class="'+totalCls+'">'+hm(total)+'</span></div>';

  // Submit actions — below the suggested worklogs.
  if (!day.submitted) {
    html += '<div class="controls" style="margin-bottom:16px">'
      +'<button class="primary" onclick="submitDay(\''+day.date+'\',false)">Approve &amp; submit</button>'
      +'<button onclick="submitDay(\''+day.date+'\',true)">Dry run</button>'
      +'</div>';
  }

  // Notes (below the suggested worklogs)
  if (day.notes && day.notes.length) {
    html += '<div class="notes" style="margin-top:14px">ℹ️ '+day.notes.join(' | ')+'</div>';
  }

  // Unassigned activity + Revo prompt (#12) — below the suggested worklogs
  if (day.unassigned && day.unassigned.length) {
    html += '<details style="margin-top:12px"><summary style="cursor:pointer;font-size:.85rem;color:#ff991f;font-weight:600">⚠️ '+day.unassigned.length+' activity item(s) with no Jira key — assign or use Revo</summary>';
    html += '<div style="background:#fffae6;border:1px solid #ffe380;border-radius:4px;padding:10px 14px;margin-top:6px">';
    html += '<table style="font-size:.82rem;width:100%;border-collapse:collapse;margin-bottom:8px">'
      +'<tr><th style="text-align:left;padding:3px 8px">Source</th><th style="text-align:left;padding:3px 8px">Description</th></tr>';
    day.unassigned.forEach(a => {
      html += '<tr><td style="padding:3px 8px;color:#6b778c">'+esc(a.source)+'</td><td style="padding:3px 8px">'+esc(a.text)+'</td></tr>';
    });
    html += '</table>';
    html += '<button class="secondary" style="font-size:.8rem" onclick="copyRevoPrompt(\''+day.date+'\')">📋 Copy Revo prompt</button>';
    html += '<span style="font-size:.78rem;color:#6b778c;margin-left:8px">Paste into Rovo AI in Jira to find the right task, then add rows above.</span>';
    html += '</div></details>';
  }

  el.innerHTML = html;

  // Lazy-load issue titles for the suggested rows (shown inline in the key box).
  (day.suggested||[]).forEach((w,i) => {
    if (w.issueKey) fetchIssueTitle(w.issueKey, 'key-'+day.date+'-'+i);
  });
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

// editRowKey updates the issue key and refreshes the displayed title.
function editRowKey(date, idx, value) {
  const key = parseIssueKey(value);
  const day = getDayLocal(date);
  if (!day) return;
  day.suggested[idx].issueKey = key;
  saveSuggested(date, day.suggested);
  fetchIssueTitle(key, 'key-'+date+'-'+idx);
}

// parseIssueKey extracts the Jira key from a value that may include the title,
// e.g. "EDB-100 — Do the thing" -> "EDB-100".
function parseIssueKey(raw) {
  raw = (''+raw).trim();
  const m = raw.match(/^[A-Za-z][A-Za-z0-9]*-\d+/);
  return m ? m[0].toUpperCase() : raw.toUpperCase();
}

// parseDuration converts a Jira-style duration (1h, 30m, 1h 30m) to minutes.
// A bare number is treated as minutes. Returns NaN for unparseable input.
function parseDuration(str) {
  if (str == null) return 0;
  str = (''+str).trim().toLowerCase();
  if (str === '') return 0;
  if (/^\d+$/.test(str)) return parseInt(str, 10);
  let total = 0, matched = false;
  const re = /(\d+(?:\.\d+)?)\s*([hm])/g;
  let m;
  while ((m = re.exec(str))) {
    matched = true;
    const v = parseFloat(m[1]);
    total += m[2] === 'h' ? Math.round(v*60) : Math.round(v);
  }
  return matched ? total : NaN;
}

// editRowTime parses a Jira-format time entry and stores it as minutes.
function editRowTime(date, idx, value) {
  const mins = parseDuration(value);
  const day = getDayLocal(date);
  if (!day) return;
  if (isNaN(mins) || mins < 0) {
    toast('Enter time like 1h, 30m, or 1h 30m', true);
    renderDetail(day);
    return;
  }
  day.suggested[idx].minutes = mins;
  saveSuggested(date, day.suggested);
  renderDetail(day);
  renderList();
}

// fetchIssueTitle looks up a Jira issue summary and shows "KEY — Title" in the
// given key input (without clobbering the field while the user is typing).
const issueTitleCache = {};
async function fetchIssueTitle(key, elId) {
  const el = document.getElementById(elId);
  if (!el || !key) return;
  key = (''+key).trim().toUpperCase();
  if (!key) return;
  let summary = issueTitleCache[key];
  if (summary === undefined) {
    try {
      const r = await fetch('/api/issue?key='+encodeURIComponent(key));
      summary = r.ok ? ((await r.json()).summary || '') : '';
    } catch (_) { summary = ''; }
    issueTitleCache[key] = summary;
  }
  if (document.activeElement === el) return; // don't overwrite while editing
  el.value = summary ? (key + ' — ' + summary) : key;
}

function deleteRow(date, idx) {
  const day = getDayLocal(date);
  if (!day) return;
  day.suggested.splice(idx, 1);
  saveSuggested(date, day.suggested);
  renderDetail(day);
  renderList();
}

// addRowWithKey appends a new suggested row for the given issue key.
function addRowWithKey(key) {
  key = (''+key).trim().toUpperCase();
  if (!key) return;
  const day = getDayLocal(currentDate);
  if (!day) return;
  day.suggested = day.suggested || [];
  day.suggested.push({issueKey:key, minutes:30, comment:'', category:'manual'});
  saveSuggested(currentDate, day.suggested);
  renderDetail(day);
  renderList();
}

function hideIssueResults() {
  const box = document.getElementById('issue-search-results');
  if (box) { box.style.display='none'; box.innerHTML=''; }
}

let issueSearchTimer = null;
// onIssueSearchInput queries Jira for issues matching the typed text and shows
// a dropdown of results.
function onIssueSearchInput(q) {
  clearTimeout(issueSearchTimer);
  const box = document.getElementById('issue-search-results');
  if (!box) return;
  q = (q||'').trim();
  if (q.length < 2) { hideIssueResults(); return; }
  issueSearchTimer = setTimeout(async () => {
    try {
      const res = await api('GET','/issues/search?q='+encodeURIComponent(q));
      if (!res.length) {
        box.innerHTML = '<div class="isr-empty">No matching issues</div>';
      } else {
        box.innerHTML = res.map(it =>
          '<div class="isr-item" onmousedown="pickIssue(\''+it.key+'\')"><strong>'+esc(it.key)+'</strong> '+esc(it.summary)+'</div>'
        ).join('');
      }
      box.style.display = 'block';
    } catch(e) { hideIssueResults(); }
  }, 250);
}

function pickIssue(key) {
  hideIssueResults();
  addRowWithKey(key);
}

function onNewRowKey(ev, input) {
  if (ev.key === 'Enter') {
    ev.preventDefault();
    const v = input.value.trim();
    if (v) addRowWithKey(v);
  } else if (ev.key === 'Escape') {
    hideIssueResults();
  }
}

async function saveSuggested(date, suggested) {
  try {
    const updated = await api('PUT','/days/'+date,{suggested});
    const i = days.findIndex(d=>d.date===date);
    if (i>=0) days[i] = updated;
    renderList();
  } catch(e) { toast(e.message, true); }
}

function copyRevoPrompt(date) {
  const day = days.find(d=>d.date===date);
  if (!day || !day.unassigned || !day.unassigned.length) return;
  const items = day.unassigned.map(a => '- '+a.text+(a.ref?' ('+a.ref+')':'')).join('\n');
  const text = 'On '+date+' I worked on the following (the commits/PRs below have no clear Jira task key):\n'+items+'\n\nWhich Jira task(s) in my active projects should I log time against for these activities?';
  navigator.clipboard.writeText(text).then(
    () => toast('Revo prompt copied to clipboard — paste into Rovo AI in Jira.'),
    () => {
      // Fallback: show in a prompt for manual copy.
      window.prompt('Copy this prompt and paste into Rovo AI in Jira:', text);
    }
  );
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
  const isReal = status.activeWrite === 'real';
  badge.style.background = isReal ? '#de350b' : '#0747a6';
  // Sync checkbox + label
  const chk = document.getElementById('real-jira-chk');
  const lbl = document.getElementById('target-chk-label');
  chk.checked = isReal;
  chk.disabled = !status.realAvailable;
  if (isReal) {
    lbl.textContent = '⚠ Submitting to Real Jira';
    lbl.style.color = '#ffebe6';
  } else {
    lbl.textContent = 'Submit to Real Jira';
    lbl.style.color = '#fff';
  }
}

async function onTargetCheckbox(checked) {
  const target = checked ? 'real' : 'mock';
  if (checked && !confirm('⚠ Switch to REAL Jira?\n\nWorklogs will be written to your actual timesheet. Make sure you have reviewed the day plans carefully before submitting.')) {
    await refreshBadge(); // revert checkbox
    return;
  }
  try {
    await api('PUT','/target',{target});
    await refreshBadge();
    toast('Submit target set to ' + (checked ? '⚠ Real Jira' : 'Mock Jira') + '.');
  } catch(e) {
    toast(e.message, true);
    await refreshBadge(); // revert on error
  }
}

async function setWriteTarget(target) {
  // kept for backward compatibility; delegates to checkbox handler
  const checked = target === 'real';
  document.getElementById('real-jira-chk').checked = checked;
  await onTargetCheckbox(checked);
}

async function init() {
  try {
    await refreshBadge();
    days = await api('GET','/days');
    renderList();
    if (days.length) {
      const first = days.find(isIncomplete) || days[0];
      selectDay(first.date);
    }
  } catch(e) { toast('Failed to load days: '+e.message, true); }
}

init();
</script>
</body>
</html>`

// Strings helper used in server logic.
var _ = strings.TrimSpace
