package httpapi

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/ittrail/sitebin/internal/ids"
)

// uiSecurityHeaders hardens the backend-served HTML pages (landing, edit).
func uiSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; "+
			"frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func (a *API) servePage(w http.ResponseWriter, name string) {
	b, err := fs.ReadFile(a.webFS, name)
	if err != nil {
		a.log.Error("missing embedded page", "name", name, "err", err)
		http.Error(w, "internal error", 500)
		return
	}
	uiSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func (a *API) landingPage(w http.ResponseWriter, r *http.Request) {
	// only the main domain has a landing page; anything else is a mistake
	if hostWithoutPort(r.Host) != a.cfg.BaseDomain {
		a.notFoundPage(w, r)
		return
	}
	a.servePage(w, "static/index.html")
}

func (a *API) editPage(w http.ResponseWriter, r *http.Request) {
	editID := r.PathValue("editID")
	if !ids.ValidID(editID) {
		a.notFoundPage(w, r)
		return
	}
	if _, err := a.st.ByEditID(editID); err != nil {
		a.notFoundPage(w, r)
		return
	}
	a.servePage(w, "static/edit.html")
}

func (a *API) notFoundPage(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, 404, "not found")
		return
	}
	a.msgPage(w, 404, "Page not found", "This page does not exist on this Sitebin instance.")
}

// asset serves the embedded shared assets (UI, viewer bundles, vendor libs)
// at /_sitebin/assets/* — available on the main domain and on every site
// origin, where Caddy routes /_sitebin/* to the backend.
func (a *API) asset(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	if !fs.ValidPath(p) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFileFS(w, r, a.webFS, p)
}
