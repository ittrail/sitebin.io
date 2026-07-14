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
| `SITEBIN_VIEW_ACCESS` | `subdomain` | How site content is served: `subdomain` (`<id>.base`, needs a wildcard cert), `path` (`base/v/<id>/`, **no wildcard needed**), or `both`. See [View access modes](#view-access-modes). |
| `SITEBIN_DNS_PROVIDER` | — | DNS module for the wildcard cert: `cloudflare`, `hetzner`, `duckdns`. Only needed for `subdomain`/`both`. |
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

# custom domains (Enterprise edition only; 403 in community)
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
| `SITEBIN_ALLOW_ANON_CREATE` | In accounts mode, still allow anonymous sites. |
| `SITEBIN_OAUTH_GOOGLE_CLIENT_ID` / `_SECRET` | Google OIDC login. |
| `SITEBIN_OAUTH_MICROSOFT_CLIENT_ID` / `_SECRET` / `_TENANT` | Microsoft OIDC (`_TENANT` default `common`). |
| `SITEBIN_SMTP_HOST` / `_PORT` / `_USER` / `_PASS` / `_FROM` / `_TLS` | Email (verification, password reset). Port default 587; `_TLS=true` for implicit TLS (465). |
| `SITEBIN_STRIPE_SECRET_KEY` / `_WEBHOOK_SECRET` | Stripe billing. Webhook: `POST /account/billing/stripe/webhook`. |
| `SITEBIN_PADDLE_API_KEY` / `_WEBHOOK_SECRET` / `_SANDBOX` | Paddle billing. Webhook: `POST /account/billing/paddle/webhook`. |
| `SITEBIN_LICENSE_KEY` | Optional Ed25519 license key; if set it must be valid. |

A tier's `price` maps it to provider price IDs, e.g. a tier with
`"price":{"stripe":"price_123","paddle":"pri_456","display":"€9/mo"}` becomes a
paid plan the dashboard sells via checkout. The dashboard lives at `/account`
on the main domain; sites created while signed in are owned by the account and
still work over the API with their edit password.

## License

The repository is [MIT](LICENSE), **except** the `ee/` directory, which is
licensed separately under [`ee/LICENSE`](ee/LICENSE) — the [Elastic License
2.0](https://www.elastic.co/licensing/elastic-license).

The premium `ee/` code is deliberately kept **source-available** (published in
this repo), not closed. Under ELv2 you may read, self-host, modify, and use it
commercially for your own purposes; you may not offer it to third parties as a
hosted/managed service or circumvent the license-key functionality. ELv2 is
perpetual — it does not convert to open source over time.
