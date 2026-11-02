package httpapi

import (
	"net/url"
	"path"
	"strings"

	"github.com/ittrail/sitebin.io/internal/store"
)

// Custom domains with a path. authz resolves a custom-domain request's host
// and path to the site mounted at the longest matching path, and answers
// Caddy in headers it copies onto the request (client copies are removed
// before the gate, so only authz can set them):
//
//   - siteHeader: the site whose folder the file server serves. Caddy's root
//     for a custom domain comes from it, never from the host: whatever spelling
//     of a path a client sends, the files served are those of the site whose
//     rules authz applied.
//   - stripHeader: the prefix Caddy strips before the file server (a file
//     site under a path) or before proxying to the backend (<mount>/_sitebin/).
//   - mountHeader: the mount, for a container (Caddy sends it on as
//     X-Forwarded-Prefix) and for the backend's own routes under a path (the
//     backend resolves the site from host + mount).
//
// See docs/superpowers/specs/2026-11-02-custom-domains-with-a-path-design.md.
const (
	siteHeader  = "X-Sitebin-Site"
	stripHeader = "X-Sitebin-Strip"
	mountHeader = "X-Sitebin-Mount"
	// publicBackend is where Caddy proxies <mount>/_sitebin/*: the address
	// the Caddyfile names for the backend's public handler.
	publicBackend = "127.0.0.1:8080"
)

// requestPath is the path of a forwarded request URI as the file server
// sees it: decoded and cleaned of dot segments and doubled slashes, its
// trailing slash kept. A URI that is not a path — Caddy never forwards one,
// but the gate must not trust that — is the host's root, whose own rules
// then answer it.
func requestPath(forwardedURI string) string {
	u, err := url.ParseRequestURI(forwardedURI)
	if err != nil || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}
	clean := path.Clean(u.Path)
	if strings.HasSuffix(u.Path, "/") && clean != "/" {
		clean += "/"
	}
	return clean
}

// siteByRequest resolves a content request: a generated address as
// siteByHost does (addr is the host, mount ""), a custom domain by
// ResolveAddress — the address it matched and that address's path.
func (a *API) siteByRequest(host, reqPath string) (site *store.Site, addr, mount string, err error) {
	h := strings.ToLower(hostWithoutPort(host))
	if a.cfg.ViewDomain != "" && strings.HasSuffix(h, "."+a.cfg.ViewDomain) {
		site, err = a.siteByHost(h)
		return site, h, "", err
	}
	site, addr, err = a.st.ResolveAddress(h, reqPath)
	if err != nil {
		return nil, "", "", err
	}
	return site, addr, store.PathOf(addr), nil
}

// underMount is what a request path is below its mount, "/" for the mount
// itself.
func underMount(reqPath, mount string) string {
	rest := strings.TrimPrefix(reqPath, mount)
	if rest == "" {
		return "/"
	}
	return rest
}

// forwardedQuery is the raw query of a forwarded request URI.
func forwardedQuery(forwardedURI string) string {
	if u, err := url.ParseRequestURI(forwardedURI); err == nil {
		return u.RawQuery
	}
	return ""
}
