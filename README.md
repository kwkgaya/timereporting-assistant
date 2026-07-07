# Timereporting Assistant

A small Windows desktop tool that **automatically fills in your Jira time reports** from your calendar meetings and Git/GitHub activity, lets you review and edit each day, then submits the worklogs to Jira with one click.

## What it does

For each working day it:
1. Reads **existing Jira worklogs** (already counted toward the 7 h target)
2. Logs **calendar meetings** first (from Outlook via published ICS URL)
3. Splits the **remaining time** across the Jira issues found in your Git commits and GitHub activity, proportional to how much you worked on each
4. Shows you a **review UI** where you can edit every suggested row before submitting

Days with no detectable activity prompt you to assign the Jira task manually.

## Quick start

1. Download the installer from [Releases](https://github.com/kwkgaya/timereporting-assistant/releases)
2. Run `TimereportingAssistant-Setup-vX.Y.Z.exe` — no admin required
3. The tray icon appears; click **Open time report** and follow the setup wizard

The wizard walks you through connecting your Jira account, GitHub token, calendar, and local git repos.

## Calendar integration

Paste a published ICS URL in Settings. Outlook: **File → Settings → Calendar → Shared calendars → Publish** → copy the ICS link. The app fetches it live.

See the in-app guide (**Settings → Calendar URL → ?**) for step-by-step screenshots.

## Configuration

All settings live in `%LOCALAPPDATA%\timereporting-assistant\config.json`.
Secrets (API tokens) are stored in **Windows Credential Manager**, never on disk.
The **Settings** page in the web UI covers everything without editing files.

## Building from source

`
go build ./...
go test ./...
`

Requires Go 1.24+. The installer is built by CI on every release tag via .github/workflows/release.yml.

## Privacy

The app communicates only with the Jira and GitHub endpoints you configure, using credentials you provide. No data is sent anywhere else.

## Contributing

Bug reports, ideas, and pull requests are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT
