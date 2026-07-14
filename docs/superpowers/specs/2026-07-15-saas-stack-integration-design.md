# SaaS-Stack Integration (generic OIDC + PayGate tiers) — Design

Date: 2026-07-15 · Status: Approved by owner ("thats exactly what i had in mind")

Make Sitebin Enterprise a first-class app of the (soon-public, MIT)
[IT-Trail SaaS Stack](https://github.com/ittrail/saas-stack): users sign in
through the stack's Auth Gateway (SSO across the operator's whole app
portfolio) and their subscription tier comes from PayGate instead of
Sitebin's built-in Stripe/Paddle webhooks. Built as two orthogonal EE
features — **generic OIDC** (valuable on its own: Keycloak, Okta, Authentik,
Entra) and a **PayGate tier source** — so the stack is the blessed
combination, not a hard coupling. Included in every enterprise tier.

## 1. Generic OIDC provider (`ee/`)

- Env: `SITEBIN_OAUTH_OIDC_ISSUER` (enables the feature; any spec-compliant
  issuer — for the stack: `https://auth.<stack-domain>/api/v1/<app-id>`),
  `SITEBIN_OAUTH_OIDC_CLIENT_ID`, `SITEBIN_OAUTH_OIDC_CLIENT_SECRET`,
  `SITEBIN_OAUTH_OIDC_LABEL` (login-button text, default "SSO").
- `eeconfig.Config` gains `OIDC *GenericOIDC{Issuer, ClientID, ClientSecret,
  Label}`; `OAuthEnabled()` includes it. Validation: issuer must be an
  http(s) URL; client id required with issuer.
- `account` gains `OIDCProv Provider = "oidc"`; `authn.NewOIDC` registers it
  with the configured issuer (same go-oidc flow as Google/Microsoft; the
  existing filesystem index `oauth/oidc:<sha256(sub)>` works unchanged).
- Login page: provider buttons get display labels ({ID, Label} instead of
  bare provider strings) — google→"Google", microsoft→"Microsoft",
  oidc→configured label.

## 2. PayGate as tier source (`ee/`)

- Env: `SITEBIN_PAYGATE_URL` (e.g. `https://paygate.saas-stack.example.com`),
  `SITEBIN_PAYGATE_APP_ID`, `SITEBIN_PAYGATE_API_KEY` (all three required
  together), `SITEBIN_PAYGATE_CACHE_TTL` (default `5m`),
  `SITEBIN_PAYGATE_MANAGE_URL` (optional dashboard "manage subscription"
  link, e.g. the stack's account/pricing page).
- Client `ee/billing/paygate.go`: `GET
  {url}/api/v1/{app}/users/{userID}/subscription` with
  `Authorization: Bearer {api key}` (the stack's admin-by-user-id endpoint —
  no user JWTs stored). Response `{"data":{"tier":…,"status":…}}`. The tier
  is honored when status ∈ {active, trialing, past_due}; anything else (or a
  tier id not present in `tiers.json`) falls back. Successes cached per user
  for the TTL; failures cached 30 s so an outage can't hammer PayGate or
  stall site creation.
- Resolution (`provider.effectiveTier`): for accounts with provider `oidc`
  (their OAuth subject **is** the stack user id) and PayGate configured →
  PayGate tier; on miss/error → the account's stored tier → `DefaultTier`.
  Other accounts (local/Google/Microsoft) keep the stored-tier path
  untouched. Used by `grantForAccount` and the dashboard.
- **Convention: stack tier ids == `tiers.json` ids.** PayGate owns *which*
  tier a user has; `tiers.json` owns *what the tier means* (quotas). No
  mapping table (YAGNI; ids are operator-chosen on both sides).
- Dashboard: with PayGate enabled the Plan card shows the effective tier and
  a "Manage subscription" link (when `SITEBIN_PAYGATE_MANAGE_URL` is set)
  instead of Sitebin's own checkout/self-select forms.
- Init-time rules: PayGate requires `SITEBIN_ACCOUNT_MODE=tiers` (error
  otherwise); warns when generic OIDC is not configured (only `oidc`
  accounts resolve via PayGate); warns that `SITEBIN_TIER_SELF_SELECT` and
  built-in Stripe/Paddle checkout are ignored for PayGate-resolved accounts.

## 3. Docs & website

- Product README: env rows + a "SaaS-Stack integration" subsection under the
  enterprise configuration; compose example gains commented vars.
- Website: enterprise page gets a saas-stack section + EE-grid card
  ("Bring your own identity — SSO via any OIDC provider; saas-stack
  native"), tier table row "SaaS-Stack integration" ✓ on all paid tiers;
  new docs page `/docs/saas-stack/` (onboarding, env, tier-id convention)
  added to every docs sidebar; open-source page cross-links the sibling
  project. GitHub links 404 until the stack repo goes public — noted on the
  launch checklist.

## 4. Testing

eeconfig parse/validation tests; PayGate client against `httptest` (auth
header, status filtering, unknown tier, cache hit/expiry, failure caching);
provider-level test that an `oidc` account's create grant follows the
PayGate tier and falls back cleanly when PayGate is down. Both build
flavors green.

## 5. Non-goals (v1)

Credit metering via PayGate, checkout embedding (`<saas-pricing>`) inside
the Sitebin dashboard, stack-side auto-onboarding of Sitebin, storing user
JWTs, per-account provider mixing rules beyond the above.
