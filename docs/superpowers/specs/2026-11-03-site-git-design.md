# A Git repository per site

Date: 2026-11-03. Status: design. The operator decided the shape in
conversation and then handed every remaining choice to the agent ("bitte
entscheide du"). Those choices are listed at the end. **Build order:** after
`2026-11-03-site-tags-design.md`, `2026-11-03-front-door-design.md` and
`2026-11-03-site-ownership-design.md` are merged (session "App.sitebin.io Login
und Projekt-Tags", branch `feat/site-tags`). Site analytics (session
`sitebin-project-b9`) comes after this and adds its tab to the tab bar
defined here.

## Why

The operator keeps every project's source on GitHub and its site on Sitebin.
Two places for one project. They want **the source next to the site**, as the
project's backup, in one place. Two more reasons:

- With site ownership, a site moves into a customer's account. If the source
  lives in the site, **it moves with it**.
- For **container sites** the source *is* the application. Today it exists
  only as the uploaded files; in a repository it has a history.

This is explicitly **not CI/CD** ("nein, keine ci/cd, zumindest vorerst
nicht"). A push stores; it never builds, deploys or restarts anything.

## Goals

1. Every site can hold **one Git repository**, reached as an ordinary remote
   over HTTPS (Git's smart HTTP protocol):
   `https://<base-domain>/git/<edit-id>.git`.
2. **Three ways to authenticate**, decided by the operator:
   - the site's **edit password** (username: the drop's id, or anything) —
     community and enterprise, one site at a time;
   - an **account API token** (`sbp_…`) as the password — enterprise;
   - **OAuth** through the SaaS stack, via the Git Credential Manager —
     enterprise, the last build step (see OAuth).
3. **The repository counts against the site's byte quota** ("speicherplatz-
   kontingent zählt das git repo natürlich dazu").
4. A **read-only view** in a new **"Git" tab** on the edit page: branches,
   commit history, files at any commit, a file's content, ZIP download of a
   commit, and how to connect. The existing cards become the "Live" tab.
5. **Community code, paid on the hosted instance** ("feature nur für
   bezahlaccounts, kann aber als community feature gebaut werden"): the code
   is core (MIT); the hosted instance grants it to paid tiers through a tier
   flag.
6. **The repository belongs to its site.** It travels with a transfer and is
   deleted with the site — and the product says so plainly ("das muss
   natürlich kommuniziert werden").

## Non-goals

- Building, deploying or restarting from a push. Nothing served changes when
  a repository changes. (A later "deploy from Git" can build on this.)
- SSH transport. Dumb HTTP. Git LFS's API (pushing LFS *pointers* works like
  any file; the LFS objects are not stored).
- Web editing, pull requests, issues, webhooks, a public repository page.
- More than one repository per site; a repository without a site.
- **MCP tools.** The MCP catalog is a contract with every saved connector
  (workspace guide); v1 does not touch it. An agent with a shell uses `git`;
  one without uses the REST read routes below with its token.

## Anti-goals

- **Anyone reaching repository content without the site's credentials.** No
  anonymous clone, ever — also not for a public site. A site is public; its
  source is not.
- **Running anything from a repository on the server.** No hooks, no
  repository config, no filters or attributes from pushed content.
- **Serving repository content as a page from any origin.** Not on the view
  domain, not on a custom domain, not on the app origin (downloads are
  attachments).
- **Losing a repository to an error or a downgrade.** A failed plan lookup
  is *unknown* and changes nothing; a plan without Git makes the repository
  **read-only**, never deleted. Only deleting the site, its expiry, the
  operator's purge of a locked site, or the owner's explicit "Delete
  repository" removes it.
- **OAuth tokens spreading.** They act on `/mcp` and on the Git endpoint, and
  nowhere else.
- **A push holding up the site.** No site lock is held while a pack streams.

## Storage

- A **bare repository at `sites/<site folder>/git/`**, beside `files/`,
  `meta.json` and `provenance.jsonl`. Outside `files/`, so it is never
  served, listed, zipped by "Download ZIP", mounted over WebDAV or FTP, or
  scanned as content. `sitebin backup` walks the whole data root, so the
  repository is in every backup already — a test pins that.
- **Created lazily** by the first authorized Git request on a site that may
  use Git (`git init --bare --initial-branch=main`). Cloning an empty
  repository works ("You appear to have cloned an empty repository"), so
  `git clone` is a valid first step as much as `git push`.
- **Git is the system binary** (`apk add git` in the runtime image; Alpine
  3.22 ships a protocol-v2 Git). The handler routes smart HTTP itself —
  `GET info/refs?service=…`, `POST git-upload-pack`, `POST git-receive-pack`
  — and runs `git upload-pack|receive-pack --stateless-rpc
  [--advertise-refs] <dir>`, passing `GIT_PROTOCOL` through for v2 and
  decoding a gzip request body. Sitebin keeps authentication, quota, locking
  and provenance in Go; Git only speaks the protocol. (`git http-backend` over
  CGI is the alternative; the builder may use it if every rule below still
  holds.)
- **Every invocation runs with a fixed, server-owned environment** —
  `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`, `HOME` an empty
  directory, `GIT_TERMINAL_PROMPT=0`, `GIT_ATTR_NOSYSTEM=1` — and passes its
  settings with `-c`, so the repository's own `config` file cannot weaken
  them even if it were edited on disk:
  - `core.hooksPath` = an empty, Sitebin-owned directory;
  - `receive.fsckObjects=true`, `transfer.fsckObjects=true`;
  - `receive.maxInputSize` = this push's byte allowance (Quota);
  - `receive.autogc=false` (the sweep collects);
  - `core.logAllRefUpdates=false`;
  - `uploadpack.allowAnySHA1InWant=false`;
  - memory bounds (`pack.threads=1`, `pack.windowMemory`,
    `core.packedGitLimit`), values the builder's call.
  - Force pushes and branch deletion are **allowed**: it is the owner's
    repository.
- **Instance settings** (names the builder's call):
  - `SITEBIN_GIT_ENABLED` (default `true`);
  - `SITEBIN_GIT_MAX_PUSH_BYTES`: a hard ceiling per push, whatever the quota
    (default 512 MiB);
  - `SITEBIN_GIT_MAX_PROCESSES`: the most Git processes at once, instance-wide
    (default 4). A request over it waits briefly, then gets a readable "busy,
    try again".
  - No `git` binary on the path at startup: Git is off, logged once, and the
    tab says "not available on this instance".
- **One push per site at a time**, through a per-site Git lock that is NOT
  the site lock. A second concurrent push is refused with a readable message.
  Fetches run concurrently.
- **Garbage collection** runs in the cleanup sweep, one repository at a time,
  with a timeout, and never on a locked site.
  - `git gc --auto` is not enough. It does nothing below 6700 loose objects
    or 50 packs, so a force push would never give its bytes back (builder
    check).
  - So a repository **changed since its last collection** gets a full
    `git gc --prune=1.day.ago --quiet`. "Changed" means a push was recorded
    after the time stored in a Sitebin-owned marker file in the repository.
    An unchanged repository costs nothing.
  - The result: the bytes of history a force push replaced come back within
    a day or two. The tab says so, and "Delete repository" frees them at
    once.
- Request duration is bounded by the existing server `ReadTimeout`
  (10 minutes). The docs say a very large first push may need to go in parts.

## Quota

- **The byte cap covers files and repository together:**
  `bytes(files/) + bytes(git/) ≤ EffMaxBytes`. The file-count cap is
  unchanged; pack files are not files anyone uploaded.
- **One store function counts a site's bytes** and reports both parts (files,
  repository) and their sum. Every **write** cap check uses the sum. A table
  test lists every byte check, in the spirit of
  `TestEveryWriteSurfaceIsScanned`, so a new funnel cannot forget the
  repository. The funnels do not all look alike today (builder check):
  - `SaveFile` and `ExtractZip` call `usage(site.ContentDir())`: they switch
    to the shared function.
  - **A staged replace** (`internal/store/replace.go`) fixes its budget when
    it begins and `Commit` checks no bytes: the budget becomes cap minus the
    repository's bytes at that moment.
  - **WebDAV**'s byte check is in `webdav.go` (~128–145); `webdavfs.go` uses
    `Usage` only for the file count.
  - **FTP** has its own walker (`internal/ftp/quota.go`), and `ftp.Session`
    carries only `Dir`, `MaxBytes` and `MaxFiles`. It gains a callback for the
    bytes held outside `Dir`, asked at every check: a figure read at login
    would be stale for the whole session.
- **What the site reports** is the same sum. The `bytes` that the edit page,
  `GET /api/sites/{id}` and MCP `get_site` show beside `max_bytes` is the
  figure counted against the cap, so an agent's arithmetic stays right. No
  field is added to MCP output, because a connector with a cached output
  schema would reject one. The edit page shows the split from the Git route.
  `/docs/api/` and `/docs/mcp/` say that `bytes` includes the repository.
- **The one exception: the container runtime's stop rule.**
  `enforceStorage` (`ee/containers/manager.go` ~1143–1158) stops a running
  project whose bytes exceed the cap, and it keeps counting **files only**.
  A push must never stop an app ("nothing served changes"), and the app
  cannot write into the repository anyway. While the sum is over the cap,
  uploads and pushes are refused, and the app keeps running. The table test
  names this exception.
- **The ownership limits check uses the same function** (agreed with the
  ownership session on 2026-11-03), so a transfer counts the repository
  without knowing about it.
- **A push's allowance** is `min(cap − current bytes, SITEBIN_GIT_MAX_PUSH_BYTES)`,
  given to Git as `receive.maxInputSize`. Git refuses a larger pack itself
  ("pack exceeds maximum allowed size"). With no allowance left, the push is
  refused before Git runs, with "storage full: X of Y used".
- A pack can leave the site slightly over its cap (concurrent uploads, pack
  overhead). Then further uploads and pushes are refused until it is under
  again. Nothing is deleted.
- The edit page's usage bar shows both parts: "Files X · Git Y · of Z".

## The gate

**Where:** `/git/<edit-id>.git/…` on the **base domain only** (Caddy already
proxies every base-domain path to the backend). Never on the view domain or
a custom domain: those are user-content origins.

**Credentials** come as HTTP Basic, the only scheme Git sends by itself. The
username is not checked (the tab suggests the drop's id). **The password's
shape decides**, and a credential that fails is refused — it never falls back
to a weaker one:

1. `sbu_…` (upload token): refused. It opens only its own routes.
2. `sbp_…` (account token): its account, if that account owns the site
   (and, once ownership ships, if it administers it) — the same `tokenOwns`.
3. A JWS compact string (three base64url segments; edit passwords are 22
   base62 characters and never contain a dot): an **OAuth access token**,
   only where MCP OAuth is configured (enterprise). See OAuth.
4. Anything else: the site's **edit password**, through `verifyEditIP` (its IP
   limits and verify cache).

No credential is `401` with `WWW-Authenticate: Basic realm="Sitebin"`, which
makes Git ask its credential helper. **Browser sessions are not accepted**
here (no cookie path, so no CSRF surface). **One rule, not two:** the Git
handler turns Basic into the same credential `withEditAuth` evaluates and
calls the same core. The builder must not restate the ownership comparison.

**After authentication, in this order:**
- a **locked** site is refused (`refuseLocked`), reads included — the lock
  is an evidence hold, and the repository is frozen with it;
- a site whose plan does not allow Git (see Plan gate): reads are allowed if
  a repository exists, a push is refused, and there is no repository to
  create;
- an **anonymous site on an accounts instance** has no Git, like WebDAV and
  FTP (`gatedAnonymous`);
- **scopes** (OAuth only): `sitebin:sites:read` to fetch,
  `sitebin:sites:write` to push.

**Readable refusals.** Git prints nothing useful for a bare 401/403. A
refusal *after* successful authentication is therefore answered on
`info/refs` as `200` with a service advertisement that carries
`ERR <message>`, which Git prints as `fatal: remote error: <message>`. The
messages are for: storage full, plan without Git (with the upgrade URL),
locked site, another push running, instance busy, and **consent outstanding**
(OAuth). **Verified by a spike (2026-11-03, git 2.49.0.windows.1):** the body
`pkt("# service=<svc>\n") + "0000" + pkt("ERR <message>\n")` with
`Content-Type: application/x-<svc>-advertisement` makes `ls-remote`, `clone`
and `push` all print `fatal: remote error: <message>`, for protocol v0 and for
a v2 request (`Git-Protocol: version=2`) alike. Git first asks without
credentials, gets the `401` and asks again with them, even when the URL
carries them. The e2e repeats this against the Linux git in the image.

**Admins (after ownership).** An admin may use Git exactly as the owner may,
force pushes included. **"Delete repository" is owner-only**, in the
ownership design's owner-only list, beside "delete the site". The ownership
session sends the final gate names before it builds that gate.

## Plan gate

- A tier flag **`git`** (bool) in `tiers.json`. Absent means **false**.
- It is **stamped**, like `webdav`: `store.Quota` gains `Git *bool`, the site
  `Meta.QuotaGit *bool` (`json:"quota_git,omitempty"`), and `ext`'s grant
  carries it. Where it is written:
  - **at creation.** Creation does not build a `store.Quota`; it writes the
    stamp fields into `Meta` directly (`internal/httpapi/sites.go` ~690–698),
    so `QuotaGit` is added there. Adoption reuses that path.
  - **by tier restamping** (`ApplyQuota`), and so by transfer acceptance.
    Every constructor of `store.Quota` passes it, or `ApplyQuota` resets it
    (the trap the `Forms` field already documents).
  - **by a reconcile pass in the sweep, `reconcileGit`**, modelled on
    `reconcileTrust` (`internal/cleanup/cleanup.go`). For every owned site it
    asks `QuotaFor` (cached per account by the extension) and, when the
    grant's `git` differs from the stamp, writes **only `QuotaGit`**. It never
    calls `ApplyQuota`, so no other cap and no expiry is touched. An error or
    an unknown tier changes nothing.

    This is what turns Git on for **existing** paid sites once the operator
    adds `"git": true`: `syncTier` and `restampSites` run only when a tier
    *id* changes, never when a tier's definition does. It also brings a
    tiers.json edit to every site within one sweep (`SITEBIN_CLEANUP_INTERVAL`).
    The ship order's verification waits for that sweep (or a restart, if the
    sweep runs at start).
- **Accounts mode without tiers** (`SITEBIN_ACCOUNT_MODE=accounts`): its grant
  (`QuotaFor`, `grantForAccount`) stamps `git: true` for every live account,
  as the ownership design lets everyone share there. Git then follows
  `SITEBIN_GIT_ENABLED` alone.
- **In effect** when `SITEBIN_GIT_ENABLED` holds, a `git` binary exists, the
  site is not `gatedAnonymous`, and:
  - with accounts enabled: `QuotaGit` is `true` (nil — a site not yet
    restamped — counts as off);
  - without accounts (community, enterprise open mode): `QuotaGit` is nil or
    `true`.
- **Hosted values:** `pro`, `studio`, `admin`, `unlimited` get `"git": true`;
  `free` and `drop` do not. An operator step in the ship order.
- **Downgrade** to a plan without Git: the repository stays and stays
  readable — clone, fetch, the tab, ZIP — so the owner can take it with them.
  Pushes are refused with the reason and the upgrade link. An upgrade
  restores pushing at the next restamp. Nothing is ever deleted for a plan.

## Lifecycle, and saying so

- **Deleting a site deletes its repository.** So does its expiry (the sweep
  deletes the site folder). A locked site keeps its repository as evidence,
  frozen, until the site is purged.
- **Said where it matters:**
  - the Git tab carries a standing line: "This repository belongs to this
    site. Deleting the site deletes the repository." With an expiry date
    set, it names it: "… The site expires on <date>; the repository goes
    with it.";
  - every delete-site confirmation names it when a repository exists ("…
    and its Git repository (12 MB)");
  - after a downgrade, the tab says the repository is read-only and why;
  - the docs page and the pricing page (Website).
- **"Delete repository"** in the tab (owner only, confirmed): removes `git/`,
  frees its bytes, recorded in provenance. `DELETE /api/sites/{id}/git`.

## Provenance

- Every push is recorded at the gate (`recorded`-style): action `git-push`,
  the refs it changed (`refs/heads/main abc1234→def5678`, created, deleted,
  forced), bytes received, credential kind, acting account.
- "Delete repository" is recorded. Reads are not, as for files today.

## Transfer, sharing, adoption

- **Nothing to copy.** The repository lives in the site folder and moves with
  the owner. The ownership design already rotates the edit password and
  revokes the old owner's tokens and shares, and the Git endpoint uses exactly
  those credentials. So a Git Credential Manager that cached the old edit
  password is refused.
- The transfer's limits check counts the repository through the shared byte
  function. A recipient whose plan lacks Git gets a read-only repository (in
  practice every paid plan has Git).
- An anonymous drop on the hosted instance never has a repository: the
  anonymous tier has no `git`.

## The Git tab

**The tab bar**, shared with later tabs (analytics, session
`sitebin-project-b9`, agreed 2026-11-03):

- `edit.js` gains a small tab mechanism: `tabs.register({id, label, panel,
  onShow})`.
- The bar shows **Live** (every existing card, unchanged, the default) and
  **Git**.
- The URL hash selects the tab (`#live`, `#git`, later `#analytics`), so the
  back button and links work.
- **Lazy loading:** only the active tab loads, and `onShow` runs on every
  showing; the tab decides whether to reload.
- It is accessible: `role="tablist"`, `aria-selected`, and arrow keys.
- The bar is hidden while the page asks for the edit password.

**What the Git tab shows, top to bottom:**

1. **Connect.**
   - The clone URL with a copy button, and the two commands
     (`git remote add sitebin <url>`, `git push sitebin main`).
   - The credential choices that apply on this instance:
     - edit password (username: the drop's id);
     - an API token, with a link to the dashboard's tokens;
     - "Sign in with your account": the `git config` lines to copy (see
       OAuth), and a note that the Git Credential Manager comes with Git for
       Windows, with an install link for macOS and Linux.
   - The page never shows the edit password itself (it is not stored in
     clear).
2. **Status.**
   - The repository's size, inside the usage bar shared with the files.
   - The last push: when, which refs, and who (credential kind) — from
     provenance.
   - The default branch.
   - Notices: empty repository (the commands to start), plan without Git
     (read-only, upgrade link), and the lifecycle line.
3. **Branches and tags.** Each with its tip commit (date, subject); the
   default branch is marked. Choosing one sets the ref for 4 and 5.
4. **History** of the chosen ref.
   - 30 commits a page, then "Show more", as in the folder browser: short
     hash, subject, author, date.
   - A commit opens its detail: full message, author and committer, parents,
     and the changed files with added and removed line counts (at most 300
     files listed). **No inline diff in v1.**
5. **Files** at the chosen ref or commit.
   - Folder by folder, with breadcrumbs and Back, like the Files card.
   - A text file (up to 512 KB) is shown as text, highlighted with the
     already vendored `highlight.js`.
   - A larger or binary file shows its size and a download link.
   - "Download ZIP" of the ref (`git archive --format=zip`).
6. **Delete repository** (owner only).

**Display safety.** Repository content reaches the page only as JSON and is
inserted as text. highlight.js escapes its input. Every download is
`Content-Disposition: attachment`, `application/octet-stream`,
`X-Content-Type-Options: nosniff`, and `Content-Security-Policy: sandbox`.
Nothing from a repository is ever rendered as markup on the app origin.

## REST read routes

All per-site, behind `withEditAuth`: edit password, account token, the
owner's browser session, and an admin once ownership ships. JSON, documented
on `/docs/api/`.

- `GET /api/sites/{id}/git`: availability, `writable` and the reason if not,
  `url`, `exists`, `bytes`, the default branch, the last push, and the OAuth
  settings to show (or `null`).
- `GET /api/sites/{id}/git/refs`: branches and tags with their tip commits.
- `GET /api/sites/{id}/git/log?ref=&after=`: 30 commits a page.
- `GET /api/sites/{id}/git/commit/{sha}`: message, people, parents and
  changed files.
- `GET /api/sites/{id}/git/tree?ref=&path=`: a folder's entries (name, type,
  size).
- `GET /api/sites/{id}/git/blob?ref=&path=`: size, binary flag, and the text
  up to 512 KB. `&download=1` sends the file as an attachment.
- `GET /api/sites/{id}/git/archive?ref=`: a ZIP attachment.
- `DELETE /api/sites/{id}/git`: owner only.

**Rules every route keeps:**

- A `ref` is resolved with `git rev-parse --verify --end-of-options
  <ref>^{commit}`, and anything starting with `-` is refused.
- A path is cleaned like `CleanRelPath` and passed after `--`.
- Every Git command has a timeout (15 s for reads) and an output cap.
- A locked site is refused.

## OAuth (enterprise, through the stack): the last build step

The operator called this optional ("nur eine idee … muss aber nicht sein");
the agent checked it works and kept it as the last step. **Facts, verified
2026-11-03:**

- **Git Credential Manager** has a generic OAuth mode for any host. It
  supports authorization code with PKCE (S256, loopback redirect) and the
  device code flow. It stores the refresh token and renews by itself, and
  sends the access token as the Basic password with the username
  `OAUTH-USER`. This was read in GCM's source (`GenericHostProvider.cs`,
  `OAuth2Client.cs`). It ships with Git for Windows.
- **Sitebin's token check** (`ee/mcpoauth.go`) verifies issuer, `typ`, the
  audience (the MCP resource, added by the `sitebin:sites:*` scopes' audience
  mapper), and the stack's consent lock. A token from a new client that
  requests those scopes passes it unchanged. **A Git token also works on
  `/mcp`:** same person, same scopes. This is an agent call.
- **The stack's MCP gate** (`saas-stack/docs/mcp-consent-gate.md`) resolves
  the app from a requested `sitebin:sites:*` scope when no `resource` is
  sent, which is GCM's case. It accepts any client except `sitebin-app`. So
  pointing GCM's authorize endpoint at the gate shows the consent screen.
- **The prod realm `saas-stack`** (Keycloak DB, read-only, 2026-11-03):
  - SSO session idle 30 min, max 10 h;
  - offline session idle 30 days, no max lifespan;
  - `offline_access` is a realm default-optional scope, and the default roles
    include it;
  - device codes live 10 min.

  So GCM requests **`offline_access`**: a person stays signed in as long as
  they use Git at least every 30 days, and can revoke it in the account
  console.
- **The trap:** when the server refuses a token, GCM keeps the refresh token
  (`EraseCredentialAsync` removes only the stored credential). A token refused
  for an **outstanding document** (for example the ownership design's new
  terms version) would therefore loop without ever opening a browser.
  - So that refusal is the readable `ERR`: "Sign in once at <base> and accept
    the updated terms".
  - Signing in to the dashboard passes the gate. The next push works with the
    same refresh token, because refusals are never cached.
  - The device flow skips the gate page but not the lock, and gets the same
    message.
  - `mcpOAuth.Verify` learns to say *why* it refused (consent outstanding),
    without changing what `/mcp` answers.

**Stack:** a static client **`sitebin-git`** in the realm, dev and cloud,
created convergently by the stack's own code, never by hand.

- **How it is declared.** Sitebin already sends its MCP resource and scopes
  in its registration (`ee/stackreg.go`, `mcp` block), and the gateway's
  `configureMcp` (`services/auth-gateway/src/adapters/keycloak/index.ts`)
  converges them on every boot. The `mcp` block gains a CLI client, for
  example `cliClient: {clientId, displayName}`, which Sitebin sends only when
  `SITEBIN_GIT_OAUTH_CLIENT_ID` is set. `configureMcp` creates the client or
  brings it in line, beside the scopes.
- **The stack ships first.** The registration schema is strict: a stack that
  does not know the field refuses the whole registration.
  `offline_access` is already among the stack's `MCP_BASE_CLIENT_SCOPES`.

The client:

- public, standard flow with PKCE S256 required;
- redirect URIs `http://127.0.0.1/*` and `http://localhost/*` (loopback, any
  port);
- the device authorization grant enabled;
- optional scopes `sitebin:sites:read`, `sitebin:sites:write` and
  `offline_access`;
- Consent Required on.

A static client means no DCR, so the DCR/CIMD standing task is not affected.

**Sitebin:**

- `SITEBIN_GIT_OAUTH_CLIENT_ID` (`sitebin-git`) turns the tab's OAuth option
  on.
- The endpoints come from the issuer's path-insertion discovery, which names
  the gate's `authorization_endpoint`, plus the realm's token and device
  endpoints.
- The tab shows, for this instance:

  ```
  git config --global credential.https://app.sitebin.io.oauthClientId sitebin-git
  git config --global credential.https://app.sitebin.io.oauthAuthorizeEndpoint <gate authorize>
  git config --global credential.https://app.sitebin.io.oauthTokenEndpoint <token>
  git config --global credential.https://app.sitebin.io.oauthDeviceEndpoint <device>
  git config --global credential.https://app.sitebin.io.oauthScopes "openid offline_access sitebin:sites:read sitebin:sites:write"
  git config --global credential.https://app.sitebin.io.oauthUseClientAuthHeader false
  ```

  The builder verifies the exact lines against the real Keycloak, including
  whether a public client needs `oauthUseClientAuthHeader false`.
- The tab notes that this config makes OAuth the way in for every repository
  on the host, on that machine.

**The OAuth rule changes in one sentence:** "OAuth tokens act on `/mcp`
only" becomes "on `/mcp` and on the Git endpoint only".

- The Git route marks its requests the way `/mcp` does; the builder either
  generalises `ext.WithMCPCaller` or adds a sibling.
- A test keeps the REST API refusing OAuth tokens.
- The repo guide's MCP section is updated in the same change.

## Website (coupled)

- **Pricing:** "Git repository per site" under Pro and Studio, not Free. It
  must agree with `tiers.json`'s `git`.
- **Docs:**
  - a new page `/docs/git/` covering the three ways in, the quota, the
    lifecycle ("deleting the site, or its expiry, deletes the repository"),
    downgrade, transfer, the limits (push size, 10 minutes), and "no deploy
    from Git";
  - `/docs/api/`: the read routes;
  - `/docs/containers/`: keep the app's source in the site's repository.
- **Terms:** none planned. The repository is part of the site's content. The
  builder checks that `/terms/` covers a site's content being deleted with
  it. If the terms speak only of "files", the wording joins the next terms
  version, which the operator approves.

## Ship order

This is a tiers change (`git`), so the fixed order applies:

1. **Product.** Merge and push, after ownership.
2. **app.sitebin.io:**
   - set `"git": true` for `pro`, `studio`, `admin` and `unlimited`;
   - deploy (the image now contains `git`; the pre-deploy snapshot goes into
     `backups/`; keep 3 rollback images; prune the build cache);
   - verify with a real paid account: push, clone, the tab, ZIP, quota
     refusal, "Delete repository".
3. **OAuth.**
   - Stack: `sitebin-git` on dev, then cloud, following the stack's own deploy
     procedure.
   - Set `SITEBIN_GIT_OAUTH_CLIENT_ID`.
   - Verify GCM end to end with a throwaway user: browser and device flow,
     then a pending document producing the readable `ERR`.
4. **Website:** pricing and docs.
5. **Tell `sitebin-project-b9`** that the tab bar is on `main`.

## Tests the builder must include

- **Gate:**
  - routing by the password's shape;
  - a bad token never falls back to the edit password;
  - `sbu_` is refused, and no credential gets the Basic challenge;
  - browser cookies are ignored;
  - locked sites are refused, reads included;
  - a plan without Git refuses a push and allows reads;
  - an anonymous site on an accounts instance is refused;
  - OAuth read and write scopes;
  - an OAuth token works on Git and `/mcp` and is refused by REST.
- **Safety:**
  - hooks never run: a `hooks/` directory planted in the repository, a
    pushed `.githooks`, and `core.hooksPath` written into the repository's
    config;
  - option injection through `ref` and `path` (`--upload-pack=…`, `-x`,
    `..`) is refused;
  - downloads are attachments with nosniff and the sandbox CSP;
  - only one push runs per site at a time.
- **Quota:**
  - every byte check counts the repository (the table test);
  - a push gets the right `receive.maxInputSize`;
  - a full site refuses a push before Git runs;
  - an upload is refused when the repository fills the cap.
- **Readable errors:** a real `git` client prints each `ERR` (skipped where
  no `git` is installed), protocols v0 and v2.
- **Stamping:**
  - `git` passes through every `store.Quota` constructor and the creation
    path;
  - `reconcileGit` backfills an existing paid site, touches no other cap or
    expiry, and changes nothing on a lookup error;
  - accounts mode without tiers allows Git;
  - a downgrade makes the repository read-only, and an upgrade restores
    pushing.
- **Containers:** a repository that pushes the sum over the cap does not stop
  a running app (`enforceStorage` counts files only).
- **Reports:** `bytes` in the API and in MCP `get_site` includes the
  repository, and the MCP output schema is unchanged.
- **gc:** after a force push and the sweep, the repository's bytes shrink.
- **Lifecycle:**
  - deleting the site removes the repository;
  - a locked site keeps it;
  - backup includes it;
  - the sweep's `gc` skips locked sites.
- **Community build:** push and clone with the edit password, with no
  provider registered.
- **E2E:**
  - a new `e2e/git.ps1` (community image, ASCII only): push, clone, fetch,
    force push, quota refusal, the read routes, delete;
  - the token and plan parts in `accounts.ps1` and `tiers.ps1`.

## Left to the builder

- `http-backend` versus direct `--stateless-rpc` (direct preferred).
- The setting names, the memory bounds, the busy wait.
- The JSON shapes of the read routes, within the fields named.
- Whether `last push` is read from provenance or kept beside it, provided it
  is not a second source of truth that can disagree.

## Calls made without asking (the operator delegated them)

- **Transport and access:**
  - HTTPS smart HTTP only; no SSH. The remote URL uses the edit id, like the
    API.
  - The username is not checked, and the password's shape picks the
    credential.
  - No anonymous clone, even of a public site.
  - Browser sessions are not accepted on the Git endpoint.
- **What a repository may do:**
  - Force pushes and branch deletion are allowed.
  - No hooks and no server-side repository config.
- **Quota and limits:**
  - One byte cap for files and repository together; the file count is
    unaffected.
  - At most 512 MiB per push, one push per site at a time, 4 Git processes
    per instance.
  - `gc` runs in the sweep, and unreachable objects are pruned after one day.
- **Plan and lifecycle:**
  - A downgrade makes the repository read-only, never deletes it.
  - "Delete repository" exists and is owner-only.
  - A locked site's repository is frozen, reads included.
- **Scope of v1:**
  - Read-only UI without inline diffs; text files up to 512 KB are
    highlighted; ZIP download of any ref.
  - REST read routes yes, MCP tools no.
  - No terms change unless the current wording does not cover it.
- **OAuth:**
  - It is the last build step, with a static stack client and
    `offline_access`.
  - A Git token is also valid on `/mcp`.
  - Consent refusals are a readable `ERR`.
- **Shared with other sessions:**
  - The tab bar, with `onShow` and hash links, is shared with analytics.
  - Bytes are counted by one function, shared with ownership.
- **From the builder check:**
  - Existing paid sites are switched on by a sweep pass that writes only
    `quota_git`.
  - Accounts mode without tiers allows Git.
  - The container stop rule ignores the repository.
  - The reported `bytes` includes the repository, with no new MCP field.
  - A changed repository gets a full `gc` with a one-day prune.

## Before planning: Git comes before ownership

**Changed 2026-11-03, the operator's decision:** tags and the front door are
merged and live (`462c0f9`). **Ownership is not built yet**, and Git is built
now, ahead of it. So:

- Git ships **without admins**. Every rule above that names an admin or
  "once ownership ships" applies when ownership is built. **Whoever builds
  ownership integrates Git:**
  - the admin standing reaches the Git endpoint through the shared gate core;
  - `DELETE /api/sites/{id}/git` joins the owner-only list;
  - transfer and adopt stamp `quota_git` through the existing paths;
  - the limits check uses the shared byte function.

  The ownership session was told so on 2026-11-03.
- Until then "owner-only" for "Delete repository" means: whoever passes the
  gate today (the edit password, the owner's token, the owner's session),
  because today every one of them is the owner.
- The plan is written against `main` at `462c0f9`.

## Corrections (post-implementation, final review 2026-11-04)

The final whole-branch review changed these rules; the sections above are
left as designed.

- **gc runs once more after the prune window.** A collection minutes after a
  push (the sweep's interval) kept what the push replaced, which was younger
  than a day, and then never ran again. A repository is now also collected
  when its last collection began within a day of its last push and that day
  has passed. gc prunes from the runner's clock (`--prune=<start - 24 h>`,
  the same cutoff as `1.day.ago`). What an interrupted push left in the object
  store (`tmp_objdir-*`, `incoming-*`, `pack/tmp_*`) is removed by the sweep
  inside the site's registration, and such a push marks the repository.
- **Memory bounds are fixed values:** `GIT_ALLOC_LIMIT=256m`,
  `core.bigFileThreshold=64m`, `transfer.unpackLimit=1` (and
  `receive.unpackLimit=1`). A pushed delta that claims gigabytes dies at once;
  a push stays a pack. A single file above 256 MiB pushes whole but is refused
  when Git sends it as a delta against an older version of itself (checked in
  the Alpine image: 100 MiB pushes, clones, collects and zips; 300 MiB pushes
  whole, its changed version is refused, and
  `git -c core.bigFileThreshold=64m push` sends it whole and passes; a clone
  that already stores the file as a delta needs
  `git -c core.bigFileThreshold=64m repack -adf` first, because pack-objects
  reuses a stored delta whatever the threshold). The limit also caps
  index-pack's and pack-objects' per-object arrays: a repository past roughly
  two million objects can no longer be cloned or collected (accepted; no
  website repository gets near it).
- **Downloads have the same write bound** (re-review): a file or ZIP download
  gets the 2-minute rolling write deadline, capped at `StreamTimeout`, so a
  client that stops reading frees its process slot.
- **The sweep also releases what a killed receive-pack kept**
  (`pack-*.keep` starting `receive-pack `) and removes a killed repack's
  `pack/.tmp-*`.
- **`GIT_DIR`** names the repository in every git process's environment.
- **Benign fsck messages** are ignored on push: `zeroPaddedFilemode`,
  `badTimezone`, `missingSpaceBeforeDate`.
- **Responses have write deadlines:** 2 minutes without progress per write,
  30 minutes per answer; then git is cancelled and its slot freed.
- **RPC requests** must carry `application/x-git-<svc>-request` (else 415), and
  every answer carries `X-Content-Type-Options: nosniff` and
  `Content-Security-Policy: sandbox`.
- **Provenance:** `git-push` entries are never merged.
- **"Last push" is one event** (found in the live check, 2026-11-04): its time
  is the recorded push's, beside that push's refs. The `sitebin-pushed` marker
  is the sweep's, and a push cut off after git started moves it without
  changing anything; it is shown only when no record is left.
- **The GCM lines are scoped to the instance's origin** (Git passes a
  credential helper only scheme and host), as the example lines above show;
  the tab, README and the website's `/docs/git/` print the same lines and say
  they apply to every repository on the host, on that machine.
