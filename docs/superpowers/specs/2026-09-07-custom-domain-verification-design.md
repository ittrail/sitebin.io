# Custom domains prove ownership before they are attached

2026-09-07. Shipped the same day, as part of the pre-launch security audit
(finding M-01). Written alongside the code; see the Corrections block at the
end for what implementation settled.

## The problem

`AddDomain` validated syntax, refused the instance's own domains, applied the
per-site cap, and then claimed the `domain-index/<host>` link for whoever asked
first. `tls-check` approved on-demand issuance for any indexed host. So a
stranger could add `docs.customer.example` to their own site today; when the
customer later pointed DNS at the instance — the README's own step order — the
stranger's page was served on it with a Let's Encrypt certificate, and the
customer was told "already in use by another site". First claim won, and
nothing ever asked whether the claimant controlled the name.

## The rule

A domain is **attached** — indexed, served, issued a certificate — only once
its DNS proves it belongs to the site that claims it. Proof is either of:

| Proof | Record |
|---|---|
| TXT | `_sitebin-challenge.<domain>` = `sitebin-verify=<token>`, the token minted per site and per domain and shown to the owner |
| CNAME | `<domain>` → `<view id>.<view domain>`, the site's own view host — which is also how the domain routes here, so that route needs nothing extra |

Until then the claim is **pending**: recorded on the site with its token so the
records can be shown and re-checked, counted against the per-site cap so claims
cannot be sprayed, but never in `CustomDomains` and never in the index. A
pending claim reserves nothing. The site whose DNS carries *its* token gets the
domain, whoever asked first — that is the whole point.

## Where it lives

`internal/store/domainverify.go`, in the core. Custom domains are an
enterprise feature by *gating* (`ext.Provider.CustomDomainsAllowed`), but the
store is where the index is written, and the rule has to hold wherever the
index is written.

- **`Meta.CustomDomains` stays the list of VERIFIED domains** and is what
  everything else reads: Caddy's `domain-index` root, `tlsCheck`, `siteByHost`,
  the admin console, MCP results, the GDPR export. Nothing downstream had to
  learn a new field to stay correct.
- **`Meta.DomainClaims`** carries every claim, pending or verified:
  `{domain, token, requested_at, verified_at?, checked_at?, failing_since?}`.
  Invariant: a name in `CustomDomains` has a claim with `verified_at`, or
  predates claims entirely; a pending claim is never in `CustomDomains`.
- **`store.DomainVerifier`** is an interface — `Verify(ctx, domain, token,
  viewHost) (ok, err)` — with two implementations: `DNSVerifier` over
  `net.DefaultResolver` (each lookup bounded to 5 s, each route tried on its
  own so a lookup error on one cannot hide a positive answer from the other),
  and `TrustingVerifier`, which attaches on the owner's word. Tests inject a
  scripted one. **A store with no verifier attaches nothing**: the safe
  default for a store nobody wired.
- **`ok=false, err=nil` is a definitive "the proof is not there"; an error
  means the question could not be answered**, and nothing acts on an error.
  `isNotFound` tells the resolver's "no such record" (an answer) from a
  timeout or SERVFAIL (the absence of one).

## The two moments it runs

**At add time.** `AddDomain` records the claim (reusing an existing claim's
token, so the record the owner already created stays valid), verifies, and
either attaches — `attach` is the ONLY place a domain enters the index and
`CustomDomains` — or returns `ErrDomainPending`. The JSON API answers `202`
with `pending_domains[]` carrying `txt_name`, `txt_value` and `cname_target`;
a repeat `POST` of the same domain re-checks ("Check now" on the edit page);
the MCP `add_domain` tool returns the same list in its result instead of
failing, and its description tells the agent what to do. Adds are logged with
the site's owner.

**In the cleanup sweep.** `ReconcileDomains` runs per site with claims:

- a pending claim older than 7 days is dropped (the owner starts over);
- a pending claim on a domain another site has meanwhile verified is dropped —
  it can never verify and only occupies a cap slot;
- a pending claim whose proof has appeared is attached, so the owner never has
  to come back;
- a verified claim is re-checked every 24 h. A definitive absence starts a
  revocation window (`failing_since`); the domain is detached — index link
  removed, name out of `CustomDomains`, claim back to pending with its token —
  only once the proof has been absent for **72 h**. Seen again inside the
  window, the window closes. **A lookup error changes nothing**, per the rule
  that a site kept too long is recoverable and a detached domain is an outage.
- a verified domain **with no claim record** predates this design and is left
  alone: there is no token to check it against, and the operator's own sites
  are among them.

Lookups happen outside the site lock (DNS is slow and the lock serializes
uploads); every answer is applied through its own locked `Update`.

## What the operator sees

`SITEBIN_DOMAIN_VERIFICATION` is `dns` (default) or `off` (the trusting
verifier, with a startup warning). `off` exists for an instance whose every
account holder is trusted — a team's own install — and for the e2e suite,
where no DNS answers: `license.ps1` runs with it so its entitlement checks
see domains attach at once; `accounts.ps1` runs without it and asserts the
pending flow, the records in the `202`, and that an unverified domain serves
nothing.

On a path-only instance (`SITEBIN_VIEW_ACCESS=path`) there is no view host,
so `cname_target` is absent and only the TXT route exists.

## What is deliberately not built

- **No CNAME to the base domain as proof.** It cannot distinguish sites; only
  the site's own view host names the site.
- **No verification for the operator's pre-existing domains** (claimless), and
  no migration writing claims for them — nothing could mint a token their DNS
  already carries.
- **No immediate detachment.** A DNS glitch lasts minutes; three days of
  definitive absence is a decision.
- **No enumeration protection on the challenge label.** The token is what is
  secret, not the label; `_sitebin-challenge.<domain>` being guessable is how
  the owner knows where to put the record.

---

> **Corrections (post-implementation).**
>
> - **The cap counts pending claims plus claimless verified domains**, not
>   `len(CustomDomains)`: a verified domain always has a claim after this
>   change, so counting both lists would count it twice, and a claimless
>   (pre-existing) domain has to be counted somewhere.
> - **Sources of the CSP and abuse reports are networks, not addresses**
>   (unrelated to this design but landed in the same audit): tests that
>   expected three addresses in one /24 to count as three sources were wrong,
>   and the tests say so now.
