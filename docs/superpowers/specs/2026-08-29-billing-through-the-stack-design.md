# Billing through the stack — design

*2026-08-29. Proposed.*

Sitebin Enterprise ships its own Stripe and Paddle integration: checkout URLs,
HMAC webhook verification, a customer→account index, and the code that turns a
provider event into a tier change. PayGate already does all of it, and better,
because it does it for every app on the stack at once.

This takes the payment providers out of Sitebin and makes the stack the billing
path — the same relationship Sitebin already has with the stack for identity.

## Why it looks like this today

Not a wrong turn against an existing stack: the direct integration landed in
`2f58c09` on **2026-07-14** ("Phase 5"), and the PayGate integration in
`017d84e` on **2026-07-15**. The stack path arrived a day later and was scoped
to reading a tier, so it grew *beside* the provider code rather than replacing
it. Nothing has needed to reconcile them until now.

## The shape

Identity is the precedent, and it is already right. `ee/authn` has a *generic*
OIDC provider whose issuer comes from config (`account.OIDCProv`), so the stack
is one issuer among others and a different IdP is a config change, not a code
change. Sitebin does not ship a bespoke "SaaS Stack login".

Billing gets the same treatment:

> **One interface, one shipped implementation, no provider SDKs in the tree.**

```go
// ee/billing
type Source interface {
    TierFor(ctx context.Context, subject string) (tier string, ok bool, err error)
    CheckoutURL(ctx context.Context, subject, tier, success, cancel string) (string, error)
    PortalURL(ctx context.Context, subject, returnURL string) (string, error)
}
```

`PayGate` implements it. Nothing else ships. An operator who wants to sell
through their own processor implements `Source` — the door stays open without
Sitebin carrying two payment integrations it cannot test against real money.

## What PayGate already offers

Verified against the stack (`services/paygate/src/routes/`):

| Sitebin has | PayGate endpoint |
|---|---|
| `handleStripeCheckout`, `handlePaddleCheckout` | `POST /api/v1/:appId/checkout` (`success_url`, `cancel_url`) |
| `ManageURL` (a static configured link) | `POST /api/v1/:appId/billing-portal` → `portal_url` |
| `handleStripeWebhook`, `handlePaddleWebhook`, `applyBillingUpdate`, `resolveBillingAccount` | `POST /webhooks/:provider/:appId` — PayGate verifies and applies |
| — | `GET/POST /api/v1/:appId/subscription` + `cancel`/`change`/`pause`/`resume` |

The webhook machinery disappears rather than moves. Sitebin does not need to
learn about a payment event: `effectiveTier` already polls `TierFor`, and
PayGate outranks the stored tier. A cancellation becomes visible at the next
request that resolves a tier — which is exactly how a tier change already
propagates today, because there has never been a PayGate webhook into Sitebin.

## Tiers carry amounts, not price ids

`eeconfig.Tier.Price` is `{stripe, paddle, display}` — identifiers for products
an operator created by hand, plus a display string. That is the wrong shape for
self-registration, which is why `ee/stackreg.go` currently declares no prices at
all and every Sitebin tier lands in PayGate's priceless branch with no payment
product attached.

The stack's contract is that the **app declares the amount** and PayGate creates
the product; there is no field for a price id you made yourself
(`services/platform-api/src/routes/apps.ts`). So:

```json
"price": { "monthly": "9.00", "annual": "90.00", "currency": "EUR" }
```

`display` goes away — a price the dashboard shows should not be able to disagree
with the price charged, and it can be formatted from the amount.

There is nothing to migrate. No products exist in any provider yet.

## Changes by file

**Removed** — `ee/billing/stripe.go`, `ee/billing/paddle.go`,
`ee/billing/billing_test.go`'s provider cases, and the checkout, webhook,
`applyBillingUpdate` and `resolveBillingAccount` handlers in `ee/billingh.go`
(~500 lines). With them go `SITEBIN_STRIPE_*` and `SITEBIN_PADDLE_*`.

**Changed**
- `ee/billing/paygate.go` — gains `CheckoutURL` and `PortalURL`; `ManageURL`
  becomes a fallback for when PayGate is not configured.
- `ee/billingh.go` — one `/account/upgrade/{tier}` route that redirects to
  PayGate's checkout, and one `/account/billing` that redirects to the portal.
- `ee/eeconfig` — the new `Price` shape; `Paid()` becomes "has an amount".
- `ee/stackreg.go` — declares `monthlyPrice`, `annualPrice`, `currency`.
- `ee/dashboard.go` — formats the price instead of printing `Price.Display`.

## Consequences worth stating

**An ee instance without the stack cannot sell anything out of the box.** That
is the deliberate trade: tiers still work, quotas still work, an operator still
sets tiers by hand or from their own `Source`. Only the shipped checkout is
gone. Standalone paid hosting is no longer a thing Sitebin does for you.

**This is a ship-order change** (workspace `CLAUDE.md`): `tiers.json` lives at
`/opt/sitebin/` on the instance, and the pricing page states the amounts. Product
first, then the server's `tiers.json` and a verified upgrade on the live
instance, then the website.
