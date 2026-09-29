# Sign-out ends the SSO session, and signing in again can pick another account

**Date:** 2026-09-29
**Status:** implemented

## Problem

"The Sign out button redirects to nothing and only deletes the auth —
afterwards I can never sign in with another user; signed in with Google it
always offers me only that Google account."

`handleLogout` bumped the account's `token_version` (every session of the
account ends), cleared the cookie and redirected to `/account/login`. On an
SSO-only instance (`SITEBIN_LOCAL_AUTH=false`, one provider — the hosted
instance) that page redirects straight into OIDC. The identity provider's own
session (Keycloak's SSO cookie on `auth.ittrail.cloud`) was never touched, so
the provider answered the new authorization request silently with the same
person, and the browser was back on the dashboard before anything had been
drawn. The button looked dead, and no second account could ever be chosen.

## Rule

1. **Sign-out ends the provider's session too — for the operator's own
   issuer only.** After revoking and clearing as before, a sign-out of an
   account that signed in through the generic OIDC provider sends the browser
   to the provider's `end_session_endpoint` (OpenID Connect RP-Initiated
   Logout 1.0), read from the discovery document, with
   - `client_id` (always),
   - `post_logout_redirect_uri=<base>/account/signed-out`,
   - `id_token_hint` when the ID token of the sign-in is still held and
     belongs to this account.

   Google and Microsoft signed in directly are never logged out at the
   provider, even though Microsoft's discovery advertises an endpoint: that
   would sign the person out of their mail and everything else on the
   account. A provider whose discovery has no `end_session_endpoint`, a
   discovery that fails at sign-out, a local account: the browser goes
   straight to the signed-out page. Sitebin's own sessions are revoked in
   every case — the provider hop is an addition, never a precondition.

2. **The hop is a handoff page, not a 303.** The dashboard's CSP says
   `form-action 'self'` and Chrome applies it to the redirect a form POST is
   answered with, so a 303 from `POST /account/logout` to the identity
   provider's origin would be dropped without a word — the very symptom being
   fixed. The route answers 200 with the existing handoff page (meta refresh
   + a Continue link), as the plan page and checkout already do. A 303 is
   used only for the same-origin case (straight to the signed-out page).

3. **The ID token is kept for exactly one purpose.** The sign-in callback
   keeps the raw ID token of a provider that is signed out at logout in a
   second cookie: `__Secure-sitebin_idt` over TLS (`sitebin_idt` on an
   HTTP-only instance), HttpOnly, SameSite=Lax, host-only, **Path
   `/account/logout`** — the browser sends it nowhere else — Max-Age the
   session TTL. It is cleared at sign-out (on every path), replaced at the
   next OIDC sign-in, cleared at a local sign-in or a sign-in through a
   provider that is not signed out, and never logged. A token larger than
   3800 bytes or with characters outside the compact-JWS alphabet is not
   stored (the logout then goes without the hint).

   It is not re-issued by the dashboard's sliding renewal (the dashboard
   never sees it — that is what the path is for), so after a week of
   continuous use the hint lapses and the next sign-out goes without it.

4. **A hint is sent only when it is this account's.** Before sending it, the
   (unverified) payload must name the configured issuer, the account's
   subject and this client (`azp`, or `aud` when there is no `azp`); anything
   else is dropped and the logout goes with `client_id` alone. Keycloak shows
   an error page for a hint issued to another client and a confirmation for
   another session, so a stale or planted cookie must not reach it. Keycloak
   verifies the signature itself; this check is only about which request to
   make.

5. **Expired hints are sent.** Keycloak 26.7's logout endpoint checks the
   hint's signature and `typ` only (`TokenManager.verifyIDTokenSignature` →
   `DefaultTokenManager.decode`, no expiry check), and its ID tokens live for
   minutes, so dropping expired hints would drop nearly all of them.

6. **Without a hint Keycloak may ask.** `client_id` +
   `post_logout_redirect_uri` without `id_token_hint`: with a live Keycloak
   session in the browser, Keycloak shows its "Do you want to log out?" page
   (a logout CSRF guard it applies without a hint) and redirects back after
   the click; without one it redirects straight back. This is the path for
   every session signed in before this change, for a hint that lapsed, and
   for an oversized token.

7. **The signed-out page never redirects.** `GET /account/signed-out` renders
   "You are signed out" in the account-page style, no script, no refresh, no
   Location. Its sign-in buttons start a **fresh** authorization request:
   `/account/auth/<provider>?fresh=1`, which adds `prompt=login` for the
   generic issuer (Keycloak shows its login page — password, Google,
   Microsoft — even if an SSO session survived) and `prompt=select_account`
   for Google and Microsoft signed in directly (both show their account
   chooser; neither knows `login` in the same sense). On an instance with
   local auth it also links `/account/login`, which renders the form there.

8. **One-click SSO stays one click.** `/account/login` on an SSO-only,
   single-provider instance still redirects straight into the provider
   without `prompt`, so the stack's hub opens Sitebin signed in. The login
   page, where it renders, keeps its one-click buttons and adds a small
   "Use a different account" link with `fresh=1`: an error page's "Back"
   goes there, and trying the same provider session again is what failed.

9. **The post-logout URI is declared to the stack.** The registration's
   `auth` block carries `postLogoutRedirectUris:
   ["<base>/account/signed-out"]`, built from the same base URL as the
   callback; the stack writes it to the Keycloak client attribute
   `post.logout.redirect.uris`. Keycloak refuses (error page) any
   `post_logout_redirect_uri` not registered there. The stack's registration
   schema is strict, so an instance with this change must only register with
   a stack that knows the field — otherwise the whole registration is
   refused (logged; Sitebin serves anyway).

## What does not change

- The CSRF check on `POST /account/logout` and the token-version bump ("sign
  out everywhere").
- MCP OAuth: `/mcp` never reads the session or the hint cookie, and OAuth
  access tokens are not bound to the token version.
- The owner-session edit flow: `sessionOwns` reads the session cookie, which
  the bump invalidates as before.
- The stack's GDPR and suspension webhooks.
- Local auth: a local account's sign-out makes no provider hop.

## What the person sees (hosted instance)

1. Dashboard → **Sign out** → a "Signing you out" page for an instant.
2. Keycloak ends the SSO session (no question when the hint is held) and
   sends the browser to `https://app.sitebin.io/account/signed-out`.
3. "You are signed out" → **Sign in** → Keycloak's login page
   (`prompt=login`), where password, Google or Microsoft can be chosen;
   the stack's Google/Microsoft IdPs ask with `prompt=select_account`, so
   Google shows its account chooser.
4. The consent gate as for any sign-in, then the dashboard of the account
   just chosen.
