# Admin Console — Design

Date: 2026-08-27 · Status: Approved for implementation

An operator running a Sitebin instance has no way to see what is on it. The
dashboard shows an account its own sites; everything else — anonymous drops,
other accounts' sites, what is about to expire, how much disk is in use — is
visible only by reading `/data` over SSH. On app.sitebin.io that is 199 sites,
196 of them anonymous, most of them test residue nobody can find again.

This adds an enterprise-only admin console: instance-wide figures, a
searchable list of every site, and the two actions an operator actually needs —
delete a site, and set or clear its expiry.

## 1. Who is an admin

Two independent conditions, **both required**:

- the account's resolved tier carries `"admin": true` in `tiers.json`, and
- the account's email appears in `SITEBIN_ADMIN_ACCOUNTS` (comma-separated,
  compared case-insensitively against the normalized address).

Neither alone grants anything. The tier flag lets the plan source (PayGate, or
a stored tier) nominate an account; the environment variable lets the operator
of the container confirm it. A misconfigured stack cannot hand out instance-wide
access on its own, and an entry in the allowlist does nothing until a tier
carries the flag.

Emails are unique per instance — `ee/account` maintains an email index — so the
allowlist addresses exactly one account each.

The tier is resolved with the ordinary `effectiveTier`, which falls open to the
stored tier when PayGate cannot be reached. That is safe here precisely because
the allowlist is the hard gate: degrading to a stored tier cannot promote anyone
the operator has not already named.

`admin` is a flag on a tier, not a tier id, so any tier can carry it and the
capability stays independent of the quota bundle. The instance will define two
uncapped tiers: `unlimited` (quotas only) and `admin` (the same quotas plus the
flag). Neither is sold; neither is self-selectable.

For a caller who is not an admin, every route in this design answers **404**,
not 403. The console should not announce itself to people who cannot use it.

The community build registers no provider and has no tiers, so none of this
exists there.

## 2. What the console shows

One page at `GET /account/admin`.

**Figures**, computed from the same single pass that builds the list: total
sites; how many are account-owned versus anonymous; total bytes and files;
how many carry an expiry, and how many of those fall due within seven days.

**The list**, one row per site: view id, owner (email, or "anonymous"), mode,
size, file count, created, expiry, custom domains. Rows are sorted by creation
date, newest first.

**Search and filters** narrow the list: a text box matching view id, owner
email or custom domain, and a selector for owned/anonymous/expiring-soon. Both
are query parameters on the page's own URL and are applied server-side.

> **Correction (during implementation).** This section originally put the
> filtering in the browser. It cannot be: the dashboard's pages are served with
> `script-src 'none'`, so any script would silently never run. Server-side
> filtering is also the smaller change — a GET form and a `switch` — and it
> leaves the CSP untouched. The same discovery moved the delete confirmation
> from a `confirm()` dialog to a step the server renders (§3): a confirmation
> that never appears is worse than none.

At some scale rendering every row stops being reasonable. The threshold is not
worth guessing at now; when `All()` starts returning more than a few thousand
sites, the fix is server-side filtering with a cursor, and the seam method is
already the right place to put it. This design deliberately does not build that
in advance.

## 3. What the console can do

Two actions, both `POST`, both CSRF-protected like the rest of the dashboard:

- **Delete a site.** Reuses the existing `SiteService.Delete`.
- **Set or clear an expiry.** A date, or empty to remove the expiry.

Deletion is two steps: the Delete control links back to the list with the site
marked for confirmation, and that row becomes a prompt naming the site, its file
count and its domains, with a real POST form and a Cancel link. No script is
involved, for the CSP reason in §2.

Both actions are logged through `slog` at info level with the acting admin's account id, the
target view id, and what changed — an operator acting on sites that are not
theirs should leave a trail.

**What an admin cannot do:** see or rotate a site's edit password, open its edit
page, or otherwise act as its owner. The claim ticket remains the only thing
that confers ownership. The console lists a site's view URL — an admin can look
at what is being hosted, which is the point — but it never turns an operator
into a site's owner.

This is a real reduction of the "unguessable URL" property for whoever holds
admin: the console is, by construction, an enumeration of every view URL on the
instance. That is inherent to the feature and is the reason §1 requires two
independent conditions to reach it.

## 4. Seam changes

`ext.SiteService` gains two methods. Both are inert in the community build,
which registers no provider to call them.

- **`All() ([]SiteInfo, error)`** — every site on the instance. A thin wrapper
  over `store.AllSites()`, which already exists for the cleanup worker and
  already skips unreadable entries rather than failing the whole pass.
- **`SetExpiry(viewID string, at *time.Time) error`** — sets or (with `nil`)
  clears a site's expiry. Nothing in the core exposes this today; expiry dates
  are written only through `applySettings` in `httpapi`. Like that path, it
  clears `ExpiryFromTier`: a date an operator chose is not one the plan imposed,
  and leaving the flag set would let the next sliding renewal overwrite it.
  Returns an error wrapping `ErrSiteGone` when the site is already gone, matching
  `ApplyQuota`.

`ext.SiteInfo` gains `Owner string` — the owning account id, empty for an
anonymous site — and `Domains []string`, the site's custom domains, which the
console lists and searches by. SiteInfo describes an owner's view of their own
site today, where the owner is a given and the domains are on the edit page; the
instance-wide view needs both as columns. The console resolves an owner id to an
email for display, falling back to the raw id when the account is gone — an
orphaned site is exactly what an operator opened this page to find.

The dashboard shows admins one link to the console; for everyone else it is
absent, matching the 404.

## 5. Configuration

- `tiers.json` gains `admin` (uncapped quotas, `"admin": true`) alongside
  `unlimited` (the same quotas, no flag).
- `SITEBIN_ADMIN_ACCOUNTS` lists admin emails.

Both tiers set explicit large caps rather than zeros: `max_site_bytes: 0` and
`max_files: 0` fall back to the instance globals (100 MB / 1000 files), and
`custom_domains: 0` means *no* domains. Only `max_sites: 0` and
`max_expiry_days: 0` genuinely mean unlimited, because both are checked as
`> 0`.

## 6. Tests

`ee`, covering §1: an admin tier plus an allowlisted email reaches the console;
the tier alone 404s; the allowlist alone 404s; a non-admin 404s; matching is
case-insensitive; the community build has no route at all.

`ee`, covering §2 and §3: the figures count owned, anonymous, expiring-soon and
totals correctly across a mixed set; delete removes the named site and nothing
else; setting an expiry writes the date and clears `ExpiryFromTier`; clearing it
removes the date; both actions reject a request without a valid CSRF token; both
log.

`internal/httpapi`, covering §4: `All()` returns every site including anonymous
ones and survives an unreadable entry; `SetExpiry` sets, clears, clears
`ExpiryFromTier`, and reports `ErrSiteGone` for a missing site.

## 7. Out of scope

- Editing a site's files, settings or password through the console.
- Account management: listing, suspending or deleting accounts.
- Bulk actions. One site at a time; a "delete all expired" button is a much
  bigger promise than it looks.
- Server-side filtering and pagination (§2).
- Any admin capability in the community build.
