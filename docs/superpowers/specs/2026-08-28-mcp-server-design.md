# MCP server — design

*2026-08-28*

Sitebin gets a Model Context Protocol server so an AI agent can publish and
manage sites directly, the way a script uses the JSON API today. It is a second
*transport* onto the authorization the API already has — not a second set of
rules.

The eventual goal is to list the hosted instance (`app.sitebin.io/mcp`) as an
official connector with Anthropic and OpenAI. That target shapes two decisions
here: the transport is Streamable HTTP (what those clients speak), and the
credential resolution is a single seam that OAuth can later replace without
touching a tool handler. OAuth itself is Phase 2 and explicitly out of scope
below.

## Why this is not an `ee/` feature

The obvious reading of "MCP is a premium capability" would put the whole server
in `ee/`. That is wrong, and for the same reason the JSON API is not in `ee/`:

- The **API** lives in `internal/httpapi` and is fully open in the community
  build. What is enterprise is *accounts* — and accounts gate the API through
  `apiAllowedFor`/`AuthorizeCreate`, from the outside.
- **Account API tokens** are `ee/`. They are the credential, not the surface.

MCP follows exactly that split. The protocol server is MIT core; a community
instance gets a working MCP endpoint with no accounts and no tokens, precisely
as it gets a working API. On an instance with accounts, MCP inherits every
restriction the API has, through the same `ext.Provider` calls. Putting the
server in `ee/` would mean a second implementation of the tool surface for the
community build, or no MCP there at all — both worse.

## Architecture

Three pieces, each understandable on its own:

```
internal/mcp/          MIT. Protocol + tool catalog. Knows the MCP SDK and
                       the Ops interface. Knows nothing about the store,
                       Caddy, quotas or accounts.
        │  mcp.Ops (interface)
        ▼
internal/httpapi/mcpops.go
                       MIT. The adapter: implements mcp.Ops on top of the
                       API's existing helpers — applySettings, sitePayload,
                       syncViewerLayout, consumeUploads' store calls, the
                       rate limiters, verifyCache, apiAllowedFor.
        │  ext.Provider
        ▼
ee/                    ELv2. Resolves a Bearer token to an account and lists
                       that account's sites. Already exists, bar one method.
```

The boundary earns its place: `internal/mcp` can be tested against a fake `Ops`
with no filesystem, and `mcpops.go` can be tested through real JSON-RPC
requests without a fake anything. Neither file needs to hold the other in
context.

### Dependency

`github.com/modelcontextprotocol/go-sdk` v1.7.0 (MIT). Importing `mcp` pulls
eight indirect modules; Sitebin already has two of them (`golang.org/x/oauth2`,
`golang.org/x/sys`), so the real cost is **six new modules**:
`google/jsonschema-go` (schema inference for tool arguments),
`segmentio/encoding` + `segmentio/asm` (its JSON codec),
`yosida95/uritemplate`, `golang.org/x/sync` and `golang.org/x/time`. All
permissive, all compatible with the MIT core.

That is not nothing for a codebase that prides itself on having no database and
no build step, so it is worth saying why it is still the right trade. This
server exists to be consumed by Anthropic's and OpenAI's clients. Conformance
with a spec those clients track — and which revises on its own schedule — *is*
the feature; a hand-rolled JSON-RPC transport would be ~300 lines today and a
standing obligation to chase every revision afterwards. Sitebin already carries
heavier dependencies for less (`ftpserverlib`, `afero`, `go-oidc`).

An earlier draft of this document claimed the SDK had zero transitive
dependencies. That was measured wrongly — `go get` into a module that imports
nothing prunes the module graph — and the numbers above are the corrected ones.

### Endpoint

`POST|GET|DELETE /mcp` on the main domain, mounted in `API.Public()`. The main
domain reverse-proxies every path to the backend, so **no Caddy change is
needed**. Streamable HTTP uses POST for JSON-RPC, GET for the optional SSE
stream, DELETE to end a session.

Controlled by `SITEBIN_MCP_ENABLED` (default `true`). When false the route is
not mounted at all and `/mcp` 404s like any other unknown path. The default is
`true` because the endpoint grants no authority the API does not already grant
to the same caller; it is a transport, and an operator who has an API has
already accepted this surface.

## Authentication

A session's identity comes from the HTTP request that opens it. There are
exactly three states, and each is the state the JSON API would be in for the
same caller:

| Request | Session | `create_site` | Per-site tools |
|---|---|---|---|
| `Authorization: Bearer sbp_…`, provider active | account-scoped | allowed; site is owned by that account and stamped with its tier caps | the token stands in for the edit password on sites that account owns; other sites still need `edit_password` |
| no token, accounts enabled | anonymous | refused, with the same message `createSite` gives: sign in at `/account` | `edit_id` + `edit_password` required, and an anonymous site is refused exactly as `apiAllowedFor` refuses it |
| no token, community build (no provider) | open | allowed | `edit_id` + `edit_password` |

Two properties follow from routing everything through the existing helpers
rather than re-deriving them:

- **`fromOwnBrowser` never applies.** It is the API's browser-shaped escape
  hatch for Sitebin's own pages; an MCP client is never Sitebin's own page. MCP
  passes a request with no fetch metadata, so `apiAllowedFor` falls through to
  the ownership check, which is the correct and stricter answer.
- **Rate limits and the Argon2 verify cache are shared with the API**, because
  they are the same limiter objects on the same `API` struct. An agent cannot
  brute-force an edit password faster than curl can.

`ext.Provider.BearerAccount` already exists and is used unchanged. The seam
grows by exactly one method:

```go
// AccountSiteIDs returns the view ids of the sites an account owns, for the
// MCP list_sites tool. ok=false means the account is unknown or accounts are
// not enabled. The core resolves each id through its own store: the extension
// says which sites, the core says what they are.
AccountSiteIDs(accountID string) ([]string, bool)
```

Without a provider (community build) `list_sites` reports that it needs an
account token, rather than failing — the community build has no accounts, so
"the sites you own" is not a question it can answer.

### Where Phase 2 attaches

All credential resolution happens in one function, `mcpops.go`'s `Authenticate`,
which turns an `*http.Request` into an `mcp.Auth{AccountID, Anonymous}`. OAuth
adds a second branch there (a validated access token resolving to an account)
and a set of discovery endpoints. No tool handler changes.

## Tool catalog

Full parity with the JSON API. Twelve tools:

| Tool | API equivalent |
|---|---|
| `create_site` | `POST /api/sites` |
| `list_sites` | *(new — dashboard equivalent)* |
| `get_site` | `GET /api/sites/{editID}` |
| `update_site` | `PUT /api/sites/{editID}` |
| `list_files` | *(the `files` field of `get_site`, as its own tool)* |
| `read_file` | `GET /api/sites/{editID}/content/{path}` |
| `write_files` | `POST /api/sites/{editID}/files[?replace=true]` |
| `delete_file` | `DELETE /api/sites/{editID}/files/{path}` |
| `delete_site` | `DELETE /api/sites/{editID}` |
| `add_domain` | `POST /api/sites/{editID}/domains` |
| `remove_domain` | `DELETE /api/sites/{editID}/domains/{domain}` |
| `download_site` | `GET /api/sites/{editID}/download` |

`POST /api/report` is deliberately excluded. It is a public abuse-reporting
endpoint for humans who found a bad site; an agent filing abuse reports is
noise, and the tool would be a spam vector with no legitimate agent use.

### File transport

MCP arguments are JSON, so files cannot be multipart. Every tool that carries
file content uses one shape:

```json
{ "path": "assets/app.js", "text": "…" }
{ "path": "logo.png", "base64": "…" }
```

Exactly one of `text` or `base64` per file. `text` is UTF-8 and is the normal
case — an agent writes HTML, CSS and JS. `base64` exists so an agent can move a
small image without a second channel.

A per-call cap of **8 MiB of decoded content** applies, well under
`MaxSiteBytes`. This is not a security limit (the store's quota is), it is a
protocol sanity limit: a request larger than that through a JSON-RPC body and
an LLM's tool-call serializer is a mistake, not a use case. Sites larger than
that are what WebDAV, FTP and the zip API endpoint are for, and the error says
so.

`download_site` returns the zip as an MCP embedded resource blob, not as text,
and refuses above the same 8 MiB — an agent should not pull 40 MB into a
context window.

### Site addressing

Every per-site tool takes `edit_id`, plus `edit_password` when the session's
token does not already own the site. `list_sites` returns `edit_id` alongside
`view_url`, so a token-authenticated agent can go from "my sites" to "edit this
one" without ever seeing a password.

### Errors

A refused tool call returns an MCP result with `isError: true` and the *same
message text* the JSON API returns for that condition, including the
`/account` upgrade hint. An agent reads these messages; they are the only
documentation it gets at the moment it is stuck, so they must say what to do
next rather than only what went wrong.

## The `mcp` provenance marker

`store.Meta` gains:

```go
Origin string `json:"origin,omitempty"` // "mcp" when created through the MCP server; empty = UI or API
```

`omitempty` and a missing field meaning "not MCP" keeps every existing
`meta.json` valid with no migration, per the filesystem-is-the-database rule.

It is **provenance only**. It does not affect `Trusted`, quotas, lifetimes or
any gate: a paying customer on a trusted tier gets the same site through MCP
that they get through curl. Recording it is cheap insurance — if agent-driven
phishing ever becomes a pattern, the admin console can filter for it, and that
question is unanswerable after the fact if the field was never written. It is
surfaced through `ext.SiteInfo.Origin` and in `sitePayload`.

## Testing

- `internal/mcp` — tool schemas, argument validation, the file-shape rules and
  error mapping, against a fake `Ops`. No filesystem.
- `internal/httpapi` — real JSON-RPC over `/mcp`: `initialize`, `tools/list`,
  every tool call, and the full authentication matrix above.
- Both build tags. The token-authenticated paths only compile under `ee`, and
  the community build needs its own test that MCP works *without* a provider —
  the corollary in CLAUDE.md that every seam addition must stay whole.
- `e2e/mcp.ps1` against the Docker image, in the shape of the existing focused
  scripts.

## Out of scope (Phase 2 — OAuth 2.1)

Listing as an official connector with Anthropic and OpenAI requires OAuth 2.1
with dynamic client registration, not bearer tokens. That is a separate design:

- `GET /.well-known/oauth-protected-resource` (RFC 9728) advertising the
  authorization server, served by the core so the community build can point at
  an external AS.
- `GET /.well-known/oauth-authorization-server` (RFC 8414), dynamic client
  registration (RFC 7591), and an authorization-code + PKCE flow with a consent
  screen rendered against the existing account session — all in `ee/`, because
  all of it presupposes accounts.
- Access tokens as a third `Authenticate` branch, scoped like the API tokens
  they sit beside.

Until then, MCP is reachable from Claude Code (`claude mcp add --transport http
… --header`), Claude Desktop and ChatGPT via a bearer header, which is the
normal way to consume a self-hosted MCP server.
