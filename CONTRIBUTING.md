# Contributing

Thank you for considering a contribution! This is a small internal tool that has grown into something shareable, so the bar is intentionally low.

## Before you start

- For **bug fixes** or **small improvements**, open an issue first so we can agree on the approach. For tiny fixes (typos, a one-liner) a PR without a prior issue is fine.
- For **new features**, please open an issue and wait for a thumbs-up before putting in a lot of work. Features that only apply to one company's Jira setup are unlikely to be merged.

## Development setup

Requirements: **Go 1.24+**, **Windows** (the tray and credential store are Windows-only; the web UI and engine compile on any platform for testing).

```powershell
git clone https://github.com/kwkgaya/timereporting-assistant
cd timereporting-assistant
go build ./...
go test ./...
```

Run the tool locally:

```powershell
go run ./cmd/timeporting --from 2026-06-01 --to 2026-06-30
```

This opens the web UI at `http://localhost:9080`. Real Jira credentials are read from `%LOCALAPPDATA%\timereporting-assistant\config.json` (create one from `config.example.json`).

## Project layout

| Path | Purpose |
|---|---|
| `cmd/timeporting/` | Main entry point — startup, CLI flags, wires everything together |
| `cmd/tray/` | Windows tray icon, auto-update, reminder toasts |
| `internal/engine/` | Core planning logic (meetings → remaining → proportional split) |
| `internal/jira/` | Jira Cloud REST v3 client |
| `internal/ics/` | ICS calendar parser |
| `internal/activity/` | Git and GitHub activity collectors |
| `internal/web/` | Local HTTP review UI (single-file Go template + embedded JS) |
| `internal/config/` | Config file + Windows Credential Manager |
| `internal/updater/` | GitHub Releases auto-update checker |
| `build/installer/` | Inno Setup script for the per-user installer |

## Making changes

1. Fork the repo and create a branch: `git checkout -b fix/my-fix`
2. Make your changes. Add or update tests where applicable.
3. `go build ./...` and `go test ./...` must pass with no failures.
4. Keep commits focused; one logical change per commit is ideal.
5. Open a PR against `main`. Describe **what** changed and **why**.

## Code style

- Standard `gofmt` / `goimports` formatting (run `go fmt ./...` before committing).
- No external dependencies added without discussion — the project aims to stay minimal (`modernc.org/sqlite`, `fyne.io/systray`, `golang.org/x/sys` are the current ones).
- The web UI is generated Go string HTML — no separate JS build step. Keep it that way unless there is a very strong reason.

## Commit messages

Use the imperative mood, short subject line (≤ 72 chars), e.g.:

```
Fix duplicate meeting suggestions when some already logged
```

## Releases

Maintainer only. Tagging `vX.Y.Z` on `main` triggers the GitHub Actions workflow that builds the installer and publishes the release.

## License

By submitting a pull request you agree that your contribution will be licensed under the [MIT License](LICENSE).
