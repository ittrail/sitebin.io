# Site analytics — an Umami replacement for every Sitebin site

Date: 2026-11-04. Status: design, agreed with the operator in conversation
(sitebin-project-b9). Touches both repos (product and `Sitebin-Website`).

## Why

The operator runs Umami (umami.ittrail.cloud, Umami 3.0.2) for 14 websites
and wants to **take Umami out**. Every one of those sites moves to Sitebin
sooner or later, so the question here is only whether Sitebin can do what
Umami does for a site it hosts. It cannot today: authz sees every request,
but all that is kept is a view counter and a last-seen time
(`internal/store/stats.go`), and Caddy writes no access log.

Two sites already on Sitebin use more of Umami than page views:

- **morgenfertig.at** (a file site) sends events with `umami.track`
  (`anfrage-start`, `anfrage`, `bewerten`, with data such as `seite`,
  `quelle`, `kampagne`, `paket`). It has two funnels
  ("Landingpage → Anfrage", "Formular begonnen → gesendet") and two goals,
  and it turns off the operator's own visits with `?statistik=aus`, which
  sets `localStorage["umami.disabled"]`.
- **rombalu.com** (a container site) sends `data-umami-event` events
  (Pinterest, Heft, JPG, Lied, Teilen, Eigenwerbung). It loads Umami only on
  the parents' pages: never in the kids view, the games, the account, admin
  or login pages. A `data-before-send` hook strips every query parameter
  except `utm_*`, removes the hash, masks folder tokens and tags app visits
  with `app`.

Marketing links already in market carry the standard
`utm_source / utm_medium / utm_campaign / utm_term / utm_content`. Google
Ads adds `{keyword}` / `{adgroupid}` and the click ids
`gclid / gbraid / wbraid`; Meta adds `fbclid`. **None of these links may
need to change.**

## The operator's decisions

- Analytics only for now. **A/B testing stays in mind but is not built.**
  The data model already carries an empty **variant** field (below), so
  A/B later needs no data change.
- The dashboard is a tab, **"Analytics"**, on the edit page. It hangs on
  the tab bar the Git work built (`tabs.register({id, label, panel,
  onShow})`, `#analytics`, loaded on first show). It may have **sub-tabs**
  and should look and work like Umami's dashboard.
- It is a **community feature** (MIT core). On sitebin.io it comes with
  **Pro and up**, through a tier flag **`analytics`**, exactly like `git`.
- **Security is not to get weaker anywhere, and nothing may come back to
  haunt us**, whether legally (privacy) or operationally (an open endpoint,
  disk). The operator left the technical side of this to the agent.
- The playback of the shape below was confirmed: off until the owner turns
  it on, two measuring modes, a Umami-compatible script, no IP stored, a
  daily salt, no cookies (except the one the owner asks for, below), owner
  and admins see it.
- **Umami history is imported per site, on request.** The operator moves
  sites one at a time and comes back after each move. The agent then
  copies that site's Umami history across with a per-site importer. There
  is no bulk migration.

## Goals

1. A site owner turns analytics on in the Analytics tab and sees, like in
   Umami:
   - visitors, visits, views, bounce rate and visit duration, compared with
     the previous period;
   - a views/visitors chart;
   - pages, entry pages and exit pages;
   - referrers, channels and UTM;
   - browsers, OS and devices;
   - countries, regions and cities;
   - languages, events and their data;
   - funnels and goals;
   - a realtime view.
2. Moving a site from Umami means **swapping one `src`** (or deleting the
   Umami tag) and nothing else. `umami.track`, `data-umami-event*`,
   `data-before-send`, `data-tag`, `data-domains`, `data-exclude-search`,
   `data-exclude-hash`, `data-do-not-track`, `data-auto-track` and
   `localStorage["umami.disabled"]` keep working.
3. A site without any script still gets page analytics, counted on the
   server.
4. Existing campaign links work unchanged.
5. A site's Umami history can be imported, one site at a time.

## Non-goals (this version)

- A/B testing itself. Only the empty `variant` field is carried.
- MCP tools for analytics. The catalog is a contract, so new tools come
  separately. The HTTP API works with an account API token, which covers
  scripts such as a weekly report.
- Analytics for sites not hosted on Sitebin.
- A world map. Journeys, retention cohorts, revenue, attribution reports,
  public share links, CSV export, e-mail reports, `umami.identify` /
  session data, heatmaps and replays.

## Anti-goals: what makes this a failure even if it works

- Anything that can **link a visitor across sites or across days**, even
  for us.
- Anything that needs a **cookie banner** on a customer's site.
- Any **IP address** on disk, any full referrer query, any form value.
- Any **third-party request**, and any **CSP relaxation**. The untrusted
  policy already allows `script-src 'self'` and `connect-src 'self'`, and
  that is all this needs.
- An open endpoint that can **fill the disk** or flood the operator.
- **Slowing page serving.** authz may do no more than hand a small record
  to a buffered channel.
- Collecting anything for a site whose owner never turned analytics on.

## How it fits together

```
 browser ──GET page──► Caddy ──forward_auth──► authz ──record (server mode)──┐
 browser ──POST /_sitebin/analytics/send─────► backend ──record (script)─────┤
 browser ──POST /_sitebin/forms/<key> ───────► backend ──record "sitebin:form"┤
                                                                              ▼
                                         recorder (buffered channel, one writer goroutine)
                                         parse UA, GeoIP, hash, sanitize, cap
                                                                              ▼
                       sites/<id>/analytics/events/<local-date>.jsonl  (one line per hit)
                                                                              ▼
                       query engine (day summaries cached, raw scan for filters/funnels)
                                                                              ▼
                       GET /api/sites/{id}/analytics/...  ──►  edit page "Analytics" tab
```

New core package **`internal/analytics`**: the record format, sanitizing,
UA parsing, channels, GeoIP lookup, salts, the recorder, the query engine,
funnels and goals, and the Umami import conversion. It is pure logic plus
file I/O under a site folder it is handed, so it never imports `httpapi`.
The `httpapi` package wires it in: authz, the send and script routes, the
forms hook and the API. The `cleanup` package adds the stamp reconcile and
retention.

## Plan gate and instance switches

Everything here copies the Git feature
(`2026-11-03-site-git-design.md`, "Plan gate"):

- **Tier flag.** `analytics` (bool) in `tiers.json`; absent means false.
  `eeconfig.Tier.Analytics`, carried by `ext.CreateGrant.Analytics *bool`
  and `store.Quota.Analytics *bool`, and stamped as
  `Meta.QuotaAnalytics *bool` (`json:"quota_analytics,omitempty"`). It is
  written:
  - at creation (`stampCaps`, and wherever creation writes stamp fields);
  - by `ApplyQuota`. Every constructor of `store.Quota` passes it, or
    `ApplyQuota` resets it;
  - by a sweep reconcile, `reconcileAnalytics`, built like `reconcileGit`:
    it writes only `QuotaAnalytics` and nothing else, and does nothing on
    an error or an unknown tier.

  `grantFromTier` copies it. Accounts mode without tiers grants `true`.
  Transfer and adoption stamp it through the existing paths.
- **In effect for a site** when all of these hold:
  - the instance switch is on;
  - the site is not locked;
  - the plan allows it: with accounts enabled `QuotaAnalytics == true`
    (nil counts as off); without accounts (community, enterprise open mode)
    nil or true;
  - **the site's own setting `enabled` is true.** It is false by default.
    Nothing is collected until the owner or an admin turns it on.
- **Downgrade.** Collection stops. Stored data and settings stay and stay
  readable, and the tab says why nothing new arrives, with the upgrade
  link. Nothing is deleted for a plan.
- **Hosted values.** `pro`, `studio`, `admin` and `unlimited` get
  `"analytics": true`; `free` and `drop` do not. This is an operator step
  in the ship order.
- **Instance configuration** (README table, `internal/config`):

  | Variable | Default | Meaning |
  |---|---|---|
  | `SITEBIN_ANALYTICS` | `true` | instance switch; off = no collection, the tab is hidden, stored data untouched |
  | `SITEBIN_ANALYTICS_TIMEZONE` | `UTC` | the default time zone of a site's analytics (hosted: `Europe/Vienna`) |
  | `SITEBIN_ANALYTICS_GEOIP` | unset | path to a MaxMind-format `.mmdb` (country or city). Unset = no location |
  | `SITEBIN_ANALYTICS_DAILY_CAP` | `50000` | records per site per local day; the rest is counted, not stored |
  | `SITEBIN_ANALYTICS_RETENTION_DAYS` | `760` | raw days older than this are deleted by the sweep; 0 = keep |

  The instance never downloads a GeoIP database by itself, so it makes no
  surprising outbound traffic. The docs give the operator a one-line
  monthly cron for DB-IP's free "IP to City Lite" `.mmdb` (CC BY 4.0). The
  dashboard shows the attribution the licence requires, taken from the
  database's own metadata description.

## A site's settings

`Meta.Analytics *store.Analytics` (`json:"analytics,omitempty"`). nil means
all defaults (off).

| Field | Meaning |
|---|---|
| `enabled bool` | the owner's switch (default false) |
| `mode string` | `"server"` (default) or `"script"`, below |
| `timezone string` | IANA zone; default `SITEBIN_ANALYTICS_TIMEZONE`. Day files and day buckets use it; a change applies from the next record on |
| `addresses []string` | the site's addresses to count (the generated address, custom domains, `host/path` addresses). Empty = all serving addresses |
| `exclude []string` | path globs never counted, max 50; `*` = any characters except `/`, `**` = any characters. Matched against the decoded path |
| `query []string` | extra query parameter names to keep, max 20, besides the always-kept `utm_*` |
| `funnels []Funnel` | max 20. `{id, name, steps: [{type: "path"\|"event", value}] (2–8), window_minutes (1–1440, default 60)}`. This is Umami's own shape, so the importer copies them 1:1 |
| `goals []Goal` | max 30. `{id, name, type: "path"\|"event", value}`, Umami's shape |

Changes go through `PUT /api/sites/{id}/analytics` (below). They are
validated (globs, zone names, address membership, limits) and recorded in
provenance as a new `ActionAnalyticsSettings`. Owner and admin may both
change them (the ownership design's `accountStanding`; nothing here is
owner-only), and so may anyone who passes `withEditAuth`. authz reads them
from the site it already loaded, so no extra I/O is needed.

## Collection

### Two modes

- **`server` (default).** authz records a page view for every admitted
  request that looks like a real browser navigating to a page (rules
  below). An optional script on the page may add **events**. Page views
  the script sends are ignored in this mode, so nothing is counted twice.
  This mode needs no change to the site at all.
- **`script`.** authz records nothing. Page views, SPA route changes,
  screen size and page title come from the script, and so do events. This
  matches Umami's numbers, because bots that run no JavaScript are not
  counted, and the site decides on which pages the script loads. Rombalu
  keeps its kids-view rule this way, unchanged.

Either way, a successful form submission (`/_sitebin/forms/<key>`, 303 to
thanks) records an event **`sitebin:form`** with data `{form: <key>}`, and
never with any value. Goals and funnels can then use form submissions in
both modes, without a script.

### Server mode: what counts as a page view

Recorded only when **all** of these hold:

- The request is admitted by authz for a file site or a container site:
  - it passed the lock, the expiry, the address rules and the view
    password;
  - redirects, gate pages and 404 answers are not counted.
- The method is `GET`.
- The request looks like a browser navigation:
  - `Accept` contains `text/html`;
  - no `Sec-Purpose` / `Purpose` header names `prefetch` or `prerender`;
  - when `Sec-Fetch-Dest` is present it is `document`, and when
    `Sec-Fetch-Mode` is present it is `navigate`.
- `User-Agent` contains `Mozilla/` and matches no entry in the bot list.

  The list lives in `internal/analytics/bots.go` and covers crawlers,
  link-preview fetchers, monitors, headless browsers and HTTP libraries
  (`bot`, `crawl`, `spider`, `slurp`, `facebookexternalhit`, `WhatsApp`,
  `Slackbot`, `TelegramBot`, `Discordbot`, `HeadlessChrome`, `Lighthouse`,
  `curl`, `wget`, `python-`, `Go-http-client`, `uptime`, `monitor` and
  similar; case-insensitive).
- **File sites only:** the path resolves to a page, with the same
  resolution `cacheValue` makes: an existing regular file, a directory with
  an index, or the SPA fallback on an SPA site. A missing file without SPA
  is a 404 and is not counted.
- The address is in `addresses` (when set), the path matches no `exclude`
  glob, and the request carries no opt-out cookie (below).

### The script: `/_sitebin/analytics.js`

Served by the backend on every site address (Caddy already hands
`/_sitebin/*` to the backend before the gate, for file and container sites
and under path mounts), so it is **same-origin**:

- `script-src 'self'` covers it, and its beacon to
  `/_sitebin/analytics/send` is covered by `connect-src 'self'`;
- no CSP changes, no CORS, no third party.

It is ours: a small, dependency-free file in `web/static/`. It is not a
vendored copy of Umami's tracker, because Umami's posts to `<origin>/api/send`,
which collides with a site's own `/api` (Rombalu has one).

Its compatibility contract with Umami's tracker (Umami 3.0.2 docs and
behaviour):

- `window.umami.track()` sends a page view. `track(name)`,
  `track(name, data)`, `track({…props})` and `track(fn)`, where `fn`
  receives the default props and returns the payload, behave as in Umami.
  `umami.identify(...)` exists and does nothing.
- Clicks on elements with `data-umami-event="name"` send that event, with
  data from every `data-umami-event-<key>` attribute. As in Umami, a link
  click delays navigation only when it is not opening a new tab, and never
  beyond a short timeout.
- Attributes on its own `<script>` tag:
  - `data-auto-track="false"` stops the automatic page views and the SPA
    tracking;
  - `data-domains="a,b"` sends only on those hostnames;
  - `data-tag` is sent as `tag`;
  - `data-exclude-search="true"` and `data-exclude-hash="true"` work as in
    Umami;
  - `data-do-not-track="true"` honours `navigator.doNotTrack`;
  - `data-before-send="fnName"` calls `window[fnName](type, payload)`, and a
    falsy return drops the send;
  - `data-website-id` and `data-host-url` are accepted and ignored. The
    endpoint is always this origin's `/_sitebin/analytics/send`.
- `localStorage.getItem("umami.disabled")` set to anything truthy sends
  nothing. So does our own opt-out flag, below.
- SPA support: `history.pushState`, `replaceState` and `popstate` send a page
  view when the URL changes, as Umami does.
- The payload is Umami's shape, so a `before-send` hook written for Umami
  keeps working:

  `{type: "event", payload: {hostname, language, referrer, screen, title,
  url, name?, data?, tag?}}`

  It is sent with `fetch(..., {keepalive: true})` and as `text/plain`
  (no preflight even if the origin rule ever changed).
- **When analytics is not in effect for the site**, the backend serves a
  stub that defines `umami.track` / `umami.identify` as no-ops. Pages
  never throw, and nothing is sent.

Migrating a page means replacing
`<script src="https://umami.ittrail.cloud/script.js" data-website-id=…>`
with `<script defer src="/_sitebin/analytics.js" …same data attributes…>`.

### The send endpoint: `POST /_sitebin/analytics/send`

- It finds the site by address with `servingSiteByAddress`, and refuses a
  locked site, analytics not in effect, or an address not in `addresses`:
  `204` with no body every time. A beacon learns nothing about the site.
- The body is at most 8 KiB and must be JSON in Umami's shape. In server
  mode a payload without `name` (a page view) is dropped. The payload's
  `url` must be on the requesting host (and under its mount), or it is
  dropped.
- **Limits** (silently dropped beyond them):
  - an event name of 50 characters;
  - 20 data properties, with keys up to 50 characters and values that are
    strings up to 200 characters, numbers or booleans. Nested values are
    dropped;
  - a title of 200 characters;
  - a path of 300 characters;
  - a screen of `\d{2,5}x\d{2,5}`;
  - a language of 35 characters;
  - a tag of 50 characters.
- **Rate.** A token bucket per (site, client network) of 60 records a
  minute, with a burst of 30, held in memory with an idle TTL. The network
  is the IPv4 `/32` or the IPv6 `/64`, from `auth.ClientIP`.
- `Sec-Fetch-Site`, when present, must be `same-origin`. This is not a
  security boundary, since any non-browser can forge it. It only keeps
  other sites' pages from sending. A forged record can only distort the
  site's own statistics, which the caps bound.
- The script's own bot check: `navigator.webdriver` true sends nothing.

### Opt-out ("don't count me")

`GET /_sitebin/analytics/opt-out` on a site address shows a small page
with one button. `POST` sets the cookie
`sitebin_analytics_optout=1` (Path `/`, or the mount; 1 year; `Secure`;
`SameSite=Lax`; `HttpOnly`), and the page also sets
`localStorage["umami.disabled"] = "1"`, so the script sends nothing either.
A second button removes both.

The cookie is set only at the visitor's explicit request and serves only
that request, so it needs no consent. authz checks it in server mode. The
Settings sub-tab links this page for each counted address. The site's own
`?statistik=aus` keeps working through `umami.disabled`.

### Sanitizing: what a record may hold

- **Path:**
  - the URL path only, without query and fragment;
  - percent-decoded for exclude matching, stored as received, cut at 300;
  - on a `host/path` address, the full path the visitor saw.
- **Query:** only `utm_source`, `utm_medium`, `utm_campaign`, `utm_term`,
  `utm_content` and the site's `query` names, each value cut at 100.
  Click ids (`gclid`, `gbraid`, `wbraid`, `fbclid`, `msclkid`, `ttclid`,
  `li_fat_id`, `twclid`) are kept as **names only** (that they were
  present), never their values.
- **Referrer:**
  - kept only when its host is not one of the site's own addresses;
  - stored as `host` + path, with no query and no fragment, cut at 200;
  - a `android-app://` or other non-http(s) referrer is kept as its scheme
    plus host.
- **No IP, no full user agent.** The UA is parsed into browser, OS and
  device in the recorder and then dropped.
- **No cookie value, no header dump.**

## The record

One JSON object per line in
`sites/<viewID>/analytics/events/<YYYY-MM-DD>.jsonl`, where the date is the
site's local date in its time zone. Keys are short because the files are
the database:

| Key | Meaning |
|---|---|
| `t` | Unix time, milliseconds |
| `k` | `"p"` page view, `"e"` event |
| `s` | source: `"srv"` server, `"js"` script, `"um"` imported from Umami |
| `a` | the address: host, or `host/path` |
| `p` | path |
| `ti` | page title (script only) |
| `q` | kept query parameters, `{name: value}` |
| `ck` | click-id names present, `["gclid"]` |
| `r` | external referrer `host/path` |
| `n`, `d` | event name and data |
| `v` | visitor id (below) |
| `vi` | visit id (only on imported records; otherwise derived) |
| `b`, `o`, `dv` | browser, OS, device (`desktop`, `laptop`, `tablet`, `mobile`) |
| `sc` | screen `WxH` (script only) |
| `l` | language tag as the browser sent it (first of `Accept-Language`) |
| `c`, `rg`, `ct` | country (ISO 3166-1 alpha-2), region (ISO 3166-2 subdivision code), city |
| `tg` | tag |
| `vr` | **variant, always `""` in this version**: the A/B hook |

Empty fields are omitted. The format version is the directory name
(`events/`). A future incompatible change uses a new directory and never
rewrites old lines.

### Visitors, salts and visits

- **Visitor id** = the first 16 hex characters of
  `sha256(salt[date] ‖ 0 ‖ siteViewID ‖ 0 ‖ clientIP ‖ 0 ‖ userAgent)`.
  The salt is 32 random bytes per calendar date, kept only in
  `<data>/analytics/salts/<date>` (0600) and deleted once that date is over
  in every time zone (two days later; the recorder and the sweep both
  enforce it).

  Once a salt is gone, nobody, us included, can tie an id to a person or
  to another day, and the site id in the hash keeps ids from linking across
  sites. This is the Plausible model.

  The consequence: "visitors" over a range is the **sum of daily unique
  visitors**, and a funnel or goal links steps only within one local day.
  The dashboard says so in a tooltip.
- **Visit:** consecutive records of one visitor on one day with gaps under
  30 minutes. It is derived at read time; imported records carry Umami's
  `visit_id` as `vi` and use it instead.

### Parsing

- **Browser and OS** come from a small own parser
  (`internal/analytics/ua.go`, table-tested). Names: `Chrome`, `Edge`,
  `Firefox`, `Safari`, `Opera`, `Samsung Internet`, `Chrome (iOS)`,
  `Firefox (iOS)`, `Android WebView`, `Yandex`, `Other`; `Windows`,
  `macOS`, `iOS`, `iPadOS`, `Android`, `Linux`, `ChromeOS`, `Other`.
  Low-entropy client hints (`Sec-CH-UA-Mobile`, `Sec-CH-UA-Platform`) win
  when present.
- **Device:** in server mode, mobile or tablet from the hints and the UA,
  else `desktop`. In script mode with a screen, as Umami does: a mobile OS
  gives `mobile` (width < 768) or `tablet`; a desktop OS gives `laptop`
  (width ≤ 1440) or `desktop`.
- **Location** comes from `github.com/oschwald/maxminddb-golang/v2`, when
  `SITEBIN_ANALYTICS_GEOIP` is set (a country database fills `c` only). It
  is looked up in the recorder; the IP is never written.
- **Channel**, at read time, from the entry record of a visit:
  - **Paid search:** a click id, or a `utm_medium` of
    `cpc|ppc|paid|paidsearch` with a search source;
  - **Paid social:** `utm_medium` of `paid-social|paidsocial|social-paid`,
    or `fbclid` with paid, or a paid medium with a social source;
  - **Email:** `utm_medium` of `email|e-mail|newsletter`, or a mail host
    referrer;
  - **Organic search:** a search engine referrer (Google, Bing,
    DuckDuckGo, Yahoo, Ecosia, Qwant, Yandex, Baidu, Startpage, Brave);
  - **Organic social:** a social referrer or `utm_medium=social` (Facebook,
    Instagram, LinkedIn, X/Twitter/t.co, YouTube, Pinterest, Reddit,
    TikTok, WhatsApp, Telegram, Xing);
  - **Other campaign:** any other `utm_*`;
  - **Referral:** another referrer;
  - **Direct:** nothing.

  The host lists live in one Go file.

### The recorder

- authz and the handlers call `recorder.Add(hit)` with the raw request
  facts and the site's view id. It is a non-blocking send on a buffered
  channel (8192). When the channel is full the hit is dropped and counted;
  the count is logged at most once a minute.
- **One writer goroutine** does all parsing, hashing, GeoIP and
  sanitizing. It groups by site and appends at most every second or every
  256 records, under the site's **stats lock** (`lockStats`, the lock
  `RecordView` uses). It never creates a site folder: when the site folder
  is gone the batch is dropped. `Delete` takes the site lock and then the
  stats lock, so a write can never resurrect a deleted site.
- **Daily cap.** Records beyond `SITEBIN_ANALYTICS_DAILY_CAP` for a site's
  local day are not written. They are counted in
  `events/<date>.over` (a single integer), and the dashboard shows
  "N records over the daily limit were not stored".
- On shutdown the writer drains the channel with a short deadline.

## Storage, lifecycle and size

- Analytics lives in `sites/<viewID>/analytics/`, beside `meta.json` and
  outside `files/`. It is never served, listed, zipped or counted against
  the site's byte quota (it is ours, bounded by the cap and the retention).
  `sitebin backup` includes it.
- **Deleting a site deletes its analytics; so does its expiry.** A
  transfer keeps them with the site. A locked site records nothing and
  keeps what it has until it is purged. Analytics is about a site's
  visitors, not about the account holder, so it is not part of an
  account's GDPR export.
- **Retention.** The sweep deletes day files (and their `.sum.json` and
  `.over`) older than `SITEBIN_ANALYTICS_RETENTION_DAYS` in the site's
  time zone.
- **Day summaries.** The first query that needs a closed day (before
  today in the site's zone) builds `events/<date>.sum.json`, holding:
  - the totals;
  - an hourly series;
  - per-dimension counts of views, visitors and visits, and of
    events with their data keys and values (top 1000 values per dimension;
    the rest summed into `(other)`).

  It carries the source file's size and mtime and is rebuilt when they
  differ, for example after an import. Today is always scanned raw. A
  filtered query, a funnel or a goal scans raw days, with a parsed-day
  LRU in memory (64 MiB). Ranges are capped at the retention or 760 days,
  whichever is smaller.

## Reading: metrics as Umami defines them

- **Views:** page-view records. **Visitors:** the sum of daily distinct
  `v`. **Visits:** as above.
- **Bounce rate:** visits with exactly one page view, divided by visits.
- **Visit duration:** the time from first to last record of a visit,
  averaged over visits (a bounce counts 0).
- **Compare:** the previous period of the same length.
- **Ranges** are local dates in the site's zone, inclusive, or
  `last24h` / `last30m`. The series unit is the hour up to 2 days, the day
  up to 92 days, and the month beyond.
- **Filters** are `dim=value`, several ANDed:
  - **visit-level dimensions** (entry, exit, referrer, channel, `utm_*`,
    click id, country, region, city, browser, OS, device, language,
    screen, address) select whole visits;
  - **record-level dimensions** (path, title, event, event data, tag)
    select records.

  So "utm_source=meta" plus the events table shows the events of Meta
  visits, the operator's own report.
- **Funnels.** For each visitor-day, the ordered first matches of each
  step, each later step within `window_minutes` of step 1. The result per
  step is visitors, drop-off and conversion. Path values are globs, as in
  Umami (`/app-entwickeln-lassen/*`).
- **Goals.** The visitors who matched at least once, the number of
  completions, and the conversion rate over all visitors in the range.

## API

Every route sits under `withEditAuth` (so the owner, an admin, an edit
password or an account token; an MCP OAuth token never) and refuses a
locked site except for reading settings. Dates are local to the site's
zone.

| Route | Answer |
|---|---|
| `GET /api/sites/{id}/analytics` | `{available, reason?, upgrade_url?, settings, addresses (all serving), geo: {present, attribution?}, retention_days, daily_cap, bytes, first_day?, imported: [{from,to}]}`. `reason` is `instance`, `plan` or `locked` |
| `PUT /api/sites/{id}/analytics` | the whole settings object, validated → `200` with the new state; `422 {error, field}` on a bad value |
| `GET …/analytics/overview?from&to&filter=dim:value…&compare=1` | `{totals: {views, visitors, visits, bounce_rate, duration_s}, previous?, series: [{t, views, visitors}], unit, over_cap}` |
| `GET …/analytics/table?dim=…&from&to&filter…&limit≤500&offset` | `{rows: [{value, visitors, views}], total}`; for `dim=event`: `{value, count, visitors}` |
| `GET …/analytics/event-data?name=…&from&to&filter…` | `{keys: [{key, values: [{value, count}]}]}` |
| `GET …/analytics/realtime` | the last 30 minutes: `{active (distinct v, last 5 min), per_minute: [{t, views}], pages, referrers, countries, latest: [{t, k, p\|n, c, b, dv}] (50)}` |
| `GET …/analytics/funnels?from&to&filter…` | the saved funnels' results |
| `GET …/analytics/goals?from&to&filter…` | the saved goals' results |

`dim` values: `path`, `entry`, `exit`, `title`, `referrer`, `channel`,
`utm_source`, `utm_medium`, `utm_campaign`, `utm_term`, `utm_content`,
`query:<name>`, `click_id`, `browser`, `os`, `device`, `screen`,
`country`, `region`, `city`, `language`, `address`, `tag`, `event`.

## The tab

The tab is registered after Git, as `{id: "analytics", label: "Analytics",
panel: $("panel-analytics"), onShow}`, in a new `web/static/edit-analytics.js`
loaded after `edit.js`, like `edit-git.js`. The tab shows when analytics is
available or only the plan lacks it; in that case it shows the upgrade
notice. It follows the edit page's dark/amber look.

**Display safety, as in the Git tab:** every value in a record is chosen by
visitors (paths, referrers, titles, event names and data, UTM values). It
enters the DOM only through `textContent` / `createTextNode`, never through
`innerHTML`, and no `href` is built from it except an `https://` referrer
link, which is built through `URL` and opens with
`rel="noopener noreferrer nofollow"`.

**Header** (above the sub-tabs, kept across them):

- the range picker: Today, Last 24 hours, Yesterday, This week, Last
  7 days, This month, Last 30 days, Last 90 days, This year, Last 12
  months, Custom;
- "Compare with previous period";
- the filter chips. Clicking any table row adds a filter; × removes it.

The state is in the hash (`#analytics/overview?from=…`) so a view can be
linked.

**Sub-tabs:**

1. **Overview**, Umami's website page:
   - the metrics bar (Visitors, Visits, Views, Bounce rate, Visit
     duration, each with its change);
   - the bar chart of views with visitors over time;
   - Pages | Entry | Exit;
   - Sources: Referrers | Channels | UTM, the last with selectors for
     source, medium, campaign, term and content;
   - Environment: Browsers | OS | Devices | Screens;
   - Location: Countries | Regions | Cities, with the GeoIP attribution;
   - Languages;
   - Events.

   Each table shows its top 10 with a share bar and "More", which opens
   the full list with paging.
2. **Events:** event names with counts, visitors and a chart. Selecting
   one shows its data keys and values.
3. **Funnels:** the saved funnels as step bars with drop-off, plus an
   editor (name, steps as path glob or event name, window).
4. **Goals:** the saved goals with completions, converted visitors and the
   rate, plus an editor.
5. **Realtime:** active visitors, views per minute, top pages, referrers
   and countries, and the latest 50 records. It polls every 10 s while
   visible and stops when the tab or sub-tab is left or the page is
   hidden.
6. **Settings:**
   - the switch;
   - the mode, with a sentence on each;
   - the script snippet with a copy button, and a line on migrating from
     Umami;
   - the addresses to count;
   - exclude globs;
   - extra query parameters;
   - the time zone (from `Intl.supportedValuesOf("timeZone")`);
   - the opt-out link per address;
   - storage (size, first day, retention, imported ranges);
   - a privacy-policy paragraph to copy, in English and German.

**Empty states:**

- off → the switch and one sentence on what turning it on does;
- on with no data yet → "Nothing recorded yet", with the mode's hint (in
  script mode: "add the script").

## Umami import, per site

- **CLI:** `sitebin analytics import-umami <siteViewID> <events.jsonl>
  [--reports <reports.json>]`. It runs inside the container (`docker exec
  sitebin sitebin analytics …`) while the server runs; it writes under the
  stats lock, as the CLI already does for other per-site writes.
- **Input.** One JSON object per Umami `website_event`, joined with its
  `session` and aggregated `event_data`. The SQL that produces it lives in
  the runbook below. It is run against Umami's Postgres for exactly one
  `website_id`.
- **Conversion:**
  - `created_at` → `t`; the local date in the site's zone picks the file;
  - `event_type` 1 → `k:"p"`, 2 → `k:"e"`, with `event_name` → `n` and
    `event_data` → `d`;
  - `hostname` → `a`; `url_path` → `p`; `utm_*` → `q`, plus the site's
    `query` names taken from `url_query`;
  - the click-id columns that are non-empty → `ck`;
  - `referrer_domain` + `referrer_path` → `r`, unless the domain is one of
    the site's addresses;
  - `page_title` → `ti`; `tag` → `tg`; `visit_id` → `vi`;
  - from the session: `browser` / `os` / `device` mapped to our names
    (`chrome` → Chrome, `ios` → Safari on iOS, `crios` → Chrome (iOS),
    `edge-chromium` → Edge, `Mac OS` → macOS, `Windows 10` → Windows,
    `Android OS` → Android, and so on), `screen` → `sc`, `language` →
    `l`, `country` → `c`, `region` → `rg`, `city` → `ct`;
  - `v` = the first 16 hex of `sha256("umami" ‖ 0 ‖ session_id ‖ 0 ‖
    localDate)`, so imported ids are per-day too;
  - `s:"um"`.
- **Idempotent.** For each day it touches, the importer rewrites the file
  with every existing `s:"um"` line removed and the imported ones merged
  in by time. Lines from `srv` / `js` are kept, and the day's summary is
  dropped. Running it twice gives the same files. It reports the days and
  records written and the range, and appends the range to
  `analytics/imported.json`.
- **Reports.** `--reports` takes Umami's `report` rows (`type`, `name`,
  `parameters`) for the website. `funnel` and `goal` rows are added to the
  site's settings when no funnel or goal of the same name exists. Other
  report types are listed as skipped.
- **The daily cap does not apply to an import.** Retention does: days
  older than the retention are skipped and reported.

### Runbook: importing one site

Kept in the product docs (`docs/analytics-umami-import.md`) with the exact
commands:

1. Find the Umami `website_id` by domain. Umami runs on srv03 VM 103,
   container `0007-1-umami-db-1`; use `qm guest exec 103 --pass-stdin 1 --
   docker exec -i … psql`.
2. Export the events with the documented `\copy (select json_build_object(…)
   …) to stdout`, and the reports likewise.
3. Copy both files to the Sitebin host, then `docker cp` them into the
   container.
4. Run the import. Check the dashboard against Umami's totals for a few
   days; the numbers must match, and visitors are per-day sums.
5. Delete the copies.

## A/B preparation

- Every record carries `vr`. The recorder writes `""` today.
- The visitor id, the visit derivation and the per-dimension machinery
  treat `vr` as an ordinary visit-level dimension, so a later A/B design
  only has to:
  - let authz pick a variant folder;
  - stamp `vr`;
  - add `variant` to `dim` and the filters.
- No `variant` dimension appears in the API or UI now.

## Security and privacy summary

| Threat / duty | Answer |
|---|---|
| Visitor tracking across sites or days | per-site, per-day salted ids; salts deleted after two days; no cookie |
| Personal data on disk | no IP, no UA string, no referrer query, no click-id values, no form values; event data bounded and shown only to the site's managers |
| Consent banner needed on customer sites | no device access except the opt-out cookie the visitor asks for; the docs give the owner a privacy-policy paragraph (the owner is the controller, Sitebin processes on their behalf) |
| CSP weakened | no; `'self'` already covers the script and the beacon |
| XSS in the dashboard | record values only through `textContent`; tested with hostile values |
| Disk exhaustion through the open endpoint | per-site daily cap, per-network rate, body and field limits, retention |
| Page latency | authz does a non-blocking channel send only |
| Data poisoning | it can only touch the site's own numbers and is bounded by the caps; same-origin and `Sec-Fetch-Site` checks keep honest browsers on other sites out |
| Locked (abuse) sites | nothing recorded, the endpoint answers 204, the data is kept as part of the evidence folder |
| Who reads it | `withEditAuth`: owner, admin, edit password, account token; never an MCP OAuth token |

## Docs, website and ship order

- **Product:**
  - README: the env vars, the Analytics section and the script
    compatibility;
  - `CLAUDE.md`: a short section with the rules a future change must keep
    (anti-goals, recorder lock, the variant hook);
  - the import runbook.
- **Website:**
  - a new `/docs/analytics/` page: turning it on, the two modes, the script
    and its Umami compatibility, exclusions, opt-out, retention, import on
    request, and the privacy paragraph;
  - the docs index;
  - the **pricing page**: "Analytics (Umami-compatible)" on Pro and
    Studio, not on Free;
  - the **privacy page and terms**: a sentence that Sitebin processes
    visitor statistics on behalf of site owners who turn analytics on
    (the operator approves this wording before the push).
- **Ship order** (workspace rule):
  1. merge and push the product, and check CI;
  2. deploy app.sitebin.io;
  3. put `"analytics": true` in `tiers.json` for pro, studio, admin and
     unlimited, and set `SITEBIN_ANALYTICS_TIMEZONE=Europe/Vienna` and
     `SITEBIN_ANALYTICS_GEOIP`, with the DB-IP file plus its cron;
  4. restart; wait for the sweep stamp (or the restart sweep) and verify
     on a test site:
     - server mode counts a browser and not curl;
     - script mode receives events;
     - the opt-out works;
  5. push the website.

  The morgenfertig and rombalu moves, each with its import, happen
  afterwards on the operator's request, one site at a time.

## Testing

- **Unit** (`internal/analytics`, table-driven):
  - sanitizing (query allowlist, click ids, referrer rules, limits);
  - the bot list and navigation detection;
  - the UA and client-hint parser;
  - channels;
  - salts and visitor ids (same day same id, next day different, other
    site different);
  - the recorder (batching, cap and `.over`, a deleted site folder is
    never recreated, the drop counter);
  - the query engine over fixture days: every metric, filters at visit
    and record level, compare, the units, time-zone days, and summaries
    equal to the raw scan;
  - funnels and goals against Umami's semantics;
  - import conversion and idempotency.
- **httpapi:**
  - authz records only admitted navigations (lock, gate, 404, prefetch,
    bot, exclude, address, opt-out, mode);
  - send-endpoint limits and modes;
  - the stub script when off;
  - settings validation and provenance;
  - the gate per edition (community nil=on; accounts needs the stamp);
  - `reconcileAnalytics`.
- **e2e:** a new `e2e/analytics.ps1` (community image, ASCII only):
  - turn analytics on;
  - browser-like and bot requests;
  - script mode with a page using `data-umami-event` and `umami.track`
    driven through the send endpoint;
  - opt-out;
  - the overview, table and funnel answers.
- **Browser:** the tab checked in a real browser (Playwright or Chrome)
  with seeded data, including hostile values rendered as text.

## Left to the builder

The exact chart drawing (hand-made SVG; no chart library is vendored), the
CSS, table paging sizes, the bot and host lists' contents beyond the named
entries, struct and file names inside `internal/analytics`, the LRU sizing,
and how summaries are encoded.

## Calls made without asking (the operator delegated the technical side)

- Off by default, per site, so nothing is collected that the owner did not
  turn on.
- Two modes, with server page views ignored from the script in server
  mode (no double counting); `sitebin:form` events in both.
- A daily salt and per-day visitors (Plausible model) instead of Umami's
  longer-lived session ids. It is more private, and the cost is no
  cross-day uniques.
- Our own tracker with Umami's API, not a vendored Umami script, because of
  the `/api/send` collision.
- The site's own time zone for days (hosted default Europe/Vienna).
- No automatic GeoIP download; the operator provides DB-IP's file.
- A raw retention of 760 days (25 months, so last year can be compared)
  and a daily cap of 50 000 records per site.
- Visits split at 30 minutes of inactivity.
- No MCP tools, world map, CSV or share link in this version.
