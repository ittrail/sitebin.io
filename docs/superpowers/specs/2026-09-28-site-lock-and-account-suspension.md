# Site locks and account suspension

**Date:** 2026-09-28
**Status:** phase 1 implemented; phase 2 implemented, its stack declaration
waits for the production stack (see "Deploy order")

## Problem

Phishing operators sign up through the stack's Google login and publish
credential-harvesting and payment-scam pages on the hosted instance. The
operator's only tools were delete and set-expiry in the instance register.
Delete destroys the evidence an abuse report (Hetzner, Google, Telegram)
needs; an expiry makes the site 410 and then the cleanup sweep deletes it
anyway. One abuser simply uploaded the same page again after the first site
was expired.

The operator's words: *"seiten sperren — verhindert dass sie nach expiration
gelöscht werden, werden aber auch nicht mehr ausgeliefert."* A lock is an
**evidence hold**: the site is kept exactly as it is, served to nobody, and
changed by nobody but the operator.

## Phase 1 — the site lock

### The record

`store.Meta.Locked *SiteLock` (`json:"locked,omitempty"`):

```go
type SiteLock struct {
    At     time.Time // when
    Reason string    // optional, shown to the owner and in the register
    By     string    // "admin" (the operator, console or CLI) or "account" (a suspension)
}
```

Rule 1 of the codebase: an older `meta.json` has no such field and is
unlocked; no migration. The reason is trimmed, stripped of control characters
and capped at 200 characters (`store.CleanLockReason`). The admin form says it
is shown to the owner — an operator who wants a private note keeps it out of
the field.

Who may replace whom: an operator lock replaces any lock; an account lock is
applied only to an unlocked site, so a suspension never overwrites (and an
unsuspension never lifts) a lock the operator placed site by site. A repeated
suspension keeps the first date.

### Not served, anywhere

`authz` answers a locked site **before** the expiry and view-password checks:
`410`, "Site suspended", "This site has been suspended by the operator for
violating the terms of use." That is one place for subdomain views, custom
domains and `/v/<id>` path views alike — every content request passes through
it. A container site is therefore never admitted with an upstream; the
runtime also stops its containers (`ext.ContainerSite.Locked` is treated like
expiry, message "the site is locked by the operator"), so nothing keeps
running and reaching out. An unlock bumps the project's `restart_seq`, which
is how the runtime is told to start a project again, so an unlocked container
site comes back in the state its owner left it (enabled or not).

Forms: a submission to a locked site is `410` ("This site has been
suspended."), its captcha challenge `404`. CSP reports keep arriving and keep
being counted: they are evidence, and cost nothing.

### Never deleted except by the operator, explicitly

- `store.Delete` refuses a locked site with `store.ErrLocked`, under the site
  lock — the one irreversible operation is guarded where it happens, whatever
  surface asks. `store.ForceDelete` is the operator's takedown and the only
  way past it: the instance register's two-step delete and `sitebin delete
  --force`.
- The cleanup sweep **skips a locked site entirely**: no trust or domain
  reconciliation, no restamp, no expiry. A locked site outlives its expiry
  indefinitely; the date is kept and applies again the moment it is unlocked
  (a site unlocked long past its date is swept at the next pass after the
  24-hour grace — the operator decides whether to extend it first).
- `SiteService.ApplyQuota` (every tier-change restamp) leaves a locked site as
  it is: no new caps, no downgrade grace.
- **Account deletion** — the owner's own (local accounts) and the stack's
  GDPR order — is refused while any of the account's sites is locked, and
  deletes nothing: the owner sees why, the stack gets a `409` naming the
  sites. Deleting the account "around" a locked site would turn the hold into
  a way to erase the account that owns the evidence, and deleting the site
  with it would defeat the hold. The operator resolves it explicitly: collect
  the evidence, delete the locked sites in the register, and let the stack
  retry. (GDPR Art. 17(3)(e) allows retention for legal claims; the lock is
  the operator's statement that it applies.)

### Frozen for the owner

Every write surface refuses a locked site with `403`, "This site is locked by
the operator" (plus the reason, when there is one):

- `withEditAuth` — every per-site JSON API route, for the edit password, an
  account token and the owner's session alike. The check follows
  authentication, so a stranger learns nothing about the site. The one route
  that answers a locked site is `GET /api/sites/{edit}` (the edit page's
  load), which returns the settings document with `"locked": {"at","reason"}`
  and **no file list**; downloads, file content and folder listings are
  refused like writes — "content stays unreachable".
- `withUploadAuth` (upload tokens), WebDAV (password and token), FTP login.
- MCP `openSite`, i.e. every per-site tool. `get_site` answers with the lock
  (and no files); `list_sites` carries it per row.
- The dashboard's rename, rotate-edit-password and delete, through
  `SiteService.SetName`, `RotateEditPassword` and `Delete`, which refuse with
  `ext.ErrSiteLocked`. The dashboard shows the lock and hides those buttons.
- Locking drops the site's cached password verifications and revokes its
  upload tokens. A replace upload that began before the lock cannot commit
  after it (`Replacement.Commit` re-reads the meta under the site lock).

The gates read `meta.json` on every request, so a lock the CLI writes from
another process takes effect on the next request without the server's
in-memory state. Known, accepted limits: an ordinary (non-replace) upload
already streaming when the lock lands finishes its current files, and an FTP
session opened before the lock can keep writing until it disconnects (FTP is
off on the hosted instance).

**MCP output schemas.** `locked` is `omitempty` on `SiteResult` and
`SiteSummary`, so an unlocked site's result is byte-for-byte what it was — a
client that cached the output schema before the deploy (see the site-names
trap) only ever sees the new field on a locked site, whose owner cannot use it
anyway.

### The seam

- `ext.SiteLock` (+ `LockByAdmin`, `LockByAccount`), `ext.SiteInfo.Locked`,
  `ext.ContainerSite.Locked`, `ext.ErrSiteLocked`.
- `SiteService.SetLock(viewID, *SiteLock)` — nil lifts any lock;
  `ReleaseLock(viewID, by) (bool, error)` — lifts only a lock set by `by`;
  `ForceDelete(viewID)`.

All inert in the community build, which registers no provider and has no
register; the core's own paths (authz, sweep, gates, CLI) work there too.

### The instance register

- Per row: **Lock** → a server-rendered step with an optional reason field and
  "Yes, lock" (the dashboard's CSP is `script-src 'none'`, so every
  confirmation is a step, like delete). A locked row shows a **Locked** badge
  with the date, who locked it and the reason, and **Unlock** (also a step:
  unlocking serves the site again and puts its expiry back in force). A site
  locked by a suspension also offers **Keep locked**, which turns it into an
  operator lock that an unsuspension does not lift.
- **Delete** stays possible on a locked site (the same two-step, with the
  confirmation saying it is locked) and uses `ForceDelete`.
- A **Locked** filter and a **Locked** figure (instance-wide, like the others).
- Every action is logged with the admin's id.

### The CLI

```
sitebin lock <view-id|edit-id|domain> [reason…]
sitebin unlock <view-id|edit-id|domain>
sitebin delete [--force] <view-id|edit-id|domain>   # --force for a locked site
sitebin list                                          # LOCK column
```

Run on the host as `docker exec sitebin sitebin lock <id> phishing`. The CLI
writes as `By: admin`.

## Phase 2 — reacting to a stack-level suspension

The stack's "Suspend user" disables the Keycloak user, ends their Keycloak
sessions and calls every member app's optional webhook. Sitebin's side:

- **Declared** as `gdpr.suspendUserUrl` (`/account/gdpr/suspend`) next to the
  delete and export URLs, only when `SITEBIN_STACK_GDPR_SECRET` is set.
- **Authenticated** exactly like the GDPR orders (`verifyGDPRRequest`: HMAC
  over `<X-Timestamp>.<body>`, ±5 min, 1 MiB, refused before parsing).
- **Body** `{"userId","email","suspended":true|false,"reason"}`. `suspended`
  is required — a verified order that does not say which way is a 400, never
  a guess. `200` for an unknown user (idempotent), `5xx` only when a lock or
  unlock actually failed (the account's own state is written first, so a
  retry converges).
- **suspended: true** — `account.SuspendedAt` / `SuspendedReason` are set
  (a repeat keeps the first date), `TokenVersion` is bumped (every browser
  session and CSRF token dies), and every site the account owns is locked
  `By: "account"` with the reason. Every account-authenticated path refuses a
  suspended account: the session check (`currentAccount`, so the dashboard,
  the owner-session edit and the register), account API tokens and MCP OAuth
  tokens (`BearerCredential`, `accountForAPI` — checked after the token
  resolves, so a token provisioning path cannot hand the account back), the
  OIDC callback and local login. `token_version` alone would not do: tokens
  are not bound to it.
- **suspended: false** — the suspension is cleared and only locks with
  `By: "account"` are lifted; sites the operator locked individually stay
  locked.
- The register shows a **Suspended** badge on the owner of such sites.

### Deploy order

The stack's registration schema is `.strict()`. Declaring `suspendUserUrl`
before the production stack knows the field breaks registration. So the
declaration is its own commit, kept off `main` until the production stack's
`services/platform-api/src/routes/apps.ts` contains `suspendUserUrl` (and the
running platform-api was rebuilt after it). Everything else in phase 2 is on
`main`: an endpoint that is mounted but not declared is never called, and is
still authenticated by the secret.

## Out of scope

- Locking by domain or by owner in the register (the CLI takes a domain; a
  suspension locks by owner).
- Showing the lock to visitors with the reason — they get the generic page.
- A lock that also freezes CSP counting or view stats.
