package store

import "time"

// renewGrace is how close a recomputed expiry must be to the stored one for a
// renewal to be skipped. It keeps a multi-file upload from rewriting meta.json
// once per file: the first file moves the expiry, the rest land inside the
// grace window and write nothing.
const renewGrace = time.Minute

// renewExpiryLocked slides an owned, capped site's expiry to now + cap. The
// caller must already hold the site lock.
//
// Anonymous sites are excluded deliberately: a drop must not be able to keep
// itself alive by rewriting one file before it expires. Their lifetime is
// fixed at creation.
func (s *Store) renewExpiryLocked(site *Site) error {
	m := site.Meta
	if m.OwnerAccountID == "" || m.QuotaExpiryDays <= 0 || m.ExpiresAt == nil {
		return nil
	}
	want := time.Now().Add(time.Duration(m.QuotaExpiryDays) * 24 * time.Hour).UTC()
	if want.Sub(*m.ExpiresAt).Abs() < renewGrace {
		return nil
	}
	meta, err := readMeta(site.dir)
	if err != nil {
		return err
	}
	meta.ExpiresAt = &want
	meta.UpdatedAt = time.Now().UTC()
	if err := writeMeta(site.dir, meta); err != nil {
		return err
	}
	site.Meta = meta
	return nil
}

// RenewExpiry is renewExpiryLocked for callers that mutate a site's files
// outside the store's own methods — the WebDAV handler, which writes through
// its own filesystem. It must not be called while holding the site lock.
func (s *Store) RenewExpiry(site *Site) error {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()
	return s.renewExpiryLocked(site)
}
