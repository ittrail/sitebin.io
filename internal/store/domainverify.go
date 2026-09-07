package store

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strings"
	"time"
)

// Custom-domain ownership.
//
// A domain is attached to a site — indexed, served, issued a certificate —
// only once its owner has proven control of it. Before that, first claim won:
// a stranger could add docs.customer.example to their own site today and be
// handed a valid certificate for it the moment the customer pointed DNS this
// way, with the customer told "already in use by another site".
//
// Proof is either of two DNS records, checked at the moment the domain is
// added and re-checked by the cleanup sweep:
//
//   - a TXT record at _sitebin-challenge.<domain> carrying
//     sitebin-verify=<token>, where the token is minted per site and per
//     domain and shown to the owner; or
//   - a CNAME at <domain> pointing at the site's own view host,
//     <view id>.<view domain> — which is also how the domain routes here, so
//     an owner who chose the CNAME route has nothing extra to create.
//
// A claim that has not verified is PENDING: recorded on the site so the
// records can be shown and re-checked, counted against the per-site cap so
// claims cannot be sprayed, but never indexed and never in CustomDomains.
// Nothing reads a pending claim as a reservation — the site that proves
// control gets the domain, whoever asked first.

var (
	// ErrDomainPending reports that the claim was recorded but the domain is
	// not attached yet: the DNS record it needs is not in place (or could not
	// be looked up). The caller shows the record and asks again later; the
	// sweep also asks.
	ErrDomainPending = errors.New("custom domain is pending verification")
)

const (
	// challengeLabel is the TXT record's owner name prefix.
	challengeLabel = "_sitebin-challenge."
	// challengePrefix prefixes the TXT record's value, so an unrelated TXT
	// record at the same name is never mistaken for proof.
	challengePrefix = "sitebin-verify="

	// pendingClaimTTL is how long a claim that never verifies is kept. Past
	// it the sweep drops the claim and the owner starts over: an abandoned
	// claim should not occupy a cap slot for ever.
	pendingClaimTTL = 7 * 24 * time.Hour
	// reverifyEvery is how often a VERIFIED domain's proof is looked up again.
	reverifyEvery = 24 * time.Hour
	// revokeAfter is how long a verified domain's proof has to be
	// definitively absent — not merely unreachable — before the domain is
	// detached. A DNS glitch lasts minutes; three days is a decision.
	revokeAfter = 72 * time.Hour
	// lookupTimeout bounds one DNS query.
	lookupTimeout = 5 * time.Second
)

// DomainClaim is one custom domain a site has asked for, verified or not.
// It lives in meta.json beside CustomDomains, which stays the list of
// VERIFIED domains and is what everything else reads. The invariant: a name in
// CustomDomains has a claim with VerifiedAt set, or predates claims entirely;
// a pending claim is never in CustomDomains.
type DomainClaim struct {
	Domain      string    `json:"domain"`
	Token       string    `json:"token"`
	RequestedAt time.Time `json:"requested_at"`
	// VerifiedAt is when the proof was last seen for the first time — set
	// on promotion, cleared on demotion.
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	// CheckedAt is the last time a lookup ran to completion, whatever it said.
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	// FailingSince is set when a verified domain's proof is first found
	// definitively absent, and cleared the moment it is seen again.
	FailingSince *time.Time `json:"failing_since,omitempty"`
}

// Verified reports whether the claim is attached.
func (c DomainClaim) Verified() bool { return c.VerifiedAt != nil }

// TXTName is the owner name of the TXT record that proves control.
func (c DomainClaim) TXTName() string { return challengeLabel + c.Domain }

// TXTValue is the value that TXT record must carry.
func (c DomainClaim) TXTValue() string { return challengePrefix + c.Token }

// DomainVerifier answers whether domain's DNS currently proves control for
// the site whose claim carries token and whose view host is viewHost (empty
// when the instance serves no view hosts, i.e. path-only). ok=false with a
// nil error is a definitive "the proof is not there"; an error means the
// question could not be answered, and callers must not act on it.
type DomainVerifier interface {
	Verify(ctx context.Context, domain, token, viewHost string) (ok bool, err error)
}

// TrustingVerifier attaches every domain unproven. It is SITEBIN_DOMAIN_VERIFICATION=off,
// for an instance whose every account holder is trusted — a team's own
// install — and for the e2e suite, where no DNS answers.
type TrustingVerifier struct{}

// Verify implements DomainVerifier.
func (TrustingVerifier) Verify(context.Context, string, string, string) (bool, error) {
	return true, nil
}

// DNSVerifier asks the system resolver.
type DNSVerifier struct {
	lookupTXT   func(ctx context.Context, name string) ([]string, error)
	lookupCNAME func(ctx context.Context, name string) (string, error)
}

// NewDNSVerifier builds the production verifier over net.DefaultResolver.
func NewDNSVerifier() *DNSVerifier {
	r := net.DefaultResolver
	return &DNSVerifier{lookupTXT: r.LookupTXT, lookupCNAME: r.LookupCNAME}
}

// Verify implements DomainVerifier. Each route is tried on its own; a lookup
// error on one does not hide a positive answer from the other, and only when
// neither answered positively does an error surface.
func (v *DNSVerifier) Verify(ctx context.Context, domain, token, viewHost string) (bool, error) {
	var lookupErr error

	tctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	txts, err := v.lookupTXT(tctx, challengeLabel+domain)
	cancel()
	switch {
	case err == nil:
		want := challengePrefix + token
		for _, t := range txts {
			if strings.TrimSpace(t) == want {
				return true, nil
			}
		}
	case !isNotFound(err):
		lookupErr = err
	}

	if viewHost != "" {
		cctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		cname, err := v.lookupCNAME(cctx, domain)
		cancel()
		switch {
		case err == nil:
			// LookupCNAME reports the name itself for a name with A records
			// and no CNAME, which is exactly not a CNAME at the view host.
			if strings.EqualFold(strings.TrimSuffix(cname, "."), viewHost) {
				return true, nil
			}
		case !isNotFound(err):
			lookupErr = err
		}
	}
	if lookupErr != nil {
		return false, lookupErr
	}
	return false, nil
}

// isNotFound tells "there is no such record" — an answer — from "the lookup
// failed" — the absence of one.
func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsNotFound
	}
	var nf interface{ NotFound() bool }
	return errors.As(err, &nf) && nf.NotFound()
}

// SetDomainVerifier installs the verifier and tells the store the view domain,
// from which a site's view host is derived for the CNAME route. viewDomain
// may be empty on a path-only instance. With no verifier installed nothing is
// ever attached, which is the safe default for a store nobody wired.
func (s *Store) SetDomainVerifier(v DomainVerifier, viewDomain string) {
	s.verifier = v
	s.viewDomain = strings.ToLower(strings.TrimSpace(viewDomain))
}

// viewHost is the host the site is served on for the CNAME route, or "".
func (s *Store) viewHost(site *Site) string {
	if s.viewDomain == "" {
		return ""
	}
	return site.ViewID + "." + s.viewDomain
}

// newClaimToken mints the per-claim secret: 20 random bytes, base32, which is
// what a person can type into a DNS console without a transcription error.
func newClaimToken() string {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("store: crypto/rand failed: " + err.Error())
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// verify asks the verifier about one claim. A store with no verifier answers
// "not proven", never an error, so a caller cannot mistake it for an outage.
func (s *Store) verify(ctx context.Context, site *Site, c DomainClaim) (bool, error) {
	if s.verifier == nil {
		return false, nil
	}
	return s.verifier.Verify(ctx, c.Domain, c.Token, s.viewHost(site))
}

// claimIndex finds the site's claim on d, or -1.
func claimIndex(m *Meta, d string) int {
	return slices.IndexFunc(m.DomainClaims, func(c DomainClaim) bool { return c.Domain == d })
}

// ReconcileDomains is the sweep's half of verification, run per site:
// pending claims are re-checked and attached when their proof has appeared,
// or dropped once they are older than pendingClaimTTL; verified claims are
// re-checked every reverifyEvery and detached only after their proof has
// been definitively absent for revokeAfter. A lookup error changes nothing —
// a site kept too long is recoverable, a detached domain is an outage.
//
// A verified domain with no claim record predates verification and is left
// alone: there is no token to check it against.
func (s *Store) ReconcileDomains(ctx context.Context, site *Site, now time.Time) error {
	// Read the claims under the lock, look them up outside it — DNS is slow
	// and the site lock serializes uploads — then apply each answer through
	// its own locked write.
	meta, err := readMeta(site.dir)
	if err != nil {
		return err
	}
	for _, c := range meta.DomainClaims {
		if !c.Verified() {
			if now.Sub(c.RequestedAt) > pendingClaimTTL {
				s.dropClaim(site, c.Domain)
				slog.Info("custom domain: dropped a claim that never verified", "site", site.ViewID, "domain", c.Domain)
				continue
			}
			if got, err := s.ByDomain(c.Domain); err == nil && got.ViewID != site.ViewID {
				// Another site proved control; this claim can never verify
				// and only occupies a cap slot.
				s.dropClaim(site, c.Domain)
				slog.Info("custom domain: dropped a claim on a domain another site verified", "site", site.ViewID, "domain", c.Domain)
				continue
			}
			ok, err := s.verify(ctx, site, c)
			if err != nil {
				slog.Warn("custom domain: verification lookup failed; will retry", "site", site.ViewID, "domain", c.Domain, "err", err)
				continue
			}
			if !ok {
				s.stampCheck(site, c.Domain, now)
				continue
			}
			switch err := s.attach(site, c.Domain, now); {
			case errors.Is(err, ErrDomainTaken):
				// Another site proved control in the meantime; this claim can
				// never verify and only occupies a cap slot.
				s.dropClaim(site, c.Domain)
				slog.Info("custom domain: dropped a claim on a domain another site verified", "site", site.ViewID, "domain", c.Domain)
			case err != nil:
				slog.Error("custom domain: could not attach a verified domain", "site", site.ViewID, "domain", c.Domain, "err", err)
			default:
				slog.Info("custom domain: verified and attached by the sweep", "site", site.ViewID, "domain", c.Domain)
			}
			continue
		}
		if c.CheckedAt != nil && now.Sub(*c.CheckedAt) < reverifyEvery {
			continue
		}
		ok, err := s.verify(ctx, site, c)
		if err != nil {
			slog.Warn("custom domain: re-verification lookup failed; leaving the domain attached", "site", site.ViewID, "domain", c.Domain, "err", err)
			continue
		}
		if ok {
			s.stampOK(site, c.Domain, now)
			continue
		}
		if c.FailingSince == nil {
			s.markFailing(site, c.Domain, now)
			slog.Warn("custom domain: proof of ownership is gone; the domain is detached if it stays gone", "site", site.ViewID, "domain", c.Domain, "after", revokeAfter)
			continue
		}
		if now.Sub(*c.FailingSince) >= revokeAfter {
			if err := s.detach(site, c.Domain, now); err != nil {
				slog.Error("custom domain: could not detach", "site", site.ViewID, "domain", c.Domain, "err", err)
				continue
			}
			slog.Warn("custom domain: detached; its proof of ownership has been gone for longer than the revocation window", "site", site.ViewID, "domain", c.Domain)
			continue
		}
		s.stampCheck(site, c.Domain, now)
	}
	return nil
}

// stampCheck records that a lookup ran to a definitive answer.
func (s *Store) stampCheck(site *Site, d string, now time.Time) {
	s.Update(site, func(m *Meta) error {
		if i := claimIndex(m, d); i >= 0 {
			t := now.UTC()
			m.DomainClaims[i].CheckedAt = &t
		}
		return nil
	})
}

// stampOK records a definitive positive answer, which also ends any
// revocation window that was running.
func (s *Store) stampOK(site *Site, d string, now time.Time) {
	s.Update(site, func(m *Meta) error {
		if i := claimIndex(m, d); i >= 0 {
			t := now.UTC()
			m.DomainClaims[i].CheckedAt = &t
			m.DomainClaims[i].FailingSince = nil
		}
		return nil
	})
}

// markFailing starts a verified domain's revocation window.
func (s *Store) markFailing(site *Site, d string, now time.Time) {
	s.Update(site, func(m *Meta) error {
		if i := claimIndex(m, d); i >= 0 {
			t := now.UTC()
			m.DomainClaims[i].CheckedAt = &t
			m.DomainClaims[i].FailingSince = &t
		}
		return nil
	})
}

// dropClaim forgets a pending claim.
func (s *Store) dropClaim(site *Site, d string) {
	s.Update(site, func(m *Meta) error {
		m.DomainClaims = slices.DeleteFunc(m.DomainClaims, func(c DomainClaim) bool { return c.Domain == d && !c.Verified() })
		return nil
	})
}

// detach turns a verified domain back into a pending claim: the index link
// goes, the name leaves CustomDomains, the token stays so the record the
// owner once created is still the one that would prove it again.
func (s *Store) detach(site *Site, d string, now time.Time) error {
	if err := s.removeLink(d); err != nil {
		return err
	}
	return s.Update(site, func(m *Meta) error {
		m.CustomDomains = slices.DeleteFunc(m.CustomDomains, func(x string) bool { return x == d })
		if i := claimIndex(m, d); i >= 0 {
			t := now.UTC()
			m.DomainClaims[i].VerifiedAt = nil
			m.DomainClaims[i].FailingSince = nil
			m.DomainClaims[i].CheckedAt = &t
			m.DomainClaims[i].RequestedAt = t
		}
		return nil
	})
}
