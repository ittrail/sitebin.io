package httpapi

import (
	"errors"
	"fmt"

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
	bytes, files, _ := s.a.st.Usage(site)
	return ext.SiteInfo{
		ViewID:    site.ViewID,
		Mode:      site.Meta.Mode,
		Bytes:     bytes,
		Files:     files,
		ViewURL:   s.a.cfg.ViewURL(site.ViewID),
		EditURL:   s.a.cfg.EditURL(site.Meta.EditID),
		CreatedAt: site.Meta.CreatedAt,
		ExpiresAt: site.Meta.ExpiresAt,
	}, true
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
		// A site the extension still has an ownership marker for can have been
		// deleted from the edit page or swept away — neither notifies it. Report
		// that as the seam's own ErrSiteGone (not the store's ErrNotFound, which
		// must not cross the seam) so the caller can drop the stale marker
		// instead of retrying a restamp that can never succeed.
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: %s", ext.ErrSiteGone, viewID)
		}
		return err
	}
	return s.a.st.ApplyQuota(site, quotaFromGrant(g), store.DowngradeGrace)
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
