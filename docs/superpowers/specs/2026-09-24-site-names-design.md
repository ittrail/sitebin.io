# Site names and custom domains in the site list

**Date:** 2026-09-24
**Status:** implemented

## Problem

The account dashboard lists an account's sites by their view URL:

```
https://bzpsxlx7hbeazvb4mznrfna33r.sitebin.app
webserver · 17.8 KB · 3 files · no expiry
```

Six random 26-character hosts are indistinguishable. The site's custom
domain — the one handle a person actually remembers — is known to the core
(`ext.SiteInfo.Domains` already carries it for the admin console) but the
dashboard never shows it, and a site without one has nothing human at all.

## What changes

1. **The dashboard shows each site's custom domains**, verified ones as links
   to where they serve, and claims still waiting for their DNS proof marked as
   pending (they serve nothing, but an owner who just added one needs to see
   it is waiting).
2. **A site can carry an optional name** — a private label the owner chooses.
   The dashboard shows it above the URL; the edit page shows it as the page
   heading and lets it be changed in the settings.
3. **The API and MCP set and return it**, like every other setting.

## The name

- `store.Meta.Name`, `json:"name,omitempty"`. Rule 1 of the codebase: older
  sites have no such field and read as unnamed; no migration.
- **Validated in one place**, `store.CleanSiteName`: control characters are
  refused (not stripped — a stripped name would silently differ from what was
  typed), surrounding space is trimmed, at most **60 characters** (the same cap
  as token and form names). An empty name clears it. Every writer — the JSON
  API, MCP, the dashboard — goes through it.
- **Private.** It is shown wherever the site is *managed* — dashboard, edit
  page, admin register, API, MCP — and never where it is *served*. Nothing in
  Caddy, the viewer or authz reads it. It is not a title and does not change
  the site's URL.
- **Not a handle.** No tool or route addresses a site by name; names need not be
  unique. The edit id stays the only address.

## Surfaces

**JSON API.** `name` is one more field of the settings document: accepted by
`PUT /api/sites/{edit}` (`{"name": "Client docs"}`, `""` clears), by a JSON
create body, and by a multipart create as a `name` form field; returned as
`"name"` by every payload (empty string when unnamed, so a client never has to
tell "absent" from "unnamed"). A bad name is a `400` with the rule in the
message.

**MCP.** `Settings.Name` (so `create_site` and `update_site` take it, `""`
clears), `SiteResult.Name` and `SiteSummary.Name` (so `get_site` and
`list_sites` return it). No new tool: renaming is a setting, and a new tool
would be a contract change for every saved connector configuration.

**Edit page.** A "Name" field at the top of the settings, saved with a button
(not on every keystroke), with a Clear button. The heading shows the name, and
the view id beneath it, when there is one.

**Account dashboard.** Per site: the name (when set), the view URL, the custom
domains, then the usual mode/size/files/expiry line, and under it a small
**Rename** (or "Add a name") disclosure; the button row is unchanged. It exists
because the edit page needs the site's edit password,
and an owner who does not have it to hand would otherwise have to *reset* it to
name a site — which breaks any deploy that uses the old one (sitebin.io's own
deploy does). The dashboard's CSP runs no script, so the control is a
`<details>` disclosure holding a plain POST form to
`/account/sites/{id}/name`, behind the CSRF token and the same ownership check
as rotate and delete.

**Admin register.** Shows the name under the view id and matches it in the
search box; operators get the same handle owners do.

## The seam

- `ext.SiteInfo` gains `Name` and `DomainLinks []ext.DomainLink` (domain, the
  URL it serves at, pending). The URL is built in the core
  (`config.SiteURL`), because the scheme and port are the core's business; the
  dashboard never assembles one.
- `ext.SiteService` gains `SetName(viewID, name string) error`, which validates
  through `store.CleanSiteName` and reports a vanished site as `ErrSiteGone`,
  like its neighbours. The error for a bad name is user-safe and shown as-is.

## Out of scope

- Sorting the dashboard by name, and search there. Six sites do not need it.
- Showing the name to visitors (e.g. as a page title). It is a label for the
  owner, and making it public would make it content.
