# Requirements & Decisions

> **For decision makers:**
> This is the single source of truth for *what the tool does and why*, captured from the design
> discussion. The headline rules: **7-hour workday**, **meetings logged first**, **remaining time
> split across the Jira tasks found in your code activity**, **rounded to 30-minute blocks**,
> with **leave/holiday handling** and a **review-before-submit** step. Real Jira is read-only until
> explicitly enabled.

## Detail (for AI agents / implementers)

Every item below is a **locked decision** made with the user. Do not silently change these.

### Core logging rules
- **Target = 7 hours per working day.** Weekends are skipped automatically.
- **Worklog start time = 12:00 noon UTC** for every entry (deliberately avoids timezone issues
  in v1; do not "improve" this without asking).
- **Meetings first:** meeting minutes for the day are logged to the **meeting task** before any
  other allocation.
- **Remaining time** (`7h − existing − meetings`) is distributed across the Jira issues found in
  the day's activity, **proportional** to activity weight (number of commits/PRs per issue).
- **Rounding:** all suggested durations are multiples of **30 minutes**. Rounding uses the
  largest-remainder method so the day still sums to the target.
- **Top-up:** worklogs already present in Jira for a day **count toward** the 7h; the tool only
  adds the difference. Never double-log.

### Special tasks (example keys; set to your own)
- **Meeting task:** `EDB-9071`
- **Leave / holiday / absence task:** `EDB-9070`

### Day status (per-day, selectable in the UI)
- `working` (default)
- `holiday` — public holiday → 7h to `EDB-9070`, no other worklogs
- `full_leave` — full-day leave → 7h to `EDB-9070`
- `half_leave` — **3.5h to `EDB-9070` + 3.5h** filled by meetings/activity (day still totals 7h)

### Activity → Jira issue mapping
- Jira keys are extracted from branch names, commit subjects, and PR titles via regex
  `[A-Z][A-Z0-9]+-\d+` (case-insensitive; branch names like `edb-100-foo` are normalised).
- Activity with **no** detectable key goes to an **"unassigned"** bucket; the user picks the
  issue in the UI. (Enhancement [#12] adds a "Rovo prompt" helper for this — see backlog.)

### No-activity days
- A working day with no detected activity is pre-filled with a **highlighted 7h suggestion on
  `EDB-9070`** that the user can accept quickly, edit, or delete and log manually.

### Convenience features
- **Clone previous day:** copy the prior day's suggested distribution onto the current day.

### Backfill window (initial)
- **June 2026**, weekdays only (`2026-06-01`–`2026-06-30`). Configurable via `--from`/`--to`.

### Data sources
| Source | Purpose | Status |
|---|---|---|
| Real Jira (read-only) | existing worklogs, issue summaries | ✅ |
| ICS calendar export | meetings (declined excluded) | ✅ |
| Work GitHub (PAT) | commits, PRs, reviews | ✅ |
| Local git repos (`git log --all`) | commits (incl. multiple clones) | ✅ (reflog pending [#16]) |
| Microsoft Teams | activity on no-commit days | ❌ out of v1 (see future/) |

### Constraints / logistics
- The repo lives on the user's **personal** GitHub account (they cannot create repos in the work org).
- The user **can** create a Jira API token; they want a **scoped** token (not classic), stored as
  a secret, ideally never on disk. See [jira-integration.md](jira-integration.md).
- **"Revo" = Atlassian Rovo AI assistant** built into Jira Cloud (used for the unassigned-task
  discovery prompt).
