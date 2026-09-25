# MCP OAuth — consent, lazy authentication and scopes — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Test first in every task: write the failing test, watch it fail, implement, watch it pass.

**Goal:** Make MCP OAuth safe to switch on: an agent can connect and work with edit passwords while OAuth is on, is challenged only for calls that need an account, gets a step-up 403 for a missing scope, and a token is honoured only on `/mcp`, only when it is an access token, and only for a person who has accepted the current documents.

**Architecture:** `internal/mcp` gains the catalog table (scope, per-site, title, annotations) that both the tools and the HTTP layer read, plus the anonymous-call rule. `internal/httpapi` replaces `sdkauth.RequireBearerToken` with a lazy wrapper that peeks at the JSON-RPC body, resolves a bearer once and hands the credential to the tools through the request context; `/mcp` always marks its requests as MCP. `internal/ext` owns the two unforgeable context keys and `Credential.OAuth`. `ee` refuses OAuth outside `/mcp`, retries discovery, checks `typ`, asks the stack's consent endpoint and auto-provisions accounts.

**Spec:** `docs/superpowers/specs/2026-09-25-mcp-oauth-consent-lazy-auth-design.md` (follows `2026-08-29-mcp-oauth-resource-server-design.md`).

## Global Constraints

- Branch `feat/mcp-oauth-consent-scopes`, worktree `.worktrees/sitebin-mcp-oauth`. Never push, never deploy.
- OAuth off (`SITEBIN_MCP_OAUTH_ISSUER` empty) changes nothing: no wrapper beyond the MCP marker, the tools answer as before.
- The per-tool scope check inside the tools stays; the HTTP 403 is the first line, not the only one.
- Unparseable, batched or oversized bodies pass through to the SDK untouched; the tools remain the authority.
- Discovery never runs under a caller's context. Consent fails closed; only positive answers are cached.
- Contract with the stack is fixed: `GET {stack}/api/v1/apps/{appId}/users/{sub}/consent/status`, admin key as bearer, `complete` decides.
- Both editions: `go vet ./...`, `go test ./...`, `go vet -tags ee ./...`, `go test -tags ee ./...`; gofmt clean.

---

### Task 1: Context markers and `Credential.OAuth` (`internal/ext`)

**Files:** Modify `internal/ext/ext.go`; create `internal/ext/ext_test.go`.

- [ ] Test: `IsMCPCaller(context.Background())` is false, true after `WithMCPCaller`; `CredentialFrom` round-trips a `Credential` set with `WithCredential` and reports false on a bare context; a value stored under a look-alike key from another package is not seen.
- [ ] Implement an unexported key type, `WithMCPCaller`/`IsMCPCaller`, `WithCredential`/`CredentialFrom`, and `Credential.OAuth bool` with a comment saying where it is honoured.
- [ ] Commit `feat: the extension seam can say a request came through /mcp and carry its verified credential`.

### Task 2: The catalog table and the anonymous-call rule (`internal/mcp`)

**Files:** Modify `internal/mcp/server.go`, `internal/mcp/ops.go`; modify `internal/mcp/server_test.go`; create `internal/mcp/catalog_test.go`.

- [ ] Tests: every registered tool is in the table and every table row is registered; `PerSite` equals "the input schema has `edit_id`"; every tool has a `Title`, read tools `ReadOnlyHint` + `OpenWorldHint=false`, write tools an explicit `DestructiveHint` and `OpenWorldHint`; `AnonymousCallAllowed` for: unknown tool, per-site with/without/empty/non-string password, `create_site` with and without accounts, `list_sites`; `ToolScope`; `authorize` never prints the placeholder and says the connection holds no Sitebin scopes; an OAuth `Auth` with no scopes is restricted.
- [ ] Implement `catalog` (name → scope, perSite, title, annotations), an `addTool` helper that fills title/annotations and runs `authorize` from the table, `ToolScope`, `AnonymousCallAllowed(name, args, accountsEnabled)`, `HeldScopes`, `Auth.OAuth`, and the new `authorize` wording.
- [ ] Commit `feat: every mcp tool carries a title, hints and a scope from one table the http layer can read`.

### Task 3: Lazy authentication on `/mcp` (`internal/httpapi`)

**Files:** Modify `internal/httpapi/mcpoauth.go`, `internal/httpapi/mcpops.go`, `internal/httpapi/mcpops_test.go`; create `internal/httpapi/mcplazy_test.go`.

- [ ] Tests with OAuth on and a fakeProvider: `initialize`/`tools/list` without credentials → 200; `list_sites` → 401 with `resource_metadata=".../oauth-protected-resource/mcp"`, `scope=`, no `error=`; per-site tool with the right `edit_password` works end to end, without it → 401; `create_site` → 401 with accounts, passes without; invalid bearer → 401 `invalid_token`; read-only OAuth + write tool → 403 `insufficient_scope` naming both scopes, + read tool → OK; `sbp_` → OK; the tools see the credential verified once (BearerCredential call count); every `/mcp` request carries the MCP marker, OAuth or not; `OPTIONS` on both metadata routes; oversized/garbage/batched bodies reach the SDK.
- [ ] Implement `mcpLazyAuth`, the challenge writers, the marker wrapper, `Authenticate` reading `ext.CredentialFrom`, `mcpResourceMetadataURL` with `/mcp`, OPTIONS routes. Remove `verifyMCPToken` and the `sdkauth.RequireBearerToken` use.
- [ ] Commit `feat: with oauth on, /mcp challenges only the calls that need an account and answers a missing scope with a step-up 403`.

### Task 4: OAuth tokens only on `/mcp` (core half)

**Files:** Modify `internal/httpapi/server.go`, `internal/httpapi/gate_test.go` (fake learns `oauth` and resolves create owners from the bearer), `internal/httpapi/apigate_test.go` or a new `oauthrest_test.go`.

- [ ] Tests: a read-only OAuth credential and a scope-less one are refused by every `/api/sites/{editID}…` write route and by `POST /api/sites`; an `sbp_` token still works on every one of them; MCP `create_site` with a write-scoped OAuth credential still creates an owned site (the marker reaches `AuthorizeCreate`).
- [ ] Implement: `tokenOwns` ignores `cred.OAuth`.
- [ ] Commit `fix: an oauth access token no longer stands in for an edit password on the json api`.

### Task 5: OAuth tokens only on `/mcp` (ee half) and the JWT test issuer

**Files:** Modify `ee/provider.go`, `ee/mcpoauth.go`; create `ee/mcpoauth_issuer_test.go` (httptest issuer, RSA key, go-jose signer).

- [ ] Tests: a valid access token verifies with its scopes and `OAuth:true`; `BearerCredential` and `accountForAPI` honour it only with `ext.WithMCPCaller`; an `sbp_` token is honoured with and without the marker.
- [ ] Implement `OAuth:true` in `Verify`, the marker checks in `BearerCredential` and `accountForAPI`.
- [ ] Commit `fix: the enterprise extension honours an oauth access token only on requests that came through /mcp`.

### Task 6: Verifier hardening — retrying discovery and `typ`

**Files:** Modify `ee/mcpoauth.go`, `ee/mcpoauth_issuer_test.go`.

- [ ] Tests: wrong audience refused; `typ:"ID"` refused; header `typ` other than `JWT`/`at+jwt`/`application/at+jwt` refused; issuer down then up (clock advanced past 10 s) → second call succeeds; no retry inside 10 s; a first call with a cancelled context does not poison later calls.
- [ ] Implement the mutex-guarded lazy init with `now` injection, a 10 s retry floor and its own 10 s timeout; the claim and header `typ` checks.
- [ ] Commit `fix: mcp oauth retries a failed discovery and accepts only access tokens`.

### Task 7: Consent lock and auto-provision

**Files:** Create `ee/mcpconsent.go`, `ee/mcpconsent_test.go`; modify `ee/mcpoauth.go`, `ee/provider.go`, `ee/mcpoauth_issuer_test.go`.

- [ ] Tests: complete → OK; outstanding → refused; stack 500 / unreachable / timeout → refused; non-UUID subject → refused without a request; positive answer cached (request count), negative not; admin key sent as bearer on the right path; cache bounded. Verifier: unknown subject with complete consent creates the account once; no email → refused; `ErrEmailTaken` → refused; without stack config an unknown subject is refused.
- [ ] Implement `stackConsent` (5 s timeout, 10 min positive cache, bounded), the `consent` and `provision` hooks on `mcpOAuth`, wired in `Init` only when `StackRegistration` is set.
- [ ] Commit `feat: an mcp access token works only for a person who accepted the current documents, and creates their account on first use`.

### Task 8: The issuer guard

**Files:** Modify `ee/eeconfig/eeconfig.go`, `ee/eeconfig/eeconfig_test.go`, `ee/provider.go`, a provider test.

- [ ] Tests: MCP issuer unset → no error whatever the login issuer; equal (trailing slash ignored) → OK; different → error naming both variables; login issuer unset → error; `Init` fails with the message.
- [ ] Implement `Config.CheckMCPOAuthIssuer` and call it from `Init`.
- [ ] Commit `feat: startup refuses an mcp oauth issuer that is not the sign-in issuer`.

### Task 9: Docs

**Files:** `README.md`, `CLAUDE.md`, `docs/superpowers/specs/2026-08-29-mcp-oauth-resource-server-design.md` (Corrections block), `e2e/stack/README.md`, comments in `cmd/sitebin/main.go` and `internal/httpapi/internalh.go`.

- [ ] Three credentials side by side, lazy auth, the 401/403 challenges, consent lock, auto-provision, OAuth only on `/mcp`, issuer must equal the login issuer.
- [ ] Commit `docs: mcp oauth is described as lazy, consent-locked and /mcp-only`.

### Task 10: Verification

- [ ] `gofmt -l .` empty; `go vet ./...`; `go test ./...`; `go vet -tags ee ./...`; `go test -tags ee ./...`.
