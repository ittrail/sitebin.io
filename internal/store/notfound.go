package store

import (
	"errors"
	"io/fs"
	"path"
	"strings"
)

// A web server site answers an address that does not exist — a missing file,
// a folder without an index — with a 404 page: its own when it has one, else
// Sitebin's. Never a directory listing. See
// docs/superpowers/specs/2026-11-05-not-found-pages-design.md.

// DefaultNotFoundPage is the convention: a site that ships this file at its
// root uses it, as GitHub Pages, Netlify and Cloudflare Pages do, and as
// Astro, Hugo and Jekyll build it.
const DefaultNotFoundPage = "404.html"

// ErrBadNotFoundPage is a page CleanNotFoundPage refused; its message is the
// rule.
var ErrBadNotFoundPage = errors.New("not_found_page must be the path of an .html file inside the site, e.g. 404.html")

// startPageNames are the files a folder is served by: Caddy's file_server
// default index names (index.html, then index.txt). The "no start page"
// notice must agree with what Caddy actually serves, or it lies.
var startPageNames = []string{"index.html", "index.txt"}

// CleanNotFoundPage validates the owner's not-found page and returns it as
// stored: a clean path inside the site, without a leading slash, naming an
// HTML file. "" is the convention. Whether the file exists is not checked: a
// page may be named before it is uploaded, and a missing one falls back to
// Sitebin's page.
func CleanNotFoundPage(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	s = strings.TrimPrefix(s, "/")
	c, err := CleanRelPath(s)
	if err != nil {
		return "", ErrBadNotFoundPage
	}
	switch strings.ToLower(path.Ext(c)) {
	case ".html", ".htm":
		return c, nil
	}
	return "", ErrBadNotFoundPage
}

// NotFoundTarget is the path, from the served root and with a leading slash,
// Caddy tries for a 404 of this site; "" when the site has no page of its
// own to try: viewer and container sites, and the SPA fallback, which never
// answers 404 for a path.
func (m Meta) NotFoundTarget() string {
	if m.Mode != ModeWebserver || m.SPAFallback {
		return ""
	}
	if m.NotFoundPage != "" {
		return "/" + m.NotFoundPage
	}
	return "/" + DefaultNotFoundPage
}

// NotFoundServes is the site's own page a 404 shows right now, without the
// leading slash, or "" when Sitebin's page answers. For the edit page — it
// looks at the files, which the serving path never does (Caddy checks the
// file only when a 404 happens). A locked site names nothing: its file names
// stay out of reach like its files.
func NotFoundServes(site *Site) string {
	t := site.Meta.NotFoundTarget()
	if t == "" || site.Meta.IsLocked() {
		return ""
	}
	rel := strings.TrimPrefix(t, "/")
	if isRegularFile(site, rel) {
		return rel
	}
	return ""
}

// MissingStartPage reports a web server site (no SPA fallback, not locked)
// that holds files but none at its root that Caddy serves for "/": visitors
// see a 404 there. An empty site is not missing one yet — its files may still
// be on their way — and any error reads as "not missing": the notice never
// rests on a guess.
func MissingStartPage(site *Site) bool {
	m := site.Meta
	if m.Mode != ModeWebserver || m.SPAFallback || m.IsLocked() {
		return false
	}
	for _, name := range startPageNames {
		if isRegularFile(site, name) {
			return false
		}
	}
	root, err := OpenServedRoot(site)
	if err != nil {
		return false
	}
	defer root.Close()
	found := false
	errStop := errors.New("stop")
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && !strings.HasPrefix(d.Name(), ".sitebin-") {
			found = true
			return errStop
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return false
	}
	return found
}

// isRegularFile reports whether rel names a regular file in the site's served
// folder, asked through its own os.Root so no path can look outside it. The
// root is never created (OpenServedRoot).
func isRegularFile(site *Site, rel string) bool {
	root, err := OpenServedRoot(site)
	if err != nil {
		return false
	}
	defer root.Close()
	fi, err := root.Lstat(rel)
	return err == nil && fi.Mode().IsRegular()
}
