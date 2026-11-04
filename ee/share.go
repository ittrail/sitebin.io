//go:build ee

package ee

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/ownership"
	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Sharing a site with an admin: an owner invites another account by email to
// manage ONE site, optionally until a date; the invitee accepts on their
// dashboard; from then on the core's gate (accountStanding) treats them as an
// admin. Either side can end it. Read
// docs/superpowers/specs/2026-11-03-site-ownership-design.md, "Sharing".
//
// What this file must keep true:
//   - The answer to an invite never depends on whether anyone owns the
//     address: same text, same status, and the mail lookup runs off the
//     request. The owner may be told specifics only about their OWN data.
//   - The owner is the owner by the site's meta.json, never by a marker; an
//     admin can never invite, end someone else's share, rotate, delete,
//     set an expiry or transfer.
//   - An invitation is accepted only by the account whose VERIFIED address it
//     names, only while its inviter still owns the site, and never on a
//     locked site. A refusal changes nothing.
//   - The seam's SetAdmins runs under the site lock and its edit function
//     calls nothing back; every other step (marker, logs, mail) comes after.
//   - Plain POST forms with CSRF, no script.

const (
	// inviteLapse is how long an invitation can be accepted.
	inviteLapse = 30 * 24 * time.Hour

	// shareAnswer is what the owner is told after inviting, whoever the
	// address belongs to. It is the transfer's text.
	shareAnswer = transferAnswer

	inviteUnavailable = "This invitation is not available right now."
	inviteGone        = "This invitation is no longer available."
	msgSiteLockedNow  = "This site is locked by the operator, so its admins cannot be changed right now."
)

func (p *provider) shareRoutes(routes map[string]http.Handler) {
	routes["POST /account/sites/{id}/share"] = http.HandlerFunc(p.handleShareInvite)
	routes["POST /account/sites/{id}/share/{account}/end"] = http.HandlerFunc(p.handleShareEnd)
	routes["POST /account/sites/{id}/leave"] = http.HandlerFunc(p.handleShareLeave)
	routes["POST /account/invites/{id}/accept"] = http.HandlerFunc(p.handleInviteAccept)
	routes["POST /account/invites/{id}/decline"] = http.HandlerFunc(p.handleInviteDecline)
	routes["POST /account/invites/{id}/cancel"] = http.HandlerFunc(p.handleInviteCancel)
}

var errInviteNotOpen = errors.New("the invitation is not open")

// siteAdminSetter is the core's ext.OwnershipSites.SetAdmins, asserted on its
// own like the other narrow seams.
type siteAdminSetter interface {
	SetAdmins(viewID, expectedOwner string, by ext.ChangeBy, edit func([]ext.SiteAdmin) ([]ext.SiteAdmin, error)) error
}

func (p *provider) setAdmins(viewID, expectedOwner string, by ext.ChangeBy, edit func([]ext.SiteAdmin) ([]ext.SiteAdmin, error)) error {
	s, ok := p.host.Sites().(siteAdminSetter)
	if !ok {
		return errors.New("the site service cannot change a site's admins")
	}
	return s.SetAdmins(viewID, expectedOwner, by, edit)
}

// siteLocked is the site's lock, from meta.json, through the core's optional
// ext.OwnershipSites.
func (p *provider) siteLocked(viewID string) (bool, error) {
	r, ok := p.host.Sites().(siteLockReader)
	if !ok {
		return false, errors.New("the site service cannot report a site's lock")
	}
	return r.SiteLocked(viewID)
}

// changeBy is the acting account, address and client of a request, for the
// site's log.
func changeBy(r *http.Request, acc *account.Account) ext.ChangeBy {
	return ext.ChangeBy{Account: acc.ID, IP: auth.ClientIP(r), UserAgent: r.UserAgent()}
}

// ---- who manages a site ----

// manages says how the account stands on the site, for the dashboard's own
// actions (rename, tags): owner is true for the site's owner, admin for an
// account the site is shared with. It is the SAME rule as the core's
// accountStanding (internal/httpapi/server.go): meta.json lists the account
// as an admin with no end date or one ahead, AND the plans of both the owner
// and the admin share. A plan that cannot be read is not a pass. Rotate and
// delete stay owner-only (owns) whatever this says.
func (p *provider) manages(acc *account.Account, viewID string) (owner, admin bool) {
	if acc == nil {
		return false, false
	}
	if p.owns(acc, viewID) {
		return true, false
	}
	ownerID, err := p.siteOwner(viewID)
	if err != nil || ownerID == "" || ownerID == acc.ID {
		return false, false
	}
	admins, err := p.siteAdmins(viewID)
	if err != nil {
		return false, false
	}
	now := time.Now()
	if !slices.ContainsFunc(admins, func(a ext.SiteAdmin) bool {
		return a.Account == acc.ID && (a.Until == nil || a.Until.After(now))
	}) {
		return false, false
	}
	for _, id := range []string{ownerID, acc.ID} {
		if ok, err := p.SharingAllowed(id); err != nil || !ok {
			return false, false
		}
	}
	return false, true
}

// ownsByMeta reports whether the site's meta.json names the account as its
// owner: the sharing routes' only test of ownership.
func (p *provider) ownsByMeta(acc *account.Account, viewID string) bool {
	owner, err := p.siteOwner(viewID)
	return err == nil && owner != "" && owner == acc.ID
}

// isInvitee reports whether acc is the invitation's recipient: its address is
// verified and, normalised, equals the invited one; and it is not the
// inviter.
func isInvitee(acc *account.Account, inv ownership.Invite) bool {
	return acc != nil && acc.EmailVerified && acc.ID != inv.From &&
		ownership.NormEmail(acc.Email) == inv.ToEmail
}

// inviteLapsed reports whether the invitation is too old to accept.
func inviteLapsed(inv ownership.Invite, now time.Time) bool {
	return now.After(inv.Created.Add(inviteLapse))
}

// liveInvite is an open invitation that has not lapsed.
func liveInvite(inv ownership.Invite, now time.Time) bool {
	return inv.State == ownership.StateOpen && !inviteLapsed(inv, now)
}

// ---- answers ----

func (p *provider) renderShareMessage(w http.ResponseWriter, status int, title, body string) {
	p.securityHeaders(w)
	w.WriteHeader(status)
	msgTmpl.Execute(w, msgView{Title: title, Body: body, Back: "/account"})
}

func (p *provider) renderShareRetry(w http.ResponseWriter) {
	p.renderShareMessage(w, http.StatusServiceUnavailable, "Could not check that right now", "Nothing was changed. Please try again in a minute.")
}

func (p *provider) renderSharePlan(w http.ResponseWriter, title, body string) {
	p.securityHeaders(w)
	w.WriteHeader(http.StatusForbidden)
	sharePlanTmpl.Execute(w, msgView{Title: title, Body: body})
}

// renderShareRefusal answers a refusal of the owner's own request.
func (p *provider) renderShareRefusal(w http.ResponseWriter, ref *refusal) {
	switch {
	case ref.plan:
		p.renderSharePlan(w, "Sharing needs Pro or Studio", "Sharing a site with another account is part of the Pro and Studio plans.")
	case ref.retry:
		p.renderShareRetry(w)
	default:
		p.renderShareMessage(w, ref.status, "The invitation was not sent", ref.msg)
	}
}

// parseShareUntil reads the optional end date (YYYY-MM-DD, UTC): the share
// ends at the close of that day. A message is returned for a date that is not
// one or is not ahead.
func parseShareUntil(raw string, now time.Time) (*time.Time, string) {
	if raw == "" {
		return nil, ""
	}
	day, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, "The end date is not a valid date."
	}
	end := day.Add(24*time.Hour - time.Second)
	if !end.After(now) {
		return nil, "The end date must be in the future."
	}
	return &end, ""
}

// ---- inviting ----

func (p *provider) handleShareInvite(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.transferPost(w, r)
	if !ok {
		return
	}
	viewID := r.PathValue("id")
	// An admin or a stranger finds no such route: only the owner invites.
	if !p.ownsByMeta(acc, viewID) {
		http.NotFound(w, r)
		return
	}
	if ref := p.sharingOK(acc); ref != nil {
		p.renderShareRefusal(w, ref)
		return
	}
	email, ok := recipientEmail(r.PostFormValue("email"))
	if !ok {
		p.renderShareRefusal(w, refuse("Enter a valid email address."))
		return
	}
	if own, _ := recipientEmail(acc.Email); own == email {
		p.renderShareRefusal(w, refuse("You cannot share a site with your own address."))
		return
	}
	now := time.Now()
	until, msg := parseShareUntil(strings.TrimSpace(r.PostFormValue("until")), now)
	if msg != "" {
		p.renderShareRefusal(w, refuse(msg))
		return
	}
	if ref := p.canInvite(acc, viewID, email, now); ref != nil {
		p.renderShareRefusal(w, ref)
		return
	}
	inv, err := p.owner.CreateInvite(ownership.Invite{Site: viewID, From: acc.ID, ToEmail: email, Until: until}, maxOffersPerDay)
	switch {
	case errors.Is(err, ownership.ErrDailyLimit):
		p.renderShareRefusal(w, refuse(dailyLimitText))
		return
	case err != nil:
		slog.Error("share: could not create an invitation", "account", acc.ID, "site", viewID, "err", err)
		p.renderShareRetry(w)
		return
	}
	// Mailed off the request: the lookup of the address runs in the
	// goroutine, so the answer cannot depend on it.
	subject, body := p.inviteMail(inv, acc.Email)
	p.mailInBackground(inv.ToEmail, subject, body)
	p.renderShareMessage(w, http.StatusOK, "Invitation sent", shareAnswer)
}

// canInvite applies the rules about the owner's own data: the site is not
// locked, not the operator's, and has room for another admin.
func (p *provider) canInvite(acc *account.Account, viewID, email string, now time.Time) *refusal {
	locked, err := p.siteLocked(viewID)
	if err != nil {
		slog.Error("share: could not read a site's lock", "site", viewID, "err", err)
		return &refusal{retry: true}
	}
	if locked {
		return refuse("This site is locked by the operator and cannot be shared.")
	}
	if p.IsOperator(acc.ID) {
		return refuse("Sites owned by the operator account cannot be shared")
	}
	admins, err := p.siteAdmins(viewID)
	if err != nil {
		slog.Error("share: could not read a site's admins", "site", viewID, "err", err)
		return &refusal{retry: true}
	}
	invs, err := p.owner.InvitesBySite(viewID)
	if err != nil {
		slog.Error("share: could not read a site's invitations", "site", viewID, "err", err)
		return &refusal{retry: true}
	}
	open := 0
	for _, inv := range invs {
		if !liveInvite(inv, now) {
			continue
		}
		open++
		if inv.ToEmail == email {
			return refuse("There is already an open invitation for this address.")
		}
	}
	// Already an admin: the owner sees the admins' addresses anyway, so
	// this reveals nothing. Changing the end date means ending the share.
	if existing, err := p.accounts.ByEmail(email); err == nil && existing != nil && listsAccount(admins, existing.ID) {
		return refuse("That account is already an admin of this site. End the share first to change the date.")
	}
	if len(admins)+open >= store.MaxAdmins {
		return refuse(fmt.Sprintf("A site can have at most %d admins, counting open invitations. End a share or cancel an invitation first.", store.MaxAdmins))
	}
	return nil
}

// ---- the invitee's side ----

// recipientInvite starts the invitee's POST routes: the account, the CSRF
// token (403 before anything is looked up), and the invitation, which anyone
// but its recipient finds missing (404).
func (p *provider) recipientInvite(w http.ResponseWriter, r *http.Request) (*account.Account, ownership.Invite, bool) {
	acc, ok := p.transferPost(w, r)
	if !ok {
		return nil, ownership.Invite{}, false
	}
	inv, err := p.owner.GetInvite(r.PathValue("id"))
	if err != nil || !isInvitee(acc, inv) {
		if err != nil && !errors.Is(err, ownership.ErrNotFound) {
			slog.Error("share: could not read an invitation", "err", err)
		}
		http.NotFound(w, r)
		return nil, ownership.Invite{}, false
	}
	return acc, inv, true
}

// inviteStateText is what the invitee is told about an invitation that is not
// open.
func inviteStateText(state string) string {
	switch state {
	case ownership.StateAccepted:
		return "This invitation has already been accepted."
	case ownership.StateDeclined:
		return "This invitation was declined."
	case ownership.StateExpired:
		return "This invitation lapsed and can no longer be accepted."
	case ownership.StateCancelled:
		return "The owner withdrew this invitation."
	}
	return inviteUnavailable
}

// setInviteState moves an open invitation to state; ok is false when it was no
// longer open (or could not be written).
func (p *provider) setInviteState(id, state string) (ownership.Invite, error) {
	return p.owner.UpdateInvite(id, func(x *ownership.Invite) error {
		if x.State != ownership.StateOpen {
			return errInviteNotOpen
		}
		x.State = state
		return nil
	})
}

// endInvite closes an invitation that can never be accepted. Best effort.
func (p *provider) endInvite(id, state string) {
	if _, err := p.setInviteState(id, state); err != nil && !errors.Is(err, errInviteNotOpen) {
		slog.Error("share: could not close an invitation", "invite", id, "err", err)
	}
}

func (p *provider) handleInviteAccept(w http.ResponseWriter, r *http.Request) {
	acc, inv, ok := p.recipientInvite(w, r)
	if !ok {
		return
	}
	if inv.State != ownership.StateOpen {
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be accepted", inviteStateText(inv.State)+" Nothing was changed.")
		return
	}
	now := time.Now()
	if inviteLapsed(inv, now) {
		p.endInvite(inv.ID, ownership.StateExpired)
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be accepted", "This invitation lapsed on "+dateText(inv.Created.Add(inviteLapse))+" (UTC) and can no longer be accepted. Nothing was changed.")
		return
	}
	if inv.Until != nil && !inv.Until.After(now) {
		p.endInvite(inv.ID, ownership.StateExpired)
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be accepted", "The access this invitation offered has already ended. Nothing was changed.")
		return
	}
	// The recipient's plan, then the site's owner as it stands NOW.
	switch ok, err := p.SharingAllowed(acc.ID); {
	case err != nil:
		slog.Error("share: could not read the invitee's plan", "account", acc.ID, "err", err)
		p.renderShareRetry(w)
		return
	case !ok:
		p.renderSharePlan(w, "Your plan cannot be an admin", "Managing a site that is shared with you is part of the Pro and Studio plans. Nothing was changed.")
		return
	}
	owner, err := p.siteOwner(inv.Site)
	switch {
	case errors.Is(err, ext.ErrSiteGone):
		p.endInvite(inv.ID, ownership.StateCancelled)
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be accepted", inviteGone+" Nothing was changed.")
		return
	case err != nil:
		slog.Error("share: could not read a site's owner", "site", inv.Site, "err", err)
		p.renderShareRetry(w)
		return
	case owner != inv.From:
		// The site was transferred after the invitation: whoever owns it now
		// never invited anyone.
		p.endInvite(inv.ID, ownership.StateCancelled)
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be accepted", inviteGone+" Nothing was changed.")
		return
	}
	switch ok, err := p.SharingAllowed(owner); {
	case err != nil:
		slog.Error("share: could not read the owner's plan", "account", owner, "err", err)
		p.renderShareRetry(w)
		return
	case !ok:
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be accepted", inviteUnavailable+" Nothing was changed.")
		return
	}
	switch locked, err := p.siteLocked(inv.Site); {
	case err != nil:
		slog.Error("share: could not read a site's lock", "site", inv.Site, "err", err)
		p.renderShareRetry(w)
		return
	case locked:
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be accepted", inviteUnavailable+" Nothing was changed.")
		return
	}
	// The one state change that decides a race with a cancel or a second
	// accept; undone below if the share cannot be written.
	cur, err := p.setInviteState(inv.ID, ownership.StateAccepted)
	switch {
	case errors.Is(err, errInviteNotOpen):
		p.renderShareMessage(w, http.StatusConflict, "The invitation changed", "The invitation changed since you opened the page. Nothing was changed. Please look at it again on your dashboard.")
		return
	case err != nil:
		slog.Error("share: could not accept an invitation", "invite", inv.ID, "err", err)
		p.renderShareRetry(w)
		return
	}
	undo := func() {
		// The marker goes unless the site still lists the account (it was an
		// admin before this invitation); a read error leaves it.
		if admins, err := p.siteAdmins(inv.Site); err == nil && !listsAccount(admins, acc.ID) {
			if err := p.owner.UnlinkShare(acc.ID, inv.Site); err != nil {
				slog.Error("share: could not drop a share marker", "site", inv.Site, "account", acc.ID, "err", err)
			}
		}
		if _, err := p.owner.UpdateInvite(inv.ID, func(x *ownership.Invite) error {
			if x.State == ownership.StateAccepted {
				x.State = ownership.StateOpen
			}
			return nil
		}); err != nil {
			slog.Error("share: could not reopen an invitation", "invite", inv.ID, "err", err)
		}
	}
	// The marker first: a marker without an admin grants nothing, an admin
	// without a marker would be invisible on the dashboard.
	if err := p.owner.LinkShare(acc.ID, inv.Site); err != nil {
		slog.Error("share: could not link a share marker", "site", inv.Site, "account", acc.ID, "err", err)
		undo()
		p.renderShareRetry(w)
		return
	}
	var until *time.Time
	if cur.Until != nil {
		u := *cur.Until
		until = &u
	}
	// The core writes only while inv.From still owns the site (under the
	// site lock), so a transfer that landed after the owner check above
	// leaves nothing behind.
	err = p.setAdmins(inv.Site, inv.From, changeBy(r, acc), func(admins []ext.SiteAdmin) ([]ext.SiteAdmin, error) {
		out := slices.DeleteFunc(slices.Clone(admins), func(a ext.SiteAdmin) bool { return a.Account == acc.ID })
		return append(out, ext.SiteAdmin{Account: acc.ID, Until: until}), nil
	})
	switch {
	case errors.Is(err, ext.ErrTooManyAdmins):
		undo()
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be accepted", "This site already has the most admins it can have. Nothing was changed.")
		return
	case errors.Is(err, ext.ErrSiteLocked):
		undo()
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be accepted", inviteUnavailable+" Nothing was changed.")
		return
	case errors.Is(err, ext.ErrNotOwner):
		// Transferred after the invitation: whoever owns the site now never
		// invited anyone.
		undo()
		p.endInvite(inv.ID, ownership.StateCancelled)
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be accepted", inviteGone+" Nothing was changed.")
		return
	case errors.Is(err, ext.ErrSiteGone):
		undo()
		p.endInvite(inv.ID, ownership.StateCancelled)
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be accepted", inviteGone+" Nothing was changed.")
		return
	case err != nil:
		slog.Error("share: could not add an admin", "site", inv.Site, "account", acc.ID, "err", err)
		undo()
		p.renderShareRetry(w)
		return
	}
	p.recordShare(r, acc, inv.From, inv.Site, "became an admin through invitation "+inv.ID, "invitation "+inv.ID+" was accepted and an admin was added")
	subject, body := p.shareAcceptedMail(cur, acc.Email)
	p.mailVerified(p.emailOf(inv.From), subject, body)
	p.redirect(w, r, "/account")
}

// removeAdminOf takes an account out of a site's admins, as actor.
func (p *provider) removeAdminOf(r *http.Request, actor *account.Account, viewID, ownerID, adminID string) error {
	return p.setAdmins(viewID, ownerID, changeBy(r, actor), func(admins []ext.SiteAdmin) ([]ext.SiteAdmin, error) {
		return slices.DeleteFunc(slices.Clone(admins), func(a ext.SiteAdmin) bool { return a.Account == adminID }), nil
	})
}

// recordShare writes the account logs of a share change: the admin's own and
// the owner's, each with the sentence that is theirs. Best effort.
func (p *provider) recordShare(r *http.Request, actor *account.Account, ownerID, viewID, adminDetail, ownerDetail string) {
	e := accountEntry(r, provenance.ActionAdmins, provenance.SurfaceDashboard, provenance.AuthSession)
	e.Account = actor.ID
	e.Site = viewID
	adminLog, ownerLog := e, e
	adminLog.Detail, ownerLog.Detail = adminDetail, ownerDetail
	p.RecordAccountProvenance(actor.ID, adminLog)
	if ownerID != actor.ID {
		p.RecordAccountProvenance(ownerID, ownerLog)
	}
}

func (p *provider) handleInviteDecline(w http.ResponseWriter, r *http.Request) {
	_, inv, ok := p.recipientInvite(w, r)
	if !ok {
		return
	}
	// A lapsed invitation answers like an ended one, and nobody is mailed.
	if inv.State == ownership.StateOpen && inviteLapsed(inv, time.Now()) {
		p.endInvite(inv.ID, ownership.StateExpired)
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be declined", inviteStateText(ownership.StateExpired))
		return
	}
	cur, err := p.setInviteState(inv.ID, ownership.StateDeclined)
	switch {
	case errors.Is(err, errInviteNotOpen):
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be declined", inviteStateText(inv.State))
		return
	case err != nil:
		slog.Error("share: could not decline an invitation", "invite", inv.ID, "err", err)
		p.renderShareRetry(w)
		return
	}
	subject, body := p.shareDeclinedMail(cur)
	p.mailVerified(p.emailOf(cur.From), subject, body)
	p.redirect(w, r, "/account")
}

// handleInviteCancel withdraws an open invitation: its inviter only.
func (p *provider) handleInviteCancel(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.transferPost(w, r)
	if !ok {
		return
	}
	inv, err := p.owner.GetInvite(r.PathValue("id"))
	if err != nil || inv.From != acc.ID {
		if err != nil && !errors.Is(err, ownership.ErrNotFound) {
			slog.Error("share: could not read an invitation", "err", err)
		}
		http.NotFound(w, r)
		return
	}
	_, err = p.setInviteState(inv.ID, ownership.StateCancelled)
	switch {
	case errors.Is(err, errInviteNotOpen):
		p.renderShareMessage(w, http.StatusConflict, "This invitation cannot be cancelled", inviteStateText(inv.State))
		return
	case err != nil:
		slog.Error("share: could not cancel an invitation", "invite", inv.ID, "err", err)
		p.renderShareRetry(w)
		return
	}
	p.redirect(w, r, "/account")
}

// ---- ending and leaving ----

// handleShareEnd ends one admin's share: the owner only, by meta.json.
func (p *provider) handleShareEnd(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.transferPost(w, r)
	if !ok {
		return
	}
	viewID, adminID := r.PathValue("id"), r.PathValue("account")
	if !p.ownsByMeta(acc, viewID) {
		http.NotFound(w, r)
		return
	}
	admins, err := p.siteAdmins(viewID)
	if err != nil || !listsAccount(admins, adminID) {
		http.NotFound(w, r)
		return
	}
	if p.endShare(w, r, acc, acc.ID, viewID, adminID) {
		p.redirect(w, r, "/account")
	}
}

// handleShareLeave is the admin leaving a site shared with them.
func (p *provider) handleShareLeave(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.transferPost(w, r)
	if !ok {
		return
	}
	viewID := r.PathValue("id")
	admins, err := p.siteAdmins(viewID)
	if err != nil || !listsAccount(admins, acc.ID) {
		http.NotFound(w, r)
		return
	}
	owner, err := p.siteOwner(viewID)
	if err != nil || owner == "" {
		http.NotFound(w, r)
		return
	}
	if p.endShare(w, r, acc, owner, viewID, acc.ID) {
		p.redirect(w, r, "/account")
	}
}

// endShare is the one way a share ends, by the owner or by the admin: the
// admin leaves the site's list (under the site lock, in the core), then the
// share marker and the admin's own tags for the site go, both account logs
// record it and both sides are mailed, naming who ended it. The marker and
// the tags are idempotent and log their own failures: meta.json already says
// the admin is gone. ok is false when a response was written.
func (p *provider) endShare(w http.ResponseWriter, r *http.Request, actor *account.Account, ownerID, viewID, adminID string) bool {
	err := p.removeAdminOf(r, actor, viewID, ownerID, adminID)
	switch {
	case errors.Is(err, ext.ErrNotOwner):
		// The site changed hands while this request ran: its new owner's
		// admins are not this request's to end.
		http.NotFound(w, r)
		return false
	case errors.Is(err, ext.ErrSiteLocked):
		p.renderShareMessage(w, http.StatusForbidden, "This site is locked by the operator", msgSiteLockedNow)
		return false
	case errors.Is(err, ext.ErrSiteGone):
		http.NotFound(w, r)
		return false
	case err != nil:
		slog.Error("share: could not remove an admin", "site", viewID, "account", adminID, "err", err)
		p.renderShareRetry(w)
		return false
	}
	if err := p.owner.UnlinkShare(adminID, viewID); err != nil {
		slog.Error("share: could not drop a share marker", "site", viewID, "account", adminID, "err", err)
	}
	if err := p.dropAdminTags(adminID, viewID); err != nil {
		slog.Error("share: could not drop an admin's tags", "site", viewID, "account", adminID, "err", err)
	}
	who := "the owner"
	if actor.ID == adminID {
		who = "the admin"
	}
	e := accountEntry(r, provenance.ActionAdmins, provenance.SurfaceDashboard, provenance.AuthSession)
	e.Account = actor.ID
	e.Site = viewID
	ownerLog, adminLog := e, e
	ownerLog.Detail = "ended a share on the site (" + who + " ended it)"
	adminLog.Detail = "no longer an admin of the site (" + who + " ended it)"
	p.RecordAccountProvenance(ownerID, ownerLog)
	if adminID != ownerID {
		p.RecordAccountProvenance(adminID, adminLog)
	}
	ownerEmail, adminEmail := p.emailOf(ownerID), p.emailOf(adminID)
	subject, body := p.shareEndedMail(viewID, ownerEmail, adminEmail, actor.Email)
	p.mailVerified(ownerEmail, subject, body)
	p.mailVerified(adminEmail, subject, body)
	return true
}

// ---- the dashboard's parts ----

type shareAdminRow struct{ Account, Email, Until string }

type shareInviteRow struct{ ID, Email, Until string }

// inviteCard is one open invitation to this account, for the "Invitations"
// card.
type inviteCard struct {
	ID    string
	From  string
	Site  string
	Until string
	// CanAccept is false while the plan cannot be an admin (NeedsPlan) or
	// cannot be read.
	CanAccept bool
	NeedsPlan bool
	// PlanUnknown: the plan could not be read; the card says to try again.
	PlanUnknown bool
}

// inviteCards are the open invitations to acc's verified address that have
// not lapsed and whose inviter still owns the site. Like the offers, they
// are found by a scan of the invitations.
func (p *provider) inviteCards(acc *account.Account) []inviteCard {
	if p.owner == nil || !acc.EmailVerified {
		return nil
	}
	invs, err := p.owner.InvitesByEmail(acc.Email)
	if err != nil {
		slog.Error("dashboard: could not read the invitations to an account", "account", acc.ID, "err", err)
		return nil
	}
	now := time.Now()
	var cards []inviteCard
	planOK, planKnown := false, false
	for _, inv := range invs {
		if !isInvitee(acc, inv) || !liveInvite(inv, now) || (inv.Until != nil && !inv.Until.After(now)) {
			continue
		}
		if owner, err := p.siteOwner(inv.Site); err != nil || owner != inv.From {
			continue
		}
		if !planKnown {
			ok, err := p.SharingAllowed(acc.ID)
			planOK, planKnown = err == nil && ok, err == nil
			if err != nil {
				slog.Error("dashboard: could not read the invitee's plan", "account", acc.ID, "err", err)
			}
		}
		c := inviteCard{ID: inv.ID, From: p.emailOf(inv.From), Site: p.labelOf(inv.Site), CanAccept: planOK, NeedsPlan: planKnown && !planOK, PlanUnknown: !planKnown}
		if c.From == "" {
			c.From = "another account"
		}
		if inv.Until != nil {
			c.Until = dateText(*inv.Until)
		}
		cards = append(cards, c)
	}
	return cards
}

// canShareFrom reports whether the owner's rows offer the share form: the
// plan shares and the account is not the operator's.
func (p *provider) canShareFrom(acc *account.Account) bool {
	if p.owner == nil {
		return false
	}
	ok, err := p.SharingAllowed(acc.ID)
	return err == nil && ok && !p.IsOperator(acc.ID)
}

// shareRowsFor gives an owned, unlocked row its share form, its admins and
// its open invitations. open is the owner's live invitations by site.
func (p *provider) shareRowsFor(row *siteRow, canShare bool, open map[string][]ownership.Invite) {
	if row.Locked != nil {
		return
	}
	row.CanShare = canShare
	for _, a := range row.SiteInfo.Admins {
		r := shareAdminRow{Account: a.Account, Email: p.emailOf(a.Account)}
		if r.Email == "" {
			r.Email = "an account that no longer exists"
		}
		if a.Until != nil {
			r.Until = dateText(*a.Until)
		}
		row.AdminRows = append(row.AdminRows, r)
	}
	for _, inv := range open[row.ViewID] {
		r := shareInviteRow{ID: inv.ID, Email: inv.ToEmail}
		if inv.Until != nil {
			r.Until = dateText(*inv.Until)
		}
		row.InviteRows = append(row.InviteRows, r)
	}
}

// liveInvitesBySite are the account's open, unlapsed invitations by site.
func (p *provider) liveInvitesBySite(acc *account.Account) map[string][]ownership.Invite {
	if p.owner == nil {
		return nil
	}
	invs, err := p.owner.InvitesBySender(acc.ID)
	if err != nil {
		slog.Error("dashboard: could not read an account's invitations", "account", acc.ID, "err", err)
		return nil
	}
	now := time.Now()
	out := map[string][]ownership.Invite{}
	for _, inv := range invs {
		if liveInvite(inv, now) {
			out[inv.Site] = append(out[inv.Site], inv)
		}
	}
	return out
}

// sharedRows are the sites shared with the account, as dashboard rows. A
// share whose standing does not hold now (a plan without sharing, an end date
// past) is listed as paused: no Manage, no tags. The tags are the admin's
// own, from their registry: the owner's Meta.Tags never reach this row.
func (p *provider) sharedRows(acc *account.Account, token string, owned map[string]bool) []siteRow {
	var rows []siteRow
	for _, id := range p.SharedSiteIDs(acc.ID) {
		if owned[id] {
			continue
		}
		info, ok := p.host.Sites().Info(id)
		if !ok || info.Owner == "" || info.Owner == acc.ID {
			continue
		}
		// The Info rendered must still list the account: a former admin
		// (the site was transferred, the share ended) never sees "shared by"
		// the new owner.
		if !listsAccount(info.Admins, acc.ID) {
			continue
		}
		_, admin := p.manages(acc, id)
		row := siteRow{
			SiteInfo: info, SizeText: humanBytes(info.Bytes + info.GitBytes),
			ExpiryText: expiryText(info.ExpiresAt), CSRF: token,
			Shared: true, SharedPaused: !admin, SharedBy: p.emailOf(info.Owner),
		}
		if row.SharedBy == "" {
			row.SharedBy = "another account"
		}
		for _, a := range info.Admins {
			if a.Account == acc.ID && a.Until != nil {
				row.SharedUntil = dateText(*a.Until)
			}
		}
		row.SiteInfo.Admins = nil
		row.SiteInfo.Tags = p.AdminTags(acc.ID, id)
		if info.GitBytes > 0 {
			row.GitText = humanBytes(info.GitBytes)
		}
		if info.Locked != nil {
			row.LockedText = info.Locked.At.Local().Format("2006-01-02")
			row.ExpiryText = "kept while locked"
		}
		rows = append(rows, row)
	}
	return rows
}
