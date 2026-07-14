package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/ittrail/sitebin/internal/store"
)

// authz answers Caddy's forward_auth subrequest for every content request on
// view subdomains, custom domains, and /v/<id> path views: 200 serve, 401 gate
// (body relayed to the client), 410 expired, 404 unknown.
func (a *API) authz(w http.ResponseWriter, r *http.Request) {
	var site *store.Site
	var err error
	pathMode := false
	if viewID := r.Header.Get("X-Sitebin-View"); viewID != "" {
		// /v/<id> path view — Caddy passes the id explicitly.
		pathMode = true
		site, err = a.st.ByViewID(viewID)
	} else {
		host := r.Header.Get("X-Forwarded-Host")
		if host == "" {
			host = r.Host
		}
		site, err = a.siteByHost(host)
	}
	if err != nil {
		a.msgPage(w, 404, "Site not found", "There is no site at this address. It may have been deleted.")
		return
	}
	if site.Meta.Expired(time.Now()) {
		a.msgPage(w, 410, "Site expired", "This site has reached its expiry date and is no longer available.")
		return
	}
	if !site.Meta.ViewPasswordProtected {
		a.countView(r, site)
		w.WriteHeader(200)
		return
	}
	if a.hasViewAccess(r, site) {
		a.countView(r, site)
		w.WriteHeader(200)
		return
	}
	// The gate form needs the site id in path mode (Host is the main domain).
	gateSite := ""
	if pathMode {
		gateSite = site.ViewID
	}
	a.gatePage(w, 401, sanitizeRedirect(r.Header.Get("X-Forwarded-Uri")), "", gateSite)
}

// tlsCheck is Caddy's on-demand TLS ask endpoint: 200 only for domains that
// exist in the domain index. Mandatory — it prevents certificate-issuance
// DoS via arbitrary domains pointed at this host.
func (a *API) tlsCheck(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		http.Error(w, "missing domain", 400)
		return
	}
	if _, err := a.st.ByDomain(domain); err != nil {
		http.Error(w, "unknown domain", 404)
		return
	}
	w.WriteHeader(200)
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// countView records a page view when tracking is on and the request looks like
// a browser navigation (Accept: text/html), so asset fetches aren't counted.
func (a *API) countView(r *http.Request, site *store.Site) {
	if a.cfg.TrackViews && strings.Contains(r.Header.Get("Accept"), "text/html") {
		a.st.RecordView(site)
	}
}
