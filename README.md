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
- **Custom domains** with automatic TLS via Caddy's on-demand issuance
  *(Enterprise)*.
- **WebDAV / FTP** (optional, per site): mount your site as a network drive.
- **SPA fallback** so single-page apps (React/Vue) work; **built-in viewer**
  for PDF, Markdown, DOCX, CSV/TSV, Jupyter notebooks, images, audio/video,
  and code.
- **In-browser editor**, **ZIP download/upload**, and a **QR code** to open a
  site on your phone.
- **Optional view password, expiry, and view counter** per site.
- **Deploy from CI** with `scripts/deploy.sh` or the bundled GitHub Action.

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
git clone https://github.com/ittrail/sitebin.io && cd sitebin.io
docker build -t sitebin:latest .
```

(The build runs `go vet` + the full test suite; multi-arch builds via
`docker buildx build --platform linux/amd64,linux/arm64`. A GitHub Actions
workflow in `.github/workflows/release.yml` publishes multi-arch images.)

### Compose

- [`deploy/docker-compose.example.yml`](deploy/docker-compose.example.yml) —
  a fully commented all-in-one example documenting **every** setting, with a
  ready-to-run local config (`docker compose -f deploy/docker-compose.example.yml up -d`
  → `http://sitebin.localtest.me:8080/`). Good for trying the variants.
- [`deploy/docker-compose.yml`](deploy/docker-compose.yml) — the multi-service
  shape (separate Caddy + backend, same image).

### Local / behind an existing proxy

`SITEBIN_HTTP_ONLY=true` disables TLS entirely — useful for local testing
(e.g. `SITEBIN_BASE_DOMAIN=sitebin.localtest.me:8085` with `-p 8085:80`) or
when an external proxy terminates TLS for `*.yourdomain` in front of Sitebin.

---

## Configuration reference

| Variable | Default | Purpose |
|---|---|---|
| `SITEBIN_BASE_DOMAIN` | — (required) | Main domain. View subdomains, edit UI, API and WebDAV live under it. May include a port for URL generation (`host:8085`). |
| `SITEBIN_VIEW_ACCESS` | `subdomain` | How site content is served: `subdomain` (`<id>.base`, needs a wildcard cert), `path` (`base/v/<id>/`, **no wildcard needed**), or `both`. See [View access modes](#view-access-modes). |
| `SITEBIN_DNS_PROVIDER` | — | DNS module for the wildcard cert: `cloudflare`, `hetzner`, `duckdns`. Only needed for `subdomain`/`both`. |
| `SITEBIN_DNS_TOKEN` | — | API token for the DNS provider (read via env by Caddy, never written to disk). |
| `SITEBIN_TLS_SNIPPET` | — | Advanced: raw lines injected into the wildcard site's `tls { … }` block (for multi-credential providers like netcup/porkbun). |
| `SITEBIN_ACME_EMAIL` | — | ACME account email. |
| `SITEBIN_HTTP_ONLY` | `false` | Serve plain HTTP only (local testing / external TLS). |
| `SITEBIN_DATA_DIR` | `/data` | Data root. |
| `SITEBIN_MAX_SITE_BYTES` | `104857600` | Per-site storage cap (100 MiB). |
| `SITEBIN_MAX_FILES` | `1000` | Per-site file-count cap. |
| `SITEBIN_MAX_EXPIRY_DAYS` | `0` (off) | Cap on how far in the future expiry may be set. A cap is also the default lifetime of a site created without an explicit expiry, and it cannot be cleared from the API. |
| `SITEBIN_WEBDAV_ENABLED` | `true` | Global WebDAV switch (per-site toggle still applies). |
| `SITEBIN_FTP_ENABLED` | `false` | Global FTP switch (per-site toggle also required). See [FTP](#ftp). |
| `SITEBIN_FTP_ADDR` / `SITEBIN_FTP_PASV_PORT_MIN` / `_MAX` | `:21` / `21000` / `21010` | FTP control port + passive data-port range (map them in `docker run`). |
| `SITEBIN_FTP_PUBLIC_HOST` | base domain | Host advertised for FTP passive mode. |
| `SITEBIN_FTP_TLS_CERT` / `SITEBIN_FTP_TLS_KEY` | — | Optional PEM cert/key for FTPS (encrypts credentials). |
| `SITEBIN_TRACK_VIEWS` | `true` | Count per-site page views (Accept: text/html) + last-seen. |
| `SITEBIN_READONLY` | `false` | Freeze new site creation. |
| `SITEBIN_EMBED_ORIGINS` | — | Origins allowed to embed the create flow cross-origin (comma-separated, or `*`). *(Enterprise; ignored with a warning in community.)* See [Embed the drop area](#embed-the-drop-area-on-your-own-site). |
| `SITEBIN_RATE_CREATE_PER_HOUR` / `SITEBIN_RATE_CREATE_BURST` | `30` / `10` | Anonymous creation limit per IP. |
| `SITEBIN_RATE_AUTH_PER_5MIN` | `10` | Password-attempt limit per (IP, site) — edit, view, and WebDAV auth. |
| `SITEBIN_CLEANUP_INTERVAL` | `10m` | Expiry sweep interval. |

A tier's `max_expiry_days` works the same way per site and adds one rule:
while a site **owned by an account** stays under a cap, every content change
(upload, delete, replace, zip extraction, WebDAV write) slides its expiry to `now + cap`. An
anonymous site keeps the expiry stamped at creation — a drop cannot renew
itself. FTP writes do not slide the expiry.

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

On an instance that runs with accounts (`SITEBIN_ACCOUNT_MODE=accounts|tiers`),
the API is an account feature: a site created anonymously answers `403` to
scripted calls and is managed through its edit page instead, and creating a
site from a script needs an account. Requests carrying browser fetch metadata
(`Sec-Fetch-Site`, plus an `Origin` that is the instance or an allowlisted
`SITEBIN_EMBED_ORIGINS` entry) are treated as coming from Sitebin's own pages.
This is a plan boundary, not a security boundary — the headers are forgeable.
A community instance has no provider and its API stays fully open.

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

# update settings (any subset; expires_at: null clears it, unless a tier caps the site)
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

# custom domains (Enterprise edition only; 403 in community)
curl -X POST -H "X-Edit-Password: $PW" -H "Content-Type: application/json" \
     -d '{"domain":"docs.client.com"}' \
     https://sitebin.example.com/api/sites/$EDIT_ID/domains
curl -X DELETE -H "X-Edit-Password: $PW" \
     https://sitebin.example.com/api/sites/$EDIT_ID/domains/docs.client.com

# read one file's content (used by the in-browser editor)
curl -H "X-Edit-Password: $PW" https://sitebin.example.com/api/sites/$EDIT_ID/content/index.html

# download the whole site as a zip
curl -H "X-Edit-Password: $PW" -o site.zip https://sitebin.example.com/api/sites/$EDIT_ID/download

# delete the site
curl -X DELETE -H "X-Edit-Password: $PW" https://sitebin.example.com/api/sites/$EDIT_ID

# report abuse (public, no auth)
curl -X POST -H "Content-Type: application/json" \
     -d '{"target":"https://abc.sitebin.example.com","reason":"phishing"}' \
     https://sitebin.example.com/api/report
```

### Deploy from CI / scripts

Deploy a folder with the bundled script (needs `zip` + `curl`):

```bash
# create a new site
SITEBIN_BASE=https://sitebin.example.com scripts/deploy.sh ./dist
# update an existing site (replaces all files)
SITEBIN_EDIT_URL=https://sitebin.example.com/e/<id> SITEBIN_EDIT_PASSWORD=... \
  scripts/deploy.sh ./dist
```

Or use the GitHub Action ([`.github/actions/deploy`](.github/actions/deploy)):

```yaml
- uses: ./.github/actions/deploy   # or ittrail/sitebin.io/.github/actions/deploy@main
  with:
    folder: dist
    edit_url: ${{ secrets.SITEBIN_EDIT_URL }}
    edit_password: ${{ secrets.SITEBIN_EDIT_PASSWORD }}
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

> **Windows/macOS + plain HTTP → 401.** The built-in WebDAV clients in Windows
> Explorer and macOS Finder refuse to send Basic-auth credentials over an
> unencrypted `http://` connection, so mounting a local/HTTP-only instance
> fails with 401 (the password is never sent). This is a client policy, not a
> server rejection. Fixes: use an **HTTPS** instance (the normal production
> case — it just works), or a client that allows Basic-over-HTTP such as
> `rclone`, WinSCP, Cyberduck, or `curl`. On Windows you can also set
> `HKLM\SYSTEM\CurrentControlSet\Services\WebClient\Parameters\BasicAuthLevel`
> to `2` and restart the `WebClient` service. Windows Explorer also caps
> downloads at 50 MB by default (`FileSizeLimitInBytes`).

### FTP

As an alternative to WebDAV, a site's files can be exposed over **FTP**. It is
**off by default** and must be enabled both instance-wide
(`SITEBIN_FTP_ENABLED=true`) and per site (toggle on the edit page, or
`"ftp_enabled": true`). Then connect with any FTP client:

- **Username** = the site's **edit UUID**
- **Password** = the site's **edit password**

```bash
# list, upload, download (curl speaks FTP)
curl --user "$EDIT_ID:$PW" ftp://sitebin.example.com/
curl -T local.html --user "$EDIT_ID:$PW" ftp://sitebin.example.com/page.html
curl --user "$EDIT_ID:$PW" ftp://sitebin.example.com/index.html
```

Each session is confined to that one site's files, with the same path and
quota rules as every other write path.

> **Plain FTP is unencrypted** — username and password travel in clear text.
> Use it only on a trusted network, or configure FTPS with
> `SITEBIN_FTP_TLS_CERT` / `SITEBIN_FTP_TLS_KEY`. Like WebDAV, FTP grants full
> write access to the site.

#### Passive mode & Docker (read this — FTP is fiddly here)

FTP opens a second connection for each data transfer (LIST, upload, download).
In **passive mode** (what clients use by default) the server tells the client
which host and port to open that connection to — and getting this right through
Docker is the part that trips everyone up. Two rules:

1. **Publish the whole passive range 1:1.** The container-internal port the
   server picks must be reachable under the *same* number on the host:
   `-p 21:21 -p 21000-21010:21000-21010` (a `21000-21010:21000-21010` mapping,
   not a single port). Widen the range with
   `SITEBIN_FTP_PASV_PORT_MIN`/`_MAX` if you expect many concurrent transfers.
2. **Set `SITEBIN_FTP_PUBLIC_HOST` to the address the *client* actually
   reaches** — **not** the container's internal IP. Locally that's `127.0.0.1`;
   in production it's your server's public IP or hostname (e.g.
   `sitebin.example.com`). If it's wrong, the control connection logs in and
   `LIST`/transfers then hang or fail — that symptom is almost always a wrong
   `SITEBIN_FTP_PUBLIC_HOST` or unmapped passive ports.

Because of all this, WebDAV (`/dav/<edit-id>/`, plain HTTPS, no extra ports) is
the smoother remote-mount option; reach for FTP when a client or workflow
specifically needs it.

### Embed the drop area on your own site

The startpage's drop-files-get-a-website flow is a reusable web component,
served by every instance:

```html
<script src="https://sitebin.example.com/_sitebin/embed.js" defer></script>
<sitebin-drop instance="https://sitebin.example.com"></sitebin-drop>
```

Attributes: `instance` (API base; defaults to the origin the script came
from), `demo` (simulate publishing, no network — great for landing pages),
`no-domains` (hide the custom-domains option), `event-only` (emit events
instead of rendering the built-in claim ticket). Events: `sitebin-published`
(detail = create response) and `sitebin-error` (detail = `{status, error}`).
Sitebin's own startpage is built on this component.

Embedding on a **different origin** needs the instance to allowlist that
origin — set `SITEBIN_EMBED_ORIGINS=https://your-site.com` (or `*`) so the
create endpoint answers with CORS headers. This is an
[Enterprise](#editions) capability; the community edition ignores the
variable (same-origin use and iframes work everywhere).

### View access modes

By default each site is served on its own random **subdomain**
(`<id>.sitebin.example.com`), which needs a wildcard certificate (hence the DNS
challenge). Set `SITEBIN_VIEW_ACCESS` to change this:

- `subdomain` (default) — random subdomains; strong per-site origin isolation.
- `path` — sites are served at `sitebin.example.com/v/<id>/` on the main
  domain. **No wildcard certificate is needed** (a normal cert for the single
  main domain suffices), which is handy when you can't get wildcard DNS/certs.
- `both` — served on subdomains *and* paths.

> **Security note for `path`/`both`:** path-served content shares the main
> domain's origin with the edit UI and API, so it does **not** get the origin
> isolation that subdomains provide. For that reason the Enterprise edition
> refuses to start with path/both view access **and** accounts enabled (a
> malicious path-hosted page could ride a logged-in visitor's session). Prefer
> `subdomain` whenever you run accounts or host untrusted HTML/JS; use `path`
> for simple, wildcard-free deployments of trusted content. Path-served sites
> should use relative links (`css/x` not `/css/x`).

### Custom domains *(Enterprise)*

Custom domains are an [Enterprise](#editions) feature. In the enterprise
edition, add a domain in the edit UI (or API), then point DNS at your server
(`A <domain> → server IP`, or `CNAME → sitebin.example.com`). The certificate
is issued automatically on the first HTTPS request, and the internal
`tls-check` endpoint ensures certificates are only issued for domains that
actually belong to a site (prevents issuance-DoS). Per-account limits follow
the tier's `custom_domains` cap. In the community edition the domain API
returns `403`.

### Expiry

An expired site answers `410 Gone`; the cleanup worker deletes its files 24 h
after expiry.

---

## Operations

Operator commands (run inside the container, e.g. `docker exec sitebin sitebin <cmd>`):

| Command | Purpose |
|---|---|
| `sitebin list` | List all sites (id, size, files, mode, created, owner/domains). |
| `sitebin reports` | List filed abuse reports. |
| `sitebin delete <id\|domain>` | Take down a site by view id, edit id, or domain. |
| `sitebin backup [file]` | Write a gzip tar of `/data` (stdout if no file). |
| `sitebin restore <file>` | Restore `/data` from a backup. |
| `sitebin caddyfile` | Print the generated Caddyfile. |
| `sitebin healthcheck` | Probe the internal health endpoint. |

- **Backup:** the `/data` volume is everything (sites + indexes + certs) — or
  use `sitebin backup` to stream a snapshot, e.g.
  `docker exec sitebin sitebin backup - > sitebin-$(date +%F).tar.gz`.
- **Health:** the image ships a `HEALTHCHECK`.
- **Freeze:** `SITEBIN_READONLY=true` disables new-site creation.
- **Logs:** structured request + lifecycle logs on stdout (`docker logs`).

### Availability & failover

Sitebin is a **single-writer** system: writes are serialized by in-process
locks, so exactly **one instance** may run against a given `/data` at any
time. Never run two containers on the same, shared, or bidirectionally
synced volume — multi-step operations (uploads with quota enforcement,
index updates, replace-all) can interleave and corrupt state. Everything
else about the design makes failover easy: the container is disposable and
`/data` is the entire instance (sites, indexes, accounts, certificates, and
the `.secret` that keeps sessions valid across a move).

**Baseline (every deployment): restore-to-fresh-server.** With streaming
backups and a low DNS TTL this alone gives minutes-level recovery:

```bash
# continuously (cron) on the primary:
docker exec sitebin sitebin backup - | ssh backup-host 'cat > sitebin-latest.tar.gz'

# disaster: on any fresh server with Docker
docker run -d --name sitebin -v sitebin-data:/data … sitebin:latest   # same env as before
cat sitebin-latest.tar.gz | docker exec -i sitebin sitebin restore /dev/stdin
docker restart sitebin
# point DNS (A records for the base domain, wildcard, and custom domains)
# at the new server; certificates re-issue automatically if missing.
```

Keep the env/compose file in version control — server + compose file +
backup is the complete instance. **Test the restore before you need it.**

**Active–passive standby (when minutes of downtime are too many):**
replicate the volume block-level to a second server — DRBD (synchronous,
RPO ≈ 0) or ZFS `send/recv` on a tight interval — with the container
*stopped* on the standby. On failure: promote the replica, start the
container, move the floating IP (or flip low-TTL DNS). The only inviolable
rule is the single-writer one: make sure the old primary is down (fencing)
before the standby starts.

**Not supported:** active–active, and serving from an rsync'd copy while
the primary accepts writes. Read replicas (one writer, many readers for
view traffic) are architecturally feasible and on the enterprise roadmap,
but not implemented.

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

## Editions

Sitebin is **open-core**:

- **Community** (default) — everything documented above. MIT licensed, built
  with `go build` / the default `sitebin:latest` image. Fully open, no
  accounts, no feature gates.
- **Enterprise** (`ee/`) — optional premium features (user accounts, tiers &
  quotas, Google/Microsoft OAuth, SMTP, and Stripe/Paddle billing), compiled in
  only with the `ee` build tag (`go build -tags ee`, image `sitebin:latest-ee`).
  All caps and toggles are configured at container startup. Design:
  [`docs/superpowers/specs/2026-07-14-accounts-tiers-billing-design.md`](docs/superpowers/specs/2026-07-14-accounts-tiers-billing-design.md).

The `ee/` tree is **source-available**, not hidden: the full premium source
lives in this repo for you to read, audit, self-host, and modify. It is simply
not MIT — it is governed by [`ee/LICENSE`](ee/LICENSE) (Elastic License 2.0),
which permits all of the above and your own commercial use, but not reselling
Sitebin as a hosted/managed service or circumventing the license key. The two
editions build from the same repository; the community `sitebin:latest` image
still contains none of the `ee/` code (it is excluded at compile time, so the
community binary stays pure MIT), while `sitebin:latest-ee` includes it.

### Enterprise configuration (all startup env vars)

| Variable | Purpose |
|---|---|
| `SITEBIN_ACCOUNT_MODE` | `open` (default) · `accounts` (login to create) · `tiers` (tiered quotas). |
| `SITEBIN_TIERS` / `SITEBIN_TIERS_FILE` | Tier definitions (inline JSON or mounted file). |
| `SITEBIN_DEFAULT_TIER` | Tier new/free accounts start on (required in tiers mode). |
| `SITEBIN_ANON_TIER` | Tier for anonymous creation (empty = require an account). |
| `SITEBIN_TIER_SELF_SELECT` | Allow users to switch among free tiers. |
| `SITEBIN_ADMIN_ACCOUNTS` | Comma-separated emails allowed to reach the **instance register** at `/account/admin` — every site on the instance, with delete and expiry control. Gated twice: the account's tier must also carry `"admin": true` in the tier config, so neither the plan source nor the environment can grant it alone. Unset disables the console entirely. |
| `SITEBIN_ALLOW_ANON_CREATE` | In accounts mode, still allow anonymous sites. |
| `SITEBIN_OAUTH_GOOGLE_CLIENT_ID` / `_SECRET` | Google OIDC login. |
| `SITEBIN_OAUTH_MICROSOFT_CLIENT_ID` / `_SECRET` / `_TENANT` | Microsoft OIDC (`_TENANT` default `common`). |
| `SITEBIN_OAUTH_OIDC_ISSUER` / `_CLIENT_ID` / `_CLIENT_SECRET` / `_LABEL` | Generic OIDC sign-in against any issuer (Keycloak, Okta, Authentik, the [SaaS Stack](#saas-stack-integration)). `_LABEL` is the login-button text (default `SSO`). |
| `SITEBIN_LOCAL_AUTH` | `true` | `false` = SSO only: no email/password form, signup/reset disabled; with a single OAuth provider, `/account/login` redirects straight to it. Requires an `SITEBIN_OAUTH_*` provider. |
| `SITEBIN_PAYGATE_URL` / `_APP_ID` / `_API_KEY` | Resolve subscription tiers from a SaaS-Stack PayGate instead of built-in billing. See [SaaS-Stack integration](#saas-stack-integration). |
| `SITEBIN_PAYGATE_CACHE_TTL` / `_MANAGE_URL` | Per-user tier cache (default `5m`); optional dashboard "manage subscription" link. |
| `SITEBIN_SMTP_HOST` / `_PORT` / `_USER` / `_PASS` / `_FROM` / `_TLS` | Email (verification, password reset). Port default 587; `_TLS=true` for implicit TLS (465). |
| `SITEBIN_STRIPE_SECRET_KEY` / `_WEBHOOK_SECRET` | Stripe billing. Webhook: `POST /account/billing/stripe/webhook`. |
| `SITEBIN_PADDLE_API_KEY` / `_WEBHOOK_SECRET` / `_SANDBOX` | Paddle billing. Webhook: `POST /account/billing/paddle/webhook`. |
| `SITEBIN_LICENSE_KEY` | Optional Ed25519 license key; if set it must be valid. |

A tier may set `"admin": true`. That does not change its quotas; it marks the
tier as one whose holders may reach the instance register, and only together
with `SITEBIN_ADMIN_ACCOUNTS`. The register lists every site on the instance —
anonymous drops included — with instance-wide figures, search and filters, and
two actions per site: delete, and set or clear the expiry. It never exposes a
site's edit password or edit page: an operator can clean up and look, but the
claim ticket stays the only thing that confers ownership.

Note that an unlimited tier needs explicit large caps, not zeros:
`max_site_bytes: 0` and `max_files: 0` fall back to the instance globals, and
`custom_domains: 0` means *no* custom domains. Only `max_sites: 0` and
`max_expiry_days: 0` mean unlimited.

A tier's `price` maps it to provider price IDs, e.g. a tier with
`"price":{"stripe":"price_123","paddle":"pri_456","display":"€9/mo"}` becomes a
paid plan the dashboard sells via checkout. The dashboard lives at `/account`
on the main domain; sites created while signed in are owned by the account and
still work over the API with their edit password.

### SaaS-Stack integration

Sitebin Enterprise is a first-class app of the MIT-licensed
[IT-Trail SaaS Stack](https://github.com/ittrail/saas-stack): onboard it like
any of your apps and it inherits single sign-on and subscription billing —
users log in once across your whole portfolio, and their Sitebin tier follows
the subscription they bought through the stack.

```bash
# 1. SSO through the stack's Auth Gateway (generic OIDC)
SITEBIN_OAUTH_OIDC_ISSUER=https://auth.saas-stack.example.com/api/v1/sitebin
SITEBIN_OAUTH_OIDC_CLIENT_ID=sitebin
SITEBIN_OAUTH_OIDC_CLIENT_SECRET=…
SITEBIN_OAUTH_OIDC_LABEL="Example SSO"

# (recommended) SSO only — local accounts would bypass PayGate billing
SITEBIN_LOCAL_AUTH=false

# 2. Tiers from PayGate (requires SITEBIN_ACCOUNT_MODE=tiers)
SITEBIN_PAYGATE_URL=https://paygate.saas-stack.example.com
SITEBIN_PAYGATE_APP_ID=sitebin
SITEBIN_PAYGATE_API_KEY=ssk_live_…
SITEBIN_PAYGATE_MANAGE_URL=https://account.example.com   # "manage subscription" link
```

Conventions and behavior: **stack tier ids must match `tiers.json` ids** —
PayGate decides *which* tier a user has, `tiers.json` decides *what it means*
(quotas). Lookups use the stack's admin-by-user-id endpoint (no user JWTs are
stored), are cached (`SITEBIN_PAYGATE_CACHE_TTL`), and fail open to the
account's stored tier so a PayGate outage never blocks publishing. Tiers
resolve via PayGate only for accounts signed in through the generic OIDC
provider (their subject *is* the stack user id); `active`, `trialing` and
`past_due` subscriptions are honored. For those accounts the dashboard links
to the stack's subscription management instead of built-in checkout, and
`SITEBIN_TIER_SELF_SELECT` is ignored.

## License

The repository is [MIT](LICENSE), **except** the `ee/` directory, which is
licensed separately under [`ee/LICENSE`](ee/LICENSE) — the [Elastic License
2.0](https://www.elastic.co/licensing/elastic-license).

The premium `ee/` code is deliberately kept **source-available** (published in
this repo), not closed. Under ELv2 you may read, self-host, modify, and use it
commercially for your own purposes; you may not offer it to third parties as a
hosted/managed service or circumvent the license-key functionality. ELv2 is
perpetual — it does not convert to open source over time.
