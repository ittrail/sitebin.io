//go:build ee

package ee

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/ownership"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The sender's side of a transfer: an owner offers a bundle of 1 to 50 of
// their sites to another account by email, with a mandatory accept-by date,
// and manages the offer until it is accepted. The recipient's side is
// transfer_accept.go. Read docs/superpowers/specs/2026-11-03-site-ownership-design.md,
// "Transfer".
//
// Three things this file must keep true:
//   - The answer to a create never depends on whether anyone owns the
//     address: same text, same status, and the mail lookup runs off the
//     request. The sender may be told specifics only about their OWN data.
//   - Nothing changes without the sender seeing what moves and what does not
//     (the confirm step) and, for a site already in an open offer of theirs,
//     the old bundle's name (the replace confirmation).
//   - Plain POST forms with CSRF, no script.

const (
	maxBundle         = 50
	maxOffersPerDay   = 20
	maxAcceptDays     = 365
	transferBodyLimit = 64 << 10
	transferPath      = "/account/transfer"

	// transferAnswer is what the sender is told after creating, whoever the
	// address belongs to.
	transferAnswer = "If an account with this address exists, it has been notified. Otherwise ask the person to sign up with exactly this address — it will appear in their account as soon as they do."
)

func (p *provider) transferRoutes(routes map[string]http.Handler) {
	routes["GET "+transferPath] = http.HandlerFunc(p.handleTransferPage)
	routes["POST "+transferPath] = http.HandlerFunc(p.handleTransferCreate)
	routes["POST "+transferPath+"/{id}/cancel"] = http.HandlerFunc(p.handleTransferCancel)
	routes["POST "+transferPath+"/{id}/dates"] = http.HandlerFunc(p.handleTransferDates)
	routes["POST "+transferPath+"/{id}/sites"] = http.HandlerFunc(p.handleTransferSites)
	p.takeoverRoutes(routes)
}

// Narrow seams over the core's ext.OwnershipSites, asserted one by one.
type hostConflictReader interface {
	HostConflicts(viewIDs []string, sender string) (map[string][]string, error)
}

type formRecipientReader interface {
	FormRecipients(viewID string) []string
}

// editableOffer reports whether the sender may still change the offer: open
// or paused. Accepting is mid-flight and the final states are over.
func editableOffer(o ownership.Offer) bool {
	return o.State == ownership.StateOpen || o.State == ownership.StatePaused
}

// ---- reading the form ----

type offerInput struct {
	Sites         []string
	Email         string
	AcceptBy      string
	AvailableFrom string
}

// uniq trims and de-duplicates, keeping the first appearance's order.
func uniq(in []string) []string {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func readOfferInput(r *http.Request) offerInput {
	r.PostFormValue("") // parse
	return offerInput{
		Sites:         uniq(r.PostForm["site"]),
		Email:         strings.TrimSpace(r.PostFormValue("email")),
		AcceptBy:      strings.TrimSpace(r.PostFormValue("accept_by")),
		AvailableFrom: strings.TrimSpace(r.PostFormValue("available_from")),
	}
}

// recipientEmail is the normalised address, or false for one that is not a
// plain address.
func recipientEmail(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 254 {
		return "", false
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" || strings.ContainsAny(s, " \t\r\n<>,;\"") {
		return "", false
	}
	return s, true
}

// parseTransferDates reads the two dates (YYYY-MM-DD, UTC). Accept-by runs to
// the end of its day; available-from starts at the beginning of its day. The
// rules are checked on the DAYS, so "at most 365 days ahead" and "before
// accept-by" do not wobble with the time of day. A message is returned for the
// first rule that fails.
func parseTransferDates(acceptRaw, availRaw string, now time.Time) (acceptBy time.Time, availFrom *time.Time, msg string) {
	if acceptRaw == "" {
		return time.Time{}, nil, "Enter the accept-by date."
	}
	day, err := time.Parse("2006-01-02", acceptRaw)
	if err != nil {
		return time.Time{}, nil, "The accept-by date is not a valid date."
	}
	today := now.UTC().Truncate(24 * time.Hour)
	if day.Before(today) {
		return time.Time{}, nil, "The accept-by date must be in the future."
	}
	if day.After(today.AddDate(0, 0, maxAcceptDays)) {
		return time.Time{}, nil, fmt.Sprintf("The accept-by date can be at most %d days ahead.", maxAcceptDays)
	}
	acceptBy = day.Add(24*time.Hour - time.Second)
	if availRaw != "" {
		from, err := time.Parse("2006-01-02", availRaw)
		if err != nil {
			return time.Time{}, nil, "The available-from date is not a valid date."
		}
		if !from.Before(day) {
			return time.Time{}, nil, "The available-from date must be before the accept-by date."
		}
		availFrom = &from
	}
	return acceptBy, availFrom, ""
}

// ---- refusals ----

// refusal is a request that cannot go ahead. Its text may be specific: it is
// about the sender's own data, never about the recipient.
type refusal struct {
	status    int
	msg       string
	plan      bool     // the plan has no sharing
	retry     bool     // a lookup failed: nothing is known, nothing was done
	conflicts []string // the sender's other sites sharing a host with the bundle
	blocked   []string // those of them that are locked: they cannot be added
}

func refuse(msg string) *refusal { return &refusal{status: http.StatusBadRequest, msg: msg} }

func (p *provider) sharingOK(acc *account.Account) *refusal {
	ok, err := p.SharingAllowed(acc.ID)
	if err != nil {
		slog.Error("transfer: could not read the plan", "account", acc.ID, "err", err)
		return &refusal{retry: true}
	}
	if !ok {
		return &refusal{status: http.StatusForbidden, plan: true}
	}
	return nil
}

// ownable checks that every id is a site meta.json gives to the account and
// that none is locked.
func (p *provider) ownable(acc *account.Account, ids []string) *refusal {
	owned, err := p.ownedSiteIDs(acc)
	if err != nil {
		slog.Error("transfer: could not list the account's sites", "account", acc.ID, "err", err)
		return &refusal{retry: true}
	}
	for _, id := range ids {
		info, found := p.host.Sites().Info(id)
		if !slices.Contains(owned, id) || !found || info.Owner != acc.ID {
			return refuse("Some of these sites are not sites of this account.")
		}
		if info.Locked != nil {
			return refuse(siteLabel(info) + " is locked and cannot be transferred.")
		}
	}
	return nil
}

// hostRule applies the host rule to a bundle: the sender's other sites on a
// host the bundle uses must come along.
func (p *provider) hostRule(acc *account.Account, bundle []string) *refusal {
	hc, ok := p.host.Sites().(hostConflictReader)
	if !ok {
		slog.Error("transfer: the site service cannot report host conflicts")
		return &refusal{retry: true}
	}
	found, err := hc.HostConflicts(bundle, acc.ID)
	if err != nil {
		// Unknown is never a pass: the sender retries.
		slog.Error("transfer: could not read the host conflicts", "err", err)
		return &refusal{retry: true}
	}
	var conflicts []string
	for _, ids := range found {
		for _, id := range ids {
			if !slices.Contains(bundle, id) && !slices.Contains(conflicts, id) {
				conflicts = append(conflicts, id)
			}
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	sort.Strings(conflicts)
	ref := &refusal{status: http.StatusConflict, conflicts: conflicts}
	for _, id := range conflicts {
		if info, ok := p.host.Sites().Info(id); ok && info.Locked != nil {
			ref.blocked = append(ref.blocked, id)
		}
	}
	return ref
}

// openOffersOf returns the open offers (any non-final state) that hold any of
// the sites, each once, except the one named.
func (p *provider) openOffersOf(ids []string, except string) ([]ownership.Offer, *refusal) {
	var out []ownership.Offer
	for _, id := range ids {
		offers, err := p.owner.OffersBySite(id)
		if err != nil {
			slog.Error("transfer: could not read the offers of a site", "site", id, "err", err)
			return nil, &refusal{retry: true}
		}
		for _, o := range offers {
			if ownership.IsFinal(o.State) || o.ID == except || slices.ContainsFunc(out, func(x ownership.Offer) bool { return x.ID == o.ID }) {
				continue
			}
			out = append(out, o)
		}
	}
	return out, nil
}

var (
	errOfferNotEditable = errors.New("the offer can no longer be changed")
	dailyLimitText      = fmt.Sprintf("You have reached today's limit of %d offers and invitations. Try again tomorrow.", maxOffersPerDay)
	msgInOtherOffer     = "A site is already in another open offer. Cancel that offer first."
)

func notEditableText(state string) string {
	switch state {
	case ownership.StateAccepting:
		return "This offer is being accepted right now and cannot be changed."
	case ownership.StateEnded:
		return "Ended: no site could be moved, so this offer can no longer be changed."
	case ownership.StateAccepted, ownership.StateDeclined, ownership.StateExpired, ownership.StateCancelled:
		return "This offer is " + state + " and can no longer be changed."
	}
	return "This offer can no longer be changed."
}

// ---- the plan for a new offer ----

type offerPlan struct {
	Sites         []string
	Email         string
	AcceptBy      time.Time
	AvailableFrom *time.Time
	// Replace is the sender's own open offers that hold one of the sites; the
	// new offer takes them over only after a confirmation.
	Replace []ownership.Offer
}

func (p *provider) planOffer(acc *account.Account, in offerInput) (*offerPlan, *refusal) {
	if ref := p.sharingOK(acc); ref != nil {
		return nil, ref
	}
	email, ok := recipientEmail(in.Email)
	if !ok {
		return nil, refuse("Enter a valid email address.")
	}
	if own, _ := recipientEmail(acc.Email); own == email {
		return nil, refuse("You cannot transfer sites to your own address.")
	}
	switch {
	case len(in.Sites) == 0:
		return nil, refuse("Choose at least one site.")
	case len(in.Sites) > maxBundle:
		return nil, refuse(fmt.Sprintf("A transfer holds at most %d sites.", maxBundle))
	}
	if ref := p.ownable(acc, in.Sites); ref != nil {
		return nil, ref
	}
	now := time.Now()
	acceptBy, availFrom, msg := parseTransferDates(in.AcceptBy, in.AvailableFrom, now)
	if msg != "" {
		return nil, refuse(msg)
	}
	if ref := p.hostRule(acc, in.Sites); ref != nil {
		return nil, ref
	}
	if p.owner.DailyCount(acc.ID, now) >= maxOffersPerDay {
		return nil, refuse(dailyLimitText)
	}
	others, ref := p.openOffersOf(in.Sites, "")
	if ref != nil {
		return nil, ref
	}
	for _, o := range others {
		if o.From != acc.ID {
			return nil, refuse(msgInOtherOffer)
		}
		if !editableOffer(o) {
			return nil, refuse(notEditableText(o.State))
		}
	}
	return &offerPlan{Sites: in.Sites, Email: email, AcceptBy: acceptBy, AvailableFrom: availFrom, Replace: others}, nil
}

// ---- views ----

type transferSite struct {
	ID      string
	Label   string
	Checked bool
	Locked  bool
}

type transferPreset struct {
	Label string
	Href  string
}

type siteLines struct {
	Site  string
	Lines []string
}

type transferNotices struct {
	NotMove []siteLines
	Forms   []siteLines
}

type transferOfferSite struct{ ID, Label string }

type transferOfferView struct {
	ID            string
	Email         string
	State         string
	AcceptBy      string
	AvailableFrom string
	Editable      bool
	Sites         []transferOfferSite
	Addable       []transferSite
}

type transferView struct {
	CSRF          string
	Error         string
	Email         string
	AcceptBy      string
	AvailableFrom string
	Sites         []transferSite
	Hosts         []transferPreset
	Tags          []transferPreset
	Offers        []transferOfferView
	Notices       transferNotices
	HasNotices    bool
}

type hiddenField struct{ Name, Value string }

type transferConfirmView struct {
	CSRF          string
	Fields        []hiddenField
	Email         string
	Sites         []transferOfferSite
	AcceptBy      string
	AvailableFrom string
	Notices       transferNotices
	Replace       []transferOldOffer
}

type transferOldOffer struct {
	Email string
	Sites []transferOfferSite
}

type transferConflictView struct {
	CSRF      string
	Action    string
	Fields    []hiddenField
	Conflicts []transferOfferSite
	Blocked   []transferOfferSite // locked conflicts: no button can resolve them
	Back      string
}

// transferAddConfirmView is the review step for sites added to an open offer.
type transferAddConfirmView struct {
	CSRF    string
	Action  string
	Fields  []hiddenField
	Email   string
	Added   []transferOfferSite
	Notices transferNotices
}

func (p *provider) labelOf(id string) string {
	if info, ok := p.host.Sites().Info(id); ok {
		return siteLabel(info)
	}
	return id
}

func (p *provider) offerSites(ids []string) []transferOfferSite {
	out := make([]transferOfferSite, 0, len(ids))
	for _, id := range ids {
		out = append(out, transferOfferSite{ID: id, Label: p.labelOf(id)})
	}
	return out
}

// noticesFor is what the sender is told about these sites before confirming:
// the addresses that will not move, and who their forms mail.
func (p *provider) noticesFor(acc *account.Account, ids []string) transferNotices {
	var n transferNotices
	mover, _ := p.host.Sites().(moveReporter)
	forms, _ := p.host.Sites().(formRecipientReader)
	for _, id := range ids {
		if mover != nil {
			if lines := mover.WillNotMove(id, acc.ID); len(lines) > 0 {
				n.NotMove = append(n.NotMove, siteLines{Site: p.labelOf(id), Lines: lines})
			}
		}
		if forms != nil {
			if lines := forms.FormRecipients(id); len(lines) > 0 {
				n.Forms = append(n.Forms, siteLines{Site: p.labelOf(id), Lines: lines})
			}
		}
	}
	return n
}

// transferForm is what the page starts from: the selection and the values.
type transferForm struct {
	Error         string
	Email         string
	AcceptBy      string
	AvailableFrom string
	Selected      []string
}

func (p *provider) renderTransferMessage(w http.ResponseWriter, status int, title, body string) {
	p.securityHeaders(w)
	w.WriteHeader(status)
	msgTmpl.Execute(w, msgView{Title: title, Body: body, Back: transferPath})
}

func (p *provider) renderTransferRetry(w http.ResponseWriter) {
	p.renderTransferMessage(w, http.StatusServiceUnavailable, "Could not check that right now", "Nothing was changed. Please try again in a minute.")
}

func (p *provider) renderTransferPlan(w http.ResponseWriter) {
	p.securityHeaders(w)
	w.WriteHeader(http.StatusForbidden)
	transferPlanTmpl.Execute(w, nil)
}

// renderRefusal answers a refusal in the shape it asks for.
func (p *provider) renderRefusal(w http.ResponseWriter, acc *account.Account, ref *refusal, f transferForm) {
	switch {
	case ref.plan:
		p.renderTransferPlan(w)
	case ref.retry:
		p.renderTransferRetry(w)
	default:
		f.Error = ref.msg
		p.renderTransfer(w, acc, ref.status, f)
	}
}

func (p *provider) renderTransfer(w http.ResponseWriter, acc *account.Account, status int, f transferForm) {
	infos, err := p.ownedSiteInfos(acc)
	if err != nil {
		p.renderTransferRetry(w)
		return
	}
	sort.Slice(infos, func(i, j int) bool {
		return strings.ToLower(siteLabel(infos[i])) < strings.ToLower(siteLabel(infos[j]))
	})
	offers, err := p.owner.OffersBySender(acc.ID)
	if err != nil {
		slog.Error("transfer: could not read the sender's offers", "account", acc.ID, "err", err)
		p.renderTransferRetry(w)
		return
	}
	v := transferView{CSRF: p.csrf(acc), Error: f.Error, Email: f.Email, AcceptBy: f.AcceptBy, AvailableFrom: f.AvailableFrom}
	inOffer := map[string]bool{}
	for _, o := range offers {
		if !ownership.IsFinal(o.State) {
			for _, id := range o.Sites {
				inOffer[id] = true
			}
		}
	}
	hosts := map[string]int{}
	tags := map[string]string{}
	var selected []string
	for _, info := range infos {
		sel := slices.Contains(f.Selected, info.ViewID) && info.Locked == nil
		if sel {
			selected = append(selected, info.ViewID)
		}
		v.Sites = append(v.Sites, transferSite{ID: info.ViewID, Label: siteLabel(info), Checked: sel, Locked: info.Locked != nil})
		seen := map[string]bool{}
		for _, d := range info.Domains {
			if h := store.HostOf(d); !seen[h] {
				seen[h] = true
				hosts[h]++
			}
		}
		for _, t := range info.Tags {
			if k := store.TagKey(t); tags[k] == "" {
				tags[k] = t
			}
		}
	}
	for _, h := range sortedStringKeys(hosts) {
		v.Hosts = append(v.Hosts, transferPreset{Label: h, Href: transferPath + "?host=" + url.QueryEscape(h)})
	}
	for _, k := range sortedStringKeys(tags) {
		v.Tags = append(v.Tags, transferPreset{Label: tags[k], Href: transferPath + "?tag=" + url.QueryEscape(tags[k])})
	}
	if len(selected) > 0 {
		v.Notices = p.noticesFor(acc, selected)
		v.HasNotices = true
	}
	for _, o := range offers {
		if ownership.IsFinal(o.State) {
			continue
		}
		ov := transferOfferView{
			ID: o.ID, Email: o.ToEmail, State: o.State, AcceptBy: dateText(o.AcceptBy),
			Editable: editableOffer(o), Sites: p.offerSites(o.Sites),
		}
		if o.AvailableFrom != nil {
			ov.AvailableFrom = dateText(*o.AvailableFrom)
		}
		if ov.Editable {
			for _, info := range infos {
				if info.Locked == nil && !inOffer[info.ViewID] {
					ov.Addable = append(ov.Addable, transferSite{ID: info.ViewID, Label: siteLabel(info)})
				}
			}
		}
		v.Offers = append(v.Offers, ov)
	}
	p.securityHeaders(w)
	w.WriteHeader(status)
	transferTmpl.Execute(w, v)
}

func sortedStringKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- handlers ----

// transferAccount is the signed-in account, or a redirect to sign in.
func (p *provider) transferAccount(w http.ResponseWriter, r *http.Request) (*account.Account, bool) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return nil, false
	}
	if p.owner == nil {
		http.NotFound(w, r)
		return nil, false
	}
	return acc, true
}

// transferPost is transferAccount for a POST: the body is bounded and the CSRF
// token is checked before anything else is looked at.
func (p *provider) transferPost(w http.ResponseWriter, r *http.Request) (*account.Account, bool) {
	acc, ok := p.transferAccount(w, r)
	if !ok {
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, transferBodyLimit)
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return acc, true
}

// handleTransferPage shows the form, preselected from ?site=, ?host= or ?tag=,
// and the sender's open offers.
func (p *provider) handleTransferPage(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.transferAccount(w, r)
	if !ok {
		return
	}
	if ref := p.sharingOK(acc); ref != nil {
		p.renderRefusal(w, acc, ref, transferForm{})
		return
	}
	infos, err := p.ownedSiteInfos(acc)
	if err != nil {
		p.renderTransferRetry(w)
		return
	}
	q := r.URL.Query()
	var sel []string
	add := func(id string) {
		if !slices.Contains(sel, id) {
			sel = append(sel, id)
		}
	}
	host := strings.ToLower(strings.TrimSpace(q.Get("host")))
	tag := store.TagKey(q.Get("tag"))
	for _, info := range infos {
		if info.ViewID == q.Get("site") {
			add(info.ViewID)
		}
		if host != "" {
			for _, d := range info.Domains {
				if store.HostOf(d) == host {
					add(info.ViewID)
				}
			}
		}
		if q.Get("tag") != "" {
			for _, t := range info.Tags {
				if store.TagKey(t) == tag {
					add(info.ViewID)
				}
			}
		}
	}
	p.renderTransfer(w, acc, http.StatusOK, transferForm{Selected: sel})
}

func fieldsOf(in offerInput, sites []string) []hiddenField {
	fs := make([]hiddenField, 0, len(sites)+3)
	for _, id := range sites {
		fs = append(fs, hiddenField{"site", id})
	}
	return append(fs, hiddenField{"email", in.Email}, hiddenField{"accept_by", in.AcceptBy}, hiddenField{"available_from", in.AvailableFrom})
}

// handleTransferCreate is the create form. It has three steps, chosen by the
// hidden field "step": "form" re-renders the form with the posted values
// (the "Add them to the bundle" button and "Change" use it); the default
// reviews (every rule, then a confirm page with the notices); "create"
// creates, and replaces the sender's older offers for the same sites only
// with replace=1.
func (p *provider) handleTransferCreate(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.transferPost(w, r)
	if !ok {
		return
	}
	in := readOfferInput(r)
	step := r.PostFormValue("step")
	f := transferForm{Email: in.Email, AcceptBy: in.AcceptBy, AvailableFrom: in.AvailableFrom, Selected: in.Sites}
	if step == "form" {
		if ref := p.sharingOK(acc); ref != nil {
			p.renderRefusal(w, acc, ref, f)
			return
		}
		p.renderTransfer(w, acc, http.StatusOK, f)
		return
	}
	plan, ref := p.planOffer(acc, in)
	if ref != nil {
		if len(ref.conflicts) > 0 {
			p.renderCreateConflict(w, acc, in, ref)
			return
		}
		p.renderRefusal(w, acc, ref, f)
		return
	}
	replaceOK := r.PostFormValue("replace") == "1"
	if step != "create" || (len(plan.Replace) > 0 && !replaceOK) {
		status := http.StatusOK
		if step == "create" {
			status = http.StatusConflict
		}
		p.renderConfirm(w, acc, in, plan, status)
		return
	}
	o, ok := p.createOffer(w, acc, in, plan)
	if !ok {
		return
	}
	// The recipient is mailed in the background when the offer is already
	// available; a future available-from waits for the loop. Either way the
	// sender gets the one answer.
	if o.AvailableFrom == nil || !o.AvailableFrom.After(time.Now()) {
		p.notifyOffer(o)
	}
	p.renderTransferMessage(w, http.StatusOK, "Transfer offered", transferAnswer)
}

// createOffer cancels the replaced offers and creates the new one. If the
// create fails after a cancel, the cancelled offers are put back as they were.
func (p *provider) createOffer(w http.ResponseWriter, acc *account.Account, in offerInput, plan *offerPlan) (ownership.Offer, bool) {
	type cancelled struct{ id, prev string }
	var done []cancelled
	restore := func() {
		for _, c := range done {
			if _, err := p.owner.UpdateOffer(c.id, func(o *ownership.Offer) error { o.State = c.prev; return nil }); err != nil {
				slog.Error("transfer: could not put a replaced offer back", "offer", c.id, "err", err)
			}
		}
	}
	changed := func() {
		restore()
		p.renderTransferMessage(w, http.StatusConflict, "The offers changed", "One of the sites changed while you were confirming. Nothing was changed. Please review the offer again.")
	}
	for _, old := range plan.Replace {
		var prev string
		_, err := p.owner.UpdateOffer(old.ID, func(o *ownership.Offer) error {
			if !editableOffer(*o) || o.From != acc.ID {
				return errOfferNotEditable
			}
			prev = o.State
			o.State = ownership.StateCancelled
			return nil
		})
		if err != nil {
			if !errors.Is(err, errOfferNotEditable) {
				slog.Error("transfer: could not cancel a replaced offer", "offer", old.ID, "err", err)
			}
			changed()
			return ownership.Offer{}, false
		}
		done = append(done, cancelled{old.ID, prev})
	}
	o, err := p.owner.CreateOffer(ownership.Offer{
		Sites: plan.Sites, From: acc.ID, ToEmail: plan.Email,
		AvailableFrom: plan.AvailableFrom, AcceptBy: plan.AcceptBy, State: ownership.StateOpen,
	}, maxOffersPerDay)
	if err != nil {
		if errors.Is(err, ownership.ErrDailyLimit) {
			restore()
			p.renderTransfer(w, acc, http.StatusBadRequest, transferForm{Error: dailyLimitText, Email: in.Email, AcceptBy: in.AcceptBy, AvailableFrom: in.AvailableFrom, Selected: in.Sites})
			return ownership.Offer{}, false
		}
		if errors.Is(err, ownership.ErrSiteOffered) {
			changed()
			return ownership.Offer{}, false
		}
		slog.Error("transfer: could not create the offer", "account", acc.ID, "err", err)
		restore()
		p.renderTransferRetry(w)
		return ownership.Offer{}, false
	}
	return o, true
}

func (p *provider) renderConfirm(w http.ResponseWriter, acc *account.Account, in offerInput, plan *offerPlan, status int) {
	v := transferConfirmView{
		CSRF: p.csrf(acc), Fields: fieldsOf(in, plan.Sites), Email: plan.Email,
		Sites: p.offerSites(plan.Sites), AcceptBy: dateText(plan.AcceptBy),
		Notices: p.noticesFor(acc, plan.Sites),
	}
	if plan.AvailableFrom != nil {
		v.AvailableFrom = dateText(*plan.AvailableFrom)
	}
	for _, o := range plan.Replace {
		v.Replace = append(v.Replace, transferOldOffer{Email: o.ToEmail, Sites: p.offerSites(o.Sites)})
	}
	p.securityHeaders(w)
	w.WriteHeader(status)
	transferConfirmTmpl.Execute(w, v)
}

func (p *provider) renderCreateConflict(w http.ResponseWriter, acc *account.Account, in offerInput, ref *refusal) {
	all := slices.Clone(in.Sites)
	for _, c := range ref.conflicts {
		if !slices.Contains(all, c) {
			all = append(all, c)
		}
	}
	p.renderConflict(w, acc, transferPath, append(fieldsOf(in, all), hiddenField{"step", "form"}), ref)
}

func (p *provider) renderConflict(w http.ResponseWriter, acc *account.Account, action string, fields []hiddenField, ref *refusal) {
	p.securityHeaders(w)
	w.WriteHeader(http.StatusConflict)
	transferConflictTmpl.Execute(w, transferConflictView{
		CSRF: p.csrf(acc), Action: action, Fields: fields,
		Conflicts: p.offerSites(ref.conflicts), Blocked: p.offerSites(ref.blocked), Back: transferPath,
	})
}

// manageOffer is the start of every route on an existing offer: the account,
// the CSRF token, the offer (404 for anyone but its sender) and the check that
// it can still change.
func (p *provider) manageOffer(w http.ResponseWriter, r *http.Request) (*account.Account, ownership.Offer, bool) {
	acc, ok := p.transferPost(w, r)
	if !ok {
		return nil, ownership.Offer{}, false
	}
	o, err := p.owner.GetOffer(r.PathValue("id"))
	if err != nil || o.From != acc.ID {
		if err != nil && !errors.Is(err, ownership.ErrNotFound) {
			slog.Error("transfer: could not read an offer", "err", err)
		}
		http.NotFound(w, r)
		return nil, ownership.Offer{}, false
	}
	if !editableOffer(o) {
		p.renderTransferMessage(w, http.StatusConflict, "The offer cannot be changed", notEditableText(o.State))
		return nil, ownership.Offer{}, false
	}
	return acc, o, true
}

// editOffer applies a change under the store's lock, re-checking that the
// offer is still the sender's and still editable. ok is false when a response
// was written.
func (p *provider) editOffer(w http.ResponseWriter, acc *account.Account, id string, change func(*ownership.Offer) error) bool {
	_, err := p.owner.UpdateOffer(id, func(o *ownership.Offer) error {
		if o.From != acc.ID || !editableOffer(*o) {
			return errOfferNotEditable
		}
		return change(o)
	})
	switch {
	case err == nil:
		return true
	case errors.Is(err, errOfferNotEditable):
		p.renderTransferMessage(w, http.StatusConflict, "The offer cannot be changed", "This offer can no longer be changed.")
	case errors.Is(err, ownership.ErrSiteOffered):
		p.renderTransfer(w, acc, http.StatusBadRequest, transferForm{Error: msgInOtherOffer})
	default:
		slog.Error("transfer: could not update an offer", "offer", id, "err", err)
		p.renderTransferRetry(w)
	}
	return false
}

func (p *provider) handleTransferCancel(w http.ResponseWriter, r *http.Request) {
	acc, o, ok := p.manageOffer(w, r)
	if !ok {
		return
	}
	if p.editOffer(w, acc, o.ID, func(x *ownership.Offer) error { x.State = ownership.StateCancelled; return nil }) {
		p.redirect(w, r, transferPath)
	}
}

func (p *provider) handleTransferDates(w http.ResponseWriter, r *http.Request) {
	acc, o, ok := p.manageOffer(w, r)
	if !ok {
		return
	}
	acceptBy, availFrom, msg := parseTransferDates(
		strings.TrimSpace(r.PostFormValue("accept_by")), strings.TrimSpace(r.PostFormValue("available_from")), time.Now())
	if msg != "" {
		p.renderTransfer(w, acc, http.StatusBadRequest, transferForm{Error: msg})
		return
	}
	if p.editOffer(w, acc, o.ID, func(x *ownership.Offer) error {
		x.AcceptBy, x.AvailableFrom = acceptBy, availFrom
		return nil
	}) {
		p.redirect(w, r, transferPath)
	}
}

// bundleAfter is the bundle with removals dropped and additions appended.
func bundleAfter(cur, add, remove []string) []string {
	out := make([]string, 0, len(cur)+len(add))
	for _, id := range cur {
		if !slices.Contains(remove, id) {
			out = append(out, id)
		}
	}
	for _, id := range add {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func (p *provider) handleTransferSites(w http.ResponseWriter, r *http.Request) {
	acc, o, ok := p.manageOffer(w, r)
	if !ok {
		return
	}
	add, remove := uniq(r.PostForm["add"]), uniq(r.PostForm["remove"])
	after := bundleAfter(o.Sites, add, remove)
	fail := func(ref *refusal) {
		if len(ref.conflicts) > 0 {
			fs := []hiddenField{}
			for _, id := range bundleAfter(add, ref.conflicts, nil) {
				fs = append(fs, hiddenField{"add", id})
			}
			for _, id := range remove {
				if !slices.Contains(ref.conflicts, id) {
					fs = append(fs, hiddenField{"remove", id})
				}
			}
			p.renderConflict(w, acc, transferPath+"/"+o.ID+"/sites", fs, ref)
			return
		}
		p.renderRefusal(w, acc, ref, transferForm{})
	}
	switch {
	case len(after) == 0:
		fail(refuse("An offer needs at least one site. Cancel the offer instead."))
		return
	case len(after) > maxBundle:
		fail(refuse(fmt.Sprintf("A transfer holds at most %d sites.", maxBundle)))
		return
	}
	if len(add) > 0 {
		if ref := p.ownable(acc, add); ref != nil {
			fail(ref)
			return
		}
		others, ref := p.openOffersOf(add, o.ID)
		if ref != nil {
			fail(ref)
			return
		}
		if len(others) > 0 {
			fail(refuse(msgInOtherOffer))
			return
		}
	}
	if ref := p.hostRule(acc, after); ref != nil {
		fail(ref)
		return
	}
	if len(add) > 0 && r.PostFormValue("confirm") != "1" {
		// Sites joining an open offer get the same review as a new one: what
		// will not move, who their forms mail, what does not travel.
		fs := []hiddenField{{"confirm", "1"}}
		for _, id := range add {
			fs = append(fs, hiddenField{"add", id})
		}
		for _, id := range remove {
			fs = append(fs, hiddenField{"remove", id})
		}
		p.securityHeaders(w)
		transferAddConfirmTmpl.Execute(w, transferAddConfirmView{
			CSRF: p.csrf(acc), Action: transferPath + "/" + o.ID + "/sites", Fields: fs,
			Email: o.ToEmail, Added: p.offerSites(add), Notices: p.noticesFor(acc, after),
		})
		return
	}
	if p.editOffer(w, acc, o.ID, func(x *ownership.Offer) error {
		next := bundleAfter(x.Sites, add, remove)
		if len(next) == 0 || len(next) > maxBundle {
			return fmt.Errorf("%w: the bundle would hold %d sites", errOfferNotEditable, len(next))
		}
		x.Sites = next
		return nil
	}) {
		p.redirect(w, r, transferPath)
	}
}
