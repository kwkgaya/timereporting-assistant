# Build & Run

> **For decision makers:**
> It's a single Go program. Start the mock Jira, run the assistant, review the suggested timesheet
> in your browser, and approve. First run walks you through setup interactively.

## Detail (for AI agents / implementers)

### Prerequisites
- Go 1.26+
- git (for local-repo activity scanning)
- A Jira scoped API token (for `mock-write`/`real` modes) — see [jira-integration.md](jira-integration.md)

### Build & test
```powershell
go build ./...
go vet ./...
go test ./...
```

### Run — safe offline mode (no credentials)
```powershell
# Terminal 1 — mock Jira (inspect page at http://localhost:9099)
go run ./cmd/mockjira

# Terminal 2 — assistant (review UI at http://localhost:9080)
go run ./cmd/timeporting --from 2026-06-01 --to 2026-06-30 --target mock
```

### Run — realistic safe mode (reads real Jira, writes to mock)
```powershell
go run ./cmd/mockjira
go run ./cmd/timeporting --from 2026-06-01 --to 2026-06-30 --target mock-write
```
First run triggers the **config wizard**, then the **credential setup flow** if no token is stored.

### Sub-commands
- `timeporting configure` — (re)run the interactive config wizard, writes `config.json`.
- `timeporting credentials` — (re)run the Jira scoped-token setup and store in the keychain.
- `timeporting [--from --to --target --config]` — main review/submit flow.

### Configuration
- Non-secret settings live in `config.json` (gitignored). Template: `config.example.json`.
- Secrets come from the OS keychain first, then env vars:
  - `JIRA_EMAIL`, `JIRA_API_TOKEN`
  - `GITHUB_TOKEN`
- Key config fields: `jira.baseUrl`, `jira.email`, `meetingIssueKey` (EDB-9071),
  `leaveIssueKey` (EDB-9070), `workdayHours` (7), `localRepos[]`, `gitAuthors[]`, `icsPath`,
  `github.username`, `mockJiraPort` (9099), `webPort` (9080), `target`.

### Using the review UI (http://localhost:9080)
- Left panel: all days, with total-vs-7h indicator (green = met).
- Per day: edit issue key / minutes / comment per row, add/delete rows, assign unassigned items,
  set day status (working/holiday/full leave/half leave), **Clone previous day**.
- **Dry run** shows what would be submitted; **Approve & submit** writes to the current target.
- Header badge shows the read/write target split (e.g. `Read: Real Jira | Write: Mock Jira`).

### Getting your ICS calendar file
Export your Outlook/Teams calendar to `.ics` and point `icsPath` at it. The parser excludes
meetings you declined; all other events (incl. all-day) are counted per the agreed rules.
