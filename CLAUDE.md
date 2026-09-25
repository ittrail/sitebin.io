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
other script; there is no aggregate entry point. A full pass is all eleven run
by hand: `e2e.ps1`, `spa.ps1`, `paths.ps1`, `ftp.ps1`, `mcp.ps1` (community
image), `forms.ps1` (community image; pulls `axllent/mailpit` as its SMTP
server), `accounts.ps1`, `tiers.ps1`, `containers.ps1` (enterprise image; the
last drives the host's real Docker Engine and pulls images), `license.ps1`, and
`consent.ps1` -- the last of which is the only one that needs a **running SaaS
Stack** (the stack's consent gate, the OIDC issuer/discovery split, and the
`consents` declaration; see `docs/superpowers/specs/2026-09-01-consent-gate-through-the-stack-design.md`).
`e2e/stack/verify.ps1` is the twelfth, run against the compose container in
`e2e/stack/` rather than one it starts itself: the registration the stack
holds, the stack-hosted self-service links, and a signed GDPR export and
deletion.

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
  `caddygen`, `httpapi`, `mcp`, `cleanup`, `ftp`, `forms`, `supervisor`, and
  `ext`.
- `ee/` — the enterprise extension (`account`, `authn`, `billing`, `containers`,
  `eeconfig`, `licensing`, `session`, `smtp`). **ELv2, not MIT.**
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

## Who may manage a site

`withEditAuth` (internal/httpapi/server.go) is the one gate for every per-site
API route. In order: an upload token is refused (it opens only its own route);
an account API token whose account owns the site; the owner's **browser
session** (`sessionOwns`, via the optional `ext.SessionAccounts`); the edit
password. `sessionOwns` is a real CSRF boundary — the session counts only with
`X-Sitebin-Session: 1` and a same-origin `Sec-Fetch-Site` when one is sent —
and must never be loosened into `fromOwnBrowser`, which is a forgeable plan
heuristic. MCP never reads the session, and the dashboard never reads a token.
See `docs/superpowers/specs/2026-09-25-owner-session-edit-design.md`.

## Custom domains prove ownership

A custom domain is attached — indexed, served, issued a certificate — only
once its DNS proves it belongs to the site: a TXT record with the claim's
token, or a CNAME at the site's own view host (`internal/store/domainverify.go`).
`CustomDomains` in `meta.json` stays the list of VERIFIED domains and is what
everything else reads; `DomainClaims` carries every claim, pending or not,
with its token. A pending claim is never indexed and reserves nothing. The
sweep re-checks claims and detaches a verified domain only after its proof
has been definitively absent for three days — a lookup error never detaches.
A verified domain with no claim record predates verification and is left
alone. Proofs are asked of the domain's AUTHORITATIVE nameservers
(`internal/store/authdns.go`), not the system resolver: every proof is first
looked up before it exists, and the hosting provider's resolver caches that
NXDOMAIN for an hour, which made "check now" useless. The system resolver is
only the fallback when no authoritative server answers. `SITEBIN_DOMAIN_VERIFICATION=off` is for trusted instances and the e2e
suite; the default is `dns`. `SITEBIN_OPERATOR_DOMAINS` names zones the
operator owns (wildcard-pointed here): inside them the proof is WHOSE site it
is, not DNS — the operator's (admin tier + allowlist, `ext.OperatorAccounts`)
attaches at once, anyone else is refused. It lives in `store.verify`, so claim,
sweep attach and daily re-check all agree; an unanswerable operator check is a
lookup error and changes nothing.

**Account zones** (`internal/store/zones.go`) are the self-service version:
an account proves a zone once (TXT `_sitebin-zone.<zone>`), and names under it
attach to that account's sites only, unlimited by the per-site cap. Verified
zones are `data/zones/<zone>.json` (found by walking a name's labels), pending
ones `data/zones/pending/<zone>~<account>.json` — a pending claim reserves
nothing, so there can be several. Zones never overlap; a foreign verified
domain inside keeps the zone pending rather than being taken; a released zone
drops its names back to per-name proof instead of detaching them. The plan
(`max_zones`, via the optional `ext.ZoneAccounts`) is asked only where a zone
or a name is ADDED — never in the sweep. Read
`docs/superpowers/specs/2026-09-23-account-zones-design.md`.

## Container sites

The third site mode (`store.ModeContainer`) runs the project its
`sitebin-container-compose.yaml` declares. Read
`docs/superpowers/specs/2026-09-22-container-sites-design.md` first.

- **The core routes, the extension runs.** No Docker code lives in
  `internal/`. `ext.ContainerProvider` is an *optional* interface, asserted,
  so the community build (no provider) simply has no container mode.
- **Desired vs observed state, two writers.** The core writes `enabled` and
  `restart_seq` in `meta.json`; only the runtime (`ee/containers.Manager`)
  writes the observed half, through `SiteService.SetContainerState`. The
  runtime re-applies whenever `(sha256(compose), restart_seq)` differs from
  what it last applied — that, plus a 5-second tick, is how an FTP or WebDAV
  write restarts a project. Do not add a "restart" command path around it.
- **authz is the router.** It answers a container site with
  `X-Sitebin-Upstream`, which Caddy copies onto the request and proxies to.
  The header is stripped from the client's request *before* forward_auth; a
  container site is never admitted without an upstream, so it never reaches
  the file server (its folders hold database files and passwords).
- **A permanent failure is recorded against the file; a transient one backs
  off.** A broken compose file or a plan over its cap is not retried until the
  file or the sequence changes. Docker and plan-lookup errors retry after a
  minute. An unknown plan starts nothing and stops nothing.
- **`max_containers` 0 means none**, like `custom_domains`. It counts running
  services across all of an account's projects.
- **No file surface follows a symlink out of a site.** Containers write into
  the site tree, so the store, WebDAV (`siteFS`) and FTP (`rootFs`) resolve
  through `os.Root`; listings, ZIP and usage count regular files only; leaving
  container mode purges every link before Caddy serves the tree; backup skips
  links out of the data root. Any new code that touches site files must go
  through `store.OpenContentRoot` or the store — never `os.Open` on a joined
  path.

## Site forms

A site's pages post plain HTML forms to `/_sitebin/forms/<key>` on their own
origin, and the core mails them to a recipient. Read
`docs/superpowers/specs/2026-09-24-site-forms-design.md` first.

- **All core, own mailer.** `internal/forms` is pure logic (rules, tokens,
  parser, MIME, SMTP, captcha); `internal/httpapi` wires it. The forms mailer
  (`SITEBIN_FORMS_SMTP_*`) is deliberately separate from `ee/smtp`: the two
  send different mail to different people.
- **The recipient consents, by POST.** A form is `pending` until its
  recipient confirms; GET on a confirm or stop link only ever shows a button,
  because mail scanners fetch every link. `seq` moves on a recipient change
  and on a stop, and that is what kills older confirmation links.
- **The cap is stamped, like custom_domains.** Submissions read `quota_forms`
  from `meta.json` and never ask the extension. With accounts enabled, an
  unstamped site has **0** forms (never the community default of 10; an
  enterprise binary in open mode gets the 10, like community), and every
  constructor of `store.Quota` must pass `Forms`, or `ApplyQuota` resets it.
  With accounts enabled, forms also need a trusted tier: a site without the
  trust marker has a cap of 0 whatever its stamp, because its CSP
  (`form-action 'none'`) leaves a form nothing to serve but a phishing page's
  own script.
- **Nothing is stored or logged.** Submissions are mailed synchronously (a
  failure is a 502 the visitor can retry) and never written down. Logs carry
  site, key, size and file count, never values, filenames or the recipient.
- **The submission mail is plain text.** No HTML part: Microsoft 365 put
  every HTML version of it — the claim-ticket look and three plainer
  redesigns — in Junk, and the nicest one in quarantine (which the recipient
  never sees) as soon as a visitor wrote an ordinary request for a quote;
  the text version arrived every time (spec, Corrections).
  `TestSubmissionMailIsTextOnly` guards it. Do not add HTML back without
  re-testing against a Microsoft 365 mailbox with a realistic, *new*
  message text (M365 fingerprints repeated bodies). The confirmation mail
  keeps its ticket look because it is delivered — leave it alone: a form
  cannot go live without it.
- **ALTCHA traps.** Always pass `DeriveKey` to `VerifySolution` (without it
  the library accepts on the signature alone), and keep the replay memory
  (the library has none). The widget is `web/vendor/altcha.min.js`; bump it
  and the Go library together.

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
- **Upload tokens (`sbu_`) are memory-only and single-site.** `open_upload`
  issues them (`internal/httpapi/uploadtokens.go`); only `/dav/{editID}/` and
  `POST /api/sites/{editID}/files` accept them, and `withEditAuth` refuses any
  `sbu_` credential before password work. A `sbu_` credential is never tried
  as an edit password (`verifyEditIP` refuses it first); on `/mcp` it is
  simply not a valid bearer — keep `uploadCredential` the single place that
  recognises one. Idle 5 min from the END of the last request, 60 min
  absolute. Read `docs/superpowers/specs/2026-09-25-mcp-upload-tokens-design.md`.
- **A replace is staged (`store.Replacement`).** `?replace=true` and
  `write_files` with `replace` stage in `<data>/tmp/replace-<viewID>-*` —
  never inside the site folder while the upload streams — and commit only
  when complete and within the caps; never call `ClearFiles` before an upload
  again. The commit, under the site lock, refuses a replacement with a failed
  write, answers `ErrNotFound` for a deleted site, moves the staging dir into
  the site folder, and only then empties and refills the content directory in
  place — it must never rename it, because container bind mounts point into
  it. One replace per site at a time: a second is `ErrReplaceBusy` (409).
  Should the commit fail after clearing began, the rest of the upload stays
  in `<site>/.replace-commit-*` until the site's next replace, which removes
  every one of them (the one-replace claim proves no commit is running), so
  they never pile up. `sitebin backup` skips those and `tmp/`.
- **`stats.json` has its own per-site lock** (`lockStats`), not the site lock:
  a page view must never wait for an upload holding the site lock. `Delete`
  takes the site lock, then the stats lock — keep that order.

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
  Amounts are instance configuration — they belong in the operator's
  `tiers.json`, never in this repo.

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
  redistributing every binary. There is deliberately NO environment override:
  anything that could set one could mint itself a licence. Tests replace the
  roots in-process (`licensing.UseRootsForTesting`); a developer bakes a
  throwaway root with the `LICENSE_ROOTS` build arg, as `e2e/license.ps1` does.
- **Signatures cover the ENCODED segment, not the JSON it decodes to** — the
  JWT convention. Verifying the decoded bytes instead would make the check
  depend on both sides serialising JSON identically (key order, spacing,
  escaping), and any disagreement looks exactly like a forgery. This was
  already got wrong once: no stack-issued licence verified until it was fixed.
- **The audience check is not optional.** `licPayload.app_id` must equal the
  certificate's *and* equal `"sitebin"`. Same reasoning as the MCP one.
- **Only an Enterprise plan licenses self-hosting** (`licensing.EnterprisePlans`:
  team, business, platform). The hosted plans are sold through the same stack
  app, the stack signs a licence for any paid plan, and a plan absent from
  `SITEBIN_STACK_LICENSING` carries no entitlements — unlimited. The first
  hosted Pro purchase (2026-09-23) was mailed exactly such a key. Anything
  else verifies as `none`.
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
- **The only calls the stack makes INTO Sitebin are the two GDPR orders**
  (`ee/gdpr.go`: delete user, export user data), authenticated by nothing but
  the HMAC over `<X-Timestamp>.<body>` with `SITEBIN_STACK_GDPR_SECRET`. A
  verified deletion IS the instruction — but a site that cannot be deleted
  still stops the order with a 5xx, so the stack keeps the identity and the
  operator retries; and a user with no account here is a 200, because the
  stack reads every other status as "the app still holds the data". Accounts
  the stack issued (OIDC) delete themselves at the stack's account console,
  never locally; see `docs/superpowers/specs/2026-09-07-gdpr-webhooks-and-two-consents-design.md`.

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
