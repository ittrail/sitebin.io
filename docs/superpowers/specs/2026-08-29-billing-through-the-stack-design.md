# Three billing backends behind one seam — design

*2026-08-29. Proposed. Supersedes the first draft of this file, which proposed
deleting the payment providers outright.*

Sitebin Enterprise ships Stripe and Paddle integrations, and separately talks to
the stack's PayGate to read a tier. The two grew side by side and never met.
This makes them three interchangeable **billing backends** behind one seam, and
adds the thing PayGate could never do: take a payment.

An operator picks one. The hosted instance picks PayGate.

## Why it looks like this today

Not a wrong turn against an existing stack: the direct integration landed in
`2f58c09` on **2026-07-14** ("Phase 5"), and the PayGate integration in
`017d84e` on **2026-07-15**, scoped only to reading a tier. The stack path
arrived a day later and grew *beside* the provider code rather than joining it.

## The seam

Identity is the precedent and it is already right: `ee/authn` has a *generic*
OIDC provider whose issuer comes from config, so the stack is one issuer among
others. Billing gets the same shape.

```go
// ee/billing — every backend can sell a tier and show an existing customer
// their subscription. Nothing else is universal.
type Backend interface {
    Name() string
    CheckoutURL(ctx context.Context, acc Account, tier string, success, cancel string) (string, error)
    PortalURL(ctx context.Context, acc Account, returnURL string) (string, error)
}
```

Two capabilities are **not** universal, so they are optional interfaces the
provider type-asserts rather than methods every backend must fake:

```go
// Implemented by backends that hold the subscription state somewhere else.
// PayGate only.
type TierSource interface {
    TierFor(ctx context.Context, subject string) (tier string, ok bool, err error)
}

// Implemented by backends that push events at us. Stripe and Paddle only.
type WebhookReceiver interface {
    WebhookRoutes() map[string]http.Handler
}
```

That split is the whole design, because the two models genuinely differ:

| | Stripe / Paddle direct | PayGate |
|---|---|---|
| Who owns the subscription | Sitebin | the stack |
| How a tier change arrives | webhook → `applyBillingUpdate` → stored tier | polled by `effectiveTier` |
| What Sitebin must verify | HMAC signatures, event mapping | nothing |
| Which processor is used | Sitebin's choice, in Sitebin's config | the stack's, and Sitebin never learns it |

## PayGate must stay provider-agnostic

This is the requirement that shapes the rest. If the stack switches from Paddle
to Stripe — or to something else — **nothing in Sitebin may change**. So:

- The PayGate backend never names a processor, in code, config, routes or UI.
  It calls `POST /api/v1/:appId/checkout` and `POST /api/v1/:appId/billing-portal`
  and follows the URL it is handed.
- It receives no webhooks. The stack verifies and applies them at
  `POST /webhooks/:provider/:appId`. Sitebin does not need to hear about a
  payment: `effectiveTier` already polls `TierFor` and PayGate outranks the
  stored tier, so a cancellation is visible at the next request that resolves a
  tier — which is how it already works, since there has never been a PayGate
  webhook into Sitebin.
- **The user-facing routes lose their provider names.** Today they are
  `POST /account/billing/stripe/checkout` and `.../paddle/checkout`, which puts
  the processor in the URL a customer clicks. They become:

  ```
  POST /account/upgrade/{tier}     → redirect to the active backend's checkout
  POST /account/billing/portal     → redirect to the active backend's portal
  ```

  Webhook routes keep their provider names (`/account/billing/stripe/webhook`),
  because the processor dictates where it posts. They are machine endpoints and
  are only mounted by a backend that implements `WebhookReceiver`.

## Choosing a backend

`SITEBIN_BILLING=stripe|paddle|paygate`. When it is unset, the backend is
inferred from whichever one is configured; **two configured backends and no
explicit choice is a startup error**, in the same spirit as `eeconfig` refusing
to start when `SITEBIN_ANON_TIER` names a missing tier. Guessing which processor
charges customers is not a thing to be relaxed about.

Zero backends stays valid: tiers still resolve, quotas still apply, and an
operator sets tiers by hand. Only self-service upgrades are absent.

## Tier prices carry amounts *and* ids

`Tier.Price` is `{stripe, paddle, display}` — price identifiers for products an
operator made by hand, plus a display string. That cannot serve PayGate, whose
contract is that the app declares the **amount** and the stack creates the
product; there is no field for a price id you made yourself
(`services/platform-api/src/routes/apps.ts`). This is why `ee/stackreg.go`
declares no prices today, and why every Sitebin tier currently lands in
PayGate's priceless branch with no payment product attached.

So it carries both:

```json
"price": {
  "monthly": "9.00", "annual": "90.00", "currency": "EUR",
  "stripe": "price_123", "paddle": "pri_456"
}
```

- **Amounts are always required for a paid tier.** They are what the dashboard
  and the pricing page show, and what `stackreg` declares to PayGate.
- **The ids are required only by the backend that needs them** — checked at
  startup, so a Stripe instance with a tier missing `price.stripe` fails to
  start instead of failing at a customer's click.
- `display` goes away. A shown price must not be able to disagree with the
  charged one; it is formatted from the amount.

There is nothing to migrate: no products exist in any provider yet.

## Changes by file

- `ee/billing/billing.go` — the `Backend`, `TierSource`, `WebhookReceiver`
  interfaces; `Update` and the event mapping stay, they are the direct backends'.
- `ee/billing/stripe.go`, `paddle.go` — implement `Backend` + `WebhookReceiver`.
  Their `CheckoutURL` already exists; it gains the interface signature.
- `ee/billing/paygate.go` — implements `Backend` + `TierSource`. Gains
  `CheckoutURL` and `PortalURL`; the static `ManageURL` becomes the fallback for
  when no backend is configured.
- `ee/billingh.go` — provider-neutral upgrade and portal routes; webhook routes
  mounted only for a `WebhookReceiver`.
- `ee/provider.go` — one `billing Backend` field replacing `stripe`/`paddle`/
  `paygate`; `paygateTier` becomes "ask the backend if it is a `TierSource`".
- `ee/eeconfig` — `SITEBIN_BILLING`, the new `Price` shape, and the startup
  validation above.
- `ee/stackreg.go` — declares `monthlyPrice`, `annualPrice`, `currency`.
- `ee/dashboard.go` — formats the amount instead of printing `Price.Display`.

## Consequences

**The ee feature set grows rather than shrinks.** Stripe and Paddle stay
sellable for anyone self-hosting; the stack becomes a third option that happens
to be what we run.

**This is a ship-order change** (workspace `CLAUDE.md`): `tiers.json` lives at
`/opt/sitebin/` and the pricing page states the amounts. Product first, then the
instance's `tiers.json` plus a verified live upgrade, then the website.
