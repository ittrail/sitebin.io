//go:build ee

package ee

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/containers"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
	"github.com/ittrail/sitebin.io/ee/ownership"
	"github.com/ittrail/sitebin.io/ee/smtp"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// ---- the fake core's TransferSite ----

// xferFake is the state of fakeSites.TransferSite.
type xferFake struct {
	mu    sync.Mutex
	calls []xferCall
	// fail fails the next TransferSite of a site once, before anything
	// changed: a crash or a transient error in the middle of a bundle.
	fail map[string]error
	// block, when set, holds every TransferSite until it is closed; entered,
	// when set, is told the site id first.
	block   chan struct{}
	entered chan string
	// after, when set, runs after a site moved.
	after func(id string)
	n     int
}

type xferCall struct {
	ViewID, From, To string
	Grant            ext.CreateGrant
	Detach           []string
	By               ext.ChangeBy
	Password         string
}

func (s *fakeSites) xf() *xferFake {
	if s.xfer == nil {
		s.xfer = &xferFake{fail: map[string]error{}}
	}
	return s.xfer
}

// TransferSite mirrors the core's: a gone, locked or not-the-sender's site,
// and an empty recipient, are refused with the seam's sentinels and nothing
// changes; otherwise the owner changes, the addresses that stay with the
// sender go (the core decides them: here the fake's will-not-move list), the
// tags and admins are cleared, and a fresh password is returned.
func (s *fakeSites) TransferSite(id, from, to string, g ext.CreateGrant, by ext.ChangeBy) (string, error) {
	x := s.xf()
	if x.entered != nil {
		x.entered <- id
	}
	if x.block != nil {
		<-x.block
	}
	pw, err := s.transfer(x, id, from, to, g, by)
	if err == nil && x.after != nil {
		x.after(id)
	}
	return pw, err
}

func (s *fakeSites) transfer(x *xferFake, id, from, to string, g ext.CreateGrant, by ext.ChangeBy) (string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := x.fail[id]; err != nil {
		delete(x.fail, id)
		return "", err
	}
	detach := s.ad().notMove[id]
	info, ok := s.infos[id]
	if !ok {
		return "", fmt.Errorf("%w: %s", ext.ErrSiteGone, id)
	}
	if info.Locked != nil {
		return "", fmt.Errorf("%w: %s", ext.ErrSiteLocked, id)
	}
	if cur, _ := s.info(id); to == "" || cur.Owner != from {
		return "", fmt.Errorf("%w: %s", ext.ErrNotOwner, id)
	}
	info.Owner = to
	delete(s.linkOwner, id)
	info.Tags, info.Admins = nil, nil
	var kept []string
	for _, d := range info.Domains {
		if !slices.Contains(detach, d) {
			kept = append(kept, d)
		}
	}
	info.Domains = kept
	s.infos[id] = info
	x.n++
	pw := fmt.Sprintf("NEWPW%02d%s", x.n, id[22:])
	x.calls = append(x.calls, xferCall{id, from, to, g, slices.Clone(detach), by, pw})
	if s.onTransferRecord != nil {
		s.onTransferRecord(id, provenance.Entry{Time: time.Now().UTC(), Action: provenance.ActionTransfer, Surface: by.Surface, Account: by.Account, IP: by.IP, UA: by.UserAgent, Site: id, Detail: "from " + from + " to " + to})
	}
	return pw, nil
}

// SiteLocked is the fake's meta-only lock read.
func (s *fakeSites) SiteLocked(id string) (bool, error) {
	i, ok := s.infos[id]
	if !ok {
		return false, fmt.Errorf("%w: %s", ext.ErrSiteGone, id)
	}
	return i.Locked != nil, nil
}

func (x *xferFake) callsFor(id string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	n := 0
	for _, c := range x.calls {
		if c.ViewID == id {
			n++
		}
	}
	return n
}

func (x *xferFake) all() []xferCall {
	x.mu.Lock()
	defer x.mu.Unlock()
	return slices.Clone(x.calls)
}

// ---- environment ----

const takerEmail = "taker@example.com"

// takeEnv is a sender ("pro@example.com", verified, Pro) and a recipient
// (takerEmail) with every mail captured.
type takeEnv struct {
	*xferEnv
	to       *account.Account
	toCookie *http.Cookie
	mailMu   sync.Mutex
	all      []sentMail
}

func newTakeEnv(t *testing.T, toTier string, verified bool) *takeEnv {
	t.Helper()
	e := &takeEnv{xferEnv: newXferEnv(t, "pro")}
	e.p.mailer = smtp.NewWithDeliver(eeconfig.SMTPConfig{From: "no-reply@sitebin.example"}, func(_ eeconfig.SMTPConfig, to, msg string) error {
		e.mailMu.Lock()
		defer e.mailMu.Unlock()
		e.all = append(e.all, sentMail{to, msg})
		return nil
	})
	e.verify(e.acc, true)
	c, id := adminUser(t, e.p, e.mux, takerEmail, toTier)
	acc, err := e.p.accounts.ByID(id)
	if err != nil {
		t.Fatal(err)
	}
	e.to, e.toCookie = acc, c
	e.verify(e.to, verified)
	return e
}

func (e *takeEnv) verify(acc *account.Account, v bool) {
	e.t.Helper()
	if err := e.p.accounts.Update(acc, func(a *account.Account) error { a.EmailVerified = v; return nil }); err != nil {
		e.t.Fatal(err)
	}
}

func (e *takeEnv) setAccount(acc *account.Account, f func(*account.Account)) {
	e.t.Helper()
	if err := e.p.accounts.Update(acc, func(a *account.Account) error { f(a); return nil }); err != nil {
		e.t.Fatal(err)
	}
}

// other signs up another account, verified.
func (e *takeEnv) other(email string) (*http.Cookie, *account.Account) {
	e.t.Helper()
	c, id := adminUser(e.t, e.p, e.mux, email, "pro")
	acc, err := e.p.accounts.ByID(id)
	if err != nil {
		e.t.Fatal(err)
	}
	e.verify(acc, true)
	return c, acc
}

// offer is an open offer of the sites to takerEmail, accept-by in ten days.
func (e *takeEnv) offer(sites ...string) ownership.Offer {
	e.t.Helper()
	o, err := e.p.owner.CreateOffer(ownership.Offer{
		Sites: sites, From: e.acc.ID, ToEmail: takerEmail,
		AcceptBy: time.Now().Add(10 * 24 * time.Hour), State: ownership.StateOpen,
	}, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return o
}

func (e *takeEnv) change(o ownership.Offer, f func(*ownership.Offer)) ownership.Offer {
	e.t.Helper()
	cur, err := e.p.owner.UpdateOffer(o.ID, func(x *ownership.Offer) error { f(x); return nil })
	if err != nil {
		e.t.Fatal(err)
	}
	return cur
}

func (e *takeEnv) current(o ownership.Offer) ownership.Offer {
	e.t.Helper()
	cur, err := e.p.owner.GetOffer(o.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return cur
}

// postOffer posts to /account/transfer/<id>/<action> as c/acc, with the
// offer's sites (or the ones given) as the card sends them.
func (e *takeEnv) postOffer(c *http.Cookie, acc *account.Account, id, action string, sites []string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	v := url.Values{"site": sites}
	if acc != nil {
		v.Set("csrf", e.p.csrf(acc))
	}
	r := form(v)
	r.URL.Path = "/account/transfer/" + id + "/" + action
	if c != nil {
		r.AddCookie(c)
	}
	for _, m := range mods {
		m(r)
	}
	return serve(e.mux, r)
}

func (e *takeEnv) accept(o ownership.Offer, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	return e.postOffer(e.toCookie, e.to, o.ID, "accept", o.Sites, mods...)
}

func (e *takeEnv) decline(o ownership.Offer) *httptest.ResponseRecorder {
	return e.postOffer(e.toCookie, e.to, o.ID, "decline", o.Sites)
}

func (e *takeEnv) dashboard(c *http.Cookie) string { return getAs(e.mux, "/account", c).Body.String() }

// mailsTo is the mails delivered to addr whose subject contains subject.
func (e *takeEnv) mailsTo(addr, subject string) []string {
	e.p.mailWG.Wait()
	e.mailMu.Lock()
	defer e.mailMu.Unlock()
	var out []string
	for _, m := range e.all {
		if m.To == addr && strings.Contains(m.Msg, "Subject: ") && strings.Contains(strings.SplitN(m.Msg[strings.Index(m.Msg, "Subject: "):], "\r\n", 2)[0], subject) {
			out = append(out, m.Msg)
		}
	}
	return out
}

func (e *takeEnv) anyMailContains(s string) bool {
	e.p.mailWG.Wait()
	e.mailMu.Lock()
	defer e.mailMu.Unlock()
	for _, m := range e.all {
		if strings.Contains(m.Msg, s) {
			return true
		}
	}
	return false
}

// unchanged asserts that none of the sites moved to the recipient.
func (e *takeEnv) unchanged(sites []string) {
	e.t.Helper()
	if calls := e.fs.xf().all(); len(calls) != 0 {
		e.t.Errorf("TransferSite ran %d times, want none", len(calls))
	}
	for _, id := range sites {
		if owner, _ := e.fs.SiteOwner(id); owner == e.to.ID {
			e.t.Errorf("%s belongs to the recipient", id)
		}
	}
	if ids, _ := e.p.accounts.ListSiteIDs(e.to); len(ids) != 0 {
		e.t.Errorf("the recipient has markers %v", ids)
	}
}

func label(id string) string { return "https://" + id + ".sitebin.app" }

func sortedCopy(s []string) []string { c := slices.Clone(s); slices.Sort(c); return c }

// ---- the bundle ----

func TestTakeoverBundle(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	a, b, c := tid(1), tid(2), tid(3)
	e.site(a, "kunde.example", "x.zone.example")
	e.site(b)
	e.site(c)
	e.fs.ad().notMove[a] = []string{"x.zone.example"}
	// b is shared with a helper, who labelled it in their own registry.
	_, helperID := adminUser(t, e.p, e.mux, "helper@example.com", "pro")
	ib := e.fs.infos[b]
	ib.Admins, ib.Tags = []ext.SiteAdmin{{Account: helperID}}, []string{"old"}
	e.fs.infos[b] = ib
	if err := e.p.owner.LinkShare(helperID, b); err != nil {
		t.Fatal(err)
	}
	if err := e.p.SetAdminTags(helperID, b, []string{"mine"}); err != nil {
		t.Fatal(err)
	}
	// c has a form and an edit page.
	e.fs.ad().forms[c] = []string{"forms@example.com"}
	ic := e.fs.infos[c]
	ic.EditURL = "http://sitebin.example/e/editc"
	e.fs.infos[c] = ic
	o := e.offer(c, a, b)

	// The card.
	card := e.dashboard(e.toCookie)
	for _, want := range []string{"Ready to take over", "pro@example.com", label(a), label(b), label(c), "kunde.example", "forms@example.com", "/account/transfer/" + o.ID + "/accept", "/account/transfer/" + o.ID + "/decline"} {
		if !strings.Contains(card, want) {
			t.Errorf("the card lacks %q", want)
		}
	}

	w := e.accept(o, fromIP("203.0.113.5"))
	if w.Code != http.StatusOK {
		t.Fatalf("accept = %d (%s)", w.Code, w.Body)
	}
	body := w.Body.String()
	calls := e.fs.xf().all()
	if len(calls) != 3 {
		t.Fatalf("TransferSite calls = %d, want 3", len(calls))
	}
	for i, want := range []string{a, b, c} {
		if calls[i].ViewID != want {
			t.Errorf("call %d = %s, want sorted view-id order", i, calls[i].ViewID)
		}
	}
	for _, cl := range calls {
		if n := strings.Count(body, cl.Password); n != 1 {
			t.Errorf("the new password of %s is shown %d times, want once", cl.ViewID, n)
		}
		g := cl.Grant
		if g.OwnerAccountID != e.to.ID || g.MaxSiteBytes != 1000 || g.MaxFiles != 10 || g.MaxExpiryDays != 30 || !g.Trusted {
			t.Errorf("grant for %s = %+v, want the recipient's Pro stamp", cl.ViewID, g)
		}
		if cl.From != e.acc.ID || cl.To != e.to.ID {
			t.Errorf("call %s from %s to %s", cl.ViewID, cl.From, cl.To)
		}
		if cl.By.Account != e.to.ID || cl.By.IP != "203.0.113.5" || cl.By.Surface != "" {
			t.Errorf("ChangeBy = %+v, want the recipient, auth.ClientIP and the dashboard", cl.By)
		}
	}
	if !slices.Equal(calls[0].Detach, []string{"x.zone.example"}) || len(calls[1].Detach) != 0 {
		t.Errorf("detach = %v / %v, want the will-not-move list", calls[0].Detach, calls[1].Detach)
	}
	for _, want := range []string{"forms@example.com", "http://sitebin.example/e/editc", label(a)} {
		if !strings.Contains(body, want) {
			t.Errorf("the result page lacks %q", want)
		}
	}

	// meta and the index
	for _, id := range []string{a, b, c} {
		if owner, _ := e.fs.SiteOwner(id); owner != e.to.ID {
			t.Errorf("owner of %s = %q, want the recipient", id, owner)
		}
	}
	if ids, _ := e.p.accounts.ListSiteIDs(e.to); !slices.Equal(sortedCopy(ids), []string{a, b, c}) {
		t.Errorf("recipient markers = %v", ids)
	}
	if ids, _ := e.p.accounts.ListSiteIDs(e.acc); len(ids) != 0 {
		t.Errorf("sender markers = %v, want none", ids)
	}

	// the offer: accepted, everything done, no password on disk
	cur := e.current(o)
	if cur.State != ownership.StateAccepted || !slices.Equal(sortedCopy(cur.Done), []string{a, b, c}) || cur.AcceptedBy != e.to.ID {
		t.Errorf("offer = %+v", cur)
	}
	raw, err := os.ReadFile(filepath.Join(e.p.host.DataDir(), "ownership", "offers", o.ID+".json"))
	if err != nil || strings.Contains(string(raw), "NEWPW") {
		t.Errorf("the offer file holds a password (%v):\n%s", err, raw)
	}

	// a reload shows the sites and no password
	d := getAs(e.mux, "/account/transfer/"+o.ID+"/done", e.toCookie)
	if d.Code != http.StatusOK || strings.Contains(d.Body.String(), "NEWPW") ||
		!strings.Contains(d.Body.String(), label(b)) || !strings.Contains(d.Body.String(), "rotate the password on the dashboard to get a new one") {
		t.Errorf("done = %d:\n%s", d.Code, d.Body)
	}
	if d := getAs(e.mux, "/account/transfer/"+o.ID+"/done", e.cookie); d.Code != http.StatusNotFound {
		t.Errorf("the sender's done page = %d, want 404", d.Code)
	}

	// the recipient's dashboard lists them, and the card is gone
	dash := e.dashboard(e.toCookie)
	if strings.Contains(dash, "Ready to take over") || !strings.Contains(dash, a) {
		t.Errorf("the recipient's dashboard after accepting:\n%s", dash)
	}

	// the sender is out
	if dash := e.dashboard(e.cookie); strings.Contains(dash, a) || strings.Contains(dash, b) || strings.Contains(dash, c) {
		t.Error("the sender's dashboard still lists a transferred site")
	}
	if w := e.post("/account/sites/"+a+"/rotate", url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("the sender's rotate = %d, want 403", w.Code)
	}
	if w := e.post("/account/sites/"+b+"/delete", url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("the sender's delete = %d, want 403", w.Code)
	}
	if len(e.fs.rotated) != 0 || len(e.fs.deleted) != 0 {
		t.Errorf("rotated %v, deleted %v", e.fs.rotated, e.fs.deleted)
	}

	// the former admin's share and tags are gone
	if shared, _ := e.p.owner.SharedSites(helperID); slices.Contains(shared, b) {
		t.Errorf("the former admin keeps a share marker: %v", shared)
	}
	if reg, _ := e.p.accounts.Tags(helperID); reg.Sites[b] != nil {
		t.Errorf("the former admin keeps tags on the site: %v", reg.Sites)
	}

	// both account logs
	for _, acc := range []*account.Account{e.acc, e.to} {
		es, _ := e.p.accounts.Provenance(acc.ID)
		var sites []string
		for _, en := range es {
			if en.Action == provenance.ActionTransfer {
				switch {
				case acc.ID == e.acc.ID && (en.Account != "" || en.IP != "" || en.UA != ""):
					t.Errorf("the sender's notice carries the recipient: %+v", en)
				case acc.ID == e.to.ID && en.Account != e.to.ID:
					t.Errorf("the recipient's own line lost its account: %+v", en)
				}
				sites = append(sites, en.Site)
			}
		}
		if !slices.Equal(sortedCopy(sites), []string{a, b, c}) {
			t.Errorf("log of %s: transfer lines for %v", acc.Email, sites)
		}
	}

	// mail to both sides, never a password
	sm := e.mailsTo("pro@example.com", "Your Sitebin transfer was accepted")
	if len(sm) != 1 || !strings.Contains(sm[0], takerEmail) || !strings.Contains(sm[0], label(a)) {
		t.Errorf("the sender's mail = %q", sm)
	}
	rm := e.mailsTo(takerEmail, "You took over")
	if len(rm) != 1 || !strings.Contains(rm[0], "pro@example.com") || !strings.Contains(rm[0], label(c)) {
		t.Errorf("the recipient's mail = %q", rm)
	}
	if e.anyMailContains("NEWPW") {
		t.Error("a mail carries a password")
	}
}

// ---- refusals ----

func TestTakeoverRefusals(t *testing.T) {
	type tcase struct {
		name       string
		toTier     string
		unverified bool
		// setup runs after the offer of a, b, c exists; it returns the sites
		// the request posts (nil: the offer's).
		setup func(e *takeEnv, o ownership.Offer) []string
		// as names who posts (nil: the recipient).
		as     func(e *takeEnv) (*http.Cookie, *account.Account)
		noCSRF bool
		status int
		want   string
		state  string
	}
	cases := []tcase{
		{name: "free recipient", toTier: "free", status: http.StatusForbidden, want: "Your plan cannot take over sites"},
		{name: "a site over a cap", setup: func(e *takeEnv, o ownership.Offer) []string {
			e.fs.ad().bytes[tid(2)] = 5000
			return nil
		}, status: http.StatusForbidden, want: label(tid(2))},
		{name: "a site the sender removed", setup: func(e *takeEnv, o ownership.Offer) []string {
			e.change(o, func(x *ownership.Offer) { x.Sites = []string{tid(1), tid(2)} })
			return o.Sites
		}, status: http.StatusConflict, want: "changed"},
		{name: "a site the sender added", setup: func(e *takeEnv, o ownership.Offer) []string {
			e.site(tid(4))
			e.change(o, func(x *ownership.Offer) { x.Sites = append(x.Sites, tid(4)) })
			return o.Sites
		}, status: http.StatusConflict, want: "changed"},
		{name: "a site the sender deleted", setup: func(e *takeEnv, o ownership.Offer) []string {
			delete(e.fs.infos, tid(3))
			return nil
		}, status: http.StatusConflict, want: "no longer"},
		{name: "a site that changed owner", setup: func(e *takeEnv, o ownership.Offer) []string {
			i := e.fs.infos[tid(3)]
			i.Owner = "someone-else"
			e.fs.infos[tid(3)] = i
			return nil
		}, status: http.StatusConflict, want: "no longer"},
		{name: "a cancelled offer", setup: func(e *takeEnv, o ownership.Offer) []string {
			e.change(o, func(x *ownership.Offer) { x.State = ownership.StateCancelled })
			return nil
		}, status: http.StatusConflict, want: "cancelled", state: ownership.StateCancelled},
		{name: "a domain added on a shared host", setup: func(e *takeEnv, o ownership.Offer) []string {
			e.site(tid(9), "kunde.example/blog")
			i := e.fs.infos[tid(1)]
			i.Domains = []string{"kunde.example"}
			e.fs.infos[tid(1)] = i
			return nil
		}, status: http.StatusConflict, want: "not available right now", state: ownership.StatePaused},
		{name: "accept-by passed", setup: func(e *takeEnv, o ownership.Offer) []string {
			e.change(o, func(x *ownership.Offer) { x.AcceptBy = time.Now().Add(-time.Hour) })
			return nil
		}, status: http.StatusConflict, want: "lapsed"},
		{name: "before available-from", setup: func(e *takeEnv, o ownership.Offer) []string {
			from := time.Now().Add(48 * time.Hour)
			e.change(o, func(x *ownership.Offer) { x.AvailableFrom = &from })
			return nil
		}, status: http.StatusConflict, want: "not available yet"},
		{name: "a paused offer", setup: func(e *takeEnv, o ownership.Offer) []string {
			e.change(o, func(x *ownership.Offer) { x.State = ownership.StatePaused })
			return nil
		}, status: http.StatusConflict, want: "not available right now", state: ownership.StatePaused},
		{name: "the sender downgraded", setup: func(e *takeEnv, o ownership.Offer) []string {
			e.setAccount(e.acc, func(a *account.Account) { a.Tier = "free" })
			return nil
		}, status: http.StatusConflict, want: "not available right now"},
		{name: "a site locked", setup: func(e *takeEnv, o ownership.Offer) []string {
			i := e.fs.infos[tid(2)]
			i.Locked = &ext.SiteLock{By: ext.LockByAdmin, At: time.Now()}
			e.fs.infos[tid(2)] = i
			return nil
		}, status: http.StatusConflict, want: "not available right now"},
		{name: "the sender suspended", setup: func(e *takeEnv, o ownership.Offer) []string {
			now := time.Now()
			e.setAccount(e.acc, func(a *account.Account) { a.SuspendedAt = &now })
			return nil
		}, status: http.StatusConflict, want: "not available right now"},
		{name: "the sender's plan cannot be read", setup: func(e *takeEnv, o ownership.Offer) []string {
			e.setAccount(e.acc, func(a *account.Account) { a.Tier = "legacy" })
			return nil
		}, status: http.StatusServiceUnavailable, want: "not available right now"},
		{name: "an unverified recipient", unverified: true, status: http.StatusNotFound},
		{name: "another account", as: func(e *takeEnv) (*http.Cookie, *account.Account) {
			return e.other("other@example.com")
		}, status: http.StatusNotFound},
		{name: "the sender", as: func(e *takeEnv) (*http.Cookie, *account.Account) {
			return e.cookie, e.acc
		}, status: http.StatusNotFound},
		{name: "no CSRF token", noCSRF: true, status: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tier := tc.toTier
			if tier == "" {
				tier = "pro"
			}
			e := newTakeEnv(t, tier, !tc.unverified)
			e.site(tid(1))
			e.site(tid(2))
			e.site(tid(3))
			o := e.offer(tid(1), tid(2), tid(3))
			sites := o.Sites
			if tc.setup != nil {
				if s := tc.setup(e, o); s != nil {
					sites = s
				}
			}
			c, acc := e.toCookie, e.to
			if tc.as != nil {
				c, acc = tc.as(e)
			}
			if tc.noCSRF {
				acc = nil
			}
			w := e.postOffer(c, acc, o.ID, "accept", sites)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("accept = %d, want %d with %q:\n%s", w.Code, tc.status, tc.want, w.Body)
			}
			e.unchanged([]string{tid(1), tid(2), tid(3)})
			want := tc.state
			if want == "" {
				want = ownership.StateOpen
			}
			if cur := e.current(o); cur.State != want || len(cur.Done) != 0 || cur.AcceptedBy != "" {
				t.Errorf("offer after = %+v, want state %s and nothing done", cur, want)
			}
			if strings.Contains(w.Body.String(), "NEWPW") {
				t.Error("a refusal shows a password")
			}
		})
	}
}

// A host conflict that appeared after the offer pauses it, and the sender is
// told why.
func TestTakeoverHostConflictPausesAndTellsTheSender(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	e.site(tid(1))
	e.site(tid(9), "kunde.example/blog")
	o := e.offer(tid(1))
	i := e.fs.infos[tid(1)]
	i.Domains = []string{"kunde.example"}
	e.fs.infos[tid(1)] = i

	if w := e.accept(o); w.Code != http.StatusConflict {
		t.Fatalf("accept = %d (%s)", w.Code, w.Body)
	}
	if cur := e.current(o); cur.State != ownership.StatePaused {
		t.Errorf("state = %s, want paused", cur.State)
	}
	m := e.mailsTo("pro@example.com", "paused")
	if len(m) != 1 || !strings.Contains(m[0], "kunde.example") || !strings.Contains(m[0], label(tid(9))) || !strings.Contains(m[0], takerEmail) {
		t.Fatalf("the sender's mail = %q", m)
	}
	if len(e.mailsTo(takerEmail, "paused")) != 0 {
		t.Error("the recipient was mailed about the pause")
	}
	// A second attempt on the paused offer mails nobody again.
	e.accept(o)
	if len(e.mailsTo("pro@example.com", "paused")) != 1 {
		t.Error("the sender was mailed twice")
	}
}

// ---- the card ----

func TestTakeoverCard(t *testing.T) {
	type tcase struct {
		name    string
		toTier  string
		unver   bool
		setup   func(e *takeEnv, o ownership.Offer)
		shown   bool
		accept  bool
		want    []string
		notWant []string
	}
	cases := []tcase{
		{name: "open", shown: true, accept: true, want: []string{"&#10003;", "Sites"}},
		{name: "a free plan", toTier: "free", shown: true, want: []string{"Your plan cannot take over sites", "/account#plan"}},
		{name: "over a cap", setup: func(e *takeEnv, o ownership.Offer) { e.fs.ad().files[tid(2)] = 99 }, shown: true, want: []string{"&#10007;", "Files", label(tid(2))}},
		{name: "paused by the sender's plan", setup: func(e *takeEnv, o ownership.Offer) {
			e.setAccount(e.acc, func(a *account.Account) { a.Tier = "free" })
		}, shown: true, want: []string{"not available right now"}, notWant: []string{"&#10003;"}},
		{name: "paused", setup: func(e *takeEnv, o ownership.Offer) {
			e.change(o, func(x *ownership.Offer) { x.State = ownership.StatePaused })
		}, shown: true, want: []string{"not available right now"}},
		{name: "before available-from", setup: func(e *takeEnv, o ownership.Offer) {
			from := time.Now().Add(48 * time.Hour)
			e.change(o, func(x *ownership.Offer) { x.AvailableFrom = &from })
		}},
		{name: "available-from passed", setup: func(e *takeEnv, o ownership.Offer) {
			from := time.Now().Add(-time.Hour)
			e.change(o, func(x *ownership.Offer) { x.AvailableFrom = &from })
		}, shown: true, accept: true},
		{name: "accept-by passed", setup: func(e *takeEnv, o ownership.Offer) {
			e.change(o, func(x *ownership.Offer) { x.AcceptBy = time.Now().Add(-time.Hour) })
		}},
		{name: "cancelled", setup: func(e *takeEnv, o ownership.Offer) {
			e.change(o, func(x *ownership.Offer) { x.State = ownership.StateCancelled })
		}},
		{name: "unverified", unver: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tier := tc.toTier
			if tier == "" {
				tier = "pro"
			}
			e := newTakeEnv(t, tier, !tc.unver)
			e.site(tid(1))
			e.site(tid(2))
			o := e.offer(tid(1), tid(2))
			if tc.setup != nil {
				tc.setup(e, o)
			}
			dash := e.dashboard(e.toCookie)
			if got := strings.Contains(dash, "Ready to take over"); got != tc.shown {
				t.Fatalf("card shown = %v, want %v", got, tc.shown)
			}
			if got := strings.Contains(dash, "/account/transfer/"+o.ID+"/accept"); got != tc.accept {
				t.Errorf("accept button = %v, want %v", got, tc.accept)
			}
			if tc.shown && !strings.Contains(dash, "/account/transfer/"+o.ID+"/decline") {
				t.Error("the card has no decline button")
			}
			for _, w := range tc.want {
				if !strings.Contains(dash, w) {
					t.Errorf("the card lacks %q", w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(dash, w) {
					t.Errorf("the card shows %q", w)
				}
			}
		})
	}
}

// ---- concurrency ----

func TestTakeoverConcurrentAccepts(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	e.site(tid(1))
	e.site(tid(2))
	o := e.offer(tid(1), tid(2))
	// The winner waits inside TransferSite until the loser has answered, so
	// the fake's maps are never written while the other request reads them.
	e.fs.xf().block = make(chan struct{})
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() { codes <- e.accept(o).Code }()
	}
	first := <-codes
	close(e.fs.xf().block)
	second := <-codes
	got := []int{first, second}
	slices.Sort(got)
	if !slices.Equal(got, []int{http.StatusOK, http.StatusConflict}) {
		t.Fatalf("answers = %v, want one 200 and one 409", got)
	}
	for _, id := range []string{tid(1), tid(2)} {
		if n := e.fs.xf().callsFor(id); n != 1 {
			t.Errorf("%s transferred %d times", id, n)
		}
	}
	if cur := e.current(o); cur.State != ownership.StateAccepted {
		t.Errorf("state = %s", cur.State)
	}
	if n := len(e.mailsTo("pro@example.com", "accepted")); n != 1 {
		t.Errorf("the sender got %d accepted mails", n)
	}
}

func TestTakeoverAcceptVersusCancel(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	for round := 0; round < 6; round++ {
		id := tid(10 + round)
		e.site(id)
		o := e.offer(id)
		var wg sync.WaitGroup
		var acceptCode, cancelCode int
		wg.Add(2)
		go func() { defer wg.Done(); acceptCode = e.accept(o).Code }()
		go func() {
			defer wg.Done()
			cancelCode = e.post("/account/transfer/"+o.ID+"/cancel", url.Values{}).Code
		}()
		wg.Wait()
		cur := e.current(o)
		owner, _ := e.fs.SiteOwner(id)
		switch {
		case acceptCode == http.StatusOK && cancelCode == http.StatusConflict:
			if cur.State != ownership.StateAccepted || owner != e.to.ID {
				t.Errorf("round %d: accept won, but state %s, owner %q", round, cur.State, owner)
			}
		case cancelCode == http.StatusSeeOther && acceptCode == http.StatusConflict:
			if cur.State != ownership.StateCancelled || owner != e.acc.ID || e.fs.xf().callsFor(id) != 0 {
				t.Errorf("round %d: cancel won, but state %s, owner %q", round, cur.State, owner)
			}
		default:
			t.Errorf("round %d: accept %d, cancel %d: want exactly one to win", round, acceptCode, cancelCode)
		}
	}
}

// ---- forward completion ----

func TestTakeoverCrashIsCompleted(t *testing.T) {
	a, b, c := tid(1), tid(2), tid(3)
	setup := func(t *testing.T) (*takeEnv, ownership.Offer) {
		e := newTakeEnv(t, "pro", true)
		e.site(a)
		e.site(b)
		e.site(c)
		o := e.offer(a, b, c)
		e.fs.xf().fail[b] = errors.New("the disk went away")
		w := e.accept(o)
		if w.Code != http.StatusOK {
			t.Fatalf("accept = %d (%s)", w.Code, w.Body)
		}
		calls := e.fs.xf().all()
		if len(calls) != 1 || calls[0].ViewID != a || !strings.Contains(w.Body.String(), calls[0].Password) {
			t.Fatalf("calls = %+v; the page must show the moved site's password", calls)
		}
		if cur := e.current(o); cur.State != ownership.StateAccepting || !slices.Equal(cur.Done, []string{a}) {
			t.Fatalf("offer after the failure = %+v, want accepting with %s done", cur, a)
		}
		if strings.Contains(w.Body.String(), "NEWPW02") {
			t.Error("a site that did not move shows a password")
		}
		return e, o
	}

	t.Run("the loop finishes the rest", func(t *testing.T) {
		e, o := setup(t)
		run, err := e.p.completeAccepting(e.current(o), e.to, ext.ChangeBy{Account: e.to.ID})
		if err != nil || !run.Complete || len(run.Moved) != 2 {
			t.Fatalf("completeAccepting = %+v, %v", run, err)
		}
		for _, id := range []string{a, b, c} {
			if n := e.fs.xf().callsFor(id); n != 1 {
				t.Errorf("%s transferred %d times", id, n)
			}
			if owner, _ := e.fs.SiteOwner(id); owner != e.to.ID {
				t.Errorf("owner of %s = %q", id, owner)
			}
		}
		for _, cl := range e.fs.xf().all()[1:] {
			if cl.By.Surface != provenance.SurfaceBackground || cl.By.IP != "" {
				t.Errorf("the loop's ChangeBy = %+v, want the background surface", cl.By)
			}
		}
		for _, acc := range []*account.Account{e.acc, e.to} {
			es, _ := e.p.accounts.Provenance(acc.ID)
			for _, en := range es {
				if en.Action != provenance.ActionTransfer {
					continue
				}
				background := en.Site == b || en.Site == c
				if background && (en.Surface != provenance.SurfaceBackground || en.Auth != provenance.AuthNone) {
					t.Errorf("a loop's log line in %s = %s/%s, want background with no session", acc.Email, en.Surface, en.Auth)
				}
				if !background && (en.Surface != provenance.SurfaceDashboard || en.Auth != provenance.AuthSession) {
					t.Errorf("the accept's log line in %s = %s/%s, want dashboard/session", acc.Email, en.Surface, en.Auth)
				}
			}
		}
		if cur := e.current(o); cur.State != ownership.StateAccepted || len(cur.Done) != 3 {
			t.Errorf("offer = %+v", cur)
		}
		if n := len(e.mailsTo("pro@example.com", "accepted")); n != 1 {
			t.Errorf("the sender got %d accepted mails", n)
		}
		// Calling it again changes nothing.
		if _, err := e.p.completeAccepting(e.current(o), e.to, ext.ChangeBy{Account: e.to.ID}); err == nil {
			t.Error("completing an accepted offer again did not refuse")
		}
		if len(e.fs.xf().all()) != 3 {
			t.Error("a second completion transferred again")
		}
		d := getAs(e.mux, "/account/transfer/"+o.ID+"/done", e.toCookie)
		if d.Code != http.StatusOK || strings.Contains(d.Body.String(), "NEWPW") || !strings.Contains(d.Body.String(), label(c)) {
			t.Errorf("done = %d:\n%s", d.Code, d.Body)
		}
	})

	t.Run("a site that changed hands stays with the sender", func(t *testing.T) {
		e, o := setup(t)
		delete(e.fs.infos, c) // the sender deleted c meanwhile
		run, err := e.p.completeAccepting(e.current(o), e.to, ext.ChangeBy{Account: e.to.ID})
		if err != nil || run.Complete || !run.Ended || len(run.Moved) != 0 {
			t.Fatalf("completeAccepting = %+v, %v", run, err)
		}
		if owner, _ := e.fs.SiteOwner(b); owner != e.acc.ID {
			t.Errorf("b moved although the bundle's rest could not: owner %q", owner)
		}
		cur := e.current(o)
		if !ownership.IsFinal(cur.State) || !slices.Equal(cur.Done, []string{a}) {
			t.Errorf("offer = %+v, want a final state with only %s done", cur, a)
		}
		sm := e.mailsTo("pro@example.com", "")
		rm := e.mailsTo(takerEmail, "")
		if !slices.ContainsFunc(sm, func(m string) bool {
			return strings.Contains(m, label(b)) && strings.Contains(m, "stay") && strings.Contains(m, "no longer exists")
		}) {
			t.Errorf("the sender was not told what stays and why: %q", sm)
		}
		if !slices.ContainsFunc(rm, func(m string) bool {
			return strings.Contains(m, label(b)) && strings.Contains(m, "stay") && strings.Contains(m, "could not be completed and has ended")
		}) {
			t.Errorf("the recipient was not told what stays: %q", rm)
		}
		for _, m := range rm {
			if strings.Contains(m, "no longer exists") || strings.Contains(m, "sender's") {
				t.Errorf("the recipient's mail carries the sender's reason:\n%s", m)
			}
		}
	})

	t.Run("a move whose record was lost is not repeated", func(t *testing.T) {
		e := newTakeEnv(t, "pro", true)
		e.site(a)
		e.site(b)
		o := e.offer(a, b)
		// The core moved a, then the process died before the offer recorded it.
		e.change(o, func(x *ownership.Offer) { x.State, x.AcceptedBy = ownership.StateAccepting, e.to.ID })
		i := e.fs.infos[a]
		i.Owner = e.to.ID
		e.fs.infos[a] = i
		run, err := e.p.completeAccepting(e.current(o), e.to, ext.ChangeBy{Account: e.to.ID})
		if err != nil || !run.Complete {
			t.Fatalf("completeAccepting = %+v, %v", run, err)
		}
		if e.fs.xf().callsFor(a) != 0 || e.fs.xf().callsFor(b) != 1 {
			t.Errorf("calls a=%d b=%d, want 0 and 1", e.fs.xf().callsFor(a), e.fs.xf().callsFor(b))
		}
		if ids, _ := e.p.accounts.ListSiteIDs(e.to); !slices.Equal(sortedCopy(ids), []string{a, b}) {
			t.Errorf("recipient markers = %v", ids)
		}
		if ids, _ := e.p.accounts.ListSiteIDs(e.acc); len(ids) != 0 {
			t.Errorf("sender markers = %v", ids)
		}
	})
}

// ---- container sites ----

func TestTakeoverMovesContainerServices(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	k, k2 := tid(1), tid(2)
	e.site(k)
	e.site(k2)
	running := func(id string, n int) ext.ContainerSite {
		cs := ext.ContainerSite{ViewID: id, Container: true, Enabled: true, Observed: ext.ContainerState{Status: "running"}}
		for i := 0; i < n; i++ {
			cs.Observed.Services = append(cs.Observed.Services, ext.ContainerService{Name: fmt.Sprintf("svc%d", i)})
		}
		return cs
	}
	e.fs.containers = []ext.ContainerSite{running(k, 2), running(k2, 1)}
	// The runtime's count, driven by meta.json's owner as the real one is.
	e.p.servicesOf = func(owner string) (int, error) {
		all, err := e.fs.ContainerSites()
		if err != nil {
			return 0, err
		}
		n := 0
		for _, cs := range all {
			if info, _ := e.fs.info(cs.ViewID); info.Owner == owner && containers.Counts(cs) {
				n += len(cs.Observed.Services)
			}
		}
		return n, nil
	}
	if n, _ := e.p.servicesOf(e.acc.ID); n != 3 {
		t.Fatalf("the sender runs %d services, want 3", n)
	}

	// Both sites together are 3 services; Pro allows 2.
	both := e.offer(k, k2)
	w := e.accept(both)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "Container services") {
		t.Fatalf("a bundle over the container cap = %d:\n%s", w.Code, w.Body)
	}
	e.unchanged([]string{k, k2})
	e.change(both, func(x *ownership.Offer) { x.State = ownership.StateCancelled })

	one := e.offer(k)
	if w := e.accept(one); w.Code != http.StatusOK {
		t.Fatalf("accept = %d (%s)", w.Code, w.Body)
	}
	if n, _ := e.p.servicesOf(e.acc.ID); n != 1 {
		t.Errorf("the sender runs %d services after, want 1", n)
	}
	if n, _ := e.p.servicesOf(e.to.ID); n != 2 {
		t.Errorf("the recipient runs %d services after, want 2", n)
	}
}

// ---- decline ----

func TestTakeoverDecline(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	e.site(tid(1))
	o := e.offer(tid(1))

	oc, other := e.other("other@example.com")
	if w := e.postOffer(oc, other, o.ID, "decline", o.Sites); w.Code != http.StatusNotFound {
		t.Errorf("another account's decline = %d, want 404", w.Code)
	}
	if w := e.postOffer(e.toCookie, nil, o.ID, "decline", o.Sites); w.Code != http.StatusForbidden {
		t.Errorf("decline without CSRF = %d, want 403", w.Code)
	}
	if cur := e.current(o); cur.State != ownership.StateOpen {
		t.Fatalf("state = %s after refused declines", cur.State)
	}

	w := e.decline(o)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account" {
		t.Fatalf("decline = %d -> %q (%s)", w.Code, w.Header().Get("Location"), w.Body)
	}
	if cur := e.current(o); cur.State != ownership.StateDeclined {
		t.Errorf("state = %s, want declined", cur.State)
	}
	e.unchanged([]string{tid(1)})
	m := e.mailsTo("pro@example.com", "declined")
	if len(m) != 1 || !strings.Contains(m[0], takerEmail) || !strings.Contains(m[0], label(tid(1))) {
		t.Errorf("the sender's mail = %q", m)
	}
	if strings.Contains(e.dashboard(e.toCookie), "Ready to take over") {
		t.Error("a declined offer still shows its card")
	}
	// Declined is final: no second decline, no accept.
	if w := e.decline(o); w.Code != http.StatusConflict {
		t.Errorf("a second decline = %d, want 409", w.Code)
	}
	if w := e.accept(o); w.Code != http.StatusConflict {
		t.Errorf("accepting a declined offer = %d, want 409", w.Code)
	}
	if n := len(e.mailsTo("pro@example.com", "declined")); n != 1 {
		t.Errorf("declined mails = %d, want 1", n)
	}

	// A paused offer can be declined.
	e.site(tid(2))
	p := e.offer(tid(2))
	e.change(p, func(x *ownership.Offer) { x.State = ownership.StatePaused })
	if w := e.decline(p); w.Code != http.StatusSeeOther {
		t.Errorf("declining a paused offer = %d", w.Code)
	}
}

// ---- fix round 1 ----

// A background completion never runs beside a live accept of the same offer:
// it would take a site the accept just moved for one that changed hands.
func TestTakeoverCompletionWaitsForALiveAccept(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	a, b, c := tid(1), tid(2), tid(3)
	e.site(a)
	e.site(b)
	e.site(c)
	o := e.offer(a, b, c)
	x := e.fs.xf()
	x.block, x.entered = make(chan struct{}), make(chan string, 3)
	res := make(chan *httptest.ResponseRecorder, 1)
	go func() { res <- e.accept(o) }()
	if first := <-x.entered; first != a {
		t.Fatalf("the accept moved %s first", first)
	}
	// The live accept is inside TransferSite(a): the loop must stand aside.
	run, err := e.p.completeAccepting(e.current(o), e.to, ext.ChangeBy{Account: e.to.ID})
	if !errors.Is(err, errAcceptBusy) || len(run.Moved) != 0 {
		t.Fatalf("completeAccepting beside a live accept = %+v, %v; want errAcceptBusy", run, err)
	}
	if cur := e.current(o); cur.State != ownership.StateAccepting || len(cur.Done) != 0 {
		t.Fatalf("the loop changed the offer: %+v", cur)
	}
	close(x.block)
	w := <-res
	if w.Code != http.StatusOK {
		t.Fatalf("accept = %d (%s)", w.Code, w.Body)
	}
	for _, cl := range x.all() {
		if strings.Count(w.Body.String(), cl.Password) != 1 {
			t.Errorf("the password of %s is not shown once", cl.ViewID)
		}
	}
	if strings.Contains(w.Body.String(), "Still to move") {
		t.Error("the page lists a site as still to move")
	}
	if cur := e.current(o); cur.State != ownership.StateAccepted || len(cur.Done) != 3 {
		t.Errorf("offer = %+v", cur)
	}
	if n := len(e.mailsTo("pro@example.com", "could not be completed")) + len(e.mailsTo(takerEmail, "could not be completed")); n != 0 {
		t.Errorf("%d false give-up mails", n)
	}
}

// An offer another run holds is not accepted beside it: the page asks for a
// reload, and nothing changes.
func TestTakeoverClaimedOfferAsksForAReload(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	e.site(tid(4))
	o2 := e.offer(tid(4))
	release, ok := e.p.claimAccept(o2.ID)
	if !ok {
		t.Fatal("could not claim a fresh offer")
	}
	if w := e.accept(o2); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already being accepted") {
		t.Errorf("accept of a claimed offer = %d:\n%s", w.Code, w.Body)
	}
	if cur := e.current(o2); cur.State != ownership.StateOpen {
		t.Errorf("a refused accept changed the state to %s", cur.State)
	}
	release()
	if w := e.accept(o2); w.Code != http.StatusOK {
		t.Errorf("accept after the claim ended = %d", w.Code)
	}
}

// The re-check finds a site no longer the sender's because the recipient
// holds it already: it is moved, not gone, and the rest still moves.
func TestTakeoverRecheckCountsTheRecipientsSiteAsMoved(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	a, b, c := tid(1), tid(2), tid(3)
	e.site(a)
	e.site(b)
	e.site(c)
	o := e.offer(a, b, c)
	e.change(o, func(x *ownership.Offer) {
		x.State, x.AcceptedBy, x.Done = ownership.StateAccepting, e.to.ID, []string{a}
	})
	i := e.fs.infos[a]
	i.Owner = e.to.ID
	e.fs.infos[a] = i
	// b reaches the recipient between the scan and the re-check.
	reads := 0
	e.fs.onOwner = func(id string) {
		if id != b {
			return
		}
		if reads++; reads == 2 {
			info := e.fs.infos[b]
			info.Owner = e.to.ID
			e.fs.infos[b] = info
		}
	}
	run, err := e.p.completeAccepting(e.current(o), e.to, ext.ChangeBy{Account: e.to.ID})
	if err != nil || !run.Complete || run.Ended {
		t.Fatalf("completeAccepting = %+v, %v; want complete", run, err)
	}
	if e.fs.xf().callsFor(b) != 0 || e.fs.xf().callsFor(c) != 1 {
		t.Errorf("transfers b=%d c=%d, want 0 and 1", e.fs.xf().callsFor(b), e.fs.xf().callsFor(c))
	}
	if cur := e.current(o); cur.State != ownership.StateAccepted || !slices.Equal(sortedCopy(cur.Done), []string{a, b, c}) {
		t.Errorf("offer = %+v", cur)
	}
}

// A site the core moved keeps its password on the page, and is never listed
// as still to move, even when recording it in the offer fails.
func TestTakeoverMovedSiteKeepsItsPasswordWhenTheRecordFails(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	a, b := tid(1), tid(2)
	e.site(a)
	e.site(b)
	o := e.offer(a, b)
	// The offer cannot take the record (here: its state changed under it).
	e.fs.xf().after = func(id string) {
		if id == a {
			e.p.owner.UpdateOffer(o.ID, func(x *ownership.Offer) error { x.State = ownership.StateCancelled; return nil })
		}
	}
	w := e.accept(o)
	body := w.Body.String()
	calls := e.fs.xf().all()
	if w.Code != http.StatusOK || len(calls) != 1 || strings.Count(body, calls[0].Password) != 1 {
		t.Fatalf("accept = %d, calls %d:\n%s", w.Code, len(calls), body)
	}
	_, pending, _ := strings.Cut(body, "Still to move")
	if strings.Count(body, label(a)) != 1 || strings.Contains(pending, label(a)) || !strings.Contains(pending, label(b)) {
		t.Errorf("the moved site is listed twice or as still to move:\n%s", body)
	}
}

// The stamp comes from the strict tier: an unknown plan stops the move.
func TestTakeoverGrantIsStrict(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	e.site(tid(1))
	o := e.offer(tid(1))
	e.change(o, func(x *ownership.Offer) { x.State, x.AcceptedBy = ownership.StateAccepting, e.to.ID })
	e.setAccount(e.to, func(a *account.Account) { a.Tier = "legacy" })
	release, _ := e.p.claimAccept(o.ID)
	run, err := e.p.runAccepting(o.ID, e.to, ext.ChangeBy{Account: e.to.ID}, false)
	release()
	if err == nil || len(run.Moved) != 0 {
		t.Fatalf("a move on an unknown plan = %+v, %v", run, err)
	}
	e.unchanged([]string{tid(1)})
	if cur := e.current(o); cur.State != ownership.StateAccepting {
		t.Errorf("state = %s, want accepting (unknown changes nothing)", cur.State)
	}
	if g, err := e.p.transferGrant(e.acc); err != nil || g.OwnerAccountID != e.acc.ID || g.MaxSiteBytes != 1000 {
		t.Errorf("transferGrant on a known plan = %+v, %v", g, err)
	}
}

// A give-up that moved nothing reads as ended, never as the sender's cancel.
func TestTakeoverGiveUpWithNothingMovedIsEnded(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	a, b := tid(1), tid(2)
	e.site(a)
	e.site(b)
	o := e.offer(a, b)
	e.fs.xf().fail[a] = errors.New("the disk went away")
	if w := e.accept(o); w.Code != http.StatusOK || len(e.fs.xf().all()) != 0 {
		t.Fatalf("accept = %d", w.Code)
	}
	delete(e.fs.infos, b)
	run, err := e.p.completeAccepting(e.current(o), e.to, ext.ChangeBy{Account: e.to.ID})
	if err != nil || !run.Ended {
		t.Fatalf("completeAccepting = %+v, %v", run, err)
	}
	if cur := e.current(o); cur.State != ownership.StateEnded || !ownership.IsFinal(cur.State) {
		t.Fatalf("state = %s, want ended", cur.State)
	}
	w := e.accept(o)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Ended: no site could be moved") || strings.Contains(w.Body.String(), "cancelled") {
		t.Errorf("accepting an ended offer = %d:\n%s", w.Code, w.Body)
	}
	d := getAs(e.mux, "/account/transfer/"+o.ID+"/done", e.toCookie)
	if !strings.Contains(d.Body.String(), "Ended: no site could be moved") {
		t.Errorf("the done page:\n%s", d.Body)
	}
	if w := e.post("/account/transfer/"+o.ID+"/cancel", url.Values{}); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Ended: no site could be moved") {
		t.Errorf("the sender's cancel of an ended offer = %d:\n%s", w.Code, w.Body)
	}
	rm := e.mailsTo(takerEmail, "could not be completed")
	if len(rm) != 1 || strings.Contains(rm[0], "no longer exists") {
		t.Errorf("the recipient's mail = %q", rm)
	}
}

// The recipient is matched by the store's own normalisation of the address.
func TestIsRecipientUsesTheStoresNormalisation(t *testing.T) {
	o := ownership.Offer{From: "sender", ToEmail: ownership.NormEmail("  Taker@Example.COM ")}
	for _, tc := range []struct {
		acc  account.Account
		want bool
	}{
		{account.Account{ID: "r", Email: "taker@example.com", EmailVerified: true}, true},
		{account.Account{ID: "r", Email: " TAKER@example.com", EmailVerified: true}, true},
		{account.Account{ID: "r", Email: "taker@example.com", EmailVerified: false}, false},
		{account.Account{ID: "sender", Email: "taker@example.com", EmailVerified: true}, false},
		{account.Account{ID: "r", Email: "other@example.com", EmailVerified: true}, false},
	} {
		if got := isRecipient(&tc.acc, o); got != tc.want {
			t.Errorf("isRecipient(%+v) = %v, want %v", tc.acc, got, tc.want)
		}
	}
}

// The card reads each site's Info once, and shares it with the limits check;
// the pause check reads locks from meta.json only.
func TestTakeoverCardReadsEachSiteOnce(t *testing.T) {
	e := newTakeEnv(t, "pro", true)
	a, b := tid(1), tid(2)
	e.site(a)
	e.site(b)
	e.offer(a, b)
	e.fs.infoMu.Lock()
	e.fs.infoCalls = map[string]int{}
	e.fs.infoMu.Unlock()
	if dash := e.dashboard(e.toCookie); !strings.Contains(dash, "Ready to take over") {
		t.Fatal("no card")
	}
	counts := func(when string) {
		t.Helper()
		e.fs.infoMu.Lock()
		defer e.fs.infoMu.Unlock()
		for _, id := range []string{a, b} {
			if n := e.fs.infoCalls[id]; n != 1 {
				t.Errorf("%s: Info(%s) read %d times for one card, want 1", when, id, n)
			}
		}
		e.fs.infoCalls = map[string]int{}
	}
	counts("open")
	// A locked site pauses the card; naming it costs no second read.
	i := e.fs.infos[b]
	i.Locked = &ext.SiteLock{By: ext.LockByAdmin, At: time.Now()}
	e.fs.infos[b] = i
	if dash := e.dashboard(e.toCookie); !strings.Contains(dash, "not available right now") {
		t.Fatal("a locked site did not pause the card")
	}
	counts("paused")
}
