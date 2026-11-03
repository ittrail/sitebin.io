# Site ownership: adopt, transfer, share

Date: 2026-11-03. Status: design, approved in conversation with the operator
(mockups reviewed in the brainstorming companion). Enterprise (`ee/`), with
the few core fields named below. Builds on
`2026-11-03-front-door-design.md` (its anonymous ticket links here) and
`2026-11-03-site-tags-design.md` (tags of shared sites, below).

## Why: the operator's own scenario

An agency (the operator, IT-Trail) builds a customer's website, then hosts it
in its own account for a year. The customer pays the agency outside Sitebin,
and asks the agency for changes. After the year the customer either renews
with the agency, or the site moves into **the customer's own Sitebin
account**. From there they can download it and host elsewhere. **To receive
it, the customer must already have booked a paid plan that holds the site**
(operator's decision: "er muss schon vorher den Tarif gebucht haben, erst
dann funktioniert die Übertragung"). The customer can still book the agency as their admin: they share the
site, now theirs, with the agency.

The operator also wants a visitor who made an **anonymous drop** to be able to
take exactly that drop into their account: from the drop's ticket on
sitebin.io, or in the account by entering the drop's public URL and edit
password.

## Goals

1. **Transfer** one owned site, **or several together** (a bundle, for
   example every site on `kunde.at`), to another account, by the other
   account's email address.
   - The offer has a **mandatory accept-by date**.
   - It can have an **optional available-from date**: from then on the
     recipient sees the site as ready to take over.
   - Ownership moves **at the moment the recipient accepts**. There is no
     automatic move at a date.
2. **Share** an owned site with another account as its **admin**, with an
   optional end date.
3. **Adopt** an anonymous drop into the signed-in account with its public URL
   and edit password: from the dashboard, or from the drop's ticket.
4. **Gated by plan.** On the hosted instance every paid account (Pro, Studio)
   may do all of it. Free and anonymous may do none of it: not send, not
   accept, not be an admin, not adopt.

## Non-goals

- Payment between the parties. The agency's fee is outside Sitebin.
- More than one share role. There is only **Admin**; viewer and editor roles
  are not part of this design.
- Transfer or share of an **anonymous** site. Adopt it first.
- API or MCP tools to create, accept or cancel transfers and shares. In v1
  they are dashboard actions. An admin's own API token and MCP connection do
  reach a shared site (see Sharing).
- Re-sharing. An admin cannot share the site further.

## Anti-goals

- **Pushing a site into someone's account without their consent.** Someone
  must not be able to plant a phishing site in a victim's account. So every
  transfer and every share needs the recipient's **acceptance**.
- **Learning whether an email address has an account.** Creating an offer
  never reveals it.
- **The previous owner keeping access after a transfer.** That means through
  the edit password, an API token, an upload token or a share.
- **A transfer leaving a site without a working address**, or going over the
  recipient's plan.
- **Destroying anything on an error or a downgrade.**
  - A plan lookup that fails is *unknown*: refuse the new action, and keep
    every existing share and offer.
  - A downgrade **pauses**. It never deletes.

## Plan gate

- A new tier flag in `tiers.json`: **`sharing`**, a bool. Absent means
  **false**, the same polarity as `custom_domains`: a tier that forgets the
  field gets nothing. The name is the builder's call.
- It is asked through a new optional seam method, for example
  `ext.SharingAccounts.SharingAllowed(accountID) (bool, error)`, following
  `ZoneAccounts`. It is asked:
  - when an offer, an invitation or an adoption is **created or accepted**,
    for both parties where both are involved;
  - when an admin **uses** a share, for the owner's and the admin's current
    plan. A `false` answer pauses the share, and an error refuses that one
    request. Neither removes the share.
- Hosted values: `pro`, `studio`, `admin` (and `unlimited`) get
  `"sharing": true`. `drop` and `free` do not. This is an operator step in
  the ship order.
- **`SITEBIN_ACCOUNT_MODE=accounts`** (no tiers) has no plans to sell the
  feature by. There, `SharingAllowed` answers **true** for every live
  account, and the limits check uses the instance's global caps. A
  self-hosted team instance shares internally without configuring tiers.
- The community build has no accounts, so none of this exists there.
- **Operator-owned sites cannot be shared** (refused at invitation).
  - Sites owned by an operator account (admin tier plus
    `SITEBIN_ADMIN_ACCOUNTS`) get two exemptions keyed on the **owner**: they
    attach `SITEBIN_OPERATOR_DOMAINS` names without proof, and the abuse
    scanner never locks them.
  - An admin would inherit both, and the code has no "who is acting" signal
    for those checks.
  - Transferring an operator-owned site is allowed: its operator-zone names
    are in the "will not move" list.

## Transfer

### Offer

- **Created by the owner** on the dashboard (per site: "Transfer"). The form
  takes:
  - the recipient's **email**;
  - **accept by**, required, in the future, at most 1 year ahead (the
    builder's call);
  - **available from**, optional, before accept-by.
- **What the owner is told before confirming:**
  - Addresses that **will not move**: any address whose proof depends on the
    sender's account, as the domain rules define it. That covers names under
    the sender's account zones, names under `SITEBIN_OPERATOR_DOMAINS` (the
    operator's own zones) and addresses held only through the host
    (`ViaHost`).
  - That the edit password, tokens, tags and shares do not travel.
- **An offer is a bundle of 1 to 50 sites** (operator's request,
  2026-11-03): "alle Seiten mit Domain kunde.at". All of them share the
  recipient, the dates, and one acceptance. A one-site transfer is simply a
  bundle of one.
  - The dashboard's "Transfer" opens a transfer page listing the owner's
    sites with checkboxes (server-rendered, no script). It has two selection
    helpers:
    - **by host**: "every site on kunde.at";
    - **by tag**: "every site tagged Müller GmbH", reusing the tag filter
      links.
- **A host travels whole.** A host has one owner. If an address of a site in
  the bundle is on a host that one of the sender's sites *outside* the
  bundle also uses (for example `kunde.at` in the bundle and `kunde.at/blog`
  on another site), the offer cannot be created as it stands.
  - The page names those sites and offers to **add them to the bundle** with
    one click, which is the normal way out. The other way out is to remove
    the conflicting addresses first.
  - Acceptance re-checks the rule, in case the sender added such an address
    after the offer was made. If the rule then fails, the offer pauses and
    the sender is told why.
- **A site is in at most one open offer.** A new offer that includes it
  replaces the old one only after confirmation, naming the old bundle.
- The sender may **remove a site from an open bundle**, and add one, as long
  as the host rule still holds. The recipient's card shows the current list.
- **Rate limited per account**, offers and invitations together: 20 a day,
  the builder's call. The limit is there to keep the forms from being used to
  send mail through the instance.
- The sender can **cancel** the offer, and **change** either date, until it is
  accepted.
- **Storage:** ee, for example `data/transfers/<offer-id>.json`, plus the
  lookups the builder needs (by site, by sender, by recipient email). It holds
  site, sender, recipient email (lowercased), dates, created and state. The
  layout is the builder's call.
- **Matching:**
  - the recipient email is compared case-insensitively against **verified**
    account emails only. On the hosted instance every account comes from the
    stack and is verified; an unverified local account never matches;
  - whether a match exists is never shown to the sender.

### Recipient

- From `available from` (or at once, without that date) until `accept by`,
  the recipient's dashboard shows a **"Ready to take over"** card per offer.
  Each card shows:
  - sender, deadline, and the bundle's sites (names and addresses);
  - **a limits check** against the recipient's current plan, **for the whole
    bundle**. It includes the plan's `sharing` flag. Each line is marked ✓ or
    ✗ with its numbers:
    - account-wide caps are summed: sites (current count + bundle size),
      container services, and the account-wide domain ceiling;
    - per-site caps are checked for every site: bytes, files, custom domains
      that move, forms. A failing site is named.
  - **Acceptance is all-or-nothing.** Taking part of a bundle could split a
    host between two owners.
  - **Accept** (enabled only when every line is ✓) and **Decline**.
  - When the plan is the problem, say so in plain words ("Your Free plan
    cannot take over sites") and link to the upgrade route
    (`/account/upgrade`, provider-neutral).
- An account that signs up later with the offered email sees the offer
  then.

### Accept: what happens, under the site locks

**For a bundle, the steps below run for every site, and the bundle moves as
one:**
- Take the site locks in a fixed order (sorted view ids, so two acceptances
  never deadlock).
- Re-check every site and the bundle's summed limits.
- Only then apply.
- The offer records its progress ("accepting", per-site done). A crash
  midway is finished by the next start or sweep, which completes the
  remaining sites; it never rolls the done ones back. Each step is
  idempotent.
- The result page lists every site, with its new edit password.

Re-check everything first. The offer must still be open and within its
dates. The site must not be locked and must still be owned by the sender.
Both parties must be live, not suspended, and allowed by plan, and the limits
check must pass. Then:

1. **Owner:** `OwnerAccountID` = the recipient. Re-index: link to the
   recipient, unlink from the sender.
   - `meta.json` is the truth. Account links are an index that the builder
     makes crash-safe, for example: link the new owner, write meta, unlink
     the old one, and a reconciling check corrects a stray link.
2. **Restamp as if the site were created in the recipient's account.** That
   covers quota caps, the forms cap, the trust marker from the recipient's
   tier, and a tier-imposed expiry recomputed from the recipient's tier. Use
   the existing tier-change restamping. An expiry the previous owner chose
   themselves (`ExpiryFromTier=false`) stays, as a site setting.
3. **Addresses:** detach the addresses listed as "will not move" through
   the normal `RemoveDomain` path, which prunes their redirects and targets.
   The never-offline rules hold: if no custom domain serves, the generated
   address serves.
4. **Cut the old owner off:**
   - a **new edit password**, shown once to the recipient on the result page;
   - the site's upload tokens revoked;
   - the site's **tags cleared**, since they are the old owner's private
     labels;
   - **shares ended**;
   - any other offer for the site cancelled.
5. **Forms keep working.** Recipients stay as confirmed, so a live form never
   breaks. But who receives the visitors' submissions must never be a
   surprise:
   - the offer's notice lists every form recipient to the sender, and the
     recipient's card lists them to the recipient ("submissions go to …");
   - the result page after accepting repeats the list with a link to change
     them.
6. **Container sites keep running.** If the runtime keys anything by owner
   (for example its per-account service count), that moves with the site.
   The builder checks `ee/containers` for owner-keyed state. (The builder
   check found the service count is computed live from `meta.json`, so it
   moves with the owner.)
7. **Record it.** A provenance line on the site, and one in each account's
   log. Mail both parties.

### Decline, expiry, cancellation

- **Decline:** the offer ends, and the sender is mailed.
- **Accept-by passes:** the sweep ends the offer, the site stays with the
  sender, and both are mailed.
- **Sender cancels:** the offer ends, and the recipient's card disappears.
- **Pauses that never delete:**
  - The **sender is downgraded** to a plan without `sharing`: the offer
    pauses, and the recipient's card says it is not available right now.
  - The **site is locked**: the offer pauses.
  - The **sender or the recipient is suspended**: the offer pauses.
  - The accept-by date still runs while an offer is paused.
- **Account deletion** of either party ends the offer.

## Sharing

### Invitation

- The owner invites by email, with an optional **until** date. The same
  matching rules and the same "never reveal" rule as transfers apply.
- **At most 10 admins per site** (the builder's call).
- The invitee **accepts** in their dashboard. Until then nothing is
  granted. An invitation not accepted within 30 days lapses (the builder's
  call).

### What an admin may do

An admin may do **everything the owner may do with this site**, on every
surface where the owner may do it: the edit page through the browser session,
the dashboard's site row, the admin's **own account API token**, and the
admin's **MCP connection**. There are exceptions, which are **refused for an
admin on every surface**:

- delete the site;
- **set, change or clear `expires_at`.** An expiry is a deletion on a timer,
  so it is checked **per field** in the settings write (API `PUT`, MCP
  `update_site`, the edit page), not per route;
- rotate the edit password (it is the owner's credential, and deploy scripts
  depend on it);
- transfer the site;
- invite admins, or end shares other than their own. An admin may always
  leave a share.

**Caps are always the owner's plan.** A shared site counts against the owner,
never the admin. An admin's account zones never apply to a shared site (zone
names attach to the zone owner's sites only, as today).

### Where the rule lives

- **The gate:** `withEditAuth`, `tokenOwns` and `sessionOwns` (core,
  `internal/httpapi/server.go`) today compare an account with
  `Meta.OwnerAccountID`. They learn a second standing: **admin**. Keep the
  one-gate rule: the owner-only exceptions are decided where the standing is
  known, not in every handler. For example the gate puts owner or admin into
  the request's actor, and the owner-only routes refuse admin.
  - The session rule is unchanged: `X-Sitebin-Session: 1`, same-origin.
  - OAuth tokens still act on `/mcp` only.
- **The data:** a new core field `store.Meta.Admins` (`omitempty`), a list of
  `{account, until}`. It is ownership data next to `OwnerAccountID`, not an
  enterprise concept leaking into core. The community build never writes it.
- **Pending invitations** live in ee, next to the transfer offers. Accepting
  one writes the entry into `Meta.Admins` under the site lock.
- **Expiry:** an entry whose `until` has passed grants nothing from that
  moment; the gate compares it. The sweep removes the entry and mails both
  sides.
- **Plan check on use:** see the Plan gate. A paused share is shown to both
  sides with the reason.
- **The admin's dashboard** lists shared sites among its own, marked "shared
  by <owner's name or email> · until <date>". MCP `list_sites` lists them too,
  with an `omitempty` marker field (for example `"role": "admin"`), so a
  connector with a cached output schema only stumbles over shared sites. The
  edit page shows a small banner "shared by …" and hides the owner-only
  controls.

### Tags on shared sites

The site's `Meta.Tags` are the owner's private labels. An admin does not see
them. An admin tags a shared site **in their own account**: the admin's tag
registry (see the tags design) gains a per-site list for sites they are an
admin of, and the dashboard's filter covers owned and shared sites alike.
This lets the agency file a customer's site under "Customer: Müller" without
the customer seeing it. The admin's tags are dropped when the share ends.

**Tags in the API and MCP, for an admin caller.**
- The `tags` field means *the caller's own tags* for the site: for the owner,
  `Meta.Tags`; for an admin, the admin's list in their registry.
- The core reaches the admin's list through a new optional seam method, for
  example `ext.AdminTags.TagsFor(accountID, viewID)` and
  `SetTagsFor(accountID, viewID, tags)`, validated by the same `CleanTags`.
- So an agency's agent can work with "Customer: Müller" through its own
  token, and the owner's labels never leak.
- Without the seam (community), there are no admins, so the question never
  arises.

## Adopting an anonymous drop

- **Dashboard:** "Adopt an anonymous drop", a form with the drop's **public
  URL** (or its view id) and its **edit password**, posted to
  `/account/adopt` behind the CSRF token.
- **From the ticket on sitebin.io:** the front-door design's anonymous ticket
  carries "Take it into my account". It opens `/account/adopt` on the
  instance with the site **prefilled from the URL**. **The edit password
  never travels in a URL.** The visitor pastes it there; the ticket has a
  copy button beside the password. If the visitor is signed out, sign-in
  comes first and returns to the prefilled form. For a visitor without a
  paid plan the ticket's line says that adopting needs Pro or Studio.
- **Conditions:**
  - the site is anonymous (`OwnerAccountID` empty), not locked, and not
    expired;
  - the password verifies, through the existing edit-password check and its
    IP limits;
  - the account's plan has `sharing`, and its limits hold (the same check as
    a transfer).
- **Rate limited per IP and per account.** A small number of attempts per
  hour; the exact number is the builder's call. Every failure has the same
  answer ("URL or password wrong"), so the form does not reveal whether a
  site exists.
- **Effect:**
  - like a creation in the account: owner set, linked, stamped from the tier
    (quota, trust marker, forms cap, tier expiry);
  - provenance "adopt";
  - **the edit password is kept**, because the adopter is the ticket holder
    and their deploy keeps working. The dashboard offers a rotate as usual.
  - Nothing else about the site changes.
- **Abuse:** adoption gives an anonymous drop the trust of a paid tier. That
  is no more than the same account creating it directly, and a drop with a
  blocking scanner finding is already locked and cannot be adopted. Every
  stamp at adoption must go through the same functions creation uses, so
  nothing is stamped twice in two ways.

## Operator view, suspension, GDPR

- **Admin register:** in the site's detail, show its admins and any open
  offer. They matter for an abuse response: who else has access, and who is
  about to own it. A lock pauses both (above).
- **Suspension:**
  - a suspended account's offers and invitations pause;
  - its shares grant nothing;
  - it cannot accept;
  - sites locked by an account suspension cannot be transferred or shared.
- **Account deletion (owner):** its sites are deleted as today, which ends
  their shares and offers. Its shares on others' sites end, and offers to it
  end.
- **Account deletion of an admin of a LOCKED site** does not block the
  deletion; only owning a locked site does, as today. The evidence of who had
  access survives in the locked site's own provenance:
  - the share's grant and every write the admin made are already there, with
    the acting account;
  - removing the admin entry adds one more line, carrying the account id and
    the email it had;
  - that provenance is kept with the locked site until the site is purged.
  - The admin's own account log follows the existing retention rule for an
    account involved with a locked site (max(90 days, lock retention)),
    which the builder extends from "owns" to "owns or is an admin of".
- **GDPR export:** include the account's offers (sent and received),
  invitations and shares. The other party's email is shown only where the
  account already knew it: the address it typed, or the sender of something
  it received.

## Mail

Plain text, like the form submission mail (Microsoft 365 quarantines HTML
mail with action links). The mails go through the account mailer
(`ee/smtp`):

- a transfer offered, or available from its date;
- a transfer accepted, declined or expired;
- a share invitation;
- a share accepted, ended or expired.

The dashboard is the source of truth, and a mail is only a notice. Without a
configured account mailer nothing is sent, and the dashboard still works.

**Mail goes only to an existing, verified account.** An offer or invitation
to an address with no account sends nothing, so the instance never mails
strangers on a user's behalf. Whatever the outcome, the sender is told the
same thing: "If an account with this address exists, it has been notified.
Otherwise ask the person to sign up with exactly this address. The offer
appears in their account as soon as they do." That keeps the never-reveal
rule.
**The hosted instance has no account mailer today** (only
`SITEBIN_FORMS_SMTP_*`). Configuring `SITEBIN_SMTP_*` (mail01) is an operator
step in the ship order.

## Terms and website

- **The terms change.** `/terms/` currently says a drop "is managed only
  through its claim ticket". The new version covers:
  - adopting a drop into an account;
  - transfer between accounts: acceptance, the recipient's plan, what does
    not move;
  - sharing with admins: the owner stays responsible for the site and its
    content, and the admin acts on the owner's behalf.

  This is a **new terms version**. It is declared to the stack with a new
  date, so the consent gate asks every user once (the 2026-09-29 procedure).
  The operator approves the wording.
- **Privacy:** what the other party sees, which is the email address and the
  name the dashboard shows.
- **Pricing page:** "Transfer and share sites" under Pro and Studio, not
  Free. The table and `tiers.json`'s `sharing` must agree, as the coupling
  rules require.
- **Docs:** a new section in `/docs/using/` (or its own page): transfer,
  sharing, adopting. In `/docs/mcp/`: shared sites in `list_sites`, and an
  admin's token reaching them.

## Tests the builder must include

- **Gate:**
  - an admin through session, token and MCP may do what the owner may;
  - every owner-only action is refused to an admin on every surface;
  - an expired, paused or ended share grants nothing;
  - a plan lookup error refuses the request and keeps the share.
- **Transfer:**
  - the dates: not visible before available-from, ending at accept-by;
  - accepting is refused while any limit fails, and for a Free plan;
  - after accept: owner, index, restamp, new password, tokens revoked, tags
    and shares cleared, zone and operator-zone names detached, generated
    address serving;
  - locked or suspended parties pause the offer;
  - accepting twice, or concurrently with a cancel, is decided under the lock;
  - a crash between the index steps is reconciled.
- **Never revealing an account's existence** through the offer and invite
  forms, and no mail to an address without a verified account.
- **The calls from the builder check:**
  - an admin setting `expires_at` is refused on API, MCP and the edit page,
    while the other settings in the same write are allowed;
  - bundles:
    - an offer that would split a host is refused, and the page offers to
      add the missing sites;
    - the selection helpers work by host and by tag;
    - the summed limits are checked (sites, container services) and per-site
      caps name the failing site;
    - acceptance is all-or-nothing, and two concurrent acceptances of
      overlapping bundles cannot deadlock;
    - a crash mid-bundle completes on restart;
    - a site already in an open offer is only re-offered after
      confirmation;
  - form recipients are listed on the offer, the card and the result page;
  - sharing an operator-owned site is refused;
  - accounts mode without tiers shares freely;
  - an admin's `tags` through the API and MCP are their own registry list;
  - deleting an admin account is not blocked by a locked site, and the
    locked site's provenance keeps the admin's id and email.
- **Adopt:**
  - the prefill from the URL;
  - one identical answer for a wrong URL, a wrong password and a locked or
    expired site;
  - the rate limits;
  - stamping identical to creation;
  - Free refused.
- **Container site:** transfer keeps it running and moves its service count.
- **E2E:** `e2e/accounts.ps1` (two accounts, `sharing` tiers), with the
  container part in `containers.ps1`.

## Ship order

This is a tiers change (`sharing`), so the fixed order applies:

1. Merge and push the product.
2. On app.sitebin.io:
   - add `"sharing": true` to `pro`, `studio`, `admin` and `unlimited` in
     `/opt/sitebin/tiers.json`;
   - configure the account mailer;
   - deploy (pre-deploy snapshot into `backups/`, keep 3 rollback images,
     prune the build cache);
   - verify with two real accounts: offer, accept, share, adopt.
3. Declare the new terms version to the stack. The operator approves the
   text first.
4. Push the website: terms, privacy, pricing, docs.

## Calls made without asking (for the operator to check)

- Transfers and shares need the recipient's acceptance. Without it, anyone
  could plant sites in someone else's account.
- The accept-by date is at most 1 year ahead.
- Invitations lapse after 30 days. A site has at most 10 admins.
- A site is in at most one open offer, and a bundle holds at most 50 sites.
- Accepting a bundle is all-or-nothing.
- At most 20 offers and invitations per account per day.
- An admin may do everything except delete, set the expiry, rotate the edit
  password, transfer and share.
- **Builder check:**
  - A host travels whole. An offer that would split it suggests adding the
    sender's other sites on that host to the bundle (bundle transfer at the
    operator's request).
  - Form recipients survive a transfer, and are shown at every step.
  - Mail goes only to existing, verified accounts.
  - Deleting an admin account does not block on locked sites; the evidence
    stays in the site's provenance.
  - Accounts mode without tiers lets everyone share.
  - Operator-owned sites cannot be shared.
  - API and MCP `tags` mean the caller's own tags.

(Not a call but the operator's decision: Free can never accept a transfer,
because a paid plan must be booked first.)
- An admin's own API token and MCP connection reach the shared site.
- After a transfer, the old owner's tags, shares, tokens and edit password
  are gone. The recipient gets a new password.
- Adoption keeps the drop's edit password.
- An admin's tags for a shared site live in the admin's own account.
- Mails are plain text. Without an account mailer, there is only the
  dashboard.
- In v1 transfers and invitations are dashboard-only: no API or MCP tools.
