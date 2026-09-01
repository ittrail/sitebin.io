# Sitebin behind the SaaS Stack's consent gate

2026-09-01

## The problem

The IT-Trail SaaS Stack now gates sign-in on two documents:

| | What it is | How often |
|---|---|---|
| The IT-Trail **platform** terms | the operator's terms for running identity, billing and licensing | once per user, ever |
| The **app's** own terms | the product's terms of service | once per app |

The stack hosts the page, inside the sign-in flow, themed for the app and
localized. An app declares a version and a URL and builds nothing: no page, no
endpoint, no callback. **If Sitebin ever renders a terms screen, the
integration is wrong.**

Two things have to be true for a Sitebin user to be asked.

**1. Discovery has to point at the Auth Gateway.** The gate lives on the
gateway's authorization endpoint. The gateway serves the realm's discovery
document with exactly one field changed — `authorization_endpoint` names the
gateway — and that one field is the entire mechanism. An app configured with
the identity provider's own endpoints never reaches the gate, and does so
**silently**: sign-in works, and nobody is ever asked anything. Earlier
releases of the stack's docs actively recommended pointing straight at the
provider "for performance".

Sitebin did exactly that: `SITEBIN_OAUTH_OIDC_ISSUER` was the realm, and
discovery came from the issuer. So Sitebin was not gated.

**2. Sitebin has to declare terms.** Without a `terms` block in the
registration payload, the gate has nothing of Sitebin's to show and asks for
the platform document alone.

## The issuer split

The gateway's document advertises **Keycloak's** `issuer` at the **gateway's**
URL. `github.com/coreos/go-oidc/v3` refuses a document whose `issuer` is not
the URL it was fetched from, so this configuration cannot even initialize
without help.

The fix is the one the stack's own Go SDK uses (`sdk/go/client.go`):

- `oidc.InsecureIssuerURLContext(ctx, issuer)` before `oidc.NewProvider`, which
  disables the **URL-equality** rule — the one Keycloak deliberately breaks;
- an explicit re-check that the document's `issuer` equals the configured one,
  which go-oidc no longer performs.

The check is therefore **tightened, not skipped**. What is given up is only
"the document lived at the issuer's URL". What is kept: the document names the
issuer we configured, and every ID token's `iss` is still verified against it.
Without the re-check, a discovery URL would become a way to accept tokens from
a realm nobody chose.

### The setting

A new setting rather than a redefinition, because
`SITEBIN_OAUTH_OIDC_ISSUER` must keep meaning what it has always meant:

| | |
|---|---|
| `SITEBIN_OAUTH_OIDC_ISSUER` | the value that must appear in every token's `iss`. Unchanged. |
| `SITEBIN_OAUTH_OIDC_DISCOVERY_URL` | where the document is **fetched**. Empty = the issuer, which is what every plain provider wants. |

Either the base or the full `/.well-known/openid-configuration` is accepted,
because both are what people copy.

**One consequence worth stating.** The gateway's document keeps Keycloak's own
**back-channel** URLs on the origin the gateway asked Keycloak over — an
internal one, e.g. `http://keycloak:8080`. So an instance discovering through
the gateway has to be able to resolve that origin: on the stack, it has to
share the stack's network. Front-channel host aliases are not enough. This is
the stack's design (the proxy stays out of the token and JWKS path), not
something Sitebin can work around.

## The terms declaration

One block in the registration payload, beside `licensing`:

```json
"terms": { "version": "...", "url": "...", "title": { "en": "..." } }
```

From `SITEBIN_STACK_TERMS`, not from a constant in this repo, for the same
reason `SITEBIN_STACK_LICENSING` is configuration: **these are one
deployment's commercial and legal terms.** They are sitebin.io's on the hosted
instance, they change when that page changes, this repo is public and released
on its own schedule, and a customer self-hosting Sitebin has their own terms or
none — neither is ours to write.

Rules that fall out of the stack's contract:

- **Omitted must mean "keep what is stored."** Registration is convergent and
  it MERGES. An absent block keeps what the app already declared; an empty one
  would replace real terms with a version and a URL of nothing. So the field is
  a pointer with `omitempty`, exactly as `licensing` is.
- **`version` and `url` are required**, and a half-filled block is refused at
  startup rather than in the background goroutine that registers.
- **A version is immutable.** Re-declaring one the stack has already recorded
  with different content is refused by the stack, because every consent already
  taken names that version. Raising the version asks every user again and
  invalidates nothing.

## What is deliberately not built

- **No terms UI in Sitebin.** See above. The stack owns the page.
- **No `"terms": null`.** The stack distinguishes omitted (keep) from `null`
  (this app has no terms of its own). Sitebin supports the first two states;
  an operator who wants the third clears it on the stack. Adding a tri-state to
  one env var to express "declare nothing, loudly" was not worth the shape.
- **No consent check on Sitebin's side.** The gate refuses the code exchange
  for a user with anything outstanding, so there is nothing for Sitebin to
  re-verify, and a second copy of the rule is how the two disagree later.

## Proving it

`e2e/consent.ps1`, against a live stack and a real container. It registers a
throwaway app, boots Sitebin with the split and a `terms` block, and drives the
sign-in over real HTTP with a cookie jar — real redirects, the realm's own
login form, the gateway's own consent form. It asserts the two documents are
shown to a brand-new user, that accepting both lands them in Sitebin's
`/account`, that a second sign-in asks nothing, and two controls: that pointing
discovery at the identity provider removes the gate, and that a document
advertising some other issuer is still refused.

Playwright is not a Sitebin dependency and adding one for this would be
disproportionate.
