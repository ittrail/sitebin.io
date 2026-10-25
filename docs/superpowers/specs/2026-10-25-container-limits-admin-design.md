# Container limits the operator sets — design

Date: 2026-10-25. Status: approved for implementation (the operator's order,
2026-10-25: "ein admin in sitebin muss die container resourcen einstellungen
für alle container aller kunden ändern können").

## Intent

Every customer container runs with the same three limits — memory, CPU,
processes — fixed by the instance at startup (`SITEBIN_CONTAINER_MEMORY_MB` /
`_CPUS` / `_PIDS`, default 512 MB / 0.5 CPU / 256). They are written into the
container when it is created and never looked at again: changing the
environment needs a restart of Sitebin AND a restart of every project before
anything changes, and there is no way to give one customer's database more
memory than another customer's static helper.

The operator needs to change these from the admin console, for **every
container of every customer**:

1. the **instance defaults** — what every container gets unless told
   otherwise — and
2. **one container's own limits** — a service of one site (`db` of site X gets
   1 GB), which outrank the defaults.

Both take effect on running containers within seconds, **without restarting
the customer's app**.

## Goals

- An admin (tier flag + `SITEBIN_ADMIN_ACCOUNTS`, the register's existing gate)
  sees every container site, each service, its effective limits and where
  they come from (default / own), and what the host has.
- The admin changes the instance defaults; every container without its own
  value for a field follows.
- The admin sets, changes or removes one service's own limits; any field left
  empty inherits the default.
- Running containers converge in place (`POST /containers/{id}/update`), not by
  being recreated. A project that is stopped picks the values up when it next
  starts.
- Deploying this changes nothing for the three containers running on
  app.sitebin.io (512 / 0.5 / 256 stays 512 / 0.5 / 256, no restart).

## Non-goals

- Limits per **tier** (`tiers.json`). Not asked for; easy to add later as a
  layer between the default and the container's own value.
- Showing limits to the **customer** (edit page, API, MCP). The API payload is
  unchanged on purpose: MCP clients with a cached output schema reject new
  fields until they reconnect (the trap the site-names and container-addresses
  work hit). The docs say the operator may change them.
- A CLI for it. The console is the operator's tool.
- Notifying the customer of a change.

## Anti-goals (a failure even if it "works")

- A limits change that **restarts** customer apps when an in-place update
  would do, or takes a running project down on a transient Docker error.
- A limit Docker refuses (more CPUs than the host has) saved and then failing
  every start: values are checked against the host before they are stored.
- Silently changing customers' limits on an error — an unreadable defaults
  file must not make the runtime "converge" every container to something else.
- The core learning about Docker. It stores numbers; the extension runs them.

## Where the values live

**Instance defaults** have two layers, per field:

1. the environment (`SITEBIN_CONTAINER_*`, unchanged, still validated at boot)
   — the baseline, and what "reset" returns to;
2. the admin's saved defaults, `<data>/containers/limits.json`
   (`{"memory_mb":1024,"cpus":1,"pids":512}`, a field absent or 0 = the
   environment's). Written only by the console through the runtime, atomically
   (temp file + rename). It is instance configuration, so it lives beside the
   other instance files under the data volume and goes into `sitebin backup`
   with them.

If the file exists but cannot be read or parsed at startup, the runtime logs
it, creates new containers with the environment's values, and **does not
update running ones** until the admin saves defaults again (the console shows
the error). An error is "unknown", never "the environment's values".

**A container's own limits** are the site's desired state, so they live in
the site's `meta.json`, beside `enabled` and `restart_seq`:

```json
"container": {
  "enabled": true,
  "restart_seq": 3,
  "limits": { "db": { "memory_mb": 1024 }, "app": { "cpus": 1, "pids": 512 } },
  ...
  "services": [ { "name": "db", ..., "limits": { "memory_mb": 1024, "cpus": 0.5, "pids": 256 } } ]
}
```

- `container.limits` — keyed by service name, written by the core
  (`SiteService.SetContainerLimits`) on the console's order, never by the
  runtime. The two-writer rule holds: desired state is the core's.
- `container.services[].limits` — observed: what was last applied to that
  container, written by the runtime through `SetContainerState`. Absent on
  containers created before this change.

They go with the site when it is deleted. A service that disappears from the
compose file keeps its entry (harmless); the console lists it as "not in the
project" with a button to remove it. Leaving container mode keeps them too —
it is the operator's setting for that site.

**Effective limits**, per field: the service's own value → the saved default →
the environment.

## Bounds

The same floors as the environment variables, and ceilings from the host:

| | floor | ceiling |
|---|---|---|
| memory | 64 MB | the host's memory (Docker `/info` `MemTotal`), at most 1 TiB |
| CPUs | 0.01 | the host's CPUs (`NCPU`), at most 64; rounded to 0.01 |
| processes | 16 | 4 194 304 (the kernel's pid ceiling) |

The runtime reads `/info` when it finds its Docker Engine; if that fails, only
the static ceilings apply. An empty field is "inherit", never 0.

## How a change reaches a container

The reconciler already re-applies a project when `(compose hash, restart_seq)`
changes. Limits are deliberately **not** folded into that key: re-applying
recreates the containers.

- **At creation** (`apply` → `createBody`) the container gets its effective
  limits, and `describe` records them as applied.
- **A running project** (status `running`, hash and sequence current) whose
  applied limits differ from its effective ones — or are not recorded — is
  updated in place: one `POST /containers/sb-<site>-<service>/update` per
  service with `Memory = MemorySwap` (no swap, as at creation), `NanoCpus`,
  `PidsLimit`; then the new applied values are recorded. This runs on the
  5-second tick for running sites, so a change lands within seconds; saving in
  the console also kicks the site.
- **Legacy containers** (no applied values recorded) get one update to their
  effective values — a no-op on app.sitebin.io — and are recorded. No restart.
- **A failed update** leaves the project running, records the error for the
  console, and is retried after the usual minute (`retryAt`). A container that
  is gone (`404`) re-applies the project, which is what the full scan's
  `refresh` already does for a vanished container.
- With the saved defaults unreadable, no update runs (see above).
- A stopped, expired, locked or failed project has no containers to update;
  its next start uses the effective values.

Changing the defaults therefore updates every running container that has no
own value for that field — across all customers, within seconds, in place.
Lowering memory below what a process uses can get it OOM-killed by the kernel;
Docker then restarts it (`unless-stopped`). The console says so.

## The console

A third tab, **Containers** (`/account/admin/containers`), beside Sites and
Reports, shown only when the instance has container mode on (otherwise the
page is a 404, like the console for a non-admin). Server-rendered forms: the
console's CSP is `script-src 'none'`.

- **Figures**: container projects, running services, memory reserved by
  running services / host memory, CPUs reserved / host CPUs. Overcommit is
  shown, not refused — limits are ceilings, not reservations.
- **Instance defaults**: three inputs prefilled with the saved values,
  placeholders showing the environment's; Save; "Reset to environment" when
  anything is saved. The text says what saving does (running containers
  without their own value follow within seconds, not restarted).
- **Projects**: one block per container site — name/view id (linking to the
  register filtered to it), owner email, status, message — and one row per
  service: name, image, Docker state, effective limits with "own"/"default"
  per field, applied values when they differ ("updating…") and the last
  update error; a form with the three inputs (value = own setting, placeholder
  = the default) and Save, plus Clear when it has own values. Services with an
  entry but not in the project are listed with Remove.

Routes (admin + CSRF, like every console POST; a refusal is `400` with the
reason, as the expiry form does):

- `GET  /account/admin/containers`
- `POST /account/admin/containers/defaults` — `memory_mb`, `cpus`, `pids`
  (empty = the environment's), or `reset=1`
- `POST /account/admin/containers/sites/{id}/limits` — `service`,
  `memory_mb`, `cpus`, `pids` (empty = the default), or `clear=1`

CPUs accept a decimal comma (`0,5`). Every change is logged
(`admin set container defaults` / `admin set container limits`, with the
admin's id, site, service and values), like the register's other actions.

## Pieces

- `internal/ext`: `ContainerLimits{MemoryMB, CPUs, Pids}` (0 = not set);
  `ContainerSite.Limits map[string]ContainerLimits`;
  `ContainerService.Limits`; `SiteService.SetContainerLimits(viewID, limits)`.
- `internal/store`: `ContainerLimits` (json `memory_mb`/`cpus`/`pids`,
  omitempty), `ContainerMeta.Limits`, `ContainerService.Limits` (omitzero).
- `internal/httpapi`: `SetContainerLimits` (replaces the map, empty clears; a
  missing site is `ErrSiteGone`), `containerSiteOf` and `observedOf` /
  `SetContainerState` carry the values. No API route changes.
- `ee/containers`: `limits.go` — the defaults book (env, saved file, error,
  host capacity, `Check`, `SetDefaults`, `For`); `docker.go` —
  `ContainerUpdate`, `Info`; `manager.go` — effective limits at creation,
  `updateLimits` in `reconcile`, capacity at setup, per-site update errors.
- `ee`: the Containers tab (`admincontainers.go`), its template, routes, the
  tab link on the other two pages.
- Docs: README (env table, container section), the repo guide's container
  bullets, the 2026-09-22 spec's "fixed by the instance" sentence gets a
  pointer here; website `/docs/containers/` Limits.

## Left to the builder

Exact wording and layout of the tab (in the console's existing style), test
names, and how the per-site update errors are kept (memory is enough: they
describe the last attempt and are re-made by the next).
