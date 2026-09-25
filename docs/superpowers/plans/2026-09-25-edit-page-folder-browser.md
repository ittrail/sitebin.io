# Folder browser on the edit page — plan

**Spec:** `docs/superpowers/specs/2026-09-25-edit-page-folder-browser-design.md`

Bounded work in existing code; each step test-first, both build tags green.

1. **Store:** `store.ListDir(site, relDir)` + `CleanDirPath` + `ErrUnreadable`
   (`internal/store/listdir.go`, `listdir_test.go`): one folder through the
   content root's `os.Root`, folders first, case-insensitive, markers and
   unaddressable names left out, links/sockets left out, 10,000 cap.
2. **API:** `GET /api/sites/{editID}/dir?path=` behind `withEditAuth`
   (`internal/httpapi/sites.go` `listDir`, `server.go` route; `ErrUnreadable`
   → 403 in `storeError`); tests in `internal/httpapi/dir_test.go`.
3. **Page:** `web/static/edit.js` folder state + `loadDir`/`openDir`/
   `renderFiles`, Back + breadcrumbs, `#dir=` fragment with push/replace/
   popstate, 15 entries + "Show N more", uploads into the current folder,
   refresh after every mutation, stale answers dropped, focus kept;
   `edit.html` crumb container and drop hint; `app.css` styles.
4. **E2E + docs:** two `e2e/e2e.ps1` assertions; README API example; website
   `/docs/api/` "List a folder" (pushed after the product is live).
5. **Verify:** Go suites (both tags, Linux non-root for the link and
   permission tests), Docker build, `e2e.ps1` + `mcp.ps1`, deploy, and a live
   look at a container site's edit page.
