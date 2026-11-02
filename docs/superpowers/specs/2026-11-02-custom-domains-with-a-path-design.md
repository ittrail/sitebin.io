# Custom domains with a path

**Date:** 2026-11-02
**Status:** design — not yet approved

## Problem

A custom domain today names a whole host, and a host belongs to exactly one
site (`domain-index/<host>` is one symlink). An owner who wants one domain to
be made of several projects has to build them into one: today
`morgenfertig.at` is a container app on port 3000, and every Astro demo shown
to a customer under it has to be built into that container, so a new demo
means redeploying the container (or at least the Astro project inside it).
The demos' traffic also runs through the app although they are static files.

What the owner wants instead is to put **independent Sitebin projects under
paths of one domain**, each deployed on its own:

- `morgenfertig.at` → container, port 3000;
  `morgenfertig.at/demos/kunde123` → a static site (an Astro build) of its own.
- `myapp.com` → a static landing page; `myapp.com/account` → a container
  backend.
- `tools.example.com/a`, `/b`, `/c` → three tools, three projects, and
  nothing at all at the root.

Requests under a mounted path are answered by Sitebin's own proxy and file
server and never reach the project at the root.

## What changes

1. **A custom domain can carry a path.** Everywhere a custom domain is
   entered or shown (edit page, JSON API, MCP), the value may be a host
   (`morgenfertig.at`, unchanged) or a host plus a path
   (`morgenfertig.at/demos/kunde123`). Internally this design calls the second
   kind a **path address**; in the product and the docs it is "a custom domain
   with a path" — the feature is not a separate product noun. "Custom domain"
   stays the word on the pricing page.
2. **All three site kinds work at any path, and at the root:** a static site
   (webserver), a file viewer (directory listing), and a container site's HTTP
   port. Which kind sits at the root of a host does not matter, and nothing has
   to sit there at all.
3. **The longest matching path wins**, on segment boundaries only.
4. **Sharing a host needs no new proof when the owner already holds it.**
   If another address of the same account holds the host by its own proof
   (see "Proof, and sharing a host"), the new address attaches at once. Otherwise the address is proven by DNS exactly as a
   custom domain is today. A host held by another account's site is refused.
5. **A path address counts as one custom domain**, against the per-site cap
   and the licence's instance-wide ceiling, with the same exemption for names
   inside the owner's verified zone (decided by the owner, 2026-11-02).
6. **A path address can serve or redirect**, like a custom domain, and on a
   container site it targets a port, like a custom domain.
7. **A warning when a static site is not built for its path** (see "The base
   path"), shown where the address is managed.

## Goals, non-goals, anti-goals

**Goals:** each project under a domain deploys on its own; static projects are
served by Sitebin's file server, never through a container; every site kind
can sit at any path; the rules (proof, caps, redirects, locks, expiry, view
password, caching, forms) behave the same under a path as on a host.

**Non-goals:**

- **Rewriting links** in HTML/CSS so a site built for the root works under a
  path. It covers `href`/`src`/`url()` and nothing JavaScript builds at
  runtime (`fetch('/api')`, client routers, lazy chunks, Astro islands), and it
  breaks integrity hashes. Discussed and rejected.
- **Guessing the project from the `Referer`** for root-absolute requests.
  Browsers omit it, caches break it, two projects can share file names, and it
  would steal requests from the root project. Rejected.
- A "strip the prefix" switch for containers. Not until someone needs it.
- Paths on the generated address (`<id>.sitebin.app/x`) or on any host of the
  instance's own domains. Path addresses are custom domains only.
- Wildcards or patterns in paths.
- Two accounts sharing one host.

**The answer for an app that cannot be told its base path** is a subdomain
(`kunde123.morgenfertig.at`) — a plain custom domain, which exists today and
needs nothing from this design. The docs say so where they explain the base
path.

**Anti-goals — failures even if everything else works:**

- An address that looks fine to the owner and is broken for visitors without
  anyone being told (the base-path warning exists for this).
- Caddy serving files from a different site than the one authz admitted. A
  view password must not be bypassable by any spelling of a path.
- One account's proven host being taken by another account.
- A rule leaving a site with no address (the never-offline rules of
  `2026-10-16-domain-redirects-design.md` hold).
- A path address changing anything for sites and hosts that do not use one.

## Constraints from the codebase

These are the repo's own rules (see `CLAUDE.md`), restated where this design
leans on them:

- **The filesystem is the database.** New state lives in `meta.json` and in
  symlink indexes under `/data`; old sites without the new fields keep working
  unchanged (`omitempty`, missing = zero value).
- **authz is the router.** Every content request passes
  `forward_auth → /internal/authz`; Caddy only copies what authz answers.
  The Caddyfile is generated once at start and never changes at runtime.
- **Custom domains are enterprise** (`claimDomain`'s edition gate), and so are
  path addresses. The community build has no provider and refuses them.
- **Nothing on the hot path asks the extension.** Resolving host + path to a
  site on a request must be a cheap filesystem lookup, never a scan of every
  site and never a call into `ee/`.

## The address

- **Form:** `<host>` or `<host><path>`. The host is normalized as today
  (`normalizeDomain`). The path starts with `/`, has no trailing slash, no
  query or fragment, no empty, `.` or `..` segment, and no segment
  `_sitebin`. Segments use lowercase `a-z`, digits, `-`, `_`, `.`, `~`.
  At most 8 segments and 200 characters. Uppercase input is refused, not
  lowercased (a lowercased path would not be the one the owner's links use).
- **Identity:** the full string `<host><path>` is the address's name
  everywhere — in the API and MCP `domain` argument, in redirect targets, in
  the edit page. A plain host is the special case with an empty path. No new
  argument is added to existing API routes or MCP tools; the existing `domain`
  string widens. (How it is stored in `meta.json` is the builder's call, as
  long as every reader of `CustomDomains` that expects a bare host — TLS
  check, zones, CNAME proof, certificate issuance — still gets one.)
- **Matching:** a request `GET <host><p>` matches address `<host><path>` when
  `p == path` or `p` starts with `path + "/"`. The longest matching path of
  the host wins; the empty path (the host itself) matches everything. Matching
  is case-sensitive and done on the same decoded, dot-cleaned path Caddy uses
  for the file server.
- **Root left empty:** a host with path addresses but no project at its root
  answers 404 "Site not found" for anything no path matches.

## Routing a request

authz resolves host + path to (site, matched path), then applies every rule
it applies today, in today's order, to that site: lock (410), expiry (410),
the address's own rule (redirect / serve), view password, admit.

**Static site and file viewer — the prefix is stripped.** authz answers with
the site's folder and the matched path in internal headers (the same pattern
as `X-Sitebin-Upstream`): Caddy removes any client copy of them before
`forward_auth`, copies authz's, strips the prefix (`uri strip_prefix`), and
takes its `root` from authz's answer instead of from `domain-index/{host}`.
This is the `/v/<id>/` path view's shape (`writePathViewRoutes`), generalized.

- **The folder is authz's answer, never recomputed by Caddy.** That is what
  makes the anti-goal above hold: whatever spelling of a path a client sends,
  Caddy serves from the folder of the site authz checked.
- A request for the bare prefix (`/demos/kunde123`) gets a `308` to
  `/demos/kunde123/`, query kept, as `/v/<id>` does — relative links in
  `index.html` need the slash.
- Trust marker, CSP, SPA fallback, directory listing, `Cache-Control`
  (computed on the path **after** stripping) all work as they do on a host,
  from the mounted site's own folder.

**Container port — the path is passed through.** authz answers the upstream
as today. The app receives the full path (`/account/login`, not `/login`) and
a header `X-Forwarded-Prefix: /account` (empty/absent at the root). This is
what frameworks configured with a base path expect (Next.js `basePath`, Astro
SSR `base`, an Express router). The app must know its base path; that is the
owner's job and the docs say so. `admit`'s refusal of containers under a path
applies to the `/v/<id>/` path view only, not to path addresses.

**What the root project sees:** nothing under a mounted path — not the
requests, not the traffic. A container at the root of `myapp.com` stops
receiving `/account/*` the moment `myapp.com/account` attaches.

**TLS:** `/internal/tls-check` answers 200 for a host as soon as any verified
address (with or without a path) is on it, so a host with only path addresses
gets its certificate.

## Sitebin's own routes under a path

Today `/_sitebin/*` goes straight to the backend, which finds the site by
host (`servingSiteByHost`). Under a shared host that would hand a mounted
site's form, its unlock form and its CSP reports to the root project.

- **`<path>/_sitebin/*` belongs to Sitebin on every path address**, exactly as
  `/_sitebin/*` does at the root — for container ports too (the gate page of a
  password-protected mounted container posts its unlock there). The mounted
  site's pages post forms to `<path>/_sitebin/forms/<key>`; in Astro that is
  `` `${import.meta.env.BASE_URL}_sitebin/forms/<key>` ``.
- The backend must resolve the site for these routes **from host + path,
  through authz's resolution or the same function** — never from a header or
  parameter a client can set without Caddy stripping it first. If the builder
  carries the site from authz to the backend in a header, Caddy strips the
  client's copy on every route, including the plain `/_sitebin/*` handle.
- The gate page and the form pages that Sitebin renders on a mounted address
  link and post to `<path>/_sitebin/…`.
- **CSP reports:** the `report-uri` stays `/_sitebin/csp-report` (it is static
  text in the Caddyfile). The tripwire attributes a report to a site by the
  reported document's host + path, not by the request's host alone. It still
  acts only on what that site's own files reference, so a forged report gains
  nothing.

## Proof, and sharing a host

- **A host's addresses all belong to one owner account.** A claim for an
  address on a host that a site of **another** account has verified (with or
  without a path) is refused (`ErrDomainTaken`), whatever DNS says — as today.
- **Two ways an address is held** — and the same two for a plain host and a
  path address alike, at claim time and in the sweep:
  - **by its own proof:** today's proofs (TXT with the claim's token, or
    CNAME, asked of the authoritative nameservers), a name inside the
    account's verified zone, or an operator domain held by the operator;
  - **through the host:** another address of the same account on the same
    host is currently held **by its own proof**. Only such an address vouches
    for a host. An address held through the host never vouches for anything —
    otherwise two of them would keep each other alive after the owner's DNS
    has moved away, and the host would never be revoked (builder check,
    2026-11-02).
- **Claiming:** if an address of the same account on the host is held by its
  own proof, the new address attaches at once, no DNS lookup. Otherwise it is
  proven by its own proof with today's rate limits and pending state. The
  proof is about the host; the path plays no part in DNS. Every claim gets its
  own token from the start, whichever way it is held.
- **Without accounts** (an enterprise instance in open mode) there is no owner
  to compare: every address on a shared host is held by its own proof only.
- **The sweep:** each claim's own proof is still checked as today, so it is
  always known which addresses can vouch. When no address on a host is held
  by its own proof any more, every address held through that host starts
  **one shared three-day clock** (not one clock per address, which would add
  up as they fall one after another); within it, one of them proving itself
  is enough to vouch for the rest again. Then they detach as today. A lookup
  error never detaches and never starts the clock. So when the root project
  is deleted or drops the domain, the paths under it are not offline at once
  — the same rule as a released zone — and the owner has three days to add
  one TXT record. This needs the sweep to look at addresses grouped by host,
  outside the serving path.
- **The edit page** shows, for an address held through the host, its own TXT
  record as "keep it working on its own", and warns before the owner removes
  the last address on a host that is held by its own proof that the others
  will need one.

## Caps and the licence

A path address is a custom domain for every count: the per-site cap
(`QuotaDomains`, verified + pending, as `countedClaims` counts today), the
licence's instance-wide `max_custom_domains` (`CustomDomainsAllowed`), and
the exemption for names in the owner's verified zone. One site may hold the
host and a path on it (`example.com` and `example.com/api`, e.g. two ports of
one container project) — each counts.

## Redirects

A path address is an address like any other, with the rules of
`2026-10-16-domain-redirects-design.md`:

- It can redirect to another of the **same site's** verified addresses (with
  or without a path), with 301/302/307/308. The remainder of the request path
  after the matched prefix, and the query, are appended to the target:
  `old.example/demos/x/a?q=1` → `new.example/a?q=1` when
  `old.example/demos/x` redirects to `new.example`.
- The generated address can redirect to a path address. This is how an owner
  whose site is built with `base: '/demos/kunde123'` keeps the generated
  address from showing a broken page.
- One hop, never a chain; never a URL; never-offline rules unchanged. On a
  container site, redirects and serve/off work per address as today.

## The view password

A protected site under a path is protected for its path only; a protected
site at the root protects only what no path address of the host matches. The view-password
cookie today has one name per host; on a mounted address it must be scoped to
the address's path (`Path=<path>/`), so that two protected projects on one host
can both stay unlocked and neither cookie is sent where it is not needed.

## The base path, and the warning

A static site under a path works only if it was built for that path. Astro
does this with `base: '/demos/kunde123'` in `astro.config.mjs`: its build
prefixes every asset it emits, but still writes to a flat `dist/`
(`dist/index.html`, `dist/_astro/…`), which is what gets uploaded. Links the
author writes by hand need `import.meta.env.BASE_URL`, and files from
`public/` are not prefixed automatically. Such a site does not work on its
generated address any more (see Redirects for the remedy).

**The warning:** when a static site or file viewer has a path address, Sitebin
reads its top-level `index.html` and looks for root-absolute `src`, `href`,
`srcset`, `action` and `poster` values (`/…`, not `//…`) that do not start
with that path. If it finds any, the address carries a warning: "This site
seems not to be built for /demos/kunde123: it links /_astro/x.css. Set your
build's base path to /demos/kunde123, or use a subdomain instead." Shown on
the edit page next to the address and in the answer to adding it (API and
MCP). It is computed when the address is added and when the edit page or
`GET /api/sites/{id}` asks — never on the serving path, bounded in size, and
it never blocks anything. Container ports get no check (Sitebin cannot read
what the app renders).

## One origin, one owner

Every project on a host shares one browser origin: cookies, `localStorage`,
service workers (within their scope), and `connect-src 'self'` all reach each
other. That is acceptable only because all of them belong to one account —
which is the other reason, beside DNS, that two accounts never share a host.
The docs state it: putting projects under one domain puts them in one origin.

## Surfaces

- **Edit page:** the custom-domain card accepts `host/path` in its input and
  lists path addresses with the domains, each with its proof state, target
  port (containers), redirect controls, and the base-path warning.
- **JSON API:** existing domain routes accept and return `host/path` strings.
  A domain in a URL path segment (`DELETE /api/sites/{id}/domains/{domain}`)
  needs the path encoded or a different route shape — the builder's call, as
  long as old calls with a bare host keep working.
- **MCP:** `add_domain`, `remove_domain`, `set_domain_redirect`,
  `set_domain_target` accept `host/path` in `domain` (and `to`). No tool is
  renamed or removed. **Trap:** adding a field to a tool's output schema has
  broken clients with cached schemas before (site names, 2026-09-24); the
  warning goes into the text/structured answer in a way that does not change
  the declared output schema, or the change is noted in the MCP docs page as a
  reconnect.
- **Dashboard site list:** shows path addresses like domains.

## Website and docs (coupled repo)

`Sitebin-Website/public/docs/` must say, after the product is live:

- `/docs/custom-domains/`: a domain with a path; sharing a host (same account,
  no new proof); longest path wins; the base path (Astro example with `base`,
  `BASE_URL`, `public/`), the subdomain alternative, the warning, one origin;
  forms under a path; the generated-address redirect.
- `/docs/containers/`: a port under a path receives the full path and
  `X-Forwarded-Prefix`; the app must be configured with its base path;
  `<path>/_sitebin/` is Sitebin's.
- `/docs/mcp/`: the `domain` argument accepts `host/path`.
- Forms docs: the action under a path.
- Pricing: no change in caps; the FAQ may say a domain with a path counts as
  one custom domain.

Ship order as in the workspace guide: product merged and pushed, deployed to
`app.sitebin.io` and verified there, then the website.

## Tests the builder should expect to write

Both build tags. At minimum: address parsing and refusals; longest-prefix
matching on segment boundaries (`/account` vs `/accounts`); a static site, a
file viewer and a container port each at the root and under a path on one
host; the root left empty; the bare-prefix 308; that a path variant
(`%2e%2e`, `//`, encoded letters, `..`) never serves from a site other than
the one authz admitted, with a view password on the mounted site; client
copies of the new internal headers stripped on every route; forms, unlock and
CSP reports under a path reaching the right site; the same-account shortcut,
the foreign-account refusal, the open-mode DNS rule; the sweep's fallback to
own proof and three-day detach; caps and licence counts; redirects with the
remainder appended; the cookie path; TLS check for a host with only path
addresses; the base-path warning's positives and negatives; old sites and
plain domains unchanged. An e2e script against a real Caddy (enterprise
image) for the routing — `paths.ps1` is taken by the `/v/<id>/` path views.

## Left to the builder

- How addresses with paths are stored in `meta.json` and indexed under
  `/data` (within the constraints above).
- The names of the internal headers and the exact Caddyfile change.
- How the backend's `/_sitebin/` routes learn the site (within the rule
  above).
- The API route shape for removing an address with a path.
- The exact wording of the warning and of the docs.

## Calls the designer made (for the owner to check)

These were not discussed with the owner; they are the designer's defaults:

- The product word stays "custom domain" (with a path); "path address" is
  internal only.
- Paths are lowercase-only, case-sensitive, at most 8 segments / 200 chars.
- `<path>/_sitebin/` is reserved on container ports under a path too.
- A host with only path addresses answers 404 at the root.
- The bare prefix of a static site redirects 308 to the slash form.
- Only an address held by its own proof vouches for a host; when none is
  left, the addresses held through the host share one three-day clock, and
  one TXT record from the owner rescues them all.
- Without accounts, every address on a shared host proves DNS itself.
- The view-password cookie is scoped to the address's path.
- No "strip prefix" switch for containers for now.
