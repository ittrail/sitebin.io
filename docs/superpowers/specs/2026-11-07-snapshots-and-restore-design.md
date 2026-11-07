# Snapshots and restore: a site's earlier files, and the whole instance

Date: 2026-11-07. Status: the operator set the requirements below in chat and
then handed every other call to the builder ("mach nun alles alleine!"); those
calls are marked *builder's call*.

## Why

The hosted instance's backups moved off the VM on 2026-11-07: Borg archives on
a Hetzner Storage Box, hourly for 48 hours and nightly for 30 days (the
retention the privacy policy and the DPA promise). They are the operator's
disaster recovery, but they can do more: almost every "can you get my site
back?" is a bad deploy, a deleted folder or an app that wrote garbage into its
data — one site, a few hours ago. The operator wants owners to undo that
themselves, and wants the disaster case to be a button too.

## The operator's requirements (2026-11-07)

1. **Owners and admins of a site** see the site's snapshots in the UI and can
   restore any one of them, each on its own.
2. **The operator** can restore the **whole instance** to a snapshot.
3. Backups stay as set up: hourly 48 h, nightly 30 days, keys and repository
   never inside the Sitebin container's reach by design.

## Goals

- A **Backups** tab on every site (edit page and workspace alike) listing the
  site's *versions*: the snapshots in which its files differ, newest first,
  with time, kind, file count and size, and the one equal to the current
  files marked. Each version can be **downloaded as a ZIP** or **restored**.
- **Restore = the site's files.** The version's files replace the site's files
  through the same staged replace an upload uses (`store.Replacement`): caps,
  abuse scan, site lock, viewer layout, sliding expiry, provenance — nothing
  new to get wrong. Settings, addresses, sharing, forms, the edit password and
  the Git repository stay as they are (*builder's call*: restoring settings
  would also restore admins and domains, and handing a removed admin back his
  access is not "undo").
- **Every restore can be undone**: before it replaces anything, the agent
  takes a fresh snapshot, so the files it replaces are the newest version in
  the list afterwards (*builder's call*).
- An **Instance** view for the operator in the workspace: backup status, every
  snapshot, and **Restore the instance** to one of them, with a typed
  confirmation. The previous data is kept as a copy for 30 days and can be
  switched back to.

## Non-goals

- Restoring a **deleted** site (its indexes, domains and owner may have moved
  on); the operator's instance restore or a manual `restore-site` later.
- Restoring **settings** or the **Git repository** of a site.
- MCP tools (no tool-catalog change; the catalog is a contract).
- Consistent **MySQL** copies — a separate design (`mysqldump` per service
  before the nightly archive). The tab says a running database's copy may not
  be consistent, as the terms already do.
- A classic-console page: the classic console links to the workspace's
  Instance view (*builder's call*: the workspace is the default interface and
  the operator's).

## Anti-goals

- Backup credentials or the repository **reachable from the Sitebin process**.
  The Sitebin container serves the internet; it may ask, never hold the keys.
- A restore that **leaves a site half-replaced** or a container project
  stopped after a failure.
- **Leaking files across owners**: a site that changed hands must not hand its
  new owner what the previous owner deleted before the transfer.
- A **second restore code path** for the instance: the agent restores through
  the same safety rules `sitebin restore` has (no link out of the data root,
  no write through a link).
- Breaking the **30-day promise**: no copy, index or cache outlives it.

## The pieces

```
 host (VM 113)                                   sitebin container
 ─────────────                                   ─────────────────
 sitebin-borg (timers) ──► Borg repo (Storage Box)
 sitebin agent  ◄──────── unix socket ─────────── core: /api/sites/{id}/backups
   │  borg list/export-tar/extract                 ee: /account/api/admin/backups
   │  docker API (instance restore only)
   └─ /opt/sitebin/data (host path of /data)
```

### 1. The agent: `sitebin agent` (core, MIT)

A subcommand of the same binary, run **on the host** as root
(systemd `sitebin-agent.service`), because that is where the backup keys, the
Docker socket and the data directory's host path are. It serves a small JSON
API over a **unix socket** in a directory bind-mounted into the Sitebin
container (`/run/sitebin-agent/agent.sock`, mode 0660, group = the
container's gid). Nothing else mounts it; customer containers cannot reach it.

**Sources** (`internal/snapshots`), behind one interface:

- `borg` — the repository the environment names (`BORG_REPO`,
  `BORG_PASSCOMMAND`, `BORG_REMOTE_PATH`, from the agent's EnvironmentFile).
  `borg list --json`, `borg list --json-lines ::A sites/`,
  `borg export-tar ::A - <paths>`, `borg extract --stdout ::A <path>`.
- `dir` — a folder of `sitebin backup` `.tar.gz` files (the README's cron
  recipe). For self-hosters without Borg, and for the e2e suite.

Snapshot ids are the archive (file) names and are only ever passed to a
command after matching an id the source listed — never a client's string. A
site is addressed only by a view id matching `^[a-z2-7]{26}$`.

**Kinds** come from the name: `hourly-*`, `nightly-*`, `predeploy-*`, else
`archive`.

**The index.** Per snapshot and per site: file count, bytes, newest mtime and
a **fingerprint** of `sites/<id>/files/` — the XOR of SHA-256 over
`path, size, mtime (seconds)` per regular file, so the order an archive lists
its files in does not matter, and hourly (ns mtimes) and nightly (tar
seconds) archives of the same files agree. Built in the background, newest
snapshot first, cached as `<state>/index/<snapshot>.json`; an index whose
snapshot is gone is deleted with it (the 30-day promise).

**Versions** of a site: the indexed snapshots that hold it, grouped into runs
of equal fingerprints; each run is one version (its newest snapshot is the one
restored; `first`/`last` say when it was seen).

**Backup now.** `POST /v1/backup-now` runs the configured command
(`--backup-now "/usr/local/sbin/sitebin-borg hourly"`) and waits for it,
then indexes the new snapshot. One at a time; the borg script's own flock
serialises it with the timers.

**API** (`/v1`, JSON unless noted):

| Route | |
|---|---|
| `GET /v1/status` | source, snapshot count, indexed count, the last backup runs (from `--status-dir`), the instance restore state |
| `GET /v1/snapshots` | `[{id, at, kind, indexed, sites, bytes}]`, newest first |
| `GET /v1/sites/{view}/versions` | `[{snapshot, at, first, kind, files, bytes, fingerprint}]` |
| `GET /v1/snapshots/{id}/sites/{view}/meta` | the site's `meta.json` in that snapshot (404 if absent) |
| `GET /v1/snapshots/{id}/sites/{view}/files` | a tar of `sites/<view>/files/**` |
| `POST /v1/backup-now` | a fresh snapshot; answers its id |
| `POST /v1/instance/restore` | `{snapshot}` or `{copy}`: starts the instance restore, 202 |
| `GET /v1/instance/restore` | its state |
| `GET /v1/instance/copies` · `DELETE /v1/instance/copies/{name}` | the kept pre-restore copies |

### 2. Restoring a site (core)

`SITEBIN_SNAPSHOTS_AGENT` = the socket's path inside the container; unset,
there is no tab and no route answers anything but 404 (community default).

- `GET /api/sites/{editID}/backups` (`withEditAuth`): `{available, versions,
  current}`. `current` is the live files' fingerprint, computed the same way,
  so the UI marks the version equal to now.
- `GET /api/sites/{editID}/backups/{snapshot}/download`: the version's files
  as a ZIP, paths relative to its content root.
- `POST /api/sites/{editID}/backups/{snapshot}/restore`
  (`recorded(provenance.ActionRestore)`), synchronous, on a context that
  survives the client leaving (the work must end with the project running
  again, not half-way). In order:
  1. **The version is offered** to this site (it is in `versions`, and not
     older than the current owner's tenure: `Meta.OwnerSince`, set by
     transfer and adoption, else the newest `transfer`/`adopt` in the
     provenance log — older sites).
  2. **Its `meta.json`** (agent) decides the snapshot's content root
     (`files/_raw/` in viewer mode, else `files/`) and its container-ness.
     **Container ≠ container now → 409** with what to switch first
     (*builder's call*: restoring an app's data into a static site would
     publish it; a static version into a container site would leave the
     project without its compose file). Static ↔ viewer is fine: the files
     are the user's either way; the current mode lays them out.
  3. **Backup now** (agent). Failing it, no restore (the undo is the promise).
  4. `BeginReplace` (one replace per site; a concurrent upload is 409).
  5. **Stream** the version's tar: every regular file under the snapshot's
     content root is `SaveFile`d at its path relative to it; Sitebin's own
     markers (`.sitebin-spa`, `.sitebin-trusted`), links, devices and
     anything outside the root are skipped (the live site's markers stay, as
     on every replace).
  6. **Container site**: `Enabled` remembered, set false, `Stop` (returns
     when the containers are gone) — only now, right before the commit, so a
     failed download never stopped anything.
  7. `Commit` → `syncViewerLayout`.
  8. **Container site that was enabled**: `Enabled` true, `RestartSeq++`,
     `Kick` — also when the commit failed (deferred), after a plan check
     (`containerModeAllowed`; a plan that no longer allows containers leaves
     it stopped and says so).
  9. Provenance: `restore`, detail = the version's time (UTC) and kind.
- An admin may restore (operator requirement); like every write it goes
  through `withEditAuth` and the lock checks.

### 3. Restoring the instance (agent + ee)

`POST /account/admin/backups/restore` (operator only: `adminAccount`, a
same-origin POST, and the typed word `restore`) asks the agent; the agent then:

1. **Checks** the snapshot exists and the data volume has room for it
   (the snapshot's size from the index + 1 GiB).
2. **Extracts** it next to the data directory (`<data>.restore-<ts>`, same
   filesystem) through the core's restore code — refactored to read a plain
   tar or a gzip one and to **skip** a link out of the root instead of
   refusing the whole archive (an hourly archive can hold one a container
   made; skipping it is what `sitebin backup` does when writing). Files get
   the data directory's owner (uid/gid of the live root).
3. **Holds the backup lock** (`--backup-lock`, the borg script's flock file)
   from here to the end, so no backup snapshots a half-swapped tree.
4. **Stops** the Sitebin container (`--container`, Docker API, 30 s) and
   **removes** every container labelled `io.sitebin.managed=true`: their bind
   mounts point into the old tree, and the runtime recreates a project whose
   containers are gone.
5. **Swaps**: `<data>` → `<data>.before-restore-<ts>`, the extracted tree →
   `<data>`. Two renames on one filesystem.
6. **Starts** the container and waits up to 3 minutes for Docker's health
   status `healthy`.
7. **Unhealthy → switches back** (stop, `<data>` → `<data>.failed-restore-<ts>`,
   the copy → `<data>`, start) and reports `rolled-back`.

The state (`preparing`, `extracting`, `stopping`, `swapping`, `starting`,
`done`, `failed`, `rolled-back`) is kept in `<state>/restore.json`, so the
console reads it after the restart. **Copies** (`before-restore-*`,
`failed-restore-*`) are listed, can be **switched back to** (steps 3–7 with the
copy instead of an extraction) and deleted, and are deleted by the agent when
older than 30 days.

The workspace's **Instance → Backups** view (`#/backups`, operator only):
status lines (last hourly / nightly / prune / check), the snapshots table
(time, kind, sites, size), *Restore the instance…* per row, the restore's
progress and result, and the copies. While Sitebin restarts the page polls
`/account/status` and then shows the agent's result.

## Configuration

| Where | What |
|---|---|
| Sitebin | `SITEBIN_SNAPSHOTS_AGENT=/run/sitebin-agent/agent.sock` |
| compose | `- /run/sitebin-agent:/run/sitebin-agent` |
| agent | `--socket`, `--socket-gid`, `--data`, `--state`, `--source borg\|dir`, `--dir`, `--backup-now`, `--backup-lock`, `--status-dir`, `--container`, `--docker` |

## Tests

- `internal/snapshots`: dir source (list, tar filter, read file, walk),
  fingerprint order-independence and seconds truncation, versions grouping,
  index cache deletion, borg source against a fake `borg` script on PATH
  (argv and parsing), the agent API's id validation.
- `cmd/sitebin`: restore of a plain tar; a link out of the root skipped.
- `internal/httpapi`: the three routes against a fake agent — versions,
  ownership cut-off, container-ness 409, markers skipped, viewer ↔ static,
  backup-now failure refuses, container stop/start (fake runtime), a failed
  commit restarts the project, provenance entry.
- agent instance restore against a fake Docker API: order of calls, swap,
  rollback on unhealthy, copies.
- e2e `e2e/snapshots.ps1` (enterprise image + agent in a second container
  with the dir source): versions appear, download, restore, container-ness
  409, the instance restore and switching back.
