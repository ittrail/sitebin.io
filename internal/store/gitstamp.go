package store

// SetQuotaGit writes the site's git stamp and nothing else. The cleanup sweep
// backfills the stamp through it: ApplyQuota would also rewrite the byte and
// file caps and the expiry, which the sweep does not own for a site without
// an expiry.
func (s *Store) SetQuotaGit(site *Site, v *bool) error {
	return s.Update(site, func(m *Meta) error {
		if v == nil {
			m.QuotaGit = nil
		} else {
			b := *v
			m.QuotaGit = &b
		}
		return nil
	})
}

// SetQuotaAccountBytes writes the site's account cap and nothing else, for
// the same reason as SetQuotaGit: the sweep backfills it on sites whose caps
// and expiry it does not own.
func (s *Store) SetQuotaAccountBytes(site *Site, n int64) error {
	return s.Update(site, func(m *Meta) error {
		m.QuotaAccountBytes = n
		return nil
	})
}
