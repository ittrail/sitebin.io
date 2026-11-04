# Site Analytics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Umami-like per-site analytics. It collects data on the server and
through a Umami-compatible script, stores it as per-site day files, and
shows it in an "Analytics" tab on the edit page with sub-tabs. A
per-site Umami importer is included. A/B is not built; only an empty
`vr` field is carried.

**Architecture:** A new core package `internal/analytics` holds the pure
logic and file I/O:
- the record format, sanitizing, bots, UA parsing and channels;
- salts and GeoIP;
- the recorder: a buffered channel feeding one writer goroutine;
- the query engine with day summaries, funnels and goals;
- the Umami conversion.

`internal/httpapi` wires it into authz, `/_sitebin/analytics*`, forms and
the API. `internal/store` holds the settings and the stamp. `cleanup`
reconciles the stamp and applies retention. The plan gate copies the Git
feature's stamped flag.

**Tech Stack:** Go 1.25 (stdlib, `time/tzdata`,
`github.com/oschwald/maxminddb-golang/v2`), vanilla JS in `web/static/`
(no build step), PowerShell e2e.

**Spec:** `docs/superpowers/specs/2026-11-04-site-analytics-design.md`. Read
it before any task; it is the source of every rule below.

## Global Constraints

- Community feature (MIT, `internal/`). No `ee/` import from core. The tier
  flag reaches core only through `ext.CreateGrant.Analytics`.
- Defaults:
  - `SITEBIN_ANALYTICS=true`;
  - `SITEBIN_ANALYTICS_TIMEZONE=UTC`;
  - `SITEBIN_ANALYTICS_GEOIP` unset;
  - `SITEBIN_ANALYTICS_DAILY_CAP=50000`;
  - `SITEBIN_ANALYTICS_RETENTION_DAYS=760`.
- A site's analytics is off until its settings say `enabled: true`. Never
  collect otherwise.
- Never write: an IP, a full UA, a referrer query, a click-id value, a
  form value, a cookie. Never add a CSP source, never make a third-party
  request.
- authz does a non-blocking `Recorder.Add` and nothing else for analytics.
- On-disk paths:
  - `sites/<viewID>/analytics/events/<YYYY-MM-DD>.jsonl`, plus `.sum.json`
    and `.over`;
  - `sites/<viewID>/analytics/imported.json`;
  - `<data>/analytics/salts/<YYYY-MM-DD>`, mode 0600.
- Record keys exactly as the spec table: `t k s a p ti q ck r n d v vi b o
  dv sc l c rg ct tg vr`. `vr` is always `""` in this version, and empty
  fields are omitted. Note that `vr` itself is omitted when empty, but the
  field exists in the struct.
- Limits:
  - send body 8 KiB;
  - event name 50 characters;
  - 20 data properties, keys 50 characters, string values 200;
  - title 200; path 300; referrer 200; query value 100; tag 50;
    language 35;
  - screen `^\d{2,5}x\d{2,5}$`;
  - rate 60/min with a burst of 30 per (site, IPv4 /32 or IPv6 /64);
  - recorder channel 8192, flushed every 1 s or 256 records;
  - visit gap 30 min;
  - table `limit` at most 500;
  - funnels max 20 with 2 to 8 steps and a window of 1 to 1440 (default 60);
  - goals max 30; exclude max 50; query max 20;
  - realtime is the last 30 min, "active" the last 5 min, and 50 latest
    records;
  - summary values top 1000 per dimension, the rest as `(other)`;
  - parsed-day LRU 64 MiB.
- Keep `e2e/*.ps1` pure ASCII.
- `internal/analytics` imports `_ "time/tzdata"`, so `time.LoadLocation`
  works in the Alpine image without system zoneinfo. It may import
  `internal/store` (store never imports analytics); `analytics` never
  imports `httpapi`.
- UI copy is English. A record value enters the DOM only through
  `textContent` / `createTextNode`.
- Commits use lowercase conventional prefixes, end with `Co-Authored-By:
  Claude Opus 5.5 <noreply@anthropic.com>`, and the subject is a sentence
  about the behaviour.
- Run both `go test ./...` and `go test -tags ee ./...` before every commit
  that touches Go.

## Review Focus

1. **Hostile values in the dashboard.** An event named `<img src=x
   onerror=alert(1)>`, a path with `"><script>`, or a referrer of
   `javascript:alert(1)` must render as literal text, never as a link
   other than `https:`. Pinned in Task 13 (browser check with seeded
   data).
2. **A site deleted while the writer holds its batch.** The batch is
   dropped, and the folder is never recreated. Pinned in Task 6
   (`TestRecorderNeverRecreatesDeletedSite`).
3. **DST days in the site's zone.** Europe/Vienna 2026-03-29 has 23 hours
   and 2026-10-25 has 25. Hourly series and day files must neither skip
   nor double an hour. Pinned in Task 9 (`TestSeriesAcrossDST`).
4. **Garbage on the send endpoint.** Non-JSON, 9 KiB, nested data, a `url`
   on another host or outside the mount, or `type` other than `event`
   must give 204 and nothing stored. Pinned in Task 8
   (`TestSendRejectsGarbage`).
5. **Downgrade.** A site with `enabled: true` but `QuotaAnalytics=false`
   in accounts mode records nothing. Its old data stays readable through
   the API, and `GET …/analytics` says `reason: "plan"`. Pinned in
   Task 12 (`TestAnalyticsDowngradeKeepsData`).

---

### Task 1: Config switches and site settings

**Files:**
- Modify: `internal/config/config.go`. Add fields `AnalyticsEnabled
  bool`, `AnalyticsTimezone string`, `AnalyticsGeoIP string`,
  `AnalyticsDailyCap int`, `AnalyticsRetentionDays int`, parsed with
  `boolVar` / `intVar` and the defaults above. Validate the time zone
  with `time.LoadLocation` (an error refuses start).
- Create: `internal/store/analytics.go`, `internal/store/analytics_test.go`.
- Modify: `internal/store/meta.go`. Add `Analytics *Analytics
  json:"analytics,omitempty"` and `QuotaAnalytics *bool
  json:"quota_analytics,omitempty"`.
- Modify: `internal/config/config_test.go` (defaults and a bad zone).

**Interfaces (Produces):**

```go
type AnalyticsStep struct{ Type, Value string }          // json "type","value"
type AnalyticsFunnel struct{ ID, Name string; Steps []AnalyticsStep; WindowMinutes int } // "id","name","steps","window_minutes"
type AnalyticsGoal struct{ ID, Name, Type, Value string } // "id","name","type","value"
type Analytics struct {
    Enabled   bool              `json:"enabled"`
    Mode      string            `json:"mode"`      // "server" | "script"; "" = server
    Timezone  string            `json:"timezone"`  // "" = instance default
    Addresses []string          `json:"addresses,omitempty"`
    Exclude   []string          `json:"exclude,omitempty"`
    Query     []string          `json:"query,omitempty"`
    Funnels   []AnalyticsFunnel `json:"funnels,omitempty"`
    Goals     []AnalyticsGoal   `json:"goals,omitempty"`
}
func (a Analytics) Validate(siteAddresses []string) (field string, err error)
func (s *Store) SetAnalytics(site *Site, a *Analytics) error   // store.Update; nil clears
func (s *Store) SetQuotaAnalytics(site *Site, v *bool) error   // like SetQuotaGit
func (s *Store) WithStatsLock(viewID string, fn func(siteDir string) error) (found bool, err error)
// WithStatsLock takes lockStats(viewID); if <sites>/<viewID> does not exist it returns (false, nil) without calling fn.
func GlobOK(pattern string) bool                         // "*" segment, "**" any; must start with "/"
func GlobMatch(pattern, path string) bool
```

- [ ] **Step 1: Write failing tests.**
  - `TestAnalyticsValidate` is table-driven:
    - a mode outside server/script → field `mode`;
    - an unknown zone → `timezone`;
    - an address not in the site's list → `addresses`;
    - 51 excludes → `exclude`;
    - an exclude without a leading `/` → `exclude`;
    - a query name with space → `query`;
    - 21 funnels;
    - a funnel with 1 step;
    - a window of 0 or 1441;
    - a step type `x`;
    - 31 goals;
    - two funnels with the same id;
    - a valid full object → `("", nil)`.
  - `TestGlobMatch`:
    - `/kinder/**` matches `/kinder/a/b` and `/kinder/` but not `/kinderx`;
    - `/*/kids/**` matches `/en/kids/x`;
    - `/app-entwickeln-lassen/*` matches `/app-entwickeln-lassen/` and
      `/app-entwickeln-lassen/x` but not `/app-entwickeln-lassen/x/y`.
  - `TestWithStatsLockMissingSite` → `found == false`, fn not called.
  - `TestConfigAnalyticsDefaults`.
- [ ] **Step 2: Run** `go test ./internal/store/ ./internal/config/`. Expected FAIL (undefined).
- [ ] **Step 3: Implement** per the interfaces. Glob semantics: `*` = `[^/]*`, `**` = `.*`. A pattern ending in `/` also matches the path without the trailing slash's children? No: `/kinder/` matches only `/kinder/` itself. Compile patterns to anchored regexps.
- [ ] **Step 4: Run** `go test ./... && go test -tags ee ./...`. Expected PASS.
- [ ] **Step 5: Commit** `feat: a site carries analytics settings, validated in one place, and the instance has analytics switches`.

### Task 2: The plan gate (tier flag, stamp, reconcile)

**Files:**
- Modify:
  - `ee/eeconfig/eeconfig.go`: `Analytics bool json:"analytics,omitempty"` on `Tier`;
  - `internal/ext/ext.go:311`: `Analytics *bool` on `CreateGrant`;
  - `internal/store/expiry.go:72`: `Analytics *bool` on `Quota`, and `ApplyQuota` stamps `m.QuotaAnalytics = q.Analytics` next to `QuotaGit`;
  - `internal/httpapi/stamp.go` (`stampCaps`), `internal/httpapi/siteservice.go:399` (`quotaFromGrant`) and `internal/cleanup/cleanup.go:301` (`store.Quota{…}`): pass `Analytics`;
  - `ee/provider.go`: `grantFromTier` copies `t.Analytics`, and the accounts-mode grants at `:404` and `:421` set `Analytics: &on`;
  - `ee/transfer_accept.go:472`: likewise.
- Create:
  - `internal/cleanup/analytics.go` (`reconcileAnalytics`, modelled on `reconcileGit`, called next to it in `Sweep`);
  - `internal/httpapi/analyticsgate.go`.
- Test:
  - `internal/httpapi/analyticsgate_test.go`;
  - `internal/cleanup/analytics_test.go`;
  - `ee/…_test.go`: grant copies the flag.

**Interfaces (Produces):**

```go
// analyticsgate.go
const (analyticsReasonInstance = "instance"; analyticsReasonPlan = "plan"; analyticsReasonLocked = "locked")
func (a *API) analyticsPlanAllows(site *store.Site) bool // accounts: QuotaAnalytics != nil && true; else nil || true
func (a *API) analyticsReason(site *store.Site) string    // "" when available (ignores the site's own Enabled)
func (a *API) analyticsInEffect(site *store.Site) bool    // reason == "" && Meta.Analytics != nil && Meta.Analytics.Enabled
```

- [ ] **Step 1: Write failing tests.**
  - `TestAnalyticsPlanAllowsCommunity`: nil → true, false → false.
  - `TestAnalyticsPlanAllowsAccounts`, using the existing accounts-mode test env pattern from `gitstate` tests: nil → false, true → true.
  - `TestAnalyticsInEffectNeedsEnabled`.
  - `TestAnalyticsReasonLocked`.
  - `TestReconcileAnalyticsWritesOnlyTheStamp`: the expiry and byte cap are unchanged, and an error from `QuotaFor` changes nothing.
  - `TestGrantFromTierCopiesAnalytics` (ee).
- [ ] **Step 2: Run** both suites. Expected FAIL.
- [ ] **Step 3: Implement.** Grep for `QuotaGit` and `Git:` and mirror every site so that no constructor of `store.Quota` misses the field.
- [ ] **Step 4: Run** `go test ./... && go test -tags ee ./...`. Expected PASS.
- [ ] **Step 5: Commit** `feat: the analytics tier flag is stamped on a site like git, and the sweep backfills it`.

### Task 3: Sanitizing, bots and navigation detection

**Files:**
- Create: `internal/analytics/{record.go,sanitize.go,bots.go,sanitize_test.go,bots_test.go}`.

**Interfaces (Produces):**

```go
type Record struct { // json tags exactly the spec keys, all omitempty
    T int64; K, S, A, P, Ti string; Q map[string]string; Ck []string; R, N string
    D map[string]any; V, Vi, B, O, Dv, Sc, L, C, Rg, Ct, Tg, Vr string
}
var ClickIDs = []string{"gclid","gbraid","wbraid","fbclid","msclkid","ttclid","li_fat_id","twclid"}
func CleanPath(rawPath string) string                                   // no query/fragment, cut 300
func KeepQuery(rawQuery string, extra []string) (q map[string]string, clicks []string)
func CleanReferrer(ref string, own []string) string                     // "" when own address/empty; host+path, cut 200; non-http(s): scheme+"://"+host
func CleanEvent(name string, data map[string]any) (string, map[string]any, bool) // false: drop
func CleanTitle, CleanTag, CleanLanguage, CleanScreen func(string) string // "" when invalid
func IsBot(ua string) bool
func IsNavigation(h http.Header, method string) bool // spec "Server mode" header rules
```

- [ ] **Step 1: Write failing tests** with exact values:
  - `KeepQuery("utm_source=meta&utm_medium=paid-social&fbclid=ABC&lang=de&x=1", []string{"lang"})` → `q={"utm_source":"meta","utm_medium":"paid-social","lang":"de"}` and `clicks=["fbclid"]`;
  - `CleanReferrer("https://www.google.com/search?q=x", own)` → `"www.google.com/search"`;
  - `CleanReferrer("https://www.morgenfertig.at/a", []string{"www.morgenfertig.at"})` → `""`;
  - `CleanReferrer("android-app://com.google.android.gm/", nil)` → `"android-app://com.google.android.gm"`;
  - `CleanEvent` with 21 properties keeps 20 (sorted by key), drops nested values, and cuts a string at 200; an empty or 51-character name gives `false`;
  - `IsBot` is true for `Googlebot`, `facebookexternalhit`, `HeadlessChrome`, `curl/8`, `python-requests`, `Go-http-client` and `UptimeRobot`, and false for current desktop Chrome, iOS Safari and Android Firefox UAs;
  - `IsNavigation`:
    - true for `Accept: text/html`, `Sec-Fetch-Dest: document`, `Sec-Fetch-Mode: navigate`, GET;
    - false with `Sec-Purpose: prefetch;prerender`, with `Sec-Fetch-Dest: image`, for POST, and for `Accept: */*`;
    - true for `Accept: text/html` with no Sec-Fetch headers (older Safari).
- [ ] **Step 2: Run** `go test ./internal/analytics/`. Expected FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run.** Expected PASS.
- [ ] **Step 5: Commit** `feat: analytics keeps only campaign parameters, external referrers and bounded event data, and knows bots from navigations`.

### Task 4: UA and client-hint parsing, devices, channels

**Files:**
- Create: `internal/analytics/{ua.go,channel.go,ua_test.go,channel_test.go}`.

**Interfaces (Produces):**

```go
func ParseUA(ua, chMobile, chPlatform string) (browser, os, device string) // spec name lists; device desktop|tablet|mobile
func DeviceFromScreen(os, screen string) string                           // spec rule; "" when no screen
func Channel(entry Record) string // "Direct","Organic search","Paid search","Organic social","Paid social","Email","Other campaign","Referral"
```

- [ ] **Step 1: Write failing tests.**
  - `TestParseUA` has at least 14 real UA strings:
    - Chrome, Edge and Firefox on Windows;
    - Safari on macOS;
    - iPhone Safari → `Safari`/`iOS`/`mobile`;
    - iPad;
    - `CriOS` → `Chrome (iOS)`;
    - Samsung Internet;
    - Android Chrome → `mobile`;
    - an Android tablet without `Mobile` → `tablet`;
    - Opera;
    - Android WebView (`; wv)`);
    - ChromeOS;
    - Linux Firefox;
    - plus `chMobile="?1"` overriding to mobile.
  - `TestDeviceFromScreen`: iOS 390x844 → `mobile`; iOS 820x1180 → `tablet`; Windows 1366x768 → `laptop`; Windows 1920x1080 → `desktop`.
  - `TestChannel`:
    - gclid → `Paid search`;
    - `utm_medium=cpc&utm_source=google` → `Paid search`;
    - `utm_source=meta&utm_medium=paid-social` → `Paid social`;
    - `utm_medium=newsletter` → `Email`;
    - referrer `www.google.com/` → `Organic search`;
    - referrer `l.facebook.com/` → `Organic social`;
    - `utm_source=linkedin&utm_medium=social` → `Organic social`;
    - `utm_source=x` → `Other campaign`;
    - referrer `example.org/` → `Referral`;
    - nothing → `Direct`.
- [ ] **Step 2–4:** run (FAIL), implement, run (PASS).
- [ ] **Step 5: Commit** `feat: analytics names browsers, systems, devices and channels the way Umami shows them`.

### Task 5: Salts, visitor ids, GeoIP

**Files:**
- Create: `internal/analytics/{salt.go,geo.go,salt_test.go,geo_test.go}`.
- Modify: `go.mod` / `go.sum` (`github.com/oschwald/maxminddb-golang/v2`).
- Add a test fixture `internal/analytics/testdata/geo-test.mmdb`, generated in the test with a writer if the library ships one. Otherwise copy MaxMind's `GeoIP2-City-Test.mmdb` from the library's test data (Apache/CC licence noted in testdata `README`).

**Interfaces (Produces):**

```go
type Salts struct{ /* dir, mu, cache */ }
func NewSalts(dataDir string) *Salts                         // <data>/analytics/salts
func (s *Salts) For(date string) ([]byte, error)             // creates 32 random bytes, 0600, on first use
func (s *Salts) Prune(today time.Time) error                 // deletes dates < today(UTC)-2d
func VisitorID(salt []byte, viewID, ip, ua string) string    // first 16 hex of sha256(salt||0||viewID||0||ip||0||ua)
type Geo struct{ /* reader */ }
func OpenGeo(path string) (*Geo, error)                      // nil,nil when path == ""
func (g *Geo) Lookup(ip string) (country, region, city string) // nil-safe; region ISO code "CC-XX" if present else English name
func (g *Geo) Attribution() string                           // metadata description "en", "" if none
```

- [ ] **Step 1: Write failing tests.**
  - `TestVisitorIDStableWithinDay`.
  - `TestVisitorIDDiffersAcrossSitesAndDays`.
  - `TestSaltsPersistAcrossRestart`: a new `Salts` on the same dir returns the same bytes.
  - `TestSaltsPrune`: a file 3 days old is removed; yesterday's is kept.
  - `TestSaltFileMode0600`: skip on Windows.
  - `TestGeoLookupTestDB`: a known test IP gives the country from the fixture.
  - `TestGeoNilSafe`.
- [ ] **Step 2–4:** run (FAIL), implement, run (PASS).
- [ ] **Step 5: Commit** `feat: analytics visitor ids use a per-day salt that is deleted after two days, and an optional GeoIP database`.

### Task 6: The recorder

**Files:**
- Create: `internal/analytics/{recorder.go,dayfile.go,recorder_test.go}`.

**Interfaces:**
- Consumes: Tasks 3–5; `store.WithStatsLock` is passed in as a func, so `analytics` never imports `store`.
- Produces:

```go
type Hit struct {
    Time time.Time; ViewID, Kind, Source string // Kind "p"|"e"; Source "srv"|"js"
    Address, RawPath, RawQuery, Referrer string
    IP, UA, CHMobile, CHPlatform, Language string // Language: first Accept-Language tag or script value
    Title, Screen, Tag, Name string; Data map[string]any
    Own []string; ExtraQuery []string; TZ *time.Location
    PageCheck func() bool // nil = no check; false = drop (404)
}
type RecorderConfig struct {
    DailyCap int; Salts *Salts; Geo *Geo
    WithSite func(viewID string, fn func(siteDir string) error) (bool, error)
    Now func() time.Time; Log *slog.Logger
}
func NewRecorder(c RecorderConfig) *Recorder
func (r *Recorder) Add(h Hit) bool          // non-blocking; false = buffer full (counted)
func (r *Recorder) Run(ctx context.Context) // writer loop; drains with a 2 s deadline on ctx.Done
func (r *Recorder) Flush()                  // test hook: process everything queued now
func (r *Recorder) Dropped() int64
func DayFile(siteDir, date string) string   // <siteDir>/analytics/events/<date>.jsonl
func LocalDate(t time.Time, loc *time.Location) string
func ReadDay(siteDir, date string) ([]Record, error) // missing file = nil,nil; skips bad lines
```

- [ ] **Step 1: Write failing tests.**
  - `TestRecorderWritesOneLinePerHit`: the record has `s:"srv"`, `k:"p"`, `v` of length 16, and no `ip`/`ua` keys. Check that the raw line contains neither the IP nor the UA string.
  - `TestRecorderUsesSiteTimezoneForTheDayFile`: 2026-11-04T23:30Z with Europe/Vienna goes to `2026-11-05.jsonl`.
  - `TestRecorderDailyCapCountsOver`: cap 3 and 5 hits → 3 lines, and `.over` contains `2`.
  - `TestRecorderNeverRecreatesDeletedSite`: `WithSite` returns false, so the site dir does not exist afterwards (Review Focus 2).
  - `TestRecorderDropsWhenFull`: `Dropped()` is greater than 0 and `Add` never blocks (10000 adds without `Run`, done in under 1 s).
  - `TestRecorderPageCheckFalseDrops`.
  - `TestRecorderVrAlwaysEmpty`.
- [ ] **Step 2–4:** run (FAIL), implement (one goroutine, a per-site batch map, append with `O_APPEND|O_CREATE`, `MkdirAll` of `analytics/events` only inside `WithSite`), run (PASS).
- [ ] **Step 5: Commit** `feat: the analytics recorder appends sanitized records per site and day without ever blocking a request`.

### Task 7: Server-mode collection, the forms event, the opt-out cookie check

**Files:**
- Modify:
  - `internal/httpapi/server.go`: an `analytics *analytics.Recorder` field. `New` builds it from `cfg` (Salts under `cfg.DataDir`, `OpenGeo(cfg.AnalyticsGeoIP)` with an error logged and Geo nil, and `WithSite: st.WithStatsLock`). Add `func (a *API) AnalyticsRecorder() *analytics.Recorder`.
  - `cmd/sitebin/main.go:328`: `go api.AnalyticsRecorder().Run(ctx)`.
  - `internal/httpapi/internalh.go`: in `authz`, after both admitted paths (container and file, next to `countView`), call `a.recordPageView(r, site, addr, mount, pathMode)`.
  - `internal/httpapi/formsubmit.go`: after a successful send, call `a.recordFormEvent(r, site, key)`.
- Create: `internal/httpapi/analyticscollect.go`, `internal/httpapi/analyticscollect_test.go`.

**Interfaces (Produces):**

```go
const analyticsOptOutCookie = "sitebin_analytics_optout"
func (a *API) recordPageView(r *http.Request, site *store.Site, addr, mount string, pathMode bool)
// in effect && mode server && IsNavigation && !IsBot && UA has "Mozilla/" && addr allowed && !excluded && no opt-out cookie;
// file sites set PageCheck to a closure over store.OpenContentRoot: regular file, dir with index.html/index.htm, or SPA fallback.
func (a *API) recordFormEvent(r *http.Request, site *store.Site, key string) // Kind "e", Name "sitebin:form", Data {"form": key}; both modes
func (a *API) analyticsTZ(site *store.Site) *time.Location
func (a *API) siteAddresses(site *store.Site) []string // generated address + verified custom domains (as stored)
```

- [ ] **Step 1: Write failing tests** with `newEnv` plus a site with analytics enabled. The test calls `e.api.AnalyticsRecorder().Flush()` and then `analytics.ReadDay`.
  - `TestAuthzRecordsBrowserNavigation`.
  - `TestAuthzSkips` is table-driven: bot UA, prefetch, an image request, POST, analytics disabled, script mode, an excluded path `/kinder/**`, an address not in `addresses`, the opt-out cookie, a locked site (410), a view-password gate (401), a 404 path without SPA (PageCheck false), and a redirecting address.
  - `TestAuthzRecordsSPAFallback`.
  - `TestAuthzRecordsContainerSite`, using the container test pattern in `containers_test.go`.
  - `TestFormSubmitRecordsEvent`: the event `sitebin:form` with `{"form":"<key>"}` and no field values.
- [ ] **Step 2–4:** run (FAIL), implement, run both suites (PASS).
- [ ] **Step 5: Commit** `feat: authz counts real page navigations for sites that turned analytics on, and a form submission is an event`.

### Task 8: The script, the send endpoint and the opt-out page

**Files:**
- Create:
  - `web/static/analytics.js`, the tracker, following the spec "The script" section in full;
  - `web/static/analytics-stub.js` (defines `window.umami = {track(){}, identify(){}}`);
  - `internal/httpapi/analyticssend.go`, `internal/httpapi/analyticssend_test.go`;
  - an opt-out page template, which follows the existing `msgPage` / page templates' look.
- Modify:
  - `internal/httpapi/server.go` routes:
    - `GET /_sitebin/analytics.js` → `a.analyticsScript`;
    - `POST /_sitebin/analytics/send` → `a.analyticsSend`;
    - `GET /_sitebin/analytics/opt-out` → `a.analyticsOptOutPage`;
    - `POST /_sitebin/analytics/opt-out` → `a.analyticsOptOut`.
  - The `API` struct gets `analyticsLimiter *auth.Limiter`, at 60/min with a burst of 30. Use the existing `auth.Limiter` constructor the CSP limiter uses.

**Interfaces (Produces):**

```go
func (a *API) analyticsScript(w http.ResponseWriter, r *http.Request)   // full script when in effect for servingSiteByAddress(r) & address allowed, else stub; Cache-Control: public, max-age=3600; Content-Type text/javascript; X-Content-Type-Options nosniff
func (a *API) analyticsSend(w http.ResponseWriter, r *http.Request)     // always 204 empty
func (a *API) analyticsOptOutPage(w http.ResponseWriter, r *http.Request)
func (a *API) analyticsOptOut(w http.ResponseWriter, r *http.Request)   // form field action=out|in; also accepts fetch from the script with Sec-Fetch-Site same-origin; sets/clears cookie (Path "/" or mount, 1y, Secure unless HTTPOnly cfg, SameSite=Lax, HttpOnly)
func clientNet(ip string) string                                          // IPv4 /32, IPv6 /64 key
```

Send handling, in order:
1. resolve the site with `servingSiteByAddress`; on a main-domain or path-view request, 204;
2. refuse locked, not in effect or address not allowed;
3. `Sec-Fetch-Site` present and not `same-origin` → drop;
4. read at most 8 KiB, otherwise drop;
5. parse JSON `{type:"event", payload:{…}}`;
6. check that the `url` host equals `r.Host` and its path is under the mount;
7. no `name` in server mode → drop;
8. rate-limit by `viewID+"|"+clientNet(ip)`;
9. build a `Hit` with `Source:"js"`, Kind `p` or `e`, Language from the payload, Screen and Title;
10. `Add`.

- [ ] **Step 1: Write failing tests.**
  - `TestSendRecordsEventInServerMode`.
  - `TestSendDropsPageviewInServerMode`.
  - `TestSendRecordsPageviewInScriptMode`, with screen `1920x1080` → `dv:"desktop"` on Windows UA.
  - `TestSendRejectsGarbage` (Review Focus 4). It is table-driven: non-JSON, 9 KiB, `type:"identify"`, url on another host, url outside the mount, nested data value dropped while the event is kept, cross-site `Sec-Fetch-Site`. Every case → 204 and the recorder line count is as expected.
  - `TestSendRateLimited`: the 31st immediate record from one IP is dropped.
  - `TestScriptStubWhenOff` (body contains `track(){}`) and `TestScriptFullWhenOn`.
  - `TestOptOutSetsAndClearsCookie`.
  - `TestScriptAndSendOnPathViewAreInert`.
- [ ] **Step 2: Run.** Expected FAIL.
- [ ] **Step 3: Implement the Go side and `analytics.js`.**
  - Read the script tag via `document.currentScript`.
  - Expose `window.umami.track` and `identify`.
  - Delegate clicks for `[data-umami-event]` with `data-umami-event-*`.
  - Wrap `pushState` and `replaceState`, and listen to `popstate`.
  - Honour `data-domains`, `data-exclude-search`, `data-exclude-hash`, `data-do-not-track`, `data-auto-track` and `data-before-send`.
  - `localStorage["umami.disabled"]` truthy → send nothing, and once per page `fetch('/_sitebin/analytics/opt-out', {method:'POST', body: 'action=out', headers:{'Content-Type':'application/x-www-form-urlencoded'}})` when `document.cookie` lacks the marker. Since the cookie is HttpOnly, keep a `sessionStorage["sitebin.optout.mirrored"]` marker instead. When the flag is absent and the marker is set, POST `action=in` and clear the marker.
  - `navigator.webdriver` → send nothing.
  - Send `fetch('/_sitebin/analytics/send', {method:'POST', keepalive:true, body: JSON.stringify(...), headers:{'Content-Type':'text/plain'}})`.
  - Under a mount, the endpoint is `<mount>/_sitebin/analytics/send`: derive the mount from the script's own `src` path prefix.
- [ ] **Step 4: Run** both suites. Expected PASS.
- [ ] **Step 5: Commit** `feat: a same-origin, Umami-compatible analytics script and its bounded send endpoint, with a visitor opt-out`.

### Task 9: The query engine (overview, tables, event data, realtime)

**Files:**
- Create: `internal/analytics/{query.go,visits.go,dims.go,cache.go,query_test.go}`.

**Interfaces (Produces):**

```go
type Filter struct{ Dim, Value string }
type Query struct {
    From, To time.Time   // instants; To exclusive
    Loc *time.Location
    Filters []Filter
    Unit string          // "hour"|"day"|"month"; "" = auto by span (<=2d hour, <=92d day, else month)
}
type Totals struct{ Views, Visitors, Visits int; BounceRate float64; DurationS float64 }
type Point struct{ T time.Time; Views, Visitors int }
type Overview struct{ Totals Totals; Previous *Totals; Series []Point; Unit string; OverCap int }
type Row struct{ Value string; Visitors, Views, Count int }
type Engine struct{ /* LRU */ }
func NewEngine(maxBytes int64) *Engine
func (e *Engine) Overview(siteDir string, q Query, compare bool) (Overview, error)
func (e *Engine) Table(siteDir string, q Query, dim string, limit, offset int) (rows []Row, total int, err error)
func (e *Engine) EventData(siteDir string, q Query, name string) ([]KeyValues, error)
func (e *Engine) Realtime(siteDir string, now time.Time, loc *time.Location) (Realtime, error)
func (e *Engine) Invalidate(siteDir, date string)
var Dims = []string{"path","entry","exit","title","referrer","channel","utm_source","utm_medium","utm_campaign","utm_term","utm_content","click_id","browser","os","device","screen","country","region","city","language","address","tag","event"} // plus "query:<name>"
func VisitLevel(dim string) bool // spec: entry, exit, referrer, channel, utm_*, click_id, country, region, city, browser, os, device, language, screen, address, query:*
```

Visits: group by `(date, v)`, sort by `t`, split at gaps over 30 min. Imported records split by `vi` instead. Entry is the first `p` of the visit and exit the last `p`. A visit's `r`/`q`/`ck` come from its entry record. Visitors in a range = Σ over days of distinct `v` in matching records. Series buckets come from local time truncation in `Loc` (use `time.Date` per bucket boundary, never add 24 h).

- [ ] **Step 1: Write failing tests** over a fixture written with `ReadDay`'s inverse helper `writeDay(t, dir, date, []Record)`:
  - `TestOverviewMetrics`: 3 visitors, 4 visits (one split by a 31-minute gap), 7 views, 2 bounces → bounce rate 0.5, duration as computed by hand.
  - `TestOverviewCompare`.
  - `TestVisitorsAreDailySums`: the same `v` on two days counts as 2.
  - `TestFilterVisitLevel`: `utm_source=meta` keeps every record of those visits, including events.
  - `TestFilterRecordLevel`: `path=/a` keeps only those page views.
  - `TestTableEntryExitReferrerChannel`.
  - `TestTableEventCounts`.
  - `TestEventData`.
  - `TestRealtime`.
  - `TestSeriesAcrossDST` (Review Focus 3): Europe/Vienna on 2026-03-29 gives 23 hourly points, on 2026-10-25 25 points, and day units give one point per local day.
  - `TestImportedVisitsUseVi`.
- [ ] **Step 2–4:** run (FAIL), implement (raw scan with the LRU of parsed days, keyed by path+size+mtime), run (PASS).
- [ ] **Step 5: Commit** `feat: the analytics engine answers Umami's overview, tables, event data and realtime from the day files`.

### Task 10: Day summaries

**Files:**
- Create: `internal/analytics/{summary.go,summary_test.go}`.
- Modify: `query.go`, so that unfiltered `Overview`/`Table` use `<date>.sum.json` for closed days (date < today in `Loc`), building it on first need. It is keyed by the source file's size and mtime; top 1000 values per dimension, the rest summed into `(other)`.

**Interfaces (Produces):** `func BuildSummary(records []Record, loc *time.Location) Summary`, plus `Summary` JSON with `size`, `mtime`, `totals`, `hours[24±1]`, `dims map[string][]Row`, `events []Row`, `event_data map[string][]KeyValues`.

- [ ] **Step 1: Write failing tests.**
  - `TestSummaryEqualsRawScan`: for every dim and the overview over a 3-day fixture, the summary path equals the raw path.
  - `TestSummaryRebuiltWhenDayChanges`.
  - `TestSummaryOtherBucket`: 1005 distinct paths → 1000 rows plus `(other)`.
- [ ] **Step 2–4:** run, implement, run.
- [ ] **Step 5: Commit** `feat: closed analytics days are summarised once, so long ranges read fast`.

### Task 11: Funnels and goals

**Files:**
- Create: `internal/analytics/{funnel.go,funnel_test.go}`.

**Interfaces (Produces):**

```go
type StepResult struct{ Visitors int; DropOff int; Conversion float64 } // Conversion vs step 1
type FunnelResult struct{ ID, Name string; Steps []StepResult }
type GoalResult struct{ ID, Name string; Completions, Visitors int; Rate float64 }
func (e *Engine) Funnels(siteDir string, q Query, fs []store.AnalyticsFunnel) ([]FunnelResult, error)
func (e *Engine) Goals(siteDir string, q Query, gs []store.AnalyticsGoal) ([]GoalResult, error)
```

`analytics` may import `store` only for these definition types. If that creates an import cycle, mirror the two structs in `analytics` and convert in `httpapi`. A path step matches a page-view record by `store.GlobMatch`, and an event step matches an event record by name. Reachability per visitor-day follows the spec algorithm: reachable times per step, each step within the window of some reachable previous-step time, any chain.

- [ ] **Step 1: Write failing tests.**
  - `TestFunnelWindowFromPreviousStep`: step 2 is 50 min after step 1 and step 3 is 50 min after step 2, with window 60 → reaches 3.
  - `TestFunnelAnyChainCounts`: the first step-1 occurrence has no follow-up, a later one does → counts.
  - `TestFunnelMorgenfertig` uses the operator's real definition `/app-entwickeln-lassen/*` → `anfrage-start` → `anfrage`, window 60.
  - `TestGoalsRate`.
  - `TestFunnelDoesNotLinkAcrossDays`.
- [ ] **Step 2–4:** run, implement, run.
- [ ] **Step 5: Commit** `feat: analytics funnels and goals count the way Umami does, per visitor and day`.

### Task 12: The analytics API, retention and salt pruning

**Files:**
- Create: `internal/httpapi/analyticsapi.go`, `internal/httpapi/analyticsapi_test.go`.
- Modify:
  - `internal/httpapi/server.go` routes, all under `a.withEditAuth` except the settings read under `withEditAuthEvenLocked`; the settings PUT is wrapped in `a.recorded(provenance.ActionAnalyticsSettings, …)`:
    - `GET` and `PUT /api/sites/{editID}/analytics`;
    - `GET …/analytics/overview`;
    - `GET …/analytics/table`;
    - `GET …/analytics/event-data`;
    - `GET …/analytics/realtime`;
    - `GET …/analytics/funnels`;
    - `GET …/analytics/goals`.
  - `internal/provenance/provenance.go`: add `ActionAnalyticsSettings = "analytics-settings"`.
  - `internal/httpapi/sites.go`: `getSite` adds `analytics_available bool` (`reason == ""` or `reason == "plan"`) for the tab.
  - `internal/cleanup/analytics.go`: `PurgeAnalytics(st *store.Store, site *store.Site, now time.Time, retentionDays int)` deletes `events/<date>.*` older than the retention in the site's zone, and is called from `Sweep`.
  - `Sweep` signature: thread `cfg.AnalyticsRetentionDays` and the `*analytics.Salts` through `cleanup.Run` the way the Git runner is; `Salts.Prune(now)` runs once per sweep.

**Interfaces (Produces):** The `API` struct gets `engine
*analytics.Engine`, built in `New` with `analytics.NewEngine(64 << 20)`,
and `func (a *API) AnalyticsEngine() *analytics.Engine` for Task 14. JSON
shapes exactly as the spec "API" table. `from`/`to` take `YYYY-MM-DD` (local, inclusive), or `range=last24h|last30m`. `filter` repeats as `dim:value`. 400 `{error}` on an unknown dim, a bad date, `to < from`, or a span over min(retention, 760) days.

- [ ] **Step 1: Write failing tests.**
  - `TestAnalyticsSettingsRoundTrip`: PUT valid → 200, and provenance has `analytics-settings`.
  - `TestAnalyticsSettings422`, field named.
  - `TestAnalyticsStateReasons`: instance off, plan, locked.
  - `TestAnalyticsDowngradeKeepsData` (Review Focus 5).
  - `TestAnalyticsOverviewEndpoint`: seeded days → numbers.
  - `TestAnalyticsTableValidation`.
  - `TestAnalyticsNeedsEditAuth`: 401 without credentials, 200 with the edit password, 200 with an account token. Use the ee env for a token test if the core env lacks tokens.
  - `TestPurgeAnalyticsRetention`.
  - `TestSweepPrunesSalts`.
- [ ] **Step 2–4:** run, implement, run both suites.
- [ ] **Step 5: Commit** `feat: the analytics API answers the site's managers, and the sweep applies retention and deletes old salts`.

### Task 13: The Analytics tab

**Files:**
- Create: `web/static/edit-analytics.js`.
- Modify:
  - `web/static/edit.html`: add the panel `<div class="tabpanel hidden" id="panel-analytics" role="tabpanel" aria-labelledby="tab-analytics"></div>` after `panel-git`, and `<script src="/_sitebin/assets/static/edit-analytics.js" defer></script>` after `edit-git.js`;
  - `web/static/edit.js`, in `unlock()` after the Git registration: `if (site.analytics_available && !tabs.list.some(t => t.id === "analytics")) tabs.register({ id: "analytics", label: "Analytics", panel: $("panel-analytics"), onShow() { analyticsTab.show(); } });`;
  - `web/static/app.css`: an `.an-*` namespace in the existing dark/amber variables.
- Before writing chart code: **invoke the `dataviz` skill**, and take the series colours from the edit page's amber accent plus a neutral for visitors.

**Interfaces:** consumes the Task 12 API. `analyticsTab.show()` loads state on the first show and re-renders from the hash `#analytics/<subtab>?from=&to=&f=dim:value&cmp=1`.

Build, in this order, each against the API:
1. The header: range picker, compare, filter chips.
2. Overview: metrics bar with deltas, an SVG bar chart (views) with a visitors line, the panel groups and tables as the spec lists them, the top 10 per table with a share bar, and "More" with paging (100 per page).
3. Events: names, a chart, data keys and values.
4. Funnels: results as horizontal step bars, plus an editor (add, edit, delete) that saves through PUT settings.
5. Goals: the same pattern.
6. Realtime: polls every 10 s only while that sub-tab is visible and `!document.hidden`.
7. Settings: every control the spec lists, the snippet with copy, the opt-out links, storage info, the privacy paragraphs (EN and DE text as below).
8. The plan-reason notice, the empty states, and the GeoIP attribution under Location.

Privacy paragraph text (fixed copy):
- EN: "This site counts visits with Sitebin Analytics, run by our host on our behalf. It sets no cookies, stores no IP addresses, and cannot recognise you on another day or on another site. We see which pages are read, where visitors came from, and their browser, device and country."
- DE: "Diese Website zählt Besuche mit Sitebin Analytics, das unser Hoster in unserem Auftrag betreibt. Es setzt keine Cookies, speichert keine IP-Adressen und kann Sie weder an einem anderen Tag noch auf einer anderen Website wiedererkennen. Wir sehen, welche Seiten gelesen werden, woher Besucher kommen, sowie Browser, Gerät und Land."

- [ ] **Step 1:** Implement the tab.
- [ ] **Step 2: Browser check.**
  - Build a local community instance: `go build -o sitebin.exe ./cmd/sitebin`, or the Docker image as `sitebin:dev`.
  - Create a site, turn analytics on, and seed two days of records (write `.jsonl` lines directly into its folder). The seed includes the hostile values from Review Focus 1.
  - Open `/e/<editID>#analytics` in a browser (Playwright or Claude in Chrome).
  - Check every sub-tab renders; filter chips work; "More" pages; Realtime stops polling when left; the hostile values show as literal text, and the `javascript:` referrer is not a link.
  - Save screenshots to the session scratchpad.
- [ ] **Step 3:** Run `go test ./internal/httpapi/` (the assets embed test).
- [ ] **Step 4: Commit** `feat: the edit page's Analytics tab shows the site's statistics like Umami, with sub-tabs`.

### Task 14: Umami import

**Files:**
- Create:
  - `internal/analytics/umami.go`, `internal/analytics/umami_test.go`;
  - `internal/httpapi/analyticsimport.go`, `internal/httpapi/analyticsimport_test.go`;
  - `docs/analytics-umami-import.md` (the runbook with the exact SQL and commands).
- Modify:
  - `internal/httpapi/server.go`: `Internal()` mux gets `POST /internal/analytics/import`;
  - `cmd/sitebin/main.go`: `case "analytics":` with subcommand `import-umami <siteViewID> <events.jsonl> [--reports <reports.json>] [--until <RFC3339>]`. It is a thin client to the internal listener, built from `cfg.InternalAddr` as `healthcheck` does.

**Interfaces (Produces):**

```go
type UmamiEvent struct { // the export's JSON object
    CreatedAt time.Time `json:"created_at"`; EventType int `json:"event_type"`; EventName, Hostname, URLPath, URLQuery string
    ReferrerDomain, ReferrerPath, PageTitle, Tag, VisitID, SessionID string
    UTMSource, UTMMedium, UTMCampaign, UTMContent, UTMTerm string
    Gclid, Fbclid, Msclkid, Ttclid, LiFatID, Twclid string
    Browser, OS, Device, Screen, Language, Country, Region, City string
    Data map[string]any `json:"event_data"`
}
func ConvertUmami(ev UmamiEvent, loc *time.Location, own, extraQuery []string) (date string, rec Record)
type ImportReport struct{ Days, Records, SkippedAfterCut, SkippedRetention int; From, To string; Funnels, Goals []string; SkippedReports []string }
func Import(siteDir string, loc *time.Location, events io.Reader, cut time.Time, retentionDays int, now time.Time, withSite func(func(string) error) (bool, error)) (ImportReport, error)
// rewrites each touched day: drop s:"um" lines, merge new by t, write tmp + rename under withSite; idempotent
func FirstOwnRecord(siteDir string) (time.Time, bool) // earliest srv/js t over all day files
```

The internal handler takes a multipart body with `site`, `events`, and optionally `reports` and `until`. It computes `cut = min(until, FirstOwnRecord)`, calls `Import`, merges reports into settings through `store.SetAnalytics`, adding by name only and keeping limits, invalidates the engine's cache for the touched days, appends the range to `imported.json`, and returns the report as JSON.

- [ ] **Step 1: Write failing tests.**
  - `TestConvertUmamiMapsNames`: `chrome` → Chrome, `ios` → Safari/iOS, `crios` → Chrome (iOS), `edge-chromium` → Edge, `Mac OS` → macOS, `Windows 10` → Windows, `Android OS` → Android; a gclid column → `ck:["gclid"]`; an own referrer domain → `r` empty; `s:"um"`, `vi` set, `v` length 16.
  - `TestImportIdempotent`: twice gives byte-identical files.
  - `TestImportKeepsOwnRecordsAndCutsAtFirstOwn`.
  - `TestImportSkipsBeyondRetention`.
  - `TestImportReportsMergeByName`: the operator's two funnels and two goals from the spec's Umami rows.
  - `TestInternalImportRoute`.
- [ ] **Step 2–4:** run, implement, run both suites.
- [ ] **Step 5: Commit** `feat: a site's Umami history can be imported through the running server, once per site and without double counting`.

### Task 15: e2e, README, CLAUDE.md

**Files:**
- Create: `e2e/analytics.ps1` (community image, ASCII only). Follow the structure of `e2e/forms.ps1` / `e2e/git.ps1`: start the container, create a site, `PUT /api/sites/<id>/analytics {"enabled":true}`.
  1. GET a page with browser headers (`Accept: text/html`, `Sec-Fetch-Dest: document`, `Sec-Fetch-Mode: navigate`, Chrome UA) three times, and once with a `curl` UA and once with `Sec-Purpose: prefetch`. Wait 2 s, then `overview` gives `views == 3`.
  2. Switch to script mode. GET `/_sitebin/analytics.js` and check it contains `umami`. POST to `/send` a page view and an event `anfrage` with data `{"seite":"/"}`. `table?dim=event` has `anfrage`, and the overview views increase by 1.
  3. Opt-out: POST the opt-out, then a browser GET with the cookie in server mode is not counted.
  4. Save a funnel; `funnels` answers.
- Modify:
  - `README.md`: the env table rows, an "Analytics" section (modes, script compatibility, privacy, import pointer);
  - `CLAUDE.md`: a short "Site analytics" section stating the anti-goals, the recorder lock rule (`WithStatsLock`, never create a site folder), "authz only calls `Add`", the `vr` A/B hook, and the import-through-the-server rule. The e2e list goes from fifteen to sixteen scripts.
- [ ] **Step 1:** Write the script and docs.
- [ ] **Step 2: Run.** `docker build -t sitebin:dev .` then `powershell -File e2e/analytics.ps1`. Expected: all checks PASS.
- [ ] **Step 3:** Run the full suites `go vet ./... && go test ./... && go test -tags ee ./...`.
- [ ] **Step 4: Commit** `test: e2e counts navigations, script events and an opt-out against the real Caddy` and `docs: analytics in the README and the repo guide`.

### Task 16 (website repo, after the product ships): docs and pricing

**Files** (in `C:\Projects\Sitebin-Project\Sitebin-Website`; read its `CLAUDE.md` first):
- Create: `public/docs/analytics/index.html`, in the style of the other docs pages. It covers turning analytics on, the two modes, the script and the Umami compatibility list, exclusions, opt-out, path views, retention, import on request, and the privacy paragraphs from Task 13.
- Modify: the docs index; the pricing page gets "Analytics (Umami-compatible)" on Pro and Studio; the privacy page gets one sentence on processing on behalf of site owners. **That sentence waits for the operator's OK.**
- [ ] **Step 1:** Write the pages and run `scripts/check-links.ps1` (green).
- [ ] **Step 2: Commit** `content: analytics docs and the pricing page's analytics line`. Push only per the ship order.
