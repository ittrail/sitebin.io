//go:build ee

package ownership

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ids"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOfferRoundTrip(t *testing.T) {
	s := newStore(t)
	from := ids.New()
	a, b := ids.New(), ids.New()
	avail := time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC)
	acceptBy := time.Date(2026, 12, 31, 8, 0, 0, 0, time.UTC)
	created := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	in := Offer{
		Sites: []string{a, b}, From: from, ToEmail: "  Bob@Example.COM ",
		AvailableFrom: &avail, AcceptBy: acceptBy, Created: created,
		State: StateOpen, Done: []string{a},
	}
	got, err := s.CreateOffer(in, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ids.ValidID(got.ID) || got.ToEmail != "bob@example.com" {
		t.Fatalf("created = %+v", got)
	}
	re, err := s.GetOffer(got.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := in
	want.ID, want.ToEmail = got.ID, "bob@example.com"
	if !reflect.DeepEqual(re, want) {
		t.Fatalf("round trip\n got %+v\nwant %+v", re, want)
	}
	if _, err := s.GetOffer(ids.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing offer = %v", err)
	}
	if _, err := s.GetOffer("../../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a path as an id = %v", err)
	}
	// The file holds no password field of any kind.
	raw, _ := os.ReadFile(filepath.Join(s.root, "offers", got.ID+".json"))
	if strings.Contains(strings.ToLower(string(raw)), "password") {
		t.Errorf("offer file mentions a password: %s", raw)
	}
}

func TestInviteRoundTrip(t *testing.T) {
	s := newStore(t)
	until := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	in := Invite{Site: ids.New(), From: ids.New(), ToEmail: " Carol@Example.com", Until: &until,
		Created: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC), State: StateOpen}
	got, err := s.CreateInvite(in, 0)
	if err != nil {
		t.Fatal(err)
	}
	re, err := s.GetInvite(got.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := in
	want.ID, want.ToEmail = got.ID, "carol@example.com"
	if !reflect.DeepEqual(re, want) {
		t.Fatalf("round trip\n got %+v\nwant %+v", re, want)
	}
	if _, err := s.GetInvite(ids.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing invite = %v", err)
	}
}

func TestUpdateRunsUnderMutexAndMayRefuse(t *testing.T) {
	s := newStore(t)
	o, _ := s.CreateOffer(Offer{Sites: []string{ids.New()}, From: ids.New(), ToEmail: "a@b.c", State: StateOpen}, 0)
	refuse := errors.New("no")
	if _, err := s.UpdateOffer(o.ID, func(o *Offer) error { o.State = StateAccepted; return refuse }); err != refuse {
		t.Fatalf("refusal = %v", err)
	}
	if g, _ := s.GetOffer(o.ID); g.State != StateOpen {
		t.Fatalf("a refused update wrote %q", g.State)
	}
	up, err := s.UpdateOffer(o.ID, func(o *Offer) error { o.ID = "x"; o.State = StatePaused; return nil })
	if err != nil || up.ID != o.ID || up.State != StatePaused {
		t.Fatalf("update = %+v %v", up, err)
	}
	if _, err := s.UpdateOffer(ids.New(), func(*Offer) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of a missing offer = %v", err)
	}
	i, _ := s.CreateInvite(Invite{Site: ids.New(), From: ids.New(), ToEmail: "a@b.c", State: StateOpen}, 0)
	if _, err := s.UpdateInvite(i.ID, func(i *Invite) error { i.State = StateDeclined; return nil }); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetInvite(i.ID); g.State != StateDeclined {
		t.Errorf("invite state %q", g.State)
	}
}

func TestASiteIsInAtMostOneOpenOffer(t *testing.T) {
	s := newStore(t)
	site, other, third := ids.New(), ids.New(), ids.New()
	first, err := s.CreateOffer(Offer{Sites: []string{site}, From: ids.New(), ToEmail: "a@b.c", State: StateOpen}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOffer(Offer{Sites: []string{other, site}, From: ids.New(), ToEmail: "x@b.c", State: StateOpen}, 0); !errors.Is(err, ErrSiteOffered) {
		t.Fatalf("create with an offered site = %v, want ErrSiteOffered", err)
	}
	// Nothing of the refused offer was written.
	if open, _ := s.OpenOffers(); len(open) != 1 {
		t.Fatalf("open offers = %d", len(open))
	}
	second, err := s.CreateOffer(Offer{Sites: []string{other}, From: ids.New(), ToEmail: "x@b.c", State: StateOpen}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Adding a site that another open offer holds is refused, and writes nothing.
	_, err = s.UpdateOffer(second.ID, func(o *Offer) error { o.Sites = append(o.Sites, site); return nil })
	if !errors.Is(err, ErrSiteOffered) {
		t.Fatalf("add of an offered site = %v, want ErrSiteOffered", err)
	}
	if g, _ := s.GetOffer(second.ID); len(g.Sites) != 1 {
		t.Fatalf("a refused add wrote %v", g.Sites)
	}
	// A free site may be added; an offer's own sites do not collide with itself.
	if _, err := s.UpdateOffer(second.ID, func(o *Offer) error { o.Sites = append(o.Sites, third); return nil }); err != nil {
		t.Fatalf("add of a free site = %v", err)
	}
	// Paused and accepting offers still hold their sites.
	for _, st := range []string{StatePaused, StateAccepting} {
		s.UpdateOffer(first.ID, func(o *Offer) error { o.State = st; return nil })
		if _, err := s.CreateOffer(Offer{Sites: []string{site}, From: ids.New(), ToEmail: "z@b.c", State: StateOpen}, 0); !errors.Is(err, ErrSiteOffered) {
			t.Fatalf("site of a %s offer = %v, want ErrSiteOffered", st, err)
		}
	}
	// Once the first offer is cancelled, the site can be offered again.
	s.UpdateOffer(first.ID, func(o *Offer) error { o.State = StateCancelled; return nil })
	if _, err := s.CreateOffer(Offer{Sites: []string{site}, From: ids.New(), ToEmail: "z@b.c", State: StateOpen}, 0); err != nil {
		t.Fatalf("offer after cancel = %v", err)
	}
}

func TestLookups(t *testing.T) {
	s := newStore(t)
	alice, bob := ids.New(), ids.New()
	s1, s2, s3 := ids.New(), ids.New(), ids.New()
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	o1, _ := s.CreateOffer(Offer{Sites: []string{s1, s2}, From: alice, ToEmail: "Z@Example.com", State: StateOpen, Created: t0}, 0)
	o2, _ := s.CreateOffer(Offer{Sites: []string{s3}, From: bob, ToEmail: "z@example.com", State: StateAccepted, Created: t0.Add(time.Hour)}, 0)
	i1, _ := s.CreateInvite(Invite{Site: s1, From: alice, ToEmail: "Y@example.com", State: StateOpen, Created: t0}, 0)
	i2, _ := s.CreateInvite(Invite{Site: s1, From: alice, ToEmail: "y@example.com", State: StateDeclined, Created: t0.Add(time.Hour)}, 0)

	offerIDs := func(os []Offer) []string {
		var out []string
		for _, o := range os {
			out = append(out, o.ID)
		}
		return out
	}
	inviteIDs := func(is []Invite) []string {
		var out []string
		for _, i := range is {
			out = append(out, i.ID)
		}
		return out
	}
	if got, _ := s.OffersBySender(alice); !reflect.DeepEqual(offerIDs(got), []string{o1.ID}) {
		t.Errorf("by sender = %v", offerIDs(got))
	}
	if got, _ := s.OffersByEmail(" Z@EXAMPLE.com"); !reflect.DeepEqual(offerIDs(got), []string{o2.ID, o1.ID}) {
		t.Errorf("by email (newest first) = %v", offerIDs(got))
	}
	if got, _ := s.OffersBySite(s2); !reflect.DeepEqual(offerIDs(got), []string{o1.ID}) {
		t.Errorf("by site = %v", offerIDs(got))
	}
	if got, _ := s.OpenOffers(); !reflect.DeepEqual(offerIDs(got), []string{o1.ID}) {
		t.Errorf("open offers = %v", offerIDs(got))
	}
	if got, _ := s.InvitesBySender(alice); !reflect.DeepEqual(inviteIDs(got), []string{i2.ID, i1.ID}) {
		t.Errorf("invites by sender = %v", inviteIDs(got))
	}
	if got, _ := s.InvitesByEmail("Y@Example.com"); len(got) != 2 {
		t.Errorf("invites by email = %v", inviteIDs(got))
	}
	if got, _ := s.InvitesBySite(s1); len(got) != 2 {
		t.Errorf("invites by site = %v", inviteIDs(got))
	}
	if got, _ := s.OpenInvites(); !reflect.DeepEqual(inviteIDs(got), []string{i1.ID}) {
		t.Errorf("open invites = %v", inviteIDs(got))
	}
}

func TestAtomicWritesSurviveAndCrashedTempIsIgnored(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	o, _ := s.CreateOffer(Offer{Sites: []string{ids.New()}, From: ids.New(), ToEmail: "a@b.c", State: StateOpen}, 0)
	for i := 0; i < 5; i++ {
		if _, err := s.UpdateOffer(o.ID, func(o *Offer) error { o.Done = append(o.Done, "x"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	// A crash left a half-written temp file beside the offer, and one that
	// even looks like another offer.
	od := filepath.Join(s.root, "offers")
	os.WriteFile(filepath.Join(od, o.ID+".json.tmp-123"), []byte(`{"id":"`), 0o600)
	os.WriteFile(filepath.Join(od, ids.New()+".json.tmp-9"), []byte(`{"state":"open"}`), 0o600)
	// A fresh store on the same directory reads it back.
	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	g, err := s2.GetOffer(o.ID)
	if err != nil || len(g.Done) != 5 {
		t.Fatalf("reread = %+v %v", g, err)
	}
	if open, err := s2.OpenOffers(); err != nil || len(open) != 1 {
		t.Fatalf("open offers with a temp file = %d %v", len(open), err)
	}
}

func TestDailyCount(t *testing.T) {
	s := newStore(t)
	me, other := ids.New(), ids.New()
	day := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	mk := func(from string, at time.Time) {
		t.Helper()
		if _, err := s.CreateOffer(Offer{Sites: []string{ids.New()}, From: from, ToEmail: "a@b.c", State: StateOpen, Created: at}, 0); err != nil {
			t.Fatal(err)
		}
	}
	mk(me, day.Add(time.Hour))
	mk(me, day.Add(23*time.Hour+59*time.Minute))
	mk(me, day.Add(-time.Minute)) // yesterday
	mk(other, day.Add(time.Hour))
	if got := s.DailyCount(me, day.Add(5*time.Hour)); got != 2 {
		t.Errorf("DailyCount = %d, want 2 (two today, one yesterday)", got)
	}
	// An invite counts too, and a later cancel does not take it back.
	i, err := s.CreateInvite(Invite{Site: ids.New(), From: me, ToEmail: "a@b.c", State: StateOpen, Created: day.Add(2 * time.Hour)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.UpdateInvite(i.ID, func(i *Invite) error { i.State = StateCancelled; return nil })
	if got := s.DailyCount(me, day.Add(5*time.Hour)); got != 3 {
		t.Errorf("with an invite = %d, want 3", got)
	}
	if got := s.DailyCount(me, day.Add(-time.Hour)); got != 1 {
		t.Errorf("yesterday = %d, want 1", got)
	}
	if got := s.DailyCount(ids.New(), day); got != 0 {
		t.Errorf("a stranger = %d", got)
	}
}

func TestShareMarkers(t *testing.T) {
	s := newStore(t)
	acc, site, site2 := ids.New(), ids.New(), ids.New()
	if got, err := s.SharedSites(acc); err != nil || len(got) != 0 {
		t.Fatalf("none = %v %v", got, err)
	}
	for _, id := range []string{site, site2, site} {
		if err := s.LinkShare(acc, id); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SharedSites(acc)
	if err != nil || len(got) != 2 {
		t.Fatalf("shared = %v %v", got, err)
	}
	if err := s.UnlinkShare(acc, site); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlinkShare(acc, site); err != nil {
		t.Fatalf("unlink twice = %v", err)
	}
	if got, _ := s.SharedSites(acc); !reflect.DeepEqual(got, []string{site2}) {
		t.Errorf("after unlink = %v", got)
	}
	if err := s.LinkShare("../x", site); err == nil {
		t.Error("a path as an account id was accepted")
	}
	if err := s.LinkShare(acc, "bad"); err == nil {
		t.Error("a bad site id was accepted")
	}
}

func TestDailyLimitHoldsUnderParallelCreates(t *testing.T) {
	s := newStore(t)
	me := ids.New()
	for i := 0; i < 19; i++ {
		if _, err := s.CreateInvite(Invite{Site: ids.New(), From: me, ToEmail: "a@b.c"}, 0); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	okOffers, okInvites := 0, 0
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := s.CreateOffer(Offer{Sites: []string{ids.New()}, From: me, ToEmail: "a@b.c"}, 20); err == nil {
				mu.Lock()
				okOffers++
				mu.Unlock()
			} else if !errors.Is(err, ErrDailyLimit) {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := s.CreateInvite(Invite{Site: ids.New(), From: me, ToEmail: "a@b.c"}, 20); err == nil {
				mu.Lock()
				okInvites++
				mu.Unlock()
			} else if !errors.Is(err, ErrDailyLimit) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if okOffers+okInvites != 1 {
		t.Fatalf("%d offers and %d invitations got through at 19/20, want exactly 1 in all", okOffers, okInvites)
	}
	if n := s.DailyCount(me, time.Now()); n != 20 {
		t.Errorf("count = %d, want 20", n)
	}
	// Another account and another day are not affected.
	if _, err := s.CreateOffer(Offer{Sites: []string{ids.New()}, From: ids.New(), ToEmail: "a@b.c"}, 20); err != nil {
		t.Errorf("another account: %v", err)
	}
	if _, err := s.CreateOffer(Offer{Sites: []string{ids.New()}, From: me, ToEmail: "a@b.c", Created: time.Now().Add(-48 * time.Hour)}, 20); err != nil {
		t.Errorf("another day: %v", err)
	}
}

func TestDailyLimitRefusesWhenTheCountCannotBeRead(t *testing.T) {
	s := newStore(t)
	me := ids.New()
	if err := os.WriteFile(filepath.Join(s.offersDir(), ids.New()+".json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateOffer(Offer{Sites: []string{ids.New()}, From: me, ToEmail: "a@b.c"}, 20)
	if err == nil || errors.Is(err, ErrDailyLimit) {
		t.Fatalf("err = %v, want a read error (a refusal, not a partial count)", err)
	}
	if _, err := s.CreateInvite(Invite{Site: ids.New(), From: me, ToEmail: "a@b.c"}, 20); err == nil {
		t.Error("an invitation got through an unreadable count")
	}
	// Without a limit nothing is counted.
	if _, err := s.CreateOffer(Offer{Sites: []string{ids.New()}, From: me, ToEmail: "a@b.c"}, 0); err != nil {
		t.Errorf("no limit: %v", err)
	}
}
