package httpapi

import "github.com/ittrail/sitebin.io/internal/store"

// A web server site answers an address that does not exist with a 404 page,
// never a directory listing. See
// docs/superpowers/specs/2026-11-05-not-found-pages-design.md.

// startPageWarning tells whoever just wrote to a web server site that its root
// will answer 404. Both ways out are named, the website first: an agent that
// is still uploading must not be pushed into the viewer.
const startPageWarning = "This site has files but no index.html at its root, so visitors see a 404 there. " +
	"Upload an index.html, or set mode to viewer if the files are documents to show or download."

// startPageWarnings is the warning as a list to append, or nil when the site
// has a start page, no files yet, or is no web server site.
func (a *API) startPageWarnings(site *store.Site) []string {
	if store.MissingStartPage(site) {
		return []string{startPageWarning}
	}
	return nil
}
