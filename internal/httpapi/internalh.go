package httpapi

import (
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

// authz answers Caddy's forward_auth subrequest for every content request on
// view subdomains, custom domains, and /v/<id> path views: 200 serve, 401 gate
// (body relayed to the client), 410 expired or locked, 404 unknown.
func (a *API) authz(w http.ResponseWriter, r *http.Request) {
	var site *store.Site
	var err error
	pathMode := false
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	// addr is the address the request came in on — the host, or on a custom
	// domain the host with the path it matched — and mount that path.
	addr, mount, reqPath := host, "", "/"
	if viewID := r.Header.Get("X-Sitebin-View"); viewID != "" {
		// /v/<id> path view — Caddy passes the id explicitly.
		pathMode = true
		site, err = a.st.ByViewID(viewID)
	} else {
		reqPath = requestPath(r.Header.Get("X-Forwarded-Uri"))
		site, addr, mount, err = a.siteByRequest(host, reqPath)
	}
	if err != nil {
		a.msgPage(w, 404, "Site not found", "There is no site at this address. It may have been deleted.")
		return
	}
	// <mount>/_sitebin/* is Sitebin's own, as /_sitebin/* is at the root,
	// where Caddy hands it to the backend before this gate: the backend's
	// routes apply each site's rules (lock, serving address, password).
	if mount != "" && strings.HasPrefix(underMount(reqPath, mount), "/_sitebin/") {
		w.Header().Set(upstreamHeader, publicBackend)
		w.Header().Set(stripHeader, mount)
		w.Header().Set(mountHeader, mount)
		w.WriteHeader(200)
		return
	}
	// A locked site is served to nobody, before any other rule: not after a
	// view password, not on a custom domain, not as a container's upstream.
	// This is the one place every content request passes through, so it is
	// the whole of "not served". The reason stays between the operator and
	// the owner; a visitor gets the generic sentence.
	if site.Meta.IsLocked() {
		// With where to report it: a visitor who came here from a phishing
		// mail may have more to tell.
		a.renderPage(w, 410, pageData{Title: msgSuspendedTitle, Message: msgSuspendedBody, Code: "410", Abuse: a.abuse()})
		return
	}
	if site.Meta.Expired(time.Now()) {
		a.msgPage(w, 410, "Site expired", "This site has reached its expiry date and is no longer available.")
		return
	}
	// The address may not serve the site at all: a custom domain that
	// redirects, or a generated address its owner switched off or redirected.
	// Caddy relays this answer to the client as it is, so the redirect leaves
	// without the file server, a container or the gate page being asked.
	switch rule := a.addressRule(site, addr, pathMode); rule.Mode {
	case store.ViewOff:
		a.msgPage(w, 404, "Site not found", "There is no site at this address. It may have been deleted.")
		return
	case store.ViewRedirect:
		a.redirectAddress(w, r, site, rule, pathMode, mount)
		return
	}
	// A file site under a path is a directory: its relative links need the
	// slash. A container gets its path as it came.
	if mount != "" && reqPath == mount && site.Meta.Mode != store.ModeContainer {
		loc := mount + "/"
		if q := forwardedQuery(r.Header.Get("X-Forwarded-Uri")); q != "" {
			loc += "?" + q
		}
		w.Header().Set("Location", loc)
		w.WriteHeader(http.StatusPermanentRedirect)
		return
	}
	if !site.Meta.ViewPasswordProtected || a.hasViewAccess(r, site) {
		a.admit(w, r, site, pathMode, addr, mount)
		return
	}
	// The gate form needs the site id in path mode (Host is the main domain).
	gateSite := ""
	if pathMode {
		gateSite = site.ViewID
	}
	a.gatePage(w, 401, sanitizeRedirect(r.Header.Get("X-Forwarded-Uri")), "", gateSite, mount)
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
func (a *API) admit(w http.ResponseWriter, r *http.Request, site *store.Site, pathMode bool, addr, mount string) {
	if site.Meta.Mode == store.ModeContainer {
		if pathMode {
			a.msgPage(w, 404, "Not available here", "This app is served on its own address, not under a path.")
			return
		}
		// The address names a port — a generated address its own, a custom
		// domain its target — and the port a running service.
		endpoint := ""
		if a.cfg.SubdomainViews() || !a.isViewHost(addr) {
			endpoint = a.endpointOf(site, addr)
		}
		upstream, ok := site.Meta.Container.Upstream(endpoint)
		if !ok {
			if c := site.Meta.Container; c == nil || !c.Enabled || c.Status != store.ContainerRunning {
				a.msgPage(w, 503, "This app is not running", "Its owner has stopped it, or it is starting. Try again in a moment.")
			} else {
				a.msgPage(w, 404, "Nothing here", "No service of this app is mapped to this address.")
			}
			return
		}
		a.countView(r, site)
		a.recordPageView(r, site, addr, mount, pathMode)
		w.Header().Set(upstreamHeader, upstream)
		// Its folder too: the file server never sees it, but the trust
		// marker in it decides the security headers on a custom domain,
		// whose root is this answer.
		w.Header().Set(siteHeader, site.ViewID)
		if mount != "" {
			w.Header().Set(mountHeader, mount)
		}
		w.WriteHeader(200)
		return
	}
	prefix := mount
	if pathMode {
		prefix = "/v/" + site.ViewID
	}
	// The compose file holds a container project's secrets and stays in the
	// site's root when the site leaves Container mode: no file mode ever
	// serves it, as if it did not exist.
	if composeRequested(r.Header.Get("X-Forwarded-Uri"), prefix) {
		a.msgPage(w, 404, "Page not found", "There is no page at this address. Check the link for typos.")
		return
	}
	a.countView(r, site)
	a.recordPageView(r, site, addr, mount, pathMode)
	// A file site's Cache-Control, which Caddy sets on a 2xx or 304 only, and
	// the Content-Type of a file the system's table cannot name, set on a 2xx
	// only — both computed on the path the file server will see.
	file := a.servedFile(site, r.Header.Get("X-Forwarded-Uri"), prefix)
	if v := siteCacheControl(site.Meta, file); v != "" {
		w.Header().Set(cacheHeader, v)
	}
	if t := a.typeValue(site, file.rel, file.isFile); t != "" {
		w.Header().Set(typeHeader, t)
	}
	// The page Caddy shows for a 404 when the site has it.
	if t := site.Meta.NotFoundTarget(); t != "" {
		w.Header().Set(notFoundHeader, t)
	}
	// The folder Caddy serves on a custom domain, and the path it strips.
	if !pathMode {
		w.Header().Set(siteHeader, site.ViewID)
	}
	if mount != "" {
		w.Header().Set(stripHeader, mount)
	}
	w.WriteHeader(200)
}

// composeRequested reports whether a file site's request names its compose
// file, on the path the file server will see (as cacheValue derives it: the
// URI decoded and cleaned, the mount or path-view prefix removed), directly or
// as the File viewer's /_raw/ copy.
func composeRequested(rawURI, prefix string) bool {
	p := "/"
	if u, err := url.ParseRequestURI(rawURI); err == nil && u.Path != "" {
		p = u.Path
	}
	if prefix != "" {
		p = strings.TrimPrefix(p, prefix)
	}
	rel := strings.TrimPrefix(path.Clean("/"+p), "/")
	return rel == store.ComposeFile || rel == "_raw/"+store.ComposeFile
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
	if !a.st.HostKnown(domain) {
		http.Error(w, "unknown domain", 404)
		return
	}
	w.WriteHeader(200)
}

// health answers the container healthcheck, and reports the capabilities an
// operator would otherwise have to infer from the environment.
//
// MCP and its OAuth mode both change how /mcp answers, and neither was
// visible from outside the process: an instance where OAuth is on answers
// every call that needs an account with a 401, which from outside looks much
// like a broken endpoint. This is the cheapest place to make that answerable.
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
