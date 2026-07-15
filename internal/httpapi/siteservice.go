package httpapi

import (
	"github.com/ittrail/sitebin.io/internal/ext"
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
