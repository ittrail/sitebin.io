package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ids"
)

// Container addresses: one generated address per HTTP port, custom domains
// pointed at a port, and the file site's rules per address.

// containerSite is a container site with the given domains attached.
func containerSite(t *testing.T, domains ...string) (*Store, *Site) {
	t.Helper()
	s, site := redirectSite(t, domains...)
	if err := s.Update(site, func(m *Meta) error { m.Mode = ModeContainer; return nil }); err != nil {
		t.Fatal(err)
	}
	return s, site
}

func addressOf(t *testing.T, site *Site, endpoint string) Address {
	t.Helper()
	for _, a := range site.Meta.Addresses {
		if a.Endpoint == endpoint {
			return a
		}
	}
	t.Fatalf("no address for %s in %+v", endpoint, site.Meta.Addresses)
	return Address{}
}

func TestSyncAddresses(t *testing.T) {
	s, site := containerSite(t, "shop.example")
	if err := s.SyncAddresses(site, []string{"app:3000", "app:3001", "api:8080", "app:3000", "bad"}); err != nil {
		t.Fatalf("SyncAddresses: %v", err)
	}
	if got := site.Meta.Endpoints(); len(got) != 3 || got[0] != "app:3000" || got[1] != "app:3001" || got[2] != "api:8080" {
		t.Fatalf("endpoints = %v, want the file's order without duplicates or junk", got)
	}
	own, second, api := addressOf(t, site, "app:3000"), addressOf(t, site, "app:3001"), addressOf(t, site, "api:8080")
	if own.Label != site.ViewID {
		t.Errorf("the first port's label = %q, want the site's own address", own.Label)
	}
	for _, a := range []Address{second, api} {
		if !ids.ValidID(a.Label) || a.Label == site.ViewID {
			t.Errorf("%s label %q: want a random id of its own", a.Endpoint, a.Label)
		}
		got, err := s.ByAddress(a.Label)
		if err != nil || got.ViewID != site.ViewID {
			t.Errorf("ByAddress(%s) = %v, %v", a.Label, got, err)
		}
	}
	if second.Label == api.Label {
		t.Error("two ports share a label")
	}
	if got := site.Meta.DomainTargets["shop.example"]; got != "app:3000" {
		t.Errorf("a domain without a target got %q, want the own address's port", got)
	}
	if _, err := s.ByAddress(site.ViewID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the own address is not in the address index: %v", err)
	}

	// The same file again changes nothing.
	if err := s.SyncAddresses(site, []string{"app:3000", "app:3001", "api:8080"}); err != nil {
		t.Fatal(err)
	}
	if addressOf(t, site, "app:3001").Label != second.Label || addressOf(t, site, "api:8080").Label != api.Label {
		t.Error("labels moved on a sync that changed nothing")
	}

	// app renamed web, api dropped: web takes the free own address, app:3001
	// keeps its label, api's goes with its index entry. The domain keeps the
	// target it had, which serves nothing now.
	if err := s.SyncAddresses(site, []string{"web:3000", "app:3001"}); err != nil {
		t.Fatal(err)
	}
	if addressOf(t, site, "web:3000").Label != site.ViewID || addressOf(t, site, "app:3001").Label != second.Label {
		t.Errorf("addresses = %+v", site.Meta.Addresses)
	}
	if _, err := s.ByAddress(api.Label); !errors.Is(err, ErrNotFound) {
		t.Errorf("a dropped port's address still resolves: %v", err)
	}
	if linkExists(filepath.Join(s.addressIndexDir(), api.Label)) {
		t.Error("a dropped port's index entry stayed")
	}
	if site.Meta.DomainTargets["shop.example"] != "app:3000" || site.Meta.DomainEndpoint("shop.example") != "" {
		t.Errorf("a vanished target: stored %q, serving %q; want kept and serving nothing",
			site.Meta.DomainTargets["shop.example"], site.Meta.DomainEndpoint("shop.example"))
	}

	// A lost index entry is repaired by the next sync; one a crash left
	// behind for a label the site no longer holds resolves to nothing.
	os.Remove(filepath.Join(s.addressIndexDir(), second.Label))
	if err := s.SyncAddresses(site, site.Meta.Endpoints()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ByAddress(second.Label); err != nil {
		t.Errorf("lost index entry not repaired: %v", err)
	}
	stale := ids.New()
	if err := makeLink(filepath.Join(s.addressIndexDir(), stale), filepath.Join("..", sitesDirName, site.ViewID), site.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ByAddress(stale); !errors.Is(err, ErrNotFound) {
		t.Errorf("a stale index entry resolved: %v", err)
	}

	// Deleting the site removes its index entries.
	if err := s.Delete(site); err != nil {
		t.Fatal(err)
	}
	if linkExists(filepath.Join(s.addressIndexDir(), second.Label)) {
		t.Error("delete left an address index entry")
	}
}

func TestSyncAddressesIgnoresOtherModes(t *testing.T) {
	s, site := redirectSite(t)
	if err := s.SyncAddresses(site, []string{"app:3000"}); err != nil {
		t.Fatal(err)
	}
	if len(site.Meta.Addresses) != 0 {
		t.Errorf("a file site got addresses: %+v", site.Meta.Addresses)
	}
}

func TestClearAddresses(t *testing.T) {
	s, site := containerSite(t, "shop.example")
	if err := s.SyncAddresses(site, []string{"app:3000", "api:8080"}); err != nil {
		t.Fatal(err)
	}
	api := addressOf(t, site, "api:8080")
	if err := s.ClearAddresses(site); err != nil {
		t.Fatal(err)
	}
	if site.Meta.Addresses != nil || site.Meta.DomainTargets != nil {
		t.Errorf("left: %+v %+v", site.Meta.Addresses, site.Meta.DomainTargets)
	}
	if linkExists(filepath.Join(s.addressIndexDir(), api.Label)) {
		t.Error("index entry left")
	}
	if len(site.Meta.CustomDomains) != 1 {
		t.Error("leaving container mode detached a domain")
	}
}

func TestContainerAddressRules(t *testing.T) {
	s, site := containerSite(t, "a.example", "b.example")
	if err := s.SyncAddresses(site, []string{"app:3000", "api:8080"}); err != nil {
		t.Fatal(err)
	}
	api := addressOf(t, site, "api:8080")

	// Targets: the main port by default, any declared port by choice.
	if site.Meta.DomainTargets["b.example"] != "app:3000" {
		t.Fatalf("targets = %v", site.Meta.DomainTargets)
	}
	if err := s.SetDomainTarget(site, "B.example", "api:8080"); err != nil {
		t.Fatalf("SetDomainTarget: %v", err)
	}
	if site.Meta.DomainEndpoint("b.example") != "api:8080" {
		t.Errorf("b serves %q", site.Meta.DomainEndpoint("b.example"))
	}
	if err := s.SetDomainTarget(site, "b.example", "db:3306"); !errors.Is(err, ErrBadTarget) {
		t.Errorf("an undeclared port: %v, want ErrBadTarget", err)
	}
	if err := s.SetDomainTarget(site, "c.example", "app:3000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown domain: %v, want ErrNotFound", err)
	}

	// Off, per port: only while a domain serves the same port.
	if err := s.SetAddressRule(site, "", ViewOff, "", 0); err != nil {
		t.Fatalf("own address off while a serves app:3000: %v", err)
	}
	if rule := site.Meta.ViewRule(); rule.Mode != ViewOff {
		t.Errorf("ViewRule = %+v, want off", rule)
	}
	if err := s.SetAddressRule(site, "api:8080", ViewOff, "", 0); err != nil {
		t.Fatalf("api off while b serves it: %v", err)
	}
	if err := s.SetDomainTarget(site, "b.example", "app:3000"); err != nil {
		t.Fatal(err)
	}
	if rule := site.Meta.LabelRule(api.Label); rule.Mode != ViewServe {
		t.Errorf("api's off with no domain serving api:8080 = %+v, want serve", rule)
	}
	if err := s.SetAddressRule(site, "api:8080", ViewServe, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAddressRule(site, "api:8080", ViewOff, "", 0); !errors.Is(err, ErrBadRedirect) {
		t.Errorf("off with no domain serving the port: %v, want ErrBadRedirect", err)
	}
	if err := s.SetAddressRule(site, "db:3306", ViewOff, "", 0); !errors.Is(err, ErrBadTarget) {
		t.Errorf("an undeclared port's address: %v, want ErrBadTarget", err)
	}

	// Redirects: a generated address to a domain, a domain to a domain.
	if err := s.SetAddressRule(site, "api:8080", ViewRedirect, "a.example", 308); err != nil {
		t.Fatalf("api → a: %v", err)
	}
	if rule := site.Meta.LabelRule(api.Label); rule.Mode != ViewRedirect || rule.To != "a.example" || rule.Code != 308 {
		t.Errorf("api's rule = %+v", rule)
	}
	if err := s.SetDomainRedirect(site, "b.example", "a.example", 0); err != nil {
		t.Fatalf("b → a on a container site: %v", err)
	}
	if r, ok := site.Meta.DomainRedirect("b.example"); !ok || r.To != "a.example" {
		t.Errorf("DomainRedirect(b) = %+v %v", r, ok)
	}
	if err := s.SetDomainRedirect(site, "a.example", "b.example", 0); !errors.Is(err, ErrBadRedirect) {
		t.Errorf("a → b while b redirects and api's address points at a: %v", err)
	}
	if err := s.SetViewAddress(site, ViewOff, "", 0); !errors.Is(err, ErrBadRedirect) {
		t.Errorf("SetViewAddress on a container site: %v, want refused", err)
	}

	// The file drops app:3000: a serves nothing, so nothing redirects to it,
	// the own address's off is not justified — and that address went with
	// its port anyway.
	if err := s.SyncAddresses(site, []string{"api:8080"}); err != nil {
		t.Fatal(err)
	}
	if site.Meta.DomainEndpoint("a.example") != "" {
		t.Error("a still serves a dropped port")
	}
	if _, ok := site.Meta.DomainRedirect("b.example"); ok {
		t.Error("a redirect to a domain serving nothing is in effect")
	}
	if rule := site.Meta.LabelRule(api.Label); rule.Mode != ViewServe {
		t.Errorf("api → a, a serving nothing = %+v, want serve", rule)
	}
	if err := s.SetDomainRedirect(site, "b.example", "a.example", 0); !errors.Is(err, ErrBadRedirect) {
		t.Errorf("a redirect to a domain serving no port: %v, want refused", err)
	}
}

func TestRemoveDomainPrunesContainerRules(t *testing.T) {
	s, site := containerSite(t, "a.example", "b.example")
	if err := s.SyncAddresses(site, []string{"app:3000", "api:8080"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDomainTarget(site, "b.example", "api:8080"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAddressRule(site, "", ViewRedirect, "a.example", 301); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAddressRule(site, "api:8080", ViewOff, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveDomain(site, "a.example"); err != nil {
		t.Fatal(err)
	}
	if _, ok := site.Meta.DomainTargets["a.example"]; ok {
		t.Error("a removed domain kept its target")
	}
	if addressOf(t, site, "app:3000").Rule != nil {
		t.Error("a redirect to a removed domain stayed")
	}
	if addressOf(t, site, "api:8080").Rule == nil {
		t.Error("an off that b still justifies was dropped")
	}
	if err := s.RemoveDomain(site, "b.example"); err != nil {
		t.Fatal(err)
	}
	if addressOf(t, site, "api:8080").Rule != nil || site.Meta.DomainTargets != nil {
		t.Errorf("an off nothing justifies stayed: %+v, targets %v", site.Meta.Addresses, site.Meta.DomainTargets)
	}
}

func TestDefaultDomainTarget(t *testing.T) {
	s, site := containerSite(t)
	// A domain added before the app declares a port has no target …
	if err := s.AddDomain(site, "early.example"); err != nil {
		t.Fatal(err)
	}
	if err := s.DefaultDomainTarget(site, "early.example"); err != nil || site.Meta.DomainTargets["early.example"] != "" {
		t.Fatalf("target with no port declared: %v %v", err, site.Meta.DomainTargets)
	}
	// … until the first sync gives it the main port.
	if err := s.SyncAddresses(site, []string{"web:80", "app:3000"}); err != nil {
		t.Fatal(err)
	}
	if site.Meta.DomainTargets["early.example"] != "web:80" {
		t.Errorf("targets = %v", site.Meta.DomainTargets)
	}
	if err := s.AddDomain(site, "late.example"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDomainTarget(site, "late.example", "app:3000"); err != nil {
		t.Fatal(err)
	}
	if err := s.DefaultDomainTarget(site, "late.example"); err != nil || site.Meta.DomainTargets["late.example"] != "app:3000" {
		t.Errorf("a chosen target was replaced: %v %v", err, site.Meta.DomainTargets)
	}
}
