package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

func provStore(t *testing.T) (*store.Store, *store.Site, *store.Site) {
	t.Helper()
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := st.Create()
	b, _, _ := st.Create()
	now := time.Now().UTC()
	st.RecordProvenance(a, provenance.Entry{Time: now, Action: provenance.ActionCreate, Surface: provenance.SurfaceAPI, Auth: provenance.AuthToken, Account: "acct1", IP: "203.0.113.7", UA: "python-requests/2.32", Files: 1, Detail: "bug.html"})
	st.RecordProvenance(a, provenance.Entry{Time: now.Add(time.Minute), Action: provenance.ActionUpload, Surface: provenance.SurfaceWebDAV, Auth: provenance.AuthPassword, IP: "198.51.100.4", Files: 3})
	st.RecordProvenance(b, provenance.Entry{Time: now, Action: provenance.ActionCreate, Surface: provenance.SurfaceUI, IP: "192.0.2.1"})
	return st, a, b
}

func TestListShowsWhereEachSiteCameFrom(t *testing.T) {
	st, a, _ := provStore(t)
	var out bytes.Buffer
	if err := listSites(st, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "FROM") || !strings.Contains(s, "203.0.113.7") || !strings.Contains(s, "192.0.2.1") {
		t.Fatalf("list:\n%s", s)
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, a.ViewID) && !strings.Contains(line, "203.0.113.7") {
			t.Fatalf("the creator's address is not on the site's row: %q", line)
		}
	}
}

func TestProvenanceCommand(t *testing.T) {
	st, a, b := provStore(t)

	var out bytes.Buffer
	if err := showProvenance(st, &out, a.EditID); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{a.ViewID, "create", "bug.html", "python-requests/2.32", "api/token", "acct1", "upload", "198.51.100.4", "webdav/password"} {
		if !strings.Contains(s, want) {
			t.Errorf("site log lacks %q:\n%s", want, s)
		}
	}

	out.Reset()
	if err := showProvenance(st, &out, "203.0.113.0/24"); err != nil {
		t.Fatal(err)
	}
	s = out.String()
	if !strings.Contains(s, a.ViewID) || strings.Contains(s, b.ViewID) || !strings.Contains(s, "1 site(s)") {
		t.Fatalf("address search:\n%s", s)
	}
	if !strings.Contains(s, "acct1") {
		t.Errorf("the address search should name the acting account:\n%s", s)
	}

	if err := showProvenance(st, &out, "nothing-like-a-site"); err == nil {
		t.Fatal("an unknown key was answered")
	}
}
