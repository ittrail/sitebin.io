//go:build ee

package ee

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/ownership"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// What an account's deletion does about transfers and shares. Both deletions
// (the stack's GDPR order and the local dashboard's) call the same two
// pieces:
//
//   - beforeAccountDelete, ahead of accounts.Delete: it ends the account's
//     shares on other people's sites, cancels its offers and invitations and
//     removes its share-marker folder. Any error it does not know how to
//     read stops the deletion, so the account is never erased while a share
//     of it may still grant access.
//   - deleteAccountAndSites, the deletion itself: accounts.Delete with
//     deleteOwnedSiteEnding as its per-site callback, then the admins the
//     deleted sites had lose their marker and their own tags for them
//     (after accounts.Delete returned: that takes their account locks).
//
// The account's tag registry lives in its account directory and goes with it.

var errSkipRecord = errors.New("not open")

// linkOwnedMarkers links a marker for every site whose meta.json names the
// account as its owner but which has none (LinkSite failed after an adopt or
// a transfer, and the hourly reconciliation has not run since). An account's
// deletion lists its sites by marker: without this, a site that lost its
// marker would outlive the account, still naming it, and a locked one would
// not stop the deletion. A walk that could not read every site is an error,
// because absence from it proves nothing; a site service without the walk
// (no ext.OwnershipSites) has nothing to link.
func (p *provider) linkOwnedMarkers(acc *account.Account) error {
	lister, ok := p.host.Sites().(siteOwnersLister)
	if !ok {
		return nil
	}
	owners, err := lister.SiteOwners()
	if err != nil {
		return fmt.Errorf("read every site's owner: %w", err)
	}
	have, err := p.accounts.ListSiteIDs(acc)
	if err != nil {
		return fmt.Errorf("list the account's site markers: %w", err)
	}
	for site, so := range owners {
		if so.Owner != acc.ID || slices.Contains(have, site) {
			continue
		}
		if err := p.accounts.LinkSite(acc, site); err != nil {
			return fmt.Errorf("link the marker of %s: %w", site, err)
		}
		slog.Info("account deletion: linked a missing site marker", "account", acc.ID, "site", site)
	}
	return nil
}

// endedShare is a share this deletion ended on someone else's site.
type endedShare struct {
	site, label, ownerID string
}

// lockedShare is an entry left on a locked site: the site is evidence.
type lockedShare struct{ site string }

// beforeAccountDelete is the first half of an account's deletion. by is the
// deletion's surface: changeBy(r, acc) for the dashboard, the background for
// the stack's order.
func (p *provider) beforeAccountDelete(acc *account.Account, by ext.ChangeBy) error {
	if p.owner == nil {
		return nil
	}
	if by.Surface == "" && by.IP == "" && by.UserAgent == "" {
		by.Surface = provenance.SurfaceBackground
	}

	// 1. Shares on others' sites.
	marked, err := p.owner.SharedSites(acc.ID)
	if err != nil {
		return fmt.Errorf("list the account's shares: %w", err)
	}
	var ended []endedShare
	var locked []lockedShare
	for _, viewID := range marked {
		admins, err := p.siteAdmins(viewID)
		switch {
		case errors.Is(err, ext.ErrSiteGone):
			continue
		case err != nil:
			return fmt.Errorf("read the admins of %s: %w", viewID, err)
		case !listsAccount(admins, acc.ID):
			continue
		}
		ownerID, err := p.siteOwner(viewID)
		switch {
		case errors.Is(err, ext.ErrSiteGone):
			continue
		case err != nil:
			return fmt.Errorf("read the owner of %s: %w", viewID, err)
		case ownerID == "" || ownerID == acc.ID:
			continue
		}
		label := p.labelOf(viewID)
		err = p.setAdmins(viewID, ownerID, by, func(cur []ext.SiteAdmin) ([]ext.SiteAdmin, error) {
			return slices.DeleteFunc(slices.Clone(cur), func(a ext.SiteAdmin) bool { return a.Account == acc.ID }), nil
		})
		switch {
		case errors.Is(err, ext.ErrSiteLocked):
			// The site is evidence and its admins cannot change. The entry
			// grants nothing once the account is gone.
			locked = append(locked, lockedShare{viewID})
			continue
		case errors.Is(err, ext.ErrNotOwner), errors.Is(err, ext.ErrSiteGone):
			continue
		case err != nil:
			return fmt.Errorf("end the share of %s: %w", viewID, err)
		}
		ended = append(ended, endedShare{viewID, label, ownerID})
	}

	// 2. Offers and invitations from the account and to its email. An
	// accepting offer is completeAccepting's.
	endedOffers, endedInvites, err := p.cancelAccountRecords(acc)
	if err != nil {
		return err
	}

	// 3. The share-marker folder. Everything above is done, so the folder
	// holds nothing a share needs.
	if err := p.owner.RemoveShares(acc.ID); err != nil {
		return fmt.Errorf("remove the account's share markers: %w", err)
	}

	// Past this point nothing can fail the order: it is the record of what
	// happened, and the mails.
	surface, credential := by.Surface, provenance.AuthNone
	if surface == "" {
		surface, credential = provenance.SurfaceDashboard, provenance.AuthSession
	}
	for _, s := range ended {
		if err := p.dropAdminTags(acc.ID, s.site); err != nil {
			slog.Error("account deletion: could not drop an admin's tags", "site", s.site, "account", acc.ID, "err", err)
		}
		e := provenance.Entry{
			Time: time.Now().UTC(), Action: provenance.ActionAdmins, Surface: surface, Auth: credential,
			Account: acc.ID, IP: by.IP, UA: by.UserAgent, Site: s.site,
			Detail: "a share on the site ended",
		}
		p.RecordAccountProvenance(s.ownerID, counterpartNotice(e))
		ownerEmail := p.emailOf(s.ownerID)
		subject, body := p.shareGoneMail(s.label, ownerEmail, acc.Email, true)
		p.mailVerified(ownerEmail, subject, body)
	}
	if sp, ok := p.host.Sites().(ext.SiteProvenance); ok {
		for _, s := range locked {
			sp.RecordSiteProvenance(s.site, provenance.Entry{
				Time: time.Now().UTC(), Action: provenance.ActionAdmins, Surface: surface, Auth: credential,
				Account: acc.ID, IP: by.IP, UA: by.UserAgent, Site: s.site,
				Detail: "account " + acc.ID + " (" + acc.Email + ") was an admin of this locked site; the account was deleted and the entry is kept as evidence",
			})
		}
	} else if len(locked) > 0 {
		slog.Warn("account deletion: the site service keeps no site log; entries on locked sites are not recorded", "account", acc.ID)
	}
	for _, o := range endedOffers {
		if o.From == acc.ID {
			p.mailOfferCancelled(o, acc)
		} else {
			p.mailOfferEnded(o)
		}
	}
	for _, inv := range endedInvites {
		if inv.From == acc.ID {
			p.mailInviteCancelled(inv, acc)
		}
		// An invitation to the account lapses silently, as every lapse does.
	}
	return nil
}

// cancelAccountRecords ends the open and paused offers and the open
// invitations from the account and to its address, and returns what it
// ended. The account's own are cancelled. Those made TO it end as a lapse
// does (expired): "cancelled" would tell their sender that an account
// existed for the address, so the sender hears the expiry's words and an
// invitation lapses silently. A listing or an update error other than "no
// longer open" stops.
func (p *provider) cancelAccountRecords(acc *account.Account) ([]ownership.Offer, []ownership.Invite, error) {
	offers, err := p.owner.OffersBySender(acc.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("list the account's offers: %w", err)
	}
	invites, err := p.owner.InvitesBySender(acc.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("list the account's invitations: %w", err)
	}
	if acc.Email != "" {
		toMe, err := p.owner.OffersByEmail(acc.Email)
		if err != nil {
			return nil, nil, fmt.Errorf("list the offers made to the account: %w", err)
		}
		offers = append(offers, toMe...)
		invToMe, err := p.owner.InvitesByEmail(acc.Email)
		if err != nil {
			return nil, nil, fmt.Errorf("list the invitations made to the account: %w", err)
		}
		invites = append(invites, invToMe...)
	}
	var cancelledOffers []ownership.Offer
	seen := map[string]bool{}
	for _, o := range offers {
		if seen[o.ID] || (o.State != ownership.StateOpen && o.State != ownership.StatePaused) {
			continue
		}
		seen[o.ID] = true
		cur, err := p.owner.UpdateOffer(o.ID, func(x *ownership.Offer) error {
			if x.State != ownership.StateOpen && x.State != ownership.StatePaused {
				return errSkipRecord
			}
			x.State = endedState(x.From, acc)
			return nil
		})
		switch {
		case errors.Is(err, errSkipRecord), errors.Is(err, ownership.ErrNotFound):
			continue
		case err != nil:
			return nil, nil, fmt.Errorf("cancel offer %s: %w", o.ID, err)
		}
		cancelledOffers = append(cancelledOffers, cur)
	}
	var cancelledInvites []ownership.Invite
	seen = map[string]bool{}
	for _, i := range invites {
		if seen[i.ID] || i.State != ownership.StateOpen {
			continue
		}
		seen[i.ID] = true
		cur, err := p.owner.UpdateInvite(i.ID, func(x *ownership.Invite) error {
			if x.State != ownership.StateOpen {
				return errSkipRecord
			}
			x.State = endedState(x.From, acc)
			return nil
		})
		switch {
		case errors.Is(err, errSkipRecord), errors.Is(err, ownership.ErrNotFound):
			continue
		case err != nil:
			return nil, nil, fmt.Errorf("cancel invitation %s: %w", i.ID, err)
		}
		cancelledInvites = append(cancelledInvites, cur)
	}
	return cancelledOffers, cancelledInvites, nil
}

// endedState is the final state an account's deletion gives a record made
// by from: cancelled for the account's own, expired for one made to it.
func endedState(from string, deleted *account.Account) string {
	if from == deleted.ID {
		return ownership.StateCancelled
	}
	return ownership.StateExpired
}

// mailOfferCancelled tells the recipient of an offer the deleted account
// made that it was cancelled, if they were ever told of the offer.
func (p *provider) mailOfferCancelled(o ownership.Offer, deleted *account.Account) {
	if o.NotifiedAt == nil {
		return
	}
	subject, body := p.transferCancelledMail(o, deleted.Email, false)
	p.mailVerified(o.ToEmail, subject, body)
}

// mailOfferEnded tells the sender of an offer made to the deleted account
// that it ended unaccepted, in the words of the expiry mail: nothing in it
// says that an account existed for the address. The deleted account, the
// recipient, is told nothing.
func (p *provider) mailOfferEnded(o ownership.Offer) {
	senderEmail := p.emailOf(o.From)
	subject, body := p.transferExpiredMail(o, senderEmail, true)
	p.mailVerified(senderEmail, subject, body)
}

// mailInviteCancelled tells the invitee of an invitation the deleted account
// made that it was cancelled.
func (p *provider) mailInviteCancelled(inv ownership.Invite, deleted *account.Account) {
	subject, body := p.inviteCancelledMail(inv, deleted.Email, false)
	p.mailVerified(inv.ToEmail, subject, body)
}

// deletedSite is a site an account's deletion removed, with the admins it
// had, for the cleanup after accounts.Delete.
type deletedSite struct {
	site, label string
	admins      []ext.SiteAdmin
}

// deleteAccountAndSites deletes the account and every site it owns
// (accounts.Delete with deleteOwnedSiteEnding), then cleans up after the
// admins of the sites that went: their share markers, their own tags for
// the site, a line in their log and a mail. That cleanup takes each admin's
// account lock, so it runs only once accounts.Delete has returned: its
// callback holds the deleted account's lock, and two deletions of accounts
// that administer each other's sites would otherwise wait on each other
// forever. Sites deleted before an error are cleaned up all the same.
func (p *provider) deleteAccountAndSites(acc *account.Account) (sites int, err error) {
	var gone []deletedSite
	err = p.accounts.Delete(acc, func(viewID string) error {
		ds, deleted, err := p.deleteOwnedSiteEnding(acc, viewID)
		if deleted {
			sites++
			gone = append(gone, ds)
		}
		return err
	})
	for _, ds := range gone {
		p.endAdminsOfDeletedSite(acc, ds)
	}
	return sites, err
}

// deleteOwnedSiteEnding is accounts.Delete's per-site callback: the site is
// deleted as deleteOwnedSite does, and the admins it had are returned for
// endAdminsOfDeletedSite. The admins are read before the delete (they go
// with the site); a read that fails only leaves the markers to the
// background reconciliation. It takes no other account's lock.
func (p *provider) deleteOwnedSiteEnding(acc *account.Account, viewID string) (deletedSite, bool, error) {
	ds := deletedSite{site: viewID}
	if p.owner != nil {
		var err error
		if ds.admins, err = p.siteAdmins(viewID); err != nil && !errors.Is(err, ext.ErrSiteGone) {
			slog.Error("account deletion: could not read a site's admins; the reconciliation will unlink them", "site", viewID, "err", err)
		}
		if len(ds.admins) > 0 {
			ds.label = p.labelOf(viewID)
		}
	}
	deleted, err := p.deleteOwnedSite(acc, viewID)
	if err != nil || !deleted {
		return deletedSite{}, deleted, err
	}
	return ds, true, nil
}

// endAdminsOfDeletedSite drops the former admins' markers and own tags for a
// deleted site and tells them. It runs outside every account lock.
func (p *provider) endAdminsOfDeletedSite(acc *account.Account, ds deletedSite) {
	for _, a := range ds.admins {
		if err := p.owner.UnlinkShare(a.Account, ds.site); err != nil {
			slog.Error("account deletion: could not drop a former admin's share marker", "site", ds.site, "account", a.Account, "err", err)
		}
		if err := p.dropAdminTags(a.Account, ds.site); err != nil {
			slog.Error("account deletion: could not drop a former admin's tags", "site", ds.site, "account", a.Account, "err", err)
		}
		p.RecordAccountProvenance(a.Account, provenance.Entry{
			Time: time.Now().UTC(), Action: provenance.ActionAdmins, Surface: provenance.SurfaceBackground, Auth: provenance.AuthNone,
			Account: a.Account, Site: ds.site, Detail: "no longer an admin of the site: the site was deleted",
		})
		adminEmail := p.emailOf(a.Account)
		subject, body := p.shareGoneMail(ds.label, acc.Email, adminEmail, false)
		p.mailVerified(adminEmail, subject, body)
	}
}
