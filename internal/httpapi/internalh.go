package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
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
	if !site.Meta.ViewPasswordProtected || a.hasViewAccess(r, site) {
		a.admit(w, r, site, pathMode)
		return
	}
	// The gate form needs the site id in path mode (Host is the main domain).
	gateSite := ""
	if pathMode {
		gateSite = site.ViewID
	}
	a.gatePage(w, 401, sanitizeRedirect(r.Header.Get("X-Forwarded-Uri")), "", gateSite)
}

// upstreamHeader carries a container site's upstream from authz to Caddy,
// which copies it onto the request and proxies to it. Caddy strips any copy a
// client sent before forward_auth runs, so only authz can set it.
const upstreamHeader = "X-Sitebin-Upstream"

// admit answers a request that passed the gate. A file site is served by
// Caddy's file server on a bare 200. A container site is never served from
// its files — its tree holds the database's files and the compose file holds
// its passwords — so it is admitted only WITH an upstream, and otherwise
// answered here with a page saying why.
func (a *API) admit(w http.ResponseWriter, r *http.Request, site *store.Site, pathMode bool) {
	if site.Meta.Mode == store.ModeContainer {
		if pathMode {
			a.msgPage(w, 404, "Not available here", "This app is served on its own address, not under a path.")
			return
		}
		host := r.Header.Get("X-Forwarded-Host")
		if host == "" {
			host = r.Host
		}
		viewHost := ""
		if a.cfg.SubdomainViews() {
			viewHost = site.ViewID + "." + a.cfg.ViewDomain
		}
		upstream, ok := site.Meta.Container.Upstream(hostWithoutPort(host), viewHost)
		if !ok {
			if c := site.Meta.Container; c == nil || !c.Enabled || c.Status != store.ContainerRunning {
				a.msgPage(w, 503, "This app is not running", "Its owner has stopped it, or it is starting. Try again in a moment.")
			} else {
				a.msgPage(w, 404, "Nothing here", "No service of this app is mapped to this address.")
			}
			return
		}
		a.countView(r, site)
		w.Header().Set(upstreamHeader, upstream)
		w.WriteHeader(200)
		return
	}
	a.countView(r, site)
	w.WriteHeader(200)
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

// health answers the container healthcheck, and reports the capabilities an
// operator would otherwise have to infer from the environment.
//
// MCP and its OAuth mode both change how /mcp answers, and neither was
// visible from outside the process: an instance where OAuth is on refuses
// every credential-less call, which looks identical to a broken endpoint.
// This is the cheapest place to make that answerable.
func (a *API) health(w http.ResponseWriter, r *http.Request) {
	payload := map[string]any{
		"status":  "ok",
		"version": Version,
		"mcp":     a.cfg.MCPEnabled,
	}
	if a.cfg.MCPEnabled {
		payload["mcp_oauth"] = a.mcpOAuthEnabled()
		if a.mcpOAuthEnabled() {
			payload["mcp_issuer"] = a.cfg.MCPOAuthIssuer
			payload["mcp_resource"] = a.mcpResource()
		}
	}
	writeJSON(w, 200, payload)
}

// countView records a page view when tracking is on and the request looks like
// a browser navigation (Accept: text/html), so asset fetches aren't counted.
func (a *API) countView(r *http.Request, site *store.Site) {
	if a.cfg.TrackViews && strings.Contains(r.Header.Get("Accept"), "text/html") {
		a.st.RecordView(site)
	}
}
