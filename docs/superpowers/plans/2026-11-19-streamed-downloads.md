# Plan — streamed downloads

Design: `docs/superpowers/specs/2026-11-19-streamed-downloads-design.md`.

1. **`internal/zipstream`** (test first): `Method(name)` stores compressed
   formats, deflates the rest; `NewWriter(w)` — 64 KiB buffer, pooled
   BestSpeed deflate. Round-trip test (names, bytes, methods, times) and a
   benchmark against archive/zip's default.
2. **`Store.ZipContent`** uses it: `CreateHeader` with `Method(rel)` and the
   file's mtime. Test: a `.webp` is stored, a `.json` deflated, times kept.
3. **`downloadBackup`** uses it.
4. **`openerStands`**: the standing switch of `uploadTokenStands`, shared.
5. **Download tickets** (`internal/httpapi/downloadlinks.go`, test first):
   registry (single use, 60 s, per-site 5, instance 2 000), canonical
   request key, `downloadLinkPaths`, `POST …/download-link`,
   `withDownloadAuth` on the three GET routes. Tests: password / session /
   token mint → GET without headers streams; second use 401; other path,
   other query, other site 401; expired 401; rotated password 401;
   transferred owner 401; locked 403; mint of a non-download path 400;
   upload token cannot mint; GET without a ticket still needs a credential.
6. **`name`** on `GET …/download`: sanitised `Content-Disposition`.
7. **UI**: `startDownload(path)` in `edit.js`; the site, backup and git
   buttons and the workspace's `WS.download` use it.
8. README API table + website docs (`/docs/api/` if it lists the route).
9. `go vet`, both suites, a local run of the enterprise image with a real
   browser download; deploy per the hosted runbook.
