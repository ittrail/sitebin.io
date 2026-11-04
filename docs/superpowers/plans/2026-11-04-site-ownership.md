# Site ownership (adopt, transfer, share): implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** paid accounts can take over anonymous drops, transfer sites
(bundles, accept-by and available-from dates, recipient must already hold a
paid plan) and share sites with admins (optional end date). All of it is
gated by the tier flag `sharing`.

**Architecture:**
- **Core (MIT).** The core owns the per-site truth: `Meta.OwnerAccountID`
  and a new `Meta.Admins`. Every ownership change is **one store operation
  under the site lock**: transfer, adopt, admins.
- **The gate** learns a second standing, **admin**. Owner-only actions are
  refused to an admin on every surface.
- **Enterprise.** It owns offers, invitations, the share index, the plan
  gate, mail, the background loop, the dashboard pages and GDPR. It reaches
  the core only through a new **optional** seam interface,
  `ext.OwnershipSites`, implemented by the core and asserted by `ee`, so no
  existing fake breaks.
- **`meta.json` is the truth** about who owns a site. Every `ee` walk over an
  account's site index re-checks the owner there.

**Tech Stack:** Go stdlib, `html/template` (server-rendered dashboard with no
new script), vanilla JS on the edit page and in the embed, PowerShell e2e
with mailpit.

**Spec:** `docs/superpowers/specs/2026-11-03-site-ownership-design.md`, which
is binding. Also binding: the git session's notes, recorded in memory
`sitebin-front-door.md` under "Update 2026-11-03 ~23:15".

## Sequencing (operator decision, 2026-11-03)

**Git per drop ships first** (session `sitebin-project-c6`, branch
`feat/site-git`). **Do not start Task 1 before git is merged to main.**
Then rebase `feat/site-ownership` onto main.
- Git's central byte function (files + repo) replaces `store.Usage` in every
  limit check below. c6 will send its name; record it in the ledger.
- Git's `quota_git` stamp flows through the shared stamp helper (Task 1).
- `DELETE /api/sites/{id}/git` joins the owner-only list (Task 3).
- **Before Task 3** (the gate), send c6 the final standing and actor names
  and the owner-only list. This was promised.

## Global Constraints

- **Branch:** product `feat/site-ownership`; website `content/site-ownership`,
  created in Task 15. Merge and push only in Task 16, and only with the
  operator's OK.
- **Verification for every Go task:** `go vet ./...`, `go vet -tags ee ./...`,
  `go test ./...` and `go test -tags ee ./...`, all green.
- **Stored data:**
  - `Meta.Admins []SiteAdmin` with json `admins`, omitempty;
  - `SiteAdmin{Account string; Until *time.Time}` with json
    `account,until,omitempty`;
  - a missing field means no admins. No migration.
- **The tier flag** is `Tier.Sharing bool`, json `sharing`, omitempty;
  absent means false.
- **Accounts mode** (no tiers): every live account is allowed to share.
- **Limits:**
  - a bundle holds 1–50 sites;
  - a site has at most 10 admins;
  - accept-by is in the future and at most 365 days ahead; available-from,
    if given, falls before accept-by;
  - invitations lapse after 30 days;
  - offers and invitations together: at most 20 a day per account;
  - adopt attempts: 10 per hour per IP and 10 per hour per account.
- **The never-reveal answer** after creating an offer or invitation, exact
  text: `If an account with this address exists, it has been notified. Otherwise ask the person to sign up with exactly this address — it will appear in their account as soon as they do.`
- **Adopt failure, exact text:** `URL or password wrong.` It is the same for
  an unknown site, a wrong password, a site that is already owned, locked or
  expired, and a malformed URL.
- **Owner-only for an admin** (refused on every surface; API answers 403
  `only the site's owner can do this`):
  - deleting the site;
  - setting, changing or clearing `expires_at`;
  - rotating the edit password;
  - transfer;
  - managing shares (an admin may leave their own share);
  - `DELETE /api/sites/{id}/git`.
- **Standing order in the gate** is unchanged: upload token refused, then
  bearer token, then session, then edit password. An admin through token or
  session gets admin standing; an edit password always gets password
  standing.
- **Mail:**
  - plain text, through `ee/smtp` `Mailer.Send`, in a goroutine,
    best-effort;
  - only to accounts whose `EmailVerified` is true;
  - with `p.mailer == nil`, no mail is sent.
- **The trust marker and caps** come only from the shared stamp helper
  (Task 1). Never set them by hand.
- **Dashboard and manage pages:** no new script; plain POST forms with CSRF.
- **E2E stays pure ASCII.**
- **Commits:** a lowercase conventional prefix and a behaviour sentence,
  ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings carried from planning (recorded here so implementers need not re-decide)

- **R1 (locking):** transfer and adopt are each one store function under
  the site lock. Restamping happens in the same function, through the
  stamp helper. Nothing nests seam calls inside a site lock.
- **R2 (truth):**
  - `p.ownedSiteIDs(acc)` = `ListSiteIDs` filtered by `Info(id).Owner == acc.ID`;
  - every `ee` walk uses it: `owns`, `renderDashboard`, `restampSites`,
    `suspend.eachSite`, `lockHolds`, GDPR export, `tagsInUse`,
    `accountSites`, and the `account.Store.Delete` callback, which must
    skip a site it does not own in meta;
  - a stray marker (meta owner ≠ account) is unlinked by the background
    loop (Task 10).
- **R3 (caller-aware results):** `sitePayload` stays as it is. Handlers wrap
  it with `a.payloadFor(r, site)`, which adds `role` and `shared_by` and,
  for an admin, replaces `tags` with the admin's own list. MCP gets
  `openSiteAs` returning the standing.
- **R5 (restamp rule):** adoption **and** transfer stamp the site exactly as
  creation does: caps, forms, trust marker, and tier expiry = now + cap, or
  none. Only a date the owner chose (`ExpiryFromTier == false` with a date)
  survives a transfer.
- **R7 (domains):**
  - the recipient tier's **per-site** `CustomDomains` cap is checked against
    the moving addresses, excluding zone names;
  - no licence check here: enforcement stays in its two existing places.
- **R11:** "Upgrade" links point to `/account#plan`, the dashboard's plan
  card, because `/account/upgrade` is POST-only.
- **R12:** `/account/status` is unchanged (yes/no only, as the privacy page
  promises). The ticket's line links to the adopt page, and that page
  explains the plan requirement.
- **R13:** "Shared by …" shows the owner's email. Accounts have no name.
- **R15:** the sender's **pending** claims on a moved host are not moved; the
  sweep drops them later. The offer notice says so.
- **R16:** tests pin that the edit page, which sends session and password
  together, gets admin standing for an admin.

## Review Focus

1. **The old owner after a transfer.** Their dashboard, tokens, MCP, edit
   password, upload links and any stale index marker must all fail. A
   reasonable owner expects "it's gone from me" to be total. Pinned in
   Tasks 2, 4 and 8.
2. **Accepting a bundle while the sender edits it**: a site removed,
   cancelled, or a domain added on a shared host. The result must be all or
   nothing, and the answer must say why. Pinned in Task 8.
3. **An admin's write that is really a delete:** an expiry, a replace of all
   files, a `PUT` that clears tags. Expiry is refused; the rest are allowed
   as owner-equivalent edits. Pinned in Task 3.
4. **A downgrade, lock or suspension mid-way:** shares and offers pause and
   are never deleted. Lifting the condition resumes them. Pinned in Task 10.
5. **Mail to strangers and enumeration.** The form answers identically,
   sends nothing to unknown addresses, and respects the daily limit. Pinned
   in Tasks 7 and 9.

---

### Task 1: Core data and the three store operations

**Files:**
- Modify: `internal/store/meta.go` (:13-97): add `Admins []SiteAdmin` and the
  `SiteAdmin` type.
- Create: `internal/store/ownership.go` with:
  - `func (s *Store) TransferOwner(site *Site, from, to string, detach []string, stamp func(*Meta, time.Time)) (newPassword string, err error)`.
    Under the site lock, re-read meta and refuse unless
    `OwnerAccountID == from` (`ErrNotOwner`) and the site is not locked
    (`ErrLocked`). Then:
    1. detach each address via an extracted `removeDomainLocked`;
       `RemoveDomain` (`domains.go:203`) becomes a lock-taking wrapper
       around it;
    2. set the owner to `to`;
    3. clear `Tags` and `Admins`;
    4. call `stamp(&m, now)`;
    5. set a fresh edit password (reuse `SetEditPassword`'s inner hashing);
    6. write.
  - `func (s *Store) AdoptOwner(site *Site, to string, stamp func(*Meta, time.Time)) error`.
    Under the lock, require `OwnerAccountID == ""`, not locked and not
    expired (`ErrNotAdoptable` otherwise); set the owner; call `stamp`;
    write.
  - `func (s *Store) UpdateAdmins(site *Site, edit func([]SiteAdmin) ([]SiteAdmin, error)) error`.
    Under the lock, refuse a locked site, de-duplicate by account, and cap
    at 10 (`ErrTooManyAdmins`).
- Create: `internal/httpapi/stamp.go` with
  `func stampGrant(m *store.Meta, g ext.CreateGrant, now time.Time)`.
  Extract it from `createSiteWith` (`sites.go:739-757`, plus the trust marker
  at :764 and the default expiry at :790-795): owner caps, forms, trust and
  tier expiry. An owner-chosen date survives (R5). Refactor
  `createSiteWith` to call it.
- Test: `internal/store/ownership_test.go`, `internal/httpapi/stamp_test.go`.

**Interfaces:**
- Produces:
  - `store.SiteAdmin`;
  - `Store.TransferOwner`, `Store.AdoptOwner`, `Store.UpdateAdmins`;
  - the errors `store.ErrNotOwner`, `ErrNotAdoptable`, `ErrTooManyAdmins`;
  - `httpapi.stampGrant`.
- Note: the trust marker is a file (`store.SetTrusted`, `store.go:99`). The
  stamp callback runs under the lock, so `stampGrant` must not call
  `SetTrusted` itself. It sets a flag that `TransferOwner` and `AdoptOwner`
  apply after the meta write, under the same lock, through a lock-free
  variant. Extract `setTrustedLocked`.

- [ ] **Step 1: Write the failing tests.**
  - `TestTransferOwnerMovesEverything`:
    - owner a→b;
    - tags and admins cleared;
    - the listed address detached and its redirects pruned;
    - the generated address serving again;
    - a new password works and the old one fails;
    - the stamp applied.
  - `TestTransferOwnerRefusesWrongOwnerAndLock`.
  - `TestAdoptOwner`: anonymous → owned and stamped; refused when owned,
    locked or expired.
  - `TestUpdateAdmins`: dedupe, the cap of 10, a locked site refused.
  - `TestStampGrantMatchesCreation`: a site created with grant g, and a site
    adopted with g, end with an identical stamp, trust marker included.
- [ ] **Step 2: Run** `go test ./internal/store ./internal/httpapi -run 'Owner|Adopt|Admins|Stamp' -v`.
  Expected: FAIL.
- [ ] **Step 3: Implement.** Keep all existing create tests green.
- [ ] **Step 4: Run** the four Go commands. Expected: PASS.
- [ ] **Step 5: Commit** `feat: the store can hand a site to another owner, adopt a drop and keep a site's admins, each under one lock`.

### Task 2: The ownership seam (core side)

**Files:**
- Modify: `internal/ext/ext.go`: a new **optional** interface
  `OwnershipSites` (doc: implemented by the core's site service; `ee`
  asserts it). Its methods:
  - `TransferSite(viewID, from, to string, g CreateGrant, detach []string) (newPassword string, err error)`.
    It drops the verify cache for the site and revokes its upload tokens.
  - `AdoptSite(viewID, to string, g CreateGrant) error`
  - `SetAdmins(viewID string, edit func([]SiteAdmin) ([]SiteAdmin, error)) error`.
    It also defines `ext.SiteAdmin{Account string; Until *time.Time}`, the
    seam twin of the store type.
  - `CheckEditPassword(viewID, password, ip string) (ok bool, retryAfter time.Duration)`,
    which uses `verifyEditIP` and its limits.
  - `ResolveSite(urlOrID string) (viewID string, ok bool)`, which takes a
    view URL, an edit URL, a view id or an edit id (reuse `resolveTarget`,
    `report.go:83`).
  - `WillNotMove(viewID, sender string) []string`: addresses whose proof
    depends on the sender (`ZoneOf != ""`, `InOperatorZone`, `ViaHost`).
  - `HostConflicts(viewIDs []string, sender string) map[string][]string`:
    for each host used by the bundle, the **other** sites of the sender on
    it (`HostAddresses`).
  - `FormRecipients(viewID string) []string`
  - `SiteBytes(viewID string) (int64, int, error)`: **the central byte
    function**. Today it is `store.Usage`; switch it to git's function after
    the rebase.
- Modify: `ext.SiteInfo` gains `Admins []SiteAdmin`.
- Create: `internal/httpapi/ownership.go`, implementing the interface on
  `siteService`. `TransferSite` and `AdoptSite` call `stampGrant` (Task 1)
  through the store callback, then record provenance:
  - new actions `ActionTransfer = "transfer"`, `ActionAdopt = "adopt"` and
    `ActionAdmins = "admins"` in `internal/provenance/provenance.go`;
  - the site line names both accounts.
- Modify: `ee/containers/manager.go`: export
  `func (m *Manager) ServicesOf(owner string) int`, wrapping the unexported
  `counts` (:673), for the limits check.
- Test: `internal/httpapi/ownership_test.go`, `ee/containers/manager_test.go`.

**Interfaces:**
- Consumes: Task 1.
- Produces: `ext.OwnershipSites`, `ext.SiteAdmin`, `SiteInfo.Admins`,
  `containers.Manager.ServicesOf`.

- [ ] **Step 1: Write the failing tests.**
  - `TestTransferSiteRevokesOldCredentials` (Review Focus 1):
    - an upload token issued before fails;
    - the old password fails even if it was cached in the last 5 minutes;
    - the new one works;
    - an account token of the old owner gets 403 on the site.
  - `TestAdoptSiteStampsLikeCreate`.
  - `TestCheckEditPasswordRateLimited`.
  - `TestResolveSiteForms`: view URL, edit URL, both ids; junk → false.
  - `TestWillNotMove`: a zone name, an operator-zone name and a ViaHost
    address are listed; a self-proven address is not.
  - `TestHostConflicts`.
  - `TestSiteBytesIsUsage`.
  - `ServicesOf` counts across projects.
- [ ] **Step 2: Run** the new tests. Expected: FAIL. **Step 3: Implement.**
  **Step 4: Run** the four Go commands. Expected: PASS.
- [ ] **Step 5: Commit** `feat: the extension can transfer, adopt and share sites through one core seam`.

### Task 3: The admin standing in the gate (core)

**Before starting:** send `sitebin-project-c6` the standing names, the
`actor.role` values and the owner-only list from Global Constraints.

**Files:**
- Modify: `internal/ext/ext.go`: an optional
  `SharingAccounts{ SharingAllowed(accountID string) (bool, error); AdminTags(accountID, viewID string) []string; SetAdminTags(accountID, viewID string, tags []string) error; SharedSiteIDs(accountID string) []string }`.
- Modify: `internal/httpapi/server.go`:
  - `tokenOwns` and `sessionOwns` become
    `tokenStanding(r, site) standing` and `sessionStanding(r, site) standing`,
    with `standing` ∈ {`standNone`, `standOwner`, `standAdmin`}.
  - **Admin** = an account in `Meta.Admins` whose `Until` is nil or in the
    future, **and** `SharingAllowed` is true for both the owner and the
    admin. An error refuses that one request and keeps the share.
  - `editAuth` puts `actor{…, role}` into the context, with `role` ∈
    {`owner`, `admin`, `password`}.
- Modify: `internal/httpapi/provenance.go`: `actor` gains `role`. `mcpActor`
  and the upload-token issuer (`mcpops.go:609-612`) record an admin as an
  admin, not as a password caller.
- Owner-only enforcement, with one helper
  `func (a *API) refuseUnlessOwner(w, r) bool`:
  - `deleteSite`: refused for an admin;
  - `writeSettings`: refused for an admin when `expires_at` is present in the
    request (per field, R3); the other fields in the same write still apply;
  - git delete: c6's route calls the same helper. Leave it a TODO comment
    only if git is not on main yet; it must be on main per Sequencing.
- `a.payloadFor(r, site)`: `sitePayload` plus `role`, plus `shared_by`
  (owner email, through a new optional `ext.AccountEmails` or the
  `SharingAccounts` provider). For an admin, `tags` comes from
  `AdminTags`. Replace the handler call sites (`sites.go:890,902,963,1011,1025,1052,1056,1137`,
  `containers.go:193`, `redirects.go:231,243,262,304`).
- **Tags written by an admin:** `writeSettings` with `tags` for an admin
  calls `SetAdminTags`, never `Meta.Tags`.
- **MCP:**
  - `openSiteAs` returns the standing;
  - `delete_site` and `update_site` with `expires_at` are refused for an
    admin with the same text;
  - `list_sites` appends `SharedSiteIDs(caller)` with `SiteSummary.Role = "admin"`
    (`json:"role,omitempty"`);
  - `siteResult` tags follow the caller.
- Test: `internal/httpapi/admin_gate_test.go`, `internal/httpapi/mcp_admin_test.go`.

**Interfaces:**
- Consumes: `Meta.Admins` (Task 1).
- Produces: `ext.SharingAccounts`, `standing`, `actor.role`,
  `refuseUnlessOwner`, `payloadFor`, `SiteSummary.Role`.

- [ ] **Step 1: Write the failing tests.**
  - `TestAdminMayDoOwnerThings`: an admin via token, session (the edit page
    shape with session **and** password headers, R16) and MCP can upload,
    change settings, add a domain and manage forms.
  - `TestAdminRefusedOwnerOnly`:
    - delete → 403 with the text;
    - `{"expires_at":…}` → 403;
    - `{"name":"x","expires_at":…}` → 403, with nothing written;
    - MCP `delete_site` and an `update_site` expiry → tool errors.
  - `TestExpiredOrPausedShareGrantsNothing`: `Until` in the past; owner or
    admin plan without sharing; a `SharingAllowed` error → none, and
    `Meta.Admins` unchanged.
  - `TestAdminTagsAreOwnInAPIAndMCP`.
  - `TestListSitesIncludesSharedWithRole`.
  - `TestProvenanceRecordsAdmin`.
- [ ] **Step 2: Run** the new tests. Expected: FAIL. **Step 3: Implement.**
  **Step 4: Run** the four Go commands, and the existing session and token
  tests unchanged. Expected: PASS.
- [ ] **Step 5: Commit** `feat: an admin of a shared site manages it like its owner, except what only an owner may do`.

### Task 4: Plan gate and owner truth (ee)

**Files:**
- Modify: `ee/eeconfig/eeconfig.go` `Tier` (:94-131): `Sharing bool`, json
  `sharing,omitempty`.
- Create: `ee/sharing.go`:
  - `SharingAllowed` implementing `ext.SharingAccounts`, modelled on
    `ee/zones.go:21-39`:
    - accounts mode → true for a live account;
    - tiers mode → `effectiveTierStrict(acc).Sharing`;
    - unknown account → false;
    - a suspended account → false.
  - `func (p *provider) ownedSiteIDs(acc *account.Account) []string` (R2).
- Modify, to use `ownedSiteIDs`: `owns` (`provider.go:574-586`),
  `renderDashboard` (`dashboard.go:740`), `restampSites` (`provider.go:678`),
  `suspend.eachSite` (`suspend.go:152`), `lockHolds` (`provenance.go:114`),
  the GDPR export (`gdpr.go:245`), `tagsInUse` (`dashtags.go:92`),
  `accountSites` (`tagsmanage.go:87`), and the `accounts.Delete` callback
  (`dashboard.go:522-571`, `gdpr.go:364`), which skips a site that meta says
  belongs to someone else.
- Optional: `stackreg.go` `tierFeatures` (:318-341) gains "Transfer and share
  sites" for tiers with `Sharing`.
- Test: `ee/sharing_test.go`.

- [ ] **Step 1: Write the failing tests.**
  - `TestSharingAllowedByMode`.
  - `TestStaleMarkerGrantsNothing` (Review Focus 1): with a marker for the
    old owner left behind and meta owner = the new owner, the old owner's
    dashboard does not list the site, rotate and delete give 403, a restamp
    skips it, and deleting the old owner's account leaves the site alone.
- [ ] **Step 2: Run** them. Expected: FAIL. **Step 3: Implement.**
  **Step 4: Run** the four Go commands. Expected: PASS.
- [ ] **Step 5: Commit** `feat: sharing is a plan feature, and meta.json decides who owns a site everywhere in the account pages`.

### Task 5: Offers, invitations, the share index and admin tags (ee storage)

**Files:**
- Create: `ee/ownership/store.go`, a new package `ownership`, `//go:build ee`.
  Files under `<data>/ownership/`, written atomically like `account.json`:
  - `offers/<id>.json`, holding `Offer{ID, Sites []string, From, ToEmail string, AvailableFrom *time.Time, AcceptBy time.Time, Created time.Time, State string /* open|paused|accepting|accepted|declined|expired|cancelled */, Done []string, Passwords map[string]string /* only between accept and the result page */}`;
  - `invites/<id>.json`, holding `Invite{ID, Site, From, ToEmail string, Until *time.Time, Created time.Time, State string}`;
  - `shares/<account>/<viewID>`, empty markers, like `LinkSite`.
  - Methods: create, get, update (with a mutate callback under a mutex),
    list by sender, by `ToEmail` (lowercased), by site, and for open items.
  - `DailyCount(account string, day time.Time) int` for the limit of 20.
  - **"A site is in at most one open offer"** is enforced here
    (`ErrSiteOffered`).
- Modify: `ee/account/tags.go`: `TagRegistry` gains
  `Sites map[string][]string \`json:"sites,omitempty"\``, the admin's own
  tags per shared site. `validate` cleans each list with `store.CleanTags`.
  The tag filter covers owned and shared sites alike.
- Implement `AdminTags`, `SetAdminTags` and `SharedSiteIDs` (Task 3's
  interface) on top of these.
- Test: `ee/ownership/store_test.go`, `ee/account/tags_test.go`.

- [ ] **Step 1: Write the failing tests.**
  - Offer and invite round-trip.
  - A site in two open offers → `ErrSiteOffered`.
  - Atomic writes survive a reread.
  - The daily count.
  - `Sites` tags validate and never touch `Meta.Tags`.
- [ ] **Step 2: Run** them. Expected: FAIL. **Step 3: Implement.**
  **Step 4: Run** the four Go commands. Expected: PASS.
- [ ] **Step 5: Commit** `feat: an account keeps its transfer offers, share invitations and its own tags for shared sites`.

### Task 6: Adopting an anonymous drop

**Files:**
- Create: `ee/adopt.go`:
  - `GET /account/adopt?site=<url or id>`: a signed-out visitor is sent to
    sign in and returned here, with the `site` value kept and never a
    password. A signed-in visitor gets the form, prefilled.
  - `POST /account/adopt` with `csrf`, `site`, `password`.
  - Rate limits from Global Constraints, per IP and per account.
- Order inside the POST:
  1. `SharingAllowed`; if false, render the page "Adopting needs Pro or
     Studio" with a link to `/account#plan` (R11).
  2. `ResolveSite`.
  3. `CheckEditPassword`.
  4. `AdoptSite(viewID, acc.ID, grantForAccount(acc))`. This includes the
     `max_sites` check through `grantForAccount`.
  5. `LinkSite`.
  6. Record provenance.

  Every failure from step 2 onwards gives the **one** exact text.
- Modify: `ee/templates.go`: an "Adopt an anonymous drop" form card on the
  dashboard, and the adopt page.
- Test: `ee/adopt_test.go`.

- [ ] **Step 1: Write the failing tests.**
  - Happy path: the site is owned, linked, stamped and listed.
  - A wrong password, an unknown URL, an owned site, a locked site, an
    expired site and junk all give exactly `URL or password wrong.`
  - The 11th attempt in an hour is refused.
  - Free or accounts-without-sharing → the plan page.
  - Signed out → login redirect keeping `site`.
  - No CSRF → 403.
  - **The edit password still works after adoption** (spec).
- [ ] **Step 2: Run** them. Expected: FAIL. **Step 3: Implement.**
  **Step 4: Run** the four Go commands. Expected: PASS.
- [ ] **Step 5: Commit** `feat: a paid account can take an anonymous drop into its account with the drop's URL and password`.

### Task 7: Creating and managing a transfer offer (sender)

**Files:**
- Create: `ee/transfer_send.go`, `ee/transfer_send_test.go`. Routes:
  - `GET /account/transfer`, the page;
  - `POST /account/transfer`, create;
  - `POST /account/transfer/{id}/cancel`;
  - `POST /account/transfer/{id}/dates`;
  - `POST /account/transfer/{id}/sites`, add or remove.
- Modify: `ee/templates.go`: a "Transfer" link in each owned site row and
  the transfer page. The page has:
  - checkboxes over `ownedSiteIDs`;
  - the helpers "every site on host …" and "every site tagged …"
    (query-link presets);
  - the notices: will-not-move addresses (`WillNotMove`), pending claims on
    moved hosts (R15), form recipients (`FormRecipients`), and that the edit
    password, tokens, tags and shares do not travel.

**Rules:**
- The sender's plan must allow sharing.
- The sites must be owned per meta, not locked, and not in another open
  offer (replacing one needs a confirmation naming the old bundle).
- The bundle is 1–50 sites.
- Dates as in Global Constraints.
- `HostConflicts` must be empty; otherwise show the conflicting sites with
  one "Add them to the bundle" button.
- The daily limit.
- The answer is **always** the never-reveal text, and the mail to a matched
  verified account is sent async (Task 10's helper).
- Test (Review Focus 5):
  - the answer is identical for a known and an unknown address;
  - no mail goes to an unknown or unverified one;
  - the 21st item in a day is refused;
  - each refusal rule fires.
- [ ] **Step 1:** Write the tests. **Step 2:** Run them (FAIL).
  **Step 3:** Implement. **Step 4:** Run the four Go commands (PASS).
- [ ] **Step 5: Commit** `feat: an owner offers one or more sites to another account by email, with a deadline`.

### Task 8: Accepting, declining and the result (recipient)

**Files:**
- Create: `ee/transfer_accept.go`, `ee/transfer_accept_test.go`. Routes:
  - `POST /account/transfer/{id}/accept`;
  - `POST /account/transfer/{id}/decline`;
  - `GET /account/transfer/{id}/done`, which shows the new passwords once
    and then clears them from the offer.
- Modify: the dashboard shows "Ready to take over" cards for offers to the
  account's verified email that are open, past available-from and before
  accept-by.

**The limits check** (`func (p *provider) transferCheck(offer, acc) []checkLine`)
is shown on the card and re-run on accept:
- the recipient's `SharingAllowed`;
- sites: owned count + bundle size ≤ `MaxSites`;
- container services: `ServicesOf(acc.ID)` + the bundle's services ≤
  `MaxContainers`;
- per site: bytes and files (`SiteBytes`), moving custom domains excluding
  zone names ≤ `CustomDomains`, and forms ≤ `MaxForms`;
- in accounts mode the caps are not checked (global caps still apply on
  writes).

A failing line names the site. A Free or no-sharing plan reads "Your plan
cannot take over sites" with `/account#plan`.

**Accept algorithm:**
1. Re-check the offer: open, dates, every site still owned by the sender per
   meta and not locked, `HostConflicts` empty, the sender not suspended,
   both plans allowing sharing, and the limits.
2. Set state `accepting`.
3. For each site in sorted view-id order:
   - `TransferSite(viewID, from, to, grantForAccount(to), WillNotMove(...))`;
   - `LinkSite(to)`, `UnlinkSite(from)`;
   - record both account logs;
   - add the site to `Done`;
   - keep its password in `Passwords`.
4. Set state `accepted`.
5. Mail both sides.
6. Redirect to `/done`.

`TransferSite` refusing a site mid-way (owner changed) stops the bundle with
a clear page. Sites already in `Done` stay transferred, and the background
loop finishes the rest only if the re-check still holds: forward completion
(spec), else the remaining sites stay with the sender and both are told.

Two accepts at once are serialised by the offer store's mutex and the state
check.

**Decline:** state `declined` and a mail to the sender.

- Test (Review Focus 1 and 2):
  - **Bundle happy path:** owner, index, stamp, new passwords shown once,
    the old owner locked out everywhere.
  - Refused for:
    - a Free recipient;
    - a site over a cap (named);
    - a site the sender removed from the bundle, or deleted;
    - an offer the sender cancelled;
    - a domain added on a shared host after the offer;
    - accept-by passed;
    - before available-from.
  - Concurrent double accept → one wins.
  - A crash simulated after 1 of 3 sites → the loop completes it.
- [ ] **Steps 1–4:** write the tests, run them (FAIL), implement, run the
  four Go commands (PASS).
- [ ] **Step 5: Commit** `feat: a paid account accepts a transfer bundle all at once, sees the new passwords once, and the sender loses every way in`.

### Task 9: Sharing with admins

**Files:**
- Create: `ee/share.go`, `ee/share_test.go`. Routes:
  - `POST /account/sites/{id}/share`, invite: `email`, `until`;
  - `POST /account/sites/{id}/share/{account}/end`, owner only;
  - `POST /account/invites/{id}/accept`;
  - `POST /account/invites/{id}/decline`;
  - `POST /account/sites/{id}/leave`, the admin leaving.

**Rules:**
- The owner's plan allows sharing.
- The site is owned per meta, not locked, and **not operator-owned**
  (`IsOperator(owner)` → refused with "Sites owned by the operator account
  cannot be shared").
- At most 10 admins (`ErrTooManyAdmins`).
- Lapse after 30 days.
- The daily limit; the never-reveal text; async mail.
- **On accept:** the recipient's plan allows sharing; then
  `SetAdmins(append {acc, until})` and the share marker; record both.
- **End or leave:** `SetAdmins(remove)`, remove the marker, drop the
  admin's `Sites` tags, record, mail.

**Dashboard (`ee/templates.go`):**
- a share form in the site row;
- the admins listed with "Admin · until <date>" and End;
- the invitations card;
- shared sites listed among the account's own, marked
  "shared by <owner email> · until <date>", with Manage and Leave, and the
  admin's own tags.

- Test:
  - invite, accept, then the admin can manage via session;
  - refused for: an operator-owned site, an 11th admin, a Free owner, a
    Free invitee at accept;
  - lapse;
  - end and leave remove access and the admin tags;
  - the never-reveal text is identical for known and unknown addresses.
- [ ] **Steps 1–4:** write the tests, run them (FAIL), implement, run the
  four Go commands (PASS).
- [ ] **Step 5: Commit** `feat: an owner shares a site with an admin account, optionally until a date, and either side can end it`.

### Task 10: Background loop, pauses and mail

**Files:**
- Create: `ee/ownership_loop.go`, started in `Init` (like `stackreg.go:208`),
  ticking every 5 minutes. It must be safe when `Init` was never called
  (`sitebin cleanup`): nil checks. Each tick:
  - mails the recipient once when an offer's available-from passes (or at
    creation, if it has none);
  - expires offers past accept-by and mails both sides;
  - lapses invitations after 30 days;
  - removes `Meta.Admins` entries whose `Until` has passed, and their
    markers and admin tags, mailing both sides;
  - completes `accepting` offers (Task 8);
  - unlinks stray index markers (R2).
- **Pause semantics** are computed, not stored:
  - an offer is `paused` while the sender's plan lacks sharing, any site is
    locked, or the sender or recipient is suspended. The card says so, and
    accept is refused;
  - shares grant nothing while paused (Task 3).
  - Nothing is deleted by a pause. The accept-by date keeps running.
- Create: `ee/ownership_mail.go` with
  `func (p *provider) mailVerified(email, subject, body string)`:
  - it looks up `ByEmail` and checks `EmailVerified`;
  - it runs `go p.mailer.Send(...)` only with a mailer;
  - it logs failures without the address.
  - Plain-text templates for: offer available, accepted, declined, expired;
    invitation; share accepted, ended, expired.
  - **The body names the site and the other party's email**, never a
    password.
- Test: `ee/ownership_loop_test.go`, with a fake mailer:
  - expiry, lapse and admin expiry;
  - a downgrade pauses and an upgrade resumes, with nothing deleted
    (Review Focus 4);
  - no mail to an unverified account;
  - the loop is nil-safe.
- [ ] **Steps 1–4:** write the tests, run them (FAIL), implement, run the
  four Go commands (PASS).
- [ ] **Step 5: Commit** `feat: offers and shares expire, pause and resume by themselves, and both sides are told by mail`.

### Task 11: GDPR, deletion, evidence and the operator register

**Files:**
- Modify: `ee/gdpr.go`:
  - **export:** add offers sent and received, invitations, and shares
    (sites the account is an admin of, and admins of its sites, giving only
    emails the account already knew);
  - **delete:** before `accounts.Delete`, end the account's shares on
    others' sites (`SetAdmins(remove)`). On a **locked** site, record a
    provenance line carrying the admin's account id and email. Cancel the
    account's open offers and invitations.
- Modify: `ee/dashboard.go` local account deletion: the same steps.
- Modify: `ee/provenance.go` `lockHolds` (:114): owns **or is an admin of**
  a locked site.
- Modify: `ee/admin.go` and `ee/adminprov.go` site detail (trail page): show
  the admins (email, until) and any open offer (to-email, dates, state).
- Test: `ee/gdpr_test.go`, `ee/ownership_gdpr_test.go`:
  - export carries the new parts;
  - deleting an admin account ends its shares;
  - a locked site keeps a provenance line with the id and email;
  - `lockHolds` extended;
  - the register shows admins and offers.
- [ ] **Steps 1–4:** write the tests, run them (FAIL), implement, run the
  four Go commands (PASS).
- [ ] **Step 5: Commit** `feat: shares and offers appear in the GDPR export, end with the account, and stay as evidence on a locked site`.

### Task 12: The edit page and the drop ticket

**Files:**
- Modify: `web/static/edit.html` and `edit.js`:
  - from `site.role`, when `"admin"`, show a banner after `#site-meta`
    (`Shared by <shared_by>` via `textContent`);
  - hide the expiry option (wrap it in an id) and the Danger zone (give it
    an id).
- Modify: `web/static/embed.js`: the anonymous ticket line with `session`
  becomes `Keep this site in your account.` plus `Take it into my account`
  → `${instance}/account/adopt?site=<view_url>` (R12). Never a password in a
  URL.
- Re-vendor `embed.js` in Task 15.
- Verify:
  - `node --check` on both files;
  - the four Go commands;
  - a controller browser check after Task 13: admin banner, hidden
    controls, the ticket link opening the prefilled adopt page.
- [ ] **Step 1:** Implement. **Step 2:** Verify. **Step 3: Commit**
  `feat: an admin sees whose site it is on the edit page, and a drop's ticket offers to take it into the account`.

### Task 13: E2E for ownership

**Files:**
- Create: `e2e/ownership.ps1` (ASCII; enterprise image).
  - Container env:
    - tiers mode with `tiers.json` `free` (no sharing) and `pro`
      (`"sharing": true`, generous caps), anon tier `drop`;
    - `SITEBIN_SMTP_*` pointed at a mailpit container (as `forms.ps1:87-92`
      does for the forms mailer);
    - local auth;
    - `SITEBIN_DOMAIN_VERIFICATION=off`;
    - port 8090.
  - Three local accounts (A, B, C) are **verified** through the verify link
    fetched from mailpit's API (R8). Set the tier with the dev selector or
    `SITEBIN_TIER_SELF_SELECT` as `tiers.ps1` does.
  - Steps:
    1. **Adopt:** A adopts an anonymous drop by URL and password.
    2. **Transfer:** A offers 2 sites to B's email. B (pro) accepts. The old
       password fails, the new one works, and A's dashboard no longer lists
       them.
    3. **Share:** B shares with C. C manages via session. C's delete is
       refused (403). C leaves.
    4. **Free:** C (free) cannot accept a transfer.
    5. **Mail:** mailpit received the expected subjects.
- Modify: `CLAUDE.md` "Commands": add `ownership.ps1` to the list and update
  the counts.
- [ ] **Step 1:** Write the script. **Step 2:** Build `sitebin:dev-ee` and
  run it: all pass, no containers left. **Step 3:** ASCII check.
- [ ] **Step 4: Commit** `test: e2e adopts a drop, transfers a bundle and shares a site between real accounts`.

### Task 14: Product docs

**Files:**
- `README.md`:
  - a "Site ownership" section: adopt, transfer (bundles, dates, what does
    not move, all-or-nothing), share (admin rights, owner-only list, until),
    plan flag `sharing`, mail;
  - MCP: shared sites in `list_sites` with `role`, and the admin's own
    token;
  - the enterprise config table: `sharing` in `tiers.json`, and
    `SITEBIN_SMTP_*` needed for notices.
- `CLAUDE.md`:
  - "Who may manage a site": the admin standing and the owner-only list;
  - "meta.json is the owner's truth" (R2);
  - the ownership seam.
- The spec gets a corrections block listing R1–R17 as decided.
- [ ] **Step 1: Commit** `docs: adopting, transferring and sharing sites`.

### Task 15: Website (branch `content/site-ownership`)

- **Pricing:** "Transfer and share sites" under Pro and Studio, not Free.
  Note the ship order.
- **`docs/using/`:** a section on adopt, transfer and share in plain words.
- **`docs/mcp/`:** `list_sites` role, and admin tokens.
- **`docs/embed/`:** the new anonymous ticket line.
- **Terms (`/terms/`):** replace "a drop is managed only through its claim
  ticket" with adopt, transfer (acceptance, recipient plan, what does not
  move) and sharing (owner stays responsible; the admin acts on the owner's
  behalf). **A new version date.**
- **Privacy:** what the other party sees (the email address).
- Re-vendor `embed.js`.
- Run the link check.
- Commit the terms and privacy separately: **the operator approves them**.

### Task 16: Ship (operator OK at each step)

1. Final whole-branch review and fixes. Before review, rebase onto main
   (git) and wire git's byte function and `quota_git` (see Sequencing).
2. Ask the operator for:
   - approval of the terms and privacy text;
   - the OK to merge, push and deploy;
   - the `tiers.json` change (`"sharing": true` on `pro`, `studio`,
     `admin`, `unlimited`);
   - `SITEBIN_SMTP_*` (mail01) in the compose env;
   - the new terms version declared to the stack (the consent gate asks
     every user once).
3. Merge and push.
4. Deploy as before: roots check, rollback tag, snapshot, `tiers.json` and
   compose backups, then the edits, then `compose up`. Keep 3 rollback
   images and prune.
5. Live check:
   - adopt a fresh anonymous drop with the operator's account;
   - share a site between two operator-controlled accounts if available;
   - logs clean.
6. Website push after the operator's approval; verify.
7. Tell `sitebin-project-c6` (and `b9` if the edit page structure changed).
   Update memory.

## Git integration names (binding, from `sitebin-project-c6`, 2026-11-04)

- **Bytes:** `func (s *Store) SiteUsage(site *Site) (store.Usage, error)`,
  with `type Usage struct{ FileBytes, GitBytes int64; Files int }` and
  `func (u Usage) Bytes() int64` (files plus repo).
  - `ext.OwnershipSites.SiteBytes(viewID)` uses `SiteUsage(...).Bytes()`
    and `.Files`.
  - The old `Store.Usage` stays files-only, for the container stop.
  - `ext.SiteInfo` gains `GitBytes`.
- **Gate:** git extracts `func (a *API) accountStands(site *store.Site, accountID string) bool`,
  today only the owner comparison. It is used by `tokenOwns`, `sessionOwns`
  and `gitAuth` (`internal/httpapi/gitauth.go`). Task 3 replaces it with
  the standing logic (owner or admin) and sets `role` on git's actor too
  (surface `git`).
- **Owner-only:** `DELETE /api/sites/{id}/git` carries
  `// TODO(ownership): refuseUnlessOwner`. Task 3 wires it.
- **Stamping:** `m.QuotaGit = grant.Git` sits in `createSiteWith`'s stamp
  block beside `QuotaWebDAV`; Task 1's `stampGrant` takes it over. Also
  `store.Quota.Git` (in `ApplyQuota`) and `ext.CreateGrant.Git *bool`.
- **Sweep:** `reconcileGit` writes only `quota_git` back, through
  `Store.SetQuotaGit`, like `reconcileTrust`.

## Git handoff notes (from `sitebin-project-c6`, 2026-11-04, branch e0644c2)

Git lands on main first; this branch rebases onto it. The names above are
confirmed unchanged. Three additions follow from c6's gate notes:

- **Task 1:** `TransferOwner` calls `s.stopGitWrite(site.ViewID)` under the
  site lock after its write, as `Delete` and `DeleteGitRepo` do
  (`internal/store/gitwrite.go`). A push the old owner started never
  finishes into the new owner's site.
- **Task 3:**
  - `gitPush` (`internal/httpapi/gitserve.go`, right after `BeginGitWrite`)
    re-reads meta and refuses when the owner or the edit-password hash
    changed since `gitAuth`. This closes the window between auth and
    registration; it does not run the password hash again.
    - The refusal is a git ERR pkt-line through `gitrepo.WriteRefusal`,
      with the advertisement already started, like gitPush's other
      refusals. A plain 403 makes git show "unexpected disconnect".
    - The check sits right after `BeginGitWrite`, before `Init` and
      `ServeRPC`. The info/refs path (`gitserve.go` ~231) also registers
      briefly for `Init` on a writable site with no repo, and gets the same
      check, so the old credential never creates the new owner's empty
      repo.
    - The owner path is pinned by `TestGitDeleteRepository*`
      (`internal/httpapi/gitapi_test.go`) and the e2e check "delete
      repository answers 204".
  - Standing covers every place that decides it: `editAuth` (whose actor
    is `OwnerAccountID`), `accountStands` (also used by `mcpops.go`'s
    actor and `gitAuth`), and ee `p.owns` (`dashboard.go`, `dashtags.go`).
- **Credentials:** git resolves the credential from its shape (`sbp_` is a
  token, a JWS is OAuth with the git marker, anything else is the edit
  password, `sbu_` is refused). Rotating the password and moving the owner
  therefore cut git access with no extra hook.
