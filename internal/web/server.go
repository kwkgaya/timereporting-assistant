// Package web serves the local review/edit/approve UI for the timereporting
// assistant. It exposes a REST-like API the browser-side JS calls, and the
// Go binary itself is the HTTP server — no build tooling required.
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/jira"
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
	IssueKey string `json:"issueKey"`
	Minutes  int    `json:"minutes"`
	Comment  string `json:"comment"`
	Category string `json:"category"`
}

// Server holds the state for the web review session.
type Server struct {
	mu          sync.Mutex
	days        []DayView      // ordered by date
	dayIndex    map[string]int // date -> index
	jiraClient  *jira.Client   // write target
	readTarget  string         // display label: "mock" / "real Jira"
	writeTarget string         // display label: "mock" / "real Jira"
	port        int
}

// New creates a Server. client targets either the mock or real Jira depending
// on target ("mock"/"mock-write"/"real"). Days is the ordered list of day plans to review.
func New(plans []model.DayPlan, client *jira.Client, target string, port int) *Server {
	read, write := targetLabels(target)
	s := &Server{
		dayIndex:    map[string]int{},
		jiraClient:  client,
		readTarget:  read,
		writeTarget: write,
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

// Handler returns the HTTP handler for the review UI.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.apiStatus)
	mux.HandleFunc("GET /api/days", s.apiGetDays)
	mux.HandleFunc("GET /api/days/{date}", s.apiGetDay)
	mux.HandleFunc("PUT /api/days/{date}", s.apiPutDay)
	mux.HandleFunc("POST /api/days/{date}/submit", s.apiSubmitDay)
	mux.HandleFunc("POST /api/days/{date}/clone-previous", s.apiClonePrevious)
	mux.HandleFunc("GET /", s.handleIndex)
	return mux
}

func (s *Server) apiStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	r, wt := s.readTarget, s.writeTarget
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"read": r, "write": wt})
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

	var submitted []WlogView
	for _, wl := range d.Suggested {
		if wl.IssueKey == "" || wl.Minutes <= 0 {
			continue
		}
		if !body.DryRun {
			if _, err := s.jiraClient.AddWorklog(wl.IssueKey, wl.Minutes, started, wl.Comment); err != nil {
				writeErr(w, http.StatusInternalServerError,
					fmt.Sprintf("submit %s: %v", wl.IssueKey, err))
				return
			}
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
		"target":    s.writeTarget,
	})
}

// apiClonePrevious copies the previous business day's suggested worklogs onto this day.
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

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

// planToView converts a model.DayPlan to a DayView.
func planToView(p model.DayPlan) DayView {
	toViews := func(wls []model.Worklog) []WlogView {
		out := make([]WlogView, 0, len(wls))
		for _, w := range wls {
			out = append(out, WlogView{
				IssueKey: w.IssueKey,
				Minutes:  w.Minutes,
				Comment:  w.Comment,
				Category: string(w.Category),
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
    html += '<table><tr><th>Issue</th><th>Time</th><th>Comment</th></tr>';
    day.existing.forEach(w => {
      html += '<tr class="cat-existing"><td>'+w.issueKey+'</td><td>'+hm(w.minutes)+'</td><td>'+esc(w.comment)+'</td></tr>';
    });
    html += '</table>';
  }

  // Suggested worklogs (editable)
  html += '<strong>Suggested worklogs</strong>';
  html += '<table id="sugg-table"><tr><th>Issue key</th><th>Time (min)</th><th>Comment</th><th></th></tr>';
  (day.suggested||[]).forEach((w,i) => {
    const rowCls = 'cat-'+(w.category||'manual')+(w.issueKey?'':' row-unassigned');
    html += '<tr class="'+rowCls+'">'
      +'<td><input type="text" value="'+esc(w.issueKey)+'" onchange="editRow(\''+day.date+'\','+i+',\'issueKey\',this.value)"></td>'
      +'<td><input type="number" min="30" step="30" value="'+w.minutes+'" onchange="editRow(\''+day.date+'\','+i+',\'minutes\',+this.value)"></td>'
      +'<td><input type="text" value="'+esc(w.comment)+'" onchange="editRow(\''+day.date+'\','+i+',\'comment\',this.value)"></td>'
      +'<td><button class="del-btn" title="Delete" onclick="deleteRow(\''+day.date+'\','+i+')">✕</button></td>'
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

async function init() {
  try {
    const status = await api('GET','/status');
    const badge = document.getElementById('target-badge');
    if (status.read === status.write) {
      badge.textContent = '→ ' + status.write;
    } else {
      badge.textContent = 'Read: ' + status.read + ' | Write: ' + status.write;
    }
    if (status.write === 'Real Jira') badge.style.background = '#de350b';
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
