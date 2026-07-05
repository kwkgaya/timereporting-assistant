# POC: Microsoft Teams / Calendar Integration

> **For decision makers:**
> Getting data out of Microsoft Teams needs an admin-approved app in the company's Microsoft
> tenant — a barrier the user cannot clear alone. For **v1 we deliberately skip Teams** and get
> meeting data from an exported calendar file (`.ics`) instead, which needs no approvals. We
> discussed building an **open-source "Teams proxy"** to make future approval easier; the idea has
> real merit but does **not** remove the admin-consent requirement, so it's parked. **Decision:
> if we ever build it, it will run locally, use least-privilege delegated permissions, and cover
> calendar only — Teams *messages* remain out of scope.**

## Detail (for AI agents / implementers)

### Why Teams is hard (the real blocker)
- Microsoft Graph access to a **work** account requires an **Entra ID (Azure AD) app
  registration** plus consent.
- **Calendar** (`Calendars.Read`) is often user-consentable.
- **Teams messages** (`Chat.Read`, `ChannelMessage.Read.All`) typically require **admin** consent
  and are treated as protected/metered APIs.
- The user **cannot register an app** in the Element Logic tenant, so anything Graph-based is
  blocked without IT involvement.

### v1 decision (locked)
- **Skip Microsoft Teams entirely.**
- Get **meetings from an exported `.ics` calendar file** (already implemented in `internal/ics`).
  This fully sidesteps Graph and admin consent for the meeting-logging requirement.
- Teams *message* activity (intended as a clue for no-commit days) is **not** available in v1.
  Its role is partially covered by local git + GitHub activity, and by the leave/no-activity
  placeholder in the engine.

### The "open-source Teams proxy" idea — analysis

**The proposal:** build a separate open-source app that holds the Teams/Graph access (registered
as an integrated application). Because it's open source, orgs can audit it before granting access,
and only have to trust that one app.

**What's genuinely good:**
- **Auditability** — IT can read exactly what it does before consenting.
- **Isolation** — sensitive Graph access is quarantined in one small, reviewable component.
- **Single point to revoke** — one app to watch.
- **Reusable** — others with the same problem benefit.

**The critical caveat (why it doesn't "solve" the blocker):**
- Open-sourcing the client **does not remove the need for an app registration + admin consent**
  in each organization. The permission gate remains; open source only makes the *approval
  conversation* easier ("here's the code, here are the 2 minimal scopes").

**Design constraints if it is ever built:**
1. **Local only.** It must run on the user's machine with tokens stored locally (OS keychain).
   A **hosted** proxy that relays other companies' Teams data through third-party infrastructure
   is a **dealbreaker** (GDPR / data-residency / compliance) and must not be built.
2. **Delegated permissions only** (acts as the signed-in user, sees only their own data). Never
   application permissions (which can read everyone's data and alarm security teams).
3. **Minimal scopes** — ideally just `Calendars.Read` (and, only if ever approved,
   `Chat.Read` for the user's own chats).
4. **No network egress** except to Microsoft Graph; verifiable.
5. **Reproducible/signed builds** so the audited source matches the running binary.
6. Beware the **multi-tenant hosted app** model: it *concentrates* risk (a leaked secret exposes
   every consenting tenant) and security teams often trust it *less* than a single-tenant
   registration.

**User decisions captured in discussion:**
- Runtime model: **local only**.
- Scope for any POC: **calendar/meetings is enough** (ICS already covers it) — **skip Teams
  messages**.

### Possible zero-admin workaround (unverified)
Some tenants allow a **user** to self-request their own Microsoft 365 data export
(privacy/GDPR export), producing parseable files of their own Teams chats — no admin app needed.
Whether Element Logic permits user self-service is **unconfirmed**; treat as "investigate, don't
assume." If viable, it would be parsed like the ICS file.

### Alternative future auth (also parked)
- **OAuth 2.0 (PKCE / device code)** against Graph with delegated calendar scope, if/when an app
  registration is available. Mirrors the keychain pattern already used for Jira.

### Status
**Parked.** Do not implement without an explicit instruction and a dedicated GitHub issue.
