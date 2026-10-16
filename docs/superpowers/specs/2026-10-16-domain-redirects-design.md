# Domain redirects and the generated address

**Date:** 2026-10-16
**Status:** implemented

## Problem

An owner who attaches `morgenfertig.at` and `morgenfertig.de` to one site gets
the same site on both — two hosts with identical content, neither of them
canonical. What they want is one domain that serves and the others that send
visitors there with a `301`, answered by this instance (with a certificate)
rather than by a redirect service somewhere else.

The site's generated address, `<id>.sitebin.app`, keeps serving the site next
to the custom domains — a third copy, on a domain the owner does not control,
and one that shares Safe Browsing's verdict with every other drop until the
PSL entry lands. Once a site has its own domain, the owner should be able to
switch that address off or have it redirect too.

## What changes

1. **A verified custom domain can redirect** to another verified custom domain
   of the same site, with a code chosen from `301`, `302`, `307`, `308`
   (default `301`). The path and query are kept:
   `https://morgenfertig.de/preise?x=1` → `https://morgenfertig.at/preise?x=1`.
2. **The generated address has a mode**: `serve` (the default, and every site
   written before this), `off` (not served: the visitor gets "Site not found"),
   or `redirect` to a custom domain with a code. Off and redirect apply to the
   subdomain (`<id>.<view domain>`) and to the path view (`/v/<id>/`) alike —
   both are the site's generated address.
3. **The edit page** shows, in the custom-domain card, one row for the
   generated address and one per verified domain, each with a target dropdown
   (serves the site / → one of the site's domains, plus "off" for the generated
   address) and a code dropdown. **The JSON API and MCP** set and return the
   same.

## Where the redirect is answered

Every request to a content host already passes `forward_auth` →
`/internal/authz` before Caddy serves a byte. authz answers a redirect address
with the `3xx` and its `Location`; Caddy relays a non-2xx answer of
`forward_auth` to the client as it is, so the redirect leaves Caddy without
the file server, the container proxy or the gate page ever running. The
Caddyfile does not change.

The alternative — one Caddy block per redirecting host — needs the Caddyfile
to change at runtime. It is generated once from the environment, and the admin
API is off on purpose; reloading Caddy on every owner's click is not a thing
this design takes on for a redirect that authz can answer in microseconds.

**Certificates come for free.** A redirecting domain is still an attached
custom domain: indexed, so `/internal/tls-check` says yes and Caddy's
on-demand TLS issues its certificate on the first visit, exactly as before.
Plain HTTP hits the `http://` catch-all's upgrade to HTTPS first, then the
redirect. The site-level `Strict-Transport-Security` header is on the relayed
answer as it is on the gate page.

**Caching.** A `301`/`308` answer carries `Cache-Control: public,
max-age=3600`: browsers otherwise keep a permanent redirect indefinitely, and
an owner who changes or removes the redirect would be overruled by every
visitor's cache. Search engines still read the `301` as permanent. `302`/`307`
carry `Cache-Control: no-store`.

## Rules

Stored in `meta.json` (rule 1: absent fields read as "serves"):

```go
DomainRedirects map[string]Redirect `json:"domain_redirects,omitempty"` // key: the redirecting domain
ViewAddress     *ViewAddress        `json:"view_address,omitempty"`     // nil: the generated address serves

type Redirect    struct { To string `json:"to"`; Code int `json:"code"` }
type ViewAddress struct { Mode string `json:"mode"`; To string `json:"to,omitempty"`; Code int `json:"code,omitempty"` }
```

**When it is set** (`store.SetDomainRedirect`, `store.SetViewAddress`, under the
site lock, against the freshly read `meta.json`):

- The redirecting domain and the target must both be **verified** custom
  domains of this site. A pending claim can be neither: it serves nothing yet.
  Targets are never outside the site, so a redirect cannot become an open
  redirector on a proven domain, and no other site's or account's rule is
  involved.
- **No chains.** The target must serve the site itself (not redirect), and a
  domain that is the target of a redirect — a domain's or the generated
  address's — cannot be made a redirect until that one changes. One hop, never
  a loop.
- A domain cannot redirect to itself; the code must be one of the four.
- **`off` needs a custom domain that serves.** Switching the generated address
  off when nothing else serves the site would take it offline.
- Container sites are refused (`409`): their addresses come from the compose
  file, which maps each host to a service, and their edit page has no domain
  card.

**When it is served** (`Meta.DomainRedirect`, `Meta.ViewRule` — authz asks only
these):

- A rule is **in effect** only while it can be followed: its domain and target
  are verified and the target serves. The sweep detaches a domain whose proof
  has been gone for three days without asking anyone; a redirect to it then
  stops and its domain serves the site, rather than sending visitors to a host
  this instance no longer answers.
- **`off` is in effect only while a custom domain serves.** If the last one is
  detached, the generated address serves again on its own, so a DNS mistake
  never takes the whole site offline. Once the domain verifies again, `off`
  applies again — it is what the owner chose.
- A container site serves on every address regardless of stored rules.

**When a domain is removed** (`store.RemoveDomain` — the owner, the API, MCP,
and the container compose sync): its own redirect goes, redirects pointing at
it go (those domains serve the site again), a generated-address redirect to it
goes (the generated address serves again), and an `off` with no serving domain
left goes. Removing is the owner's decision, so the stored rules follow it;
detaching in the sweep is not, so it changes nothing stored.

**Order in authz:** lock (410) → expiry (410) → address rule → view password →
container routing. A locked or expired site says so on every address. A
redirect comes before the password gate: the gate is asked on the domain the
visitor lands on.

## Everything else on a non-serving address

Requests under `/_sitebin/*` reach the backend without authz. On an address
that does not serve the site, the form endpoint (`/_sitebin/forms/…`, including
`?_site=` on a path view) and the unlock form answer `404` — the same as for an
address that belongs to no site. CSP reports and the abuse report page still
resolve the site: a report about a redirecting host is still a report about
the site.

## Surfaces

**JSON API.**

- `PUT /api/sites/{edit}/domains/{domain}/redirect` `{"to": "morgenfertig.at", "code": 301}`
  sets it (`code` defaults to `301`); `DELETE` on the same path makes the
  domain serve the site again. `404` for a domain the site has not attached,
  `400` for a rule the checks refuse, `409` on a container site.
- `PUT /api/sites/{edit}/view-address` `{"mode": "off"}`,
  `{"mode": "redirect", "to": "morgenfertig.at", "code": 301}` or
  `{"mode": "serve"}`.
- The site document gains `domain_redirects` (`{domain: {to, code, in_effect}}`,
  `{}` when none), `view_address` (`{mode, to, code, in_effect}`, mode `serve`
  when unset) and `primary_url` — where the site is served first: the generated
  address while it serves, else the first custom domain that serves.
  `view_url` stays the generated address: it is the site's identity, the CNAME
  proof target, and what the docs define it as.
- Both writes are provenance-recorded (`domain-redirect`, `view-address`).

**MCP.** Two new tools — adding a tool is not a contract break, and neither
fits an existing one (`update_site` takes no per-domain arguments):

- `set_domain_redirect {edit_id, domain, to, code}` — `to` empty serves again.
- `set_view_address {edit_id, mode, to, code}`.

Both return the `SiteResult`, which gains `domain_redirects` and
`view_address`, each **omitempty** and present only on a site that uses them:
a client that cached the output schema before the deploy rejects a result
carrying a field it has not seen (the site-names trap, 2026-09-24), and this
way every result for a site without rules is exactly what it was.

**Edit page.** The custom-domain card lists, above the verified domains, the
site's generated address — only once the site has a verified domain, because
before that there is nothing to switch to. Each row has a target dropdown and
a code dropdown that saves on change; the "open site" link follows
`primary_url`. A rule that is stored but not in effect says why ("its target is
not attached; serving the site meanwhile").

**Account dashboard.** A redirecting domain is listed as `morgenfertig.de →
morgenfertig.at`; a generated address that is off is shown without a link and
marked off, and the site's first link is `primary_url`.

## What does not change

- The Caddyfile, `tls-check`, the domain index, verification, the sweep, the
  per-site domain cap (a redirecting domain is still a domain) and the licence
  ceiling.
- No redirect to anything outside the site's own verified domains, no redirect
  of a single path, no "drop the path" option. Each would be a different
  feature with its own abuse surface.

## Testing

- `store`: every refusal of the two setters, clearing, `RemoveDomain` pruning,
  and the in-effect rules (detached target, last serving domain detached,
  container site).
- `httpapi`: authz answers (code, `Location` with path and query, path-view
  prefix stripped, cache headers, lock and expiry first, redirect before the
  gate, `off` → 404); the two routes; the payload; forms and unlock on a
  non-serving address; MCP tools through the adapter.
- `e2e/redirects.ps1` (enterprise image, verification off): the whole chain
  through the real Caddy — the redirect, its `Location`, and `off`.
