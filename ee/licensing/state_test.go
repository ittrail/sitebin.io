//go:build ee

package licensing

import (
	"errors"
	"testing"
	"time"
)

// snapWith builds a loaded snapshot around a licence with the given dates.
func snapWith(expires, grace time.Time) Snapshot {
	return Snapshot{
		Loaded: true,
		License: &License{
			Cert: CertPayload{AppID: AppID, ExpiresAt: refTime.Add(10 * 365 * 24 * time.Hour)},
			Lic:  LicensePayload{AppID: AppID, Holder: "ACME GmbH", Plan: "team", ExpiresAt: expires, GraceUntil: grace},
		},
		TrialStart: refTime.Add(-time.Hour),
		Source:     "env",
	}
}

func TestStateBoundaries(t *testing.T) {
	expires := refTime
	grace := refTime.Add(90 * 24 * time.Hour)
	s := snapWith(expires, grace)

	cases := []struct {
		name       string
		at         time.Time
		want       State
		restricted bool
	}{
		{"well before expiry", expires.Add(-30 * 24 * time.Hour), StateLicensed, false},
		{"exactly at expiry", expires, StateLicensed, false},
		{"one second past expiry", expires.Add(time.Second), StateGrace, false},
		{"exactly at grace end", grace, StateGrace, false},
		{"one second past grace", grace.Add(time.Second), StateExpired, true},
		{"long past grace", grace.Add(365 * 24 * time.Hour), StateExpired, true},
	}
	for _, c := range cases {
		got := s.StatusAt(c.at)
		if got.State != c.want || got.Restricted != c.restricted {
			t.Errorf("%s: state=%s restricted=%v, want %s/%v", c.name, got.State, got.Restricted, c.want, c.restricted)
		}
	}
}

// A licence with no grace_until must not gain one, and must not have its expiry
// pushed out either.
func TestNoGraceMeansExpiryIsTheEnd(t *testing.T) {
	s := snapWith(refTime, time.Time{})
	if got := s.StatusAt(refTime.Add(time.Second)); got.State != StateExpired {
		t.Fatalf("state = %s, want expired", got.State)
	}
}

// A grace_until BEFORE expires_at is a stack bug; it must never pull the expiry
// forward.
func TestGraceNeverPullsExpiryForward(t *testing.T) {
	s := snapWith(refTime.Add(24*time.Hour), refTime.Add(-24*time.Hour))
	if got := s.StatusAt(refTime); got.State != StateLicensed {
		t.Fatalf("state = %s, want licensed", got.State)
	}
}

func TestPerpetualLicenseNeverExpires(t *testing.T) {
	s := snapWith(time.Time{}, time.Time{})
	s.License.Cert.ExpiresAt = time.Time{} // a certificate with no expiry either
	if got := s.StatusAt(refTime.Add(100 * 365 * 24 * time.Hour)); got.State != StateLicensed {
		t.Fatalf("state = %s, want licensed", got.State)
	}
}

// The rule that matters most: a key that cannot be verified is "none", with a
// trial, and NEVER "expired". A configuration mistake must not punish harder
// than having no licence at all.
func TestUnverifiableKeyIsNoneNotExpired(t *testing.T) {
	s := Snapshot{Loaded: true, Err: ErrCertSig, Source: "env", TrialStart: refTime}
	got := s.StatusAt(refTime.Add(time.Hour))
	if got.State != StateNone {
		t.Fatalf("state = %s, want none", got.State)
	}
	if got.Restricted {
		t.Error("an unverifiable key restricted creation while still inside the trial")
	}
	if !errors.Is(got.Err, ErrCertSig) {
		t.Errorf("the reason was lost: %v", got.Err)
	}
}

func TestTrialBoundaries(t *testing.T) {
	start := refTime
	s := Snapshot{Loaded: true, TrialStart: start}
	end := start.Add(TrialPeriod)

	if got := s.StatusAt(start); got.State != StateNone || got.Restricted {
		t.Errorf("day 0: %s restricted=%v, want none/false", got.State, got.Restricted)
	}
	if got := s.StatusAt(end); got.Restricted {
		t.Error("the last instant of the trial restricted creation")
	}
	if got := s.StatusAt(end.Add(time.Second)); !got.Restricted || got.State != StateNone {
		t.Errorf("past the trial: %s restricted=%v, want none/true", got.State, got.Restricted)
	}
}

// Unknown is not expired, twice over: before anything is loaded, and when the
// trial marker could not be read.
func TestUnknownNeverRestricts(t *testing.T) {
	if got := (Snapshot{}).StatusAt(refTime); got.State != StateUnknown || got.Restricted {
		t.Errorf("unloaded: %s restricted=%v", got.State, got.Restricted)
	}
	noMarker := Snapshot{Loaded: true} // trial start unknown
	if got := noMarker.StatusAt(refTime.Add(10 * 365 * 24 * time.Hour)); got.State != StateUnknown || got.Restricted {
		t.Errorf("no trial marker: %s restricted=%v", got.State, got.Restricted)
	}
}

// A certificate that expires while the process runs stops vouching for the
// licence — which is unverifiable, so "none", not "expired".
func TestExpiredCertFallsBackToTheTrialArm(t *testing.T) {
	s := snapWith(refTime.Add(365*24*time.Hour), refTime.Add(400*24*time.Hour))
	s.License.Cert.ExpiresAt = refTime
	s.TrialStart = refTime.Add(-TrialPeriod - time.Hour) // trial long gone
	got := s.StatusAt(refTime.Add(time.Hour))
	if got.State != StateNone {
		t.Fatalf("state = %s, want none", got.State)
	}
	if got.Holder != "" {
		t.Error("an expired certificate still reported its licence holder")
	}
}

// The notice appears from expires_at onward, and never before it.
func TestNoticeAppearsFromExpiryOnward(t *testing.T) {
	s := snapWith(refTime, refTime.Add(90*24*time.Hour))
	if n := s.StatusAt(refTime.Add(-time.Hour)).Notice(); n != "" {
		t.Errorf("a licensed instance showed a notice: %q", n)
	}
	if n := s.StatusAt(refTime.Add(time.Hour)).Notice(); n == "" {
		t.Error("grace showed no notice")
	}
	if n := s.StatusAt(refTime.Add(200 * 24 * time.Hour)).Notice(); n == "" {
		t.Error("expired showed no notice")
	}
	// An elapsed trial blocks creation, so it says so too.
	trial := Snapshot{Loaded: true, TrialStart: refTime}
	if n := trial.StatusAt(refTime.Add(TrialPeriod + time.Hour)).Notice(); n == "" {
		t.Error("an elapsed trial blocks creation but showed no notice")
	}
	if n := trial.StatusAt(refTime.Add(time.Hour)).Notice(); n != "" {
		t.Errorf("a running trial showed a notice: %q", n)
	}
}

// ---- entitlements ----

func TestDomainEntitlementIsACeiling(t *testing.T) {
	s := snapWith(refTime.Add(365*24*time.Hour), time.Time{})
	s.License.Lic.Entitlements.MaxCustomDomains = 25
	st := s.StatusAt(refTime)

	if !st.DomainsAllowed(24) {
		t.Error("the 25th domain was refused")
	}
	if st.DomainsAllowed(25) {
		t.Error("the 26th domain was allowed past a cap of 25")
	}
	// It never removes anything: an instance already over the cap (a downgrade,
	// or a cap introduced later) keeps what it has and is only refused the next.
	if st.DomainsAllowed(40) {
		t.Error("an instance over its cap was allowed to add another")
	}
	// Counting failed = unknown = never restrictive.
	if !st.DomainsAllowed(-1) {
		t.Error("an uncountable instance was restricted")
	}
}

// Absent or zero entitlements mean UNLIMITED, so a licence issued before a
// limit existed is not retroactively restrictive.
func TestAbsentEntitlementsMeanUnlimited(t *testing.T) {
	s := snapWith(refTime.Add(365*24*time.Hour), time.Time{})
	st := s.StatusAt(refTime)
	if st.Entitlements.MaxCustomDomains != 0 {
		t.Fatalf("max_custom_domains = %d, want 0", st.Entitlements.MaxCustomDomains)
	}
	if !st.DomainsAllowed(10_000) {
		t.Error("a licence with no entitlement restricted domains")
	}
	// And so do the unlicensed and unknown states.
	for _, snap := range []Snapshot{{}, {Loaded: true, TrialStart: refTime}} {
		if !snap.StatusAt(refTime).DomainsAllowed(10_000) {
			t.Error("an unlicensed/unknown instance restricted domains")
		}
	}
}

// Entitlements survive into grace — service continues, so the ceiling does too
// — and are dropped entirely when the certificate stops vouching.
func TestEntitlementsInGraceAndAfterCertExpiry(t *testing.T) {
	s := snapWith(refTime, refTime.Add(90*24*time.Hour))
	s.License.Lic.Entitlements.MaxCustomDomains = 3
	if got := s.StatusAt(refTime.Add(time.Hour)); got.State != StateGrace || got.Entitlements.MaxCustomDomains != 3 {
		t.Errorf("grace: state=%s cap=%d", got.State, got.Entitlements.MaxCustomDomains)
	}
	s.License.Cert.ExpiresAt = refTime
	if got := s.StatusAt(refTime.Add(time.Hour)); got.Entitlements.MaxCustomDomains != 0 {
		t.Errorf("an unvouched licence kept its cap: %d", got.Entitlements.MaxCustomDomains)
	}
}
