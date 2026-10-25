# The stack's other pages in Sitebin's look

**Date:** 2026-10-25 · **Status:** implemented · Follows
[`2026-09-30-sign-in-page-theme.md`](2026-09-30-sign-in-page-theme.md)

## Problem

Sitebin's `theme.customCss` (`ee/keycloak-theme.css`) styled only the
Keycloak login. The SaaS Stack now applies the same stylesheet to the other
pages a Sitebin customer meets there, which showed the stack's plain layout in
Sitebin's colours:

- the auth-gateway's account selector and consent gate (with its decline and
  error pages);
- the portal's hosted plan page (`/apps/sitebin/plan`, built from the
  stack's widgets) and its order page (`/order/:id`, the "order with
  obligation to pay" step).

The stack documents stable hooks for them (README § 11.5 of the saas-stack
repo): `html[data-ss-page]` and the gateway's class names;
`html[data-ss-portal-page]` and the portal pages' elements; the widgets'
`--saas-theme-*` custom properties and `::part()` names.

## Design

No new style: the login's claim ticket, the dashboard's chrome and
sitebin.io's pricing cards, carried onto these pages.

- **Gateway pages:** the wordmark (bin mark + lowercase "sitebin") above one
  ticket with the gradient rim; a dashed tear line with punched notches under
  the head (the subtitle as a mono stamp line; on the decline and error pages
  the title), the account row or the consent documents, the amber primary and
  raised secondary buttons, and the stub (operator line, legal links) torn
  off at the foot. The card is `.container::before`, drawn behind the content
  from below the wordmark, because the markup has no card element.
- **Order page:** `main` is the ticket, read as a receipt: mono labels, dashed
  rows, ticks on the features, the withdrawal notice in a dashed box, a tear
  line over the order button. One column on phones.
- **Plan page:** the dashboard's topbar; the subscription panel is the
  dashboard's notched `.ticket`; the catalogue is sitebin.io/pricing's cards
  (dashed rim, notches on the features' tear line, amber for the featured
  plan, green for the one held); every widget button raised ink, the action
  amber.

### Widgets, through the contract only

Custom properties inherit through the shadow boundary, so the palette and
fonts are `--saas-theme-*` on the page. `::part()` reaches the cards, prices,
switch, features and buttons. Two findings made the rest possible (Chromium
153, checked with a probe): a custom property set on a part reaches that
part's own inner elements, and `::part(x)::before` renders. So the cycle
switch's active option takes `--saas-primary` from `::part(cycle-switch)`
(the website toggle's `#202b45` instead of amber under ivory text), the
feature ticks take green from `::part(features)`, and the features' notches
take the card's rim colour from a `--sb-notch` set on `card-featured` /
`card-current`.

### Size: nesting

The stack caps `customCss` at 50,000 characters after `stackreg.go` strips
the comments, and the login rules take 31,925 of them. Written flat, the new
rules came to 21,254 (53,179 in all; minified still 50,640): the scope prefix
alone was 174 selectors, 4,255 characters. So each family is one nested block
with its scope written once, and the total is 47,835.

- One level of nesting only, and every nested selector starts with `&`: the
  browsers from before relaxed nesting (Chrome 112–119, Safari 16.5–17.1)
  refuse a nested selector that starts with a type.
- `&` never follows a `::part()` (a nesting selector cannot stand for a
  pseudo-element), so those rules spell the pseudo-element out.
- A browser with no nesting at all drops the nested rules and keeps the
  stack's own layout in Sitebin's colours: degraded, never broken.
- `TestStackStylesheetNestsOnlyOneLevelBehindAnAmpersand` holds the form.

### Left to the stack

Where the stack's own default already sits well on Sitebin's ground, the
sheet adds nothing: the plan and order pages' footer, the change-done notice,
the consent gate's "tick every box" hint. `primaryColor` and
`backgroundColor` already give the widgets `--saas-theme-primary` and
`-bg`.

## Constraints (tests hold each)

1. Scoped to one of `html.login-pf`, `html[data-ss-page]`,
   `html[data-ss-portal-page]` — the account console carries none
   (`TestStackStylesheetIsScopedToItsOwnPages`).
2. No `@import`, `url(`, `expression(`, `behavior:`, `-moz-binding`, and no
   `<`: the gateway and portal escape it when they write the sheet into a
   `<style>` (`TestSignInStylesheetSurvivesTheStacksFilter`).
3. Under 50,000 characters after stripping
   (`TestSignInStylesheetFitsTheStacksLimit`).

## Verified

Rendered with the stack's own templates (`renderAccountSelector`,
`renderConsentGate`, `renderConsentDeclined`, `renderConsentError`,
`orderPage` for a subscription, a change and a failed change, `planPage` with
the built widget bundle and stubbed PayGate answers for a free user and a Pro
subscriber, English and German) at 1280 and 390: no horizontal overflow, no
page errors. A page with none of the hooks, and a login page, compute the
same styles with and without the new rules. Keyboard focus shows the blue
ring on the gateway and order pages.

## Not covered

- The widgets' confirm, consent and error modals are light-DOM elements with
  hard-coded colours and no contract hooks; they keep the stack's look.
- Inside the shadow DOM, what no part reaches stays the stack's: the
  catalogue heading and the "How it works" note, badge colours, the invoice
  table's links, the single-cycle label.
- The cycle switch shows its focus ring (`:focus-within`) after a mouse
  click too: the widget re-focuses the switch after it re-renders, and the
  inner `:focus-visible` ring cannot be made visible without brightening the
  selected option past the website's toggle.
