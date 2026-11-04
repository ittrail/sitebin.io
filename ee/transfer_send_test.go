//go:build ee

package ee

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
	"github.com/ittrail/sitebin.io/ee/ownership"
	"github.com/ittrail/sitebin.io/ee/smtp"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The fake core's side of the sender's page: the host rule and the form
// recipients, over fakeSites' infos.
func (s *fakeSites) HostConflicts(ids []string, sender string) map[string][]string {
	in := map[string]bool{}
	for _, id := range ids {
		in[id] = true
	}
	hosts := map[string]bool{}
	for _, id := range ids {
		for _, d := range s.infos[id].Domains {
			hosts[store.HostOf(d)] = true
		}
	}
	out := map[string][]string{}
	for id, info := range s.infos {
		if in[id] || info.Owner != sender {
			continue
		}
		for _, d := range info.Domains {
			if h := store.HostOf(d); hosts[h] && !slices.Contains(out[h], id) {
				out[h] = append(out[h], id)
			}
		}
	}
	return out
}

func (s *fakeSites) FormRecipients(id string) []string { return s.ad().forms[id] }

// ---- environment ----

type xferEnv struct {
	*adoptEnv
	mu    sync.Mutex
	mails []sentMail
}

type sentMail struct{ To, Msg string }

func newXferEnv(t *testing.T, tier string) *xferEnv {
	t.Helper()
	x := &xferEnv{adoptEnv: newAdoptEnv(t, tier)}
	x.p.mailer = smtp.NewWithDeliver(eeconfig.SMTPConfig{From: "no-reply@sitebin.example"}, func(_ eeconfig.SMTPConfig, to, msg string) error {
		x.mu.Lock()
		defer x.mu.Unlock()
		// Signing the test's people up mails them a verification link; only
		// the ownership mail is under test.
		if strings.Contains(msg, "Subject: A Sitebin transfer is offered to you") {
			x.mails = append(x.mails, sentMail{to, msg})
		}
		return nil
	})
	return x
}

// sent waits for the background mail and returns what was delivered.
func (x *xferEnv) sent() []sentMail {
	x.p.mailWG.Wait()
	x.mu.Lock()
	defer x.mu.Unlock()
	return slices.Clone(x.mails)
}

// tid is a valid site id (26 base32 characters) made from a number.
func tid(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	return strings.Repeat("a", 24) + string(letters[n/26%26]) + string(letters[n%26])
}

// site registers a site the sender owns, with verified custom domains.
func (x *xferEnv) site(id string, domains ...string) {
	x.t.Helper()
	if err := linkOwned(x.p, x.acc, id); err != nil {
		x.t.Fatal(err)
	}
	x.fs.infos[id] = ext.SiteInfo{ViewID: id, Mode: "webserver", Owner: x.acc.ID, ViewURL: "https://" + id + ".sitebin.app", Domains: domains}
}

// person registers another account; verified sets its EmailVerified.
func (x *xferEnv) person(email string, verified bool) {
	x.t.Helper()
	_, id := adminUser(x.t, x.p, x.mux, email, "pro")
	acc, err := x.p.accounts.ByID(id)
	if err != nil {
		x.t.Fatal(err)
	}
	if err := x.p.accounts.Update(acc, func(a *account.Account) error { a.EmailVerified = verified; return nil }); err != nil {
		x.t.Fatal(err)
	}
}

func daysAhead(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }

// offerForm is a create request at the confirming step.
func offerForm(email, accept, avail string, sites ...string) url.Values {
	v := url.Values{"email": {email}, "accept_by": {accept}, "site": sites, "step": {"create"}}
	if avail != "" {
		v.Set("available_from", avail)
	}
	return v
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = slices.Clone(vs)
	}
	return out
}

func (x *xferEnv) postAs(c *http.Cookie, csrf, path string, v url.Values) *httptest.ResponseRecorder {
	v = cloneValues(v)
	if csrf != "" {
		v.Set("csrf", csrf)
	}
	r := form(v)
	r.URL.Path = path
	if c != nil {
		r.AddCookie(c)
	}
	return serve(x.mux, r)
}

func (x *xferEnv) post(path string, v url.Values) *httptest.ResponseRecorder {
	return x.postAs(x.cookie, x.p.csrf(x.acc), path, v)
}

func (x *xferEnv) get(path string) *httptest.ResponseRecorder { return getAs(x.mux, path, x.cookie) }

func (x *xferEnv) offers() []ownership.Offer {
	x.t.Helper()
	os, err := x.p.owner.OffersBySender(x.acc.ID)
	if err != nil {
		x.t.Fatal(err)
	}
	return os
}

// ---- the answer never reveals ----

func TestTransferAnswerIsIdenticalForEveryAddress(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.person("known@example.com", true)
	x.person("unverified@example.com", false)
	for i := 1; i <= 3; i++ {
		x.site(tid(i))
	}
	emails := []string{"known@example.com", "unverified@example.com", "nobody@example.com"}
	var bodies []string
	for i, em := range emails {
		w := x.post("/account/transfer", offerForm(em, daysAhead(10), "", tid(i+1)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d (%s)", em, w.Code, w.Body)
		}
		if !strings.Contains(w.Body.String(), "ask the person to sign up with exactly this address") {
			t.Fatalf("%s: the answer lacks the never-reveal text:\n%s", em, w.Body)
		}
		bodies = append(bodies, w.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("the answers differ between a known, an unverified and an unknown address")
	}
	if len(x.offers()) != 3 {
		t.Fatalf("offers = %d, want 3", len(x.offers()))
	}
	mails := x.sent()
	if len(mails) != 1 || mails[0].To != "known@example.com" {
		t.Fatalf("mails = %+v, want exactly one, to the verified account", mails)
	}
	m := mails[0].Msg
	for _, want := range []string{"pro@example.com", tid(1), daysAhead(10), "http://sitebin.example/account"} {
		if !strings.Contains(m, want) {
			t.Errorf("the mail lacks %q:\n%s", want, m)
		}
	}
	if strings.Contains(strings.ToLower(m), "password") {
		t.Errorf("the mail mentions a password:\n%s", m)
	}
	var notified int
	for _, o := range x.offers() {
		if o.NotifiedAt != nil {
			notified++
		}
	}
	if notified != 3 {
		t.Errorf("NotifiedAt set on %d offers, want all 3 (the sender cannot tell them apart)", notified)
	}
}

func TestTransferFutureAvailableFromSendsNoMail(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.person("known@example.com", true)
	x.site(tid(1))
	w := x.post("/account/transfer", offerForm("known@example.com", daysAhead(20), daysAhead(5), tid(1)))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", w.Code, w.Body)
	}
	if mails := x.sent(); len(mails) != 0 {
		t.Errorf("mails = %+v, want none before the available-from date", mails)
	}
	if o := x.offers(); len(o) != 1 || o[0].NotifiedAt != nil || o[0].AvailableFrom == nil {
		t.Errorf("offer = %+v, want NotifiedAt unset and the date kept", o)
	}
	// An available-from that has already passed mails at once.
	x.site(tid(2))
	x.post("/account/transfer", offerForm("known@example.com", daysAhead(20), daysAhead(-2), tid(2)))
	if mails := x.sent(); len(mails) != 1 {
		t.Errorf("mails = %d, want 1 for a date already passed", len(mails))
	}
}

func TestTransferDailyLimit(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.site(tid(1))
	for i := 0; i < 10; i++ {
		if _, err := x.p.owner.CreateInvite(ownership.Invite{Site: tid(100 + i), From: x.acc.ID, ToEmail: "a@example.com"}, 0); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 9; i++ {
		if _, err := x.p.owner.CreateOffer(ownership.Offer{Sites: []string{tid(200 + i)}, From: x.acc.ID, ToEmail: "a@example.com", AcceptBy: time.Now().Add(time.Hour)}, 0); err != nil {
			t.Fatal(err)
		}
	}
	// 19 today: the 20th is allowed, the 21st is not.
	w := x.post("/account/transfer", offerForm("a@example.com", daysAhead(3), "", tid(1)))
	if w.Code != http.StatusOK {
		t.Fatalf("20th = %d (%s)", w.Code, w.Body)
	}
	x.site(tid(2))
	w = x.post("/account/transfer", offerForm("a@example.com", daysAhead(3), "", tid(2)))
	if w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "20") {
		t.Fatalf("21st = %d, want a refusal naming the limit (%s)", w.Code, w.Body)
	}
	if n := len(x.offers()); n != 10 {
		t.Errorf("offers = %d, want 10", n)
	}
}

// ---- refusals ----

func TestTransferRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(x *xferEnv) url.Values
		want  string
		code  int
	}{
		{"no sites", func(x *xferEnv) url.Values { return offerForm("a@example.com", daysAhead(5), "") }, "at least one", 400},
		{"too many sites", func(x *xferEnv) url.Values {
			var ids []string
			for i := 0; i < 51; i++ {
				x.site(tid(i + 1))
				ids = append(ids, tid(i+1))
			}
			return offerForm("a@example.com", daysAhead(5), "", ids...)
		}, "at most 50", 400},
		{"not owned", func(x *xferEnv) url.Values {
			x.fs.infos[tid(9)] = ext.SiteInfo{ViewID: tid(9), Owner: "someone-else"}
			return offerForm("a@example.com", daysAhead(5), "", tid(9))
		}, "not sites of this account", 400},
		{"locked", func(x *xferEnv) url.Values {
			x.site(tid(1))
			i := x.fs.infos[tid(1)]
			i.Locked = &ext.SiteLock{At: time.Now(), By: "operator"}
			x.fs.infos[tid(1)] = i
			return offerForm("a@example.com", daysAhead(5), "", tid(1))
		}, "locked", 400},
		{"no accept-by", func(x *xferEnv) url.Values { x.site(tid(1)); return offerForm("a@example.com", "", "", tid(1)) }, "accept-by date", 400},
		{"accept-by in the past", func(x *xferEnv) url.Values {
			x.site(tid(1))
			return offerForm("a@example.com", daysAhead(-1), "", tid(1))
		}, "in the future", 400},
		{"accept-by too far", func(x *xferEnv) url.Values {
			x.site(tid(1))
			return offerForm("a@example.com", daysAhead(366), "", tid(1))
		}, "365 days", 400},
		{"available-from after accept-by", func(x *xferEnv) url.Values {
			x.site(tid(1))
			return offerForm("a@example.com", daysAhead(5), daysAhead(6), tid(1))
		}, "before the accept-by", 400},
		{"available-from equals accept-by", func(x *xferEnv) url.Values {
			x.site(tid(1))
			return offerForm("a@example.com", daysAhead(5), daysAhead(5), tid(1))
		}, "before the accept-by", 400},
		{"own address", func(x *xferEnv) url.Values {
			x.site(tid(1))
			return offerForm(" PRO@example.com ", daysAhead(5), "", tid(1))
		}, "your own address", 400},
		{"not an address", func(x *xferEnv) url.Values {
			x.site(tid(1))
			return offerForm("not-an-address", daysAhead(5), "", tid(1))
		}, "valid email address", 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := newXferEnv(t, "pro")
			v := c.setup(x)
			w := x.post("/account/transfer", v)
			if w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
				t.Fatalf("= %d, want %d with %q:\n%s", w.Code, c.code, c.want, w.Body)
			}
			if n := len(x.offers()); n != 0 {
				t.Errorf("a refused request left %d offers", n)
			}
			if mails := x.sent(); len(mails) != 0 {
				t.Errorf("a refused request mailed %d", len(mails))
			}
		})
	}
}

func TestTransferNeedsASharingPlan(t *testing.T) {
	x := newXferEnv(t, "free")
	x.site(tid(1))
	w := x.post("/account/transfer", offerForm("a@example.com", daysAhead(5), "", tid(1)))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "/account#plan") {
		t.Fatalf("= %d, want 403 with a link to the plan (%s)", w.Code, w.Body)
	}
	if w := x.get("/account/transfer"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "/account#plan") {
		t.Errorf("GET = %d, want the same plan page", w.Code)
	}
	if len(x.offers()) != 0 {
		t.Error("an offer was made without a sharing plan")
	}
}

func TestTransferPlanLookupErrorIsRetryNeverPass(t *testing.T) {
	// Tiers mode with an account whose tier cannot be resolved.
	x := newXferEnv(t, "nosuchtier")
	x.site(tid(1))
	w := x.post("/account/transfer", offerForm("a@example.com", daysAhead(5), "", tid(1)))
	if w.Code != http.StatusServiceUnavailable || len(x.offers()) != 0 {
		t.Fatalf("= %d with %d offers, want exactly 503 and none", w.Code, len(x.offers()))
	}
	v := offerForm("a@example.com", daysAhead(5), "", tid(1))
	v.Set("step", "form")
	if w := x.post("/account/transfer", v); w.Code != http.StatusServiceUnavailable {
		t.Errorf("step=form with an unknown plan = %d, want 503", w.Code)
	}
}

func TestTransferDeduplicatesSites(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.site(tid(1))
	x.site(tid(2))
	w := x.post("/account/transfer", offerForm("a@example.com", daysAhead(5), "", tid(1), tid(2), tid(1)))
	if w.Code != http.StatusOK {
		t.Fatalf("= %d (%s)", w.Code, w.Body)
	}
	o := x.offers()
	if len(o) != 1 || len(o[0].Sites) != 2 {
		t.Fatalf("offers = %+v, want one offer of two sites", o)
	}
}

func TestTransferHostConflictOffersAddingThem(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.site(tid(1), "kunde.at")
	x.site(tid(2), "kunde.at/blog")
	x.site(tid(3))
	w := x.post("/account/transfer", offerForm("a@example.com", daysAhead(5), "", tid(1)))
	body := w.Body.String()
	if w.Code != http.StatusConflict || !strings.Contains(body, "Add them to the bundle") || !strings.Contains(body, tid(2)) {
		t.Fatalf("= %d, want the conflict page naming the other site (%s)", w.Code, body)
	}
	if strings.Contains(body, tid(3)) {
		t.Error("the conflict page names a site that does not conflict")
	}
	if len(x.offers()) != 0 {
		t.Fatal("an offer was made despite the host rule")
	}
	// The button: the form recreated with the conflicting site added.
	if !strings.Contains(body, `name="csrf"`) || !strings.Contains(body, `name="step" value="form"`) {
		t.Fatalf("the add button is not a CSRF form that recreates the form:\n%s", body)
	}
	w = x.post("/account/transfer", url.Values{"email": {"a@example.com"}, "accept_by": {daysAhead(5)}, "site": {tid(1), tid(2)}, "step": {"form"}})
	if w.Code != http.StatusOK || strings.Count(w.Body.String(), " checked") < 2 {
		t.Fatalf("recreated form = %d, want both sites checked (%s)", w.Code, w.Body)
	}
	if len(x.offers()) != 0 {
		t.Fatal("recreating the form made an offer")
	}
	w = x.post("/account/transfer", offerForm("a@example.com", daysAhead(5), "", tid(1), tid(2)))
	if w.Code != http.StatusOK || len(x.offers()) != 1 {
		t.Fatalf("with both sites: %d, offers %d", w.Code, len(x.offers()))
	}
}

func TestTransferReplaceNeedsConfirmation(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.site(tid(1))
	x.site(tid(2))
	if w := x.post("/account/transfer", offerForm("old@example.com", daysAhead(5), "", tid(1))); w.Code != http.StatusOK {
		t.Fatalf("first offer = %d (%s)", w.Code, w.Body)
	}
	old := x.offers()[0]

	w := x.post("/account/transfer", offerForm("new@example.com", daysAhead(6), "", tid(1), tid(2)))
	body := w.Body.String()
	if w.Code != http.StatusConflict || !strings.Contains(body, "old@example.com") || !strings.Contains(body, tid(1)) {
		t.Fatalf("without confirming = %d, want a page naming the old bundle (%s)", w.Code, body)
	}
	if offs := x.offers(); len(offs) != 1 || offs[0].State != ownership.StateOpen {
		t.Fatalf("nothing may change without confirmation: %+v", offs)
	}

	v := offerForm("new@example.com", daysAhead(6), "", tid(1), tid(2))
	v.Set("replace", "1")
	if w := x.post("/account/transfer", v); w.Code != http.StatusOK {
		t.Fatalf("confirmed = %d (%s)", w.Code, w.Body)
	}
	got, _ := x.p.owner.GetOffer(old.ID)
	if got.State != ownership.StateCancelled {
		t.Errorf("old offer is %q, want cancelled", got.State)
	}
	var open []ownership.Offer
	for _, o := range x.offers() {
		if !ownership.IsFinal(o.State) {
			open = append(open, o)
		}
	}
	if len(open) != 1 || open[0].ToEmail != "new@example.com" || len(open[0].Sites) != 2 {
		t.Errorf("open offers = %+v, want the new bundle only", open)
	}
}

func TestTransferReviewStepShowsNoticesAndCreatesNothing(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.site(tid(1), "kunde.at")
	x.fs.ad().notMove[tid(1)] = []string{"shop.kunde-zone.at"}
	x.fs.ad().forms[tid(1)] = []string{"office@kunde.at"}
	v := offerForm("a@example.com", daysAhead(5), "", tid(1))
	v.Del("step")
	w := x.post("/account/transfer", v)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("= %d (%s)", w.Code, w.Body)
	}
	for _, want := range []string{"shop.kunde-zone.at", "office@kunde.at", "submissions keep going to", "pending domain claims", "do not travel", `value="create"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the confirm step lacks %q:\n%s", want, body)
		}
	}
	if len(x.offers()) != 0 {
		t.Error("the review step made an offer")
	}
}

// ---- the page ----

// checkedBox reports whether the checkbox with this value carries "checked".
func checkedBox(body, id string) bool {
	i := strings.Index(body, `value="`+id+`"`)
	if i < 0 {
		return false
	}
	end := strings.Index(body[i:], ">")
	return strings.Contains(body[i:i+end], "checked")
}

func TestTransferPageListsAndPreselects(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.site(tid(1), "kunde.at")
	x.site(tid(2), "kunde.at/blog")
	x.site(tid(3))
	i := x.fs.infos[tid(3)]
	i.Tags = []string{"Müller GmbH"}
	x.fs.infos[tid(3)] = i
	x.fs.ad().notMove[tid(1)] = []string{"shop.kunde-zone.at"}
	x.fs.ad().forms[tid(1)] = []string{"office@kunde.at"}

	body := x.get("/account/transfer").Body.String()
	for _, id := range []string{tid(1), tid(2), tid(3)} {
		if !strings.Contains(body, `value="`+id+`"`) || checkedBox(body, id) {
			t.Errorf("site %s: not listed or preselected without a query", id)
		}
	}
	if !strings.Contains(body, "host=kunde.at") || !strings.Contains(body, "tag=") {
		t.Errorf("the host and tag helpers are missing:\n%s", body)
	}

	body = x.get("/account/transfer?site=" + tid(1)).Body.String()
	if !checkedBox(body, tid(1)) || checkedBox(body, tid(2)) {
		t.Errorf("?site= preselects wrongly")
	}
	if !strings.Contains(body, "shop.kunde-zone.at") || !strings.Contains(body, "office@kunde.at") {
		t.Errorf("the notices for the selected site are missing")
	}
	body = x.get("/account/transfer?host=kunde.at").Body.String()
	if !checkedBox(body, tid(1)) || !checkedBox(body, tid(2)) || checkedBox(body, tid(3)) {
		t.Errorf("?host= preselects wrongly")
	}
	body = x.get("/account/transfer?tag=" + url.QueryEscape("müller gmbh")).Body.String()
	if checkedBox(body, tid(1)) || !checkedBox(body, tid(3)) {
		t.Errorf("?tag= preselects wrongly")
	}
	// A site that is not the viewer's is never listed.
	x.fs.infos[tid(9)] = ext.SiteInfo{ViewID: tid(9), Owner: "other"}
	if body := x.get("/account/transfer?site=" + tid(9)).Body.String(); strings.Contains(body, tid(9)) {
		t.Error("a foreign site shows on the page")
	}
	// The dashboard links each owned site.
	if dash := x.get("/account").Body.String(); !strings.Contains(dash, "/account/transfer?site="+tid(1)) {
		t.Error("the dashboard row has no Transfer link")
	}
	// Signed out: sign-in.
	if w := getAs(x.mux, "/account/transfer", nil); w.Code != http.StatusSeeOther {
		t.Errorf("signed out = %d", w.Code)
	}
}

// ---- managing an open offer ----

func (x *xferEnv) openOffer(sites ...string) ownership.Offer {
	x.t.Helper()
	w := x.post("/account/transfer", offerForm("a@example.com", daysAhead(10), "", sites...))
	if w.Code != http.StatusOK {
		x.t.Fatalf("offer = %d (%s)", w.Code, w.Body)
	}
	return x.offers()[0]
}

func sorted(s []string) []string { c := slices.Clone(s); slices.Sort(c); return c }

func TestTransferSenderCanCancelChangeDatesAndSites(t *testing.T) {
	x := newXferEnv(t, "pro")
	for i := 1; i <= 4; i++ {
		x.site(tid(i))
	}
	o := x.openOffer(tid(1), tid(2))
	base := "/account/transfer/" + o.ID

	// Dates.
	w := x.post(base+"/dates", url.Values{"accept_by": {daysAhead(20)}, "available_from": {daysAhead(3)}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("dates = %d (%s)", w.Code, w.Body)
	}
	got, _ := x.p.owner.GetOffer(o.ID)
	if got.AcceptBy.UTC().Format("2006-01-02") != daysAhead(20) || got.AvailableFrom == nil || got.AvailableFrom.UTC().Format("2006-01-02") != daysAhead(3) {
		t.Errorf("dates = %v / %v", got.AcceptBy, got.AvailableFrom)
	}
	if w := x.post(base+"/dates", url.Values{"accept_by": {daysAhead(-1)}}); w.Code != http.StatusBadRequest {
		t.Errorf("a past date = %d, want 400", w.Code)
	}
	if w := x.post(base+"/dates", url.Values{"accept_by": {daysAhead(400)}}); w.Code != http.StatusBadRequest {
		t.Errorf("a far date = %d, want 400", w.Code)
	}
	if w := x.post(base+"/dates", url.Values{"accept_by": {daysAhead(5)}, "available_from": {daysAhead(5)}}); w.Code != http.StatusBadRequest {
		t.Errorf("available-from not before accept-by = %d, want 400", w.Code)
	}
	if w := x.post(base+"/dates", url.Values{"accept_by": {daysAhead(20)}}); w.Code != http.StatusSeeOther {
		t.Fatalf("clearing available-from = %d", w.Code)
	}
	if got, _ = x.p.owner.GetOffer(o.ID); got.AvailableFrom != nil {
		t.Error("available-from was not cleared")
	}

	// Sites: add one, remove one.
	// Adding is reviewed first: nothing changes until the notices are confirmed.
	x.fs.ad().forms[tid(3)] = []string{"office@kunde.at"}
	review := x.post(base+"/sites", url.Values{"add": {tid(3)}, "remove": {tid(1)}})
	if review.Code != http.StatusOK || !strings.Contains(review.Body.String(), "office@kunde.at") || !strings.Contains(review.Body.String(), "do not travel") || !strings.Contains(review.Body.String(), `name="confirm" value="1"`) {
		t.Fatalf("add review = %d, want the notices page (%s)", review.Code, review.Body)
	}
	if got, _ = x.p.owner.GetOffer(o.ID); !slices.Equal(sorted(got.Sites), []string{tid(1), tid(2)}) {
		t.Fatalf("the review changed the offer: %v", got.Sites)
	}
	if w := x.post(base+"/sites", url.Values{"add": {tid(3)}, "remove": {tid(1)}, "confirm": {"1"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("sites = %d (%s)", w.Code, w.Body)
	}
	got, _ = x.p.owner.GetOffer(o.ID)
	if !slices.Equal(sorted(got.Sites), []string{tid(2), tid(3)}) {
		t.Errorf("sites = %v", got.Sites)
	}
	// Removing every site leaves an empty bundle: refused.
	if w := x.post(base+"/sites", url.Values{"remove": {tid(2), tid(3)}}); w.Code != http.StatusBadRequest {
		t.Errorf("emptying the bundle = %d, want 400", w.Code)
	}
	// A not-owned site cannot be added.
	x.fs.infos[tid(9)] = ext.SiteInfo{ViewID: tid(9), Owner: "other"}
	if w := x.post(base+"/sites", url.Values{"add": {tid(9)}}); w.Code != http.StatusBadRequest {
		t.Errorf("adding a foreign site = %d, want 400", w.Code)
	}
	// A site in another open offer cannot be added.
	if other := x.post("/account/transfer", offerForm("b@example.com", daysAhead(10), "", tid(4))); other.Code != http.StatusOK {
		t.Fatal(other.Body)
	}
	if w := x.post(base+"/sites", url.Values{"add": {tid(4)}}); w.Code != http.StatusBadRequest {
		t.Errorf("adding a site of another open offer = %d, want 400", w.Code)
	}
	// The host rule holds when adding or removing.
	x.site(tid(5), "kunde.at")
	x.site(tid(6), "kunde.at/blog")
	if w := x.post(base+"/sites", url.Values{"add": {tid(5)}}); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Add them to the bundle") {
		t.Errorf("adding one host site = %d, want the conflict page (%s)", w.Code, w.Body)
	}
	if got, _ = x.p.owner.GetOffer(o.ID); slices.Contains(got.Sites, tid(5)) {
		t.Error("the conflicting site was added")
	}
	if w := x.post(base+"/sites", url.Values{"add": {tid(5), tid(6)}, "confirm": {"1"}}); w.Code != http.StatusSeeOther {
		t.Errorf("adding both = %d (%s)", w.Code, w.Body)
	}
	if w := x.post(base+"/sites", url.Values{"remove": {tid(6)}}); w.Code != http.StatusConflict {
		t.Errorf("removing one of a shared host = %d, want the conflict page", w.Code)
	}

	// Cancel.
	if w := x.post(base+"/cancel", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("cancel = %d", w.Code)
	}
	if got, _ = x.p.owner.GetOffer(o.ID); got.State != ownership.StateCancelled {
		t.Errorf("state = %q", got.State)
	}
	// A cancelled offer refuses every change.
	before := got
	for _, sub := range []string{"/cancel", "/dates", "/sites"} {
		v := url.Values{"accept_by": {daysAhead(9)}, "add": {tid(3)}}
		if w := x.post(base+sub, v); w.Code != http.StatusConflict {
			t.Errorf("%s on a cancelled offer = %d, want 409", sub, w.Code)
		}
	}
	if got, _ = x.p.owner.GetOffer(o.ID); got.State != ownership.StateCancelled || !slices.Equal(got.Sites, before.Sites) || !got.AcceptBy.Equal(before.AcceptBy) {
		t.Errorf("a cancelled offer changed: %+v", got)
	}
}

func TestTransferFinalStatesRefuseChanges(t *testing.T) {
	for _, st := range []string{ownership.StateAccepted, ownership.StateDeclined, ownership.StateExpired} {
		x := newXferEnv(t, "pro")
		x.site(tid(1))
		x.site(tid(2))
		o := x.openOffer(tid(1))
		if _, err := x.p.owner.UpdateOffer(o.ID, func(o *ownership.Offer) error { o.State = st; return nil }); err != nil {
			t.Fatal(err)
		}
		for _, sub := range []string{"/cancel", "/dates", "/sites"} {
			w := x.post("/account/transfer/"+o.ID+sub, url.Values{"accept_by": {daysAhead(9)}, "add": {tid(2)}})
			if w.Code != http.StatusConflict {
				t.Errorf("%s: %s = %d, want 409", st, sub, w.Code)
			}
		}
		got, _ := x.p.owner.GetOffer(o.ID)
		if got.State != st || len(got.Sites) != 1 {
			t.Errorf("%s: offer changed: %+v", st, got)
		}
	}
}

func TestTransferOthersGetNotFoundAndNoCSRFIsForbidden(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.site(tid(1))
	x.site(tid(2))
	o := x.openOffer(tid(1))
	otherCookie, otherID := adminUser(t, x.p, x.mux, "other@example.com", "pro")
	otherAcc, _ := x.p.accounts.ByID(otherID)
	base := "/account/transfer/" + o.ID
	for _, sub := range []string{"/cancel", "/dates", "/sites"} {
		w := x.postAs(otherCookie, x.p.csrf(otherAcc), base+sub, url.Values{"accept_by": {daysAhead(9)}, "add": {tid(2)}})
		if w.Code != http.StatusNotFound {
			t.Errorf("another account: %s = %d, want 404", sub, w.Code)
		}
		w = x.postAs(x.cookie, "", base+sub, url.Values{"accept_by": {daysAhead(9)}, "add": {tid(2)}})
		if w.Code != http.StatusForbidden {
			t.Errorf("no csrf: %s = %d, want 403", sub, w.Code)
		}
	}
	if w := x.postAs(x.cookie, "", "/account/transfer", offerForm("a@example.com", daysAhead(5), "", tid(2))); w.Code != http.StatusForbidden {
		t.Errorf("create without csrf = %d, want 403", w.Code)
	}
	if got, _ := x.p.owner.GetOffer(o.ID); got.State != ownership.StateOpen || len(got.Sites) != 1 {
		t.Errorf("the offer changed: %+v", got)
	}
	if len(x.offers()) != 1 {
		t.Error("a request without csrf made an offer")
	}
	// The page lists the sender's open offer with its forms.
	body := x.get("/account/transfer").Body.String()
	for _, want := range []string{base + "/cancel", base + "/dates", base + "/sites", "a@example.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// And not to the other account.
	if body := getAs(x.mux, "/account/transfer", otherCookie).Body.String(); strings.Contains(body, o.ID) || strings.Contains(body, "a@example.com") {
		t.Error("another account sees the offer")
	}
}

// ---- fix round 1 ----

func TestTransferParallelCreatesRespectTheDailyLimit(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.person("known@example.com", true)
	const n = 12
	for i := 0; i < n; i++ {
		x.site(tid(i + 1))
	}
	for i := 0; i < 19; i++ {
		if _, err := x.p.owner.CreateInvite(ownership.Invite{Site: tid(300 + i), From: x.acc.ID, ToEmail: "a@example.com"}, 0); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = x.post("/account/transfer", offerForm("known@example.com", daysAhead(5), "", tid(i+1))).Code
		}()
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	if ok != 1 || len(x.offers()) != 1 {
		t.Fatalf("%d requests succeeded and %d offers exist, want exactly 1 (codes %v)", ok, len(x.offers()), codes)
	}
	if mails := x.sent(); len(mails) != 1 {
		t.Errorf("mails = %d, want 1", len(mails))
	}
}

func TestTransferInOtherAccountsOfferIsRefusedGenerically(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.site(tid(1))
	other, err := x.p.owner.CreateOffer(ownership.Offer{Sites: []string{tid(1)}, From: "someone-else", ToEmail: "z@example.com", AcceptBy: time.Now().Add(time.Hour)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	w := x.post("/account/transfer", offerForm("a@example.com", daysAhead(5), "", tid(1)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "another open offer") {
		t.Fatalf("= %d, want the generic refusal (%s)", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "z@example.com") || strings.Contains(w.Body.String(), "someone-else") {
		t.Error("the refusal names the other offer")
	}
	if got, _ := x.p.owner.GetOffer(other.ID); got.State != ownership.StateOpen {
		t.Errorf("the other account's offer was touched: %q", got.State)
	}
	if len(x.offers()) != 0 {
		t.Error("an offer was created")
	}
}

func TestTransferEditsNeitherMailNorCountTowardTheLimit(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.person("known@example.com", true)
	x.site(tid(1))
	x.site(tid(2))
	if w := x.post("/account/transfer", offerForm("known@example.com", daysAhead(10), daysAhead(5), tid(1))); w.Code != http.StatusOK {
		t.Fatal(w.Body)
	}
	o := x.offers()[0]
	base := "/account/transfer/" + o.ID
	day := time.Now()
	before := x.p.owner.DailyCount(x.acc.ID, day)
	if w := x.post(base+"/dates", url.Values{"accept_by": {daysAhead(30)}, "available_from": {daysAhead(-1)}}); w.Code != http.StatusSeeOther {
		t.Fatalf("dates = %d", w.Code)
	}
	if w := x.post(base+"/sites", url.Values{"add": {tid(2)}, "confirm": {"1"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("sites = %d", w.Code)
	}
	if after := x.p.owner.DailyCount(x.acc.ID, day); after != before {
		t.Errorf("daily count %d -> %d: edits must not count", before, after)
	}
	if mails := x.sent(); len(mails) != 0 {
		t.Errorf("edits mailed %d", len(mails))
	}
}

func TestTransferLockedSiteBlocksAHostAndOffersNoButton(t *testing.T) {
	x := newXferEnv(t, "pro")
	x.site(tid(1), "kunde.at")
	x.site(tid(2), "kunde.at/blog")
	i := x.fs.infos[tid(2)]
	i.Locked = &ext.SiteLock{At: time.Now(), By: "operator"}
	x.fs.infos[tid(2)] = i
	w := x.post("/account/transfer", offerForm("a@example.com", daysAhead(5), "", tid(1)))
	body := w.Body.String()
	if w.Code != http.StatusConflict || !strings.Contains(body, "A locked site blocks this host") {
		t.Fatalf("= %d, want the blocked page (%s)", w.Code, body)
	}
	if strings.Contains(body, "Add them to the bundle") || strings.Contains(body, `name="step"`) {
		t.Error("the page offers a button that can never resolve the conflict")
	}
}

func TestTransferMailFailureLogCarriesNoAddress(t *testing.T) {
	if got := mailErrClass(fmt.Errorf("550 5.1.1 <victim@example.com>: user unknown")); strings.Contains(got, "victim") {
		t.Errorf("class = %q", got)
	}
}
