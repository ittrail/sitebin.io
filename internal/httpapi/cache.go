package httpapi

import (
	"errors"
	"io/fs"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/ittrail/sitebin.io/internal/store"
)

// Browser caching. authz answers a file site's content request with
// cacheHeader — the whole Cache-Control value for that response — and Caddy
// copies it onto the request and sets Cache-Control from it on a 2xx or 304
// only. Every rule lives here; the Caddyfile only copies a value. See
// docs/superpowers/specs/2026-10-16-browser-caching-design.md.

// cacheHeader carries the value from authz to Caddy. Caddy strips any copy a
// client sent before the gate runs, as it does for the upstream header.
const cacheHeader = "X-Sitebin-Cache"

// Lifetimes, in seconds.
const (
	cacheYear = 31536000
	cacheWeek = 604800
	cacheDay  = 86400
	cacheHour = 3600
)

// documentExts are the files that point at everything else — pages and data.
// They always revalidate: a stale page keeps visitors on the old site, and a
// stale feed (electron-updater's latest*.yml) keeps an app on the old release.
var documentExts = map[string]bool{
	"": true, ".html": true, ".htm": true, ".xhtml": true,
	".json": true, ".xml": true, ".txt": true, ".webmanifest": true,
	".yml": true, ".yaml": true,
}

// immutableTrees are directories frameworks keep content-addressed as a
// whole: a path under one never holds other bytes. They matter beyond the
// name rule because a quarter of Vite's 8-character hashes carry no digit,
// which the name rule needs; Astro and Nuxt build with Vite. The one file in
// one of them that does change — Nuxt's builds/latest.json — is data, and
// data always revalidates before this is asked.
var immutableTrees = []string{"/_next/static/", "/_app/immutable/", "/_astro/", "/_nuxt/"}

// cacheControl is the Cache-Control value for one response: policy is the
// site's (store.Meta.Cache, "" = auto), p the request path within the site,
// isFile whether p names an existing regular file, private whether the site
// is behind a view password. "" means no header.
//
// Anything that is not an existing file — a directory, the SPA fallback's
// index.html, a 404 — is treated as a page: never cached long, so a missing
// hashed bundle never leaves a year of index.html in a visitor's cache.
func cacheControl(policy, p string, isFile, private bool) string {
	if policy == store.CacheOff {
		return ""
	}
	scope := "public"
	if private {
		scope = "private"
	}
	maxAge := func(s int, extra string) string {
		v := scope + ", max-age=" + strconv.Itoa(s)
		if extra != "" {
			v += ", " + extra
		}
		return v
	}
	revalidate := maxAge(0, "must-revalidate")
	if !isFile || strings.HasSuffix(p, "/") || documentExts[strings.ToLower(path.Ext(p))] {
		return revalidate
	}
	switch policy {
	case store.CacheRevalidate:
		return revalidate
	case store.CacheHour:
		return maxAge(cacheHour, "")
	case store.CacheDay:
		return maxAge(cacheDay, "")
	case store.CacheWeek:
		return maxAge(cacheWeek, "")
	case store.CacheYear:
		return maxAge(cacheYear, "")
	case store.CacheImmutable:
		return maxAge(cacheYear, "immutable")
	}
	if fingerprinted(p) {
		return maxAge(cacheYear, "immutable")
	}
	return revalidate
}

// fingerprinted reports whether a path names a build file whose name carries
// a hash of its content, so the name never holds other bytes: a segment of
// the file name, separated by "." or "-", that is hash-shaped (hashSegment)
// and at least 8 characters beside a name (index-BqCz7v8e.js,
// main.8f9e7d6a.css) or at least 16 as the whole name (webpack's
// [hash][ext]); or a file under an immutable framework tree. A hand-named
// file — style.css, report2024.pdf, jquery-3.7.1.min.js — never is.
func fingerprinted(p string) bool {
	for _, tree := range immutableTrees {
		if strings.Contains(p, tree) {
			return true
		}
	}
	base := path.Base(p)
	name := strings.TrimSuffix(base, path.Ext(base))
	segs := strings.FieldsFunc(name, func(r rune) bool { return r == '.' || r == '-' })
	if len(segs) == 0 {
		// A dotfile (.env, .htaccess): its whole name is the extension.
		return false
	}
	if len(segs) == 1 {
		return len(segs[0]) >= 16 && hashSegment(segs[0])
	}
	for _, s := range segs[1:] {
		if len(s) >= 8 && hashSegment(s) {
			return true
		}
	}
	return false
}

// hashSegment reports whether s looks like a content hash: at most 64 letters,
// digits or "_", with a digit and a lowercase letter, and either lowercase hex
// (webpack, Angular, Expo) or a mix of upper- and lowercase (Vite, esbuild's
// base32/64). A word with a year in it — report2024 — is neither, and nor is
// a date.
func hashSegment(s string) bool {
	if len(s) > 64 {
		return false
	}
	var digit, upper, lower, nonHex bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z':
			lower = true
			if r > 'f' {
				nonHex = true
			}
		case r >= 'A' && r <= 'Z':
			upper = true
		case r == '_':
			nonHex = true
		default:
			return false
		}
	}
	if !digit || !lower {
		return false // a date or a number is not a hash, nor is SHOUTING
	}
	return (!upper && !nonHex) || upper
}

// cacheValue is what authz answers for a file site's request: the policy
// applied to the path the visitor asked for, after the /v/<id> prefix of a
// path view. Whether that path is an existing regular file is asked of the
// folder the file server serves (files/, which in viewer mode holds _raw/),
// through its own os.Root, so no path can look outside it. That root is
// never created: a site deleted after authz loaded it must not come back
// as a folder without meta.json.
func (a *API) cacheValue(site *store.Site, rawURI, prefix string) string {
	if site.Meta.Cache == store.CacheOff && len(site.Meta.CacheRules) == 0 {
		return ""
	}
	return siteCacheControl(site.Meta, a.servedFile(site, rawURI, prefix))
}

// siteCacheControl is the Cache-Control value for a file site's request: the
// first of the owner's cache rules that matches decides for an existing file
// that is not a page or data, and the site's policy for everything else. A
// rule never makes a page, a document or a missing file long-lived —
// cacheControl revalidates those whatever the policy — so "stable/*"
// immutable leaves stable/latest.json fresh. In viewer mode the owner's
// files are served under _raw/, and a rule names them as the owner does.
func siteCacheControl(m store.Meta, f served) string {
	policy := m.Cache
	if f.isFile && len(m.CacheRules) > 0 {
		subject := f.rel
		if m.Mode == store.ModeViewer {
			if rest, ok := strings.CutPrefix(subject, "_raw/"); ok {
				subject = rest
			}
		}
		if c, ok := m.CacheRuleFor(subject); ok {
			policy = c
		}
	}
	return cacheControl(policy, f.path, f.isFile, m.ViewPasswordProtected)
}

// served is what a file site's request names: path is the request path within
// the site (after the /v/<id> or mount prefix), rel the same path cleaned and
// relative to the served folder, isFile whether rel is an existing regular
// file there.
type served struct {
	path, rel string
	isFile    bool
}

// servedFile resolves a file site's request against the folder the file
// server serves (files/, which in viewer mode holds _raw/), through its own
// os.Root, so no path can look outside it. That root is never created: a
// site deleted after authz loaded it must not come back as a folder without
// meta.json.
func (a *API) servedFile(site *store.Site, rawURI, prefix string) served {
	p := "/"
	if u, err := url.ParseRequestURI(rawURI); err == nil && u.Path != "" {
		p = u.Path
	}
	if prefix != "" {
		p = strings.TrimPrefix(p, prefix)
	}
	f := served{path: p}
	if rel := strings.TrimPrefix(path.Clean("/"+p), "/"); rel != "" && !strings.HasSuffix(p, "/") {
		f.rel = rel
		if root, err := store.OpenServedRoot(site); err == nil {
			if fi, err := root.Stat(rel); err == nil && fi.Mode().Type() == 0 {
				f.isFile = true
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				a.log.Debug("served stat", "id", site.ViewID, "err", err)
			}
			root.Close()
		}
	}
	return f
}
