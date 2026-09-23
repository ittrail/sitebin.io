package httpapi

import (
	"context"
	"errors"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Account zones, as the enterprise dashboard manages them. Zones are an
// account setting, so they are reachable only through this seam — never
// through the JSON API or MCP, where an API token acts on sites and never on
// the account. Adding a NAME inside a zone is an ordinary domain add.

func (s siteService) ClaimZone(accountID, zone string) (ext.ZoneInfo, bool, error) {
	z, err := s.a.st.ClaimZone(context.Background(), accountID, zone)
	switch {
	case err == nil:
		s.a.log.Info("zone claimed and verified", "account", accountID, "zone", z.Zone)
		return s.zoneInfo(z), true, nil
	case errors.Is(err, store.ErrZonePending):
		s.a.log.Info("zone claimed, pending verification", "account", accountID, "zone", z.Zone, "conflicts", len(z.Conflicts))
		if z.Zone == "" {
			return ext.ZoneInfo{}, false, err // the lookup failed before a claim existed
		}
		return s.zoneInfo(z), false, nil
	default:
		return ext.ZoneInfo{}, false, err
	}
}

func (s siteService) Zones(accountID string) ([]ext.ZoneInfo, error) {
	zs, err := s.a.st.Zones(accountID)
	if err != nil {
		return nil, err
	}
	out := make([]ext.ZoneInfo, 0, len(zs))
	for _, z := range zs {
		out = append(out, s.zoneInfo(z))
	}
	return out, nil
}

func (s siteService) ReleaseZone(accountID, zone string) error {
	return s.a.st.ReleaseZone(accountID, zone)
}

func (s siteService) ReleaseZones(accountID string) error {
	return s.a.st.ReleaseZones(accountID)
}

// zoneDomains maps each attached domain held through the owner's own account
// zone to that zone, for the edit page's "via zone" note. Omitted when empty.
func (a *API) zoneDomains(site *store.Site) map[string]string {
	var out map[string]string
	for _, d := range site.Meta.CustomDomains {
		if z := a.st.ZoneOf(d, site.Meta.OwnerAccountID); z != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[d] = z
		}
	}
	return out
}

func (s siteService) zoneInfo(z store.Zone) ext.ZoneInfo {
	info := ext.ZoneInfo{
		Zone: z.Zone, Verified: z.Verified(), TXTName: z.TXTName(), TXTValue: z.TXTValue(),
		RequestedAt: z.RequestedAt, FailingSince: z.FailingSince, Conflicts: z.Conflicts,
	}
	for _, n := range s.a.st.ZoneDomains(z.Zone) {
		if n.Owner == z.AccountID {
			info.Names = append(info.Names, ext.ZoneName{Domain: n.Domain, ViewID: n.ViewID})
		}
	}
	return info
}
