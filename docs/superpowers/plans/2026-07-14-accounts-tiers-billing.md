# Accounts / Tiers / Billing — Implementation Plan

> Executes the design at
> `docs/superpowers/specs/2026-07-14-accounts-tiers-billing-design.md`.
> All premium code lives under `ee/` (Elastic License 2.0, `ee` build tag). TDD; the community
> build must stay green at every step. Enterprise tests run with `-tags ee`.

**Foundation (done):** `internal/ext` seam, `ee/` tree + license, build-tag
edition wiring, provider Init at startup. Both editions build/test green.

## File structure (enterprise tree)

```
ee/
  LICENSE
  ee.go                     # provider registration (init, //go:build ee)
  config/econfig.go         # parse account mode, tiers, oauth, smtp, billing env
  account/store.go          # filesystem account store + symlink indexes
  account/account.go        # Account struct, account.json IO
  session/session.go        # signed-cookie sessions (reuse auth.TokenSigner)
  authn/local.go            # local signup/login/verify/reset (argon2)
  authn/oidc.go             # Google + Microsoft OIDC
  quota/quota.go            # per-account usage vs tier caps
  smtp/mailer.go            # SMTP send + templates
  billing/stripe.go         # checkout + webhook
  billing/paddle.go         # checkout + webhook
  billing/billing.go        # provider-agnostic plan state
  web/                      # dashboard templates + assets (embedded under ee)
  provider.go               # ext.Provider impl wiring the above (routes, gate)
  *_test.go                 # per package
```

The **core** gains only small, open-safe seams (already partly present):
- `store.Meta.OwnerAccountID` (optional field; ignored in community).
- `ext.Host` grows accessors as phases need them (Store handle, Signer).
- httpapi calls `ext.Get().AuthorizeCreate` before creating (no-op in community)
  and mounts `PublicRoutes()` on the public mux.

## Phase 1 — Accounts core (`accounts` mode, local auth)

1. **econfig**: parse `SITEBIN_ACCOUNT_MODE`, `SITEBIN_DEFAULT_TIER`,
   `SITEBIN_ANON_TIER`, `SITEBIN_TIER_SELF_SELECT`, tiers JSON. Tests: defaults,
   parse, validation (unknown mode, bad tier json, anon-tier-not-defined).
2. **account store**: `Create`, `ByEmail`, `ByOAuth`, `ByID`, `Delete`,
   `LinkSite`/`UnlinkSite`, `ListSites`; symlink indexes; per-account lock;
   atomic account.json. Tests mirror the site store's traversal/concurrency set.
3. **sessions**: `Issue(accountID)`, `Verify(cookie)`, `Revoke` via
   token_version. Tests: round-trip, tamper, expiry, revocation.
4. **local authn**: signup (argon2 hash, email index), login, change password.
   Tests: duplicate email, wrong password, rate limiting.
5. **ownership seam**: add `OwnerAccountID` to Meta; httpapi create consults
   `AuthorizeCreate`; owned sites list in dashboard. Community build: field
   unused, `AuthorizeCreate` nil → open.
6. **dashboard**: `/account` (login/signup), `/account/sites` (list, rotate edit
   password, open/manage), `/account/delete`. CSRF on POST. Reuse UI tokens.
7. **provider.go**: implement `ext.Provider` — `AccountsEnabled()` from mode,
   `PublicRoutes()` mounting the dashboard + auth, `AuthorizeCreate` enforcing
   login (Phase 1) and later quota (Phase 2).

## Phase 2 — Tiers + quotas
Enforce Σ(owned usage) vs tier cap at create/upload/domain-add; per-tier
webdav/custom-domain/expiry gating; anon tier per-site caps; self-select.

## Phase 3 — OAuth (Google + Microsoft)
`x/oauth2` + `go-oidc`; `/account/auth/{provider}` + `/callback`; state+nonce;
link by verified email; account.json provider fields.

## Phase 4 — SMTP
`smtp/mailer.go`; verification + reset tokens (single-use, TTL); lifecycle
notices. No SMTP → operator CLI (`sitebin account …`).

## Phase 5 — Billing (Stripe + Paddle)
Checkout for a tier's price; signature-verified webhooks flip
account tier + billing.status; cancel → revert to default tier after grace.
Price↔tier map from startup config. `stripe-go`; Paddle via verified HTTP.

## Phase 6 — License gating + enterprise image/CI
Ed25519 `SITEBIN_LICENSE_KEY` unlock; `Dockerfile` enterprise target
(`--build-arg EDITION=ee`); CI matrix builds both images; E2E for signup →
create-owned → dashboard → delete, and a Stripe/Paddle webhook simulation.

## Testing discipline
Every phase: unit tests (`-tags ee`), community build stays green, and the
existing 68-check E2E must still pass unchanged in `open` mode. New E2E added
in Phase 6 for the enterprise image in `accounts`/`tiers` modes.
