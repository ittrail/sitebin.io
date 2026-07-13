# Sitebin

**Drop files. Get a website.**

Sitebin is a dead-simple, no-login service for publishing and previewing files
on the web. Upload one or more files — from the browser, a script, or an AI
agent — and instantly get a public URL. Each upload (a **site**) either acts as
a **real static web server** (HTML apps, static sites) or as a **file viewer**
(in-browser rendering for PDF, Markdown, DOCX, images, video, audio, and
source code).

- **No accounts.** Access control is unguessable URLs + per-site passwords.
- **No database.** The filesystem is the database — one folder + `meta.json`
  per site, symlink indexes for lookups.
- **One container.** Caddy (TLS, routing, static serving) + a single Go
  backend, shipped as one image. One volume holds everything.
- **Custom domains** with automatic TLS via Caddy's on-demand issuance.
- **WebDAV** (optional, per site): mount your site as a network drive.
- **Optional view password and expiry** per site.

---

## Quickstart (all-in-one, `docker run` and done)

Requirements:

- A domain, e.g. `sitebin.example.com`, with **two DNS records** pointing at
  your server:

  | Record | Name | Value |
  |---|---|---|
  | A/AAAA | `sitebin.example.com` | your server IP |
  | A/AAAA | `*.sitebin.example.com` | your server IP |

- A **DNS provider API token** — the wildcard certificate for
  `*.sitebin.example.com` can only be issued via a DNS-01 challenge.
  Built-in single-token providers: `cloudflare`, `hetzner`, `duckdns`
  (`netcup` and `porkbun` are compiled in too, see `SITEBIN_TLS_SNIPPET`).
- Ports 80 + 443 reachable from the internet.

```bash
docker run -d --name sitebin \
  -p 80:80 -p 443:443 -p 443:443/udp \
  -v sitebin-data:/data \
  -e SITEBIN_BASE_DOMAIN=sitebin.example.com \
  -e SITEBIN_DNS_PROVIDER=cloudflare \
  -e SITEBIN_DNS_TOKEN=<your-dns-api-token> \
  -e SITEBIN_ACME_EMAIL=you@example.com \
  --restart unless-stopped \
  sitebin:latest
```

Open `https://sitebin.example.com` and drop files. That's the whole setup.

Everything durable — sites, indexes, certificates — lives in the single
`/data` volume. Back up that path and you have backed up everything; the
container itself is disposable.

### Build the image yourself

```bash
git clone <this repo> && cd sitebin
docker build -t sitebin:latest .
```

(The build runs `go vet` + the full test suite; multi-arch builds via
`docker buildx build --platform linux/amd64,linux/arm64`. A GitHub Actions
workflow in `.github/workflows/release.yml` publishes multi-arch images.)

### Compose variant (separate Caddy + backend)

See [`deploy/docker-compose.yml`](deploy/docker-compose.yml) for the
multi-service shape (same image, `server` command + a Caddy sidecar).

### Local / behind an existing proxy

`SITEBIN_HTTP_ONLY=true` disables TLS entirely — useful for local testing
(e.g. `SITEBIN_BASE_DOMAIN=sitebin.localtest.me:8085` with `-p 8085:80`) or
when an external proxy terminates TLS for `*.yourdomain` in front of Sitebin.

---

## Configuration reference

| Variable | Default | Purpose |
|---|---|---|
| `SITEBIN_BASE_DOMAIN` | — (required) | Main domain. View subdomains, edit UI, API and WebDAV live under it. May include a port for URL generation (`host:8085`). |
| `SITEBIN_DNS_PROVIDER` | — | DNS module for the wildcard cert: `cloudflare`, `hetzner`, `duckdns`. |
| `SITEBIN_DNS_TOKEN` | — | API token for the DNS provider (read via env by Caddy, never written to disk). |
| `SITEBIN_TLS_SNIPPET` | — | Advanced: raw lines injected into the wildcard site's `tls { … }` block (for multi-credential providers like netcup/porkbun). |
| `SITEBIN_ACME_EMAIL` | — | ACME account email. |
| `SITEBIN_HTTP_ONLY` | `false` | Serve plain HTTP only (local testing / external TLS). |
| `SITEBIN_DATA_DIR` | `/data` | Data root. |
| `SITEBIN_MAX_SITE_BYTES` | `104857600` | Per-site storage cap (100 MiB). |
| `SITEBIN_MAX_FILES` | `1000` | Per-site file-count cap. |
| `SITEBIN_MAX_EXPIRY_DAYS` | `0` (off) | Optional cap on how far in the future expiry may be set. |
| `SITEBIN_WEBDAV_ENABLED` | `true` | Global WebDAV switch (per-site toggle still applies). |
| `SITEBIN_READONLY` | `false` | Freeze new site creation. |
| `SITEBIN_RATE_CREATE_PER_HOUR` / `SITEBIN_RATE_CREATE_BURST` | `30` / `10` | Anonymous creation limit per IP. |
| `SITEBIN_RATE_AUTH_PER_5MIN` | `10` | Password-attempt limit per (IP, site) — edit, view, and WebDAV auth. |
| `SITEBIN_CLEANUP_INTERVAL` | `10m` | Expiry sweep interval. |

---

## Using Sitebin

### The claim ticket

Creating a site returns three things, shown **exactly once**:

- **View URL** — `https://<random-id>.sitebin.example.com`, share this.
- **Edit URL** — `https://sitebin.example.com/e/<random-id>`, manage the site.
- **Edit password** — random secret; only its Argon2id hash is stored, so it
  cannot be recovered. Save it immediately.

### Modes

- **Web server** — files served exactly as uploaded, `index.html` is the
  entry point. Directory listings appear when no index exists.
- **File viewer** — the site renders one document in the browser (PDF,
  Markdown, DOCX, images, video, audio, code/text with highlighting). The raw
  files stay available under `/_raw/…`, and the viewer has a built-in file
  switcher + download button. Switching modes back and forth is lossless.

### API (for scripts and agents)

The only credential is the edit id (in the URL) + the edit password
(`X-Edit-Password` header). No accounts, no OAuth.

```bash
# create a site (multipart; repeat "files" freely; folders via filename paths)
curl -F "files=@index.html" -F "files=@app.js;filename=js/app.js" \
     https://sitebin.example.com/api/sites

# create a viewer site with settings
curl -F "mode=viewer" -F "view_password=sesame" \
     -F "expires_at=2026-12-31T23:59:59Z" -F "webdav=true" \
     -F "domain=docs.client.com" \
     -F "files=@report.pdf" \
     https://sitebin.example.com/api/sites

# upload a zip to be unpacked server-side
curl -F "zip=@site.zip" https://sitebin.example.com/api/sites

# read settings / files / usage
curl -H "X-Edit-Password: $PW" https://sitebin.example.com/api/sites/$EDIT_ID

# update settings (any subset; expires_at: null clears)
curl -X PUT -H "X-Edit-Password: $PW" -H "Content-Type: application/json" \
     -d '{"mode":"viewer","entry_file":"report.pdf","webdav_enabled":true}' \
     https://sitebin.example.com/api/sites/$EDIT_ID

# add / remove files
curl -X POST -H "X-Edit-Password: $PW" -F "files=@new.html;filename=new.html" \
     "https://sitebin.example.com/api/sites/$EDIT_ID/files"              # add
curl -X POST -H "X-Edit-Password: $PW" -F "zip=@all.zip" \
     "https://sitebin.example.com/api/sites/$EDIT_ID/files?replace=true" # replace all
curl -X DELETE -H "X-Edit-Password: $PW" \
     https://sitebin.example.com/api/sites/$EDIT_ID/files/js/app.js

# custom domains
curl -X POST -H "X-Edit-Password: $PW" -H "Content-Type: application/json" \
     -d '{"domain":"docs.client.com"}' \
     https://sitebin.example.com/api/sites/$EDIT_ID/domains
curl -X DELETE -H "X-Edit-Password: $PW" \
     https://sitebin.example.com/api/sites/$EDIT_ID/domains/docs.client.com

# delete the site
curl -X DELETE -H "X-Edit-Password: $PW" https://sitebin.example.com/api/sites/$EDIT_ID
```

### WebDAV (mount a site as a drive)

Enable WebDAV for the site (UI toggle or `"webdav_enabled": true`), then
mount `https://sitebin.example.com/dav/<edit-id>/` — username anything,
password = the **edit password**.

- **Windows:** Explorer → "Map network drive" → the URL above.
- **macOS:** Finder → Go → "Connect to Server…".
- **rclone:** `rclone lsf :webdav: --webdav-url=https://…/dav/<edit-id>/ --webdav-pass=$(rclone obscure $PW)`

WebDAV grants full write access — treat the mount URL + edit password like
the edit URL itself.

### Custom domains

Add a domain in the edit UI (or API), then point DNS at your server
(`A <domain> → server IP`, or `CNAME → sitebin.example.com`). The certificate
is issued automatically on the first HTTPS request. The internal
`tls-check` endpoint ensures certificates are only issued for domains that
actually belong to a site (prevents issuance-DoS).

### Expiry

An expired site answers `410 Gone`; the cleanup worker deletes its files 24 h
after expiry.

---

## Operations

- **Backup:** the `/data` volume is everything (sites + indexes + certs).
- **Health:** the image ships a `HEALTHCHECK` (`sitebin healthcheck` against
  the internal listener).
- **Takedown (abuse):** operators can delete any site by view id, edit id, or
  domain: `docker exec sitebin sitebin delete <id-or-domain>`.
- **Freeze:** `SITEBIN_READONLY=true` disables new-site creation.
- **Logs:** structured request + lifecycle logs on stdout (`docker logs`).

### Security notes (please read before hosting publicly)

- User content is **only** served on random subdomains and custom domains —
  never on the main domain. Each site gets its own origin.
- Passwords are stored as Argon2id hashes; password attempts (API, gate, and
  WebDAV) are rate limited per IP and per site.
- Uploads are sanitized against path traversal; symlinks in zips are
  rejected; per-site size/count quotas are enforced during streaming.
- The authz/tls-check/health endpoints live on a separate listener that is
  never proxied publicly.
- Open, no-login file hosting attracts phishing and malware. As the operator
  **you are responsible** for what your instance serves: keep the takedown
  command handy, consider tight `SITEBIN_MAX_*` limits and
  `SITEBIN_MAX_EXPIRY_DAYS`, and put the instance behind abuse monitoring if
  it is exposed to strangers.

---

## Development

```bash
go test ./...                        # unit + handler tests
go run ./cmd/sitebin caddyfile       # inspect generated Caddyfile
powershell -File e2e/e2e.ps1         # full E2E against the Docker image (Windows host)
```

Layout: `cmd/sitebin` (entrypoint + supervisor), `internal/*` (config, ids,
auth, store, viewer, caddygen, httpapi, cleanup), `web/` (embedded UI +
vendored viewer libraries — no Node build step).

## License

[MIT](LICENSE)
