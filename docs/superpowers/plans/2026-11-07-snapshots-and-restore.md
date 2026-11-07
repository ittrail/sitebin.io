# Snapshots and restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Owners and admins restore a site's files from a backup snapshot; the operator restores the whole instance — through a host-side agent that alone holds the backup keys.

**Architecture:** `sitebin agent` (core subcommand, run on the host) serves a JSON API on a unix socket bind-mounted into the container; it reads Borg (or a folder of `sitebin backup` tarballs), indexes per-site fingerprints, takes a snapshot on request and performs the instance restore with the Docker API. The core asks it over the socket (`SITEBIN_SNAPSHOTS_AGENT`) and restores a site through `store.Replacement`; the ee workspace adds the operator's view.

**Tech Stack:** Go 1.x stdlib (net/http over unix sockets, archive/tar, archive/zip), Borg 1.2 CLI, Docker Engine API, vanilla JS (edit.js tab registry, workspace app.js).

**Spec:** `docs/superpowers/specs/2026-11-07-snapshots-and-restore-design.md`

## Global Constraints

- Snapshot ids reach a command only after matching an id the source listed; view ids match `^[a-z2-7]{26}$`.
- Fingerprint = XOR of SHA-256(`path \x00 size \x00 mtime-unix-seconds`) over regular files under `sites/<id>/files/`.
- Kinds by name prefix: `hourly-`, `nightly-`, `predeploy-`, else `archive`.
- Copies and indexes never outlive 30 days.
- Sitebin's markers `.sitebin-spa`, `.sitebin-trusted` are never restored into a site.
- `e2e/*.ps1` pure ASCII.
- Community build stays whole: with `SITEBIN_SNAPSHOTS_AGENT` unset nothing changes.

## Review Focus

- A site restore whose stream breaks half-way: the site keeps its old files and a container project runs again.
- A version from before the site's transfer: refused (409) and not listed.
- A snapshot taken in Container mode restored into a Static site: 409, nothing written.
- An hourly archive holding a link a container made out of the data root: the instance restore skips it instead of failing.
- The instance restore when the restored instance does not become healthy: the previous data is back and serving.

---

### Task 1: Restore reads plain tar, skips links out of the root, needs only the data dir

**Files:** Modify `cmd/sitebin/backup.go`, `cmd/sitebin/main.go`; Test `cmd/sitebin/backup_test.go`

**Interfaces:**
- Produces: `restoreFrom(root string, r io.Reader, o restoreOpts) (int, error)`; `type restoreOpts struct { Owner *[2]int }` (uid, gid to chown every entry to; nil = leave). `restoreData(root, inPath)` keeps its signature and calls it.

- [ ] Tests: `TestRestoreReadsAPlainTar` (a tar without gzip restores the same entries as the gzip one), `TestRestoreSkipsALinkOutOfTheRoot` (archive with link `sites/x/files/out -> /etc` and a file `sites/x/files/ok.txt`: no error, ok.txt restored, no `out`), existing `TestBackupRestoreRoundTrip` still passes.
- [ ] Implement: sniff the first two bytes (`0x1f 0x8b` → gzip) through a `bufio.Reader`; a symlink failing `linkStaysUnder` is reported on stderr and skipped; `parentStaysUnder` still refuses writes through links. `restore` CLI reads `SITEBIN_DATA_DIR` (default `/data`) instead of `mustConfig()`.
- [ ] `go test ./cmd/sitebin/` passes; commit `fix: sitebin restore reads plain tar and skips a link out of the data root`.

### Task 2: `internal/snapshots` — sources, fingerprints, index, versions

**Files:** Create `internal/snapshots/{snapshots.go,dir.go,borg.go,index.go}` + tests.

**Interfaces:**
- `type Snapshot struct{ ID string; At time.Time; Kind string }`
- `type Item struct{ Path string; Size int64; Mtime time.Time; Regular bool }`
- `type Source interface { List(ctx) ([]Snapshot, error); Walk(ctx, id, prefix string, fn func(Item) error) error; Tar(ctx, id string, paths []string, w io.Writer) error; ReadFile(ctx, id, path string, max int64) ([]byte, error) }` — `ErrNotFound` for a missing file/snapshot.
- `func KindOf(name string) string`; `func ValidViewID(s string) bool`
- `type SiteSummary struct{ Files int; Bytes int64; Newest time.Time; Fingerprint string }`; `type Fingerprinter` (`Add(path string, size int64, mtime time.Time)`, `Sum() string`).
- `func Summarize(ctx, src, id) (map[string]SiteSummary, error)` (walks `sites/`).
- `type Index struct{ Dir string }` with `Get(id) (map[string]SiteSummary, bool)`, `Put(id, m)`, `Prune(keep []string)`.
- `type Version struct{ Snapshot string; At, First time.Time; Kind string; Files int; Bytes int64; Fingerprint string }`; `func Versions(snaps []Snapshot, idx func(id string) (map[string]SiteSummary, bool), view string) []Version` (newest first, consecutive equal fingerprints merged, newest snapshot kept).
- `NewDirSource(dir string)`, `NewBorgSource(env []string, bin string)`.

- [ ] Tests: dir source list/kind/at, `Tar` filter by prefix, `ReadFile`, `Walk`; fingerprint order-independent and ns→s truncation; `Versions` grouping + ordering; `Index.Prune`; borg source with a fake `borg` script (`BORG` bin path) asserting argv and parsing `--json`/`--json-lines`.
- [ ] Implement; `go test ./internal/snapshots/`; commit `feat: snapshot sources, per-site fingerprints and versions`.

### Task 3: The agent server

**Files:** Create `internal/snapshots/{agent.go,docker.go,instance.go}` + tests; `cmd/sitebin/agent.go` (flags → `snapshots.AgentConfig`, socket listen/chmod/chown, `Extract` = `restoreFrom`).

**Interfaces:**
- `type AgentConfig struct{ Source Source; StateDir, DataDir, BackupNow, BackupLock, StatusDir, Container string; Docker DockerAPI; Extract func(dst string, r io.Reader) error; Now func() time.Time; Healthy time.Duration }`
- `func NewAgent(cfg AgentConfig) *Agent`; `(*Agent).Handler() http.Handler`; `(*Agent).Run(ctx)` (index worker every minute + copy pruning).
- `type DockerAPI interface{ Stop(ctx, name string) error; Start(ctx, name string) error; Health(ctx, name string) (string, error); RemoveManaged(ctx) (int, error) }`; `NewDockerClient(sock string) DockerAPI`.
- Restore state JSON: `{"state","snapshot","copy","started","finished","message","previous"}`.

- [ ] Tests (httptest + fakes): `/v1/snapshots`, `/v1/sites/{bad}/versions` 400, unknown snapshot 404, `/meta`, `/files` tar; backup-now runs the command and returns the newest id; instance restore call order (extract → lock → stop → removeManaged → swap → start → healthy → done), rollback on unhealthy, restore from a copy, copies list/delete, prune of copies > 30 days.
- [ ] Implement; `go test ./internal/snapshots/ ./cmd/sitebin/`; commit `feat: sitebin agent serves snapshots and restores the instance`.

### Task 4: Store and provenance

**Files:** Modify `internal/store/meta.go` (`OwnerSince *time.Time json:"owner_since,omitempty"`), `internal/store/ownership.go` (set in `TransferOwner`, `AdoptOwner`), `internal/provenance/provenance.go` (`ActionRestore = "restore"`), Create `internal/store/fingerprint.go` (`func (s *Store) ContentFingerprint(site *Site) (string, error)` — same algorithm, paths `sites/<id>/files/...`).
- [ ] Tests: transfer and adopt stamp `OwnerSince`; `ContentFingerprint` equals `snapshots` fingerprint of a `sitebin backup` of the same site.
- [ ] Commit `feat: a site records when its owner got it`.

### Task 5: Core API

**Files:** Create `internal/snapshots/client.go` (`NewClient(sock)`: `Versions`, `Meta`, `Files`, `BackupNow`, plus instance calls for ee), `internal/httpapi/backups.go` + test; Modify `internal/config/config.go` (`SnapshotsAgent string` ← `SITEBIN_SNAPSHOTS_AGENT`), `internal/httpapi/server.go` (3 routes), payload `backups_available`.
- [ ] Tests (fake agent over httptest on a unix socket or injected client): list marks `current`; ownership cut-off hides and refuses; container-ness 409; restore replaces files, skips markers, viewer↔static layout; backup-now failure refuses with 503; container site stopped then kicked (fake runtime), also after a failed commit; provenance `restore`; download ZIP entries.
- [ ] Commit `feat: owners restore a site's files from a snapshot`.

### Task 6: The Backups tab

**Files:** Create `web/static/edit-backups.js`; Modify `web/static/edit.html`, `ee/ui/site.html` (panel + script), `web/static/edit.js` (register when `site.backups_available`), `ee/ui/site.js` (tab in `tabsList`, `go()`, activity sentence `restore` → "Restored the files from …"), CSS in `web/static/app.css` / `ee/ui/app.css` as needed.
- [ ] Manual check in the local stack with the dir agent; commit `feat: a Backups tab lists a site's versions and restores one`.

### Task 7: Operator view

**Files:** Modify `ee/workspace_api.go` (`GET /account/api/admin/backups`), `ee/dashboard.go` routes (`POST /account/admin/backups/restore`, `POST /account/admin/backups/copies/{name}/delete`), Create `ee/adminbackups.go` + test; Modify `ee/ui/app.js` (view `backups`, nav entry, polling during restart), classic console nav link.
- [ ] Tests: non-admin 404; typed word required; agent errors surface. Commit `feat: the operator restores the instance from a snapshot`.

### Task 8: e2e, docs, deploy

- [ ] `e2e/snapshots.ps1`; README (agent, env, restore rules); website `/docs/backups/`.
- [ ] Deploy per ship order: product, agent unit on VM 113, compose mount + env, live check, website.
