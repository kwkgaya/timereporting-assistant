# timereporting-assistant — Documentation

> **5-minute read — for anyone.**
> This tool **automatically drafts your Jira time reports** from what you actually did
> (git commits, GitHub PRs, calendar meetings), lets you **review and edit** each day in a
> small web page, and only writes to Jira **after you approve**. It was built to solve one
> concrete pain: *"I keep forgetting to log time in Jira and fall a month behind."*

## What it does, in plain terms

For each working day it targets **7 hours** and builds a suggested timesheet:
1. Reads what you've **already logged** in Jira (so it never double-counts).
2. Logs your **meetings** first (from an exported calendar file) to a meeting task.
3. Splits the **remaining hours** across the Jira issues it finds in your git/GitHub activity,
   proportional to how much you worked on each, rounded to 30-minute blocks.
4. Days with no detected activity get a highlighted placeholder you can quickly fix.
5. You **review, edit, and approve** every day before anything is written.

**Safety first:** all writing is tested against a **mock Jira** (a fake local Jira) before
anything touches the real system. Your real Jira stays read-only until you explicitly opt in.

## Current status

- ✅ **MVP is built and working** against the mock Jira.
- ✅ Secure credential handling (scoped token in Windows Credential Manager).
- ✅ Guided first-run setup for both configuration and credentials.
- 🔜 A few enhancements remain (see the backlog).
- 💤 Microsoft Teams integration is intentionally **out of scope for v1** (see the future plans).

## Where to read more

| If you want to… | Read |
|---|---|
| Understand the goal and vision | [project-overview.md](project-overview.md) |
| See every decision we locked in (and why) | [requirements-and-decisions.md](requirements-and-decisions.md) |
| Understand how the code is structured | [architecture.md](architecture.md) |
| Understand the Jira connection & auth | [jira-integration.md](jira-integration.md) |
| Build and run it yourself | [build-and-run.md](build-and-run.md) |
| See what's done and what's left | [status-and-backlog.md](status-and-backlog.md) |
| See future ideas (e.g. Teams) | [future/](future/) |

## Key links

- **Repository:** https://github.com/kwkgaya/timereporting-assistant
- **Issue tracker / milestone:** https://github.com/kwkgaya/timereporting-assistant/milestones
- **Jira:** your organization's Jira Cloud instance (configured locally)

---

### Note for AI agents picking up this project

Read the documents in this order to reconstruct full context:
`project-overview.md` → `requirements-and-decisions.md` → `architecture.md` →
`jira-integration.md` → `build-and-run.md` → `status-and-backlog.md`.
Each doc has a human summary at the top and implementation detail below.
Follow the **standing rule**: create a GitHub issue before starting work, and close it via the
commit message (`closes #N`).
