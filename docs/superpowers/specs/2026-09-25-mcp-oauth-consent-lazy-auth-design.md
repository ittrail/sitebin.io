# MCP OAuth — consent, lazy authentication and scopes

*2026-09-25. Follows
[`2026-08-29-mcp-oauth-resource-server-design.md`](2026-08-29-mcp-oauth-resource-server-design.md),
which shipped but was never switched on. An audit before switching it on found
three things that had to change first; this design is those changes.*

## The three rules this design is built on

These are the operator's decisions, not trade-offs still open:

1. **The consent gate is never bypassed.** A person who has not accepted the
   platform's and Sitebin's current documents gets no token that Sitebin
   honours. The gate appears *in the OAuth sign-in*, the same page a browser
   sign-in shows.
2. **The edit password stays.** OAuth is an additional way in, not a
   replacement. `/mcp` accepts an edit password, an account API token, or an
   OAuth access token, and turning OAuth on removes none of them.
3. **Scopes mean what they say.** A token granted `sitebin:sites:read` can read
   and cannot write, anywhere.

## 1 · Consent

### Where the gate is shown

The stack's consent gate lives on the Auth Gateway's authorization endpoint.
An MCP client discovers the authorization server from Sitebin's
protected-resource metadata (`authorization_servers` = the realm issuer) and
then fetches the RFC 8414 document at the **path-insertion** URL
(`https://auth.<domain>/.well-known/oauth-authorization-server/realms/<realm>`).
The stack now serves that document from the gateway with
`authorization_endpoint` pointing at the gateway's MCP gate, and the issuer
unchanged — so the client's issuer checks (RFC 8414 §3.3, RFC 9207) still hold,
and the browser passes the gate before Keycloak issues a code. That is stack
work, specified in the stack repo (`docs/mcp-authorization.md`, "Consent").
Sitebin's metadata does not change for it.

### Why Sitebin checks as well

Steering is not a lock: a client that ignores the metadata can send the browser
to Keycloak's own endpoint and get a token without seeing the gate. So Sitebin,
the resource server, refuses a token whose subject has an outstanding required
document. It asks the stack:

```
GET {SITEBIN_STACK_URL}/api/v1/apps/{appId}/users/{sub}/consent/status
Authorization: Bearer <stack admin key>
→ 200 {"complete": true|false, "outstanding": [...], "gate": "enabled"|"disabled"}
```

- Only where stack registration is configured (`SITEBIN_STACK_URL`,
  `SITEBIN_STACK_APP_ID`, admin key) — the instance that has a gate to ask
  about. A self-hosted instance pointed at another issuer has no gate, and
  behaves as before.
- **Positive answers are cached** per subject for 10 minutes. Negative answers
  are never cached: the next request after the person accepted must succeed.
- **Fails closed.** A stack that cannot answer means Sitebin cannot know, and a
  token is refused rather than waved through. The positive cache absorbs short
  outages for everyone already in.
- A refused token is a `401 invalid_token`. The client signs in again, is
  steered through the gate, accepts, and the new token works.

### New users no longer loop

The previous design refused a valid token whose subject had never signed in to
Sitebin, because creating the account from a token would have skipped the gate.
With the gate enforced above, that reason is gone, and the refusal was a
401-loop for every newcomer. Now:

- consent complete **and** stack configured → the account is created exactly
  as a first browser sign-in creates it (`CreateOAuth` with the token's
  `email` / `email_verified`, the tier for new accounts);
- no `email` claim → refused (the stack's MCP scopes carry it);
- no stack configured → refused, as before.

## 2 · Lazy authentication

With OAuth on, the old wrapper refused every request without a bearer — which
removed the edit-password path. The wrapper is replaced by one that inspects
the JSON-RPC message and challenges **only the calls that need an account**:

| Request without `Authorization` | Result |
|---|---|
| `initialize`, `notifications/*`, `ping`, `tools/list`, anything but `tools/call` | passes |
| `tools/call` of a per-site tool **with** a non-empty `edit_password` | passes — the tool verifies the password as before |
| `tools/call` of `create_site` where the instance has no accounts | passes |
| any other `tools/call` (`list_sites`, a per-site tool without a password, `create_site` on an account instance) | `401` + `WWW-Authenticate: Bearer resource_metadata="…", scope="sitebin:sites:read sitebin:sites:write"` |

With a bearer:

| Credential | Result |
|---|---|
| unrecognised, expired, wrong audience, consent outstanding | `401` + `error="invalid_token"` |
| valid, `tools/call` of a tool whose scope it lacks | `403` + `error="insufficient_scope", scope="<held sitebin scopes> <needed>"` — the step-up signal clients act on |
| valid | passes; the credential is handed to the tools through the request context, so it is verified once per request, not twice |

Only an HTTP 401 makes a client start a sign-in, so this is what lets an agent
connect without credentials, work with edit passwords, and be offered a
sign-in the moment it calls something an account is needed for.

`ResourceMetadataURL` in the challenge now names the `/mcp`-suffixed document
(RFC 9728 §3.1 pairs the bare one with the origin), and both metadata routes
answer `OPTIONS` for browser-based clients.

With OAuth **off** nothing changes: no wrapper, and the tools answer as they
always did.

## 3 · Scopes

- **OAuth tokens are for `/mcp` only.** Their audience is the MCP resource.
  `ext.Credential` gains `OAuth bool`; the JSON API's owner check
  (`tokenOwns`) and account resolution for site creation (`accountForAPI`)
  ignore an OAuth credential unless the request carries the MCP caller marker
  — a context value only the `/mcp` handler sets. Before this, a read-only
  token could create, overwrite and delete sites through `/api/*`.
- **Step-up.** A missing scope is an HTTP 403 `insufficient_scope` (above), not
  only a tool error. The tools keep their own check as the second line.
- **Only access tokens.** A JWT whose `typ` claim is present and is not
  `Bearer`, or whose header `typ` is neither `JWT` nor `at+jwt`, is refused. A
  Keycloak ID token carrying the resource in its audience used to pass.
- **The issuer has to be the sign-in issuer.** Accounts are found by the login
  provider's subject index; a token from a different issuer would be looked up
  in the wrong namespace. `SITEBIN_MCP_OAUTH_ISSUER` must equal
  `SITEBIN_OAUTH_OIDC_ISSUER` (trailing slash ignored), or startup fails with a
  message saying so.
- The "no scope" placeholder is never shown to an agent.
- **A failed discovery is retried.** The verifier used to cache the first
  discovery error until restart. It now retries (at most every 10 s), under
  its own timeout rather than the first caller's request context.
- Every tool carries a `title` and the read-only / destructive / open-world
  hints the connector directories require.

## Rollout

Unchanged from the previous design: nothing happens until
`SITEBIN_MCP_OAUTH_ISSUER` is set, and setting it makes the registration carry
the `mcp` block. The stack side ships first (gate, discovery, DCR policies),
then Sitebin, then the variable, then the website.

## Testing

- `internal/mcp`: the scope table covers every tool; the anonymous-call rule.
- `internal/httpapi`: every row of both tables above; a read-only OAuth
  credential is refused by every `/api/*` write route and by `POST /api/sites`;
  an `sbp_` token still works everywhere; the challenge headers.
- `ee`: JWT verification against an `httptest` issuer and a local key pair —
  valid token, wrong audience, ID token, issuer outage then recovery,
  cancelled first request, consent outstanding, consent complete with and
  without an account (auto-provision), no email, stack down (fail closed).
- End to end against the local stack: a real dynamic registration, the gate
  page, Keycloak's consent, the token, `/mcp` — scripted in the stack repo.
