# Not-found pages instead of directory listings — design

Date: 2026-11-05. Status: approved by the operator in conversation (2026-11-05),
pending the builder check.

## Why

A visitor who asks a Sitebin site for something that is not there gets one of
two bad answers today:

- **A missing file** gets Caddy's empty `404` — a blank page, even when the
  site ships its own `404.html`. Found on IT-Trail, Markus Furtlehner and
  Morgenfertig, which all ship one and never see it.
- **A folder without `index.html`** gets Caddy's directory listing
  (`file_server browse`, `internal/caddygen/caddygen.go` `writeFileServing`).
  Every `assets/`, `images/`, `_astro/` and `__MACOSX/` folder of a real site
  lists its whole content to anyone who types the path.

The listing was a deliberate choice of the first design
(`2026-07-13-sitebin-design.md`, point 11: "styled default instead of 404").
The operator's reasoning for reversing it: a drop that is only files — three
PDFs, a download — is what the **File viewer** mode is for. A **web server**
drop is a website, and a website answers a missing page with a 404, never with
a list of its files.

## What the live instance holds (survey 2026-11-05, read-only)

37 sites: 31 web server (none with SPA fallback), 3 viewer, 3 container.

- 13 web server sites have a root `index.html` but sub-folders without one
  (assets, images, build output, `__MACOSX`). They list today, 404 after. Wanted.
- 10 web server sites have no root `index.html`:
  - 4 anonymous test drops (one locked, the others expire 2026-11-06) — irrelevant;
  - 5 copies of one ZIP drop (2026-11-01, expire 2026-11-08) whose
    `index.html` sits inside a wrapper folder `bobby_gift_final/`. Their root
    will 404. Accepted: the uploader handles the wrapper (operator decision);
  - `RustDesk` (`help.ittrail.at`), a single `.exe`. Its root will 404; the
    direct link keeps working. The operator switches it to File viewer after
    the deploy — which needs the viewer change below to be useful.
- 3 sites ship a root `404.html` (IT-Trail, Markus Furtlehner, Morgenfertig).
  They get their own page with no change on their side.

## Goals

1. **No mode ever serves a directory listing.** `browse` goes, everywhere
   (view hosts, custom domains incl. host/path mounts, `/v/<id>/` path views).
2. **Web server mode:** a folder without an index file and a missing file both
   answer **404** — at the root as in any sub-folder.
3. **The 404 body is a page, never empty:**
   - the site's own not-found page when it has one (below);
   - otherwise Sitebin's own styled page — the same look as every other page
     Sitebin answers with (`msgPage` / `basePageTmpl`, the dark/amber
     claim-ticket look), e.g. "Page not found".
4. **A site's own not-found page, per drop:**
   - **convention:** a regular file `404.html` at the site root is used
     automatically (as GitHub Pages, Netlify and Cloudflare Pages do; Astro,
     Hugo and Jekyll emit it);
   - **setting:** the owner may name another file of the site instead — edit
     page, JSON API settings, MCP `update_site`;
   - when the named file does not exist (deleted, renamed, not uploaded yet),
     Sitebin's styled page answers instead. *(Agent's call.)*
5. **The status is always 404, and every 404 carries `Cache-Control: no-store`**
   — Sitebin's page (`renderPage` already sets it) and the site's own page
   alike. The site's cache policy never applies to a 404 (it is set on 2xx/304
   only, unchanged). `e2e/caching.ps1` line ~124 ("a 404 carries no
   Cache-Control") changes to require `no-store`. With `no-store` a browser
   keeps no copy and sends no conditional request, so the `ETag` /
   `Last-Modified` that `file_server` adds to a custom page cannot turn into
   an empty 304-as-404; the builder may still drop them. *(Agent's call, after
   the builder check.)*
6. **File viewer: a "no preview — download" view** for files the viewer cannot
   show (executables, archives, disk images, fonts, other binaries), instead of
   today's fallback to the text renderer (`internal/viewer/viewer.go`
   `RendererFor` → `"text"`; `web/viewer/viewer.js` `renderText` fetches the
   whole file and shows up to 2 MB of it as text). The view shows the file
   name, size and the download button. Text and code files keep their
   highlighted preview.
7. **Say so where a web server drop has no start page** *(agent's call)*:
   - the edit page shows a notice when a web server site has no root index
     file: visitors see a 404 at the root; offer to switch to File viewer;
   - the API and MCP add a line to the existing `warnings` field (both
     already carry one) wherever they return a site after a write or a read:
     JSON API create and file upload responses, MCP `create_site`,
     `write_files`, `update_site` and `get_site`;
   - the condition is: web server mode, no SPA fallback, **at least one file**,
     and no root index file. An empty site gets no warning — agents create a
     site first and fill it through `open_upload`, and must not be pushed to
     switch a site to the viewer before its website has arrived;
   - the wording names both ways out, website first: "This site has files but
     no index.html at its root, so visitors see a 404 there. Upload an
     index.html, or set mode to viewer if the files are documents to show or
     download." *(Builder may polish the words, not the order.)*

## Non-goals

- **No automatic mode choice** on the JSON API, curl or MCP. The default stays
  `webserver` (`internal/store/store.go` `Store.Create`) whatever the files are; the drop form keeps
  its existing suggestion (`embed.js` `_suggestMode`). Whoever wants a viewer
  switches to it. (Operator decision.)
- **No unwrapping of a ZIP's single top-level folder.** The uploader takes care
  of it. (Operator decision.)
- **No migration of existing sites.** Nothing is switched or rewritten.
- **Container sites are not changed.** They never reach the file server, so they
  never listed. A 404 the app answers is the app's and passes through untouched
  — an API answering JSON 404s must not get an HTML page. Sitebin's own answers
  for containers ("This app is not running" 503, "Nothing here" 404) already
  come from `msgPage` and are already styled.
- **SPA fallback is not changed**: it never 404s (everything falls to
  `/index.html`), and the not-found page does not apply to it.
- **No custom not-found page for viewer sites.** The viewer is not a website;
  a missing path there (e.g. under `/_raw/`) gets Sitebin's styled page.
  *(Agent's call.)*
- Analytics: whether a 404 counts as a page view is not changed by this work.

## Anti-goals (would make it a failure even if it works)

- **A not-found page that escapes the site's security headers.** A site's own
  `404.html` is user content. Served without the untrusted CSP of a site that
  lacks the trust marker, it becomes the one page on the site where a phishing
  kit may post forms and load scripts freely. Every 404 response — custom or
  Sitebin's — must carry exactly the headers a 200 from the same site carries
  (untrusted CSP or not by the trust marker, HSTS, nosniff, …). This needs a
  test against the real Caddy, on a site with and one without the trust marker.
- **A client choosing which file becomes the not-found page.** If the page's
  path travels from authz to Caddy in a request header (see "Mechanism"), that
  header joins the list Caddy strips from the client's request before the gate,
  like `X-Sitebin-Upstream`, `X-Sitebin-Site` and the others.
- **A 404 that turns into a 200**, or a not-found page cached for a year.
- **Any app's own 404 rewritten** (containers, see above).
- **Anything new on the hot path that asks the extension.** All of this is
  core and decided from `meta.json` and the site's files.

## Constraints from the codebase

- **Rules live in Go, the Caddyfile only copies a value**, as for
  `X-Sitebin-Cache` (`internal/httpapi/cache.go`). Which page a site's 404 uses
  is authz's answer, not a Caddyfile decision.
- **Prefer no extra filesystem work per request.** Checking whether the
  not-found page exists only when a 404 actually happens (e.g. Caddy's `file`
  matcher inside `handle_errors`) is better than a `stat` in authz on every
  request.
- **Three places serve files**: the view wildcard block, the custom-domain
  catch-all (with `X-Sitebin-Strip` under a host/path mount and the folder
  from `X-Sitebin-Site`), and the main domain's `/v/<id>/` path views (with
  `uri strip_prefix`). All three must get the same behaviour; the not-found
  page's path is relative to the site's root after the strip.
- **`.sitebin-spa` and `.sitebin-trusted` are hidden** by `file_server hide`;
  neither may ever be served as a not-found page.
- **`store.Meta` is on-disk data** with no migration: a new field is
  `omitempty`, and missing means "use the convention" (`404.html` if it exists).
- **The MCP output schema is a contract for open sessions.** A field added to
  `SiteResult` makes a client that cached the schema reject a site-shaped
  result carrying it until it reconnects (found 2026-09-24 with `name`). The
  precedent is `cache` (`internal/mcp/ops.go`): `Settings.Cache` as input,
  `SiteResult.Cache` `omitempty` and present only when it is not the default,
  so only sites that use the setting ever carry the new field. Do the same:
  `SiteResult.NotFoundPage` (`not_found_page,omitempty`) present only when set.
  Anything else new for the agent goes in `warnings`, which every client
  already knows. *(Agent's call, after the builder check.)*
- **What counts as "no start page"** for goal 7 must match what Caddy actually
  serves as a folder's index (Caddy's default index names are `index.html` and
  `index.txt`), or the notice lies.
- **Community build stays whole.** Everything here is core; no seam change.
- **Coupled repos.** `README.md` ("Directory listings appear when no index
  exists", section Modes) and the website's `public/docs/using/index.html`
  (line ~113, "directory listing instead") promise the listing. Both change,
  and both gain a paragraph on the not-found page. API and MCP docs on the
  website gain the new setting. Plain ship order: product first, live check,
  then the website.

## The setting

- One optional string per site, the path of the not-found page inside the
  site, e.g. `404.html` or `errors/missing.html`. Empty = the convention.
- Name *(agent's call, builder may refine)*: `not_found_page` in `meta.json`,
  the JSON API settings and `mcp.Settings`.
- Validation on write: a clean relative path inside the site (no `..`, no
  leading `/` after normalisation, no `_sitebin/`, not a `.sitebin-*` marker),
  ending in `.html` or `.htm` *(agent's call: the page is a page)*. Existence is
  NOT required at write time — a page may be set before it is uploaded; goal 4's
  fallback covers it, and the edit page may say the file is missing.
- Applies in web server mode without SPA fallback. In other modes it is kept
  but has no effect (like `spa_fallback` outside web server mode).

## Mechanism (left to the builder, within the constraints)

The likely shape, to be verified against the Caddy version in the image:

- Remove `browse` from `writeFileServing`. Without it, `file_server` answers a
  folder without an index with a 404 error, as it does a missing file.
- authz's `admit` names the site's not-found page for a file site in a new
  response header that Caddy copies (and strips from clients first).
- A `handle_errors` block per content site block: for a 404 where that file
  exists, rewrite to it and serve it with `file_server` and status 404
  (Caddy's `file_server` has a `status` option for exactly this); otherwise
  hand the request to the backend for Sitebin's styled page (a `/_sitebin/…`
  route rendering `msgPage(404, …)`), which already sets `no-store`.
- Verify what `handle_errors` sees: the request headers authz's copy wrote,
  the stripped URI, the `root` variable, and the response headers the
  `header` directives set inside the `route` (see the anti-goal on security
  headers). If the headers set before the error do not survive into the error
  route, the error route sets them itself, from the same generated lines.

Known limit to document, not to fix: the not-found page is served AT the
missing URL (no redirect), so relative links inside `404.html` resolve against
the missing path's folder. Pages generated by Astro/Hugo/Jekyll use root-relative
links and work; hand-written pages should too. Under a host/path mount,
root-relative links point outside the mount, as they do on every page there.

## Tests that must exist

- caddygen: no `browse` anywhere in the generated Caddyfile; a `handle_errors`
  in each content block; the new header in every strip list.
- authz: the header names the configured page, the convention, or nothing for
  non-file modes / SPA.
- Settings: validation (accepted and refused paths), API and MCP round trip;
  `not_found_page` absent from `get_site` output unless set.
- Warnings: the "no start page" line appears for a web server site with files
  and no root index (create, upload, `write_files`, `get_site`), and not for
  an empty site, a site with a root index, an SPA site or a viewer site.
- Viewer: a binary gets the download view (no full fetch), a `.go`/`.txt`
  file still gets the highlighted preview.
- E2E through the real Caddy (extend `e2e.ps1`, `paths.ps1`, `spa.ps1` as
  fits): folder without index → 404 with Sitebin's page; missing file → 404
  with the site's `404.html`; named page wins over the convention; named page
  missing → Sitebin's page; status is 404 and `Cache-Control: no-store` in
  every case, also for a conditional request (`If-None-Match`) to a missing
  path; untrusted site's custom 404 carries the untrusted CSP, trusted site's
  does not; same under a host/path mount and a `/v/<id>/` path view; SPA site
  unchanged; container app's own 404 passes through unchanged.

## After the deploy (operator)

- Switch `RustDesk` (`help.ittrail.at`) to File viewer, once the download view
  is live.
- Check IT-Trail, Markus Furtlehner and Morgenfertig show their own 404 page.
