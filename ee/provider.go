//go:build ee

package ee

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/authn"
	"github.com/ittrail/sitebin.io/ee/billing"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
	"github.com/ittrail/sitebin.io/ee/licensing"
	"github.com/ittrail/sitebin.io/ee/session"
	"github.com/ittrail/sitebin.io/ee/smtp"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// provider is the enterprise ext.Provider: it gates site creation behind
// accounts/tiers and serves the account dashboard.
type provider struct {
	host     ext.Host
	cfg      eeconfig.Config
	accounts *account.Store
	sessions *session.Manager
	local    *authn.Local
	oidc     *authn.OIDC
	mailer   *smtp.Mailer
	stripe   *billing.Stripe
	paddle   *billing.Paddle
	paygate  *billing.PayGate
	secret   []byte
}

func newProvider() *provider { return &provider{} }

func (p *provider) Name() string    { return "sitebin-ee" }
func (p *provider) Version() string { return Version }

func (p *provider) Init(h ext.Host) error {
	p.host = h
	p.secret = h.Secret()

	cfg, err := eeconfig.Load(os.Getenv, os.ReadFile)
	if err != nil {
		return fmt.Errorf("enterprise config: %w", err)
	}
	p.cfg = cfg

	// License check: a key is optional (self-host is permitted under the Elastic
	// License 2.0), but if supplied it must be valid. Circumventing this check
	// is prohibited by ee/LICENSE.
	if key := strings.TrimSpace(os.Getenv("SITEBIN_LICENSE_KEY")); key != "" {
		pub, err := licensing.VendorKey()
		if err != nil {
			return err
		}
		lic, err := licensing.Verify(key, pub, time.Now())
		if err != nil {
			return fmt.Errorf("SITEBIN_LICENSE_KEY: %w", err)
		}
		slog.Info("enterprise license", "holder", lic.Holder, "plan", lic.Plan, "expires", lic.ExpiresAt)
	} else if cfg.Enabled() {
		slog.Warn("running Sitebin Enterprise without a license key (self-host mode); see ee/LICENSE")
	}

	// Safety: serving user content on /v/<id> paths puts it on the SAME origin
	// as the account dashboard and API. Combined with accounts, a malicious
	// path-hosted page could ride a logged-in visitor's session (same-origin
	// fetch of the CSRF-protected dashboard). Refuse that combination.
	if cfg.Enabled() && h.PathViews() {
		return fmt.Errorf("SITEBIN_VIEW_ACCESS=path/both cannot be combined with accounts (SITEBIN_ACCOUNT_MODE=%s): serving user content on the main-domain origin alongside the account session is an account-takeover risk. Use subdomain view access with accounts, or run without accounts", cfg.Mode)
	}

	store, err := account.New(h.DataDir())
	if err != nil {
		return fmt.Errorf("account store: %w", err)
	}
	p.accounts = store
	p.sessions = session.New(h.Secret(), !h.HTTPOnly(), session.DefaultTTL)
	p.local = authn.NewLocal(store)
	p.oidc = authn.NewOIDC(cfg, p.baseURL())
	if cfg.EmailEnabled() {
		p.mailer = smtp.New(*cfg.SMTP)
	}
	if cfg.BillingEnabled() {
		if cfg.Billing.Stripe != nil {
			p.stripe = billing.NewStripe(*cfg.Billing.Stripe)
		}
		if cfg.Billing.Paddle != nil {
			p.paddle = billing.NewPaddle(*cfg.Billing.Paddle)
		}
	}
	if cfg.PayGate != nil {
		p.paygate = billing.NewPayGate(*cfg.PayGate)
		slog.Info("paygate tier source active", "url", cfg.PayGate.URL, "app", cfg.PayGate.AppID)
		if cfg.OIDC == nil {
			slog.Warn("PayGate is configured but SITEBIN_OAUTH_OIDC_* is not: only accounts signed in through the generic OIDC provider resolve tiers via PayGate")
		}
		if cfg.SelfSelect {
			slog.Warn("SITEBIN_TIER_SELF_SELECT is ignored for PayGate-resolved accounts (PayGate owns their subscription)")
		}
		if cfg.BillingEnabled() {
			slog.Warn("built-in Stripe/Paddle billing and PayGate are both configured; PayGate takes precedence for SSO accounts")
		}
	}
	return nil
}

func (p *provider) AccountsEnabled() bool { return p.cfg.Enabled() }

// CustomDomainsAllowed makes custom domains available in the enterprise
// edition. Per-account/per-tier limits are still enforced by the tier's
// custom_domains cap.
func (p *provider) CustomDomainsAllowed() bool { return true }

// EmbedOriginsAllowed makes SITEBIN_EMBED_ORIGINS effective in the enterprise
// edition, allowing allowlisted foreign origins to embed the create flow.
func (p *provider) EmbedOriginsAllowed() bool { return true }

// AuthorizeCreate gates site creation and returns the owner + per-site quota
// caps. Logged-in users own their sites; anonymous creation is allowed only
// when the mode/config permits it.
func (p *provider) AuthorizeCreate(r *http.Request) (ext.CreateGrant, error) {
	if !p.cfg.Enabled() {
		return ext.CreateGrant{}, nil
	}
	if acc, ok := p.currentAccount(r); ok {
		return p.grantForAccount(acc)
	}
	switch p.cfg.Mode {
	case eeconfig.ModeAccounts:
		if p.cfg.AllowAnon {
			return ext.CreateGrant{}, nil
		}
	case eeconfig.ModeTiers:
		if p.cfg.AnonTier != "" {
			t, _ := p.cfg.Tier(p.cfg.AnonTier)
			return grantFromTier("", t), nil
		}
	}
	return ext.CreateGrant{}, &ext.CreateError{Status: 401, Msg: "sign in to create a site: " + p.baseURL() + "/account"}
}

// effectiveTier resolves the tier that governs an account's quotas. With
// PayGate configured, accounts signed in through the generic OIDC provider
// (their OAuth subject is the stack user id) resolve live from PayGate;
// everything else — other providers, unknown tier ids, PayGate misses or
// outages — falls back to the stored tier, then the default.
func (p *provider) effectiveTier(acc *account.Account) eeconfig.Tier {
	if p.paygate != nil && acc.Provider == account.OIDCProv && acc.OAuthSubject != "" {
		id, ok, err := p.paygate.TierFor(context.Background(), acc.OAuthSubject)
		switch {
		case err != nil:
			slog.Warn("paygate lookup failed; using stored tier", "account", acc.ID, "err", err)
		case ok:
			if t, found := p.cfg.Tier(id); found {
				return t
			}
			slog.Warn("paygate returned a tier missing from tiers config; using stored tier", "tier", id)
		}
	}
	if t, ok := p.cfg.Tier(acc.Tier); ok {
		return t
	}
	t, _ := p.cfg.Tier(p.cfg.DefaultTier)
	return t
}

// effectiveTierStrict resolves an account's tier like effectiveTier, but
// surfaces a PayGate failure instead of falling back to the stored tier.
// Callers that would destroy data on a wrong answer use this one.
func (p *provider) effectiveTierStrict(acc *account.Account) (eeconfig.Tier, error) {
	if p.paygate != nil && acc.Provider == account.OIDCProv && acc.OAuthSubject != "" {
		id, ok, err := p.paygate.TierFor(context.Background(), acc.OAuthSubject)
		if err != nil {
			return eeconfig.Tier{}, fmt.Errorf("paygate tier lookup for %s: %w", acc.ID, err)
		}
		if ok {
			if t, found := p.cfg.Tier(id); found {
				return t, nil
			}
			slog.Warn("paygate returned a tier missing from tiers config; using stored tier", "tier", id)
		}
	}
	if t, ok := p.cfg.Tier(acc.Tier); ok {
		return t, nil
	}
	t, _ := p.cfg.Tier(p.cfg.DefaultTier)
	return t, nil
}

// QuotaFor returns the caps the owner's current tier grants. See ext.Provider.
func (p *provider) QuotaFor(ownerAccountID string) (ext.CreateGrant, bool, error) {
	if !p.cfg.Enabled() || ownerAccountID == "" {
		return ext.CreateGrant{}, false, nil
	}
	acc, err := p.accounts.ByID(ownerAccountID)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			// an account that no longer exists is not an error: the site is
			// orphaned and its caller may proceed
			return ext.CreateGrant{}, false, nil
		}
		return ext.CreateGrant{}, false, err
	}
	if p.cfg.Mode != eeconfig.ModeTiers {
		return ext.CreateGrant{OwnerAccountID: acc.ID}, true, nil
	}
	t, err := p.effectiveTierStrict(acc)
	if err != nil {
		return ext.CreateGrant{}, false, err
	}
	return grantFromTier(acc.ID, t), true, nil
}

// grantForAccount enforces the account's tier site-count cap and returns the
// grant carrying its per-site quota caps.
func (p *provider) grantForAccount(acc *account.Account) (ext.CreateGrant, error) {
	if p.cfg.Mode != eeconfig.ModeTiers {
		return ext.CreateGrant{OwnerAccountID: acc.ID}, nil // accounts mode: ownership only
	}
	tier := p.effectiveTier(acc)
	if tier.MaxSites > 0 {
		owned, _ := p.accounts.ListSiteIDs(acc)
		if len(owned) >= tier.MaxSites {
			label := tier.Label
			if label == "" {
				label = tier.ID
			}
			return ext.CreateGrant{}, &ext.CreateError{
				Status: 403,
				Msg:    fmt.Sprintf("your %s plan allows %d site(s); delete one or upgrade at %s/account", label, tier.MaxSites, p.baseURL()),
			}
		}
	}
	return grantFromTier(acc.ID, tier), nil
}

// grantFromTier stamps a tier's caps into a CreateGrant.
func grantFromTier(owner string, t eeconfig.Tier) ext.CreateGrant {
	webdav := t.WebDAV
	domains := t.CustomDomains
	return ext.CreateGrant{
		OwnerAccountID:  owner,
		MaxSiteBytes:    t.MaxSiteBytes,
		MaxFiles:        t.MaxFiles,
		MaxExpiryDays:   t.MaxExpiryDays,
		MaxCustomDomain: &domains,
		WebDAV:          &webdav,
	}
}

// OnSiteCreated records site ownership on the account.
func (p *provider) OnSiteCreated(ownerAccountID, viewID string) error {
	if ownerAccountID == "" {
		return nil
	}
	acc, err := p.accounts.ByID(ownerAccountID)
	if err != nil {
		return err
	}
	return p.accounts.LinkSite(acc, viewID)
}

// currentAccount resolves the session cookie to an account, honoring token
// version (revocation).
func (p *provider) currentAccount(r *http.Request) (*account.Account, bool) {
	id, ver, ok := p.sessions.Validate(r)
	if !ok {
		return nil, false
	}
	acc, err := p.accounts.ByID(id)
	if err != nil || acc.TokenVersion != ver {
		return nil, false
	}
	return acc, true
}

// owns reports whether the account owns the given site.
func (p *provider) owns(acc *account.Account, viewID string) bool {
	ids, err := p.accounts.ListSiteIDs(acc)
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id == viewID {
			return true
		}
	}
	return false
}

// baseURL returns the main-domain URL for building links.
func (p *provider) baseURL() string {
	scheme := "https"
	if p.host.HTTPOnly() {
		scheme = "http"
	}
	return scheme + "://" + p.host.BaseDomain()
}

// ---- CSRF (stateless, derived from the session) ----

// csrf returns a token bound to the account + token version. Combined with the
// SameSite=Lax session cookie this defends state-changing POSTs.
func (p *provider) csrf(acc *account.Account) string {
	mac := hmac.New(sha256.New, p.secret)
	fmt.Fprintf(mac, "%s|%d|csrf", acc.ID, acc.TokenVersion)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (p *provider) checkCSRF(r *http.Request, acc *account.Account) bool {
	got := r.PostFormValue("csrf")
	want := p.csrf(acc)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// tierForNewAccount returns the tier id a new account starts on.
func (p *provider) tierForNewAccount() string {
	if p.cfg.DefaultTier != "" {
		return p.cfg.DefaultTier
	}
	return ""
}

var _ = strconv.Itoa // reserved for future numeric config
