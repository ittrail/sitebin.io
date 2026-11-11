# Account storage cap

Date: 2026-11-11. Ordered by the operator on 2026-11-11 after spotting that the
pricing page promised Studio "500 sites · 5 GB per site" (2.5 TB in theory) for
€19 and Pro "100 sites · 1 GB per site" (100 GB) for €6, on a 48 GB disk.

## Problem

A tier caps bytes **per site** (`max_site_bytes`) and the **number** of sites
(`max_sites`), never what an account holds in total. The product has no notion of
an account total at all: `store.SiteUsage` measures one site, and every write
funnel checks that one site against its stamped `QuotaBytes`.

## Decision (operator, 2026-11-11)

- A new tier field **`max_account_bytes`**: the bytes all of an account's sites
  may hold together. `0` / absent = no account cap (per-site caps only, as
  today), the polarity of `max_site_bytes` and `max_sites`.
- Hosted values: **Free 100 MB** (one site may take the whole account),
  **Pro 10 GB**, **Studio 50 GB**. Per-site caps stay (100 MB / 1 GB / 5 GB):
  they bound one site and one upload. Drop is anonymous and has no account.
- **Container data counts.** It already does without extra work: a container
  writes into the site's `files/` (its database files included), which
  `SiteUsage` counts. What is new is that the container stop rule also enforces
  the account cap.
- What counts is exactly what counts against a site's cap today: files plus
  the Git repository (`SiteUsage().Bytes()`), over every site the account OWNS
  (`Meta.OwnerAccountID`). A site shared with the account as an admin counts
  for its owner, not for the admin. A **locked** site does not count: the
  operator holds it as evidence, and the owner can neither change nor delete it.

## Design

### The cap is stamped, like every other cap

The hot-path rule stands ("nothing on the hot path asks the extension"):
`Meta.QuotaAccountBytes` (`quota_account_bytes`, omitempty) carries the owner's
account cap on every site, stamped from `CreateGrant.MaxAccountBytes` wherever
the other caps are stamped — `stampCaps` (creation, transfer, adoption),
`quotaFromGrant` (restamp on a tier change), `cleanup.reconcile` (an expired
site's last check). `store.Quota` gains `AccountBytes`; every `store.Quota`
constructor must pass it, or `ApplyQuota` resets it to "no account cap".

A `tiers.json` edit changes no tier id, so no restamp runs. The sweep backfills
and follows it: `reconcileAccountBytes`, modelled on `reconcileGit`, writes only
that field when the owner's grant differs, and changes nothing on a lookup error
or an unknown tier. After the deploy and the `tiers.json` edit, every owned site
carries the cap within one sweep (10 minutes).

A write checks against the stamp of the site being written. While a restamp is
half done, two sites of one account may carry different caps; each write uses its
own site's, which is the value the plan had when that site was last stamped.

### The account total is an index in the store, kept fresh by what already happens

Summing an account's sites on every write would walk up to 500 sites per file
(WebDAV writes file by file). Instead the store keeps an in-memory index
`view id → (owner, bytes)` (`internal/store/accountusage.go`):

- **`SiteUsage` notes its answer.** It is the one byte function: every write's
  cap check, every size report (edit page, `get_site`, dashboard, the admin
  console) and the container runtime's tick (`Info`) go through it, so a site
  that is being used is always current in the index. An anonymous or locked
  site is dropped instead.
- **A full measure** (`Store.MeasureAccounts`) walks every owned site: at
  startup (in the background) and at the start of every sweep. On the hosted
  instance today that is 62 sites and 84,000 files, 0.16 s.
- **Delete, `ForceDelete` and `PurgeLocked` drop the site**; transfer and
  adoption (`commitOwnership`, `AdoptOwner`) move its entry to the new owner, so a
  site that changed hands counts for the new owner at once.
- **Near the cap, the figure is re-measured.** When what the account leaves this
  site (`cap − other sites`) is smaller than the site's own cap, the account side
  binds, and a figure older than 30 seconds could refuse a write that deleting
  files elsewhere made room for. The owner's other sites are then re-measured
  first (at most once per 30 s per account). Far from the cap a stale figure
  cannot decide anything, so nothing is walked.

The index is an approximation in one direction only: it can be LOW, never
permanently high. A low figure lets a write through that should not have gone:
two parallel uploads to two sites of an almost full account each see the other's
older figure, and the account ends up over by at most those uploads. For a
storage plan that is acceptable. A refusal is never based on a figure older
than 30 seconds.

After a restart, until the startup measure has finished (seconds), only the sites
touched since then are known; the account cap is softer for that window.

### One cap function for writes

`Store.ByteCap(site) (max int64, account bool)` is what every write funnel checks
against: `EffMaxBytes(site)` (the per-site cap, unchanged), or, when the site
carries an account cap and fewer bytes remain there, `cap − other sites` with
`account` true. It can be negative (the account is already over), which every
funnel already treats as "full".

- `EffMaxBytes` keeps its meaning — the per-site cap — and stays what reports show
  as "x of y per site".
- The funnels switch from `EffMaxBytes` to `ByteCap`: `SaveFile`, `ExtractZip`,
  `BeginReplace` (its budget is fixed there), the WebDAV PUT, FTP and the Git push
  allowance. The `byteChecks` census in `usage_test.go` gets a second table,
  `accountChecks`, which every one of them must join with a test that writes to
  one site of an account whose OTHER site fills it.
- A refusal because of the account is **`store.ErrAccountFull`** (its `Is` reports
  `ErrTooLarge` too, so every existing 413/507 path still applies), with its own
  words: API `413 "account storage limit exceeded: your sites hold X of the Y your
  plan allows in total"`, MCP "your account's storage is full …", WebDAV `507`
  with the same sentence, Git `storage full: your sites hold X of Y`.
- FTP keeps `MaxBytes` fixed at login; `UsedOutside`, asked at every check,
  adds what the account cap takes away (`EffMaxBytes − ByteCap`, at least 0), so
  the session's own arithmetic enforces the account.

### Containers: the stop rule enforces the account too

`ext.ContainerSite` gains `AccountCap` and `AccountOthers` (0 when the plan has no
account cap). `enforceStorage` stops a project when its files plus the
account's other sites exceed the cap, with "your sites use X of the Y storage
your plan allows in total, so this project was stopped; free some space and
press Start". The existing rule is unchanged: a project's own repository never
stops it, because the rule counts this site's files only. The other sites count
whole, repositories included: they are the account's storage.

A downgrade that leaves an account over its new cap therefore stops its
container projects, exactly as a downgrade below a site's own cap does today.

### Ownership: an adoption or transfer must fit

`limitsCheck` (adopt, transfer accept) gains one line for the whole bundle:
"Storage (all sites): your sites hold X, these add Y, and your plan allows Z in
total." It refuses like the other lines. The owned bytes are measured through
`SiteBytes` over `ownedSiteIDs` (not the index: the extension has no access to
it, and an accept is rare).

### Over the cap is not offline

As with a site over its own cap after a downgrade: every site stays online and
readable, deletes always work, and every write that adds bytes is refused until
the account is under its cap again. Nothing is ever deleted for being over.

### What people see

- **API** `GET /api/sites/{id}` (the edit page's settings read): `account_bytes`
  and `account_max_bytes`, only when the site carries an account cap. The edit
  page shows "Your account: X of Y in use across all sites" beside the site's own
  storage line.
- **MCP**: no new output field (clients cache output schemas and reject a new
  field until they reconnect); only the refusal text is new.
- **Workspace UI** (`ee/ui/app.js`): `wsCaps.account_bytes`; the Storage meter
  becomes "X of Y · each site up to Z" with a bar and the 85 % warning, and the
  site list's lede "21 of 500 sites, 4.8 GB of 50 GB in use."
- **Classic dashboard**: one line "Storage: X of Y in use across your sites."
- **The stack's plan lines** (`tierFeatures`): "N GB storage in total" before
  the per-site line, so the stack's order step shows it too.
- **Website** (after the deploy, ship order): pricing cards and table ("100 MB in
  total", "10 GB in total · up to 1 GB per site", "50 GB in total · up to 5 GB per
  site"), the downgrade FAQ, `/docs/` pages that state the caps, the terms if
  they name them, and `max_account_bytes` in the self-hosting tier docs.

## Not in scope

- A per-account file-count cap. Files stay capped per site.
- Counting analytics, provenance or snapshots: they are Sitebin's own data, not
  the customer's upload, and are bounded elsewhere.
- Pre-checking an upload's total size against the account before the body is
  read. The funnels refuse at the first byte past the cap, as they do for a site.
