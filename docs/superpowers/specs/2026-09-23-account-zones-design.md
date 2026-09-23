# Account zones: a customer's own wildcard zone, proven once

2026-09-23. Asked for by the operator right after operator zones shipped
("ab der Studio-Variante die Möglichkeit für den Account selbst, eine oder
mehrere Betreiber-URLs zu konfigurieren … als EE-Feature eine dynamische
Variante"). The rules below were agreed in chat the same day. One was changed
there: names in a customer's own zone do **not** count against the plan's
custom-domain cap. The decisions the conversation did not settle are listed
under **Decisions taken without asking**.

Read `2026-09-07-custom-domain-verification-design.md` first. This builds
directly on its claim/verify/sweep machinery, and on operator zones
(`internal/store/operatorzones.go`, `SITEBIN_OPERATOR_DOMAINS`).

## What the customer gets

A Studio customer who owns `kunde.at` points a wildcard at the instance
(`*.kunde.at CNAME app.sitebin.io`, or an `A` record) and adds `kunde.at` as a
**zone** on their account page. They prove it once with a TXT record:

```
_sitebin-zone.kunde.at  TXT  "sitebin-zone=<token>"
```

From then on, every name under `kunde.at`, including the apex, attaches to
any of **their** sites at once, with no record per name. **Every other
account is refused** in that zone. There is no limit on how many names they
use: names in their own zone do not count against the plan's per-site
`custom_domains` cap.

This is the operator-zones feature, made self-service. The env-configured
operator zones become one kind of zone: owned by "the operator" and
configured at startup rather than claimed.

## Why the zone needs its own proof

A per-name claim proves one name, with a TXT record or a CNAME to the site's own view host.
A zone claim has to prove the whole subtree belongs to one account. A CNAME
cannot do that: the wildcard `*.kunde.at → instance` is exactly what
everyone in the zone would share, and it says nothing about *which account*
owns it. Closing that gap is the point of zones. Only a record carrying the
account's token proves it, so the zone proof is TXT only.

## Rules

### Claiming a zone

- Only an account can hold a zone. A zone is an account's, not a site's.
- The zone name is a domain of at least two labels. It is normalized as a
  custom domain is, and a leading `*.` is accepted and dropped.
- The following are refused at claim time with `ErrBadDomain`:
  - Overlap in **either direction** with the instance's base domain, its view
    domain, any reserved domain, any operator zone, or any **verified** zone of
    another account. So `kunde.at` blocks `app.kunde.at` and vice versa.
  - Overlap with the claiming account's own verified zones. This avoids nested
    zones, which would only complicate revocation.
- A pending zone reserves nothing, exactly like a pending domain claim. Two
  accounts may both hold a pending claim on `kunde.at`. The first to prove it
  wins, and the other's claim is dropped when it is next checked, because it
  now overlaps a verified zone.
- Pending zone claims expire after 7 days, like domain claims.

### Verifying a zone

The zone verifies when both hold:

1. `_sitebin-zone.<zone>` carries `sitebin-zone=<token>`.
2. **No other account holds a verified domain inside the zone.** If one does
   (say a former agency's `shop.kunde.at`, proven by its own TXT record), the
   zone stays pending, and the account page lists the conflicting names. We
   never take a verified domain away from someone automatically. The
   conflict clears when that domain goes, whether it is removed or its own
   proof lapses through the ordinary revocation window.

The owner's own verified domains inside the zone are not conflicts. They keep
their own claims unchanged.

When the zone verifies, other accounts' **pending** claims inside it are
dropped. They reserve nothing and could now never attach.

### Names inside a verified zone

`store.verify` answers for a name `d` in a verified account zone:

- **The zone owner's site:** verified. No DNS query is made for the name.
- **Anyone else's site:** refused at `AddDomain` with `ErrBadDomain` ("reserved
  for the owner of kunde.at"). Nothing is recorded.
- **Anonymous sites:** refused, as for anyone else.

Before that, adding a name also asks the extension whether the owner's plan
still allows zones (see Gating). A downgraded account keeps what it has but
cannot add new names in the zone.

Names added in the owner's zone are **exempt from the per-site
`custom_domains` cap**. They are still counted by the **licence's
instance-wide domain ceiling** (`max_custom_domains`), because that ceiling
counts the domain index, and zone names are in it. See Decisions.

### Revocation

- The sweep re-checks each verified zone's TXT record on the same daily
  cadence as domains. After the proof has been **definitively absent for three
  days**, the zone is released. A lookup error never counts as absence.
- When a zone is released, whether by revocation, by the owner, or by account
  deletion, the names that relied on it **fall back to ordinary per-name
  verification**. A name that also has its own TXT record, or a CNAME to its
  view host, stays attached. A name without one enters the ordinary
  three-day revocation window and is detached after it. Nothing is detached
  on the spot, so an accidental "remove zone" is recoverable for three days.
- Account deletion (local or through the stack's GDPR order) releases every
  zone of the account. Its sites are deleted anyway.

### Precedence

The checks run in this order, and the first match decides:

1. Reserved and instance domains.
2. Operator zones (env).
3. Verified account zones.
4. Ordinary per-name proof.

Because zones never overlap each other (see Claiming a zone), at most one zone
can match a name.

## Gating (EE)

- A new tier field, `max_zones`, is added to `tiers.json`. As with
  `custom_domains` and `max_containers`, **0 or absent means none**. The hosted
  instance values are:
  - `studio: 3`
  - `unlimited: 1000`
  - every other tier: `0`
- A new optional extension interface is asserted like `ContainerProvider` and
  `OperatorAccounts`:

  ```go
  type ZoneAccounts interface {
      // ZonesAllowed returns how many zones the account's CURRENT plan
      // permits. An error means unknown: refuse the new zone or name, never
      // release an existing one.
      ZonesAllowed(accountID string) (int, error)
  }
  ```

  It is consulted in two places only:
  - when a zone is **claimed**, where `count(verified + pending zones) <
    max` is required;
  - when a **name is added** inside the owner's zone, where `max > 0` is
    required.

  It is never on the serving path, and never in the sweep. A plan that shrinks
  does not release zones. It only refuses new zones and new names, as the
  licence entitlement already does. The community build has no provider, so it
  has no account zones. Operator zones do not depend on this interface.

## Where it lives

- **Mechanism in the core store** (`internal/store/zones.go`), beside
  `operatorzones.go`. Zone claims, verification, the lookup "which zone owns
  this name" and the sweep step are all core code, like domain verification,
  because `store.verify` and `AddDomain` are core. The core knows an account
  only as the `OwnerAccountID` string it already stores on sites.
- **On disk:** one file per zone, `data/zones/<zone>.json`:

  ```json
  {"zone":"kunde.at","account_id":"…","token":"…",
   "requested_at":"…","verified_at":"…","last_ok_at":"…","failing_since":"…",
   "conflicts":["shop.kunde.at"]}
  ```

  - The same `omitempty` discipline as `meta.json` applies. There is no
    migration: an instance without the directory has no zones.
  - Looking up a name walks its parent labels (`a.b.kunde.at` → `b.kunde.at`
    → `kunde.at`) and stats each file. That costs O(labels) and needs no scan.
  - Listing one account's zones is a `ReadDir` of the directory. Zone counts
    are small, and this is only needed on the account page.
- **The operator-zone code is generalized, not duplicated.** A single
  `zoneFor(d) (zone, owner, ok)` answers for both kinds, and `verify` and
  `refuseOperatorZone` (renamed `refuseForeignZone`) use it.
- **Account UI (EE dashboard):** the account page gets a **Zones** section. It
  offers:
  - adding a zone;
  - the TXT record to create, plus the wildcard record to point here;
  - the status (pending, verified, conflicts listed, failing since);
  - the names in use, and which site holds each;
  - "Remove".

  It reaches the core through new `ext.SiteService` methods (`ClaimZone`,
  `Zones(accountID)`, `ReleaseZone`).
- **Edit page:** a name in the owner's zone attaches immediately and is shown
  as "via zone kunde.at". Names in someone else's zone get the refusal text.
- **API, MCP and tokens:** adding a *name* works through the existing site
  endpoints and the MCP domain tool, unchanged. It is a site operation.
  Managing *zones* is dashboard-only. An API token acts on sites and never on
  the account (the existing rule), so no token can claim or release a zone.

## Safeguards

- **Certificates stay per name, over HTTP-01/TLS-ALPN.** No wildcard
  certificate is involved, since that would need DNS-01 against the customer's
  DNS provider. On-demand TLS still issues only for **attached** names, so a
  request to `random123.kunde.at` triggers no issuance. The wildcard cannot be
  used to burn certificates.
- **ACME rate.** Unlimited names must not let one account exhaust the
  instance's own Let's Encrypt order budget (300 new orders per 3 hours per
  ACME account, shared by every customer). Newly attached zone names are
  therefore rate-limited per account:
  - The default is **50 per hour**. The setting is
    `SITEBIN_ZONE_NAMES_PER_HOUR`, where 0 means off.
  - The limit is a throttle, not a cap. The refusal says when to try again,
    and the total stays unlimited.
- **Case, trailing dots, IDN:** normalization is shared with custom domains.
  Punycode names are compared in their A-label form, as today.

## Website and docs (ship after the instance runs it)

- **Pricing:**
  - Studio lists "Own wildcard zones (up to 3), unlimited names in them".
  - The Pro and Studio custom-domain rows stay as they are.
  - The FAQ gets a short "What is a zone?" entry.
- **`/docs/custom-domains/`:**
  - a "Zones" section with the TXT record, the wildcard record and the
    conflict and revocation rules;
  - the existing operator-zones section is reworded as the self-hosting
    variant.
- **`/docs/enterprise/`:** `max_zones` in the tier table and
  `SITEBIN_ZONE_NAMES_PER_HOUR` in the env table.
- **Product README and CLAUDE.md:** same content as the website pages, plus
  the precedence list.
- **Terms:** no change needed. Custom domains are already covered, and a zone
  is a bulk custom-domain proof. This is flagged for the legal review that is
  already pending, not blocking.

## Testing

- **Store (core):**
  - claim, verify and attach, where the owner's names attach without DNS and
    the apex is included;
  - foreign and anonymous names are refused, and nothing is recorded;
  - overlap refusals in both directions, against every kind (base, view,
    reserved, operator, other account's verified zone, own verified zone);
  - two pending claimants, where the first prover wins and the loser is
    dropped;
  - a foreign verified domain in the zone keeps the zone pending with the
    conflict listed, and it clears once that domain is removed;
  - other accounts' pending claims in the zone are dropped on verify;
  - revocation after three days of absence; a lookup error changes nothing;
    released names fall back to per-name proof (one keeps a CNAME and stays,
    one without detaches after its window);
  - names in the zone are exempt from the per-site cap;
  - the rate limit refuses the 51st name in an hour, and a zero setting
    disables it.
- **EE:**
  - `ZonesAllowed` per tier;
  - a zone claim over the plan's count is refused;
  - a downgraded owner cannot add names but keeps the attached ones;
  - account deletion releases the account's zones;
  - an API token cannot reach the zone routes.
- **Community build:** no provider means no zone routes, and a zone file on
  disk is ignored for ownership. Operator zones still work.
- **E2E:** extend `e2e/accounts.ps1` with a zone claimed under
  `SITEBIN_DOMAIN_VERIFICATION=off`. The TXT path is unit-tested with the
  scripted verifier.

## Rollout

Follow the ship order:

1. Push the product.
2. Deploy to app.sitebin.io.
3. Add `max_zones` to `tiers.json` (`studio: 3`, `unlimited: 1000`).
4. Verify with a real zone on the live instance. `ittrail.at` itself can serve
   as the test zone, since the operator account holds the `unlimited` tier.
5. Push the website.

`SITEBIN_OPERATOR_DOMAINS=app.ittrail.dev` stays as it is.

## Decisions taken without asking

- **The licence ceiling still counts zone names.** The agreed exemption
  concerns the hosted *plan* cap. The licence's `max_custom_domains` is what a
  self-hoster bought (Team 25, Business 250). If zones bypassed it, a Team
  licence holder would get Platform by adding one zone. It is instance-wide
  and applies to the operator's own instance too, but there the licence is
  Platform, which is unlimited.
- **Pending zones reserve nothing.** The first to prove a zone wins. This is
  the same rule as for domain claims, and it stops someone blocking a zone
  just by typing its name.
- **Foreign verified domains block the zone** instead of being taken over.
  This follows the "never destructive" rule. Both parties proved DNS control
  at some point; the older proof is kept until it lapses by itself.
- **Releasing a zone falls back to per-name proof** instead of detaching at
  once, so a mistake is recoverable for three days.
- **No nested zones**, not even within one account, so revocation stays
  one-level.
- **An ACME throttle of 50 new names per hour per account.** It is a
  protection of the shared certificate budget, not a product limit.
- **Hosted values:** Studio 3 zones, and the unlimited/admin tier 1000.
