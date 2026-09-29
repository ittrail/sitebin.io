//go:build ee

package account

import (
	"os"
	"path/filepath"
	"time"

	"github.com/ittrail/sitebin.io/internal/ids"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// An account's provenance log (see internal/provenance) — sign-up, sign-ins,
// API tokens minted, and the core's mirror of the account's site creations
// and deletions — lives in the account's folder, so deleting the account
// deletes it.

func (s *Store) provenancePath(id string) string {
	return filepath.Join(s.accountDir(id), provenance.FileName)
}

// RecordProvenance adds an entry to the account's log, under the account's
// lock. Delete holds that lock too, and the record refuses an account whose
// record is gone, so it can never recreate a deleted account's folder.
func (s *Store) RecordProvenance(id string, e provenance.Entry) error {
	if !ids.ValidID(id) {
		return ErrNotFound
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	if _, err := os.Stat(filepath.Join(s.accountDir(id), "account.json")); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return provenance.Record(s.provenancePath(id), e)
}

// Provenance returns the account's log, oldest first.
func (s *Store) Provenance(id string) ([]provenance.Entry, error) {
	if !ids.ValidID(id) {
		return nil, ErrNotFound
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	return provenance.Read(s.provenancePath(id))
}

// PurgeProvenance drops the account's entries older than before.
func (s *Store) PurgeProvenance(id string, before time.Time) (int, error) {
	if !ids.ValidID(id) {
		return 0, ErrNotFound
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	return provenance.Purge(s.provenancePath(id), before)
}

// ListIDs returns the id of every account. Folders that are not account ids
// are skipped.
func (s *Store) ListIDs() ([]string, error) {
	entries, err := os.ReadDir(s.accountsDir())
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && ids.ValidID(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}
