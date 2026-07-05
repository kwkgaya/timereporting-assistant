# Architecture

> **For decision makers:**
> The whole thing is one small Go program (plus a companion "mock Jira" program for safe testing).
> It reads your activity, builds a suggested timesheet, and shows it in a local web page for
> approval. It has no cloud component and stores nothing sensitive on disk.

## Detail (for AI agents / implementers)

### Module path
`github.com/kwkgaya/timereporting-assistant` (Go, stdlib-first).

### Directory layout
```
cmd/
  timeporting/     Main binary: config+credential setup, build day plans, serve review UI
  mockjira/        Standalone mock Jira server (SQLite-backed)
internal/
  config/          Config loader: JSON file + secrets from keychain/env; target modes
  model/           Domain types (Activity, Meeting, Worklog, DayPlan) + date helpers
  adf/             Atlassian Document Format encode/decode (Jira v3 comments)
  keychain/        OS credential storage (Windows Credential Manager; env fallback elsewhere)
  setup/           Interactive first-run config wizard + guided Jira credential flow
  jira/            Jira Cloud REST v3 client (read + write); same client points at mock or real
  mockjira/        SQLite-backed mock Jira: REST subset + inspect UI (search + week view)
  ics/             iCalendar (.ics) parser for meetings (excludes declined)
  activity/        Activity collectors: GitHub REST API + local git (git log)
  jirakey/         Jira-key extraction (regex) + grouping with weights
  engine/          Timesheet engine: allocation, rounding, leave, top-up, clone
  web/             Local review/edit/approve UI + JSON API
docs/              This documentation
```

### Data flow (per run)
```
[real Jira read] ─┐
[ICS file]        ├─► engine.BuildDayPlan(per day) ─► []model.DayPlan ─► web.Server
[GitHub API]      │                                                        │
[local git]       ┘                                              user reviews/edits/approves
                                                                          │
                                                                 jira.Client.AddWorklog
                                                                          │
                                                         ┌────────────────┴────────────────┐
                                                    (mock target)                    (real target)
                                                    mock Jira :8099                  real Jira
```

### Key design choices
- **Single Jira client type** used for both mock and real; the difference is only the base URL
  and whether Basic-auth credentials are attached. This is what makes the mock-first testing and
  the split `mock-write` mode trivial.
- **Target modes** (`internal/config`):
  - `mock` — read and write to the mock (offline, no credentials).
  - `mock-write` — **read from real Jira, write to mock** (recommended for realistic, safe testing).
  - `real` — read and write to real Jira (requires explicit opt-in).
- **The engine is pure** (no I/O): it takes existing worklogs + meeting minutes + activities and
  returns a `DayPlan`. This makes it thoroughly unit-testable and is where all allocation rules live.
- **Worklog idempotency marker:** comments the tool writes include the tag `[timereporting]` so
  re-runs can detect and avoid duplicate logs.
- **Web UI is embedded** as an HTML/JS string served by the Go binary — no separate build step,
  single binary, easy for unattended agents to build and test.

### Dependencies (deliberately few)
- `modernc.org/sqlite` — pure-Go SQLite (mock Jira store; no CGO).
- `github.com/danieljoos/wincred` — Windows Credential Manager (build-tagged `windows` only).
- `golang.org/x/term` — hidden password input for the credential setup flow.

### Testing
- Every logic package has unit tests. HTTP clients are tested against `httptest`/the mock.
- CI (`.github/workflows/ci.yml`) runs `go build`, `go vet`, `go test` on push/PR.
- The engine tests assert the invariant that every day's suggestions sum to the target and are
  multiples of 30 minutes.
