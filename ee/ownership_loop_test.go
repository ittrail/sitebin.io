//go:build ee

package ee

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/ownership"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// initT is Init with the ownership loop stopped when the test ends.
func initT(t testing.TB, p *provider, h ext.Host) error {
	t.Cleanup(p.stopOwnershipLoop)
	return p.Init(h)
}

// SiteOwners is the fake core's walk over every site: meta only.
func (s *fakeSites) SiteOwners() (map[string]ext.SiteOwnership, error) {
	if s.ownersErr != nil {
		return nil, s.ownersErr
	}
	out := map[string]ext.SiteOwnership{}
	for id := range s.infos {
		i, _ := s.info(id)
		so := ext.SiteOwnership{Owner: i.Owner}
		for _, a := range i.Admins {
			so.Admins = append(so.Admins, a.Account)
		}
		out[id] = so
	}
	return out, nil
}

const (
	subjOffered      = "transfer is offered"
	subjTransferGone = "transfer has expired"
	subjShareExpired = "share has expired"
	loopOfferLogLine = "could not complete an accepting offer"
)

type loopEnv struct{ *shareEnv }

func newLoopEnv(t *testing.T) *loopEnv {
	t.Helper()
	e := &loopEnv{newShareEnv(t)}
	e.site(tid(31))
	e.site(tid(32))
	return e
}

// tick runs one loop step at now and waits for its background mail.
func (e *loopEnv) tick(now time.Time) {
	e.t.Helper()
	e.p.ownershipTick(now)
	e.p.mailWG.Wait()
}

// later is a tick time far enough ahead that every marker just written counts
// as old, and the hourly reconciliation is due.
func later() time.Time { return time.Now().Add(2 * time.Hour) }

func (e *loopEnv) mails(addr, subject string) int { return len(e.mailsTo(addr, subject)) }

func (e *loopEnv) allMails() int {
	e.p.mailWG.Wait()
	e.mailMu.Lock()
	defer e.mailMu.Unlock()
	return len(e.all)
}

func (e *loopEnv) markers(acc *account.Account) []string {
	e.t.Helper()
	ids, err := e.p.accounts.ListSiteIDs(acc)
	if err != nil {
		e.t.Fatal(err)
	}
	return ids
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLog routes the default logger into a buffer for the test.
func captureLog(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

// ---- offers ----

func TestLoopExpiresOffersPastAcceptByAndMailsBothSides(t *testing.T) {
	e := newLoopEnv(t)
	e.site(tid(33))
	e.site(tid(34))
	past := time.Now().Add(-time.Hour)
	open := e.offer(tid(31))
	paused := e.offer(tid(32))
	told := time.Now().Add(-48 * time.Hour)
	e.change(open, func(x *ownership.Offer) { x.AcceptBy = past; x.NotifiedAt = &told })
	e.change(paused, func(x *ownership.Offer) { x.AcceptBy = past; x.State = ownership.StatePaused; x.NotifiedAt = &told })
	fresh := e.offer(tid(33))
	declined := e.offer(tid(34))
	e.change(declined, func(x *ownership.Offer) { x.State = ownership.StateDeclined; x.AcceptBy = past })

	e.tick(time.Now())

	for _, o := range []ownership.Offer{open, paused} {
		if got := e.current(o).State; got != ownership.StateExpired {
			t.Errorf("offer %s = %s, want expired (accept-by keeps running while paused)", o.ID, got)
		}
	}
	if got := e.current(fresh).State; got != ownership.StateOpen {
		t.Errorf("an offer within its dates = %s, want open", got)
	}
	if got := e.current(declined).State; got != ownership.StateDeclined {
		t.Errorf("a declined offer was touched: %s", got)
	}
	// Both sides, once per offer, naming the site and the other party.
	sender := e.mailsTo("pro@example.com", subjTransferGone)
	rcpt := e.mailsTo(takerEmail, subjTransferGone)
	if len(sender) != 2 || len(rcpt) != 2 {
		t.Fatalf("expired mails: sender %d, recipient %d, want 2 each", len(sender), len(rcpt))
	}
	for _, want := range []string{takerEmail, label(tid(31)), label(tid(32))} {
		if !strings.Contains(strings.Join(sender, "\n"), want) {
			t.Errorf("the sender's expired mail lacks %q", want)
		}
	}
	for _, want := range []string{"pro@example.com", label(tid(31)), label(tid(32))} {
		if !strings.Contains(strings.Join(rcpt, "\n"), want) {
			t.Errorf("the recipient's expired mail lacks %q", want)
		}
	}
	for _, m := range append(slices.Clone(sender), rcpt...) {
		if strings.Contains(strings.ToLower(m), "password") {
			t.Errorf("an expiry mail mentions a password:\n%s", m)
		}
	}
	// Nothing again on the next tick.
	e.tick(time.Now().Add(5 * time.Minute))
	if e.mails("pro@example.com", subjTransferGone) != 2 || e.mails(takerEmail, subjTransferGone) != 2 {
		t.Error("a second tick mailed an expired offer again")
	}
	// The sites are untouched: expiry deletes nothing.
	for _, id := range []string{tid(31), tid(32)} {
		if owner, _ := e.fs.SiteOwner(id); owner != e.acc.ID {
			t.Errorf("owner of %s = %q after the expiry", id, owner)
		}
	}
}

func TestLoopNeverExpiresAnAcceptingOffer(t *testing.T) {
	e := newLoopEnv(t)
	o := e.offer(tid(31), tid(32))
	e.change(o, func(x *ownership.Offer) {
		x.State, x.AcceptedBy = ownership.StateAccepting, e.to.ID
		x.AcceptBy = time.Now().Add(-48 * time.Hour)
	})
	e.tick(time.Now())
	cur := e.current(o)
	if cur.State != ownership.StateAccepted || len(cur.Done) != 2 {
		t.Fatalf("offer = %+v; the loop must complete an accepting offer, not expire it", cur)
	}
	for _, id := range []string{tid(31), tid(32)} {
		if owner, _ := e.fs.SiteOwner(id); owner != e.to.ID {
			t.Errorf("owner of %s = %q, want the recipient", id, owner)
		}
	}
	if e.mails(takerEmail, subjTransferGone) != 0 || e.mails("pro@example.com", subjTransferGone) != 0 {
		t.Error("an accepting offer was mailed as expired")
	}
	for _, c := range e.fs.xf().all() {
		if c.By.Surface != provenance.SurfaceBackground {
			t.Errorf("the loop's move was recorded through %q", c.By.Surface)
		}
	}
}

func TestLoopSkipsAnOfferAnotherRunHolds(t *testing.T) {
	e := newLoopEnv(t)
	logs := captureLog(t)
	o := e.offer(tid(31))
	e.change(o, func(x *ownership.Offer) { x.State, x.AcceptedBy = ownership.StateAccepting, e.to.ID })
	release, ok := e.p.claimAccept(o.ID)
	if !ok {
		t.Fatal("could not claim the offer")
	}
	e.tick(time.Now())
	if cur := e.current(o); cur.State != ownership.StateAccepting || len(cur.Done) != 0 || len(e.fs.xf().all()) != 0 {
		t.Fatalf("a held claim changed the offer: %+v, calls %d", cur, len(e.fs.xf().all()))
	}
	if strings.Contains(logs.String(), loopOfferLogLine) {
		t.Errorf("a busy claim was logged as an error:\n%s", logs.String())
	}
	release()
	e.tick(time.Now().Add(5 * time.Minute))
	if cur := e.current(o); cur.State != ownership.StateAccepted {
		t.Errorf("after the claim was released, the offer is %s", cur.State)
	}
}

func TestLoopLogsALastingErrorOncePerHour(t *testing.T) {
	e := newLoopEnv(t)
	logs := captureLog(t)
	o := e.offer(tid(31))
	e.change(o, func(x *ownership.Offer) { x.State, x.AcceptedBy = ownership.StateAccepting, e.to.ID })
	t0 := time.Now()
	for _, d := range []time.Duration{0, 5 * time.Minute, 30 * time.Minute, 59 * time.Minute, 61 * time.Minute, 90 * time.Minute} {
		e.fs.xf().fail[tid(31)] = errors.New("the disk went away")
		e.tick(t0.Add(d))
	}
	if n := strings.Count(logs.String(), loopOfferLogLine); n != 2 {
		t.Errorf("the lasting error was logged %d times in 90 minutes, want 2 (t0 and t0+61m):\n%s", n, logs.String())
	}
	if cur := e.current(o); cur.State != ownership.StateAccepting {
		t.Errorf("a failing move ended the offer: %s", cur.State)
	}
	// Once the fault clears, the next tick completes it.
	e.tick(t0.Add(2 * time.Hour))
	if cur := e.current(o); cur.State != ownership.StateAccepted {
		t.Errorf("offer = %s after the fault cleared", cur.State)
	}
}

func TestLoopNotifiesOnceWhenAvailableFromPasses(t *testing.T) {
	e := newLoopEnv(t)
	e.site(tid(33))
	from := time.Now().Add(48 * time.Hour)
	waiting := e.offer(tid(31))
	e.change(waiting, func(x *ownership.Offer) { x.AvailableFrom = &from })
	nofrom := e.offer(tid(32))
	moved := e.offer(tid(33))
	earlier := time.Now().Add(-time.Hour)
	e.change(moved, func(x *ownership.Offer) { x.AvailableFrom = &earlier }) // moved into the past by the sender

	e.tick(time.Now())
	if n := e.mails(takerEmail, subjOffered); n != 2 {
		t.Fatalf("offer mails after the first tick = %d, want 2 (no available-from, and one moved into the past)", n)
	}
	if e.current(waiting).NotifiedAt != nil {
		t.Error("an offer before its available-from was notified")
	}
	if e.current(nofrom).NotifiedAt == nil || e.current(moved).NotifiedAt == nil {
		t.Error("notified offers carry no NotifiedAt")
	}
	e.tick(time.Now().Add(5 * time.Minute))
	if n := e.mails(takerEmail, subjOffered); n != 2 {
		t.Errorf("a second tick mailed again: %d", n)
	}
	e.tick(from.Add(time.Minute))
	if n := e.mails(takerEmail, subjOffered); n != 3 {
		t.Errorf("offer mails once available-from passed = %d, want 3", n)
	}
	e.tick(from.Add(10 * time.Minute))
	if n := e.mails(takerEmail, subjOffered); n != 3 {
		t.Errorf("available-from mailed twice: %d", n)
	}
}

// ---- pauses are computed, never stored (Review Focus 4) ----

func TestLoopPausesAreComputedAndNothingIsDeleted(t *testing.T) {
	type cond struct {
		name string
		on   func(e *loopEnv)
		off  func(e *loopEnv)
		// noCard: the recipient cannot even sign in while it holds.
		noCard bool
	}
	conds := []cond{
		{name: "the sender's plan is downgraded",
			on:  func(e *loopEnv) { e.setTier(e.acc, "free") },
			off: func(e *loopEnv) { e.setTier(e.acc, "pro") }},
		{name: "a site is locked",
			on: func(e *loopEnv) {
				i := e.fs.infos[tid(32)]
				i.Locked = &ext.SiteLock{By: ext.LockByAdmin, At: time.Now()}
				e.fs.infos[tid(32)] = i
			},
			off: func(e *loopEnv) {
				i := e.fs.infos[tid(32)]
				i.Locked = nil
				e.fs.infos[tid(32)] = i
			}},
		{name: "the sender is suspended",
			on: func(e *loopEnv) {
				now := time.Now()
				e.setAccount(e.acc, func(a *account.Account) { a.SuspendedAt = &now })
			},
			off: func(e *loopEnv) { e.setAccount(e.acc, func(a *account.Account) { a.SuspendedAt = nil }) }},
		{name: "the recipient is suspended", noCard: true,
			on: func(e *loopEnv) {
				now := time.Now()
				e.setAccount(e.to, func(a *account.Account) { a.SuspendedAt = &now })
			},
			off: func(e *loopEnv) { e.setAccount(e.to, func(a *account.Account) { a.SuspendedAt = nil }) }},
	}
	for _, c := range conds {
		t.Run(c.name, func(t *testing.T) {
			e := newLoopEnv(t)
			o := e.offer(tid(31), tid(32))
			c.on(e)

			e.tick(time.Now())
			cur := e.current(o)
			if cur.State != ownership.StateOpen {
				t.Errorf("a pause was STORED: state = %s, want open (pauses are computed)", cur.State)
			}
			if cur.NotifiedAt != nil || e.mails(takerEmail, subjOffered) != 0 {
				t.Error("the recipient was mailed an offer that is paused")
			}
			if !c.noCard {
				dash := e.dashboard(e.toCookie)
				if !strings.Contains(dash, "not available right now") || strings.Contains(dash, "/account/transfer/"+o.ID+"/accept") {
					t.Errorf("the card is not unavailable while paused:\n%s", dash)
				}
			}
			if !c.noCard {
				if w := e.takeEnv.accept(o); w.Code == http.StatusSeeOther || w.Code == http.StatusOK {
					t.Errorf("accept while paused = %d, want refused", w.Code)
				}
			}
			for _, id := range []string{tid(31), tid(32)} {
				if owner, _ := e.fs.SiteOwner(id); owner != e.acc.ID {
					t.Errorf("owner of %s = %q while paused", id, owner)
				}
			}
			if after := e.current(o); after.State != ownership.StateOpen || len(after.Sites) != 2 {
				t.Errorf("a pause changed the offer: %+v", after)
			}
			// Many ticks later, still nothing sent and nothing deleted.
			e.tick(time.Now().Add(time.Hour))
			if e.current(o).NotifiedAt != nil || e.mails(takerEmail, subjOffered) != 0 {
				t.Error("a later tick mailed while still paused")
			}

			c.off(e)
			if dash := e.dashboard(e.toCookie); !c.noCard && !strings.Contains(dash, "/account/transfer/"+o.ID+"/accept") {
				t.Errorf("the card did not come back when the pause lifted:\n%s", dash)
			}
			e.tick(time.Now().Add(2 * time.Hour))
			if e.current(o).NotifiedAt == nil || e.mails(takerEmail, subjOffered) != 1 {
				t.Errorf("the offer mail after the pause lifted = %d, want 1", e.mails(takerEmail, subjOffered))
			}
			e.tick(time.Now().Add(3 * time.Hour))
			if e.mails(takerEmail, subjOffered) != 1 {
				t.Error("the offer mail went out twice")
			}
			if w := e.takeEnv.accept(o); !c.noCard && w.Code != http.StatusOK {
				t.Errorf("accept after the pause lifted = %d (%s)", w.Code, w.Body)
			}
		})
	}
}

func TestLoopSharePausesWithThePlanAndResumes(t *testing.T) {
	e := newLoopEnv(t)
	e.shared(inDays(5))
	if err := e.p.SetAdminTags(e.to.ID, shareSite, []string{"mine"}); err != nil {
		t.Fatal(err)
	}
	e.setTier(e.acc, "free") // the owner's plan stops sharing
	e.tick(time.Now())
	e.tick(later())
	if _, admin := e.p.manages(e.to, shareSite); admin {
		t.Error("an admin manages while the owner's plan does not share")
	}
	// Nothing was deleted by the pause: the entry, the marker and the tags.
	if a := e.admins(shareSite); len(a) != 1 || a[0].Account != e.to.ID {
		t.Errorf("admins = %+v after a pause", a)
	}
	if ids, _ := e.p.owner.SharedSites(e.to.ID); len(ids) != 1 {
		t.Errorf("share markers = %v after a pause", ids)
	}
	if reg, _ := e.p.accounts.Tags(e.to.ID); len(reg.Sites[shareSite]) != 1 {
		t.Errorf("admin tags = %+v after a pause", reg.Sites)
	}
	e.setTier(e.acc, "pro")
	if _, admin := e.p.manages(e.to, shareSite); !admin {
		t.Error("the share did not resume with the plan")
	}
}

func TestLoopReopensAHostConflictPause(t *testing.T) {
	e := newLoopEnv(t)
	e.site(tid(39), "kunde.example/blog")
	i := e.fs.infos[tid(31)]
	i.Domains = []string{"kunde.example"}
	e.fs.infos[tid(31)] = i
	o := e.offer(tid(31))
	e.change(o, func(x *ownership.Offer) { x.State = ownership.StatePaused })

	e.tick(time.Now())
	if cur := e.current(o); cur.State != ownership.StatePaused || cur.NotifiedAt != nil {
		t.Fatalf("a standing conflict: offer = %+v, want still paused and not notified", cur)
	}
	if e.mails(takerEmail, subjOffered) != 0 {
		t.Error("the recipient was mailed while paused")
	}
	// The conflict goes away, but the sender's plan no longer shares: the
	// offer is paused for that reason (computed), so it stays stored paused.
	i = e.fs.infos[tid(39)]
	i.Domains = nil
	e.fs.infos[tid(39)] = i
	e.setTier(e.acc, "free")
	e.tick(time.Now().Add(10 * time.Minute))
	if cur := e.current(o); cur.State != ownership.StatePaused {
		t.Errorf("reopened while the plan pauses it: %s", cur.State)
	}
	e.setTier(e.acc, "pro")
	e.tick(time.Now().Add(20 * time.Minute))
	cur := e.current(o)
	if cur.State != ownership.StateOpen {
		t.Fatalf("offer = %s, want open once the conflict is gone", cur.State)
	}
	if cur.NotifiedAt == nil || e.mails(takerEmail, subjOffered) != 1 {
		t.Errorf("the recipient's offer mail after the reopen = %d, want 1", e.mails(takerEmail, subjOffered))
	}
	e.tick(time.Now().Add(30 * time.Minute))
	if e.mails(takerEmail, subjOffered) != 1 {
		t.Error("mailed twice")
	}
}

// ---- invitations ----

func TestLoopLapsesInvitationsSilently(t *testing.T) {
	e := newLoopEnv(t)
	old := e.open(shareSite, takerEmail, nil)
	young := e.open(shareSite, "other@example.com", nil)
	accepted := e.open(shareSite, "third@example.com", nil)
	set := func(inv ownership.Invite, f func(*ownership.Invite)) {
		if _, err := e.p.owner.UpdateInvite(inv.ID, func(x *ownership.Invite) error { f(x); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	set(old, func(x *ownership.Invite) { x.Created = time.Now().Add(-31 * 24 * time.Hour) })
	set(young, func(x *ownership.Invite) { x.Created = time.Now().Add(-29 * 24 * time.Hour) })
	set(accepted, func(x *ownership.Invite) {
		x.Created = time.Now().Add(-40 * 24 * time.Hour)
		x.State = ownership.StateAccepted
	})

	before := e.allMails()
	e.tick(time.Now())
	if got := e.inviteState(old); got != ownership.StateExpired {
		t.Errorf("an invitation of 31 days = %s, want expired", got)
	}
	if got := e.inviteState(young); got != ownership.StateOpen {
		t.Errorf("an invitation of 29 days = %s, want open", got)
	}
	if got := e.inviteState(accepted); got != ownership.StateAccepted {
		t.Errorf("an accepted invitation was touched: %s", got)
	}
	if e.allMails() != before {
		t.Error("a lapsed invitation sent mail (it lapses silently)")
	}
}

// ---- admin expiry ----

func TestLoopEndsAnExpiredAdmin(t *testing.T) {
	e := newLoopEnv(t)
	e.site(shareSite2)
	e.shared(inDays(1)) // ends in a day
	inv := e.open(shareSite2, takerEmail, inDays(10))
	if w := e.accept(inv); w.Code != http.StatusSeeOther {
		t.Fatalf("accept = %d", w.Code)
	}
	if err := e.p.SetAdminTags(e.to.ID, shareSite, []string{"mine"}); err != nil {
		t.Fatal(err)
	}
	if err := e.p.SetAdminTags(e.to.ID, shareSite2, []string{"keep"}); err != nil {
		t.Fatal(err)
	}
	e.fs.adminBy = nil

	e.tick(time.Now().Add(48 * time.Hour))

	if a := e.admins(shareSite); len(a) != 0 {
		t.Errorf("an admin past its end date is still in meta: %+v", a)
	}
	if a := e.admins(shareSite2); len(a) != 1 || a[0].Account != e.to.ID {
		t.Errorf("an admin with a future end date was touched: %+v", a)
	}
	ids, _ := e.p.owner.SharedSites(e.to.ID)
	if len(ids) != 1 || ids[0] != shareSite2 {
		t.Errorf("share markers = %v, want only the live share", ids)
	}
	reg, _ := e.p.accounts.Tags(e.to.ID)
	if _, gone := reg.Sites[shareSite]; gone || len(reg.Sites[shareSite2]) != 1 {
		t.Errorf("admin tags = %+v, want the expired share's gone and the live one's kept", reg.Sites)
	}
	if len(e.fs.adminBy) != 1 || e.fs.adminBy[0].Surface != provenance.SurfaceBackground {
		t.Errorf("SetAdmins calls = %+v, want one through the background surface", e.fs.adminBy)
	}
	owner := e.mailsTo("pro@example.com", subjShareExpired)
	admin := e.mailsTo(takerEmail, subjShareExpired)
	if len(owner) != 1 || len(admin) != 1 {
		t.Fatalf("share expired mails: owner %d, admin %d, want 1 each", len(owner), len(admin))
	}
	for _, m := range []string{owner[0], admin[0]} {
		for _, want := range []string{label(shareSite), "pro@example.com", takerEmail} {
			if !strings.Contains(m, want) {
				t.Errorf("a share expired mail lacks %q:\n%s", want, m)
			}
		}
		if strings.Contains(strings.ToLower(m), "password") {
			t.Errorf("a share expired mail mentions a password:\n%s", m)
		}
	}
	// Both account logs record it.
	for _, acc := range []*account.Account{e.acc, e.to} {
		es, _ := e.p.accounts.Provenance(acc.ID)
		found := false
		for _, en := range es {
			if en.Action == provenance.ActionAdmins && en.Site == shareSite && en.Surface == provenance.SurfaceBackground {
				found = true
			}
		}
		if !found {
			t.Errorf("%s has no background log line for the expired share", acc.Email)
		}
	}
	e.tick(time.Now().Add(49 * time.Hour))
	if e.mails("pro@example.com", subjShareExpired) != 1 || e.mails(takerEmail, subjShareExpired) != 1 {
		t.Error("a second tick mailed the expiry again")
	}
}

func TestLoopSkipsASiteThatMovedAndNeverActsOnAnError(t *testing.T) {
	e := newLoopEnv(t)
	e.shared(inDays(1))
	_, third := e.other("else@example.com")
	// The site changes hands between the loop's read and its write.
	e.fs.onSetAdmins = func(id string) {
		i := e.fs.infos[id]
		i.Owner = third.ID
		e.fs.infos[id] = i
		e.fs.onSetAdmins = nil
	}
	e.tick(time.Now().Add(48 * time.Hour))
	if a := e.admins(shareSite); len(a) != 1 {
		t.Errorf("admins = %+v; a refused write must leave them", a)
	}
	if ids, _ := e.p.owner.SharedSites(e.to.ID); len(ids) != 1 {
		t.Errorf("share markers = %v; a site that moved is skipped, not unlinked", ids)
	}
	if e.mails(takerEmail, subjShareExpired) != 0 || e.mails("pro@example.com", subjShareExpired) != 0 {
		t.Error("mail went out for a refused write")
	}

	// A read error is unknown: nothing is removed.
	e2 := newLoopEnv(t)
	e2.shared(inDays(1))
	e2.fs.adminErrs = map[string]error{shareSite: errors.New("disk")}
	e2.tick(time.Now().Add(48 * time.Hour))
	e2.fs.adminErrs = nil
	if a := e2.admins(shareSite); len(a) != 1 {
		t.Errorf("admins = %+v after a read error", a)
	}
	if ids, _ := e2.p.owner.SharedSites(e2.to.ID); len(ids) != 1 {
		t.Errorf("share markers = %v after a read error", ids)
	}
}

// ---- mail goes only to verified accounts ----

func TestLoopMailsNobodyUnverified(t *testing.T) {
	e := newLoopEnv(t)
	e.shared(inDays(1))
	e.verify(e.to, false)
	offered := e.offer(tid(31))
	expiring := e.offer(tid(32))
	e.change(expiring, func(x *ownership.Offer) { x.AcceptBy = time.Now().Add(-time.Hour) })

	e.tick(time.Now().Add(48 * time.Hour))
	if e.current(offered).NotifiedAt == nil {
		t.Error("the offer was not marked notified")
	}
	if got := e.current(expiring).State; got != ownership.StateExpired {
		t.Errorf("state = %s", got)
	}
	if a := e.admins(shareSite); len(a) != 0 {
		t.Errorf("the share did not end: %+v", a)
	}
	for _, subj := range []string{subjOffered, subjTransferGone, subjShareExpired} {
		if n := e.mails(takerEmail, subj); n != 0 {
			t.Errorf("%d %q mails to an unverified account", n, subj)
		}
	}
	if e.mails("pro@example.com", subjTransferGone) != 1 || e.mails("pro@example.com", subjShareExpired) != 1 {
		t.Error("the verified sender was not told")
	}
}

// ---- the hourly reconciliation ----

func has(ids []string, id string) bool { return slices.Contains(ids, id) }

func TestLoopReconcilesTheAccountIndex(t *testing.T) {
	e := newLoopEnv(t)
	// A stray marker: the site's meta names the sender.
	if err := e.p.accounts.LinkSite(e.to, tid(31)); err != nil {
		t.Fatal(err)
	}
	// A dangling marker: no such site.
	gone := tid(50)
	if err := e.p.accounts.LinkSite(e.acc, gone); err != nil {
		t.Fatal(err)
	}
	// A missing marker: meta names the sender, LinkSite never landed.
	missing := tid(51)
	e.fs.infos[missing] = ext.SiteInfo{ViewID: missing, Mode: "webserver", Owner: e.acc.ID, ViewURL: label(missing)}
	// An anonymous site names no owner, so no marker is made for it.
	anon := tid(52)
	e.fs.infos[anon] = ext.SiteInfo{ViewID: anon, Mode: "webserver", ViewURL: label(anon)}

	e.tick(later())

	if has(e.markers(e.to), tid(31)) {
		t.Error("a stray marker (another account's site) was kept")
	}
	if !has(e.markers(e.acc), tid(31)) {
		t.Error("the owner's own marker was removed")
	}
	if has(e.markers(e.acc), gone) {
		t.Error("a dangling marker was kept")
	}
	if !has(e.markers(e.acc), missing) {
		t.Error("a missing marker was not linked")
	}
	if has(e.markers(e.acc), anon) || has(e.markers(e.to), anon) {
		t.Error("a marker was made for an ownerless site")
	}
	// Not every tick: a new stray waits for the next hour.
	if err := e.p.accounts.LinkSite(e.to, tid(32)); err != nil {
		t.Fatal(err)
	}
	t1 := later()
	e.tick(t1.Add(10 * time.Minute))
	if !has(e.markers(e.to), tid(32)) {
		t.Error("the reconciliation ran again within the hour")
	}
	e.tick(t1.Add(61 * time.Minute))
	if has(e.markers(e.to), tid(32)) {
		t.Error("the reconciliation did not run an hour later")
	}
}

func TestLoopReconcilesSharesAndAdminTags(t *testing.T) {
	e := newLoopEnv(t)
	e.shared(nil) // a real share of shareSite
	if err := e.p.SetAdminTags(e.to.ID, shareSite, []string{"keep"}); err != nil {
		t.Fatal(err)
	}
	// A stray share marker (the site lists nobody), one for a site that is
	// gone, and stray tags for both.
	if err := e.p.owner.LinkShare(e.to.ID, tid(31)); err != nil {
		t.Fatal(err)
	}
	if err := e.p.owner.LinkShare(e.to.ID, tid(60)); err != nil {
		t.Fatal(err)
	}
	if err := e.p.accounts.UpdateTags(e.to.ID, func(r *account.TagRegistry) error {
		r.Sites[tid(31)] = []string{"stale"}
		r.Sites[tid(60)] = []string{"stale"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Young markers are not stray yet: an invitation being accepted links
	// the marker a moment before it writes the admin entry.
	e.tick(time.Now().Add(time.Minute))
	if ids, _ := e.p.owner.SharedSites(e.to.ID); len(ids) != 3 {
		t.Fatalf("young share markers = %v, want all kept", ids)
	}

	e.tick(later().Add(2 * time.Hour))
	ids, _ := e.p.owner.SharedSites(e.to.ID)
	if len(ids) != 1 || ids[0] != shareSite {
		t.Errorf("share markers = %v, want only the real share", ids)
	}
	reg, _ := e.p.accounts.Tags(e.to.ID)
	if len(reg.Sites) != 1 || len(reg.Sites[shareSite]) != 1 {
		t.Errorf("admin tags = %+v, want only the real share's", reg.Sites)
	}
}

func TestLoopReconciliationLeavesEverythingAloneOnAReadError(t *testing.T) {
	setup := func(t *testing.T) *loopEnv {
		e := newLoopEnv(t)
		e.shared(nil)
		if err := e.p.accounts.LinkSite(e.to, tid(31)); err != nil { // stray
			t.Fatal(err)
		}
		if err := e.p.accounts.LinkSite(e.acc, tid(50)); err != nil { // dangling
			t.Fatal(err)
		}
		if err := e.p.owner.LinkShare(e.to.ID, tid(32)); err != nil { // stray share
			t.Fatal(err)
		}
		if err := e.p.accounts.UpdateTags(e.to.ID, func(r *account.TagRegistry) error {
			r.Sites = map[string][]string{tid(32): {"stale"}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return e
	}
	untouched := func(t *testing.T, e *loopEnv) {
		t.Helper()
		if !has(e.markers(e.to), tid(31)) || !has(e.markers(e.acc), tid(50)) {
			t.Errorf("an index marker was unlinked on an unknown: to=%v acc=%v", e.markers(e.to), e.markers(e.acc))
		}
		if ids, _ := e.p.owner.SharedSites(e.to.ID); !has(ids, tid(32)) || !has(ids, shareSite) {
			t.Errorf("a share marker was unlinked on an unknown: %v", ids)
		}
		if reg, _ := e.p.accounts.Tags(e.to.ID); len(reg.Sites[tid(32)]) != 1 {
			t.Errorf("tags were pruned on an unknown: %+v", reg.Sites)
		}
	}
	t.Run("the walk fails", func(t *testing.T) {
		e := setup(t)
		e.fs.ownersErr = errors.New("disk")
		e.tick(later().Add(2 * time.Hour))
		// The walk only nominates: the fresh single-site read is the
		// verdict, so a stray and a dangling marker are still cleaned (one
		// unreadable meta must not stop dangling cleanup instance-wide).
		if has(e.markers(e.to), tid(31)) || has(e.markers(e.acc), tid(50)) {
			t.Errorf("a proven-stray marker survived a failed walk: to=%v acc=%v", e.markers(e.to), e.markers(e.acc))
		}
	})
	t.Run("every read fails", func(t *testing.T) {
		e := setup(t)
		e.fs.ownersErr = errors.New("disk")
		e.fs.ownerErrs = map[string]error{tid(31): errors.New("disk"), tid(50): errors.New("disk")}
		e.fs.adminErrs = map[string]error{tid(32): errors.New("disk"), shareSite: errors.New("disk")}
		e.tick(later().Add(2 * time.Hour))
		untouched(t, e)
	})
	t.Run("the fresh read of a candidate fails", func(t *testing.T) {
		e := setup(t)
		e.fs.ownerErrs = map[string]error{tid(31): errors.New("disk"), tid(50): errors.New("disk")}
		e.fs.adminErrs = map[string]error{tid(32): errors.New("disk"), shareSite: errors.New("disk")}
		e.tick(later().Add(2 * time.Hour))
		untouched(t, e)
	})
}

// ---- nil safety, start and stop ----

func TestLoopIsNilSafeWithoutInit(t *testing.T) {
	var zero provider
	zero.ownershipTick(time.Now()) // no store, no mailer, no site service
	p := newProvider()
	p.ownershipTick(time.Now())
	p.stopOwnershipLoop()
	p.stopOwnershipLoop()

	// A provider that opened its stores but has no site service.
	e := newLoopEnv(t)
	e.p.host = nil
	e.p.ownershipTick(time.Now())
}

func TestLoopWithoutAMailerStillMovesStates(t *testing.T) {
	e := newLoopEnv(t)
	e.p.mailer = nil
	o := e.offer(tid(31))
	e.change(o, func(x *ownership.Offer) { x.AcceptBy = time.Now().Add(-time.Hour) })
	e.tick(time.Now())
	if got := e.current(o).State; got != ownership.StateExpired {
		t.Errorf("state = %s", got)
	}
}

func TestInitStartsTheLoopAndStopEndsIt(t *testing.T) {
	e := newLoopEnv(t)
	done := e.p.loopDone
	if done == nil {
		t.Fatal("Init did not start the ownership loop")
	}
	e.p.stopOwnershipLoop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop")
	}
	e.p.stopOwnershipLoop() // twice is fine
}

// ---- fix round 1 ----

// A marker linked just now for an account that is not yet meta's owner is a
// transfer in flight (the recipient's marker comes first): it survives; once
// older than the grace it is stray.
func TestLoopIndexMarkerGrace(t *testing.T) {
	e := newLoopEnv(t)
	if err := e.p.accounts.LinkSite(e.to, tid(31)); err != nil {
		t.Fatal(err)
	}
	e.tick(time.Now().Add(time.Minute))
	if !has(e.markers(e.to), tid(31)) {
		t.Fatal("a young marker was unlinked: the recipient's marker of a transfer in flight")
	}
	e.tick(time.Now().Add(2 * time.Hour))
	if has(e.markers(e.to), tid(31)) {
		t.Error("a marker older than the grace, for a site meta gives to another account, was kept")
	}
}

func TestLoopKeepsTheMarkerOfAnAcceptingRecipient(t *testing.T) {
	e := newLoopEnv(t)
	o := e.offer(tid(31))
	e.change(o, func(x *ownership.Offer) { x.State, x.AcceptedBy = ownership.StateAccepting, e.to.ID })
	if err := e.p.accounts.LinkSite(e.to, tid(31)); err != nil {
		t.Fatal(err)
	}
	// The move is held by another run, so the loop cannot finish it.
	release, ok := e.p.claimAccept(o.ID)
	if !ok {
		t.Fatal("no claim")
	}
	e.tick(later())
	if !has(e.markers(e.to), tid(31)) {
		t.Error("the marker of the recipient of an accepting offer was unlinked")
	}
	release()
}

func TestLoopLinksAMissingShareMarker(t *testing.T) {
	e := newLoopEnv(t)
	e.shared(nil)
	if err := e.p.owner.UnlinkShare(e.to.ID, shareSite); err != nil {
		t.Fatal(err)
	}
	e.tick(later())
	if ids, _ := e.p.owner.SharedSites(e.to.ID); !has(ids, shareSite) {
		t.Errorf("share markers = %v, want the listed admin's marker linked", ids)
	}
	// An unreadable admins list links nothing.
	e2 := newLoopEnv(t)
	e2.shared(nil)
	e2.p.owner.UnlinkShare(e2.to.ID, shareSite)
	e2.fs.adminErrs = map[string]error{shareSite: errors.New("disk")}
	e2.tick(later())
	if ids, _ := e2.p.owner.SharedSites(e2.to.ID); has(ids, shareSite) {
		t.Error("a marker was linked on an unknown")
	}
}

// A re-accept between SetAdmins and the unlink keeps the marker and tags.
func TestLoopExpiredShareRenewedMeanwhileKeepsItsMarker(t *testing.T) {
	e := newLoopEnv(t)
	e.shared(inDays(1))
	if err := e.p.SetAdminTags(e.to.ID, shareSite, []string{"mine"}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	e.fs.onAdmins = func(id string) {
		reads++
		if reads == 2 { // 1: the expiry check, 2: the fresh read after SetAdmins
			i := e.fs.infos[id]
			i.Admins = []ext.SiteAdmin{{Account: e.to.ID, Until: inDays(30)}}
			e.fs.infos[id] = i
		}
	}
	e.tick(time.Now().Add(48 * time.Hour))
	e.fs.onAdmins = nil
	if ids, _ := e.p.owner.SharedSites(e.to.ID); !has(ids, shareSite) {
		t.Errorf("share markers = %v, want the renewed share's kept", ids)
	}
	if reg, _ := e.p.accounts.Tags(e.to.ID); len(reg.Sites[shareSite]) != 1 {
		t.Errorf("tags = %+v, want kept", reg.Sites)
	}
}

func TestLoopTransferExpiredSkipsARecipientNeverNotified(t *testing.T) {
	e := newLoopEnv(t)
	o := e.offer(tid(31))
	e.change(o, func(x *ownership.Offer) { x.AcceptBy = time.Now().Add(-time.Hour); x.State = ownership.StatePaused })
	e.tick(time.Now())
	if got := e.current(o).State; got != ownership.StateExpired {
		t.Fatalf("state = %s", got)
	}
	if e.mails("pro@example.com", subjTransferGone) != 1 {
		t.Error("the sender was not told")
	}
	if e.mails(takerEmail, subjTransferGone) != 0 {
		t.Error("a recipient who was never told of the offer got an expiry mail")
	}
}

func TestLoopHostConflictReadErrorIsUnknown(t *testing.T) {
	e := newLoopEnv(t)
	o := e.offer(tid(31))
	e.change(o, func(x *ownership.Offer) { x.State = ownership.StatePaused })
	e.fs.conflictsErr = errors.New("disk")
	e.tick(time.Now())
	if got := e.current(o).State; got != ownership.StatePaused {
		t.Errorf("state = %s, want paused on an unknown", got)
	}
	e.fs.conflictsErr = nil
	e.tick(time.Now().Add(time.Minute))
	if got := e.current(o).State; got != ownership.StateOpen {
		t.Errorf("state = %s, want open once readable", got)
	}
}

func TestHostConflictReadErrorRefusesCreateAndAccept(t *testing.T) {
	e := newLoopEnv(t)
	o := e.offer(tid(31))
	e.fs.conflictsErr = errors.New("disk")
	if w := e.takeEnv.accept(o); w.Code == http.StatusOK || w.Code == http.StatusSeeOther {
		t.Errorf("accept with unreadable host conflicts = %d, want refused", w.Code)
	}
	if owner, _ := e.fs.SiteOwner(tid(31)); owner != e.acc.ID {
		t.Error("a site moved on an unknown")
	}
	if ref := e.p.hostRule(e.acc, []string{tid(32)}); ref == nil || !ref.retry {
		t.Errorf("hostRule on an error = %+v, want a retry refusal, never a pass", ref)
	}
}

func TestLoopAPanickingStepDoesNotStopTheOthers(t *testing.T) {
	e := newLoopEnv(t)
	captureLog(t)
	if err := e.p.accounts.LinkSite(e.to, tid(31)); err != nil { // stray
		t.Fatal(err)
	}
	e.shared(inDays(1))
	e.fs.onAdmins = func(string) { panic("boom") }
	e.tick(later().Add(48 * time.Hour)) // must not panic
	e.fs.onAdmins = nil
	if has(e.markers(e.to), tid(31)) {
		t.Error("the reconciliation did not run after earlier steps panicked")
	}
}

func TestNotifyOfferRechecksDatesInTheStore(t *testing.T) {
	e := newLoopEnv(t)
	from := time.Now().Add(48 * time.Hour)
	o := e.offer(tid(31))
	o = e.change(o, func(x *ownership.Offer) { x.AvailableFrom = &from })
	e.p.notifyOfferAt(o, time.Now())
	e.p.mailWG.Wait()
	if e.current(o).NotifiedAt != nil || e.mails(takerEmail, subjOffered) != 0 {
		t.Error("an offer before its available-from was notified")
	}
	e.change(o, func(x *ownership.Offer) { x.AvailableFrom = nil; x.AcceptBy = time.Now().Add(-time.Hour) })
	e.p.notifyOfferAt(e.current(o), time.Now())
	e.p.mailWG.Wait()
	if e.current(o).NotifiedAt != nil {
		t.Error("an offer past accept-by was notified")
	}
}

// panicSitesHost is a host whose Sites() panics: ownershipTick asks for it
// in its guard, outside every step.
type panicSitesHost struct{ ext.Host }

func (panicSitesHost) Sites() ext.SiteService { panic("the site service is broken") }

// Final review N1: a panic outside a step (the nil guards, the log pruning,
// the due checks) is recovered by the tick itself, so the loop -- and the
// server -- survive it, and the next tick runs.
func TestLoopTickRecoversAPanicOutsideAStep(t *testing.T) {
	e := newLoopEnv(t)
	good := e.p.host
	e.p.host = panicSitesHost{good}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("a panic outside a step escaped the tick: %v", r)
			}
		}()
		e.p.safeTick()
	}()
	// The tick lock was not left held: the next tick runs.
	e.p.host = good
	done := make(chan struct{})
	go func() {
		e.p.safeTick()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the tick after a panic never finished")
	}
}

// Final review N2: the accepting offers are asked AFTER the site's owner is
// read. An accept that begins just before that read has linked its
// recipient's marker ahead of the move; the reconciliation must see the
// accept and keep the marker.
func TestLoopKeepsTheMarkerOfAnAcceptThatBeganBeforeTheOwnerRead(t *testing.T) {
	e := newLoopEnv(t)
	site := tid(31) // the sender's
	if err := e.p.accounts.LinkSite(e.to, site); err != nil {
		t.Fatal(err)
	}
	o := e.offer(site)
	var once sync.Once
	e.fs.onOwner = func(id string) {
		if id != site {
			return
		}
		once.Do(func() {
			e.change(o, func(x *ownership.Offer) { x.State, x.AcceptedBy = ownership.StateAccepting, e.to.ID })
		})
	}
	e.p.reconcileMarkers(later())
	if !has(e.markers(e.to), site) {
		t.Error("the marker of an accept that began before the owner read was unlinked")
	}
}
