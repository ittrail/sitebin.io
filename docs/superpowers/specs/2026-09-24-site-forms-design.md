# Site forms: form submissions by email, for the sites Sitebin hosts

2026-09-24. Asked for by the operator ("ich würde gerne eine sitebin funktion
einbauen um zb statischen seiten die möglichkeit zur übermittlung von
formularen … gerne als opensource feature, aber in der saas erst ab pro
verfügbar … ich stelle es mir vom using ein wenig wie formbee vor"). The rules
below were agreed in chat the same day, section by section. Four things were
added or changed there:

- the forms mailer gets its **own** SMTP settings, separate from the account
  mailer ("diese 2 versandsysteme haben nichts miteinander zu tun");
- the plan cap is **per site**, not per account (Pro 1, Studio 10);
- uploads are forwarded as **attachments** (5 files of 2 MiB, both env-set);
- every mail carries a **machine-readable JSON** attachment.

"If we can do without JavaScript, that is fine" was also said, and it shaped
the design: a form works as plain HTML, and only the captcha needs a script.

The decisions the conversation did not settle are listed under **Decisions
taken without asking**.

## What the customer gets

A site owner adds a form on the edit page (or through the API or MCP): a
name, a recipient address, and three switches — captcha, attachments, a
thank-you page. The recipient gets one email asking them to confirm. From
then on the form's snippet works on the site:

```html
<form action="/_sitebin/forms/k7f3m2q9xaw4npd6" method="post">
  <input name="name" required>
  <input name="email" type="email" required>
  <textarea name="message"></textarea>
  <input name="_gotcha" tabindex="-1" autocomplete="off" aria-hidden="true"
         style="position:absolute;left:-9999px">
  <altcha-widget challenge="/_sitebin/forms/k7f3m2q9xaw4npd6/challenge"></altcha-widget>
  <button>Send</button>
</form>
<script type="module" src="/_sitebin/altcha.js"></script>
```

The two captcha lines appear only when the form has the captcha on. A form
with attachments adds `enctype="multipart/form-data"` and a file input. The
honeypot is moved off-screen rather than marked `hidden`, because form bots
skip hidden inputs and fill visible-looking ones. The server builds the
snippet from the form's settings (`forms.Snippet`), and the API and MCP
return it with every form, so the edit page and an agent paste the same
thing.

Each submission arrives as one email: from the form's name, replying to the
submitter, with the fields laid out in the order the form has them, the
uploaded files attached, and `submission.json` attached for machines.

Forms work only on sites Sitebin hosts: the view host, custom domains, and
path views. They are not a general form backend for sites hosted elsewhere.

## Why the endpoint lives on the site's own origin

Caddy already sends `/_sitebin/*` on **every content origin** — the view
wildcard and every custom domain — straight to the backend, without the authz
subrequest (`writeContentRoutes`; the unlock form and CSP reports use it). A
form posting to `/_sitebin/forms/<key>` is therefore same-origin with the page
it sits on:

- no CORS, and no JavaScript needed to submit;
- the site is known from the `Host`, so a key works only on the site it
  belongs to. Copying a key into another site's page finds nothing;
- the key needs no instance-wide index. It is looked up in the site's own
  `meta.json`.

The key is **not a secret**. It is in the page's HTML for anyone to read. What
protects a form is that its recipient is fixed server-side and has consented,
plus the rate limits, the honeypot and the captcha.

## Rules

### Forms and their keys

- A site holds an ordered list of forms in `meta.json` (`forms`), in creation
  order. The order matters: it decides which forms stay active when a
  downgrade pauses some (see Gating).
- **Key:** 16 characters from `internal/ids`' lowercase base32 alphabet,
  unique within the site. Immutable.
- **Name:** 1–60 characters, plain text. Control characters (CR and LF
  included) are refused, not stripped: a name is also the `From` display name,
  and a stripped name would silently differ from what the owner typed.
- **Recipient:** exactly one address, parsed with `net/mail.ParseAddress`. A
  display name is refused; only the bare address is stored.
- **Redirect** (optional): the thank-you page, a path on the **same site**. It
  must start with `/`, must not start with `//`, must hold no `\`, no scheme
  and no control characters, and is at most 512 characters. Anything else is
  refused when it is set, so the endpoint can never become an open redirect.
- **Captcha**, **files**: booleans, both off by default.

### States

| State | Meaning | Submissions |
|---|---|---|
| `pending` | created, or recipient changed; waiting for the recipient to confirm | refused, 403 |
| `active` | recipient confirmed | accepted |
| `stopped` | the recipient used the stop link | refused, 403 |

Plus one **computed** state, never stored: **paused** — the form's position in
the list is at or beyond the site's forms cap. Its stored state is kept and
comes back when the cap allows it again.

A form's `seq` counter is bumped when its recipient changes and when it is
stopped, and at no other time. That is what kills earlier confirmation links. Confirming does
not bump it, so a second click on the same link still lands on the success
page.

### Recipient consent

**Confirming.**

1. Creating a form, changing its recipient, and "resend confirmation" each
   send the recipient a confirmation email, through the forms mailer, in the
   same design as a submission. Its `From` display name is `Sitebin`, never
   the form's name, so the person creating the form does not control who the
   mail appears to come from. "Resend" on a `stopped` form moves it back to
   `pending`. On an `active` form it is refused (409), because there is
   nothing to confirm. It says which site (its public host) wants to
   send which form's submissions to this address. It has one button, and it
   tells anyone not expecting it to ignore the mail.
2. The link goes to `https://<base domain>/forms/confirm?t=<token>`. That is
   the app's domain, never user content.
3. **GET only shows a page with a button. The POST it submits confirms.**
   Outlook Safe Links and corporate mail scanners fetch every link in a mail;
   a GET that confirmed would confirm every form whose recipient's mail is
   scanned.
4. The token is an `auth.TokenSigner` token with purpose `forms:confirm`, valid
   for 7 days. Its subject binds the site, the form key, the recipient address
   and the form's `seq`. A recipient change or a stop therefore kills every
   earlier link, and a link from before a stop can never re-activate the form.
5. Confirming an already-active form is a success page, not an error.

**Stopping.**

- Every forwarded submission carries a stop link: "Stop emails from this form".
  It also carries `List-Unsubscribe` and `List-Unsubscribe-Post:
  List-Unsubscribe=One-Click` (RFC 8058), so Gmail and Outlook offer their own
  unsubscribe button.
- The link goes to `https://<base domain>/forms/stop?t=<token>`. GET shows a
  page with a button, and its POST stops the form. The RFC 8058 one-click POST
  to the same URL stops it directly.
- The stop token has purpose `forms:stop`. It binds the site, the form key and
  the address, **but not `seq`**, and it has a 100-year TTL. A stop link in a
  months-old mail must still work. The signer has no "never expires" mode, and
  it does not need one.
- A stop link whose address is no longer the form's recipient changes nothing
  and shows the same success page. That address no longer receives this form
  anyway.
- A stopped form's owner sees "Stopped by recipient" and can resend the
  confirmation. Only the recipient's own click reactivates it.

**Confirmation mails must not become a spam channel.** The only free text the
person creating a form controls in the confirmation mail is the form name: 60
plain-text characters. The host it names is the site's view host or a domain
already verified for it. The mails are throttled in memory: **at most 10 per site per day, and 3 per
address per day** across the instance. Over either limit the API answers
**429**, with the wait in the message.

### Submitting

`POST /_sitebin/forms/{key}` on the site's host.

**Finding the site.** `siteByHost(r.Host)` resolves it, as the unlock endpoint
does. On a path-view instance (`SITEBIN_VIEW_ACCESS=path|both`) the page lives
on the main domain, so every `/_sitebin/forms/…` URL there carries the site as
`?_site=<view id>`: the form's action, the challenge URL, and the default
thank-you page. The snippet adds it. It is a query parameter rather than a
body field so the site is known before the body is read, which keeps the
check order below. `_site` is honoured only on the main domain; on a site's
own host it is ignored, so it can never point a key at another site. A
configured thank-you path is prefixed with `/v/<view id>` there.

**Checks, in order — cheap before expensive:**

1. The site is found, and the key is in its `forms` → else **404**. Forms not
   enabled on the instance (no `SITEBIN_FORMS_SMTP_HOST`) → **404**.
2. The site has expired → **410**. The form is `pending`, `stopped`, or paused
   → **403**, and the message says which one.
3. Rate limits, per client IP (`auth.ClientIP`) and per form → **429**.
   Challenges are not counted here. They have their own per-IP bucket, three
   times the size, so a captcha form's challenge does not halve how often a
   person may submit.
4. The body is read under a hard cap: `max_files × max_file_bytes + 256 KiB`.
   Over it → **413**.
5. **Honeypot:** `_gotcha` is non-empty → answer exactly as a success would, and
   send nothing.
6. **Captcha** (form has it on): verify the `altcha` field (see Captcha) → **403**
   on failure.
7. **Fields:** at most 50, each value at most 10,000 characters. Invalid UTF-8
   is replaced with U+FFFD rather than refused. Fields named `altcha` or
   starting with `_` are control fields and are never forwarded. At least one
   forwarded field must be non-empty → else **400**.
8. **Files** (the form has files on; otherwise any non-empty file part is **400**): at
   most `SITEBIN_FORMS_MAX_FILES`, each at most `SITEBIN_FORMS_MAX_FILE_BYTES`.
   Empty file inputs (no filename, zero bytes) are ignored. Refused
   extensions: `.exe .com .bat .cmd .scr .pif .msi .msp .jar .js .jse .vbs .vbe
   .wsf .wsh .ps1 .psm1 .hta .cpl .lnk .reg .dll .app .apk` (case-insensitive,
   the last extension of the name) → **400**. The big mailbox providers reject
   the whole mail over these, so accepting them would only turn into a 502.
9. **Send**, synchronously, 30-second deadline covering dial to QUIT. On
   failure → **502**, so the person can try again. Nothing is queued, since
   nothing is stored.

**Parsing keeps the form's order.** `r.ParseForm` and `ParseMultipartForm`
collect fields into maps and lose it. `internal/forms` reads
`application/x-www-form-urlencoded` by splitting the body on `&` itself, and
`multipart/form-data` part by part with `multipart.Reader`, counting bytes as
it goes. Repeated names (a checkbox group) stay repeated. Any other content
type → **415**.

**Answer.**

- `Accept` includes `application/json` → a JSON body: `{"ok":true}`, or
  `{"error":"…"}` with the status above. This is for anyone who submits with
  their own `fetch`.
- Otherwise, success → **303** to the form's redirect path, or else to
  `/_sitebin/forms/{key}/thanks`. That is a small Sitebin-styled page on the
  site's own origin with a "Back" link to the same-host `Referer` path, or `/`
  without one. Failure → a small HTML page with the reason and a "Back" link,
  with the status above.

### The email

**Headers.**

- `From: "<form name>" <SITEBIN_FORMS_SMTP_FROM>`. The display name is
  RFC 2047-encoded (`mime.QEncoding`). The address is always the instance's,
  because SPF, DKIM and DMARC are the instance's.
- `To:` the confirmed recipient.
- `Reply-To:` the submitted `email` field, when it parses as exactly one
  address. Otherwise no `Reply-To`.
- `Subject:` the `_subject` field (control characters removed, 200 characters
  at most), else `New message via <form name>`. RFC 2047-encoded.
- `Date`, a `Message-ID` on the `From` address's domain, `MIME-Version`,
  `List-Unsubscribe`, `List-Unsubscribe-Post`, and `X-Sitebin-Site: <view id>`
  for tracing abuse.
- Every header value that came from a person passes one function that refuses
  CR and LF, and a test feeds it each of them.

**Structure.**

```
multipart/mixed
├── multipart/alternative
│   ├── text/plain; charset=utf-8     (quoted-printable)
│   └── text/html; charset=utf-8      (quoted-printable)
├── <uploaded files …>                (base64, filename RFC 2231-encoded)
└── submission.json                   (application/json, base64)
```

**HTML part.** This uses the claim-ticket design language of the product and
the website — ivory, slate, amber, mono labels, dashed ticket border, an
uppercase stamp — built the way mail clients need it:

- tables for layout and inline `style` attributes only, because Gmail and
  Outlook drop `<style>` blocks in several of their clients;
- a **light** card on a light background. Clients that force dark mode then
  invert a light design legibly, whereas a dark design often ends up
  unreadable;
- system font stacks with a monospace fallback, and no web fonts or remote
  images, so nothing loads from anywhere and there is no tracking pixel;
- the layout: a header with the form name as the amber stamp and the site host,
  then one row per field (mono uppercase label, value underneath) with line
  breaks kept, then a list of attachments with sizes, then a footer with the
  time (UTC), the site and the stop link;
- rendered through `html/template`, so every value is escaped.

The layout is agreed on a rendered preview before it is merged (see Testing).

**Text part.** It is built in Go code rather than from a template, because
`text/template` whitespace control makes an exact plain-text layout fragile.
It has the same content, readable as plain text. `Label: value` per
line, and a multi-line value goes under its label, indented. Then the
attachments, the site, the time, and the stop link.

**`submission.json`.** It is always attached and does not count against the
file limit.

```json
{
  "version": 1,
  "form": { "key": "k7f3m2q9xaw4npd6", "name": "Contact" },
  "site": { "id": "<view id>", "host": "www.example.com" },
  "submitted_at": "2026-09-24T10:15:00Z",
  "fields": [
    { "name": "name",   "value": "Anna Muster" },
    { "name": "email",  "value": "anna@example.com" },
    { "name": "topics", "value": "Hosting" },
    { "name": "topics", "value": "Domains" }
  ],
  "files": [
    { "field": "cv", "filename": "cv.pdf", "content_type": "application/pdf",
      "size": 183244, "sha256": "…" }
  ]
}
```

`fields` is an array so that order and repeated names survive. `host` is the
host the form was submitted on. The client IP is deliberately left out.

**Language:** English, like the account mails.

**Logging:** a submission is logged as site, key, status, size and number of
files. **Never** the field values, the filenames or the recipient.

### Captcha (ALTCHA v2)

- **Server:** `github.com/altcha-org/altcha-lib-go/v2` (MIT), which implements
  the current KDF-based protocol (PBKDF2/SHA-256). It is pinned as
  `v2.0.0-20260923082747-352eeeca913a`, the commit tagged `v2/v2.2.0`. The tag
  is named so that Go cannot resolve it, hence the pseudo-version. The module
  zip carries no licence file, so the attribution ships in
  `web/vendor/altcha.LICENSE`. The HMAC keys (challenge signature and key
  signature) are derived from the instance secret under their own purpose
  labels, never the raw secret.
- **Mode:** deterministic. The server picks the counter (a random number from
  1000 to 1999) at cost 1000 and signs the derived key, so the work is
  predictable and verifying is one HMAC. Measured natively, that is about
  0.1 ms per derivation, so about 0.2 s single-threaded; the widget spreads
  it over up to four workers. Two library traps are closed in code:
  `DeriveKey` is always passed, because without it `VerifySolution` accepts
  on the signature alone, and replays are refused by us, because the library
  does not track them.
- **Challenge:** `GET /_sitebin/forms/{key}/challenge` returns a fresh
  challenge, but only for a form that exists, is active, is not paused, and has
  captcha on; anything else is 404. It is rate-limited per IP in its own bucket
  (see Submitting). It expires after **5 minutes**.
- **Binding:** the challenge's `data` (`{"site": <view id>, "form": <key>}`,
  ASCII only, because the widget encodes the payload with `btoa`) is covered
  by the signature. A solution for one form is refused on every other.
- **Replay:** a verified challenge is remembered in memory until it expires,
  and a second use is refused. A restart forgets them, which allows at most
  one extra use of a challenge that is at most 5 minutes old. That is
  accepted.
- **Widget:** the ALTCHA widget (MIT) is vendored at `altcha@3.2.3`
  (`dist/main/altcha.min.js`, 115,652 bytes, sha256
  `102bb89eb6ee4556068e2514880b7755495b23d90438c751809cb4f0ecbd4efb`), the
  release line that speaks protocol v2. Its element takes `challenge="<url>"`
  (v3 dropped `challengeurl`) and submits the field `altcha`. It starts its
  workers from `blob:` URLs and injects a `<style>`; a site with its own
  strict CSP needs `worker-src blob:`. The docs page says so. It is vendored as `web/vendor/altcha.min.js`
  next to the other vendored libraries, with its licence alongside as
  `web/vendor/altcha.LICENSE`, and served at `/_sitebin/altcha.js`. Nothing
  loads from a third party at runtime. Whoever updates one of the two pins
  must check the other.
- **Cost:** a constant, not configuration (see Mode). The rollout checks the
  solve time on a real phone; if it is well over a second, the constant
  comes down.

## Gating and limits

**The per-site cap is stamped, like `custom_domains`.** A submission reads
only `meta.json`. It never asks the extension, which is the core's rule for
anything on a hot path.

- `eeconfig.Tier` gets `max_forms` (`int`, `omitempty`). **0 or absent means
  none**, the same polarity as `custom_domains`, `max_containers` and
  `max_zones`, because a free tier that forgets the field must not send mail.
- `ext.CreateGrant` gets `MaxForms *int`. `grantFromTier` always sets it, so
  in tiers mode every new site is stamped with an explicit value. Accounts
  mode leaves it nil, as it leaves every other cap.
- `store.Meta` gets `QuotaForms *int` (`quota_forms`, `omitempty`), and
  `store.Quota` gets `Forms *int`. `Store.ApplyQuota` writes it.
- **Every constructor of `store.Quota` passes `Forms`:** `quotaFromGrant` in
  httpapi, and `reconcile` in the cleanup sweep. `ApplyQuota` writes every
  field it is given, so a caller that left `Forms` out would reset the cap to
  nil on every expired-site reconcile. A test covers each caller.

**What a site's cap is.**

1. `quota_forms` is set → that value.
2. Unset, the site is **owned**, a provider is registered, and a form is being
   **added or listed** (edit page, API, MCP) → ask `QuotaFor(owner)` once and
   stamp its `MaxForms` if it is non-nil. This is how sites created before
   this feature get their plan's value; without the listing half, a Pro site
   from before would read "not in your plan" on its edit page until someone
   tried to add a form anyway. Neither is a hot path. An error from
   `QuotaFor` refuses an add without stamping anything; a listing then shows
   the instance value and tries again next time.
3. Otherwise → the instance value `SITEBIN_FORMS_MAX_PER_SITE`. Its default is
   **10 with no provider and 0 with one**. Without that, every existing Drop
   and Free site on a tiers instance would get 10 forms the moment this ships.
   An accounts-mode EE operator who wants forms sets the variable.

**Where the cap is enforced.**

- **Adding** a form when the site already holds `cap` forms → **403**, with
  "this site's plan allows N form(s)".
- **Submitting** to a form whose position is `>= cap` → **403**, paused.
  Nothing is ever deleted over a cap. A downgrade pauses the newest forms, and
  an upgrade resumes them.

A tier change reaches existing sites through the existing path: the tier sync
calls `SiteService.ApplyQuota`, which now carries `MaxForms`.

**Hosted values:** Drop 0, Free 0, Pro 1, Studio 10, unlimited/admin 100.

**CSP needs no change.** Trusted tiers have no `form-action` restriction.
Untrusted ones (Drop, Free) keep `form-action 'none'` and have no forms
anyway. The community build marks every site trusted.

**`SITEBIN_READONLY`** is untouched: it disables creating sites, and only
that, as before. Forms are a site's settings, like its domains and passwords,
and stay editable; submissions write nothing at all.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `SITEBIN_FORMS_SMTP_HOST` | — | Enables forms. Unset: no form routes answer, and the API and edit page say forms are off. |
| `SITEBIN_FORMS_SMTP_PORT` | `587` | |
| `SITEBIN_FORMS_SMTP_USER` / `_PASS` | — | Optional SMTP AUTH (PLAIN). |
| `SITEBIN_FORMS_SMTP_FROM` | — | **Required** with `_HOST`. A bare address; a display name is a startup error. |
| `SITEBIN_FORMS_SMTP_TLS` | `false` | Implicit TLS (port 465). Otherwise STARTTLS whenever the server offers it. |
| `SITEBIN_FORMS_MAX_PER_SITE` | 10 without a provider, 0 with one | The cap for sites with no stamped value. |
| `SITEBIN_FORMS_MAX_FILES` | `5` | Files per submission. `0` disables attachments instance-wide, and a form's `files` switch is then refused (400). |
| `SITEBIN_FORMS_MAX_FILE_BYTES` | `2097152` | Bytes per file. |
| `SITEBIN_FORMS_PER_IP_HOUR` | `10` | Submissions per client IP per hour, across all forms. Challenges get three times this, in their own bucket. |
| `SITEBIN_FORMS_PER_FORM_HOUR` | `60` | Submissions per form per hour. |

These are entirely separate from `SITEBIN_SMTP_*`, the enterprise account
mailer: different package, different licence, different credentials. An
instance can run either one, both, or neither.

## Where it lives

All of it is in the MIT core except the tier field.

- **`internal/forms`** (new): no HTTP handlers and no store access, so it can
  be tested alone.
  - the form rules (name, recipient, redirect validation);
  - the ordered body parser;
  - message building (headers, MIME tree, templates, `submission.json`);
  - SMTP delivery (its own; the core cannot import `ee/smtp`, and it has a
    deadline that `smtp.SendMail` lacks);
  - the ALTCHA wrapper and the replay memory;
  - the rate limiters and the confirmation throttle;
  - token subjects for confirm and stop.
  - The HTML and text templates are embedded.
- **`internal/store/forms.go`:** the `Form` record, and add, update, delete and
  state changes under the site lock. Like `DomainClaim`, the record lives in
  the store package. `internal/forms` holds no persisted type.
- **`internal/config`:** the variables above.
- **`internal/httpapi`:**
  - `forms.go`: the public `/_sitebin/forms/…` routes and `/_sitebin/altcha.js`,
    plus `/forms/confirm` and `/forms/stop` on the main domain;
  - `formsapi.go`: the JSON API;
  - `mcpops.go`: the adapter methods.
- **`internal/mcp`:** five tools.
- **`internal/cleanup`:** `reconcile` passes `Forms`.
- **`ee/`:** `eeconfig.Tier.MaxForms`, and `grantFromTier` sets `MaxForms`.
  Nothing else.
- **`web/static`:** the Forms section of `edit.html` and `edit.js`.
- **`web/vendor`:** the vendored widget and its licence.
- **`internal/caddygen`:** nothing. `/_sitebin/*` is already proxied on every
  content origin, and the main domain is all backend.

### JSON API

The auth is the same as every site endpoint (`withEditAuth`): the edit
password, or an account credential on a site that account owns.

| | |
|---|---|
| `GET /api/sites/{editID}/forms` | `{"forms":[…], "limit":N, "used":N, "enabled":bool}` |
| `POST /api/sites/{editID}/forms` | `{name, recipient, captcha?, files?, redirect?}` → 201 and the form, `pending`. The confirmation mail is sent. |
| `PUT /api/sites/{editID}/forms/{key}` | Partial update. A new recipient means `pending`, `seq+1` and a new confirmation mail. |
| `DELETE /api/sites/{editID}/forms/{key}` | 204 |
| `POST /api/sites/{editID}/forms/{key}/confirmation` | Resend, throttled → 202 |

A form in a response has `key`, `name`, `recipient`, `captcha`, `files`,
`redirect`, `status` (the computed one, so `paused` appears here),
`created_at`, `confirmed_at`, `stopped_at`, and `snippet`. Tokens never
appear.

Forms off on the instance → **409** "forms are not enabled on this instance"
on the writes. The `GET` answers with `enabled:false`.

### MCP tools

These are additive, so no saved connector configuration breaks. They are named
like `add_domain`/`remove_domain`, take `edit_id` like every site tool, and use
the same read and write scopes.

- `list_forms`: read-only
- `add_form`
- `update_form`
- `remove_form`: marked destructive
- `resend_form_confirmation`

The descriptions say that a new form stays inactive until its recipient
confirms by email. An agent must not report a form as working when it is not.

### Edit page

A "Forms" section, hidden when the instance has forms off, and showing "Not
available on this site's plan" when the cap is 0. It contains:

- a list with status badges: Awaiting confirmation, Active, Stopped by
  recipient, Paused — plan limit;
- adding and editing a form (name, recipient, captcha, attachments, thank-you
  page);
- **Copy snippet**, built from the form's settings (and `_site` on path-view
  instances);
- resend confirmation, and delete.

## Safeguards

| Abuse | Answer |
|---|---|
| The instance as a spam relay to arbitrary addresses | The recipient must confirm. Confirmation mails are throttled per site and per address, and their only free text is a 60-character name. |
| A form flooded by bots | Honeypot, optional captcha, per-IP and per-form limits. |
| Spoofed senders | `From` is always the instance's address. The submitter's address is only a validated `Reply-To`. |
| Header injection | Every header value from a person refuses CR and LF, and the display name and subject are RFC 2047-encoded. |
| Malware by mail | Attachments are off per form by default. Executable types are refused, sizes are capped. |
| Open redirect | The thank-you page is a same-site path, validated when it is set. |
| Mail scanners confirming or stopping forms | GET only shows a button. Only the RFC 8058 one-click POST acts without one, and it can only stop, never confirm. |
| Content leaks | Submissions are never stored and never logged. |

## Website and docs (ship after the instance runs it)

In `Sitebin-Website`:

- **Pricing:** "1 form per site" on Pro and "10 forms per site" on Studio, a
  "Forms (per site)" row in the comparison table (— for Drop and Free), and a
  FAQ entry.
- **`/docs/forms/`** (new): the snippet, the field conventions (`email`,
  `_subject`, `_gotcha`, `_site`), captcha, attachments, the
  `submission.json` schema, HTML and JSON answers, confirm and stop, limits.
- **`/docs/mcp/`:** the five tools in the catalog.
- **`/docs/api/`:** the endpoints.
- **`/docs/configuration/`:** the `SITEBIN_FORMS_*` variables.
- **Privacy policy:** hosted Sitebin now passes form data through to site
  owners, as their processor. A section is drafted with the rest of the
  website changes. The operator has it reviewed legally before it is
  published; it ships with the other website changes, not ahead of them.

## Testing

Tests come first, and both build tags run.

**`internal/forms`:**
- the parser keeps order and repeated names, for urlencoded and multipart
  alike;
- the size, count and length caps;
- invalid UTF-8;
- control fields are dropped;
- the honeypot;
- refused extensions, including upper case and double extensions;
- the built message is parsed back with `net/mail` and `mime/multipart`: the
  tree shape, the parts, the attachments and `submission.json` all round-trip;
- a value with markup comes out escaped in HTML and verbatim in text;
- CR and LF in the name, the subject, a filename and the `email` field;
- `Reply-To` with 0, 1 and 2 addresses;
- ALTCHA: a valid solution, an expired one, one for another form, one for
  another site, a replay (the test solves challenges with the library's own
  solver);
- the rate limiters and the confirmation throttle, on an injected clock;
- tokens: `seq` binding, expiry, purpose separation, stop without `seq`.

**`internal/store`:** add, update and delete under the lock; `seq` bumps; the
paused computation against a cap; `ApplyQuota` writes `Forms`.

**`internal/cleanup`:** `reconcile` keeps `quota_forms`.

**`internal/httpapi`** (fake mailer):
- every status in the check order;
- 303 versus JSON answers;
- the path-view `_site` field;
- a key used on the wrong host is a 404;
- confirm and stop: GET changes nothing, POST does, a stale `seq` is
  refused, the one-click POST works;
- API CRUD and its caps;
- the three cap rules: stamped value, add-time lookup, instance default with
  and without a provider, including "a provider plus nil means 0";
- forms off on the instance;
- the MCP tools through `mcpops`.

**`ee`:** `grantFromTier` carries `max_forms`. A downgrade pauses the newest
forms and an upgrade resumes them.

**E2E:**
- `e2e/forms.ps1` (community image, pure ASCII) runs against a Mailpit
  container as the SMTP server. Its HTTP API returns what it received. The
  script covers create, confirm, submit with an attachment, the delivered MIME
  and `submission.json`, stop, and the refusal afterwards.
- `e2e/tiers.ps1` gains the per-site caps (Free 0, Pro 1).
- The captcha is covered in Go, where the library can solve challenges.

**Mail preview:** a `go run ./internal/forms/preview` tool writes sample
submission and confirmation mails as `.html` and `.eml`. The operator
approves the look before the merge, and the samples are opened in at least
Gmail (web), Outlook and Apple Mail.

## Rollout

The workspace ship order applies:

1. Merge and push the product repo.
2. On `app.sitebin.io`:
   - add `max_forms` to `/opt/sitebin/tiers.json` (Pro 1, Studio 10,
     unlimited/admin 100);
   - set the `SITEBIN_FORMS_SMTP_*` variables. The operator provides the
     sender address and the account, and SPF and DKIM for that domain must
     cover the SMTP host;
   - restart, then verify live: a form on a Pro site, confirm, submit,
     receive, stop.
3. Push the website repo.

## Corrections (post-implementation)

The final whole-branch review changed these rules after the sections above
were written. The sections above are left as they were; where they disagree
with this block, this block is what the code does.

- **Forms need a trusted site when accounts are enabled.** With a provider
  registered and accounts enabled, `formsLimit` answers 0 for a site without
  the trust marker, whatever `quota_forms` says. An add is refused with 403
  ("this site's plan includes no forms"), a submission with 403 (paused), a
  challenge with 404, and the listing shows limit 0, so the edit page says
  "not included". Why: an untrusted site is served with `form-action 'none';
  connect-src 'self'`. A plain HTML form there cannot post at all, so the only
  thing a form would still serve is a phishing drop's own `fetch` (with
  `Accept: application/json`) to its confirmed form. "CSP needs no change"
  above assumed every tier with forms is trusted, and now the code enforces
  that instead of assuming it. The check stats the marker and never asks the
  extension, so it is allowed on the submission path. The community build is
  unchanged, because it marks every site trusted at creation. The hosted Pro
  and Studio tiers are trusted. **The rollout must confirm `"trusted": true`
  for every tier with `max_forms > 0`** in `/opt/sitebin/tiers.json`,
  including the unlimited/admin tier. Otherwise that tier's sites get no forms.
- **Confirmation throttles also apply per caller and instance-wide.** On top of
  10 per site and 3 per address per day, confirmation mails are limited to
  **20 per caller IP per day** (the client IP of the API or MCP request) and
  **500 per day across the instance**. These are constants, like the other
  two. The per-address key is lowercased, and a `+tag` in the local part is
  dropped (`a+x@example.com` counts as `a@example.com`). Why: the per-site
  and per-address budgets multiplied with the number of sites one person can
  create, and `victim+N@` gave every variant a fresh budget. The 429 no longer
  says which throttle refused.
- **The per-form bucket is charged last.** The per-IP bucket stays at step 3,
  before the body is read, because cheap checks come before expensive ones.
  The per-form bucket is charged only after the honeypot, captcha and content
  checks, just before the mail is built. A honeypot hit still answers like a
  success and spends only the per-IP bucket. Why: when it was charged at step
  3, bots from many addresses could use up a form's hourly budget with posts
  that were never going to be mailed, which locked real visitors out.
- **Small hardening:**
  - A submission is refused unless the form's stored status is `active` (403
    "This form is not active."). The specific pending and stopped messages
    stay. This fails closed on a status this binary does not know.
  - In a field's label, control characters become spaces, and the label is
    capped at 100 runes. Otherwise a field name could put lines of its own,
    such as a fake stop link, into the text part.
  - An attachment's filename is capped at 150 UTF-8 bytes rather than 200
    runes. The tail is kept, cut at a rune boundary, so the extension survives.
    Why: RFC 2231 writes every non-ASCII byte as `%XX` into a header line that
    nothing folds, and SMTP refuses lines over 998 octets.
  - With `SITEBIN_FORMS_MAX_PER_SITE` unset, the default is 0 only when a
    provider is registered **and** accounts are enabled. An enterprise binary
    in open mode behaves like the community build and gets 10. Rule 3 of "What
    a site's cap is" and the configuration table above say "0 with a provider".
- **The body cap is `fileRoom + 256 KiB + 64 KiB`**, not
  `max_files × max_file_bytes + 256 KiB` (step 4 above). `fileRoom` is
  `max_files × max_file_bytes` for a form with attachments on, and 0 when they
  are off. The 64 KiB is room for multipart headers and boundaries. Why: a
  form without attachments has no reason to accept megabytes, and the
  multipart framing needs room of its own.
- **Using a stop link for a site or form that no longer exists answers 200
  "Stopped"**, whether through the page's button or the RFC 8058 one-click
  POST. The goal, no more mail, is already met, and one-click senders expect
  a 2xx. Only confirm links answer 410 when used on a deleted form. (Opening
  such a stop link with a GET still shows the "This form no longer exists"
  page.)
- **`deleteForm` answers 409 with forms off**, like every other write. It
  does not delete.
- **`CleanName` refuses a control character anywhere**, including at the
  edges. It checks before trimming, so trimming cannot remove one first.
- **The rate limiters and throttles live in `internal/httpapi`** on
  `auth.Limiter`, not in `internal/forms` as "Where it lives" lists. They key
  on the client IP, the site and the caller, which are HTTP and store
  concerns.
- **`e2e/tiers.ps1` tests free = 1 form on a trusted tier**, not "Free 0,
  Pro 1". The script runs one tier, and what it has to prove is that the cap
  is enforced. The tier is trusted because forms now need that.
- **Rollback caveat.** A binary older than this feature does not know `forms`
  or `quota_forms`, and drops both on its next `meta.json` write. Once
  customers have forms, do not roll back past this feature, or restore
  `meta.json` from the pre-rollback backup.
- **The captcha solution is released on every refusal after it, not just spent
  before sending.** `Captcha.Release` undoes exactly the spend `Verify`
  recorded; `submitForm` calls it before answering the empty-form 400, the
  per-form 429, a mail-build 500 and an SMTP 502 — every check that still runs
  after the captcha and can refuse a submission whose solution already
  verified. A retry with the browser's re-posted `altcha` field then works
  without reloading for a fresh challenge, as long as the solution has not
  expired. Only a real send leaves the solution spent, so replaying it is
  still refused. This replaces the original v1 acceptance of the reload
  requirement, which the operator asked to fix once it was live.
- **No Reply-To when the submitter's "email" field shares the recipient's own
  domain.** `replyAddress` now also takes the recipient and compares the two
  addresses' domains (case-insensitive, the part after the last `@`); on a
  match it answers `""`, same as an invalid or multi-address field, and the
  "Reply to this email to answer the sender directly" hint disappears with it
  (it already keys off whether a Reply-To was produced). The submitted address
  is unaffected everywhere else — still an ordinary field in both mail parts
  and in `submission.json`. Why: a live test submission from `noreply@sitebin.io`
  to `office@ittrail.at` (Microsoft 365) carrying `Reply-To: office@ittrail.at`
  was quarantined as "Phishing / High confidence" (first contact, advanced
  filter) although SPF, DKIM and DMARC all passed. An external sender whose
  Reply-To points back into the recipient's own domain is the classic
  business-email-compromise pattern — and it is exactly what every site owner
  produces the first time they test their own form with their own address.
- **The submission mail is plain text — no HTML part at all.** This replaces
  the "HTML part" section above. Live tests to a Microsoft 365 mailbox on
  2026-09-24 (one change per mail, otherwise identical Sitebin output) put
  every submission in the claim-ticket look in Junk (`SCL:5`, `SFV:SPM`,
  `CAT:SPM`, `BCL:0`, IP not listed, SPF/DKIM/DMARC/compauth all pass), while
  the confirmation mail — same sender, same styling — got `SCL:1`. None of
  these alone changed the verdict: the JSON attachment, `List-Unsubscribe`,
  the random view host, the sender name, the language, the hidden preheader,
  the `font-size:0` spacers, the mono labels, `<title>` and viewport. Plainer
  HTML redesigns reached the inbox with a neutral message, but a bare white
  page, a stamp design, and the best-looking redesign (dark header, ivory
  card) went to **quarantine** as soon as the visitor wrote an ordinary
  request for a quote — and quarantine is the one outcome the recipient never
  sees. The same content as text only reached the inbox every time, with
  `submission.json` attached as well as without. For a contact form,
  arriving beats looking good, so the mail is text: `compose` writes the
  text part straight into `multipart/mixed`, the `submission.html` template
  is gone, and `TestSubmissionMailIsTextOnly` pins it. The confirmation
  mail keeps its ticket look, because it is delivered and a form cannot go
  live without it; it is deliberately left unchanged. The "Confirmed" page
  instead asks the recipient to add the sender address to their contacts or
  safe senders — the one moment they are certainly looking. The MCP
  `list_forms`/`add_form` descriptions say what arrives, so an agent sets the
  user's expectation. Tests were run against one mailbox; read them as "these
  combinations crossed the threshold", not as a rule set.
- **The attachment is `submission.json` again.** An intermediate change
  (`193b882`) renamed it to `submission.txt` because Outlook reported
  `submission.json` as a "potentially unsafe attachment". That diagnosis was
  wrong: Outlook blocks *every* attachment — `.txt` included — on a message in
  the Junk folder, and converts it to plain text with links disabled. In the
  inbox it is delivered with the mail. The same intermediate change fixed `compose`, and
  that fix stays: `mime.FormatMediaType` answers `""` for a type that already
  carries parameters, so every upload whose `mime.TypeByExtension` type has a
  `charset` (`.txt`, `.html`, `.css`, …) had gone out as
  `application/octet-stream`. Parameters are now parsed and `name` merged
  into them.

## Decisions taken without asking

- **The mail language is English**, like the account mails. This was offered in
  chat as the default and not objected to. A per-form language would be a later
  addition to the templates.
- **Consent is per form, not per (site, address).** A Studio site with ten
  forms to one address asks ten times. That is simpler to reason about, and
  stopping one form never silences another.
- **The stop link never expires and ignores `seq`**, so a stop link in an old
  mail keeps working. It cannot re-activate anything, so an old one is
  harmless.
- **Throttles:** 10 confirmation mails per site per day and 3 per address per
  day. These are constants, not configuration.
- **Submissions are sent synchronously and never queued.** With no storage, a
  queue would be the only copy of someone's message on our disk. A 502 the
  person can retry is more honest.
- **The instance default is 0 when a provider is registered.** Without that,
  every existing site on a tiers instance would get forms when the feature
  ships.
- **The path-view site field is `_site`,** not `site` like the unlock form's,
  because it shares a namespace with the customer's field names.
- **The captcha cost is a constant,** tuned once. Operators have no useful
  reason to tune proof-of-work.
- **Challenges and rate-limit counters live in memory.** A restart resets them,
  and the replay window that opens is at most 5 minutes.
- **The unlimited/admin tier gets `max_forms` 100,** because 0 means none.
  This is the zero-value trap the admin tier has hit before.
- **The client IP is not in `submission.json` or the mail.** The site owner
  gets what the person typed, and nothing the person did not know they were
  sending.
