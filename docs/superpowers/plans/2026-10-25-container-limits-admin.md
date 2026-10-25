# Container limits the operator sets — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The admin console changes the memory / CPU / process limits of every customer container — instance defaults and per-service values — and running containers follow in place.

**Architecture:** Per-service values are desired state in `meta.json` (`container.limits`), written by the core on the console's order; the instance defaults are a file the runtime owns (`<data>/containers/limits.json`) layered over the environment. The reconciler creates containers with the effective values and updates running ones with `POST /containers/{id}/update` when the applied values (recorded per service) differ.

**Tech Stack:** Go 1.25, Docker Engine API v1.45, `html/template`, no new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-25-container-limits-admin-design.md`

## Global Constraints

- Bounds: memory 64 MB … min(host memory, 1 TiB); CPUs 0.01 … min(host CPUs, 64), rounded to 0.01; processes 16 … 4 194 304. Empty = inherit, never 0.
- Effective value per field: the service's own → the saved default → the environment.
- Limits are NOT part of the compose hash: a limits change never recreates a project unless its container is gone.
- An unreadable `limits.json` = unknown: new containers get the environment's values, running ones are not updated.
- The customer API / MCP payload does not change.
- Both suites: `go test ./...` and `go test -tags ee ./...`; `go vet ./...` and `go vet -tags ee ./...`.
- `e2e/*.ps1` stay pure ASCII.

## Review Focus

- A Docker update failing for a transient reason must leave the project running and retry (no teardown). → Task 4 `TestLimitUpdateFailureKeepsTheProjectRunning`.
- A container recorded before this change (no applied values) is updated once and NOT recreated. → Task 4 `TestLegacyContainerIsUpdatedNotRecreated`.
- `0`, negatives, letters and `0,5` in the console's form. → Task 5 `TestParseLimitsForm`.
- A CPU value above the host's CPUs is refused before it is stored. → Task 2 `TestCheckAgainstTheHost`.
- The defaults file unreadable at startup. → Task 2 `TestUnreadableDefaultsAreUnknown`, Task 4 `TestNoUpdatesWhileDefaultsAreUnknown`.

---

### Task 1: The seam and the store carry limits

**Files:**
- Modify: `internal/ext/ext.go` (ContainerLimits, ContainerSite.Limits/Name, ContainerService.Limits, SiteService.SetContainerLimits)
- Modify: `internal/store/container.go` (store.ContainerLimits, ContainerMeta.Limits, ContainerService.Limits)
- Modify: `internal/httpapi/containers.go` (SetContainerLimits, containerSiteOf, observedOf, SetContainerState)
- Modify: fakes in `ee/provider_test.go`, `ee/containers/manager_test.go`
- Test: `internal/httpapi/containers_test.go` (or the file holding the siteService container tests)

**Interfaces — Produces:**
- `ext.ContainerLimits{MemoryMB int; CPUs float64; Pids int}`; `func (l ContainerLimits) Or(d ContainerLimits) ContainerLimits` (per field: l's if > 0, else d's).
- `ext.ContainerSite.Limits map[string]ext.ContainerLimits`, `ext.ContainerSite.Name string`.
- `ext.ContainerService.Limits ext.ContainerLimits` (as applied).
- `SiteService.SetContainerLimits(viewID string, limits map[string]ext.ContainerLimits) error` — replaces the map; nil/empty clears; missing site → wraps `ext.ErrSiteGone`. Drops entries whose fields are all zero.
- store json: `container.limits.<svc>.{memory_mb,cpus,pids}` (omitempty), `container.services[].limits` (omitzero).

- [ ] Step 1: tests — `TestSetContainerLimitsIsDesiredState` (set → `ContainerSite(id).Limits` equals it; then `SetContainerState` with services carrying Limits → desired map unchanged, `Observed.Services[i].Limits` round-trips; `SetContainerLimits(id, nil)` → `Limits` empty and `meta.json` has no `"limits":{` key for the site); `TestContainerLimitsOmittedWhenUnset` (a container site's meta.json without limits marshals no `limits` key); `ext` `TestLimitsOr`.
- [ ] Step 2: run `go test ./internal/...` — FAIL (undefined).
- [ ] Step 3: implement; fakes get `SetContainerLimits`.
- [ ] Step 4: `go test ./... && go test -tags ee ./...` — PASS.
- [ ] Step 5: commit `feat: a container site records the operator's per-service limits and what was applied`.

### Task 2: The limit book (defaults, file, bounds)

**Files:**
- Create: `ee/containers/limits.go`, `ee/containers/limits_test.go`

**Interfaces — Produces:**
- `func NewLimitBook(dataDir string, env ext.ContainerLimits) *LimitBook` — reads `<dataDir>/containers/limits.json`; a missing file is no error; an unreadable/invalid one is recorded (`Err`).
- `(*LimitBook) Env() / Saved() / Defaults() ext.ContainerLimits` — Defaults = Saved.Or(Env); with Err set, Defaults = Env.
- `(*LimitBook) Err() error`, `Known() bool` (Err == nil).
- `(*LimitBook) SetCapacity(cpus int, memBytes int64)`, `Capacity() (int, int64)`.
- `(*LimitBook) Check(l ext.ContainerLimits) error` — every non-zero field in bounds; negatives refused.
- `(*LimitBook) SetDefaults(l ext.ContainerLimits) error` — Check, write atomically (all zero → remove the file), clear Err.
- `(*LimitBook) For(own map[string]ext.ContainerLimits, service string) ext.ContainerLimits` — own[service].Or(Defaults()).

- [ ] Step 1: tests — `TestDefaultsLayerOverTheEnvironment` (env 512/0.5/256, save {MemoryMB:1024} → Defaults 1024/0.5/256; new book on the same dir reads it; save zero → file gone, Defaults = env); `TestForPrefersTheServicesOwn`; `TestCheckBounds` (63 MB, 0.001 CPU, 15 pids, -1 refused; 64/0.01/16 ok; 4194305 pids refused); `TestCheckAgainstTheHost` (SetCapacity(6, 16<<30): 6.5 CPUs and 17000 MB refused, 6 CPUs ok; capacity 0 → static ceilings 64 CPUs / 1 TiB); `TestUnreadableDefaultsAreUnknown` (garbage file → Err non-nil, Known false, Defaults = env; SetDefaults clears Err).
- [ ] Step 2: run `go test -tags ee ./ee/containers/ -run 'Defaults|For|Check|Unreadable'` — FAIL.
- [ ] Step 3: implement (mutex-guarded; temp file + rename in the same dir, 0o644).
- [ ] Step 4: PASS.
- [ ] Step 5: commit `feat: the container runtime keeps instance default limits the console can change, checked against the host`.

### Task 3: Docker client — update and info

**Files:**
- Modify: `ee/containers/docker.go`
- Create: `ee/containers/docker_test.go`

**Interfaces — Produces:**
- `type Resources struct{ Memory, MemorySwap, NanoCpus, PidsLimit int64 }` (JSON names as the Engine's).
- `func (c *Client) ContainerUpdate(ctx context.Context, id string, r Resources) error` — `POST /containers/{id}/update`.
- `type EngineInfo struct{ NCPU int; MemTotal int64 }`; `func (c *Client) Info(ctx context.Context) (EngineInfo, error)` — `GET /info`.

- [ ] Step 1: test `TestContainerUpdateAndInfo` against an `httptest.Server` via `NewClient("tcp://"+addr)`: the update request is `POST /v1.45/containers/sb-x-app/update` with exactly the four fields; a 404 answer `errors.Is(err, errNotFound)`; `/v1.45/info` `{"NCPU":6,"MemTotal":16762245120}` decodes.
- [ ] Step 2: FAIL. Step 3: implement. Step 4: PASS.
- [ ] Step 5: commit together with Task 4 (the engine interface needs both).

### Task 4: The manager applies and converges limits

**Files:**
- Modify: `ee/containers/manager.go`, `ee/containers/manager_test.go`

**Interfaces:**
- Consumes: Task 1 types, Task 2 `LimitBook`, Task 3 client methods.
- Produces: `engine` gains `ContainerUpdate`, `Info`; `(*Manager) Limits() *LimitBook`; `(*Manager) UpdateError(viewID string) string`; `resources(l ext.ContainerLimits) Resources` used by `createBody` and the update.

- [ ] Step 1: tests (fake engine records updates per name, `updateErr`, `info`):
  - `TestOwnLimitsAtCreation` — site with Limits{"db":{MemoryMB:1024}}: db HostConfig Memory 1024<<20, NanoCpus 5e8; app 512<<20; Observed services carry those as Limits.
  - `TestDefaultsChangeUpdatesInPlace` — running project; `m.Limits().SetDefaults({CPUs:1})`; tick reconcile → one update per service with NanoCpus 1e9, `eng.created` unchanged, Observed Limits CPUs 1; a second tick sends no update.
  - `TestOwnLimitsChangeUpdatesOnlyThatService`.
  - `TestLegacyContainerIsUpdatedNotRecreated` — Observed services with zero Limits → update to 512/0.5/256, no create.
  - `TestLimitUpdateFailureKeepsTheProjectRunning` — updateErr → status stays running, `UpdateError` non-empty, no teardown; next non-kicked tick within a minute sends nothing; after `Kick` it retries; success clears the error.
  - `TestLimitUpdateOfAVanishedContainerReapplies` — container removed from the fake → update 404 → project re-created with the new limits.
  - `TestNoUpdatesWhileDefaultsAreUnknown` — book with Err → no update.
  - `TestSetupReadsHostCapacity` — fake info 6 / 16 GiB → `Limits().Capacity()`.
- [ ] Step 2: FAIL. Step 3: implement per the spec's "How a change reaches a container" (update only on the tick path, `present == nil`; the full scan keeps `refresh`). Step 4: `go test -tags ee ./ee/...` PASS.
- [ ] Step 5: commit `feat: running containers follow changed limits in place, without a restart`.

### Task 5: The console's Containers tab

**Files:**
- Create: `ee/admincontainers.go`, `ee/admincontainers_test.go`
- Modify: `ee/dashboard.go` (routes), `ee/provider.go` (`limits *containers.LimitBook`), `ee/containers.go` (set it), `ee/templates.go` + `ee/abuse.go` (tab link when containers are on), `ee/provider_test.go` (fakeSites: `containers []ext.ContainerSite`, `limitsSet`)

**Interfaces:**
- Consumes: `p.limits` (`*containers.LimitBook`), `p.containers.Kick/UpdateError` when non-nil, `SiteService.ContainerSites/SetContainerLimits`.
- Produces: routes `GET /account/admin/containers`, `POST /account/admin/containers/defaults`, `POST /account/admin/containers/sites/{id}/limits`; `parseLimitsForm(r *http.Request) (ext.ContainerLimits, error)`.

- [ ] Step 1: tests —
  - `TestContainersTabGate` — non-admin 404, signed-out 404, admin with `p.limits == nil` 404, admin with a book 200.
  - `TestContainersTabShowsEveryService` — two sites, three services, one own value: page names each service, the owner email, "own" on the overridden field, the defaults, host capacity.
  - `TestParseLimitsForm` — `""` → 0; `"0,5"` → 0.5; `"0.333"` → 0.33; `"0"`, `"-1"`, `"abc"` → error.
  - `TestSaveContainerDefaults` — missing CSRF 403; `cpus=99` with capacity 6 → 400, nothing saved; valid → 303 to `?flash=defaults`, book Saved updated; `reset=1` → Saved zero.
  - `TestSaveContainerLimits` — valid → `SetContainerLimits` got the merged map (other services kept); all fields empty or `clear=1` → that service's entry dropped; unknown site → 404.
- [ ] Step 2: FAIL. Step 3: implement (server-rendered, the console's CSS and tab style). Step 4: `go test -tags ee ./ee/...` PASS.
- [ ] Step 5: commit `feat: the admin console's Containers tab sets limits for every customer container`.

### Task 6: Docs, full verification, e2e

**Files:** `README.md`, `CLAUDE.md` (container bullets), `docs/superpowers/specs/2026-09-22-container-sites-design.md` (pointer), `e2e/containers.ps1` (admin sets a service's memory; `docker inspect` shows it, the container's StartedAt unchanged), website `public/docs/containers/index.html`, workspace `../CLAUDE.md` coupling line.

- [ ] Step 1: docs.
- [ ] Step 2: `go vet ./... && go vet -tags ee ./... && go test ./... && go test -tags ee ./...` — all ok.
- [ ] Step 3: build `sitebin:dev-ee`, run `e2e/containers.ps1` — all checks pass.
- [ ] Step 4: commit `docs: …` / `test: …`.

### Task 7: Ship

- [ ] Product: merge to main, push; deploy to app.sitebin.io per the ops procedure (rollback tag, snapshot into `backups/`, `compose up -d`, keep 3 rollback images, prune build cache).
- [ ] Verify live: log `containers: runtime ready`, the three containers unchanged (512/0.5/256, same StartedAt), the tab renders for the admin.
- [ ] Website: commit `content:` and push.
