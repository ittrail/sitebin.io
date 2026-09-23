//go:build ee

package ee

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// Account zones on the account page: claim, re-check, remove. The mechanism
// lives in the core store (internal/store/zones.go); the extension says how
// many a plan allows and gives the owner a page. Zones are an account
// setting, so these routes take the session cookie only — an API token never
// reaches them, as it never reaches any account route.

var _ ext.ZoneAccounts = (*provider)(nil)

// ZonesAllowed is the account's max_zones. It resolves the tier strictly: a
// zone reserves names for everybody else, so an unknown plan is an error,
// never a guess.
func (p *provider) ZonesAllowed(accountID string) (int, error) {
	acc, err := p.accounts.ByID(accountID)
	if errors.Is(err, account.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	t, err := p.effectiveTierStrict(acc)
	if err != nil {
		return 0, err
	}
	return t.MaxZones, nil
}

func (p *provider) zoneRoutes(routes map[string]http.Handler) {
	routes["POST /account/zones"] = http.HandlerFunc(p.handleClaimZone)
	routes["POST /account/zones/{zone}/delete"] = http.HandlerFunc(p.handleReleaseZone)
}

type zoneRow struct {
	ext.ZoneInfo
	CSRF string
}

func (p *provider) zoneRows(acc *account.Account, csrf string) []zoneRow {
	zs, err := p.host.Sites().Zones(acc.ID)
	if err != nil {
		slog.Error("list zones", "account", acc.ID, "err", err)
		return nil
	}
	rows := make([]zoneRow, 0, len(zs))
	for _, z := range zs {
		rows = append(rows, zoneRow{ZoneInfo: z, CSRF: csrf})
	}
	return rows
}

// handleClaimZone claims a zone, or re-checks one already claimed: asking
// again is how the owner says "the record is in place now".
func (p *provider) handleClaimZone(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	zone := strings.TrimSpace(r.FormValue("zone"))
	if zone == "" || len(zone) > 253 {
		p.redirect(w, r, "/account")
		return
	}
	z, verified, err := p.host.Sites().ClaimZone(acc.ID, zone)
	switch {
	case err != nil:
		p.renderMessage(w, msgView{Title: "Zone not added", Body: err.Error(), Back: "/account"})
	case verified:
		slog.Info("zone verified", "account", acc.ID, "zone", z.Zone)
		p.redirect(w, r, "/account#zones")
	default:
		p.redirect(w, r, "/account#zones")
	}
}

func (p *provider) handleReleaseZone(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := p.host.Sites().ReleaseZone(acc.ID, r.PathValue("zone")); err != nil {
		p.renderMessage(w, msgView{Title: "Zone not removed", Body: "That zone is not on this account.", Back: "/account"})
		return
	}
	slog.Info("zone removed by its owner", "account", acc.ID, "zone", r.PathValue("zone"))
	p.redirect(w, r, "/account#zones")
}

// releaseZones drops every zone of a deleted account. A failure is logged,
// not fatal: the account and its sites are already gone. A zone left behind
// keeps its names reserved for an account that no longer exists, so the log
// line names it for the operator, who removes data/zones/<zone>.json by hand.
func (p *provider) releaseZones(accountID string) {
	if err := p.host.Sites().ReleaseZones(accountID); err != nil {
		slog.Error("account deletion: could not release the account's zones", "account", accountID, "err", err)
	}
}
