package httpapi

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

// davMutating lists the methods that change a site's content, and so are
// serialized with API writes, renew the expiry and regenerate the viewer.
// It is NOT the list of methods whose paths are validated: that is every
// method, and it happens in siteFS, where it cannot be skipped per method.
var davMutating = map[string]bool{
	"PUT": true, "DELETE": true, "MKCOL": true, "MOVE": true, "COPY": true,
	"PROPPATCH": true,
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

	// The API is an account feature, and WebDAV must not be a back door
	// around that: when accounts are enabled, an anonymous site has no
	// WebDAV either, exactly like it has no JSON API.
	if a.gatedAnonymous(site) {
		writeError(w, 403, "this site was created without an account, so it has no WebDAV access — create it while signed in at "+a.apiAccountHint()+" to use WebDAV")
		return
	}

	// A readable refusal for the common cases. siteFS below is the guarantee
	// -- it validates every name on every method -- but a 400 with a reason
	// beats the generic status the DAV library turns a refused open into.
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
		remaining := a.st.EffMaxBytes(site) - used
		if r.ContentLength > remaining || remaining <= 0 {
			http.Error(w, "site size limit exceeded", http.StatusInsufficientStorage)
			return
		}
		// Only a NEW file counts against the cap; overwriting an existing
		// one at the cap is a change, not an addition.
		if count >= a.st.EffMaxFiles(site) && !a.davExists(site, sub) {
			http.Error(w, "file count limit exceeded", http.StatusInsufficientStorage)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, remaining)
	}

	h := &webdav.Handler{
		Prefix:     "/dav/" + editID,
		FileSystem: newSiteFS(a.st, site),
		LockSystem: a.davLockSystems.get(site.ViewID),
	}
	if davMutating[r.Method] {
		// serialize with API writes / mode switches on the same site
		a.st.WithLock(site.ViewID, func() error { h.ServeHTTP(w, r); return nil })
	} else {
		h.ServeHTTP(w, r)
	}

	if davMutating[r.Method] {
		if err := a.st.RenewExpiry(site); err != nil {
			a.log.Error("renew expiry after webdav", "id", site.ViewID, "err", err)
		}
	}

	if davMutating[r.Method] && site.Meta.Mode == store.ModeViewer {
		if err := a.syncViewerLayout(site); err != nil {
			a.log.Error("viewer regen after webdav", "id", site.ViewID, "err", err)
		}
	}
}

// davExists reports whether sub names an existing regular file in the site's
// content root. A name the store would refuse is reported as absent.
func (a *API) davExists(site *store.Site, sub string) bool {
	rel, err := store.CleanRelPath(sub)
	if err != nil {
		return false
	}
	fi, err := os.Lstat(filepath.Join(site.ContentDir(), filepath.FromSlash(rel)))
	return err == nil && fi.Mode().IsRegular()
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
