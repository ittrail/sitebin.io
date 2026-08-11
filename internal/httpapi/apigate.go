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

// noAccountProtocolAccess reports whether site must be refused on a
// non-browser write protocol (WebDAV, FTP) because it has no owning
// account. WebDAV and FTP clients never send browser fetch metadata, so
// apiAllowedFor's fromOwnBrowser fallback is meaningless here — ownership is
// checked directly instead. Gated only where a provider gates creation at
// all, exactly like apiAllowedFor, so the community build (no provider)
// stays fully open.
func (a *API) noAccountProtocolAccess(site *store.Site) bool {
	p, ok := ext.Get()
	if !ok || !p.AccountsEnabled() {
		return false
	}
	return site.Meta.OwnerAccountID == ""
}
