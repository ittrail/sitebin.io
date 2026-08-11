# Tier Changes & Quota Sync — Design

Date: 2026-08-12 · Status: Approved for implementation

A site's quotas are stamped into its `meta.json` when it is created, from the
grant the extension returns. Nothing ever restamps them. That was invisible
while every tier had the same (unlimited) lifetime; with Free capped at 7 days
and paid tiers uncapped, it breaks in both directions:

- **Upgrade Free → Pro.** Sites created on Free keep `QuotaExpiryDays = 7` and
  still expire seven days after their last content change, although Pro
  promises they live until deleted. A paying customer's back catalogue deletes
  itself. This is the direction that costs money and trust.
- **Downgrade Pro → Free.** Old paid sites keep `QuotaExpiryDays = 0` and never
  expire. A single month of Pro buys permanent hosting for everything created
  in it.

The bug predates the lifetime work (it arrived with the expiry-cap commit
`bd38b03`); the lifetime work is what makes it bite. The hosted instance must
not receive the new `tiers.json` until this ships.

## 1. Why the quotas stay stamped

`internal/store` is the MIT core and deliberately knows nothing about accounts
or tiers — stamping the caps into `meta.json` at creation *is* the boundary
between core and enterprise extension. Resolving a tier live would mean the
core asks the extension for the owner's plan on every upload, WebDAV write and
FTP transfer, on the hot path, through a seam built to avoid exactly that.

So the quotas stay stamped, and the fix is to correct *when* they are stamped
again.

## 2. The deletion guard — the upgrade direction

The only moment that happens whether or not the customer visits is the cleanup
sweep. That is where the protection belongs.

Before deleting an expired site that has an owner, `cleanup.Sweep` asks the
extension for the owner's current caps:

- **No provider registered** (community build) — delete, exactly as today.
- **Owner unknown to the extension** — the account is gone; delete.
- **Lookup failed** — do not delete. Skip the site and retry on the next
  sweep. A PayGate outage must never destroy data; a site kept too long is
  recoverable, a deleted one is not.
- **Current cap is `0` (unlimited) and the expiry came from the tier** —
  restamp the site's quotas from the current grant, clear `ExpiresAt`, keep the
  site. The upgrade arrived, late but in time.
- **Current cap is `0` and the expiry was chosen by the user** — the owner
  asked for this date. Delete.
- **Current cap is still positive** — the site genuinely expired under its
  plan. Delete.

Because the sweep runs on `SITEBIN_CLEANUP_INTERVAL` (10 minutes by default)
and expiry is followed by a 24-hour grace window before deletion, an upgrade
has a full day to land before anything is lost.

## 3. Tier sync on notice — the downgrade direction and the other quotas

Wherever the resolved tier is already being computed, compare it with the tier
stored on the account. When they differ, persist the new tier and restamp every
site the account owns. The call sites:

- the account dashboard render,
- `AuthorizeCreate` (before a new site is created),
- the self-select tier handler in `ee/dashboard.go`,
- the billing webhook in `ee/billingh.go`.

PayGate has no webhook into Sitebin — it is polled through `effectiveTier` — so
these are the moments a PayGate-driven change becomes visible. A delay here
favours the customer (their old caps are more generous than their new ones, or
their sites simply have not started their grace window yet), which is the right
direction for this error to fall.

Per site, the restamp writes the new `QuotaBytes`, `QuotaFiles`,
`QuotaExpiryDays`, `QuotaDomains` and `QuotaWebDAV`, then adjusts the expiry:

| New cap | Site's expiry | Action |
|---|---|---|
| `0` | from the tier | cleared — the plan no longer expires sites |
| `0` | chosen by the user | left alone |
| `> 0` | none | set to `now + 30 days`, marked as coming from the tier |
| `> 0` | from the tier, beyond the new cap | clamped to `now + cap` |
| `> 0` | chosen by the user, beyond the new cap | clamped to `now + cap` |
| `> 0` | within the new cap | left alone |

The 30-day figure is the downgrade grace: a customer who cancels keeps
everything for a month, sees the date in the dashboard, and gets it removed by
upgrading again. It is a named constant, not the tier's own cap — dropping a
hundred permanent sites to seven days would be a deletion in all but name.

## 4. Where an expiry came from

Half the table above depends on knowing whether a date was imposed by the plan
or chosen by the owner. Nothing records that today, and an upgrade that wiped a
deliberately chosen expiry would be its own bug.

`store.Meta` gains `ExpiryFromTier bool` (`expiry_from_tier,omitempty`), set
when:

- the tier cap stamps the default lifetime at creation,
- sliding renewal moves the expiry to `now + cap`,
- the downgrade grace stamps a date.

and cleared whenever a caller sets `expires_at` explicitly through the API or
the edit page.

## 5. Seam changes

Both additions are inert in the community build, which registers no provider.

- `ext.Provider` gains `QuotaFor(ownerAccountID string) (CreateGrant, bool, error)`
  — the caps the owner's current tier grants. `false` means the account is
  unknown; a non-nil error means the tier could not be determined and the
  caller must not act destructively. The enterprise implementation resolves it
  through the existing `effectiveTier` + `grantFromTier` pair, so PayGate,
  stored tiers and the default all keep working exactly as they do for
  creation.
- `ext.SiteService` gains `ApplyQuota(viewID string, g CreateGrant) error` — so
  the extension can restamp a site without importing the core store, the same
  way it already deletes sites and rotates passwords.

`internal/cleanup` gains a dependency on `internal/ext`, which it does not have
today. That is the one new coupling, and it is the same seam the rest of the
core already uses.

## 6. Website

The pricing FAQ answer "What happens if I cancel a paid plan?" currently
promises *"Nothing is deleted"*. With a 30-day grace that is no longer true.
Rewrite it to state the grace window, that the dashboard shows the date, and
that upgrading again removes it. `C:\Projects\Sitebin-Website`,
`public/pricing/index.html`.

## 7. Tests

`internal/cleanup`, covering each row of §2: anonymous expired site deleted;
community build (no provider) deletes; owner unknown deletes; lookup error
keeps the site and leaves its meta untouched; owner now uncapped keeps the site
with a cleared expiry and restamped quotas; owner still capped deletes;
user-chosen expiry on an uncapped tier deletes.

`ee`, covering §3: a resolved tier differing from the stored one persists and
restamps; upgrade to an uncapped tier clears a tier expiry and preserves a
user-chosen one; downgrade to a capped tier stamps the 30-day grace on a site
that had no expiry and clamps one that is beyond the cap; no difference means
no writes.

`internal/store` and `internal/httpapi`, covering §4: the creation stamp and
sliding renewal set `ExpiryFromTier`; an explicit `expires_at` clears it.

## 8. Out of scope

- Existing data. This ships alongside the greenfield lifetime change; no
  migration, no backfill.
- Storage and domain overages on downgrade. Those keep today's behaviour
  (the site stays online, uploads beyond the cap are refused) and the website
  already describes it correctly.
- Account-level API tokens, and any adoption flow for anonymous sites. Both
  remain out of scope, as in the lifetime design.
