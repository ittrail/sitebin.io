# Folder browser on the edit page

**Date:** 2026-09-25
**Status:** implemented

## Problem

The edit page's Files card lists every file of a site as one flat list of full
paths. A container site's `node_modules` turns that into thousands of rows
(`app/node_modules/@emnapi/runtime/dist/…`), and the list is cut at the first
2000 entries (`maxListedContainerFiles`), so most of such a site cannot be
reached at all.

## What changes

The Files card browses the site like a file manager:

- It shows **one folder at a time**: its sub-folders first, then its files,
  each sorted by name (case-insensitive). A folder row opens the folder.
- **Breadcrumbs** (`files / app / node_modules`) jump to any ancestor, and a
  **Back** button goes up one level. The folder is kept in the URL fragment
  (`#dir=app/node_modules`), so the browser's own back button and a reload
  work too.
- At most **15 entries** are shown; a **"Show N more"** link reveals the rest
  of the folder. No paging.
- File rows keep what they had: a link to the file on the site (not for a
  container site, which serves its app, not its files), the size, **Edit** for
  text files and **Delete**.
- Uploading plain files or a folder while inside a folder puts them **into
  that folder** (the drop area says so); a `.zip` and "replace all" still act
  on the whole site.

## The listing comes from the server, one folder at a time

`GET /api/sites/{editID}/dir?path=<folder>` answers
`{"path": "<folder>", "entries": [{"name", "dir", "size"}], "truncated"}` for
the immediate children of one folder of the content root. It goes through
`withEditAuth` like every other edit route (edit password, owning account
token, owner's session; an upload token is refused).

`store.ListDir` reads the folder through the content root's `os.Root`, so a
link a container planted cannot lead the listing out of the site. It lists
directories and regular files only — links, sockets and pipes a container
left are not content, exactly as `ListFiles` counts only regular files — and
leaves out Sitebin's own markers at the top. A folder with more than 10,000
entries is cut there and says `truncated`. A bad path is a 400, a folder that
does not exist a 404.

Doing this on the server, not by building a tree from the site payload's
`files`, is what makes a container site fully navigable: that list is capped
at 2000 and would leave most folders empty.

## Out of scope

- Deleting or renaming whole folders from the page.
- Paging within a folder.
- Folder sizes or item counts (they would cost a walk of every sub-tree).
