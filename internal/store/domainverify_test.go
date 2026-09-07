package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Custom-domain ownership. A domain is attached only once its owner has
// proven control of it -- a TXT record carrying the site's token, or a CNAME
// at the site's own view host -- so a stranger can no longer pre-claim
// docs.customer.com and be handed a certificate for it the moment the
// customer points DNS this way.

// scriptedVerifier answers per domain: true, false (definitively absent), or
// an error (the lookup itself failed).
type scriptedVerifier struct {
	answers map[string]bool
	errs    map[string]error
	calls   []string
	seen    map[string]string // domain -> token it was asked to look for
	hosts   map[string]string // domain -> view host it was told
}

func newScripted() *scriptedVerifier {
	return &scriptedVerifier{answers: map[string]bool{}, errs: map[string]error{}, seen: map[string]string{}, hosts: map[string]string{}}
}

func (v *scriptedVerifier) Verify(_ context.Context, domain, token, viewHost string) (bool, error) {
	v.calls = append(v.calls, domain)
	v.seen[domain] = token
	v.hosts[domain] = viewHost
	if err := v.errs[domain]; err != nil {
		return false, err
	}
	return v.answers[domain], nil
}

func verifiedStore(t *testing.T) (*Store, *scriptedVerifier) {
	t.Helper()
	s := newTestStore(t)
	v := newScripted()
	s.SetDomainVerifier(v, "sitebin.example")
	return s, v
}

func TestAddDomainIsPendingUntilVerified(t *testing.T) {
	s, v := verifiedStore(t)
	site, _, _ := s.Create()

	err := s.AddDomain(site, "Docs.Customer.Example")
	if !errors.Is(err, ErrDomainPending) {
		t.Fatalf("AddDomain before DNS is in place = %v, want ErrDomainPending", err)
	}
	if len(site.Meta.CustomDomains) != 0 {
		t.Errorf("an unverified domain was attached: %v", site.Meta.CustomDomains)
	}
	if _, err := s.ByDomain("docs.customer.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unverified domain resolves through the index: %v", err)
	}
	claims := site.Meta.DomainClaims
	if len(claims) != 1 || claims[0].Domain != "docs.customer.example" || claims[0].Token == "" || claims[0].VerifiedAt != nil {
		t.Fatalf("claim not recorded as pending: %+v", claims)
	}
	c := claims[0]
	if c.TXTName() != "_sitebin-challenge.docs.customer.example" || !strings.HasPrefix(c.TXTValue(), "sitebin-verify=") {
		t.Errorf("record instructions wrong: %q %q", c.TXTName(), c.TXTValue())
	}
	if v.seen["docs.customer.example"] != c.Token {
		t.Error("the verifier was not asked for the claim's own token")
	}
	if v.hosts["docs.customer.example"] != site.ViewID+".sitebin.example" {
		t.Errorf("the verifier was told view host %q", v.hosts["docs.customer.example"])
	}

	// Asking again re-checks with the SAME token, so the record the owner
	// already created stays valid.
	v.answers["docs.customer.example"] = true
	if err := s.AddDomain(site, "docs.customer.example"); err != nil {
		t.Fatalf("AddDomain once DNS is in place: %v", err)
	}
	if site.Meta.DomainClaims[0].Token != c.Token {
		t.Error("the token changed between attempts")
	}
	if got := site.Meta.CustomDomains; len(got) != 1 || got[0] != "docs.customer.example" {
		t.Errorf("domains after verification = %v", got)
	}
	if site.Meta.DomainClaims[0].VerifiedAt == nil {
		t.Error("claim not marked verified")
	}
	if got, err := s.ByDomain("docs.customer.example"); err != nil || got.ViewID != site.ViewID {
		t.Fatalf("verified domain does not resolve: %v", err)
	}
	// And a third call is the idempotent no-op it always was.
	if err := s.AddDomain(site, "docs.customer.example"); err != nil {
		t.Fatalf("re-adding a verified domain: %v", err)
	}
}

func TestAddDomainLookupErrorStaysPending(t *testing.T) {
	s, v := verifiedStore(t)
	site, _, _ := s.Create()
	v.errs["flaky.example.org"] = errors.New("dns timeout")
	if err := s.AddDomain(site, "flaky.example.org"); !errors.Is(err, ErrDomainPending) {
		t.Fatalf("a failed lookup must leave the claim pending, got %v", err)
	}
	if len(site.Meta.DomainClaims) != 1 {
		t.Fatal("claim not recorded")
	}
}

// A pending claim is not a reservation: the site that proves control gets the
// domain, whoever asked first.
func TestPendingClaimDoesNotReserveTheDomain(t *testing.T) {
	s, v := verifiedStore(t)
	attacker, _, _ := s.Create()
	victim, _, _ := s.Create()
	if err := s.AddDomain(attacker, "docs.acme.example"); !errors.Is(err, ErrDomainPending) {
		t.Fatal(err)
	}
	v.answers["docs.acme.example"] = true // the victim's DNS carries the victim's token
	if err := s.AddDomain(victim, "docs.acme.example"); err != nil {
		t.Fatalf("the rightful owner was refused: %v", err)
	}
	if got, _ := s.ByDomain("docs.acme.example"); got.ViewID != victim.ViewID {
		t.Fatal("the domain resolves to the wrong site")
	}
	// The attacker's claim can never verify now: the sweep drops it.
	v.answers["docs.acme.example"] = false
	if err := s.ReconcileDomains(context.Background(), attacker, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(attacker.Meta.DomainClaims) != 0 {
		t.Errorf("a claim on a domain another site verified survives: %+v", attacker.Meta.DomainClaims)
	}
}

func TestPendingClaimsCountAgainstTheCap(t *testing.T) {
	s, _ := verifiedStore(t)
	site, _, _ := s.Create()
	one := 1
	s.Update(site, func(m *Meta) error { m.QuotaDomains = &one; return nil })
	if err := s.AddDomain(site, "a.example.org"); !errors.Is(err, ErrDomainPending) {
		t.Fatal(err)
	}
	if err := s.AddDomain(site, "b.example.org"); !errors.Is(err, ErrTooManyDomain) {
		t.Fatalf("a second pending claim past the cap = %v, want ErrTooManyDomain", err)
	}
}

func TestRemoveDomainDropsAPendingClaim(t *testing.T) {
	s, _ := verifiedStore(t)
	site, _, _ := s.Create()
	s.AddDomain(site, "gone.example.org")
	if err := s.RemoveDomain(site, "gone.example.org"); err != nil {
		t.Fatal(err)
	}
	if len(site.Meta.DomainClaims) != 0 {
		t.Errorf("claim survives removal: %+v", site.Meta.DomainClaims)
	}
	if err := s.RemoveDomain(site, "gone.example.org"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing nothing = %v", err)
	}
}

func TestReconcileDomainsPromotesPendingClaims(t *testing.T) {
	s, v := verifiedStore(t)
	site, _, _ := s.Create()
	s.AddDomain(site, "later.example.org")
	now := time.Now()

	// still absent: nothing changes, the check is stamped
	if err := s.ReconcileDomains(context.Background(), site, now); err != nil {
		t.Fatal(err)
	}
	if len(site.Meta.CustomDomains) != 0 {
		t.Fatal("promoted without proof")
	}
	// the record appears: the sweep attaches the domain
	v.answers["later.example.org"] = true
	if err := s.ReconcileDomains(context.Background(), site, now); err != nil {
		t.Fatal(err)
	}
	if got := site.Meta.CustomDomains; len(got) != 1 || got[0] != "later.example.org" {
		t.Fatalf("not promoted: %v", got)
	}
	if _, err := s.ByDomain("later.example.org"); err != nil {
		t.Fatal("promoted domain is not indexed")
	}
}

func TestReconcileDomainsExpiresStaleClaims(t *testing.T) {
	s, _ := verifiedStore(t)
	site, _, _ := s.Create()
	s.AddDomain(site, "forgotten.example.org")
	old := time.Now().Add(pendingClaimTTL + time.Hour)
	if err := s.ReconcileDomains(context.Background(), site, old); err != nil {
		t.Fatal(err)
	}
	if len(site.Meta.DomainClaims) != 0 {
		t.Errorf("a claim older than %v survives: %+v", pendingClaimTTL, site.Meta.DomainClaims)
	}
}

// A verified domain is re-checked, but only a failure that PERSISTS for the
// revocation window detaches it, and a lookup error never does: a DNS glitch
// must not take a customer's domain down.
func TestReconcileDomainsReverifiesAndDetachesAfterPersistentFailure(t *testing.T) {
	s, v := verifiedStore(t)
	site, _, _ := s.Create()
	v.answers["live.example.org"] = true
	if err := s.AddDomain(site, "live.example.org"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()

	// inside the re-check interval: no lookup at all
	v.calls = nil
	s.ReconcileDomains(context.Background(), site, t0.Add(time.Hour))
	if len(v.calls) != 0 {
		t.Errorf("a freshly verified domain was re-checked after an hour: %v", v.calls)
	}

	// a lookup error: untouched, not even marked failing
	v.errs["live.example.org"] = errors.New("servfail")
	s.ReconcileDomains(context.Background(), site, t0.Add(reverifyEvery+time.Hour))
	if c := site.Meta.DomainClaims[0]; c.FailingSince != nil || len(site.Meta.CustomDomains) != 1 {
		t.Fatalf("a lookup error changed the claim: %+v", c)
	}
	delete(v.errs, "live.example.org")

	// definitively gone: marked failing, still attached
	v.answers["live.example.org"] = false
	t1 := t0.Add(reverifyEvery + 2*time.Hour)
	s.ReconcileDomains(context.Background(), site, t1)
	if c := site.Meta.DomainClaims[0]; c.FailingSince == nil || len(site.Meta.CustomDomains) != 1 {
		t.Fatalf("first failure did not start the window, or detached at once: %+v", c)
	}
	// comes back within the window: failure cleared
	v.answers["live.example.org"] = true
	s.ReconcileDomains(context.Background(), site, t1.Add(reverifyEvery+time.Hour))
	if c := site.Meta.DomainClaims[0]; c.FailingSince != nil {
		t.Fatalf("a recovered domain is still marked failing: %+v", c)
	}
	// gone for longer than the window: detached and back to pending
	v.answers["live.example.org"] = false
	t2 := t1.Add(3 * reverifyEvery)
	s.ReconcileDomains(context.Background(), site, t2)
	s.ReconcileDomains(context.Background(), site, t2.Add(revokeAfter+reverifyEvery))
	if len(site.Meta.CustomDomains) != 0 {
		t.Fatalf("a domain whose proof has been gone for %v is still attached", revokeAfter)
	}
	if _, err := s.ByDomain("live.example.org"); !errors.Is(err, ErrNotFound) {
		t.Error("detached domain still indexed")
	}
	if c := site.Meta.DomainClaims[0]; c.VerifiedAt != nil || c.Token == "" {
		t.Errorf("detached claim should be pending again with its token: %+v", c)
	}
}

// A domain attached before verification existed has no claim record and is
// left exactly as it is: there is no token to check it against, and the
// operator's own sites are among them.
func TestReconcileDomainsLeavesClaimlessDomainsAlone(t *testing.T) {
	s, v := verifiedStore(t)
	site, _, _ := s.Create()
	if err := s.Update(site, func(m *Meta) error { m.CustomDomains = []string{"legacy.example.org"}; return nil }); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(s.domainIndexDir(), "legacy.example.org")
	if err := makeLink(link, filepath.Join("..", sitesDirName, site.ViewID), site.Dir()); err != nil {
		t.Fatal(err)
	}
	v.answers["legacy.example.org"] = false
	for i := 0; i < 5; i++ {
		s.ReconcileDomains(context.Background(), site, time.Now().Add(time.Duration(i)*revokeAfter))
	}
	if len(site.Meta.CustomDomains) != 1 {
		t.Fatal("a claimless domain was detached")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal("its index link was removed")
	}
	if len(v.calls) != 0 {
		t.Errorf("a claimless domain was looked up: %v", v.calls)
	}
}

// With no verifier configured nothing can ever be proven, so nothing is ever
// attached: the safe default for a store somebody forgot to wire.
func TestNoVerifierMeansNothingAttaches(t *testing.T) {
	s, err := New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	site, _, _ := s.Create()
	if err := s.AddDomain(site, "x.example.org"); !errors.Is(err, ErrDomainPending) {
		t.Fatalf("AddDomain with no verifier = %v", err)
	}
}

func TestTrustingVerifierAttachesAtOnce(t *testing.T) {
	s := newTestStore(t)
	s.SetDomainVerifier(TrustingVerifier{}, "sitebin.example")
	site, _, _ := s.Create()
	if err := s.AddDomain(site, "x.example.org"); err != nil {
		t.Fatal(err)
	}
	if len(site.Meta.CustomDomains) != 1 || site.Meta.DomainClaims[0].VerifiedAt == nil {
		t.Fatal("not attached")
	}
}

// The DNS verifier's decision, with the resolver replaced.
func TestDNSVerifierDecision(t *testing.T) {
	notFound := &dnsNotFound{}
	cases := []struct {
		name    string
		txt     []string
		txtErr  error
		cname   string
		cnErr   error
		want    bool
		wantErr bool
	}{
		{"txt matches", []string{"other", "sitebin-verify=tok"}, nil, "", notFound, true, false},
		{"txt wrong token", []string{"sitebin-verify=nope"}, nil, "", notFound, false, false},
		{"cname at the view host", nil, notFound, "abc.sitebin.example.", nil, true, false},
		{"cname elsewhere", nil, notFound, "cdn.example.net.", nil, false, false},
		{"a-record only reports itself", nil, notFound, "docs.example.org.", nil, false, false},
		{"nothing at all", nil, notFound, "", notFound, false, false},
		{"txt lookup fails", nil, errors.New("timeout"), "", notFound, false, true},
		{"cname lookup fails but txt matches", []string{"sitebin-verify=tok"}, nil, "", errors.New("timeout"), true, false},
		{"both fail", nil, errors.New("timeout"), "", errors.New("timeout"), false, true},
	}
	for _, tc := range cases {
		v := &DNSVerifier{
			lookupTXT:   func(context.Context, string) ([]string, error) { return tc.txt, tc.txtErr },
			lookupCNAME: func(context.Context, string) (string, error) { return tc.cname, tc.cnErr },
		}
		got, err := v.Verify(context.Background(), "docs.example.org", "tok", "abc.sitebin.example")
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s: got %v, %v; want %v, err=%v", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
	// no view host (path-only instances): the CNAME route is off
	v := &DNSVerifier{
		lookupTXT:   func(context.Context, string) ([]string, error) { return nil, notFound },
		lookupCNAME: func(context.Context, string) (string, error) { return "abc.sitebin.example.", nil },
	}
	if ok, _ := v.Verify(context.Background(), "docs.example.org", "tok", ""); ok {
		t.Error("a CNAME verified against an empty view host")
	}
}

// dnsNotFound stands in for the resolver's "no such record" answer.
type dnsNotFound struct{}

func (*dnsNotFound) Error() string  { return "no such host" }
func (*dnsNotFound) NotFound() bool { return true }
