package httpapi

import (
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ittrail/sitebin.io/internal/analytics"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Collection: what authz and the forms handler hand the analytics recorder.
// Every rule that decides whether a request counts is checked here, before a
// hit exists, because the recorder writes whatever it is given for any site
// that exists. authz only queues (Recorder.Add never blocks), and nothing
// here changes what it answers. See "Collection" in
// docs/superpowers/specs/2026-11-04-site-analytics-design.md.

// analyticsOptOutCookie is the visitor's "don't count me" (value "1"), set
// on the site's own origin by the opt-out page. The browser sends it with
// every request there, and forward_auth passes the Cookie header to authz.
const analyticsOptOutCookie = "sitebin_analytics_optout"

// formEventName is the event a sent form submission records.
const formEventName = "sitebin:form"

// recordPageView queues a page view for a request authz admitted, when it
// counts: analytics in effect and in server mode, a browser navigating to a
// page (GET, HTML, no prefetch), a user agent that is a browser and no bot,
// no opt-out, an address the site counts and a path it does not exclude. A
// file site's hit carries the check that the path is a page, which the
// recorder runs off the request path.
//
// addr is the address authz matched and mount its path; a path view counts
// under the site's generated address.
func (a *API) recordPageView(r *http.Request, site *store.Site, addr, mount string, pathMode bool) {
	s := site.Meta.Analytics
	// The site's own switch first: most sites have none, and authz stays
	// as cheap for them as it was.
	if s == nil || !s.Enabled || s.Mode == "script" || !a.analyticsInEffect(site) {
		return
	}
	// authz is forward_auth's GET; the visitor's method comes with it.
	method := r.Header.Get("X-Forwarded-Method")
	if method == "" {
		method = r.Method
	}
	ua := r.Header.Get("User-Agent")
	if !analytics.IsNavigation(r.Header, method) || !strings.Contains(ua, "Mozilla/") || analytics.IsBot(ua) || optedOut(r) {
		return
	}
	prefix := mount
	if pathMode {
		addr, prefix = a.generatedAddress(site), "/v/"+site.ViewID
	}
	if !analyticsCounts(s, addr) {
		return
	}
	uri := r.Header.Get("X-Forwarded-Uri")
	full := requestPath(uri) // decoded and cleaned, as the file server sees it
	inSite := underMount(full, prefix)
	if analyticsExcluded(s.Exclude, full, inSite) {
		return
	}
	rawPath, rawQuery, _ := strings.Cut(uri, "?")
	if !strings.HasPrefix(rawPath, "/") {
		rawPath = full
	}
	h := a.analyticsHit(r, site, addr)
	h.Kind = "p"
	h.RawPath, h.RawQuery = rawPath, rawQuery
	h.Referrer = r.Header.Get("Referer")
	if site.Meta.Mode != store.ModeContainer {
		h.PageCheck = servedPage(site, inSite)
	}
	a.analytics.Add(h)
}

// recordFormEvent queues the event of a form submission that was sent, in
// either mode, when analytics is in effect, the address counts, the visitor
// has not opted out and the page the form was on is not excluded. It names
// the form's key and nothing the visitor wrote.
func (a *API) recordFormEvent(r *http.Request, site *store.Site, key string) {
	s := site.Meta.Analytics
	if !a.analyticsInEffect(site) || optedOut(r) {
		return
	}
	// The address the form was posted on, as formSite resolved it.
	addr, prefix := strings.ToLower(hostWithoutPort(r.Host)), r.Header.Get(mountHeader)
	if a.formPathViewID(r) != "" {
		addr, prefix = a.generatedAddress(site), "/v/"+site.ViewID
	} else {
		addr += prefix
	}
	if !analyticsCounts(s, addr) {
		return
	}
	// The page the form was on, when the browser said and it is this host's.
	var rawPath string
	if u, err := url.Parse(r.Referer()); err == nil && strings.EqualFold(u.Host, r.Host) && strings.HasPrefix(u.Path, "/") {
		rawPath = u.EscapedPath()
		full := requestPath(rawPath)
		if analyticsExcluded(s.Exclude, full, underMount(full, prefix)) {
			return
		}
	}
	h := a.analyticsHit(r, site, addr)
	h.Kind, h.Name, h.Data = "e", formEventName, map[string]any{"form": key}
	h.RawPath = rawPath
	h.Referrer = r.Referer()
	a.analytics.Add(h)
}

// analyticsHit is a hit with what every kind shares: the server's time, the
// site, the address, the visitor's network address and browser, and the
// site's own addresses, query names and zone. Its slices are its own.
func (a *API) analyticsHit(r *http.Request, site *store.Site, addr string) analytics.Hit {
	return analytics.Hit{
		Time:       time.Now(),
		ViewID:     site.ViewID,
		Source:     "srv",
		Address:    addr,
		IP:         clientIP(r),
		UA:         r.Header.Get("User-Agent"),
		CHMobile:   r.Header.Get("Sec-CH-UA-Mobile"),
		CHPlatform: r.Header.Get("Sec-CH-UA-Platform"),
		Language:   firstLanguage(r.Header.Get("Accept-Language")),
		Own:        a.siteAddresses(site),
		ExtraQuery: slices.Clone(site.Meta.Analytics.Query),
		TZ:         a.analyticsTZ(site),
	}
}

// optedOut reports whether the request carries the opt-out cookie.
func optedOut(r *http.Request) bool {
	for _, c := range r.CookiesNamed(analyticsOptOutCookie) {
		if c.Value == "1" {
			return true
		}
	}
	return false
}

// analyticsCounts reports whether the site counts addr: every address when
// its settings name none.
func analyticsCounts(s *store.Analytics, addr string) bool {
	return len(s.Addresses) == 0 || slices.Contains(s.Addresses, addr)
}

// firstLanguage is the first tag of an Accept-Language header.
func firstLanguage(h string) string {
	tag, _, _ := strings.Cut(h, ",")
	tag, _, _ = strings.Cut(tag, ";")
	return strings.TrimSpace(tag)
}

// generatedAddress is the site's own address as analytics names it: its
// subdomain, or on an instance without subdomain views its path view on the
// main domain.
func (a *API) generatedAddress(site *store.Site) string {
	if a.cfg.SubdomainViews() {
		return site.ViewID + "." + a.cfg.ViewDomain
	}
	return a.cfg.BaseDomain + "/v/" + site.ViewID
}

// siteAddresses is every address of the site as stored: its generated
// address (a container site's one per HTTP port as well) and its verified
// custom domains, host or host/path.
func (a *API) siteAddresses(site *store.Site) []string {
	out := []string{a.generatedAddress(site)}
	if site.Meta.Mode == store.ModeContainer && a.cfg.SubdomainViews() {
		for _, ad := range site.Meta.Addresses {
			if ad.Label != site.ViewID {
				out = append(out, ad.Label+"."+a.cfg.ViewDomain)
			}
		}
	}
	return append(out, site.Meta.CustomDomains...)
}

// analyticsZones caches loaded time zones by name. The names are a site's
// setting or the instance's, both validated as IANA names on the way in.
var analyticsZones sync.Map // name → *time.Location

// analyticsTZ is the zone the site's days are counted in: its setting, else
// the instance's. A name that does not load is UTC.
func (a *API) analyticsTZ(site *store.Site) *time.Location {
	name := a.cfg.AnalyticsTimezone
	if s := site.Meta.Analytics; s != nil && s.Timezone != "" {
		name = s.Timezone
	}
	if loc, ok := analyticsZones.Load(name); ok {
		return loc.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil || name == "Local" { // never the server's own zone
		return time.UTC
	}
	analyticsZones.Store(name, loc)
	return loc
}

// analyticsGlobs caches compiled exclude globs by pattern, so authz does not
// compile a site's list on every request. Patterns change only with a site's
// settings; the cache starts over when it holds more than maxCachedGlobs.
var (
	analyticsGlobs     sync.Map // pattern → *store.Glob (nil: not a usable glob)
	analyticsGlobCount atomic.Int64
)

const maxCachedGlobs = 4096

func analyticsGlob(pattern string) *store.Glob {
	if g, ok := analyticsGlobs.Load(pattern); ok {
		return g.(*store.Glob)
	}
	g, _ := store.CompileGlob(pattern)
	if analyticsGlobCount.Add(1) > maxCachedGlobs {
		analyticsGlobs.Clear()
		analyticsGlobCount.Store(1)
	}
	analyticsGlobs.Store(pattern, g)
	return g
}

// analyticsExcluded reports whether an exclude glob matches one of the
// decoded paths: the one the visitor asked for and, below a mount or a path
// view, the site's own. An exclusion is a promise not to count, so either
// spelling of the page keeps it.
func analyticsExcluded(patterns []string, paths ...string) bool {
	for _, p := range patterns {
		if g := analyticsGlob(p); g != nil {
			for _, s := range paths {
				if g.Match(s) {
					return true
				}
			}
		}
	}
	return false
}

// servedPage is a file site's page check for p, the decoded path within the
// site. It holds the site as authz loaded it (read-only from here on) and
// the path, never the request, and runs in the recorder. A site deleted in
// the meantime has no page.
func servedPage(site *store.Site, p string) func() bool {
	return func() bool {
		root, err := store.OpenServedRoot(site)
		if err != nil {
			return false
		}
		defer root.Close()
		return isServedPage(root, p)
	}
}

// indexNames are the file server's index files (Caddy's defaults; the
// Caddyfile sets none).
var indexNames = []string{"index.html", "index.txt"}

// isServedPage mirrors the file serving the Caddyfile sets up
// (caddygen.writeFileServing): with the SPA marker every path is a page, the
// fallback being index.html; otherwise a regular file asked for without a
// trailing slash, or a folder asked for with one that holds an index file.
// Anything else is a 404, a redirect to the canonical form, or a folder
// listing, and none of those is a page view.
func isServedPage(root *os.Root, p string) bool {
	if b := path.Base(p); b == store.SPAMarker || b == store.TrustedMarker {
		return false // hidden by the file server
	}
	if fi, err := root.Stat(store.SPAMarker); err == nil && fi.Mode().IsRegular() {
		return true
	}
	asDir := strings.HasSuffix(p, "/")
	name := strings.Trim(p, "/")
	if name == "" {
		name = "."
	}
	fi, err := root.Stat(name)
	switch {
	case err != nil:
		return false
	case fi.Mode().IsRegular():
		return !asDir
	case fi.IsDir() && asDir:
		for _, index := range indexNames {
			if fi, err := root.Stat(path.Join(name, index)); err == nil && fi.Mode().IsRegular() {
				return true
			}
		}
	}
	return false
}
