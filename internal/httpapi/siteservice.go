package httpapi

import (
	"errors"
	"fmt"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// SiteService returns the ext.SiteService implementation the enterprise
// dashboard uses to read and manage owned sites. It goes through the API so
// mutations also invalidate the auth caches.
func (a *API) SiteService() ext.SiteService { return siteService{a} }

type siteService struct{ a *API }

func (s siteService) Info(viewID string) (ext.SiteInfo, bool) {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return ext.SiteInfo{}, false
	}
	return s.infoOf(site), true
}

// infoOf maps one site onto the seam's view of it. Shared by Info and All so
// the admin console and the account dashboard can never disagree about what a
// site looks like.
func (s siteService) infoOf(site *store.Site) ext.SiteInfo {
	bytes, files, _ := s.a.st.Usage(site)
	st := s.a.st.Stats(site)
	return ext.SiteInfo{
		Violations: st.CSPViolations,
		Blocked:    st.CSPBlocked,
		ViewID:     site.ViewID,
		Owner:      site.Meta.OwnerAccountID,
		Mode:       site.Meta.Mode,
		Domains:    site.Meta.CustomDomains,
		Bytes:      bytes,
		Files:      files,
		ViewURL:    s.a.cfg.ViewURL(site.ViewID),
		EditURL:    s.a.cfg.EditURL(site.Meta.EditID),
		CreatedAt:  site.Meta.CreatedAt,
		ExpiresAt:  site.Meta.ExpiresAt,
	}
}

func (s siteService) All() ([]ext.SiteInfo, error) {
	sites, err := s.a.st.AllSites()
	if err != nil {
		return nil, err
	}
	out := make([]ext.SiteInfo, 0, len(sites))
	for _, site := range sites {
		out = append(out, s.infoOf(site))
	}
	return out, nil
}

func (s siteService) SetExpiry(viewID string, at *time.Time) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	err = s.a.st.Update(site, func(m *store.Meta) error {
		m.ExpiresAt = at
		// An operator's date is not the plan's. Leaving ExpiryFromTier set
		// would let the next sliding renewal move the date the admin just
		// chose, which is the same bug the tier-lifetime work removed from the
		// API path.
		m.ExpiryFromTier = false
		return nil
	})
	return mapSiteGone(err, viewID)
}

func (s siteService) RotateEditPassword(viewID string) (string, error) {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return "", err
	}
	pw, err := s.a.st.SetEditPassword(site)
	if err != nil {
		return "", err
	}
	// drop any cached verifications for the old password
	s.a.verifyCache.Drop(site.EditID + ":")
	return pw, nil
}

func (s siteService) Delete(viewID string) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return err
	}
	s.a.verifyCache.Drop(site.EditID + ":")
	return s.a.st.Delete(site)
}

func (s siteService) ApplyQuota(viewID string, g ext.CreateGrant) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	// A tier change can flip trust in either direction, so the marker is
	// restamped with the caps. Failing here would leave the site's headers
	// disagreeing with its tier, which for a downgrade means unprotected.
	if err := s.a.st.SetTrusted(site, g.Trusted); err != nil {
		return err
	}
	if err := s.a.st.ApplyQuota(site, quotaFromGrant(g), store.DowngradeGrace); err != nil {
		// The site can vanish here too, not just at the lookup above: the same
		// edit-page delete or cleanup sweep that races the lookup can just as
		// easily land between it and this write. The store reports that the
		// same way, with ErrNotFound, so the write needs the same mapping —
		// otherwise this one path leaks store.ErrNotFound across the seam and
		// the caller treats a routine "it's gone" as a real failure instead of
		// dropping its stale marker.
		return mapSiteGone(err, viewID)
	}
	return nil
}

// mapSiteGone maps the store's own not-found sentinel onto the seam's
// ErrSiteGone, so callers on the other side of ext never see a core error
// type. A site the extension still has an ownership marker for can have been
// deleted from the edit page or swept away — neither notifies it — so this is
// the caller's cue to drop the stale marker instead of retrying an operation
// that can never succeed. Errors of any other kind pass through unchanged.
func mapSiteGone(err error, viewID string) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %s", ext.ErrSiteGone, viewID)
	}
	return err
}

// quotaFromGrant maps the extension's grant onto the store's cap set.
func quotaFromGrant(g ext.CreateGrant) store.Quota {
	return store.Quota{
		Bytes:      g.MaxSiteBytes,
		Files:      g.MaxFiles,
		ExpiryDays: g.MaxExpiryDays,
		Domains:    g.MaxCustomDomain,
		WebDAV:     g.WebDAV,
	}
}
