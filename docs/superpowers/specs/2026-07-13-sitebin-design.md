# Sitebin — Implementation Design

Date: 2026-07-13 · Status: Approved for implementation (autonomous goal session)
Source spec: `Sitebin-PRD.md` (Draft v0.3). This document records the concrete
technology and design decisions for v1, including resolutions for the PRD's
open questions (§12). Deployment target: `sitebin.ittrail.cloud`.

## 1. Stack

- **Backend:** Go 1.24, single static binary `sitebin`. stdlib `net/http`,
  `golang.org/x/net/webdav`, `golang.org/x/crypto/argon2`. No database, no ORM.
- **Web server / TLS:** Caddy 2 built with `xcaddy` and DNS modules for the
  wildcard cert (cloudflare, hetzner, netcup, porkbun, duckdns). The backend
  generates the Caddyfile from environment variables at startup.
- **Frontend:** hand-written static HTML/CSS/JS (no framework, no build step),
  embedded in the Go binary via `go:embed`. Viewer libraries vendored into the
  repo (pdf.js, markdown-it, DOMPurify, docx-preview + JSZip, highlight.js) so
  the image is fully self-contained/offline.
- **Image:** multi-stage Dockerfile → Alpine final image, non-root user,
  `tini` as init, backend supervises Caddy as a child process (PRD §6.9-A).
  Multi-arch (amd64/arm64) via buildx; CI workflow included.
- **License:** MIT.

## 2. Resolved open questions (PRD §12)

| Question | Decision |
|---|---|
| Multi-file upload | Both: multi-select/folder upload (webkitRelativePath preserved) **and** zip upload with server-side extraction (multipart field `zip`). |
| View-password UX | Forward-auth + cookie. Gate page is returned by the authz endpoint (401 body); it POSTs to a reserved backend route on the *site's own origin* (`/_sitebin/unlock`), which sets a host-scoped signed cookie and redirects. Works for SPAs, assets, and custom domains alike. |
| WebDAV endpoint shape | Path on main domain: `https://<base>/dav/<edit_uuid>/`. |
| `entry_file` selectable | Yes — viewer mode with multiple files exposes an entry-file selector in the edit UI / `PUT` API. |
| Rate limits | In-memory token buckets. Defaults: site creation 30/hour/IP (burst 10); password verification (edit + view + WebDAV) 10 failures per 5 min per (IP, target) then 429 with backoff. Env-tunable. |
| License | MIT. |

## 3. Key architectural decisions (deltas / clarifications vs. PRD)

1. **`_raw` lives inside the served root** (`files/_raw/…`), not beside it —
   Caddy stays a dumb file server and the wrapper can fetch `/_raw/<file>`
   same-origin. Switching viewer→webserver moves `_raw/*` back up and removes
   the generated wrapper.
2. **Shared viewer assets, not per-site copies.** Every site origin routes
   `/_sitebin/*` to the backend (a `handle` before `file_server` in Caddy).
   The generated wrapper references `/_sitebin/assets/...` (embedded FS), so
   pdf.js etc. are never duplicated per site. `/_sitebin/` and `_raw/` are
   reserved names, rejected in uploads.
3. **authz on every content request.** The wildcard and custom-domain blocks
   always run `forward_auth` → `/internal/authz?host={host}`. The backend
   answers from `meta.json` (with small in-memory cache): `200` serve,
   `401` + gate page (password protected, no valid cookie), `410` expired,
   `404` unknown. This gives password *and* expiry enforcement with a static
   Caddyfile.
4. **Two listeners.** Public mux on `:8080` (proxied by Caddy: API, UI, edit
   pages, WebDAV, `/_sitebin/*`). Internal mux on `:9000` (`/internal/authz`,
   `/internal/tls-check`, `/internal/health`) — never proxied, so internal
   endpoints are unreachable from the public internet by construction.
5. **View-password cookie:** `sitebin_v=<HMAC token>` (site id + expiry,
   signed with a per-instance secret persisted at `/data/.secret`), host-only
   cookie on the site's origin, TTL 12h.
6. **Argon2id** (t=1, m=64MiB, p=4) for both passwords. Because WebDAV sends
   Basic auth per request, successful verifications are cached in memory
   (key: edit_uuid + SHA-256(password), TTL 5 min) to avoid 64MiB hash work
   per request. Failures always hit the rate limiter.
7. **IDs:** 26-char lowercase base32 (a–z, 2–7) = 130 bits for both view id
   (subdomain label) and edit id. Edit password: 128-bit random, base62.
8. **Windows dev fallback:** index entries are symlinks on Linux (source of
   truth per PRD); on Windows (tests only) the store falls back to directory
   junctions so the suite runs without admin rights.
9. **Expiry lifecycle:** authz serves `410` once `now > expires_at`; the
   cleanup worker (10 min interval) deletes site folder + dangling index
   links after `expires_at + 24h`, so Gone is observable for a day.
10. **Local/testing mode:** `SITEBIN_HTTP_ONLY=true` generates an HTTP-only
    Caddyfile (`auto_https off`), enabling full E2E on
    `sitebin.localtest.me` without certificates. Production uses wildcard
    DNS-challenge cert + on-demand TLS for custom domains, exactly per PRD.
11. **Directory browse:** webserver-mode sites without `index.html` get
    Caddy's `file_server browse` listing (styled default) instead of 404.
12. **DNS providers:** first-class single-token support for
    `cloudflare`, `hetzner`, `duckdns`; `netcup`/`porkbun` compiled in but
    need `SITEBIN_TLS_SNIPPET` (raw lines injected into the wildcard `tls`
    block) for their multi-credential config. Escape hatch documented.

## 4. Repository layout

```
cmd/sitebin/            main: subcommands run|server|caddyfile|cleanup|healthcheck
internal/config/        env parsing + defaults
internal/ids/           id + secret generation
internal/auth/          argon2, cookie tokens, verify-cache, rate limiting
internal/store/         filesystem store: sites, meta.json, indexes, uploads, zip
internal/viewer/        wrapper generation (entry-file type → renderer)
internal/httpapi/       public + internal handlers, edit/landing UI, WebDAV
internal/caddygen/      Caddyfile generation from config
internal/cleanup/       expiry scanner/deleter
internal/supervisor/    all-in-one: spawn caddy, signal handling
web/                    embedded assets: app UI, viewer wrapper JS, vendor libs
e2e/                    end-to-end test scripts + fixtures (pdf, docx, md, zip)
deploy/docker-compose.yml
Dockerfile · README.md · LICENSE · .github/workflows/release.yml
```

## 5. API (unchanged from PRD §7)

All PRD endpoints implemented as specified; auth header `X-Edit-Password`.
Additions: `POST /_sitebin/unlock` (view-password gate, site origins only),
`GET /_sitebin/assets/*` (shared viewer assets), `GET /internal/health`.
`PUT /sites/{edit_uuid}` accepts `mode`, `entry_file`, `view_password`
(+ `view_password_protected`), `webdav_enabled`, `expires_at` (RFC3339 or
null). File upload: `POST /sites/{edit_uuid}/files` multipart (`files` parts,
optional `zip` part extracted server-side); same fields accepted on create.

## 6. Testing

- **Unit** (run on Windows dev + in CI): store (create/update/delete,
  traversal & symlink-upload rejection, zip bombs/limits, junction fallback),
  auth (argon2 round-trip, token forgery, rate limits), httpapi (httptest:
  every endpoint, authz matrix: open/protected/expired/missing), caddygen
  (golden files for HTTPS + HTTP-only), viewer wrapper generation, cleanup.
- **E2E against the real Docker image** (the deliverable): build image, run
  with `SITEBIN_BASE_DOMAIN=sitebin.localtest.me`, `SITEBIN_HTTP_ONLY=true`,
  port 80. Script exercises: create (UI-shaped multipart + pure API), view on
  `<uuid>.sitebin.localtest.me`, viewer mode for PDF/MD/DOCX/txt/image,
  password gate (401 → unlock → cookie → 200), expiry → 410, WebDAV
  (PROPFIND/PUT/GET/DELETE via curl), custom domain (Host-header on
  catch-all + tls-check inside container), rate limiting, health check,
  non-root verification, volume persistence across container recreate.
- **Browser E2E:** drive the real UI in Chrome (create flow, edit flow,
  viewer rendering, password gate) and screenshot for UX review.

## 7. Security checklist (PRD §9 → implementation)

- Filename sanitation: reject `..`, absolute paths, `\`, reserved names
  (`_sitebin`, `_raw`, `meta.json`), control chars; per-part and per-site
  size + file-count caps enforced streaming (multipart *and* zip extraction).
- Uploaded symlinks impossible: files created via `O_CREATE` writes only;
  zip entries with symlink mode rejected; WebDAV cannot create links.
- Internal endpoints on a separate listener (decision 4).
- Security headers on UI + gate + wrapper pages (CSP, nosniff,
  frame-ancestors, referrer-policy). Markdown sanitized with DOMPurify.
- tls-check endpoint mandatory-on (decision: cannot be disabled).
- Abuse: `DELETE` by operator = documented `docker exec sitebin-admin
  delete <view-uuid|domain>` admin subcommand + README abuse section.
