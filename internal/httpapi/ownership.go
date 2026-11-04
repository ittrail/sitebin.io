package httpapi

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The core's site service is the optional ext.OwnershipSites: the extension
// asserts it, and a SiteService without it stays whole.
var _ ext.OwnershipSites = siteService{}

// mapOwnershipErr maps the store's ownership refusals onto the seam's
// sentinels; not-found and lock refusals map as everywhere else.
func mapOwnershipErr(err error, viewID string) error {
	switch {
	case errors.Is(err, store.ErrNotOwner):
		return fmt.Errorf("%w: %s", ext.ErrNotOwner, viewID)
	case errors.Is(err, store.ErrNotAdoptable):
		return fmt.Errorf("%w: %s", ext.ErrNotAdoptable, viewID)
	case errors.Is(err, store.ErrTooManyAdmins):
		return fmt.Errorf("%w: %s", ext.ErrTooManyAdmins, viewID)
	}
	return mapSiteErr(err, viewID)
}

// stampWith is the store's restamp callback for a grant: exactly what
// creation stamps (stampGrant), and whether the site is trusted.
func stampWith(g ext.CreateGrant) func(*store.Meta, time.Time) bool {
	return func(m *store.Meta, now time.Time) bool { return stampGrant(m, g, now) }
}

// ownershipEntry is a site-log line for a change the extension asked for.
// The extension names the acting account, address and user agent (by).
func ownershipEntry(action string, by ext.ChangeBy, detail string) provenance.Entry {
	return provenance.Entry{
		Time:    time.Now().UTC(),
		Action:  action,
		Surface: provenance.SurfaceDashboard,
		Auth:    provenance.AuthSession,
		Account: by.Account,
		IP:      by.IP,
		UA:      by.UserAgent,
		Detail:  detail,
	}
}

func (s siteService) TransferSite(viewID, from, to string, g ext.CreateGrant, detach []string, by ext.ChangeBy) (string, error) {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return "", mapSiteGone(err, viewID)
	}
	pw, err := s.a.st.TransferOwner(site, from, to, detach, stampWith(g))
	if err != nil {
		return "", mapOwnershipErr(err, viewID)
	}
	// The hash is replaced, so the old password fails on its own, but a
	// verification cached in the last minutes would still pass; and an upload
	// token was issued under the old owner's authority.
	s.a.verifyCache.Drop(site.EditID + ":")
	s.a.uploads.revokeSite(site.ViewID)
	s.a.record(site, ownershipEntry(provenance.ActionTransfer, by, "from "+from+" to "+to))
	return pw, nil
}

func (s siteService) AdoptSite(viewID, to string, g ext.CreateGrant, by ext.ChangeBy) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	if err := s.a.st.AdoptOwner(site, to, stampWith(g)); err != nil {
		return mapOwnershipErr(err, viewID)
	}
	s.a.record(site, ownershipEntry(provenance.ActionAdopt, by, "adopted by "+to))
	return nil
}

func (s siteService) SetAdmins(viewID string, by ext.ChangeBy, edit func([]ext.SiteAdmin) ([]ext.SiteAdmin, error)) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	err = s.a.st.UpdateAdmins(site, func(cur []store.SiteAdmin) ([]store.SiteAdmin, error) {
		out, err := edit(extAdmins(cur))
		if err != nil {
			return nil, err
		}
		next := make([]store.SiteAdmin, 0, len(out))
		for _, a := range out {
			next = append(next, store.SiteAdmin{Account: a.Account, Until: a.Until})
		}
		return next, nil
	})
	if err != nil {
		return mapOwnershipErr(err, viewID)
	}
	s.a.record(site, ownershipEntry(provenance.ActionAdmins, by, fmt.Sprintf("%d admin(s)", len(site.Meta.Admins))))
	return nil
}

// extAdmins maps the store's admins onto the seam's; nil stays nil.
func extAdmins(in []store.SiteAdmin) []ext.SiteAdmin {
	if len(in) == 0 {
		return nil
	}
	out := make([]ext.SiteAdmin, 0, len(in))
	for _, a := range in {
		out = append(out, ext.SiteAdmin{Account: a.Account, Until: a.Until})
	}
	return out
}

// CheckEditPassword goes through verifyEditIP, so the per-address and
// per-site limits and the verify cache apply exactly as on every surface.
func (s siteService) CheckEditPassword(viewID, password, ip string) (bool, time.Duration) {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return false, 0
	}
	switch s.a.verifyEditIP(ip, site, password) {
	case verifyOK:
		return true, 0
	case verifyThrottled:
		return false, s.a.authRetryAfter()
	}
	return false, 0
}

// authRetryAfter is how long until the password limiter has a token again:
// it refills RateAuthPer5Min tokens per five minutes. Never zero, so a
// throttled caller always has something to wait for.
func (a *API) authRetryAfter() time.Duration {
	n := max(a.cfg.RateAuthPer5Min, 1)
	return max(5*time.Minute/time.Duration(n), time.Second)
}

// ResolveSite maps a view URL, an edit URL, a view id, an edit id or a custom
// domain (host or host/path) to the site's view id. The report resolver
// covers the view forms and a domain; the edit forms are added here.
func (s siteService) ResolveSite(in string) (string, bool) {
	in = strings.TrimSpace(in)
	if in == "" || len(in) > 2048 {
		return "", false
	}
	if u, err := url.Parse(in); err == nil && u.Host != "" && strings.EqualFold(hostWithoutPort(u.Host), s.a.cfg.BaseDomain) {
		if rest, ok := strings.CutPrefix(u.Path, "/e/"); ok {
			id, _, _ := strings.Cut(rest, "/")
			if site, err := s.a.st.ByEditID(strings.ToLower(id)); err == nil {
				return site.ViewID, true
			}
		}
	}
	if site := s.a.resolveTarget(in); site != nil {
		return site.ViewID, true
	}
	if site, err := s.a.st.ByEditID(strings.ToLower(in)); err == nil {
		return site.ViewID, true
	}
	return "", false
}

// WillNotMove lists the addresses whose proof is the sender's account, not
// the address's own DNS: a name in the sender's zone, a name in an operator
// zone, and an address held through its host. A transfer detaches them.
func (s siteService) WillNotMove(viewID, sender string) []string {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range site.Meta.CustomDomains {
		viaHost := false
		for _, c := range site.Meta.DomainClaims {
			if c.Domain == d && c.ViaHost {
				viaHost = true
			}
		}
		if viaHost || s.a.st.ZoneOf(d, sender) != "" || s.a.st.InOperatorZone(d) {
			out = append(out, d)
		}
	}
	return out
}

// HostConflicts maps each host the bundle's sites use to the sender's other
// sites that also have an address on it: they would be left sharing a host
// with a site that changed owner.
func (s siteService) HostConflicts(viewIDs []string, sender string) map[string][]string {
	out := map[string][]string{}
	if sender == "" {
		return out
	}
	for _, id := range viewIDs {
		site, err := s.a.st.ByViewID(id)
		if err != nil {
			continue
		}
		for _, d := range site.Meta.CustomDomains {
			host := store.HostOf(d)
			for _, ha := range s.a.st.HostAddresses(host) {
				other := ha.Site
				if slices.Contains(viewIDs, other.ViewID) || other.Meta.OwnerAccountID != sender {
					continue
				}
				if !slices.Contains(out[host], other.ViewID) {
					out[host] = append(out[host], other.ViewID)
				}
			}
		}
	}
	for _, ids := range out {
		slices.Sort(ids)
	}
	return out
}

// FormRecipients lists the recipients of the site's active and pending
// forms, each once, in the order the forms hold them.
func (s siteService) FormRecipients(viewID string) []string {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range site.Meta.Forms {
		if (f.Status == store.FormActive || f.Status == store.FormPending) && f.Recipient != "" && !slices.Contains(out, f.Recipient) {
			out = append(out, f.Recipient)
		}
	}
	return out
}

// SiteBytes is the central byte function: files plus the Git repository
// (store.SiteUsage), and the file count.
func (s siteService) SiteBytes(viewID string) (int64, int, error) {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return 0, 0, mapSiteGone(err, viewID)
	}
	u, err := s.a.st.SiteUsage(site)
	if err != nil {
		return 0, 0, err
	}
	return u.Bytes(), u.Files, nil
}
