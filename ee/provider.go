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
	mcpOAuth *mcpOAuth
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
	// MCP OAuth is inert unless an issuer is configured. Sitebin validates
	// tokens that issuer signed; it never issues any.
	p.mcpOAuth = newMCPOAuth(h.MCPOAuthIssuer(), h.MCPResource(), func(subject string) (string, bool) {
		acc, err := p.accounts.ByOAuth(account.OIDCProv, subject)
		if err != nil {
			return "", false
		}
		return acc.ID, true
	})
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
	// An API token names the account outright; the session cookie is the
	// browser's way of doing the same. Either identifies an owner for the site
	// about to be created.
	if acc, ok := p.accountForAPI(r); ok {
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

// errUnknownTier reports that an account names a tier id the configuration does
// not contain — a renamed id, a truncated tiers.json, a bad env rollout. It is
// an operator mistake, never a customer state, and the only honest answer is
// "unknown": the default tier is a guess, and acting on that guess would
// downgrade the account and start a deletion countdown on its sites with no
// record left of what the account was actually on.
var errUnknownTier = errors.New("account references a tier id that is not configured")

// paygateTier resolves an account's tier from PayGate, when PayGate is
// configured and governs that account. ok=false with a nil error means PayGate
// does not govern this account, has no subscription for it, or answered with a
// tier id this instance does not configure — all of which mean "fall back to
// the stored tier". Both effectiveTier and effectiveTierStrict go through here
// so their PayGate handling cannot drift apart.
func (p *provider) paygateTier(acc *account.Account) (eeconfig.Tier, bool, error) {
	if p.paygate == nil || acc.Provider != account.OIDCProv || acc.OAuthSubject == "" {
		return eeconfig.Tier{}, false, nil
	}
	id, ok, err := p.paygate.TierFor(context.Background(), acc.OAuthSubject)
	if err != nil {
		return eeconfig.Tier{}, false, fmt.Errorf("paygate tier lookup for %s: %w", acc.ID, err)
	}
	if !ok {
		return eeconfig.Tier{}, false, nil
	}
	if t, found := p.cfg.Tier(id); found {
		return t, true, nil
	}
	slog.Warn("paygate returned a tier missing from tiers config; using stored tier", "tier", id)
	return eeconfig.Tier{}, false, nil
}

// storedTier resolves the tier recorded on the account itself. An account with
// no tier at all (a fresh account, or one created before tiers mode was turned
// on) legitimately starts on the configured default, so that answer is
// authoritative.
//
// authoritative=false means the account NAMES a tier the configuration does not
// have: the returned default is a guess about a plan nobody configured. Callers
// that persist or delete must not act on it — see errUnknownTier.
func (p *provider) storedTier(acc *account.Account) (eeconfig.Tier, bool) {
	if acc.Tier == "" {
		t, ok := p.cfg.Tier(p.cfg.DefaultTier)
		return t, ok
	}
	if t, ok := p.cfg.Tier(acc.Tier); ok {
		return t, true
	}
	t, _ := p.cfg.Tier(p.cfg.DefaultTier)
	return t, false
}

// effectiveTier resolves the tier that governs an account's quotas. With
// PayGate configured, accounts signed in through the generic OIDC provider
// (their OAuth subject is the stack user id) resolve live from PayGate;
// everything else — other providers, unknown tier ids, PayGate misses or
// outages — falls back to the stored tier, then the default.
//
// It fails open, so it is the right choice for creation: a config or PayGate
// problem must not stop a customer publishing. Callers that PERSIST the answer
// or act destructively on it (tier sync, the cleanup sweep's quota lookup) must
// use effectiveTierStrict instead, which reports what this one hides.
func (p *provider) effectiveTier(acc *account.Account) eeconfig.Tier {
	t, ok, err := p.paygateTier(acc)
	switch {
	case err != nil:
		slog.Warn("paygate lookup failed; using stored tier", "account", acc.ID, "err", err)
	case ok:
		return t
	}
	t, _ = p.storedTier(acc) // fail open: a guessed cap beats refusing to serve
	return t
}

// effectiveTierStrict resolves an account's tier like effectiveTier, but
// returns an error instead of guessing: a PayGate failure, or (wrapping
// errUnknownTier) an account naming a tier this instance does not configure.
// Callers that would destroy data or overwrite the stored tier on a wrong
// answer use this one, and get no tier at all when the answer is not known.
func (p *provider) effectiveTierStrict(acc *account.Account) (eeconfig.Tier, error) {
	t, ok, err := p.paygateTier(acc)
	if err != nil {
		return eeconfig.Tier{}, err
	}
	if ok {
		return t, nil
	}
	t, authoritative := p.storedTier(acc)
	if !authoritative {
		return eeconfig.Tier{}, fmt.Errorf("%w: account %s is on tier %q, which is not in SITEBIN_TIERS", errUnknownTier, acc.ID, acc.Tier)
	}
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
	// Strict: the cleanup sweep deletes on this answer. A PayGate outage or an
	// account on a tier the config does not have both surface as an error, and
	// the sweep keeps the site rather than deleting it against a guessed cap.
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
	// Creation is the only moment a downgraded customer is guaranteed to come
	// back for. Their existing sites have no expiry, so the cleanup sweep skips
	// them entirely and never consults the tier at all — it is structurally
	// incapable of noticing a downgrade. Without this call, someone who cancels
	// in the billing portal and never opens /account again keeps permanent
	// hosting for their whole back catalogue, which is the bug this branch
	// exists to kill. syncTier no-ops on a resolve error, so a PayGate outage
	// still cannot block creation.
	p.syncTier(acc)
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
		// An anonymous site is never trusted, whatever its tier says: the
		// anonymous tier is a quota bundle, not a statement about who uploaded.
		Trusted: t.Trusted && owner != "",
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

// accountForAPI resolves an API caller: an Authorization: Bearer token, else
// the session cookie (which is how the browser's own drop page creates sites).
// accountForAPI resolves the caller of an API-shaped request.
//
// A presented bearer is answered ONLY from that bearer: an account API token,
// or — where MCP OAuth is configured — an access token from the issuer. It
// deliberately does not fall back to the session cookie when a bearer was sent
// and did not check out, because a wrong credential must fail rather than
// quietly succeed as whoever happens to be logged in.
func (p *provider) accountForAPI(r *http.Request) (*account.Account, bool) {
	if secret := bearerToken(r); secret != "" {
		if acc, ok := p.accounts.ByToken(secret); ok {
			return acc, true
		}
		if p.mcpOAuth != nil {
			if cred, ok := p.mcpOAuth.Verify(r.Context(), secret); ok {
				if acc, err := p.accounts.ByID(cred.AccountID); err == nil {
					return acc, true
				}
			}
		}
		return nil, false
	}
	return p.currentAccount(r)
}

// bearerToken extracts an Authorization: Bearer credential, or "" when the
// header is absent or shaped differently.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// BearerCredential resolves an Authorization: Bearer credential. See
// ext.Provider.
//
// Two kinds of credential arrive on the same header, and telling them apart is
// this method's job so the core never has to. An account API token is
// recognised by its own prefix and grants everything its account can do, which
// is why it carries no scopes. Anything else is offered to the OAuth verifier,
// when one is configured.
func (p *provider) BearerCredential(r *http.Request) (ext.Credential, bool) {
	if !p.cfg.Enabled() {
		return ext.Credential{}, false
	}
	secret := bearerToken(r)
	if secret == "" {
		return ext.Credential{}, false
	}
	if acc, ok := p.accounts.ByToken(secret); ok {
		return ext.Credential{AccountID: acc.ID}, true
	}
	if p.mcpOAuth != nil {
		if cred, ok := p.mcpOAuth.Verify(r.Context(), secret); ok {
			return cred, true
		}
	}
	return ext.Credential{}, false
}

// AccountSiteIDs returns the view ids the account owns. Like BearerAccount it
// reports an unknown account and a disabled instance the same way — the caller
// is asking "which sites may I list", and both answers are "none you can name".
func (p *provider) AccountSiteIDs(accountID string) ([]string, bool) {
	if !p.cfg.Enabled() {
		return nil, false
	}
	acc, err := p.accounts.ByID(accountID)
	if err != nil {
		return nil, false
	}
	ids, err := p.accounts.ListSiteIDs(acc)
	if err != nil {
		return nil, false
	}
	return ids, true
}

// currentAccount resolves the SESSION COOKIE to an account, honoring token
// version (revocation).
//
// It deliberately does not accept API tokens. Those are script credentials for
// the site API; letting one drive the dashboard would let it read a CSRF token
// and go on to change the tier, rotate passwords or delete the account. A
// token can act on sites, not on the account that owns them.
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
func (p *provider) baseURL() string { return p.host.BaseURL() }

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

// syncTier reconciles an account's sites with its current tier when a tier
// change is discovered without any dedicated notice. PayGate has no webhook
// at all — it is only ever polled — so a dashboard render is the first
// moment Sitebin can notice a plan change made there. The billing webhook
// and self-select routes already know the new tier the instant they write
// it, so they call restampSites directly instead of this.
//
// It logs its failures instead of returning them: it runs on request paths
// whose primary job is something else, and a stale stamp is not worth failing
// a page render over. The cleanup sweep is the backstop that keeps a missed
// sync from destroying data.
func (p *provider) syncTier(acc *account.Account) {
	if p.cfg.Mode != eeconfig.ModeTiers {
		return
	}
	t, err := p.effectiveTierStrict(acc)
	if err != nil {
		// An unresolvable tier is never a reason to rewrite the account: the
		// stored tier is the only remaining record of what it is on, and the
		// default is a guess that would downgrade it and put a deletion date on
		// every site it owns. Leave everything as it is and say so loudly —
		// errUnknownTier is an operator mistake that only a human can fix.
		if errors.Is(err, errUnknownTier) {
			slog.Error("tier sync: account tier is not in the tiers config; leaving the account and its sites untouched",
				"account", acc.ID, "tier", acc.Tier, "err", err)
		} else {
			slog.Warn("tier sync: could not resolve tier", "account", acc.ID, "err", err)
		}
		return
	}
	if t.ID == acc.Tier {
		return
	}
	// Restamp BEFORE persisting the new tier. acc.Tier is this function's only
	// retry marker: the moment it matches the resolved tier, every later pass
	// early-returns above, so sites that failed to restamp would keep their old
	// caps forever. Leaving it stale costs one repeated pass and converges;
	// writing it first makes a partial failure permanent.
	//
	// The retry is safe because restamping is idempotent: once the first pass
	// has stamped the new cap on a site, a second pass with the same tier sees
	// an unchanged cap and leaves the expiry — including a freshly stamped
	// 30-day downgrade grace — exactly where it is. See store.ApplyQuota.
	if err := p.restampSites(acc, t); err != nil {
		slog.Error("tier sync: leaving the stored tier stale so the next pass retries the restamp",
			"account", acc.ID, "tier", t.ID, "err", err)
		return
	}
	if err := p.accounts.Update(acc, func(cur *account.Account) error { cur.Tier = t.ID; return nil }); err != nil {
		slog.Error("tier sync: could not persist tier", "account", acc.ID, "tier", t.ID, "err", err)
	}
}

// restampSites applies tier t's caps to every site acc owns, via
// ext.SiteService.ApplyQuota, which also reconciles each site's expiry with
// the new lifetime cap. It is the single place the three tier-change routes
// (PayGate sync, self-select, billing webhook) do this, so none of them can
// silently skip logging a failure.
//
// It logs every failure itself, so callers that have nothing better to do than
// log may ignore the returned error; it exists for syncTier, which uses it to
// decide whether the account's stored tier may be advanced yet. A site that
// could not be restamped never stops the others from being tried.
//
// A site that no longer exists is NOT a failure. Ownership markers outlive the
// site whenever it is deleted anywhere but the dashboard — the edit page's
// delete and the cleanup sweep both go straight to the store — and a dangling
// marker that counted as a failure would block this account's tier from ever
// being persisted again, re-running the whole restamp on every dashboard render
// and every site creation for good. The marker is dropped instead, so the
// account heals itself on the pass that discovers it.
func (p *provider) restampSites(acc *account.Account, t eeconfig.Tier) error {
	ids, err := p.accounts.ListSiteIDs(acc)
	if err != nil {
		slog.Error("restamp sites: could not list sites", "account", acc.ID, "tier", t.ID, "err", err)
		return fmt.Errorf("list sites of %s: %w", acc.ID, err)
	}
	grant := grantFromTier(acc.ID, t)
	var failed []error
	stamped := 0
	for _, id := range ids {
		err := p.host.Sites().ApplyQuota(id, grant)
		switch {
		case err == nil:
			stamped++
		case errors.Is(err, ext.ErrSiteGone):
			slog.Info("restamp sites: dropping the ownership marker of a site that no longer exists",
				"account", acc.ID, "site", id)
			if err := p.accounts.UnlinkSite(acc, id); err != nil {
				// The marker survives to the next pass, which will discover it
				// is stale again and retry the unlink. That is a nuisance, not a
				// failure of the restamp, so it must not block the tier persist —
				// blocking on it is exactly the deadlock this arm exists to end.
				slog.Error("restamp sites: could not drop a stale ownership marker",
					"account", acc.ID, "site", id, "err", err)
			}
		default:
			slog.Error("restamp sites: could not restamp site", "account", acc.ID, "site", id, "err", err)
			failed = append(failed, fmt.Errorf("site %s: %w", id, err))
		}
	}
	if len(failed) > 0 {
		return errors.Join(failed...)
	}
	slog.Info("restamp sites: applied new tier caps", "account", acc.ID, "tier", t.ID, "sites", stamped)
	return nil
}

// tierForNewAccount returns the tier id a new account starts on.
func (p *provider) tierForNewAccount() string {
	if p.cfg.DefaultTier != "" {
		return p.cfg.DefaultTier
	}
	return ""
}

var _ = strconv.Itoa // reserved for future numeric config
