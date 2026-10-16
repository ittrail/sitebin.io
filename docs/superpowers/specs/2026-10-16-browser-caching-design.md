# Browser caching

**Date:** 2026-10-16
**Status:** implemented

## Problem

Sitebin serves a site's files with an `ETag` and a `Last-Modified` and no
`Cache-Control`. Browsers then fall back to heuristic freshness — typically a
tenth of the time since the file last changed — so a page can be served stale
from the cache after an upload, while a build's content-hashed bundles, which
never change under their name, are re-checked over and over. Lighthouse's
"Serve static assets with an efficient cache policy" flags every one of them,
and an agent building a site for its owner told them it could not be fixed
from inside the site.

## What changes

Every file site gets a **cache policy**, a setting like the SPA fallback,
chosen on the edit page, through the JSON API (`cache`) and over MCP
(`update_site` / `create_site` settings). Sitebin turns it into a
`Cache-Control` header on every successful response (`2xx` and `304`).

| Policy | Pages and data | Fingerprinted build files | Other files |
|---|---|---|---|
| `auto` (default) | revalidate | 1 year, `immutable` | revalidate |
| `revalidate` | revalidate | revalidate | revalidate |
| `hour` / `day` / `week` / `year` | revalidate | the chosen time | the chosen time |
| `off` | — | — | — |

- **revalidate** is `max-age=0, must-revalidate`: the browser keeps the file
  but asks every time, and an unchanged file costs a `304` with no body. A
  visitor never sees an old page after an upload.
- **Pages and data** are `.html`, `.htm`, `.xhtml`, `.json`, `.xml`, `.txt`,
  `.webmanifest`, anything without an extension, directories, and every
  request that does not name an existing file (the SPA fallback answers those
  with `index.html`, and a missing file's answer must never be cached long).
  They always revalidate: they are what points at everything else.
- **Fingerprinted** means the file name carries a content hash — a `.`- or
  `-`-separated segment of 8–64 letters, digits or `_`, with a digit in it,
  that is lowercase hex or mixes upper and lower case (`index-BqCz7v8e.js`,
  `main.8f9e7d6a.css`, `entry-3fa9…c1.js`), or the file lives under
  `/_next/static/` or `/_app/immutable/`, which those frameworks keep
  content-addressed. Such a name never holds different bytes, so a year with
  `immutable` is safe — and it is what Lighthouse asks for. A hand-named file
  (`style.css`, `logo.png`, `report2024.pdf`) is never mistaken for one.
- The fixed times (`hour` … `year`) apply to every file but pages and data —
  the owner's promise that those files do not change under their names for
  that long. `year` is what a site of hashed builds wants when its hashes do
  not match the rule.
- `off` sends no `Cache-Control` — the old behaviour, for whoever relied on it.
- A site behind a **view password** gets `private` instead of `public`, so no
  shared cache keeps its files.
- **Container sites** are untouched: their upstream decides.

`auto` is the default for every site, existing ones included. Compared with
before, pages are fresher (no heuristic staleness) and hashed bundles are
cached properly; hand-named assets revalidate instead of being cached
heuristically — correct, and a `304` each.

## How

authz already answers every content request before the file server runs. For
a file site that passes the gate it now also answers `X-Sitebin-Cache` — the
complete `Cache-Control` value, or nothing for `off` — computed from the
policy, the request path and whether that path is an existing regular file in
the site (one `Stat` through the site's `os.Root`). Caddy copies it onto the
request (`copy_headers`, like the container upstream), strips any copy the
client sent before the gate, and sets `Cache-Control` from it with a response
matcher, `status 2xx 304`: an error page, a redirect or the gate is never
cached. All the rules live in Go (`internal/httpapi/cache.go`), testable; the
Caddyfile only copies a value.

## Surfaces

- `store.Meta.Cache` (`json:"cache,omitempty"`, `""` = `auto`), validated by
  `store.CleanCachePolicy`.
- API: `"cache"` in `PUT /api/sites/{edit}`, a JSON create body and a
  multipart create field; every settings document returns `"cache"`.
- MCP: `Settings.Cache`; `SiteResult.Cache` is omitempty and present only when
  the policy is not `auto`, so results for every other site keep the shape
  clients cached.
- Edit page: a "Browser caching" select in the Behavior card (file sites).
- Provenance: a settings change records `cache=<policy>`.

## Testing

`cache_test.go` for the rules (each policy, documents, fingerprints and the
names that must not count as one, missing files, private); authz returning the
header; the API round trip; the generated Caddyfile; and `e2e/caching.ps1`,
which checks the real headers through Caddy — including that a 404 carries
none.
