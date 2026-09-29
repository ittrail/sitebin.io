package ext

import (
	"time"

	"github.com/ittrail/sitebin.io/internal/provenance"
)

// Provenance crosses the seam in two directions, both OPTIONAL and asserted
// — like ContainerProvider — so a fake of either side needs none of it and
// the community build (no provider) keeps its site logs and has no account
// logs. See docs/superpowers/specs/2026-09-29-provenance-csp-apex.md.

// SiteProvenance is implemented by the core's SiteService: the extension
// reads a site's log for the admin register and records the writes it makes
// itself (the dashboard's rename and password rotation).
type SiteProvenance interface {
	// SiteProvenance returns the site's log, oldest first. A site that no
	// longer exists is ErrSiteGone.
	SiteProvenance(viewID string) ([]provenance.Entry, error)
	// RecordSiteProvenance adds an entry to the site's log. Best effort:
	// provenance never fails the change it describes.
	RecordSiteProvenance(viewID string, e provenance.Entry)
	// SitesSeenFrom returns, per site whose log names an address in m, the
	// matching entries. The register's address search.
	SitesSeenFrom(m provenance.Match) (map[string][]provenance.Entry, error)
}

// AccountProvenance is implemented by a Provider that keeps an account log
// (sign-up, sign-ins, API tokens). The core mirrors the creation and deletion
// of an account's sites into it, so a site its owner deleted still leaves
// "created from this address" behind, and its cleanup sweep hands it the
// retention cutoff.
type AccountProvenance interface {
	// RecordAccountProvenance adds an entry to the account's log. Best
	// effort; an unknown account records nothing.
	RecordAccountProvenance(accountID string, e provenance.Entry)
	// PurgeProvenance drops account entries older than before. An account
	// the extension holds as evidence — a suspended one, or one owning a
	// locked site — keeps entries back to heldBefore instead (the lock
	// retention; zero keeps them all), and one whose locked site carries an
	// evidence hold keeps everything.
	PurgeProvenance(before, heldBefore time.Time)
}
