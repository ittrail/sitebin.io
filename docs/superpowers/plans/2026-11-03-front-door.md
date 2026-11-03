# One front door: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the drop on www.sitebin.io is real and creates owned sites for
signed-in visitors. Every page knows "Sign in" vs "My account".
`app.sitebin.io/` sends visitors to the front door or their account.

**Architecture:** almost all of it is core (MIT).
- Two new settings, `SITEBIN_SESSION_ORIGINS` and `SITEBIN_FRONT_DOOR`, are
  checked in two places: their static rules in `config.Load`, and the
  account-dependent rules after the extension is initialised.
- The create gate drops the session cookie from any cross-origin request
  that is not a session-origin request with `X-Sitebin-Session: 1`.
- `GET /account/status` and the landing redirect are core routes. They ask
  the existing `ext.SessionAccounts`, so `ee/` needs one new `ext.Host`
  method (`PublishURL`) for its dashboard link.
- `<sitebin-drop>` gains a `session` attribute and the landing page's QR
  and "Copy all".
- The website then switches its drop to live, rewrites the nav on all 25
  pages and rewrites the privacy page.

**Tech Stack:** Go (stdlib), vanilla JS (custom element, no build step),
static HTML website, PowerShell e2e.

**Spec:** `docs/superpowers/specs/2026-11-03-front-door-design.md`, binding.
Its "Operator decisions after the builder check" are binding too.

## Global Constraints

- **Branches:**
  - product `feat/front-door` (from main 2ab74d1);
  - website `content/front-door`, created in Task 9.
  - Merging and pushing happen only in Task 10, and only with the operator's
    OK.
- **Verification for every Go task:** `go vet ./...`, `go vet -tags ee ./...`,
  `go test ./...` and `go test -tags ee ./...`, all green.
- **`SITEBIN_SESSION_ORIGINS`:**
  - a comma-separated list of origins, lowercased and trimmed, empty entries
    skipped;
  - each is `https://host[:port]` with no path or query; `http://` is allowed
    only when `SITEBIN_HTTP_ONLY=true`;
  - `*` is refused;
  - each must also be allowed by `SITEBIN_EMBED_ORIGINS`: listed there, or
    that list is `*`.
- **`SITEBIN_FRONT_DOOR`:** a bool, default `false`.
- **Refusal texts** (config errors exit at startup, as today):
  - `SITEBIN_SESSION_ORIGINS: %q is not an origin (want https://host[:port])`
  - `SITEBIN_SESSION_ORIGINS: %q is not in SITEBIN_EMBED_ORIGINS`
  - `SITEBIN_FRONT_DOOR needs SITEBIN_HOME_URL set to a site other than this instance`
  - `SITEBIN_FRONT_DOOR: the origin of SITEBIN_HOME_URL (%s) must be in SITEBIN_EMBED_ORIGINS`
  - `SITEBIN_FRONT_DOOR: the origin of SITEBIN_HOME_URL (%s) must be in SITEBIN_SESSION_ORIGINS when accounts are on`
  - `SITEBIN_FRONT_DOOR needs the enterprise edition (embedding the drop on another site)`
    — but the **community build only warns once and ignores the setting**,
    as `main.go:299-301` does for embed origins.
- **The session header** is `X-Sitebin-Session: 1`, the existing
  `sessionHeader` const.
- **Session origins are honoured only** when a provider is registered with
  `AccountsEnabled()` and `EmbedOriginsAllowed()`, and it implements
  `ext.SessionAccounts`.
- **`GET /account/status`:**
  - body exactly `{"signed_in":true}` or `{"signed_in":false}`;
  - `Content-Type: application/json`, `Cache-Control: no-store`,
    `Vary: Origin`, `X-Content-Type-Options: nosniff`;
  - never sets a cookie;
  - 404 when no session provider exists.
- **The create response** gains `"owned": true|false`, only on `POST /api/sites`.
- **Front door:** `GET /` on the base host answers `303 /account` with a
  valid session, else `302` to `SITEBIN_HOME_URL`. Nothing else changes.
- **Embed copy** (English, exact):
  - fine print signed in: `publishes to your account at <host>`;
  - owned ticket line: `Saved to your account.` with the link text
    `Open my account`;
  - anonymous ticket line with `session`: `Sign in, and your next drop renews itself.`
    with the link text `Sign in`. This is the copy-only meaning ruled for the
    time before adoption ships.
- **E2E scripts stay pure ASCII.**
- **The dashboard and edit page add no script.** The landing page's CSP
  stays as it is (`connect-src 'self'`).
- **Hosted values (Task 10):**
  - `SITEBIN_SESSION_ORIGINS=https://sitebin.io,https://www.sitebin.io`;
  - `SITEBIN_FRONT_DOOR=true`;
  - `SITEBIN_HOME_URL=https://sitebin.io` and `SITEBIN_EMBED_ORIGINS` are
    already set.
- **Commits:** a lowercase conventional prefix and a behaviour sentence,
  ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **A session that expires between the status check and the upload.** The
   embed showed "publishes to your account", but the create comes back
   `owned:false`. The visitor must get the anonymous ticket and its sign-in
   line, with no error. Pinned in Task 6 (ticket chosen by `owned` alone)
   and Task 2 (an invalid cookie means anonymous).
2. **A browser that sends no cookie** (privacy settings, a private window).
   The status says signed out, the drop works anonymously, and the nav says
   "Sign in". Pinned in Task 3 (no cookie → false) and Task 6.
3. **The apex and www pair.** `SITEBIN_HOME_URL=https://sitebin.io` redirects
   to www, and both origins are in both lists. The config checks use the
   HOME_URL's origin. Neither origin may trip a refusal. Pinned in Task 1.
4. **A redirect loop.** A HOME_URL equal to the base URL, or one whose
   origin is the instance itself, must refuse to start. Pinned in Task 1.
5. **A self-hosted landing page whose status call fails** (404, 500, network
   error). The page works and shows no account link. Pinned in Task 5.

---

### Task 1: The two settings and their startup rules

**Files:**
- Modify: `internal/config/config.go`:
  - fields beside `EmbedOrigins` (:103-106):
    `SessionOrigins []string` and `FrontDoor bool`;
  - parsing after EmbedOrigins (:427-443), reusing its origin parser:
    extract a helper `parseOrigins(name, v string, allowHTTP bool) ([]string, error)`
    if that keeps the code DRY.
- Create: `internal/config/frontdoor.go` holding
  `func (c Config) CheckFrontDoor(embedHonoured, accountsOn bool) error` and
  `func (c Config) HomeOrigin() string`.
- Modify: `cmd/sitebin/main.go`. After `p.Init` (:248-254), call
  `cfg.CheckFrontDoor(...)`:
  - an error returns from `serve()`;
  - in community, with `FrontDoor` or `SessionOrigins` set, warn once (like
    :299-301) and set `cfg.FrontDoor=false` and `cfg.SessionOrigins=nil`
    before `httpapi` is built.
- Test: `internal/config/config_test.go`, `internal/config/frontdoor_test.go`.

**Interfaces:**
- Produces:
  - `Config.SessionOrigins []string` (lowercased origins);
  - `Config.FrontDoor bool`;
  - `Config.HomeOrigin() string`: the scheme://host[:port] of HomeURL,
    lowercased;
  - `Config.CheckFrontDoor(embedHonoured, accountsOn bool) error`.
- **Static rules in `Load`:** the session-origin format and membership in
  EmbedOrigins; and FrontDoor's need for a HomeURL whose origin differs from
  the base URL's origin and is in EmbedOrigins.
- **`CheckFrontDoor`:** when `FrontDoor` is set, require `embedHonoured`;
  with `accountsOn`, also require HomeOrigin in SessionOrigins.

- [ ] **Step 1: Write the failing tests.**
  - `TestSessionOrigins`: the hosted pair is accepted and lowercased;
    `http://x` is refused without HTTP_ONLY and accepted with it; `*`,
    `https://a/path`, `ftp://a` are refused with the format text; an origin
    missing from EMBED_ORIGINS is refused with the "not in" text; with
    EMBED_ORIGINS=`*` any valid origin is accepted.
  - `TestFrontDoorConfig`:
    - FRONT_DOOR=true with HOME_URL unset → refused;
    - HOME_URL equal to the base, or `https://BASE/x` (same origin) → refused
      (Review Focus 4);
    - HOME_URL `https://sitebin.io` with EMBED_ORIGINS
      `https://sitebin.io,https://www.sitebin.io` → OK (Review Focus 3);
    - HOME_URL origin not embedded → refused.
  - `TestCheckFrontDoor`:
    - `(false, *)` with FrontDoor → the enterprise text;
    - `(true, true)` with HomeOrigin not in SessionOrigins → the
      "when accounts are on" text;
    - `(true, false)` → nil;
    - FrontDoor off → nil whatever the arguments.
- [ ] **Step 2: Run** `go test ./internal/config -run 'Session|FrontDoor' -v`.
  Expected: FAIL.
- [ ] **Step 3: Implement**, including the main.go wiring.
- [ ] **Step 4: Run** the four Go commands. Expected: PASS.
- [ ] **Step 5: Commit** `feat: an instance can name the front door and the origins that may use its session`.

### Task 2: The create gate uses a session only from a session origin

**Files:**
- Modify: `internal/httpapi/apigate.go`. Add:
  - `func (a *API) sessionOriginsHonoured() bool`;
  - `func (a *API) sessionOriginRequest(r *http.Request) bool`: Origin
    (lowercased) is in `cfg.SessionOrigins`, `X-Sitebin-Session == "1"`, and
    `sessionOriginsHonoured()`;
  - `func (a *API) crossOriginBrowser(r *http.Request) bool`: an `Origin`
    that is not `originIsOwnHost`, or a `Sec-Fetch-Site` that is non-empty
    and not `same-origin`.
- Modify: `internal/httpapi/sites.go`:
  - `createSiteWith` (:683-696): when `crossOriginBrowser(r) && !sessionOriginRequest(r)`,
    pass `AuthorizeCreate` a clone with the `Cookie` header deleted. Keep `r`
    for everything else.
  - `createCORS` (:612-630): for an Origin in SessionOrigins (and honoured),
    also set `Access-Control-Allow-Credentials: true`.
  - `createPreflight` (:635-644): for such an origin, allow the headers
    `Content-Type, X-Sitebin-Session` and set ACAC; plain embed origins stay
    exactly as today.
  - `createSite` (:865): `resp["owned"] = site.Meta.OwnerAccountID != ""`.
- Test: `internal/httpapi/frontdoor_test.go`. Reuse `newEnv`, `createResp`
  (add `Owned bool`), and the session fake in `session_test.go` (cookie
  `sb_test`). Add a fake provider whose `AuthorizeCreate` grants the
  session's owner when the `sb_test` cookie is present, so stripping is
  observable.

**Interfaces:**
- Consumes: `cfg.SessionOrigins` (Task 1).
- Produces: `API.sessionOriginsHonoured()`, `API.sessionOriginRequest(r)`
  (Task 3 reuses both), and the create-response key `owned`.

- [ ] **Step 1: Write the failing tests.**
  - `TestCreateFromSessionOriginIsOwned`: Origin `https://www.sitebin.io`,
    `X-Sitebin-Session: 1`, a valid cookie → 201, `owned:true`, owner set,
    `Access-Control-Allow-Credentials: true`.
  - `TestCreateFromSessionOriginWithoutHeaderIsAnonymous`: the same without
    the header → 201, `owned:false`.
  - `TestCreateFromPlainEmbedOriginIgnoresCookie`: an embed-only origin with
    header and cookie → `owned:false`, and no ACAC header.
  - `TestCreateSameSiteWithCookieIsAnonymous`: `Sec-Fetch-Site: same-site`,
    no session-origin match → `owned:false`. This closes the latent hole.
  - `TestCreateSameOriginStillOwned`: `Sec-Fetch-Site: same-origin` with a
    cookie → `owned:true`, unchanged.
  - `TestCreateNonBrowserStillOwned`: no Origin and no Sec-Fetch-Site, with
    a cookie (curl) → `owned:true`, unchanged.
  - `TestCreateExpiredSessionFromSessionOrigin`: an invalid cookie value →
    201 `owned:false` (Review Focus 1).
  - `TestPreflightSessionOrigin`: OPTIONS from a session origin → 204,
    Allow-Headers contains `X-Sitebin-Session`, and ACAC is true; from a
    plain embed origin → Allow-Headers is exactly `Content-Type` and no
    ACAC.
  - `TestSessionOriginsInertInCommunity`: no provider → no ACAC, and the
    cookie is never honoured.
- [ ] **Step 2: Run** `go test ./internal/httpapi -run 'Create|Preflight|SessionOrigins' -v`.
  Expected: the new tests FAIL; the old ones pass.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the four Go commands. Expected: PASS, including
  `TestCreateCORS`, `TestCreatePreflight` and `apigate_test.go` unchanged.
- [ ] **Step 5: Commit** `feat: a session counts for a new site only from the instance itself or a session origin`.

### Task 3: `GET /account/status`

**Files:**
- Create: `internal/httpapi/status.go` holding
  `func (a *API) accountStatus(w http.ResponseWriter, r *http.Request)`.
- Modify: `internal/httpapi/server.go`: register `GET /account/status`
  **before** the provider routes (:175-179). Check that `ee`'s
  `PublicRoutes` has no such pattern (it does not).
- Test: `internal/httpapi/status_test.go`.

**Interfaces:**
- Consumes: `sessionProvider()` (`server.go:378`); `sessionOriginsHonoured()`
  (Task 2).
- **Behaviour:**
  - no session provider → `notFoundPage`, an HTML 404;
  - otherwise `SessionAccount(r)` → the JSON body and headers from Global
    Constraints;
  - **CORS only for an Origin in SessionOrigins**: ACAO = that origin, plus
    ACAC true. Any other Origin gets the body with no CORS headers.
  - A suspended or expired session is already `ok=false` in
    `SessionAccount`.

- [ ] **Step 1: Write the failing tests.**
  - `TestAccountStatus`:
    - signed in → `{"signed_in":true}`;
    - no cookie → false (Review Focus 2);
    - an invalid cookie → false;
    - headers: no-store, Vary Origin, JSON content type, and no `Set-Cookie`.
  - `TestAccountStatusCORS`: a session origin → ACAO equals the origin and
    ACAC is true; `https://evil.example` → no ACAO; no Origin → no ACAO.
  - `TestAccountStatusCommunity404`: no provider → 404.
- [ ] **Step 2: Run** `go test ./internal/httpapi -run AccountStatus -v`.
  Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the four Go commands. Expected: PASS.
- [ ] **Step 5: Commit** `feat: the instance tells its own pages and its front door whether the visitor is signed in`.

### Task 4: The front-door redirect and the dashboard's publish link

**Files:**
- Modify: `internal/httpapi/pages.go` `landingPage` (:35-42). After the host
  check, if `cfg.FrontDoor`:
  - a valid session (`sessionProvider` then `SessionAccount`) → 303
    `/account`;
  - else 302 `cfg.HomeURL`.
- Modify: `internal/ext/ext.go` `Host` (:259-287): add `PublishURL() string`,
  meaning where a person goes to publish a new site. It is `cfg.HomeURL`
  when the front door is on, else `"/"`.
- Implement it in `cmd/sitebin/host.go` (`extHost`) and in the fakes
  `ee/provider_test.go:376-399` (`fakeHost`) and `ee/tiers_test.go:629-642`
  (`storeHost`).
- Modify: `ee/dashboard.go` `dashView` (:800-809) gains `PublishURL string`,
  set from `p.host.PublishURL()`.
- Modify: `ee/templates.go:224`: `href="{{.PublishURL}}"`.
- Leave `web/static/edit.js:1582` ("Publish something new" → `/`) as it is:
  with the front door, `/` sends a signed-out editor to the front door and a
  signed-in one to the dashboard, whose link then leads to the front door.
- Test: `internal/httpapi/frontdoor_test.go`, `ee/dashboard_test.go` or
  `ee/sitenames_test.go` style.

**Interfaces:**
- Consumes: `cfg.FrontDoor` and `cfg.HomeURL` (Task 1).
- Produces: `ext.Host.PublishURL() string`.

- [ ] **Step 1: Write the failing tests.**
  - `TestFrontDoorRedirects`:
    - front door on, no session → 302 with `Location` = HomeURL;
    - a valid session → 303 `/account`;
    - front door off → 200 landing page (the existing `TestPages`);
    - a non-base host → 404 as today.
  - `TestDashboardPublishLinkFollowsFrontDoor`: the fake host's
    `PublishURL` is `https://sitebin.io` → the empty-dashboard link
    `href="https://sitebin.io"`; `"/"` → `href="/"`.
- [ ] **Step 2: Run** `go test ./internal/httpapi -run FrontDoor -v` and
  `go test -tags ee ./ee -run PublishLink -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the four Go commands. Expected: PASS.
- [ ] **Step 5: Commit** `feat: with a front door, the instance root sends visitors there or to their account`.

### Task 5: The landing page's account link (self-hosted case)

**Files:**
- Modify: `web/static/index.html` topbar (:14-21): after the spacer, add
  `<a id="acct" class="hidden" href="/account"></a>`.
- Modify: `web/static/app.js`: on load,
  `fetch("/account/status", {credentials: "same-origin"})`:
  - 200 → hide `.topbar .tag` and show `#acct` with the text `Sign in` or
    `My account`;
  - anything else, or a network error → change nothing (Review Focus 5).
- Modify: `web/static/app.css`: style `#acct` like the existing topbar
  buttons. There is no new colour.

**Interfaces:**
- Consumes: `GET /account/status` (Task 3).

- [ ] **Step 1: Implement.** Text goes through `textContent` only.
- [ ] **Step 2: Verify.**
  - `node --check web/static/app.js`.
  - `go test ./...`: the embedded assets compile.
  - Run the community image or `go run`, and confirm with curl that `/`
    serves the new markup.
  - The controller's browser check (after Task 6) covers the visual parts:
    community has no link, ee shows Sign in or My account.
- [ ] **Step 3: Commit** `feat: the instance's landing page links to the account when accounts exist`.

### Task 6: `<sitebin-drop session>`, the owned ticket, the QR code and Copy all

**Files:**
- Modify: `web/static/embed.js`:
  - **header doc:** the `session` attribute.
  - **`SCRIPT_DIR`:** the directory URL of `document.currentScript.src`,
    beside `SCRIPT_ORIGIN` (:27-33).
  - **getter:** `get session()`.
  - **`connectedCallback` / `_applyAttrs`:** when `session`, fetch
    `${instance}/account/status` with `credentials: "include"`. On
    `signed_in`, set the fine print to `publishes to your account at <host>`.
  - **`_publish`** (:647): when `session`, set `xhr.withCredentials = true`
    and the header `X-Sitebin-Session: 1`.
  - **`_published`** (:704-749): choose the ticket by `body.owned` alone,
    never by the status (Review Focus 1).
    - Owned: the line `Saved to your account.` plus `Open my account` →
      `${instance}/account`, and the edit password inside a `<details>` with
      summary `Edit password (for scripts and deploys)`.
    - Not owned, with `session`: the line
      `Sign in, and your next drop renews itself.` plus `Sign in` →
      `${instance}/account`.
    - Without `session`: today's ticket, unchanged.
  - **QR and Copy all, for every real (non-demo) publish:**
    - lazily load `${SCRIPT_DIR}qrcode.min.js` once;
    - render as in `app.js:38-49`, and hide the box if the load fails;
    - add the "Copy all" button and handler as in `app.js:109-116`;
    - add their CSS to the component's `STYLE`, mirroring `app.css:432-439`.
- Modify: `internal/httpapi/server.go` and `pages.go`: add
  `GET /_sitebin/qrcode.min.js`, serving `web/vendor/qrcode.min.js` with the
  same headers as `embedScript` (:92). Then `/_sitebin/embed.js` and
  `/_sitebin/qrcode.min.js` share a directory, as the website's
  `assets/embed.js` and `assets/qrcode.min.js` will.
- Test: `internal/httpapi/httpapi_test.go` beside `TestEmbedScriptRoute`
  (:1147), as `TestQRScriptRoute`.

**Interfaces:**
- Consumes: the create key `owned` (Task 2), `/account/status` (Task 3).

- [ ] **Step 1: Write the failing Go test** `TestQRScriptRoute`: 200, a
  JavaScript content type, ACAO `*`, and a body starting like
  `web/vendor/qrcode.min.js`.
- [ ] **Step 2: Run** it and see it FAIL. Implement the route, then run it
  and see it PASS.
- [ ] **Step 3: Implement the embed changes.** All text goes through
  `textContent`; no `innerHTML` carries data.
- [ ] **Step 4: Verify.** `node --check web/static/embed.js`; the four Go
  commands.
- [ ] **Step 5: Controller browser check (Ruling 1 carried over).**
  - Two local containers: the ee image as the instance, with `SESSION_ORIGINS`
    and `EMBED_ORIGINS` set to a local "front door" origin; a static page on
    that origin embeds `<sitebin-drop session>`. Both run under `*.localhost`.
  - Signed out: an anonymous ticket with the sign-in line, a QR code and
    Copy all.
  - Signed in: the fine print reads "publishes to your account", and the
    owned ticket appears.
  - The self-hosted landing page shows its account link (Task 5).
- [ ] **Step 6: Commit** `feat: the drop component publishes into the visitor's account from a front door and shows the full ticket`.

### Task 7: E2E for the front door

**Files:**
- Create: `e2e/frontdoor.ps1` (ASCII only), modelled on `e2e/accounts.ps1`
  (helpers `Assert` :24, `Req` :30, the container start :44-52; readiness
  probe on `/account/login`, **not** `/`).
- Container env:
  - `SITEBIN_ACCOUNT_MODE=accounts`, `SITEBIN_HTTP_ONLY=true`;
  - `SITEBIN_EMBED_ORIGINS=http://door.localtest.me,http://other.localtest.me`;
  - `SITEBIN_SESSION_ORIGINS=http://door.localtest.me`;
  - `SITEBIN_HOME_URL=http://door.localtest.me`;
  - `SITEBIN_FRONT_DOOR=true`.
- Modify: `CLAUDE.md` "Commands": the list of hand-run e2e scripts gains
  `frontdoor.ps1` (enterprise image). Update the count wording.

- [ ] **Step 1: Write the script.** It asserts:
  - signed out: `GET /` → 302 to the home URL;
  - signup with the cookie jar → `GET /` → 303 `/account`;
  - `GET /account/status` → `{"signed_in":true}`, and without the jar →
    false;
  - with `Origin: http://door.localtest.me` → ACAO and ACAC; with
    `Origin: http://other.localtest.me` → no ACAO;
  - a create with the jar + door Origin + the header → 201 `"owned":true`;
  - the same without the header → `"owned":false`;
  - other Origin + header + jar → `"owned":false`;
  - OPTIONS from door → 204 with `X-Sitebin-Session` in Allow-Headers.
- [ ] **Step 2: Build** `sitebin:dev-ee` and run
  `powershell -File e2e/frontdoor.ps1`. Expected: all pass, exit 0, no
  containers left.
- [ ] **Step 3: Check** `grep -nP '[^\x00-\x7F]' e2e/frontdoor.ps1` prints
  nothing.
- [ ] **Step 4: Commit** `test: e2e checks the front door, the status call and session-origin creation`.

### Task 8: Product docs and re-vendoring

**Files:**
- Modify: `README.md`:
  - core env table: `SITEBIN_SESSION_ORIGINS` and `SITEBIN_FRONT_DOOR`
    beside `SITEBIN_EMBED_ORIGINS` (:122) and `SITEBIN_HOME_URL` (:143), with
    the same-site cookie note from the spec (§1);
  - "Embed the drop area" (:662-683): the `session` attribute, `owned`, QR
    and Copy all.
- Modify: `CLAUDE.md`, "Who may manage a site": one paragraph on creation.
  A cross-origin request's session counts only from a session origin with
  `X-Sitebin-Session`; this is `createSiteWith`'s clone without the cookie.
  Point to the spec.
- Modify: the spec, adding a "Corrections (planning)" block:
  - `/account/status` is a core route through `ext.SessionAccounts`, not an
    ee route;
  - the QR library is served at `/_sitebin/qrcode.min.js`;
  - `edit.js`'s "Publish something new" stays `/`.
  - The spec leaves the provenance surface to the builder. A
    session-origin create is recorded like any create (`recordCreate`:
    surface, auth = session, account, IP, user agent). There is no new
    field, because `Meta.Origin` stays the MCP marker.

- [ ] **Step 1: Write the docs.**
- [ ] **Step 2: Commit** `docs: the front door settings, the session attribute and the creation rule`.

### Task 9: The website (branch `content/front-door`)

**Files** (all in `Sitebin-Website/`, and read its `CLAUDE.md` first):
- **Vendor:** copy `public/assets/embed.js` from the product's
  `web/static/embed.js`, and `public/assets/qrcode.min.js` from
  `web/vendor/qrcode.min.js`.
- **`public/index.html`:**
  - the drop at :66 becomes `<sitebin-drop instance="https://app.sitebin.io" session>`;
  - rewrite the caption (:67-71): drops are real; anonymous ones last 24
    hours; signed in, they go to your account and renew;
  - keep `#try`.
- **Nav on all 25 pages:**
  - remove `Open the app` from `.links` and `.sheet`;
  - `Sign in` gets `class="navcta"` in `.links`, and gets
    `data-account-link` in both places;
  - remove the 4 footer `Open the app` items (`index.html:261`,
    `enterprise/index.html:360`, `open-source/index.html:200`,
    `pricing/index.html:360`).
- **`public/assets/site.js`** (already deferred on every page): a section
  that `fetch("https://app.sitebin.io/account/status", {credentials: "include"})`.
  On `signed_in:true`, set the `textContent` of every `[data-account-link]`
  to `My account`. Ignore failures.
- **`public/assets/site.css`:** make `.navcta` the amber CTA it replaces.
  Check the hover and the mobile sheet.
- **`public/pricing/index.html:82`:** "Drop files now" → `../#try`.
- **`public/privacy/index.html`:** rewrite :61-69 and add a short section,
  following spec §7:
  - dropped files are uploaded to app.sitebin.io and handled as in the
    app's sections;
  - the sign-in check sends the app's own session cookie if one exists,
    gets back yes or no, and is logged like any request;
  - the website itself sets no cookie;
  - check the meta description (:7) and the footer line on every page
    against this.
  - **The operator approves this text before the website push** (Task 10).
- **Docs:**
  - `docs/embed/index.html`: the attribute table (:89-100), events
    (:102-121, `owned`), cross-origin (:123-132), and the `#demo` section
    (:134-139: sitebin.io no longer uses demo);
  - `docs/enterprise/index.html` env table (:145-184): both settings;
  - `docs/configuration/index.html`: the row next to `SITEBIN_HOME_URL`
    (:180) pointing to the enterprise table;
  - `docs/api/index.html` near :141-148: `owned` in the create response.
- **`CLAUDE.md` and `README.md` of the website:** drop the "demo mode" lines
  (CLAUDE.md "What has to stay true"; README :15-16 and :202-203); state
  `session` and the re-vendoring of both files.

- [ ] **Step 1: Make the changes.**
- [ ] **Step 2: Run** `powershell -File scripts/check-links.ps1`. Expected:
  green.
- [ ] **Step 3: Check** with grep:
  - `grep -rl "Open the app" public` prints nothing;
  - `grep -rc "data-account-link" public --include=*.html` gives 2 per page;
  - `grep -n " demo" public/index.html` shows no `demo` attribute.
- [ ] **Step 4: Commit** `content: the drop on sitebin.io is real and the nav knows whether you are signed in`.
  Commit the privacy rewrite on its own:
  `content: the privacy page describes the real drop and the sign-in check`.

### Task 10: Ship (needs the operator's OK at each step)

- [ ] **Step 1:** Final whole-branch review, then fixes.
- [ ] **Step 2: Ask the operator.**
  - Show the privacy text (diff) for approval.
  - Ask for the OK to merge, push and deploy, including the env change on
    the server.
- [ ] **Step 3:** Merge `feat/front-door` into main and push.
- [ ] **Step 4: Deploy** as for tags (memory `sitebin-hosted-instance-ops.md`):
  1. LICENSE_ROOTS from auth01, checked against the running binary.
  2. Rollback tag, build.
  3. Snapshot into `backups/`.
  4. **Then** add `SITEBIN_SESSION_ORIGINS` and `SITEBIN_FRONT_DOOR=true` to
     `/opt/sitebin/docker-compose.yml`, keeping a backup copy of the file.
  5. `docker compose up -d`.
  6. Keep 3 rollback images and prune the build cache.
- [ ] **Step 5: Verify live.**
  - `curl -I https://app.sitebin.io/` → 302 to `https://sitebin.io`;
  - `/account/status` with `Origin: https://www.sitebin.io` → ACAO and ACAC;
  - the anonymous OPTIONS preflight from www → 204 with
    `X-Sitebin-Session`;
  - the logs: `backend up` with the new version, the licence, no errors.
- [ ] **Step 6:** Merge `content/front-door` and push the website, then
  verify on www.sitebin.io: no `demo`, the vendored files are served, and
  the nav carries `data-account-link`. The operator tries a real drop
  signed in and signed out.
- [ ] **Step 7:** Message the sessions `sitebin-project-c6` (merge and
  deploy). Update memory.
