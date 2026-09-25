# A signed-in owner manages their sites without the edit password

**Date:** 2026-09-25
**Status:** implemented

## Problem

The dashboard's "Manage" button opens the site's edit page, and the edit page
asks for the site's edit password — even when the person clicking is signed in
as the account that owns the site. The password protects nothing there: the
same session can already press "Reset edit password" on the dashboard and
receive a new one. It only costs friction, and it pushes owners into
*resetting* the password, which breaks every deploy that uses the old one
(sitebin.io's own deploy does).

It was never a decision. Accounts were designed as an additive layer ("owned
sites keep their edit URL + edit password", accounts design §2), the edit page
and the JSON API are core, and the core only ever learnt two credentials: the
edit password, and since 2026-08-28 the account API token. The browser session
lives in `ee/` and the seam had no way to ask for it.

## Rule

On the per-site API routes (`withEditAuth`), a request is authorized for a site
when **the account its browser session belongs to owns that site** — the same
standing an account API token has, reached through a different credential.
Order: upload token refused → account token → **session** → edit password.

The session is honoured **only** when:

1. the site has an owner, and that owner is the session's account;
2. the request carries `X-Sitebin-Session: 1`;
3. `Sec-Fetch-Site`, if the browser sent one, is `same-origin`.

## Why the header, and why it is a real boundary

A cookie rides along on any request the browser makes; an edit password or a
bearer token never does. Every per-site write was therefore CSRF-proof by
construction, and accepting the cookie alone would end that. A custom request
header is the classic fix: a page on another origin cannot add one without a
CORS preflight, and this API answers no preflight for the per-site routes (only
`POST /api/sites` has a preflight handler, and it allows `Content-Type` only).

`SameSite=Lax` on the session cookie is *not* sufficient on its own:
`sitebin.io` — itself a Sitebin site, serving uploaded content — is
**same-site** with `app.sitebin.io`, so Lax cookies travel between them.
`Sec-Fetch-Site: same-origin` closes that too, where the browser reports it;
where it does not (Safari before 16.4), the header rule still holds, because
such a browser cannot send the header cross-origin either.

This is deliberately **unlike `fromOwnBrowser`**, which is a plan boundary made
of forgeable fetch metadata. `sessionOwns` must never be loosened into it: the
header is the security property, not a hint.

## What does not change

- The edit password keeps working everywhere, and stays the only credential
  for WebDAV, FTP and an anonymous site.
- **MCP stays token-only.** It never reads the session (`mcpOps.Authenticate`),
  as its adapter already documents: an agent holds a token it was given, it
  does not ride a person's browser login.
- **The dashboard still refuses tokens.** A session reaching the site API is
  the account's own power in its own browser; a token reaching the account
  would be the reverse, and stays forbidden.
- Anonymous sites, and sites of other accounts, still show the lock screen.

## Seam

`ext.SessionAccounts` — an **optional** interface, asserted like
`OperatorAccounts` and `ZoneAccounts`, so the community build (no provider) and
any provider without sessions are unaffected:

```go
type SessionAccounts interface {
    SessionAccount(r *http.Request) (accountID string, ok bool)
}
```

The extension answers "whose session is this" (`currentAccount`: signed
cookie, account exists, token version current — so signing out revokes it);
the core does the ownership comparison and the CSRF rule, as it does for
tokens.

## Edit page

- It always sends `X-Sitebin-Session: 1`, and on load tries the API with no
  password: an owner's page opens straight away, a stored password is still
  tried, and anyone else sees the lock screen.
- A 401 for a missing password carries `account_url` when the instance has
  account sessions, and the lock screen then offers "Your own site? Sign in".

## Out of scope

- Session access on WebDAV/FTP (different clients, Basic auth).
- Letting a session create sites through `POST /api/sites` differently than
  today (`AuthorizeCreate` already honours the session there).
