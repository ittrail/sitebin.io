# Container usage: memory, CPU and public traffic — design

Date: 2026-10-25. Status: approved (the operator, 2026-10-25: current/max RAM
and CPU and network in/out per second per container on the edit page, and
the same for all containers in the admin console; traffic "reiner in und
outbound von public wenn möglich" — public only, not app ↔ database).

## Intent

The operator can now set every container's limits
(`2026-10-25-container-limits-admin-design.md`), but neither the operator nor
the customer can see what a container actually uses. Both need the current
figures next to the limits: is the database near its memory limit, is the app
busy, how much traffic does it serve.

## Goals

- Per container (compose service), current values:
  - **memory** in use / its limit,
  - **CPU** in use (cores, averaged over the last interval) / its limit,
  - **processes** in use / its limit,
  - **public traffic in and out per second**.
- The customer sees them for their own project on the edit page's container
  card, refreshed while the page is open.
- The operator sees them for every container in the console's Containers tab,
  plus instance totals against the host.

## Non-goals

- History, graphs, alerts. Current values only.
- An MCP tool. The site payload (and so `get_site`'s output schema) does not
  change: MCP clients with a cached schema reject new fields until they
  reconnect. The figures come from their own endpoint.
- Changing how container networks are wired (see "Public traffic").

## Public traffic — what is counted

The operator asked for public traffic only: what a container exchanges with
the outside world, not with the other services of its project. Docker counts
bytes per network interface, and a service's project-network interface
carries both its visitors' requests (from Sitebin's proxy) and its database
traffic. So the figures are taken where the two are separate:

- **Web traffic** (visitors ↔ the app). Every HTTP request reaches a container
  through Sitebin's own proxy, and Sitebin's container has one interface per
  project network. Its counters on that interface are exactly the project's
  web traffic: what Sitebin sends is the visitors' requests (**in** to the
  project), what it receives is the responses (**out**). Read from Sitebin's
  own `/sys/class/net/<if>/statistics`, the interface found by its MAC
  address in Sitebin's container inspection (`NetworkSettings.Networks`).
- **Internet traffic** (the container's own outbound connections). A service
  with `egress: allowed` reaches the internet over the project's egress
  network only. Its interface on that network gets a fixed name, `egress0`
  (the endpoint option `com.docker.network.endpoint.ifname`, Docker Engine
  28+), so the service's own stats name it. Containers created before this
  change have it as `eth1` — the runtime connects the egress network after
  creating the container on the project network, and Docker numbers in that
  order — and that name is the fallback.
- **Per container**: the internet traffic is the service's own. The web
  traffic belongs to the project; it is the service's when exactly one
  service of the project declares HTTP ports (every project on
  app.sitebin.io today). When several do, it is shown once for the project,
  not split — splitting it would need a network per web service, a rewiring
  of every running project, which is not worth it for that case.
- A service's **public in** = web in (if it is the project's web service) +
  internet received; **public out** = web out + internet sent.

Rates are deltas between two samples divided by the time between them. A
counter that went backwards (a container recreated, Sitebin restarted) gives
no rate for that sample.

## Sampling

The runtime samples every running managed container every **10 seconds**,
one `GET /containers/{id}/stats?stream=false&one-shot=true` each (at most 4
at a time, 5 s timeout), plus one inspection of its own container and a read
of its own interface counters. It keeps the previous raw sample per container
and per interface, and the latest computed figures per site, in memory only:
nothing is written to disk, a restart starts from nothing, and the first rate
appears after the second sample.

- **Memory in use** = `memory_stats.usage` − inactive file cache
  (`stats.inactive_file`, cgroup v2; `total_inactive_file`, v1) — what
  `docker stats` shows. The limit is the container's applied memory limit.
- **CPU** = Δ`cpu_stats.cpu_usage.total_usage` (ns) / Δ`read` (ns), in cores.
  The limit is the applied CPUs.
- **Processes** = `pids_stats.current` / the applied pids limit.
- Figures older than 60 s are not shown (the sampler stopped or the
  container did).

## Where it is shown

**API** (core): `GET /api/sites/{editID}/containers/stats`, behind
`withEditAuth` like the logs route, read-only (not recorded in provenance).
409 for a site not in container mode, 403 without a runtime. Answer:

```json
{
  "sampled_at": "2026-10-25T09:00:00Z",
  "services": [
    {"name": "app", "memory_bytes": 129000000, "memory_limit_bytes": 2147483648,
     "cpu": 0.03, "cpu_limit": 2, "pids": 12, "pids_limit": 512,
     "in_bps": 1830.5, "out_bps": 20480.2}
  ],
  "web": null
}
```

`cpu`, `in_bps` and `out_bps` are `null` until there are two samples; `web` is
`{"in_bps":…, "out_bps":…}` only for a project whose web traffic cannot be
given to one service. Services not running are absent.

**Edit page**: each service row of the container card gets a usage line —
`RAM 123 MB / 2 GB · CPU 0.03 / 2 · 12 / 512 processes · ↓ 1.8 KB/s ↑ 20 KB/s`
— and the card a "web traffic of the project" line when it applies. The page
polls the endpoint every 10 s while the card is shown, the project runs and
the tab is visible.

**Console** (Containers tab): the same line in each service row, and four
figures: memory in use (of the host's), CPU in use (of the host's), public
traffic in, public traffic out (sums over all running containers, web
traffic counted once). The console runs without JavaScript, so the page is a
snapshot "as of HH:MM:SS"; a **Live** link reloads it every 10 s
(`?live=1`, a `<meta http-equiv="refresh">`), and says that the forms are
best used with Live off.

## Pieces

- `ee/containers/docker.go`: `ContainerStats(ctx, id)` (one-shot) and the
  fields of `Inspect` for MAC addresses; `ifname` on the egress connect.
- `ee/containers/usage.go`: the sampler (raw samples, rates, attribution) and
  `Manager.Usage(viewID)`; the Sitebin-side interface reader behind a small
  interface so tests can fake `/sys/class/net`.
- `internal/ext`: `ContainerUsage`, `ServiceUsage`, `ContainerRuntime.Usage`.
- `internal/httpapi`: the stats route.
- `web/static/edit.js` + `edit.html`/`app.css`: the usage line and the poll.
- `ee/admincontainers.go`: figures, per-row line, Live.
- Docs: README (container section, API list), website `/docs/containers/`.

## Left to the builder

Formatting details (units, rounding), the CSS of the usage line in the
existing card style, test names.
