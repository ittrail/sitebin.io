//go:build ee

package account

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/provenance"
)

func TestAccountProvenance(t *testing.T) {
	s := newStore(t)
	a, _ := s.CreateLocal("a@example.com", "h", "free")
	old := time.Now().Add(-100 * 24 * time.Hour).UTC()
	if err := s.RecordProvenance(a.ID, provenance.Entry{Time: old, Action: provenance.ActionSignup, IP: "203.0.113.7"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProvenance(a.ID, provenance.Entry{Action: provenance.ActionTokenMint, IP: "203.0.113.7", Detail: "testing"}); err != nil {
		t.Fatal(err)
	}
	es, err := s.Provenance(a.ID)
	if err != nil || len(es) != 2 || es[1].Detail != "testing" {
		t.Fatalf("log: %+v %v", es, err)
	}
	if n, err := s.PurgeProvenance(a.ID, time.Now().Add(-provenance.Retention)); err != nil || n != 1 {
		t.Fatalf("purged %d %v", n, err)
	}
	// An unknown account records nothing and creates no folder.
	if err := s.RecordProvenance("aaaaaaaaaaaaaaaaaaaaaaaaaa", provenance.Entry{Action: provenance.ActionSignin}); err == nil {
		t.Fatal("recorded for an unknown account")
	}
	if _, err := os.Stat(filepath.Join(s.accountDir("aaaaaaaaaaaaaaaaaaaaaaaaaa"))); !os.IsNotExist(err) {
		t.Fatal("a record created an account folder")
	}
}

// Deleting an account deletes its log.
func TestDeleteTakesTheAccountLog(t *testing.T) {
	s := newStore(t)
	a, _ := s.CreateLocal("a@example.com", "h", "free")
	s.RecordProvenance(a.ID, provenance.Entry{Action: provenance.ActionSignup, IP: "203.0.113.7"})
	if err := s.Delete(a, nil); err != nil {
		t.Fatal(err)
	}
	if es, _ := s.Provenance(a.ID); len(es) != 0 {
		t.Fatalf("the log outlived its account: %+v", es)
	}
}

func TestListIDs(t *testing.T) {
	s := newStore(t)
	a, _ := s.CreateLocal("a@example.com", "h", "free")
	b, _ := s.CreateLocal("b@example.com", "h", "free")
	got, err := s.ListIDs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{a.ID, b.ID}
	sort.Strings(want)
	sort.Strings(got)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ids: %v want %v", got, want)
	}
}
