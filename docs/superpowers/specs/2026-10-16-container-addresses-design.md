# Container addresses

**Date:** 2026-10-16
**Status:** implemented

## Problem

A container site's addresses lived in its compose file: each service listed
`domains: ["*:3000", "shop.example.com:3000"]`, `*` being the site's own
address. That made addresses a second, poorer kind of domain:

- the domain redirects and the generated-address switch every file site has
  (2026-10-16-domain-redirects-design.md) were refused on container sites,
  because their edit page had no domain card to hang them on;
- adding a domain meant editing the file that also holds the app's passwords,
  and every edit of that file redeploys the whole project;
- a service on a second port had an address only if the owner thought of a
  domain for it.

PhysioPrint shows the cost: it maps four addresses to one service and lets
the app answer the canonical redirect itself, which sits behind Sitebin's
view-password gate — a visitor unlocks the apex, is redirected to `www`, and
unlocks again, because the gate's cookie is per host.

## What changes

The compose file says **what runs and which ports speak HTTP**. The site's
domain settings say **which address serves which port**, exactly as they do
for a file site.

```yaml
services:
  app:
    image: alpine-node-22
    http: [3000, 3001]
  db:
    image: mysql-8.4
```

- **`http:`** lists a service's HTTP ports (1–65535, at most 10, no
  duplicates). Each `service:port` is an **endpoint**. A service without
  `http:` has no address; services still reach each other on any port by
  name. The key is not `expose`: in Docker that means *internal only*, and
  compose files in the wild carry `expose: [5432]` on their databases — a
  ported file would publish them.
- **`domains:` is gone.** A file that still has it is refused with a message
  that says what replaced it. There is no compatibility path: every container
  site on the hosted instance belongs to the operator, who rewrites them
  (decided 2026-10-16).
- **Every endpoint gets a generated address.** The first endpoint of the file
  gets the site's own address, `<view id>.<view domain>` — the common app has
  one port, and its `view_url` then means what it means for a file site.
  Every further endpoint gets an address of its own with a **random** label,
  as unguessable as a view id: an admin port must not be found by reading the
  app's address. An endpoint keeps its address as long as the file declares
  it; a new endpoint takes the site's own address whenever no endpoint holds
  it (a renamed service keeps the site's address), else a fresh random one.
- **Custom domains are added on the edit page, the API and MCP** — the same
  claim, DNS proof, quota and certificate as for a file site — and each one
  **serves an endpoint** (its *target*). Without one named, a domain serves
  the endpoint of the site's own address; a domain that has no target yet (no
  port declared when it was added) gets that endpoint as soon as there is one.
- **Every address serves, redirects or is off**, with the file site's rules:
  - a custom domain serves its endpoint, or redirects (301/302/307/308, path
    and query kept) to another of the site's domains that serves;
  - a generated address serves its endpoint, redirects to a domain that
    serves, or is off — **off only while a custom domain serves the same
    endpoint**, the file site's "never offline" rule, per port. An owner who
    wants a port not public at all does not list it under `http:`.
- **A port the file stops declaring** takes its generated address with it.
  Custom domains pointed at it **stay**, answer `404 Nothing here`, and the
  domain card says so: a domain is never moved to another service on its own
  (that could put the wrong app under it) and never detached (that is the
  owner's decision).

Nothing in the compose file touches an address any more, so adding,
redirecting or switching off an address never restarts the project.

## Rules in effect

The stored rules are the owner's; what is in effect is decided against what
exists now, as for file sites:

- a domain **serves** when it is attached, no redirect of its own is in effect,
  and — on a container site — its target is an endpoint the file declares;
- a redirect is in effect when its target serves and has no rule of its own
  (one hop, never a chain);
- a generated address's off is in effect while a domain serving its endpoint
  exists; its redirect while the target serves.

A rule that is not in effect serves (or, for a domain without a live target,
answers 404), so a sweep detachment or a compose edit never leaves a dead
redirect behind.

## How

**Store.** `Meta.Addresses` (container sites only) lists the endpoints, in
file order, each with its label and its rule (`nil` serves, else off or a
redirect). `Meta.DomainTargets` maps a custom domain to its endpoint. A random
label is indexed in `data/address-index/<label>` → `../sites/<view id>`, like
the edit and domain indexes; `ByAddress` resolves it, delete and
`DanglingIndexLinks` clean it up. `Meta.ViewAddress` stays the file site's
own-address rule and is ignored on a container site, whose own address's rule
is its entry in `Addresses`.

**Runtime → core.** After the runtime parses a compose file successfully it
calls `SiteService.SyncContainerEndpoints(viewID, endpoints)` (replacing
`SyncContainerDomains`), before the plan check, so a project the plan refuses
still shows its addresses. The core assigns labels, drops addresses of
vanished endpoints (and their index links), repairs missing links, and fills
in missing domain targets. A broken compose file or a stopped project changes
nothing: the addresses stay as they were. The observed services carry their
`ports` instead of domain mappings.

**authz.** `siteByHost` resolves a view-domain label by view id, then through
the address index. The address rule (redirect, off) is answered after the lock
and the expiry and before the view-password gate, as for file sites — so
PhysioPrint's apex redirects before anyone is asked for a password. Admitting
a container site resolves the host to an endpoint (generated label → its
endpoint; custom domain → its target) and the endpoint to the running
service's host and port. A label or domain with no live endpoint is `404
Nothing here`; a project that is not running is `503`, as before.

**Mode changes.** Leaving container mode drops `Addresses`, their index links
and `DomainTargets`; the domains themselves and their redirects stay and serve
the files. Entering it starts with no addresses until the runtime's first
successful parse.

## Surfaces

- **API.** `POST /api/sites/{edit}/domains` takes an optional `"target":
  "app:3000"`. `PUT /api/sites/{edit}/domains/{domain}/target`
  `{"endpoint": "app:3001"}` points a domain at another port.
  `PUT /api/sites/{edit}/view-address` takes an optional `"endpoint"` naming
  which generated address (default: the site's own). Domain redirects work
  unchanged. The payload gains `addresses` (`endpoint`, `url`, `mode`, `to`,
  `code`, `in_effect`) and `domain_targets`; `view_address` on a container site
  is its own address's rule; container services report `ports`. A file site
  answers `addresses: []`, `domain_targets: {}`.
- **MCP.** `add_domain` takes an optional `target`; a new tool
  `set_domain_target` points a domain at another port; `set_view_address`
  takes an optional `endpoint`. `SiteResult` gains `addresses` and
  `domain_targets`, both omitempty and present only on container sites, so a
  client that cached the schema sees no change for any other site. Nothing is
  renamed or removed.
- **Edit page.** A container site shows the domain card: one row per
  generated address (`app:3000`) with serve / off / redirect, one row per
  custom domain whose dropdown serves a port or redirects to a domain, and a
  port select beside "Add domain" when the app has more than one. The
  container card lists each service's ports instead of domain mappings.
- **Provenance.** `domain-target` records a target change; a view-address
  change names the endpoint.

## Testing

- `ee/containers`: `http:` parsing (lists, scalars, bounds, duplicates),
  `domains:` refused with its hint, endpoints in file order, the runtime
  syncing endpoints after a good parse and not after a bad one.
- `store`: label assignment (own address first, random afterwards, stable,
  own address reused by a new endpoint, dropped with its link, links
  repaired), filling missing targets, every rule in effect or not (per-port
  off, redirect to a domain without a live target, vanished endpoint), the
  setters' refusals, `RemoveDomain` pruning targets and address rules, delete
  and leaving the mode dropping the index.
- `httpapi`: authz routing by own address, random label and domain target;
  redirect and off before the gate; 404 for a vanished target; the routes; the
  payload; MCP tools.
- `e2e/containers.ps1`: the real project — own address, a second port through
  a domain, a random address for a second service, a domain redirect and an
  off on a container site, a dropped port turning its domain into a 404.
