# Plan — provenance, stricter untrusted CSP, view-domain apex

Design: `docs/superpowers/specs/2026-09-29-provenance-csp-apex.md`. Each task
is test-first; both suites (`go test ./...`, `go test -tags ee ./...`) and
`go vet` stay green after each.

1. **`internal/provenance`** — `Entry`, `Record` (merge window 10 min, cap
   100 keeping the origin line, atomic rewrite), `Read`, `Purge`,
   `CleanUA`, `ParseMatch` (IP or CIDR). Unit tests.
2. **Store** — `RecordProvenance` / `Provenance` / `PurgeProvenance` on
   `sites/<id>/provenance.jsonl` under the stats lock; no resurrection after
   `Delete`.
3. **Core recording** — actor in the request context from `editAuth` /
   `withUploadAuth` (upload tokens remember their issuing account); record on
   create (API/UI/MCP), upload, replace, delete-file, settings, domains,
   forms, containers, WebDAV, MCP write tools, open_upload, FTP (optional
   recorder on the authenticator). Mirror `site-create` / `site-delete` into
   `ext.AccountProvenance`.
4. **Seam** — `ext.SiteProvenance` (optional on `SiteService`),
   `ext.AccountProvenance` (optional on `Provider`); `siteService`
   implements the first, including `SitesSeenFrom`.
5. **Sweep** — purge site logs past `provenance.Retention` (locked sites
   skipped), call `PurgeProvenance` on the provider.
6. **ee accounts** — account log in `accounts/<id>/`; sign-up / sign-in on
   OIDC callback, local sign-up and login, MCP provisioning (request info via
   context); token mint; `PurgeProvenance` with suspended / locked holds;
   dashboard rename / rotate / delete recorded.
7. **GDPR export** — the account log plus the account's own entries on its
   sites.
8. **Register** — creator + last write per row, Trail page, account page,
   address search (IP/CIDR) with the accounts panel.
9. **CLI** — `sitebin list` FROM column, `sitebin provenance <id|ip>`.
10. **CSP** — `SITEBIN_CSP_{SCRIPT,STYLE,FONT,IMG}_HOSTS`, validated; the
    untrusted policy built from config; exact-string tests on both matchers.
11. **Apex** — `SITEBIN_ABUSE_CONTACT`, `SITEBIN_ABUSE_REPORT_URL`,
    `SITEBIN_HOME_URL`; Caddy block for `<view>` + `www.<view>`; backend
    host guard with info page, security.txt, 404; base-domain security.txt;
    abuse line on the 410 suspended page.
12. **`.well-known` uploads** — tests on every write path and on listing.
13. **Docs** — README env table and provenance section, CLAUDE.md rules.
14. **Verification** — build the enterprise image, run it HTTP-only with an
    untrusted anonymous tier, and check with headless Chrome that the kit's
    `fetch` and an image beacon are blocked while Google Fonts, a jsDelivr
    library and the site's own images load; curl the headers, the apex and
    security.txt.
