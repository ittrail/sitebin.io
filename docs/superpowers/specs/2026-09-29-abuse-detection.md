# Abuse detection: tripwire, upload scanner, reports that reach a human

**Date:** 2026-09-29
**Status:** implemented
**Follows:** the phishing incident of 2026-09-25…28 (internal record in the
website repo, `docs/internal/2026-09-28-phishing-incident.md`), whose "P0"
proposal this is, and
[`2026-09-28-site-lock-and-account-suspension.md`](2026-09-28-site-lock-and-account-suspension.md),
whose lock it uses.

## Problem

Two phishing kits (a Telegram credential harvester, a UPI payment page) were
uploaded by fresh Google-login accounts; one minted an API token in its first
minute and re-uploaded the page hours after the takedown. The untrusted-tier
CSP stopped the exfiltration and its reports even named the Telegram bot —
into `stats.json`, where nobody looked. Abuse reports sat in `/data/reports`.
Nothing told the operator anything; the kits were found by chance.

## What ships

1. **CSP-report tripwire** — a CSP report naming a known exfiltration
   destination locks the site, if the site's own files reference it.
2. **Upload scanner** — every write is scanned for kit signatures before it
   becomes visible; a kit on an untrusted tier locks the site first.
3. **Reports reach a human** — a public report page, a mail per report, a
   Reports tab and scanner hits in the instance register.

Plus the plumbing all three share: plain-text alert mail with aggregation,
and `sitebin scan` for the sites already on disk.

## Where it lives, and why the core

Everything is **core (MIT)** except the register's pages. The lock is core
(`store.Meta.Locked`), CSP reports are core, every write path is core, the
mailer is the core forms mailer, and the one tier fact the policy needs —
trusted or not — is the trust marker the core stamps into the site folder.
The extension is asked only two optional questions, and only when a rule has
already hit: *is this owner the operator* (the existing
`store.SetOperatorCheck`, i.e. `ext.OperatorAccounts`) and *what is this
account's email* (new optional `ext.AccountDirectory`, for alert mails).
Nothing on the serving or upload hot path asks the extension; a clean upload
never does.

The community build therefore runs the scanner too. It has no accounts and
marks every site trusted, so it never auto-locks — it records, logs and (with
`SITEBIN_ABUSE_ALERTS_TO` and the forms mailer) mails; the operator locks with
`sitebin lock`.

- `internal/abuse` — rules (compiled defaults + the instance's rules file,
  hot-reloaded), the streaming matcher, destination matching. Pure logic.
- `internal/store/guard.go` — the policy and where it is applied: every
  write path, the tripwire check, the CLI scan, findings and the reviewed
  fingerprints in `meta.json`.
- `internal/httpapi` — CSP tripwire, alerts, report page, the surfaces'
  error mapping; WebDAV and FTP write through staged files.
- `ee` — the register: Reports tab, Flagged filter, findings on rows.

## Rules are data

Built-in defaults are compiled in. The instance may add to them or override
them with **`<data>/abuse-rules.json`** (`/opt/sitebin/data/abuse-rules.json`
on the host). The file is checked at most every 10 seconds, on use, by its
modification time; a file that does not parse or validate is logged at ERROR
and the **last good rules stay in force** (the defaults, if none was ever
good). A deleted file means the defaults again.

```json
{
  "defaults": "extend",
  "rules": [
    {"id": "new-kit-2026-10", "severity": "block",
     "all": ["type=\"password\"", "re:sendto(telegram|discord)"],
     "description": "..."},
    {"id": "obfuscation-charcode", "severity": "off"}
  ],
  "exfil": [
    {"id": "formspree", "url": "formspree.io", "action": "alert"}
  ]
}
```

- `defaults`: `"extend"` (default) merges by id — a file entry with a built-in
  id replaces it, `"off"` disables it; `"replace"` drops every built-in (an
  empty `rules` list is the scanner's kill switch).
- A **rule** matches a file when every `all` pattern occurs in it and, if
  `any` is non-empty, at least one `any` pattern does. That is how "a password
  input AND a cross-origin exfiltration reference" is expressed, so the plain
  login form of a legitimate app does not trip a rule that needs both.
  Patterns are case-insensitive substrings, or RE2 regular expressions when
  prefixed `re:`. `files` optionally limits a rule to extensions.
- Severity **block** locks (below); **flag** alerts only; **off** disables.
- An **exfil** entry is a tripwire destination: `url` is a host with an
  optional path prefix (`api.telegram.org/bot`), matched against the host and
  path of a CSP report's blocked URL; `action` is `lock`, `alert` or `off`.

### Starter rules

From the two kits (checked against the quarantined copies) and the usual
shapes. `pw` below is the password-field pattern
`re:type\s*[=:]\s*["']?password`.

| id | severity | matches |
|---|---|---|
| `telegram-bot-api` | block | `api.telegram.org/bot` |
| `telegram-sender` | block | `sendtotelegram` |
| `chat-webhook` | block | Discord / Slack webhook URLs |
| `botcheck-password` | block | `id="botcheck"` + pw |
| `clearbit-logo-password` | block | `logo.clearbit.com` + pw |
| `dns-lookup-password` | block | `dns.google/resolve` or `cloudflare-dns.com/dns-query` + pw |
| `ip-lookup-password` | block | ipify / ip-api / ipapi.co / ipinfo / db-ip + pw |
| `hash-email-password` | block (markup only) | `atob(` + `location.hash` + pw |
| `brand-login-title` | block | `<title>` naming Microsoft / Office 365 / Outlook / OneDrive / SharePoint / DocuSign / Adobe / WeTransfer / "Account Verification" + pw |
| `brand-login-text` | block | "sign in to your microsoft account" and similar lure sentences + pw |
| `wallet-deeplink` | block | `tez://upi`, `phonepe://`, `paytmmp://`, `gpay://` |
| `upi-deeplink` | flag | `upi://pay` (legitimate merchants use it too) |
| `ipfs-script` | flag | a `<script src>` on `ipfs.io` |
| `obfuscation-eval-atob` | flag | `eval(atob(` |
| `obfuscation-charcode` | flag | `String.fromCharCode(` with 40+ numbers |
| `obfuscation-unescape` | flag | `unescape("` + 60+ `%xx` |

Initial exfil destinations (all `lock`): `api.telegram.org/bot`,
`discord.com/api/webhooks`, `discordapp.com/api/webhooks`, `hooks.slack.com`,
`api.ipify.org`, `ipapi.co`, `ip-api.com`, `ipinfo.io`, `api.db-ip.com`,
`script.google.com/macros`, `formspree.io`, `getform.io`, `formsubmit.co`,
`submit-form.com`, `api.emailjs.com`, `webhook.site`, `pipedream.net`.

## What is scanned

Every file on every write path, as it is written, at most the first 8 MiB of
each (`abuse.MaxScanBytes`); known binary extensions (images, fonts, media,
archives, PDF, wasm) are skipped unread. NUL bytes are dropped before
matching — never taken to mean "binary, skip": a browser renders a page with
a NUL in a comment, and UTF-16 text is NULs between ASCII letters. A rule can
only **block** in an *active* file, one a browser renders or runs from a
static host: HTML, XHTML, SVG, any XML (an XHTML root runs script in any XML
type) or JavaScript — decided by extension and by the type the server's own
MIME table gives it (`mime.TypeByExtension`, which reads the container's
mailcap, as Caddy does). The same hit in a `.txt`, `.md`, `.json` or `.php`
(served as text or download here) is recorded as a flag. Caddy never sniffs a
content type, so an unknown extension is never rendered as HTML.

Known limits, accepted: a kit split so that no single file satisfies a
combined rule is caught only by its single-pattern rules and the tripwire; a
file padded past 8 MiB is scanned in its first 8 MiB; rename-to-evade across
extensions is handled (below), string-splitting obfuscation is not. While a
file streams it exists under a temp name (`<name>.sbtmp`, `<name>.sbtmp-<rand>`)
that has no known extension, so Caddy serves it without a content type and a
browser (told `nosniff`) never renders it as a page.

## The decision

For each file with hits, under the site lock:

1. A file whose SHA-256 is in the site's **reviewed** set at its current
   severity is not recorded at all: the operator has already looked at
   exactly this content. A content reviewed only as a flag (say, in a
   `.txt`) is still held as an active page.
2. Each hit becomes a **finding** (`rule`, `severity`, `path`, `excerpt`,
   `sha256`, `at`, `source`), recorded in `meta.json` (`abuse.findings`,
   at most 20, newest kept; one per rule and file).
3. The site is **held** — locked `By: "scanner"`, reason naming the rule and
   file — when a block finding exists and the site is *lockable*: not locked
   already, **no trust marker** (drop, free, anonymous; never pro / studio /
   unlimited / admin), **not a container site**, and **not the operator's**.
   Otherwise the finding is a flag. A scanner lock, like an account lock, is
   placed only on an unlocked site and never replaces a lock.
4. Every decision is logged at INFO with site, owner, rule, path and the
   decision (`held`, `flagged`, `exempt: trusted`, `exempt: container`,
   `exempt: operator`, `already locked`, `reviewed`), and handed to the alert
   hook.

The lock is **written before the file becomes visible**, so a held upload is
never served, not even for the moment between two syscalls:

- **API, MCP, upload tokens, zip** (`store.SaveFile`, `ExtractZip`): the file
  is written to its temp name, scanned as it streams, the verdict is applied
  to `meta.json`, and only then is it renamed into place — all under the site
  lock. authz reads the lock from `meta.json`, so the next request is a 410.
  The write returns `store.ErrHeld`; the rest of the upload is not written.
- **Replace** (`?replace=true`, `write_files` with `replace`): the staged
  files are scanned as they stage; the verdict is applied inside `Commit`,
  under the site lock, before the staged files move in. The kit is kept, as
  evidence, in a site no one is served.
- **WebDAV and FTP** write through `store.StagedFile`: the bytes go to a temp
  file beside the target, and `Close` scans it, applies the verdict and then
  renames. A rename (DAV `MOVE`, FTP `RNTO`) scans the source *as the
  destination* first — `kit.txt` renamed to `kit.html` becomes active.

**The caller** gets `403` with the lock message ("This site is locked by the
operator: held for review — …"), the MCP tool the same sentence, WebDAV a 403,
FTP the transfer error. A **creation** that is held still finishes its
bookkeeping — the site is stamped with its tier's lifetime and linked to its
owner (so the owner's dashboard shows it, locked) — and then answers `403`
without the edit password: the site exists only as evidence.

The **released** state is the reviewed set: when the operator unlocks a site
(register or `sitebin unlock`) or dismisses its findings in the register,
every finding's fingerprint moves into `abuse.reviewed` (at most 200) — the
bare `sha256` for a blocking finding, `sha256:flag` for a flag — and the
findings are cleared. The same bytes uploaded again are then not recorded and
never re-locked at that severity; any new content is scanned as usual. An
unsuspension (an account lock released) reviews nothing, and when an
unreviewed blocking finding was recorded while the account lock stood (the
tripwire, `sitebin scan --lock`), the account lock becomes a scanner lock
instead of being lifted.

**Evidence stays put.** A request that passed its gate before a hold landed
must not change the site after it: `SaveFile`, `ExtractZip` and `DeleteFile`
re-read the lock under the site lock, a staged commit and `RenameChecked`
refuse a locked site, and WebDAV and FTP check it before a delete or a new
folder (`store.CheckUnlocked`).

**Leaving container mode** scans the tree first (`ScanBeforeServing`): what
the containers wrote was never an upload, and from the switch on it is served
as files. The verdict is the upload guard's without the container exemption;
a hold stops the switch.

## The tripwire

`POST /_sitebin/csp-report` keeps counting every report into `stats.json`
exactly as before. Then, when the blocked URL matches an exfil destination:

1. The report's document URL (`document-uri` / `documentURL`), when it has
   one, must resolve to the same site as the report's `Host`; otherwise the
   report is not the site's and is ignored by the tripwire.
2. At most 6 checks per hour per site, whatever destinations its reports
   name, and 2 at a time.
3. **Verification against the site's own files**: the content is walked
   (regular files, 5000 files / 64 MiB at most, counting every byte read —
   a file that would overrun the budget is not read) for the destination's
   text.
   A report is unauthenticated, so this is what stops a forged report from
   locking an arbitrary site: only content that actually references the
   destination can be locked by it.
4. Referencing, unreviewed files become findings (`source: csp`, rule
   `csp:<id>`, the blocked URL as excerpt), and the site is held when the
   destination's action is `lock` and the site is lockable (as above — so a
   trusted site is alerted, never locked). No referencing file: the event is
   logged and alerted as unverified, nothing is recorded on the site.

## Alerts

`SITEBIN_ABUSE_ALERTS_TO` (comma-separated addresses; default: the
`SITEBIN_ADMIN_ACCOUNTS` addresses) receives **plain-text** mail — Microsoft
365 quarantines this server's HTML mail — through the forms mailer
(`SITEBIN_FORMS_SMTP_*`). Without that mailer alerts are logged only, and a
startup warning says so. Subjects start `[sitebin abuse]`. Every URL from
content or a report is **defanged** (`hxxps://api[.]telegram[.]org/…`) so the
alert itself does not read as phishing to a mail filter; links to the
instance's own register are not.

Every event — a hold, a flag, a tripwire hit, a stored report — names the
site, its owner (email, through `ext.AccountDirectory`, and id), its lock
state, the rule, file and excerpt, and links to the register row
(`https://<base>/account/admin?q=<id>`) where there is a register.

Aggregation, so a flood cannot mail-bomb: at most **one mail per site (or
report target) per hour** and **ten immediate mails per hour** in all;
everything else goes into a **digest**, sent an hour after the first event it
holds (at most 200 lines, then a count). A **hold** is always mailed at once
and spends no budget (a site is held once); an **unverified** tripwire event
— which anyone can cause with a forged report — only ever goes to the digest.
Sending is asynchronous with a 30-second timeout per mail; a failure is
logged.

## Reports reach a human

- **`GET /report`** on the base domain: a server-rendered page — target URL
  (prefilled from `?url=`), reason (phishing, malware, fraud or scam, spam,
  illegal content, copyright, other), details, optional reporter email. No
  JavaScript: its CSP is `default-src 'none'; style-src 'unsafe-inline';
  form-action 'self'; frame-ancestors 'none'; base-uri 'none'`.
- **`POST /report`**: spam protection that works without JS — the site-forms
  honeypot (`_gotcha`, off-screen) and a signed form ticket (HMAC with the
  instance secret, at least 3 seconds and at most 2 hours old). The forms
  feature's ALTCHA needs JavaScript, so it is not used here. Then the same
  limits as `POST /api/report` (20/hour per address, 200/hour instance-wide,
  one report per target and source network per day), which it shares.
- A report stores the optional **contact** address (new `Report.Contact`) and
  where it came from (`Report.Via`: `page` or `api`); `POST /api/report`
  accepts `contact` too. Reports are still purged after 14 days.
- Targets resolve as before, plus `/v/<id>/` path URLs.
- Every **stored** report mails the operator (aggregated like the rest).
- Linked from the landing page's footer here, and — through
  `SITEBIN_ABUSE_REPORT_URL`, whose default is this page — from the 410 "Site
  suspended" page, the view-domain apex page and `security.txt`
  ([`2026-09-29-provenance-csp-apex.md`](2026-09-29-provenance-csp-apex.md)).
- A held write fails for its caller but happened: the site's provenance
  trail records it (creation, upload, replace, WebDAV, FTP), marked "held for
  review".

## The register

- A **Reports** tab (`/account/admin/reports`), newest first, 300 shown: when,
  reason, target, details, contact, source network, and the resolved site with
  owner and lock state. An unlocked resolved site has **Lock site**: one POST
  (CSRF-checked) that locks it and returns to the tab, with the reason
  prefilled as "abuse report: <reason>" in a visible, editable field — an API
  report's reason is anyone's free text, and a lock reason is shown to the
  owner.
- The **Flagged** filter now means scanner findings *or* CSP-blocked requests;
  a **Scanner** figure counts sites with findings.
- A row with findings lists them (rule · file · excerpt, first three) and
  offers **Dismiss**, which moves them into the reviewed set without unlocking.
- A scanner lock reads "locked … by the scanner" and, like a suspension's,
  offers **Keep** to turn it into the operator's own lock.

Seam additions: `ext.LockByScanner`, `ext.ScanFinding`, `SiteInfo.Findings`,
`ext.AbuseReport`, `SiteService.Reports`, `SiteService.ClearFindings`, and the
optional `ext.AccountDirectory`.

## The CLI

```
sitebin scan <view-id|edit-id|domain> [--lock]
sitebin scan --all [--lock]
```

Report only (no write at all) unless `--lock`, which records the findings and
holds the sites the upload scanner would hold (same policy; trusted sites are
never locked). Run on the host as `docker exec sitebin sitebin scan --all`.

## Testing

- `internal/abuse`: every starter rule on a positive and a legitimate negative
  (a plain login form, an SPA bundle); `all`/`any`; regex; chunk boundaries;
  binary skip; file format, merge, replace, off, invalid file keeps the last
  good rules, reload on mtime.
- Store: held before visible (the file is not in place when the lock is
  written — asserted from inside the hook); trusted, container, operator and
  already-locked exemptions; reviewed fingerprint after unlock; new content
  re-locks; replace commit; staged file and rename; tripwire verification.
- Every write surface through HTTP: create (multipart, zip), upload, replace,
  MCP `create_site` / `write_files` (plain and replace), upload token (POST and
  WebDAV), WebDAV PUT / COPY / MOVE, FTP. Plus a **write-path census**: a test
  that parses the packages that touch site content and fails when a new call
  that writes into a site appears outside the guarded functions.
- Tripwire: verified lock, forged report (content does not reference it) not
  locked, foreign document URL ignored, trusted alert-only, reviewed.
- Alerts: plain text, defanged, per-site hourly limit, global cap, digest.
- Report page: GET renders without scripts, POST stores + mails, honeypot,
  ticket age, limits; API report mails. Register: tab, lock from report,
  findings, dismiss, filter.

## Corrections (review before merge)

An independent review of the branch found ten problems; all were fixed
before it was pushed, and the sections above describe the result:

1. A NUL byte in the first 8 KiB made a file "binary" and unscanned, and
   UTF-16 always did — a trivial bypass. NULs are now dropped before
   matching; only binary extensions are skipped.
2. Active types were a short extension list; `.xht`, `.xml` and other XML or
   script types the container's mailcap serves as such could not block.
   `IsActive` now also asks the MIME table.
3. Dismissing a kit as a flag in a `.txt` reviewed its bytes for any name, so
   the same bytes as `index.html` were never held. Reviews now carry their
   severity.
4. A write, zip, delete, or DAV/FTP remove that passed its gate before a
   hold could still change the evidence. They re-check the lock now.
5. One forged CSP report could make the tripwire hash a whole large file;
   the budget now counts every byte read, and the limit is per site.
6. Forged reports could use up the hourly alert budget and push real
   alerts into the digest. Unverified events go to the digest only; holds
   bypass the caps.
7. The Reports tab's one-click lock published a reporter's free text as the
   lock reason unseen. The reason is a visible, editable field.
8. Leaving container mode served what the containers wrote unscanned.
9. `OpenStaged` copied whole files for a handle never written (PROPPATCH);
   the copy is now made on first use.
10. An unsuspension could serve a kit found while the account lock stood; it
    now turns into a scanner lock.

Also: the operator check reads the admin allowlist before the tier, so an
upload that trips a rule costs no PayGate call for someone who is not on it.
