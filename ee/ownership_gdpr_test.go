//go:build ee

package ee

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/ownership"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// ownSites is the fake core with a site log and a SetAdmins that can be made
// to fail, for the account-deletion tests.
type ownSites struct {
	*provSites
	setErr error
}

func (s *ownSites) SetAdmins(id, owner string, by ext.ChangeBy, edit func([]ext.SiteAdmin) ([]ext.SiteAdmin, error)) error {
	if s.setErr != nil {
		return s.setErr
	}
	return s.provSites.SetAdmins(id, owner, by, edit)
}

type ownHost struct {
	*fakeHost
	sites *ownSites
}

func (h *ownHost) Sites() ext.SiteService { return h.sites }

type gdprEnv struct {
	*shareEnv
	sites *ownSites
}

func newGDPREnv(t *testing.T) *gdprEnv {
	t.Helper()
	e := &gdprEnv{shareEnv: newShareEnv(t)}
	e.p.cfg.GDPRSecret = testGDPRSecret
	e.sites = &ownSites{provSites: &provSites{fakeSites: e.fs}}
	e.p.host = &ownHost{fakeHost: e.p.host.(*fakeHost), sites: e.sites}
	return e
}

func (e *gdprEnv) oidc(sub, email string) *account.Account {
	e.t.Helper()
	acc, err := e.p.accounts.CreateOAuth(account.OIDCProv, sub, email, true, "pro")
	if err != nil {
		e.t.Fatal(err)
	}
	return acc
}

// own makes the account the owner of a new site.
func (e *gdprEnv) own(acc *account.Account, id string) {
	e.t.Helper()
	e.fs.infos[id] = ext.SiteInfo{ViewID: id, Mode: "webserver", Owner: acc.ID, ViewURL: "https://" + id + ".sitebin.app"}
	if err := linkOwned(e.p, acc, id); err != nil {
		e.t.Fatal(err)
	}
}

// makeAdmin lists admin on the site and links the share marker, as an
// accepted invitation does.
func (e *gdprEnv) makeAdmin(owner, admin *account.Account, id string, until *time.Time) {
	e.t.Helper()
	err := e.fs.SetAdmins(id, owner.ID, ext.ChangeBy{}, func(a []ext.SiteAdmin) ([]ext.SiteAdmin, error) {
		return append(a, ext.SiteAdmin{Account: admin.ID, Until: until}), nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.p.owner.LinkShare(admin.ID, id); err != nil {
		e.t.Fatal(err)
	}
}

func (e *gdprEnv) tag(acc *account.Account, site string) {
	e.t.Helper()
	err := e.p.accounts.UpdateTags(acc.ID, func(r *account.TagRegistry) error {
		if r.Sites == nil {
			r.Sites = map[string][]string{}
		}
		r.Sites[site] = []string{"mine"}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *gdprEnv) gdprDelete(acc *account.Account) *httptest.ResponseRecorder {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.p.handleGDPRDelete(w, stackOrder(gdprDeletePath, testGDPRSecret, time.Now(), `{"userId":"`+acc.OAuthSubject+`"}`))
	return w
}

func (e *gdprEnv) gdprExport(acc *account.Account) (string, map[string]any) {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.p.handleGDPRExport(w, stackOrder(gdprExportPath, testGDPRSecret, time.Now(), `{"userId":"`+acc.OAuthSubject+`"}`))
	if w.Code != 200 {
		e.t.Fatalf("export = %d (%s)", w.Code, w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return w.Body.String(), out
}

func (e *gdprEnv) localDelete(c *http.Cookie, acc *account.Account) *httptest.ResponseRecorder {
	return e.postTo(c, acc, "/account/delete/confirm", url.Values{})
}

func (e *gdprEnv) gone(acc *account.Account) bool {
	_, err := e.p.accounts.ByID(acc.ID)
	return errors.Is(err, account.ErrNotFound)
}

func (e *gdprEnv) lock(id string, hold bool) {
	l := &ext.SiteLock{At: time.Now().Add(-time.Hour), By: ext.LockByAdmin, Reason: "abuse"}
	if hold {
		l.Hold = &ext.LockHold{At: time.Now(), By: "cli"}
	}
	info := e.fs.infos[id]
	info.Locked = l
	e.fs.infos[id] = info
}

func (e *gdprEnv) offerFrom(from *account.Account, to string, notified bool, sites ...string) ownership.Offer {
	e.t.Helper()
	o := ownership.Offer{Sites: sites, From: from.ID, ToEmail: to, AcceptBy: time.Now().Add(10 * 24 * time.Hour), State: ownership.StateOpen}
	if notified {
		now := time.Now()
		o.NotifiedAt = &now
	}
	o, err := e.p.owner.CreateOffer(o, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return o
}

func (e *gdprEnv) inviteFrom(from *account.Account, to, site, state string) ownership.Invite {
	e.t.Helper()
	inv, err := e.p.owner.CreateInvite(ownership.Invite{Site: site, From: from.ID, ToEmail: to, State: state}, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	if state != ownership.StateOpen {
		inv, err = e.p.owner.UpdateInvite(inv.ID, func(i *ownership.Invite) error { i.State = state; return nil })
		if err != nil {
			e.t.Fatal(err)
		}
	}
	return inv
}

func (e *gdprEnv) offerState(o ownership.Offer) string {
	e.t.Helper()
	return e.current(o).State
}

func (e *gdprEnv) siteLog(id string) []provenance.Entry {
	es, _ := e.sites.SiteProvenance(id)
	return es
}

// ---- the export ----

func listOf(t *testing.T, m map[string]any, keys ...string) []map[string]any {
	t.Helper()
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("no %v in the export", keys)
		}
		cur = mm[k]
	}
	arr, ok := cur.([]any)
	if !ok {
		t.Fatalf("%v is not a list: %T", keys, cur)
	}
	var out []map[string]any
	for _, x := range arr {
		out = append(out, x.(map[string]any))
	}
	return out
}

func TestExportCarriesOffersInvitationsAndShares(t *testing.T) {
	e := newGDPREnv(t)
	owner := e.oidc("sub-owner", "owner@example.com")
	rcpt := e.oidc("sub-rcpt", "rcpt@example.com")
	third := e.oidc("sub-third", "third@example.com")
	const s1, s2, s3 = "aaaaaaaaaaaaaaaaaaaaaaaaba", "aaaaaaaaaaaaaaaaaaaaaaaabb", "aaaaaaaaaaaaaaaaaaaaaaaabc"
	e.own(owner, s1)
	e.own(owner, s2)
	e.own(third, s3)
	until := inDays(20)
	e.inviteFrom(owner, "rcpt@example.com", s1, ownership.StateAccepted)
	e.makeAdmin(owner, rcpt, s1, until)

	sent := e.offerFrom(owner, "rcpt@example.com", true, s2)
	e.own(owner, "aaaaaaaaaaaaaaaaaaaaaaaabs")
	e.offerFrom(owner, "stranger@example.com", false, "aaaaaaaaaaaaaaaaaaaaaaaabs")
	// A third party's offer to the recipient that never reached them.
	e.offerFrom(third, "rcpt@example.com", false, s3)
	// An accepting offer: the sender must not learn who accepted.
	e.own(owner, "aaaaaaaaaaaaaaaaaaaaaaaabd")
	acc2 := e.offerFrom(owner, "somebody@example.com", true, "aaaaaaaaaaaaaaaaaaaaaaaabd")
	e.change(acc2, func(o *ownership.Offer) { o.State, o.AcceptedBy = ownership.StateAccepting, rcpt.ID })

	// The sender's export.
	raw, out := e.gdprExport(owner)
	offers := listOf(t, out, "ownership", "offers_sent")
	if len(offers) != 3 {
		t.Fatalf("offers_sent = %d, want 3: %s", len(offers), raw)
	}
	var found bool
	for _, o := range offers {
		if o["id"] == sent.ID && o["to_email"] == "rcpt@example.com" && o["state"] == "open" {
			found = true
		}
	}
	if !found {
		t.Errorf("the sent offer is missing its address or state: %v", offers)
	}
	if inv := listOf(t, out, "ownership", "invitations_sent"); len(inv) != 1 || inv[0]["to_email"] != "rcpt@example.com" {
		t.Errorf("invitations_sent = %v", inv)
	}
	admins := listOf(t, out, "ownership", "admins_of_my_sites")
	if len(admins) != 1 || admins[0]["email"] != "rcpt@example.com" || admins[0]["site"] != s1 || admins[0]["until"] == nil {
		t.Errorf("admins_of_my_sites = %v", admins)
	}
	for _, leak := range []string{rcpt.ID, "accepted_by", "third@example.com", third.ID} {
		if strings.Contains(raw, leak) {
			t.Errorf("the sender's export carries %q, which it never knew", leak)
		}
	}

	// The recipient's export.
	raw, out = e.gdprExport(rcpt)
	recv := listOf(t, out, "ownership", "offers_received")
	if len(recv) != 2 {
		t.Fatalf("offers_received = %v", recv)
	}
	var fromOwner, fromThird map[string]any
	for _, o := range recv {
		switch o["id"] {
		case sent.ID:
			fromOwner = o
		default:
			fromThird = o
		}
	}
	if fromOwner == nil || fromOwner["from_email"] != "owner@example.com" {
		t.Errorf("a received offer must show its sender: %v", recv)
	}
	if fromThird == nil || fromThird["from_email"] != nil && fromThird["from_email"] != "" {
		t.Errorf("an offer that never reached the account must not show its sender: %v", fromThird)
	}
	if strings.Contains(raw, "third@example.com") {
		t.Error("an unseen offer leaked its sender's address")
	}
	if inv := listOf(t, out, "ownership", "invitations_received"); len(inv) != 1 || inv[0]["from_email"] != "owner@example.com" {
		t.Errorf("invitations_received = %v", inv)
	}
	shared := listOf(t, out, "ownership", "shared_with_me")
	if len(shared) != 1 || shared[0]["site"] != s1 || shared[0]["owner_email"] != "owner@example.com" || shared[0]["until"] == nil {
		t.Errorf("shared_with_me = %v", shared)
	}
}

func TestExportOwnershipPartsAreEmptyListsForAPlainAccount(t *testing.T) {
	e := newGDPREnv(t)
	acc := e.oidc("sub-plain", "plain@example.com")
	raw, out := e.gdprExport(acc)
	for _, k := range []string{"offers_sent", "offers_received", "invitations_sent", "invitations_received", "shared_with_me", "admins_of_my_sites"} {
		if l := listOf(t, out, "ownership", k); len(l) != 0 {
			t.Errorf("%s = %v", k, l)
		}
	}
	if !strings.Contains(raw, `"offers_sent":[]`) {
		t.Errorf("empty lists must be [] not null: %s", raw)
	}
}

// ---- deleting an admin account ----

func TestGDPRDeleteOfAnAdminEndsItsShares(t *testing.T) {
	e := newGDPREnv(t)
	owner := e.oidc("sub-owner", "owner@example.com")
	adm := e.oidc("sub-adm", "adm@example.com")
	const s1 = "aaaaaaaaaaaaaaaaaaaaaaaaba"
	e.own(owner, s1)
	e.makeAdmin(owner, adm, s1, nil)
	e.tag(adm, s1)

	if w := e.gdprDelete(adm); w.Code != 200 {
		t.Fatalf("delete = %d (%s)", w.Code, w.Body)
	}
	if as, _ := e.fs.SiteAdmins(s1); len(as) != 0 {
		t.Errorf("the deleted admin is still listed: %v", as)
	}
	if ids, _ := e.p.owner.SharedSites(adm.ID); len(ids) != 0 {
		t.Errorf("share markers remain: %v", ids)
	}
	if _, err := e.p.owner.SharingAccounts(); err != nil {
		t.Fatal(err)
	}
	if accs, _ := e.p.owner.SharingAccounts(); contains(accs, adm.ID) {
		t.Error("the account's share directory remains")
	}
	if !e.gone(adm) {
		t.Error("the account survived")
	}
	if n := len(e.mailsTo("owner@example.com", "A Sitebin share has ended")); n != 1 {
		t.Errorf("owner mails = %d, want 1", n)
	}
}

func TestLocalDeleteOfAnAdminEndsItsShares(t *testing.T) {
	e := newGDPREnv(t)
	e.shared(nil)
	e.tag(e.to, shareSite)
	if w := e.localDelete(e.toCookie, e.to); w.Code != 200 {
		t.Fatalf("delete = %d (%s)", w.Code, w.Body)
	}
	if as := e.admins(shareSite); len(as) != 0 {
		t.Errorf("admins = %v", as)
	}
	if ids, _ := e.p.owner.SharedSites(e.to.ID); len(ids) != 0 {
		t.Errorf("markers = %v", ids)
	}
	if n := len(e.mailsTo("pro@example.com", "A Sitebin share has ended")); n != 1 {
		t.Errorf("owner mails = %d, want 1", n)
	}
	if !e.gone(e.to) {
		t.Error("the account survived")
	}
}

func TestDeleteOfAnAdminOfALockedSiteKeepsTheEntryAndRecordsIt(t *testing.T) {
	for _, mode := range []string{"gdpr", "local"} {
		t.Run(mode, func(t *testing.T) {
			e := newGDPREnv(t)
			var adm *account.Account
			var cookie *http.Cookie
			if mode == "gdpr" {
				ownerAcc := e.oidc("sub-owner", "owner@example.com")
				adm = e.oidc("sub-adm", "adm@example.com")
				e.own(ownerAcc, "aaaaaaaaaaaaaaaaaaaaaaaaba")
				e.makeAdmin(ownerAcc, adm, "aaaaaaaaaaaaaaaaaaaaaaaaba", nil)
			} else {
				adm, cookie = e.to, e.toCookie
				e.shared(nil)
			}
			site := "aaaaaaaaaaaaaaaaaaaaaaaaba"
			if mode == "local" {
				site = shareSite
			}
			e.lock(site, false)

			if locked, _, err := e.p.lockHolds(adm); err != nil || !locked {
				t.Fatalf("lockHolds(admin) = %v, %v; an admin of a locked site is involved", locked, err)
			}
			var w *httptest.ResponseRecorder
			if mode == "gdpr" {
				w = e.gdprDelete(adm)
			} else {
				w = e.localDelete(cookie, adm)
			}
			if w.Code != 200 {
				t.Fatalf("the deletion was blocked: %d (%s)", w.Code, w.Body)
			}
			if !e.gone(adm) {
				t.Error("the account survived")
			}
			as, _ := e.fs.SiteAdmins(site)
			if len(as) != 1 || as[0].Account != adm.ID {
				t.Errorf("the entry on a locked site must stay: %v", as)
			}
			var lines []provenance.Entry
			for _, en := range e.siteLog(site) {
				if en.Action == provenance.ActionAdmins {
					lines = append(lines, en)
				}
			}
			if len(lines) != 1 {
				t.Fatalf("provenance lines on the locked site = %d, want 1: %+v", len(lines), e.siteLog(site))
			}
			if !strings.Contains(lines[0].Detail, adm.ID) || !strings.Contains(lines[0].Detail, adm.Email) {
				t.Errorf("the line must carry the id and the email: %q", lines[0].Detail)
			}
		})
	}
}

func TestDeleteStopsWhenAShareCannotBeEnded(t *testing.T) {
	for _, mode := range []string{"gdpr", "local"} {
		t.Run(mode, func(t *testing.T) {
			e := newGDPREnv(t)
			var adm *account.Account
			var cookie *http.Cookie
			site := shareSite
			if mode == "gdpr" {
				owner := e.oidc("sub-owner", "owner@example.com")
				adm = e.oidc("sub-adm", "adm@example.com")
				site = "aaaaaaaaaaaaaaaaaaaaaaaaba"
				e.own(owner, site)
				e.makeAdmin(owner, adm, site, nil)
			} else {
				e.shared(nil)
				adm, cookie = e.to, e.toCookie
			}
			open := e.inviteFrom(adm, "x@example.com", site, ownership.StateOpen)
			e.sites.setErr = errors.New("disk on fire")

			var w *httptest.ResponseRecorder
			if mode == "gdpr" {
				w = e.gdprDelete(adm)
			} else {
				w = e.localDelete(cookie, adm)
			}
			if w.Code < 500 {
				t.Fatalf("status = %d, want 5xx (%s)", w.Code, w.Body)
			}
			if e.gone(adm) {
				t.Fatal("the account was deleted while a share may still grant access")
			}
			if ids, _ := e.p.owner.SharedSites(adm.ID); len(ids) != 1 {
				t.Errorf("the marker must stay: %v", ids)
			}
			if cur, _ := e.p.owner.GetInvite(open.ID); cur.State != ownership.StateOpen {
				t.Errorf("nothing else may be cancelled after the failure, invite = %s", cur.State)
			}
			// The fault clears and the retry completes.
			e.sites.setErr = nil
			if mode == "gdpr" {
				w = e.gdprDelete(adm)
			} else {
				w = e.localDelete(cookie, adm)
			}
			if w.Code != 200 || !e.gone(adm) {
				t.Fatalf("retry = %d, gone=%v", w.Code, e.gone(adm))
			}
			if as, _ := e.fs.SiteAdmins(site); len(as) != 0 {
				t.Errorf("admins after the retry = %v", as)
			}
		})
	}
}

func TestNotOwnerAndGoneSkipTheShare(t *testing.T) {
	e := newGDPREnv(t)
	owner := e.oidc("sub-owner", "owner@example.com")
	adm := e.oidc("sub-adm", "adm@example.com")
	e.own(owner, "aaaaaaaaaaaaaaaaaaaaaaaaba")
	e.makeAdmin(owner, adm, "aaaaaaaaaaaaaaaaaaaaaaaaba", nil)
	// A marker whose site is gone.
	if err := e.p.owner.LinkShare(adm.ID, "aaaaaaaaaaaaaaaaaaaaaaaabz"); err != nil {
		t.Fatal(err)
	}
	e.sites.setErr = ext.ErrNotOwner
	if w := e.gdprDelete(adm); w.Code != 200 {
		t.Fatalf("a not-owner answer is a skip: %d (%s)", w.Code, w.Body)
	}
	if !e.gone(adm) {
		t.Error("the account survived")
	}
}

// ---- offers and invitations ----

func TestDeleteCancelsOffersAndInvitationsFromAndToTheAccount(t *testing.T) {
	e := newGDPREnv(t)
	// e.to (local, takerEmail) is deleted. It owns a site it offered; e.acc
	// offered it a site and invited it.
	const mine, theirs, accepting = "aaaaaaaaaaaaaaaaaaaaaaaabe", "aaaaaaaaaaaaaaaaaaaaaaaabf", "aaaaaaaaaaaaaaaaaaaaaaaabg"
	e.own(e.to, mine)
	e.site(theirs)
	e.site(accepting)

	fromMe, err := e.p.owner.CreateOffer(ownership.Offer{Sites: []string{mine}, From: e.to.ID, ToEmail: "friend@example.com", AcceptBy: time.Now().Add(48 * time.Hour), State: ownership.StateOpen}, 0)
	if err != nil {
		t.Fatal(err)
	}
	toMe := e.offer(theirs)
	e.change(toMe, func(o *ownership.Offer) { now := time.Now(); o.NotifiedAt = &now })
	pausedToMe := e.offer(accepting)
	e.change(pausedToMe, func(o *ownership.Offer) { o.State = ownership.StatePaused })
	inviteFromMe := e.inviteFrom(e.to, "friend@example.com", mine, ownership.StateOpen)
	inviteToMe := e.inviteFrom(e.acc, takerEmail, shareSite, ownership.StateOpen)
	acceptedInvite := e.inviteFrom(e.acc, takerEmail, shareSite, ownership.StateAccepted)

	if w := e.localDelete(e.toCookie, e.to); w.Code != 200 {
		t.Fatalf("delete = %d (%s)", w.Code, w.Body)
	}
	for name, got := range map[string]string{
		"offer from the account":   e.offerState(fromMe),
		"offer to its email":       e.offerState(toMe),
		"paused offer to its mail": e.offerState(pausedToMe),
	} {
		if got != ownership.StateCancelled {
			t.Errorf("%s = %s, want cancelled", name, got)
		}
	}
	for name, inv := range map[string]ownership.Invite{"invitation from": inviteFromMe, "invitation to": inviteToMe} {
		if cur, _ := e.p.owner.GetInvite(inv.ID); cur.State != ownership.StateCancelled {
			t.Errorf("%s = %s, want cancelled", name, cur.State)
		}
	}
	if cur, _ := e.p.owner.GetInvite(acceptedInvite.ID); cur.State != ownership.StateAccepted {
		t.Errorf("a final invitation was touched: %s", cur.State)
	}
	// The sender of the offer made to it is told; the text names no reason.
	ms := e.mailsTo("pro@example.com", "A Sitebin transfer was cancelled")
	if len(ms) == 0 {
		t.Fatal("the sender of a cancelled offer was not told")
	}
	for _, m := range ms {
		if strings.Contains(strings.ToLower(m), "delet") {
			t.Errorf("the mail names the reason:\n%s", m)
		}
	}
}

func TestAnAcceptingOfferIsLeftToCompleteAcceptingWhichRefusesTheDeletedRecipient(t *testing.T) {
	e := newGDPREnv(t)
	const moving = "aaaaaaaaaaaaaaaaaaaaaaaabh"
	e.site(moving)
	o := e.offer(moving)
	o = e.change(o, func(o *ownership.Offer) { o.State, o.AcceptedBy = ownership.StateAccepting, e.to.ID })

	if w := e.localDelete(e.toCookie, e.to); w.Code != 200 {
		t.Fatalf("delete = %d (%s)", w.Code, w.Body)
	}
	if got := e.offerState(o); got != ownership.StateAccepting {
		t.Fatalf("the deletion touched an accepting offer: %s", got)
	}
	e.p.ownershipTick(time.Now())
	cur := e.current(o)
	if cur.State != ownership.StateEnded {
		t.Fatalf("offer state = %s, want ended: the sites stay with the sender", cur.State)
	}
	if owner, _ := e.fs.SiteOwner(moving); owner != e.acc.ID {
		t.Errorf("the site moved to a deleted account: owner %q", owner)
	}
}

// ---- deleting an owner ----

func TestDeleteOfAnOwnerEndsItsSitesSharesAndCleansTheAdmins(t *testing.T) {
	e := newGDPREnv(t)
	e.shared(nil)
	e.tag(e.to, shareSite)
	if w := e.localDelete(e.cookie, e.acc); w.Code != 200 {
		t.Fatalf("delete = %d (%s)", w.Code, w.Body)
	}
	if _, ok := e.fs.infos[shareSite]; ok {
		t.Fatal("the site survived its owner")
	}
	if ids, _ := e.p.owner.SharedSites(e.to.ID); len(ids) != 0 {
		t.Errorf("the former admin's marker remains: %v", ids)
	}
	reg, err := e.p.accounts.Tags(e.to.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Sites[shareSite]; ok {
		t.Errorf("the former admin's tags for the site remain: %v", reg.Sites)
	}
	if n := len(e.mailsTo(takerEmail, "A Sitebin share has ended")); n != 1 {
		t.Errorf("the former admin mails = %d, want 1", n)
	}
}

func TestGDPRDeleteOfAnOwnerCleansItsAdmins(t *testing.T) {
	e := newGDPREnv(t)
	owner := e.oidc("sub-owner", "owner@example.com")
	adm := e.oidc("sub-adm", "adm@example.com")
	e.own(owner, "aaaaaaaaaaaaaaaaaaaaaaaaba")
	e.makeAdmin(owner, adm, "aaaaaaaaaaaaaaaaaaaaaaaaba", nil)
	e.tag(adm, "aaaaaaaaaaaaaaaaaaaaaaaaba")
	if w := e.gdprDelete(owner); w.Code != 200 {
		t.Fatalf("delete = %d (%s)", w.Code, w.Body)
	}
	if ids, _ := e.p.owner.SharedSites(adm.ID); len(ids) != 0 {
		t.Errorf("the admin's marker remains: %v", ids)
	}
	if reg, _ := e.p.accounts.Tags(adm.ID); len(reg.Sites) != 0 {
		t.Errorf("the admin's tags remain: %v", reg.Sites)
	}
}

// ---- lockHolds ----

func TestLockHoldsCountsAnAdminOfALockedSite(t *testing.T) {
	e := newGDPREnv(t)
	owner := e.oidc("sub-owner", "owner@example.com")
	adm := e.oidc("sub-adm", "adm@example.com")
	bystander := e.oidc("sub-by", "by@example.com")
	const s1 = "aaaaaaaaaaaaaaaaaaaaaaaaba"
	e.own(owner, s1)
	e.makeAdmin(owner, adm, s1, nil)

	locked, hold, err := e.p.lockHolds(adm)
	if err != nil || locked || hold {
		t.Fatalf("an unlocked site holds nothing: %v %v %v", locked, hold, err)
	}
	e.lock(s1, false)
	if locked, hold, err = e.p.lockHolds(adm); err != nil || !locked || hold {
		t.Errorf("admin of a locked site = %v %v %v, want locked", locked, hold, err)
	}
	if locked, _, _ = e.p.lockHolds(bystander); locked {
		t.Error("an uninvolved account holds a lock")
	}
	e.lock(s1, true)
	if locked, hold, err = e.p.lockHolds(adm); err != nil || !locked || !hold {
		t.Errorf("admin of a held site = %v %v %v, want case open", locked, hold, err)
	}
}

// ---- the register ----

func TestRegisterShowsAdminsAndOpenOffers(t *testing.T) {
	t.Setenv("SITEBIN_ADMIN_ACCOUNTS", "boss@example.com")
	e := newGDPREnv(t)
	boss, _ := adminUser(t, e.p, e.mux, "boss@example.com", "admin")
	e.shared(inDays(12))
	e.site(tid(44))
	done := e.offerFrom(e.acc, "old@example.com", true, tid(44))
	e.change(done, func(x *ownership.Offer) { x.State = ownership.StateCancelled })

	list := getAs(e.mux, "/account/admin?q="+shareSite, boss)
	trail := getAs(e.mux, "/account/admin/sites/"+shareSite+"/trail", boss)
	for name, w := range map[string]*httptest.ResponseRecorder{"list": list, "trail": trail} {
		if w.Code != 200 {
			t.Fatalf("%s = %d", name, w.Code)
		}
		body := w.Body.String()
		for _, want := range []string{takerEmail, daysAhead(12)} {
			if !strings.Contains(body, want) {
				t.Errorf("the %s lacks the admin's %q", name, want)
			}
		}
	}
	// An open offer on a site.
	e.site(tid(43))
	e.offer(tid(43))
	for name, path := range map[string]string{"list": "/account/admin?q=" + tid(43), "trail": "/account/admin/sites/" + tid(43) + "/trail"} {
		w := getAs(e.mux, path, boss)
		if w.Code != 200 {
			t.Fatalf("%s = %d", name, w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, takerEmail) || !strings.Contains(body, "open") {
			t.Errorf("the %s lacks the open offer's recipient and state", name)
		}
	}
	// A cancelled offer is not shown.
	w := getAs(e.mux, "/account/admin/sites/"+tid(44)+"/trail", boss)
	if strings.Contains(w.Body.String(), "old@example.com") {
		t.Error("a final offer is shown as open")
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ---- no account log names another account ----

func TestExportsNeverNameTheOtherPartysAccount(t *testing.T) {
	e := newGDPREnv(t)
	sender := e.oidc("sub-sender", "sender@example.com")
	rcpt := e.oidc("sub-rcpt", "rcpt@example.com")
	const moving = "aaaaaaaaaaaaaaaaaaaaaaaabm"
	e.own(sender, moving)
	o := e.offerFrom(sender, "rcpt@example.com", true, moving)
	o = e.change(o, func(x *ownership.Offer) { x.State, x.AcceptedBy = ownership.StateAccepting, rcpt.ID })
	by := ext.ChangeBy{Account: rcpt.ID, IP: "203.0.113.9", UserAgent: "recipient-browser"}
	if _, err := e.p.completeAccepting(o, rcpt, by); err != nil {
		t.Fatal(err)
	}
	if owner, _ := e.fs.SiteOwner(moving); owner != rcpt.ID {
		t.Fatalf("the transfer did not complete: owner %q", owner)
	}
	senderRaw, _ := e.gdprExport(sender)
	rcptRaw, _ := e.gdprExport(rcpt)
	if strings.Contains(senderRaw, rcpt.ID) || strings.Contains(senderRaw, "203.0.113.9") {
		t.Errorf("the sender's export carries the recipient's account id or address:\n%s", senderRaw)
	}
	if strings.Contains(rcptRaw, sender.ID) {
		t.Errorf("the recipient's export carries the sender's account id:\n%s", rcptRaw)
	}
	if !strings.Contains(senderRaw, o.ID) {
		t.Error("the sender's log should reference the offer id")
	}
}

func TestShareLogLinesNameNoOtherAccount(t *testing.T) {
	e := newGDPREnv(t)
	owner := e.oidc("sub-owner", "owner@example.com")
	adm := e.oidc("sub-adm", "adm@example.com")
	const s1 = "aaaaaaaaaaaaaaaaaaaaaaaaba"
	e.own(owner, s1)
	e.makeAdmin(owner, adm, s1, nil)
	if w := e.gdprDelete(adm); w.Code != 200 {
		t.Fatalf("delete = %d", w.Code)
	}
	raw, _ := e.gdprExport(owner)
	if strings.Contains(raw, adm.ID) {
		t.Errorf("the owner's export names the deleted admin's account id:\n%s", raw)
	}
}
