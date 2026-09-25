package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Asset caching.
//
// The backend's pages link their scripts and styles under /_sitebin/assets/.
// Those URLs used to be the same for every build and cached for an hour, so
// after a deploy a browser could run the new page with last build's script
// for up to an hour. Now a page's asset URLs carry the build's version
// (?v=…): a new build means new URLs, which reach every browser at once, and
// a versioned asset can be cached for good. An asset asked for without a
// version — the enterprise dashboard's templates, a site's viewer wrapper, an
// old link — is revalidated on every use (no-cache + ETag), which costs a 304,
// not an hour of a stale file.

// startedAt distinguishes one run of a development build from the next,
// which all report the version "dev".
var startedAt = time.Now()

// assetRef matches an asset URL in a page, quoted, without a query yet.
var assetRef = regexp.MustCompile(`(["'])(/_sitebin/assets/[^"'?#]+)(["'])`)

// assetCache holds what is derived from the embedded files, which do not
// change while the process runs: pages with versioned asset URLs, and each
// asset's ETag.
type assetCache struct {
	versionOnce sync.Once
	version     string
	pages       sync.Map // page name -> []byte
	etags       sync.Map // asset path -> string
}

// assetVersion is the ?v= a page appends to its asset URLs: the build's
// version, or for a development build one that changes with every start.
func (a *API) assetVersion() string {
	a.assets.versionOnce.Do(func() {
		v := Version
		if v == "" || v == "dev" {
			v = "dev-" + strconv.FormatInt(startedAt.Unix(), 36)
		}
		a.assets.version = v
	})
	return a.assets.version
}

// versionedPage returns the embedded page with its asset URLs versioned.
func (a *API) versionedPage(name string) ([]byte, error) {
	if b, ok := a.assets.pages.Load(name); ok {
		return b.([]byte), nil
	}
	b, err := fs.ReadFile(a.webFS, name)
	if err != nil {
		return nil, err
	}
	b = assetRef.ReplaceAll(b, []byte("${1}${2}?v="+a.assetVersion()+"${3}"))
	a.assets.pages.Store(name, b)
	return b, nil
}

// assetETag is a strong ETag of the embedded asset p, or "" if it cannot be
// read (the file server then answers 404 on its own).
func (a *API) assetETag(p string) string {
	if t, ok := a.assets.etags.Load(p); ok {
		return t.(string)
	}
	b, err := fs.ReadFile(a.webFS, p)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	t := `"` + hex.EncodeToString(sum[:12]) + `"`
	a.assets.etags.Store(p, t)
	return t
}
