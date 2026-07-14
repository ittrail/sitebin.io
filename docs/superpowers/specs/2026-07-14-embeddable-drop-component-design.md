# Sitebin Embeddable Drop Component — Design

Date: 2026-07-14 · Status: Approved for implementation (autonomous goal session)

Extract the startpage's file-drop / site-creation area into a reusable,
embeddable component so (a) the startpage consumes it (no logic drift) and
(b) any external page — first consumer: the sitebin.io marketing site — can
embed a working "drop files → get a site" widget pointed at a Sitebin
instance. Cross-origin embedding against an instance is an **Enterprise**
capability (the operator must opt in with an origin allowlist).

## 1. The component: `<sitebin-drop>`

One self-contained, dependency-free file: `web/static/embed.js`. It defines a
custom element with Shadow DOM (style isolation on foreign pages):

```html
<script src="https://app.sitebin.io/_sitebin/embed.js" defer></script>
<sitebin-drop instance="https://app.sitebin.io"></sitebin-drop>
```

Faithful extraction of today's create flow — drop slab (drag & drop incl.
folder traversal), file/zip/folder pickers, staged-file chips, options
(mode + auto-suggest, unzip toggle, view password, expiry, WebDAV, custom
domains), publish with upload progress, error bar.

Attributes (all optional):

- `instance` — API base URL. Default: the origin the script was loaded from,
  else the page origin. All requests go to `<instance>/api/sites`.
- `demo` — no network; publishing simulates a response after a short delay
  and shows a fake ticket marked as a demo. Lets sitebin.io show the real
  interaction before/without a live backend, and works offline.
- `no-domains` — hide the custom-domains option (for embeds against
  community instances where the domain API returns 403).
- `event-only` — suppress the built-in ticket; the host page renders its own
  from the `sitebin-published` event.

Events (composed, bubbling): `sitebin-published` (detail = full API response
body), `sitebin-error` (detail = {status, error}).

Built-in ticket (default): view URL / edit URL / edit password rows with copy
buttons + "save the password now" warning + "publish another" reset. No QR
(keeps the file dependency-free; the startpage keeps its richer ticket).

Styling: the component carries its own styles (Sitebin dark design tokens)
inside the shadow root; fonts inherit from the host page. A `--sitebin-*`
CSS custom-property surface is *not* offered in v1 (YAGNI).

## 2. Startpage consumes it

`web/static/index.html` replaces the `#create` section's slab/options/publish
markup with `<sitebin-drop event-only>`; `app.js` shrinks to: listen for
`sitebin-published` → render the existing full claim ticket (QR, copy-all,
publish-another). The slab/option/chip styles move out of `app.css` into the
component. Behavior parity is required (same options, same auto mode
suggestion, same API payloads).

## 3. Serving & CORS (the Enterprise gate)

- `GET /_sitebin/embed.js` — serves the component with
  `Access-Control-Allow-Origin: *` (the script itself is public code) and
  modest caching. Available in both editions: same-origin composition and
  iframes work everywhere.
- **Cross-origin create** is what's gated. New config
  `SITEBIN_EMBED_ORIGINS` — comma-separated list of exact origins
  (`https://sitebin.io,https://www.sitebin.io`) or `*`.
  - Enterprise (provider registered, mirrors custom-domains gating): when a
    request's `Origin` matches the allowlist, `POST /api/sites` (and its
    `OPTIONS` preflight) answer with `Access-Control-Allow-Origin: <origin>`
    + `Vary: Origin`.
  - Community: the variable is ignored with a startup warning ("embed
    origins require the enterprise edition"); no CORS headers are ever
    emitted, so foreign-origin embeds cannot read the create response.
- `ext.Provider` gains `EmbedOriginsAllowed() bool`; the ee provider returns
  `true` (same semantics as `CustomDomainsAllowed`). Community has no
  provider → gate closed.
- No credentials are involved (`Access-Control-Allow-Credentials` never set);
  the create endpoint is already public + rate-limited, so allowing chosen
  origins to read responses adds no new authz surface.

## 4. Testing

- Config: parse/normalize `SITEBIN_EMBED_ORIGINS` (trim, lowercase, `*`).
- httpapi (table-driven, with `ext.Reset()` + fake provider as in existing
  tests): embed.js route serves JS with `ACAO:*`; create with allowed origin
  → `ACAO` echoed only when provider present; disallowed/absent origin →
  no header; `OPTIONS /api/sites` preflight answers methods/headers.
- JS: no unit-test infra exists (by design, no Node); parity verified via
  the Docker compose example + browser walkthrough (publish a site through
  the refactored startpage and through a foreign-origin embed page).

## 5. Docs

README: new "Embed the drop area on your own site" section (script tag,
attributes, events, `SITEBIN_EMBED_ORIGINS`, edition note). The sitebin.io
docs get a full page (separate repo).

## 6. Non-goals (v1)

Theming API, npm package, React/Vue wrappers, QR in the embedded ticket,
upload resumption, per-origin rate limits (instance limits apply as-is).
