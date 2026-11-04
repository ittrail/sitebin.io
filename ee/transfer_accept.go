//go:build ee

package ee

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
	"github.com/ittrail/sitebin.io/ee/ownership"
	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// The recipient's side of a transfer: the "Ready to take over" card on the
// dashboard, accepting the whole bundle at once, declining, and the result.
// The sender's side is transfer_send.go. Read
// docs/superpowers/specs/2026-11-03-site-ownership-design.md, "Recipient",
// "Accept" and "Decline, expiry, cancellation".
//
// What this file must keep true:
//   - Only the account whose VERIFIED address is the offer's sees or acts on
//     it; to anyone else an offer id is a 404, as if there were none.
//   - Nothing moves until every check passed: the offer, its dates, the
//     sites, the host rule, the pauses, both plans and the limits. A refusal
//     changes nothing, except a host conflict that appeared after the offer
//     was made, which pauses it and tells the sender.
//   - One state change (open -> accepting) decides every race: two accepts,
//     an accept and a cancel. The sites then move one by one in sorted order,
//     each under its own site lock in the core, and a run that stops is
//     finished FORWARD by completeAccepting, never rolled back.
//   - One run per offer at a time in this process (claimAccept): the accept
//     holds the claim from its checks to its last move, and the background
//     loop skips an offer that is claimed, so the two never read each
//     other's moves as a site that changed hands.
//   - The new edit passwords exist in memory only: the accept response shows
//     each once, and no file, log line or mail ever holds one.
//   - Plain POST forms with CSRF, no script.

const (
	takeoverUnavailable = "This offer is not available right now."
	// rotateHint is what a site gets in place of a password it no longer has
	// to show: a reload of the result, or a site the background moved.
	rotateHint = "Its edit password is not shown again: rotate the password on the dashboard to get a new one."
)

var (
	// errAcceptBusy is an offer another run in this process is moving now.
	// The background loop skips it; the accept asks for a reload.
	errAcceptBusy    = errors.New("the offer is being moved by another run")
	errNotAccepting  = errors.New("the offer is not being accepted")
	errOfferNotOpen  = errors.New("the offer is not open")
	errOfferChanged  = errors.New("the offer changed")
	errTagsUnchanged = errors.New("no tags to drop")
)

func (p *provider) takeoverRoutes(routes map[string]http.Handler) {
	routes["POST "+transferPath+"/{id}/accept"] = http.HandlerFunc(p.handleTakeoverAccept)
	routes["POST "+transferPath+"/{id}/decline"] = http.HandlerFunc(p.handleTakeoverDecline)
	routes["GET "+transferPath+"/{id}/done"] = http.HandlerFunc(p.handleTakeoverDone)
}

// The core's ext.OwnershipSites.TransferSite and SiteLocked, asserted on
// their own like the other narrow seams.
type siteTransferrer interface {
	TransferSite(viewID, from, to string, g ext.CreateGrant, by ext.ChangeBy) (string, error)
}

type siteLockReader interface {
	SiteLocked(viewID string) (bool, error)
}

// ---- who, when, whether ----

// isRecipient reports whether acc is the offer's recipient: its address is
// verified and, normalised the way the store normalises the offer's, equals
// it; and it is not the sender. An unverified account is never anyone's
// recipient.
func isRecipient(acc *account.Account, o ownership.Offer) bool {
	return acc != nil && acc.EmailVerified && acc.ID != o.From &&
		ownership.NormEmail(acc.Email) == o.ToEmail
}

// claimAccept claims the offer for one run in this process: an accept from
// its checks to its last move, or one call of completeAccepting. ok is false
// while another run holds it.
func (p *provider) claimAccept(offerID string) (release func(), ok bool) {
	if _, busy := p.acceptRuns.LoadOrStore(offerID, struct{}{}); busy {
		return nil, false
	}
	return func() { p.acceptRuns.Delete(offerID) }, true
}

// offerWindow is "" while now is within the offer's dates, else what the
// recipient is told.
func offerWindow(o ownership.Offer, now time.Time) string {
	if o.AvailableFrom != nil && now.Before(*o.AvailableFrom) {
		return "This offer is not available yet."
	}
	if now.After(o.AcceptBy) {
		return "This offer lapsed on " + dateText(o.AcceptBy) + " (UTC) and can no longer be accepted."
	}
	return ""
}

// stateText is what the recipient is told about an offer that is not open.
func stateText(state string) string {
	switch state {
	case ownership.StatePaused:
		return takeoverUnavailable
	case ownership.StateAccepting:
		return "This offer is being accepted right now."
	case ownership.StateAccepted:
		return "This offer has already been accepted."
	case ownership.StateDeclined:
		return "This offer was declined."
	case ownership.StateExpired:
		return "This offer lapsed and can no longer be accepted."
	case ownership.StateCancelled:
		return "The sender cancelled this offer."
	case ownership.StateEnded:
		return "Ended: no site could be moved."
	}
	return takeoverUnavailable
}

// sameSites reports whether two site lists hold the same sites.
func sameSites(a, b []string) bool {
	x, y := uniq(a), uniq(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// offerPause says why an offer that is not final cannot be accepted right
// now: the sender's account is gone or suspended, the sender's plan no longer
// shares, a site of the bundle is locked, or the recipient's account is
// suspended. "" means none of these. An error means the answer is unknown,
// and the caller refuses the accept. The reason never says anything about the
// recipient's account, so it is safe to tell the sender.
//
// A host conflict is not a pause reason here: the accept finds it, pauses the
// offer and tells the sender (acceptCheck). Locks are read from meta.json
// only (SiteLocked), never by walking a site.
func (p *provider) offerPause(o ownership.Offer) (string, error) {
	return p.pauseReason(o, nil)
}

// pauseReason is offerPause with the sites' Info already read (infos, by
// view id), so a locked site's name costs no second walk.
func (p *provider) pauseReason(o ownership.Offer, infos map[string]ext.SiteInfo) (string, error) {
	sender, err := p.accounts.ByID(o.From)
	switch {
	case errors.Is(err, account.ErrNotFound):
		return "the sender's account no longer exists", nil
	case err != nil:
		return "", fmt.Errorf("read the sender's account: %w", err)
	case sender.Suspended():
		return "the sender's account is suspended", nil
	}
	allowed, err := p.SharingAllowed(o.From)
	if err != nil {
		return "", fmt.Errorf("read the sender's plan: %w", err)
	}
	if !allowed {
		return "the sender's plan no longer includes transfers", nil
	}
	locks, ok := p.host.Sites().(siteLockReader)
	if !ok {
		return "", errors.New("the site service cannot report a site's lock")
	}
	for _, id := range o.Sites {
		locked, err := locks.SiteLocked(id)
		switch {
		case errors.Is(err, ext.ErrSiteGone):
			// Not a pause: the owner check refuses a site that is gone.
		case err != nil:
			return "", fmt.Errorf("read the lock of %s: %w", id, err)
		case locked:
			if info, ok := infos[id]; ok {
				return siteLabel(info) + " is locked by the operator", nil
			}
			return p.labelOf(id) + " is locked by the operator", nil
		}
	}
	rcpt, err := p.accounts.ByEmail(o.ToEmail)
	switch {
	case errors.Is(err, account.ErrNotFound):
	case err != nil:
		return "", fmt.Errorf("read the recipient's account: %w", err)
	case rcpt.EmailVerified && rcpt.Suspended():
		// Never said in so many words: the sender learns nothing about the
		// recipient's account.
		return "the offer cannot be accepted right now", nil
	}
	return "", nil
}

// The kinds of takeRefusal.
const (
	takePaused = "paused" // offerPause gave a reason
	takeGone   = "gone"   // a site no longer exists or is no longer the sender's
	takeHost   = "host"   // a site shares a host with a site of the sender outside the offer
	takePlan   = "plan"   // the recipient's plan does not share
	takeLimits = "limits" // the sites do not fit the recipient's plan
)

// takeRefusal is why sites of an offer cannot move.
type takeRefusal struct {
	kind string
	// reason is a sentence for the mails, safe for both parties.
	reason string
	// lines are the failing lines of the limits check (plan, limits).
	lines []checkLine
}

// acceptCheck re-checks what moving the sites of o to acc needs, in this
// order: the pauses (offerPause), every site still the sender's by meta.json,
// the host rule, then acc's plan and limits for the sites. The caller passes
// the offer with the sites still to move. An error is unknown, never a pass.
func (p *provider) acceptCheck(o ownership.Offer, acc *account.Account) (*takeRefusal, error) {
	reason, err := p.offerPause(o)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		return &takeRefusal{kind: takePaused, reason: reason}, nil
	}
	for _, id := range o.Sites {
		owner, err := p.siteOwner(id)
		switch {
		case errors.Is(err, ext.ErrSiteGone):
			return &takeRefusal{kind: takeGone, reason: "a site of the offer no longer exists"}, nil
		case err != nil:
			return nil, err
		case owner != o.From:
			return &takeRefusal{kind: takeGone, reason: "a site of the offer is no longer the sender's"}, nil
		}
	}
	hc, ok := p.host.Sites().(hostConflictReader)
	if !ok {
		return nil, errors.New("the site service cannot report host conflicts")
	}
	conflicts, err := hc.HostConflicts(o.Sites, o.From)
	if err != nil {
		return nil, fmt.Errorf("read the host conflicts: %w", err)
	}
	if len(conflicts) > 0 {
		return &takeRefusal{kind: takeHost, reason: p.hostConflictText(conflicts)}, nil
	}
	lines, err := p.limitsCheck(acc, o.Sites)
	if err != nil {
		return nil, err
	}
	failed := failedLines(lines)
	if len(failed) == 0 {
		return nil, nil
	}
	ref := &takeRefusal{kind: takeLimits, reason: "they do not fit the recipient's plan", lines: failed}
	if planRefuses(failed) {
		ref.kind, ref.reason = takePlan, "the recipient's plan does not include taking sites over"
	}
	return ref, nil
}

// planRefuses reports whether the plan's sharing line is among the lines.
func planRefuses(lines []checkLine) bool {
	return slices.ContainsFunc(lines, func(l checkLine) bool { return l.Label == "Sharing" && !l.OK })
}

// hostConflictText names each shared host and the sites outside the offer
// that use it.
func (p *provider) hostConflictText(conflicts map[string][]string) string {
	var parts []string
	for _, host := range sortedStringKeys(conflicts) {
		var labels []string
		for _, id := range conflicts[host] {
			labels = append(labels, p.labelOf(id))
		}
		parts = append(parts, host+" is also used by "+strings.Join(labels, ", "))
	}
	return strings.Join(parts, "; ") + ", which the offer does not include"
}

// ---- moving the sites ----

// acceptRun is what one run of the forward completion did.
type acceptRun struct {
	// Moved are the sites this run moved, with their new edit passwords.
	// Memory only: the accept response shows them once.
	Moved []movedSite
	// Complete: every site is done and the offer is accepted.
	Complete bool
	// Ended: the sites not done stay with the sender, and the offer is final.
	Ended bool
	// Offer is the offer as the run left it (zero when it could not be read).
	Offer ownership.Offer
}

type movedSite struct{ ID, Password string }

// completeAccepting is the ONE forward completion of an accepting offer: the
// accept runs its body right after the state change (runAccepting), and the
// background loop calls it for an offer a run left accepting. It moves to the
// account that accepted (to, which must be o.AcceptedBy) every site of the
// offer not yet done, in sorted view-id order, and never moves one twice:
//
//   - It claims the offer first (claimAccept). An offer another run in this
//     process holds is errAcceptBusy: the loop skips it this time.
//   - A site whose meta.json already names the recipient moved in an earlier
//     run whose record did not land: its bookkeeping is finished (every step
//     is idempotent) and it counts as done.
//   - The rest are re-checked first (acceptCheck, except the dates: the
//     acceptance was made in time). A site the check finds no longer the
//     sender's is looked at once more, as the recipient's may have just moved
//     it. If the check fails, the rest stay with the sender, the offer
//     becomes final (accepted when some sites moved, ended when none did) and
//     both sides are mailed: the sender with the reason, the recipient with a
//     generic sentence. An unknown answer changes nothing: the next call asks
//     again.
//   - A move the core refuses or fails stops the run; the offer stays
//     accepting for the next call.
//
// With no request behind it (no address, no user agent), it records the
// change as the background loop's.
func (p *provider) completeAccepting(o ownership.Offer, to *account.Account, by ext.ChangeBy) (acceptRun, error) {
	release, ok := p.claimAccept(o.ID)
	if !ok {
		return acceptRun{}, errAcceptBusy
	}
	defer release()
	if by.IP == "" && by.UserAgent == "" && by.Surface == "" {
		by.Surface = provenance.SurfaceBackground
	}
	return p.runAccepting(o.ID, to, by, true)
}

// runAccepting is completeAccepting's body; the caller holds the claim.
// recheck is false only for the accept itself, which checked everything a
// moment before its state change, under the same claim.
func (p *provider) runAccepting(id string, to *account.Account, by ext.ChangeBy, recheck bool) (acceptRun, error) {
	var run acceptRun
	o, err := p.owner.GetOffer(id)
	if err != nil {
		return run, err
	}
	run.Offer = o
	if o.State != ownership.StateAccepting {
		return run, fmt.Errorf("%w: offer %s is %s", errNotAccepting, id, o.State)
	}
	if to == nil || o.AcceptedBy == "" || o.AcceptedBy != to.ID {
		return run, fmt.Errorf("offer %s was not accepted by this account", id)
	}
	// The caps and the plan are the recipient's as they are now. An account
	// that is gone keeps the caller's copy: the re-check then refuses it (its
	// plan shares nothing) and the sites stay with the sender.
	recipientGone := false
	if fresh, err := p.accounts.ByID(to.ID); err == nil {
		to = fresh
	} else if errors.Is(err, account.ErrNotFound) {
		recipientGone = true
	} else {
		return run, fmt.Errorf("read the recipient's account: %w", err)
	}
	ts, ok := p.host.Sites().(siteTransferrer)
	if !ok {
		return run, errors.New("the site service cannot transfer sites")
	}
	from := p.accountOrStub(o.From)

	var todo []string
	for pass := 0; ; pass++ {
		if todo, err = p.recordMoved(&run, from, to, by); err != nil {
			return run, err
		}
		if len(todo) == 0 || !recheck {
			break
		}
		// An account deleted after it accepted receives nothing: the sites
		// stay with the sender.
		if recipientGone {
			return p.endAccepting(run, todo, "the recipient's account no longer exists")
		}
		rest := run.Offer
		rest.Sites = todo
		ref, err := p.acceptCheck(rest, to)
		if err != nil {
			return run, fmt.Errorf("re-check before completing: %w", err)
		}
		if ref == nil {
			break
		}
		if ref.kind == takeGone && pass == 0 {
			continue // a site the recipient already holds is moved, not gone
		}
		return p.endAccepting(run, todo, ref.reason)
	}

	if len(todo) > 0 {
		grant, err := p.transferGrant(to)
		if err != nil {
			return run, fmt.Errorf("the recipient's caps: %w", err)
		}
		for _, vid := range todo {
			if err := p.moveSite(&run, vid, from, to, grant, ts, by); err != nil {
				return run, err
			}
		}
	}

	final, err := p.owner.UpdateOffer(id, func(x *ownership.Offer) error {
		if x.State != ownership.StateAccepting {
			return errNotAccepting
		}
		if len(notDone(*x)) > 0 {
			return fmt.Errorf("offer %s still has sites to move", x.ID)
		}
		x.State = ownership.StateAccepted
		return nil
	})
	if err != nil {
		return run, err
	}
	run.Offer, run.Complete = final, true
	slog.Info("transfer accepted", "offer", id, "from", final.From, "to", to.ID, "sites", len(final.Done))
	p.mailAccepted(final, nil, "")
	return run, nil
}

// recordMoved finishes the bookkeeping of every site not yet done whose
// meta.json already names the recipient (its move's record did not land) and
// returns the sites still to move, in sorted view-id order.
func (p *provider) recordMoved(run *acceptRun, from, to *account.Account, by ext.ChangeBy) ([]string, error) {
	var todo []string
	for _, vid := range notDone(run.Offer) {
		owner, err := p.siteOwner(vid)
		if err != nil || owner != to.ID {
			todo = append(todo, vid)
			continue
		}
		if err := p.accounts.LinkSite(to, vid); err != nil {
			slog.Error("transfer: could not link a moved site to its new owner; meta.json decides", "site", vid, "account", to.ID, "err", err)
		}
		p.afterMove(vid, from, to, nil, by)
		cur, err := p.markDone(run.Offer.ID, vid)
		if err != nil {
			return nil, fmt.Errorf("record %s as moved: %w", vid, err)
		}
		run.Offer = cur
	}
	return todo, nil
}

// transferGrant is the stamp a site taken over gets: the recipient's caps
// from its plan as the STRICT tier lookup gives it. An unknown plan is an
// error that stops the move; it never falls back to the stored or default
// tier the way a creation does. The site cap was checked for the whole
// bundle (limitsCheck), so it is not asked again per site.
func (p *provider) transferGrant(acc *account.Account) (ext.CreateGrant, error) {
	if p.cfg.Mode != eeconfig.ModeTiers {
		gitOn := true
		return ext.CreateGrant{OwnerAccountID: acc.ID, Git: &gitOn}, nil
	}
	t, err := p.effectiveTierStrict(acc)
	if err != nil {
		return ext.CreateGrant{}, err
	}
	return grantFromTier(acc.ID, t), nil
}

// notDone is the offer's sites not yet moved, in sorted view-id order.
func notDone(o ownership.Offer) []string {
	var out []string
	for _, id := range uniq(o.Sites) {
		if !slices.Contains(o.Done, id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// endAccepting leaves the sites that are not done with the sender, makes the
// offer final (accepted when some sites moved, ended when none did) and tells
// both sides.
func (p *provider) endAccepting(run acceptRun, stayed []string, reason string) (acceptRun, error) {
	final, err := p.owner.UpdateOffer(run.Offer.ID, func(x *ownership.Offer) error {
		if x.State != ownership.StateAccepting {
			return errNotAccepting
		}
		if len(x.Done) > 0 {
			x.State = ownership.StateAccepted
		} else {
			x.State = ownership.StateEnded
		}
		return nil
	})
	if err != nil {
		return run, err
	}
	run.Offer, run.Ended = final, true
	slog.Info("transfer ended with sites left with the sender", "offer", final.ID, "moved", len(final.Done), "stayed", len(stayed))
	p.mailAccepted(final, stayed, reason)
	return run, nil
}

// moveSite moves one site and does its bookkeeping. Once the core moved the
// site, its password is in run.Moved and its id in run.Offer.Done, even when
// recording it fails after: the response must still show that password, and
// never list the site as still to move.
func (p *provider) moveSite(run *acceptRun, viewID string, from, to *account.Account, g ext.CreateGrant, ts siteTransferrer, by ext.ChangeBy) error {
	// The former admins, read before the transfer clears them: their share
	// markers and their own tags for the site end with the share.
	admins, err := p.siteAdmins(viewID)
	if err != nil {
		return fmt.Errorf("read the admins of %s: %w", viewID, err)
	}
	// The recipient's marker first. A stray one (the transfer refused below)
	// is harmless, because meta.json is the truth; a missing one would hide a
	// moved site from its new owner's lists until the reconciliation.
	if err := p.accounts.LinkSite(to, viewID); err != nil {
		return fmt.Errorf("link %s to the recipient: %w", viewID, err)
	}
	// The core decides, under the site lock, which addresses stay with the
	// sender.
	pw, err := ts.TransferSite(viewID, from.ID, to.ID, g, by)
	if err != nil {
		return fmt.Errorf("transfer %s: %w", viewID, err)
	}
	run.Moved = append(run.Moved, movedSite{viewID, pw})
	p.afterMove(viewID, from, to, admins, by)
	cur, err := p.markDone(run.Offer.ID, viewID)
	if err != nil {
		if !slices.Contains(run.Offer.Done, viewID) {
			run.Offer.Done = append(run.Offer.Done, viewID)
		}
		return fmt.Errorf("record %s as moved: %w", viewID, err)
	}
	run.Offer = cur
	return nil
}

// afterMove is the bookkeeping after the core moved a site: the sender's
// marker, the former admins' share markers and own tags, and both account
// logs. Each step is idempotent and logs its own failure: meta.json already
// says who owns the site, and a stray marker or tag grants nothing.
func (p *provider) afterMove(viewID string, from, to *account.Account, admins []ext.SiteAdmin, by ext.ChangeBy) {
	if err := p.accounts.UnlinkSite(from, viewID); err != nil {
		slog.Error("transfer: could not drop the sender's marker; meta.json decides", "site", viewID, "account", from.ID, "err", err)
	}
	for _, a := range admins {
		if err := p.owner.UnlinkShare(a.Account, viewID); err != nil {
			slog.Error("transfer: could not drop a former admin's share marker", "site", viewID, "account", a.Account, "err", err)
		}
		if err := p.dropAdminTags(a.Account, viewID); err != nil {
			slog.Error("transfer: could not drop a former admin's tags", "site", viewID, "account", a.Account, "err", err)
		}
	}
	// A session asked on the dashboard; the background loop presents no
	// credential of its own (it finishes what a session asked for earlier).
	surface, credential := by.Surface, provenance.AuthSession
	switch surface {
	case "":
		surface = provenance.SurfaceDashboard
	case provenance.SurfaceBackground:
		credential = provenance.AuthNone
	}
	e := provenance.Entry{
		Time: time.Now().UTC(), Action: provenance.ActionTransfer, Surface: surface, Auth: credential,
		Account: by.Account, IP: by.IP, UA: by.UserAgent, Site: viewID,
	}
	sent := e
	sent.Detail = "handed over to account " + to.ID
	p.RecordAccountProvenance(from.ID, sent)
	taken := e
	taken.Detail = "taken over from account " + from.ID
	p.RecordAccountProvenance(to.ID, taken)
}

// markDone records a moved site in the offer, which must still be accepting.
func (p *provider) markDone(offerID, viewID string) (ownership.Offer, error) {
	return p.owner.UpdateOffer(offerID, func(x *ownership.Offer) error {
		if x.State != ownership.StateAccepting {
			return errNotAccepting
		}
		if !slices.Contains(x.Done, viewID) {
			x.Done = append(x.Done, viewID)
		}
		return nil
	})
}

// dropAdminTags removes an account's own tags for a site it no longer
// administers. SetAdminTags cannot: it refuses unless meta.json still lists
// the account, and the transfer has cleared the admins.
func (p *provider) dropAdminTags(accountID, viewID string) error {
	err := p.accounts.UpdateTags(accountID, func(r *account.TagRegistry) error {
		if _, ok := r.Sites[viewID]; !ok {
			return errTagsUnchanged
		}
		delete(r.Sites, viewID)
		return nil
	})
	if errors.Is(err, errTagsUnchanged) || errors.Is(err, account.ErrNotFound) {
		return nil
	}
	return err
}

// accountOrStub is the account with this id, or one carrying only the id
// when it cannot be read: the marker and the log need nothing else.
func (p *provider) accountOrStub(id string) *account.Account {
	if acc, err := p.accounts.ByID(id); err == nil {
		return acc
	}
	return &account.Account{ID: id}
}

// emailOf is an account's address, "" when it cannot be read.
func (p *provider) emailOf(id string) string {
	if acc, err := p.accounts.ByID(id); err == nil {
		return acc.Email
	}
	return ""
}

// ---- the card ----

type takeoverSite struct {
	ID      string
	Label   string
	Domains []string
	Forms   []string
}

// takeoverCard is one "Ready to take over" card.
type takeoverCard struct {
	ID       string
	From     string
	Count    string
	AcceptBy string
	Sites    []takeoverSite
	// Unavailable: paused, or a check could not be made; no Accept.
	Unavailable bool
	// NeedsPlan: the recipient's plan does not share.
	NeedsPlan bool
	Lines     []checkLine
	CanAccept bool
}

// takeoverCards are the cards for the offers made to acc's verified address
// that are open or paused and within their dates. OffersByEmail scans every
// offer on each render; an index by email is the next step once offers
// number in the thousands.
func (p *provider) takeoverCards(acc *account.Account) []takeoverCard {
	if p.owner == nil || !acc.EmailVerified {
		return nil
	}
	offers, err := p.owner.OffersByEmail(acc.Email)
	if err != nil {
		slog.Error("dashboard: could not read the offers to an account", "account", acc.ID, "err", err)
		return nil
	}
	now := time.Now()
	var out []takeoverCard
	for _, o := range offers {
		if !isRecipient(acc, o) || (o.State != ownership.StateOpen && o.State != ownership.StatePaused) || offerWindow(o, now) != "" {
			continue
		}
		out = append(out, p.takeoverCard(acc, o))
	}
	return out
}

func (p *provider) takeoverCard(acc *account.Account, o ownership.Offer) takeoverCard {
	c := takeoverCard{ID: o.ID, From: p.emailOf(o.From), Count: sitesWord(len(o.Sites)), AcceptBy: dateText(o.AcceptBy)}
	if c.From == "" {
		c.From = "An account"
	}
	forms, _ := p.host.Sites().(formRecipientReader)
	usable := o.State == ownership.StateOpen
	// Info walks a site's files: it is read once per site here and shared
	// with the limits check. The pause check reads locks from meta.json only.
	infos := make(map[string]ext.SiteInfo, len(o.Sites))
	for _, id := range o.Sites {
		s := takeoverSite{ID: id, Label: id}
		info, ok := p.host.Sites().Info(id)
		if ok {
			infos[id] = info
			s.Label, s.Domains = siteLabel(info), info.Domains
		}
		if !ok || info.Owner != o.From {
			usable = false
		}
		if forms != nil {
			s.Forms = forms.FormRecipients(id)
		}
		c.Sites = append(c.Sites, s)
	}
	if usable {
		reason, err := p.pauseReason(o, infos)
		if err != nil {
			slog.Error("dashboard: could not check an offer's pauses", "offer", o.ID, "err", err)
		}
		usable = err == nil && reason == ""
	}
	if !usable {
		c.Unavailable = true
		return c
	}
	lines, err := p.limitsCheckWith(acc, o.Sites, infos)
	if err != nil {
		slog.Error("dashboard: could not check an offer against the plan", "offer", o.ID, "account", acc.ID, "err", err)
		c.Unavailable = true
		return c
	}
	c.Lines = lines
	c.NeedsPlan = planRefuses(lines)
	c.CanAccept = len(failedLines(lines)) == 0
	return c
}

// ---- handlers ----

func (p *provider) renderTakeoverMessage(w http.ResponseWriter, status int, title, body string) {
	p.securityHeaders(w)
	w.WriteHeader(status)
	msgTmpl.Execute(w, msgView{Title: title, Body: body, Back: "/account"})
}

type takeoverRefusedView struct {
	Title string
	Body  string
	Lines []checkLine
	Plan  bool
}

func (p *provider) renderTakeoverRefused(w http.ResponseWriter, status int, v takeoverRefusedView) {
	p.securityHeaders(w)
	w.WriteHeader(status)
	takeoverRefusedTmpl.Execute(w, v)
}

// recipientOffer starts the recipient's POST routes: the account, the CSRF
// token (403 before anything is looked up), and the offer, which anyone but
// its recipient finds missing (404).
func (p *provider) recipientOffer(w http.ResponseWriter, r *http.Request) (*account.Account, ownership.Offer, bool) {
	acc, ok := p.transferPost(w, r)
	if !ok {
		return nil, ownership.Offer{}, false
	}
	o, err := p.owner.GetOffer(r.PathValue("id"))
	if err != nil || !isRecipient(acc, o) {
		if err != nil && !errors.Is(err, ownership.ErrNotFound) {
			slog.Error("transfer: could not read an offer", "err", err)
		}
		http.NotFound(w, r)
		return nil, ownership.Offer{}, false
	}
	return acc, o, true
}

// openForRecipient answers an offer that is not open or outside its dates,
// and reports whether it is open and within them.
func (p *provider) openForRecipient(w http.ResponseWriter, o ownership.Offer) bool {
	if o.State != ownership.StateOpen {
		p.renderTakeoverMessage(w, http.StatusConflict, "This offer cannot be accepted", stateText(o.State)+" Nothing was changed.")
		return false
	}
	if msg := offerWindow(o, time.Now()); msg != "" {
		p.renderTakeoverMessage(w, http.StatusConflict, "This offer cannot be accepted", msg+" Nothing was changed.")
		return false
	}
	return true
}

func (p *provider) renderOfferChanged(w http.ResponseWriter) {
	p.renderTakeoverMessage(w, http.StatusConflict, "The offer changed",
		"The offer changed since you opened the page. Nothing was changed. Please look at it again on your dashboard.")
}

// handleTakeoverAccept accepts the whole bundle: the checks, the one state
// change that decides every race, the moves, and the result page with the
// new edit passwords, rendered in this response and nowhere else.
func (p *provider) handleTakeoverAccept(w http.ResponseWriter, r *http.Request) {
	acc, o, ok := p.recipientOffer(w, r)
	if !ok {
		return
	}
	if !p.openForRecipient(w, o) {
		return
	}
	// The recipient accepts exactly the bundle the card showed.
	if !sameSites(r.PostForm["site"], o.Sites) {
		p.renderOfferChanged(w)
		return
	}
	// One run per offer: held from the checks to the last move, so the
	// background loop cannot take a site this run moves for one that changed
	// hands.
	release, claimed := p.claimAccept(o.ID)
	if !claimed {
		p.renderTakeoverMessage(w, http.StatusConflict, "This offer is already being accepted",
			"It is being accepted right now. Reload your dashboard in a moment to see the result.")
		return
	}
	defer release()
	ref, err := p.acceptCheck(o, acc)
	if err != nil {
		slog.Error("transfer: could not check an offer; refusing the accept", "offer", o.ID, "account", acc.ID, "err", err)
		p.renderTakeoverMessage(w, http.StatusServiceUnavailable, takeoverUnavailable, "Nothing was changed. Please try again in a minute.")
		return
	}
	if ref != nil {
		p.refuseTakeover(w, o, ref)
		return
	}

	by := ext.ChangeBy{Account: acc.ID, IP: auth.ClientIP(r), UserAgent: r.UserAgent()}
	started, err := p.owner.UpdateOffer(o.ID, func(x *ownership.Offer) error {
		if x.State != ownership.StateOpen {
			return errOfferNotOpen
		}
		if !sameSites(x.Sites, o.Sites) || offerWindow(*x, time.Now()) != "" {
			return errOfferChanged
		}
		x.State, x.AcceptedBy = ownership.StateAccepting, acc.ID
		return nil
	})
	switch {
	case errors.Is(err, errOfferNotOpen):
		state := ""
		if cur, gerr := p.owner.GetOffer(o.ID); gerr == nil {
			state = cur.State
		}
		p.renderTakeoverMessage(w, http.StatusConflict, "This offer cannot be accepted", stateText(state)+" Nothing was changed.")
		return
	case errors.Is(err, errOfferChanged):
		p.renderOfferChanged(w)
		return
	case err != nil:
		slog.Error("transfer: could not start accepting an offer", "offer", o.ID, "err", err)
		p.renderTakeoverMessage(w, http.StatusServiceUnavailable, takeoverUnavailable, "Nothing was changed. Please try again in a minute.")
		return
	}

	run, err := p.runAccepting(o.ID, acc, by, false)
	if err != nil {
		// Whatever moved stays moved; the rest is the background loop's.
		slog.Error("transfer: the bundle stopped part-way; it stays accepting for the background loop",
			"offer", o.ID, "moved", len(run.Moved), "err", err)
	}
	if run.Offer.ID == "" {
		run.Offer = started // the run could not read the offer back
	}
	p.renderTakeoverResult(w, run.Offer, run.Moved)
}

// refuseTakeover answers a failed acceptCheck. A host conflict also pauses
// the offer and tells the sender why.
func (p *provider) refuseTakeover(w http.ResponseWriter, o ownership.Offer, ref *takeRefusal) {
	switch ref.kind {
	case takePlan:
		p.renderTakeoverRefused(w, http.StatusForbidden, takeoverRefusedView{
			Title: "Your plan cannot take over sites",
			Body:  "Taking sites over from another account needs a plan that includes it. Nothing was changed.",
			Lines: ref.lines, Plan: true,
		})
	case takeLimits:
		p.renderTakeoverRefused(w, http.StatusForbidden, takeoverRefusedView{
			Title: "These sites do not fit your plan",
			Body:  "Nothing was changed. This is what stands in the way:",
			Lines: ref.lines, Plan: true,
		})
	case takeGone:
		p.renderTakeoverMessage(w, http.StatusConflict, "This offer cannot be accepted",
			"A site of this offer no longer belongs to the sender. Nothing was changed.")
	case takeHost:
		p.pauseOffer(o, ref.reason, "A host moves whole: add that site to the offer, or remove its address on that host.")
		p.renderTakeoverMessage(w, http.StatusConflict, takeoverUnavailable, "Nothing was changed. The sender has been told why.")
	default:
		p.renderTakeoverMessage(w, http.StatusConflict, takeoverUnavailable, "Nothing was changed.")
	}
}

// pauseOffer pauses an open offer and tells the sender why. An offer that is
// no longer open is left alone, and nobody is mailed twice for one pause.
func (p *provider) pauseOffer(o ownership.Offer, reason, advice string) {
	cur, err := p.owner.UpdateOffer(o.ID, func(x *ownership.Offer) error {
		if x.State != ownership.StateOpen {
			return errOfferNotOpen
		}
		x.State = ownership.StatePaused
		return nil
	})
	if err != nil {
		if !errors.Is(err, errOfferNotOpen) {
			slog.Error("transfer: could not pause an offer", "offer", o.ID, "err", err)
		}
		return
	}
	slog.Info("transfer: offer paused", "offer", o.ID)
	subject, body := p.pausedMail(cur, reason, advice)
	p.mailVerified(p.emailOf(cur.From), subject, body)
}

// handleTakeoverDecline ends an open or paused offer and tells the sender.
func (p *provider) handleTakeoverDecline(w http.ResponseWriter, r *http.Request) {
	_, o, ok := p.recipientOffer(w, r)
	if !ok {
		return
	}
	if o.State != ownership.StateOpen && o.State != ownership.StatePaused {
		p.renderTakeoverMessage(w, http.StatusConflict, "This offer cannot be declined", stateText(o.State))
		return
	}
	if msg := offerWindow(o, time.Now()); msg != "" {
		p.renderTakeoverMessage(w, http.StatusConflict, "This offer cannot be declined", msg)
		return
	}
	cur, err := p.owner.UpdateOffer(o.ID, func(x *ownership.Offer) error {
		if x.State != ownership.StateOpen && x.State != ownership.StatePaused {
			return errOfferNotOpen
		}
		x.State = ownership.StateDeclined
		return nil
	})
	switch {
	case errors.Is(err, errOfferNotOpen):
		p.renderTakeoverMessage(w, http.StatusConflict, "This offer cannot be declined", "It changed a moment ago. Please look at it again on your dashboard.")
		return
	case err != nil:
		slog.Error("transfer: could not decline an offer", "offer", o.ID, "err", err)
		p.renderTakeoverMessage(w, http.StatusServiceUnavailable, "Could not decline right now", "Nothing was changed. Please try again in a minute.")
		return
	}
	subject, body := p.declinedMail(cur)
	p.mailVerified(p.emailOf(cur.From), subject, body)
	p.redirect(w, r, "/account")
}

// handleTakeoverDone shows what an accept moved, for a reload of the result:
// the sites and their form recipients, never a password.
func (p *provider) handleTakeoverDone(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.transferAccount(w, r)
	if !ok {
		return
	}
	o, err := p.owner.GetOffer(r.PathValue("id"))
	if err != nil || o.AcceptedBy == "" || o.AcceptedBy != acc.ID {
		http.NotFound(w, r)
		return
	}
	p.renderTakeoverResult(w, o, nil)
}

// ---- the result ----

type takeoverResultSite struct {
	Label    string
	Password string
	EditURL  string
	Forms    []string
}

type takeoverResultView struct {
	Title      string
	Lede       string
	Sites      []takeoverResultSite
	NoPassword string
	Pending    []string
	Stayed     []string
}

// renderTakeoverResult is the result page: every site the offer moved, with
// its new edit password when this response moved it (moved), else the hint
// to rotate; the sites still moving or left with the sender; and each site's
// form recipients with a link to change them.
func (p *provider) renderTakeoverResult(w http.ResponseWriter, o ownership.Offer, moved []movedSite) {
	v := takeoverResultView{NoPassword: rotateHint}
	forms, _ := p.host.Sites().(formRecipientReader)
	done := slices.Clone(o.Done)
	for _, m := range moved {
		if !slices.Contains(done, m.ID) {
			done = append(done, m.ID) // moved, though its record did not land
		}
	}
	for _, id := range o.Sites {
		if !slices.Contains(done, id) {
			if o.State == ownership.StateAccepting {
				v.Pending = append(v.Pending, p.labelOf(id))
			} else {
				v.Stayed = append(v.Stayed, p.labelOf(id))
			}
			continue
		}
		s := takeoverResultSite{Label: id, EditURL: "/account"}
		if info, ok := p.host.Sites().Info(id); ok {
			s.Label = siteLabel(info)
			if info.EditURL != "" {
				s.EditURL = info.EditURL
			}
		}
		for _, m := range moved {
			if m.ID == id {
				s.Password = m.Password
			}
		}
		if forms != nil {
			s.Forms = forms.FormRecipients(id)
		}
		v.Sites = append(v.Sites, s)
	}
	switch {
	case len(v.Sites) == 0 && len(v.Pending) > 0:
		v.Title = "Taking over " + sitesWord(len(v.Pending))
		v.Lede = "Nothing could be moved right now. The sites are moved in the background if they still can be; otherwise they stay with the sender, and you are both told by mail."
	case len(v.Pending) > 0:
		v.Title = fmt.Sprintf("%d of %d sites are yours so far", len(v.Sites), len(o.Sites))
		v.Lede = "The other sites could not be moved right now. They are moved in the background if they still can be; otherwise they stay with the sender, and you are both told by mail."
	case len(v.Sites) == 0:
		v.Title = "No site was moved"
		if o.State == ownership.StateEnded {
			v.Title = "Ended: no site could be moved"
		}
		v.Lede = "The sites of this offer stay with the sender."
	default:
		v.Title = "You took over " + sitesWord(len(v.Sites))
		v.Lede = "They are sites of your account now."
	}
	if len(moved) > 0 {
		v.Lede += " Each moved site has a new edit password, shown below this once: store it now. A lost one can be reset on your dashboard."
	}
	p.securityHeaders(w)
	w.WriteHeader(http.StatusOK)
	takeoverResultTmpl.Execute(w, v)
}
