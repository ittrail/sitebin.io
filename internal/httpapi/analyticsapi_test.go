package httpapi

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/analytics"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The analytics API. See "API" in
// docs/superpowers/specs/2026-11-04-site-analytics-design.md.

type analyticsStateResp struct {
	Available  bool            `json:"available"`
	Reason     string          `json:"reason"`
	UpgradeURL string          `json:"upgrade_url"`
	Settings   store.Analytics `json:"settings"`
	Effective  string          `json:"effective_timezone"`
	Addresses  []string        `json:"addresses"`
	Geo        struct {
		Present     bool   `json:"present"`
		Attribution string `json:"attribution"`
	} `json:"geo"`
	RetentionDays int                       `json:"retention_days"`
	DailyCap      int                       `json:"daily_cap"`
	Bytes         int64                     `json:"bytes"`
	FirstDay      string                    `json:"first_day"`
	Imported      []analytics.ImportedRange `json:"imported"`
}

// analyticsCall asks the site's analytics route path ("" is the state) with
// the edit password; a non-nil body is sent as JSON.
func (e *env) analyticsCall(t *testing.T, method, edit, pw, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/api/sites/"+edit+"/analytics"+path, rd)
	if pw != "" {
		req.Header.Set("X-Edit-Password", pw)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return e.public(t, req)
}

func (e *env) analyticsState(t *testing.T, edit, pw string) analyticsStateResp {
	t.Helper()
	w := e.analyticsCall(t, "GET", edit, pw, "", nil)
	if w.Code != 200 {
		t.Fatalf("GET analytics = %d %s", w.Code, w.Body)
	}
	return decodeState(t, w)
}

func decodeState(t *testing.T, w *httptest.ResponseRecorder) analyticsStateResp {
	t.Helper()
	var s analyticsStateResp
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("state: %v (%s)", err, w.Body)
	}
	return s
}

// decodeInto decodes a 200 answer.
func decodeInto(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("answer = %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body)
	}
}

// seedAnalytics writes recs as the site's day file of date, as the recorder
// would have.
func seedAnalytics(t *testing.T, site *store.Site, date string, recs ...analytics.Record) {
	t.Helper()
	var b bytes.Buffer
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	p := analytics.DayFile(site.Dir(), date)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func pageView(at time.Time, v, path string) analytics.Record {
	return analytics.Record{T: at.UnixMilli(), K: "p", S: "srv", V: v, P: path, B: "Chrome", C: "AT", Dv: "desktop"}
}

func event(at time.Time, v, name string, data map[string]any) analytics.Record {
	return analytics.Record{T: at.UnixMilli(), K: "e", S: "js", V: v, N: name, D: data}
}

// daysAgo is hh:mm UTC on the day n days before now's (UTC). Each test
// takes now once, and seeds and asks from it, so a test that runs across
// midnight still asks for the days it seeded.
func daysAgo(now time.Time, n, hh, mm int) time.Time {
	d := now.UTC().AddDate(0, 0, -n)
	return time.Date(d.Year(), d.Month(), d.Day(), hh, mm, 0, 0, time.UTC)
}

func dateOf(t time.Time) string { return analytics.LocalDate(t, time.UTC) }

// seededSite is a site whose analytics hold two closed days, three and two
// days ago (UTC, the instance's default zone), with a funnel and a goal:
//
//	day 1: a views / and /pricing five minutes apart, b views / once;
//	day 2: a views / and signs up a minute later (plan: pro).
//
// Over both days: 4 views, 3 visitors (2 + 1), 3 visits, two of them
// bounces, and an average duration of 100 s (300, 0 and 0).
func seededSite(t *testing.T, e *env, now time.Time) (site *store.Site, edit, pw string) {
	t.Helper()
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	seedAnalytics(t, site, dateOf(daysAgo(now, 3, 0, 0)),
		pageView(daysAgo(now, 3, 10, 0), "a", "/"),
		pageView(daysAgo(now, 3, 10, 5), "a", "/pricing"),
		pageView(daysAgo(now, 3, 11, 0), "b", "/"),
	)
	seedAnalytics(t, site, dateOf(daysAgo(now, 2, 0, 0)),
		pageView(daysAgo(now, 2, 9, 0), "a", "/"),
		event(daysAgo(now, 2, 9, 1), "a", "signup", map[string]any{"plan": "pro"}),
	)
	if err := e.st.SetAnalytics(site, &store.Analytics{
		Enabled: true,
		Funnels: []store.AnalyticsFunnel{{ID: "f1", Name: "Sign-up", WindowMinutes: 60,
			Steps: []store.AnalyticsStep{{Type: "path", Value: "/"}, {Type: "event", Value: "signup"}}}},
		Goals: []store.AnalyticsGoal{{ID: "g1", Name: "Signed up", Type: "event", Value: "signup"}},
	}); err != nil {
		t.Fatal(err)
	}
	return site, editIDFrom(t, c.EditURL), c.EditPassword
}

// rangeOf is the query of the local dates n and m days before now's,
// inclusive.
func rangeOf(now time.Time, n, m int) string {
	return "from=" + dateOf(daysAgo(now, n, 0, 0)) + "&to=" + dateOf(daysAgo(now, m, 0, 0))
}

func TestAnalyticsSettingsRoundTrip(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	site, _ := e.st.ByViewID(c.ID)
	gen := e.api.generatedAddress(site)

	// No settings yet: the defaults. The zone is the instance's, so the
	// settings name none and the zone in effect stands beside them.
	s := e.analyticsState(t, edit, c.EditPassword)
	if !s.Available || s.Reason != "" || s.UpgradeURL != "" {
		t.Errorf("available %v, reason %q, upgrade %q", s.Available, s.Reason, s.UpgradeURL)
	}
	if s.Settings.Enabled || s.Settings.Mode != "server" || s.Settings.Timezone != "" || s.Effective != "UTC" {
		t.Errorf("default settings = %+v, effective zone %q", s.Settings, s.Effective)
	}
	if !reflect.DeepEqual(s.Addresses, []string{gen}) || s.RetentionDays != 760 || s.DailyCap != 50000 || s.Geo.Present {
		t.Errorf("addresses %v, retention %d, cap %d, geo %+v", s.Addresses, s.RetentionDays, s.DailyCap, s.Geo)
	}

	want := store.Analytics{
		Enabled:   true,
		Mode:      "script",
		Timezone:  "Europe/Vienna",
		Addresses: []string{gen},
		Exclude:   []string{"/admin/**"},
		Query:     []string{"ref"},
		Funnels: []store.AnalyticsFunnel{{ID: "f1", Name: "Sign-up", WindowMinutes: 30,
			Steps: []store.AnalyticsStep{{Type: "path", Value: "/pricing"}, {Type: "event", Value: "signup"}}}},
		Goals: []store.AnalyticsGoal{{ID: "g1", Name: "Signed up", Type: "event", Value: "signup"}},
	}
	w := e.analyticsCall(t, "PUT", edit, c.EditPassword, "", want)
	if w.Code != 200 {
		t.Fatalf("PUT = %d %s", w.Code, w.Body)
	}
	if got := decodeState(t, w); !reflect.DeepEqual(got.Settings, want) || got.Effective != "Europe/Vienna" {
		t.Errorf("PUT answered %+v (effective %q), want %+v", got.Settings, got.Effective, want)
	}
	if got := e.analyticsState(t, edit, c.EditPassword); !reflect.DeepEqual(got.Settings, want) {
		t.Errorf("GET after PUT = %+v", got.Settings)
	}
	stored, _ := e.st.ByViewID(c.ID)
	if stored.Meta.Analytics == nil || !reflect.DeepEqual(*stored.Meta.Analytics, want) {
		t.Errorf("stored = %+v", stored.Meta.Analytics)
	}
	last := lastEntry(t, e.trail(t, c.ID))
	if provenance.ActionAnalyticsSettings != "analytics-settings" || last.Action != provenance.ActionAnalyticsSettings || last.Detail != "on (script)" {
		t.Errorf("provenance = %+v", last)
	}

	// Off again: the whole object replaces the old one.
	w = e.analyticsCall(t, "PUT", edit, c.EditPassword, "", store.Analytics{})
	if got := decodeState(t, w); w.Code != 200 || got.Settings.Enabled || len(got.Settings.Funnels) != 0 || got.Settings.Timezone != "" || got.Effective != "UTC" {
		t.Errorf("off: %d %+v, effective %q", w.Code, got.Settings, got.Effective)
	}
	// A second change within minutes joins the first entry's burst.
	if last := lastEntry(t, e.trail(t, c.ID)); last.Action != provenance.ActionAnalyticsSettings || last.Detail != "on (script), off" {
		t.Errorf("provenance after turning off = %+v", last)
	}

	// A site without a zone of its own follows the instance's default,
	// and the state says which one that is without storing it.
	hosted := newEnv(t, map[string]string{"SITEBIN_ANALYTICS_TIMEZONE": "Europe/Vienna"})
	hc := hosted.createSite(t, nil, map[string]string{"index.html": "x"})
	if got := hosted.analyticsState(t, editIDFrom(t, hc.EditURL), hc.EditPassword); got.Settings.Timezone != "" || got.Effective != "Europe/Vienna" {
		t.Errorf("instance default: timezone %q, effective %q", got.Settings.Timezone, got.Effective)
	}
}

func TestAnalyticsSettings422(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	before := len(e.trail(t, c.ID))
	for _, tc := range []struct {
		body  any
		field string
	}{
		{map[string]any{"mode": "bogus"}, "mode"},
		{map[string]any{"timezone": "Mars/Olympus"}, "timezone"},
		{map[string]any{"addresses": []string{"elsewhere.example"}}, "addresses"},
		{map[string]any{"exclude": []string{"no-slash"}}, "exclude"},
		{map[string]any{"goals": []map[string]any{{"id": "g1", "name": "x", "type": "click", "value": "y"}}}, "goals"},
		// A field the settings do not have is refused, not dropped.
		{map[string]any{"enabled": true, "colour": "amber"}, "colour"},
		{map[string]any{"goals": []map[string]any{{"id": "g1", "name": "x", "type": "event", "value": "y", "weight": 2}}}, "weight"},
	} {
		w := e.analyticsCall(t, "PUT", edit, c.EditPassword, "", tc.body)
		var got struct{ Error, Field string }
		json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != 422 || got.Field != tc.field || got.Error == "" {
			t.Errorf("%v: %d %s, want 422 naming %q", tc.body, w.Code, w.Body, tc.field)
		}
	}
	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit+"/analytics", strings.NewReader("{not json")), c.EditPassword)
	if w := e.public(t, req); w.Code != 400 {
		t.Errorf("bad JSON = %d, want 400", w.Code)
	}
	big := `{"exclude":["/` + strings.Repeat("a", maxAnalyticsSettings) + `"]}`
	req = authed(httptest.NewRequest("PUT", "/api/sites/"+edit+"/analytics", strings.NewReader(big)), c.EditPassword)
	if w := e.public(t, req); w.Code != 413 {
		t.Errorf("a body over 1 MiB = %d %s, want 413", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.Analytics != nil {
		t.Errorf("a refused PUT stored %+v", site.Meta.Analytics)
	}
	if n := len(e.trail(t, c.ID)); n != before {
		t.Errorf("a refused PUT was recorded (%d entries, was %d)", n, before)
	}
}

func TestAnalyticsStateReasons(t *testing.T) {
	now := time.Now()
	no := false
	available := func(t *testing.T, e *env, edit, pw string) bool {
		t.Helper()
		var p struct {
			AnalyticsAvailable *bool `json:"analytics_available"`
		}
		decodeInto(t, e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), pw)), &p)
		if p.AnalyticsAvailable == nil {
			t.Fatal("the site payload has no analytics_available")
		}
		return *p.AnalyticsAvailable
	}

	t.Run("available", func(t *testing.T) {
		e := newEnv(t, nil)
		c := e.createSite(t, nil, map[string]string{"index.html": "x"})
		edit := editIDFrom(t, c.EditURL)
		w := e.analyticsCall(t, "GET", edit, c.EditPassword, "", nil)
		if strings.Contains(w.Body.String(), `"reason"`) || strings.Contains(w.Body.String(), `"upgrade_url"`) {
			t.Errorf("an available site's state names a reason: %s", w.Body)
		}
		if !available(t, e, edit, c.EditPassword) {
			t.Error("analytics_available = false")
		}
	})

	t.Run("instance", func(t *testing.T) {
		e := newEnv(t, map[string]string{"SITEBIN_ANALYTICS": "false"})
		_, edit, pw := seededSite(t, e, now)
		s := e.analyticsState(t, edit, pw)
		if s.Available || s.Reason != analyticsReasonInstance || s.UpgradeURL != "" {
			t.Errorf("available %v, reason %q, upgrade %q", s.Available, s.Reason, s.UpgradeURL)
		}
		if available(t, e, edit, pw) {
			t.Error("the tab is shown with the instance switch off")
		}
		// What was collected stays readable all the same.
		var ov analytics.Overview
		decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/overview?"+rangeOf(now, 3, 2), nil), &ov)
		if ov.Totals.Views != 4 {
			t.Errorf("views with the instance switch off = %d, want 4", ov.Totals.Views)
		}
	})

	t.Run("plan", func(t *testing.T) {
		e := newEnv(t, nil)
		c := e.createSite(t, nil, map[string]string{"index.html": "x"})
		edit := editIDFrom(t, c.EditURL)
		site, _ := e.st.ByViewID(c.ID)
		if err := e.st.SetQuotaAnalytics(site, &no); err != nil {
			t.Fatal(err)
		}
		s := e.analyticsState(t, edit, c.EditPassword)
		if s.Available || s.Reason != analyticsReasonPlan || s.UpgradeURL != "http://sitebin.example/account" {
			t.Errorf("available %v, reason %q, upgrade %q", s.Available, s.Reason, s.UpgradeURL)
		}
		if !available(t, e, edit, c.EditPassword) {
			t.Error("the tab is hidden when only the plan lacks analytics; it carries the upgrade notice")
		}
	})

	t.Run("locked", func(t *testing.T) {
		e := newEnv(t, nil)
		_, edit, pw := seededSite(t, e, now)
		site, _ := e.st.ByEditID(edit)
		lockSite(t, e, site.ViewID, "phishing")
		s := e.analyticsState(t, edit, pw)
		if s.Available || s.Reason != analyticsReasonLocked || s.UpgradeURL != "" || !s.Settings.Enabled {
			t.Errorf("available %v, reason %q, upgrade %q, settings %+v", s.Available, s.Reason, s.UpgradeURL, s.Settings)
		}
		if available(t, e, edit, pw) {
			t.Error("the tab is shown for a locked site")
		}
		for _, path := range []string{"/overview?" + rangeOf(now, 3, 2), "/table?dim=path&" + rangeOf(now, 3, 2), "/event-data?name=signup&" + rangeOf(now, 3, 2), "/realtime", "/funnels?" + rangeOf(now, 3, 2), "/goals?" + rangeOf(now, 3, 2)} {
			if w := e.analyticsCall(t, "GET", edit, pw, path, nil); w.Code != 403 || !strings.Contains(w.Body.String(), "locked") {
				t.Errorf("GET %s on a locked site = %d %s, want 403", path, w.Code, w.Body)
			}
		}
		if w := e.analyticsCall(t, "PUT", edit, pw, "", store.Analytics{}); w.Code != 403 {
			t.Errorf("PUT on a locked site = %d, want 403", w.Code)
		}
		if got, _ := e.st.ByViewID(site.ViewID); got.Meta.Analytics == nil || !got.Meta.Analytics.Enabled {
			t.Error("a locked site's settings changed")
		}
	})
}

// The state's storage figures, and the GeoIP database's attribution.
func TestAnalyticsStateStorage(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_ANALYTICS_GEOIP": filepath.Join("..", "analytics", "testdata", "geo-test.mmdb")})
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	site, _ := e.st.ByViewID(c.ID)
	s := e.analyticsState(t, edit, c.EditPassword)
	if s.Bytes != 0 || s.FirstDay != "" || s.Imported == nil || len(s.Imported) != 0 {
		t.Errorf("nothing stored: bytes %d, first %q, imported %#v", s.Bytes, s.FirstDay, s.Imported)
	}
	if !s.Geo.Present || s.Geo.Attribution == "" {
		t.Errorf("geo = %+v, want the test database and its description", s.Geo)
	}

	seedAnalytics(t, site, "2026-10-02", pageView(time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC), "a", "/"))
	seedAnalytics(t, site, "2026-09-30", pageView(time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC), "a", "/"))
	imported := `[{"from":"2025-01-01","to":"2026-09-29"}]`
	if err := os.WriteFile(filepath.Join(site.Dir(), "analytics", "imported.json"), []byte(imported), 0o644); err != nil {
		t.Fatal(err)
	}
	var want int64 = int64(len(imported))
	for _, d := range []string{"2026-10-02", "2026-09-30"} {
		fi, err := os.Stat(analytics.DayFile(site.Dir(), d))
		if err != nil {
			t.Fatal(err)
		}
		want += fi.Size()
	}
	s = e.analyticsState(t, edit, c.EditPassword)
	if s.Bytes != want || s.FirstDay != "2026-09-30" {
		t.Errorf("bytes %d (want %d), first day %q", s.Bytes, want, s.FirstDay)
	}
	if !reflect.DeepEqual(s.Imported, []analytics.ImportedRange{{From: "2025-01-01", To: "2026-09-29"}}) {
		t.Errorf("imported = %+v", s.Imported)
	}
}

// Review Focus 5: a downgrade stops collection, never access. The tab still
// shows what was collected, and its owner may still edit or turn off the
// settings.
func TestAnalyticsDowngradeKeepsData(t *testing.T) {
	now := time.Now()
	e := newEnv(t, nil)
	site, edit, pw := seededSite(t, e, now)
	no := false
	if err := e.st.SetQuotaAnalytics(site, &no); err != nil {
		t.Fatal(err)
	}
	if s := e.analyticsState(t, edit, pw); s.Reason != analyticsReasonPlan || !s.Settings.Enabled || len(s.Settings.Funnels) != 1 {
		t.Fatalf("after the downgrade: reason %q, settings %+v", s.Reason, s.Settings)
	}
	read := func(when string) {
		t.Helper()
		var ov analytics.Overview
		decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/overview?"+rangeOf(now, 3, 2), nil), &ov)
		if ov.Totals.Views != 4 || ov.Totals.Visitors != 3 {
			t.Errorf("%s: totals %+v", when, ov.Totals)
		}
		var fs struct{ Funnels []analytics.FunnelResult }
		decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/funnels?"+rangeOf(now, 3, 2), nil), &fs)
		if len(fs.Funnels) != 1 || fs.Funnels[0].Steps[1].Visitors != 1 {
			t.Errorf("%s: funnels %+v", when, fs.Funnels)
		}
	}
	read("downgraded")
	w := e.analyticsCall(t, "PUT", edit, pw, "", store.Analytics{Enabled: false, Funnels: []store.AnalyticsFunnel{{ID: "f1", Name: "Sign-up", WindowMinutes: 60,
		Steps: []store.AnalyticsStep{{Type: "path", Value: "/"}, {Type: "event", Value: "signup"}}}}})
	if w.Code != 200 {
		t.Fatalf("PUT after the downgrade = %d %s", w.Code, w.Body)
	}
	read("downgraded and turned off")
	if _, err := os.Stat(analytics.DayFile(site.Dir(), dateOf(daysAgo(now, 3, 0, 0)))); err != nil {
		t.Errorf("a day file went with the downgrade: %v", err)
	}
}

func TestAnalyticsOverviewEndpoint(t *testing.T) {
	now := time.Now()
	e := newEnv(t, nil)
	site, edit, pw := seededSite(t, e, now)
	if err := os.WriteFile(filepath.Join(site.Dir(), "analytics", "events", dateOf(daysAgo(now, 3, 0, 0))+".over"), []byte("7"), 0o644); err != nil {
		t.Fatal(err)
	}
	var ov analytics.Overview
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/overview?"+rangeOf(now, 4, 2)+"&compare=1", nil), &ov)
	tot := ov.Totals
	if tot.Views != 4 || tot.Visitors != 3 || tot.Visits != 3 || math.Abs(tot.BounceRate-2.0/3) > 1e-9 || math.Abs(tot.DurationS-100) > 1e-9 {
		t.Errorf("totals = %+v", tot)
	}
	if ov.Unit != "day" || len(ov.Series) != 3 || ov.Series[0].Views != 0 || ov.Series[1].Views != 3 || ov.Series[2].Views != 1 || ov.Series[1].Visitors != 2 {
		t.Errorf("unit %q, series %+v", ov.Unit, ov.Series)
	}
	if !ov.Series[0].T.Equal(daysAgo(now, 4, 0, 0)) {
		t.Errorf("the series starts at %v, want local midnight %v", ov.Series[0].T, daysAgo(now, 4, 0, 0))
	}
	if ov.Previous == nil || ov.Previous.Views != 0 || ov.OverCap != 7 {
		t.Errorf("previous %+v, over cap %d", ov.Previous, ov.OverCap)
	}
	// Without compare=1 there is no previous period.
	w := e.analyticsCall(t, "GET", edit, pw, "/overview?"+rangeOf(now, 4, 2), nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"previous"`) {
		t.Errorf("no compare: %d %s", w.Code, w.Body)
	}
	// A record-level filter keeps the page views it names.
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/overview?"+rangeOf(now, 3, 2)+"&filter=path:/pricing", nil), &ov)
	if ov.Totals.Views != 1 || ov.Totals.Visitors != 1 {
		t.Errorf("filtered totals = %+v", ov.Totals)
	}
	// One day is its hours.
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/overview?"+rangeOf(now, 3, 3), nil), &ov)
	if ov.Unit != "hour" || len(ov.Series) != 24 || ov.Totals.Views != 3 {
		t.Errorf("one day: unit %q, %d points, %+v", ov.Unit, len(ov.Series), ov.Totals)
	}
	// The last 24 hours end now.
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/overview?range=last24h", nil), &ov)
	if ov.Unit != "hour" || ov.Totals.Views != 0 {
		t.Errorf("last 24 hours: unit %q, %+v", ov.Unit, ov.Totals)
	}
}

// A day file over the read limit (256 MiB) is a 500 that says so, never
// read into memory. The file is extended without being written, so it takes
// no time and, where the file system can, no space.
func TestAnalyticsRefusesADayTooLargeToRead(t *testing.T) {
	now := time.Now()
	e := newEnv(t, nil)
	site, edit, pw := seededSite(t, e, now)
	if err := os.Truncate(analytics.DayFile(site.Dir(), dateOf(daysAgo(now, 3, 0, 0))), 256<<20+1); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/overview?" + rangeOf(now, 4, 2), "/table?dim=path&" + rangeOf(now, 3, 3), "/funnels?" + rangeOf(now, 3, 2)} {
		w := e.analyticsCall(t, "GET", edit, pw, path, nil)
		if w.Code != 500 || !strings.Contains(w.Body.String(), "this day's analytics file is too large to read") {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	// A range that does not reach it (nor pads to it: one day on each side
	// is read for a zone change) still answers.
	if w := e.analyticsCall(t, "GET", edit, pw, "/overview?"+rangeOf(now, 1, 1), nil); w.Code != 200 {
		t.Errorf("another day: %d %s", w.Code, w.Body)
	}
}

// The site's zone, not the server's, decides what a date means.
func TestAnalyticsDatesAreTheSitesLocalDays(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	site, _ := e.st.ByViewID(c.ID)
	if err := e.st.SetAnalytics(site, &store.Analytics{Timezone: "Pacific/Kiritimati"}); err != nil { // UTC+14
		t.Fatal(err)
	}
	// 2026-10-01 20:00 UTC is 2 October in Kiritimati.
	seedAnalytics(t, site, "2026-10-02", pageView(time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC), "a", "/"))
	var ov analytics.Overview
	decodeInto(t, e.analyticsCall(t, "GET", edit, c.EditPassword, "/overview?from=2026-10-01&to=2026-10-01", nil), &ov)
	if ov.Totals.Views != 0 {
		t.Errorf("1 October (Kiritimati) = %d views, want 0", ov.Totals.Views)
	}
	decodeInto(t, e.analyticsCall(t, "GET", edit, c.EditPassword, "/overview?from=2026-10-02&to=2026-10-02", nil), &ov)
	if _, off := ov.Series[0].T.Zone(); ov.Totals.Views != 1 || off != 14*3600 {
		t.Errorf("2 October (Kiritimati) = %d views, series at UTC%+d s", ov.Totals.Views, off)
	}
}

func TestAnalyticsReadRoutes(t *testing.T) {
	now := time.Now()
	e := newEnv(t, nil)
	site, edit, pw := seededSite(t, e, now)
	rg := rangeOf(now, 3, 2)

	type table struct {
		Rows  []map[string]any `json:"rows"`
		Total int              `json:"total"`
	}
	var tbl table
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/table?dim=path&"+rg, nil), &tbl)
	if tbl.Total != 2 || len(tbl.Rows) != 2 || tbl.Rows[0]["value"] != "/" || tbl.Rows[0]["visitors"] != 3.0 || tbl.Rows[0]["views"] != 3.0 {
		t.Errorf("path table = %+v", tbl)
	}
	tbl = table{} // a decode into used maps would merge into them
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/table?dim=path&limit=1&offset=1&"+rg, nil), &tbl)
	if tbl.Total != 2 || len(tbl.Rows) != 1 || tbl.Rows[0]["value"] != "/pricing" {
		t.Errorf("second page = %+v", tbl)
	}
	// The event table counts events: {value, count, visitors}.
	tbl = table{}
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/table?dim=event&"+rg, nil), &tbl)
	if len(tbl.Rows) != 1 || !reflect.DeepEqual(tbl.Rows[0], map[string]any{"value": "signup", "count": 1.0, "visitors": 1.0}) {
		t.Errorf("event table = %+v", tbl.Rows)
	}
	// A visit-level filter keeps whole visits; a query parameter's value
	// may hold a colon.
	tbl = table{}
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/table?dim=path&filter=country:AT&filter=query:ref:a:b&"+rg, nil), &tbl)
	if tbl.Total != 0 {
		t.Errorf("no visit carries ref=a:b, yet %+v", tbl)
	}

	var ed struct {
		Keys []analytics.KeyValues `json:"keys"`
	}
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/event-data?name=signup&"+rg, nil), &ed)
	if !reflect.DeepEqual(ed.Keys, []analytics.KeyValues{{Key: "plan", Values: []analytics.ValueCount{{Value: "pro", Count: 1}}}}) {
		t.Errorf("event data = %+v", ed.Keys)
	}
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/event-data?name=signup&filter=data:plan:pro&"+rg, nil), &ed)
	if len(ed.Keys) != 1 {
		t.Errorf("event data filtered by its own value = %+v", ed.Keys)
	}

	var fs struct {
		Funnels []analytics.FunnelResult `json:"funnels"`
	}
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/funnels?"+rg, nil), &fs)
	if len(fs.Funnels) != 1 || fs.Funnels[0].ID != "f1" || fs.Funnels[0].Steps[0].Visitors != 3 || fs.Funnels[0].Steps[1].Visitors != 1 {
		t.Errorf("funnels = %+v", fs.Funnels)
	}
	var gs struct {
		Goals []analytics.GoalResult `json:"goals"`
	}
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/goals?"+rg, nil), &gs)
	if len(gs.Goals) != 1 || gs.Goals[0].Completions != 1 || gs.Goals[0].Visitors != 1 || math.Abs(gs.Goals[0].Rate-1.0/3) > 1e-9 {
		t.Errorf("goals = %+v", gs.Goals)
	}

	// Realtime: the last 30 minutes, whatever the range parameters say.
	live := time.Now().Add(-time.Minute)
	seedAnalytics(t, site, dateOf(live), pageView(live, "c", "/live"))
	var rt analytics.Realtime
	decodeInto(t, e.analyticsCall(t, "GET", edit, pw, "/realtime", nil), &rt)
	if rt.Active != 1 || len(rt.PerMinute) != 30 || len(rt.Latest) != 1 || rt.Latest[0].P != "/live" {
		t.Errorf("realtime = %+v", rt)
	}

	// No saved funnels or goals: empty lists, not null.
	other := e.createSite(t, nil, map[string]string{"index.html": "x"})
	for _, path := range []string{"/funnels?" + rg, "/goals?" + rg} {
		w := e.analyticsCall(t, "GET", editIDFrom(t, other.EditURL), other.EditPassword, path, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "[]") {
			t.Errorf("%s with nothing saved = %d %s", path, w.Code, w.Body)
		}
	}
}

func TestAnalyticsTableValidation(t *testing.T) {
	now := time.Now()
	e := newEnv(t, nil)
	_, edit, pw := seededSite(t, e, now)
	day := func(n int) string { return dateOf(daysAgo(now, n, 0, 0)) }
	today := day(0)
	long := strings.Repeat("x", 500)
	for _, path := range []string{
		"/table?" + rangeOf(now, 3, 2),                    // no dim
		"/table?dim=nope&" + rangeOf(now, 3, 2),           // unknown dim
		"/table?dim=data:plan&" + rangeOf(now, 3, 2),      // a filter, not a table
		"/table?dim=path&limit=0&" + rangeOf(now, 3, 2),   // limit below 1
		"/table?dim=path&limit=501&" + rangeOf(now, 3, 2), // limit over 500
		"/table?dim=path&limit=ten&" + rangeOf(now, 3, 2), // not a number
		"/table?dim=path&offset=-1&" + rangeOf(now, 3, 2), // offset below 0
		"/table?dim=path",                                                 // no range
		"/table?dim=path&from=" + today,                                   // no end
		"/table?dim=path&from=2026-13-01&to=2026-13-02",                   // no such month
		"/table?dim=path&from=26-11-01&to=26-11-02",                       // not a date
		"/table?dim=path&from=" + day(2) + "&to=" + day(3),                // to before from
		"/table?dim=path&from=" + day(760) + "&to=" + today,               // 761 days
		"/table?dim=path&range=last7d",                                    // no such range
		"/table?dim=path&range=last24h&from=" + today,                     // both
		"/table?dim=path&filter=nope:x&" + rangeOf(now, 3, 2),             // unknown filter dim
		"/table?dim=path&filter=pathonly&" + rangeOf(now, 3, 2),           // no value
		"/table?dim=path&filter=path:/" + long + "&" + rangeOf(now, 3, 2), // a filter of 506 characters
		"/table?dim=title&filter=" + url.QueryEscape("title:"+strings.Repeat("页", 495)) + "&" + rangeOf(now, 3, 2), // 501 characters
		"/table?dim=path" + strings.Repeat("&filter=country:AT", 21) + "&" + rangeOf(now, 3, 2),                    // 21 filters
		"/overview?filter=nope:x&" + rangeOf(now, 3, 2),
		"/overview?from=" + day(2) + "&to=" + day(3),
		"/event-data?" + rangeOf(now, 3, 2), // no name
		"/funnels?filter=nope:x&" + rangeOf(now, 3, 2),
		"/goals?range=yesterday",
	} {
		w := e.analyticsCall(t, "GET", edit, pw, path, nil)
		var got struct{ Error string }
		json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != 400 || got.Error == "" {
			t.Errorf("GET %s = %d %s, want 400 with an error", shortPath(path), w.Code, w.Body)
		}
	}
	for _, path := range []string{
		"/table?dim=path&limit=500&" + rangeOf(now, 3, 2),
		"/table?dim=path&offset=99&" + rangeOf(now, 3, 2),
		"/table?dim=query:ref&" + rangeOf(now, 3, 2),
		"/table?dim=utm_source&range=last30m",
		"/table?dim=path&from=" + day(759) + "&to=" + today,                                   // 760 days
		"/table?dim=path&filter=path:/" + strings.Repeat("x", 494) + "&" + rangeOf(now, 3, 2), // 500 characters
		// Characters, not bytes: a title in CJK is three bytes a character.
		"/table?dim=title&filter=" + url.QueryEscape("title:"+strings.Repeat("页", 200)) + "&" + rangeOf(now, 3, 2),
		"/table?dim=title&filter=" + url.QueryEscape("title:"+strings.Repeat("页", 494)) + "&" + rangeOf(now, 3, 2), // 500 characters
		"/table?dim=path" + strings.Repeat("&filter=country:AT", 20) + "&" + rangeOf(now, 3, 2),                    // 20 filters
		"/overview?range=last30m",
	} {
		if w := e.analyticsCall(t, "GET", edit, pw, path, nil); w.Code != 200 {
			t.Errorf("GET %s = %d %s, want 200", shortPath(path), w.Code, w.Body)
		}
	}
}

// shortPath keeps a failure message readable when a path carries a long
// filter.
func shortPath(p string) string {
	if len(p) > 120 {
		return p[:120] + "…"
	}
	return p
}

// The longest range is the retention when that is shorter than 760 days; a
// retention of 0 (keep every day) still caps it at 760.
func TestAnalyticsSpanFollowsTheRetention(t *testing.T) {
	now := time.Now()
	day := func(n int) string { return dateOf(daysAgo(now, n, 0, 0)) }
	for _, tc := range []struct {
		retention  string
		ok, tooBig int // the days back to the first date of a range ending today
	}{
		{"30", 29, 30},
		{"0", 759, 760},
	} {
		e := newEnv(t, map[string]string{"SITEBIN_ANALYTICS_RETENTION_DAYS": tc.retention})
		c := e.createSite(t, nil, map[string]string{"index.html": "x"})
		edit := editIDFrom(t, c.EditURL)
		if w := e.analyticsCall(t, "GET", edit, c.EditPassword, "/overview?from="+day(tc.ok)+"&to="+day(0), nil); w.Code != 200 {
			t.Errorf("retention %s, %d days: %d %s", tc.retention, tc.ok+1, w.Code, w.Body)
		}
		if w := e.analyticsCall(t, "GET", edit, c.EditPassword, "/overview?from="+day(tc.tooBig)+"&to="+day(0), nil); w.Code != 400 {
			t.Errorf("retention %s, %d days: %d, want 400", tc.retention, tc.tooBig+1, w.Code)
		}
	}
}

// Every analytics route is a site manager's: the edit password or an
// account token that stands on the site, never an MCP OAuth token and never
// nobody.
func TestAnalyticsNeedsEditAuth(t *testing.T) {
	registerProvider(t, &fakeProvider{enabled: true, owner: "acct-1",
		bearer: map[string]string{"sbp_tok": "acct-1", "sbp_other": "acct-2", "jwt-rw": "acct-1"},
		oauth:  map[string]bool{"jwt-rw": true},
	})
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	routes := []struct{ method, path, body string }{
		{"GET", "", ""},
		{"PUT", "", `{"enabled":true}`},
		{"GET", "/overview?range=last24h", ""},
		{"GET", "/table?dim=path&range=last24h", ""},
		{"GET", "/event-data?name=x&range=last24h", ""},
		{"GET", "/realtime", ""},
		{"GET", "/funnels?range=last24h", ""},
		{"GET", "/goals?range=last24h", ""},
	}
	creds := []struct {
		name string
		set  func(*http.Request)
		want int
	}{
		{"nothing", func(*http.Request) {}, 401},
		{"edit password", func(r *http.Request) { r.Header.Set("X-Edit-Password", c.EditPassword) }, 200},
		{"account token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer sbp_tok") }, 200},
		{"another account's token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer sbp_other") }, 401},
		{"OAuth token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer jwt-rw") }, 401},
	}
	for _, cr := range creds {
		for _, rt := range routes {
			req := httptest.NewRequest(rt.method, "/api/sites/"+edit+"/analytics"+rt.path, strings.NewReader(rt.body))
			if rt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			cr.set(req)
			if w := e.public(t, req); w.Code != cr.want {
				t.Errorf("%s: %s %s = %d %s, want %d", cr.name, rt.method, rt.path, w.Code, w.Body, cr.want)
			}
		}
	}
}
