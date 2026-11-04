package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/analytics"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The script's half of analytics: the tracker, the endpoint it sends to and
// the visitor's opt-out page. All three answer on a site's own origin under
// /_sitebin/ (<mount>/_sitebin/ under a custom domain with a path), which
// Caddy hands the backend before the gate, so they are same-origin for every
// page of the site: no CSP source, no CORS, no third party. See "The
// script", "The send endpoint" and "Opt-out" in
// docs/superpowers/specs/2026-11-04-site-analytics-design.md.

const (
	// maxSendBody bounds a beacon; a larger one is dropped unread.
	maxSendBody = 8 << 10
	// analyticsSendPerHour and analyticsSendBurst are the records one client
	// network may send one site: 60 a minute, 30 at once.
	analyticsSendPerHour = 3600
	analyticsSendBurst   = 30
	// optOutTTL is how long the opt-out cookie keeps a browser out.
	optOutTTL = 365 * 24 * time.Hour
)

// analyticsTarget resolves the site a /_sitebin/analytics request is for and
// the address it came in on (the host, or host/path under a mount), when the
// site collects there: analytics in effect and the address counted. The main
// domain, and with it every path view, names no site.
func (a *API) analyticsTarget(r *http.Request) (site *store.Site, addr, mount string, ok bool) {
	site, mount, err := a.servingSiteByAddress(r)
	if err != nil || !a.analyticsInEffect(site) {
		return nil, "", "", false
	}
	addr = strings.ToLower(hostWithoutPort(r.Host)) + mount
	if !analyticsCounts(site.Meta.Analytics, addr) {
		return nil, "", "", false
	}
	return site, addr, mount, true
}

// analyticsScript is GET /_sitebin/analytics.js: the tracker where the site
// collects on this address, else a stub whose umami.track and
// umami.identify do nothing, so a page calling them never throws.
func (a *API) analyticsScript(w http.ResponseWriter, r *http.Request) {
	name := "static/analytics-stub.js"
	if _, _, _, ok := a.analyticsTarget(r); ok {
		name = "static/analytics.js"
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFileFS(w, r, a.webFS, name)
}

// sendBody is a beacon in Umami's shape. The payload's hostname, and any
// field Umami adds that is not here, are ignored: the host is the request's.
type sendBody struct {
	Type    string `json:"type"`
	Payload struct {
		URL      string         `json:"url"`
		Referrer string         `json:"referrer"`
		Title    string         `json:"title"`
		Language string         `json:"language"`
		Screen   string         `json:"screen"`
		Tag      string         `json:"tag"`
		Name     string         `json:"name"`
		Data     map[string]any `json:"data"`
	} `json:"payload"`
}

// analyticsSend is POST /_sitebin/analytics/send. It answers 204 with no
// body whatever became of the beacon, so a sender learns nothing: not
// whether the site exists, collects, or kept the record.
func (a *API) analyticsSend(w http.ResponseWriter, r *http.Request) {
	a.collectBeacon(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// collectBeacon queues the hit a beacon describes, when it counts. The
// checks run cheapest first, and the rate is spent only on a record about to
// be queued. Nothing here is logged: not the payload, not the visitor.
func (a *API) collectBeacon(w http.ResponseWriter, r *http.Request) {
	site, addr, mount, ok := a.analyticsTarget(r)
	if !ok || optedOut(r) {
		return
	}
	// The browser rule server mode applies: a script that runs is no proof
	// of a browser (headless ones run it too).
	if ua := r.UserAgent(); !strings.Contains(ua, "Mozilla/") || analytics.IsBot(ua) {
		return
	}
	// Not a security boundary (anything but a browser can send it): it only
	// keeps other sites' pages from sending here.
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSendBody))
	if err != nil {
		return
	}
	var b sendBody
	if json.Unmarshal(raw, &b) != nil || b.Type != "event" {
		return
	}
	p := b.Payload
	rawPath, rawQuery, ok := beaconPage(p.URL, r.Host, mount)
	if !ok {
		return
	}
	s := site.Meta.Analytics
	full := requestPath(rawPath)
	if analyticsExcluded(s.Exclude, full, underMount(full, mount)) {
		return
	}
	// In server mode authz counts the page views itself; the script's would
	// count every one twice.
	if p.Name == "" && s.Mode != "script" {
		return
	}
	if !a.analyticsLimiter.Allow(site.ViewID + "|" + clientNet(clientIP(r))) {
		return
	}
	h := a.analyticsHit(r, site, addr)
	h.Source, h.Kind = "js", "p"
	h.RawPath, h.RawQuery = rawPath, rawQuery
	h.Referrer, h.Title, h.Screen, h.Tag = p.Referrer, p.Title, p.Screen, p.Tag
	if p.Language != "" {
		h.Language = p.Language // else the browser's Accept-Language, as authz records it
	}
	if p.Name != "" {
		h.Kind, h.Name, h.Data = "e", p.Name, p.Data
	}
	a.analytics.Add(h)
}

// beaconPage is the page a beacon names, as its raw path and query, when it
// is on host (the request's) and under mount. A bare path is on the
// requesting host: Umami's tracker accepts one from umami.track({url}) and a
// before-send hook. Anything else — another host, another scheme, a path
// beside the mount or one that leaves it by dot segments — names no page of
// this address.
func beaconPage(raw, host, mount string) (rawPath, rawQuery string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return "", "", false
	}
	if u.Scheme != "" || u.Host != "" {
		if u.Scheme != "http" && u.Scheme != "https" || !strings.EqualFold(u.Hostname(), hostWithoutPort(host)) {
			return "", "", false
		}
	}
	rawPath = u.EscapedPath()
	if !strings.HasPrefix(rawPath, "/") || !withinMount(requestPath(rawPath), mount) {
		return "", "", false
	}
	return rawPath, u.RawQuery, true
}

// withinMount reports whether the decoded, cleaned path p is the mount or
// below it. Every path is within the root ("").
func withinMount(p, mount string) bool {
	return mount == "" || p == mount || strings.HasPrefix(p, mount+"/")
}

// clientNet is the network a client address counts as for the send rate:
// an IPv4 address itself, an IPv6 address its /64 (one subscriber's usual
// allocation, so rotating addresses within it buys nothing). Anything that
// does not parse is its own key.
func clientNet(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	if addr = addr.Unmap(); addr.Is4() {
		return addr.String()
	}
	p, err := addr.Prefix(64)
	if err != nil {
		return ip
	}
	return p.String()
}

// analyticsOptOutPage is GET /_sitebin/analytics/opt-out: the visitor's
// "don't count me", on every address that serves a site, whether or not the
// site collects (the page never says which).
func (a *API) analyticsOptOutPage(w http.ResponseWriter, r *http.Request) {
	noFraming(w)
	_, mount, err := a.servingSiteByAddress(r)
	if err != nil {
		a.msgPage(w, 404, "Site not found", "There is no site at this address.")
		return
	}
	out := optedOut(r)
	now := "Right now this browser is not left out on " + optOutAddress(r, mount) + "."
	if out {
		now = "Right now this browser is left out on " + optOutAddress(r, mount) + "."
	}
	a.renderOptOut(w, mount, out, pageData{
		Title:   "Visitor statistics",
		Message: "Sites on Sitebin may count their visits anonymously, without cookies and without storing IP addresses. You can still leave this browser out.",
		More:    []string{now},
	})
}

// analyticsOptOut is POST /_sitebin/analytics/opt-out, action=out|in: it
// sets or clears the opt-out cookie, which authz and the send endpoint
// honour. From the page's form it answers a page that also sets or clears
// localStorage["umami.disabled"], the flag the script (and Umami's) reads;
// from the script's own fetch, which mirrors that flag into the cookie, it
// answers 204. Only the site's own pages may post here.
func (a *API) analyticsOptOut(w http.ResponseWriter, r *http.Request) {
	noFraming(w)
	_, mount, err := a.servingSiteByAddress(r)
	if err != nil {
		a.msgPage(w, 404, "Site not found", "There is no site at this address.")
		return
	}
	page := mount + "/_sitebin/analytics/opt-out"
	if a.fromAnotherSite(r) {
		a.renderPage(w, 403, pageData{Title: "Nothing changed", Code: "403",
			Message: "This request came from another site. Use the buttons on this site's own page instead.", Back: page})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	action := ""
	if r.ParseForm() == nil {
		action = r.PostFormValue("action")
	}
	if action != "out" && action != "in" {
		a.renderPage(w, 400, pageData{Title: "Nothing changed", Code: "400", Message: "Choose one of the two buttons.", Back: page})
		return
	}
	out := action == "out"
	http.SetCookie(w, a.optOutCookie(mount, out))
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" && m != "navigate" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	d := pageData{
		Title:       "This browser is counted again",
		Message:     "Visits from this browser to " + optOutAddress(r, mount) + " are counted again, anonymously, like everyone else's.",
		Script:      mount + "/_sitebin/assets/static/analytics-optout.js?v=" + a.assetVersion(),
		ScriptState: action,
	}
	if out {
		d.Title = "This browser is not counted"
		d.Message = "Visits from this browser to " + optOutAddress(r, mount) + " are no longer counted. The choice is kept in this browser for a year."
	}
	a.renderOptOut(w, mount, out, d)
}

// fromAnotherSite reports whether a browser says the POST comes from a page
// that is not this address's own: Sec-Fetch-Site other than same-origin, or
// — from a browser that sends no Sec-Fetch-Site (Safari before 16.4) — an
// Origin other than this request's scheme and host ("null" included).
func (a *API) fromAnotherSite(r *http.Request) bool {
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" {
		return s != "same-origin"
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	scheme := "https" // the scheme the instance's own URLs carry (config.SiteURL)
	if a.cfg.HTTPOnly {
		scheme = "http"
	}
	return !strings.EqualFold(o, scheme+"://"+r.Host)
}

// noFraming keeps the opt-out page out of other sites' frames, where a
// visitor could be made to press its buttons unseen.
func noFraming(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
}

// renderOptOut shows d with the page's two buttons, the one that would
// change nothing in the quiet style, and a way back to the site.
func (a *API) renderOptOut(w http.ResponseWriter, mount string, out bool, d pageData) {
	d.ChoiceAction = mount + "/_sitebin/analytics/opt-out"
	d.Choices = []pageChoice{
		{Value: "out", Label: "Don't count this browser", Quiet: out},
		{Value: "in", Label: "Count this browser again", Quiet: !out},
	}
	d.Back = mount + "/"
	a.renderPage(w, 200, d)
}

// optOutAddress is the address the opt-out page is shown on, as the visitor
// sees it.
func optOutAddress(r *http.Request, mount string) string {
	return strings.ToLower(hostWithoutPort(r.Host)) + mount
}

// optOutCookie sets (out) or clears the visitor's opt-out cookie for the
// address: the host's root, or its mount (no trailing slash, as the view
// cookie). HttpOnly: nothing on the page needs to read it.
func (a *API) optOutCookie(mount string, out bool) *http.Cookie {
	c := &http.Cookie{
		Name:     analyticsOptOutCookie,
		Value:    "1",
		Path:     "/",
		MaxAge:   int(optOutTTL.Seconds()),
		HttpOnly: true,
		Secure:   !a.cfg.HTTPOnly,
		SameSite: http.SameSiteLaxMode,
	}
	if mount != "" {
		c.Path = mount
	}
	if !out {
		c.Value, c.MaxAge = "", -1
	}
	return c
}
