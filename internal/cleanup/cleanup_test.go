package cleanup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

func TestSweep(t *testing.T) {
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	healthy, _, _ := st.Create()
	expiredRecent, _, _ := st.Create() // inside grace period → kept
	expiredOld, _, _ := st.Create()    // past grace → deleted
	if err := st.AddDomain(expiredOld, "old.example.org"); err != nil {
		t.Fatal(err)
	}

	set := func(s *store.Site, when time.Time) {
		if err := st.Update(s, func(m *store.Meta) error { m.ExpiresAt = &when; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	set(expiredRecent, now.Add(-1*time.Hour))
	set(expiredOld, now.Add(-25*time.Hour))

	removed, err := Sweep(st, now)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := st.ByViewID(healthy.ViewID); err != nil {
		t.Errorf("healthy site removed: %v", err)
	}
	if _, err := st.ByViewID(expiredRecent.ViewID); err != nil {
		t.Errorf("in-grace site removed: %v", err)
	}
	if _, err := st.ByViewID(expiredOld.ViewID); err == nil {
		t.Error("expired site survived")
	}
	if _, err := st.ByDomain("old.example.org"); err == nil {
		t.Error("expired site's domain link survived")
	}
	if _, err := st.ByEditID(expiredOld.EditID); err == nil {
		t.Error("expired site's edit link survived")
	}
}

func TestSweepPrunesDanglingLinks(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir, "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	site, _, _ := st.Create()
	// simulate a crash that removed the site dir but left index links behind
	if err := os.RemoveAll(site.Dir()); err != nil {
		t.Fatal(err)
	}
	if _, err := Sweep(st, time.Now()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "edit-index", site.EditID)); !os.IsNotExist(err) {
		t.Error("dangling edit link not pruned")
	}
}
