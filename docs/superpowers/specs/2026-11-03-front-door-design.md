# One front door: drop on sitebin.io, account button everywhere

Date: 2026-11-03. Status: design, approved in conversation with the operator.
Touches both repos (product and `Sitebin-Website`).

## Why

The operator's words: the root of `https://app.sitebin.io` offers no way to
reach your account, and having a *demo* drop on www.sitebin.io beside the
*real* one on app.sitebin.io is confusing. They want a visitor who lands on
www.sitebin.io to be able to create a bin right there, and the "Sign in" link
to turn into "My account" once they are signed in.

Today:

- www.sitebin.io embeds `<sitebin-drop instance="https://app.sitebin.io" demo>`.
  Nothing is uploaded and the ticket is simulated, but the instance has
  allowed real anonymous creation from `https://sitebin.io` and
  `https://www.sitebin.io` since 2026-07-15 (`SITEBIN_EMBED_ORIGINS`).
- The website's nav has "Sign in" (→ `app.sitebin.io/account`) and an amber
  "Open the app" (→ `app.sitebin.io`) on all 25 pages. Neither knows whether
  the visitor is signed in.
- `app.sitebin.io/` is the core landing page (`web/static/index.html`). It has
  no account link at all, and its tag reads "self-hosted · no accounts",
  which is wrong on the hosted instance.
- Creation through the embed never carries credentials (`createCORS`:
  "Credentials are never allowed"), so a signed-in visitor who dropped on
  sitebin.io would get an anonymous 24-hour site. That is the confusion
  again.

## Goals

1. **The drop on www.sitebin.io is real.** Signed out, it makes an ordinary
   anonymous drop (the instance's anonymous tier, 24 h on the hosted
   instance), and the claim ticket appears right there on sitebin.io.
2. **Signed in, the drop on www.sitebin.io makes a site owned by the
   account.** It renews like any owned site, and the ticket says it was saved
   to the account and links to it.
3. **The website's nav knows whether the visitor is signed in.** "Sign in"
   becomes "My account" (→ `app.sitebin.io/account`). "Open the app" is
   removed everywhere, because with one front door it has nothing left to
   open (operator's decision).
4. **One front door on the hosted instance** (operator's choice, "variant B"):
   `app.sitebin.io/` sends a signed-out visitor to www.sitebin.io and a
   signed-in one to `/account`. Everything else on app.sitebin.io (edit
   pages, `/api`, `/mcp`, `/account/*`, `/report`, the forms routes) stays
   where it is.
5. **Self-hosted instances keep their landing page.** The redirect is an
   operator setting, off by default. When accounts are enabled, their landing
   page gets the same "Sign in" / "My account" link in its top bar, and loses
   the "no accounts" tag.

## Non-goals

- Moving the marketing site onto app.sitebin.io, or the app onto the apex.
  The domain layout stays (the app cannot share the apex, because Sitebin
  serves its own UI on its base domain).
- A dashboard drop zone. Signed-in users upload on the front door.
- Showing who is signed in (name, email, avatar) on the website. The website
  learns one bit: signed in or not.
- Making cross-origin session use available to arbitrary embedders.
  `SITEBIN_EMBED_ORIGINS` keeps meaning anonymous creation only.

## Anti-goals (would make this a failure even if it works)

- **Any origin other than the operator's own front door can act with a
  visitor's session.** The session is a real CSRF boundary
  (`sessionOwns`, `docs/superpowers/specs/2026-09-25-owner-session-edit-design.md`).
  This design adds exactly one narrow use (create a site, read signed-in),
  for an explicit list of origins, and nothing else. It never lets a session
  origin manage, read, or delete an existing site.
- A redirect loop or a dead end: a signed-in user must always reach their
  account, and a signed-out one must always reach a drop.
- A self-hosted instance whose root starts redirecting after an upgrade
  without its operator asking for it.

## Design

### 1. Session origins (`SITEBIN_SESSION_ORIGINS`, enterprise)

A new comma-separated list of web origins that may use the visitor's
**session** cross-origin, for two things only: creating a site owned by the
signed-in account, and asking whether the visitor is signed in.

- Validated at startup: each entry must be an absolute `https://` origin (no
  path; `http://` only under `SITEBIN_HTTP_ONLY`), and each must also be in
  `SITEBIN_EMBED_ORIGINS`, since a session origin is an embed origin with one
  more right. A violation refuses to start, like the other config errors.
- Honoured only when the enterprise extension is active and accounts are
  enabled. In the community build the variable does nothing (log it once if
  set).
- Hosted value: `https://sitebin.io,https://www.sitebin.io`.

Why the browser will send the cookie at all: www.sitebin.io and
app.sitebin.io are the same *site* (registrable domain `sitebin.io`), so the
`SameSite=Lax` session cookie travels on a credentialed `fetch`/XHR from one
to the other, and third-party-cookie blocking does not apply. A front door on
an unrelated domain would not get the cookie. That is acceptable: document it
in the config reference rather than detecting it.

### 2. Signed-in creation from a session origin

`POST /api/sites` today takes the owner from an API token, else from the
session cookie (`AuthorizeCreate` → `accountForAPI` → `currentAccount`),
whatever the origin.

New rule, in the core where the create gate already lives (`createSiteWith`
/ `fromOwnBrowser` / `createCORS`), so the extension stays origin-agnostic:

- **A cross-origin browser request counts its session cookie only when** its
  `Origin` is a session origin **and** it carries `X-Sitebin-Session: 1`.
  The custom header forces a CORS preflight, which only a session origin
  passes. Any other cross-origin request that happens to carry the cookie is
  treated as anonymous: the cookie is ignored, not refused. A
  "cross-origin browser request" is one with an `Origin` that is not the
  instance's own host, or with `Sec-Fetch-Site` other than `same-origin`.
- Same-origin creation (the landing page) and non-browser clients (no
  `Origin`, no `Sec-Fetch-Site`: curl with a token) are unchanged.
- How the core tells the extension to ignore the cookie is the builder's
  choice, within rule 2 of the codebase (a seam method or a request-context
  marker the core sets, never an `ee` import). For example, the core could
  strip the session cookie from the request it hands to `AuthorizeCreate`.
- CORS: for a session origin, `createCORS` adds
  `Access-Control-Allow-Credentials: true`, and the preflight allows the
  header `X-Sitebin-Session` besides `Content-Type`. For a plain embed origin
  nothing changes: no credentials, no extra header. `Vary: Origin` as today.
- The create response gains `"owned": true|false`, so the embed can tell
  the two tickets apart without a second request.
- Provenance: the creation is recorded as today. The surface should say it
  came through the front door, for example `origin` = the session origin;
  the exact field is the builder's choice.

This also closes a latent hole. Today any *same-site* page could make a
visitor's browser create a site in their account with a plain credentialed
form POST. No such page exists right now: the legacy `*.app.sitebin.io` user
hosts no longer resolve, as checked 2026-11-03. Test it explicitly anyway: a
credentialed cross-origin POST without the header, or from a non-session
origin, yields an anonymous site.

### 3. Signed-in status: `GET /account/status` (enterprise)

A new public route mounted through `PublicRoutes`. It answers
`{"signed_in": true}` or `{"signed_in": false}` and nothing else: no id,
name, email, or tier.

- Same-origin: always answered (the landing page uses it).
- Cross-origin: answered with `Access-Control-Allow-Origin: <origin>` and
  `Access-Control-Allow-Credentials: true` only for a session origin. Any
  other origin gets the body without CORS headers, so its script cannot read
  it.
- `Cache-Control: no-store`, `Vary: Origin`, and the dashboard's usual
  security headers.
- It does not slide the session (no re-issued cookie). A status poll must
  not keep a session alive.
- A suspended account reads as `false`, because `currentAccount` already
  refuses it.
- In the community build the route does not exist (404). The landing page
  treats a 404 as "no accounts here".

### 4. `<sitebin-drop>`: a `session` attribute

`web/static/embed.js` gains a boolean attribute `session`. With it, the
component:

- sends the create request with credentials and `X-Sitebin-Session: 1`;
- asks `<instance>/account/status` once on connect (credentialed). When the
  visitor is signed in, it changes its fine print from the anonymous
  lifetime to something like "publishes to your account". Exact copy is the
  builder's call, in the site's English voice;
- on a response with `"owned": true`, shows the **owned ticket**: "site
  published" stamp, View URL, Edit URL, a "Saved to your account" line with
  a link to `<instance>/account`, and the edit password behind a small
  disclosure. Owned sites still have one, and deploy scripts use it, so it
  is shown once as today, just not as the headline. Without `owned` it shows
  today's anonymous ticket, which should carry a "Sign in to keep it" link
  to `<instance>/account`. **OPEN QUESTION for the operator: what that link
  promises** (see the end of this document).
- **matches the landing page's ticket.** With the front door on, the public
  only ever sees the embed's ticket, and the website promises "a QR code for
  your phone". So the embed's ticket gains the QR code and "Copy all" that
  `web/static/index.html` + `app.js` have. The QR library is the one in
  `web/vendor/qrcode.min.js`. It is loaded only after a successful publish,
  from the same base the component itself was loaded from, so the website
  serves its vendored copy (`public/assets/`) and keeps its "no third-party
  resources" promise. Re-vendor both files together.

Without the attribute the component behaves exactly as today. Re-vendor
`embed.js` into `Sitebin-Website/public/assets/embed.js` (coupled file, see
the workspace guide), and document the attribute on `/docs/embed/`.

### 5. The front-door redirect (`SITEBIN_FRONT_DOOR`, enterprise)

A new boolean, default `false`. When `true`, `GET /` on the base domain:

- for a visitor with a valid session (asked through the existing optional
  `ext.SessionAccounts`): `303` to `/account`;
- otherwise: `302` to `SITEBIN_HOME_URL`.

Only the exact root path is affected: `GET /{$}`, the `landingPage` route.
The redirect itself may live in the core's `landingPage`, but the setting is
only honoured with the enterprise extension active.

**It must never leave the instance without a working drop** (anti-goal). A
drop on another origin only works through `SITEBIN_EMBED_ORIGINS`, which only
the enterprise edition honours. So startup refuses `SITEBIN_FRONT_DOOR=true`
unless every one of these holds:
- `SITEBIN_HOME_URL` is set and differs from the base URL;
- its origin is in `SITEBIN_EMBED_ORIGINS` and embed origins are honoured;
- with accounts enabled, its origin is also in `SITEBIN_SESSION_ORIGINS`.

The community build ignores the setting and logs that once.
- With the front door on, every in-app link that means "go publish
  something" points to `SITEBIN_HOME_URL`, not `/`. Otherwise a signed-in
  user is bounced straight back to `/account`. Known places: the
  dashboard's "No sites yet. Publish one" (`ee/templates.go`). Find any
  others with a grep for `href="/"`. The wordmark links (`/`) may stay: for a
  signed-in user they lead to the account, for others to the front door.
- Hosted value: `SITEBIN_FRONT_DOOR=true` (`SITEBIN_HOME_URL=https://sitebin.io`
  is already set).

### 6. The landing page's top bar (core page, self-hosted case)

With the front door off, the landing page (`web/static/index.html` +
`app.js`) fetches `/account/status` same-origin on load:

- `200` + `signed_in: false`: show "Sign in" (→ `/account`) at the right of
  the top bar;
- `200` + `signed_in: true`: show "My account" (→ `/account`);
- `404` or a network error: show nothing (community, no accounts).

The tag "self-hosted · no accounts" is only true without accounts. When the
status route answers, replace it with something true for every
accounts-enabled instance, or hide it. The wording is the builder's call.
The landing page's CSP (`connect-src 'self'`) already permits the fetch.

### 7. The website (`Sitebin-Website`)

- `public/index.html`: the drop becomes
  `<sitebin-drop instance="https://app.sitebin.io" session>` (no `demo`).
  Rewrite the caption under it: drops are real, anonymous ones last 24
  hours, and signed in they go to your account and renew. Keep the
  `#try` anchor and "Try it right here".
- **Nav on all 25 pages** (desktop `.links` and the mobile `.sheet`):
  - remove "Open the app";
  - "Sign in" takes the amber `navcta` style.
  - A small shared script (e.g. `public/assets/account-link.js`, loaded with
    `defer` on every page) asks `https://app.sitebin.io/account/status` with
    `credentials: "include"`. On `signed_in: true` it swaps the link's text to
    "My account". The href stays `https://app.sitebin.io/account`.
  - Without JS, or if the call fails, the link stays "Sign in", which still
    lands a signed-in user on their dashboard.
- Remove the footer's "Open the app" link on the home page (and anywhere
  else it appears). Replace the remaining copy that points people to
  app.sitebin.io to "do it for real" (home caption, docs quickstart, and so
  on) where it now misleads. Keep it where app.sitebin.io is the correct
  technical address (API base URL, curl examples, MCP endpoint).
- The website's CSP on the instance (`object-src 'none'; base-uri 'self'`,
  trusted site) already permits the cross-origin calls.
- **The privacy page must change in the same push.** It is a legal page, and
  every push publishes it live.
  - Today `public/privacy/index.html` says the landing demo "runs entirely in
    your browser" and that files are "not transmitted" anywhere. Once the drop
    is real, that is false.
  - The rewrite says that dropped files are uploaded to app.sitebin.io and
    processed like any other upload, under the existing app sections.
  - It also describes the sign-in check (see the open question): what is sent
    (the app's own session cookie, if there is one), what comes back (signed
    in yes or no), and what is logged (the usual server log, IP included).
  - The website's own CLAUDE.md already forbids dropping `demo` without this.
  - Check the footer line "no cookies · no trackers" on every page, and the
    privacy page's meta description, against the final behaviour. The
    website itself still sets no cookie.
  - The operator approves the privacy text before the website push.

## Security summary

| Who | Can now | Cannot |
|---|---|---|
| A page on a session origin (the operator's own marketing site) | create a site owned by the signed-in visitor; learn signed-in yes/no | read, change or delete any existing site; learn who the visitor is; use a token |
| A page on a plain embed origin | create anonymous sites (as today) | anything with the session |
| Any other page, same-site or not | nothing new, and it loses the latent same-site create-with-cookie path | — |

Whoever can edit the session origin's site, today the operator's deploy
through its claim ticket, can therefore create sites in visitors' accounts.
That is acceptable for the operator's own front door and is why the list is
explicit and separate from `SITEBIN_EMBED_ORIGINS`.

## Tests the builder must include

- Core: cross-origin create with the cookie but without the header → anonymous
  site; from a non-session origin with the header → preflight refused / no
  credentials allowed; from a session origin with the header → owned site and
  `owned: true`; same-origin create unchanged; community build: session
  origins and the front door inert (the landing page is served as today).
- Enterprise: `/account/status` for signed in, signed out, suspended,
  expired token version; CORS headers only for session origins; no cookie
  re-issued.
- Config: the startup refusals:
  - a session origin not in the embed origins;
  - a session origin that is not https;
  - the front door without a distinct home URL;
  - the front door with the home URL's origin missing from the embed origins
    or, with accounts on, from the session origins.
- E2E: extend `e2e/accounts.ps1` (enterprise image) with the status route and
  a session-origin create, plus the front-door redirect both ways.

## Docs and coupling

- Product README env table + `Sitebin-Website/public/docs/configuration/`:
  `SITEBIN_SESSION_ORIGINS`, `SITEBIN_FRONT_DOOR`.
- `/docs/embed/`: the `session` attribute and the owned ticket.
- `/docs/api/`: `owned` in the create response.

## Ship order

1. Product: merge, push, deploy to app.sitebin.io with
   `SITEBIN_SESSION_ORIGINS` and `SITEBIN_FRONT_DOOR=true`. Follow the deploy
   rules: pre-deploy snapshot into `/opt/sitebin/backups/`, keep the newest 3
   rollback images, prune the build cache.
2. Verify on the live instance: the status route from www.sitebin.io (signed
   in and out), an owned drop through a session origin, and the root redirect
   both ways.
3. Website: push (vendored `embed.js`, the nav script, the 25 nav edits, the
   home caption). The deploy workflow publishes on push to `main`.

Between steps 1 and 3 the old website keeps working: its demo makes no calls,
and its "Open the app" lands on app.sitebin.io/, which now redirects to the
website or the account.

## Calls made without asking (for the operator to check)

- The session cookie travels to the front door only because it is the same
  site. This is documented, not detected.
- `GET /account/status` never slides the session.
- A cross-origin request with a stray cookie is downgraded to anonymous, not
  refused.
- "Sign in" inherits the amber CTA style once "Open the app" is gone.
- Owned tickets still show the edit password once, behind a disclosure.
- The front door is enterprise-only, and startup refuses it unless the home
  URL can actually run a drop. Raised by the builder check.
- The embed's ticket gains the QR code and "Copy all", with the QR library
  vendored beside the component. Raised by the builder check.
- The privacy page is rewritten in the same website push, and the operator
  approves it first. Raised by the builder check.

## Operator decisions after the builder check (2026-11-03)

1. **"Sign in to keep it" really adopts this drop.** Signed in, the visitor
   takes exactly this anonymous drop into their account. That is not a
   front-door feature. It is part of the site-ownership work (adopt,
   transfer, share), which has its own design:
   `2026-11-03-site-ownership-design.md`.
   - Adopting needs a paid plan.
   - The link opens the instance's `/account/adopt` with the site prefilled.
   - The edit password never goes into a URL.
   - The front door's anonymous ticket links to that adoption flow.
   - Until the flow ships, the link carries the copy-only meaning ("sign in,
     then your next drop renews itself").
   - Adoption changes the terms ("a drop is managed only through its claim
     ticket"), so it brings a new terms version with it.
2. **The sign-in check runs on all 25 pages.** The privacy page describes it
   (Section 7). The legal assessment is the operator's.
