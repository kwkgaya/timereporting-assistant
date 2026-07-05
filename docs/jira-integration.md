# Jira Integration & Authentication

> **For decision makers:**
> The tool talks to **Jira Cloud** using a **scoped API token** (limited to reading and writing
> worklogs — nothing else). The token is stored in the **Windows Credential Manager**, never in a
> file or in the repo. During testing, the tool writes to a **fake local "mock Jira"** so real
> timesheets are never touched until you deliberately switch to the real target.

## Detail (for AI agents / implementers)

### The Jira instance
- **Base URL:** `https://application.jira.elementlogic.no`
- **Deployment:** Jira **Cloud** on a custom domain (confirmed via `/rest/api/3/serverInfo` →
  `deploymentType: "Cloud"`). Use **REST API v3**.
- **Auth:** HTTP Basic with `email:api_token` (base64). Comments use ADF (see `internal/adf`).

### Endpoints used
| Purpose | Method + path |
|---|---|
| Verify token / whoami | `GET /rest/api/3/myself` |
| Read existing worklogs | `GET /rest/api/3/search` (JQL) + `GET /rest/api/3/issue/{key}/worklog` |
| Issue summary | `GET /rest/api/3/issue/{key}?fields=summary` |
| Add worklog | `POST /rest/api/3/issue/{key}/worklog` |

Existing-worklog JQL: `worklogAuthor = currentUser() AND worklogDate >= "<start>" AND worklogDate <= "<end>"`.
Worklogs are then filtered locally by author email + date and grouped by day.

### Authentication decisions (locked)
- Use a **scoped API token**, NOT a classic token. Required scopes (minimal):
  - `read:jira-work`
  - `write:jira-work`
- Create at: https://id.atlassian.com/manage-profile/security/api-tokens →
  "Create API token" → **"API token with scopes"**.
- **Storage:** Windows Credential Manager, target name `timereporting-assistant/jira`
  (`internal/keychain`). On non-Windows, fall back to env vars `JIRA_EMAIL` + `JIRA_API_TOKEN`.
- **Never** store the token in `config.json` or the repo.
- The company (Element Logic) allows classic API tokens freely and already permits API-based
  Jira integration (the user has done GitHub→Jira field updates via API).

### Guided setup (implemented, [#14])
`timeporting credentials` (or automatic trigger when no credential is found) walks the user
through creating the scoped token, opens the Atlassian page in the browser, reads the token with
**hidden input**, validates it against `/rest/api/3/myself`, and stores it in the keychain.

### Target modes (implemented, [#17])
| `target` | Reads from | Writes to | Needs real creds? |
|---|---|---|---|
| `mock` | mock Jira | mock Jira | no |
| `mock-write` | **real Jira** | **mock Jira** | yes (read-only use) |
| `real` | real Jira | real Jira | yes |

`mock-write` is the recommended mode for realistic, safe testing: real existing worklogs make the
top-up math correct, but nothing is written to the real system.

### Mock Jira (implemented, [#2], [#10])
- SQLite-backed (fresh DB each start; re-seeds `EDB-9070`, `EDB-9071`, `EDB-100/200/300`).
- Implements the REST subset above.
- Inspect UI at `http://localhost:8099` with **issue search** (by key or title) and a
  **weekly timelog view** (Mon–Fri grid, per-issue minutes, day totals, prev/next navigation).

### Future auth option (not built)
OAuth 2.0 (Atlassian app + PKCE/device flow, no stored token) — deferred. See `future/`.
