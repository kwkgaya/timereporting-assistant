# Troubleshooting

## Jira connection issues

### "Credentials rejected (401 Unauthorized)"
Your API token has expired or been revoked.
1. Go to **Settings → Jira token** and click **Edit**.
2. Generate a new token at [id.atlassian.com/manage-profile/security/api-tokens](https://id.atlassian.com/manage-profile/security/api-tokens).
3. Paste the new token and click **Save**.

### "Access denied (403 Forbidden)"
Your token exists but lacks the required OAuth scopes.
1. Open [developer.atlassian.com/console/myapps](https://developer.atlassian.com/console/myapps/) and find your API token / OAuth app.
2. Make sure both **`read:jira-work`** and **`write:jira-work`** scopes are enabled.
3. Re-generate the token and update it in **Settings → Jira token**.

### "Unable to connect" / "no such host" at startup
1. Check that the **Jira base URL** in Settings is correct — it should look like `https://yourcompany.atlassian.net`.
2. Verify you are connected to your company VPN if Jira is only accessible on VPN.
3. Visit the Jira URL in your browser to confirm it is reachable.

### "Rate limit hit (429 Too Many Requests)"
Jira has temporarily throttled requests from your IP/account.  
Wait a minute, then click **Reload** in the app or restart it.

### "Jira is temporarily unavailable (502 / 503)"
Atlassian may have an active incident. Check [status.atlassian.com](https://status.atlassian.com) for outages.

---

## Calendar not loading

### No meetings appear / wrong meetings
1. Open **Settings → Calendar URL** and confirm the ICS URL is still valid — Outlook-published URLs can expire if the calendar sharing is revoked.
2. Re-publish the calendar in Outlook (**File → Settings → Calendar → Shared calendars → Publish**) and paste the new URL.
3. The URL must start with `https://`.

### "Poya day" or holiday not detected as non-working
The app detects all-day calendar events containing **"holiday"** or **"poya day"** in the title. Make sure your calendar event spans a full day and the title matches one of those keywords (case-insensitive).

---

## Git / GitHub activity not showing

### No commits detected
1. Check **Settings → Local repos** — the path must be the root of a git repository (the folder that contains `.git/`).
2. Confirm the commit author email matches the email set in **Settings → Jira** (used to filter commits).
3. Try running `git log --author="you@example.com" --since="2026-07-01"` in the repo folder to verify commits are visible.

### GitHub activity missing
1. Confirm your **GitHub username** is correct in Settings.
2. Your **GitHub token** needs `read:user` and `repo` (or `public_repo`) scopes.
3. If you use GitHub Enterprise, set the API base URL to `https://github.yourcompany.com/api/v3` in the config file (`%LOCALAPPDATA%\timereporting-assistant\config.json`).

---

## Suggested worklogs look wrong

### Already-logged meetings appear as suggestions
This was fixed in **v0.29.5**. Update the app to the latest version via the tray icon menu (**Check for updates**).

### Wrong issue key was submitted
This was fixed in **v0.29.4**. Update to the latest version.

### Total is less than 7 h even though there's activity
The engine rounds allocations to 30-minute blocks, which can cause the total to fall slightly short. Add or edit a row manually to top up the remaining time.

---

## Installer / startup issues

### "The app is already running" when installing
The installer automatically stops any running `timeporting.exe` and `tray.exe` processes before upgrading. If it still fails, open Task Manager, end those processes manually, then re-run the installer.

### Tray icon doesn't appear after install
1. Check the Windows system tray overflow area (the `^` arrow in the taskbar).
2. If it is not there, open Task Manager and look for `tray.exe`. If absent, run it manually from `%LOCALAPPDATA%\Programs\timereporting-assistant\tray.exe`.
3. Check the log file at `%LOCALAPPDATA%\timereporting-assistant\logs\timeporting.log` for startup errors.

### Web UI doesn't open (port 9080 conflict)
Another process may be using port 9080. You can change the port in the config file:
```json
{ "webPort": 9081 }
```
Set `webPort` in `%LOCALAPPDATA%\timereporting-assistant\config.json` and restart the app.

---

## Log files

Detailed logs are written to:
```
%LOCALAPPDATA%\timereporting-assistant\logs\timeporting.log
```
Open the folder quickly via the tray icon menu → **Open logs folder**.

When reporting a bug, please attach the relevant portion of this log.

---

## Still stuck?

Open an issue at [github.com/kwkgaya/timereporting-assistant/issues](https://github.com/kwkgaya/timereporting-assistant/issues) with:
- The error message you see
- The relevant lines from the log file
- Your app version (shown in Settings)
