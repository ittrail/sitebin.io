//go:build ee

package ee

import (
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/ownership"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// The background loop of site ownership: the work no request does. Read
// docs/superpowers/specs/2026-11-03-site-ownership-design.md, "Decline,
// expiry, cancellation" and "Sharing", "Expiry".
//
// What this file must keep true:
//   - An error is an unknown, never a verdict. A read that fails leaves the
//     record, the marker or the tag exactly where it is; the next tick asks
//     again. Nothing here deletes a site, a file or an offer's sites.
//   - An accepting offer is ended by completeAccepting and by nothing else:
//     the loop never expires or cancels one, and never reads a run's Done
//     after an error.
//   - Pauses (the sender's plan, a lock, a suspension) are COMPUTED by
//     offerPause and manages; the loop stores none. Only a host conflict,
//     which an accept found, is a stored `paused`, and the loop reopens it.
//   - Every state change is decided inside the store's own locked update
//     (UpdateOffer, UpdateInvite), so an accept, a cancel or a decline that
//     lands at the same moment wins or loses cleanly, and the mail goes out
//     only for the change this tick made.
//   - The store's own locked callbacks (UpdateOffer, UpdateInvite) and the
//     seam's SetAdmins edit function do slice and state work only and call
//     nothing back. tickMu serialises ticks and is private to the loop: no
//     request path or seam call ever takes it, so holding it across the
//     tick's seam calls cannot deadlock anything.
//   - A marker is only ever removed after a fresh read of that one site
//     shows it wrong AND the marker is older than markerGrace: the
//     recipient's marker is linked before meta.json changes owner, and a
//     share marker before the admin entry is written.

const (
	// ownershipTickEvery is how often the loop runs; the first tick comes
	// ownershipFirstTick after the start, when the host is surely ready.
	ownershipTickEvery = 5 * time.Minute
	ownershipFirstTick = time.Minute
	// reconcileEvery is how often the markers are reconciled with meta.json.
	reconcileEvery = time.Hour
	// markerGrace is how old a marker must be before it can be called stray:
	// an invitation being accepted links the share marker a moment before it
	// writes the admin entry, and a transfer links the recipient's marker
	// before meta.json changes owner.
	markerGrace = 10 * time.Minute
	// logEvery is the most often one lasting error is logged for one subject.
	logEvery = time.Hour
)

var (
	errOfferMoved = errors.New("the offer changed meanwhile")
)

// startOwnershipLoop runs the loop until stopOwnershipLoop (tests); the
// running server keeps it for the process lifetime, like the licence
// refresh. It does nothing without accounts or without the stores.
func (p *provider) startOwnershipLoop() {
	if p.owner == nil || p.accounts == nil || !p.cfg.Enabled() || p.loopStop != nil {
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	p.loopStop, p.loopDone = stop, done
	go func() {
		defer close(done)
		timer := time.NewTimer(ownershipFirstTick)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-timer.C:
				p.safeTick()
				timer.Reset(ownershipTickEvery)
			}
		}
	}()
}

// stopOwnershipLoop ends the loop. Safe without a loop, and safe twice.
func (p *provider) stopOwnershipLoop() {
	if p == nil {
		return
	}
	p.loopStopOnce.Do(func() {
		if p.loopStop != nil {
			close(p.loopStop)
		}
	})
}

// safeTick is one tick (each step recovers on its own, see step).
func (p *provider) safeTick() { p.ownershipTick(time.Now()) }

// step runs one step of a tick so that a panic in it is logged and the other
// steps still run: a repeating panic in one step must not stop the rest.
func (p *provider) step(name string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("ownership: a step of the background loop panicked; the other steps go on", "step", name, "panic", r)
		}
	}()
	f()
}

// ownershipTick is one step of the loop at time now, callable directly. The
// order: finish what was accepted, end what lapsed, reopen what a conflict
// paused, tell the recipients whose offer is due, lapse invitations, end
// admins whose date has passed, and (hourly) reconcile the markers.
// Nothing happens where Init never ran (sitebin cleanup): no store, no site
// service.
func (p *provider) ownershipTick(now time.Time) {
	if p == nil || p.owner == nil || p.accounts == nil || p.host == nil || p.host.Sites() == nil {
		return
	}
	p.tickMu.Lock()
	defer p.tickMu.Unlock()
	p.pruneLoopLog(now)

	p.step("accepting", func() { p.finishAccepting(now) })
	p.step("expire-offers", func() { p.expireOffers(now) })
	p.step("reopen", func() { p.reopenPaused(now) })
	p.step("notify", func() { p.notifyDue(now) })
	p.step("invites", func() { p.lapseInvites(now) })
	p.step("admins", func() { p.expireAdmins(now) })
	if p.lastReconcile.IsZero() || now.Sub(p.lastReconcile) >= reconcileEvery {
		p.lastReconcile = now
		p.step("reconcile", func() { p.reconcileMarkers(now) })
	}
}

// ---- logging ----

// loopLog logs an error at most once per logEvery for one key (an offer, a
// step): a fault that lasts must not fill the log every five minutes.
func (p *provider) loopLog(key string, now time.Time, msg string, args ...any) {
	if last, ok := p.loopLogged[key]; ok && now.Sub(last) < logEvery {
		return
	}
	if p.loopLogged == nil {
		p.loopLogged = map[string]time.Time{}
	}
	p.loopLogged[key] = now
	slog.Error(msg, args...)
}

func (p *provider) pruneLoopLog(now time.Time) {
	for k, at := range p.loopLogged {
		if now.Sub(at) >= logEvery {
			delete(p.loopLogged, k)
		}
	}
}

// ---- offers ----

// openOffers lists the offers that are not final; ok is false (and the step
// is skipped) when they cannot be read.
func (p *provider) openOffers(now time.Time, step string) ([]ownership.Offer, bool) {
	offers, err := p.owner.OpenOffers()
	if err != nil {
		p.loopLog("list-offers:"+step, now, "ownership: could not list the offers", "step", step, "err", err)
		return nil, false
	}
	return offers, true
}

// finishAccepting completes every accepting offer through completeAccepting,
// the one forward completion. A busy claim (the accept or another run is
// moving it) and a state that changed meanwhile are skipped; any other error
// means "try again later" and is logged at most once an hour per offer. The
// run's in-memory result is not read: on an error nothing is acted on, and
// on success completeAccepting has already recorded and mailed everything.
func (p *provider) finishAccepting(now time.Time) {
	offers, ok := p.openOffers(now, "accepting")
	if !ok {
		return
	}
	for _, o := range offers {
		if o.State != ownership.StateAccepting {
			continue
		}
		if o.AcceptedBy == "" {
			p.loopLog("accept:"+o.ID, now, "ownership: an accepting offer names no recipient account", "offer", o.ID)
			continue
		}
		to, err := p.accounts.ByID(o.AcceptedBy)
		switch {
		case errors.Is(err, account.ErrNotFound):
			// The re-check refuses a recipient that is gone, and the sites
			// stay with the sender.
			to = &account.Account{ID: o.AcceptedBy}
		case err != nil:
			p.loopLog("accept:"+o.ID, now, "ownership: could not complete an accepting offer", "offer", o.ID, "err", err)
			continue
		}
		by := ext.ChangeBy{Account: to.ID, Surface: provenance.SurfaceBackground}
		if _, err := p.completeAccepting(o, to, by); err != nil &&
			!errors.Is(err, errAcceptBusy) && !errors.Is(err, errNotAccepting) {
			p.loopLog("accept:"+o.ID, now, "ownership: could not complete an accepting offer", "offer", o.ID, "err", err)
		}
	}
}

// expireOffers ends open and paused offers whose accept-by date has passed
// (the date keeps running while an offer is paused) and mails both sides. An
// accepting offer is never touched: the state is re-checked in the store's
// own update.
func (p *provider) expireOffers(now time.Time) {
	offers, ok := p.openOffers(now, "expire")
	if !ok {
		return
	}
	for _, o := range offers {
		if (o.State != ownership.StateOpen && o.State != ownership.StatePaused) || !now.After(o.AcceptBy) {
			continue
		}
		cur, err := p.owner.UpdateOffer(o.ID, func(x *ownership.Offer) error {
			if (x.State != ownership.StateOpen && x.State != ownership.StatePaused) || !now.After(x.AcceptBy) {
				return errOfferMoved
			}
			x.State = ownership.StateExpired
			return nil
		})
		if err != nil {
			if !errors.Is(err, errOfferMoved) {
				p.loopLog("expire:"+o.ID, now, "ownership: could not expire an offer", "offer", o.ID, "err", err)
			}
			continue
		}
		slog.Info("transfer offer expired", "offer", cur.ID)
		p.mailTransferExpired(cur)
	}
}

// reopenPaused sets an offer the accept paused for a host conflict back to
// open once the conflict is gone and nothing else pauses it (offerPause).
func (p *provider) reopenPaused(now time.Time) {
	offers, ok := p.openOffers(now, "reopen")
	if !ok {
		return
	}
	hc, hasHC := p.host.Sites().(hostConflictReader)
	for _, o := range offers {
		if o.State != ownership.StatePaused || now.After(o.AcceptBy) || !hasHC {
			continue
		}
		reason, err := p.offerPause(o)
		if err != nil {
			p.loopLog("pause:"+o.ID, now, "ownership: could not tell whether an offer is paused", "offer", o.ID, "err", err)
			continue
		}
		if reason != "" {
			continue
		}
		conflicts, err := hc.HostConflicts(o.Sites, o.From)
		if err != nil {
			// Unknown: the offer stays paused.
			p.loopLog("conflicts:"+o.ID, now, "ownership: could not read the host conflicts of a paused offer", "offer", o.ID, "err", err)
			continue
		}
		if len(conflicts) > 0 {
			continue
		}
		if _, err := p.owner.UpdateOffer(o.ID, func(x *ownership.Offer) error {
			if x.State != ownership.StatePaused {
				return errOfferMoved
			}
			x.State = ownership.StateOpen
			return nil
		}); err != nil {
			if !errors.Is(err, errOfferMoved) {
				p.loopLog("reopen:"+o.ID, now, "ownership: could not reopen an offer", "offer", o.ID, "err", err)
			}
			continue
		}
		slog.Info("transfer offer reopened", "offer", o.ID)
	}
}

// notifyDue mails the recipient of every open offer that is due and was never
// mailed: available-from passed or absent (a sender may have moved it into
// the past), within accept-by, and not paused. While paused nothing is sent;
// the mail goes out once the pause lifts. notifyOffer marks NotifiedAt before
// it mails, so no offer is mailed twice.
func (p *provider) notifyDue(now time.Time) {
	offers, ok := p.openOffers(now, "notify")
	if !ok {
		return
	}
	for _, o := range offers {
		if o.State != ownership.StateOpen || o.NotifiedAt != nil || now.After(o.AcceptBy) ||
			(o.AvailableFrom != nil && now.Before(*o.AvailableFrom)) {
			continue
		}
		reason, err := p.offerPause(o)
		if err != nil {
			p.loopLog("pause:"+o.ID, now, "ownership: could not tell whether an offer is paused", "offer", o.ID, "err", err)
			continue
		}
		if reason != "" {
			continue
		}
		p.notifyOfferAt(o, now)
	}
}

// ---- invitations ----

// lapseInvites expires open invitations older than inviteLapse, silently.
func (p *provider) lapseInvites(now time.Time) {
	invs, err := p.owner.OpenInvites()
	if err != nil {
		p.loopLog("list-invites", now, "ownership: could not list the invitations", "err", err)
		return
	}
	for _, inv := range invs {
		if inv.State != ownership.StateOpen || !inviteLapsed(inv, now) {
			continue
		}
		if _, err := p.owner.UpdateInvite(inv.ID, func(x *ownership.Invite) error {
			if x.State != ownership.StateOpen || !inviteLapsed(*x, now) {
				return errInviteNotOpen
			}
			x.State = ownership.StateExpired
			return nil
		}); err != nil && !errors.Is(err, errInviteNotOpen) {
			p.loopLog("lapse:"+inv.ID, now, "ownership: could not lapse an invitation", "invite", inv.ID, "err", err)
		}
	}
}

// ---- admin expiry ----

// expiredAdmin is an admin entry for the account whose end date has passed:
// the same test as the core's (store.Meta.LiveAdmin) turned around.
func expiredAdmin(a ext.SiteAdmin, accountID string, now time.Time) bool {
	return a.Account == accountID && a.Until != nil && !now.Before(*a.Until)
}

// expireAdmins ends the shares whose end date has passed. The sites with
// admins are found through the share markers (one folder per sharing
// account), not by walking every site.
func (p *provider) expireAdmins(now time.Time) {
	accs, err := p.owner.SharingAccounts()
	if err != nil {
		p.loopLog("list-sharers", now, "ownership: could not list the sharing accounts", "err", err)
		return
	}
	for _, accID := range accs {
		sites, err := p.owner.SharedSites(accID)
		if err != nil {
			p.loopLog("list-shares:"+accID, now, "ownership: could not list an account's shares", "account", accID, "err", err)
			continue
		}
		for _, viewID := range sites {
			p.expireAdmin(now, accID, viewID)
		}
	}
}

// expireAdmin removes one account's expired entry from one site: SetAdmins
// with the owner read just now (a site that moved is the next owner's, and
// the seam refuses), then the marker, the admin's tags, both account logs and
// the mail. A read error or a refusal changes nothing; the next tick, or the
// hourly reconciliation, picks it up.
func (p *provider) expireAdmin(now time.Time, accID, viewID string) {
	admins, err := p.siteAdmins(viewID)
	if err != nil {
		if !errors.Is(err, ext.ErrSiteGone) {
			p.loopLog("admins:"+viewID, now, "ownership: could not read a site's admins", "site", viewID, "err", err)
		}
		return
	}
	if !slices.ContainsFunc(admins, func(a ext.SiteAdmin) bool { return expiredAdmin(a, accID, now) }) {
		return
	}
	owner, err := p.siteOwner(viewID)
	if err != nil || owner == "" {
		if err != nil && !errors.Is(err, ext.ErrSiteGone) {
			p.loopLog("owner:"+viewID, now, "ownership: could not read a site's owner", "site", viewID, "err", err)
		}
		return
	}
	removed := false
	err = p.setAdmins(viewID, owner, ext.ChangeBy{Surface: provenance.SurfaceBackground},
		func(cur []ext.SiteAdmin) ([]ext.SiteAdmin, error) {
			return slices.DeleteFunc(slices.Clone(cur), func(a ext.SiteAdmin) bool {
				if expiredAdmin(a, accID, now) {
					removed = true
					return true
				}
				return false
			}), nil
		})
	switch {
	case errors.Is(err, ext.ErrNotOwner), errors.Is(err, ext.ErrSiteGone):
		return // the site moved or is gone: the next tick reconciles
	case errors.Is(err, ext.ErrSiteLocked):
		return // a locked site's admins cannot change; ask again later
	case err != nil:
		p.loopLog("expire-admin:"+viewID+":"+accID, now, "ownership: could not end an expired share", "site", viewID, "account", accID, "err", err)
		return
	case !removed:
		return // renewed meanwhile
	}
	// The marker and the tags go only if a fresh read shows the account is
	// no longer listed: a re-accept between SetAdmins and here keeps both.
	// A read error is unknown, and the reconciliation settles it later.
	if p.stillAdmin(accID, viewID) {
		slog.Info("share expired but was renewed meanwhile; its marker stays", "site", viewID, "account", accID)
	} else {
		if err := p.owner.UnlinkShare(accID, viewID); err != nil {
			slog.Error("share: could not drop a share marker", "site", viewID, "account", accID, "err", err)
		}
		if err := p.dropAdminTags(accID, viewID); err != nil {
			slog.Error("share: could not drop an admin's tags", "site", viewID, "account", accID, "err", err)
		}
	}
	e := provenance.Entry{
		Time: time.Now().UTC(), Action: provenance.ActionAdmins,
		Surface: provenance.SurfaceBackground, Auth: provenance.AuthNone, Site: viewID,
	}
	ownerLog, adminLog := e, e
	ownerLog.Detail = "a share on the site ended on its end date"
	adminLog.Detail = "no longer an admin of the site (its end date passed)"
	p.RecordAccountProvenance(owner, ownerLog)
	if accID != owner {
		p.RecordAccountProvenance(accID, adminLog)
	}
	slog.Info("share expired", "site", viewID, "account", accID)
	if p.mailer == nil {
		return
	}
	ownerEmail, adminEmail := p.emailOf(owner), p.emailOf(accID)
	subject, body := p.shareExpiredMail(viewID, ownerEmail, adminEmail)
	p.mailVerified(ownerEmail, subject, body)
	p.mailVerified(adminEmail, subject, body)
}

// ---- reconciliation ----

type siteOwnersLister interface {
	SiteOwners() (map[string]ext.SiteOwnership, error)
}

// reconcileMarkers brings the markers back in line with meta.json, which is
// the truth. It only ever removes a marker it has positively shown to be
// wrong by a fresh read of that one site, and only once the marker is older
// than markerGrace; an unreadable site, an incomplete walk or a failed
// listing leaves the marker alone.
func (p *provider) reconcileMarkers(now time.Time) {
	accIDs, err := p.accounts.ListIDs()
	if err != nil {
		p.loopLog("list-accounts", now, "ownership: could not list the accounts", "err", err)
		return
	}
	var owners map[string]ext.SiteOwnership
	if lister, ok := p.host.Sites().(siteOwnersLister); ok {
		var werr error
		owners, werr = lister.SiteOwners()
		if werr != nil {
			// The list is partial or missing: what it holds is still true,
			// but absence from it proves nothing. Every verdict is a fresh
			// read of the one site anyway.
			p.loopLog("walk", now, "ownership: could not read every site's owner; the markers are reconciled only where the reads were sure", "err", werr)
		}
	}
	p.reconcileIndex(now, accIDs, owners)
	p.reconcileShares(now, accIDs, owners)
}

// heldByAcceptance reports whether the account is the recipient of an
// accepting offer holding the site: its marker is linked ahead of the move
// and must stay until meta.json says so. Read fresh; a read error counts as
// held.
func (p *provider) heldByAcceptance(accID, viewID string) bool {
	offers, err := p.owner.OpenOffers()
	if err != nil {
		return true
	}
	return slices.ContainsFunc(offers, func(o ownership.Offer) bool {
		return o.State == ownership.StateAccepting && o.AcceptedBy == accID && slices.Contains(o.Sites, viewID)
	})
}

// reconcileIndex reconciles the account index markers both ways: a marker
// whose site belongs to another account, or whose site is gone, is unlinked;
// a site whose meta names an owner with no marker gets one (LinkSite can fail
// after an adopt or a transfer). An ownerless site names nobody, so its
// markers are left alone and none is made. The walk only nominates
// candidates; a candidate is acted on after the grace, after the accepting
// offers are ruled out, and after a fresh read of that one site, which
// unlinks only on "gone" or on another named owner.
func (p *provider) reconcileIndex(now time.Time, accIDs []string, owners map[string]ext.SiteOwnership) {
	known := map[string]bool{} // accounts whose markers were listed
	linked := map[string]map[string]bool{}
	for _, id := range accIDs {
		acc, err := p.accounts.ByID(id)
		if err != nil {
			continue
		}
		markers, err := p.accounts.SiteMarkers(acc)
		if err != nil {
			p.loopLog("markers:"+id, now, "ownership: could not list an account's site markers", "account", id, "err", err)
			continue
		}
		known[id] = true
		linked[id] = map[string]bool{}
		for _, m := range markers {
			linked[id][m.Site] = true
			if so, listed := owners[m.Site]; listed && (so.Owner == "" || so.Owner == id) {
				continue
			}
			if now.Sub(m.At) < markerGrace || p.heldByAcceptance(id, m.Site) {
				continue
			}
			cur, err := p.siteOwner(m.Site)
			if !errors.Is(err, ext.ErrSiteGone) && (err != nil || cur == "" || cur == id) {
				continue
			}
			if err := p.accounts.UnlinkSite(acc, m.Site); err != nil {
				p.loopLog("unlink:"+id+":"+m.Site, now, "ownership: could not unlink a stray site marker", "account", id, "site", m.Site, "err", err)
				continue
			}
			delete(linked[id], m.Site)
			slog.Info("ownership: unlinked a stray site marker", "account", id, "site", m.Site)
		}
	}
	for site, so := range owners {
		if so.Owner == "" || !known[so.Owner] || linked[so.Owner][site] {
			continue
		}
		if cur, err := p.siteOwner(site); err != nil || cur != so.Owner {
			continue
		}
		acc, err := p.accounts.ByID(so.Owner)
		if err != nil {
			continue
		}
		if err := p.accounts.LinkSite(acc, site); err != nil {
			p.loopLog("link:"+so.Owner+":"+site, now, "ownership: could not link a missing site marker", "account", so.Owner, "site", site, "err", err)
			continue
		}
		slog.Info("ownership: linked a missing site marker", "account", so.Owner, "site", site)
	}
}

// reconcileShares reconciles the share markers both ways against meta.json's
// admins: a marker whose site is gone or no longer lists the account is
// unlinked (after the grace), a listed admin with no marker gets one, and an
// account's registry tags for sites it no longer administers are pruned. A
// site whose admins cannot be read is left alone.
func (p *provider) reconcileShares(now time.Time, accIDs []string, owners map[string]ext.SiteOwnership) {
	accSet := map[string]bool{}
	for _, id := range accIDs {
		accSet[id] = true
	}
	sharers, err := p.owner.SharingAccounts()
	listedOK := err == nil
	if err != nil {
		p.loopLog("list-sharers", now, "ownership: could not list the sharing accounts", "err", err)
	}
	marked := map[string]map[string]bool{} // accounts whose share markers were listed
	for _, id := range sharers {
		markers, err := p.owner.ShareMarkers(id)
		if err != nil {
			p.loopLog("share-markers:"+id, now, "ownership: could not list an account's share markers", "account", id, "err", err)
			listedOK = false
			continue
		}
		marked[id] = map[string]bool{}
		for _, m := range markers {
			marked[id][m.Site] = true
			if now.Sub(m.At) < markerGrace || p.stillAdmin(id, m.Site) {
				continue
			}
			if err := p.owner.UnlinkShare(id, m.Site); err != nil {
				p.loopLog("unlink-share:"+id+":"+m.Site, now, "ownership: could not unlink a stray share marker", "account", id, "site", m.Site, "err", err)
				continue
			}
			delete(marked[id], m.Site)
			slog.Info("ownership: unlinked a stray share marker", "account", id, "site", m.Site)
		}
	}
	if listedOK {
		for site, so := range owners {
			for _, adm := range so.Admins {
				// An account with no share folder has no markers at all.
				if !accSet[adm] || marked[adm][site] {
					continue
				}
				admins, err := p.siteAdmins(site)
				if err != nil || !listsAccount(admins, adm) {
					continue
				}
				if err := p.owner.LinkShare(adm, site); err != nil {
					p.loopLog("link-share:"+adm+":"+site, now, "ownership: could not link a missing share marker", "account", adm, "site", site, "err", err)
					continue
				}
				slog.Info("ownership: linked a missing share marker", "account", adm, "site", site)
			}
		}
	}
	for _, id := range accIDs {
		reg, err := p.accounts.Tags(id)
		if err != nil || len(reg.Sites) == 0 {
			continue
		}
		var stray []string
		for site := range reg.Sites {
			if !p.stillAdmin(id, site) {
				stray = append(stray, site)
			}
		}
		if len(stray) == 0 {
			continue
		}
		if err := p.accounts.UpdateTags(id, func(r *account.TagRegistry) error {
			changed := false
			for _, s := range stray {
				if _, ok := r.Sites[s]; ok {
					delete(r.Sites, s)
					changed = true
				}
			}
			if !changed {
				return errTagsUnchanged
			}
			return nil
		}); err != nil && !errors.Is(err, errTagsUnchanged) && !errors.Is(err, account.ErrNotFound) {
			p.loopLog("prune-tags:"+id, now, "ownership: could not prune an account's admin tags", "account", id, "err", err)
		}
	}
}

// stillAdmin is false only when the site's meta.json positively says the
// account is not one of its admins (or the site is gone). An unreadable site
// counts as still an admin: nothing is removed on an unknown.
func (p *provider) stillAdmin(accID, viewID string) bool {
	admins, err := p.siteAdmins(viewID)
	switch {
	case errors.Is(err, ext.ErrSiteGone):
		return false
	case err != nil:
		return true
	}
	return listsAccount(admins, accID)
}
