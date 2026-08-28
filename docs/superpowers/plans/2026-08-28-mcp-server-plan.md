# MCP server — implementation plan

Design: [`../specs/2026-08-28-mcp-server-design.md`](../specs/2026-08-28-mcp-server-design.md)

Tests first on every task. Run both build tags (`go test ./...` and
`go test -tags ee ./...`) — they cover different paths.

## Task 1 — the provenance marker

`store.Meta.Origin string \`json:"origin,omitempty"\``, `ext.SiteInfo.Origin`,
and `"origin"` in `sitePayload`. A `store.SetOrigin` helper alongside
`SetTrusted`.

- Test: a `meta.json` written before this field round-trips with `Origin == ""`.
- Test: `SetOrigin` persists and `sitePayload` reports it.

## Task 2 — the seam method

`ext.Provider.AccountSiteIDs(accountID string) ([]string, bool)`, implemented in
`ee/provider.go` over `p.accounts.ListSiteIDs`.

- Test (ee): returns the account's ids; `ok=false` for an unknown account and
  when accounts are disabled.
- Test (core): every existing `ext.Provider` test double still compiles — this
  is the whole cost of a seam addition, and it must be paid visibly.

## Task 3 — config

`SITEBIN_MCP_ENABLED`, default true, as `config.Config.MCPEnabled`.

- Test: default, explicit `false`, and a malformed value erroring like the
  other bool vars.

## Task 4 — `internal/mcp`: the Ops interface and the auth type

No SDK yet. Define `Auth`, `Ops`, the input/output structs, the file-shape
rules (`text` xor `base64`, the 8 MiB decoded cap), and `DecodeFiles`.

- Test: `text` only, `base64` only, both set → error, neither set → error,
  bad base64 → error, over-cap → the "use WebDAV/FTP/zip" error.

## Task 5 — `internal/mcp`: the server and tool catalog

Wire the SDK. `NewHandler(ops Ops, opts) http.Handler` using
`mcp.NewStreamableHTTPHandler`, building a per-request server bound to
`ops.Authenticate(r)`. All twelve tools with schemas, descriptions and the
server `instructions` string.

- Test: `initialize` reports the server; `tools/list` lists all twelve with
  non-empty descriptions; each tool call reaches the fake `Ops` with the parsed
  arguments; an `Ops` error becomes `isError: true` carrying the message.

## Task 6 — `internal/httpapi/mcpops.go`: the adapter

Implement `mcp.Ops` over the existing helpers. `Authenticate` resolves the
bearer token through `ext`. A shared `openSite(auth, editID, editPassword)`
does the ownership-or-password check exactly as `withEditAuth` does, including
`apiAllowedFor` and the rate limiters.

- Test: the authentication matrix from the design, both build tags.

## Task 7 — mount it

`API.Public()` mounts `/mcp` when `cfg.MCPEnabled`. `create_site` stamps
`Origin = "mcp"`.

- Test: real JSON-RPC over the mounted handler creates a site whose
  `meta.json` records `origin: "mcp"`; disabled config 404s.

## Task 8 — docs

README section "MCP server (for AI agents)" with `claude mcp add`, the config
row for `SITEBIN_MCP_ENABLED`, the tool table, and a pointer at the token
section. `CLAUDE.md` layout list gains `mcp`. Website docs page + the workspace
`CLAUDE.md` coupling note.

## Task 9 — E2E

`e2e/mcp.ps1` in the shape of `e2e/accounts.ps1`: initialize, tools/list,
create a site through MCP, fetch it over HTTP, verify the origin marker, delete
it.

## Task 10 — ship

Product repo first (per the workspace ship order), then the website. The
instance update on `app.sitebin.io` is a separate manual step, flagged to the
user.
