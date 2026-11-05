# Not-found pages Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** No Sitebin site ever serves a directory listing; a missing page answers 404 with the site's own not-found page or Sitebin's styled one, and the File viewer offers a download view for binaries.

**Architecture:** authz names the site's not-found page in a new header (`X-Sitebin-NotFound`) that Caddy copies like `X-Sitebin-Cache`; the generated Caddyfile drops `browse` and gains a `handle_errors 404` block per content site block that serves that file with status 404 when it exists and otherwise proxies to a new backend route rendering `msgPage(404, …)`. Settings, warnings and the edit page ride on the existing `updateSet` / `sitePayload` / `mcp.SiteResult` paths.

**Tech Stack:** Go 1.x (stdlib), Caddy v2.11.4 Caddyfile (generated), vanilla JS (edit page, viewer), PowerShell 5.1 e2e.

**Spec:** `docs/superpowers/specs/2026-11-05-not-found-pages-design.md`

## Global Constraints

- The header is `X-Sitebin-NotFound`; its value is a path starting with `/`, relative to the served site root (after any strip).
- Convention file: `404.html` at the site root. Setting: `not_found_page` (meta, JSON API, MCP), `""` = convention.
- Every 404 carries `Cache-Control: no-store`, status 404, and the same security headers a 200 of that site carries.
- A start page is a regular file `index.html` or `index.txt` at the content root (Caddy's default index names, verified against v2.11.4).
- Warning text (exact): `This site has files but no index.html at its root, so visitors see a 404 there. Upload an index.html, or set mode to viewer if the files are documents to show or download.`
- Styled page copy (exact): title `Page not found`, message `There is no page at this address. Check the link for typos.`
- `meta.json` field is `omitempty`; MCP `SiteResult.NotFoundPage` is `omitempty` (present only when set).
- `e2e/*.ps1` stay pure ASCII.
- Commits: lowercase conventional prefixes, behaviour sentences, `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Run BOTH `go test ./...` and `go test -tags ee ./...`.

## Review Focus

- A conditional or Range request to a missing path (`If-None-Match`, `If-Modified-Since`, `Range`) must still get the full 404 page body, never an empty 404 or a 206 — the spike showed an empty body without stripping those headers. Pinned in Task 7.
- A folder asked for WITHOUT its trailing slash (`/assets`) must 404 like `/assets/`, not redirect into a listing or a 200. Pinned in Task 7.
- A password-protected site's missing path without the cookie must still answer the 401 gate, not the 404 page (authz runs first). Pinned in Task 7.
- A viewer site: `/_raw/` answers the styled 404, `/_raw/<file>` keeps serving 200. Pinned in Task 7.
- `HEAD` on a missing path answers 404 with no body and the same headers. Pinned in Task 7.

---

### Task 1: Store — the setting, the target, the start-page check

**Files:**
- Create: `internal/store/notfound.go`, `internal/store/notfound_test.go`
- Modify: `internal/store/meta.go` (field beside `Cache`)

**Interfaces:**
- Produces:
  - `Meta.NotFoundPage string` — `json:"not_found_page,omitempty"`
  - `const DefaultNotFoundPage = "404.html"`
  - `var ErrBadNotFoundPage = errors.New("not_found_page must be the path of an .html file inside the site, e.g. 404.html")`
  - `func CleanNotFoundPage(s string) (string, error)` — trims space; `""` → `""`; strips ONE leading `/`; then `CleanRelPath`; extension `.html`/`.htm` case-insensitive; any failure → `ErrBadNotFoundPage`.
  - `func (m Meta) NotFoundTarget() string` — `""` unless `Mode == ModeWebserver && !SPAFallback`; else `"/" + page` where page is `NotFoundPage` or `DefaultNotFoundPage`.
  - `func NotFoundServes(site *Site) string` — `""` when `NotFoundTarget()` is `""` or the target is not a regular file in `OpenServedRoot(site)`; else the path without the leading `/`.
  - `func MissingStartPage(site *Site) bool` — true only when mode is webserver, no SPA, not locked, the served root holds at least one regular file that is not a `.sitebin-*` marker (walk stops at the first), and neither `index.html` nor `index.txt` is a regular file at the root. Any error → false (never warn on a guess). Uses `OpenServedRoot`, never `os.Open` on a joined path.

- [ ] **Step 1: Write the failing tests** in `notfound_test.go`:
  - `TestCleanNotFoundPage`: table — `"404.html"→"404.html"`, `"/404.html"→"404.html"`, `" errors/Missing.HTM "→"errors/Missing.HTM"`, `""→""`; refused (`ErrBadNotFoundPage`): `"../x.html"`, `"//x.html"`, `"x.txt"`, `"_sitebin/x.html"`, `"_raw/x.html"`, `".sitebin-trusted"`, `"dir/"`, `"a\\b.html"`.
  - `TestNotFoundTarget`: webserver default → `"/404.html"`; webserver with `NotFoundPage:"e/m.html"` → `"/e/m.html"`; webserver+SPA → `""`; viewer → `""`; container → `""`.
  - `TestNotFoundServes`: site (use the package's existing test helper that creates a store and site) with `404.html` → `"404.html"`; configured `e/m.html` missing while `404.html` exists → `""`; a directory named `404.html` → `""`.
  - `TestMissingStartPage`: empty site → false; only `.sitebin-trusted` → false; `a.pdf` → true; `a.pdf`+`index.html` → false; `a.pdf`+`index.txt` → false; only `sub/index.html` → true; SPA on → false; viewer → false.
- [ ] **Step 2:** `go test ./internal/store -run 'NotFound|StartPage'` → FAIL (undefined).
- [ ] **Step 3:** Implement `notfound.go` and the meta field per Interfaces.
- [ ] **Step 4:** `go test ./internal/store` → PASS.
- [ ] **Step 5:** Commit `feat: a site can name its not-found page, and the store knows when a web server site has no start page`.

### Task 2: Settings, payload and warnings (API + MCP)

**Files:**
- Modify: `internal/httpapi/sites.go` (`updateSet`, `settingsFromForm`, `writeSettings`, `sitePayload`, `createSite`, `uploadFiles`), `internal/httpapi/provenance.go` (`settingsDetail`), `internal/httpapi/mcpops.go` (`settingsToUpdateSet`, `siteResult`, `CreateSite`), `internal/mcp/ops.go` (`Settings`, `SiteResult`)
- Create: `internal/httpapi/notfound.go` (warning const + helper)
- Test: `internal/httpapi/notfound_test.go`, MCP tests beside the existing ones in `internal/httpapi`

**Interfaces:**
- Consumes: Task 1 (`CleanNotFoundPage`, `NotFoundServes`, `MissingStartPage`, `ErrBadNotFoundPage`).
- Produces:
  - `updateSet.NotFoundPage *string` `json:"not_found_page"`; form field `not_found_page` (present = set, empty clears).
  - `const startPageWarning = "<exact text from Global Constraints>"` in `notfound.go`.
  - `func (a *API) startPageWarnings(site *store.Site) []string` — `[startPageWarning]` when `store.MissingStartPage(site)`, else nil.
  - `sitePayload` keys: `"not_found_page"` (stored value), `"not_found_serves"` (`store.NotFoundServes`), `"start_page_missing"` (bool).
  - `mcp.Settings.NotFoundPage *string` `json:"not_found_page,omitempty"` with jsonschema `the page shown, with status 404, for an address that does not exist on a web server site: the path of an .html file inside the site; an empty string returns to the default (404.html when the site has one, otherwise Sitebin's own page)`.
  - `mcp.SiteResult.NotFoundPage string` `json:"not_found_page,omitempty" jsonschema:"the site's own not-found page, when one is set"`.

- [ ] **Step 1: Write failing tests:**
  - `TestSettingsNotFoundPage`: PUT `{"not_found_page":"/errors/x.html"}` → 200, payload `not_found_page == "errors/x.html"`; PUT `{"not_found_page":"x.txt"}` → 400 with `ErrBadNotFoundPage`'s message; PUT `{"not_found_page":""}` → stored `""`.
  - `TestPayloadNotFoundServes`: site with `404.html` → `not_found_serves == "404.html"`; without → `""`.
  - `TestStartPageWarning`: create (multipart, one `a.pdf`) → 201, `warnings` contains `startPageWarning`, `start_page_missing == true`; create with `index.html` → no such warning; upload of `a.pdf` into an empty site → response `warnings` contains it; create with no files → no warning.
  - `TestMCPNotFoundPage`: `update_site` with `not_found_page` round-trips into `SiteResult.NotFoundPage`; `get_site` of a site that never set it has no `not_found_page` key in the JSON; `create_site` with only `a.pdf` → `warnings` contains `startPageWarning`, and a domain warning (if any) is kept beside it (append, never overwrite).
  - `TestSettingsDetailNotFoundPage`: `settingsDetail` of a set with `NotFoundPage` contains `not_found_page`.
- [ ] **Step 2:** `go test ./internal/httpapi -run 'NotFound|StartPage'` → FAIL.
- [ ] **Step 3:** Implement: `writeSettings` validates with `store.CleanNotFoundPage` (400 on error); `settingsFromForm` reads the field; `settingsDetail` appends `"not_found_page"`; `sitePayload` adds the three keys; `createSite` appends `a.startPageWarnings(site)` to `warnings` before the `len(warnings) > 0` check; `uploadFiles` sets `payload["warnings"]` when non-empty; MCP `siteResult` fills `NotFoundPage` and appends `startPageWarnings` to `out.Warnings`; MCP `CreateSite` changes `res.Warnings = warnings` to `res.Warnings = append(warnings, res.Warnings...)`.
- [ ] **Step 4:** `go test ./internal/httpapi ./internal/mcp && go test -tags ee ./internal/httpapi ./internal/mcp` → PASS.
- [ ] **Step 5:** Commit `feat: the not-found page is a site setting, and a web server site without a start page says so`.

### Task 3: authz names the page; the backend renders Sitebin's page

**Files:**
- Modify: `internal/httpapi/internalh.go` (`admit`), `internal/httpapi/server.go` (route), `internal/httpapi/notfound.go`
- Test: `internal/httpapi/notfound_test.go`

**Interfaces:**
- Consumes: `Meta.NotFoundTarget()` (Task 1).
- Produces:
  - `const notFoundHeader = "X-Sitebin-NotFound"` (httpapi; caddygen mirrors it in Task 4).
  - `admit`: for a file site, `if t := site.Meta.NotFoundTarget(); t != "" { w.Header().Set(notFoundHeader, t) }` (never on a container answer).
  - Route `GET /_sitebin/not-found` → `func (a *API) siteNotFound(w http.ResponseWriter, r *http.Request)` = `a.msgPage(w, 404, "Page not found", "There is no page at this address. Check the link for typos.")`.

- [ ] **Step 1: Write failing tests:** `TestAuthzNamesNotFoundPage` (webserver → `/404.html`; configured → `/e/m.html`; SPA → absent; viewer → absent; container with upstream → absent); `TestSiteNotFoundRoute` (GET `/_sitebin/not-found` on a view host → 404, body contains `Page not found`, `Cache-Control: no-store`; HEAD → 404).
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3:** Implement per Interfaces.
- [ ] **Step 4:** both tag suites for `./internal/httpapi` → PASS.
- [ ] **Step 5:** Commit `feat: authz names a site's not-found page, and Sitebin renders its own for every other 404`.

### Task 4: Caddyfile — no listings, a page for every 404

**Files:**
- Modify: `internal/caddygen/caddygen.go`
- Test: `internal/caddygen/caddygen_test.go`

**Interfaces:**
- Consumes: the header name from Task 3 (mirrored as a local `const notFoundHeader = "X-Sitebin-NotFound"`, with the same mirror comment as `upstreamHeader`).
- Produces: `func writeNotFound(b *strings.Builder, backend func(string) string)` emitting, at site-block level (one tab), exactly the shape the spike proved on v2.11.4:

```
	handle_errors 404 {
		@notfoundpage {
			header X-Sitebin-NotFound /*
			file {http.request.header.X-Sitebin-NotFound}
		}
		handle @notfoundpage {
			request_header -If-None-Match
			request_header -If-Modified-Since
			request_header -If-Match
			request_header -If-Unmodified-Since
			request_header -Range
			request_header -If-Range
			header Cache-Control no-store
			rewrite * {http.request.header.X-Sitebin-NotFound}
			file_server {
				status 404
				hide .sitebin-spa .sitebin-trusted
			}
		}
		handle {
			rewrite * /_sitebin/not-found
			reverse_proxy 127.0.0.1:8080
		}
	}
```

- [ ] **Step 1: Write failing tests:**
  - `TestNoDirectoryListings`: `browse` appears nowhere, for subdomain-only, path, and both view access modes.
  - `TestEveryContentOriginAnswers404WithAPage`: the view wildcard block, the custom-domain catch-all (`http://:80 {` and, in TLS mode, `https:// {`) and the main domain block with `SITEBIN_VIEW_ACCESS=both` each contain `handle_errors 404 {`, `file {http.request.header.X-Sitebin-NotFound}`, `status 404`, `header Cache-Control no-store`, `request_header -If-None-Match`, `request_header -Range`, `rewrite * /_sitebin/not-found`; the main domain block WITHOUT path views has no `handle_errors`.
  - `TestClientCannotNameTheNotFoundPage`: in each content route (wildcard, catch-all, `handle @view {`), `request_header -X-Sitebin-NotFound` comes before `forward_auth`, and the `copy_headers` line names `X-Sitebin-NotFound`.
- [ ] **Step 2:** `go test ./internal/caddygen` → FAIL.
- [ ] **Step 3:** Remove `browse` from `writeFileServing` (update its comment: unknown paths and folders without an index are 404 errors answered by `handle_errors`); add the header to the strip list and `copy_headers` in `writeContentRoutes` and to `writePathViewRoutes` (strip + copy); call `writeNotFound` once per block: the view wildcard block, the custom-domain block, and the main domain block when `cfg.PathViews()`.
- [ ] **Step 4:** `go test ./internal/caddygen`, then `go run ./cmd/sitebin caddyfile` with `SITEBIN_BASE_DOMAIN=sitebin.localtest.me SITEBIN_VIEW_ACCESS=both SITEBIN_HTTP_ONLY=true` piped into `docker run --rm -i --entrypoint caddy sitebin:dev adapt --adapter caddyfile --config /dev/stdin` → exits 0 (the generated file parses in the image's Caddy).
- [ ] **Step 5:** Commit `feat: sites never list a folder, and every 404 is a page`.

### Task 5: File viewer — a download view for binaries

**Files:**
- Modify: `internal/viewer/viewer.go`, `web/viewer/viewer.js`, `web/viewer/viewer.css`
- Test: `internal/viewer/viewer_test.go`

**Interfaces:**
- Produces: renderer kind `"download"`. Go `rendererByExt` gains (all → `"download"`): `.exe .msi .dmg .pkg .app .apk .ipa .deb .rpm .appimage .zip .7z .rar .tar .gz .tgz .bz2 .xz .zst .iso .img .bin .dll .so .dylib .jar .wasm .woff .woff2 .ttf .otf .eot .psd .ai .sketch .fig .blend .xlsx .xls .pptx .ppt .doc .odt .ods .odp .epub .heic .tif .tiff .mkv .avi .wmv .flv .sqlite .db`. The JS `rendererByExt` mirrors the Go map exactly.
- JS: `function renderDownload(stage, path)` — no fetch; shows file name, size (from `cfg.files`), a line `No preview for this kind of file.` and a primary download link (`rawURL(path)`, `download` attribute). `show()` dispatches `kind === "download"` to it. `renderText` fetches with header `Range: bytes=0-<CAP>` (CAP = 2 MiB), reads an `ArrayBuffer`, and when the first 8192 bytes contain a `0x00` byte calls `renderDownload` instead; otherwise decodes with `TextDecoder` and marks truncated when the buffer is longer than CAP.

- [ ] **Step 1: Write failing tests:** `TestRendererForBinaries` (`setup.EXE`→download, `a.tar.gz`→download, `main.go`→text, `notes.txt`→text, `a.pdf`→pdf); `TestViewerJSMirrorsRenderers` — reads `../../web/viewer/viewer.js`, extracts the `rendererByExt` object's `ext: "kind"` pairs with a regexp, and requires them to equal the Go map (without the leading dot).
- [ ] **Step 2:** `go test ./internal/viewer` → FAIL.
- [ ] **Step 3:** Implement Go map, JS mirror, `renderDownload`, `renderText` change, CSS for the download panel (centred, uses the viewer's existing variables).
- [ ] **Step 4:** `go test ./internal/viewer` → PASS.
- [ ] **Step 5:** Commit `feat: the file viewer offers a download instead of showing a binary as text`.

### Task 6: Edit page — the setting and the notice

**Files:**
- Modify: `web/static/edit.html` (Behavior card), `web/static/edit.js` (`render`, handlers)

**Interfaces:**
- Consumes: payload keys from Task 2 (`not_found_page`, `not_found_serves`, `start_page_missing`), `put(body, toast)`.
- Produces (DOM ids): `notfoundwrap` (shown only for `mode === "webserver" && !spa_fallback`), input `e-notfound` (placeholder `404.html`), buttons `save-notfound` (`put({not_found_page: value}, "Not-found page saved")`) and `clear-notfound` (`put({not_found_page: ""}, "Not-found page: automatic")`, hidden when unset), status line `notfound-status`: `Visitors see <not_found_serves> for an address that does not exist.` or, when `not_found_serves` is empty, `Visitors see Sitebin's not-found page.` plus, when `not_found_page` is set, ` <not_found_page> does not exist yet.`; notice `startpage-note` (class `rulenote`) in the Behavior card, shown when `start_page_missing`, text from the warning plus a button `Switch to File viewer` that does `put({mode: "viewer"}, "Switched to File viewer")`.

- [ ] **Step 1:** Implement markup and wiring; keep the input untouched while focused (as `e-name` does).
- [ ] **Step 2:** Verify in a browser against a running `sitebin:dev` container built from this branch (Task 7's image): the field shows/hides with mode and SPA, save/clear work, the notice appears for a PDF-only web server site and the button switches it.
- [ ] **Step 3:** Commit `feat: the edit page sets the not-found page and says when a site has no start page`.

### Task 7: E2E and the docs in this repo

**Files:**
- Create: `e2e/notfound.ps1` (community image, default `-Image sitebin:dev`, port 8197)
- Modify: `e2e/caching.ps1` (line ~124 and its header comment), `CLAUDE.md` (E2E list: add `notfound.ps1`, the count becomes nineteen and `e2e/stack/verify.ps1` the twentieth), `README.md` (Modes: replace "Directory listings appear when no index exists." with the 404 behaviour and the not-found page; settings/API docs: `not_found_page`; MCP table row for `update_site`/`get_site`)

**Interfaces:**
- Consumes: everything above, through the real image.

- [ ] **Step 1: Update `caching.ps1`:** the assertion becomes `a 404 carries no-store, never the site's policy` with `(Header $r "Cache-Control") -eq "no-store"`.
- [ ] **Step 2: Write `notfound.ps1`** (pattern of `caching.ps1`: `Req`, `Header`, `Assert`, `$ExpectedAssertions`), run with `SITEBIN_VIEW_ACCESS=both`, `SITEBIN_HTTP_ONLY=true`. Sites: A = `index.html` + `404.html` (body `own-404`) + `assets/a.css`; B = `index.html` only; C = `a.pdf` only (web server); D = A's files with `not_found_page` set to `errors/gone.html` (body `named-404`); E = viewer with `a.pdf`; F = A's files with a view password. Make B untrusted with `docker exec <name> rm /data/sites/<id>/files/.sitebin-trusted` (community sites are trusted). Assertions:
  - A `/missing` → 404, body `own-404`, `Cache-Control: no-store`; A `/assets/` and A `/assets` → 404 `own-404`.
  - B `/missing` → 404, body contains `Page not found`, `no-store`, CSP contains `form-action 'none'` (untrusted); A `/missing` CSP does not contain `form-action 'none'` (trusted).
  - C `/` → 404 `Page not found`; C `/a.pdf` → 200.
  - D `/missing` → 404 `named-404`; delete `errors/gone.html` via API → D `/missing` → 404 `Page not found`.
  - E `/_raw/` → 404 `Page not found`; E `/_raw/a.pdf` → 200.
  - F `/missing` without cookie → 401 (gate).
  - A `/missing` with `If-Modified-Since: <A's 404.html Last-Modified>` → 404 with body `own-404`; with `Range: bytes=0-3` → 404 with body `own-404`; `HEAD` → 404, empty body.
  - A `/missing` with client header `X-Sitebin-NotFound: /index.html` → body `own-404` (not the index).
  - A under the path view `/v/<id>/missing` → 404 `own-404`; B under the path view → `Page not found`.
  - API: C's payload `start_page_missing` true and `warnings` on creation; A's `not_found_serves == "404.html"`.
  - SPA site (A's files, `spa_fallback:true`) `/deep/link` → 200 index.
- [ ] **Step 2b: Enterprise-only paths** (custom domains are enterprise): in `mounts.ps1`, give one file site under a path a `404.html` and assert `<mount>/missing` → 404 with its body and `no-store`, and `<mount>/nothing-here/` → 404; in `containers.ps1`, assert that a path the test app does not know answers with the app's own status and body (not `Page not found`).
- [ ] **Step 3:** Build `docker build -t sitebin:dev .` and `docker build --build-arg EDITION=enterprise -t sitebin:dev-ee .`; run `notfound.ps1`, `caching.ps1`, `spa.ps1`, `paths.ps1`, `e2e.ps1` (community) and `mounts.ps1`, `containers.ps1` (enterprise) → all green.
- [ ] **Step 4:** Update README and CLAUDE.md as listed.
- [ ] **Step 5:** Commit `test: not-found pages through the real Caddy` and `docs: sites answer a missing page with a 404 page, never a listing`.

### Task 8: Website docs (repo `Sitebin-Website`, committed now, pushed after the product is live)

**Files:**
- Modify: `public/docs/using/index.html` (~line 113 listing promise → 404 + own `404.html` + `not_found_page`, File viewer for file drops, root-relative links note), `public/docs/api/index.html` (settings: `not_found_page`), `public/docs/mcp/index.html` (`update_site` settings mention, if the page lists settings)

- [ ] **Step 1:** Edit the pages; run `scripts/check-links.ps1`.
- [ ] **Step 2:** Commit `content: a missing page is a 404 page, and a site can bring its own`. Do not push until the product is live (ship order).
