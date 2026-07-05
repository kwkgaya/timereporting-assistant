# Project Overview

> **For decision makers:**
> `timereporting-assistant` is a personal productivity tool that auto-drafts Jira worklogs from
> a person's real activity, so they can catch up on (and keep up with) time reporting in minutes
> instead of hours. The immediate goal was to backfill **one month of missing worklogs**. The
> longer-term vision is a broader "personal work assistant." It is a private, single-user tool
> today; it could grow to serve a team.

## Detail (for AI agents / implementers)

### The problem
The user routinely forgets to log time in Jira at the end of a workday, procrastinates, and ends
up ~a month behind. Manually reconstructing what was done on each day is tedious and error-prone.

### The vision (staged)
- **Now (MVP):** backfill the user's Jira worklogs for a date range (initially **June 2026**),
  day by day, with review-and-approve.
- **Mid-term:** usable by the user's team.
- **Long-term:** a personal work assistant/secretary that can keep colleagues updated on the
  user's work presence.

### Who it's for
- v1: the user only (single-user, runs locally).
- Later: the team.

### Product principles established during design
1. **Human-in-the-loop.** The tool *suggests*; the user always reviews and approves before any
   write. Nothing is auto-submitted silently.
2. **Safe by default.** All write testing goes to a **mock Jira**. Real Jira is read-only until
   the user explicitly switches targets.
3. **No secrets on disk.** Credentials live in the OS keychain, never in the repo or config file.
4. **Minimal dependencies.** Prefer the Go standard library; only a few well-known modules are used.
5. **Unblock, don't stall.** Where an integration is blocked by org policy (e.g. Microsoft Teams
   needing admin consent), fall back to a workaround (e.g. calendar `.ics` export) rather than
   blocking the whole tool.

### Tech stack
- **Language:** Go (stdlib-first).
- **Interface:** CLI + a local web review UI served by the Go binary. (A richer React/Next.js UI
  is deferred; the current UI is embedded HTML/JS.)
- **Storage:** the mock Jira uses SQLite (in-memory, pure-Go driver).

See [architecture.md](architecture.md) for how these map to code.
