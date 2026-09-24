# Site names and domains in the site list — plan

Design: [`../specs/2026-09-24-site-names-design.md`](../specs/2026-09-24-site-names-design.md).
Tests first in every task; run `go test ./...` and `go test -tags ee ./...`.

1. **store** — `Meta.Name` (`omitempty`); `CleanSiteName` + `ErrBadSiteName`
   in `internal/store/sitename.go`, with a table test (trim, empty clears,
   61 runes refused, 60 multi-byte runes accepted, CR/LF/tab refused).
2. **JSON API** — `updateSet.Name`; `settingsFromForm` reads a `name` field
   (present-but-empty clears); `writeSettings` validates through
   `CleanSiteName` (400); `sitePayload` returns `"name"`. Tests: PUT sets,
   PUT `""` clears, PUT over-long is 400 and leaves the old name, multipart
   create with `name`.
3. **Seam** — `ext.SiteInfo.Name`, `ext.DomainLink` + `SiteInfo.DomainLinks`
   (verified with `config.SiteURL`, then pending claims), and
   `SiteService.SetName`. Tests on `siteService`: Info carries name + links,
   SetName validates and maps a missing site to `ErrSiteGone`.
4. **MCP** — `Settings.Name`, `SiteResult.Name`, `SiteSummary.Name`;
   `settingsToUpdateSet` maps it; `update_site` description mentions it.
   Tests: `update_site` sets and clears, `create_site` with a name,
   `list_sites` returns it.
5. **Dashboard (ee)** — site card shows name, domain links, pending claims;
   `<details>` Rename form → `POST /account/sites/{id}/name` (CSRF + `owns`).
   `fakeSites.SetName`. Tests: render shows name/domains/pending, rename
   stores, foreign site 403, bad CSRF 403, bad name shows the rule.
6. **Admin register** — name under the view id, matched by the search.
7. **Edit page** — Name option + Save/Clear; heading shows the name.
8. **Docs** — README (API example, MCP table, dashboard), website
   `/docs/api/`, `/docs/mcp/`, `/docs/enterprise/`, `/docs/using/`.
9. **Ship** — product push, server rebuild + restart + live check, website push.
