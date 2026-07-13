package httpapi

import (
	"net/http"
	"time"
)

// authz answers Caddy's forward_auth subrequest for every content request on
// view subdomains and custom domains: 200 serve, 401 gate (body relayed to
// the client), 410 expired, 404 unknown.
func (a *API) authz(w http.ResponseWriter, r *http.Request) {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	site, err := a.siteByHost(host)
	if err != nil {
		a.msgPage(w, 404, "Site not found", "There is no site at this address. It may have been deleted.")
		return
	}
	if site.Meta.Expired(time.Now()) {
		a.msgPage(w, 410, "Site expired", "This site has reached its expiry date and is no longer available.")
		return
	}
	if !site.Meta.ViewPasswordProtected {
		w.WriteHeader(200)
		return
	}
	if a.hasViewAccess(r, site) {
		w.WriteHeader(200)
		return
	}
	a.gatePage(w, 401, sanitizeRedirect(r.Header.Get("X-Forwarded-Uri")), "")
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
