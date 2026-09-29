package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/provenance"
)

func TestSiteProvenanceRecordReadPurge(t *testing.T) {
	st := newTestStore(t)
	site, _, err := st.Create()
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-100 * 24 * time.Hour).UTC()
	if err := st.RecordProvenance(site, provenance.Entry{Time: old, Action: provenance.ActionCreate, IP: "203.0.113.7"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordProvenance(site, provenance.Entry{Action: provenance.ActionUpload, IP: "198.51.100.1", Files: 3}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Provenance(site)
	if err != nil || len(got) != 2 || got[1].Files != 3 || got[1].Time.IsZero() {
		t.Fatalf("provenance: %+v %v", got, err)
	}

	n, err := st.PurgeProvenance(site, time.Now().Add(-provenance.Retention))
	if err != nil || n != 1 {
		t.Fatalf("purged %d %v", n, err)
	}
	if got, _ := st.Provenance(site); len(got) != 1 || got[0].Action != provenance.ActionUpload {
		t.Fatalf("after purge: %+v", got)
	}
}

// The log is the site's record, not the site's content: it is never listed,
// counted against the quota, or served.
func TestProvenanceIsNotASiteFile(t *testing.T) {
	st := newTestStore(t)
	site, _, _ := st.Create()
	st.SaveFile(site, "index.html", strings.NewReader("<h1>hi</h1>"))
	st.RecordProvenance(site, provenance.Entry{Action: provenance.ActionCreate, IP: "203.0.113.7"})
	files, _ := st.ListFiles(site)
	if len(files) != 1 || files[0].Path != "index.html" {
		t.Fatalf("files: %+v", files)
	}
	if _, n, _ := st.Usage(site); n != 1 {
		t.Fatalf("usage counts the log: %d files", n)
	}
	if _, err := os.Stat(filepath.Join(site.FilesDir(), provenance.FileName)); !os.IsNotExist(err) {
		t.Fatal("the log landed in the served folder")
	}
}

// A record that races a delete must not bring the deleted site's folder back.
func TestProvenanceNeverResurrectsADeletedSite(t *testing.T) {
	st := newTestStore(t)
	site, _, _ := st.Create()
	if err := st.Delete(site); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordProvenance(site, provenance.Entry{Action: provenance.ActionUpload, IP: "203.0.113.7"}); err == nil {
		t.Fatal("recorded into a deleted site")
	}
	if _, err := os.Stat(site.Dir()); !os.IsNotExist(err) {
		t.Fatalf("site folder is back: %v", err)
	}
}

// Deleting a site takes its log with it.
func TestDeleteTakesTheLog(t *testing.T) {
	st := newTestStore(t)
	site, _, _ := st.Create()
	st.RecordProvenance(site, provenance.Entry{Action: provenance.ActionCreate, IP: "203.0.113.7"})
	path := filepath.Join(site.Dir(), provenance.FileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	st.Delete(site)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the log outlived its site")
	}
}
