# Cluster mode, milestone 0: a licence bound to one cluster — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A licence works on exactly one installation (a cluster, or a single
instance as a cluster of one), bound at its first renewal, movable by its
holder.

**Architecture:** Two repos.
- **SaaS stack** (`C:\Projects\Sitebin-Project\saas-stack`):
  - signs an optional `cluster_id` into the licence payload;
  - `paygate` binds a licence row to the first cluster id that renews it
    (re-signing the key with the claim), refuses every other one with
    `409 LICENSE_BOUND`, and lets the holder or the operator release the
    binding.
- **Sitebin:**
  - every instance gets a persistent cluster id;
  - the renewal sends it;
  - verification treats a key bound to another cluster as `none`;
  - a `409` from the stack makes an unbound key count as `none`, with a
    notice.

**Tech Stack:**
- Stack: TypeScript, Fastify, zod, drizzle, Postgres, `tsx --test`
  (`node:test`).
- Sitebin: Go with the `ee` build tag, PowerShell e2e.

**Spec:** `docs/superpowers/specs/2026-11-07-cluster-design.md`, §9 "Licence
bound to one cluster", §15 milestone 0.

## Global Constraints

- **Cluster id format:** 26 characters of lowercase base32, `^[a-z2-7]{26}$`,
  made by `internal/ids.New()`. The file is `<data>/cluster/id`, and Sitebin
  never regenerates an existing one.
- **Wire format:**
  - The renewal body is `{"license": "<key>", "cluster_id": "<id>"}`;
    `cluster_id` is optional.
  - The refusal is HTTP `409` with body `{"error":"LICENSE_BOUND","message":…}`.
  - The payload field is `cluster_id` (omitted when unbound).
- **Licence states:** a key whose `cluster_id` names another cluster
  verifies as `none`, never `expired`. A key bound to the instance's own
  cluster is never downgraded.
- **Enforcement:** no third enforcement point in Sitebin. Only `licenseGate`
  and `CustomDomainsAllowed` read `Restricted`/entitlements, as today.
- **Compatibility:** a renewal with no `cluster_id` renews unbound licences
  exactly as today. Once a licence is bound, the stack refuses a renewal
  without `cluster_id`.
- **Moving a licence** (changed by Ruling 9, see design Corrections: a
  release names its target `cluster_id`, required for the holder, optional
  for the operator):
  - The holder may release at most once per 7 days. The operator has no
    limit.
  - A release remembers the cluster it released (`previous_cluster_id`),
    and binding to that cluster is refused until the next release.
- **Mail:**
  - Two new templates, `license.bound_elsewhere` and `license.released`, in
    en, de, fr and es, each with subject, html and txt.
  - `license.bound_elsewhere` mails at most once per foreign cluster per
    day.
- **Ship order:** the stack first (dev, then cloud), then Sitebin
  (`app.sitebin.io`), then the website.
- **Where the id is shown:** the spec's `sitebin cluster id` command comes
  with milestone 1's `sitebin cluster` CLI; the core CLI has no enterprise
  seam yet. Until then the id is in `/data/cluster/id` and in the licence's
  startup log line (`cluster_id=`).
- **Commits:** lowercase conventional prefixes, a sentence about the
  behaviour, trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Stack schema changes** go only through
  `docker_compose_shared/migrations/NNN-*.sql`, which must be idempotent
  and grant to its role (stack AGENTS.md trap 33).

## Review Focus

1. **Two installations renew in the same second with different ids:**
   exactly one wins and the other gets `409`. The claim is one conditional
   `UPDATE … WHERE cluster_id IS NULL`, never read-then-write (Task S2,
   e2e S5).
2. **A subscription renewal webhook re-issues a bound licence:** the new key
   keeps `cluster_id`, otherwise the next renewal looks unbound and the
   binding is lost (Task S2).
3. **The old installation is still running after a move:** its daily
   renewal must not bind the licence back before the new one does
   (`previous_cluster_id`, Task S3; e2e S5).
4. **A cluster id file that exists but is empty or invalid** (a crash
   before the link, a hand edit): never silently regenerated. The licence
   path then sends no id, and the log says why (Task B1).
5. **The stack answers the renewal with the same key it already holds,
   after a `409` was recorded:** the bound-elsewhere marker must clear on
   any successful renewal, not only on a changed key (Task B3).

---

## Part A — SaaS stack

Work in a worktree of `saas-stack` on branch `feat/licence-cluster-binding`
(`git worktree add .claude/worktrees/licence-cluster-binding -b feat/licence-cluster-binding main`).
Read `AGENTS.md` and README § 16 first. Run tests with
`npm test --workspace=services/paygate` and
`npm test --workspace=services/platform-api`, and the type check with
`npx tsc -p tsconfig.typecheck.json --noEmit`.

### Task 1 (S1): The licence payload carries an optional `cluster_id`

**Files:**
- Modify: `packages/shared/src/license.ts`: `LicPayload` gets `cluster_id?: string`, with a doc comment (the installation it is bound to; absent = unbound).
- Modify: `services/platform-api/src/routes/licensing.ts:25-38`: `issueLicenseSchema` gets `cluster_id: z.string().regex(/^[a-z2-7]{26}$/).optional()`, passed to the service as `clusterId`.
- Modify: `services/platform-api/src/services/licensing-service.ts`: `IssueLicenseRequest.clusterId?: string`. `issueLicense` adds `cluster_id` to the payload only when set (no `cluster_id: undefined` key in the JSON).
- Test: `services/platform-api/test/licensing-cluster-id.test.ts`

**Interfaces:**
- Produces: `POST /internal/licenses/issue` accepts `cluster_id`. `/internal/licenses/verify` returns it inside `lic_payload` unchanged (it already returns the whole decoded payload).

- [ ] **Step 1: Write failing tests**
  - `issue route passes cluster_id to the service`: Fastify inject with a fake `licensing` service; the body with `cluster_id: 'abcdefghijklmnopqrstuvwxyz'` reaches `issueLicense` as `clusterId: 'abcdefghijklmnopqrstuvwxyz'`.
  - `issue route refuses a malformed cluster_id`: `'ABC'` gives `400`.
  - `a payload without cluster_id has no such key`: build a licence with `buildLicense` (shared) and a payload lacking the field; `decodeLicense(...).lic` has no `cluster_id` property (`assert.equal('cluster_id' in lic, false)`).
- [ ] **Step 2:** `npm test --workspace=services/platform-api`. Expected: the new tests FAIL.
- [ ] **Step 3:** Implement the three modifications above.
- [ ] **Step 4:** Tests pass, and the type check passes.
- [ ] **Step 5: Commit** `feat: a licence can name the installation it is bound to`

### Task 2 (S2): Renewal binds a licence to the first cluster and refuses every other

**Files:**
- Create: `docker_compose_shared/migrations/006-license-cluster-binding.sql`. On `paygate_db`, `licenses` gets:
  - `cluster_id VARCHAR(26)`
  - `cluster_bound_at TIMESTAMPTZ`
  - `previous_cluster_id VARCHAR(26)`
  - `cluster_released_at TIMESTAMPTZ`

  All `ADD COLUMN IF NOT EXISTS`, then `GRANT SELECT, INSERT, UPDATE ON licenses TO paygate_role`. Follow the header style of `005-change-orders.sql`.
- Modify: `packages/db/src/schemas/paygate.ts:183-206`: the same four columns (`clusterId`, `clusterBoundAt`, `previousClusterId`, `clusterReleasedAt`).
- Create: `services/paygate/src/services/license-binding.ts`: the pure decision.
- Modify: `services/paygate/src/services/license-service.ts`:
  - `LicenseRecord` gets `clusterId: string | null`, `clusterBoundAt: string | null`, `previousClusterId: string | null`.
  - `renewFromLicense(presented, clusterId?)` follows the decision.
  - `issue()` re-signs with the previous record's `clusterId`.
  - `signLicense` takes an optional `cluster_id`.
- Modify: `services/paygate/src/routes/licenses.ts`:
  - `renewSchema` gets `cluster_id` (same regex as S1, optional);
  - `bound_elsewhere` answers `409 { error: 'LICENSE_BOUND', message: 'This licence is bound to another installation. Its holder can move it to this one in the account portal.' }`;
  - the GET route adds `cluster_id` and `cluster_bound_at`.
- Modify: `packages/shared/src/mail-templates.ts`: add the key `license.bound_elsewhere` (the template files come in S3, and the built-in fallback text here).
- Test: `services/paygate/test/license-binding.test.ts`

**Interfaces:**
- Produces (`license-binding.ts`):
  ```ts
  export type RenewalDecision = 'as_today' | 'bind' | 'resign' | 'current' | 'bound_elsewhere';
  export function decideRenewal(
    row: { clusterId: string | null; previousClusterId: string | null; keyClusterId: string | null },
    presented: string | undefined,
  ): RenewalDecision;
  ```
  - `keyClusterId` is the `cluster_id` decoded from the stored `licenseKey` (`decodeLicense`).
  - Rules:
    - unbound row with no id presented → `as_today`;
    - unbound row with an id equal to `previousClusterId` → `bound_elsewhere`;
    - unbound row with any other id → `bind`;
    - bound row with no id or a different id → `bound_elsewhere`;
    - bound row with the same id, but `keyClusterId` differs → `resign`;
    - otherwise → `current`.
- Produces (service): `renewFromLicense(presented: string, clusterId?: string)` returns the existing union plus `{ status: 'bound_elsewhere'; reason: string }`.

- [ ] **Step 1: Write failing tests**
  - In `license-binding.test.ts`, one `it` per rule above, with literal ids `'a'.repeat(26)` and `'b'.repeat(26)`. One more: a row bound to A whose stored key has no claim (signing failed after the claim) → `resign` for A.
  - A route test with a fake `licenseService` returning `bound_elsewhere`: `409`, `error === 'LICENSE_BOUND'`. A body with `cluster_id: 'x'` → `400`.
- [ ] **Step 2:** `npm test --workspace=services/paygate`. Expected: FAIL.
- [ ] **Step 3: Implement `decideRenewal`, then `renewFromLicense`**
  - `bind`: claim with ONE statement:
    `UPDATE licenses SET cluster_id=$id, cluster_bound_at=now() WHERE app_id=$a AND user_id=$u AND cluster_id IS NULL AND (previous_cluster_id IS DISTINCT FROM $id) RETURNING id`.
    - No row returned → re-read and decide again (the loser of a race ends at `bound_elsewhere`).
    - Then re-sign with the same holder, plan, `expires_at`, `grace_until` and entitlements plus `cluster_id`, store the key, and return the record.
    - No mail. Log `'Licence bound to an installation'` with appId, userId and clusterId.
  - `resign`: re-sign and store as above, without the claim.
  - `bound_elsewhere`: `notifications.notify({ key: 'license.bound_elsewhere', dedupeKey: \`license_bound_${presented ?? 'none'}_${YYYY-MM-DD}\`, vars: { planName, boundSince, … } })`, best effort and never thrown, then return the status.
  - `issue()`: pass `previous?.clusterId ?? undefined` into `signLicense`. The upsert's `set` must NOT touch the four new columns.
- [ ] **Step 4:** Tests and the type check pass.
- [ ] **Step 5: Document** in README § 16, Licence issuance:
  - the renewal body's `cluster_id`;
  - the binding at the first renewal;
  - `409 LICENSE_BOUND`;
  - that a webhook re-issue keeps the binding;
  - the compatibility rule.

  In `AGENTS.md`: the mail key list and its count, and a new trap: "a licence binds at its first renewal with a cluster id; never reset `cluster_id` in an upsert".
- [ ] **Step 6: Commit** `feat: a licence binds to the first installation that renews it and refuses every other`

### Task 3 (S3): The holder or the operator releases a licence's binding

(Changed by Ruling 9, see design Corrections: `release` takes a target
`cluster_id` and binds the licence to it in the same write; the holder must
name one; `ALREADY_BOUND_THERE` is `409`; the mail names the target.)

**Files:**
- Modify: `services/paygate/src/services/license-service.ts`: add `release(appId: string, userId: string, by: 'holder' | 'operator'): Promise<ReleaseResult>`.
- Modify: `services/paygate/src/routes/licenses.ts`: add `POST /api/v1/:appId/users/:userId/license/release`, with body `{ by: 'holder' | 'operator' }` and `requirePlatformKey` (the admin portal holds that key for both the holder's and the super-admin's pages).
- Create: the mail templates `mail-templates/license.bound_elsewhere{,.de,.fr,.es}.{subject.txt,html,txt}` and `mail-templates/license.released{,.de,.fr,.es}.{subject.txt,html,txt}`, 24 files modelled on `license.renewed.*`.
- Modify: `packages/shared/src/mail-templates.ts`: add the key `license.released`, the subjects in all four languages, and the built-in fallbacks.
- Test: `services/paygate/test/license-release.test.ts`

**Interfaces:**
- Produces:
  ```ts
  export type ReleaseResult =
    | { status: 'released'; previousClusterId: string }
    | { status: 'not_bound' }
    | { status: 'too_soon'; nextAllowedAt: string }
    | { status: 'no_license' };
  ```
  - The route maps them: `200 { data: { released: true, previous_cluster_id } }`, `200 { data: { released: false } }`, `409 { error: 'RELEASE_TOO_SOON', next_allowed_at }`, `404`.
  - A pure helper `releaseAllowed(by, lastReleasedAt: Date | null, now: Date): { ok: true } | { ok: false; nextAllowedAt: Date }` lives in `license-binding.ts`, with a 7-day window, and always `ok` for `operator`.

- **Copy** (the de and en texts; fr and es are translated from en in the same tone):
  - `license.bound_elsewhere`:
    - en subject: "Your licence was presented by another installation".
    - en body: "Your {{planName}} licence is bound to the installation it was first used on (since {{boundSince}}). Another installation tried to renew it today and was refused. If you have moved to a new installation, release the licence in your account portal; the new installation picks it up at its next daily check. If you did not expect this, someone may be using a copy of your licence key."
    - de subject: "Ihre Lizenz wurde von einer anderen Installation vorgelegt".
    - de body: "Ihre {{planName}}-Lizenz ist an die Installation gebunden, auf der sie zuerst verwendet wurde (seit {{boundSince}}). Heute hat eine andere Installation versucht, sie zu erneuern, und wurde abgewiesen. Wenn Sie auf eine neue Installation umgezogen sind, geben Sie die Lizenz im Kundenportal frei; die neue Installation übernimmt sie bei ihrer nächsten täglichen Prüfung. Wenn Sie damit nicht gerechnet haben, verwendet möglicherweise jemand eine Kopie Ihres Lizenzschlüssels."
  - `license.released`:
    - en subject: "Your licence was released".
    - en body: "Your {{planName}} licence is no longer bound to the installation it was used on. The next installation that renews it takes it over; the previous one cannot take it back until you release it again. Released by: {{releasedBy}}."
    - de subject: "Ihre Lizenz wurde freigegeben".
    - de body: "Ihre {{planName}}-Lizenz ist nicht mehr an die bisherige Installation gebunden. Die nächste Installation, die sie erneuert, übernimmt sie; die bisherige kann sie erst nach einer erneuten Freigabe zurückbekommen. Freigegeben durch: {{releasedBy}}."
  - `{{releasedBy}}` is "you" / "Sie" or "the operator" / "den Betreiber".

- [ ] **Step 1: Write failing tests**
  - `releaseAllowed`:
    - a holder 6 days after the last release → not ok, with `nextAllowedAt` = last + 7 d;
    - a holder after 7 days → ok;
    - the operator → always ok;
    - a holder who never released → ok.
  - The route with a fake service, each `ReleaseResult` → its status code.
  - `a released cluster cannot bind again until the next release`: `decideRenewal({ clusterId: null, previousClusterId: A, keyClusterId: A }, A) === 'bound_elsewhere'`, and the same with B → `bind`.
  - The mail catalogue: every key in `MAIL_TEMPLATE_KEYS` has a subject in en, de, fr and es (extend the existing catalogue test, if there is one, rather than adding a second).
- [ ] **Step 2:** Run, and expect FAIL.
- [ ] **Step 3: Implement `release`** with one `UPDATE … SET previous_cluster_id = cluster_id, cluster_id = NULL, cluster_bound_at = NULL, cluster_released_at = now() WHERE … AND cluster_id IS NOT NULL RETURNING previous_cluster_id`, after `releaseAllowed`. Then mail `license.released` (dedupe key `license_released_<released_at ISO>`) and log.
- [ ] **Step 4:** Tests and the type check pass. `npm run test:mail` lists 18 templates (its count is updated).
- [ ] **Step 5:** README § 16: the release route, its 7-day rule, `previous_cluster_id`, and the two mails.
- [ ] **Step 6: Commit** `feat: a licence holder can move a licence to a new installation`

### Task 4 (S4): The portal shows the binding and offers the release

(Changed by Ruling 9, see design Corrections: both confirmation pages ask for
the new installation's id, required for the holder and optional for the
super-admin, checked against `^[a-z2-7]{26}$` before PayGate is called.)

**Files:**
- Modify: `admin-portal/server.js`:
  - **Holder:** `GET /apps/:appId/license` and `POST /apps/:appId/license/release`, for any signed-in user, acting only on the session's own `sub`.
  - **Super-admin:** `POST /users/:userId/apps/:appId/license/release` (`requireSuperAdmin`), behind a server-rendered confirmation page like the existing cancel and suspend actions.
- Create: `admin-portal/license.js`: page renderers for the holder page and the confirmation pages.
- Modify: `admin-portal/directory.js:225-260`: the licence cell shows "bound since <date>" or "not bound yet", and links to the release confirmation.
- Modify: the hosted plan page (`admin-portal/server.js` around `:438`): a "Licence" link to `/apps/<appId>/license`, shown only when a licence exists. No inline-script edits (trap 37).
- Test: `admin-portal/test/license.test.js` (the style of `admin-portal/test/cancel.test.js`)

**Interfaces:**
- Consumes: `GET /api/v1/:appId/users/:userId/license` (now carrying `cluster_id` and `cluster_bound_at`) and S3's release route with `by: 'holder'` or `by: 'operator'`.
- **The holder page shows:**
  - plan, expiry and grace;
  - "Bound to installation `<first 6 characters of cluster_id>…` since `<date>`" or "Not bound to an installation yet";
  - the button "Move to a new installation" ("Auf neue Installation umziehen"), leading to a confirmation that states the 7-day rule and that the old installation keeps running unlicensed after its cached key expires.
- `too_soon` renders the next allowed date. The page is themed like the plan page (`data-ss-portal-page="license"`).

- [ ] **Step 1: Write failing tests**
  - The holder page renders the bound state and the unbound state.
  - A POST for another user's licence is impossible: the route takes no `userId`, and the test asserts the paygate call names the session's `sub`.
  - `too_soon` shows the date.
  - The super-admin release requires `super-admin` (403 otherwise).
- [ ] **Step 2:** `npm test --workspace=admin-portal`, and expect FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Tests pass.
- [ ] **Step 5: Commit** `feat: the portal shows where a licence is bound and lets its holder move it`

### Task 5 (S5): Stack e2e, then deploy dev before cloud

**Files:**
- Create: `scripts/license-binding-e2e-test.ts`, with `npm run test:license-binding` in the root `package.json`, modelled on the other `scripts/*-e2e-test.ts`.

The e2e runs against a running stack with `PLATFORM_ADMIN_KEY`, on a throwaway user of the stack's test app:
1. Issue an operator licence.
2. Renew without `cluster_id` → 200, the key has no claim.
3. Renew with A → 200, the key's `cluster_id === A`.
4. Renew with A again → the same key.
5. Two parallel renewals, with A and B, on a fresh licence → exactly one 200 and one 409.
6. Renew with B → 409 `LICENSE_BOUND`, and MailHog (dev) shows one `license.bound_elsewhere`; repeating it sends no second mail.
7. Renew without an id → 409.
8. Holder release → `released`.
9. Renew with A → 409 (the previous cluster).
10. Renew with B → bound to B.
11. A second holder release within 7 days → 409 `RELEASE_TOO_SOON`.
12. Operator release → ok.
13. Clean up the throwaway user.

Added by the final review (Ruling 9 and Important 3; steps 8–10 then name
their target, see design Corrections):
- After binding to A: an operator re-issue → the GET still shows
  `cluster_id` A, the new key claims A, and a renewal with A → 200.
- A holder release with target B → the GET shows B, and a renewal with A →
  409.
- A renewal with B → the re-signed key claims B.

- [ ] **Step 1:** Write the script, and run it against the dev stack (`auth.ittrail.dev`) after deploying the branch to dev (`ssh auth01`, the stack's dev compose, `db-migrate` runs 006). Expected: every step passes; `docker compose logs db-migrate` shows 006 applied.
- [ ] **Step 2:** Merge to `main`, push, and deploy cloud the same way. Check that `GET …/users/<operator>/license` for the hosted instance's licence still answers, with `cluster_id: null` until Sitebin ships.
- [ ] **Step 3: Commit** `test: e2e for binding, refusing and moving a licence`, and record the deploy in the stack's docs where deploys are recorded.

---

## Part B — Sitebin

Work in this worktree (branch `worktree-cluster-mode`). Run
`go test ./... && go test -tags ee ./...` and `go vet ./...`, plus
`go vet -tags ee ./...`.

### Task 6 (B1): Every instance has a persistent cluster id

**Files:**
- Create: `ee/cluster/doc.go` (package doc; ELv2 header like `ee/licensing/doc.go`) and `ee/cluster/id.go` (`//go:build ee`)
- Test: `ee/cluster/id_test.go`

**Interfaces:**
- Produces: `func LoadOrCreateID(dataDir string) (string, error)` in package `cluster` (`ee/cluster`).
  - The file is `<dataDir>/cluster/id` (the directory has mode 0700, the file 0600). Its content is the id plus `\n`.
  - Creation writes a temp file in the same directory, `fsync`s it, then `os.Link`s it to `id`. The link fails if `id` exists, which means another process won; in that case it reads `id`. The temp file is then removed.
  - An existing file whose trimmed content fails `^[a-z2-7]{26}$` returns `ErrInvalidID` (exported) and is never overwritten.
  - `const IDFile = "cluster/id"`.

- [ ] **Step 1: Write failing tests**
  - `TestLoadOrCreateIDCreatesOnce`: the first call returns a valid id, a second call returns the same, and the file content is `id+"\n"`.
  - `TestLoadOrCreateIDKeepsInvalidFile`: write `"garbage"` → `ErrInvalidID`, and the file still holds `"garbage"`.
  - `TestLoadOrCreateIDEmptyFile`: an empty file → `ErrInvalidID`.
  - `TestLoadOrCreateIDConcurrent`: 8 goroutines on a fresh dir all get the same id.
- [ ] **Step 2:** `go test -tags ee ./ee/cluster/`. Expected: FAIL (no package).
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS. `go build ./...` (community) is unaffected.
- [ ] **Step 5: Commit** `feat: every enterprise instance has a persistent cluster id`

### Task 7 (B2): A key bound to another cluster verifies as `none`

**Files:**
- Modify: `ee/licensing/licensing.go:70-90`: `LicensePayload` gets `ClusterID string \`json:"cluster_id,omitempty"\``.
- Modify: `ee/licensing/manager.go`:
  - `func (m *Manager) SetClusterID(id string)`, which must be called before `Load`/`Start`, like `SetRefresh`;
  - a private `verify(key string) (License, error)` that wraps `Verify` and returns `ErrOtherCluster` (exported) when `lic.Lic.ClusterID != "" && lic.Lic.ClusterID != m.clusterID`. Every verification in the manager (cache, env and stack paths) goes through it.
- Modify: `ee/licensing/state.go`:
  - `Snapshot` gets `BoundElsewhere bool` (exported; set by B3's marker or by `ErrOtherCluster`);
  - `Status` gets `BoundElsewhere bool` and `ClusterID string`;
  - `StatusAt`: when `BoundElsewhere` is set and the licence is unbound (`ClusterID == ""`), it is ignored (the trial arm); a licence bound to this cluster is unaffected.
  - `Notice()` gets a first case for `BoundElsewhere`;
  - `LogArgs` adds `cluster_id` and `bound_elsewhere` when set.
- Modify: `e2e/mintlicense/main.go`: a `-cluster-id` flag that sets `ClusterID`.
- Test: `ee/licensing/manager_test.go`, `ee/licensing/state_test.go`

**Interfaces:**
- Produces:
  - `licensing.ErrOtherCluster`;
  - `(*Manager).SetClusterID(string)`;
  - `Snapshot.BoundElsewhere`;
  - `Status.BoundElsewhere`;
  - `Status.ClusterID` (the instance's own id, for the console).
- **Notice copy:** "This Sitebin Enterprise licence is bound to another installation, so this instance runs unlicensed: a 90-day trial from its first start, then no new sites or drops. The licence holder can move the licence to this installation in the account portal (installation <first 6 characters of the cluster id>…)." (Changed by Ruling 9, see design Corrections: the notice ends "…in the account portal with this installation's id: <full id>.")

- [ ] **Step 1: Write failing tests**
  - `TestManagerKeyBoundToOwnCluster`: a cached key minted with cluster A, and the manager set to A → `StateLicensed`.
  - `TestManagerKeyBoundToOtherCluster`: a key for B, the manager set to A → `StateNone`, `errors.Is(st.Err, ErrOtherCluster)`, `st.BoundElsewhere`, the notice mentions "bound to another installation".
  - `TestManagerUnboundKeyUnaffected`: a key with no claim, the manager set to A → `StateLicensed`.
  - `TestManagerNoClusterIDBoundKey`: a key for A, the manager's id `""` → `StateNone` (bound elsewhere, never expired).
  - `TestStatusBoundElsewhereIgnoresUnboundKey` (in state_test): a snapshot with an unbound licence and `BoundElsewhere` → `StateNone`, and the trial arm applies.
  - `TestStatusBoundElsewhereKeepsOwnBoundKey`: a licence bound to A, own id A, `BoundElsewhere` set → `StateLicensed`.
- [ ] **Step 2:** `go test -tags ee ./ee/licensing/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS, including the existing licensing tests unchanged.
- [ ] **Step 5: Commit** `feat: a licence bound to another installation counts as no licence`

### Task 8 (B3): The renewal sends the cluster id and records a refusal

**Files:**
- Modify: `ee/license.go`:
  - `stackLicenseFetcher` gets `clusterID string`;
  - `Fetch` sends `cluster_id` when non-empty;
  - a `409` whose JSON `error` is `"LICENSE_BOUND"` returns `licensing.ErrBoundElsewhere`;
  - any other `409` stays a generic error;
  - the wiring at `:65` calls `cluster.LoadOrCreateID(p.host.DataDir())`. On an error it logs at ERROR level with the path and the reason (and that renewals go out without an id), and continues with `""`. Then `p.license.SetClusterID(id)`, and the id goes into the fetcher.
- Modify: `ee/licensing/manager.go`:
  - `ErrBoundElsewhere` (exported);
  - in `refreshOnce`: on `ErrBoundElsewhere`, write the marker `<data>/license/bound-elsewhere` (RFC3339 time), set `snap.BoundElsewhere`, and log at ERROR;
  - on ANY successful fetch (`ok`, including the same key), remove the marker and clear the flag before the same-key early return;
  - at load, read the marker into the snapshot.
- Test: `ee/license_test.go` (fetcher with `httptest`), `ee/licensing/manager_test.go`

**Interfaces:**
- Consumes: `cluster.LoadOrCreateID` (B1), `SetClusterID` and `Snapshot.BoundElsewhere` (B2).
- Produces: `licensing.ErrBoundElsewhere`. The marker file name is `bound-elsewhere`.

- [ ] **Step 1: Write failing tests**
  - `TestFetcherSendsClusterID`: the test server reads the body; `cluster_id == "a…a"`, and `license` is present.
  - `TestFetcherOmitsEmptyClusterID`: the body has no `cluster_id` key.
  - `TestFetcherMapsLicenseBound`: `409 {"error":"LICENSE_BOUND"}` → `errors.Is(err, licensing.ErrBoundElsewhere)`.
  - `TestFetcherOther409IsGeneric`: `409 {"error":"X"}` → an error that is not `ErrBoundElsewhere`.
  - `TestRefreshBoundElsewhereThenSameKeyClears`:
    1. A fake fetcher returns `ErrBoundElsewhere` → the status has `BoundElsewhere` and the marker exists.
    2. Then it returns the SAME key `ok` → the marker is gone and the flag is cleared.
  - `TestBoundElsewhereMarkerSurvivesRestart`: a new manager on the same dir loads with `BoundElsewhere`.
- [ ] **Step 2:** Run, and expect FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS. `go vet -tags ee ./...` is clean.
- [ ] **Step 5: Commit** `feat: the licence renewal names its installation and remembers a refusal`

### Task 9 (B4): The licence e2e and the docs

**Files:**
- Modify: `e2e/license.ps1`, with two new scenarios using `mintlicense -cluster-id`:
  1. **The instance's own cluster:** start the container, read `/data/cluster/id` (`docker exec … cat`), mint a key with that id, restart with `SITEBIN_LICENSE_KEY` → the startup log shows `state=licensed` and `cluster_id=<id>`.
  2. **Another cluster:** a key minted with another id → `state=none` `bound_elsewhere=true`, and the account UI notice contains "bound to another installation".
- Modify: `README.md`, the licensing section: the cluster id, the binding at the first renewal, moving a licence, the air-gapped note (the spec's known limits).
- Modify: `CLAUDE.md`, "Enterprise licensing": one bullet with the rules of this plan's Global Constraints that a later change could break (never regenerate the id; a `409` clears only on a successful renewal; `ErrOtherCluster` is `none`).

- [ ] **Step 1:** Add the scenarios, then build: `go run -tags ee ./e2e/mintlicense` (to check the flag), then `docker build --build-arg EDITION=enterprise --build-arg LICENSE_ROOTS=<throwaway root> -t sitebin:dev-ee .` as `license.ps1` documents.
- [ ] **Step 2:** `powershell -File e2e/license.ps1`. Expected: all scenarios PASS, the old ones included.
- [ ] **Step 3:** Write the README and `CLAUDE.md` text.
- [ ] **Step 4: Commit** `test: the licence e2e covers keys bound to this and to another installation` and `docs: licences bind to one installation`

### Task 10 (B5): Ship Sitebin, then the website

- [ ] **Step 1:** Only after S5 step 2 (stack cloud live): merge `worktree-cluster-mode` into `main` (only the milestone 0 commits and the design and plan docs; cluster code does not exist yet), push, and deploy `app.sitebin.io` per the hosted-instance procedure (pre-deploy snapshot into `/opt/sitebin/backups/`, keep 3 rollback images, prune the build cache).
- [ ] **Step 2: Verify** on the hosted instance: read `/opt/sitebin/data/cluster/id` and the last `enterprise license` line of the container log. Expected: a valid id. After the first refresh: `state=licensed` with `cluster_id=<id>`, and the stack's GET for the operator licence shows the same `cluster_id`. (Set `SITEBIN_LICENSE_REFRESH` only if waiting a day is not acceptable, and remove it again.)
- [ ] **Step 3:** Website `Sitebin-Website/public/docs/enterprise/index.html`: a short "One licence, one installation" section (binding, moving in the portal, a cluster counts as one installation). Check `public/terms/index.html` for wording that contradicts it. Commit `content: a licence binds to one installation and moves with its holder`, and push (deploys `public/`).
- [ ] **Step 4:** Update the workspace memory (`sitebin-hosted-instance-ops.md`): the hosted licence is bound to cluster `<id>`, and a re-install must keep `/opt/sitebin/data/cluster/id` or release the licence first.
