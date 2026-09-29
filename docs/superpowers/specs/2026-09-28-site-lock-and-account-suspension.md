# Site locks and account suspension

**Date:** 2026-09-28
**Status:** implemented, both phases (see "Deploy order" for how the
declaration shipped). A lock no longer lasts forever: see the addendum
"Lock retention and the evidence hold" (2026-09-29) at the end, which
amends "Never deleted except by the operator".

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

*(Amended 2026-09-29: the cleanup sweep also purges a locked site once its
lock is older than `SITEBIN_LOCK_RETENTION_DAYS`, unless the operator placed
an evidence hold — see the addendum at the end.)*

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
declaration is its own commit, to be deployed only once the production stack's
`services/platform-api/src/routes/apps.ts` contains `suspendUserUrl` and the
running platform-api was built after it. An endpoint that is mounted but not
declared is never called, and is still authenticated by the secret, so the
rest of phase 2 never needed to wait.

On 2026-09-28 the running production platform-api already accepted
`gdpr.suspendUserUrl` (an optional URL, the body in the order above) by the
time phase 2 was ready, so both commits shipped together. A stack older than
that — a dev stack not rebuilt since — refuses the registration of any
instance built from this commit on; it logs the refusal and keeps serving.

## Out of scope

- Locking by domain or by owner in the register (the CLI takes a domain; a
  suspension locks by owner).
- Showing the lock to visitors with the reason — they get the generic page.
- A lock that also freezes CSP counting or view stats.

## Addendum (2026-09-29): lock retention and the evidence hold

**Status:** implemented. This changes Phase 1's "never swept": a lock now
keeps a site for a bounded time.

### Why

Phase 1 made a lock last forever: the sweep skipped a locked site, and
nothing but the operator's delete ever removed one. The website now promises
something narrower (the abuse page, and the privacy policy's "Locked
sites"): locked content is kept as evidence only as long as a case needs it,
and **purged after at most 180 days after the lock unless a case,
investigation or proceeding is still open**. The product has to do exactly
that on its own, or the promise is only as good as the operator's memory.

### The rule

- **`SITEBIN_LOCK_RETENTION_DAYS`** — default `180`; `0` keeps locked sites
  forever (the behaviour before this addendum, for an instance whose policy
  says so); a negative or unparsable value refuses to start, and so does
  one above `36500` (a century): a `time.Duration` holds about 292 years,
  and past that some values wrap to a retention of minutes, which would
  purge every locked site at the next sweep.
- **The clock is `Locked.At`, the start of the continuous lock.** A lock that
  replaces a lock keeps it — the operator's **Keep** over a scanner's or a
  suspension's lock, the operator re-locking with a new reason, an
  unsuspension that turns into a scanner lock (`ReleaseLock`). An account or
  scanner lock never replaces a lock anyway. Only an **unlock** ends the
  clock; a lock placed after it starts a new one. Without this, the natural
  sequence *scanner lock → Keep → suspension lock → Keep again* would reset
  the clock at every step and retention would never end. Consequence: after
  a Keep the register reads "locked <original date> by an admin"; the admin's
  action itself is logged with their id. `SiteLock.At` handed to `SetLock`
  is used only for a site that is not locked yet.
- **The cleanup sweep purges a locked site whose lock is older than the
  retention** (`now > At + retention`), completely: `store.PurgeLocked`
  deletes it the way `ForceDelete` does — the site folder (files,
  `meta.json` with its findings, `stats.json`, `provenance.jsonl`) and its
  edit-index and domain-index links. A container project's containers and
  networks are removed first (`stopContainers`, as for an expired site; a
  runtime that cannot stop them keeps the site for the next sweep). The
  extension's ownership marker goes stale and is dropped the next time the
  account is read (`ErrSiteGone`), as after every sweep deletion. The
  decision is taken **again under the site lock, from `meta.json`**: an
  evidence hold placed, an unlock, or a lock lifted while the sweep ran —
  from the register, or from the CLI in another process — wins, and an
  unreadable `meta.json` is never purged.
- **Unless the lock carries an evidence hold** — `Locked.Hold {at, by}`, the
  operator's statement that a case is still open. It is placed and released
  in the instance register (two server-rendered steps, like every other
  action under `script-src 'none'`) or with `sitebin hold <id>` /
  `sitebin unhold <id>`; `by` is the admin's account id (the register shows
  the email) or `cli`. Only a locked site can be held (`store.ErrNotLocked` /
  `ext.ErrSiteNotLocked`, `409` in the register): a hold without a lock would
  hold nothing. A replaced lock keeps its hold, a second hold keeps the
  first, an unlock ends the hold with the lock, and `SetLock` never places
  one. A hold does not move the date the retention ends; it only stops the
  purge. Releasing it lets the next sweep purge a site whose lock is already
  older than the retention — the release step and `sitebin unhold` say so.
- **The operator hears of every purge.** The sweep logs it at INFO
  (`cleanup: purged a locked site past the lock retention` — id, owner,
  `locked_at`, `locked_by`, reason, `retention_days`), and the running
  server's store hook (`SetPurgeHook`) hands it to the abuse alert mailer as
  **one plain-text line in the hourly digest** (view id, lock date and
  author, retention, owner, domains and reason defanged). Digest-only on
  purpose: a routine purge must never spend the hourly budget of immediate
  mails that real alerts need — the reasoning of abuse-detection Correction
  6. The one-shot `sitebin cleanup` has no mailer: it logs and prints.

Why the CLI is `sitebin hold` / `sitebin unhold` and not `sitebin lock
--hold`: a hold is placed on and released from a lock that already exists,
often weeks later. A flag on `lock` would need a second flag to take it off,
and would re-lock the site (new author, new reason) just to change the hold.

### Where it shows

- **Register:** every locked row carries a second line under the LOCKED tag:
  "purge due <date>", or "held (case open) since <date> by <email> ·
  retention ends <date>" (amber), or "kept until unlocked" with retention
  `0`. Actions: **Hold** (only with a retention) or **Release hold**, each a
  confirmation step naming the date. The lock step says the site is purged N
  days after the lock unless held; the Keep step says the lock keeps its
  date; the unlock step says a hold ends with it.
- **CLI:** `sitebin list`'s lock line ends in the same state; `sitebin lock`
  prints since when the site is locked and when it is due.
- **Alerts:** the site block of every alert names the purge date or the
  hold, and a hold alert says how to keep the site (`sitebin hold`).
- **Owner:** nothing new. The settings read, `get_site` and the dashboard
  show the lock as before — never whether a case is open or when it ends.

### Provenance follows the same rule

Stated precisely, with *R* the lock retention and 90 days
`provenance.Retention`:

1. **A locked site's own log** (`sites/<id>/provenance.jsonl`) is not purged
   while the site is locked (the sweep does nothing else to a locked site)
   and is deleted **with the site** when the site is purged — at most *R*
   after the lock, or later only while an evidence hold stands. Its entries
   from before the lock are then at most 90 + *R* days old (the log was
   purged at 90 days while the site was unlocked).
2. **An account's log** (enterprise, `accounts/<id>/provenance.jsonl`), at
   each sweep:
   - an account neither suspended nor owning a locked site: entries older
     than **90 days** are purged, as before;
   - a **suspended** account, or one **owning a locked site**: entries older
     than **max(90 days, *R*)** are purged — its log outlives the 90 days
     only up to the lock retention;
   - an account **one of whose locked sites carries an evidence hold**:
     nothing is purged;
   - *R* = 0: a suspended account or one owning a locked site keeps its
     whole log, as before this addendum.

   The account log's clock is its **entries' age**, not a lock's: a
   suspended account's sign-up from 200 days ago goes at 180 days even if
   its site was locked last month. A suspended account that owns no site
   cannot carry a hold (holds are per site); an operator who needs its trail
   longer exports it from the register's account page into the case file.
   The account purge runs **after** the site loop, so an account whose last
   locked site was purged in the same sweep is judged without it.
3. The seam carries both cutoffs:
   `ext.AccountProvenance.PurgeProvenance(before, heldBefore)`, with
   `heldBefore` zero for *R* = 0.

### Account deletion

Unchanged: refused while any of the account's sites is locked, held or not
(the owner's own deletion and the stack's GDPR order, `409`). Once the sweep
has purged the locked sites the account owns none, and the deletion — the
owner's, or the stack's retried GDPR order — goes through.

### Seam and store additions

`ext.LockHold`, `ext.SiteLock.Hold` and `.PurgeAt` (when the retention runs
out, computed by the core from the one rule in `store/retention.go` — the
register never computes a date), `ext.ErrSiteNotLocked`,
`SiteService.SetHold`, `Host.LockRetention()` (wording only), and the
two-cutoff `AccountProvenance.PurgeProvenance`. Store: `SiteLock.Hold`,
`LockHold`, `SetHold`, `SetLockRetention`, `LockRetentionEnds`,
`LockPurgeAt`, `LockPurgeDue`, `PurgeLocked`, `SetPurgeHook`. A store nobody
configured has retention 0 and never purges; `mustStore` sets it from the
configuration for the server and every CLI command.

### Tests

- `internal/store`: re-lock keeps the date (scanner → Keep → suspension →
  re-lock), an unlock starts a new clock, an account lock turned scanner
  lock keeps date and hold; a hold only on a locked site, the first hold
  stays, a re-lock keeps it, `SetLock` cannot place one, an unlock ends it;
  purge and retention-end dates; the purge before, at and after the
  retention (files, meta, edit and domain index, provenance, hook); the
  re-check under the lock (hold, unlock, retention 0 after the read).
- `internal/cleanup`: purge past the retention and not before; a held lock
  kept and purged once released; the re-lock chain purged on the first
  lock's date; retention 0 never purges; a container site stopped first and
  kept when it cannot be; the account-purge cutoffs for *R* = 180, 30 and 0,
  and the order (sites first).
- `internal/httpapi`: the seam (`PurgeAt`, `Hold`, `SetHold`, Keep keeps the
  date, `ErrSiteNotLocked`, `ErrSiteGone`); the owner's payload never shows
  the hold; a purge is one digest line, never an immediate mail; a hold
  alert names the retention.
- `internal/config`: default, `0`, refusals.
- `cmd/sitebin`: `hold` / `unhold` (refused unlocked, repeated, released past
  the date), `list` and `lock` output.
- `ee`: register rows (purge due, held, kept), the Hold and Release steps,
  CSRF, admin only, `409` for an unlocked site, retention 0 hides Hold; the
  account purge (suspended, owner of a locked site, held, *R* = 0); account
  deletion refused while locked or held and accepted once the site is purged
  (real store).
