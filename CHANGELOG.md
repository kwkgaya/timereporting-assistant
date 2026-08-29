# Changelog

All notable changes to this project will be documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [Unreleased]

## [0.35.4] — 2026-08-29
### Fixed
- "Clone previous day" was still offered on days that are locked because they are already complete (Jira time at or above the daily target). The button now follows the same rule as the day-status selector and is hidden for submitted *and* fully-logged days

## [0.35.0-beta.2] — 2026-08-05
### Fixed
- PR reviews, comments, and authored PRs whose only Jira key was in the source branch name (not the title) were silently dropped ([#100](https://github.com/kwkgaya/timereporting-assistant/issues/100)). The `/search/issues` API never returns the branch name, so it's now fetched from the PR's own API URL instead. Covers merged PRs too, whose branch may already be deleted, and matches the branch's Jira key case-insensitively

## [0.35.0-beta.1] — 2026-08-05
### Added
- GitHub activity detection now picks up plain PR comments, not just formal review submissions ([#99](https://github.com/kwkgaya/timereporting-assistant/issues/99)). Previously, days where the only GitHub activity was commenting on a teammate's PR (without submitting an Approve/Request changes/Comment review) showed up as having no activity

## [0.34.0-beta.3] — 2026-08-05
### Fixed
- Clicking the reminder toast opened the system browser instead of the app's own window ([#97](https://github.com/kwkgaya/timereporting-assistant/issues/97)). The toast used a bare `http://` launch target, which Windows hands to the default browser; it now protocol-activates a dedicated `timereporting://` handler that relays the click back to the running tray, which opens the embedded window like "Open time report" does
- A black console window briefly flashed whenever a toast was shown (reminder, credential error, or the beta "Test reminder toast" button) ([#98](https://github.com/kwkgaya/timereporting-assistant/issues/98)). The PowerShell process used to display the toast was missing `CREATE_NO_WINDOW`, unlike every other background process this app spawns

## [0.34.0-beta.2] — 2026-08-05
### Fixed
- The reminder toast's hero banner and logo were blurry — both were stretched up from the 32×32 tray icon. They now use a dedicated 256×256 render of the same icon

## [0.34.0-beta.1] — 2026-08-05
### Added
- The daily reminder toast (and the Jira credential-error toast) is now much bigger and harder to miss: a hero banner and large logo, plus `scenario="urgent"`, which breaks through Focus Assist/Do Not Disturb on Windows 11 and keeps the toast on screen until it's dismissed
- Beta builds only: a "Test reminder toast" tray menu item that fires the reminder toast immediately, for previewing without waiting for the idle/unlock cycle

## [0.33.0-beta.1] — 2026-08-03
### Added
- Cancelled meetings are excluded from the day's time. A calendar entry whose title is a cancellation notice (`Cancelled - …`, `Canceled: …`) or that carries `STATUS:CANCELLED` no longer contributes suggested time. Titles that merely start with the word, such as "Cancelled flights follow-up", are kept — a separator is required. Cancelling a single occurrence of a recurring meeting removes only that day, not the series
- The day summary line now reports a **Balance** — the time still missing from (or logged over) the 7h target. Balance replaces **Total**, so exactly one of the two is shown: Total only when the day lands exactly on target, Balance otherwise

### Changed
- The product is displayed as "Time Reporting Assistant" throughout the UI, tray, installer and setup

### Fixed
- The review UI ignored the `workdayHours` setting: the daily target was hard-coded to 7h in the summary line, the "incomplete day" test, the "logged / 7h" labels and the default time for a new row. All of them now use the configured value, which the server injects into the page
- "Open time report" did nothing when the window was already open behind other windows. Windows refuses `SetForegroundWindow` to a process that does not own the foreground window, so the request was downgraded to a taskbar flash; the window is now genuinely raised and focused
- A tray click no longer waits behind the daily reminder check for a second full startup — both now join the same start attempt
- An unexpected exit of the background service is written to the log instead of passing unnoticed

## [0.32.0-beta.3] — 2026-08-03
### Added
- "Open time report" now opens the window immediately with a spinner explaining that git activity, calendar events and Jira issues are being collected, instead of leaving a blank unresponsive frame during the wait
- Startup failures (missing `timeporting.exe`, the service exiting early, or the port never opening) are reported in the window with a message and a pointer to the logs, rather than hanging

### Fixed
- Two application windows could be opened. WebView2 takes seconds to initialise, and the window handle was only recorded afterwards, so a second tray click during that gap started a second window
- Two background services could be started at once when a tray click and the daily reminder check raced
- A duplicate `timeporting.exe` now exits immediately instead of repeating the whole (slow) plan build and only then failing to listen — the web port is claimed before the build starts
- A second tray process now exits at startup instead of adding a second tray icon
- Removed `calendar.ics`, a personal calendar export that was committed by mistake and read by nothing; `*.ics` is now ignored

## [0.32.0-beta.2] — 2026-08-03
### Fixed
- Issue picker could not find an issue by its key (e.g. `EDB-11549`). JQL's `text ~` clause only searches summary/description/comments and never matches a key, and the unescaped `-` made the whole query fail. Keys are now resolved with a direct issue lookup, the free-text search also covers `summary`, and a key hit survives a failed text search
- Keyboard flow when adding a worklog: focus now moves to the Time box after picking an issue (the table is rebuilt on add, which previously dropped focus and made Tab restart from the top of the page), and Tab commits the highlighted search result

## [0.32.0-beta.1] — 2026-08-03
### Added
- **Recurring calendar events are now expanded.** The ICS parser ignored `RRULE` entirely, so every repeating meeting — daily standups, weekly syncs, monthly retros — was silently dropped and most days looked like they had no meetings at all. Supports `FREQ=DAILY/WEEKLY/MONTHLY/YEARLY` with `INTERVAL`, `COUNT`, `UNTIL`, `BYDAY` (including ordinals such as `2TU` and `-1FR`), `BYMONTHDAY` and `BYSETPOS`, plus `EXDATE` exclusions and `RECURRENCE-ID` rescheduled instances
- Per-day note when a working day has no calendar events at all, since that usually means a stale calendar rather than a genuinely meeting-free day

### Fixed
- The calendar warning was only computed on the full plan build and read by the UI once at page load, so it never appeared. It is now recomputed on every day build and the banners are refreshed after loading and every 30 s

## [0.31.0-beta.2] — 2026-08-03
### Fixed
- **Application hang on Submit (present since 0.30.x).** Three independent defects, each of which could freeze the whole app:
  - Changing a day's status rebuilt the day plan (git + Jira + GitHub + calendar, potentially minutes) **while holding the global server lock**, so every other request — including Submit — blocked until it finished. The rebuild now runs unlocked.
  - A panic inside any handler left the global lock permanently held, because critical sections unlocked explicitly rather than with `defer`. Every later request then blocked forever. Lock handling is now panic-safe and a recovery middleware returns a JSON 500 instead of dropping the connection.
  - `POST /days/{date}/rows/{i}/submit` released the lock across the Jira call and then reused the now-stale day index and row index. When the background rebuild of incomplete days replaced the day in that window, the resulting out-of-range panic wedged the server. The day and row are now re-resolved after the call.
- Every `git` invocation now has a 30 s timeout and interactive credential prompts are disabled, so a stalled repository can no longer block a day build indefinitely
- The blocking "Building day plan…" overlay is reference counted, so overlapping operations can no longer leave it stuck on screen
- Browser requests now time out after 3 minutes and non-JSON error responses (e.g. CSRF rejections) surface their real message instead of a parse error

## [0.31.0-beta.1] — 2026-08-03
### Added
- Calendar health warning: an amber banner is shown when the published calendar URL fails to load, when no calendar is configured, or when the calendar loads but contains no events (a revoked Outlook publish link still returns a valid but empty feed)

### Fixed
- Taskbar/Explorer/shortcut icons: the icon resource was never actually linked into any executable (`go-winres --out` takes a path prefix, and the emitted `_windows_amd64.syso` was ignored by the Go toolchain because of the leading underscore)
- Window icon is also applied to the WebView2 window class, which the taskbar uses in preference to `WM_SETICON`
- Installer, uninstall entry and shortcuts now carry the app icon
- Branch name is now shown for commits found only in the git reflog

## [0.30.0-beta.1] — 2026-07-07
### Added
- `--version` flag; version shown in Settings page footer and tray tooltip/menu
- CHANGELOG.md (this file); release notes shown in update notification toast
- Date range (from / to) configurable in Settings without restarting the app
- `go mod verify` step in CI to ensure reproducible builds

### Fixed
- Security: CSRF protection on all state-changing API endpoints (localhost-only requests still require correct `Origin`/`Referer`)
- Security: ICS calendar URL validated as HTTPS before fetching
- Security: API tokens no longer logged even at debug level

## [0.29.7] — 2026-07-07
### Added
- Troubleshooting.md with common issues and fixes
- Error toasts stay visible 8 s and link to Troubleshooting guide

### Fixed
- Jira error messages now include actionable guidance (401 → re-enter token, 403 → check scopes, 429 → rate limit, 502/503 → Atlassian outage)
- Save & rebuild plans button moved to the top of the Settings page

## [0.29.6] — 2026-07-07
### Added
- CONTRIBUTING.md with development setup, project layout, and code style guide

### Fixed
- README: removed stale "Local export" option from calendar integration section

## [0.29.5] — 2026-07-07
### Fixed
- Meetings already logged in Jira (matched by comment) are no longer re-suggested
- Summary line (Target / Existing / Suggested / Total) is now always visible, even when Jira time already reaches the target

## [0.29.4] — 2026-07-06
### Fixed
- Submit was using stale server-side issue keys instead of what the user saw in the UI; local state is now flushed to the server before any submit operation

## [0.29.3] — 2026-07-06
### Fixed
- All-day calendar events containing "poya day" (e.g. "Full Moon poya day") are now treated as public holidays

## [0.29.2] — 2026-07-06
### Changed
- When no activity is detected for a day, the leave/absence task is no longer pre-filled; the user can select it from the issue search dropdown

## [0.29.1] — 2026-07-06
### Fixed
- TDZ crash "can't access lexical declaration 'dayFull' before initialization" on page load

## [0.29.0] — 2026-07-05
### Added
- Lazy startup: app opens immediately with stub plans; full git/ICS scan happens on demand when you navigate to a day

## [0.28.0] — 2026-07-04
### Added
- Clone previous day button copies the previous business day's suggested worklogs
- Status change (working → holiday / leave) rebuilds suggestions automatically
