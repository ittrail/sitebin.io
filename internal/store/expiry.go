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
	meta.ExpiryFromTier = true
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

// DowngradeGrace is how long a site that had no expiry keeps living after its
// owner moves to a tier that caps lifetimes. It is deliberately longer than any
// tier's own cap: dropping a permanent site to its new tier's lifetime would be
// a deletion in all but name.
const DowngradeGrace = 30 * 24 * time.Hour

// Quota is the set of per-site caps a tier grants. It mirrors the Quota* fields
// of Meta; nil pointers mean "inherit the instance global".
type Quota struct {
	Bytes      int64
	Files      int
	ExpiryDays int
	Domains    *int
	WebDAV     *bool
}

// ApplyQuota restamps a site's per-site caps and reconciles its expiry with the
// new lifetime cap. It is the single place the tier-change transition table
// lives, so the cleanup sweep and the enterprise tier sync cannot drift apart.
//
// grace is how long a site that currently has NO expiry gets before the new cap
// applies. Callers that must not create an expiry (the cleanup sweep, which is
// only ever looking at sites that already have one) pass 0.
//
// An expiry the owner chose is never lifted, only clamped when it exceeds the
// new cap.
func (s *Store) ApplyQuota(site *Site, q Quota, grace time.Duration) error {
	return s.Update(site, func(m *Meta) error {
		m.QuotaBytes = q.Bytes
		m.QuotaFiles = q.Files
		m.QuotaExpiryDays = q.ExpiryDays
		m.QuotaDomains = q.Domains
		m.QuotaWebDAV = q.WebDAV

		now := time.Now()
		if q.ExpiryDays <= 0 {
			// the tier no longer expires sites; lift only what the tier imposed
			if m.ExpiryFromTier {
				m.ExpiresAt = nil
				m.ExpiryFromTier = false
			}
			return nil
		}
		limit := now.Add(time.Duration(q.ExpiryDays) * 24 * time.Hour).UTC()
		switch {
		case m.ExpiresAt == nil:
			if grace <= 0 {
				return nil
			}
			until := now.Add(grace).UTC()
			m.ExpiresAt = &until
			m.ExpiryFromTier = true
		case m.ExpiresAt.After(limit): // an expiry exactly at the limit counts as "within" and is left alone
			m.ExpiresAt = &limit
			m.ExpiryFromTier = true
		}
		return nil
	})
}
