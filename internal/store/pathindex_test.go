package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// pathStore is a store with verification off and n fresh sites of one owner.
func pathStore(t *testing.T, n int) (*Store, []*Site) {
	t.Helper()
	s := newTestStore(t)
	s.SetDomainVerifier(TrustingVerifier{}, "sitebin.example")
	var sites []*Site
	for i := 0; i < n; i++ {
		site, _, err := s.Create()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Update(site, func(m *Meta) error { m.OwnerAccountID = "u1"; return nil }); err != nil {
			t.Fatal(err)
		}
		sites = append(sites, site)
	}
	return s, sites
}

func mustAdd(t *testing.T, s *Store, site *Site, addr string) {
	t.Helper()
	if err := s.AddDomain(site, addr); err != nil {
		t.Fatalf("AddDomain(%s): %v", addr, err)
	}
}

func TestAttachPathAddressIndexes(t *testing.T) {
	s, sites := pathStore(t, 2)
	a, b := sites[0], sites[1]
	mustAdd(t, s, a, "m.at")
	mustAdd(t, s, b, "m.at/demos/k1")
	if got, err := s.ByDomain("m.at/demos/k1"); err != nil || got.ViewID != b.ViewID {
		t.Fatalf("ByDomain(path) = %v, %v; want b", got, err)
	}
	if got, err := s.ByDomain("m.at"); err != nil || got.ViewID != a.ViewID {
		t.Fatalf("ByDomain(host) = %v, %v; want a", got, err)
	}
	if !linkExists(filepath.Join(s.Root(), "path-index", "m.at", "demos+k1")) {
		t.Fatal("the path address has no link in path-index/m.at/demos+k1")
	}
	if !slicesContains(b.Meta.CustomDomains, "m.at/demos/k1") {
		t.Fatalf("CustomDomains = %v", b.Meta.CustomDomains)
	}
}

func slicesContains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestResolveAddressLongestPrefix(t *testing.T) {
	s, sites := pathStore(t, 3)
	a, b, c := sites[0], sites[1], sites[2]
	mustAdd(t, s, a, "m.at")
	mustAdd(t, s, b, "m.at/demos")
	mustAdd(t, s, c, "m.at/demos/k1")
	cases := []struct{ path, want, addr string }{
		{"/demos/k1/x.css", c.ViewID, "m.at/demos/k1"},
		{"/demos/k1/", c.ViewID, "m.at/demos/k1"},
		{"/demos/k1", c.ViewID, "m.at/demos/k1"},
		{"/demos/k10", b.ViewID, "m.at/demos"},
		{"/demos", b.ViewID, "m.at/demos"},
		{"/demosx", a.ViewID, "m.at"},
		{"/", a.ViewID, "m.at"},
		{"", a.ViewID, "m.at"},
	}
	for _, tc := range cases {
		got, addr, err := s.ResolveAddress("m.at", tc.path)
		if err != nil || got.ViewID != tc.want || addr != tc.addr {
			t.Errorf("ResolveAddress(%q) = %v, %q, %v; want %s at %s", tc.path, got, addr, err, tc.want, tc.addr)
		}
	}
	if _, _, err := s.ResolveAddress("M.AT", "/demos/k1/"); err != nil {
		t.Errorf("the host is matched case-insensitively: %v", err)
	}
	s2, only := pathStore(t, 1)
	mustAdd(t, s2, only[0], "n.at/a")
	if _, _, err := s2.ResolveAddress("n.at", "/other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty root must be ErrNotFound, got %v", err)
	}
}

func TestHostKnownPathOnly(t *testing.T) {
	s, sites := pathStore(t, 1)
	mustAdd(t, s, sites[0], "m.at/a")
	if !s.HostKnown("m.at") {
		t.Error("a host with only a path address must be known (TLS)")
	}
	if s.HostKnown("x.at") {
		t.Error("an unknown host must not be known")
	}
	if got := s.HostAddresses("m.at"); len(got) != 1 || got[0].Addr != "m.at/a" || got[0].Site.ViewID != sites[0].ViewID {
		t.Errorf("HostAddresses = %+v", got)
	}
}

func TestCountDomainsCountsPaths(t *testing.T) {
	s, sites := pathStore(t, 2)
	mustAdd(t, s, sites[0], "m.at")
	mustAdd(t, s, sites[1], "m.at/a")
	mustAdd(t, s, sites[1], "n.at/b")
	if n, err := s.CountDomains(); err != nil || n != 3 {
		t.Fatalf("CountDomains = %d, %v; want 3", n, err)
	}
}

func TestDeleteRemovesPathLinks(t *testing.T) {
	s, sites := pathStore(t, 1)
	mustAdd(t, s, sites[0], "m.at/a")
	if err := s.Delete(sites[0]); err != nil {
		t.Fatal(err)
	}
	if linkExists(filepath.Join(s.Root(), "path-index", "m.at", "a")) {
		t.Fatal("Delete left the path link behind")
	}
	if s.HostKnown("m.at") {
		t.Fatal("the host is still known after its only site was deleted")
	}
}

func TestRemoveDomainPathAddress(t *testing.T) {
	s, sites := pathStore(t, 1)
	mustAdd(t, s, sites[0], "m.at/a")
	if err := s.RemoveDomain(sites[0], "m.at/a"); err != nil {
		t.Fatal(err)
	}
	if s.HostKnown("m.at") || len(sites[0].Meta.CustomDomains) != 0 {
		t.Fatalf("still attached: %v", sites[0].Meta.CustomDomains)
	}
}

func TestDanglingIndexLinksFindsPathLinks(t *testing.T) {
	s, sites := pathStore(t, 1)
	mustAdd(t, s, sites[0], "m.at/a")
	// The site's folder disappears behind the link (a crash mid-delete).
	if err := os.RemoveAll(filepath.Join(s.Root(), "sites", sites[0].ViewID)); err != nil {
		t.Fatal(err)
	}
	links, err := s.DanglingIndexLinks()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(s.Root(), "path-index", "m.at", "a")
	found := false
	for _, l := range links {
		if l == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("DanglingIndexLinks = %v, want it to include %s", links, want)
	}
}

func TestRedirectAndTargetAcceptPaths(t *testing.T) {
	s, sites := pathStore(t, 1)
	site := sites[0]
	mustAdd(t, s, site, "m.at/old")
	mustAdd(t, s, site, "m.at/new")
	if err := s.SetDomainRedirect(site, "m.at/old", "m.at/new", 301); err != nil {
		t.Fatalf("SetDomainRedirect: %v", err)
	}
	if r, ok := site.Meta.DomainRedirect("m.at/old"); !ok || r.To != "m.at/new" {
		t.Fatalf("DomainRedirect = %+v, %v", r, ok)
	}
	cs, ct := containerSite(t)
	if err := cs.SyncAddresses(ct, []string{"web:3000", "web:4000"}); err != nil {
		t.Fatal(err)
	}
	if err := cs.AddDomain(ct, "c.at/api"); err != nil {
		t.Fatal(err)
	}
	if err := cs.SetDomainTarget(ct, "c.at/api", "web:4000"); err != nil {
		t.Fatalf("SetDomainTarget: %v", err)
	}
	if got := ct.Meta.DomainEndpoint("c.at/api"); got != "web:4000" {
		t.Fatalf("DomainEndpoint = %q", got)
	}
}

func TestTXTNameUsesHost(t *testing.T) {
	if got := (DomainClaim{Domain: "m.at/a"}).TXTName(); got != "_sitebin-challenge.m.at" {
		t.Fatalf("TXTName = %q", got)
	}
}
