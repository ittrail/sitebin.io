# File viewer: a file list with folders, views and sorting

Date: 2026-11-05. Status: approved by the operator in chat ("du entscheidest,
los gehts" — the operator set the requirements below and left every other
call to the builder; those calls are marked *builder's call*).

## Why

The File viewer shows one file at a time, with a dropdown of all files in its
top bar. Until today a visitor could also get a plain list of a folder from
Caddy's directory listing, but that listing was a fallback, not a feature, and
the not-found work (`2026-11-05-not-found-pages-design.md`) removed it
everywhere. What is left cannot present a collection: ten PDFs, a photo set,
an installer with its manual. The operator wants the viewer to do it the way
a file explorer does.

## The operator's requirements (2026-11-05)

1. **"Viewer shows" stays.** When nothing is chosen there, the viewer's start
   page is the file list.
2. The list has **folder navigation**, **view styles like Explorer** (a
   details list, and a preview grid whose size can be scaled), **sorting**,
   and the usual explorer comforts.
3. When a file **is** chosen in "Viewer shows", the viewer opens that file
   first, and offers a way into the folder view.
4. **`/_raw/<path>` stays exactly as it is.**

## Goals

### Start page

- `Meta.EntryFile` keeps its meaning and gains one value:
  - `""` — **the file list** (explicit; never replaced by a fallback);
  - the path of an existing file — that file opens first;
  - a path that names no file (the creation default `index.html`, a deleted
    file) — *builder's call*: **one visible file → that file; otherwise the
    list (`""`)**, and the result is persisted as before
    (`syncViewerLayout`). A single PDF or a lone installer opens at once (the
    list would only add a click), a collection opens as a list.
- The edit page's "Viewer shows" select gets a first option **"File list"**
  (value `""`). The API and MCP accept `entry_file: ""` and mean the list
  (today `""` is a 400).
- No migration: every existing viewer site has a stored entry file (the old
  fallback persisted the first file), so each keeps opening that file and
  gains the way into the list. The owner switches to "File list" if wanted.

### Addresses (*builder's call*: hash routing)

- Every folder and file has its own address in the URL fragment:
  `#/` (top folder), `#/docs/2024/` (a folder — trailing slash),
  `#/docs/2024/report.pdf` (a file). Segments are percent-encoded.
- No fragment: the start page (the entry file, or the top folder).
- Back/forward and reload keep the place. Links can be shared.
- Why the fragment and not `/docs/report.pdf`: it needs no change to routing,
  authz or the Caddyfile, it works unchanged under `/v/<id>/` path views and
  mounted custom-domain paths, and `/_raw/<path>` keeps its meaning.
- An address that names nothing: "There is no file at this address." with a
  link to the list. A file path that is a folder opens the folder.

### Folder view

- **Top bar:** the Sitebin mark (links to `#/`), a breadcrumb
  (`Files / docs / 2024`, every segment a link).
- **Toolbar:** a filter field, sort control, view switch (Details / Grid), and
  a size slider in grid view.
- **Summary line:** `3 folders · 24 files · 312 MB` (sizes summed below the
  folder).
- **Folders first**, then files, each group in the chosen order.
- **Details view:** a real table — Name (with a type icon), Size, Type,
  Modified. Clicking a column header sorts by it; clicking again reverses.
  A folder's Size cell says how many items it holds. On narrow screens Type
  and Modified are hidden.
- **Grid view:** tiles with a preview and the name (two lines at most) and
  size. The slider scales the tiles (96–288 px, default 160).
  Previews (*builder's call*, all client-side, all lazy — only tiles that
  scroll into view load anything):
  - images: the file itself (`<img loading="lazy">`, contained, not cropped);
  - videos: the first frame (`preload="metadata"`, muted);
  - PDFs up to 25 MB: page 1 rendered with the vendored pdf.js using range
    requests (`disableAutoFetch`, `disableStream`), at most two at a time,
    cached for the page's lifetime as a `blob:` image;
  - everything else, and any preview that fails: a type icon with the
    extension.
- **Sorting:** Name (natural order: `file2` before `file10`, case-insensitive),
  Size, Type, Modified; ascending or descending. Folders sort by name, by their
  summed size, or by their newest file.
- **Filter:** typing filters the current folder **and its subfolders** by
  name; matches are listed flat with their folder shown under the name.
  Empty filter: the plain folder.
- **Default view style** until the visitor picks one (*builder's call*):
  Grid when at least 60 % of the folder's files are images or videos,
  Details otherwise.
- **The visitor's choices** (view style, sort, tile size) are remembered in
  `localStorage`, keyed by the site's base path (path views share an origin),
  and storage being unavailable changes nothing but the memory.
- Returning from a file to its folder restores the folder's scroll position.
- Empty folder: "This folder is empty." Empty site: "This site has no files
  yet."

### File view

- Top bar: the mark, the breadcrumb to the file (folders are links), the size,
  then **‹ 3 / 12 ›** (previous / next file in the same folder, in the
  visitor's sort order; also the ← → keys), a **Files** button (opens the
  file's folder) and **Download** (`/_raw/<path>`).
- Escape returns to the folder. The renderers themselves are unchanged.
- The old file dropdown goes: the breadcrumb, the Files button and ‹ › replace
  it.

### What the list shows (*builder's call*)

- Hidden from the list (still served at `/_raw/` as today): names starting
  with `.` (including `.sitebin-*` markers, which Caddy never serves anyway),
  `__MACOSX` folders, `Thumbs.db`, `desktop.ini` — anywhere in the path.
  They are left out of the page itself, not just hidden by script.
- An explicitly chosen entry file is honoured even when it is hidden from the
  list.
- Each file carries its modification time (`mtime`, Unix seconds) in the
  wrapper's config, so the list can show and sort by it.

## Non-goals

- **No ZIP download of a folder for visitors.** It would be a new public
  endpoint streaming archives of whole sites to anyone (bandwidth, abuse).
  A follow-up if wanted.
- **No server-side thumbnails.** No image processing in the container and no
  thumbnail cache to keep in step; the grid uses the originals lazily.
- No path-style addresses, no change to `/_raw/`, to webserver mode (which
  never lists), to containers, or to the Caddyfile.
- No uploading, renaming or deleting from the viewer.
- No migration of existing viewer sites (see Start page).

## Anti-goals

- A file name is user content: it is only ever set as text
  (`textContent`, attributes), never parsed as HTML.
- No new CSP allowance and no third-party request: the list must work under
  the wrapper's own policy and the untrusted site policy (`img-src`/`media-src`
  `'self' blob:` already cover the previews).
- The page must stay light: nothing loads for a tile that is not on screen.

## Constraints

- No build step, no new library: plain ES modules next to `viewer.js`
  (`web/viewer/`), served from `/_sitebin/assets/viewer/` like it (unversioned
  assets are revalidated on every use, so a deploy reaches browsers at once).
- Old wrappers keep working with the new script: a config without `mtime`
  shows "—" and sorts those files last by date.
- Works on a phone: the toolbar wraps, the grid reflows, Details drops columns.
- Keyboard: tiles and rows are links, the sort headers are buttons with
  `aria-sort`, focus is visible.

## Left to the builder

Icon drawings, exact copy, layout details, how the folder tree is built in
the script, and how the edit page words the "File list" option — in the
product's dark/amber look.
