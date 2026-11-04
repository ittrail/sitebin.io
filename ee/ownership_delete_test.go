//go:build ee

package ee

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/ownership"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// ---- a site that lost its marker (final review M2) ----

// An account's deletion lists its sites by marker. A site whose meta.json
// names the account but which lost its marker is linked first, and goes with
// the account.
func TestDeletionFindsASiteThatLostItsMarker(t *testing.T) {
	e := newGDPREnv(t)
	owner := e.oidc("sub-owner", "owner@example.com")
	const lost = "aaaaaaaaaaaaaaaaaaaaaaaabj"
	e.fs.infos[lost] = ext.SiteInfo{ViewID: lost, Mode: "webserver", Owner: owner.ID}
	if w := e.gdprDelete(owner); w.Code != http.StatusOK {
		t.Fatalf("delete = %d (%s)", w.Code, w.Body)
	}
	if _, ok := e.fs.infos[lost]; ok {
		t.Error("a site that lost its marker outlived its owner")
	}
	if !e.gone(owner) {
		t.Error("the account was kept")
	}
}

// A locked site that lost its marker stops the deletion, before anything
// ends -- by its owner and by the stack's order alike.
func TestDeletionStopsOnALockedSiteThatLostItsMarker(t *testing.T) {
	e := newGDPREnv(t)
	const lost, theirs = "aaaaaaaaaaaaaaaaaaaaaaaabk", "aaaaaaaaaaaaaaaaaaaaaaaabl"
	e.fs.infos[lost] = ext.SiteInfo{ViewID: lost, Mode: "webserver", Owner: e.to.ID}
	e.lock(lost, false)
	e.site(theirs)
	o := e.offer(theirs) // made to e.to: it would end with the account
	if w := e.localDelete(e.toCookie, e.to); w.Code != http.StatusConflict {
		t.Fatalf("local delete = %d, want 409 (%s)", w.Code, w.Body)
	}
	if e.gone(e.to) {
		t.Fatal("the account was deleted")
	}
	if got := e.offerState(o); got != ownership.StateOpen {
		t.Errorf("an offer to the account ended though it was kept: %s", got)
	}
	if _, ok := e.fs.infos[lost]; !ok {
		t.Error("the locked site was deleted")
	}

	owner := e.oidc("sub-owner", "owner@example.com")
	const lost2 = "aaaaaaaaaaaaaaaaaaaaaaaabm"
	e.fs.infos[lost2] = ext.SiteInfo{ViewID: lost2, Mode: "webserver", Owner: owner.ID}
	e.lock(lost2, false)
	if w := e.gdprDelete(owner); w.Code != http.StatusConflict {
		t.Fatalf("stack order = %d, want 409 (%s)", w.Code, w.Body)
	}
	if e.gone(owner) {
		t.Error("the stack's order deleted the account")
	}
}

// A walk that could not read every site proves nothing about the sites it
// missed: the deletion stops, and nothing is deleted.
func TestDeletionStopsWhenTheWalkIsIncomplete(t *testing.T) {
	e := newGDPREnv(t)
	owner := e.oidc("sub-owner", "owner@example.com")
	const mine = "aaaaaaaaaaaaaaaaaaaaaaaabn"
	e.own(owner, mine)
	e.fs.ownersErr = errors.New("site x: a meta.json could not be read")
	if w := e.gdprDelete(owner); w.Code < 500 {
		t.Fatalf("delete = %d, want a 5xx the stack retries (%s)", w.Code, w.Body)
	}
	if e.gone(owner) {
		t.Error("the account was deleted on an incomplete walk")
	}
	if _, ok := e.fs.infos[mine]; !ok {
		t.Error("a site was deleted on an incomplete walk")
	}
}

// ---- mutual admins (final review M3) ----

// barrierSites holds every DeleteSiteIfOwner until both deletions are inside
// accounts.Delete's callback (each holding its own account's lock), and
// serialises the fake's map access, which two deletions now share.
type barrierSites struct {
	*ownSites
	mu      sync.Mutex
	arrived sync.WaitGroup
}

func (s *barrierSites) Info(id string) (ext.SiteInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownSites.Info(id)
}

func (s *barrierSites) SiteAdmins(id string) ([]ext.SiteAdmin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownSites.SiteAdmins(id)
}

func (s *barrierSites) DeleteSiteIfOwner(id, owner string) error {
	s.arrived.Done()
	s.arrived.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownSites.DeleteSiteIfOwner(id, owner)
}

type barrierHost struct {
	*fakeHost
	sites *barrierSites
}

func (h *barrierHost) Sites() ext.SiteService { return h.sites }

// Two accounts that administer each other's sites are deleted at the same
// moment. The cleanup after each deleted site takes the former admin's
// account lock, so it must run after accounts.Delete returned: inside its
// callback the deleted account's lock is held, and the two deletions would
// wait on each other forever.
func TestConcurrentDeletionsOfMutualAdminsComplete(t *testing.T) {
	e := newGDPREnv(t)
	a := e.oidc("sub-a", "a@example.com")
	b := e.oidc("sub-b", "b@example.com")
	const siteA, siteB = "aaaaaaaaaaaaaaaaaaaaaaaabo", "aaaaaaaaaaaaaaaaaaaaaaaabp"
	e.own(a, siteA)
	e.own(b, siteB)
	e.makeAdmin(a, b, siteA, nil)
	e.makeAdmin(b, a, siteB, nil)
	e.tag(a, siteB)
	e.tag(b, siteA)

	bs := &barrierSites{ownSites: e.sites}
	bs.arrived.Add(2)
	e.p.host = &barrierHost{fakeHost: e.p.host.(*ownHost).fakeHost, sites: bs}

	errs := make(chan error, 2)
	for _, acc := range []*struct{ id string }{{a.ID}, {b.ID}} {
		go func() {
			cur, err := e.p.accounts.ByID(acc.id)
			if err != nil {
				errs <- err
				return
			}
			_, err = e.p.deleteAccountAndSites(cur)
			errs <- err
		}()
	}
	timeout := time.After(10 * time.Second)
	for range 2 {
		select {
		case err := <-errs:
			if err != nil {
				t.Errorf("a deletion failed: %v", err)
			}
		case <-timeout:
			t.Fatal("two deletions of mutual admins never completed: they wait on each other's account lock")
		}
	}
	if !e.gone(a) || !e.gone(b) {
		t.Error("an account was kept")
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	for _, id := range []string{siteA, siteB} {
		if _, ok := e.fs.infos[id]; ok {
			t.Errorf("%s outlived its owner", id)
		}
	}
}
