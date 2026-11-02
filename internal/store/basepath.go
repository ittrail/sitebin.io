package store

import (
	"io"
	"regexp"
	"strings"
)

// A site served under a path works only if it was built for that path: a
// build for the root links /_astro/x.css, which under morgenfertig.at/demos/k1
// is a request for the root project's file. Sitebin does not rewrite links
// (see the design doc's non-goals); it says so where the address is managed.

// basePathReadLimit bounds how much of index.html the check reads.
const basePathReadLimit = 512 << 10

// linkAttrRe finds the values of the attributes that load or navigate to a
// URL: quoted with " or ', or unquoted.
var linkAttrRe = regexp.MustCompile(`(?i)\b(src|href|action|poster|srcset)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)

// globalSitebinPaths are Sitebin's own routes that answer the same on every
// site, so a site under a path may link them from the root.
var globalSitebinPaths = []string{"/_sitebin/assets/", "/_sitebin/embed.js", "/_sitebin/altcha.js"}

// BasePathWarning is what to tell the owner when the static site at addr —
// a custom domain with a path — links root-absolute URLs outside that path
// from its top-level index.html. "" when it does not, when addr has no path,
// and for a container (Sitebin cannot read what an app renders) or a file
// viewer (its page is Sitebin's own and links relatively). Off the serving
// path; it never blocks anything.
func (s *Store) BasePathWarning(site *Site, addr string) string {
	path := pathOf(addr)
	if path == "" || site.Meta.Mode == ModeContainer || site.Meta.Mode == ModeViewer {
		return ""
	}
	root, err := OpenContentRoot(site)
	if err != nil {
		return ""
	}
	defer root.Close()
	f, err := root.Open("index.html")
	if err != nil {
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, basePathReadLimit))
	if err != nil {
		return ""
	}
	for _, m := range linkAttrRe.FindAllStringSubmatch(string(b), -1) {
		val := m[2] + m[3] + m[4]
		urls := []string{val}
		if strings.EqualFold(m[1], "srcset") {
			urls = srcsetURLs(val)
		}
		for _, u := range urls {
			if outsidePath(strings.TrimSpace(u), path) {
				return "This site seems not to be built for " + path + ": it links " + u +
					". Set your build's base path to " + path + ", or use a subdomain instead."
			}
		}
	}
	return ""
}

// srcsetURLs are the URLs of a srcset value ("a.png 1x, b.png 2x").
func srcsetURLs(v string) []string {
	var out []string
	for _, cand := range strings.Split(v, ",") {
		if f := strings.Fields(cand); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}

// outsidePath reports whether u is a root-absolute URL (not //host) that is
// neither path nor under it, nor one of Sitebin's global routes.
func outsidePath(u, path string) bool {
	if !strings.HasPrefix(u, "/") || strings.HasPrefix(u, "//") {
		return false
	}
	p, _, _ := strings.Cut(u, "?")
	p, _, _ = strings.Cut(p, "#")
	if p == path || strings.HasPrefix(p, path+"/") {
		return false
	}
	for _, g := range globalSitebinPaths {
		if strings.HasPrefix(p, g) || p == strings.TrimSuffix(g, "/") {
			return false
		}
	}
	return true
}
