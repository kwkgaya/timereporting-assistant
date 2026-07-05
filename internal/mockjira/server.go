// Package mockjira implements a SQLite-backed Jira-compatible server used to
// test the worklog write flow safely, without touching a real Jira instance.
// The database is created fresh each time New/NewDefault is called (in-memory
// or file). It implements just enough of the REST v3 surface the client needs,
// plus a richer inspect UI with issue search and week-view timelog summary.
package mockjira

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/adf"
	_ "modernc.org/sqlite" // pure-Go SQLite driver
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

// Server is a SQLite-backed mock Jira.
type Server struct {
	db         *sql.DB
	authorMail string
	authorName string
}

func newDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`
		CREATE TABLE issues (
			key     TEXT PRIMARY KEY,
			summary TEXT NOT NULL
		);
		CREATE TABLE worklogs (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_key TEXT NOT NULL REFERENCES issues(key),
			author    TEXT NOT NULL,
			seconds   INTEGER NOT NULL CHECK(seconds > 0),
			started   TEXT NOT NULL,
			comment   TEXT
		);
		CREATE INDEX idx_worklogs_started ON worklogs(substr(started,1,10));
	`)
	return db, err
}

// New creates an empty mock server with a fresh SQLite database.
func New() *Server {
	db, err := newDB()
	if err != nil {
		panic("mockjira: create db: " + err.Error())
	}
	return &Server{db: db, authorMail: "mock.user@example.com", authorName: "Mock User"}
}

// NewDefault creates a mock server seeded with the meeting task, the leave
// task, and a few sample development issues.
func NewDefault() *Server {
	s := New()
	s.Seed("EDB-9070", "Absence / Leave / Public holiday")
	s.Seed("EDB-9071", "Internal meetings")
	s.Seed("EDB-100", "Implement inventory widget")
	s.Seed("EDB-200", "Fix login redirect bug")
	s.Seed("EDB-300", "Refactor worklog API")
	return s
}

// Seed adds (or replaces) an issue.
func (s *Server) Seed(key, summary string) {
	_, err := s.db.Exec(
		`INSERT INTO issues(key,summary) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET summary=excluded.summary`,
		key, summary,
	)
	if err != nil {
		panic("mockjira: seed: " + err.Error())
	}
}

// WorklogCount returns the number of worklogs recorded against key.
func (s *Server) WorklogCount(key string) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM worklogs WHERE issue_key=?`, key).Scan(&n)
	return n
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/issue/{key}/worklog", s.handleListWorklog)
	mux.HandleFunc("POST /rest/api/3/issue/{key}/worklog", s.handleAddWorklog)
	mux.HandleFunc("GET /rest/api/3/issue/{key}", s.handleGetIssue)
	mux.HandleFunc("GET /rest/api/3/search", s.handleSearch)
	mux.HandleFunc("GET /", s.handleIndex)
	return mux
}

// ── REST API ─────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"errorMessages": []string{msg}, "errors": map[string]any{}})
}

func (s *Server) issueExists(key string) bool {
	var k string
	return s.db.QueryRow(`SELECT key FROM issues WHERE key=?`, key).Scan(&k) == nil
}

func (s *Server) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var summary string
	if err := s.db.QueryRow(`SELECT summary FROM issues WHERE key=?`, key).Scan(&summary); err != nil {
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
	if !s.issueExists(key) {
		writeErr(w, http.StatusNotFound, "Issue does not exist: "+key)
		return
	}
	rows, err := s.db.Query(
		`SELECT id,author,seconds,started,comment FROM worklogs WHERE issue_key=? ORDER BY id`,
		key,
	)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	var logs []Worklog
	for rows.Next() {
		var wl Worklog
		var comment sql.NullString
		var id int64
		if err := rows.Scan(&id, &wl.Author.EmailAddress, &wl.TimeSpentSeconds, &wl.Started, &comment); err != nil {
			continue
		}
		wl.ID = fmt.Sprintf("%d", id)
		wl.Author.DisplayName = wl.Author.EmailAddress
		if comment.Valid {
			wl.Comment = json.RawMessage(comment.String)
		}
		logs = append(logs, wl)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"startAt": 0, "maxResults": len(logs), "total": len(logs), "worklogs": logs,
	})
}

func (s *Server) handleAddWorklog(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !s.issueExists(key) {
		writeErr(w, http.StatusNotFound, "Issue does not exist: "+key)
		return
	}
	var body struct {
		TimeSpentSeconds int             `json:"timeSpentSeconds"`
		Started          string          `json:"started"`
		Comment          json.RawMessage `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.TimeSpentSeconds <= 0 {
		writeErr(w, http.StatusBadRequest, "timeSpentSeconds must be > 0")
		return
	}
	var commentStr sql.NullString
	if len(body.Comment) > 0 && string(body.Comment) != "null" {
		commentStr = sql.NullString{String: string(body.Comment), Valid: true}
	}
	res, err := s.db.Exec(
		`INSERT INTO worklogs(issue_key,author,seconds,started,comment) VALUES(?,?,?,?,?)`,
		key, s.authorMail, body.TimeSpentSeconds, body.Started, commentStr,
	)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, Worklog{
		ID:               fmt.Sprintf("%d", id),
		Author:           Author{EmailAddress: s.authorMail, DisplayName: s.authorName},
		TimeSpentSeconds: body.TimeSpentSeconds,
		Started:          body.Started,
		Comment:          body.Comment,
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT key,summary FROM issues ORDER BY key`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	var issues []map[string]any
	for rows.Next() {
		var key, summary string
		if err := rows.Scan(&key, &summary); err != nil {
			continue
		}
		issues = append(issues, map[string]any{
			"key":    key,
			"fields": map[string]any{"summary": summary},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"startAt": 0, "maxResults": len(issues), "total": len(issues), "issues": issues,
	})
}

// ── Inspect UI ───────────────────────────────────────────────────────────────

// weekStart returns the Monday 00:00 UTC of the week containing t.
func weekStart(t time.Time) time.Time {
	u := t.UTC()
	d := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	wd := int(d.Weekday())
	if wd == 0 {
		wd = 7
	}
	return d.AddDate(0, 0, 1-wd)
}

func hm(mins int) string {
	if mins <= 0 {
		return ""
	}
	h, m := mins/60, mins%60
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

type issueRow struct{ Key, Summary string }
type dayCell struct{ Minutes int }

type weekData struct {
	WeekOf    string
	PrevURL   string
	NextURL   string
	Days      []string // Mon–Fri YYYY-MM-DD
	Rows      []issueRow
	Cells     map[string]map[string]dayCell // key -> date -> cell
	DayTotals map[string]int                // date -> minutes
}

type searchResult struct {
	Query   string
	Results []searchHit
}
type searchHit struct {
	Key, Summary string
	Worklogs     int
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	searchQ := strings.TrimSpace(q.Get("q"))
	weekParam := q.Get("week")

	var ws time.Time
	if weekParam != "" {
		if t, err := time.Parse("2006-01-02", weekParam); err == nil {
			ws = weekStart(t)
		}
	}
	if ws.IsZero() {
		ws = weekStart(time.Now())
	}

	var srch *searchResult
	if searchQ != "" {
		srch = s.doSearch(searchQ)
	}
	wd := s.buildWeekData(ws)

	data := struct {
		Search  *searchResult
		Week    weekData
		SearchQ string
	}{srch, wd, searchQ}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *Server) doSearch(q string) *searchResult {
	sr := &searchResult{Query: q}
	like := "%" + strings.ToLower(q) + "%"
	rows, err := s.db.Query(`
		SELECT i.key, i.summary, COUNT(w.id)
		FROM issues i LEFT JOIN worklogs w ON w.issue_key=i.key
		WHERE LOWER(i.key) LIKE ? OR LOWER(i.summary) LIKE ?
		GROUP BY i.key ORDER BY i.key`,
		like, like,
	)
	if err != nil {
		return sr
	}
	defer rows.Close()
	for rows.Next() {
		var h searchHit
		if err := rows.Scan(&h.Key, &h.Summary, &h.Worklogs); err == nil {
			sr.Results = append(sr.Results, h)
		}
	}
	return sr
}

func (s *Server) buildWeekData(ws time.Time) weekData {
	we := ws.AddDate(0, 0, 6)
	days := make([]string, 5)
	for i := range days {
		days[i] = ws.AddDate(0, 0, i).Format("2006-01-02")
	}

	rows, err := s.db.Query(`
		SELECT w.issue_key, i.summary, w.seconds, substr(w.started,1,10) AS day
		FROM worklogs w JOIN issues i ON i.key=w.issue_key
		WHERE substr(w.started,1,10) >= ? AND substr(w.started,1,10) <= ?
		ORDER BY w.issue_key, day`,
		ws.Format("2006-01-02"), we.Format("2006-01-02"),
	)

	type accum struct {
		Summary string
		Order   int
		DaySecs map[string]int
	}
	issues := map[string]*accum{}
	order := 0
	dayTotalSecs := map[string]int{}

	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var key, summary, day string
			var secs int
			if err := rows.Scan(&key, &summary, &secs, &day); err != nil {
				continue
			}
			if _, ok := issues[key]; !ok {
				issues[key] = &accum{Summary: summary, Order: order, DaySecs: map[string]int{}}
				order++
			}
			issues[key].DaySecs[day] += secs
			dayTotalSecs[day] += secs
		}
	}

	// Sort by order of first appearance.
	issueRows := make([]issueRow, 0, len(issues))
	keys := make([]string, 0, len(issues))
	for k := range issues {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys)-1; i++ {
		for j := i + 1; j < len(keys); j++ {
			if issues[keys[j]].Order < issues[keys[i]].Order {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, k := range keys {
		issueRows = append(issueRows, issueRow{k, issues[k].Summary})
	}

	cells := map[string]map[string]dayCell{}
	for _, ir := range issueRows {
		cells[ir.Key] = map[string]dayCell{}
		for _, day := range days {
			cells[ir.Key][day] = dayCell{Minutes: issues[ir.Key].DaySecs[day] / 60}
		}
	}
	dayTotals := map[string]int{}
	for day, secs := range dayTotalSecs {
		dayTotals[day] = secs / 60
	}

	return weekData{
		WeekOf:    ws.Format("2006-01-02"),
		PrevURL:   "/?week=" + ws.AddDate(0, 0, -7).Format("2006-01-02"),
		NextURL:   "/?week=" + ws.AddDate(0, 0, 7).Format("2006-01-02"),
		Days:      days,
		Rows:      issueRows,
		Cells:     cells,
		DayTotals: dayTotals,
	}
}

var _ = adf.Text // imported for use in templates if needed

var indexTmpl = template.Must(template.New("index").Funcs(template.FuncMap{"hm": hm}).Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Mock Jira</title>
<style>
*{box-sizing:border-box}
body{font-family:system-ui,Arial,sans-serif;margin:0;background:#f4f5f7;color:#172b4d}
header{background:#0052cc;color:#fff;padding:10px 20px;display:flex;align-items:center;gap:16px}
header h1{margin:0;font-size:1rem;font-weight:700}
.pill{background:#0747a6;font-size:.75rem;padding:2px 10px;border-radius:12px}
main{max-width:1100px;margin:24px auto;padding:0 16px}
section{background:#fff;border:1px solid #dfe1e6;border-radius:4px;padding:16px 20px;margin-bottom:24px}
h2{font-size:.95rem;font-weight:700;margin:0 0 12px;border-bottom:1px solid #dfe1e6;padding-bottom:8px}
.search-form{display:flex;gap:8px;margin-bottom:12px}
.search-form input{flex:1;padding:6px 10px;border:1px solid #dfe1e6;border-radius:4px;font:inherit}
.search-form button{padding:6px 14px;background:#0052cc;color:#fff;border:none;border-radius:4px;cursor:pointer;font:inherit}
.search-form button:hover{background:#0747a6}
table{width:100%;border-collapse:collapse;font-size:.83rem}
th,td{border:1px solid #dfe1e6;padding:6px 10px;text-align:left}
th{background:#f4f5f7;font-weight:600}
tr:hover td{background:#fafafa}
.key{font-weight:700;font-family:monospace}
.muted{color:#6b778c;font-size:.8rem}
.no-result{color:#6b778c;font-size:.85rem;padding:8px 0}
.week-nav{display:flex;align-items:center;gap:12px;margin-bottom:12px}
.week-nav a{text-decoration:none;background:#f4f5f7;border:1px solid #dfe1e6;border-radius:4px;padding:4px 12px;font-size:.85rem;color:#172b4d}
.week-nav a:hover{background:#e9f2ff}
th.day{text-align:center;min-width:72px}
td.day{text-align:center;color:#172b4d}
td.zero{color:#dfe1e6;text-align:center}
tr.total-row td{background:#e9f2ff;font-weight:700;text-align:center}
tr.total-row td:first-child,tr.total-row td:nth-child(2){text-align:left}
.empty-week{color:#6b778c;font-size:.85rem;padding:12px 0;text-align:center}
td.summary{max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:#6b778c;font-size:.78rem}
</style>
</head><body>
<header>
  <h1>Mock Jira</h1>
  <span class="pill">DEV ONLY — SQLite</span>
</header>
<main>

<section>
  <h2>Search issues</h2>
  <form class="search-form" method="GET" action="/">
    <input name="q" value="{{.SearchQ}}" placeholder='Search by issue key (e.g. "EDB-100") or title keyword'>
    {{if .Week.WeekOf}}<input type="hidden" name="week" value="{{.Week.WeekOf}}">{{end}}
    <button type="submit">Search</button>
  </form>
  {{if .Search}}
    {{if .Search.Results}}
    <table>
      <thead><tr><th>Key</th><th>Summary</th><th>Worklogs</th></tr></thead>
      <tbody>
      {{range .Search.Results}}
      <tr>
        <td><span class="key">{{.Key}}</span></td>
        <td>{{.Summary}}</td>
        <td>{{.Worklogs}}</td>
      </tr>
      {{end}}
      </tbody>
    </table>
    {{else}}
    <p class="no-result">No issues found for <strong>{{.Search.Query}}</strong>.</p>
    {{end}}
  {{end}}
</section>

<section>
  <h2>Weekly timelog view</h2>
  <div class="week-nav">
    <a href="{{.Week.PrevURL}}">&#8592; Prev week</a>
    <strong>Week of {{.Week.WeekOf}}</strong>
    <a href="{{.Week.NextURL}}">Next week &#8594;</a>
  </div>
  {{if .Week.Rows}}
  <table>
    <thead>
      <tr>
        <th>Issue</th><th>Summary</th>
        {{range .Week.Days}}<th class="day">{{slice . 5}}</th>{{end}}
      </tr>
    </thead>
    <tbody>
    {{range .Week.Rows}}
    {{$key := .Key}}
    <tr>
      <td class="key">{{.Key}}</td>
      <td class="summary" title="{{.Summary}}">{{.Summary}}</td>
      {{range $.Week.Days}}
        {{$c := index (index $.Week.Cells $key) .}}
        {{if $c.Minutes}}<td class="day">{{hm $c.Minutes}}</td>{{else}}<td class="zero">–</td>{{end}}
      {{end}}
    </tr>
    {{end}}
    <tr class="total-row">
      <td colspan="2">Day total</td>
      {{range .Week.Days}}
        {{$t := index $.Week.DayTotals .}}
        {{if $t}}<td>{{hm $t}}</td>{{else}}<td class="zero">–</td>{{end}}
      {{end}}
    </tr>
    </tbody>
  </table>
  {{else}}
  <p class="empty-week">No worklogs found for this week. Submit some from the <a href="http://localhost:8080">review UI</a>.</p>
  {{end}}
</section>

</main>
</body></html>`))
