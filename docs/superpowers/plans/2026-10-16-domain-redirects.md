# Domain redirects and the generated address — plan

Spec: `docs/superpowers/specs/2026-10-16-domain-redirects-design.md`.
Tests first in every task; both suites (`go test ./...`, `go test -tags ee ./...`)
green before moving on.

1. **store: rules and setters** — `internal/store/redirects.go`: `Redirect`,
   `ViewAddress`, `RedirectCodes`, `ErrBadRedirect`; `Meta.DomainRedirects`,
   `Meta.ViewAddress`; `Meta.DomainRedirect(host)`, `Meta.ViewRule()`,
   `Meta.ServingDomains()`; `Store.SetDomainRedirect`, `Store.SetViewAddress`;
   `RemoveDomain` prunes. Tests: `redirects_test.go`.
2. **httpapi: authz** — address rule after lock/expiry, before the gate;
   `Location` from `cfg.SiteURL(to)` + `X-Forwarded-Uri` (path-view prefix
   stripped); cache headers; `off` → 404. Forms and unlock refuse a
   non-serving address. Tests in `redirects_test.go`.
3. **httpapi: routes and payload** — `PUT|DELETE /api/sites/{edit}/domains/{domain}/redirect`,
   `PUT /api/sites/{edit}/view-address`, provenance actions, container 409;
   `domain_redirects`, `view_address`, `primary_url` in `sitePayload`.
4. **MCP** — `set_domain_redirect`, `set_view_address` in the catalog and
   server; `SiteResult.DomainRedirects` / `ViewAddress` (omitempty); adapter
   in `mcpops.go` calling the same helpers as the API.
5. **Dashboard** — `ext.DomainLink.RedirectTo`, `ext.SiteInfo.ViewOff` and
   `PrimaryURL`; template.
6. **Edit page** — generated-address row and per-domain dropdowns in
   `edit.js`/`edit.html`/`app.css`; "open site" follows `primary_url`.
7. **e2e** — `e2e/redirects.ps1` against the enterprise image.
8. **Docs** — README, `CLAUDE.md` (custom domains section), website
   `docs/custom-domains/`, `docs/mcp/`, `docs/api/`.
9. **Ship** — push, deploy app.sitebin.io per the ops memory, verify live,
   then push the website.
