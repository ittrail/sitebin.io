package httpapi

import (
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// stampCaps writes an owner's per-site caps from a grant onto m. Creation
// calls it before any upload, so the tier caps hold for that request's files.
func stampCaps(m *store.Meta, g ext.CreateGrant) {
	m.QuotaBytes = g.MaxSiteBytes
	m.QuotaFiles = g.MaxFiles
	m.QuotaExpiryDays = g.MaxExpiryDays
	m.QuotaDomains = g.MaxCustomDomain
	m.QuotaWebDAV = g.WebDAV
	m.QuotaGit = g.Git
	m.QuotaForms = g.MaxForms
}

// setTierExpiry imposes the plan's lifetime: now plus the site's cap, labelled
// as the tier's. The caller has checked the cap is set.
func setTierExpiry(m *store.Meta, now time.Time) {
	exp := now.Add(time.Duration(m.QuotaExpiryDays) * 24 * time.Hour).UTC()
	m.ExpiresAt = &exp
	m.ExpiryFromTier = true
}

// stampGrant stamps a grant onto a site exactly as creation does: the caps,
// forms and git flag, and the lifetime. A tier-imposed (or absent) expiry is
// set to now plus the new cap, or cleared when the cap is none. An
// owner-chosen date survives, clamped to now plus the cap when it is later,
// and is never relabelled as the tier's (the tier-change rule). It returns
// whether the site is trusted; the caller applies the trust marker, under
// the same site lock as the write.
func stampGrant(m *store.Meta, g ext.CreateGrant, now time.Time) (trusted bool) {
	stampCaps(m, g)
	switch {
	case m.ExpiresAt == nil || m.ExpiryFromTier:
		if m.QuotaExpiryDays > 0 {
			setTierExpiry(m, now)
		} else {
			m.ExpiresAt = nil
			m.ExpiryFromTier = false
		}
	case m.QuotaExpiryDays > 0:
		if limit := now.Add(time.Duration(m.QuotaExpiryDays) * 24 * time.Hour).UTC(); m.ExpiresAt.After(limit) {
			m.ExpiresAt = &limit
		}
	}
	return g.Trusted
}
