package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// embedOriginAllowed reports whether origin (already lowercased) is
// allowlisted for cross-origin use of the create flow. Cross-origin embedding
// is an enterprise capability, so it also requires an active provider.
func (a *API) embedOriginAllowed(origin string) bool {
	if origin == "" || len(a.cfg.EmbedOrigins) == 0 {
		return false
	}
	if p, ok := ext.Get(); !ok || !p.EmbedOriginsAllowed() {
		return false
	}
	for _, allowed := range a.cfg.EmbedOrigins {
		if allowed == "*" || allowed == origin {
			return true
		}
	}
	return false
}

// fromOwnBrowser reports whether the request carries the fetch metadata a
// browser sends when Sitebin's own pages — or an allowlisted embed — call the
// API. Scripts, CI jobs and agents typically send neither header.
//
// This is a PLAN boundary, not a security boundary. Sec-Fetch-Site and Origin
// are trivially forgeable outside a browser; the point is to keep automation
// on accounts, not to make anonymous automation impossible. Never use this to
// protect anything that matters. Because it costs nothing to be permissive
// here — curl sends neither header by default, and that's the entire
// population being gated — false positives (a real browser 403ing on its own
// edit page) are treated as bugs, while false negatives (a script that goes
// out of its way to look browser-shaped) are accepted by design.
func (a *API) fromOwnBrowser(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "same-origin" {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	// Compare by host, not the full URL: under SITEBIN_HTTP_ONLY=true behind
	// an external TLS terminator (a supported configuration), a.cfg.SiteURL
	// computes "http://base" for the backend's own unterminated scheme while
	// the browser sends "Origin: https://base". A host-matching Origin is
	// treated as browser-shaped even without Sec-Fetch-Site: older Safari,
	// embedded WebViews and header-stripping proxies send Origin but not
	// Sec-Fetch-Site (which only shipped in Safari 16.4).
	if a.originIsOwnHost(origin) {
		return true
	}
	// The same holds for the operator's own front door (a session origin):
	// a signed-out visitor on Safari < 16.4 drops from there with Origin and
	// no Sec-Fetch-Site. Only this heuristic moves; whether the session
	// counts stays sessionOriginRequest's question.
	if a.isSessionOrigin(origin) {
		return true
	}
	if r.Header.Get("Sec-Fetch-Site") == "" {
		return false
	}
	return a.embedOriginAllowed(strings.ToLower(origin))
}

// originIsOwnHost reports whether origin's host matches the instance's own
// host, ignoring scheme.
func (a *API) originIsOwnHost(origin string) bool {
	ou, err := url.Parse(origin)
	if err != nil {
		return false
	}
	su, err := url.Parse(a.cfg.SiteURL(a.cfg.BaseDomain))
	if err != nil {
		return false
	}
	return ou.Host != "" && strings.EqualFold(ou.Host, su.Host)
}

// sessionOriginsHonoured reports whether this edition honours
// SITEBIN_SESSION_ORIGINS at all: a provider with accounts enabled, the
// cross-origin embed capability, and a browser session lookup. Without any one
// of them a session origin is at most a plain embed origin.
func (a *API) sessionOriginsHonoured() bool {
	if _, ok := a.sessionProvider(); !ok {
		return false
	}
	p, ok := ext.Get()
	return ok && p.EmbedOriginsAllowed()
}

// isSessionOrigin reports whether origin is one of SITEBIN_SESSION_ORIGINS
// and session origins are honoured. It says nothing about the request: the
// session header is sessionOriginRequest's to check. Every session origin is
// also an embed origin; config.Load refuses one that is not.
func (a *API) isSessionOrigin(origin string) bool {
	if origin == "" || !a.sessionOriginsHonoured() {
		return false
	}
	origin = strings.ToLower(origin)
	for _, o := range a.cfg.SessionOrigins {
		if o == origin {
			return true
		}
	}
	return false
}

// sessionOriginRequest reports whether r comes from a session origin's page
// and asks for the visitor's session with the session header. The header
// forces a CORS preflight, which only a session origin passes (see
// createPreflight), so a page elsewhere cannot send it from a browser; and
// the Origin a browser sends cannot be chosen by the page.
//
// THIS IS A SECURITY BOUNDARY, like sessionOwns and unlike fromOwnBrowser: it
// decides whether a cross-origin request may create a site in the visitor's
// account. Never relax it into fromOwnBrowser's heuristics.
func (a *API) sessionOriginRequest(r *http.Request) bool {
	return r.Header.Get(sessionHeader) == "1" && a.isSessionOrigin(r.Header.Get("Origin"))
}

// crossOriginBrowser reports whether r is a browser request from a page on
// another origin: an Origin that is not the instance's own host, or fetch
// metadata that says anything but same-origin. The landing page's own fetch
// is not one, and neither is a request with neither header (curl, a CI job,
// an agent), which carries a cookie only if its sender put it there.
func (a *API) crossOriginBrowser(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" && !a.originIsOwnHost(o) {
		return true
	}
	s := r.Header.Get("Sec-Fetch-Site")
	return s != "" && s != "same-origin"
}

// withoutCookies is r as the extension sees it when the visitor's session
// must not count: a clone with no Cookie header, on the same context, so a
// bearer token and the MCP marker still reach it.
func withoutCookies(r *http.Request) *http.Request {
	rc := r.Clone(r.Context())
	rc.Header.Del("Cookie")
	return rc
}

// apiAllowedFor reports whether the caller may drive site through the JSON
// API. Sites created without an account have no API: their claim ticket
// manages them through the edit page only.
//
// The rule applies only where a provider gates creation at all, so the
// community build — which has no provider — stays fully open.
func (a *API) apiAllowedFor(site *store.Site, r *http.Request) bool {
	p, ok := ext.Get()
	if !ok || !p.AccountsEnabled() {
		return true
	}
	if site.Meta.OwnerAccountID != "" {
		return true
	}
	return a.fromOwnBrowser(r)
}

// apiAccountHint is the upgrade path shown when the gate refuses a caller.
func (a *API) apiAccountHint() string {
	return a.cfg.SiteURL(a.cfg.BaseDomain) + "/account"
}

// gatedAnonymous reports whether site is an account-less site on an instance
// where accounts are actually enabled — the condition every "no account, no
// X" rule in this package keys on: WebDAV and FTP refuse the connection
// entirely (they never carry browser fetch metadata, so apiAllowedFor's
// fromOwnBrowser fallback would be meaningless there — ownership is checked
// directly), and applySettings refuses to push a capped anonymous site's
// expiry later. Gated only where a provider is registered and reports
// AccountsEnabled(), exactly like apiAllowedFor, so the community build (no
// provider, no concept of "sign in") is never affected by it.
func (a *API) gatedAnonymous(site *store.Site) bool {
	p, ok := ext.Get()
	if !ok || !p.AccountsEnabled() {
		return false
	}
	return site.Meta.OwnerAccountID == ""
}
