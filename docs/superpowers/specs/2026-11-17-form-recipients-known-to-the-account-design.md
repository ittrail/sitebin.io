# Form recipients the account already knows

Date: 2026-11-17. Status: approved by the operator in conversation (2026-11-17), with point 3
(the sign-in address) included.

Builds on `2026-09-24-site-forms-design.md` (forms) and `2026-11-03-site-ownership-design.md`
(owners, admins, transfers).

## The problem

A form is `pending` until its recipient clicks the link in a confirmation mail. The consent is per
form: adding a second form, a form on another site or changing a form's recipient back sends the
same person another mail to click, even when they confirmed that very address for the same owner
a minute ago. The operator, who is customer zero, confirms their own address again and again.

## The rule

**An owner skips the confirmation for an address their account already holds.**

- **Who.** Only a caller that stands on the site as its **owner through an account**: the
  owner's browser session on the edit page, an account API token, or an OAuth token on MCP. Never
  an admin of a shared site, never the edit password, never an anonymous site. The community
  build and a provider without `ext.FormAccounts` behave exactly as before.
- **When.** Adding a form, changing a form's recipient, and resending the confirmation of a
  `pending` form. A `stopped` form never skips (see the stop rule).
- **Which address.** The full address, compared case-insensitively; a `+tag` is a different
  address. It is known when either holds:
  1. it is the account's **verified sign-in address** (`ext.FormAccounts.VerifiedEmail`;
     `via = "sign_in"`), or
  2. a form on a site **the account owns** has it as recipient, is `active`, and was confirmed
     **while the account owned the site** (`ConfirmedAt` not before `Meta.OwnerSince` when that
     is set), on a site that is not locked (`via = "account"`). A form that was itself confirmed
     through the account counts too: the consent came from the same recipient for the same owner.
- **A standing stop blocks it.** When any form on the account's sites with that recipient carries
  a stop (`StoppedAt` set), the address is not known, whatever else holds — also for the sign-in
  address. A stop stands until the recipient confirms that form again: `RequestConfirmation` no
  longer clears `StoppedAt` (it moves the form back to `pending` only), `ConfirmForm` clears it.
  Without that change a second "Resend" of a stopped form would find it merely `pending` and
  skip the recipient. Changing that form's recipient or deleting it drops its stop with it; that
  is deliberate, the stop was for that form.
- **Unknown means mail.** An error from the extension (the account, its site list) or a site
  whose `meta.json` cannot be read (other than gone) means the address is not known: the form
  goes the usual way. Never skip on an error.

### What happens when it is known

- The form is `active` at once, with `ConfirmedAt` = now and the new
  `Form.ConfirmedVia` (`"sign_in"` or `"account"`, `omitempty`; `""` = the recipient clicked).
  `UpdateForm` still moves `seq`, so every earlier confirmation link dies.
- No confirmation mail, and no confirmation throttle is spent.
- The log says `form confirmed through the account` with site, form and `via`, never the
  address.
- **The owner is checked again under the site lock.** The address is looked up before the write
  (it spans several sites, and the store never holds several site locks). The store then
  activates only while `meta.json` still names that owner, and for a resend only while the form
  is still `pending` with no stop. Otherwise the form stays `pending`, the throttles are spent
  and the mail goes out as before (a throttle refusal there is the existing "not sent" warning).
- A stop that arrives between the look-up and the write is a race of milliseconds and accepted:
  the new form's every submission mail carries its own stop link.

## Surfaces

- **Seam.** New optional `ext.FormAccounts { VerifiedEmail(accountID) (string, error);
  OwnedSiteIDs(accountID) ([]string, error) }`, implemented in `ee` with the account's
  `EmailVerified` and the existing `ownedSiteIDs` (by `meta.json`, not markers). Asked only on
  those three form writes, never on a submission.
- **Core.** `store`: `Form.ConfirmedVia`, a `FormShortcut{Owner, Via}` argument on `AddForm`,
  `UpdateForm` (through `FormPatch`) and `RequestConfirmation`, the stop rule above.
  `httpapi`: `knownRecipient` (the look-up), a `formCaller{ip, owner}` for the three form writes
  (API: the actor's `roleOwner`; MCP: `standOwner`).
- **JSON API.** `confirmed_via` on a form (`omitempty`).
- **MCP.** The output is unchanged (no new field: clients that cached a closed output schema
  before 2026-11-11 would reject it). The tool descriptions of `add_form`, `update_form` and
  `resend_form_confirmation` say when a form is active at once; `status` says it.
- **Edit page.** The toasts after add, save and resend say "active at once" and why; an active
  form confirmed through the account says so under its status.
- **Website.** `/docs/forms/` and the `add_form` / `update_form` rows of `/docs/mcp/`.

## Not done, on purpose

- No stored list of confirmed addresses per account. The answer is computed from the forms that
  exist, so nothing new about a person is stored, and a deleted form or site takes its consent
  with it. An address that no active form holds any more is confirmed by mail again.
- The confirmation mail's text is unchanged (it is delivered and gates every form; see the forms
  design's deliverability notes).
- Forms that are `pending` today are not activated in the background; their next resend does it.

## Corrections (post-implementation)

- **The confirmation mail says what the click covers.** "The confirmation mail's text is
  unchanged" no longer holds. With the privacy page and the DPA (version 2026-11-17) describing
  the shortcut, a recipient who confirms one form must learn that the click also lets the same
  account's other forms send to the address. On a site an account owns, on an instance whose
  extension answers `ext.FormAccounts` (`confirmsAccountWide`), both parts of the mail add one
  sentence: "Confirming also lets the other forms of the same Sitebin account send to this
  address without asking you again." It is said wherever a click might count, not only where it
  will (a site locked or transferred later still got the sentence): the mail must never cover
  less than the click does.
- **A deleted stopped form no longer blocks.** The standing stop is read from the forms that
  exist, so an owner who removes the stopped form lifts the block for the address (another
  active, confirmed form still vouches). The recipient's stop link on each form still works. The
  legal texts therefore do not promise that a stop makes the account ask again.
