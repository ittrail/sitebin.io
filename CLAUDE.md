# Sitebin — product repo

Drop files, get a website. Go backend + Caddy in one container, no database, no
Node build step. Open-core: MIT core, ELv2 `ee/`.

Workspace-level context (how this repo relates to the website repo, ship order,
shared conventions): [`../CLAUDE.md`](../CLAUDE.md).

## Commands

```bash
go build ./...                       # community build
go build -tags ee ./...              # enterprise build
go test ./...                        # core suite
go test -tags ee ./...               # enterprise suite — run BOTH, they differ
go vet ./...
go run ./cmd/sitebin caddyfile       # inspect the generated Caddyfile
powershell -File e2e/e2e.ps1         # the CORE E2E only -- see below (Windows host)
docker build -t sitebin:latest .     # community image; runs go vet + the full suite
docker build --build-arg EDITION=enterprise -t sitebin:latest-ee .   # enterprise image
```

**`e2e.ps1` is not the full E2E.** It is the core HTTP suite and references no
other script; there is no aggregate entry point. A full pass is all eight run
by hand: `e2e.ps1`, `spa.ps1`, `paths.ps1`, `ftp.ps1`, `mcp.ps1` (community
image), `accounts.ps1`, `tiers.ps1` (enterprise image), `license.ps1`.

They default to `-Image sitebin:dev` (`sitebin:dev-ee` for `accounts.ps1` and
`tiers.ps1`), tags nothing in this repo builds. Tag them yourself, and build the
enterprise one with `--build-arg EDITION=enterprise` -- without it the tag holds
a community binary with no accounts, and the enterprise scripts fail in ways
that look like product bugs.

`e2e/license.ps1` is self-contained: `e2e/mintlicense` (a `//go:build ee` tool,
`go run -tags ee ./e2e/mintlicense`) generates a throwaway root and mints the
licences, and the image is built with that root through the Dockerfile's
`LICENSE_ROOTS` build arg. No stack, no secrets. `e2e/stack/` is the compose
file for the half that does need a running SaaS Stack.

## Layout

- `cmd/sitebin` — entrypoint and the supervisor that runs Caddy alongside the Go
  server.
- `internal/` — the MIT core: `config`, `ids`, `auth`, `store`, `viewer`,
  `caddygen`, `httpapi`, `mcp`, `cleanup`, `ftp`, `supervisor`, and `ext`.
- `ee/` — the enterprise extension (`account`, `authn`, `billing`, `eeconfig`,
  `licensing`, `session`, `smtp`). **ELv2, not MIT.**
- `web/` — embedded UI, vendored viewer libraries, `static/embed.js`.
- `docs/superpowers/{specs,plans}/` — design docs and implementation plans.

## The two rules that shape this codebase

**1. The filesystem is the database.** One folder plus `meta.json` per site,
symlink indexes for lookups, everything durable under the single `/data` volume.
There is no schema, no migration step, and no second writer — a change to
`store.Meta` is a change to on-disk data that older sites will not have. Use
`omitempty` and treat a missing field as its zero value.

**2. `internal/ext` is the only seam between core and enterprise.** The core has
no compile-time reference to any `ee/` package; `ee` registers a `Provider` in an
`init()` guarded by the `ee` build tag, so the community binary does not contain
the code at all. When core needs something the extension knows (an owner's tier,
say), the answer is a new method on `ext.Provider` or `ext.SiteService` — not an
import.

Corollaries worth stating, because they have been violated before:

- **Nothing on the hot path asks the extension.** Quota caps are *stamped* into
  `meta.json` at creation precisely so an upload, a WebDAV write or an FTP
  transfer never has to resolve a tier. Resolving live would defeat the seam.
- **The community build must stay whole.** Every seam addition is inert with no
  provider registered — and the community path needs its own test.
- **Never act destructively on an error.** `Provider.QuotaFor` returning an error
  means the tier is *unknown*, not "unlimited" and not "expired". The cleanup
  sweep keeps the site and retries. A site kept too long is recoverable; a
  deleted one is not.

## Tiers, quotas and lifetimes

The area with the most subtlety, and where the current unmerged work sits.

- A tier grants `QuotaBytes`, `QuotaFiles`, `QuotaExpiryDays`, `QuotaDomains`,
  `QuotaWebDAV`, stamped onto the site from the `CreateGrant`. `0` means
  unlimited / inherit the instance global.
- `store.Meta.ExpiryFromTier` records **who chose the expiry date** — the plan or
  the owner. Almost every rule below keys off it, so any code that sets or moves
  `ExpiresAt` has to say where the date came from. An explicit `expires_at`
  through the API or the edit page clears it.
- **Sliding renewal:** a content change pushes a tier-imposed expiry out to
  `now + cap`. It never touches an owner-chosen date, and never pulls an expiry
  *closer* — a one-minute no-op window keeps a multi-file upload to a single
  `meta.json` write.
- **Restamping on tier change:** clamp only when the cap actually *shrank*; a
  cap that grows carries a tier-imposed expiry out with it; clamping never
  relabels an owner's date as tier-imposed. A downgrade stamps a 30-day grace —
  a named constant, deliberately longer than the new tier's cap.
- **The cleanup sweep is the last line of defence** for a late upgrade: before
  deleting an expired owned site it re-checks the owner's current caps.

Read `docs/superpowers/specs/2026-08-12-tier-change-quota-sync-design.md` before
touching any of it — including its "Corrections (post-implementation)" blocks,
which record three rules that review had to fix after the fact. Tiers themselves
are configured per instance (`SITEBIN_TIERS` / `tiers.json`), not in this repo.

## The MCP server

`internal/mcp` is the protocol and the tool catalog; `internal/httpapi/mcpops.go`
is the adapter that implements `mcp.Ops` over the JSON API's own helpers. The
split is the point: **no authorization rule is stated twice**. If MCP and the
API ever disagree about who may do what, the bug is in the adapter, not in a
second copy of the rule.

- MCP is **core, not `ee/`** — for the same reason the API is. Accounts are the
  enterprise part, and they gate MCP from outside through `ext.Provider`.
- MCP is deliberately stricter than the API in one place: it never takes the
  `fromOwnBrowser` escape hatch, because an MCP client is never one of
  Sitebin's own pages.
- The SDK's DNS-rebinding guard is **off on purpose** (`DisableLocalhostProtection`).
  It refuses any non-loopback `Host` on a loopback listener, which is every
  deployment behind Caddy. `CrossOriginProtection` replaces it and is the
  defence that actually applies here. Do not "fix" this by re-enabling it.
- `store.Meta.Origin` is provenance only. Nothing gates on it, and nothing
  should start to without saying so in the design doc.
- **OAuth is opt-in and Sitebin is only ever a resource server.** With
  `SITEBIN_MCP_OAUTH_ISSUER` unset, none of it is mounted. Sitebin never issues
  a token, registers a client or shows a consent screen; it points at any
  issuer. Do not add an authorization server here — that is what keeps "one
  container, no dependencies" true.
- **Empty scopes mean unrestricted**, because that is what an account API token
  has always granted. An OAuth token with no `scope` claim gets a placeholder
  that matches nothing, so "the issuer told us nothing" never reads as
  "everything".
- **The audience check is not optional.** It is the only thing stopping a token
  minted for another resource server on the same issuer from working here.

Read `docs/superpowers/specs/2026-08-28-mcp-server-design.md` and
`2026-08-29-mcp-oauth-resource-server-design.md`.

## Billing: three backends, one seam

`ee/billing` sells a paid tier three ways — Stripe direct, Paddle direct, and
the SaaS Stack's PayGate. `SITEBIN_BILLING` selects **exactly one**; `provider`
holds a single `billing.Backend`, so an unselected provider is absent rather
than merely unrouted.

- **The agnostic rule, which outranks convenience:** if the stack changes
  payment provider, *nothing in Sitebin may change* — no config, no code, no
  redeploy. The PayGate backend therefore names no processor in code, config,
  routes or UI, and sells by tier **name**; the stack resolves that to its own
  price id. Do not add a "which provider is the stack using" lookup, however
  useful it looks.
- **Customer-facing routes are provider-neutral** (`/account/upgrade`,
  `/account/billing/portal`). Only webhook paths carry a provider name, because
  the provider decides where it delivers — and PayGate mounts none.
- **`TierSource` and `WebhookReceiver` are optional interfaces on purpose.** The
  two models genuinely differ: with a direct provider Sitebin owns the
  subscription and hears about changes by webhook; with PayGate the stack owns
  it and `effectiveTier` polls. Compile-time assertions in `billing.go` keep
  PayGate out of `WebhookReceiver`. Making either universal would force one side
  to fake it.
- **`BillingEnabled()` means "a backend is selected", including PayGate.** It
  does *not* mean "Stripe/Paddle are configured" — that is `cfg.Billing != nil`.
  This already caused a false warning on every PayGate start; check the right
  one.
- **Ambiguity is a startup error.** Two backends configured with no explicit
  `SITEBIN_BILLING` refuses to boot. The old code silently preferred Stripe.
  Which processor charges customers is not a thing to infer from the
  environment.
- **PayGate knows people by the stack's identity** (the Keycloak user UUID,
  which is the OIDC subject). A local account has no subscription there and
  never will, so `SITEBIN_LOCAL_AUTH=false` is the recommendation on a stack
  instance.
- **Tier prices differ by backend.** Direct backends read `price.stripe` /
  `price.paddle`; PayGate needs `price.monthly` / `annual` / `currency`
  **amounts**, because the stack creates the product and has no field for a
  hand-made price id. A tier with no amount creates no payment product.
  Amounts are instance configuration — they belong in `/opt/sitebin/tiers.json`,
  never in this repo.

Read `docs/superpowers/specs/2026-08-29-billing-through-the-stack-design.md`.

## Enterprise licensing

A license is **four** base64url segments — `<certPayload>.<certSig>.<licPayload>.<licSig>`
— issued by the SaaS Stack, which is the certificate authority: one root per
stack, one signing key per registered app, and the root-signed certificate
travels inside the license string. `ee/licensing` verifies it offline; there is
no license server and there never was.

What is easy to get wrong:

- **The trusted roots are a LIST, baked in at build time** (`-ldflags -X
  …/ee/licensing.trustedRootsB64=`). A list, so a root can be rotated without
  redistributing every binary. `SITEBIN_LICENSE_ROOTS_DEV` overrides them for
  **development only** — anything that can set it can mint itself a licence.
- **Signatures cover the ENCODED segment, not the JSON it decodes to** — the
  JWT convention. Verifying the decoded bytes instead would make the check
  depend on both sides serialising JSON identically (key order, spacing,
  escaping), and any disagreement looks exactly like a forgery. This was
  already got wrong once: no stack-issued licence verified until it was fixed.
- **The audience check is not optional.** `licPayload.app_id` must equal the
  certificate's *and* equal `"sitebin"`. Same reasoning as the MCP one.
- **Startup NEVER fails on a licence problem.** Absent, malformed, unverifiable
  and expired keys are all logged and shown in the account UI.
- **A malformed or unverifiable key is `none`, never `expired`.** A config
  mistake must not punish harder than having no licence.
- **Four states** — `licensed`, `grace` (loud notice), `expired` (notice + no
  new sites or drops), `none` (as licensed for a 90-day trial from the marker
  written under the data dir, then as expired). Plus `unknown`, which never
  restricts anything.
- **Enforcement lives at exactly TWO places, and they enforce different
  things.** The *state* — expired, or trial elapsed — is enforced only in
  `AuthorizeCreate`, via `licenseGate` (`ee/provider.go` → `ee/license.go`).
  The *entitlements* are enforced only in `CustomDomainsAllowed`, via
  `licenseAllowsAnotherDomain`. Neither is ever on the serving path, neither
  runs on updates to an existing site, and neither brings an expiry forward.
  Do not add a third: any new enforcement belongs inside one of these two.
- **`entitlements.max_custom_domains` is an instance-wide ceiling**, checked
  where a domain is *added* (`CustomDomainsAllowed`). Zero/absent = unlimited.
  It never removes a configured domain — it refuses only the next one — and the
  tier's own per-site cap still applies on top.
- **Staying current:** the instance collects its licence from the stack daily,
  caches it under the data dir and applies it without a restart.
  `SITEBIN_LICENSE_KEY` wins when set, for air-gapped installs.
- **The licence is its own credential on that call, and no other credential is
  ever sent.** A customer self-hosting Sitebin is *not* a registered app on our
  stack: no app id, no API key, and never the stack admin key, which acts on
  every app there is. The instance posts the licence it holds and the stack —
  which signed it — verifies it. An expired licence still authenticates, since
  a signature does not care about expiry and expiry is exactly when a renewal
  is wanted. With no licence at all it asks nothing: the first one arrives by
  email.

Read `docs/superpowers/specs/2026-08-30-licensing-through-the-stack-design.md`.

## Enterprise config

All caps and toggles are startup env vars (`SITEBIN_*`) — see the README's
"Enterprise configuration" table. Two that bite:

- `eeconfig` refuses to start if `SITEBIN_ANON_TIER` names a tier missing from
  the tier file.
- PayGate has **no webhook into Sitebin**; tiers are polled through
  `effectiveTier`. A plan change is only ever noticed at a request that already
  resolves the tier, so there is no "on tier change" hook to hang work on.

## Working here

- **Keep `e2e/*.ps1` pure ASCII.** The scripts are UTF-8 with no BOM, so
  PowerShell 5.1 decodes them as the system codepage: an em dash's third byte
  (`0x94`) becomes a curly closing quote and silently breaks string quoting for
  the rest of the file. The failure surfaces far from the cause — a
  `CommandNotFoundException` naming some innocent word. This has already cost
  two debugging sessions. Write `--`, not `—`.


- **Tests first**, and run both build tags — the `ee` suite covers paths the
  core suite cannot even compile.
- **Design doc before non-trivial code**, in `docs/superpowers/specs/`, with the
  task-by-task plan in `docs/superpowers/plans/`.
- **Licensing hygiene:** MIT and ELv2 code are separated by directory. Do not
  move `ee/` logic into `internal/`, and keep `ee/LICENSE` intact.
- Licence signing keys live on the **stack**, not in this repo: the root is
  generated at the stack's first bootstrap and its private half never leaves.
  Only the root PUBLIC key comes here, through the build pipeline.
- This repo is **public**. Internal business material belongs in the private
  website repo under `docs/internal/`.
