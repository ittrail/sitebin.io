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
		Violations:  st.CSPViolations,
		Blocked:     st.CSPBlocked,
		Reporters:   st.CSPSources,
		ViewID:      site.ViewID,
		Owner:       site.Meta.OwnerAccountID,
		Mode:        site.Meta.Mode,
		Name:        site.Meta.Name,
		Domains:     site.Meta.CustomDomains,
		DomainLinks: s.domainLinks(site),
		Origin:      site.Meta.Origin,
		Bytes:       bytes,
		Files:       files,
		ViewURL:     s.a.cfg.ViewURL(site.ViewID),
		EditURL:     s.a.cfg.EditURL(site.Meta.EditID),
		CreatedAt:   site.Meta.CreatedAt,
		ExpiresAt:   site.Meta.ExpiresAt,
		Locked:      s.a.extLock(site.Meta.Locked),
		Findings:    extFindings(site.Meta.Abuse),
	}
}

// extFindings maps the abuse guard's unreviewed findings onto the seam's.
func extFindings(a *store.AbuseState) []ext.ScanFinding {
	if a == nil || len(a.Findings) == 0 {
		return nil
	}
	out := make([]ext.ScanFinding, 0, len(a.Findings))
	for _, f := range a.Findings {
		out = append(out, ext.ScanFinding{Rule: f.Rule, Severity: f.Severity, Path: f.Path, Excerpt: f.Excerpt, Source: f.Source, At: f.At})
	}
	return out
}

// extLock maps the store's lock record onto the seam's, with the date its
// retention runs out — the core's rule, so the register never states another.
func (a *API) extLock(l *store.SiteLock) *ext.SiteLock {
	if l == nil {
		return nil
	}
	out := &ext.SiteLock{At: l.At, Reason: l.Reason, By: l.By}
	if h := l.Hold; h != nil {
		out.Hold = &ext.LockHold{At: h.At, By: h.By}
	}
	if at, ok := a.st.LockRetentionEnds(l); ok {
		out.PurgeAt = &at
	}
	return out
}

// domainLinks lists the verified domains with the URL each serves at, then the
// pending claims, which serve nothing yet.
func (s siteService) domainLinks(site *store.Site) []ext.DomainLink {
	var out []ext.DomainLink
	for _, d := range site.Meta.CustomDomains {
		out = append(out, ext.DomainLink{Domain: d, URL: s.a.cfg.SiteURL(d)})
	}
	for _, c := range site.PendingDomains() {
		out = append(out, ext.DomainLink{Domain: c.Domain, Pending: true})
	}
	return out
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

func (s siteService) CustomDomainCount() (int, error) { return s.a.st.CountDomains() }

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

func (s siteService) SetName(viewID, name string) error {
	clean, err := store.CleanSiteName(name)
	if err != nil {
		return err
	}
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	err = s.a.st.Update(site, func(m *store.Meta) error {
		if m.IsLocked() {
			return store.ErrLocked
		}
		m.Name = clean
		return nil
	})
	return mapSiteErr(err, viewID)
}

func (s siteService) RotateEditPassword(viewID string) (string, error) {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return "", err
	}
	// A new password would be a new way in to a site the operator froze.
	if site.Meta.IsLocked() {
		return "", fmt.Errorf("%w: %s", ext.ErrSiteLocked, viewID)
	}
	pw, err := s.a.st.SetEditPassword(site)
	if err != nil {
		return "", err
	}
	// drop any cached verifications for the old password
	s.a.verifyCache.Drop(site.EditID + ":")
	// and every upload token issued under it: rotating is how an owner cuts
	// off whoever held access
	s.a.uploads.revokeSite(site.ViewID)
	return pw, nil
}

func (s siteService) Delete(viewID string) error { return s.delete(viewID, false) }

func (s siteService) ForceDelete(viewID string) error { return s.delete(viewID, true) }

func (s siteService) delete(viewID string, force bool) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	// Refused before anything is torn down: a locked site's containers and
	// tokens are already stopped, and nothing about it may change.
	if site.Meta.IsLocked() && !force {
		return fmt.Errorf("%w: %s", ext.ErrSiteLocked, viewID)
	}
	s.a.verifyCache.Drop(site.EditID + ":")
	s.a.uploads.revokeSite(site.ViewID)
	s.a.stopContainersBeforeDelete(site)
	if force {
		err = s.a.st.ForceDelete(site)
	} else {
		err = s.a.st.Delete(site)
	}
	return mapSiteErr(err, viewID)
}

func (s siteService) SetLock(viewID string, lock *ext.SiteLock) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	var sl *store.SiteLock
	if lock != nil {
		sl = &store.SiteLock{At: lock.At, Reason: lock.Reason, By: lock.By}
	}
	changed, err := s.a.st.SetLock(site, sl)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	if changed {
		s.a.lockChanged(site)
	}
	return nil
}

func (s siteService) SetHold(viewID string, hold *ext.LockHold) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	var h *store.LockHold
	if hold != nil {
		h = &store.LockHold{At: hold.At, By: hold.By}
	}
	_, err = s.a.st.SetHold(site, h)
	if errors.Is(err, store.ErrNotLocked) {
		return fmt.Errorf("%w: %s", ext.ErrSiteNotLocked, viewID)
	}
	return mapSiteGone(err, viewID)
}

func (s siteService) ReleaseLock(viewID, by string) (bool, error) {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return false, mapSiteGone(err, viewID)
	}
	released, err := s.a.st.ReleaseLock(site, by)
	if err != nil {
		return false, mapSiteGone(err, viewID)
	}
	if released {
		s.a.lockChanged(site)
	}
	return released, nil
}

func (s siteService) ClearFindings(viewID string) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	_, err = s.a.st.ClearFindings(site)
	return mapSiteGone(err, viewID)
}

func (s siteService) Reports() ([]ext.AbuseReport, error) {
	reps, err := s.a.st.ListReports()
	if err != nil {
		return nil, err
	}
	out := make([]ext.AbuseReport, 0, len(reps))
	for _, r := range reps {
		via := r.Via
		if via == "" {
			via = store.ReportViaAPI // before the page, the API was the only way in
		}
		out = append(out, ext.AbuseReport{Time: r.Time, Target: r.Target, ViewID: r.ViewID, Reason: r.Reason,
			Details: r.Details, Contact: r.Contact, Source: r.Source, Via: via})
	}
	return out, nil
}

func (s siteService) ApplyQuota(viewID string, g ext.CreateGrant) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	// The hold freezes the site's caps and expiry with everything else: a
	// downgrade's grace date or a shrunken cap on a locked site would only
	// be a surprise waiting for the unlock.
	if site.Meta.IsLocked() {
		return nil
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

// mapSiteErr is mapSiteGone that also maps the store's lock refusal onto the
// seam's ErrSiteLocked, for the methods a lock refuses.
func mapSiteErr(err error, viewID string) error {
	if errors.Is(err, store.ErrLocked) {
		return fmt.Errorf("%w: %s", ext.ErrSiteLocked, viewID)
	}
	return mapSiteGone(err, viewID)
}

// quotaFromGrant maps the extension's grant onto the store's cap set.
func quotaFromGrant(g ext.CreateGrant) store.Quota {
	return store.Quota{
		Bytes:      g.MaxSiteBytes,
		Files:      g.MaxFiles,
		ExpiryDays: g.MaxExpiryDays,
		Domains:    g.MaxCustomDomain,
		WebDAV:     g.WebDAV,
		Forms:      g.MaxForms,
	}
}
