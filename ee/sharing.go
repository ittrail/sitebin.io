//go:build ee

package ee

import (
	"errors"
	"log/slog"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// Sharing: the plan gate for transfer and admins, and the one rule for who
// owns a site. The core keeps the per-site truth (owner and admins in
// meta.json); this file answers only about accounts.

var _ ext.SharingAccounts = (*provider)(nil)

// SharingAllowed reports whether the account's CURRENT plan includes sharing.
// It sits on the write path of an admin's token, so it adds no network call:
// effectiveTierStrict reads the PayGate cache, and ByID is a file read.
//
// An unknown or suspended account is false, nil. In accounts mode there are no
// tiers to withhold a feature, so a live account may share. In tiers mode an
// unknown plan is an ERROR, never false: the core then refuses the admin
// standing for that one request and ends no share on a failed lookup.
func (p *provider) SharingAllowed(accountID string) (bool, error) {
	acc, err := p.accounts.ByID(accountID)
	if errors.Is(err, account.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if acc.Suspended() {
		return false, nil
	}
	if p.cfg.Mode != eeconfig.ModeTiers {
		return true, nil
	}
	t, err := p.effectiveTierStrict(acc)
	if err != nil {
		return false, err
	}
	return t.Sharing, nil
}

// AdminTags is an admin's own tags on a shared site. Task 5 replaces this.
func (p *provider) AdminTags(accountID, viewID string) []string { return nil }

// SetAdminTags stores an admin's own tags. Task 5 replaces this.
func (p *provider) SetAdminTags(accountID, viewID string, tags []string) error {
	return errors.New("admin tags are not available yet")
}

// SharedSiteIDs lists the sites shared with the account. Task 5 replaces this.
func (p *provider) SharedSiteIDs(accountID string) []string { return nil }

// ownedSiteIDs lists the view ids the account owns, by the truth: the site's
// meta.json owner, not the marker under accounts/<id>/sites/. A transfer
// leaves ee to relink the marker, and a crash in between can leave a stale one
// for the old owner; a marker alone grants nothing.
//
// A marker whose site no longer exists is kept in the list, so the callers
// that heal dangling markers (restampSites, suspend.eachSite, the deletions)
// still meet it as ErrSiteGone. A site with no owner in its meta is nobody
// else's and is kept too. On a listing error it returns nil and the error
// (and logs), never a partial list to be taken for complete.
func (p *provider) ownedSiteIDs(acc *account.Account) ([]string, error) {
	ids, err := p.accounts.ListSiteIDs(acc)
	if err != nil {
		slog.Error("could not list the sites of an account", "account", acc.ID, "err", err)
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if p.ownsByMeta(acc, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// ownsByMeta is false only when meta.json names a DIFFERENT owner.
func (p *provider) ownsByMeta(acc *account.Account, viewID string) bool {
	info, ok := p.host.Sites().Info(viewID)
	return !ok || info.Owner == "" || info.Owner == acc.ID
}

// deleteOwnedSite is the account-deletion callback's delete: a site whose
// meta names someone else is skipped (a stale marker must not cost another
// account its site) and left to Store.Delete, which removes the marker with
// the account directory.
func (p *provider) deleteOwnedSite(acc *account.Account, viewID string) error {
	if !p.ownsByMeta(acc, viewID) {
		slog.Warn("account deletion: skipping a site that meta.json gives to another account",
			"account", acc.ID, "site", viewID)
		return nil
	}
	err := p.host.Sites().Delete(viewID)
	if errors.Is(err, ext.ErrSiteGone) {
		return nil // a stale ownership marker is not a reason to keep the account
	}
	return err
}
