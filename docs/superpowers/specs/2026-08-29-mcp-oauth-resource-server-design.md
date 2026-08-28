# MCP OAuth — Sitebin as a resource server

*2026-08-29. Phase 2 of
[`2026-08-28-mcp-server-design.md`](2026-08-28-mcp-server-design.md).*

Sitebin's MCP server authenticates with account API tokens today. That works in
every client that can set a header, but the connector directories at Anthropic
and OpenAI require OAuth 2.1: a stranger's client must be able to register
itself, send the user through a consent screen, and receive a token scoped to
*this* server.

This design makes Sitebin an OAuth **resource server**. It does not make Sitebin
an authorization server, and that distinction is the whole shape of the work.

## The rule this design is built on

> **Sitebin is a resource server. It is never an authorization server.**

Sitebin points at an issuer — any OIDC/OAuth 2.1 authorization server: the
IT-Trail SaaS Stack's Keycloak, a self-hoster's own Keycloak, Auth0, Zitadel.
It validates tokens, publishes what it is, and refuses what it should. It never
issues a token, never registers a client, never renders a consent screen.

Three things follow, and each of them protects something Sitebin already
promises:

- **"One container, no dependencies" survives.** Running Sitebin never requires
  running an authorization server, let alone a specific one. Without an issuer
  configured, none of this code path exists at runtime.
- **The community build stays whole.** No provider means no accounts means
  nothing for a token to own. The OAuth path is inert there, exactly as
  `AccountsEnabled()` already makes the account gate inert.
- **We do not write security-critical code that Keycloak has already written.**
  An authorization server is DCR, consent, PKCE, refresh rotation, revocation
  and a login UI. Writing that to avoid a config setting would be a poor trade.

## Non-goals

- No authorization server in `ee/`, now or as a fallback.
- No coupling to the SaaS Stack. The stack is *an* issuer, named nowhere in the
  code.
- No new MCP tools. This changes who may call the twelve that exist.

## What the SDK already gives us

Most of the resource-server half ships with `modelcontextprotocol/go-sdk`,
which is already a dependency:

| Need | SDK |
|---|---|
| RFC 9728 metadata document | `auth.ProtectedResourceMetadataHandler(*oauthex.ProtectedResourceMetadata)` |
| `401` + `WWW-Authenticate: Bearer resource_metadata="…"` | `auth.RequireBearerToken(verifier, *auth.RequireBearerTokenOptions)` — ordinary `func(http.Handler) http.Handler` middleware |
| Audience claim matching | `oauthex.MatchesResource(claims []string, resource string)` |
| Token facts (scopes, expiry, user id) | `auth.TokenInfo` |

So the work is a token verifier, a config block, and a scope check — not a
protocol implementation.

## Configuration

Two new startup variables, both enterprise-tier in effect (they need accounts)
but read by the core, like every other `SITEBIN_*`:

| Variable | Default | Meaning |
|---|---|---|
| `SITEBIN_MCP_OAUTH_ISSUER` | `SITEBIN_OAUTH_OIDC_ISSUER` | The authorization server. **Empty disables everything in this document.** |
| `SITEBIN_MCP_OAUTH_RESOURCE` | `<SiteURL(BaseDomain)>/mcp` | This server's resource identifier — the value that must appear in a token's `aud`. |

Defaulting the issuer to the login issuer is deliberate: on an instance where
users already sign in through an OIDC provider, that provider is almost always
the right authorization server too, and the same `sub` then resolves the same
account. Setting it separately stays possible for the case where it is not.

The resource identifier is **immutable once published**. It is baked into every
issued token's audience and into every client's saved configuration, so it is
derived from the base domain and never from a request.

## Discovery

Mounted only when an issuer is configured:

```
GET /.well-known/oauth-protected-resource
GET /.well-known/oauth-protected-resource/mcp
```

Both, because clients disagree about which one to fetch: RFC 9728 defines the
path-suffixed form for a resource at a path, while several clients request the
bare one. Serving both costs one extra route and removes a class of "works in
Claude, not in X" bug.

The document names the resource, the issuer, the supported scopes, and
`bearer_methods_supported: ["header"]`.

## Authentication

`/mcp` gains a middleware wrapper — again, only with an issuer configured:

```go
handler := mcp.NewHandler(mcpOps{a}, info)
if a.cfg.MCPOAuthIssuer != "" {
    handler = auth.RequireBearerToken(a.verifyMCPToken, &auth.RequireBearerTokenOptions{
        ResourceMetadataURL: a.cfg.SiteURL(a.cfg.BaseDomain) + "/.well-known/oauth-protected-resource",
    })(handler)
}
```

**The middleware must not reject account API tokens.** `sbp_…` credentials keep
working exactly as they do now — that is a shipped, documented feature and an
OAuth rollout must not break it. So `verifyMCPToken` accepts both:

1. A credential with the `sbp_` prefix → hand to the existing token path,
   return `TokenInfo` with no scopes.
2. Anything else → verify as a JWT from the configured issuer: signature
   against the issuer's JWKS, `iss`, `exp`, and `aud` containing the resource
   identifier (`oauthex.MatchesResource`).

Because `RequireBearerTokenOptions.Scopes` is endpoint-wide and Sitebin needs
per-tool granularity, no scope is required at this layer. Scope enforcement
happens per tool (below).

**Audience validation is not optional.** Without it, a token minted for another
resource server on the same issuer would be accepted here. It is the single
check that makes a shared authorization server safe to share.

### The seam

`ext.Provider.BearerAccount(r) (string, bool)` cannot carry scopes, so it is
replaced by one richer method rather than joined by a second:

```go
// BearerCredential resolves an Authorization: Bearer credential to what it
// grants. ok=false means no usable credential was presented — a wrong one and
// an absent one are the same answer.
//
// Scopes is empty for an account API token, which grants everything its
// account can do; that is what the credential has always meant and the core
// treats an empty slice as "unrestricted". An OAuth access token carries the
// scopes the user consented to.
BearerCredential(r *http.Request) (Credential, bool)

type Credential struct {
    AccountID string
    Scopes    []string
}
```

Two call sites change: `API.tokenOwns` (which only needs the account id) and
`mcpOps.Authenticate`. The extension decides which kind of credential it was
looking at — the core never learns the difference, which is the same division
of labour the seam already draws.

`mcp.Auth` gains `Scopes []string` alongside `AccountID`.

## Scopes

Two, named on the stack-wide convention `<appId>:<resource>:<action>`:

| Scope | Grants |
|---|---|
| `sitebin:sites:read` | `list_sites`, `get_site`, `list_files`, `read_file`, `download_site` |
| `sitebin:sites:write` | `create_site`, `update_site`, `write_files`, `delete_file`, `delete_site`, `add_domain`, `remove_domain` |

The split is worth having on its own merits: "this agent may look at my sites
but may not publish" is a real thing to want when the agent is publishing to
the open web under your account.

Each tool declares its scope in the catalog in `internal/mcp/server.go`. It is
declared explicitly rather than derived from `ToolAnnotations.ReadOnlyHint`,
even though the two agree today — an annotation is a hint to a model, a scope
is an authorization decision, and quietly deriving one from the other means a
future annotation tweak silently changes who may call what.

**An empty `Auth.Scopes` means unrestricted.** That keeps `sbp_` tokens and the
community build behaving exactly as they do now, and makes the scope check a
pure addition rather than a change.

A refused call returns the usual MCP tool error naming the missing scope, so
the agent can tell "I may not do this" from "this failed".

## Testing

- `internal/mcp` — a tool called without its scope is refused; with it, allowed;
  empty scopes allow everything. Against the fake `Ops`, no issuer needed.
- `internal/httpapi` — the discovery documents are served and well-formed; the
  routes are absent with no issuer configured; a token for the wrong audience is
  refused; an `sbp_` token still works with OAuth enabled (the regression that
  matters most); the `401` carries `WWW-Authenticate` with the metadata URL.
- Both build tags, as always. The community build must still serve MCP with no
  issuer, no provider, and no OAuth routes mounted.
- JWT verification is tested against a locally generated key pair, not a live
  issuer — the tests must not need a network.

## Rollout

Additive at every step, which is what makes it safe to ship before the stack
side is finished:

1. Ship the code with `SITEBIN_MCP_OAUTH_ISSUER` unset. Nothing changes.
2. Configure the issuer on `app.sitebin.io`. Discovery appears; `sbp_` tokens
   keep working; OAuth tokens start being accepted.
3. Apply for the connector directories.

## What this design depends on, and does not contain

The authorization server must offer dynamic client registration (RFC 7591),
PKCE `S256`, and tokens whose `aud` is this server's resource identifier. How
that is configured on the IT-Trail SaaS Stack — client scopes, the audience
mapper, DCR policies, the onboarding contract — is the stack's own design and
is deliberately not described here. Sitebin's side is finished when it can
validate a correct token from any issuer that does those three things.
