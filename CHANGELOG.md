# Changelog

All notable changes to this project will be documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [Unreleased]

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
