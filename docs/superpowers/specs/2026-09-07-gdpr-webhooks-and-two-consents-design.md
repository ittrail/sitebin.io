# GDPR orders from the stack, two consent documents, and self-service on the stack's pages

2026-09-07. Shipped the same day; see the Corrections block at the end for
what the implementation settled that this text left open.

Three gaps between Sitebin and the IT-Trail SaaS Stack, closed together
because they share one configuration surface (`SITEBIN_STACK_*`) and one
rule: **the stack hosts it, Sitebin links or answers it, and builds none of
it.**

## 1. Two consent documents, not one

`SITEBIN_STACK_TERMS` declared one document through the stack's `terms`
shorthand. sitebin.io has two the gate must collect — the terms of service and
the data processing agreement (Art. 28) — and the shorthand has room for one:
it is exactly `consents: [{key: "terms", …}]`, and a payload carrying both
`terms` and `consents` is a `400` (`CONSENT_DECLARATION_AMBIGUOUS`).

So the variable is replaced, not extended. `SITEBIN_STACK_CONSENTS` is the
stack's `consents` list verbatim:

```json
[
  {"key":"terms","version":"2026-09-08","url":"https://sitebin.io/terms/","title":{"en":"Sitebin Terms of Service","de":"Sitebin Nutzungsbedingungen"}},
  {"key":"dpa","version":"2026-09-08","url":"https://sitebin.io/dpa/","title":{"en":"Data Processing Agreement","de":"Auftragsverarbeitungsvertrag"}}
]
```

What carries over from the one-document design unchanged: omitted means
"keep what the stack holds" (registration merges); `version` is opaque,
immutable once recorded, and raising it re-asks everyone — now **per
document**, because `(app, key, version)` identifies a document and the DPA's
version can move without touching the terms'. What is new:

- **`key` is identity, for ever.** Changing it declares a new document and
  asks everybody again. The stack's pattern is enforced at boot
  (`^[a-z0-9][a-z0-9_-]{0,63}$`, unique within the list, at most 20).
- **Order is presentation order** after the platform's own document.
- **`required` is a pointer.** Unstated is unsent, so the stack applies its
  own default (true) rather than Sitebin restating it. `false` is shown,
  recorded, and does not block — a marketing consent.
- **An empty list is refused at boot.** It is not "declare nothing" (that is
  the variable being unset) but "this app asks for nothing", a real state the
  stack distinguishes and an operator sets on the stack on purpose.

The wire field is `consents`; `terms` is never sent. The old name is gone
entirely — this repo is greenfield and an alias would be a second way to say
one thing.

## 2. GDPR: the stack orders, Sitebin erases

A data subject asks the stack — the account console, or the operator in the
stack's user directory — and the stack calls every app the person belongs to:
export (Art. 20), and for a deletion (Art. 17) the app **before** the identity,
so a name and an email never outlive the only thing that made the app's data
findable. These are the only calls the stack ever makes into a Sitebin
instance.

### The contract (the stack's, `docs/gdpr-integration-guide.md`)

| | |
|---|---|
| Declared | `gdpr: {deleteUserUrl, exportUserDataUrl, webhookSecret}` in the registration; the secret at least 32 characters |
| Request | `POST <url>`, JSON `{"userId": "<keycloak uuid>", "email": "…"}` |
| Signature | `X-Signature: sha256=<hex HMAC-SHA256(secret, "<X-Timestamp>.<body>")>`, `X-Timestamp` in Unix seconds |
| Export answer | `200` with a JSON object of everything the app holds |
| Delete answer | `200`/`204` = done; **any other status, `404` included, aborts** and keeps the identity |

The last row shapes the design more than any other: an endpoint that answers
`404` for a user it has already erased can never let a deletion complete.

### Sitebin's side

`POST /account/gdpr/delete` and `POST /account/gdpr/export`, in `ee/gdpr.go`,
mounted and declared **only when `SITEBIN_STACK_GDPR_SECRET` is set** — a URL
the stack could call but nothing could verify would be an unauthenticated
deletion endpoint. The secret is required whenever `SITEBIN_STACK_URL` is set
(a self-registered instance that cannot be erased is one that leaves personal
data behind) and accepted alone for an app registered by hand. The URLs are
built from the same base URL as the OIDC callback, so what is declared and
what is served cannot disagree.

**Verification is the whole authentication.** `X-Timestamp` must parse and lie
within five minutes of now in either direction; the MAC over
`<timestamp>.<body>` is compared with `hmac.Equal`, prefix included. No
session, admin key or API token is accepted there, and a request that fails
is refused before its body is parsed, with the same `401` for every reason —
the reason goes to the log. The timestamp inside the MAC is what stops a
captured order being replayed with a fresh one; the window is what stops it
being replayed with its own.

**The account is looked up by OIDC subject only.** The stack's user id is the
subject; local accounts are unknown to the stack and an order can never reach
one.

**Export** returns the account record (never the password hash — a verifier,
not the subject's data), the metadata of every owned site (id, view URL, mode,
custom domains, origin, bytes, files, created, expires), the metadata of every
API token (never a secret; none is stored), and a statement about sessions:
none are stored — they are signed cookies the browser keeps — and the
revocation counter is the only related thing held. An unknown user gets the
same shape with nothing in it, not an error; the stack folds the answer into a
combined document and a `4xx` would only mark it incomplete.

**Delete** removes the account, its ownership markers, its sites, its API
tokens (now including their index entries — `Store.Delete` sweeps them, which
it had never done), and — by removing the record every cookie is validated
against — its sessions. It is **idempotent**: a user with no account here is a
`200` with `found: false`. And it honours CLAUDE.md's rule without
contradicting the order: a verified deletion *is* the instruction, but a site
that cannot be deleted stops the order with a `500` and keeps the account, so
the stack keeps the identity and the operator retries. Whatever was deleted
before the failure stays deleted, and the retry steps over it — which needed
`siteService.Delete` to report a missing site as `ext.ErrSiteGone` like its
siblings, a one-line seam change.

## 3. Self-service on the stack's pages

Once a user is signed in through the stack, "manage my account" and "manage my
plan" are links the stack's SDK already knows how to build
(`packages/oidc/src/urls.ts`):

- **`accountUrl(issuer, {referrer})`** → `<issuer>/account/?referrer=<client id>`.
  Password, sessions, devices, second factors, linked identities, data export,
  account deletion. The dashboard shows it as "Manage account" for OIDC
  accounts.
- **`planUrl(portalBase, appId)`** → `<issuer origin>/apps/<app id>/plan`.
  Current plan, change, cancel, resume, invoices. `POST /account/billing/portal`
  redirects there when PayGate is the backend and an issuer is configured; the
  processor's own portal (a stack call) remains the fallback for PayGate
  without an issuer.

Both are **derived**, and `SITEBIN_PAYGATE_MANAGE_URL` is removed. A
configured URL was one more value that could disagree with the issuer the
instance actually signs in against; the stack defines where the pages are,
and so does the derivation. The issuer's origin is the portal's origin because
`auth.<domain>` serves both the realm and the stack portal.

**Deleting a stack account happens at the console.** The danger zone sends an
OIDC user there, and a `POST /account/delete` from such an account redirects
there too. The stack erases the identity and orders Sitebin to erase its half
through §2. Deleting locally first would leave an identity behind that still
names this app, with nothing of its own left to come back to. The rule is
`Config.StackDeletion()`: an OIDC issuer **and** a GDPR secret — without the
secret nothing will ever order the local erasure, so the console cannot be
where deletion goes, and the local form stays. Local accounts always delete
locally; the stack has never heard of them.

## What is deliberately not built

- **No replay cache inside the window.** The stack mints a fresh timestamp per
  call, but two orders in the same second with the same body carry the same
  signature — an operator clicking export twice — and a cache would turn the
  second into a spurious `401` the stack reports as "the app failed". The
  five-minute window is the contract; a captured order is useless after it and
  can only repeat an idempotent action inside it.
- **No consent check on Sitebin's side, and no consent UI**, as before.
- **No `referrer_uri` on the console link.** Keycloak drops it unless it
  matches a registered redirect URI, and declaring a second redirect URI that
  no handler answers, just to get a "Back to Sitebin" link, is a surface for a
  link.
- **No local "export my data" button.** The stack's console has one, and it
  produces the combined document — identity, billing, consents and Sitebin's
  half — which is the one a data subject actually wants.

## Proving it

- `go test -tags ee ./...`: `ee/gdpr_test.go` (every refusal, the export's
  contents, the erasure's completeness and idempotency, the stop-on-failure and
  retry, local accounts unreachable), `ee/selfservice_test.go` (the links, the
  portal redirect, deletion routing for stack and local accounts, with and
  without the secret), `ee/stackreg_test.go` (the `consents` and `gdpr` wire
  shapes, never `terms`), `ee/eeconfig` (parsing and every refusal at boot).
- `e2e/consent.ps1`: two documents declared, three on the gate, both recorded
  per user and counted per document.
- `e2e/stack/verify.ps1`: against the compose container — the registration
  the stack holds, a real sign-in, the links, the portal redirect, a signed
  export and deletion, idempotency. The stack itself cannot place the GDPR
  calls on the local testbed: its SSRF guard refuses a webhook URL that
  resolves to a private address, which every `*.localtest.me` name does.

---

> **Corrections (post-implementation).** Two things surfaced while wiring
> the local container that this document should carry.
>
> - **The stack returns `gdpr.webhookSecret` in clear** from
>   `GET /api/v1/apps/<id>`. `redactAppConfig` in `packages/shared` covers
>   `email.smtp.password` and nothing else, so an admin-key read of the app
>   record hands back the secret Sitebin declared. That is a stack-side item
>   (already on the launch plan's open list); `verify.ps1` notes it when it
>   sees it and asserts only that a secret is held. Nothing in Sitebin can fix
>   it — the secret has to be declared for the stack to sign with it.
> - **`siteService.Delete` did not report a missing site as `ErrSiteGone`.**
>   `ApplyQuota` and `SetExpiry` did; `Delete` returned the store's raw
>   not-found error, which no caller outside the core could recognise without
>   importing the store. The retry semantics above depend on telling "already
>   gone" from "could not delete", so it now maps the error like its siblings
>   and the seam's doc comment promises it. The test fake mirrors that and
>   reports an unregistered id the same way.
