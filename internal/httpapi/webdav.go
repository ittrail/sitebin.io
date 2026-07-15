package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/net/webdav"

	"github.com/ittrail/sitebin.io/internal/store"
)

// davLocks keeps one in-memory WebDAV lock system per site.
type davLocks struct {
	mu sync.Mutex
	m  map[string]webdav.LockSystem
}

func newDavLocks() *davLocks { return &davLocks{m: make(map[string]webdav.LockSystem)} }

func (d *davLocks) get(viewID string) webdav.LockSystem {
	d.mu.Lock()
	defer d.mu.Unlock()
	ls, ok := d.m[viewID]
	if !ok {
		ls = webdav.NewMemLS()
		d.m[viewID] = ls
	}
	return ls
}

var davMutating = map[string]bool{
	"PUT": true, "DELETE": true, "MKCOL": true, "MOVE": true, "COPY": true,
	"PROPPATCH": true, "LOCK": false, "UNLOCK": false,
}

// webdav serves /dav/{editID}/... — a network-drive view of the site's own
// files, gated by the edit password over HTTP Basic auth. Write access equals
// full edit rights, exactly like the API.
func (a *API) webdav(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.WebDAVAllowed {
		writeError(w, 404, "WebDAV is disabled on this instance")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/dav/")
	editID, sub, _ := strings.Cut(rest, "/")
	site, err := a.st.ByEditID(editID)
	if err != nil || !site.Meta.WebDAVEnabled {
		writeError(w, 404, "not found")
		return
	}

	_, pw, ok := r.BasicAuth()
	if !ok || pw == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="Sitebin WebDAV (password = edit password)"`)
		writeError(w, 401, "authentication required")
		return
	}
	switch a.verifyEdit(r, site, pw) {
	case verifyThrottled:
		writeError(w, 429, "too many password attempts")
		return
	case verifyFailed:
		w.Header().Set("WWW-Authenticate", `Basic realm="Sitebin WebDAV (password = edit password)"`)
		writeError(w, 401, "wrong edit password")
		return
	}

	if davMutating[r.Method] {
		if sub != "" {
			if _, err := store.CleanRelPath(sub); err != nil {
				writeError(w, 400, "invalid path")
				return
			}
		}
		if dest := r.Header.Get("Destination"); dest != "" {
			if !a.davDestinationOK(dest, editID) {
				writeError(w, 400, "invalid destination")
				return
			}
		}
		if r.Method == "PUT" {
			used, count, err := a.st.Usage(site)
			if err != nil {
				writeError(w, 500, "internal error")
				return
			}
			remaining := a.cfg.MaxSiteBytes - used
			if r.ContentLength > remaining || remaining <= 0 {
				http.Error(w, "site size limit exceeded", http.StatusInsufficientStorage)
				return
			}
			if count >= a.cfg.MaxFiles {
				http.Error(w, "file count limit exceeded", http.StatusInsufficientStorage)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, remaining)
		}
	}

	h := &webdav.Handler{
		Prefix:     "/dav/" + editID,
		FileSystem: webdav.Dir(site.ContentDir()),
		LockSystem: a.davLockSystems.get(site.ViewID),
	}
	if davMutating[r.Method] {
		// serialize with API writes / mode switches on the same site
		a.st.WithLock(site.ViewID, func() error { h.ServeHTTP(w, r); return nil })
	} else {
		h.ServeHTTP(w, r)
	}

	if davMutating[r.Method] && site.Meta.Mode == store.ModeViewer {
		if err := a.syncViewerLayout(site); err != nil {
			a.log.Error("viewer regen after webdav", "id", site.ViewID, "err", err)
		}
	}
}

// davDestinationOK validates MOVE/COPY targets: same site, sane path.
func (a *API) davDestinationOK(dest, editID string) bool {
	u, err := url.Parse(dest)
	if err != nil {
		return false
	}
	rel, ok := strings.CutPrefix(u.Path, "/dav/"+editID+"/")
	if !ok {
		return false
	}
	if rel == "" {
		return false
	}
	_, err = store.CleanRelPath(strings.TrimSuffix(rel, "/"))
	return err == nil
}
