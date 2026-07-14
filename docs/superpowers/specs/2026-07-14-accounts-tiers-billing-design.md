# Sitebin Accounts / Tiers / Billing — Design (open-core premium extension)

Date: 2026-07-14 · Status: Approved for phased implementation (autonomous goal session)

This adds **optional** accounts, tiers, quotas, OAuth, SMTP, and paid billing to
Sitebin as **premium (non-MIT) features** delivered via an open-core split. When
the premium extension is not built in or not activated, Sitebin behaves exactly
as it does today (open, no accounts). Every cap and toggle is set **at image
startup** (env / mounted config).

## 1. Licensing model (open-core, Portainer/GitLab style)

- The existing core stays **MIT**.
- Premium code lives in a dedicated **`ee/`** tree with its **own license**.
- A Go **build tag `ee`** includes it: `go build` → pure-MIT *community* binary;
  `go build -tags ee` → *enterprise* binary. Same repo, two images
  (`sitebin:latest` community, `sitebin:latest-ee` enterprise).
- The core exposes a small **extension seam** (`internal/ext`): interfaces the
  core calls if a provider is registered, with open/no-op behavior otherwise.
  `ee/` registers the real provider in an `init()` guarded by `//go:build ee`.
- **License choice (decided): Elastic License 2.0** for `ee/` —
  source-available and **perpetual** (never converts to open source). It
  forbids offering the software to third parties as a hosted/managed service
  and forbids circumventing the license-key functionality, while permitting
  self-hosting, modification, and the owner's own commercial use. Chosen over
  BSL 1.1 (which mandates conversion to open source after a Change Date) and
  over a fully proprietary license (Portainer-BE style). Confirm with counsel
  before public release. Protection is legal, not technical: because the `ee/`
  source is published, the license key is enforced by the license terms rather
  than by withholding source.
- **Runtime activation:** enterprise features additionally require
  `SITEBIN_LICENSE_KEY` (signed, offline-verifiable) so the enterprise image
  can ship publicly but only unlock with a key. Community image never contains
  `ee/` code at all (compile-time exclusion, not just a flag).

## 2. Modes (startup-configured)

`SITEBIN_ACCOUNT_MODE = open | accounts | tiers` (default `open`):

- **open** — today's behavior; extension inert even if built in.
- **accounts** — creating a site requires a logged-in account; sites gain an
  owner; optional per-account quota. Anonymous creation off (or on, per toggle).
- **tiers** — tiers defined at startup; per-tier caps; optional anonymous/free
  tier (`SITEBIN_ANON_TIER=<name>` or empty to require an account); optional
  user self-select (`SITEBIN_TIER_SELF_SELECT=true|false`); paid tiers gated by
  a payment provider.

Ownership is **additive**: owned sites keep their edit URL + edit password and
still work over the public API for agents. Accounts add a management layer,
they do not replace the edit-password path. Anonymous (ownerless) sites keep
working; toggling the mode never requires data migration.

## 3. Filesystem data model (no DB)

```
/data/
  accounts/
    <account-id>/
      account.json     # id, provider(local|google|microsoft), subject/email,
                       #   email_verified, password_hash (local), tier,
                       #   quota_override, billing{provider,customer,sub,status},
                       #   token_version, created_at, updated_at
      sites/           # symlinks → each owned site (mirrors edit-index pattern)
  account-index/
    email/<sha256(email)>            -> ../accounts/<id>   # local + linking
    oauth/<provider>:<sha256(sub)>   -> ../accounts/<id>   # OAuth identity
    billing/<provider>:<customer>    -> ../accounts/<id>   # webhook → account
```

Each site's `meta.json` gains optional `owner_account_id`. Same tmp-write+rename
atomicity, same symlink indexes, same per-entity lock map as the site store —
so "no DB" holds. Listing an account's sites is O(its own sites).

## 4. Sessions

Stateless **signed cookie** reusing `auth.TokenSigner`: payload
`account-id | token_version | exp`, HTTP-only, `Secure` when not HTTP-only,
SameSite=Lax, 30-day sliding TTL. Revocation (logout-all, password change) bumps
`token_version` in `account.json`. Dashboard forms carry CSRF tokens; OAuth uses
`state` + `nonce`.

## 5. Tiers & quotas (startup)

Tier definitions from a mounted `tiers.json` (structured) or compact
`SITEBIN_TIERS`:

```json
[
  {"id":"free","label":"Free","max_site_bytes":104857600,"max_files":100,
   "max_sites":3,"webdav":false,"custom_domains":0,"max_expiry_days":30,"price":null},
  {"id":"pro","label":"Pro","max_site_bytes":5368709120,"max_files":5000,
   "max_sites":100,"webdav":true,"custom_domains":10,"max_expiry_days":0,
   "price":{"stripe":"price_123","paddle":"pri_456","display":"€9/mo"}}
]
```

Per-account quota = Σ(owned-site usage) vs the account's tier cap (or
`quota_override`). Enforced at create/upload/domain-add. Anonymous tier applies
only per-site caps (cumulative anonymous usage isn't attributable). Tier caps
override the global `SITEBIN_MAX_*` for owned sites.

## 6. Auth providers

- **Local** — email + password (Argon2id, existing helper). Signup → optional
  SMTP verification. Password reset via SMTP token (or operator CLI when SMTP
  off).
- **Google / Microsoft** — OIDC via `golang.org/x/oauth2` + `github.com/coreos/go-oidc`.
  Store `provider:sub`; link to an existing email account when verified.
  Config: `SITEBIN_OAUTH_GOOGLE_CLIENT_ID/SECRET`,
  `SITEBIN_OAUTH_MICROSOFT_CLIENT_ID/SECRET/TENANT`; redirect
  `https://<base>/account/auth/<provider>/callback`. OAuth needs a real HTTPS
  base domain.

## 7. SMTP

`SITEBIN_SMTP_HOST/PORT/USER/PASS/FROM/TLS`. Uses for: email verification,
password reset, and billing/lifecycle notices (payment ok, payment failed,
tier downgrade on cancel, account deletion confirmation). Templated, plain-text
+ minimal HTML. Absent SMTP → verification/reset disabled, operator CLI used.

## 8. Billing (Stripe + Paddle, startup-configured)

- **Stripe** — Checkout Session for a tier's `price.stripe`; webhook
  (`/account/billing/stripe/webhook`, signature-verified) on
  `checkout.session.completed` / `customer.subscription.updated|deleted` →
  set/clear the account's tier + `billing.status`. `stripe-go` SDK.
- **Paddle** — Billing checkout for `price.paddle`; webhook
  (`/account/billing/paddle/webhook`, signature-verified) on
  `subscription.activated|updated|canceled`. HTTP + verified signatures
  (no heavy SDK needed).
- Config: `SITEBIN_BILLING_PROVIDER=stripe|paddle|both|none`, provider keys +
  webhook secrets. Payment success → tier upgrade; cancellation/expiry → revert
  to `SITEBIN_DEFAULT_TIER` (grace-period configurable). All price↔tier mapping
  from the startup tier config, never hardcoded.

## 9. Dashboard (`/account`, authenticated, EE-gated)

Signup/login (local + OAuth buttons for configured providers), then: list owned
sites (reusing the site payload), **rotate a site's edit password** (ownership
proves identity — a capability accounts newly unlock), per-account usage vs tier,
tier selection / upgrade (Checkout when paid), billing status + manage link,
and a **danger zone**: delete account → delete all owned sites (`store.Delete`
each) → remove account + indexes. Same dark UI system as the rest of Sitebin.

## 10. Security

Sessions signed + CSRF + secure cookies; OAuth state/nonce; webhook signature
verification (Stripe/Paddle) is mandatory; billing endpoints rate-limited;
password reset tokens single-use + short TTL; account enumeration avoided
(uniform responses); edit-password rotation and account deletion require a fresh
session; license-key verification offline (Ed25519). Premium code compile-excluded
from the community image.

## 11. Phasing (each independently shippable, TDD, tested like the core)

0. **Foundation** — open-core scaffold (`ee/`, license, build tags, extension
   seam), config for modes + tier parsing. ← this session start
1. **Accounts core** — account store, sessions, local auth, ownership, dashboard,
   edit-pw rotation, delete-account.
2. **Tiers + quotas** — enforcement, anon tier, self-select.
3. **OAuth** — Google + Microsoft.
4. **SMTP** — verification + reset + notices.
5. **Billing** — Stripe + Paddle.
6. **License gating + enterprise image/CI + E2E**.

## 12. Non-goals (v1)

Team/org accounts, per-site collaborators, usage-metered billing, SSO/SAML,
admin analytics UI. (Filesystem model + open-core seam leave room for these
later.)
