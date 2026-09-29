package cleanup

import (
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

var _ ext.AccountProvenance = (*purgingProvider)(nil)

// purgingProvider is stubProvider with an account log to purge.
type purgingProvider struct {
	*stubProvider
	cutoffs, held []time.Time
	// onPurge, if set, runs when the sweep asks for the purge.
	onPurge func()
}

func (p *purgingProvider) RecordAccountProvenance(string, provenance.Entry) {}
func (p *purgingProvider) PurgeProvenance(before, heldBefore time.Time) {
	p.cutoffs = append(p.cutoffs, before)
	p.held = append(p.held, heldBefore)
	if p.onPurge != nil {
		p.onPurge()
	}
}

// Provenance is kept 90 days: the sweep drops older entries from every
// site's log — except a locked site's, which is part of the evidence the lock
// holds — and hands the extension the same cutoff for the account logs.
func TestSweepPurgesProvenancePastRetention(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)
	p := &purgingProvider{stubProvider: &stubProvider{}}
	ext.Register(p)

	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.Add(-provenance.Retention - time.Hour)
	fresh := now.Add(-provenance.Retention + time.Hour)

	open, _, _ := st.Create()
	held, _, _ := st.Create()
	for _, s := range []*store.Site{open, held} {
		st.RecordProvenance(s, provenance.Entry{Time: old, Action: provenance.ActionCreate, IP: "203.0.113.7"})
		st.RecordProvenance(s, provenance.Entry{Time: fresh, Action: provenance.ActionUpload, IP: "203.0.113.8"})
	}
	if _, err := st.SetLock(held, &store.SiteLock{At: now, By: store.LockByAdmin}); err != nil {
		t.Fatal(err)
	}

	if _, err := Sweep(st, now); err != nil {
		t.Fatal(err)
	}
	if es, _ := st.Provenance(open); len(es) != 1 || es[0].Action != provenance.ActionUpload {
		t.Fatalf("open site after the sweep: %+v", es)
	}
	if es, _ := st.Provenance(held); len(es) != 2 {
		t.Fatalf("the locked site's log was purged: %+v", es)
	}
	if len(p.cutoffs) != 1 || !p.cutoffs[0].Equal(now.Add(-provenance.Retention)) {
		t.Fatalf("account purge cutoffs: %v", p.cutoffs)
	}
}

// The community build has no account logs, and its site logs are purged all
// the same.
func TestSweepPurgesProvenanceInCommunityBuild(t *testing.T) {
	ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	now := time.Now().UTC()
	s, _, _ := st.Create()
	st.RecordProvenance(s, provenance.Entry{Time: now.Add(-100 * 24 * time.Hour), Action: provenance.ActionCreate, IP: "203.0.113.7"})
	if _, err := Sweep(st, now); err != nil {
		t.Fatal(err)
	}
	if es, _ := st.Provenance(s); len(es) != 0 {
		t.Fatalf("not purged: %+v", es)
	}
}
