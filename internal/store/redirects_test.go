package store

import (
	"errors"
	"testing"
)

// Domain redirects and the generated address. A verified custom domain can
// redirect to another verified domain of the same site, and the site's
// generated address can be switched off or redirected — but never in a way
// that leaves the site with no address that serves it.

// redirectSite is a site with the given domains attached.
func redirectSite(t *testing.T, domains ...string) (*Store, *Site) {
	t.Helper()
	s := newTestStore(t)
	s.SetDomainVerifier(TrustingVerifier{}, "sitebin.example")
	site, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range domains {
		if err := s.AddDomain(site, d); err != nil {
			t.Fatalf("attach %s: %v", d, err)
		}
	}
	return s, site
}

func TestSetDomainRedirect(t *testing.T) {
	s, site := redirectSite(t, "morgenfertig.at", "morgenfertig.de")

	if err := s.SetDomainRedirect(site, "Morgenfertig.DE", "morgenfertig.at.", 0); err != nil {
		t.Fatalf("SetDomainRedirect: %v", err)
	}
	got, ok := site.Meta.DomainRedirects["morgenfertig.de"]
	if !ok || got.To != "morgenfertig.at" || got.Code != 301 {
		t.Fatalf("stored rule = %+v (ok %v), want → morgenfertig.at 301 (the default)", got, ok)
	}
	r, ok := site.Meta.DomainRedirect("morgenfertig.de")
	if !ok || r.To != "morgenfertig.at" || r.Code != 301 {
		t.Errorf("DomainRedirect = %+v %v, want the stored rule in effect", r, ok)
	}
	if _, ok := site.Meta.DomainRedirect("morgenfertig.at"); ok {
		t.Error("the target redirects too")
	}

	if err := s.SetDomainRedirect(site, "morgenfertig.de", "morgenfertig.at", 308); err != nil {
		t.Fatalf("change the code: %v", err)
	}
	if site.Meta.DomainRedirects["morgenfertig.de"].Code != 308 {
		t.Errorf("code not changed: %+v", site.Meta.DomainRedirects)
	}

	// Reloaded from disk, the rule is still there.
	again, err := s.ByViewID(site.ViewID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Meta.DomainRedirects["morgenfertig.de"].To != "morgenfertig.at" {
		t.Errorf("rule not persisted: %+v", again.Meta.DomainRedirects)
	}

	// An empty target makes the domain serve the site again.
	if err := s.SetDomainRedirect(site, "morgenfertig.de", "", 0); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if len(site.Meta.DomainRedirects) != 0 {
		t.Errorf("rule not cleared: %+v", site.Meta.DomainRedirects)
	}
	if err := s.SetDomainRedirect(site, "morgenfertig.de", "", 0); err != nil {
		t.Errorf("clearing a domain without a rule must be a no-op: %v", err)
	}
}

func TestSetDomainRedirectRefusals(t *testing.T) {
	s, site := redirectSite(t, "a.example", "b.example", "c.example")
	v := newScripted()
	s.SetDomainVerifier(v, "sitebin.example")
	if err := s.AddDomain(site, "pending.example"); !errors.Is(err, ErrDomainPending) {
		t.Fatalf("pending claim: %v", err)
	}
	other, _, _ := s.Create()
	s.SetDomainVerifier(TrustingVerifier{}, "sitebin.example")
	if err := s.AddDomain(other, "foreign.example"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, domain, to string
		code             int
		want             error
	}{
		{"unknown domain", "nope.example", "a.example", 301, ErrNotFound},
		{"pending domain", "pending.example", "a.example", 301, ErrNotFound},
		{"to itself", "a.example", "a.example", 301, ErrBadRedirect},
		{"to a pending claim", "a.example", "pending.example", 301, ErrBadRedirect},
		{"to another site's domain", "a.example", "foreign.example", 301, ErrBadRedirect},
		{"to a URL", "a.example", "https://b.example/x", 301, ErrBadRedirect},
		{"bad code", "a.example", "b.example", 303, ErrBadRedirect},
		{"bad code 200", "a.example", "b.example", 200, ErrBadRedirect},
	}
	for _, c := range cases {
		if err := s.SetDomainRedirect(site, c.domain, c.to, c.code); !errors.Is(err, c.want) {
			t.Errorf("%s: SetDomainRedirect(%q → %q, %d) = %v, want %v", c.name, c.domain, c.to, c.code, err, c.want)
		}
	}
	if len(site.Meta.DomainRedirects) != 0 {
		t.Fatalf("a refused rule was stored: %+v", site.Meta.DomainRedirects)
	}

	// No chains: b → a stands, so a cannot redirect, and nothing can point at b.
	if err := s.SetDomainRedirect(site, "b.example", "a.example", 301); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDomainRedirect(site, "a.example", "c.example", 301); !errors.Is(err, ErrBadRedirect) {
		t.Errorf("a target was made a redirect (chain b → a → c): %v", err)
	}
	if err := s.SetDomainRedirect(site, "c.example", "b.example", 301); !errors.Is(err, ErrBadRedirect) {
		t.Errorf("a redirect was made a target (chain c → b → a): %v", err)
	}
	// Changing b's own target is not a chain.
	if err := s.SetDomainRedirect(site, "b.example", "c.example", 302); err != nil {
		t.Errorf("re-pointing a redirect: %v", err)
	}

	// The generated address's target is a target too.
	if err := s.SetViewAddress(site, ViewRedirect, "a.example", 301); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDomainRedirect(site, "a.example", "c.example", 301); !errors.Is(err, ErrBadRedirect) {
		t.Errorf("the generated address's target was made a redirect: %v", err)
	}
}

func TestSetViewAddress(t *testing.T) {
	s, bare := redirectSite(t)
	if err := s.SetViewAddress(bare, ViewOff, "", 0); !errors.Is(err, ErrBadRedirect) {
		t.Errorf("off on a site without a custom domain = %v, want ErrBadRedirect: nothing would serve it", err)
	}

	s, site := redirectSite(t, "a.example", "b.example")
	if err := s.SetViewAddress(site, ViewOff, "", 0); err != nil {
		t.Fatalf("off: %v", err)
	}
	if va := site.Meta.ViewAddress; va == nil || va.Mode != ViewOff {
		t.Fatalf("stored = %+v, want off", va)
	}
	if rule := site.Meta.ViewRule(); rule.Mode != ViewOff {
		t.Errorf("ViewRule = %+v, want off in effect", rule)
	}

	if err := s.SetViewAddress(site, ViewRedirect, "b.example", 0); err != nil {
		t.Fatalf("redirect: %v", err)
	}
	if rule := site.Meta.ViewRule(); rule.Mode != ViewRedirect || rule.To != "b.example" || rule.Code != 301 {
		t.Errorf("ViewRule = %+v, want redirect → b.example 301", rule)
	}

	if err := s.SetViewAddress(site, ViewServe, "", 0); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if site.Meta.ViewAddress != nil {
		t.Errorf("serve must clear the stored rule: %+v", site.Meta.ViewAddress)
	}
	if rule := site.Meta.ViewRule(); rule.Mode != ViewServe {
		t.Errorf("ViewRule = %+v, want serve", rule)
	}

	for _, c := range []struct {
		name, mode, to string
		code           int
	}{
		{"unknown mode", "hide", "", 0},
		{"redirect without target", ViewRedirect, "", 301},
		{"redirect to a stranger", ViewRedirect, "elsewhere.example", 301},
		{"redirect with a bad code", ViewRedirect, "a.example", 304},
	} {
		if err := s.SetViewAddress(site, c.mode, c.to, c.code); !errors.Is(err, ErrBadRedirect) {
			t.Errorf("%s: SetViewAddress = %v, want ErrBadRedirect", c.name, err)
		}
	}

	// A redirecting domain is not a target, and it does not count as a
	// domain that serves.
	if err := s.SetDomainRedirect(site, "b.example", "a.example", 301); err != nil {
		t.Fatal(err)
	}
	if err := s.SetViewAddress(site, ViewRedirect, "b.example", 301); !errors.Is(err, ErrBadRedirect) {
		t.Errorf("the generated address was pointed at a redirect: %v", err)
	}
	if got := site.Meta.ServingDomains(); len(got) != 1 || got[0] != "a.example" {
		t.Errorf("ServingDomains = %v, want [a.example]", got)
	}
}

// Removing a domain is the owner's decision, and the rules follow it.
func TestRemoveDomainPrunesAddressRules(t *testing.T) {
	s, site := redirectSite(t, "a.example", "b.example", "c.example")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetDomainRedirect(site, "b.example", "a.example", 301))
	must(s.SetDomainRedirect(site, "c.example", "a.example", 302))
	must(s.SetViewAddress(site, ViewRedirect, "a.example", 308))

	must(s.RemoveDomain(site, "a.example"))
	if len(site.Meta.DomainRedirects) != 0 {
		t.Errorf("redirects to a removed domain survive: %+v", site.Meta.DomainRedirects)
	}
	if site.Meta.ViewAddress != nil {
		t.Errorf("the generated address still redirects to a removed domain: %+v", site.Meta.ViewAddress)
	}

	must(s.SetDomainRedirect(site, "c.example", "b.example", 301))
	must(s.RemoveDomain(site, "c.example"))
	if len(site.Meta.DomainRedirects) != 0 {
		t.Errorf("a removed domain's own redirect survives: %+v", site.Meta.DomainRedirects)
	}

	// off survives while a domain serves, and goes with the last one.
	must(s.SetViewAddress(site, ViewOff, "", 0))
	must(s.RemoveDomain(site, "b.example"))
	if site.Meta.ViewAddress != nil {
		t.Errorf("off survives the last serving domain: %+v", site.Meta.ViewAddress)
	}
}

// What is in effect is decided against the domains attached right now: the
// sweep detaches a domain without asking anyone, and a rule that can no longer
// be followed must fall back to serving the site, never to a dead end.
func TestAddressRulesInEffect(t *testing.T) {
	m := Meta{
		CustomDomains:   []string{"a.example", "b.example"},
		DomainRedirects: map[string]Redirect{"b.example": {To: "a.example", Code: 301}, "gone.example": {To: "a.example", Code: 301}},
		ViewAddress:     &ViewAddress{Mode: ViewOff},
	}
	if r, ok := m.DomainRedirect("B.Example"); !ok || r.To != "a.example" {
		t.Errorf("DomainRedirect(b) = %+v %v", r, ok)
	}
	if _, ok := m.DomainRedirect("gone.example"); ok {
		t.Error("a detached domain's rule is in effect")
	}
	if rule := m.ViewRule(); rule.Mode != ViewOff {
		t.Errorf("ViewRule = %+v, want off", rule)
	}

	// a.example detached by the sweep: b serves again, and so does the
	// generated address while nothing else serves.
	m.CustomDomains = []string{"b.example"}
	if _, ok := m.DomainRedirect("b.example"); ok {
		t.Error("a redirect to a detached domain is in effect")
	}
	if got := m.ServingDomains(); len(got) != 1 || got[0] != "b.example" {
		t.Errorf("ServingDomains = %v, want [b.example]", got)
	}
	if rule := m.ViewRule(); rule.Mode != ViewOff {
		t.Errorf("off must hold while b serves: %+v", rule)
	}
	m.CustomDomains = []string{}
	if rule := m.ViewRule(); rule.Mode != ViewServe {
		t.Errorf("off with no custom domain serving = %+v, want serve", rule)
	}

	m.CustomDomains = []string{"a.example", "b.example"}
	m.ViewAddress = &ViewAddress{Mode: ViewRedirect, To: "b.example", Code: 301}
	if rule := m.ViewRule(); rule.Mode != ViewServe {
		t.Errorf("a generated-address redirect to a redirecting domain = %+v, want serve", rule)
	}

	// On a container site a domain serves only a port its compose file
	// declares, so nothing redirects to one without; and its own address
	// follows its entry in Addresses, never ViewAddress.
	m.Mode, m.ID = ModeContainer, "cccccccccccccccccccccccccc"
	m.ViewAddress = &ViewAddress{Mode: ViewOff}
	if _, ok := m.DomainRedirect("b.example"); ok {
		t.Error("a redirect to a domain that serves no port is in effect")
	}
	if rule := m.ViewRule(); rule.Mode != ViewServe {
		t.Errorf("a container site's ViewAddress = %+v, want it ignored", rule)
	}
	m.Addresses = []Address{{Endpoint: "app:3000", Label: m.ID}}
	m.DomainTargets = map[string]string{"a.example": "app:3000"}
	if r, ok := m.DomainRedirect("b.example"); !ok || r.To != "a.example" {
		t.Errorf("container DomainRedirect(b) = %+v %v, want → a.example", r, ok)
	}
}
