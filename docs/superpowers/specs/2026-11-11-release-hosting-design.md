# Release hosting: large uploads, WebDAV folders, file types, cache rules, atomic uploads

Date: 2026-11-11. Asked for by the Atoo Mail session, which publishes the
desktop app's update feed (installers of 150–200 MB, `.blockmap`, `latest*.yml`
for electron-updater, `SHA256SUMS`) from a Sitebin site
(`updates.ittrail.at/atoo-mail`). The operator's rule: what update hosting needs
is built into Sitebin. Two deploys: A (points 1–3, bugs), then B (points 4–5,
features).

## 1. An upload past the instance's default cap answered 500

**Cause.** `uploadFiles` and `createSite` wrapped the body in
`http.MaxBytesReader(a.cfg.MaxSiteBytes + 10 MiB)` — the instance's DEFAULT
cap (100 MB), not the site's own, which the plan stamps (`Meta.QuotaBytes`;
the operator's plan holds 1 TiB). A 198 MB installer was cut off at
115,343,360 bytes, and `*http.MaxBytesError` fell through `storeError` to
`500 internal error`. The zip spool had the same default as its bound. WebDAV
was right all along: it bounds a PUT by the site's remaining room.

**Fix.**
- The body limit is `uploadBodyLimit(site)` = `EffMaxBytes(site)` +
  `uploadFormRoom` (10 MiB for boundaries and fields). In `createSite` it is
  set inside `fill`, after the site exists and is stamped — nothing reads the
  body before.
- The zip spool is bounded by the site's cap (`consumeUploads` passes it).
- A body past the limit is `413` naming the limit
  (`one request to this site may carry at most …`), between parts too
  (`bodyError` classifies `NextPart` errors before "malformed").
- A body still arriving when the public server's `ReadTimeout`
  (`httpapi.UploadReadTimeout`, 10 minutes, now the one constant `cmd/sitebin`
  uses) runs out is `408` saying so, not `500`.

A failed upload can leave the empty folders its path named (as before);
they hold nothing and count for nothing.

## 2. WebDAV PUT into a missing folder answered 409

RFC 4918 allows (and x/net/webdav does) `409` for a PUT whose parent
collection is missing; the multipart API creates folders as it goes. Scripts
that publish file by file over an upload token needed a `MKCOL` per folder.

**Decision:** a PUT creates the folders its path names (`siteFS.parentFolders`,
like nginx's `create_full_put_path`). Clients that MKCOL first are unaffected.
The name has passed `check` (every segment a name the store accepts), the
folders are made through the content `os.Root` (never through a link out of
the site), and a locked site refuses (`unlocked`). A file where a folder
would go stays `409`.

## 3. Release files had no Content-Type

Caddy's `file_server` types by extension from the system's table (mailcap's
`/etc/mime.types` in the image) and, finding none, sends NO Content-Type — on
purpose, so nothing is sniffed into a page. mailcap has no `.dmg`, `.yml`,
`.yaml`, `.AppImage`, `.msix`/`.appx`(bundle), `.appinstaller`, `.msi`,
`.blockmap`, `.7z`, `.sha256`/`.sha512`/`.minisig`, and nothing can name a file
without an extension (`SHA256SUMS`, a Linux binary).

**Decision:** authz names those, Caddy sets them. authz already resolves the
request against the served folder for the cache policy (`servedFile`, now
shared); `typeValue` answers `X-Sitebin-Type` for an existing regular file
when:
- its extension is in `extraTypes` (ctype.go) — Sitebin's table, which wins
  over the system's for those few extensions; every other extension stays
  Caddy's, unchanged;
- it has no extension: its first 512 bytes decide between `text/plain`
  (`http.DetectContentType` says text) and `application/octet-stream`.
  Never anything else: an HTML file without an extension is a download, so
  no upload is sniffed into a page the scanner did not treat as one.

Caddy strips a client's copy before the gate (every content block and the
path views), copies authz's answer, and sets `Content-Type` from it on a
**2xx only** — a 404 page, the gate and redirects keep their own. authz
answers container sites before any of this, so a proxied answer keeps the
app's type. `.yml`/`.yaml` are also documents for the cache policy (they
always revalidate, like `.json`): `latest*.yml` is the feed electron-updater
polls.

## 4. Cache rules per path (deploy B)

The site's policy (`Meta.Cache`) is one value for every file. A release
folder wants installers cached for a year and the feed revalidated.

**Decision:** `Meta.CacheRules []CacheRule{Pattern, Cache}` — at most 20, the
first that matches decides, no match falls back to the site's policy.
- `pattern` is a `path.Match` glob over the path inside the site
  (`stable/*.exe`); a pattern without `/` matches the file name in any folder
  (`*.dmg`). Validated on write (syntax, length ≤ 200, no leading `/`).
- `cache` is any value of the site's policy plus `immutable` (a year +
  `immutable`).
- Rules decide for existing regular files that are NOT documents. A page
  (folder, fallback, 404) and a document (`.html`, `.json`, `.xml`, `.txt`,
  `.webmanifest`, `.yml`, `.yaml`, no extension) always revalidate, rules or
  not: `stable/*` → `immutable` must not freeze `stable/latest.json`.
- A rule matches even when the site's policy is `off`.
- API: `cache_rules` in `PUT /api/sites/{id}` (array of `{pattern, cache}`, `[]`
  clears) and in the site payload. MCP: `update_site` `cache_rules`; the
  output carries it `omitempty` (cached output schemas). Edit page: Behavior →
  Browser caching → rules, one per line `pattern policy`. Admins may set it.

The semver-in-the-name heuristic was rejected: a hand-named file can be
re-uploaded under the same name (a botched build), and `fingerprinted` must
never match one — a false positive is a year of stale content.

## 5. Atomic uploads (deploy B)

A release is "installers first, manifest last"; a failed upload half way left
the installers that arrived. `?replace=true` is atomic but replaces the WHOLE
site.

**Decision:** `POST /api/sites/{id}/files?atomic=true` stages every file of
the request (the replace machinery, `Store.BeginMerge`) and commits them only
when all arrived — none if any failed, was cut off, scanned as held or does
not fit. The commit runs under the site lock: it re-reads meta.json (deleted,
locked), checks the caps against the live usage at that moment (bytes of
files it overwrites leave, staged bytes arrive; only new paths count as
files), settles the abuse verdict, moves the staging directory into the site
folder and renames each file into place **in the order the request carried
them**, through the site folder's `os.Root`. Renames are not one atomic step
for a reader, but they take microseconds and keep the order, so a feed
uploaded last is never visible before the files it names. One staged upload
per site at a time (replace or atomic): a second is `409`. `replace=true`
wins over `atomic=true` (it already is).

Not in MCP: `write_files` files arrive in one call. `open_upload`'s
instructions name `?atomic=true`.

## Corrections (post-implementation)

- **The account storage cap landed between deploys A and B** (08e2ba5,
  another session). Every write funnel checks `Store.ByteCap(site)` — the
  site's own cap, or what the owner's account has left — not `EffMaxBytes`.
  The merge commit asks `ByteCap` again under the site lock (the owner's
  other sites may have grown while the upload streamed), refuses with
  `ErrAccountFull` when the account bound it, and notes what the site holds
  after the commit in the account index. It joins `byteChecks`
  (`TestMergeCountsRepository`) and `accountChecks`
  (`TestMergeCommitCountsTheAccount`) in `internal/store/usage_test.go`.
- The upload body limit of point 1 stays the site's own cap plus 10 MiB: the
  account cap is enforced per file by the store, and a body bound by it
  would refuse a valid overwrite.

## Not done

- Bandwidth and load for downloads (point 6): deliberately deferred by the
  requester; the answer is a move or cluster mode.
