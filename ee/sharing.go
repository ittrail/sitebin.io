//go:build ee

package ee

import (
	"errors"
	"fmt"
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
// It sits on the write path of an admin's token, so it stays cheap: ByID is a
// file read, and effectiveTierStrict answers from the PayGate cache; only on a
// cache miss does it ask PayGate, at most once per account per cache period.
//
// An unknown or suspended account is false, nil. In accounts mode there are no
// tiers to withhold a feature, so a live account may share. In tiers mode an
// unknown plan is an ERROR, never false: the core then refuses the admin
// standing for that one request and ends no share on a failed lookup.
func (p *provider) SharingAllowed(accountID string) (bool, error) {
	if !p.cfg.Enabled() {
		return false, nil // open mode has no accounts to share between
	}
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
	switch p.cfg.Mode {
	case eeconfig.ModeAccounts:
		return true, nil
	case eeconfig.ModeTiers:
	default:
		return false, nil // open mode has no accounts to share between
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

// The two methods of the core's ext.OwnershipSites that the account pages use,
// asserted on their own so a SiteService (or a test fake) that does not
// carry the rest of that interface still serves them.
type siteOwnerReader interface {
	SiteOwner(viewID string) (string, error)
}

type ownerGuardedDeleter interface {
	DeleteSiteIfOwner(viewID, owner string) error
}

// siteOwner is the owner in a site's meta.json, through the core's optional
// ext.OwnershipSites. It reads meta only. A SiteService without it cannot
// vouch for any owner, which every caller treats as "not yours".
func (p *provider) siteOwner(viewID string) (string, error) {
	r, ok := p.host.Sites().(siteOwnerReader)
	if !ok {
		return "", errors.New("the site service cannot report a site's owner")
	}
	return r.SiteOwner(viewID)
}

// ownedSiteIDs lists the view ids the account owns, by the truth: the site's
// meta.json owner, not the marker under accounts/<id>/sites/. A transfer
// leaves ee to relink the marker, and a crash in between can leave a stale one
// for the old owner; a marker alone grants nothing. An ownerless (anonymous)
// site is nobody's by this rule: a marker on it grants nothing either.
//
// A marker whose site no longer exists (ErrSiteGone) stays in the list, so
// the callers that heal dangling markers (restampSites, suspend.eachSite, the
// deletions) still meet it as ErrSiteGone. A site whose owner cannot be read
// is left out, logged, and reported in the returned error together with the
// sites that did resolve: a caller that passes errors on fails, and one that
// ignores it sees fewer sites, never more. A listing error returns nil and
// the error.
func (p *provider) ownedSiteIDs(acc *account.Account) ([]string, error) {
	ids, err := p.accounts.ListSiteIDs(acc)
	if err != nil {
		slog.Error("could not list the sites of an account", "account", acc.ID, "err", err)
		return nil, err
	}
	out := make([]string, 0, len(ids))
	var failed []error
	for _, id := range ids {
		owner, err := p.siteOwner(id)
		switch {
		case errors.Is(err, ext.ErrSiteGone):
			out = append(out, id)
		case err != nil:
			slog.Error("could not read the owner of a site; leaving it out", "account", acc.ID, "site", id, "err", err)
			failed = append(failed, fmt.Errorf("site %s: %w", id, err))
		case owner == acc.ID:
			out = append(out, id)
		}
	}
	return out, errors.Join(failed...)
}

// ownedSiteInfos is the Info of every site meta.json gives to the account, in
// the one Info read each (which carries the owner). A site that no longer
// exists is skipped. For walks that need the Info anyway.
func (p *provider) ownedSiteInfos(acc *account.Account) ([]ext.SiteInfo, error) {
	ids, err := p.accounts.ListSiteIDs(acc)
	if err != nil {
		slog.Error("could not list the sites of an account", "account", acc.ID, "err", err)
		return nil, err
	}
	out := make([]ext.SiteInfo, 0, len(ids))
	for _, id := range ids {
		if info, ok := p.host.Sites().Info(id); ok && info.Owner == acc.ID {
			out = append(out, info)
		}
	}
	return out, nil
}

// deleteOwnedSiteStrict is the owner-checked delete for a single site (the
// dashboard's delete): every refusal is returned as the core gave it.
func (p *provider) deleteOwnedSiteStrict(viewID, owner string) (bool, error) {
	d, ok := p.host.Sites().(ownerGuardedDeleter)
	if !ok {
		return false, errors.New("the site service cannot delete a site by owner")
	}
	if err := d.DeleteSiteIfOwner(viewID, owner); err != nil {
		return false, err
	}
	return true, nil
}

// deleteOwnedSite is the account-deletion callback's delete. The core deletes
// the site only if, under the site lock, meta.json still names the account:
// a stale marker must not cost another account its site. Not the owner is a
// skip (the marker goes with the account directory); a site that is already
// gone is not a failure; every other error stops the deletion.
func (p *provider) deleteOwnedSite(acc *account.Account, viewID string) (deleted bool, err error) {
	d, ok := p.host.Sites().(ownerGuardedDeleter)
	if !ok {
		return false, errors.New("the site service cannot delete a site by owner")
	}
	err = d.DeleteSiteIfOwner(viewID, acc.ID)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ext.ErrSiteGone):
		return false, nil // a stale ownership marker is not a reason to keep the account
	case errors.Is(err, ext.ErrNotOwner):
		slog.Warn("account deletion: skipping a site that meta.json gives to another account",
			"account", acc.ID, "site", viewID)
		return false, nil
	}
	return false, err
}
