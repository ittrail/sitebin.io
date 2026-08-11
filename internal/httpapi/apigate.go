package httpapi

import (
	"net/http"
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
// API. Scripts, CI jobs and agents send neither header.
//
// This is a PLAN boundary, not a security boundary. Sec-Fetch-Site and Origin
// are trivially forgeable outside a browser; the point is to keep automation
// on accounts, not to make anonymous automation impossible. Never use this to
// protect anything that matters.
func (a *API) fromOwnBrowser(r *http.Request) bool {
	fetchSite := r.Header.Get("Sec-Fetch-Site")
	if fetchSite == "" {
		return false
	}
	origin := strings.ToLower(r.Header.Get("Origin"))
	if origin == "" {
		// Same-origin GETs and some same-origin fetches omit Origin entirely.
		return fetchSite == "same-origin"
	}
	if origin == strings.ToLower(a.cfg.SiteURL(a.cfg.BaseDomain)) {
		return true
	}
	return a.embedOriginAllowed(origin)
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
