# Sitebin Enterprise against a running SaaS Stack

This directory wires a Sitebin Enterprise container to an **already running**
IT-Trail SaaS Stack, so the parts that cannot be tested offline — OIDC sign-in
through the Auth Gateway, tier resolution through PayGate, and collecting a
renewed licence from the stack — can be exercised against the real thing.

**This compose file cannot bring the stack up, and never will.** The stack is a
separate product in its own repo, with its own compose file and its own
bootstrap — and that bootstrap is where the licensing **root keypair** is
generated, which is precisely the thing Sitebin must not be able to produce.
Start the stack from the stack repo first, then point this at it.

For the offline half of licensing — licensed, grace, expired, none, an
untrusted root, and the custom-domain entitlement — use
[`../license.ps1`](../license.ps1). It mints its own throwaway root with
`e2e/mintlicense` and needs no stack at all. Everything here is the part that
harness deliberately does not cover.

## Running it

```bash
cp .env.example .env      # then fill it in
docker compose up -d --build
docker compose logs -f sitebin
```

The image is rebuilt from the repo root (`context: ../..`), so `go vet` and both
test suites run inside the build, as they do for every Sitebin image.

## What to fill in

| Variable | Where it comes from | Notes |
|---|---|---|
| `LICENSE_ROOTS` | `GET /api/v1/licensing/trusted-public-keys` on the platform API (admin key), `public_keys` joined with commas — or the admin portal's licence-root page | **Build arg, not runtime env.** Baked in with `-ldflags -X …/ee/licensing.trustedRootsB64=`. It is the LIST of every non-revoked root, so a root can be rotated without a rebuild. |
| `STACK_NETWORK` | `docker network ls` on the stack host | The compose file joins it. Discovery through the gateway hands back Keycloak's **internal** back-channel URLs (`http://keycloak:8080/...`), so the token and JWKS calls only resolve from inside the stack's own network. `extra_hosts` covers the front-channel names only. |
| `SITEBIN_OAUTH_OIDC_ISSUER` | the **realm** URL | e.g. `http://auth.saas.localtest.me:8080/realms/saas-stack`. This is the value every token's `iss` carries -- not the gateway. |
| `SITEBIN_OAUTH_OIDC_DISCOVERY_URL` | the **Auth Gateway** | e.g. `http://auth-gw.saas-stack.saas.localtest.me:8080/api/v1/<app-id>`. **This is what puts sign-in behind the consent gate.** The gateway serves the realm's document with `authorization_endpoint` pointed at itself; Keycloak's own endpoints bypass the gate entirely and do it silently. |
| `SITEBIN_OAUTH_OIDC_CLIENT_ID` / `_SECRET` | the client the registration created (`<app id>-app`) | the secret comes back as `auth.client.secret` on **every** registration, so one `POST /api/v1/apps` with the admin key — or one start of Sitebin with `SITEBIN_STACK_*` set — hands it over. Redirect URI is `<base>/account/auth/oidc/callback`. |
| `SITEBIN_PAYGATE_URL` | PayGate base URL | |
| `SITEBIN_PAYGATE_APP_ID` | the app id Sitebin is registered under | URL, app id and key must be set **together** or PayGate refuses to start |
| `SITEBIN_PAYGATE_API_KEY` | PayGate app key (`ssk_…`) | not the stack admin key. Returned **once**, by the first registration; if it is lost, revoke and reissue through the platform API's api-keys routes. |
| `SITEBIN_STACK_CONSENTS` | this deployment's own consent documents | an ordered list, `[{"key":"terms","version":"…","url":"https://…","title":{"en":"…"}},{"key":"dpa",…}]`. The documents the gate asks for after the platform's; without it users are asked for the platform's alone. Only meaningful with `SITEBIN_STACK_*` set. |
| `SITEBIN_STACK_LICENSING` | what a licence from this stack is worth | `{"graceMonths":3,"plans":{"team":{"max_custom_domains":25},…}}`; a plan absent from `plans` is unlimited |
| `SITEBIN_STACK_GDPR_SECRET` | `openssl rand -hex 24` | **required** with `SITEBIN_STACK_*`, at least 32 characters. The stack signs its deletion and export orders with it; the instance declares it with the registration and mounts `/account/gdpr/{delete,export}` only when it is set. |
| `SITEBIN_STACK_*` | self-registration | all three together or none; `SITEBIN_STACK_ADMIN_KEY` acts on **every** app on the stack, so it belongs only on an instance you operate yourself |
| `SITEBIN_MCP_OAUTH_ISSUER` | the realm issuer, optional | with it, `/mcp` accepts the stack's access tokens and the registration declares the MCP resource and scopes |
| `SITEBIN_LICENSE_KEY` | leave **empty** here | empty is the point: the instance then collects its licence from PayGate. Setting it wins and turns collection off — that is the air-gapped path. |

One port, everywhere. `SITEBIN_PORT` and the port in `SITEBIN_BASE_DOMAIN` must
agree, because the OIDC redirect URI, the MCP resource URL and the GDPR
endpoints are all built from the base URL and declared to the stack verbatim —
a registration made from one port and a container listening on another is a
sign-in that never comes back.

`tiers.json` in this directory is mounted at `/cfg/tiers.json`. It is instance
configuration and deliberately not the hosted instance's real file. For PayGate
a paid tier needs `price.monthly` / `price.annual` / `price.currency`
**amounts** — the stack creates the payment product itself and has no field for
a hand-made price id. A tier with no amount creates no payment product.

## What to look for once it is up

- `enterprise license state=…` on the first log line after start. With no
  `SITEBIN_LICENSE_KEY` and no cached key this is `state=none` — the 90-day
  trial — not `expired`.
- The instance posts to `<paygate>/api/v1/licenses/renew` presenting the licence
  it already holds and **no other credential**. With no licence at all it asks
  nothing: the first licence arrives by email. So to exercise collection, start
  once with a licence, let it cache under `/data/license/license.key`, then
  renew it on the stack and watch it apply without a restart. **Set
  `SITEBIN_LICENSE_REFRESH` first** (`5m`, or `30s` if you are impatient) --
  collection runs once a day by default, so without it "watch it apply" is a
  day-long experiment. It is ignored while `SITEBIN_LICENSE_KEY` is set.
- Sign-in goes to the Auth Gateway; `SITEBIN_LOCAL_AUTH=false` is deliberate.
  PayGate knows people by the stack's identity (the OIDC subject), so a local
  account has no subscription there and never will.
- **The consent gate.** With `SITEBIN_OAUTH_OIDC_DISCOVERY_URL` at the gateway
  and `SITEBIN_STACK_CONSENTS` declared, a user registering for the first time
  is stopped on a page the stack hosts and shown **three** documents -- the
  IT-Trail platform terms, then Sitebin's terms and its DPA, in the declared
  order -- before the callback ever fires. Sitebin renders none of it; if you
  see a Sitebin-drawn consent screen, that is a bug. A second sign-in asks
  nothing, and declining comes back to `/account/auth/oidc/callback` with
  `error=access_denied`. `../consent.ps1` drives exactly this against a live
  stack, with a throwaway app of its own.
- **The registration the stack holds.** `GET /api/v1/apps/sitebin` with the
  admin key shows `consents` with both documents, `gdpr` with the two URLs on
  this container's base URL, `licensing`, and `billing.tiers` from
  `tiers.json` with amounts on the paid ones.
- **Self-service.** Signed in, the dashboard's "Manage account" goes to
  `<issuer>/account/?referrer=sitebin-app` and "Manage subscription" posts to
  `/account/billing/portal`, which answers `303` to
  `http://auth.saas.localtest.me:8080/apps/sitebin/plan`. The danger zone
  sends a stack user to the console rather than deleting locally.
- **GDPR.** `verify.ps1` signs an export and a deletion the way the stack's
  `platform-api/src/routes/gdpr.ts` does and shows the account and its sites
  gone afterwards. The stack itself cannot place those calls on this testbed:
  its SSRF guard refuses a webhook URL that resolves to a private address,
  which every `*.localtest.me` name does. On a public host it calls them for
  real.
- A licence problem **never** stops the container. If it does, that is a bug in
  the licensing code, not in this compose file.

## Verifying it

```powershell
powershell -File e2e\stack\verify.ps1
```

Reads `.env` for the admin key, the GDPR secret and the port, and against the
running container asserts everything in the list above: the registration the
stack holds, a real sign-in of a throwaway user through the gate (all three
documents, recorded), the two self-service links, the portal redirect, a signed
export that lists the account and its sites, a signed deletion that removes
them, and that the deletion is idempotent. The throwaway user is removed at the
end.

## Cleaning up

```bash
docker compose down -v
```
