# Plan: the workspace UI

Design: `docs/superpowers/specs/2026-11-06-workspace-ui-design.md`.

## Backend (tests first, both build tags)

1. `account.Account.UI` + `POST /account/ui` (CSRF, value check) + "Try the
   new interface" form on the classic dashboard.
2. `uiWriter` middleware on every PublicRoutes handler: marks workspace
   accounts (frame) and `X-Sitebin-UI: json` requests (JSON answers from
   `redirect`, `renderMessage`, `leaveTo`, `renderLocked`).
3. Twin templates: every ee page template parsed with the classic head and
   with the workspace head; `Execute` picks by the writer's mark.
4. `GET /account` → `app.html` for workspace accounts; `GET /account/ui/{file}`
   embedded assets; CSP with `script-src 'self'`.
5. `GET /account/api/state`: explicit DTOs (sites, tags, groups, tokens,
   zones, takeovers, invites, plan, billing, licence, links, flags).
6. Core: `ext.SiteInfo` `UpdatedAt`, `ContainerEnabled`, `ContainerStatus`;
   optional `ext.EditPages`; `editPage` asks it first.
7. ee `ServeEditPage`: workspace account → `site.html`.
8. `GET /account/api/sites/{id}/activity` (owner/admin, redacted).
9. Operator JSON: register (+ figures, IP panel), reports, containers, trail,
   account; health.

## Frontend (`ee/ui/`)

10. `app.css`: the mockup's tokens and components, plus restyling of the
    markup edit.js renders and of classic templates inside the frame.
11. `shell.js`: sidebar from state, Ctrl+K palette, toast, dialogs (message,
    secret, confirm), menus, `post()` (JSON mode), `api()` helpers.
12. `app.js`: Sites (table, chips, group-by, sort, search, bulk, needs-you,
    row menu, visitors), Shared, Inbox (+ adopt), Tags, Domains & zones,
    Tokens & agents, Plan & usage, Account; Instance: Overview, Register
    (+ side ticket), Reports, Containers.
13. `site.html` + `site.js`: ticket header, tab bar over edit.js's tabs,
    Overview, People, Activity, J/K, Esc, list order from the shell.

## Verify

14. `go vet`, `go test ./...`, `go test -tags ee ./...`.
15. Enterprise image, local run with local auth; walk the parity table in
    Chrome in both interfaces; `accounts.ps1`, `ownership.ps1`.
16. Corrections block in the design doc for whatever review changes.
