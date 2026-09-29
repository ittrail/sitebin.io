# Provenance, a stricter untrusted CSP, and the view-domain apex

**Date:** 2026-09-29
**Status:** implemented
**Context:** the phishing incident of 2026-09-25…28 (internal record in the
website repo, `docs/internal/2026-09-28-phishing-incident.md`), proposals
P1.6, P1.7 and P2.9. Builds on
`2026-08-27-untrusted-content-headers-design.md` (read its Caddy traps) and
`2026-09-28-site-lock-and-account-suspension.md`.

Three independent pieces, plus one finding:

1. **Provenance** — who made each site and each change, from where.
2. **A stricter CSP for untrusted tiers** — closes the image-beacon channel and
   keeps third-party scripts off drops and free sites.
3. **The view-domain apex** — `sitebin.app` and `www.sitebin.app` answer with
   an info/abuse page and `security.txt`, and so does the base domain's
   `security.txt`.
4. **`/.well-known/` on uploaded sites** — verified, not changed.

## 1. Provenance

### What the incident lacked

Two abusers, one of whom minted an API token in the minute the account was
created and re-uploaded a deleted page hours later. Nothing recorded where a
site came from: not the address, not the client, not whether it arrived
through the UI, a script or an agent. "Was this the same person?" had no
answer.

### What is recorded

An **entry** (`internal/provenance.Entry`) is one event:

| field | meaning |
|---|---|
| `t`, `last`, `n` | when; for a merged burst the latest time and the count |
| `action` | `create`, `upload`, `replace`, `delete-file`, `mkdir`, `move`, `copy`, `settings`, `domain-add`, `domain-remove`, `form-add`, `form-update`, `form-remove`, `form-resend`, `container`, `open-upload`, `rename`, `rotate-password` (site log); `signup`, `signin`, `token-mint`, `site-create`, `site-delete` (account log) |
| `surface` | where it came in: `ui` (Sitebin's own pages — a browser-shaped request, the `fromOwnBrowser` heuristic, a label only), `api`, `mcp`, `upload-token`, `webdav`, `ftp`, `dashboard`, `oidc`, `local` |
| `auth` | the credential that authorised it: `password`, `token`, `oauth`, `session`, `upload-token`, `none` |
| `account` | the acting account, empty for anonymous / edit-password |
| `ip` | the client address as `auth.ClientIP` derives it: the LAST `X-Forwarded-For` entry, the one Caddy appended — never a client-chosen one; the connection's peer for FTP |
| `ua` | the User-Agent, control characters stripped, capped at 200 bytes |
| `site` | account log: the site acted on |
| `files`, `detail` | what changed: a file count, the first paths, which settings, a domain, a token's name |

**Where.** Append-style JSONL, one file per subject, next to the data it is
about, so deleting the subject deletes its trail:

- `sites/<view-id>/provenance.jsonl` — every creation and write of the site.
  Outside `files/`, so it is never served, listed, counted against a quota,
  zipped into a download or reachable from a container's bind mount.
- `accounts/<id>/provenance.jsonl` (enterprise) — sign-up (address, client,
  how), sign-ins, every API token minted (name, address, client), and a
  mirror of `site-create` / `site-delete` for the account's sites, so that a
  site deleted by its owner still leaves "account A created a site from
  address X" behind.

**Bounded.** A file holds at most 100 entries; the oldest go first, except the
first entry when it is the origin (`create`, `signup`), which is what the
register shows. A burst is merged into one line: the same action, surface,
credential, account, address and client within 10 minutes of the previous
line becomes `n` repetitions (a 500-file WebDAV sync is one line with
`files: 500`), except origin events and token mints, which always stand
alone. Each record rewrites the (small) file atomically; the site's stats
lock serialises it with `Delete`, which takes that lock too, so a record
racing a delete can never resurrect a deleted site's folder.

**Which writes.** Every surface that changes a site, at the one place that
authenticated it:

- JSON API — `withEditAuth` / `withUploadAuth` put the actor in the request
  context (token, session, password, upload token — an upload token carries
  the account that opened it through MCP); the handlers record.
- Creation — `createSiteWith`, once, for API, UI and MCP alike.
- MCP — the adapter records after each write tool, with `mcp` and the
  connection's account.
- WebDAV — every mutating method (`PUT`, `DELETE`, `MKCOL`, `MOVE`, `COPY`,
  `PROPPATCH`), password or upload token.
- FTP — writes through the session's filesystem, via an optional recorder the
  authenticator implements (FTP is off on the hosted instance).
- Dashboard (enterprise) — rename and edit-password rotation through the seam;
  a dashboard delete goes to the account log.

Reads are not recorded. The operator's own register actions are not
provenance (they are logged with the admin id, as before).

### Retention — 90 days

Legitimate interest (GDPR Art. 6(1)(f)): detecting and proving abuse of the
service, and answering hosting providers' and authorities' abuse reports.
The cleanup sweep purges every entry whose latest time is older than 90 days
(`provenance.Retention`), and removes a file left empty. Two exceptions, both
evidence holds the operator placed deliberately:

- a **locked** site's log is not purged — the sweep skips locked sites
  entirely, and the log is part of what the lock holds;
- a **suspended** account's log, or one of an account owning a locked site, is
  not purged either.

*(Corrected 2026-09-29, see "Corrections (post-implementation)" below: both
holds are now bounded by the lock retention.)*

Deleting a site deletes its log (it lives in the site folder); deleting an
account deletes its log (it lives in the account folder) and its sites. An
entry naming the account in a site it did NOT own — possible only when it
acted on a stranger's site through that site's edit password over MCP —
stays with that site until its 90 days are up: it is that site's record. The
nightly host backups (7 days) keep purged entries for at most that long.

The GDPR export ordered by the stack carries the account's own log and, from
each site it owns, the entries whose acting account is this account
(`provenance.account` / `provenance.sites`). Entries made on its sites
through the edit password are not exported: nothing says they were this
person.

### The seam

- `ext.SiteProvenance` (optional, asserted on the `SiteService`):
  `SiteProvenance(viewID)`, `RecordSiteProvenance(viewID, entry)` for the
  dashboard's own writes, and `SitesSeenFrom(match)` for the register's
  address search. The core implements it; fakes need not.
- `ext.AccountProvenance` (optional, asserted on the `Provider`):
  `RecordAccountProvenance(accountID, entry)` — the core mirrors
  `site-create` / `site-delete` there — and `PurgeProvenance(before)`, which
  the cleanup sweep calls with the retention cutoff.

The community build has neither: sites still get their own log, and no
account log exists.

### The register

- Each row shows the creator's address and surface (and client on hover), and
  the latest write; the address links to the address search. A **Trail**
  button opens `/account/admin/sites/{id}/trail`: the site's whole log, and
  the owner's account log — sign-up, tokens minted, the last sign-ins.
- A token minted within ten minutes of sign-up is marked, with the delay
  ("12 s after sign-up"): scripted accounts do exactly that.
- **Address search.** A query that is an IP address or a CIDR range
  (`203.0.113.7`, `203.0.113.0/24`, `2001:db8::/48`) switches the register to
  every site whose log names an address in it, and lists above it every
  account seen there — from its own log (sign-up, sign-in, token) or as the
  acting account on a matching site entry — with what it did and when.
- `/account/admin/accounts/{id}` shows one account's log, for an account that
  owns no site.

All server-rendered under the console's `script-src 'none'`.

### The CLI

`sitebin list` gains a `FROM` column (the creator's address).
`sitebin provenance <view-id|edit-id|domain>` prints the site's log;
`sitebin provenance <ip|cidr>` lists every site whose log names it, with the
matching entries. Account logs are the extension's data and are not read by
the core CLI.

## 2. A stricter CSP for untrusted tiers

The 2026-08-27 design left two channels open on purpose: the image beacon
and top-level navigation, and it let scripts load from anywhere. The incident
added two observations: every blocked exfiltration so far went through
`connect-src`, but the image beacon would have gone through; and a new drop
loaded a contact-centre chat widget from a third-party host. The untrusted
policy therefore gains:

```
img-src 'self' data: blob:
script-src 'self' 'unsafe-inline' 'unsafe-eval' <script hosts>
style-src 'self' 'unsafe-inline' <style hosts>
font-src 'self' data: <font hosts>
media-src 'self' data: blob:
worker-src 'self' blob:
manifest-src 'self'
```

with the host lists configurable, because a CDN the policy names is a
decision that will need revisiting without a release:

| variable | default |
|---|---|
| `SITEBIN_CSP_SCRIPT_HOSTS` | `https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com https://cdn.tailwindcss.com https://code.jquery.com` |
| `SITEBIN_CSP_STYLE_HOSTS` | `https://fonts.googleapis.com https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com` |
| `SITEBIN_CSP_FONT_HOSTS` | `https://fonts.gstatic.com https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com` |
| `SITEBIN_CSP_IMG_HOSTS` | *(none)* |

Comma- or space-separated host sources (`https://cdn.example`,
`https://*.example.com`, a port, a path); `none` empties a list. Anything
else — a keyword, a quote, a semicolon — is refused at startup, because the
value is written into the Caddyfile and into a header. The keywords
(`'self'`, `'unsafe-inline'`, `'unsafe-eval'`, `data:`, `blob:`) are fixed.
The directives are read at startup: changing a list is an environment change
and a restart.

**Why this shape.** `'unsafe-inline'` and `'unsafe-eval'` stay because nearly
every static site inlines a script and many frameworks evaluate one; the aim
is which *hosts* a page may pull code from, not whether it may run its own.
The allowlist is not a wall — jsDelivr and unpkg serve any npm package and
any GitHub repository, so a determined author can still host a script there —
it keeps arbitrary hosts (IPFS gateways, a Telegram-hosted kit, trackers,
third-party widgets) off drops and free sites, and every attempt now lands in
the CSP report the tripwire reads. Exfiltration is closed by `connect-src`,
`form-action` and now `img-src`, not by `script-src`.

**What it costs a legitimate untrusted site.** External images (a hotlinked
picture, a README badge rendered by the viewer), scripts from any other CDN,
fonts from any other host, and embedded third-party media. Paid tiers are
trusted and unaffected, as before.

**What stays working.** Everything Sitebin serves on a site origin comes from
that origin (`/_sitebin/assets/…`, the viewer and its PDF worker, the ALTCHA
script): `'self'`. The viewer keeps its own stricter `<meta>` policy, which
intersects. Forms are unaffected: an untrusted site has a forms cap of 0 and
`form-action 'none'` already. The embed was already inert on an untrusted
site — `connect-src 'self'` refuses its upload to the instance — and now its
script is refused too unless loaded from the site's own origin.

**Both matchers, one complete policy each.** Nothing about the Caddy shape
changes: `@untrusted` carries the whole policy above, `@trusted` the baseline,
exact complements, inside the `route` after `forward_auth`. The report
endpoint and format are unchanged (`report-uri /_sitebin/csp-report;
report-to csp` + `Reporting-Endpoints`), so blocked URIs keep arriving where
the CSP-report tripwire reads them.

## 3. The view-domain apex and security.txt

`sitebin.app` answered nothing (its HTTPS handshake failed: the custom-domain
catch-all asked the backend, which refused, so no certificate existed) and
`www.sitebin.app` fell into the site wildcard and answered 404. Nobody outside
could learn whose domain it is or how to report a site on it.

When `SITEBIN_VIEW_DOMAIN` differs from the base domain (and sites are served
on subdomains), the generated Caddyfile gets one more site block for
`<view>` and `www.<view>`, with the same `tls` block as the wildcard (the DNS
challenge, `propagation_delay 60s`, or the operator's snippet), HSTS with
`includeSubDomains` and a proxy to the backend. A named host sorts before the
wildcard in Caddy, so `www.<view>` leaves the site wildcard. `www` can never
be a site: view ids are exactly 26 base32 characters, and the whole view
namespace is already reserved against custom domains.

The backend answers those two hosts with nothing but:

- `/` — a static `noindex` page in the system pages' look (the gate/410 card):
  this domain serves user-generated content for `<base>`; sites here are made
  by third parties, not reviewed before they go live; how to report abuse
  (the report page and the abuse mailbox); a link to the operator's site.
  No external resources; its own CSP is `default-src 'none'` plus inline
  styles; `X-Robots-Tag: noindex`.
- `/.well-known/security.txt` — RFC 9116.
- everything else — 404. The app's routes (`/api`, `/account`, `/mcp`, …)
  never answer on the view domain.

`/.well-known/security.txt` is also served on the base domain:

```
Contact: mailto:<SITEBIN_ABUSE_CONTACT>
Contact: <report URL>
Expires: <now + 180 days, midnight UTC>
Preferred-Languages: en, de
Canonical: https://<base>/.well-known/security.txt
Canonical: https://<view>/.well-known/security.txt
Canonical: https://www.<view>/.well-known/security.txt
Policy: <report URL>
```

| variable | meaning |
|---|---|
| `SITEBIN_ABUSE_CONTACT` | the public abuse mailbox; omitted everywhere when unset |
| `SITEBIN_ABUSE_REPORT_URL` | the report page; default `https://<base>/report`, `none` to omit |
| `SITEBIN_HOME_URL` | the operator's main site, linked from the info page; default the base URL |

The **410 "Site suspended"** page carries the same "report abuse" line.

## 4. `/.well-known/` on uploaded sites

sitebin.io is itself a Sitebin site, deployed with `POST /files?replace=true`,
and its repository adds `public/.well-known/security.txt`. Every write path
validates names with `store.CleanRelPath`, which refuses a reserved first
segment (`_sitebin`, `_raw`, `meta.json`) and any first segment starting
`.sitebin-` — and nothing else about dots. So `.well-known/security.txt` is
accepted by the multipart upload, a zip, a staged replace, MCP
`write_files`, an upload token, WebDAV and FTP, listed like any file, and
served by Caddy's `file_server`, which hides only the two markers. Tests
now hold that for every path. No change was needed; the `.sitebin-*` marker
protections are untouched.

## Tests

- `internal/provenance`: merge window, origin kept past the cap, purge, UA
  cleaning, IP/CIDR matching, tolerant reads.
- `internal/store`: record/read/purge; a record after `Delete` does not
  recreate the folder; the log is not a site file.
- `internal/httpapi`: an entry for every write path — create (UI, API, MCP),
  upload, replace, delete-file, settings, domains, forms, upload token (with
  the issuing account), WebDAV, FTP, MCP writes, open_upload — with surface,
  auth, account, IP (the last `X-Forwarded-For` entry) and UA; the account
  mirror; apex/www/security.txt routing, the app never answering on the view
  domain, no user site shadowed.
- `internal/cleanup`: the sweep purges past 90 days, keeps a locked site's
  log, calls the extension's purge.
- `internal/caddygen`: the exact untrusted policy in both matchers, the
  configured host lists, the apex block and its certificate.
- `internal/config`: the lists' parsing and refusals, the abuse variables.
- `ee`: sign-up / sign-in / token-mint entries on every sign-in path, MCP
  provisioning, the account purge and its holds, the GDPR export, the
  register's trail, address search and account page.

## Corrections (post-implementation)

**2026-09-29 — the evidence holds are bounded.** The two exceptions under
"Retention — 90 days" were open-ended: a locked site's log lasted as long as
the lock, and a suspended account's (or a locked-site owner's) log as long as
the suspension or the lock — which, before the lock retention, was forever.
The privacy policy now promises at most 180 days after the lock unless a case
is still open, so the rule is, with *R* = `SITEBIN_LOCK_RETENTION_DAYS`
(default 180):

- A **locked site's** log is still not purged while the site is locked, and
  goes **with the site** when the sweep purges it at *R* after the lock —
  unless the lock carries the operator's **evidence hold** ("case open").
- A **suspended** account's log, and that of an account **owning a locked
  site**, is purged of entries older than **max(90 days, *R*)** — kept past
  the 90 days only up to the lock retention — unless one of the account's
  locked sites carries an evidence hold, which keeps the whole log.
- *R* = 0 restores the open-ended holds.

The seam became `AccountProvenance.PurgeProvenance(before, heldBefore)`. The
full statement, the clock and the hold are in the lock-retention addendum of
[`2026-09-28-site-lock-and-account-suspension.md`](2026-09-28-site-lock-and-account-suspension.md).
