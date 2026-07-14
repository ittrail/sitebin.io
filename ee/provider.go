//go:build ee

package ee

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/ittrail/sitebin/ee/account"
	"github.com/ittrail/sitebin/ee/authn"
	"github.com/ittrail/sitebin/ee/billing"
	"github.com/ittrail/sitebin/ee/eeconfig"
	"github.com/ittrail/sitebin/ee/session"
	"github.com/ittrail/sitebin/ee/smtp"
	"github.com/ittrail/sitebin/internal/ext"
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
	return nil
}

func (p *provider) AccountsEnabled() bool { return p.cfg.Enabled() }

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

// grantForAccount enforces the account's tier site-count cap and returns the
// grant carrying its per-site quota caps.
func (p *provider) grantForAccount(acc *account.Account) (ext.CreateGrant, error) {
	if p.cfg.Mode != eeconfig.ModeTiers {
		return ext.CreateGrant{OwnerAccountID: acc.ID}, nil // accounts mode: ownership only
	}
	tier, ok := p.cfg.Tier(acc.Tier)
	if !ok {
		tier, _ = p.cfg.Tier(p.cfg.DefaultTier)
	}
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
	return ext.CreateGrant{
		OwnerAccountID:  owner,
		MaxSiteBytes:    t.MaxSiteBytes,
		MaxFiles:        t.MaxFiles,
		MaxExpiryDays:   t.MaxExpiryDays,
		MaxCustomDomain: t.CustomDomains,
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
