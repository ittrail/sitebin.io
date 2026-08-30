//go:build ee

package licensing

import (
	"fmt"
	"time"
)

// TrialPeriod is how long an instance with no licence at all behaves as
// licensed, counted from the timestamp written on its first start.
const TrialPeriod = 90 * 24 * time.Hour

// State is what this instance's licence is doing right now.
type State string

const (
	// StateLicensed: a verified licence, now <= expires_at. Nothing happens.
	StateLicensed State = "licensed"
	// StateGrace: expires_at < now <= grace_until. Loud, permanent notice in
	// the account UI; nothing is restricted.
	StateGrace State = "grace"
	// StateExpired: now > grace_until. Notice, and no new sites or drops.
	StateExpired State = "expired"
	// StateNone: no key, or a key that could not be verified. Behaves as
	// licensed for the trial, then as expired. A malformed or unverifiable key
	// lands HERE and never in StateExpired: a configuration mistake must not be
	// more punishing than no licence at all.
	StateNone State = "none"
	// StateUnknown: the state could not be determined — nothing has loaded yet,
	// or the trial marker could not be read. Nothing is ever restricted on it.
	// Same instinct as the cleanup sweep: a site not created is an annoyance, a
	// customer wrongly blocked is a broken product.
	StateUnknown State = "unknown"
)

// Snapshot is everything the instance KNOWS about its licence — the facts,
// with no time in them. The state is derived from a snapshot at a given
// instant by StatusAt, so a licence sliding from licensed into grace into
// expired needs no reload, no ticker and no restart.
type Snapshot struct {
	// Loaded is false until the manager has looked. Before that the state is
	// unknown, not "none".
	Loaded bool
	// License is the verified chain, or nil when there was no key or it did
	// not verify.
	License *License
	// Err says why a supplied key was rejected; nil when none was supplied.
	// It is reported, never enforced on.
	Err error
	// Source names where the key came from, for logs and the dashboard:
	// "env", "cache", "stack", or "" for none.
	Source string
	// TrialStart is when this instance first ran. Zero means it could not be
	// determined, which keeps the trial arm unknown rather than elapsed.
	TrialStart time.Time
	// raw is the key text as loaded, so a refresh can tell "the stack sent the
	// same key again" without re-verifying it.
	raw string
}

// Status is the derived answer at one instant.
type Status struct {
	State State
	// Restricted is the ONLY thing enforcement reads.
	Restricted  bool
	Holder      string
	Plan        string
	ExpiresAt   time.Time
	GraceUntil  time.Time
	TrialEndsAt time.Time
	Err         error
	Source      string
	// Entitlements are the licence's limits. They are zero — meaning
	// unlimited — whenever there is no verified licence, so an unknown or
	// unlicensed state can never be MORE restrictive than a licensed one.
	Entitlements Entitlements
}

// DomainsAllowed reports whether one more custom domain may be added to an
// instance that already serves `current` of them.
//
// It is a ceiling checked at ADD time and nothing else. It never removes a
// domain and never stops one already configured from serving: a licence that
// shrinks — a downgrade, or a cap introduced later — leaves what is there
// alone and refuses only the next one. Same rule as the cleanup sweep.
//
// current < 0 means the instance's domains could not be counted, which is
// unknown, which never restricts.
func (s Status) DomainsAllowed(current int) bool {
	if s.Entitlements.MaxCustomDomains <= 0 || current < 0 {
		return true
	}
	return current < s.Entitlements.MaxCustomDomains
}

// StatusAt derives the state at now. It is cheap and time-pure: call it per
// request rather than caching a state that quietly goes stale.
func (s Snapshot) StatusAt(now time.Time) Status {
	if !s.Loaded {
		return Status{State: StateUnknown}
	}
	st := Status{Err: s.Err, Source: s.Source}
	if l := s.License; l != nil {
		st.Holder, st.Plan = l.Lic.Holder, l.Lic.Plan
		st.ExpiresAt, st.GraceUntil = l.Lic.ExpiresAt, l.GraceEnd()
		// A certificate that has expired since it was verified stops vouching
		// for the licence; that is unverifiable, so it falls through to the
		// unlicensed arm rather than to "expired".
		certLive := l.Cert.ExpiresAt.IsZero() || !now.After(l.Cert.ExpiresAt)
		if certLive {
			st.Entitlements = l.Lic.Entitlements
			switch {
			case l.Lic.ExpiresAt.IsZero() || !now.After(l.Lic.ExpiresAt):
				st.State = StateLicensed
			case !now.After(l.GraceEnd()):
				st.State = StateGrace
			default:
				st.State = StateExpired
				st.Restricted = true
			}
			return st
		}
		st.Holder, st.Plan = "", ""
		st.ExpiresAt, st.GraceUntil = time.Time{}, time.Time{}
		st.Entitlements = Entitlements{}
	}

	// No usable licence: the trial arm.
	st.State = StateNone
	if s.TrialStart.IsZero() {
		// Unknown, not elapsed. We do not know when this instance first ran, so
		// we cannot claim its trial is over.
		st.State = StateUnknown
		return st
	}
	st.TrialEndsAt = s.TrialStart.Add(TrialPeriod)
	st.Restricted = now.After(st.TrialEndsAt)
	return st
}

// Notice is the permanent, non-dismissable message for the ACCOUNT UI, or ""
// when there is nothing to say. It is never rendered into a served site.
func (s Status) Notice() string {
	switch {
	case s.State == StateGrace:
		return fmt.Sprintf(
			"This Sitebin Enterprise licence expired on %s. Service continues until %s, after which no new sites or drops can be created. Renew to keep publishing.",
			s.ExpiresAt.Format("2 January 2006"), s.GraceUntil.Format("2 January 2006"))
	case s.State == StateExpired:
		return fmt.Sprintf(
			"This Sitebin Enterprise licence expired on %s and its grace period ended on %s. Existing sites keep serving and can still be updated, but no new sites or drops can be created until it is renewed.",
			s.ExpiresAt.Format("2 January 2006"), s.GraceUntil.Format("2 January 2006"))
	case s.State == StateNone && s.Restricted:
		return "The 90-day Sitebin Enterprise trial on this instance has ended. Existing sites keep serving and can still be updated, but no new sites or drops can be created until a licence key is installed."
	}
	return ""
}

// LogArgs are the key/values for a single startup log line.
func (s Status) LogArgs() []any {
	args := []any{"state", string(s.State)}
	if s.Source != "" {
		args = append(args, "source", s.Source)
	}
	if s.Holder != "" {
		args = append(args, "holder", s.Holder)
	}
	if s.Plan != "" {
		args = append(args, "plan", s.Plan)
	}
	if !s.ExpiresAt.IsZero() {
		args = append(args, "expires", s.ExpiresAt.Format(time.RFC3339))
	}
	if !s.GraceUntil.IsZero() && !s.GraceUntil.Equal(s.ExpiresAt) {
		args = append(args, "grace_until", s.GraceUntil.Format(time.RFC3339))
	}
	if !s.TrialEndsAt.IsZero() {
		args = append(args, "trial_ends", s.TrialEndsAt.Format(time.RFC3339))
	}
	if s.Entitlements.MaxCustomDomains > 0 {
		args = append(args, "max_custom_domains", s.Entitlements.MaxCustomDomains)
	}
	args = append(args, "restricts_creation", s.Restricted)
	if s.Err != nil {
		args = append(args, "err", s.Err)
	}
	return args
}
