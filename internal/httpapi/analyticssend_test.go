package httpapi

import (
	"bytes"
	"encoding/json"
	"html"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/analytics"
	"github.com/ittrail/sitebin.io/internal/store"
	"github.com/ittrail/sitebin.io/web"
)

// The script, the send endpoint and the opt-out page. See "The script", "The
// send endpoint" and "Opt-out" in
// docs/superpowers/specs/2026-11-04-site-analytics-design.md.

// sendReq is the script's beacon to /_sitebin/analytics/send on host: a
// same-origin fetch, with the headers a browser sends with one.
func sendReq(host, body string) *http.Request {
	req := httptest.NewRequest("POST", "/_sitebin/analytics/send", strings.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("User-Agent", navUA)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	return req
}

// beacon is what the script sends for the page at u, in Umami's shape, with
// payload fields replaced or added from extra (a nil value removes one).
func beacon(u string, extra map[string]any) string {
	host := ""
	if p, err := url.Parse(u); err == nil {
		host = p.Hostname()
	}
	p := map[string]any{
		"hostname": host,
		"language": "de-AT",
		"referrer": "",
		"screen":   "1920x1080",
		"title":    "About",
		"url":      u,
	}
	for k, v := range extra {
		if v == nil {
			delete(p, k)
		} else {
			p[k] = v
		}
	}
	b, _ := json.Marshal(map[string]any{"type": "event", "payload": p})
	return string(b)
}

// sent posts req to the public handler and checks the answer every beacon
// gets: 204, nothing else.
func (e *env) sent(t *testing.T, req *http.Request) {
	t.Helper()
	w := e.public(t, req)
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("send = %d %q, want 204 and no body", w.Code, w.Body)
	}
}

func TestSendRecordsEventInServerMode(t *testing.T) {
	since := time.Now()
	e, site := collectSite(t)
	collecting(t, e, site, store.Analytics{})

	page := "http://" + viewHost(site) + "/about.html?utm_campaign=herbst&gclid=abc123&email=a%40b.c#top"
	e.sent(t, sendReq(viewHost(site), beacon(page, map[string]any{
		"name":     "anfrage",
		"data":     map[string]any{"seite": "/", "anzahl": 3, "neu": true},
		"referrer": "https://www.google.com/search?q=secret",
		"tag":      "blau",
	})))

	recs := analyticsRecords(t, e, site, since)
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want 1", recs)
	}
	r := recs[0]
	if r.K != "e" || r.S != "js" || r.N != "anfrage" || r.A != viewHost(site) || r.P != "/about.html" {
		t.Errorf("kind/source/name/address/path = %q %q %q %q %q", r.K, r.S, r.N, r.A, r.P)
	}
	if len(r.D) != 3 || r.D["seite"] != "/" || r.D["anzahl"] != float64(3) || r.D["neu"] != true {
		t.Errorf("data = %v", r.D)
	}
	if len(r.Q) != 1 || r.Q["utm_campaign"] != "herbst" || len(r.Ck) != 1 || r.Ck[0] != "gclid" {
		t.Errorf("query = %v, click ids = %v", r.Q, r.Ck)
	}
	if r.R != "google.com/search" || r.Tg != "blau" || r.L != "de-AT" || r.Ti != "About" || r.Sc != "1920x1080" {
		t.Errorf("referrer/tag/language/title/screen = %q %q %q %q %q", r.R, r.Tg, r.L, r.Ti, r.Sc)
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
}

func TestAnalyticsAccessLogCarriesNoIP(t *testing.T) {
	e, site := scriptEnv(t)
	collecting(t, e, site, store.Analytics{})
	var logs bytes.Buffer
	e.api.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	visitor := map[string]string{"X-Forwarded-For": "203.0.113.7", "User-Agent": navUA, "Sec-Fetch-Site": "same-origin"}
	page := "http://" + viewHost(site) + "/about.html"

	// Every route a visitor reaches through analytics, the opt-out page's
	// own script with its version query included.
	e.sent(t, sendReq(viewHost(site), beacon(page, map[string]any{"name": "anfrage"})))
	get(t, e, viewHost(site), "/_sitebin/analytics.js", visitor)
	get(t, e, viewHost(site), "/_sitebin/analytics/opt-out", visitor)
	post(t, e, viewHost(site), "/_sitebin/analytics/opt-out", urlenc, strings.NewReader("action=out"), visitor)
	get(t, e, viewHost(site), "/_sitebin/assets/static/analytics-optout.js?v="+e.api.assetVersion(), visitor)
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("log lines = %d, want 5:\n%s", len(lines), logs.String())
	}
	for _, leak := range []string{"203.0.113.7", "anfrage", "Mozilla", "ip="} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("the log holds %q:\n%s", leak, logs.String())
		}
	}
	// The beacon comes once per page view: its line is debug, the rest info.
	if !strings.Contains(lines[0], "level=DEBUG") || !strings.Contains(lines[0], "path=/_sitebin/analytics/send") {
		t.Errorf("the beacon's line = %q, want a debug line", lines[0])
	}
	for _, l := range lines[1:] {
		if !strings.Contains(l, "level=INFO") {
			t.Errorf("line = %q, want info", l)
		}
	}
	// Every other route keeps the address, as before.
	logs.Reset()
	get(t, e, viewHost(site), "/_sitebin/assets/static/app.css", visitor)
	if !strings.Contains(logs.String(), "ip=203.0.113.7") {
		t.Errorf("another asset's line lost its address:\n%s", logs.String())
	}
}

func TestSendDropsPageviewInServerMode(t *testing.T) {
	since := time.Now()
	e, site := collectSite(t)
	collecting(t, e, site, store.Analytics{Mode: "server"})
	page := "http://" + viewHost(site) + "/about.html"
	// authz counts this page view in server mode; the script's would count it twice.
	e.sent(t, sendReq(viewHost(site), beacon(page, nil)))
	e.sent(t, sendReq(viewHost(site), beacon(page, map[string]any{"name": ""})))
	if recs := analyticsRecords(t, e, site, since); len(recs) != 0 {
		t.Fatalf("records = %+v, want none", recs)
	}
	e.sent(t, sendReq(viewHost(site), beacon(page, map[string]any{"name": "klick"})))
	if recs := analyticsRecords(t, e, site, since); len(recs) != 1 || recs[0].K != "e" || recs[0].N != "klick" {
		t.Fatalf("records = %+v, want the event", recs)
	}
}

func TestSendRecordsPageviewInScriptMode(t *testing.T) {
	since := time.Now()
	e, site := collectSite(t)
	collecting(t, e, site, store.Analytics{Mode: "script"})
	e.sent(t, sendReq(viewHost(site), beacon("http://"+viewHost(site)+"/about.html", map[string]any{"referrer": "https://news.example/item?id=7"})))
	recs := analyticsRecords(t, e, site, since)
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want 1", recs)
	}
	r := recs[0]
	if r.K != "p" || r.S != "js" || r.N != "" || r.A != viewHost(site) || r.P != "/about.html" || r.Ti != "About" {
		t.Errorf("kind/source/name/address/path/title = %q %q %q %q %q %q", r.K, r.S, r.N, r.A, r.P, r.Ti)
	}
	if r.B != "Chrome" || r.O != "Windows" || r.Sc != "1920x1080" || r.Dv != "desktop" || r.R != "news.example/item" {
		t.Errorf("browser/os/screen/device/referrer = %q %q %q %q %q", r.B, r.O, r.Sc, r.Dv, r.R)
	}
	// The visitor id hashes the address Caddy saw and the request's user
	// agent, as server mode's does.
	salt, err := e.api.AnalyticsSalts().For(analytics.LocalDate(time.UnixMilli(r.T), time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if want := analytics.VisitorID(salt, site.ViewID, "203.0.113.7", navUA); r.V != want {
		t.Errorf("visitor id = %q, want %q", r.V, want)
	}
	// Without a language in the payload, the browser's own header names it.
	e.sent(t, sendReq(viewHost(site), beacon("http://"+viewHost(site)+"/", map[string]any{"language": nil})))
	if recs := analyticsRecords(t, e, site, since); len(recs) != 2 || recs[1].L != "en-US" {
		t.Fatalf("records = %+v, want the header's language", recs)
	}
}

func TestSendRejectsGarbage(t *testing.T) {
	lock := func(t *testing.T, e *env, site *store.Site) {
		if _, err := e.st.SetLock(site, &store.SiteLock{By: store.LockByAdmin}); err != nil {
			t.Fatal(err)
		}
	}
	type rec = analytics.Record
	for _, c := range []struct {
		name  string
		host  string                              // default: the generated address
		url   string                              // default: http://<host>/about.html
		body  func(u string) string               // default: beacon(u, extra)
		extra map[string]any                      // payload changes
		hdr   map[string]string                   // request header changes
		setup func(*testing.T, *env, *store.Site) // after script mode is on
		n     int                                 // records expected
		check func(*testing.T, rec)
	}{
		{name: "a page view", n: 1},
		{name: "not JSON", body: func(string) string { return "a page view, please" }},
		{name: "a body over 8 KiB", extra: map[string]any{"title": strings.Repeat("a", 9<<10)}},
		{name: "an identify", body: func(u string) string {
			return strings.Replace(beacon(u, nil), `"type":"event"`, `"type":"identify"`, 1)
		}},
		{name: "a payload that is not an object", body: func(string) string { return `{"type":"event","payload":"x"}` }},
		{name: "data that is not an object", extra: map[string]any{"name": "klick", "data": []int{1, 2}}},
		{name: "a url on another host", url: "https://evil.example/about.html"},
		{name: "a protocol-relative url on another host", url: "//evil.example/about.html"},
		{name: "a url that is not http", url: "ftp://{host}/about.html"},
		{name: "no url", extra: map[string]any{"url": nil}},
		{name: "a path on this host", url: "/about.html?utm_source=x", n: 1, check: func(t *testing.T, r rec) {
			if r.P != "/about.html" || r.Q["utm_source"] != "x" {
				t.Errorf("path/query = %q %v", r.P, r.Q)
			}
		}},
		{name: "a url under the mount", host: "example.org/blog", url: "http://example.org/blog/about.html", n: 1, check: func(t *testing.T, r rec) {
			if r.A != "example.org/blog" || r.P != "/blog/about.html" {
				t.Errorf("address/path = %q %q", r.A, r.P)
			}
		}},
		{name: "a url outside the mount", host: "example.org/blog", url: "http://example.org/about.html"},
		{name: "a url beside the mount", host: "example.org/blog", url: "http://example.org/blogger/about.html"},
		{name: "a url leaving the mount by dot segments", host: "example.org/blog", url: "http://example.org/blog/../about.html"},
		{name: "a nested data value is dropped and the event kept", extra: map[string]any{
			"name": "klick", "data": map[string]any{"ziel": "kontakt", "tief": map[string]any{"a": 1}, "liste": []int{1}},
		}, n: 1, check: func(t *testing.T, r rec) {
			if r.K != "e" || len(r.D) != 1 || r.D["ziel"] != "kontakt" {
				t.Errorf("event = %q %v", r.K, r.D)
			}
		}},
		{name: "an event name over 50 characters", extra: map[string]any{"name": strings.Repeat("n", 51)}},
		{name: "a cross-site page", hdr: map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{name: "a same-site page", hdr: map[string]string{"Sec-Fetch-Site": "same-site"}},
		{name: "a client that sends no fetch metadata", hdr: map[string]string{"Sec-Fetch-Site": "", "Sec-Fetch-Mode": "", "Sec-Fetch-Dest": ""}, n: 1},
		{name: "a bot", hdr: map[string]string{"User-Agent": "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"}},
		{name: "a headless browser", hdr: map[string]string{"User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/130.0.0.0 Safari/537.36"}},
		{name: "a user agent without the Mozilla token", hdr: map[string]string{"User-Agent": "Opera/9.80 (Windows NT 6.1; U; en) Presto/2.12.388 Version/12.18"}},
		{name: "no user agent", hdr: map[string]string{"User-Agent": ""}},
		{name: "the opt-out cookie", hdr: map[string]string{"Cookie": "a=b; " + analyticsOptOutCookie + "=1"}},
		{name: "an excluded path", url: "http://{host}/kinder/spiel.html", setup: func(t *testing.T, e *env, site *store.Site) {
			collecting(t, e, site, store.Analytics{Mode: "script", Exclude: []string{"/kinder/**"}})
		}},
		{name: "a percent-encoded excluded path", url: "http://{host}/%6Binder/spiel.html", setup: func(t *testing.T, e *env, site *store.Site) {
			collecting(t, e, site, store.Analytics{Mode: "script", Exclude: []string{"/kinder/**"}})
		}},
		{name: "an address not counted", setup: func(t *testing.T, e *env, site *store.Site) {
			collecting(t, e, site, store.Analytics{Mode: "script", Addresses: []string{"example.org"}})
		}},
		{name: "analytics off", setup: func(t *testing.T, e *env, site *store.Site) {
			if err := e.st.SetAnalytics(site, &store.Analytics{Mode: "script"}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a locked site", setup: lock},
		{name: "a redirecting address", host: "www.example.org", setup: func(t *testing.T, e *env, site *store.Site) {
			if err := e.st.SetDomainRedirect(site, "www.example.org", "example.org", 301); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "an address of no site", host: "nobody.example"},
	} {
		t.Run(c.name, func(t *testing.T) {
			since := time.Now()
			e, site := collectSite(t)
			collecting(t, e, site, store.Analytics{Mode: "script"})
			if c.setup != nil {
				c.setup(t, e, site)
			}
			addr := c.host
			if addr == "" {
				addr = viewHost(site)
			}
			host, mount, _ := strings.Cut(addr, "/")
			u := c.url
			if u == "" {
				u = "http://{host}/about.html"
			}
			u = strings.ReplaceAll(u, "{host}", host)
			body := beacon(u, c.extra)
			if c.body != nil {
				body = c.body(u)
			}
			req := sendReq(host, body)
			if mount != "" {
				req.Header.Set(mountHeader, "/"+mount)
			}
			for k, v := range c.hdr {
				req.Header.Set(k, v)
			}
			e.sent(t, req)
			recs := analyticsRecords(t, e, site, since)
			if len(recs) != c.n {
				t.Fatalf("records = %+v, want %d", recs, c.n)
			}
			if c.check != nil {
				c.check(t, recs[0])
			}
		})
	}
}

func TestSendRateLimited(t *testing.T) {
	since := time.Now()
	e, site := collectSite(t)
	collecting(t, e, site, store.Analytics{Mode: "script"})
	now := time.Now()
	e.api.analyticsLimiter.Now = func() time.Time { return now }
	page := "http://" + viewHost(site) + "/about.html"
	from := func(ip string) {
		req := sendReq(viewHost(site), beacon(page, nil))
		req.Header.Set("X-Forwarded-For", ip)
		e.sent(t, req)
	}
	for range 31 {
		from("203.0.113.7")
	}
	if n := len(analyticsRecords(t, e, site, since)); n != 30 {
		t.Fatalf("records = %d, want the burst of 30", n)
	}
	// Another client is not held back; the bucket refills at one a second.
	from("198.51.100.4")
	now = now.Add(time.Second)
	from("203.0.113.7")
	if n := len(analyticsRecords(t, e, site, since)); n != 32 {
		t.Fatalf("records = %d, want 32", n)
	}
	// An IPv6 client is its /64: every address in it shares one bucket.
	for i := range 31 {
		from("2001:db8:a:b::" + strconv.FormatInt(int64(i+1), 16))
	}
	from("2001:db8:a:c::1")
	if n := len(analyticsRecords(t, e, site, since)); n != 32+30+1 {
		t.Fatalf("records = %d, want %d", n, 32+30+1)
	}
}

func TestClientNet(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"203.0.113.7", "203.0.113.7"},
		{"::ffff:203.0.113.7", "203.0.113.7"},
		{"2001:db8:a:b:1:2:3:4", "2001:db8:a:b::/64"},
		{"2001:db8:a:b::ffff", "2001:db8:a:b::/64"},
		{"not an address", "not an address"},
		{"", ""},
	} {
		if got := clientNet(c.in); got != c.want {
			t.Errorf("clientNet(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// scriptEnv is collectSite serving the real embedded scripts.
func scriptEnv(t *testing.T) (*env, *store.Site) {
	t.Helper()
	e, site := collectSite(t)
	e.api.webFS = web.Assets
	return e, site
}

// getScript fetches /_sitebin/analytics.js on addr (host or host/path) and
// checks the headers every answer carries.
func getScript(t *testing.T, e *env, addr string) string {
	t.Helper()
	return getScriptAt(t, e, addr, "/_sitebin/analytics.js")
}

func getScriptAt(t *testing.T, e *env, addr, target string) string {
	t.Helper()
	host, mount, _ := strings.Cut(addr, "/")
	hdr := map[string]string{}
	if mount != "" {
		hdr[mountHeader] = "/" + mount
	}
	w := get(t, e, host, target, hdr)
	if w.Code != 200 {
		t.Fatalf("script on %s = %d", addr, w.Code)
	}
	for k, want := range map[string]string{
		"Content-Type":           "text/javascript; charset=utf-8",
		"Cache-Control":          "public, max-age=3600",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s on %s = %q, want %q", k, addr, got, want)
		}
	}
	return w.Body.String()
}

// isStub: umami.track and umami.identify resolve at once and send nothing.
func isStub(body string) bool {
	return strings.Contains(body, "track() { return Promise.resolve(); }") &&
		strings.Contains(body, "identify() { return Promise.resolve(); }") &&
		!strings.Contains(body, "/_sitebin/analytics/send") && !strings.Contains(body, "fetch(")
}

func isFull(body string) bool {
	return strings.Contains(body, "/_sitebin/analytics/send") && strings.Contains(body, "umami")
}

func TestScriptStubWhenOff(t *testing.T) {
	e, site := scriptEnv(t)
	if body := getScript(t, e, viewHost(site)); !isStub(body) {
		t.Fatalf("analytics off: not the stub:\n%s", body)
	}
	// In effect, but not counted on this address.
	collecting(t, e, site, store.Analytics{Addresses: []string{"example.org"}})
	if body := getScript(t, e, viewHost(site)); !isStub(body) {
		t.Fatalf("an address not counted: not the stub:\n%s", body)
	}
	if body := getScript(t, e, "example.org"); !isFull(body) {
		t.Fatalf("the counted address: not the script:\n%s", body)
	}
	if _, err := e.st.SetLock(site, &store.SiteLock{By: store.LockByAdmin}); err != nil {
		t.Fatal(err)
	}
	if body := getScript(t, e, "example.org"); !isStub(body) {
		t.Fatalf("a locked site: not the stub:\n%s", body)
	}
	if body := getScript(t, e, "nobody.example"); !isStub(body) {
		t.Fatalf("no site: not the stub:\n%s", body)
	}
}

func TestScriptFullWhenOn(t *testing.T) {
	e, site := scriptEnv(t)
	collecting(t, e, site, store.Analytics{})
	for _, addr := range []string{viewHost(site), "example.org", "example.org/blog"} {
		if body := getScript(t, e, addr); !isFull(body) {
			t.Fatalf("%s: not the script:\n%s", addr, body)
		}
	}
	// The address of a domain that redirects serves nothing of the site.
	if err := e.st.SetDomainRedirect(site, "www.example.org", "example.org", 301); err != nil {
		t.Fatal(err)
	}
	if body := getScript(t, e, "www.example.org"); !isStub(body) {
		t.Fatalf("a redirecting address: not the stub:\n%s", body)
	}
}

// optOutCookieOf is the opt-out cookie a response sets, or nil.
func optOutCookieOf(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == analyticsOptOutCookie {
			return c
		}
	}
	return nil
}

// scriptSrc is the src of the one external script a page loads.
var scriptSrc = regexp.MustCompile(`<script src="([^"]+)"`)

func TestOptOutSetsAndClearsCookie(t *testing.T) {
	since := time.Now()
	e, site := scriptEnv(t)
	collecting(t, e, site, store.Analytics{Mode: "script"})
	form := map[string]string{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}
	optOut := func(host, mount, action string, hdr map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		h := map[string]string{}
		maps.Copy(h, hdr)
		if mount != "" {
			h[mountHeader] = mount
		}
		return post(t, e, host, "/_sitebin/analytics/opt-out", urlenc, strings.NewReader("action="+action), h)
	}

	// The page: two buttons, posting to this address.
	w := get(t, e, viewHost(site), "/_sitebin/analytics/opt-out", nil)
	body := w.Body.String()
	if w.Code != 200 || optOutCookieOf(w) != nil {
		t.Fatalf("page = %d, cookie %v", w.Code, optOutCookieOf(w))
	}
	for _, want := range []string{`action="/_sitebin/analytics/opt-out"`, `name="action" value="out"`, `name="action" value="in"`, "t count this browser", "Count this browser again"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q:\n%s", want, body)
		}
	}

	// Out: the cookie, and the page mirrors the choice into localStorage
	// through a script of its own (never an inline one).
	w = optOut(viewHost(site), "", "out", form)
	c := optOutCookieOf(w)
	if w.Code != 200 || c == nil {
		t.Fatalf("out = %d, cookie %v", w.Code, c)
	}
	if c.Value != "1" || c.Path != "/" || c.MaxAge != 365*24*3600 || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure {
		t.Errorf("cookie = %+v", c)
	}
	body = w.Body.String()
	if !strings.Contains(body, `src="/_sitebin/assets/static/analytics-optout.js?v=`) || !strings.Contains(body, `data-state="out"`) || strings.Contains(body, "<script>") {
		t.Errorf("out page does not load the mirror:\n%s", body)
	}
	if m := scriptSrc.FindStringSubmatch(body); m == nil {
		t.Errorf("no script on the out page")
	} else if w := get(t, e, viewHost(site), html.UnescapeString(m[1]), nil); w.Code != 200 || !strings.Contains(w.Body.String(), "umami.disabled") {
		t.Errorf("the mirror script %s = %d:\n%s", m[1], w.Code, w.Body)
	}
	// With the cookie, the browser's beacons count no more.
	page := "http://" + viewHost(site) + "/about.html"
	req := sendReq(viewHost(site), beacon(page, nil))
	req.AddCookie(c)
	e.sent(t, req)
	if recs := analyticsRecords(t, e, site, since); len(recs) != 0 {
		t.Fatalf("records = %+v, want none after the opt-out", recs)
	}
	// The page knows this browser is out.
	w = get(t, e, viewHost(site), "/_sitebin/analytics/opt-out", map[string]string{"Cookie": analyticsOptOutCookie + "=1"})
	if !strings.Contains(w.Body.String(), "left out") {
		t.Errorf("page for an opted-out browser:\n%s", w.Body)
	}

	// In: the cookie is cleared, and so is the flag.
	w = optOut(viewHost(site), "", "in", form)
	if c := optOutCookieOf(w); w.Code != 200 || c == nil || c.MaxAge >= 0 || c.Path != "/" {
		t.Fatalf("in = %d, cookie %+v", w.Code, c)
	}
	if !strings.Contains(w.Body.String(), `data-state="in"`) {
		t.Errorf("in page does not clear the flag:\n%s", w.Body)
	}

	// The script's mirror is a fetch: the cookie and nothing else.
	w = optOut(viewHost(site), "", "out", map[string]string{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"})
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 || optOutCookieOf(w) == nil {
		t.Fatalf("fetch = %d %q, cookie %v", w.Code, w.Body, optOutCookieOf(w))
	}

	// Under a path, the cookie is the path's and the page posts there.
	w = optOut("example.org", "/blog", "out", form)
	if c := optOutCookieOf(w); w.Code != 200 || c == nil || c.Path != "/blog" {
		t.Fatalf("mounted out = %d, cookie %+v", w.Code, c)
	}
	if body := w.Body.String(); !strings.Contains(body, `action="/blog/_sitebin/analytics/opt-out"`) || !strings.Contains(body, `src="/blog/_sitebin/assets/static/analytics-optout.js?v=`) {
		t.Errorf("mounted page:\n%s", body)
	}

	// Refused: another site's page, an action that is neither, no site. A
	// browser without fetch metadata (Safari before 16.4) still sends Origin.
	for _, c := range []struct {
		name, host, action string
		hdr                map[string]string
		want               int
	}{
		{"cross-site", viewHost(site), "out", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate"}, 403},
		{"same-site", viewHost(site), "out", map[string]string{"Sec-Fetch-Site": "same-site", "Sec-Fetch-Mode": "navigate"}, 403},
		{"another origin, no fetch metadata", viewHost(site), "out", map[string]string{"Origin": "http://evil.example"}, 403},
		{"a sibling site's origin, no fetch metadata", viewHost(site), "out", map[string]string{"Origin": "http://other.sitebin.example"}, 403},
		{"this host on another scheme, no fetch metadata", viewHost(site), "out", map[string]string{"Origin": "https://" + viewHost(site)}, 403},
		{"an opaque origin, no fetch metadata", viewHost(site), "out", map[string]string{"Origin": "null"}, 403},
		{"no action", viewHost(site), "maybe", form, 400},
		{"no site", "nobody.example", "out", form, 404},
	} {
		if w := optOut(c.host, "", c.action, c.hdr); w.Code != c.want || optOutCookieOf(w) != nil {
			t.Errorf("%s = %d, cookie %v; want %d and none", c.name, w.Code, optOutCookieOf(w), c.want)
		}
	}
	// Its own origin, or no word at all (not a browser), is let through.
	for _, hdr := range []map[string]string{
		{"Origin": "http://" + viewHost(site)},
		{"Origin": "HTTP://" + strings.ToUpper(viewHost(site))},
		{},
	} {
		if w := optOut(viewHost(site), "", "out", hdr); w.Code != 200 || optOutCookieOf(w) == nil {
			t.Errorf("%v = %d, cookie %v; want 200 and the cookie", hdr, w.Code, optOutCookieOf(w))
		}
	}
	if w := optOut("example.org", "/blog", "out", map[string]string{"Origin": "http://example.org"}); w.Code != 200 || optOutCookieOf(w) == nil {
		t.Errorf("mounted, own origin = %d, cookie %v", w.Code, optOutCookieOf(w))
	}

	// On a TLS instance the cookie is Secure, and the own origin is https.
	e.api.cfg.HTTPOnly = false
	if c := optOutCookieOf(optOut(viewHost(site), "", "out", form)); c == nil || !c.Secure {
		t.Errorf("TLS cookie = %+v", c)
	}
	if w := optOut(viewHost(site), "", "out", map[string]string{"Origin": "https://" + viewHost(site)}); w.Code != 200 {
		t.Errorf("TLS, own origin = %d", w.Code)
	}
	if w := optOut(viewHost(site), "", "out", map[string]string{"Origin": "http://" + viewHost(site)}); w.Code != 403 {
		t.Errorf("TLS, plain-http origin = %d, want 403", w.Code)
	}
}

func TestOptOutPageIsNeverFramed(t *testing.T) {
	e, site := scriptEnv(t)
	form := map[string]string{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"the page":         get(t, e, viewHost(site), "/_sitebin/analytics/opt-out", nil),
		"the out page":     post(t, e, viewHost(site), "/_sitebin/analytics/opt-out", urlenc, strings.NewReader("action=out"), form),
		"the in page":      post(t, e, viewHost(site), "/_sitebin/analytics/opt-out", urlenc, strings.NewReader("action=in"), form),
		"a refusal":        post(t, e, viewHost(site), "/_sitebin/analytics/opt-out", urlenc, strings.NewReader("action=out"), map[string]string{"Origin": "http://evil.example"}),
		"the mounted page": get(t, e, "example.org", "/_sitebin/analytics/opt-out", map[string]string{mountHeader: "/blog"}),
	} {
		if got := w.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
			t.Errorf("%s: Content-Security-Policy = %q", name, got)
		}
		if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q", name, got)
		}
	}
}

func TestScriptAndSendOnPathViewAreInert(t *testing.T) {
	since := time.Now()
	e := newEnv(t, map[string]string{"SITEBIN_VIEW_ACCESS": "both"})
	e.api.webFS = web.Assets
	site := e.ownedSite(t, "", map[string]string{"index.html": "<h1>home</h1>"})
	collecting(t, e, site, store.Analytics{Mode: "script"})
	main := e.cfg.BaseDomain
	pathPage := "http://" + main + "/v/" + site.ViewID + "/"

	for _, target := range []string{"/_sitebin/analytics.js", "/_sitebin/analytics.js?_site=" + site.ViewID} {
		if body := getScriptAt(t, e, main, target); !isStub(body) {
			t.Fatalf("%s on the main domain: not the stub:\n%s", target, body)
		}
	}
	for _, target := range []string{"/_sitebin/analytics/send", "/_sitebin/analytics/send?_site=" + site.ViewID} {
		req := sendReq(main, beacon(pathPage, nil))
		req.URL.RawQuery = strings.TrimPrefix(target, "/_sitebin/analytics/send?")
		e.sent(t, req)
	}
	if recs := analyticsRecords(t, e, site, since); len(recs) != 0 {
		t.Fatalf("records = %+v, want none from the main domain", recs)
	}
	if w := get(t, e, main, "/_sitebin/analytics/opt-out?_site="+site.ViewID, nil); w.Code != 404 {
		t.Errorf("opt-out page on the main domain = %d, want 404", w.Code)
	}
	w := post(t, e, main, "/_sitebin/analytics/opt-out?_site="+site.ViewID, urlenc, strings.NewReader("action=out"), map[string]string{"Sec-Fetch-Site": "same-origin"})
	if w.Code != 404 || optOutCookieOf(w) != nil {
		t.Errorf("opt-out on the main domain = %d, cookie %v; want 404 and none", w.Code, optOutCookieOf(w))
	}

	// The same site counts on its own address.
	if body := getScript(t, e, viewHost(site)); !isFull(body) {
		t.Fatalf("the site's own address: not the script:\n%s", body)
	}
	e.sent(t, sendReq(viewHost(site), beacon("http://"+viewHost(site)+"/", nil)))
	if recs := analyticsRecords(t, e, site, since); len(recs) != 1 {
		t.Fatalf("records = %+v, want the one from the site's address", recs)
	}
}
