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
| `SITEBIN_MCP_ENABLED` | `true` | Serve the MCP endpoint at `/mcp` for AI agents. See [MCP server](#mcp-server-for-ai-agents). |
| `SITEBIN_MCP_OAUTH_ISSUER` | = `SITEBIN_OAUTH_OIDC_ISSUER` | Authorization server whose access tokens `/mcp` accepts. Empty disables OAuth; the endpoint then authenticates with edit passwords and account tokens only. |
| `SITEBIN_MCP_OAUTH_RESOURCE` | `<base>/mcp` | This server's OAuth resource identifier — the value a token's audience must contain. Immutable once published. |
| `SITEBIN_TRACK_VIEWS` | `true` | Count per-site page views (Accept: text/html) + last-seen. |
| `SITEBIN_READONLY` | `false` | Freeze new site creation. |
| `SITEBIN_EMBED_ORIGINS` | — | Origins allowed to embed the create flow cross-origin (comma-separated, or `*`). *(Enterprise; ignored with a warning in community.)* See [Embed the drop area](#embed-the-drop-area-on-your-own-site). |
| `SITEBIN_RATE_CREATE_PER_HOUR` / `SITEBIN_RATE_CREATE_BURST` | `30` / `10` | Anonymous creation limit per IP. |
| `SITEBIN_RATE_AUTH_PER_5MIN` | `10` | Password-attempt limit per (IP, site) — edit, view, and WebDAV auth. |
| `SITEBIN_CLEANUP_INTERVAL` | `10m` | Expiry sweep interval. |
| `SITEBIN_PUBLIC_ADDR` | `:8080` | Address of the Go backend listener that Caddy proxies. Change it only if `8080` is taken inside the container. |
| `SITEBIN_INTERNAL_ADDR` | `:9000` | Address of the authz / `tls-check` / health listener. It is **never proxied publicly**; do not expose it. |
| `SITEBIN_BACKEND_HOST` | `127.0.0.1` | Host Caddy dials to reach the backend. Only differs from loopback if you run Caddy and the Go server in separate containers, which is not a supported layout. |

A tier's `max_expiry_days` works the same way per site and adds one rule:
while a site **owned by an account** stays under a cap, every content change
(upload, delete, replace, zip extraction, WebDAV write) slides its expiry to
`now + cap`. An anonymous site keeps the expiry stamped at creation — a drop
cannot renew itself. FTP writes do not slide the expiry.

Sliding renewal is deliberately narrow, and both limits are load-bearing:

- **An expiry the owner chose never slides.** Sitebin records who picked the
  date. Setting `expires_at` through the API or the edit page makes it yours,
  and from then on uploads leave it alone — "delete this on the 20th" means the
  20th. Only a date the *plan* imposed is renewed. The cap still clamps an
  owner-chosen date; it just never moves it.
- **A renewal never pulls a date closer.** If the stored expiry is already
  further out than `now + cap`, it stays. That is what keeps the 30-day grace a
  downgrade stamps from being cut back to the new tier's cap by a single
  upload, and it is why "renew" here only ever means *later*.

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

### MCP server (for AI agents)

Sitebin speaks the [Model Context Protocol](https://modelcontextprotocol.io) at
`/mcp`, so Claude, ChatGPT and any other MCP client can publish and manage
sites directly. It is the JSON API's tool surface over a second transport, with
the same authorization: what an agent may do is exactly what a script with the
same credential may do.

```bash
# Claude Code, anonymous (a community instance, or one running open)
claude mcp add --transport http sitebin https://sitebin.example.com/mcp

# with an account API token (see "Account API tokens" below)
claude mcp add --transport http sitebin https://sitebin.example.com/mcp \
  --header "Authorization: Bearer $TOKEN"
```

Other clients take the same URL; those that cannot send a header (or that
require stdio) can bridge with `npx mcp-remote https://…/mcp --header …`.

**Tools**

| Tool | What it does |
|---|---|
| `create_site` | Publish files as a new site; returns the URL, the edit id and — once — the edit password |
| `list_sites` | The connected account's sites *(needs a token)* |
| `get_site` / `update_site` | Read and change settings, expiry, view password, WebDAV/FTP, SPA fallback |
| `list_files` / `read_file` | Inspect a site's contents |
| `write_files` | Add or overwrite files; with `replace`, the site ends up containing exactly what you pass |
| `delete_file` / `delete_site` | Remove a file, or the whole site |
| `add_domain` / `remove_domain` | Custom domains *(Enterprise)* |
| `download_site` | The site as a zip, attached as a resource |

**Authentication** mirrors the API exactly:

- **With a token** (`Authorization: Bearer sbp_…`) new sites belong to that
  account and get its tier's quotas, `list_sites` works, and the token stands
  in for the edit password on every site the account owns.
- **Without a token**, per-site tools need the site's `edit_password`, and on an
  instance running with accounts, creating a site is refused — the same rule the
  JSON API applies. A community instance has no accounts and stays fully open.
- An MCP client never gets the browser escape hatch the API extends to
  Sitebin's own pages, so an account-less site cannot be driven by an agent on
  a gated instance.

Files travel as JSON: `{"path": "index.html", "text": "…"}`, or `"base64"` for
binary. One call carries at most 8 MiB of content — for more than that, use
WebDAV, FTP or the API's zip upload. Sites created through MCP are recorded in
`meta.json` as `"origin": "mcp"`; that is provenance for the admin console and
changes nothing about how the site is served.

#### OAuth 2.1 *(optional)*

Bearer tokens work in every client that can set a header. For the connector
directories at Anthropic and OpenAI, `/mcp` can also be an **OAuth 2.1 protected
resource**: set `SITEBIN_MCP_OAUTH_ISSUER` to an authorization server and
Sitebin will publish `/.well-known/oauth-protected-resource`, answer
unauthenticated calls with a `WWW-Authenticate` challenge pointing at it, and
accept access tokens that server signed.

**Sitebin is a resource server and never an authorization server.** It issues
no tokens, registers no clients and renders no consent screen — point it at
whatever issuer you already run. A token is accepted only when its signature,
issuer and expiry check out, its audience contains this server's resource
identifier, and its subject matches an account that has signed in here.

Two scopes carve up the tool surface, so an agent can be given read access
without the ability to publish:

| Scope | Tools |
|---|---|
| `sitebin:sites:read` | `list_sites`, `get_site`, `list_files`, `read_file`, `download_site` |
| `sitebin:sites:write` | `create_site`, `update_site`, `write_files`, `delete_file`, `delete_site`, `add_domain`, `remove_domain` |

Account API tokens keep working unchanged with OAuth enabled — they carry no
scopes and grant everything their account can do, exactly as before.

The authorization server has to offer dynamic client registration, PKCE `S256`,
and tokens whose audience is this server's resource identifier. The IT-Trail
SaaS Stack does this from an `mcp` block at app onboarding; any other compliant
issuer works the same way. Design:
[`docs/superpowers/specs/2026-08-29-mcp-oauth-resource-server-design.md`](docs/superpowers/specs/2026-08-29-mcp-oauth-resource-server-design.md).

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
actually belong to a site (prevents issuance-DoS). In the community edition the
domain API returns `403`.

Two independent limits apply, and neither is per account:

- **Per site**, the tier's `custom_domains` cap. It is stamped onto the site
  when the site is created, so a site keeps the cap its owner's plan granted at
  the time — changing plans restamps it, it is not resolved per request.
  Ten sites on a tier with `"custom_domains": 5` may carry five domains *each*.
- **Per instance**, the Enterprise licence's `entitlements.max_custom_domains`
  ceiling — the total across every site on the deployment, anonymous ones
  included. Absent or zero means unlimited, which is also what having no
  licence means. It is checked only where a domain is *added*, so a licence
  that shrinks refuses the next domain and never removes one already serving:

  > this instance's license covers 25 custom domains and 25 are configured;
  > remove one or upgrade the license

  If the instance cannot count its domains, the domain is allowed: an
  unreadable `meta.json` must not cost a customer a domain they paid for.

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
go test ./...                        # unit + handler tests (community)
go test -tags ee ./...               # enterprise suite -- run BOTH, they differ
go vet ./... && go vet -tags ee ./...
go run ./cmd/sitebin caddyfile       # inspect generated Caddyfile
docker build -t sitebin:latest .                                   # community image
docker build --build-arg EDITION=enterprise -t sitebin:latest-ee .  # enterprise image
```

Either `docker build` runs `go vet` and both test suites inside the builder, so
a green build is a green suite.

### End-to-end tests *(Windows host, Docker required)*

`e2e/` holds **eight independent scripts**. There is no "run everything" entry
point: `e2e.ps1` is the core suite and calls none of the others, so a full pass
means running them all.

| Script | Covers | Default `-Image` |
|---|---|---|
| `e2e.ps1` | core HTTP: create, view, edit, quotas, rate limits | `sitebin:dev` |
| `spa.ps1` | per-site `index.html` fallback | `sitebin:dev` |
| `paths.ps1` | `SITEBIN_VIEW_ACCESS=path` | `sitebin:dev` |
| `ftp.ps1` | FTP/FTPS access | `sitebin:dev` |
| `mcp.ps1` | the `/mcp` endpoint | `sitebin:dev` |
| `accounts.ps1` | accounts mode *(enterprise)* | `sitebin:dev-ee` |
| `tiers.ps1` | tiers and quotas *(enterprise)* | `sitebin:dev-ee` |
| `license.ps1` | licensing *(enterprise)* — builds its own image | `sitebin:e2e-license` |

The defaults are tags the scripts do **not** build; tag them yourself first, and
note that the enterprise ones need `--build-arg EDITION=enterprise` or they will
silently exercise a community binary that has no accounts at all:

```bash
docker build -t sitebin:dev .
docker build --build-arg EDITION=enterprise -t sitebin:dev-ee .
```

```powershell
powershell -File e2e\e2e.ps1
powershell -File e2e\spa.ps1
powershell -File e2e\paths.ps1
powershell -File e2e\ftp.ps1
powershell -File e2e\mcp.ps1
powershell -File e2e\accounts.ps1
powershell -File e2e\tiers.ps1
powershell -File e2e\license.ps1   # self-contained: mints a throwaway root and builds its own image
```

`e2e/stack/` is the compose file for the part that needs a running SaaS Stack;
see its own README.

**Keep these scripts pure ASCII.** They are UTF-8 with no BOM, so PowerShell
5.1 decodes them in the system codepage and an em dash silently breaks string
parsing for the rest of the file. Write `--`, not an em dash.

Layout: `cmd/sitebin` (entrypoint + supervisor), `internal/*` (config, ids,
auth, store, viewer, caddygen, httpapi, cleanup), `web/` (embedded UI +
vendored viewer libraries — no Node build step).

## Editions

Sitebin is **open-core**:

- **Community** (default) — everything documented above. MIT licensed, built
  with `go build` / the default `sitebin:latest` image. Fully open, no
  accounts, no feature gates.
- **Enterprise** (`ee/`) — optional premium features (user accounts, tiers &
  quotas, Google/Microsoft OAuth, SMTP, and billing through Stripe, Paddle or
  the SaaS Stack), compiled in
  only with the `ee` build tag (`go build -tags ee`, image `sitebin:latest-ee`).
  All caps and toggles are configured at container startup.

  For how tiers, quotas and site lifetimes actually behave, read
  [`2026-08-11-tier-lifetimes-and-api-gating-design.md`](docs/superpowers/specs/2026-08-11-tier-lifetimes-and-api-gating-design.md)
  and
  [`2026-08-12-tier-change-quota-sync-design.md`](docs/superpowers/specs/2026-08-12-tier-change-quota-sync-design.md)
  — including the latter's "Corrections (post-implementation)" blocks, which
  are the rules, not footnotes. The original
  [`2026-07-14-accounts-tiers-billing-design.md`](docs/superpowers/specs/2026-07-14-accounts-tiers-billing-design.md)
  is kept for the shape of the accounts/billing design only; **its tier table
  is obsolete** (it proposes Free = 3 sites / 30-day and a €9 Pro) and no
  shipped instance has ever run those numbers. Tiers are per-instance
  configuration, never a table in this repo.

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
| `SITEBIN_VIEW_DOMAIN` | Domain user sites are served from, as `<id>.<view-domain>` (default: the base domain). Point it at a **separate registrable domain** and list that domain in the [Public Suffix List](https://publicsuffix.org/) to stop uploaded content sharing a browser "site" with the dashboard: no cookie can be written upward onto the app, `SameSite` stops treating navigations from a user site as same-site, and a phishing takedown against one site does not endanger the app's own domain. Needs its own wildcard DNS record and DNS-challenge access. Cannot be combined with `SITEBIN_VIEW_ACCESS=path\|both`, which would serve content from the main domain again. |
| `SITEBIN_ADMIN_ACCOUNTS` | Comma-separated emails allowed to reach the **instance register** at `/account/admin` — every site on the instance, with delete and expiry control. Gated twice: the account's tier must also carry `"admin": true` in the tier config, so neither the plan source nor the environment can grant it alone. Unset disables the console entirely. |
| `SITEBIN_ALLOW_ANON_CREATE` | In accounts mode, still allow anonymous sites. |
| `SITEBIN_OAUTH_GOOGLE_CLIENT_ID` / `_SECRET` | Google OIDC login. |
| `SITEBIN_OAUTH_MICROSOFT_CLIENT_ID` / `_SECRET` / `_TENANT` | Microsoft OIDC (`_TENANT` default `common`). |
| `SITEBIN_OAUTH_OIDC_ISSUER` / `_CLIENT_ID` / `_CLIENT_SECRET` / `_LABEL` | Generic OIDC sign-in against any issuer (Keycloak, Okta, Authentik, the [SaaS Stack](#saas-stack-integration)). `_LABEL` is the login-button text (default `SSO`). |
| `SITEBIN_LOCAL_AUTH` | `true` | `false` = SSO only: no email/password form, signup/reset disabled; with a single OAuth provider, `/account/login` redirects straight to it. Requires an `SITEBIN_OAUTH_*` provider. |
| `SITEBIN_BILLING` | Which backend may charge customers: `stripe`, `paddle` or `paygate` (case-insensitive). Unset = inferred when exactly one is configured; **two configured and no choice is a startup error**. Exactly one backend is ever active: with `paygate` selected, configured Stripe/Paddle credentials are inert *and their webhook routes are not mounted*, so provider deliveries get a silent `404` — remove the webhook from the provider's dashboard, or you will be debugging retries. Startup also refuses a catalogue the selected direct backend cannot sell: every tier with a `price` must carry the matching `price.stripe` / `price.paddle`. See [Billing](#billing). |
| `SITEBIN_PAYGATE_URL` / `_APP_ID` / `_API_KEY` | Sell tiers and resolve subscriptions through a SaaS-Stack PayGate. See [Billing](#billing) and [SaaS-Stack integration](#saas-stack-integration). |
| `SITEBIN_PAYGATE_CACHE_TTL` / `_MANAGE_URL` | Per-user tier cache (default `5m`); optional dashboard "manage subscription" link. |
| `SITEBIN_SMTP_HOST` / `_PORT` / `_USER` / `_PASS` / `_FROM` / `_TLS` | Email (verification, password reset). Port default 587; `_TLS=true` for implicit TLS (465). |
| `SITEBIN_STRIPE_SECRET_KEY` / `_WEBHOOK_SECRET` | Stripe billing, direct. Webhook: `POST /account/billing/stripe/webhook`. |
| `SITEBIN_PADDLE_API_KEY` / `_WEBHOOK_SECRET` / `_SANDBOX` | Paddle billing, direct. Webhook: `POST /account/billing/paddle/webhook`. |
| `SITEBIN_LICENSE_KEY` | Optional Enterprise license key (four base64url segments: a stack-signed certificate plus the license it vouches for). Verified offline; it never blocks startup. When set it WINS over any license collected from the stack, which is what makes an air-gapped install work. Without a valid key an instance behaves as licensed for 90 days from first start, then keeps serving and updating every existing site but creates no new ones. |
| `SITEBIN_LICENSE_REFRESH` | How often the instance collects its license from the stack. Default **24h**; any positive Go duration (`5m`, `1h`). Shorten it to watch a renewal apply without a restart, which is otherwise a day-long experiment. A value that is not a positive duration is warned about and ignored. Ignored entirely when `SITEBIN_LICENSE_KEY` is set. |
| `SITEBIN_LICENSE_URL` | Optional override for where the running instance collects a renewed license (default: `<SITEBIN_PAYGATE_URL>/api/v1/licenses/renew`). The request carries **no credential**: the instance presents the license it already holds, and the stack — which signed it — verifies it. Fetched daily, cached under the data dir and applied without a restart; a failure never restricts anything. Ignored when `SITEBIN_LICENSE_KEY` is set. |
| `SITEBIN_STACK_LICENSING` | JSON `licensing` block sent with the self-registration above, declaring what a Sitebin **Enterprise license** is worth and how long a lapsed one stays usable: `{"graceMonths":3,"plans":{"team":{"max_custom_domains":25},"platform":{}}}`. The stack mints licenses, so it has to be told; a plan absent from `plans` carries no entitlements, which means **unlimited**. Only meaningful alongside `SITEBIN_STACK_URL`, and only the vendor's own deployment (the one holding the platform admin key) ever sets it. Absent = declare nothing, and the stack keeps whatever it already holds — registration merges, so an empty block would erase the entitlements rather than leave them. |
| `SITEBIN_STACK_URL` / `_APP_ID` / `_ADMIN_KEY` | Self-registration against the IT-Trail SaaS Stack. With all three set, the instance announces itself to the stack on every start — its identity, its OIDC callback, its tier catalogue and its MCP block — so auth, billing and MCP are configured by deploying rather than by hand. `_ADMIN_KEY` is the stack's platform admin key: a master credential, so keep it in a secret store. Unset = no self-registration. |

### Account API tokens *(Enterprise)*

The dashboard at `/account` issues API tokens (`sbp_…`), optionally named, and
revokes them. Send one as `Authorization: Bearer <token>` to

- **create** sites owned by that account, without a browser session, and
- **manage** any site the account owns, in place of that site's edit password.

```bash
TOKEN=sbp_...
# publish a build artifact
curl -H "Authorization: Bearer $TOKEN" -F "files=@dist.zip"      https://app.example.com/api/sites
# and update it later, no per-site secret needed
curl -H "Authorization: Bearer $TOKEN" -F "files=@dist.zip"      "https://app.example.com/api/sites/<edit-id>/files?replace=true"
```

The same token authenticates the [MCP server](#mcp-server-for-ai-agents), where
it also replaces the edit password on the account's sites.

A token acts on **sites, not on the account**: it is refused by the dashboard,
so it cannot change the plan, rotate passwords or delete the account. It reaches
only sites its own account owns — never another account's, and never an
anonymous site. The secret is shown once and stored only as a SHA-256, so a lost
token is replaced rather than recovered. Up to 25 per account.

A tier may set `"trusted": true`. Sites it owns are served without the strict
content-security headers Sitebin applies to untrusted uploads — anonymous drops
and sites on tiers without the flag get `form-action 'none'`, `connect-src
'self'`, `frame-ancestors 'none'` and a `no-referrer` policy on top of the
baseline every site receives (`nosniff`, `object-src 'none'`, `base-uri 'self'`,
a restrictive `Permissions-Policy`). Scripts and images still load from
anywhere: the aim is to stop a phishing page from *shipping* what it captures,
not to stop it rendering, because blocking remote scripts would equally break
every legitimate app that loads a library from a CDN.

Violations are reported to `/_sitebin/csp-report`, counted per site, and shown
in the instance register with the destinations that were blocked. A site whose
first visitor trips `form-action` against a foreign host is almost always
phishing. Grant `trusted` to tiers whose holders you can hold accountable; an
anonymous site never qualifies, whatever its tier says. **The community build is
unaffected** — it registers no provider, so every site there is trusted.

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

### Billing

Sitebin Enterprise can sell a paid tier three ways. **Exactly one is active**,
chosen with `SITEBIN_BILLING`:

| Backend | Who owns the subscription | You need |
|---|---|---|
| `stripe` | Sitebin | a Stripe account, and `price.stripe` on each paid tier |
| `paddle` | Sitebin | a Paddle account, and `price.paddle` on each paid tier |
| `paygate` | the [SaaS Stack](#saas-stack-integration) | the stack, and an amount on each paid tier |

With no backend configured, tiers and quotas still work — an operator just sets
them by hand, and no plan can be bought.

If exactly one backend is configured, it is used. If more than one is and
`SITEBIN_BILLING` is unset, Sitebin **refuses to start**: which processor
charges your customers is not something to be inferred from what happens to be
in the environment.

**The routes a customer touches never name the processor.** Upgrading posts to
`/account/upgrade`, managing a subscription to `/account/billing/portal`, and
the active backend decides where that leads. Only webhooks carry a provider
name (`/account/billing/stripe/webhook`), because the provider decides where it
delivers — and a backend that receives no webhooks mounts none.

#### Direct: Stripe or Paddle

Sitebin owns the subscription. It creates the checkout against a price you made
in that provider, verifies the provider's webhook signatures, and applies the
result to the account's tier. Put the identifier on the tier:

```json
{ "id": "pro", "label": "Pro", "price": { "stripe": "price_123" } }
```

#### Through the stack: PayGate

The stack owns the subscription, and **Sitebin never learns which processor is
behind it**. It sells by tier *name*: the stack resolves that to whatever its
provider calls the price, takes the payment, and receives the webhooks itself.
Sitebin finds out at the next request that resolves a tier, because PayGate is
already the tier source and outranks the stored tier.

That is deliberate, and it is the reason no Stripe or Paddle identifier appears
anywhere on this path. If the stack changes payment provider, nothing in Sitebin
changes — no config, no code, no redeploy. So a tier here carries an amount
rather than an identifier:

```json
{ "id": "pro", "label": "Pro", "price": { "monthly": "9.00", "currency": "EUR" } }
```

The stack creates the product from that when the instance registers itself. A
tier with no amount creates no payment product, so free tiers cost nothing to
declare.

One limit worth knowing: PayGate identifies people by the identity the stack
issued, so only accounts signed in through the generic OIDC provider can be
sold to. A local account has no subscription there and never will — which is
why `SITEBIN_LOCAL_AUTH=false` is the recommendation on a stack instance.

Setting `SITEBIN_PAYGATE_MANAGE_URL` replaces Sitebin's own plan card with a
link to that page. It is an override, not an addition: two competing answers to
"where do I manage my subscription" is worse than either one.

#### A different processor entirely

`billing.Backend` in `ee/billing` is a three-method interface — `Name`,
`CheckoutURL`, `PortalURL` — plus two optional ones for the parts that are not
universal: `TierSource` for a backend that holds the subscription somewhere
else, and `WebhookReceiver` for one that pushes events at Sitebin. Implementing
it is how you sell through something Sitebin does not ship, without patching
any of the code above.

### SaaS-Stack integration

Sitebin Enterprise is a first-class app of the MIT-licensed
[IT-Trail SaaS Stack](https://github.com/ittrail/saas-stack): it inherits single
sign-on and subscription billing — users log in once across your whole
portfolio, and their Sitebin tier follows the subscription they bought through
the stack.

**It onboards itself.** Give it `SITEBIN_STACK_URL`, `SITEBIN_STACK_APP_ID` and
`SITEBIN_STACK_ADMIN_KEY` and it announces itself to the stack on every start,
so auth, billing and MCP are configured by deploying rather than by hand. The
call is convergent: run it a thousand times and the stack simply matches what
Sitebin declared.

What it declares is only what Sitebin alone knows — its identity, the OIDC
callback it will actually use, its tier catalogue from `tiers.json`, and its
MCP resource and scopes. It never declares identity providers, password policy,
MFA or realm registration: those are realm-wide settings shared with every other
app on the stack, and an app that set them would overwrite an operator's choice
on each restart.

What it declares of a tier is the **amount**, never a price identifier: the
stack creates the product in whatever processor it uses. A tier with no amount
declares no payment product, which is how free plans — and plans whose pricing
is not settled yet — announce themselves.

It also declares how the catalogue should *read* on the stack's hosted plan
page. The stack has no sort field — the order of the declaration is the order —
so the order of `tiers.json` is the order customers see, and `"featured": true`
on a tier marks the plan the page leads with. Neither is guessed: a catalogue
that says nothing renders unemphasised, in whatever order it happens to be in.

And, when `SITEBIN_STACK_LICENSING` is set, it declares what a **Sitebin
Enterprise license** is worth — see [Enterprise licensing](#enterprise-licensing).
Registration merges rather than replaces, so a block that is absent leaves what
the stack already holds untouched.

Registration never blocks startup. A stack that is briefly unreachable makes
the attempt fail and log; Sitebin serves sites regardless and converges again
on the next start.

```bash
# 1. SSO through the stack's Auth Gateway (generic OIDC)
SITEBIN_OAUTH_OIDC_ISSUER=https://auth.saas-stack.example.com/api/v1/sitebin
SITEBIN_OAUTH_OIDC_CLIENT_ID=sitebin
SITEBIN_OAUTH_OIDC_CLIENT_SECRET=…
SITEBIN_OAUTH_OIDC_LABEL="Example SSO"

# (recommended) SSO only — local accounts would bypass PayGate billing
SITEBIN_LOCAL_AUTH=false

# 2. Self-registration: identity, callback, tiers and MCP, on every start
SITEBIN_STACK_URL=https://platform.saas-stack.example.com
SITEBIN_STACK_APP_ID=sitebin
SITEBIN_STACK_ADMIN_KEY=…            # the stack's PLATFORM_ADMIN_KEY

# 3. Tiers from PayGate (requires SITEBIN_ACCOUNT_MODE=tiers)
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

### Enterprise licensing

The enterprise edition is license-gated. This section is what an operator
building or running their own Enterprise image needs; none of it applies to the
community build.

**What a license is.** Four base64url segments —
`<certPayload>.<certSig>.<licPayload>.<licSig>` — a certificate signed by a
root, plus the license that certificate's key signed. Verification is two
Ed25519 checks and needs **no network**: there is no license server.

**Startup never fails on a license problem.** Absent, malformed, unverifiable
and expired keys are all logged and shown in the account UI. Serving is never
touched, and neither are existing sites.

| State | When | Effect |
|---|---|---|
| `licensed` | valid | entitlements apply |
| `grace` | past `expires_at`, within `grace_until` | loud notice in the account UI, nothing restricted |
| `expired` | past `grace_until` | notice, **and no new sites or drops** |
| `none` | no key, or a key that did not verify | as `licensed` for a 90-day trial from first start, then as `expired` |
| `unknown` | the state could not be determined | **nothing is restricted** |

A malformed or unverifiable key is `none`, never `expired`: a configuration
mistake must not punish harder than having no license at all. Nothing is ever
restricted except the *creation* of a new site or drop — updating and serving an
existing site keep working in every state.

#### Building your own Enterprise image

Three build inputs, all easy to miss, and missing them produces a binary that
looks fine and then stops creating sites after 90 days with no explanation:

```bash
docker build \
  --build-arg EDITION=enterprise \
  --build-arg LICENSE_ROOTS="<base64url ed25519 root public key>[,<another>]" \
  -t sitebin:latest-ee .
```

- **`EDITION=enterprise`** compiles the `ee/` tree in (`-tags ee`). Without it
  you get a community binary: no accounts, no tiers, no licensing at all.
- **`LICENSE_ROOTS`** is the comma-separated list of trusted Ed25519 **root
  public keys**, baked in with
  `-ldflags -X github.com/ittrail/sitebin.io/ee/licensing.trustedRootsB64=…`. A
  *list*, so a root can be rotated without redistributing every binary. **A
  build with no roots trusts nothing**, so no license can ever verify and the
  instance runs its 90-day trial and then restricts creation. It says so at
  startup — `this build carries no trusted license roots; running unlicensed` —
  and that line is the only warning you get.
- The root **public** key is all that is ever distributed. The private half is
  generated at the issuing stack's first bootstrap and never leaves it.
- `SITEBIN_LICENSE_ROOTS_DEV` overrides the baked roots **for development
  only**. Anything that can set it can mint itself a license; never set it on
  an instance you care about.

#### Getting and keeping a license

**The first license arrives by email**, when the subscription is bought. It
cannot come over the wire: the renewal endpoint authenticates with the license
itself, so an instance holding none has nothing to present and asks for nothing.
Paste it into `SITEBIN_LICENSE_KEY`, or let the instance collect it — see below.

**Renewals arrive on their own.** An instance that already holds a license posts
it to `<SITEBIN_PAYGATE_URL>/api/v1/licenses/renew` (override with
`SITEBIN_LICENSE_URL`) every `SITEBIN_LICENSE_REFRESH` (default 24h), caches the
result under the data dir and **applies it without a restart**. The request
carries no API key and never the stack admin key: the license is its own
credential, and an expired one still authenticates, since a signature does not
care about expiry and expiry is exactly when a renewal is wanted. A failed
collection never restricts anything.

> **The one trap worth knowing.** `SITEBIN_LICENSE_KEY` wins over anything
> collected — that is what makes an air-gapped install work — and it wins
> *before* the key is verified. So a key with a typo in it costs you twice: the
> instance falls back to the unlicensed trial arm **and** stops collecting
> renewals for as long as the variable is set. This is the likeliest way a
> paying customer ends up restricted. Sitebin logs it at `ERROR` on every start
> —
>
> ```
> license: SITEBIN_LICENSE_KEY DID NOT VERIFY, so this instance is running
> UNLICENSED ... and because the key is set, it will NOT collect a licence from
> the stack either. Fix or unset SITEBIN_LICENSE_KEY.
> ```
>
> — and the account UI shows the license as absent. If you are on a stack,
> unsetting `SITEBIN_LICENSE_KEY` is usually the fix: collection then works.

#### Entitlements

A license carries the limits its **plan** bought. The one Sitebin enforces today
is `entitlements.max_custom_domains`: an **instance-wide ceiling** on custom
domains across every site, checked where a domain is added. Absent or zero means
unlimited, which is also what no license means. It never removes a domain
already configured — it refuses only the next one — and a tier's own per-site
`custom_domains` cap still applies on top. See
[Custom domains](#custom-domains-enterprise).

Entitlements are set by the issuer, not by the instance: on the IT-Trail SaaS
Stack they come from the `licensing` block of the app registration
(`SITEBIN_STACK_LICENSING`), and a plan listed with no entitlements is
unlimited rather than zero.

## License

The repository is [MIT](LICENSE), **except** the `ee/` directory, which is
licensed separately under [`ee/LICENSE`](ee/LICENSE) — the [Elastic License
2.0](https://www.elastic.co/licensing/elastic-license).

The premium `ee/` code is deliberately kept **source-available** (published in
this repo), not closed. Under ELv2 you may read, self-host, modify, and use it
commercially for your own purposes; you may not offer it to third parties as a
hosted/managed service or circumvent the license-key functionality. ELv2 is
perpetual — it does not convert to open source over time.
