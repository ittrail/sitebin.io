# Streamed downloads — the browser saves a ZIP as the server packs it

Date: 2026-11-19. Status: approved by the operator's order ("geht das
schneller, effizienter und für die UI/UX mehr in Echtzeit? z. B. packen
während des Downloads").

## The problem

"Download ZIP" on PhysioPrint (a container site: 21 628 files, 373 MB, of
them 13 774 `.webp`) seemed to build the whole archive on the server before
anything happened, and cost CPU and RAM.

What actually happens:

1. **The server already streams.** `Store.ZipContent` writes each file into
   a `zip.Writer` on the response as it walks the tree; nothing is spooled.
2. **The page holds the whole download.** `edit.js` (and the workspace's
   `shell.js`, `edit-backups.js`, `edit-git.js`) fetch the ZIP with
   `fetch()` and `res.blob()`, because the request needs the page's
   credential headers (`X-Edit-Password`, `X-Sitebin-Session`). The browser
   keeps every byte in memory and offers the file only when the last one has
   arrived — nothing visible for minutes, then 373 MB of RAM in the tab.
3. **The server deflates everything at level 5** (archive/zip's default),
   including formats that are compressed already — every `.webp`, `.jpg`,
   `.mp4`, `.woff2`, `.zip` — which is CPU spent for nothing. Writes go out
   in archive/zip's 4 KiB buffer.

## The design

### A. The browser downloads natively, through a one-minute link

`POST /api/sites/{editID}/download-link` `{"path": "/download"}` — behind
`withEditAuth`, so every credential that may download may ask — answers
`{"url": "/api/sites/{editID}/download?ticket=sbd_…", "expires_at": …}`.
The page opens the URL with an `<a download>` click, and the browser's own
download manager takes over: the transfer shows at once with its progress,
the bytes go to disk as they arrive, the page never holds them.

The ticket:

- **Memory only, single use, 60 seconds.** Taken (deleted) by the first
  request that presents it; a restart forgets all of them. The URL in the
  browser's history is dead the moment the download has started.
- **Bound to one site and one exact request**: the edit id, the path, and
  the query without `ticket` (canonical `url.Values.Encode`), so a ticket for
  `/download` never opens `/backups/x/download`, and one for a git archive of
  `main` never opens another ref.
- **Only download routes accept it**: `GET …/download`,
  `GET …/backups/{snapshot}/download`, `GET …/git/archive`
  (`downloadLinkRoute`). The mint refuses any other path (400). These routes
  are wrapped in `withDownloadAuth`, which answers a request carrying
  `ticket` from the ticket alone and passes every other request to
  `withEditAuth` unchanged. The ticket is never read from a header and never
  tried as a password.
- **It re-checks what minted it** on use, like an upload token
  (`openerStands`, now shared with `uploadTokenStands`): an owner's ticket
  holds while the account owns the site, an admin's while the admin stands,
  the edit password's while the site's hash is the one it was minted
  against. A locked site is refused (`refuseLocked`). The handler then runs
  with the minting actor, so every check the route makes itself
  (`offeredVersion`, the backup download limiter, `gitReadable`) still runs.
- **Bounded**: 5 live tickets per site (the oldest is evicted), 2 000 per
  instance (then 503).
- **A cross-site page cannot mint one**: the mint is a POST through
  `withEditAuth`, whose session standing needs `X-Sitebin-Session` and a
  same-origin fetch. The session rule is NOT loosened: a plain GET with a
  cookie still opens nothing.

`GET /download` takes an optional `name` (the workspace's site list names
the ZIP after the site, as it did); it is sanitised into the
`Content-Disposition` filename (`mime.FormatMediaType`, RFC 2231 for
non-ASCII), never echoed raw.

Scripts are unchanged: `curl -H "X-Edit-Password: …" …/download` streams as
before. MCP's `download_site` (bounded, in memory) is unchanged.

### B. The server packs with less work

`internal/zipstream` is the one ZIP writer for downloads (`ZipContent`,
the backup download):

- **Store, don't deflate, what is compressed already** (`zipstream.Method`,
  by extension: images `webp jpg jpeg png gif avif heic heif jxl`, video and
  audio `mp4 m4v mov webm mkv mp3 m4a aac ogg oga opus flac`, fonts
  `woff woff2`, archives and packages `zip gz tgz bz2 xz zst br 7z rar lz4
  jar apk aab ipa nupkg whl vsix xpi crx epub docx xlsx pptx odt ods odp dmg
  msi msix appimage deb rpm snap`, and `pdf`). Storing is a copy and a
  CRC-32.
- **Deflate the rest at `flate.BestSpeed`**, from a `sync.Pool` of writers
  (archive/zip pools only its own level-5 writer).
- **64 KiB writes**: the `zip.Writer` writes into a 64 KiB `bufio.Writer`
  (archive/zip reuses a `bufio.Writer` it is given), so a download is fewer,
  larger chunks.
- **Modification times are kept** in the site ZIP (the backup ZIP already
  kept them); unpacked files no longer all say 1980.
- **Sitebin's markers stay out of the site ZIP** (any top-level
  `.sitebin-` name), as they already did in the backup ZIP. Found while
  testing: a downloaded ZIP of a trusted or SPA site held `.sitebin-trusted`
  / `.sitebin-spa`, which `CleanRelPath` refuses, so uploading the ZIP again
  failed as a whole ("invalid file path").
- **A failure mid-archive aborts the connection** (`http.ErrAbortHandler`,
  as the backup download already did); before the first byte it is a 500.
  Ended normally, a browser would keep a truncated archive as a finished
  download.

### Not done, on purpose

- **No `Content-Length`, so no percentage.** An exact length needs the
  archive's size before the first byte: every entry stored (text sent
  uncompressed) and every file's size frozen for the whole download — a file
  changing mid-download would leave a broken archive with a length that
  lies. The browser shows bytes received and the transfer rate; the edit
  page already shows the site's file count and size.
- **Git's own archive** (`git archive --format=zip`) keeps git's compression;
  only its button moves to the link.
- **No concurrency limit** on site downloads: unchanged from before; with
  `BestSpeed` and stored media one download is mostly disk I/O.
