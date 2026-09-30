# The stack's sign-in pages in Sitebin's look

**Date:** 2026-09-30 · **Status:** implemented

## Problem

`app.sitebin.io` signs in through the SaaS Stack's Keycloak
(`auth.ittrail.cloud`, client `sitebin-app`). That page showed the stack's
stock theme: the operator's IT-Trail mark in a white circle, uppercase
Source Sans, a white sign-in button. Sitebin's registration already declared a
theme (`displayName`, three colours, `faviconUrl`), but on the login page
those reach only a few custom properties — the stack's sheet hard-codes
`#kc-login` white, so `primaryColor` changed nothing visible, and only the
background colour showed.

Sitehorse, the other app on the stack, looks like itself on the same page. It
declares `theme.logoUrl` and a `theme.customCss` of its own. The stack
supports both per client; Sitebin used neither.

## Design

The registration's `theme` block gains:

- `logoUrl` — the bin mark (`/_sitebin/assets/static/favicon.svg`, served by
  the instance, the same file as `faviconUrl`). The login theme exposes it as
  `--brand-logo-url` on `#kc-header-wrapper`; the consent gate, the account
  selector and the plan page show it as an `<img>` in their logo circle.
- `customCss` — `ee/keycloak-theme.css`, embedded with `go:embed` and declared
  with its comments stripped (32 KB instead of 36 KB; the stack caps it at
  50,000 characters, and a refused registration converges nothing).

The stylesheet is the claim-ticket look from `web/static/app.css`: deep-space
ink with the slow aurora, a wordmark (bin mark + lowercase "sitebin"), the
card as a ticket with the amber-to-blue gradient rim, dashed tear lines with
punched notches under the head (the page title, or on sign-in the "admit one"
stamp and the one-account notice) and above the stub (powered-by and the
operator's legal links), fields on `#0d1322` with the blue focus ring, the
amber gradient primary button, Google/Microsoft side by side.

## Constraints the stylesheet lives under

1. **It loads nothing.** Before injecting, the stack's `safeCss`
   (`branding.ftl`, `saas-stack-account.js`) replaces every `url(…)` with
   `none` and cuts an import rule up to the next semicolon — a stylesheet on a
   password page must not be able to fetch or send anything. So no web font
   (Space Grotesk is used where installed, the system UI face otherwise) and
   no image except the logo, which comes through `--brand-logo-url`. The cut
   also runs through comments: the word for an import rule inside a comment
   swallows the comment's end and the rules after it.
   `TestSignInStylesheetSurvivesTheStacksFilter` refuses every pattern the
   filter rewrites, comments included.
2. **Every rule is scoped to `html.login-pf`.** The stack injects the same
   `customCss` into the Keycloak account console (React, PatternFly 5) when it
   is opened with `?referrer=sitebin-app`. Only the login flow's `<html>`
   carries `login-pf`. `TestSignInStylesheetIsScopedToTheLoginPages` walks
   every selector.
3. **Specificity.** The stack's sheet uses `!important` throughout and colours
   text with `html body .login-pf-page #kc-content span` — an ID selector.
   Sitebin's sheet comes later and wins ties, but any rule that colours a
   `span` inside `#kc-content` (field errors, the required asterisk, labels,
   alert text, the legal separators) must carry `#kc-content` too, or the
   stack's grey wins. Found in review: the error text rendered grey.

## Verified

Rendered against the live login flow before deployment by routing
`/branding/sitebin-app` in Playwright to the candidate CSS (so `branding.ftl`
and its filter apply it exactly as production does): sign-in, registration
(empty and with validation errors), password reset; 1280×900 and 390×844.

## Stack issues noticed, not fixed here

- `primaryColor` has no visible effect on the login page: `saas-stack.css`
  sets `#kc-login`/`.pf-m-primary` white with `!important` after reading
  `--brand-primary` into PatternFly variables nobody uses.
- `#kc-social-providers a` styles every link in that block as a full-width
  button, including the "Privacy Policy" link inside the social-login notice.
  Sitebin's sheet puts it back inline for its own pages.
