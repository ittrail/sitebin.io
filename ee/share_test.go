//go:build ee

package ee

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/ownership"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// SetAdmins mirrors the core's: gone, then locked, then the callback, then the
// de-duplication and the cap.
func (s *fakeSites) SetAdmins(id, expectedOwner string, by ext.ChangeBy, edit func([]ext.SiteAdmin) ([]ext.SiteAdmin, error)) error {
	if s.onSetAdmins != nil {
		s.onSetAdmins(id)
	}
	info, ok := s.info(id)
	if !ok {
		return fmt.Errorf("%w: %s", ext.ErrSiteGone, id)
	}
	if expectedOwner == "" || info.Owner != expectedOwner {
		return fmt.Errorf("%w: %s", ext.ErrNotOwner, id)
	}
	if info.Locked != nil {
		return fmt.Errorf("%w: %s", ext.ErrSiteLocked, id)
	}
	next, err := edit(slices.Clone(info.Admins))
	if err != nil {
		return err
	}
	var out []ext.SiteAdmin
	for _, a := range next {
		if !slices.ContainsFunc(out, func(x ext.SiteAdmin) bool { return x.Account == a.Account }) {
			out = append(out, a)
		}
	}
	if len(out) > store.MaxAdmins {
		return fmt.Errorf("%w: %s", ext.ErrTooManyAdmins, id)
	}
	info.Admins = out
	s.infos[id] = info
	s.adminBy = append(s.adminBy, by)
	return nil
}

const (
	shareSite  = "aaaaaaaaaaaaaaaaaaaaaaaaab"
	shareSite2 = "aaaaaaaaaaaaaaaaaaaaaaaaac"
	invSubject = "invited"
)

type shareEnv struct {
	*takeEnv
}

func newShareEnv(t *testing.T) *shareEnv {
	t.Helper()
	e := &shareEnv{newTakeEnv(t, "pro", true)}
	e.site(shareSite)
	info := e.fs.infos[shareSite]
	info.EditURL = "https://edit.example/" + shareSite
	e.fs.infos[shareSite] = info
	return e
}

func (e *shareEnv) postTo(c *http.Cookie, acc *account.Account, path string, v url.Values) *httptest.ResponseRecorder {
	if v == nil {
		v = url.Values{}
	}
	if acc != nil {
		v.Set("csrf", e.p.csrf(acc))
	}
	r := form(v)
	r.URL.Path = path
	if c != nil {
		r.AddCookie(c)
	}
	return serve(e.mux, r)
}

func (e *shareEnv) invite(email, until string) *httptest.ResponseRecorder {
	v := url.Values{"email": {email}}
	if until != "" {
		v.Set("until", until)
	}
	return e.postTo(e.cookie, e.acc, "/account/sites/"+shareSite+"/share", v)
}

func (e *shareEnv) invitesOf(site string) []ownership.Invite {
	e.t.Helper()
	is, err := e.p.owner.InvitesBySite(site)
	if err != nil {
		e.t.Fatal(err)
	}
	return is
}

// open is an open invitation of the site to takerEmail, made directly.
func (e *shareEnv) open(site, email string, until *time.Time) ownership.Invite {
	e.t.Helper()
	inv, err := e.p.owner.CreateInvite(ownership.Invite{Site: site, From: e.acc.ID, ToEmail: email, Until: until}, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return inv
}

func (e *shareEnv) act(c *http.Cookie, acc *account.Account, inv ownership.Invite, action string) *httptest.ResponseRecorder {
	return e.postTo(c, acc, "/account/invites/"+inv.ID+"/"+action, nil)
}

func (e *shareEnv) accept(inv ownership.Invite) *httptest.ResponseRecorder {
	return e.act(e.toCookie, e.to, inv, "accept")
}

// shared makes the recipient an admin of shareSite through the routes.
func (e *shareEnv) shared(until *time.Time) ownership.Invite {
	e.t.Helper()
	inv := e.open(shareSite, takerEmail, until)
	if w := e.accept(inv); w.Code != http.StatusSeeOther {
		e.t.Fatalf("accept = %d (%s)", w.Code, w.Body)
	}
	return inv
}

func (e *shareEnv) admins(site string) []ext.SiteAdmin {
	e.t.Helper()
	a, err := e.fs.SiteAdmins(site)
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *shareEnv) inviteState(inv ownership.Invite) string {
	e.t.Helper()
	cur, err := e.p.owner.GetInvite(inv.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return cur.State
}

func (e *shareEnv) setTier(acc *account.Account, tier string) {
	e.setAccount(acc, func(a *account.Account) { a.Tier = tier })
}

func inDays(n int) *time.Time {
	t := time.Now().UTC().AddDate(0, 0, n)
	return &t
}

// ---- the happy path ----

func TestShareInviteAcceptThenTheAdminManages(t *testing.T) {
	e := newShareEnv(t)
	w := e.invite(takerEmail, daysAhead(30))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), shareAnswerHTML()) {
		t.Fatalf("invite = %d (%s)", w.Code, w.Body)
	}
	invs := e.invitesOf(shareSite)
	if len(invs) != 1 || invs[0].From != e.acc.ID || invs[0].ToEmail != takerEmail || invs[0].Until == nil || dateText(*invs[0].Until) != daysAhead(30) {
		t.Fatalf("invitations = %+v", invs)
	}
	if n := len(e.mailsTo(takerEmail, invSubject)); n != 1 {
		t.Fatalf("invitation mails to the invitee = %d, want 1", n)
	}
	m := e.mailsTo(takerEmail, invSubject)[0]
	for _, want := range []string{"pro@example.com", shareSite, daysAhead(30), "/account"} {
		if !strings.Contains(m, want) {
			t.Errorf("the invitation mail lacks %q:\n%s", want, m)
		}
	}
	if len(e.admins(shareSite)) != 0 {
		t.Fatal("an invitation made an admin before it was accepted")
	}

	if w := e.accept(invs[0]); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account" {
		t.Fatalf("accept = %d %q (%s)", w.Code, w.Header().Get("Location"), w.Body)
	}
	admins := e.admins(shareSite)
	if len(admins) != 1 || admins[0].Account != e.to.ID || admins[0].Until == nil || dateText(*admins[0].Until) != daysAhead(30) {
		t.Fatalf("admins = %+v", admins)
	}
	if got := e.inviteState(invs[0]); got != ownership.StateAccepted {
		t.Errorf("invitation state = %q", got)
	}
	if ids, _ := e.p.owner.SharedSites(e.to.ID); !slices.Equal(ids, []string{shareSite}) {
		t.Errorf("share markers = %v", ids)
	}
	if got := e.p.SharedSiteIDs(e.to.ID); !slices.Equal(got, []string{shareSite}) {
		t.Errorf("SharedSiteIDs = %v", got)
	}
	if n := len(e.fs.adminBy); n != 1 || e.fs.adminBy[0].Account != e.to.ID {
		t.Errorf("SetAdmins was called as %+v", e.fs.adminBy)
	}
	if n := len(e.mailsTo("pro@example.com", "accepted")); n != 1 {
		t.Errorf("mails to the owner = %d, want 1", n)
	}
	for _, id := range []string{e.acc.ID, e.to.ID} {
		log, err := e.p.accounts.Provenance(id)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, en := range log {
			found = found || (en.Site == shareSite && en.Action == "admins")
		}
		if !found {
			t.Errorf("account %s has no log line for the share: %+v", id, log)
		}
	}

	if owner, admin := e.p.manages(e.to, shareSite); owner || !admin {
		t.Errorf("manages(admin) = %v, %v; want false, true", owner, admin)
	}
	if owner, admin := e.p.manages(e.acc, shareSite); !owner || admin {
		t.Errorf("manages(owner) = %v, %v; want true, false", owner, admin)
	}
	// The dashboard's rename works for the admin.
	if w := e.postTo(e.toCookie, e.to, "/account/sites/"+shareSite+"/name", url.Values{"name": {"By the admin"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("rename by the admin = %d (%s)", w.Code, w.Body)
	}
	if got := e.fs.infos[shareSite].Name; got != "By the admin" {
		t.Errorf("name = %q", got)
	}
}

func TestManagesFollowsTheCoresStanding(t *testing.T) {
	e := newShareEnv(t)
	past := time.Now().Add(-time.Hour)
	e.shared(inDays(5))
	if _, admin := e.p.manages(e.to, shareSite); !admin {
		t.Fatal("a live admin does not manage")
	}
	_, other := e.other("else@example.com")
	if o, a := e.p.manages(other, shareSite); o || a {
		t.Error("a stranger manages")
	}
	info := e.fs.infos[shareSite]
	info.Admins = []ext.SiteAdmin{{Account: e.to.ID, Until: &past}}
	e.fs.infos[shareSite] = info
	if _, admin := e.p.manages(e.to, shareSite); admin {
		t.Error("an admin past its end date manages")
	}
	info.Admins = []ext.SiteAdmin{{Account: e.to.ID}}
	e.fs.infos[shareSite] = info
	e.setTier(e.to, "free")
	if _, admin := e.p.manages(e.to, shareSite); admin {
		t.Error("an admin whose plan does not share manages")
	}
	e.setTier(e.to, "pro")
	e.setTier(e.acc, "free")
	if _, admin := e.p.manages(e.to, shareSite); admin {
		t.Error("an admin of an owner whose plan does not share manages")
	}
	e.setTier(e.acc, "legacy") // an unknown plan: never a pass
	if _, admin := e.p.manages(e.to, shareSite); admin {
		t.Error("an unknown plan passed")
	}
}

// ---- the answer never reveals ----

func shareAnswerHTML() string { return shareAnswer }

func TestShareAnswerIsIdenticalForEveryAddress(t *testing.T) {
	e := newShareEnv(t)
	e.other("unused@example.com")
	c, id := adminUser(t, e.p, e.mux, "unver@example.com", "pro")
	_ = c
	acc, _ := e.p.accounts.ByID(id)
	e.verify(acc, false)

	var bodies []string
	for _, addr := range []string{takerEmail, "unver@example.com", "nobody@example.com"} {
		w := e.invite(addr, "")
		if w.Code != http.StatusOK {
			t.Fatalf("invite %s = %d (%s)", addr, w.Code, w.Body)
		}
		bodies = append(bodies, w.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("the answers differ:\n%s\n---\n%s\n---\n%s", bodies[0], bodies[1], bodies[2])
	}
	if !strings.Contains(bodies[0], "If an account with this address exists, it has been notified.") {
		t.Errorf("the answer is not the agreed text: %s", bodies[0])
	}
	if n := len(e.mailsTo(takerEmail, invSubject)); n != 1 {
		t.Errorf("mails to the verified address = %d, want 1", n)
	}
	if n := len(e.mailsTo("unver@example.com", invSubject)) + len(e.mailsTo("nobody@example.com", invSubject)); n != 0 {
		t.Errorf("mails to an unverified or unknown address = %d, want 0", n)
	}
}

// ---- refusing an invitation ----

func TestShareInviteRefusals(t *testing.T) {
	t.Run("own address", func(t *testing.T) {
		e := newShareEnv(t)
		w := e.invite("PRO@example.com", "")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "your own") || len(e.invitesOf(shareSite)) != 0 {
			t.Errorf("= %d (%s)", w.Code, w.Body)
		}
	})
	t.Run("not an address", func(t *testing.T) {
		e := newShareEnv(t)
		if w := e.invite("not an address", ""); w.Code != http.StatusBadRequest || len(e.invitesOf(shareSite)) != 0 {
			t.Errorf("= %d (%s)", w.Code, w.Body)
		}
	})
	t.Run("an end date in the past or not a date", func(t *testing.T) {
		e := newShareEnv(t)
		for _, u := range []string{daysAhead(-2), "soon"} {
			if w := e.invite(takerEmail, u); w.Code != http.StatusBadRequest || len(e.invitesOf(shareSite)) != 0 {
				t.Errorf("until %q = %d (%s)", u, w.Code, w.Body)
			}
		}
	})
	t.Run("operator owned", func(t *testing.T) {
		e := newShareEnv(t)
		e.p.cfg.AdminAccounts = []string{"pro@example.com"}
		e.setTier(e.acc, "admin")
		w := e.invite(takerEmail, "")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Sites owned by the operator account cannot be shared") || len(e.invitesOf(shareSite)) != 0 {
			t.Errorf("= %d (%s)", w.Code, w.Body)
		}
	})
	t.Run("a free owner", func(t *testing.T) {
		e := newShareEnv(t)
		e.setTier(e.acc, "free")
		w := e.invite(takerEmail, "")
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "/account#plan") || len(e.invitesOf(shareSite)) != 0 {
			t.Errorf("= %d (%s)", w.Code, w.Body)
		}
	})
	t.Run("an unknown plan is a retry, never a pass", func(t *testing.T) {
		e := newShareEnv(t)
		e.setTier(e.acc, "legacy")
		w := e.invite(takerEmail, "")
		if w.Code != http.StatusServiceUnavailable || len(e.invitesOf(shareSite)) != 0 {
			t.Errorf("= %d (%s)", w.Code, w.Body)
		}
	})
	t.Run("a locked site", func(t *testing.T) {
		e := newShareEnv(t)
		info := e.fs.infos[shareSite]
		info.Locked = &ext.SiteLock{At: time.Now(), By: "operator"}
		e.fs.infos[shareSite] = info
		w := e.invite(takerEmail, "")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "locked") || len(e.invitesOf(shareSite)) != 0 {
			t.Errorf("= %d (%s)", w.Code, w.Body)
		}
	})
	t.Run("the eleventh admin counts admins and open invitations", func(t *testing.T) {
		e := newShareEnv(t)
		info := e.fs.infos[shareSite]
		for i := 0; i < 8; i++ {
			info.Admins = append(info.Admins, ext.SiteAdmin{Account: fmt.Sprintf("admin-%d", i)})
		}
		e.fs.infos[shareSite] = info
		e.open(shareSite, "one@example.com", nil)
		if w := e.invite("two@example.com", ""); w.Code != http.StatusSeeOther && w.Code != http.StatusOK {
			t.Fatalf("the tenth slot was refused: %d (%s)", w.Code, w.Body)
		}
		w := e.invite("three@example.com", "")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "10") || len(e.invitesOf(shareSite)) != 2 {
			t.Errorf("eleventh = %d (%s), invitations %d", w.Code, w.Body, len(e.invitesOf(shareSite)))
		}
	})
	t.Run("an open invitation to the same address", func(t *testing.T) {
		e := newShareEnv(t)
		e.open(shareSite, takerEmail, nil)
		if w := e.invite(takerEmail, ""); w.Code != http.StatusBadRequest || len(e.invitesOf(shareSite)) != 1 {
			t.Errorf("= %d (%s)", w.Code, w.Body)
		}
	})
	t.Run("the daily limit is shared with offers", func(t *testing.T) {
		e := newShareEnv(t)
		for i := 0; i < maxOffersPerDay/2; i++ {
			if _, err := e.p.owner.CreateOffer(ownership.Offer{
				Sites: []string{fmt.Sprintf("offered-%d", i)}, From: e.acc.ID, ToEmail: "x@example.com",
				AcceptBy: time.Now().Add(time.Hour), State: ownership.StateOpen,
			}, 0); err != nil {
				t.Fatal(err)
			}
			e.open(fmt.Sprintf("invited-%d", i), "x@example.com", nil)
		}
		w := e.invite(takerEmail, "")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "limit") || len(e.invitesOf(shareSite)) != 0 {
			t.Errorf("= %d (%s)", w.Code, w.Body)
		}
	})
	t.Run("not the owner", func(t *testing.T) {
		e := newShareEnv(t)
		oc, other := e.other("else@example.com")
		if w := e.postTo(oc, other, "/account/sites/"+shareSite+"/share", url.Values{"email": {"x@example.com"}}); w.Code != http.StatusNotFound {
			t.Errorf("a stranger = %d", w.Code)
		}
		e.shared(nil)
		if w := e.postTo(e.toCookie, e.to, "/account/sites/"+shareSite+"/share", url.Values{"email": {"x@example.com"}}); w.Code != http.StatusNotFound {
			t.Errorf("an admin re-sharing = %d (%s)", w.Code, w.Body)
		}
		if len(e.invitesOf(shareSite)) != 1 { // only the accepted one
			t.Errorf("invitations = %+v", e.invitesOf(shareSite))
		}
	})
	t.Run("no csrf", func(t *testing.T) {
		e := newShareEnv(t)
		if w := e.postTo(e.cookie, nil, "/account/sites/"+shareSite+"/share", url.Values{"email": {takerEmail}}); w.Code != http.StatusForbidden {
			t.Errorf("= %d", w.Code)
		}
		if len(e.invitesOf(shareSite)) != 0 {
			t.Error("an invitation was made without a token")
		}
	})
}

// ---- accepting ----

func TestShareAcceptRefusals(t *testing.T) {
	check := func(t *testing.T, e *shareEnv, inv ownership.Invite, status int, wantState string) {
		t.Helper()
		w := e.accept(inv)
		if w.Code != status {
			t.Errorf("accept = %d (%s), want %d", w.Code, w.Body, status)
		}
		if len(e.admins(shareSite)) != 0 {
			t.Errorf("admins = %+v after a refused accept", e.admins(shareSite))
		}
		if ids, _ := e.p.owner.SharedSites(e.to.ID); len(ids) != 0 {
			t.Errorf("share markers = %v after a refused accept", ids)
		}
		if got := e.inviteState(inv); got != wantState {
			t.Errorf("invitation state = %q, want %q", got, wantState)
		}
	}
	t.Run("a free invitee", func(t *testing.T) {
		e := newShareEnv(t)
		inv := e.open(shareSite, takerEmail, nil)
		e.setTier(e.to, "free")
		check(t, e, inv, http.StatusForbidden, ownership.StateOpen)
	})
	t.Run("an unknown plan of the invitee is a retry", func(t *testing.T) {
		e := newShareEnv(t)
		inv := e.open(shareSite, takerEmail, nil)
		e.setTier(e.to, "legacy")
		check(t, e, inv, http.StatusServiceUnavailable, ownership.StateOpen)
	})
	t.Run("a free owner", func(t *testing.T) {
		e := newShareEnv(t)
		inv := e.open(shareSite, takerEmail, nil)
		e.setTier(e.acc, "free")
		check(t, e, inv, http.StatusConflict, ownership.StateOpen)
	})
	t.Run("the inviter no longer owns the site", func(t *testing.T) {
		e := newShareEnv(t)
		inv := e.open(shareSite, takerEmail, nil)
		info := e.fs.infos[shareSite]
		info.Owner = "someone-else"
		e.fs.infos[shareSite] = info
		check(t, e, inv, http.StatusConflict, ownership.StateCancelled)
	})
	t.Run("a locked site", func(t *testing.T) {
		e := newShareEnv(t)
		inv := e.open(shareSite, takerEmail, nil)
		info := e.fs.infos[shareSite]
		info.Locked = &ext.SiteLock{At: time.Now(), By: "operator"}
		e.fs.infos[shareSite] = info
		check(t, e, inv, http.StatusConflict, ownership.StateOpen)
	})
	t.Run("older than 30 days", func(t *testing.T) {
		e := newShareEnv(t)
		inv := e.open(shareSite, takerEmail, nil)
		e.p.owner.UpdateInvite(inv.ID, func(i *ownership.Invite) error { i.Created = time.Now().Add(-31 * 24 * time.Hour); return nil })
		check(t, e, inv, http.StatusConflict, ownership.StateExpired)
	})
	t.Run("an end date already past", func(t *testing.T) {
		e := newShareEnv(t)
		past := time.Now().Add(-time.Hour)
		inv := e.open(shareSite, takerEmail, &past)
		if w := e.accept(inv); w.Code != http.StatusConflict {
			t.Errorf("accept = %d", w.Code)
		}
		if len(e.admins(shareSite)) != 0 {
			t.Error("an admin whose share had already ended was made")
		}
	})
	t.Run("a site with ten admins", func(t *testing.T) {
		e := newShareEnv(t)
		info := e.fs.infos[shareSite]
		for i := 0; i < 10; i++ {
			info.Admins = append(info.Admins, ext.SiteAdmin{Account: fmt.Sprintf("admin-%d", i)})
		}
		e.fs.infos[shareSite] = info
		inv := e.open(shareSite, takerEmail, nil)
		if w := e.accept(inv); w.Code != http.StatusConflict {
			t.Errorf("accept = %d (%s)", w.Code, w.Body)
		}
		if got := e.inviteState(inv); got != ownership.StateOpen {
			t.Errorf("state = %q", got)
		}
		if ids, _ := e.p.owner.SharedSites(e.to.ID); len(ids) != 0 {
			t.Errorf("share markers = %v", ids)
		}
	})
	t.Run("someone else, or an unverified address", func(t *testing.T) {
		e := newShareEnv(t)
		inv := e.open(shareSite, takerEmail, nil)
		oc, other := e.other("else@example.com")
		if w := e.act(oc, other, inv, "accept"); w.Code != http.StatusNotFound {
			t.Errorf("a stranger = %d", w.Code)
		}
		if w := e.act(e.cookie, e.acc, inv, "accept"); w.Code != http.StatusNotFound {
			t.Errorf("the inviter = %d", w.Code)
		}
		e.verify(e.to, false)
		if w := e.accept(inv); w.Code != http.StatusNotFound {
			t.Errorf("an unverified recipient = %d", w.Code)
		}
		e.verify(e.to, true)
		if w := e.accept(inv); w.Code != http.StatusSeeOther {
			t.Errorf("the real invitee = %d (%s)", w.Code, w.Body)
		}
	})
	t.Run("no csrf", func(t *testing.T) {
		e := newShareEnv(t)
		inv := e.open(shareSite, takerEmail, nil)
		if w := e.postTo(e.toCookie, nil, "/account/invites/"+inv.ID+"/accept", nil); w.Code != http.StatusForbidden {
			t.Errorf("= %d", w.Code)
		}
		if w := e.postTo(e.toCookie, nil, "/account/invites/"+inv.ID+"/decline", nil); w.Code != http.StatusForbidden {
			t.Errorf("decline = %d", w.Code)
		}
		if len(e.admins(shareSite)) != 0 {
			t.Error("accepted without a token")
		}
	})
}

func TestShareAcceptTwiceIsOneAdmin(t *testing.T) {
	e := newShareEnv(t)
	inv := e.shared(nil)
	if w := e.accept(inv); w.Code != http.StatusConflict {
		t.Errorf("second accept = %d", w.Code)
	}
	if n := len(e.admins(shareSite)); n != 1 {
		t.Errorf("admins = %d", n)
	}
}

// ---- declining and cancelling ----

func TestShareDeclineAndCancel(t *testing.T) {
	e := newShareEnv(t)
	inv := e.open(shareSite, takerEmail, nil)
	oc, other := e.other("else@example.com")
	if w := e.act(oc, other, inv, "decline"); w.Code != http.StatusNotFound {
		t.Errorf("a stranger declining = %d", w.Code)
	}
	if w := e.act(e.toCookie, e.to, inv, "decline"); w.Code != http.StatusSeeOther {
		t.Fatalf("decline = %d (%s)", w.Code, w.Body)
	}
	if got := e.inviteState(inv); got != ownership.StateDeclined {
		t.Errorf("state = %q", got)
	}
	if n := len(e.mailsTo("pro@example.com", "declined")); n != 1 {
		t.Errorf("mails to the owner = %d", n)
	}
	if w := e.accept(inv); w.Code != http.StatusConflict {
		t.Errorf("accepting a declined invitation = %d", w.Code)
	}

	inv2 := e.open(shareSite, takerEmail, nil)
	if w := e.act(oc, other, inv2, "cancel"); w.Code != http.StatusNotFound {
		t.Errorf("a stranger cancelling = %d", w.Code)
	}
	if w := e.act(e.toCookie, e.to, inv2, "cancel"); w.Code != http.StatusNotFound {
		t.Errorf("the invitee cancelling = %d", w.Code)
	}
	if w := e.postTo(e.cookie, nil, "/account/invites/"+inv2.ID+"/cancel", nil); w.Code != http.StatusForbidden {
		t.Errorf("cancel without a token = %d", w.Code)
	}
	if w := e.act(e.cookie, e.acc, inv2, "cancel"); w.Code != http.StatusSeeOther {
		t.Fatalf("cancel = %d (%s)", w.Code, w.Body)
	}
	if got := e.inviteState(inv2); got != ownership.StateCancelled {
		t.Errorf("state = %q", got)
	}
	if w := e.accept(inv2); w.Code != http.StatusConflict {
		t.Errorf("accepting a cancelled invitation = %d", w.Code)
	}
}

// ---- ending and leaving ----

func (e *shareEnv) withTags() {
	e.t.Helper()
	if err := e.p.SetAdminTags(e.to.ID, shareSite, []string{"mine"}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *shareEnv) gone(label string) {
	e.t.Helper()
	if len(e.admins(shareSite)) != 0 {
		e.t.Errorf("%s: admins = %+v", label, e.admins(shareSite))
	}
	if ids, _ := e.p.owner.SharedSites(e.to.ID); len(ids) != 0 {
		e.t.Errorf("%s: share markers = %v", label, ids)
	}
	if tags := e.p.AdminTags(e.to.ID, shareSite); len(tags) != 0 {
		e.t.Errorf("%s: admin tags = %v", label, tags)
	}
	if owner, admin := e.p.manages(e.to, shareSite); owner || admin {
		e.t.Errorf("%s: the former admin still manages", label)
	}
	log, _ := e.p.accounts.Provenance(e.to.ID)
	// A burst of the same action by the same actor is merged into one line,
	// so the accept and a leave may be one; the end is in it either way.
	ended := false
	for _, en := range log {
		ended = ended || (en.Site == shareSite && en.Action == "admins" && strings.Contains(en.Detail, "no longer an admin"))
	}
	if !ended {
		e.t.Errorf("%s: the admin's log does not record the end: %+v", label, log)
	}
}

func TestShareOwnerEndsIt(t *testing.T) {
	e := newShareEnv(t)
	e.shared(nil)
	e.withTags()
	oc, other := e.other("else@example.com")
	path := "/account/sites/" + shareSite + "/share/" + e.p.shareRef(shareSite, e.to.ID) + "/end"
	if w := e.postTo(oc, other, path, nil); w.Code != http.StatusNotFound {
		t.Errorf("a stranger ending = %d", w.Code)
	}
	if w := e.postTo(e.toCookie, e.to, path, nil); w.Code != http.StatusNotFound {
		t.Errorf("the admin ending its own share through the owner's route = %d", w.Code)
	}
	if w := e.postTo(e.cookie, nil, path, nil); w.Code != http.StatusForbidden {
		t.Errorf("no token = %d", w.Code)
	}
	if len(e.admins(shareSite)) != 1 {
		t.Fatal("a refused request ended the share")
	}
	if w := e.postTo(e.cookie, e.acc, "/account/sites/"+shareSite+"/share/"+e.p.shareRef(shareSite, other.ID)+"/end", nil); w.Code != http.StatusNotFound {
		t.Errorf("ending a share nobody holds = %d", w.Code)
	}
	// The share is named by its reference only, never by the account id.
	if w := e.postTo(e.cookie, e.acc, "/account/sites/"+shareSite+"/share/"+e.to.ID+"/end", nil); w.Code != http.StatusNotFound {
		t.Errorf("ending a share by the admin's account id = %d", w.Code)
	}
	if len(e.admins(shareSite)) != 1 {
		t.Fatal("a refused request ended the share")
	}
	if w := e.postTo(e.cookie, e.acc, path, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("end = %d (%s)", w.Code, w.Body)
	}
	e.gone("end")
	if n := len(e.mailsTo("pro@example.com", "ended")); n != 1 {
		t.Errorf("mails to the owner = %d", n)
	}
	m := e.mailsTo(takerEmail, "ended")
	if len(m) != 1 || !strings.Contains(m[0], "pro@example.com") {
		t.Errorf("mails to the admin = %v", m)
	}
	if tags := e.fs.infos[shareSite].Tags; len(tags) != 0 {
		t.Errorf("the owner's tags = %v", tags)
	}
}

func TestShareAdminLeaves(t *testing.T) {
	e := newShareEnv(t)
	e.shared(nil)
	e.withTags()
	oc, other := e.other("else@example.com")
	path := "/account/sites/" + shareSite + "/leave"
	if w := e.postTo(oc, other, path, nil); w.Code != http.StatusNotFound {
		t.Errorf("a stranger leaving = %d", w.Code)
	}
	if w := e.postTo(e.cookie, e.acc, path, nil); w.Code != http.StatusNotFound {
		t.Errorf("the owner leaving = %d", w.Code)
	}
	if w := e.postTo(e.toCookie, nil, path, nil); w.Code != http.StatusForbidden {
		t.Errorf("no token = %d", w.Code)
	}
	if w := e.postTo(e.toCookie, e.to, path, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("leave = %d (%s)", w.Code, w.Body)
	}
	e.gone("leave")
	if n := len(e.mailsTo("pro@example.com", "ended")); n != 1 {
		t.Errorf("mails to the owner = %d", n)
	}
	m := e.mailsTo(takerEmail, "ended")
	if len(m) != 1 || !strings.Contains(m[0], takerEmail) {
		t.Errorf("mails to the admin = %v", m)
	}
}

func TestShareAnAdminCannotEndSomeoneElsesShare(t *testing.T) {
	e := newShareEnv(t)
	e.shared(nil)
	// A second admin, made directly.
	info := e.fs.infos[shareSite]
	info.Admins = append(info.Admins, ext.SiteAdmin{Account: "second-admin"})
	e.fs.infos[shareSite] = info
	if w := e.postTo(e.toCookie, e.to, "/account/sites/"+shareSite+"/share/"+e.p.shareRef(shareSite, "second-admin")+"/end", nil); w.Code != http.StatusNotFound {
		t.Errorf("= %d", w.Code)
	}
	if len(e.admins(shareSite)) != 2 {
		t.Error("an admin ended another admin's share")
	}
}

func TestShareEndOnALockedSiteIsRefused(t *testing.T) {
	e := newShareEnv(t)
	e.shared(nil)
	info := e.fs.infos[shareSite]
	info.Locked = &ext.SiteLock{At: time.Now(), By: "operator"}
	e.fs.infos[shareSite] = info
	if w := e.postTo(e.cookie, e.acc, "/account/sites/"+shareSite+"/share/"+e.p.shareRef(shareSite, e.to.ID)+"/end", nil); w.Code != http.StatusForbidden {
		t.Errorf("end on a locked site = %d (%s)", w.Code, w.Body)
	}
	if len(e.admins(shareSite)) != 1 {
		t.Error("the share ended on a locked site")
	}
}

// ---- the dashboard ----

func TestShareDashboardListsSharedSiteWithTheAdminsOwnTags(t *testing.T) {
	e := newShareEnv(t)
	e.fs.infos[shareSite] = func() ext.SiteInfo { i := e.fs.infos[shareSite]; i.Tags = []string{"owners-secret"}; return i }()
	inv := e.open(shareSite, takerEmail, inDays(20))

	// Before accepting: the invitations card and the owner's open invitation.
	body := e.dashboard(e.toCookie)
	if !strings.Contains(body, "Invitations") || !strings.Contains(body, "/account/invites/"+inv.ID+"/accept") || !strings.Contains(body, "/account/invites/"+inv.ID+"/decline") {
		t.Errorf("the invitee's dashboard lacks the invitation:\n%s", body)
	}
	if strings.Contains(body, "shared by") {
		t.Error("a site is listed as shared before the invitation was accepted")
	}
	ob := e.dashboard(e.cookie)
	if !strings.Contains(ob, "/account/invites/"+inv.ID+"/cancel") || !strings.Contains(ob, takerEmail) || !strings.Contains(ob, "/account/sites/"+shareSite+"/share") {
		t.Errorf("the owner's dashboard lacks the open invitation or the share form:\n%s", ob)
	}

	if w := e.accept(inv); w.Code != http.StatusSeeOther {
		t.Fatalf("accept = %d (%s)", w.Code, w.Body)
	}
	body = e.dashboard(e.toCookie)
	if !strings.Contains(body, "shared by pro@example.com · until "+dateText(*inv.Until)) {
		t.Errorf("the shared site is not marked:\n%s", body)
	}
	if !strings.Contains(body, "/account/sites/"+shareSite+"/leave") || !strings.Contains(body, "https://edit.example/"+shareSite) {
		t.Errorf("Manage or Leave is missing:\n%s", body)
	}
	if strings.Contains(body, "owners-secret") {
		t.Error("the owner's tags are visible to the admin")
	}
	if strings.Contains(body, "/account/sites/"+shareSite+"/rotate") || strings.Contains(body, "/account/sites/"+shareSite+"/delete") {
		t.Error("the admin is offered an owner-only action")
	}
	if strings.Contains(body, "Invitations") {
		t.Error("an accepted invitation is still listed")
	}
	ob = e.dashboard(e.cookie)
	if !strings.Contains(ob, "Admin · until "+dateText(*inv.Until)) || !strings.Contains(ob, "/account/sites/"+shareSite+"/share/"+e.p.shareRef(shareSite, e.to.ID)+"/end") {
		t.Errorf("the owner's dashboard lacks the admin or End:\n%s", ob)
	}
	// Final review M4: the owner's page addresses the share by an opaque
	// reference and never carries the admin's internal account id.
	if strings.Contains(ob, e.to.ID) {
		t.Errorf("the owner's dashboard carries the admin's account id %s", e.to.ID)
	}

	// The admin's tags are the admin's own.
	if w := e.postTo(e.toCookie, e.to, "/account/sites/"+shareSite+"/tags/add", url.Values{"tag": {"Mine"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("tag by the admin = %d (%s)", w.Code, w.Body)
	}
	if got := e.p.AdminTags(e.to.ID, shareSite); !slices.Equal(got, []string{"Mine"}) {
		t.Errorf("admin tags = %v", got)
	}
	if got := e.fs.infos[shareSite].Tags; !slices.Equal(got, []string{"owners-secret"}) {
		t.Errorf("the owner's tags = %v", got)
	}
	if !strings.Contains(e.dashboard(e.toCookie), "Mine") || strings.Contains(e.dashboard(e.cookie), "Mine") {
		t.Error("the admin's tag shows on the wrong dashboard")
	}
	// The tag filter covers shared sites.
	if f := getAs(e.mux, "/account?t=mine", e.toCookie).Body.String(); !strings.Contains(f, "shared by pro@example.com") {
		t.Errorf("the tag filter hides the shared site:\n%s", f)
	}
	if f := getAs(e.mux, "/account?untagged=1", e.toCookie).Body.String(); strings.Contains(f, "shared by pro@example.com") {
		t.Error("the tag filter shows a shared site that does not carry the tag")
	}
	if w := e.postTo(e.toCookie, e.to, "/account/sites/"+shareSite+"/tags/remove", url.Values{"tag": {"mine"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("untag by the admin = %d (%s)", w.Code, w.Body)
	}
	if got := e.p.AdminTags(e.to.ID, shareSite); len(got) != 0 {
		t.Errorf("admin tags = %v after removing", got)
	}
}

func TestShareAdminCannotRotateOrDelete(t *testing.T) {
	e := newShareEnv(t)
	e.shared(nil)
	for _, action := range []string{"rotate", "delete"} {
		if w := e.postTo(e.toCookie, e.to, "/account/sites/"+shareSite+"/"+action, nil); w.Code != http.StatusForbidden {
			t.Errorf("%s by the admin = %d", action, w.Code)
		}
	}
	if len(e.fs.rotated) != 0 || len(e.fs.deleted) != 0 {
		t.Errorf("rotated %v, deleted %v", e.fs.rotated, e.fs.deleted)
	}
	// A stranger cannot rename or tag it.
	oc, other := e.other("else@example.com")
	if w := e.postTo(oc, other, "/account/sites/"+shareSite+"/name", url.Values{"name": {"x"}}); w.Code != http.StatusForbidden {
		t.Errorf("rename by a stranger = %d", w.Code)
	}
	if w := e.postTo(oc, other, "/account/sites/"+shareSite+"/tags/add", url.Values{"tag": {"x"}}); w.Code != http.StatusForbidden {
		t.Errorf("tag by a stranger = %d", w.Code)
	}
}

func TestShareAPausedShareShowsTheReasonAndNoManage(t *testing.T) {
	e := newShareEnv(t)
	e.shared(nil)
	e.setTier(e.acc, "free")
	body := e.dashboard(e.toCookie)
	// Final review M5: each side reads why, in plain words.
	if !strings.Contains(body, "shared by pro@example.com") || !strings.Contains(body, html.EscapeString(sharePausedOwnersPlan)) {
		t.Errorf("the paused share is not explained to the admin:\n%s", body)
	}
	if ob := e.dashboard(e.cookie); !strings.Contains(ob, takerEmail+" · Admin · "+sharePausedYourPlan) {
		t.Errorf("the paused share is not explained to the owner:\n%s", ob)
	}
	if strings.Contains(body, "https://edit.example/"+shareSite) {
		t.Error("a paused share still links Manage")
	}
	if !strings.Contains(body, "/account/sites/"+shareSite+"/leave") {
		t.Error("a paused share cannot be left")
	}
	// Rename and tags are refused while the share is paused.
	if w := e.postTo(e.toCookie, e.to, "/account/sites/"+shareSite+"/name", url.Values{"name": {"x"}}); w.Code != http.StatusForbidden {
		t.Errorf("rename while paused = %d", w.Code)
	}

	// The admin's plan: each side reads it the other way round.
	e.setTier(e.acc, "pro")
	e.setTier(e.to, "free")
	if body := e.dashboard(e.toCookie); !strings.Contains(body, sharePausedYourPlan) {
		t.Errorf("the admin is not told their own plan pauses the share:\n%s", body)
	}
	if ob := e.dashboard(e.cookie); !strings.Contains(ob, sharePausedAdminsPlan) {
		t.Errorf("the owner is not told the admin's plan pauses the share:\n%s", ob)
	}
	// A share that holds says nothing.
	e.setTier(e.to, "pro")
	if ob := e.dashboard(e.cookie); strings.Contains(ob, "Paused:") {
		t.Errorf("a share that holds is shown as paused:\n%s", ob)
	}
}

// Final review M7: an owner who becomes the operator lends no exemptions.
// SharingAllowed is false for the operator account, so every share they made
// pauses -- at the core's gate as on the dashboard -- and resumes if they
// stop being the operator.
func TestShareOfAnOwnerWhoBecomesTheOperatorPauses(t *testing.T) {
	e := newShareEnv(t)
	e.shared(nil)
	if _, admin := e.p.manages(e.to, shareSite); !admin {
		t.Fatal("the share does not hold to begin with")
	}
	e.p.cfg.AdminAccounts = []string{"pro@example.com"}
	e.setTier(e.acc, "admin")
	if ok, err := e.p.SharingAllowed(e.acc.ID); ok || err != nil {
		t.Errorf("SharingAllowed(operator) = %v, %v; want false", ok, err)
	}
	if _, admin := e.p.manages(e.to, shareSite); admin {
		t.Error("the operator's share still stands")
	}
	if body := e.dashboard(e.toCookie); !strings.Contains(body, sharePausedOperator) {
		t.Errorf("the admin is not told why the share paused:\n%s", body)
	}
	if w := e.postTo(e.toCookie, e.to, "/account/sites/"+shareSite+"/name", url.Values{"name": {"x"}}); w.Code != http.StatusForbidden {
		t.Errorf("rename through the operator's share = %d", w.Code)
	}
	e.p.cfg.AdminAccounts = nil
	if _, admin := e.p.manages(e.to, shareSite); !admin {
		t.Error("the share did not resume once the account is no longer the operator")
	}
}

func TestShareInvitationsCardHidesWhatCannotBeAccepted(t *testing.T) {
	e := newShareEnv(t)
	inv := e.open(shareSite, takerEmail, nil)
	e.setTier(e.to, "free")
	body := e.dashboard(e.toCookie)
	if !strings.Contains(body, "Invitations") || strings.Contains(body, "/account/invites/"+inv.ID+"/accept") {
		t.Errorf("a plan that cannot share is offered Accept:\n%s", body)
	}
	e.setTier(e.to, "pro")
	// A lapsed invitation and one whose inviter no longer owns the site are not listed.
	e.p.owner.UpdateInvite(inv.ID, func(i *ownership.Invite) error { i.Created = time.Now().Add(-31 * 24 * time.Hour); return nil })
	if b := e.dashboard(e.toCookie); strings.Contains(b, "/account/invites/"+inv.ID+"/accept") {
		t.Error("a lapsed invitation is listed")
	}
	inv2 := e.open(shareSite, takerEmail, nil)
	info := e.fs.infos[shareSite]
	info.Owner = "someone-else"
	e.fs.infos[shareSite] = info
	if b := e.dashboard(e.toCookie); strings.Contains(b, "/account/invites/"+inv2.ID+"/accept") {
		t.Error("an invitation of a former owner is listed")
	}
}

// ---- fix round 1 ----

func TestShareTransferBetweenTheChecksAndTheWriteLeavesNothing(t *testing.T) {
	e := newShareEnv(t)
	inv := e.open(shareSite, takerEmail, nil)
	e.fs.onSetAdmins = func(id string) {
		// The site changes hands after every check of the handler passed.
		info := e.fs.infos[id]
		info.Owner = "new-owner"
		info.Admins = nil
		e.fs.infos[id] = info
		e.fs.onSetAdmins = nil
	}
	w := e.accept(inv)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "no longer available") {
		t.Fatalf("accept = %d (%s)", w.Code, w.Body)
	}
	if got := e.admins(shareSite); len(got) != 0 {
		t.Errorf("the new owner's site lists %+v", got)
	}
	if ids, _ := e.p.owner.SharedSites(e.to.ID); len(ids) != 0 {
		t.Errorf("share markers = %v", ids)
	}
	if got := e.inviteState(inv); got != ownership.StateCancelled {
		t.Errorf("invitation state = %q", got)
	}
	if n := len(e.mailsTo("pro@example.com", "accepted")); n != 0 {
		t.Errorf("the old owner was told of an acceptance")
	}
	// A former admin never sees "shared by <new owner>".
	if b := e.dashboard(e.toCookie); strings.Contains(b, "shared by") {
		t.Errorf("the dashboard lists the transferred site:\n%s", b)
	}
}

func TestShareEndAfterATransferTouchesNothing(t *testing.T) {
	e := newShareEnv(t)
	e.shared(nil)
	e.fs.onSetAdmins = func(id string) {
		info := e.fs.infos[id]
		info.Owner = "new-owner"
		e.fs.infos[id] = info
		e.fs.onSetAdmins = nil
	}
	if w := e.postTo(e.cookie, e.acc, "/account/sites/"+shareSite+"/share/"+e.p.shareRef(shareSite, e.to.ID)+"/end", nil); w.Code != http.StatusNotFound {
		t.Errorf("end by the former owner = %d", w.Code)
	}
	if len(e.admins(shareSite)) != 1 {
		t.Error("the former owner changed the new owner's admins")
	}
}

func TestShareRefusesAnAccountThatIsAlreadyAnAdmin(t *testing.T) {
	e := newShareEnv(t)
	e.shared(nil)
	w := e.invite(takerEmail, daysAhead(5))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "already an admin") {
		t.Errorf("= %d (%s)", w.Code, w.Body)
	}
	if n := len(e.invitesOf(shareSite)); n != 1 {
		t.Errorf("invitations = %d", n)
	}
}

func TestShareDeclineRespectsLapse(t *testing.T) {
	e := newShareEnv(t)
	inv := e.open(shareSite, takerEmail, nil)
	e.p.owner.UpdateInvite(inv.ID, func(i *ownership.Invite) error { i.Created = time.Now().Add(-31 * 24 * time.Hour); return nil })
	if w := e.act(e.toCookie, e.to, inv, "decline"); w.Code != http.StatusConflict {
		t.Errorf("decline of a lapsed invitation = %d", w.Code)
	}
	if got := e.inviteState(inv); got != ownership.StateExpired {
		t.Errorf("state = %q", got)
	}
	if n := len(e.mailsTo("pro@example.com", "declined")); n != 0 {
		t.Errorf("the inviter was mailed %d times", n)
	}
}

func TestShareCardSaysTryAgainWhenThePlanCannotBeRead(t *testing.T) {
	e := newShareEnv(t)
	inv := e.open(shareSite, takerEmail, nil)
	e.setTier(e.to, "legacy")
	body := e.dashboard(e.toCookie)
	if !strings.Contains(body, "try again") || strings.Contains(body, "/account/invites/"+inv.ID+"/accept") {
		t.Errorf("the card does not explain the missing Accept:\n%s", body)
	}
}

func TestShareInviteMailPromisesNoSharedTags(t *testing.T) {
	e := newShareEnv(t)
	e.invite(takerEmail, "")
	m := e.mailsTo(takerEmail, invSubject)
	if len(m) != 1 || strings.Contains(strings.ToLower(m[0]), "tags") {
		t.Errorf("mail = %v", m)
	}
}
