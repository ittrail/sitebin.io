# Licensing through the stack — design

*2026-08-30. **Shipped.** See the corrections at the end of this file.*

Sitebin Enterprise licences are today an Ed25519 string signed by a private key
that lives in one gitignored file on one laptop, with no backup and no way to
issue a key except an ad-hoc program. Customers will buy ee licences through the
website, so issuing belongs where the payment already is: the SaaS Stack.

The stack becomes a small certificate authority and licence issuer. Sitebin —
and any future app, desktop ones included — verifies **offline**.

## The trust model

> **One root per stack instance. A signing key per registered app. The licence
> carries its own certificate.**

- The **root** keypair is generated once, at the stack's first bootstrap. The
  private half never leaves the stack — not to an app, not to an admin, not
  through the registration API.
- Each registered app gets its **own** signing keypair, minted by the stack when
  the app registers. The root signs a certificate binding `app_id` to that
  public key.
- A licence is signed by the **app** key, and the certificate travels **inside
  the licence string**. Verification is two Ed25519 checks and needs no network.

This is what makes the deploy-and-done story hold: a new app self-registers, the
stack mints its key and certificate against the existing root, and nobody opens
the admin portal. The root public key is the same value for every app on the
stack, so it is set once in the build pipeline and never touched again.

### Why the public key is baked in and not fetched

A public key's worth is not secrecy — it is *knowing it is the right one*. Fetch
it over the network and anyone controlling that answer (a hosts entry, a proxy,
an env var) can mint their own keypair, sign themselves a perpetual licence and
point the fetch at their own server. The check would then be asking the attacker
what the truth is. Browsers ship root CAs in the binary for the same reason.

Rebuilding a binary with a different root is possible, of course — and it is a
plain breach of the Elastic License 2.0's prohibition on circumventing licence
key functionality. That is the point: the anchor makes evasion a deliberate,
provable act rather than a configuration option.

### Roots are a list, not a constant

The build carries **several** trusted roots, not one. A single baked constant
turns a lost or compromised root into "rebuild and redistribute every binary".
With a list you introduce a second root, honour both for a while, and retire the
first. It costs nothing now and cannot be added later without the very rebuild
it exists to avoid.

## The licence string

Four base64url segments, dot-separated — one opaque value that still fits in an
env var:

```
<certPayload>.<certSig>.<licPayload>.<licSig>
```

```jsonc
// certPayload — signed by the ROOT
{ "app_id": "sitebin", "pubkey": "<base64 ed25519>", "issued_at": "…", "expires_at": "…" }

// licPayload — signed by the APP key named in the certificate
{ "app_id": "sitebin", "holder": "ACME GmbH", "plan": "team",
  "issued_at": "…", "expires_at": "…", "grace_until": "…",
  "entitlements": { "max_custom_domains": 25 } }
```

`entitlements` is an object rather than a flat field so a later limit can be
added without reissuing every licence in the field. An absent or zero value
means **unlimited**, which keeps the "unknown is never restrictive" rule true
for a licence issued before a limit existed.

Verification, in order, all of it mandatory:

1. Split into four; anything else is malformed.
2. `certSig` verifies over `certPayload` under **one of** the trusted roots.
3. The certificate is not expired.
4. `licSig` verifies over `licPayload` under `certPayload.pubkey`.
5. `licPayload.app_id` equals `certPayload.app_id` **and** equals this
   product's own app id. A licence minted for another app on the same stack must
   not work here — the same reasoning as the MCP audience check.

### The plan must carry its limits, not just its name

The website sells licence tiers that *scale by custom-domain cap* — Team,
Business, Platform. But the customer self-hosts, so the caps live in their own
`tiers.json`: without an entitlement in the licence, a Team licence holder edits
one number and has Platform. The tiers would be honour-based while the sales
page implies they are not.

So the licence carries the limit, and ee enforces it:

- `entitlements.max_custom_domains` is the **instance-wide total** of custom
  domains this deployment may serve — not a per-site number. That is what the
  sales page is describing.
- It is enforced when a custom domain is **added**, which is rare and nowhere
  near the hot path. The tier's own per-site allowance still applies; the
  licence is an additional ceiling, never a grant.
- **It never removes anything.** A licence that shrinks — a downgrade, or a
  cap introduced later — leaves already-configured domains serving and only
  refuses the next one. Same rule as the cleanup sweep: never act destructively
  on a limit.

## Two dates, because one cannot warn

`expires_at` is the real end of the subscription. `grace_until` is that plus the
grace period, configured on the stack (default three months) per app.

Folding the grace into `expires_at` would leave the instance unable to tell
"valid" from "in grace for ten weeks", so the operator gets no warning and is
surprised at the end of it. With both, service continues to `grace_until` while
the dashboard says loudly, from `expires_at` onward, what is happening.

## What Sitebin does with it

**It never refuses to start.** Not without a licence, not with an expired one,
not with a malformed one. A licence problem is logged and surfaced in the
dashboard; it never takes the instance down, and it never touches serving.

Five states. Four of them describe a licence; the fifth says the question could
not be answered, and is the one that must never restrict anything:

| State | When | Effect |
|---|---|---|
| `licensed` | valid, `now <= expires_at` | entitlements apply |
| `grace` | `expires_at < now <= grace_until` | loud, permanent notice in the account UI |
| `expired` | `now > grace_until` | notice **+ no new sites and no new drops** |
| `none` | no key at all | as `licensed` for a 90-day trial from first start, then as `expired` |
| `unknown` | nothing has loaded yet, or the trial marker could not be read | **nothing is restricted** |

`unknown` is reachable on two paths, both in `Snapshot.StatusAt`: a snapshot
that has not loaded, and a snapshot with no usable licence whose trial start
could not be determined. It is not a rounding error in the state machine — it
is the state machine's safety valve, and it is why "unknown is not expired"
below is a rule rather than an aspiration.

A malformed or unverifiable key counts as `none`, never as `expired`: a
configuration mistake must not be more punishing than no licence at all.

Three rules the implementation must not soften:

- **Existing sites are never touched.** No interstitial, no banner injected into
  served pages, no expiry brought forward. The restriction is on *creating*, and
  content updates to an existing site keep working — otherwise sliding renewal
  stops and sites quietly lapse, which is the very harm this avoids.
- **Unknown is not expired.** If the state cannot be determined — the stack is
  unreachable, the cached key has not loaded — nothing is restricted. Same
  instinct as the cleanup sweep: a site not created is an annoyance, a customer
  wrongly blocked is a broken product.
- **Enforcement lives at exactly two places, and they enforce different
  things.** The licence STATE is enforced in `ext.Provider.AuthorizeCreate`,
  which already resolves tier and quota; the licence's ENTITLEMENTS are
  enforced in `ext.Provider.CustomDomainsAllowed`, where a domain is added.
  Neither is on the serving path — "nothing on the hot path asks the
  extension" still holds — and no third place may be added.

### Staying current without a restart

A renewed licence that only arrives by email is useless to a running container:
it would keep the old key and lock the customer out anyway. So the instance
fetches its own licence from the stack periodically (daily is plenty), caches it
under `/data`, and **reloads it without a restart**. `SITEBIN_LICENSE_KEY` still
works and wins when set, for air-gapped installs.

## What the stack does

- **Root at first bootstrap.** Generated if absent, honouring an operator-
  supplied root if one is configured, so the key can live in a secret store
  rather than the database.
- **App key at registration.** `POST /api/v1/apps` mints the app's keypair and
  certificate. Convergent like everything else: an existing app keeps its key,
  because re-issuing it would invalidate every licence already sold.
- **Issuance follows the subscription.** A paid ee subscription produces a
  licence whose `expires_at` is the period end and whose `grace_until` adds the
  configured grace. Renewal re-issues; the customer is emailed automatically.
- **A fetch endpoint**, so a running instance and a future desktop app can
  collect the current licence rather than wait for a human to paste an email.
- **The admin portal offers the root public key for download**, because that is
  the value that goes into every app's build.

## Consequences

**ee is no longer unlimited without a key.** After 90 days an unlicensed
instance cannot create new sites. That is a deliberate commercial change and
ELv2 covers it — the licence explicitly contemplates key-gated functionality —
but the README and CLAUDE.md currently promise the opposite and must change with
it. There have been no downloads, so this is a green-field change rather than a
withdrawal of something people rely on.

**The root becomes the crown jewel.** Lose it and no shipped binary will trust
anything new. The problem of "a key that must be backed up" does not disappear;
it moves from a laptop with no backup to a server that has one, which is the
actual win.

**ee never calls a licence server to run.** Verification is offline and always
was; the fetch endpoint is a convenience for staying current, never a
precondition for starting or serving.


---

> **Corrections (post-implementation).** Three things this document states did
> not survive contact with the built code:
>
> - **"Four states" was one short.** The implementation has five; `unknown` is
>   a real, reachable state and not a footnote. The table above now lists it,
>   because a reader who counts four states has no name for the case the whole
>   design leans on ("unknown is not expired").
> - **"Exactly one place" became two, deliberately.** The state gate is in
>   `AuthorizeCreate`; the entitlement ceiling could not go there, because it
>   is checked when a *domain* is added and there is no site creation to hang
>   it on. `CustomDomainsAllowed` is the second place, and it is a place, not a
>   sprinkling: the count is two and both are named in `CLAUDE.md`,
>   `ee/license.go` and `ee/licensing/doc.go`. Anything further is a
>   regression.
> - **The entitlements were verified but never declared.** Sitebin verifies
>   `entitlements.max_custom_domains` on a licence it receives, but its stack
>   self-registration declared no `licensing` block, so the stack minted every
>   licence with no entitlements — and absent entitlements mean *unlimited*.
>   The Enterprise pricing axis (licence tiers that scale by custom-domain cap)
>   was therefore unenforced end to end while every part of the mechanism
>   worked. The stack's own onboarding code records this happening to sitebin.
>   `ee/stackreg.go` now declares the block, sourced from
>   `SITEBIN_STACK_LICENSING` rather than a table in this repo: the numbers are
>   the commercial terms on sitebin.io's pricing page, and this repo is public
>   and released on its own schedule.
