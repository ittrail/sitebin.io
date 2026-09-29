# Plan: sign-out ends the SSO session

Design: `docs/superpowers/specs/2026-09-29-sign-out-ends-the-sso-session.md`.

1. **`ee/session`: the hint cookie.** Tests first: name with and without TLS,
   HttpOnly, SameSite=Lax, Path `/account/logout`, Max-Age = TTL, cleared on
   empty/oversized/odd input, `Hint(r)` reads it back. Then `HintCookie`,
   `ClearHint`, `Hint`, `HintName`.
2. **`ee/authn`: logout URL and fresh prompt.** Tests first against an
   httptest discovery document: `LogoutURL` with and without
   `end_session_endpoint`, with a matching hint, a hint for another subject,
   client or issuer, a malformed one; the endpoint's own query survives;
   Google/Microsoft never; `AuthCodeURL(..., fresh)` adds `prompt=login` for
   the generic issuer only when asked, and the direct providers carry
   `select_account`. Then: discovery reads `end_session_endpoint`, `Identity`
   carries `LogoutHint`, `AuthCodeURL` takes `fresh`.
3. **`ee`: sign-in keeps the hint.** Test through the callback with the test
   issuer: the hint cookie is set with the ID token; a provider without an
   end-session endpoint clears it; a local sign-in clears it.
4. **`ee`: logout.** Tests: handoff to the end-session URL with all three
   parameters; without the hint cookie `client_id` only; an expired hint is
   still sent; a hint of another subject is dropped; no endpoint /
   discovery down / local account → 303 `/account/signed-out`; both cookies
   cleared and every session revoked in every branch; CSRF still 403; not
   signed in → 303 signed-out.
5. **`ee`: signed-out page and fresh sign-in.** Tests: 200, no Location, no
   refresh, no script, sign-in link `?fresh=1`; SSO start with `fresh=1`
   carries `prompt=login`, without it none; `/account/login` still
   auto-redirects; the login page shows "Use a different account".
6. **`ee/stackreg.go`: `postLogoutRedirectUris`.** Test the wire key and
   value, then add the field.
7. Docs: README (SaaS Stack section, self-registration row), repo CLAUDE.md
   note, website `docs/saas-stack/` (declaration list, strict-schema note).
8. Both suites, `go vet`, merge, push, CI; deploy once the production stack
   accepts the field; verify the Keycloak client attribute read-only.
