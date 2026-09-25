# Owner session on the edit page — plan

Design: [`../specs/2026-09-25-owner-session-edit-design.md`](../specs/2026-09-25-owner-session-edit-design.md).
Tests first; run `go test ./...` and `go test -tags ee ./...`.

1. **Seam** — `ext.SessionAccounts` (optional interface).
2. **Core** — `sessionOwns` in `internal/httpapi/server.go`, called in
   `withEditAuth` after `tokenOwns`; 401 for a missing password carries
   `account_url` when a session provider is active. Tests with a
   `sessionProvider` fake: owner session opens GET/PUT/DELETE; missing header,
   `Sec-Fetch-Site: same-site` / `cross-site`, a foreign account's session, an
   anonymous site, a provider without sessions, accounts disabled — all 401;
   absent `Sec-Fetch-Site` with the header is accepted; the per-site route
   answers no CORS preflight; MCP ignores the session.
3. **ee** — `provider.SessionAccount` over `currentAccount`. Tests: valid
   cookie → id; no cookie, bad cookie, signed-out (token version bumped) → no.
4. **Edit page** — send the header everywhere; boot tries without a password;
   lock-screen sign-in hint from `account_url`.
5. **Docs** — README (API auth + enterprise dashboard), `CLAUDE.md` (the
   boundary must not become `fromOwnBrowser`), website `/docs/using/`,
   `/docs/enterprise/`, `/docs/api/`.
6. **Ship** — merge to main, push, server rebuild + restart + live check,
   website push.
