# Container usage — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Current memory, CPU, processes and public traffic per container, on the edit page and in the console's Containers tab.

**Architecture:** The runtime samples every running container every 10 s (Docker one-shot stats + Sitebin's own per-project-network interface counters), computes rates in memory, and answers `Manager.Usage(viewID)`; the core exposes it as `GET /api/sites/{editID}/containers/stats`; the edit page polls it; the console renders a snapshot.

**Tech Stack:** Go 1.25, Docker Engine API v1.45 (+ endpoint `ifname`, Engine 28+), vanilla JS.

**Spec:** `docs/superpowers/specs/2026-10-25-container-usage-design.md`

## Global Constraints

- Sample every 10 s; ≤ 4 stats calls at a time, 5 s timeout each; figures older than 60 s are not shown.
- Memory in use = usage − inactive_file (v2) / total_inactive_file (v1).
- Web traffic: Sitebin's counters on its interface in the project network; Sitebin tx = project **in**, rx = project **out**. Given to a service only when exactly one service of the project declares HTTP ports, else `web` on the project.
- Internet traffic: the service's `egress0` interface, fallback `eth1` for a service with egress created before.
- A counter going backwards gives no rate. Nothing about usage is written to disk.
- The site payload and the MCP schema do not change.
- Both suites, `e2e/*.ps1` ASCII.

## Review Focus

- First sample after start/restart: no rates, no crash, `null` in JSON. → Task 2 `TestFirstSampleHasNoRates`.
- Counter reset (container recreated): no negative rate. → Task 2 `TestCounterResetGivesNoRate`.
- A project with two HTTP services: web on the project, not on either service. → Task 2 `TestWebTrafficOfSeveralWebServicesStaysWithTheProject`.
- Docker stats failing for one container: the others still sampled, stale figures expire. → Task 2 `TestSampleSurvivesAFailingContainer`.
- Unauthenticated / wrong-mode stats request. → Task 3 `TestContainerStatsRoute`.

---

### Task 1: Docker client — stats, MACs, egress ifname
**Files:** `ee/containers/docker.go`, `ee/containers/docker_test.go`, `ee/containers/manager.go` (egress connect), `ee/containers/manager_test.go` (fake)
**Produces:** `type Stats struct{ Read time.Time; Memory struct{Usage uint64; Stats map[string]uint64}; CPU struct{ Usage struct{Total uint64} }; Pids struct{Current uint64}; Networks map[string]struct{Rx, Tx uint64} }` (Engine JSON names); `(*Client) ContainerStats(ctx, id) (Stats, error)` → `GET /containers/{id}/stats?stream=false&one-shot=true`; `Inspect.NetworkSettings.Networks map[string]struct{MacAddress string}`; `NetworkConnect(ctx, network, container string, aliases []string, ifname string)`; egress connect passes `"egress0"`.
- [ ] Test `TestContainerStats` (httptest: path, query, decode incl. networks), `TestEgressInterfaceIsNamed` (fake engine records the ifname on the egress connect). RED → implement → GREEN → commit with Task 2.

### Task 2: The sampler
**Files:** `internal/ext/ext.go`, `ee/containers/usage.go`, `ee/containers/usage_test.go`, `ee/containers/manager.go` (Run ticker), fakes
**Produces:** `ext.ContainerUsage{At time.Time; Services []ext.ServiceUsage; Web *ext.Traffic}`, `ext.ServiceUsage{Name string; MemoryBytes, MemoryLimitBytes int64; CPU *float64; CPULimit float64; Pids, PidsLimit int; In, Out *float64}`, `ext.Traffic{In, Out float64}`; `ContainerRuntime.Usage(viewID string) (ext.ContainerUsage, bool)`; `(*Manager) sample(ctx)`; `ifaceReader` interface `Counters() (map[string]ifaceCounters /*by MAC*/, error)` with a `/sys/class/net` implementation.
- [ ] Tests: `TestUsageFromTwoSamples` (memory minus inactive_file, CPU cores, egress rates, web in/out from Sitebin tx/rx given to the single web service, limits from applied), `TestFirstSampleHasNoRates`, `TestCounterResetGivesNoRate`, `TestWebTrafficOfSeveralWebServicesStaysWithTheProject`, `TestSampleSurvivesAFailingContainer`, `TestLegacyEgressInterface` (eth1). RED → GREEN → commit `feat: the container runtime measures each container's memory, CPU and public traffic`.

### Task 3: The stats route
**Files:** `internal/httpapi/containers.go`, `server.go`, `containers_test.go` (fakeRuntime.Usage)
- [ ] Test `TestContainerStatsRoute`: wrong password 401/403 as the logs route, file site 409, no runtime 403, JSON shape with `null` rates and `web`. RED → GREEN → commit `feat: GET /api/sites/{id}/containers/stats answers each container's usage`.

### Task 4: Edit page
**Files:** `web/static/edit.js`, `web/static/app.css` (or edit.html styles)
- [ ] Usage line per service row, project web line, 10 s poll while visible/running; verified in the e2e and by eye. Commit `feat: the edit page shows each container's memory, CPU and traffic`.

### Task 5: Console
**Files:** `ee/admincontainers.go`, `ee/admincontainers_test.go`, `ee/provider.go`/`containers.go` (usage source for tests)
- [ ] Tests: `TestContainersTabShowsUsage` (row line, figures totals with web counted once, "as of"), `TestContainersTabLive` (`?live=1` has the refresh meta, default has none). RED → GREEN → commit `feat: the console's Containers tab shows what every container uses`.

### Task 6: Docs, e2e, ship
- [ ] README, website docs; `containers.ps1`: stats endpoint answers memory/limits, rates after 25 s, the egress service's interface is `egress0`. Full suites, e2e, review, deploy, website push.
