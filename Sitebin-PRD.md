# PRD — Sitebin

> A **site** is the core unit throughout this document: one upload = one site (served as-is or previewed). ("site" replaces the earlier "fiddle" wording — swap for "drop" if preferred.)

**Status:** Draft v0.3
**Owner:** _you_
**Type:** Open-source, self-hostable web app + optional hosted service
**Distribution:** Ships as a Docker container (single all-in-one image; Compose for the multi-service variant)

---

## 1. Summary

Sitebin is a dead-simple, no-login service for publishing and previewing files on the web. A user (or an AI agent via the API) uploads one or more files and instantly gets a public URL. Each upload — a **site** — can behave in one of two ways: as a **real static web server** (files served as-is, e.g. an HTML app or static site) or as a **file viewer** (an in-browser preview for formats like PDF, Markdown, or DOCX).

There are **no user accounts**. A site is addressed by random, unguessable URLs, and editing is gated by a per-site edit password. All state lives on the **filesystem** — one folder plus a metadata file per site. **No database.**

It ships as a single `docker run` for self-hosting, with an optional hosted free/paid tier.

---

## 2. Motivation

Delivering small, throwaway artifacts to a client or colleague — an HTML prototype, a slide export, a PDF, a rendered Markdown doc — is disproportionately painful today. Full platforms (Vercel, Netlify, Coolify) require git flows, accounts, builds, and ops. Instant-hosting SaaS (Tiiny.host, Static.app) solve the flow but are closed-source and can't be self-hosted, which rules them out for confidential client work or teams that don't want a third-party SaaS holding the content.

The gap Sitebin targets: a **lightweight, open-source, self-hostable** "drop a file → get a link" tool with custom-domain support and a built-in multi-format viewer — kept as simple as a code playground. The hard infrastructure part (per-domain TLS, reverse proxy) is a solved primitive via Caddy's on-demand TLS, so the product can stay small.

---

## 3. Goals & Non-Goals

### Goals
- Upload → instant public URL, with zero login.
- Two explicit modes: **webserver** (serve as-is) and **viewer** (in-browser preview).
- Built-in viewer for at least PDF, Markdown, and DOCX at launch.
- Custom domains with automatic TLS certificates and reverse proxy.
- Optional view-password protection and optional expiry date.
- **Optional per-site WebDAV access** so a site's files can be mounted as a network drive.
- Public API usable by AI agents and scripts, authenticated by the edit URL + edit password.
- Filesystem-only persistence (a metafile beside each site folder). No DB.
- **Ships as a Docker container** — self-hostable with a single `docker run` (all-in-one image) plus a Compose option; open-source license.

### Non-Goals (initially)
- No user accounts, sessions, or dashboards spanning multiple sites.
- No MCP server — agents are expected to use the HTTP API directly.
- No server-side build step or code execution (no PHP/Node/JSX transpile). Launch scope is **static files only**; JSX/React must be pre-built by the user before upload.
- No collaborative/multi-user editing.
- No analytics beyond basic operational logging (future).

---

## 4. Core Concepts & Terminology

| Term | Meaning |
|---|---|
| **Site** | One upload unit: a folder of files + settings. |
| **View URL** | Public address of a site: a random-UUID **subdomain** on the main domain (e.g. `a1b2c3.sitebin.app`). |
| **Edit URL** | A random-UUID **path** on the main domain (e.g. `sitebin.app/e/9f8e7d…`) that opens the edit UI / is used as the API resource. Distinct from the view UUID. |
| **Edit password** | Random-generated secret returned once at creation; required to edit a site (UI, API, and WebDAV). |
| **View password** | Optional, user-set secret that gates public viewing when password protection is on. |
| **Mode** | `webserver` or `viewer`. |
| **WebDAV** | Optional per-site network-drive access to the site's files, gated by the edit password. |
| **Metafile** | `meta.json` stored beside the site's files, holding all UUIDs, hashes, and settings. |
| **Custom domain** | Zero or more external domains the site also answers on, with auto-TLS. |

---

## 5. Functional Requirements

### 5.1 Site creation
- Anyone can create a site without an account, via the web UI or the API.
- On creation the system generates: a **view UUID** (subdomain), a separate **edit UUID** (path token), and a random **edit password**.
- The creation response returns the **view URL**, the **edit URL**, and the **edit password exactly once**. The plaintext edit password is never stored (only its hash) and never retrievable again.
- Default mode is `webserver` unless `viewer` is requested.

### 5.2 Modes
1. **Webserver mode** — Files are served exactly as uploaded. `index.html` is the entry point; the site behaves like a normal static site/app. This is the "act as a real web server" mode.
2. **Viewer mode** — Instead of raw delivery, the site presents a minimal in-browser viewer that renders the uploaded document. Recommended implementation keeps the server dumb (see §6.4): at save time the backend writes a small static viewer wrapper as the entry point, and the raw file is kept in a reserved subpath. This way both modes are served by the same static file server.

### 5.3 Viewer format support (launch)
- **PDF** — rendered inline (e.g. pdf.js).
- **Markdown** — rendered to sanitized HTML (e.g. markdown-it), with code highlighting.
- **DOCX** — rendered to HTML client-side (e.g. mammoth.js / docx-preview).
- **Plain text / source** — shown with basic formatting.
- **Images** — shown directly.
- The set is extensible; each format is an isolated renderer module. Formats without a good client-side renderer may later use server-side conversion (future).

### 5.4 Custom domains
- A site can have **zero or more** custom domains.
- Adding a domain triggers automatic certificate provisioning and reverse-proxy/file-serving on first request (Caddy on-demand TLS).
- The user is shown the DNS record they must set (A/AAAA/CNAME to the host).
- Removing a domain stops serving it.

### 5.5 Password protection (view)
- A per-site toggle. When on, the public view (both modes and custom domains) requires the view password before any content is served.
- Enforced at the edge via forward-auth to the backend, which validates against the stored hash and issues a short-lived cookie (see §6.5).

### 5.6 Expiry
- A site may carry an optional expiry timestamp.
- After expiry the site stops serving (returns 410 Gone) and is eligible for deletion by a background cleanup job.
- On the hosted tier, a maximum expiry may be enforced by plan.

### 5.7 WebDAV access (optional, per site)
- A per-site on/off setting (default **off**).
- When enabled, the site's files are exposed over **WebDAV**, so the user can mount the site as a network drive (macOS Finder, Windows Explorer, Cyberduck, rclone, etc.) and add/replace/delete files without the UI or API.
- Access is **read + write** and therefore equivalent to full edit rights; it is authenticated by the site's **edit password** (see §6.7 for the endpoint and mechanism).
- Toggling it off immediately stops serving WebDAV for that site.

### 5.8 Editing
- Editing requires the edit UUID **and** the edit password (UI, API, or WebDAV).
- Editable: upload/replace/delete files, switch mode, manage custom domains, toggle & set view password, toggle WebDAV, set/clear expiry, delete the site.

### 5.9 UI (intentionally minimal — playground-simple)
A single-screen create/edit view containing only:
- File upload (drag-and-drop, one or many; folder/zip upload for multi-file sites).
- Mode selector (`webserver` / `viewer`).
- Custom domains (add/remove list, 0..n).
- Password protection (on/off + password field).
- **WebDAV (on/off).** When on, show the mount URL and a reminder that the edit password is the login.
- Expiry (optional date/time).
- On save: display view URL, edit URL, and edit password (copyable), with a clear "store the edit password now" warning.

### 5.10 Public API
- Fully public; the only credential is the edit URL (edit UUID) + edit password. See §7.

---

## 6. Architecture

### 6.1 Principles
- **Filesystem is the database.** One folder per site; a `meta.json` beside it. Lookups by secondary keys (edit UUID, custom domain) use filesystem index directories of symlinks — no DB, no scanning.
- **Caddy is the whole web server**: TLS termination, routing, and static file serving. The backend service handles uploads, metadata, auth checks, WebDAV, and the TLS ask endpoint.

### 6.2 Storage layout
```
/data/
  sites/
    <view-uuid>/
      meta.json                 # all settings, hashes, UUIDs
      files/                    # served root (webserver mode) or wrapper+raw (viewer mode)
        index.html
        ...
      _raw/                     # (viewer mode) original uploaded file(s)
  edit-index/
    <edit-uuid>  ->  ../sites/<view-uuid>     # symlink: resolves edit UUID → site
  domain-index/
    <custom-domain>  ->  ../sites/<view-uuid> # symlink: resolves custom domain → site
```
- The site folder is named by the **view UUID**, so the subdomain label maps to it directly.
- `edit-index/` and `domain-index/` give O(1) filesystem resolution for the other two lookup keys without a database.

### 6.3 Routing (Caddy)

**Random view subdomains** (single wildcard cert via DNS challenge):
```caddyfile
*.sitebin.app {
    tls {
        dns <provider> {env.DNS_TOKEN}
    }
    root * /data/sites/{labels.2}/files
    file_server
}
```
`a1b2c3.sitebin.app` → `/data/sites/a1b2c3/files/`.

**Custom domains** (on-demand TLS, ask endpoint gates issuance):
```caddyfile
{
    on_demand_tls {
        ask http://backend:9000/internal/tls-check
    }
}

https:// {
    tls { on_demand }
    root * /data/domain-index/{host}/files
    file_server
}
```
`client.com` → symlink → `/data/sites/<view-uuid>/files/`.

**Main domain** (edit UI + API + WebDAV; never serves user content):
```caddyfile
sitebin.app {
    handle /api/* { reverse_proxy backend:8080 }
    handle /dav/* { reverse_proxy backend:8080 }   # WebDAV
    handle /e/*   { reverse_proxy backend:8080 }    # edit UI
    handle        { reverse_proxy backend:8080 }    # landing / create UI
}
```

> **Security invariant:** user-uploaded content is served **only** on random subdomains and custom domains, never on `sitebin.app`. Each site therefore gets its own origin (unguessable subdomain), giving natural origin isolation for hosted HTML/JS.

### 6.4 Viewer mode without a smart server
Recommended: viewer mode is realized by **generating a static viewer wrapper at save time**, so Caddy stays a pure file server for both modes.
- On save with `mode = viewer`, the backend writes `files/index.html` = the viewer bundle (pdf.js / markdown-it / mammoth, chosen by file type) and moves the original into `_raw/`.
- The wrapper fetches `/_raw/<file>` client-side and renders it.
- Switching a site back to `webserver` mode restores raw files as the served root.

This avoids per-request mode branching in Caddy. (Alternative if client-side rendering is insufficient for a format: reverse-proxy that site to a server-side render service — deferred.)

### 6.5 View-password enforcement
For password-protected sites, insert a forward-auth check before `file_server`:
```caddyfile
forward_auth backend:8080 {
    uri /internal/authz?site={labels.2}
    copy_headers Set-Cookie
}
```
The backend returns 200 if a valid auth cookie is present, else 401 with a redirect to a password prompt; on correct password it sets a short-lived, site-scoped cookie. Non-protected sites skip this.

### 6.6 On-demand TLS ask endpoint
`GET /internal/tls-check?domain=<d>` → `200` if `/data/domain-index/<d>` exists, else `404`. Stateless, filesystem-only. **Mandatory** — without it, anyone could point arbitrary domains at the host and exhaust certificate issuance (DoS).

### 6.7 WebDAV handler
- Exposed on the main domain at `https://sitebin.app/dav/<edit-uuid>/`, reverse-proxied to the backend. (A dedicated `dav.sitebin.app` subdomain is an alternative.)
- Backed by the backend's WebDAV handler (Go `golang.org/x/net/webdav`) **rooted at that site's `files/` directory**, or a Caddy build with the `webdav` plugin scoped per site.
- **Enabled only when** the site's `meta.json` has `webdav_enabled: true`; otherwise the endpoint returns 404/403.
- **Auth:** HTTP Basic over TLS; the password is the site's **edit password** (username ignored or set to the edit UUID). Standard WebDAV clients handle Basic auth natively.
- **Scope & safety:** operations are confined to the site's `files/`; the same path-traversal and symlink protections as uploads apply. WebDAV write access equals full edit rights, so it is gated exactly like the API.

### 6.8 Components
- **Caddy** — TLS, routing, static serving.
- **Backend service** (single small binary/app) — create/edit API, metadata read/write, filesystem indexing (symlinks), auth checks, **WebDAV handler**, TLS ask endpoint, edit UI, viewer-wrapper generation.
- **Cleanup worker** (cron/loop) — deletes expired sites and dangling index symlinks.
- **Shared volume** — `/data`, mounted by Caddy (read) and backend (read/write).

### 6.9 Containerization (primary distribution)
Sitebin is designed as a **Docker container** first. Two supported shapes, same codebase:

**A. All-in-one image (recommended default, "docker run and done").**
One image runs Caddy + backend + cleanup worker under a minimal init/supervisor (e.g. `s6-overlay`, `tini`+small supervisor, or the backend spawning Caddy as a child). This is the simplest self-host experience.
```bash
docker run -d \
  -p 80:80 -p 443:443 \
  -v sitebin-data:/data \
  -e SITEBIN_BASE_DOMAIN=sitebin.example \
  -e SITEBIN_DNS_PROVIDER=cloudflare \
  -e SITEBIN_DNS_TOKEN=... \          # for the *.sitebin.example wildcard cert
  ghcr.io/<you>/sitebin:latest
```
- **Single persistent volume** (`/data`) holds sites, indexes, metafiles **and** Caddy's certificate/ACME state — back up one path, back up everything.
- Configuration is entirely via environment variables (see §6.10); no config files to edit for the common case.
- Image should be multi-arch (amd64 + arm64) so it runs on a Raspberry Pi / small ARM VPS as easily as on x86.

**B. Compose (multi-service variant).**
Separate `caddy` and `backend` (+ optional `cleanup`) containers sharing the `/data` volume, for operators who want to scale or replace pieces independently. Ships as a `docker-compose.yml` in the repo.

**Container requirements & notes:**
- Binds host ports **80 and 443**; on-demand + wildcard TLS need outbound reach to the ACME provider.
- Runs as a **non-root** user where possible; place data under `/data`.
- Healthcheck endpoint on the backend (e.g. `GET /internal/health`) for `HEALTHCHECK` and orchestrators.
- Stateless except for the `/data` volume — the container can be destroyed/recreated freely as long as the volume persists.
- Graceful shutdown so in-flight uploads and Caddy connections drain on `SIGTERM`.

### 6.10 Configuration (environment variables)
| Var | Purpose |
|---|---|
| `SITEBIN_BASE_DOMAIN` | Main domain, e.g. `sitebin.example` (view subdomains + edit UI live here). |
| `SITEBIN_DNS_PROVIDER` / `SITEBIN_DNS_TOKEN` | DNS-challenge provider + token for the `*.base` wildcard cert. |
| `SITEBIN_DATA_DIR` | Data root (default `/data`). |
| `SITEBIN_ACME_EMAIL` | ACME account email. |
| `SITEBIN_MAX_SITE_BYTES` / `SITEBIN_MAX_FILES` | Per-site limits. |
| `SITEBIN_MAX_EXPIRY_DAYS` | Optional cap on expiry (hosted tier). |
| `SITEBIN_WEBDAV_ENABLED` | Global switch to allow/forbid WebDAV across the instance (per-site toggle still applies). |
| `SITEBIN_RATE_LIMIT_*` | Anonymous-create and edit-attempt rate limits. |
| `SITEBIN_ENABLE_SIGNUPS` / `SITEBIN_READONLY` | Operational toggles (e.g. freeze new sites). |

---

## 7. API Specification

Base: `https://sitebin.app/api`. No accounts. Edit operations authenticate with the **edit UUID** (in the path) plus the **edit password** (header `X-Edit-Password`). Responses are JSON; file bodies via multipart.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `POST` | `/sites` | none | Create a site (files + settings). Returns view URL, edit URL, edit password (once). |
| `GET` | `/sites/{edit_uuid}` | edit pw | Read site settings/metadata (never returns the edit password). |
| `PUT` | `/sites/{edit_uuid}` | edit pw | Update settings (mode, view password, expiry, **webdav_enabled**). |
| `POST` | `/sites/{edit_uuid}/files` | edit pw | Upload/replace files. |
| `DELETE` | `/sites/{edit_uuid}/files/{path}` | edit pw | Delete a file. |
| `POST` | `/sites/{edit_uuid}/domains` | edit pw | Add a custom domain. |
| `DELETE` | `/sites/{edit_uuid}/domains/{domain}` | edit pw | Remove a custom domain. |
| `DELETE` | `/sites/{edit_uuid}` | edit pw | Delete the site. |
| `ANY` | `/dav/{edit_uuid}/*` | edit pw (Basic) | WebDAV access when enabled (§6.7). |
| `GET` | `/internal/tls-check` | internal | On-demand TLS ask (domain existence). |
| `GET` | `/internal/authz` | internal | View-password forward-auth check. |

**Create response (example shape):**
```json
{
  "id": "a1b2c3",
  "view_url": "https://a1b2c3.sitebin.app",
  "edit_url": "https://sitebin.app/e/9f8e7d6c5b4a",
  "edit_password": "generated-secret-shown-once",
  "mode": "webserver"
}
```

**Agent usage:** an agent creates a site, stores the returned `edit_url` + `edit_password`, and later updates files via `POST /sites/{edit_uuid}/files`. No MCP layer required.

---

## 8. Metafile Schema (`meta.json`)

```json
{
  "id": "a1b2c3",
  "edit_id": "9f8e7d6c5b4a",
  "edit_password_hash": "argon2id$...",
  "mode": "webserver",
  "view_password_protected": false,
  "view_password_hash": null,
  "webdav_enabled": false,
  "custom_domains": ["client.com"],
  "entry_file": "index.html",
  "expires_at": null,
  "created_at": "2026-07-13T10:00:00Z",
  "updated_at": "2026-07-13T10:00:00Z"
}
```
- Passwords stored only as **Argon2id** hashes; plaintext never persisted.
- `custom_domains` mirrors the symlinks in `domain-index/` (symlinks are the source of truth for routing; this field is for the API/UI).
- `webdav_enabled` controls whether the `/dav/<edit_uuid>/` endpoint serves this site.

---

## 9. Security Considerations

- **Unguessable identifiers** — view/edit UUIDs must be high-entropy (≥122-bit, e.g. UUIDv4 or 160-bit random). They are the only access control besides passwords.
- **Password hashing** — Argon2id for both edit and view passwords.
- **Brute-force protection** — rate-limit edit-password attempts (API **and** WebDAV Basic-auth) per edit UUID and per IP; add exponential backoff.
- **WebDAV = write access** — treat it as equivalent to the edit password. Serve only over TLS, only when `webdav_enabled`, scoped strictly to the site's `files/`. Reject uploaded symlinks here too.
- **Path traversal & symlinks** — sanitize upload filenames; resolve writes strictly within the site folder; **reject uploaded symlinks** (Caddy's file-server root is not a hard sandbox — a symlink inside the root can escape it).
- **Origin isolation** — never serve user content on the apex/main domain; each site lives on its own random subdomain origin. Keep the edit UI, API, and WebDAV strictly on the main domain.
- **On-demand TLS ask endpoint is mandatory** (cert-exhaustion DoS).
- **Abuse / phishing / malware** — open, no-login file hosting is a known phishing and malware-distribution magnet. Required for any public/hosted deployment: an abuse-report endpoint, a takedown mechanism (delete by domain/UUID), upload size and type limits, and ideally reputation/safe-browsing checks on the hosted tier. Document this clearly for self-hosters (they are the operator and bear responsibility).
- **Resource limits** — per-site storage cap, total-size cap, request rate limits.
- **Content-Security** — set sensible security headers on the viewer wrapper; sanitize rendered Markdown/DOCX HTML to prevent stored XSS in viewer mode.

---

## 10. Deployment

### Self-hosted (container-first)
- **Default:** a single `docker run` with the all-in-one image (see §6.9-A) — one volume, a handful of env vars, done.
- **Alternative:** `docker compose up` for the multi-service variant (§6.9-B).
- Requirements: a domain, a wildcard DNS record (`*.sitebin.example`) plus a DNS-provider API token for the wildcard cert challenge, ports 80/443, and public reachability.
- Data (sites, indexes, metafiles, **and** Caddy cert state) lives entirely under the `/data` volume — back it up and you've backed up everything. Recreate the container freely; the volume is the only durable state.

### Hosted (optional, later)
- Free tier and paid tier(s). Suggested differentiators by plan: storage per site / total, number of custom domains, WebDAV availability, max expiry window, bandwidth, and abuse-scanning. Pricing and limits TBD.

---

## 11. Key Flows

**Create (UI or API):** upload files → pick mode → (optional) domains, view password, WebDAV, expiry → server generates view UUID, edit UUID, edit password → writes `meta.json`, `edit-index` and `domain-index` symlinks, and (viewer mode) the viewer wrapper → returns the three secrets.

**View:** request hits `*.sitebin.app` or a custom domain → Caddy resolves to the site folder → (if protected) forward-auth password gate → files served (raw or via viewer wrapper).

**Edit via API (agent):** `POST /sites/{edit_uuid}/files` with `X-Edit-Password` → backend validates hash → writes files → done.

**Edit via WebDAV:** user enables WebDAV → mounts `https://sitebin.app/dav/<edit_uuid>/` with the edit password → drags files in/out like a network drive.

**Add custom domain:** user adds domain in UI/API → backend creates `domain-index/<domain>` symlink and shows the DNS record → on first HTTPS request Caddy asks `/internal/tls-check`, sees the symlink, issues the cert.

**Expiry:** cleanup worker scans `meta.json` files → past `expires_at` → serve 410 and remove folder + dangling symlinks.

---

## 12. Open Questions

- Multi-file uploads for webserver mode: zip-and-extract, folder upload, or both?
- View-password UX for `webserver`-mode SPAs (cookie + forward-auth vs. a gate page) — confirm the cleanest approach.
- WebDAV endpoint shape: path on main domain (`/dav/<edit_uuid>/`) vs. dedicated `dav.` subdomain — pick one for v1.
- Should `entry_file` be user-selectable when a site has multiple documents in viewer mode?
- Rate-limit thresholds and per-IP quotas for anonymous creation (abuse control on the hosted tier).
- License choice (e.g. MIT vs. AGPL) — AGPL better protects a hosted-SaaS-around-OSS strategy.

---

## 13. Out of Scope (explicitly, for v1)
- User accounts / auth providers.
- MCP server.
- Server-side build or code execution (JSX transpile, PHP, etc.).
- Versioning / history of site contents.
- Team features, analytics dashboards.
