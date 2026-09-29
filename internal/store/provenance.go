package store

import (
	"os"
	"path/filepath"
	"time"

	"github.com/ittrail/sitebin.io/internal/provenance"
)

// The site's provenance log (see internal/provenance) lives beside meta.json,
// outside files/: it is the site's record, not its content, so it is never
// served, listed, zipped, counted against a quota or reachable from a
// container's bind mounts — and the site's folder going takes it along.

func provenancePath(siteDir string) string { return filepath.Join(siteDir, provenance.FileName) }

// RecordProvenance adds an entry to the site's log.
//
// It takes the site's stats lock, not the site lock: a record follows every
// write, and a streaming upload can hold the site lock for minutes. Delete
// takes the stats lock too, and the record refuses a site whose meta.json is
// gone, so a record racing a delete cannot recreate the deleted folder.
func (s *Store) RecordProvenance(site *Site, e provenance.Entry) error {
	l := s.lockStats(site.ViewID)
	l.Lock()
	defer l.Unlock()
	if _, err := os.Stat(metaPath(site.dir)); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return provenance.Record(provenancePath(site.dir), e)
}

// Provenance returns the site's log, oldest first.
func (s *Store) Provenance(site *Site) ([]provenance.Entry, error) {
	l := s.lockStats(site.ViewID)
	l.Lock()
	defer l.Unlock()
	return provenance.Read(provenancePath(site.dir))
}

// PurgeProvenance drops the site's entries older than before (the cleanup
// sweep passes now - provenance.Retention) and reports how many went.
func (s *Store) PurgeProvenance(site *Site, before time.Time) (int, error) {
	l := s.lockStats(site.ViewID)
	l.Lock()
	defer l.Unlock()
	return provenance.Purge(provenancePath(site.dir), before)
}
