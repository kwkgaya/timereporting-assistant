# Status & Backlog

> **For decision makers:**
> The MVP is **built, tested, and working** against the mock Jira, including secure credential
> handling and guided setup. Three enhancements remain in the backlog. Microsoft Teams integration
> is deliberately deferred (see future plans).

_Last updated: 2026-07-05. Verify live state on the
[milestone board](https://github.com/kwkgaya/timereporting-assistant/milestones)._

## Detail (for AI agents / implementers)

### Milestone
**"MVP: June 2026 worklog backfill"** — https://github.com/kwkgaya/timereporting-assistant/milestones

### Completed (closed issues)
| # | Title |
|---|---|
| 1 | Scaffold: project layout, CI, config loader |
| 2 | Mock Jira server (safe write target) |
| 3 | Jira read/write client (REST v3) |
| 4 | ICS meeting parser |
| 5 | GitHub + local-git activity collector |
| 6 | Jira-key extraction and grouping |
| 7 | Timesheet engine (allocation/leave/top-up/clone) |
| 9 | Review web UI + submit (mock first) |
| 8 | Polish, config, README/docs |
| 10 | Mock Jira: SQLite backend + issue search + week view UI |
| 13 | Jira auth: scoped API token in Windows Credential Manager |
| 14 | Guided setup flow: scoped Jira token with screenshots |
| 15 | First-run config wizard |
| 17 | Split read/write target (`mock-write`) |

### Open backlog
| # | Title | Notes |
|---|---|---|
| 11 | [Multi-clone dedup](https://github.com/kwkgaya/timereporting-assistant/issues/11) | Same repo cloned in multiple folders → dedup commits by hash |
| 12 | [Unassigned activity + Rovo prompt](https://github.com/kwkgaya/timereporting-assistant/issues/12) | Show commit summaries; generate paste-ready Rovo prompt to find the task |
| 16 | [Reflog-only commits](https://github.com/kwkgaya/timereporting-assistant/issues/16) | Include detached-HEAD / rebased / stashed commits |

### Recommended next order
`#11` → `#16` (both improve activity fidelity) → `#12` (UX for unassigned time).
None are blockers for the core flow; all are independent.

### Standing working rule (IMPORTANT for agents)
**Always create a GitHub issue before starting a piece of work**, then close it via the commit
message (`closes #N`). Keep this doc's tables roughly in sync, but the issue tracker is the
source of truth.

### Environment facts
- GitHub: user `kwkgaya`, repo `timereporting-assistant` (private). `gh` CLI authenticated.
- Local folder on disk is still named `timeporting-assistant` (pre-rename); harmless, code and
  remote both use `timereporting-assistant`.
- Commit author used by the agent: `kwkgaya <kwkgaya@users.noreply.github.com>`.
- Windows + PowerShell environment. Note: multi-line PowerShell here-strings sent to the terminal
  can be unreliable — prefer `gh ... --body-file <file>` for issue creation.
