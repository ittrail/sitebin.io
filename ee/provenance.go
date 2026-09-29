//go:build ee

package ee

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// The account half of provenance: where an account came from (sign-up), where
// it signs in from, where each of its API tokens was minted — one abuser of
// 2026-09-28 minted a token in the minute the account was created — and the
// core's mirror of its site creations and deletions. See
// docs/superpowers/specs/2026-09-29-provenance-csp-apex.md.

var _ ext.AccountProvenance = (*provider)(nil)

// accountEntry builds an entry for a request that acted on an account. The
// address is auth.ClientIP's: the last X-Forwarded-For entry, the one Caddy
// appended.
func accountEntry(r *http.Request, action, surface, credential string) provenance.Entry {
	return provenance.Entry{
		Time:    time.Now().UTC(),
		Action:  action,
		Surface: surface,
		Auth:    credential,
		IP:      auth.ClientIP(r),
		UA:      r.UserAgent(),
	}
}

// RecordAccountProvenance adds an entry to the account's log. Best effort: a
// sign-in or a token is never refused because its record failed.
func (p *provider) RecordAccountProvenance(accountID string, e provenance.Entry) {
	if p.accounts == nil || accountID == "" {
		return
	}
	if err := p.accounts.RecordProvenance(accountID, e); err != nil && !errors.Is(err, account.ErrNotFound) {
		slog.Warn("provenance: could not record for an account", "account", accountID, "action", e.Action, "err", err)
	}
}

// recordAccount is RecordAccountProvenance for a request the account itself
// made.
func (p *provider) recordAccount(r *http.Request, accountID, action, surface, credential, detail string) {
	e := accountEntry(r, action, surface, credential)
	e.Account = accountID
	e.Detail = detail
	p.RecordAccountProvenance(accountID, e)
}

// PurgeProvenance drops account entries older than before. An account held as
// evidence keeps its log: a suspended one, or one owning a locked site — the
// same holds that keep a locked site's own log and stop a GDPR deletion.
func (p *provider) PurgeProvenance(before time.Time) {
	if p.accounts == nil {
		return
	}
	ids, err := p.accounts.ListIDs()
	if err != nil {
		slog.Error("provenance: could not list accounts for the purge", "err", err)
		return
	}
	for _, id := range ids {
		acc, err := p.accounts.ByID(id)
		if err != nil || acc.Suspended() {
			continue
		}
		if locked, err := p.lockedSites(acc); err != nil || len(locked) > 0 {
			continue // an error: keep, and ask again at the next sweep
		}
		if n, err := p.accounts.PurgeProvenance(id, before); err != nil {
			slog.Error("provenance: purge an account's log", "account", id, "err", err)
		} else if n > 0 {
			slog.Info("provenance: purged account entries past retention", "account", id, "entries", n)
		}
	}
}

// recordSite adds a change the dashboard made to a site's log, through the
// core's optional seam.
func (p *provider) recordSite(r *http.Request, acc *account.Account, viewID, action, detail string) {
	sp, ok := p.host.Sites().(ext.SiteProvenance)
	if !ok {
		return
	}
	e := accountEntry(r, action, provenance.SurfaceDashboard, provenance.AuthSession)
	e.Account = acc.ID
	e.Detail = detail
	sp.RecordSiteProvenance(viewID, e)
}

// recordSiteDelete keeps a dashboard deletion in the account's log: the
// site's own log went with the site.
func (p *provider) recordSiteDelete(r *http.Request, acc *account.Account, viewID string) {
	e := accountEntry(r, provenance.ActionSiteDelete, provenance.SurfaceDashboard, provenance.AuthSession)
	e.Account = acc.ID
	e.Site = viewID
	p.RecordAccountProvenance(acc.ID, e)
}

// reqInfo carries a request's address and client into a context that does
// not otherwise have them: the MCP token verifier, which may create the
// account of a first-time user.
type reqInfo struct{ ip, ua string }

type reqInfoKey struct{}

func withReqInfo(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, reqInfoKey{}, reqInfo{ip: auth.ClientIP(r), ua: r.UserAgent()})
}

// recordTokenSignup records the sign-up of an account the MCP verifier just
// created from an access token.
func (p *provider) recordTokenSignup(ctx context.Context, accountID string) {
	info, _ := ctx.Value(reqInfoKey{}).(reqInfo)
	p.RecordAccountProvenance(accountID, provenance.Entry{
		Time:    time.Now().UTC(),
		Action:  provenance.ActionSignup,
		Surface: provenance.SurfaceMCP,
		Auth:    provenance.AuthOAuth,
		Account: accountID,
		IP:      info.ip,
		UA:      info.ua,
	})
}

// tokenDetail describes a minted token for the log: its name and the prefix
// the dashboard shows, never the secret.
func tokenDetail(tok account.Token) string {
	d := tok.Prefix + "…"
	if tok.Name != "" {
		d = "\"" + tok.Name + "\" " + d
	}
	return d
}
