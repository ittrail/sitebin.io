# Container sites: a third mode that runs the project instead of serving it

2026-09-22. Requested by the operator the same day ("Neben WebServer und
FileView soll auch noch Container verfügbar sein"), with the instruction to
proceed without further questions. Every call this document makes that the
request did not settle is listed under **Decisions taken without asking**, so
it can be revisited.

## What the customer gets

A site has had two modes: **Web server** (serve the files) and **File viewer**
(wrap one document). A third, **Container**, runs the project instead. The
project's root holds a `sitebin-container-compose.yaml`:

```yaml
services:
  app:
    image: alpine-node-22
    environment:
      NODE_ENV: development
      DB_HOST: db
    volumes:
      - app:/usr/src/app
    domains:
      - "*:3000"
      - "shop.example.com:3001"
    egress: allowed
  db:
    image: mysql-8.4
    environment:
      MYSQL_ROOT_PASSWORD: rootpassword
      MYSQL_DATABASE: appdb
    volumes:
      - db:/var/lib/mysql
```

- **`image`** is one of Sitebin's own names, never a registry reference. The
  catalogue is fixed in code: `alpine-node-22` (`node:22.23.2-alpine3.24`) and
  `mysql-8.4` (`mysql:8.4.11`), both pinned by tag **and** digest.
- **`volumes`** mount a *root folder of the project* into the container:
  `<folder>:<absolute path>[:ro]`. A folder that does not exist yet is created.
  The folder is the same one the edit page, WebDAV and FTP show, so uploading
  code into `app/` is how code reaches the container.
- **`domains`** map a host to a port inside the container, like a port
  mapping: `<host>:<port>`. `*` is the site's own address
  (`<id>.<view domain>`); anything else is a custom domain, which goes through
  the same DNS proof as any other custom domain.
- **Services reach each other by name** (`db:3306`) on a network private to
  the project.
- **`egress: allowed`** lets a service reach the internet. The default is no
  egress at all. (`egres`, as the request spelled it, is accepted as a
  synonym.)
- **`environment`** is a map or a `KEY=value` list, as in Compose. There is
  **no variable interpolation**: `$HOME` is the five characters `$HOME`.
- Optional **`command`**, **`working_dir`** and **`depends_on`** (start order
  only, no health waiting) are accepted because nearly every real Compose file
  has them. Every other key is an error that names the key, so nobody wonders
  why their `ports:` did nothing.

**Every change to the compose file restarts the project** — however the file
arrived: upload, ZIP, the in-browser editor, WebDAV, FTP, the API or MCP.

The edit page gets a **Container** mode. In it, the custom-domains editor is
replaced by the mappings the compose file declares (with the DNS records a
pending custom domain still needs), per-service state, Start / Stop / Restart,
and each service's recent log.

It is an **Enterprise** feature and a **paid-tier** feature: the tier's
`max_containers` caps the number of running services **across all of an
account's projects** — Pro 3, Studio 20 on the hosted instance. Memory, CPU and
process limits are fixed per container by the instance, not chosen by the
customer.

## Architecture

The two rules that shape this codebase decide where each piece goes.

### Core (MIT) — the mode, the routing, the file safety

The core knows that a site can be in container mode, what it should be doing
(**desired state**) and what it was last seen doing (**observed state**), and
how to route a request to it. It contains no Docker code.

- `store.ModeContainer = "container"`, and `Meta.Container *ContainerMeta`
  (`omitempty`: older sites simply do not have it).
  - Desired: `enabled` (Start/Stop), `restart_seq` (Restart bumps it).
  - Observed, written only by the runtime through the seam: `status`
    (`starting` · `running` · `stopped` · `error`), `message`,
    `applied_hash` / `applied_seq`, and per service its image, egress,
    volumes, domain mappings, live state and upstream name.
- **Routing stays off the extension.** `authz` already answers every content
  request; for a container site it now also answers *where to*: it finds the
  service whose domain matches the request host and returns
  `X-Sitebin-Upstream: sb-<id>-<service>:<port>`. Caddy copies that header
  onto the request (`forward_auth … copy_headers`) and proxies to it. The
  answer comes from `meta.json` like everything else authz reads, so the hot
  path never asks the extension — the rule the tier stamping exists for.
- A container site **is never served statically**. authz answers 200 only
  with an upstream; with none it answers 503 ("not running") or 404 ("nothing
  is mapped to this address"), and forward_auth relays that page. This
  matters: the project folder holds the database's files and the compose file
  holds its passwords.
- The Caddyfile grows three lines per content route, and they are inert for
  every other site: strip any client-sent `X-Sitebin-Upstream` *before*
  forward_auth (otherwise a visitor could choose the upstream — an SSRF), and
  after it `reverse_proxy @proxied {http.request.header.X-Sitebin-Upstream}`
  with the header removed on the way up.
- `/v/<id>` path views do not proxy; a container site is only reachable on its
  own host.
- **API** (edit auth, like every site endpoint):
  `POST /api/sites/{edit}/containers/{start|stop|restart}` and
  `GET /api/sites/{edit}/containers/{service}/logs?tail=N`. Mode changes go
  through the existing `PUT` with `"mode": "container"`.

### The seam

`ext.ContainerProvider` is an **optional** interface a Provider may implement:
`Containers() ext.ContainerRuntime`, nil when the instance has containers off.
Optional, so no existing provider or test double changes, and the community
build — no provider — has no container mode at all, which is the whole
community path and has its own test.

```go
type ContainerRuntime interface {
    Allowed(ownerAccountID string) error   // may this owner use container mode?
    Kick(viewID string)                     // converge this site now (async)
    Stop(viewID string) error               // remove its containers, synchronously
    Logs(ctx context.Context, viewID, service string, tail int) (string, error)
}
```

`ext.SiteService` gains what the runtime needs from the core, which owns the
filesystem layout: `ContainerSites()` / `ContainerSite(id)` (desired state, the
compose file's bytes, owner, expiry), `PrepareVolume(id, folder)` (create the
root folder, refuse a symlink, return its path relative to the data dir),
`SetContainerState(id, state)` and `SyncContainerDomains(id, domains)` (claims
for the compose file's custom domains, through the same `AddDomain` gate and
quotas as the domain editor).

### Enterprise (ELv2) — `ee/containers`

- `compose.go` — the strict parser and validator. Pure, table-tested.
- `catalog.go` — the image catalogue and each image's defaults.
- `docker.go` — a small Docker Engine API client over the unix socket (or
  `tcp://`, for a socket proxy). No SDK: the SDK would add a very large
  dependency tree to a binary whose selling point is having almost none, and
  we need eleven endpoints.
- `manager.go` — the reconciler. It converges Docker to the desired state and
  reports observed state back. It is the only writer of observed state.

**Reconciling, not commanding.** The core writes desired state and calls
`Kick`; the manager compares `(sha256(compose), restart_seq)` with what it last
applied and re-applies on any difference. A 5-second tick re-checks the known
container sites (that is what makes *every* write path restart the project —
WebDAV and FTP never tell anyone they wrote a file), and a 60-second full scan
also: re-attaches Sitebin itself to every project network (a redeploy creates
a new Sitebin container that is on none of them), refreshes per-service
state, removes containers whose site is gone, no longer in container mode,
stopped or expired, and enforces the plan cap after a downgrade. A failed
apply records the hash it failed on, so a broken compose file is reported once
rather than retried every five seconds; a *transient* failure (Docker down,
plan unknown) backs off and retries.

**Apply** = validate → check the plan → pull missing images → remove the
project's containers → ensure networks → attach Sitebin → prepare volumes →
create and start in `depends_on` order → claim domains → report.

### Isolation

Per container: `User` = Sitebin's own uid:gid (1000), so every file a
container writes is one Sitebin can manage and delete; `CapDrop ALL`;
`no-new-privileges`; **read-only root filesystem** with tmpfs for `/tmp` and
the image's run directories (tmpfs is charged to the memory limit, so it is
bounded); the image's declared data paths get a tmpfs too when the customer
does not mount a folder there, so an unmounted `/var/lib/mysql` cannot become
an unbounded anonymous volume; `Memory` = `MemorySwap` (no swap), `NanoCPUs`,
`PidsLimit`; `json-file` logs capped at 2 × 10 MB; restart policy
`unless-stopped`; an optional `SITEBIN_CONTAINERS_RUNTIME` (e.g. `runsc`) for
operators who want gVisor. No port is ever published on the host.

Per project: an **internal** bridge network `sb-<id>` (no route out) on which
services carry their names as aliases, and — only if some service has egress —
`sb-<id>-egress`, a normal bridge with inter-container traffic disabled, that
only those services join. Projects never share a network. Sitebin joins each
internal network so Caddy can reach the upstreams by name; Sitebin's backend
listens on loopback only, so a container on that network reaches nothing it
could not reach from the internet.

### Volumes, and the symlink problem they create

Mounting the project's folders into untrusted containers means **the site's
file tree is now written by code Sitebin does not control**. A container can
create `app/x -> /data`. Until now nothing inside `files/` could be a symlink
(uploads and ZIPs refuse them), so every file surface followed paths freely:
the API's read, write and delete, the ZIP download, WebDAV (`webdav.Dir`), FTP
(`afero.BasePathFs`, which is lexical only). Each would have followed that
link out of the site — reading the instance secret, writing into other
customers' sites. Caddy's `file_server` would too, if the site were switched
back to web-server mode.

The fix is a core invariant, for every mode: **Sitebin never follows a symlink
out of a site's content directory.** Store operations, WebDAV and FTP resolve
paths through `os.Root` (Go 1.25), which confines resolution with `openat`
and is not subject to the check-then-use race a per-component `Lstat` would
have against a container running concurrently. Listings and the ZIP skip
symlinks. Leaving container mode stops the containers synchronously and then
removes every symlink in the tree, so Caddy never serves one. `sitebin backup`
skips symlinks that point outside the data root (which `restore` would refuse,
failing the whole archive) and anything that is not a file, a directory or a
link (a socket from a container would otherwise abort the backup).

Mounts are built from the host path of Sitebin's `/data`, which Docker must
know. The manager inspects its own container at start: a bind mount gives the
host path; a named volume uses `VolumeOptions.Subpath` (Engine API 1.45,
Docker 26+). `SITEBIN_CONTAINERS_DATA_MOUNT` overrides either. The folder is
checked to be a real directory, not a link, immediately before it is mounted.

### Quotas

- **Containers per account**: tier `max_containers`. `0` / absent means *no
  containers* — the same polarity as `custom_domains`, because a free tier
  that forgets the field must not get compute. It is resolved when a project
  is applied (the tier is not on the hot path there) and on the 60-second
  scan; if a downgrade leaves an account over its cap, the most recently
  applied projects are stopped until it fits. A tier that cannot be resolved
  stops nothing and starts nothing new.
- **Bytes**: the site's byte cap applies to everything in the tree, including
  what containers write. Uploads are refused as before; a container that
  writes past the cap cannot be refused mid-write, so the scan measures
  container sites every five minutes and stops a project that is over,
  with a message saying why. Stopping is never destructive: the files stay.
- **File count** does not apply in container mode. `npm install` alone
  writes tens of thousands of files; a 5,000-file cap would make the mode
  useless while protecting nothing the byte cap does not. The listing the
  edit page receives is capped at 2,000 entries for the same reason.

### Configuration (ee)

| Variable | Default | |
|---|---|---|
| `SITEBIN_CONTAINERS` | `off` | `docker` turns the mode on |
| `SITEBIN_CONTAINERS_DOCKER_HOST` | `unix:///var/run/docker.sock` | or `tcp://socket-proxy:2375` |
| `SITEBIN_CONTAINERS_DATA_MOUNT` | auto | host path of `/data`, or `volume:<name>` |
| `SITEBIN_CONTAINERS_SELF` | auto | Sitebin's own container id |
| `SITEBIN_CONTAINERS_RUNTIME` | *(Docker's)* | e.g. `runsc` |
| `SITEBIN_CONTAINER_MEMORY_MB` | `512` | per container |
| `SITEBIN_CONTAINER_CPUS` | `0.5` | per container |
| `SITEBIN_CONTAINER_PIDS` | `256` | per container |

Startup never fails because Docker is unreachable — the mode reports it, like
the licence does. A malformed value is a startup error, like every other
`eeconfig` value.

**Giving Sitebin the Docker socket is giving it root on the host.** The
docs say so plainly and recommend a socket proxy and a dedicated host.

## Decisions taken without asking

1. `*` means the site's own address, and a project may map it once. Several
   default hostnames per project would need a naming scheme under the view
   domain; nothing in the request needed it yet.
2. Custom domains in the compose file are claimed automatically and still need
   the DNS proof. Skipping the proof would reopen the takeover
   `2026-09-07-custom-domain-verification-design.md` closed.
3. `command`, `working_dir`, `depends_on` accepted; nothing else. The node
   image's default command installs dependencies when `node_modules` is
   missing (egress permitting) and runs `npm start`, or `node index.js`
   without a `package.json`.
4. MySQL runs with `--performance-schema=OFF`, a 128 MB buffer pool and
   `--secure-file-priv=NULL`, so it fits the fixed 512 MB.
5. Tiers mode only: without tiers there is no `max_containers` to read, and
   inventing a second cap source is not worth it before anyone asks.
6. No licence enforcement: the licensing rules allow exactly two enforcement
   points and neither is "start a container". The tier cap is the gate.
7. No MCP tools yet — the `mode` description names the new mode, so an agent
   can switch a site to it. A `restart_site` tool is the obvious follow-up and
   a contract addition in both repos.
8. Files stay listed and editable: they are the project's source.
9. Stop removes the containers (keeping networks would buy nothing — the data
   is in the folders).

## Testing

- Parser: table tests for every key, every error, the example above.
- Manager: against a fake `engine` interface — apply, change detection,
  restart, stop, orphan removal, cap enforcement on apply and on downgrade,
  quota stop, backoff, re-attach after redeploy.
- Docker client: an integration test behind `-tags dockertest`, run in a Linux
  container with the socket mounted.
- Core: authz upstream answers and refusals, the header strip in the
  Caddyfile, mode validation in both editions, the API endpoints, the symlink
  confinement of every surface (API, ZIP, WebDAV, FTP, backup).
- E2E: `e2e/containers.ps1` runs the enterprise image with the socket, deploys
  the example project (node + mysql), and fetches a page that the node app
  rendered from a MySQL query, through Caddy.
