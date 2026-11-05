# File viewer file list — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The File viewer gets an explorer-style file list (folders, details/grid views, sorting, filter, linkable addresses) as its start page when "Viewer shows" names no file, and a way into it from any file.

**Architecture:** The Go side only changes what the generated wrapper carries (`entry` may be `""` = list, files carry `mtime`, hidden names left out) and lets the API/MCP set `entry_file: ""`. Everything else is client-side in plain ES modules beside `web/viewer/viewer.js`, routed by the URL fragment, so routing, authz and the Caddyfile do not change.

**Tech Stack:** Go (internal/viewer, internal/httpapi, internal/mcp), plain JS modules + CSS (web/viewer), edit page (web/static), PowerShell e2e.

**Spec:** `docs/superpowers/specs/2026-11-05-viewer-file-list-design.md`

## Global Constraints

- `entry_file` `""` = the file list, never replaced by a fallback. A missing entry: one visible file → that file, else `""`; persisted by `syncViewerLayout`.
- `/_raw/<path>` unchanged; no Caddyfile, authz or routing change.
- Addresses: `#/` top folder, `#/a/b/` folder, `#/a/b/c.pdf` file; segments percent-encoded.
- Hidden from the list (left out of the wrapper config): any path segment starting with `.`, `__MACOSX`, `Thumbs.db`, `desktop.ini`. An explicit entry is honoured even if hidden.
- Grid tile size 96–288 px, default 160; PDF previews up to 25 MB, two at a time; Grid default when ≥ 60 % of a folder's files are images/videos.
- File names only ever as text. No new CSP allowance, no third-party request, no new library, no build step.
- `store.FileInfo` (API/MCP output shape) is not changed; the wrapper gets its own file struct. MCP output fields stay as they are.
- e2e scripts stay pure ASCII.

## Review Focus

1. A file or folder name with `#`, `%`, `?`, spaces, unicode or `..`-like text: its address round-trips and its name never becomes markup.
2. An old wrapper (no `mtime`, entry always set) loaded with the new script still works.
3. A site whose only visible file is hidden-named, or whose entry is hidden: start page sane, no crash.
4. Path views (`/v/<id>/`) and mounted paths: `/_raw/` URLs, the mark link and addresses stay inside the site's base.
5. A large folder (1000 files) in grid view loads nothing for tiles off screen; PDF previews never fetch whole large files.

---

### Task 1: Wrapper data and the `""` entry

**Files:**
- Modify: `internal/viewer/viewer.go` (wrapperConfig, generate, listRaw use), `internal/httpapi/sites.go` (writeSettings entry_file), `internal/mcp/ops.go` (Settings.EntryFile description)
- Test: `internal/viewer/viewer_test.go`, `internal/httpapi/viewerlist_test.go` (new)

**Interfaces:**
- Produces: wrapper config JSON `{"entry": string, "renderer": string, "files": [{"path","size","mtime"}]}`; `entry` `""` means the list.

- [ ] Step 1: tests — `TestApplyListsSeveralFilesWithoutEntry` (replaces `TestApplyPicksEntryWhenUnset`: entry `"index.html"` missing, 2 files → `""`), `TestApplyOpensTheOnlyFile` (1 file → that file), `TestApplyKeepsAnExplicitList` (entry `""`, 1 file → `""`), `TestWrapperCarriesMtimeAndLeavesHiddenOut` (config has `mtime` > 0; `.env`, `a/.git/x`, `__MACOSX/y`, `Thumbs.db` absent; visible ones present), `TestHiddenEntryIsHonoured` (entry `.notes.md` existing → kept), `TestEntryFileEmptyMeansTheList` (httpapi: PUT `{"mode":"viewer","entry_file":""}` → 200, meta `""`, wrapper `"entry":""`).
- [ ] Step 2: run, watch them fail.
- [ ] Step 3: implement (`viewerFile{Path,Size,Mtime}`; `isHiddenPath`; entry rule; `""` accepted in writeSettings; MCP description "the file the File viewer opens first; empty shows the file list").
- [ ] Step 4: `go test ./internal/viewer/ ./internal/httpapi/` green, then both suites.
- [ ] Step 5: commit `feat: a viewer site with no chosen file opens on its file list`.

### Task 2: The file list and the new file view (web/viewer)

**Files:**
- Create: `web/viewer/files.js` (pure: tree, routes, sorting, kinds, formatting), `web/viewer/browser.js` (folder view, previews, prefs)
- Modify: `web/viewer/viewer.js` (routing, file-view chrome, boot), `web/viewer/viewer.css`
- Test: `internal/viewer/viewer_test.go` — `TestViewerModulesShip` (the embedded FS has `viewer/files.js` and `viewer/browser.js`, and viewer.js imports both); `TestViewerJSMirrorsRenderers` keeps passing.

- [ ] Step 1: `TestViewerModulesShip`, watch it fail.
- [ ] Step 2: implement the modules per the spec (folder view: breadcrumb, toolbar, summary, details table with sortable headers, grid with slider and lazy previews, filter, prefs, scroll restore; file view: breadcrumb, ‹ n/m ›, ← → Esc, Files, Download).
- [ ] Step 3: test green; `node --check` on each module (local syntax check only).
- [ ] Step 4: browser check on a local `sitebin:dev` container (localhost): list, folders, both views, slider, sort, filter, file view, prev/next, keys, a name with `#` and spaces, `/v/<id>/` path view, an untrusted site (CSP console clean).
- [ ] Step 5: commit `feat: the File viewer lists a site's files like an explorer`.

### Task 3: Edit page "File list" option

**Files:** Modify `web/static/edit.js` (entry select: first option "File list (all files)", value `""`; status text).

- [ ] Step 1: implement; Step 2: browser check (select shows File list for `""`, choosing it saves `""`); Step 3: commit `feat: the edit page can make the file list a viewer site's start page`.

### Task 4: e2e

**Files:** Modify `e2e/e2e.ps1` (viewer section).

- [ ] Assertions: a viewer site with `docs/a.pdf`, `docs/b.md`, `.secret`, `top.txt` and no entry → wrapper `"entry":""`, has `"mtime":`, lacks `.secret`; `/_raw/.secret` still 200; `/_sitebin/assets/viewer/files.js` and `browser.js` 200; PUT `entry_file` `""` on the report.pdf site → wrapper `"entry":""`.
- [ ] Run `e2e.ps1` against a fresh `sitebin:dev`; plus `paths.ps1` and `notfound.ps1`. Commit `test: e2e covers the viewer's file list`.

### Task 5: Docs

**Files:** `README.md` (Modes, API `entry_file`), `CLAUDE.md` (short "File viewer" rules), website `public/docs/using/`, `api/`, `mcp/` (entry_file `""`).

- [ ] Write, run the website link check, commit both repos (`docs:` / `content:`).
