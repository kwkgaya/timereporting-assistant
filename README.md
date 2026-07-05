# timereporting-assistant

Backfill and auto-generate your **Jira Cloud** worklogs from your **GitHub activity** and
**calendar meetings** (from an exported `.ics` file), review them in a small local web UI, and
submit them — first against a **mock Jira** so nothing touches your real timesheet until you
explicitly approve.

> Status: MVP in progress. Goal: backfill **June 2026** worklogs (weekdays), 7h/day.

## How it works

For each weekday in the target range the tool builds a **day plan** targeting **7 hours**:

1. **Existing worklogs** already in Jira are read (read-only) and counted toward the 7h (top-up).
2. **Meetings** from your `.ics` (excluding ones you declined) are logged first to the meeting
   task (`EDB-9071`).
3. The **remaining time** is split across the Jira issues found in your GitHub / local-git
   activity, **proportional** to how much you worked on each, rounded to **30-minute** blocks
   (the rounding is balanced so each day still sums to 7h).
4. Activity with **no Jira key** is left **unassigned** for you to assign in the UI.
5. **No-activity** working days are pre-filled with a highlighted 7h on the leave task
   (`EDB-9070`) that you can accept, edit, or delete.
6. Per-day **status**: Working / Public holiday / Full-day leave (7h -> `EDB-9070`) /
   Half-day leave (3.5h -> `EDB-9070` + 3.5h work).
7. All worklog start times are **12:00 UTC** (avoids timezone issues in this first version).

You review and edit every day in the UI, then approve. Submission targets the **mock Jira** by
default; real Jira is only used when you explicitly switch the target.

## Layout

```
cmd/timeporting   CLI + local web review UI
cmd/mockjira      Mock Jira server (safe write target)
internal/config   Config loader (JSON file + env secrets)
internal/model    Domain types
internal/jira     Jira REST v3 client (read/write; points at mock or real)
internal/mockjira Mock Jira server implementation
internal/ics      ICS calendar parser
internal/github   GitHub API + local git activity collectors
internal/jirakey  Jira-key extraction + grouping
internal/engine   Timesheet planning engine
internal/web      Review UI server
```

## Configuration

Copy `config.example.json` to `config.json` (gitignored) and edit it. Secrets come from the
environment, never from the file:

- `JIRA_API_TOKEN` — Jira Cloud API token (read-only usage until you enable writes)
- `GITHUB_TOKEN` — GitHub personal access token (read-only)

## Safety

- Real Jira is **read-only** until you explicitly set `"target": "real"` and confirm.
- Secrets are read from environment variables and are never written to the repo.
- All write testing goes to the mock server.

## License

MIT
