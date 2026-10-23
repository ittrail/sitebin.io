# What Sitebin takes from the stack's consumer commerce (2026-10-23)

The SaaS Stack now sells to consumers with the obligations EU and German law
put on it (stack design: `saas-stack/docs/superpowers/specs/2026-10-23-consumer-commerce.md`,
guide: `saas-stack/docs/consumer-commerce.md`). Most of it needs nothing from
an app: the order page in front of the payment page, the renewal reminder,
VAT inside the gross price, mail in four languages. Four things do need
Sitebin. This note records them. They are small, so there is no separate plan.

## 1. A customer who already pays is sent to the plan page

The stack's checkout refuses to sell a second subscription. It answers
`409 already_subscribed` (or `already_entitled` for a tier bought for good)
with `details.manageUrl`. That is the stack's plan page with the change
preselected (`?tier=&cycle=`), or a store's subscription page.

`post()` used to turn every non-2xx into a plain error. The dashboard then
showed "Checkout unavailable" for what is really a plan change. Now:

- `post()` keeps the answer whole (`paygateError`).
- `CheckoutURL` turns exactly those two codes, carrying an **https** `manageUrl`,
  into `billing.AlreadySubscribedError{ManageURL}`. Every other answer stays an
  error, a conflict included.
- `handleUpgrade` sends the browser there with the handoff page. That is
  `leaveTo`, never a 303: `form-action 'self'` drops a form POST's redirect to
  another origin.

The error type lives in `billing.go`, not with PayGate. A direct backend may
learn the same thing one day. The handler names no processor either way.

## 2. The cancellation page is always one click away (§ 312k BGB)

German law requires a cancellation button wherever a consumer can sign a
contract. The stack hosts it: `<issuer origin>/apps/<app>/cancel`, no sign-in,
four languages, confirmed by mail. `PayGateConfig.CancelURL` derives it the way
`PlanURL` is derived (`cancelUrl()` in the stack's SDK). The dashboard links it:

- in the Billing card;
- at the foot of the page, as "Cancel contracts here", whenever the instance
  has one.

The marketing site's footer links it too (website repo).

## 3. Deleting a stack account opens the deletion itself

The stack hosts a deletion page per app: `<issuer origin>/account/delete?app=<app>`,
`deleteAccountUrl()` in its SDK. It opens the deletion directly, branded for
the app. The console's start page left the user to find it.

`Config.AccountDeletionURL()` returns that page, under the same condition as
`StackDeletion()`, and "" otherwise. The danger zone links it. A stale local
delete form posting to `/account/delete` is handed on to it with the handoff
page. That used to be a 303, which Chrome dropped for the same CSP reason as
in section 1.

The app id is the registration's (`SITEBIN_STACK_APP_ID`), else PayGate's.

## 4. The terms travel with the purchase confirmation

The stack attaches the terms to a purchase confirmation (order S22: a durable
medium, not only a link). It fetches the file named by
`consents[].documentUrl`. `StackConsent.DocumentURL` carries it from
`SITEBIN_STACK_CONSENTS`:

- It must be https. The stack refuses anything else, and a refused
  registration only surfaces as a background error at boot, so Sitebin checks
  it at startup.
- It is omitted when unset.

The stack's content hash covers `url` and `title` only. Adding `documentUrl`
to a version already recorded is therefore accepted; no version bump is
needed.

On the hosted instance, `terms` declares `documentUrl: https://sitebin.io/terms/`.
