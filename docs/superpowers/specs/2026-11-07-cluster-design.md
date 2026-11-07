# Cluster mode: one writer, synced nodes, a hot spare

Date: 2026-11-07. Status: design, not built. The operator set the
requirements below in chat (in German; quoted where the wording matters);
every other decision is the builder's and is marked *builder's call*.
Enterprise (`ee/`, ELv2).

## Why

Today one instance owns `/data`. An update, a broken VM or a dead host takes
every site offline, and the only standby the README offers is a restore from
backup or block-level replication outside Sitebin ("Availability &
failover"). The operator wants a **hot spare**: a second node, always in
sync, that can take over.

The first use is planned work. Updating the primary becomes: switch the
writer to the spare, update the primary, let it catch up, switch back, with
"max. ein paar Minuten Unterbrechung". The second use is the day the primary
dies.

The operator's first spare must serve visitors **only when the operator
says so** ("der soll wirklich nur dann ausliefern wenn ich es ausdrücklich
will").

Later the operator wants nodes in several countries, closer to visitors
("näher on the edge"), and a failover that loses nothing. Neither is built
here, but this design must not make either harder.

## The operator's requirements (2026-11-07)

1. Cluster mode is an **Enterprise** feature. **One licence covers every
   node of ONE cluster.**
2. **Exactly one writer** at a time, **chosen by the operator by hand**.
   There is no automatic writer failover.
3. **Every node may become the writer**, but **only a node that is current**
   (fully synced) may be made the writer.
4. **Nodes may join at any time** and **sync themselves**.
5. **Every node is always kept in sync**, whether it serves or not.
6. **Serving is the operator's switch, per node.** A node switched on
   serves by itself while it is current and healthy, and drops out by itself
   when it is not. A node switched off never serves, not even when every
   other node is down, until the operator switches it on.
7. **Container sites run, and are served, only on the writer.** Other nodes
   pass their requests on. Apps need not know they run in a cluster.
8. An unplanned writer failure **may lose a few seconds** of changes for
   now. Zero loss is a later requirement, and the design must leave a path
   to it.
9. A **planned switch interrupts for a few minutes at most**.
10. **Later: nodes in several countries**, near visitors. Nothing here may
    make that unnecessarily complicated.
11. **The licence is bound to one cluster technically, built from the
    start** ("ja sollte schon, und ich würde ihn gleich von anfang an
    mitbauen"). The hot spare may wait for it (§9, §15).

## Goals

- A node runs as the **writer** or a **follower** of one cluster. A single
  instance without cluster mode behaves exactly as today, except that its
  licence binds to it (§9).
- Followers hold a complete copy of the data root (minus per-node state,
  §3) and serve file sites from it. **Nothing on a follower's serving path
  waits for the writer.**
- **Planned switchover without data loss.** **Emergency promotion** states
  the possible loss before the operator confirms it.
- The console shows every node: its role, its serving switch, whether it is
  in rotation, whether it is current or how far behind it is (in changes and
  seconds), its last contact, its version and its database replicas.
- Joining takes a one-time token; nodes can be removed.

## Non-goals (this design)

- Automatic writer election or failover: no quorum, no consensus protocol.
- Running a container project on more than one node at a time.
- Synchronous replication, where a write waits for a follower's
  confirmation. The path to it is kept open (§12).
- More than one writer, for example regional writes.
- A load balancer or GeoDNS service. Sitebin publishes each node's health,
  and the distributor in front is the operator's (§10).
- Several nodes sharing one data directory (NFS, a shared volume). Every
  node has its own.
- Windows as a cluster host. Cluster mode runs on Linux; Windows dev hosts
  run the unit tests only (*builder's call*: index links are junctions there).
- Where nodes stand. That is the operator's business, including what a DPA
  promises about hosting ("ein betriebsthema und nicht für die funktion
  relevant").

## Anti-goals (a failure even if it works)

- Two nodes acting as writer at the same time, or one project's containers
  running on two nodes.
- **Anything the writer protects being unprotected on a follower**, even for
  the length of an apply: a held file (the abuse guard locks before the
  rename), a locked or expired site, a view password, an address switched
  off, a removed domain. A follower may briefly be *stricter* than the
  writer, never looser. (Builder check, 2026-11-07: an atomic switch of
  whole site states is not required.)
- A node that has not caught up serving visitors (stale sites, or a 404 for
  new ones), or being offered for promotion as current.
- Any request on a follower's serving path waiting for the writer.
- An IP address or user agent leaving the node that received the request,
  through analytics or view counts. A salt living longer than two days past
  its date on any node, or reaching any backup.
- The community build changing, or a single instance behaving differently
  (apart from the licence binding, §9).
- Backup keys or the Docker socket reaching further than today.

## Terms

- **Cluster:** nodes that share one cluster id, which is random and created
  when the first node turns cluster mode on.
- **Writer:** the one node that accepts writes, runs the background jobs and
  runs the containers.
- **Follower:** every other node.
- **Epoch:** a counter that grows by one at each promotion. A node is writer
  *of an epoch*.
- **Position:** (epoch, sequence). The sequence numbers the writer's change
  records and strictly grows within an epoch.
- **Serving-current** and **promotable:** two different tests, defined in
  §4.
- **In rotation:** the node's serving switch is on, it is serving-current
  and healthy, and its last contact with the writer is within the staleness
  limit (§4).

## 1. Cluster state, identity, transport

- `<data>/cluster/` holds three things:
  - `cluster.json`: cluster id, epoch, writer node id, and the node list.
    Each node has an id, a name, its cluster address, its serving switch,
    its certificate fingerprint, when it joined and the version it last
    reported.
  - The **cluster CA** (key and certificate). *Builder's call:* it is
    replicated, because every node may become the writer, and the writer
    issues node certificates when a node joins.
  - `node/`, the per-node state, which is never replicated: node id, node
    key and certificate, last applied position, apply staging.
- Only the writer writes `cluster.json`. The one exception is a promotion,
  which the new writer writes with epoch + 1.
- Nodes talk to each other over **HTTPS with mutual TLS** against the
  cluster CA, on a dedicated cluster listener
  (`SITEBIN_CLUSTER_LISTEN`, *builder's call*: default `:7443`). The public
  listeners are never used for this. A certificate names its node id.
  `cluster.json` lists the allowed fingerprints; anything else is refused,
  which is how a removed node is shut out.
- **The follower opens every connection** (*builder's call*): replication
  is a pull, and forwarding is a request to the writer. A node behind NAT
  can follow and serve. A node that may become the writer must be reachable
  at its `SITEBIN_CLUSTER_ADDRESS` from every other node. The console marks
  a node that another node cannot reach, and such a node cannot be promoted.
- Every node polls the cluster state of every known node (*builder's call*:
  every 10 s). A node that sees a higher epoch adopts it.

## 2. What a follower serves itself, and what it forwards

- **The base domain belongs to the writer.** A follower forwards every
  request on the base domain to the writer unchanged: UI, API, MCP, WebDAV,
  git, `/account/*`, billing webhooks, the stack's GDPR and suspend orders,
  and `/v/<id>` path views. *Builder's call:* the dashboard is not
  latency-critical, and this takes every per-process registry out of the
  cluster problem. Upload tokens, WebDAV locks, the git push registry,
  limiters, the captcha spend set, OAuth state and the consent cache stay on
  the writer only, as today.
- **Content domains are served by the follower itself.** That covers the
  view domain, `*.<view>`, custom domains (with paths), the view apex with
  `www`, redirects, not-found pages, the view-password gate (a stateless
  HMAC cookie) and the security headers. authz runs on local data, and
  `file_server` serves the local copy.
- **Container sites:** on a follower, authz answers with the writer as the
  upstream. The writer routes the request to the container as today; the
  container's name resolves only through the writer's Docker DNS.
- **`/_sitebin/*` on content domains:**
  - Served locally: assets, `embed.js`, `altcha.js`, `analytics.js` and the
    not-found page.
  - Forwarded to the writer: `analytics/send`, `csp-report`, reports and the
    report page, every forms route, and the view-password unlock (its
    attempt limit must count across the cluster).
  - *Builder's call:* one table in code decides "local" or "forward", and a
    test lists every route so that a new route has to be classified.
- **Forwarding behaves like a direct request.** On the writer, a forwarded
  request takes the same pipeline as one that arrived there directly:
  authz, security headers, container routing. So the answer never depends
  on which node received the request. A follower also never restarts or
  reloads its Caddy when the writer changes, because serving must not blip
  (Caddy runs with `admin off`). *Builder's suggestion:* Caddy on a follower
  proxies the forwarded classes to a local forwarder in the backend, whose
  target follows the current writer. The writer's cluster listener feeds
  them into its Caddy through a loopback listener.
- **The visitor's address survives forwarding.** Today `auth.ClientIP` takes
  the last `X-Forwarded-For` entry, Caddy's own. A forwarded request would
  name the follower instead. The writer therefore takes the client address
  from a dedicated header (*builder's call*: `X-Sitebin-Client-IP`), and
  only on a request from an authenticated cluster peer. Every public request
  has the header stripped. This keeps provenance, rate limits and abuse
  records naming the visitor.
- **Follower mode refuses every local write.** The store gains a follower
  mode in which every write funnel and atomic writer returns `ErrFollower`.
  This includes the CLI's writing commands (`delete`, `lock`/`unlock`,
  `hold`, `scan --lock`, `cleanup`, `restore`), which answer "not the
  writer". Only the replication applier writes (§3).
  - The writes the census found on read paths must become refusals that do
    not break serving: `openContent` creating a folder, summary caches,
    salt creation, `git init` on a read, the licence trial marker,
    `.secret`. A test serves every content route on a follower with a
    read-only data root.
  - `SITEBIN_READONLY` is not this mode and stays as it is.
- **The serving switch is enforced by the node itself**, not just reported
  to the distributor. A node switched off answers every public request
  (content, base domain, FTP) with `503`, even when traffic reaches it by
  mistake, such as a DNS entry pointing at it or a distributor without
  health checks. It answers only its cluster peers. A writer switched off
  therefore handles only what serving followers forward to it.
- **The writer unreachable:** content keeps serving from the local copy for
  the staleness limit (§4), then the node leaves rotation. Forwarded
  requests answer `503` with `Retry-After` and a plain page. Container
  sites answer `503`.

## 3. How changes travel

There is no single place every change passes through. The census (2026-11-07)
found about twenty writers of their own besides the four content funnels,
plus the CLI (its own `Store`, outside the server's locks), git, Caddy and
the container processes. So replication rests on two parts:

- a **change journal**, for speed;
- a **reconciliation**, for correctness, which finds and repairs whatever
  the journal missed.

A follower that applied the journal and passed a reconciliation holds what
the writer holds.

### Units

Changes are tracked per **unit**, not per file (*builder's call*):

- a site: `sites/<id>/` with `meta.json`, `files/`, `stats.json`,
  provenance, analytics and `git/`;
- an account: `accounts/<id>/`;
- each index directory: `edit-index`, `domain-index`, `path-index`,
  `address-index`, `account-index/*`;
- `zones/`;
- `ownership/`;
- `reports/`;
- `billing-events/`;
- `analytics/salts/`;
- `license/`;
- `containers/limits.json`;
- `.secret`;
- `abuse-rules.json`;
- `backup-last.json`;
- the cluster CA and `cluster.json`;
- Caddy's storage, per key (§6).

**Never replicated (per node):** `cluster/node/`, the generated
`caddy/Caddyfile`, `caddy-home/`, `gitstate/`, `tmp/` and every staging or
temp file (`*.tmp`, `.sbtmp*`, `.replace-commit-*`). From milestone 2 on,
also the datadirs of replicated databases on followers (§5).

The builder completes this list from the census and keeps it complete. A
test walks a fully featured data root and fails on any path that no unit or
exclusion names.

### The journal

- The writer appends a record `(epoch, sequence, unit, time)` once a change
  is durable. A record says *which unit* changed, never the bytes.
- **The hook is the unit's lock** (*builder's call*): releasing a site lock,
  stats lock, account lock, `zones.mu`, the ownership mutex or `Salts.mu`
  after a write marks its unit dirty. That covers most writers without
  touching them one by one.
- Writers outside a lock call the hook themselves: index link and unlink,
  markers, `Chtimes`, the viewer layout, licence, limits, reports, billing
  events, the end of a git push or gc, and Caddy storage writes through the
  storage module.
- Running container sites are marked dirty on a timer, because the apps'
  own writes are invisible to Sitebin. *Builder's call:* their data folders
  (the compose file's volumes) every 15 minutes, the rest of the site as
  usual. Every 10 s would keep a database's files permanently in flight
  over a VPN. A planned switchover syncs them in full after the project
  stops (§4), so the interval only matters for an emergency promotion.
- **The CLI** builds its own `Store`. Its writes reach the journal through
  an inbox (`<data>/cluster/inbox/`), which the server drains
  (*builder's call*). Reconciliation catches whatever is missed.
- **Retention** (*builder's call*): 7 days or 1 GiB of records. A follower
  that fell out of the window, or that comes from an older epoch, reconciles
  fully. That still moves only the files that differ, so a node back from an
  update catches up incrementally.

### Applying a unit on a follower

1. The follower asks the writer for the unit's **manifest** in its current
   state: path, type, size, mtime, mode, symlink target, and a content hash
   when asked for. The writer reads it under the unit's lock.
2. The follower fetches only the entries that differ. Small files travel
   whole. Large files (git packs, container data, especially database
   files) travel as **block deltas**: these are required, because whole
   files would be too much to move over a VPN line.
3. It stages them beside the target.
4. It moves them into place in three steps, so that a follower is never
   looser than the writer:
   1. **First, the strictest of the old and the new `meta.json`**
      (`strictest(old, new)`). Per field it keeps whichever protects more:
      the lock, a view password, an earlier expiry, an address switched
      off, the smaller domain list.
   2. **Then the files.**
   3. **Last, the new `meta.json`.**

   If nothing gets stricter, step 1 is the old meta and changes nothing.
   This generalises the abuse guard's rule on the writer (lock before
   rename), so a follower never serves a held file, never serves private
   files before their view password, and never serves old private files
   after the password is gone.
5. Deletions come last.
6. A unit is applied whole or not at all. A crash during the apply is
   resumed from the staging area on the next start.

**Indexes and sites are separate units**, so they can arrive in either
order. authz (and `ByDomain` with the other index lookups) therefore
serves an address only if the target site's `meta.json` names that address
too. Today an index link alone is enough. This check is cheap, and it also
holds on a single instance.

Two more rules:

- **mtimes are replicated exactly.** Several carry meaning: the account and
  share markers (the 10-minute grace), the git push and gc markers, the
  analytics day files, restored file times and the billing events.
  Index links are replicated as links with their target and are never
  followed.
- **A follower applies only the epoch it follows.** It refuses a record or
  a state from a lower epoch.

### Reconciliation

- The follower compares each unit's manifest of metadata (path, size,
  mtime, mode, link target) with the writer's, and applies every unit that
  differs. Content hashes are fetched only where the metadata differs.
- It runs after a join, after a long absence, after an instance restore,
  and on a schedule (*builder's call*: every 10 minutes). Because mtimes are
  replicated, an unchanged file compares equal without being read.
- The snapshots feature's per-site fingerprints (`liveFingerprint`) are the
  model.

### Versions

- The protocol carries a version. Each release also states a
  **serving-format version**, which is bumped whenever a stored field
  changes what authz or Caddy serve.
- A follower whose serving-format version is older than the writer's keeps
  syncing but **leaves rotation** until it is updated. It never serves what
  it does not understand.
- **Promotion requires a version ≥ the writer's.** Data written by a newer
  version is never handed to an older writer, which could rewrite
  `meta.json` without the fields it does not know.
- That fits the update flow:
  1. Switch the writer to the spare (same version).
  2. Update the primary, which follows as a newer follower. Newer reads
     older thanks to the `omitempty` rule.
  3. Switch back to the primary (newer ≥ older).
  4. The spare follows the newer writer, may leave rotation, and becomes
     promotable once it is updated.

## 4. Current, switchover, promotion

### Two tests: serving-current and promotable

One test for both would fail in ordinary operation: view counts and
analytics change with every page view, and a running database is never
"finished". So there are two (builder check, 2026-11-07, *builder's call*):

**Serving-current** (for rotation) covers only what serving reads:
`meta.json`, the site files, the indexes, zones, certificates, `.secret`
and the analytics salts. All records of these units up to the writer's
latest must be applied, within a lag limit (*builder's call*: 5 s). The
serving-format version must also fit (§3).

**Promotable** (for "make writer") means everything the operator would
lose on a switch is present:

- every unit is applied up to the writer's latest position, with two
  exceptions:
  - view counts and analytics may lag up to 5 minutes;
  - the data folders of running container projects follow their
    15-minute interval. A planned switchover syncs them after the stop.
- the last reconciliation found no difference, or the difference has been
  applied;
- the version allows it (§3);
- the writer-relevant configuration matches (§9);
- from milestone 2 on, the database replicas are running and have caught
  up (§5).

That is how requirement 3 ("fully synced") reads in practice. A missing
image of an enabled project is a warning, not a block (*builder's call*).

**The staleness limit** (`SITEBIN_CLUSTER_STALE_AFTER`, *builder's call*:
1 h). A follower that cannot reach the writer keeps serving its copy for
this long, then leaves rotation. It cannot tell a dead writer from a
broken link, and in that time it learns of no new lock, deletion or
expiry. A site locked for abuse keeps running on that node for at most this
long. The limit is short enough for that risk, and long enough to cover a
VPN outage or the operator's time to react.

**With the writer unreachable, "promotable" cannot be known.** For each
follower the console shows its applied position, the last contact with the
writer, and its lag at that moment. It offers the node with the highest
position and says that changes after that position may be lost.

### Planned switchover (the writer alive, the target promotable)

One operator action:

1. **The writer starts handing over.** New writes, both local and
   forwarded, wait (*builder's call*: up to 15 s, then `503` with
   `Retry-After`). Background jobs and the container manager pause.
   Container projects stop:
   - milestone 1: then their files sync;
   - milestone 2: first the database replicas confirm the primary's last
     transaction (GTID).
2. The writer writes its last record: "handover to T at sequence N".
3. T applies everything up to N and confirms. The old writer switches its
   store into follower mode **before** it accepts the confirmation. From
   then on it can no longer write.
4. T writes epoch + 1 with itself as the writer. It starts the background
   jobs, has the container manager start every enabled project at once
   (bumping `restart_seq` as `startAfterRestore` does, so this does not wait
   for the 60 s scan), and, from milestone 2 on, promotes the replicas.
5. Writes that were waiting are passed on to T.

If any step before 4 fails, the writer stops handing over and carries on as
the writer, with nothing changed.

The target: writes pause for at most 30 s plus the containers' stop and
start. File sites are never interrupted. The serving switches are separate,
and the console offers "make writer and serve" as one action.

### Emergency promotion (the writer unreachable)

- Only on the target node itself, through `sitebin cluster promote`, with a
  confirmation that names the position and the possible loss.
- The operator also confirms that **the old writer is down or cut off from
  the distributor**. Without a quorum, Sitebin cannot fence it. That is the
  price of manual promotion, and the console says so.
- The target writes epoch + 1 and starts as the writer.

### The old writer coming back

- **A node that was the writer starts in follower mode:** the store refuses
  writes, containers are not started and background jobs are off. It asks
  every known node for its cluster state.
- **If any node knows a higher epoch**, it follows that epoch's writer and
  removes its own managed containers. Whatever it wrote after the last
  position the new writer had applied is **set aside**, never merged
  (*builder's call*):
  - it goes to `<data>/cluster/diverged/<epoch>-<time>/` as unit states;
  - it is listed in the console;
  - it is deleted after 30 days, per the privacy policy's limit.
- **If every node it reaches confirms that it is still the writer** of the
  current epoch, it resumes.
- **If it reaches no node**, it stays a follower. The console and
  `sitebin cluster resume` offer "resume as writer", never automatically.
  A cluster of one resumes at once, as today.
- **The container manager runs only on the writer of the current epoch.**
  It checks the epoch when it starts and before every apply. A writer that
  learns of a higher epoch stops its containers at once.

## 5. Containers and their databases

### Where containers run

- Only on the writer of the current epoch. On a follower the container
  manager does nothing; in particular, its full scan tears nothing down.
- A demoted writer removes its managed containers.
- **Followers pull ahead** the images (pinned by digest) of every enabled
  project, so a switchover does not wait for a pull of up to 10 minutes.
- The container user is uid:gid 1000 on every node. It is the same image,
  but the join checks it.

### Milestone 1: container data travels as files

- A project's data, including a MySQL datadir under `files/db`, replicates
  like any other site file, as block deltas. While the project runs, its
  data folders are marked dirty every 15 minutes (§3).
- **A planned switchover is consistent**: the project stops before the final
  sync, so the new writer starts on a clean copy.
- **An emergency promotion starts on a hot copy.** For a database that is no
  better than the hot copies in today's backups. The promotion confirmation
  names every container site with a database, and the console offers each a
  site restore from a backup.
- SQLite and other files an app writes itself follow the same rules.

### Milestone 2: database replicas

For each enabled project with a `mysql-8.4` service whose datadir is
mounted, **every follower runs a replica**:

- **The container:** `sb-<id>-<svc>-replica`, with its own label
  `io.sitebin.replica`, so that applying or tearing down the project never
  touches it. It is read-only, with the same memory and CPU limits as the
  primary. Its datadir is `<data>/cluster/replicas/<id>/<svc>/`: not
  replicated, not backed up, and not counted against the customer's cap.
  It is the operator's redundancy, and every database costs memory on every
  node.
- **The stream:** asynchronous GTID replication. The replica connects to a
  relay of the follower's Sitebin. The relay carries the stream over the
  cluster link to the writer's Sitebin, which connects to
  `sb-<id>-<svc>:3306` on the project network it already joins. No port is
  published, and the egress firewall stays untouched.
- **Settings the customer cannot remove:** a unique `server_id` per node,
  `gtid_mode=ON`, `enforce_gtid_consistency=ON` and the binary log. Today a
  customer's `command:` replaces the catalog's flags, and a mount over
  `/etc/mysql/conf.d` is allowed. *Builder's call:* in cluster mode Sitebin
  mounts a read-only config file at a path the image reads last, and the
  compose check refuses customer mounts over `/etc/mysql/conf.d` and
  `/etc/my.cnf` with a message that says why. The builder verifies the
  include path against `mysql:8.4.11`.
- **The replication user:** Sitebin creates it on the primary with the root
  password from the compose environment (`MYSQL_ROOT_PASSWORD`), at the
  first start and whenever it is missing. A replica becomes **broken** when
  there is no usable root password, or the customer removed the user or
  changed the password. A broken or new replica is seeded again through the
  clone plugin (*builder's call*). If that fails, the console shows the
  project as "no replica".
- **Promotion:** a node is promotable only while every replica runs and has
  caught up. Otherwise the operator confirms the listed projects that would
  start from a hot copy. On promotion, the replica stops replicating, its
  datadir moves into the site's `files/db` on the new writer, and the
  project starts on it. The demoted node seeds new replicas.
- **Restores and hand edits:** a site restore (or an edit through the
  editor, WebDAV or FTP) that writes into a replicated datadir seeds every
  replica of that project again.
- From milestone 2 on, followers exclude replicated datadirs from file
  replication.

## 6. Certificates

- **Caddy's storage becomes cluster storage** (*builder's call*). Caddy is
  already built with `xcaddy`, which gives room for a storage module
  `sitebin`:
  - Reads come from the local copy of `<data>/caddy`.
  - Every write, delete and lock goes to the writer over the cluster link.
  - On the writer the storage stays `file_system` as today, and each write
    becomes a journal record.
- That makes Caddy's own cluster coordination work across nodes: locks and
  challenge tokens live in storage, as Caddy documents for shared storage.
  So each certificate is issued once per cluster, not once per node, which
  also spares the ACME rate limits.
- **Locks and challenge tokens are read through to the writer**, never from
  the local copy, because the copy may lag.
- On a follower the on-demand `ask` answers from the local indexes
  (`HostKnown`). A domain attached seconds ago may be refused until its
  index arrives; the next handshake gets it.
- The wildcard certificate (DNS-01) needs `SITEBIN_DNS_TOKEN` on every node
  (§9).
- **The writer unreachable:** existing certificates keep serving. Issuance
  and renewal wait.

## 7. Analytics, view counts, reports

- **View counts and analytics hits are produced by authz** on whichever node
  serves the page. On a follower they go into a **forwarding queue** instead
  of the local recorder or `stats.json`.
  - The queue sends batches to the writer (*builder's call*: every 1 s or
    256 records), and the writer feeds them into its own recorder and view
    counter as if they were local.
  - Day files and `stats.json` are written only by the writer, so caps and
    byte budgets hold for the whole cluster.
  - A full queue drops, as the recorder does. Statistics are best effort.
- **Nothing identifying leaves the node.** The queue hashes the visitor id
  with the day's salt before sending, so a record never carries an IP
  address or a user agent.
- **Salts** are created only by the writer and replicated. A follower asks
  for the day's salt off the request path, and holds that day's hits in its
  queue until the salt arrives. Every node prunes salts two days past their
  date. Every node's backup leaves them out (`isAnalyticsSalts`), even
  though only the writer backs up.
- **Forwarded to the writer:** CSP reports (the tripwire can lock a site,
  the aggregator writes stats), abuse reports and the report page.

## 8. Backups and restore

- **Only the writer backs up.** `sitebin backup` and the agent's
  `/v1/backup-now` refuse on a follower. The host timers run on every node,
  and the wrapper skips a node that `sitebin cluster role` does not report
  as the writer (*builder's call*).
- The repository must be reachable from every node's host, so that the
  history carries on after a switch. The hosted instance's repository is
  already off the VM.
- **A site restore** is an ordinary write on the writer. From milestone 2 on,
  it also seeds the database replicas again.
- **An instance restore** runs only on the writer. It **bumps the epoch**
  (*builder's call*: a restore starts a new history, and positions from
  before it mean nothing). Every follower reconciles fully and leaves
  rotation until it is serving-current again.

## 9. Licence, stack registration, secrets, joining

- **Licence bound to one cluster** (requirements 1 and 11; built first,
  §15). Today a licence names no instance (app id, holder, plan, dates,
  `max_custom_domains`), so one key works anywhere.
  - **Every instance has a cluster id**, single instances included. It is
    created at the first start on the new version (`<data>/cluster/id`,
    random), replicated within a cluster, and kept by a restore and by
    `cluster init`. `sitebin cluster id` prints it.
  - **The renewal sends it**: `{"license": …, "cluster_id": …}`. The stack
    binds the licence to the first cluster id that renews it, and signs
    `cluster_id` into the payload of the key it returns.
  - **A renewal with another cluster id is refused** (`409`, "bound to
    another installation"), and the stack mails the holder. Once a licence
    is bound, a renewal with no cluster id at all is refused too. Otherwise
    an older version on a second installation would escape the binding.
  - **Verification:** a key whose `cluster_id` names another cluster
    verifies as `none`, like any unverifiable key, never as `expired`. That
    is part of verifying the key, not a third enforcement point. A key
    without `cluster_id` (issued before its first renewal, or by an older
    stack) verifies as today.
  - **After a refusal** (*builder's call*): the instance records it and
    treats an unbound key as `none` from then on. A notice names the reason
    and the portal action. A key bound to the instance's own cluster is never
    downgraded.
  - **Moving a licence:** the holder releases the binding in the stack's
    portal ("Lizenz auf neue Installation umziehen"), and the next renewal
    binds it anew. The platform admin can do the same. A release is recorded
    and mailed to the holder.
  - **Compatibility:** an older Sitebin sends no cluster id, and the stack
    renews its unbound licence as today until the licence is bound.
  - **Ship order:** the stack first, then Sitebin.
  - **Known limits:**
    - A data root copied whole (including the cluster id) renews as the same
      cluster. Copying a whole installation with all its customer data is a
      deliberate breach of the licence terms, which no check here can
      detect.
    - An air-gapped installation (`SITEBIN_LICENSE_KEY`, no renewal) stays
      unbound unless the holder enters its cluster id in the portal and
      installs the bound key the portal then issues (*builder's call*,
      optional for milestone 1).
  - Only the writer renews. The key replicates, and the trial marker too,
    so there is one trial per cluster.
  - The domain ceiling counts the cluster's index, which is one index for
    the whole cluster.
  - **No third enforcement point** (*builder's call*, following the
    codebase rule "enforcement lives at exactly two places"). Cluster mode
    is part of the Enterprise edition, as container sites are, and is not
    gated one by one. `cluster init` and `join` check no licence state. An
    expired licence restricts a cluster exactly where it restricts a single
    instance: no new sites, no new domains past the ceiling. It never stops
    a running cluster, never stops replication, and never removes a node.
  - The licence has no node count: one licence covers every node of the
    cluster (requirement 1).
- **Stack registration:** one per cluster. The writer registers on start
  and on promotion. Every URL it declares is on the base domain, which
  reaches the writer through any node.
- **Secrets:**
  - `<data>/.secret` replicates, so every node signs and verifies the same
    cookies and links.
  - Environment secrets (OAuth clients, SMTP, stack keys, PayGate,
    Stripe/Paddle, DNS token, FTP TLS) and the tiers file must be the same
    on every node that may become the writer. *Builder's call:* every node
    reports a fingerprint of its writer-relevant settings: an HMAC under the
    cluster id, never the values. The console marks differences, and a node
    with differences cannot be promoted.
- **Joining:**
  1. The operator creates a one-time join token in the console, valid for
     1 h.
  2. The new node starts with an **empty data root**, plus
     `SITEBIN_CLUSTER_JOIN=<writer's cluster address>` and the token.
  3. It creates its key, and the writer issues its certificate.
  4. The writer adds it to `cluster.json` with **serving off**
     (*builder's call*: a new node never serves until the operator switches
     it on).
  5. The node reconciles from empty and pulls ahead the images.
- **Turning cluster mode on:** `sitebin cluster init` makes an existing
  instance the first node and writer of a new cluster. It creates the
  cluster id, the CA and epoch 1, and moves no data.
- **Removing a node:** the writer drops it from `cluster.json`, and its
  certificate is refused from then on.

### Corrections (post-implementation)

Changed by the final review of milestone 0 (2026-11-07, Ruling 9 and
Ruling 6), after the licence binding was built:

- **A release names its target.** "Moving a licence" above said the holder
  releases the binding and the next renewal binds it anew. That left the
  licence open between the release and the next renewal: anyone holding a copy
  of any key ever signed for the holder could renew first, take the binding
  and keep it (the hostage race). So a release now names the installation the
  licence moves to (`cluster_id` in the release body), and the stack binds the
  licence to that id in the same write (`previous_cluster_id` = the old one,
  `cluster_bound_at` = now). The target's next renewal re-signs the key with
  its id; every other installation, the old one included, is refused.
- **Holder releases require the target**; without one the stack answers `400`.
  The holder finds the new installation's id in its `<data>/cluster/id`, in
  its startup log line `cluster_id=`, or in the bound-elsewhere notice, which
  therefore shows the FULL id. A target equal to the current binding is
  `409 ALREADY_BOUND_THERE` and does not use up the holder's 7-day window.
  Moving back to the previously released installation is allowed.
- **The operator may still release without a target**, deliberately: the
  licence is then open and the next installation that renews takes it.
- **The `license.bound_elsewhere` mail is sent at most once per holder per
  day** (Ruling 6), not once per foreign cluster per day: the cluster id is
  chosen by the caller, so keying on it would let a copy of the key mint one
  mail per invented id. It is sent without holding the `409` on the mail relay.

## 10. The distributor in front

Sitebin ships no load balancer. Each node offers a plain-HTTP health
listener (`SITEBIN_CLUSTER_HEALTH_LISTEN`, off by default, meant for the
operator's private network; *builder's call*):

- `GET /serve` answers `200` while the node is in rotation, and `503`
  otherwise. HAProxy's agent-check format is an option.
- `GET /writer` answers `200` only on the writer of the current epoch, and
  only while its serving switch is on. It is for everything that must reach
  the writer directly, such as FTP (port 21 and the passive range). With
  the writer switched off, FTP is down: a switched-off node answers no
  public traffic (§2).

What a distributor needs (*builder's call*, documented for operators):

- **The client address:** Caddy accepts the PROXY protocol from
  `SITEBIN_PROXY_FROM=<cidrs>`. Without it, every visitor would carry the
  distributor's address in provenance, rate limits and analytics.
- **HTTP/3:** the distributor must pass UDP 443, or the nodes must stop
  advertising HTTP/3 (`SITEBIN_HTTP3=off`).
- **For the edge later:** the same `/serve` check works for a GeoDNS or
  anycast provider.

## 11. Console, CLI, mail

- **Workspace → Cluster** (operator only):
  - a table of nodes: name, role, epoch, version, serving switch, in
    rotation, current or lag (changes and seconds), last contact,
    reachability, configuration match, database replicas;
  - actions: the serving switch, "make writer" (offered only for promotable
    nodes), "make writer and serve", a join token, removing a node, and the
    list of set-aside changes.
- **A warning** whenever an action would leave no node in rotation.
- **The console lives on the base domain, so it needs the writer.** With
  the writer gone, the CLI on the target node is the way:
  `sitebin cluster status | promote --accept-loss | resume`.
- **The CLI's full set:** `init`, `join`, `status`, `role`,
  `serve on|off <node>`, `switch <node>`, `promote`, `resume`, `leave`.
- **Operator mail** (plain text, like the abuse alerts):
  - a follower more than 5 minutes behind;
  - a follower that cannot reach the writer for more than 5 minutes;
  - a replica that breaks;
  - set-aside changes after a node returns.

## 12. Keeping the edge and zero loss open

- **The traffic between nodes needs no shared LAN:** HTTPS with mutual TLS,
  opened by the follower, resumable from a position. That works across the
  internet and through NAT. There is no shared disk and no floating IP in
  Sitebin.
- **The serving path never depends on the writer**, so a node far from the
  writer serves at local speed. Only writes and the base domain travel.
- **The `/serve` health check fits HAProxy, GeoDNS and anycast alike.**
- **Zero loss later.** The writer already knows every follower's applied
  position. "Answer a write only once k followers have applied it" is a
  wait on the writer over those positions, with no format change. One
  condition: a follower reports a position only after the apply is durable.
- **Nothing here assumes two nodes.** The same rules hold for ten.

## 13. Seams: core and enterprise

- Cluster mode lives in `ee/cluster`.
- **The core gains inert seams only** (rule 2): with no provider registered,
  each does nothing, and each has a community-path test.
  - a dirty-unit observer on the store's locks and writers;
  - follower mode in the store;
  - a forwarding decision for authz and the route table (an optional
    `ext.ClusterRouter`);
  - a sink for view counts and analytics hits;
  - inputs for caddygen (the cluster listener, PROXY protocol, HTTP/3
    switch, storage module).
- **The applier is not a user write.** It bypasses the abuse scan, which
  already ran on the writer, and the quotas, but it keeps the ordering rule
  of §3. `TestWritePathCensus` classifies its writes.
- The README's "Availability & failover" says read replicas are "on the
  enterprise roadmap" and lists "serving from an rsync'd copy" as
  unsupported. It gets rewritten to describe cluster mode, and keeps the
  single-writer rule for everything outside it.

## 14. Testing

- **Unit tests** for each part.
- **`e2e/cluster.ps1`** on the enterprise image: three containers on one
  Docker network — the writer, a serving follower and a follower switched
  off — behind a small HAProxy, with an internal ACME (Pebble). It covers:
  - `init`, and `join` with a token;
  - syncing creates, replaces, renames, deletes, domains with paths,
    redirects, git pushes and an account deletion;
  - **a follower never serving a held file**: an upload that hits the
    scanner, raced against reads on the follower;
  - analytics and view counts counted once, with no IP address on the wire;
  - a certificate issued through a request on a follower;
  - the serving switch and rotation;
  - a follower without a writer keeps serving content for the staleness
    limit, then leaves rotation;
  - a switched-off node refuses public traffic that reaches it anyway;
  - the client address in provenance across a forward;
  - a planned switchover with a container site, measuring the write pause;
  - an emergency promotion;
  - the old writer returning and its writes set aside;
  - the version rule (an older node is not promotable);
  - an instance restore and the full reconciliation after it.
- **Fault tests:** `kill -9` during an apply, and during each switchover
  step.
- Milestone 2 adds a MySQL project: switchover with no lost transaction,
  emergency promotion within the lag, a broken replica and its re-seed.

## 15. Milestones (*builder's call*)

0. **Licence binding** (§9), across two repos: the SaaS stack first
   (cluster id at renewal, binding, `cluster_id` in the payload, refusal,
   release in the portal), then Sitebin (cluster id, renewal, verification,
   notice). It ships on its own, before cluster mode, and the hot spare may
   wait for it (requirement 11).
1. **Cluster core:**
   - state, join, transport;
   - journal, applier, reconciliation, follower mode, forwarding;
   - analytics and view counts;
   - Caddy storage;
   - health listener, console and CLI;
   - planned switchover and emergency promotion.

   Containers run on the writer, and their data travels as files.
2. **Database replicas** for `mysql-8.4`.

**Coupled work** (workspace `CLAUDE.md`, ship order):

- the SaaS stack (`saas-stack`): the licence binding, its portal action
  and mail, shipped before Sitebin's half;
- the README;
- a website docs page `/docs/cluster/`, and the licensing docs (one
  licence per installation or cluster, how to move it);
- the pricing page, which names cluster mode under Enterprise only;
- the website's "Availability" wording, if any.

## Left to the builder

- The journal's storage format.
- The wire protocol and its messages.
- Manifest caching on the writer.
- The delta algorithm and the threshold for "large".
- Exact timeouts, plus every number marked *builder's call*.
- The route table's contents.
- How forwarding enters the writer's pipeline, within §2's constraints.
- The MySQL config mechanism, verified against the pinned image.

## Decided after the builder check (2026-11-07)

- **Where nodes stand** is operations, not this design (Non-goals). The
  operator's first spare runs at the same hosting provider.
- **The licence binding is built from the start** as milestone 0 (§9, §15).
- The builder's calls taken in the check: two tests instead of one
  "current" (§4); a staleness limit of 1 h for followers cut off from the
  writer (§4); `strictest(old, new)` applied first, and authz checking an
  index against `meta.json` (§3); the serving switch enforced by the node
  itself (§2); container data every 15 minutes as block deltas (§3, §5); no
  third licence enforcement point (§9).
