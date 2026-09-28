# Site locks and account suspension — plan

Design: [`../specs/2026-09-28-site-lock-and-account-suspension.md`](../specs/2026-09-28-site-lock-and-account-suspension.md).
Tests first; run `go vet ./...`, `go test ./...`, `go vet -tags ee ./...` and
`go test -tags ee ./...`.

## Phase 1 — site lock (ships alone)

1. **Store** — `store.SiteLock`, `Meta.Locked`, `CleanLockReason`,
   `SetLock` / `ReleaseLock` (account locks never replace a lock; an unlock
   bumps a container project's `restart_seq`), `ErrLocked`; `Delete` refuses a
   locked site, `ForceDelete` does not; `Replacement.Commit` refuses one.
   Tests: lock/unlock/replace rules, delete refused, force delete, commit
   refused, old meta reads unlocked.
2. **Sweep** — skip locked sites before any reconciliation. Tests: a locked
   site long past expiry survives and is not restamped; unlocked it is swept.
3. **Serving** — `authz` 410 "Site suspended" first; forms 410/404. Tests:
   subdomain, custom domain, path view, view-password site, expired site,
   container site (no upstream), form submit and challenge.
4. **Gates** — `withEditAuth` (+ `withEditAuthEvenLocked` for `GET` only),
   `withUploadAuth`, WebDAV, FTP, MCP `openSite` (+ get_site variant),
   payload/`SiteResult`/`SiteSummary` lock fields, no files for a locked site.
   Tests: every API route with password, token and session; upload token;
   WebDAV password and token; FTP; every MCP tool.
5. **Seam** — `ext.SiteLock`, `SiteInfo.Locked`, `ContainerSite.Locked`,
   `ErrSiteLocked`; `SiteService.SetLock`, `ReleaseLock`, `ForceDelete`;
   `Delete`/`SetName`/`RotateEditPassword` refuse, `ApplyQuota` skips. Lock
   drops caches and upload tokens and kicks the container runtime. Tests.
6. **Containers runtime** — `Locked` stops like expiry and does not count
   against the cap. Test.
7. **ee** — register: lock/unlock/keep steps, badge, filter, figure, force
   delete; dashboard: lock line, buttons hidden, refusals; account deletion and
   GDPR deletion refused while a site is locked. Tests.
8. **CLI** — `lock`, `unlock`, `delete --force`, `list` column. Tests.
9. **Edit page** — a locked site shows the lock card instead of the editor.
10. **Docs** — README CLI + register, `CLAUDE.md` invariant.
11. **Ship** — ff `main`, push, CI green, production rebuild per the ops
    procedure, live check with a throwaway site (create → lock → 410 →
    unlock → delete).

## Phase 2 — account suspension

1. **Account** — `SuspendedAt`, `SuspendedReason`, `Suspended()`.
2. **Refusals** — `currentAccount`, `BearerCredential`, `accountForAPI`, OIDC
   callback, local login. Tests: session, account token, OAuth token (MCP),
   callback.
3. **Webhook** — `verifyGDPRRequest` split out; `POST /account/gdpr/suspend`.
   Tests: signature, stale/future timestamp, missing `suspended`, unknown user
   200, suspend locks every site `By: account` and keeps admin locks,
   unsuspend lifts only account locks, repeat is idempotent.
4. **Register** — Suspended badge.
5. **Declaration** (separate commit, branch until the stack accepts it) —
   `stackGDPR.SuspendUserURL`; test.
