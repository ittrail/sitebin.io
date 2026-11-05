package httpapi

import (
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/analytics"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Server-mode collection and the forms event. See "Server mode: what counts
// as a page view" in docs/superpowers/specs/2026-11-04-site-analytics-design.md.

// navUA is a desktop Chrome on Windows.
const navUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

// navReq is authz's subrequest for a browser navigating to uri on host, with
// the headers Caddy forwards from a real page load.
func navReq(host, uri string) *http.Request {
	req := authzReq(host, uri, "")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("User-Agent", navUA)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept-Language", "de-AT,de;q=0.9,en;q=0.8")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	return req
}

// collecting turns analytics on for the site with the given settings.
func collecting(t *testing.T, e *env, site *store.Site, s store.Analytics) {
	t.Helper()
	s.Enabled = true
	if err := e.st.SetAnalytics(site, &s); err != nil {
		t.Fatal(err)
	}
}

// analyticsRecords flushes the recorder and returns the site's records of
// today, and of the day the test started should it run over midnight.
func analyticsRecords(t *testing.T, e *env, site *store.Site, since time.Time) []analytics.Record {
	t.Helper()
	e.api.AnalyticsRecorder().Flush()
	var out []analytics.Record
	for _, d := range slices.Compact([]string{analytics.LocalDate(since, time.UTC), analytics.LocalDate(time.Now(), time.UTC)}) {
		recs, err := analytics.ReadDay(site.Dir(), d)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, recs...)
	}
	return out
}

// dayFileText is the raw text of the site's day files, to prove what never
// reaches the disk.
func dayFileText(t *testing.T, site *store.Site, since time.Time) string {
	t.Helper()
	var b strings.Builder
	for _, d := range slices.Compact([]string{analytics.LocalDate(since, time.UTC), analytics.LocalDate(time.Now(), time.UTC)}) {
		raw, err := os.ReadFile(analytics.DayFile(site.Dir(), d))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		b.Write(raw)
	}
	return b.String()
}

// collectSite is a file site with pages, an image and a folder, attached
// at example.org, www.example.org and example.org/blog.
func collectSite(t *testing.T) (*env, *store.Site) {
	t.Helper()
	e := mountEnv(t)
	site := e.ownedSite(t, "", map[string]string{
		"index.html":        "<h1>home</h1>",
		"about.html":        "<h1>about</h1>",
		"logo.png":          "png",
		"kinder/spiel.html": "<h1>spiel</h1>",
		"docs/index.html":   "<h1>docs</h1>",
		"empty/note.txt":    "no index here",
	}, "example.org", "www.example.org", "example.org/blog")
	return e, site
}

func TestAuthzRecordsBrowserNavigation(t *testing.T) {
	since := time.Now()
	e, site := collectSite(t)

	// What authz answers with analytics off is what it answers with it on.
	req := func() *http.Request {
		r := navReq(viewHost(site), "/about.html?utm_source=news&gclid=abc123&email=a%40b.c")
		r.Header.Set("Referer", "https://www.google.com/search?q=secret")
		return r
	}
	before := e.internal(t, req())
	collecting(t, e, site, store.Analytics{})
	w := e.internal(t, req())
	if w.Code != 200 || w.Code != before.Code || !maps.EqualFunc(w.Header(), before.Header(), slices.Equal) || w.Body.String() != before.Body.String() {
		t.Fatalf("authz changed: %d %v %q, was %d %v %q", w.Code, w.Header(), w.Body, before.Code, before.Header(), before.Body)
	}

	recs := analyticsRecords(t, e, site, since)
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want 1", recs)
	}
	r := recs[0]
	if r.K != "p" || r.S != "srv" || r.A != viewHost(site) || r.P != "/about.html" {
		t.Errorf("kind/source/address/path = %q %q %q %q", r.K, r.S, r.A, r.P)
	}
	if len(r.Q) != 1 || r.Q["utm_source"] != "news" || !slices.Equal(r.Ck, []string{"gclid"}) {
		t.Errorf("query = %v, click ids = %v", r.Q, r.Ck)
	}
	if r.R != "google.com/search" {
		t.Errorf("referrer = %q", r.R)
	}
	if r.B != "Chrome" || r.O != "Windows" || r.Dv != "desktop" || r.L != "de-AT" {
		t.Errorf("browser/os/device/language = %q %q %q %q", r.B, r.O, r.Dv, r.L)
	}
	// The visitor id hashes the address Caddy saw and the user agent with
	// the salt the instance hands the sweep: one salt store for both.
	salt, err := e.api.AnalyticsSalts().For(analytics.LocalDate(time.UnixMilli(r.T), time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if want := analytics.VisitorID(salt, site.ViewID, "203.0.113.7", navUA); r.V != want {
		t.Errorf("visitor id = %q, want %q", r.V, want)
	}
	if at := time.UnixMilli(r.T); at.Before(since.Add(-time.Second)) || at.After(time.Now().Add(time.Second)) {
		t.Errorf("time = %v, not the server's", at)
	}
	raw := dayFileText(t, site, since)
	for _, leak := range []string{"203.0.113.7", "Mozilla", "abc123", "secret", "a@b.c", "a%40b.c"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the day file holds %q:\n%s", leak, raw)
		}
	}

	// A path view records the site's generated address and the visitor's
	// path; an address with a path records itself and the full path.
	for _, c := range []struct {
		name, host, uri, view, addr, path string
	}{
		{"path view", "sitebin.example", "/v/" + site.ViewID + "/about.html", site.ViewID, viewHost(site), "/v/" + site.ViewID + "/about.html"},
		{"mounted address", "example.org", "/blog/about.html", "", "example.org/blog", "/blog/about.html"},
		{"custom domain", "Example.org:443", "/docs/", "", "example.org", "/docs/"},
	} {
		t.Run(c.name, func(t *testing.T) {
			had := len(analyticsRecords(t, e, site, since))
			r := navReq(c.host, c.uri)
			if c.view != "" {
				r.Header.Set("X-Sitebin-View", c.view)
			}
			if w := e.internal(t, r); w.Code != 200 {
				t.Fatalf("authz = %d", w.Code)
			}
			recs := analyticsRecords(t, e, site, since)[had:]
			if len(recs) != 1 || recs[0].A != c.addr || recs[0].P != c.path {
				t.Fatalf("records = %+v, want address %q path %q", recs, c.addr, c.path)
			}
		})
	}
}

func TestAuthzSkips(t *testing.T) {
	for _, c := range []struct {
		name  string
		want  int // authz's answer, which analytics never changes
		n     int // records expected
		host  string
		uri   string
		hdr   map[string]string
		setup func(t *testing.T, e *env, site *store.Site)
	}{
		{name: "a browser navigation", want: 200, n: 1},
		{name: "a bot", want: 200, hdr: map[string]string{"User-Agent": "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"}},
		{name: "a user agent without the Mozilla token", want: 200, hdr: map[string]string{"User-Agent": "Opera/9.80 (Windows NT 6.1; U; en) Presto/2.12.388 Version/12.18"}},
		{name: "a prefetch", want: 200, hdr: map[string]string{"Sec-Purpose": "prefetch"}},
		{name: "an image", want: 200, uri: "/logo.png", hdr: map[string]string{"Accept": "image/avif,image/webp,*/*", "Sec-Fetch-Dest": "image", "Sec-Fetch-Mode": "no-cors"}},
		{name: "a POST", want: 200, hdr: map[string]string{"X-Forwarded-Method": "POST"}},
		{name: "analytics disabled", want: 200, setup: func(t *testing.T, e *env, site *store.Site) {
			if err := e.st.SetAnalytics(site, &store.Analytics{Enabled: false}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "script mode", want: 200, setup: func(t *testing.T, e *env, site *store.Site) {
			collecting(t, e, site, store.Analytics{Mode: "script"})
		}},
		{name: "an excluded path", want: 200, uri: "/kinder/spiel.html", setup: func(t *testing.T, e *env, site *store.Site) {
			collecting(t, e, site, store.Analytics{Exclude: []string{"/kinder/**"}})
		}},
		{name: "a percent-encoded excluded path", want: 200, uri: "/%6Binder/spiel.html", setup: func(t *testing.T, e *env, site *store.Site) {
			collecting(t, e, site, store.Analytics{Exclude: []string{"/kinder/**"}})
		}},
		{name: "an excluded path below a mount", want: 200, host: "example.org", uri: "/blog/kinder/spiel.html", setup: func(t *testing.T, e *env, site *store.Site) {
			collecting(t, e, site, store.Analytics{Exclude: []string{"/kinder/**"}})
		}},
		{name: "an address not counted", want: 200, setup: func(t *testing.T, e *env, site *store.Site) {
			collecting(t, e, site, store.Analytics{Addresses: []string{"example.org"}})
		}},
		{name: "the opt-out cookie", want: 200, hdr: map[string]string{"Cookie": "a=b; " + analyticsOptOutCookie + "=1"}},
		{name: "a locked site", want: 410, setup: func(t *testing.T, e *env, site *store.Site) {
			if _, err := e.st.SetLock(site, &store.SiteLock{By: store.LockByAdmin}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a view password gate", want: 401, setup: func(t *testing.T, e *env, site *store.Site) {
			if err := e.st.Update(site, func(m *store.Meta) error {
				m.ViewPasswordProtected, m.ViewPasswordHash = true, "x"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a missing page without SPA", want: 200, uri: "/missing.html"},
		{name: "a folder without an index file", want: 200, uri: "/empty/"},
		{name: "a folder without its slash", want: 200, uri: "/docs"},
		{name: "a file with a slash", want: 200, uri: "/about.html/"},
		{name: "a redirecting address", want: 301, host: "www.example.org", setup: func(t *testing.T, e *env, site *store.Site) {
			if err := e.st.SetDomainRedirect(site, "www.example.org", "example.org", 301); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			since := time.Now()
			e, site := collectSite(t)
			collecting(t, e, site, store.Analytics{})
			if c.setup != nil {
				c.setup(t, e, site)
			}
			host, uri := c.host, c.uri
			if host == "" {
				host = viewHost(site)
			}
			if uri == "" {
				uri = "/about.html"
			}
			req := navReq(host, uri)
			for k, v := range c.hdr {
				req.Header.Set(k, v)
			}
			if w := e.internal(t, req); w.Code != c.want {
				t.Fatalf("authz = %d, want %d", w.Code, c.want)
			}
			if recs := analyticsRecords(t, e, site, since); len(recs) != c.n {
				t.Fatalf("records = %+v, want %d", recs, c.n)
			}
		})
	}
}

func TestAuthzRecordsSPAFallback(t *testing.T) {
	since := time.Now()
	e, site := collectSite(t)
	if err := e.st.Update(site, func(m *store.Meta) error { m.SPAFallback = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.st.WriteSPAMarker(site); err != nil {
		t.Fatal(err)
	}
	collecting(t, e, site, store.Analytics{})
	if w := e.internal(t, navReq(viewHost(site), "/dashboard/settings")); w.Code != 200 {
		t.Fatalf("authz = %d", w.Code)
	}
	recs := analyticsRecords(t, e, site, since)
	if len(recs) != 1 || recs[0].P != "/dashboard/settings" {
		t.Fatalf("records = %+v, want the SPA route", recs)
	}
}

func TestAuthzRecordsContainerSite(t *testing.T) {
	since := time.Now()
	withContainers(t, &fakeRuntime{})
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, nil)
	running(t, e, c.ID, ext.ContainerService{Name: "app", Host: "sb-" + c.ID + "-app", Ports: []int{3000}})
	if err := e.api.SiteService().SyncContainerEndpoints(c.ID, []string{"app:3000"}); err != nil {
		t.Fatal(err)
	}
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	if err := e.st.SetQuotaAnalytics(site, &on); err != nil { // accounts are on: the plan must say so
		t.Fatal(err)
	}
	collecting(t, e, site, store.Analytics{})
	host := c.ID + "." + e.cfg.ViewDomain
	w := e.internal(t, navReq(host, "/app/route?utm_medium=email"))
	if w.Code != 200 || w.Header().Get(upstreamHeader) != "sb-"+c.ID+"-app:3000" {
		t.Fatalf("authz = %d %q", w.Code, w.Header().Get(upstreamHeader))
	}
	recs := analyticsRecords(t, e, site, since)
	if len(recs) != 1 || recs[0].A != host || recs[0].P != "/app/route" || recs[0].Q["utm_medium"] != "email" {
		t.Fatalf("records = %+v", recs)
	}
}

func TestFormSubmitRecordsEvent(t *testing.T) {
	since := time.Now()
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{Recipient: "office@example.com"})
	collecting(t, e, site, store.Analytics{})
	hdr := map[string]string{
		"User-Agent":      navUA,
		"Referer":         "http://" + viewHost(site) + "/contact.html?ref=x",
		"X-Forwarded-For": "203.0.113.7",
	}

	// Refused and honeypotted submissions are no event.
	if w := submit(t, e, viewHost(site), f.Key, "name=&message=+", hdr); w.Code != 400 {
		t.Fatalf("empty = %d", w.Code)
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&_gotcha=bot", hdr); w.Code != 303 {
		t.Fatalf("honeypot = %d", w.Code)
	}
	if recs := analyticsRecords(t, e, site, since); len(recs) != 0 {
		t.Fatalf("records before a real send = %+v", recs)
	}

	w := submit(t, e, viewHost(site), f.Key, "name=Anna&email=anna%40example.com&message=Hallo+Geheimnis", hdr)
	if w.Code != 303 || rs.count() != 1 {
		t.Fatalf("submit = %d, mails %d", w.Code, rs.count())
	}
	recs := analyticsRecords(t, e, site, since)
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want 1", recs)
	}
	r := recs[0]
	if r.K != "e" || r.S != "srv" || r.N != "sitebin:form" || len(r.D) != 1 || r.D["form"] != f.Key {
		t.Errorf("event = %q %q %q %v", r.K, r.S, r.N, r.D)
	}
	if r.A != viewHost(site) || r.P != "/contact.html" {
		t.Errorf("address/path = %q %q", r.A, r.P)
	}
	raw := dayFileText(t, site, since)
	for _, leak := range []string{"Anna", "anna", "Hallo", "Geheimnis", "office@example.com", "203.0.113.7", "Mozilla"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the day file holds %q:\n%s", leak, raw)
		}
	}

	// Without analytics in effect, or for a visitor who opted out, a
	// submission is mailed and recorded nowhere.
	if err := e.st.SetAnalytics(site, nil); err != nil {
		t.Fatal(err)
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=again", hdr); w.Code != 303 {
		t.Fatalf("submit = %d", w.Code)
	}
	collecting(t, e, site, store.Analytics{})
	optOut := maps.Clone(hdr)
	optOut["Cookie"] = analyticsOptOutCookie + "=1"
	if w := submit(t, e, viewHost(site), f.Key, "message=again", optOut); w.Code != 303 {
		t.Fatalf("submit = %d", w.Code)
	}
	if recs := analyticsRecords(t, e, site, since); len(recs) != 1 {
		t.Fatalf("records = %+v, want still 1", recs)
	}
}
