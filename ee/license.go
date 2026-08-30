//go:build ee

package ee

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/ittrail/sitebin.io/ee/licensing"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// Enterprise licensing.
//
// Three rules, none of which may be softened:
//
//   - **It never refuses to start.** No key, an expired one and a malformed one
//     are all logged and surfaced in the account UI; none of them takes the
//     instance down and none of them touches serving.
//   - **Existing sites are never touched.** No interstitial, no banner injected
//     into a served page, no expiry brought forward. The restriction is on
//     CREATING; content updates keep working, because otherwise sliding renewal
//     stops and sites quietly lapse.
//   - **Unknown is not expired.** If the state cannot be determined, nothing is
//     restricted.
//
// Enforcement lives at exactly one place: licenseGate, called from
// AuthorizeCreate. Never on the serving path.

// initLicensing resolves the licence and starts collecting renewals. It
// returns no error on purpose — there is no licence problem that is worth
// failing a start over.
func (p *provider) initLicensing() {
	roots, err := licensing.TrustedRoots()
	if err != nil {
		// A build whose baked roots do not parse trusts nothing, which is the
		// unlicensed state — not a reason to refuse to run.
		slog.Error("license: the trusted roots baked into this build are unusable; no license can be verified", "err", err)
	}
	if len(roots) == 0 {
		slog.Warn("license: this build carries no trusted license roots; running unlicensed")
	}
	p.license = licensing.NewManager(p.host.DataDir(), roots, licensing.AppID, nil)

	st := p.license.Load(os.Getenv("SITEBIN_LICENSE_KEY"))
	slog.Info("enterprise license", st.LogArgs()...)
	if st.Err != nil {
		slog.Warn("license: the supplied license key could not be verified; treating this instance as unlicensed (NOT as expired)", "err", st.Err)
	}

	// Collection is asynchronous and optional: it exists so a RENEWED licence
	// reaches a running container, never as a precondition for starting.
	if f := p.licenseFetcher(); f != nil {
		p.license.Start(f)
	}
}

// licenseStatus is the licence state right now, or the unknown state when
// licensing has not been wired (a hand-built provider in a test). Unknown
// restricts nothing.
func (p *provider) licenseStatus() licensing.Status {
	if p.license == nil {
		return licensing.Status{State: licensing.StateUnknown}
	}
	return p.license.Status()
}

// licenseGate is the ONE place the licence is enforced. It refuses the
// creation of a new site or a new anonymous drop when the licence is expired,
// or when there is none and the trial has elapsed. It never refuses on an
// unknown state, and it is never consulted by anything but AuthorizeCreate.
func (p *provider) licenseGate() error {
	st := p.licenseStatus()
	if !st.Restricted {
		return nil
	}
	msg := "this Sitebin Enterprise instance cannot create new sites: its license has expired. Existing sites keep serving and can still be updated."
	if st.State == licensing.StateNone {
		msg = "this Sitebin Enterprise instance cannot create new sites: its 90-day trial has ended and no license key is installed. Existing sites keep serving and can still be updated."
	}
	return &ext.CreateError{Status: 402, Msg: msg}
}

// licenseAllowsAnotherDomain reports whether the instance may serve one more
// custom domain under its licence's entitlement.
//
// The entitlement is an INSTANCE-WIDE ceiling, not a per-site allowance: the
// website sells licence tiers that scale by custom-domain cap, and the customer
// self-hosts, so without this a Team licence holder edits one number in their
// own tiers.json and has Platform. The tier's per-site cap still applies on top
// — the licence can only ever reduce what the tier already permits.
//
// Two things it deliberately does not do:
//
//   - It never removes or breaks a domain already configured. It is consulted
//     when a domain is ADDED and nowhere else, so a licence that shrinks leaves
//     what is there serving and refuses only the next one.
//   - It never restricts on an unknown answer. No entitlement, no licence, or an
//     instance whose domains cannot be counted all mean "allowed".
func (p *provider) licenseAllowsAnotherDomain() error {
	st := p.licenseStatus()
	if st.Entitlements.MaxCustomDomains <= 0 {
		return nil // unlimited, which is also what "no licence" means
	}
	n, err := p.customDomainCount()
	if err != nil {
		// Counting failed: unknown, so nothing is refused. Enumerating sites is
		// how this is answered and one unreadable meta.json must not cost a
		// customer their domain.
		slog.Warn("license: could not count the instance's custom domains; allowing the domain", "err", err)
		return nil
	}
	if n < st.Entitlements.MaxCustomDomains {
		return nil
	}
	slog.Warn("license: refusing a custom domain; the license entitles this instance to fewer",
		"configured", n, "max_custom_domains", st.Entitlements.MaxCustomDomains)
	// The reason reaches the customer, because being told this is an
	// enterprise feature when you have paid for it is worse than a refusal.
	return fmt.Errorf("this instance's license covers %d custom domains and %d are configured; remove one or upgrade the license",
		st.Entitlements.MaxCustomDomains, n)
}

// customDomainCount totals the custom domains configured across the whole
// instance, anonymous sites included. Sites().All() is the same enumeration the
// admin console does; it is a rare path and nowhere near the hot one.
func (p *provider) customDomainCount() (int, error) {
	if p.host == nil {
		return 0, fmt.Errorf("no host")
	}
	sites, err := p.host.Sites().All()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range sites {
		n += len(s.Domains)
	}
	return n, nil
}

// ---- collecting the licence from the stack ----

// licenseFetcher builds the stack collector, or nil when this instance has no
// stack to ask. SITEBIN_LICENSE_URL overrides the derived URL.
//
// The endpoint is POST <paygate>/api/v1/licenses/renew and it takes NO API
// key: the licence authenticates itself. That is the whole point — a customer
// running Sitebin Enterprise on their own hardware is not a registered app on
// our stack. They have no app id and no API key, and they must never be given
// the stack's admin key, which acts on every app there is. What they do have
// is the signed licence we issued them, which the stack can verify because it
// signed it.
//
// The first licence therefore never comes from here; it arrives by email. This
// path is for renewals, so that a customer whose subscription renewed is not
// locked out because nobody read the mail.
func (p *provider) licenseFetcher() licensing.Fetcher {
	if url := strings.TrimSpace(os.Getenv("SITEBIN_LICENSE_URL")); url != "" {
		return &stackLicenseFetcher{url: url}
	}
	if p.cfg.PayGate != nil {
		return &stackLicenseFetcher{
			url: strings.TrimRight(p.cfg.PayGate.URL, "/") + "/api/v1/licenses/renew",
		}
	}
	return nil
}

type stackLicenseFetcher struct {
	url string
	// client is nil in production (http.DefaultClient); tests inject one.
	client *http.Client
}

func (f *stackLicenseFetcher) Fetch(ctx context.Context, current string) (string, bool, error) {
	current = strings.TrimSpace(current)
	if current == "" {
		// Nothing to prove who we are with. Not an error: an instance that has
		// never been licensed simply has no renewal to collect.
		return "", false, nil
	}
	body, err := json.Marshal(map[string]string{"license": current})
	if err != nil {
		return "", false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url, bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	c := f.client
	if c == nil {
		c = http.DefaultClient
	}
	res, err := c.Do(req)
	if err != nil {
		return "", false, err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusNoContent:
		// The stack has no licence for this instance. That is an answer, not a
		// failure — and it is emphatically not "expired".
		return "", false, nil
	case res.StatusCode < 200 || res.StatusCode >= 300:
		return "", false, fmt.Errorf("license endpoint returned %d: %s", res.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var payload struct {
		License string `json:"license"`
		Key     string `json:"key"`
	}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return "", false, fmt.Errorf("license endpoint returned unparseable JSON: %w", err)
	}
	key := payload.License
	if key == "" {
		key = payload.Key
	}
	if strings.TrimSpace(key) == "" {
		return "", false, nil
	}
	return key, true, nil
}

// licenseNotice is the account-UI banner. It is empty for every state before
// expires_at, and it is rendered ONLY in the account dashboard — never into a
// served site, which the design forbids outright.
type licenseNotice struct {
	Text string
	// Severity picks the styling: "warn" while in grace, "err" once creation
	// is actually restricted.
	Severity string
}

func (p *provider) licenseNotice() *licenseNotice {
	st := p.licenseStatus()
	text := st.Notice()
	if text == "" {
		return nil
	}
	sev := "warn"
	if st.Restricted {
		sev = "err"
	}
	return &licenseNotice{Text: text, Severity: sev}
}
