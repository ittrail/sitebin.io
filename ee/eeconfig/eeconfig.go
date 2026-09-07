//go:build ee

package eeconfig

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Mode selects how site creation is gated.
type Mode string

const (
	ModeOpen     Mode = "open"     // no accounts (community behavior)
	ModeAccounts Mode = "accounts" // login required, optional per-account quota
	ModeTiers    Mode = "tiers"    // tiered quotas, optional paid/anon tiers
)

// Price says what a paid tier costs, in the two forms the billing backends
// need. They are not alternatives to each other:
//
//   - Monthly/Annual/Currency are AMOUNTS. PayGate is handed these and creates
//     the product in whatever processor the stack uses, so Sitebin never learns
//     which one that is.
//   - Stripe/Paddle are identifiers for products an operator created in that
//     provider themselves. Only a direct backend can use them.
//
// A tier may carry both; the active backend reads the fields it needs.
type Price struct {
	Monthly  string `json:"monthly,omitempty"`
	Annual   string `json:"annual,omitempty"`
	Currency string `json:"currency,omitempty"` // ISO 4217, defaults to EUR

	Stripe string `json:"stripe,omitempty"`
	Paddle string `json:"paddle,omitempty"`

	// Display is the human string the dashboard shows while no amount is set.
	// An amount wins wherever both exist: a shown price must never be able to
	// disagree with the charged one.
	Display string `json:"display,omitempty"`
}

// Amount reports the monthly amount and currency, if the tier carries one.
func (p *Price) Amount() (string, string, bool) {
	if p == nil || strings.TrimSpace(p.Monthly) == "" {
		return "", "", false
	}
	cur := strings.TrimSpace(p.Currency)
	if cur == "" {
		cur = "EUR"
	}
	return p.Monthly, cur, true
}

// Label is the price the dashboard shows on a tier's card.
//
// The AMOUNT wins wherever both exist, which is the whole reason this is not
// a plain read of Display: a shown price must never be able to disagree with
// the charged one, and a PayGate catalogue carries amounts and no display
// string at all. Display is the fallback for a tier that has only ever had a
// hand-written price (a direct backend's price id says nothing about money).
func (p *Price) Label() string {
	if p == nil {
		return ""
	}
	if amount, currency, ok := p.Amount(); ok {
		return formatAmount(amount, currency) + "/mo"
	}
	return strings.TrimSpace(p.Display)
}

// formatAmount renders an amount in its currency. Only the currencies Sitebin
// is actually sold in get a symbol; anything else is shown as "12.00 CHF",
// which is unambiguous and never wrong.
func formatAmount(amount, currency string) string {
	switch strings.ToUpper(currency) {
	case "EUR":
		return "€" + amount
	case "USD":
		return "$" + amount
	case "GBP":
		return "£" + amount
	}
	return amount + " " + strings.ToUpper(currency)
}

// Tier is a named quota bundle. Zero MaxSiteBytes/MaxFiles/MaxSites mean "fall
// back to the global SITEBIN_MAX_* / unlimited"; the enforcing code decides.
type Tier struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	MaxSiteBytes  int64  `json:"max_site_bytes"`
	MaxFiles      int    `json:"max_files"`
	MaxSites      int    `json:"max_sites"`
	WebDAV        bool   `json:"webdav"`
	CustomDomains int    `json:"custom_domains"`
	MaxExpiryDays int    `json:"max_expiry_days"`
	// Admin marks a tier whose holders may reach the admin console. It is only
	// half of the gate: the account must ALSO be listed in
	// SITEBIN_ADMIN_ACCOUNTS. A tier source (PayGate, a stored tier) can
	// nominate an account; only the operator of the container can confirm it.
	Admin bool `json:"admin,omitempty"`
	// Trusted exempts the tier's sites from the strict content-security headers
	// that untrusted uploads get. Grant it to tiers whose holders you can hold
	// accountable; anonymous sites never qualify.
	Trusted bool `json:"trusted,omitempty"`
	// Featured marks the plan a pricing page should lead with. The stack takes
	// it per tier in the registration declaration; only the app knows which of
	// its plans it wants to sell, and the stack will not guess. The tier ORDER
	// needs no field: it is the order of the tier catalogue itself.
	Featured bool   `json:"featured,omitempty"`
	Price    *Price `json:"price,omitempty"`
}

// Paid reports whether the tier requires payment to activate.
func (t Tier) Paid() bool {
	if t.Price == nil {
		return false
	}
	return t.Price.Monthly != "" || t.Price.Annual != "" ||
		t.Price.Stripe != "" || t.Price.Paddle != ""
}

// OAuthProvider holds one OIDC provider's credentials.
type OAuthProvider struct {
	ClientID     string
	ClientSecret string
	Tenant       string // Microsoft only (default "common")
}

// GenericOIDC configures sign-in against any spec-compliant OIDC issuer —
// Keycloak, Okta, Authentik, or the IT-Trail SaaS Stack's Auth Gateway.
type GenericOIDC struct {
	// Issuer is the value that must appear in every token's `iss`, and — when
	// DiscoveryURL is empty — also where the discovery document is fetched.
	// On the SaaS Stack this is the realm's own URL,
	// https://auth.<stack-domain>/realms/<realm>.
	Issuer string

	// DiscoveryURL is where the discovery document is FETCHED, when that is
	// not the issuer's own origin. Empty = fetch it from Issuer, which is
	// what a plain OIDC provider wants.
	//
	// This exists for one deployment shape, and it is the SaaS Stack's: the
	// stack's Auth Gateway serves Keycloak's document with
	// `authorization_endpoint` pointed at the gateway, because that is where
	// the consent gate lives. An app that discovers from Keycloak directly
	// never passes the gate and its users are never asked to accept any
	// terms. `issuer` in that document is still Keycloak's, so the document's
	// URL and its issuer deliberately disagree — see ee/authn.oidcProvider.init
	// for how that is handled without loosening the check that matters.
	//
	// Either the realm/app base or the full .well-known URL; the suffix is
	// trimmed either way.
	DiscoveryURL string

	ClientID     string
	ClientSecret string
	Label        string // login-button text (default "SSO")
}

// Config is the parsed enterprise configuration.
type Config struct {
	Mode        Mode
	Tiers       []Tier
	DefaultTier string // tier assigned to a new/free account
	AnonTier    string // tier for anonymous creation ("" = anonymous disabled in tiers mode)
	SelfSelect  bool   // may users pick their own (free) tier
	// AdminAccounts are the emails allowed to reach the admin console, from
	// SITEBIN_ADMIN_ACCOUNTS. Normalized to lowercase. Empty disables the
	// console outright, whatever the tiers say.
	AdminAccounts []string
	AllowAnon     bool // in accounts mode, still allow anonymous creation
	LocalAuth     bool // email+password auth (default true; false = SSO only)

	Google    *OAuthProvider // nil = not configured
	Microsoft *OAuthProvider
	OIDC      *GenericOIDC // generic issuer (saas-stack, Keycloak, Okta, …)

	SMTP    *SMTPConfig    // nil = email disabled
	Billing *BillingConfig // nil = no direct payment provider configured
	PayGate *PayGateConfig // nil = built-in billing / stored tiers only

	// BillingBackend names the one backend that may sell a tier: "stripe",
	// "paddle", "paygate", or "" when none is configured. Exactly one is
	// active — see SITEBIN_BILLING.
	BillingBackend string
	// StackRegistration makes the instance announce itself to the SaaS Stack
	// at startup. nil = it does not.
	StackRegistration *StackConfig
	// GDPRSecret is the shared secret the SaaS Stack signs its GDPR webhook
	// calls with (delete user, export user data). Empty = the endpoints are
	// not mounted at all, because nothing could verify a caller. Required,
	// and at least 32 characters, whenever SITEBIN_STACK_URL is set: a stack
	// instance that cannot take a deletion order is one that leaves personal
	// data behind after the identity is gone.
	GDPRSecret string

	byID map[string]Tier
}

// MinGDPRSecretLen is the stack's own floor for the webhook secret
// (`gdpr.webhookSecret`, min 32); refusing a shorter one here rather than in
// the registration goroutine is what makes the mistake visible at boot.
const MinGDPRSecretLen = 32

// PayGateConfig points Sitebin at a SaaS-Stack PayGate as the subscription
// source of truth: accounts signed in via the generic OIDC provider get their
// tier from PayGate (stack tier ids must match tiers.json ids).
type PayGateConfig struct {
	URL      string        // PayGate base URL, no trailing slash
	AppID    string        // the stack app id Sitebin is onboarded as
	APIKey   string        // stack app API key (ssk_…)
	CacheTTL time.Duration // per-user tier cache (default 5m)
	// PlanURL is the stack's hosted manage-my-plan page for this app —
	// current plan, the plans it could move to, change, cancel, resume,
	// invoices — DERIVED from the OIDC issuer and the app id exactly as the
	// stack's own SDK does (`planUrl()` in packages/oidc): the issuer's origin
	// is the stack portal's origin, and the page is `/apps/<appId>/plan` on
	// it. Empty when no generic OIDC issuer is configured, in which case there
	// is no stack identity to show a plan for anyway.
	//
	// Derived rather than configured on purpose. It used to be an operator
	// setting (a "manage URL"), which is one more value that can disagree
	// with the issuer the instance actually signs in against; the stack
	// defines where the page is, and so does this.
	PlanURL string
}

// StackConfig makes this instance register itself with the IT-Trail SaaS
// Stack on every start. nil = no self-registration, which is the default and
// what every instance not run against that stack does.
//
// AdminKey is the stack's PLATFORM_ADMIN_KEY. It is a master credential — an
// app holding it can act on any app in the stack — and that is the deliberate
// trade for a deploy that needs no console visit. Keep it in a secret store,
// never in an image.
type StackConfig struct {
	URL      string // platform-api base URL, no trailing slash
	AppID    string // the app id to register as
	AdminKey string // the stack's platform admin key
	// Licensing is what a Sitebin Enterprise licence is worth, declared to the
	// stack so it can mint licences with the right entitlements. nil = declare
	// nothing, and the stack keeps whatever it already holds.
	Licensing *StackLicensing
	// Consents are this deployment's own consent documents — its terms of
	// service, its data processing agreement — declared to the stack so its
	// consent gate can ask for them, in this order, after the platform's own
	// document. nil = declare nothing, and the stack keeps whatever it
	// already holds.
	Consents []StackConsent
}

// StackConsent is one entry of the registration payload's `consents` block:
// one document the stack's consent gate shows inside the sign-in, after the
// platform's own (which no app may declare). Sitebin renders nothing and
// stores nothing; declaring the list is the whole integration.
//
// It comes from SITEBIN_STACK_CONSENTS rather than from a constant in this
// repo for exactly the reason SITEBIN_STACK_LICENSING does: these are one
// deployment's commercial and legal documents — sitebin.io's, on the hosted
// instance — they change when those pages change, and this repo is public and
// released on its own schedule. A self-hosted Sitebin has its own terms or
// none, and neither is ours to write.
//
// The shape is the stack's, verbatim, so an operator can paste the list the
// stack's README documents:
//
//	[{"key":"terms","version":"2026-09-08","url":"https://sitebin.io/terms/","title":{"en":"Sitebin Terms of Service"}},
//	 {"key":"dpa","version":"2026-09-08","url":"https://sitebin.io/dpa/","title":{"en":"Data Processing Agreement"}}]
//
// Key is the document's IDENTITY within this app, for ever: changing it does
// not rename a document, it declares a new one and asks everybody again.
// Version is opaque and RAISING IT ASKS EVERY USER AGAIN, for that document
// alone; it is also immutable, because re-declaring a version the stack has
// already recorded with different content is refused outright, and
// registration runs on every boot where a warning would scroll past. Required
// defaults to true on the stack; a false entry is shown and recorded and
// does not block, which is what a marketing consent needs. The list's order
// is the presentation order.
type StackConsent struct {
	Key     string            `json:"key"`
	Version string            `json:"version"`
	URL     string            `json:"url"`
	Title   map[string]string `json:"title,omitempty"`
	// Required is a pointer so that "not stated" is sent as nothing at all
	// and the stack applies its own default (true), rather than Sitebin
	// restating a default it does not own.
	Required *bool `json:"required,omitempty"`
}

// consentKeyRe is the stack's CONSENT_KEY_PATTERN, verbatim: a key is a slug
// the gate puts in form values and record rows, not a heading.
var consentKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// maxStackConsents is the stack's MAX_APP_CONSENT_DOCUMENTS.
const maxStackConsents = 20

// StackLicensing is the registration payload's `licensing` block: the
// entitlements each licence PLAN carries and how long a lapsed licence stays
// usable. The stack is the issuer, so it has to be told; Sitebin itself only
// ever verifies what comes back.
//
// It comes from SITEBIN_STACK_LICENSING rather than from a table in this
// repo, for the same reason tier amounts do (CLAUDE.md, "Tier prices differ by
// backend"): these numbers are the commercial terms of the Enterprise price
// list on sitebin.io, they change when that page changes, and this repo is
// public and released on its own schedule. Baking them in would mean a
// tagged release every time a plan's domain cap moves, and would publish the
// vendor's price list as source. Only the instance holding the stack's
// platform admin key can register at all, so only the vendor's own deployment
// ever sets this.
//
// The shape is the stack's, verbatim, so an operator can paste the block the
// stack's README documents:
//
//	{"graceMonths":3,"plans":{"team":{"max_custom_domains":25},"platform":{}}}
//
// A plan absent from `plans` carries no entitlements, which means UNLIMITED,
// not zero — the same rule ee/licensing applies to a licence that arrives
// without them. Never write a number nobody configured.
type StackLicensing struct {
	GraceMonths int                       `json:"graceMonths,omitempty"`
	Plans       map[string]map[string]int `json:"plans,omitempty"`
}

// BillingConfig holds payment-provider credentials. Prices per tier come from
// the tier config (Tier.Price).
type BillingConfig struct {
	Stripe *StripeConfig
	Paddle *PaddleConfig
}

type StripeConfig struct {
	SecretKey     string
	WebhookSecret string
}

type PaddleConfig struct {
	APIKey        string
	WebhookSecret string
	Sandbox       bool
}

// SMTPConfig configures outbound email (verification, password reset, notices).
type SMTPConfig struct {
	Host string
	Port int
	User string
	Pass string
	From string
	TLS  bool // implicit TLS (port 465); otherwise STARTTLS
}

// OAuthEnabled reports whether any OAuth provider is configured.
func (c Config) OAuthEnabled() bool {
	return c.Google != nil || c.Microsoft != nil || c.OIDC != nil
}

// EmailEnabled reports whether SMTP is configured.
func (c Config) EmailEnabled() bool { return c.SMTP != nil }

// BillingEnabled reports whether a backend can sell a tier.
func (c Config) BillingEnabled() bool { return c.BillingBackend != "" }

// Backend names, as accepted by SITEBIN_BILLING.
const (
	BackendStripe  = "stripe"
	BackendPaddle  = "paddle"
	BackendPayGate = "paygate"
)

// Enabled reports whether account gating is active.
func (c Config) Enabled() bool { return c.Mode != ModeOpen }

// Tier looks up a tier by id.
func (c Config) Tier(id string) (Tier, bool) { t, ok := c.byID[id]; return t, ok }

// Load parses the enterprise configuration. readFile reads SITEBIN_TIERS_FILE
// (injected for testability).
func Load(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	cfg := Config{Mode: ModeOpen, byID: map[string]Tier{}}

	mode := strings.ToLower(strings.TrimSpace(getenv("SITEBIN_ACCOUNT_MODE")))
	if mode == "" {
		mode = string(ModeOpen)
	}
	switch Mode(mode) {
	case ModeOpen, ModeAccounts, ModeTiers:
		cfg.Mode = Mode(mode)
	default:
		return cfg, fmt.Errorf("SITEBIN_ACCOUNT_MODE %q is invalid (want open|accounts|tiers)", mode)
	}

	cfg.SelfSelect = boolish(getenv("SITEBIN_TIER_SELF_SELECT"))
	cfg.AdminAccounts = emailList(getenv("SITEBIN_ADMIN_ACCOUNTS"))
	cfg.AllowAnon = boolish(getenv("SITEBIN_ALLOW_ANON_CREATE"))
	cfg.LocalAuth = true
	if v := strings.ToLower(strings.TrimSpace(getenv("SITEBIN_LOCAL_AUTH"))); v != "" {
		cfg.LocalAuth = boolish(v)
	}
	if url := strings.TrimSpace(getenv("SITEBIN_STACK_URL")); url != "" {
		appID := strings.TrimSpace(getenv("SITEBIN_STACK_APP_ID"))
		key := strings.TrimSpace(getenv("SITEBIN_STACK_ADMIN_KEY"))
		// The admin key acts on every app on the stack. A file (a docker
		// secret, a mounted volume) keeps it out of the container's
		// environment, where docker inspect and every child process read it.
		if path := strings.TrimSpace(getenv("SITEBIN_STACK_ADMIN_KEY_FILE")); path != "" {
			b, err := readFile(path)
			if err != nil {
				return cfg, fmt.Errorf("SITEBIN_STACK_ADMIN_KEY_FILE: %w", err)
			}
			key = strings.TrimSpace(string(b))
		}
		if appID == "" || key == "" {
			return cfg, fmt.Errorf("SITEBIN_STACK_URL needs SITEBIN_STACK_APP_ID and SITEBIN_STACK_ADMIN_KEY (or SITEBIN_STACK_ADMIN_KEY_FILE)")
		}
		cfg.StackRegistration = &StackConfig{
			URL: strings.TrimRight(url, "/"), AppID: appID, AdminKey: key,
		}
		if raw := strings.TrimSpace(getenv("SITEBIN_STACK_LICENSING")); raw != "" {
			var lic StackLicensing
			if err := json.Unmarshal([]byte(raw), &lic); err != nil {
				return cfg, fmt.Errorf("SITEBIN_STACK_LICENSING: %w", err)
			}
			// A block that decodes to nothing is an operator mistake worth
			// catching here: declaring it empty would be indistinguishable from
			// not declaring it, and the stack MERGES, so the silent outcome is
			// "the entitlements you thought you just set are the old ones".
			if lic.GraceMonths == 0 && len(lic.Plans) == 0 {
				return cfg, fmt.Errorf("SITEBIN_STACK_LICENSING declares neither graceMonths nor plans")
			}
			if lic.GraceMonths < 0 {
				return cfg, fmt.Errorf("SITEBIN_STACK_LICENSING: graceMonths must not be negative")
			}
			cfg.StackRegistration.Licensing = &lic
		}
		if raw := strings.TrimSpace(getenv("SITEBIN_STACK_CONSENTS")); raw != "" {
			consents, err := parseConsents(raw)
			if err != nil {
				return cfg, fmt.Errorf("SITEBIN_STACK_CONSENTS: %w", err)
			}
			cfg.StackRegistration.Consents = consents
		}
	}
	// The GDPR webhook secret. Parsed outside the SITEBIN_STACK_URL block so an
	// operator who registered the app by hand can still take the stack's
	// deletion and export calls; required inside it, because a self-registered
	// instance that declares no `gdpr` block is one the stack can never erase.
	cfg.GDPRSecret = strings.TrimSpace(getenv("SITEBIN_STACK_GDPR_SECRET"))
	if cfg.GDPRSecret != "" && len(cfg.GDPRSecret) < MinGDPRSecretLen {
		return cfg, fmt.Errorf("SITEBIN_STACK_GDPR_SECRET must be at least %d characters (the stack refuses a shorter one)", MinGDPRSecretLen)
	}
	if cfg.StackRegistration != nil && cfg.GDPRSecret == "" {
		return cfg, fmt.Errorf("SITEBIN_STACK_GDPR_SECRET is required with SITEBIN_STACK_URL: the stack must be able to order a deletion and an export")
	}
	cfg.DefaultTier = strings.TrimSpace(getenv("SITEBIN_DEFAULT_TIER"))
	cfg.AnonTier = strings.TrimSpace(getenv("SITEBIN_ANON_TIER"))

	if id := strings.TrimSpace(getenv("SITEBIN_OAUTH_GOOGLE_CLIENT_ID")); id != "" {
		cfg.Google = &OAuthProvider{ClientID: id, ClientSecret: getenv("SITEBIN_OAUTH_GOOGLE_CLIENT_SECRET")}
	}
	if id := strings.TrimSpace(getenv("SITEBIN_OAUTH_MICROSOFT_CLIENT_ID")); id != "" {
		tenant := strings.TrimSpace(getenv("SITEBIN_OAUTH_MICROSOFT_TENANT"))
		if tenant == "" {
			tenant = "common"
		}
		cfg.Microsoft = &OAuthProvider{ClientID: id, ClientSecret: getenv("SITEBIN_OAUTH_MICROSOFT_CLIENT_SECRET"), Tenant: tenant}
	}

	if issuer := strings.TrimSpace(getenv("SITEBIN_OAUTH_OIDC_ISSUER")); issuer != "" {
		if !strings.HasPrefix(issuer, "https://") && !strings.HasPrefix(issuer, "http://") {
			return cfg, fmt.Errorf("SITEBIN_OAUTH_OIDC_ISSUER: %q is not an http(s) URL", issuer)
		}
		clientID := strings.TrimSpace(getenv("SITEBIN_OAUTH_OIDC_CLIENT_ID"))
		if clientID == "" {
			return cfg, fmt.Errorf("SITEBIN_OAUTH_OIDC_CLIENT_ID is required with SITEBIN_OAUTH_OIDC_ISSUER")
		}
		label := strings.TrimSpace(getenv("SITEBIN_OAUTH_OIDC_LABEL"))
		if label == "" {
			label = "SSO"
		}
		discovery := strings.TrimSpace(getenv("SITEBIN_OAUTH_OIDC_DISCOVERY_URL"))
		if discovery != "" {
			if !strings.HasPrefix(discovery, "https://") && !strings.HasPrefix(discovery, "http://") {
				return cfg, fmt.Errorf("SITEBIN_OAUTH_OIDC_DISCOVERY_URL: %q is not an http(s) URL", discovery)
			}
			discovery = strings.TrimRight(discovery, "/")
			discovery = strings.TrimSuffix(discovery, "/.well-known/openid-configuration")
			discovery = strings.TrimRight(discovery, "/")
		}
		cfg.OIDC = &GenericOIDC{
			Issuer: strings.TrimRight(issuer, "/"), DiscoveryURL: discovery, ClientID: clientID,
			ClientSecret: getenv("SITEBIN_OAUTH_OIDC_CLIENT_SECRET"), Label: label,
		}
	}

	if host := strings.TrimSpace(getenv("SITEBIN_SMTP_HOST")); host != "" {
		port := 587
		if v := strings.TrimSpace(getenv("SITEBIN_SMTP_PORT")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return cfg, fmt.Errorf("SITEBIN_SMTP_PORT: %q is not a number", v)
			}
			port = n
		}
		from := strings.TrimSpace(getenv("SITEBIN_SMTP_FROM"))
		if from == "" {
			return cfg, fmt.Errorf("SITEBIN_SMTP_FROM is required when SITEBIN_SMTP_HOST is set")
		}
		cfg.SMTP = &SMTPConfig{
			Host: host, Port: port, From: from,
			User: getenv("SITEBIN_SMTP_USER"), Pass: getenv("SITEBIN_SMTP_PASS"),
			TLS: boolish(getenv("SITEBIN_SMTP_TLS")),
		}
	}

	var billing BillingConfig
	if k := strings.TrimSpace(getenv("SITEBIN_STRIPE_SECRET_KEY")); k != "" {
		billing.Stripe = &StripeConfig{SecretKey: k, WebhookSecret: getenv("SITEBIN_STRIPE_WEBHOOK_SECRET")}
	}
	if k := strings.TrimSpace(getenv("SITEBIN_PADDLE_API_KEY")); k != "" {
		billing.Paddle = &PaddleConfig{
			APIKey: k, WebhookSecret: getenv("SITEBIN_PADDLE_WEBHOOK_SECRET"),
			Sandbox: boolish(getenv("SITEBIN_PADDLE_SANDBOX")),
		}
	}
	if billing.Stripe != nil || billing.Paddle != nil {
		cfg.Billing = &billing
	}

	pgURL := strings.TrimSpace(getenv("SITEBIN_PAYGATE_URL"))
	pgApp := strings.TrimSpace(getenv("SITEBIN_PAYGATE_APP_ID"))
	pgKey := strings.TrimSpace(getenv("SITEBIN_PAYGATE_API_KEY"))
	if pgURL != "" || pgApp != "" || pgKey != "" {
		if pgURL == "" || pgApp == "" || pgKey == "" {
			return cfg, fmt.Errorf("SITEBIN_PAYGATE_URL, SITEBIN_PAYGATE_APP_ID and SITEBIN_PAYGATE_API_KEY must be set together")
		}
		if !strings.HasPrefix(pgURL, "https://") && !strings.HasPrefix(pgURL, "http://") {
			return cfg, fmt.Errorf("SITEBIN_PAYGATE_URL: %q is not an http(s) URL", pgURL)
		}
		if cfg.Mode != ModeTiers {
			return cfg, fmt.Errorf("PayGate integration requires SITEBIN_ACCOUNT_MODE=tiers (got %q): PayGate maps stack subscriptions onto tiers", cfg.Mode)
		}
		ttl := 5 * time.Minute
		if v := strings.TrimSpace(getenv("SITEBIN_PAYGATE_CACHE_TTL")); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return cfg, fmt.Errorf("SITEBIN_PAYGATE_CACHE_TTL: %q is not a positive duration", v)
			}
			ttl = d
		}
		cfg.PayGate = &PayGateConfig{
			URL: strings.TrimRight(pgURL, "/"), AppID: pgApp, APIKey: pgKey,
			CacheTTL: ttl,
		}
		if cfg.OIDC != nil {
			cfg.PayGate.PlanURL = stackPlanURL(cfg.OIDC.Issuer, pgApp)
		}
	}

	// Lower-cased like SITEBIN_ACCOUNT_MODE and SITEBIN_VIEW_ACCESS. It used to
	// be compared as typed, so SITEBIN_BILLING=Stripe was a hard boot failure
	// on an otherwise correct configuration.
	if err := resolveBillingBackend(&cfg, strings.ToLower(strings.TrimSpace(getenv("SITEBIN_BILLING")))); err != nil {
		return cfg, err
	}

	raw, err := tierBytes(getenv, readFile)
	if err != nil {
		return cfg, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg.Tiers); err != nil {
			return cfg, fmt.Errorf("tiers config: %w", err)
		}
	}
	for _, t := range cfg.Tiers {
		if strings.TrimSpace(t.ID) == "" {
			return cfg, fmt.Errorf("tiers config: a tier has an empty id")
		}
		if _, dup := cfg.byID[t.ID]; dup {
			return cfg, fmt.Errorf("tiers config: duplicate tier id %q", t.ID)
		}
		cfg.byID[t.ID] = t
	}

	if err := validateBackendPrices(&cfg); err != nil {
		return cfg, err
	}

	if !cfg.LocalAuth && !cfg.OAuthEnabled() {
		return cfg, fmt.Errorf("SITEBIN_LOCAL_AUTH=false requires at least one OAuth provider (SITEBIN_OAUTH_*), otherwise nobody can sign in")
	}

	if cfg.Mode == ModeTiers {
		if len(cfg.Tiers) == 0 {
			return cfg, fmt.Errorf("SITEBIN_ACCOUNT_MODE=tiers requires SITEBIN_TIERS or SITEBIN_TIERS_FILE")
		}
		if cfg.DefaultTier == "" {
			return cfg, fmt.Errorf("SITEBIN_DEFAULT_TIER is required in tiers mode")
		}
		if _, ok := cfg.byID[cfg.DefaultTier]; !ok {
			return cfg, fmt.Errorf("SITEBIN_DEFAULT_TIER %q is not one of the configured tiers", cfg.DefaultTier)
		}
		if cfg.AnonTier != "" {
			if _, ok := cfg.byID[cfg.AnonTier]; !ok {
				return cfg, fmt.Errorf("SITEBIN_ANON_TIER %q is not one of the configured tiers", cfg.AnonTier)
			}
		}
	}
	return cfg, nil
}

func tierBytes(getenv func(string) string, readFile func(string) ([]byte, error)) ([]byte, error) {
	if inline := strings.TrimSpace(getenv("SITEBIN_TIERS")); inline != "" {
		return []byte(inline), nil
	}
	if path := strings.TrimSpace(getenv("SITEBIN_TIERS_FILE")); path != "" {
		b, err := readFile(path)
		if err != nil {
			return nil, fmt.Errorf("SITEBIN_TIERS_FILE: %w", err)
		}
		return b, nil
	}
	return nil, nil
}

func boolish(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// emailList parses a comma-separated address list, lowercasing and trimming
// each entry and dropping empties, so a trailing comma or a stray space in the
// container's environment cannot quietly cost an operator their access.
func emailList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if e := strings.ToLower(strings.TrimSpace(part)); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// parseConsents decodes SITEBIN_STACK_CONSENTS and refuses at boot what the
// stack would refuse in the registration goroutine, where the failure would
// only be a log line: a document with no key, version or URL, a key the gate
// cannot put in a form value, two entries claiming one key, more than the
// stack's cap, and an EMPTY list — which is not "declare nothing" (that is the
// variable being unset) but "this app asks for nothing", a state the stack
// distinguishes and an operator clears on the stack rather than by accident.
func parseConsents(raw string) ([]StackConsent, error) {
	var consents []StackConsent
	if err := json.Unmarshal([]byte(raw), &consents); err != nil {
		return nil, err
	}
	if len(consents) == 0 {
		return nil, fmt.Errorf("declares no documents; unset it to declare nothing, or clear the app's consents on the stack")
	}
	if len(consents) > maxStackConsents {
		return nil, fmt.Errorf("declares %d documents; the stack accepts at most %d", len(consents), maxStackConsents)
	}
	seen := map[string]bool{}
	for i := range consents {
		c := &consents[i]
		c.Key = strings.TrimSpace(c.Key)
		c.Version = strings.TrimSpace(c.Version)
		c.URL = strings.TrimSpace(c.URL)
		if !consentKeyRe.MatchString(c.Key) {
			return nil, fmt.Errorf("document %d: key %q must be lowercase letters, digits, \"-\" or \"_\" (e.g. \"terms\", \"dpa\")", i+1, c.Key)
		}
		if seen[c.Key] {
			return nil, fmt.Errorf("key %q is declared twice; a key IS the document within this app", c.Key)
		}
		seen[c.Key] = true
		if c.Version == "" || c.URL == "" {
			return nil, fmt.Errorf("document %q: both version and url are required", c.Key)
		}
		if !strings.HasPrefix(c.URL, "https://") && !strings.HasPrefix(c.URL, "http://") {
			return nil, fmt.Errorf("document %q: url %q is not an http(s) URL", c.Key, c.URL)
		}
	}
	return consents, nil
}

// stackPlanURL is the stack's hosted plan page for one app, built exactly as
// the stack's own SDK builds it (`planUrl()` in packages/oidc/src/urls.ts):
// the portal's origin, then `/apps/<appId>/plan`. The portal's origin is the
// issuer's origin — `https://auth.<domain>` serves both the realm and the
// stack portal — so the issuer is the one value it is derived from.
func stackPlanURL(issuer, appID string) string {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/apps/" + url.PathEscape(appID) + "/plan"
}

// AccountConsoleURL is the identity provider's account console — password,
// sessions, second factors, linked identities, data export and account
// deletion — for a user who signed in through the generic OIDC provider.
// Built as the stack's own SDK builds it (`accountUrl()` in
// packages/oidc/src/urls.ts): `<issuer>/account/`, with `referrer` naming the
// client so the console brands itself for this app and offers a way back.
// Empty when no generic OIDC issuer is configured.
//
// It is Keycloak's convention and it works for any Keycloak-backed issuer, not
// only the stack; a plain OIDC provider that has no console at that path
// simply answers 404 to a link the operator can see on the dashboard.
func (c Config) AccountConsoleURL() string {
	if c.OIDC == nil {
		return ""
	}
	return strings.TrimRight(c.OIDC.Issuer, "/") + "/account/?referrer=" + url.QueryEscape(c.OIDC.ClientID)
}

// StackDeletion reports whether the account console, not Sitebin, is where an
// OIDC-signed-in user deletes their account. It is true only when the stack
// can actually order the deletion back — the GDPR webhook secret is set —
// because sending a user to the console when nothing will ever call Sitebin
// afterwards would leave their sites and tokens behind with no owner to be
// found. Local accounts always delete locally: the stack has never heard of
// them.
func (c Config) StackDeletion() bool {
	return c.OIDC != nil && c.GDPRSecret != ""
}

// resolveBillingBackend settles which single backend may sell a tier.
//
// An explicit SITEBIN_BILLING always wins and must be configured. With no
// explicit choice the backend is inferred, but ONLY when exactly one is
// configured: two configured backends and no choice is a startup error rather
// than a guess, because guessing which processor charges customers is not a
// thing to be relaxed about. It is the same instinct as refusing to start when
// SITEBIN_ANON_TIER names a tier that does not exist.
func resolveBillingBackend(cfg *Config, want string) error {
	have := map[string]bool{}
	if cfg.Billing != nil && cfg.Billing.Stripe != nil {
		have[BackendStripe] = true
	}
	if cfg.Billing != nil && cfg.Billing.Paddle != nil {
		have[BackendPaddle] = true
	}
	if cfg.PayGate != nil {
		have[BackendPayGate] = true
	}

	if want != "" {
		switch want {
		case BackendStripe, BackendPaddle, BackendPayGate:
		default:
			return fmt.Errorf("SITEBIN_BILLING: %q is not one of stripe, paddle, paygate", want)
		}
		if !have[want] {
			return fmt.Errorf("SITEBIN_BILLING=%s but %s is not configured", want, want)
		}
		cfg.BillingBackend = want
		return nil
	}

	var configured []string
	for _, name := range []string{BackendStripe, BackendPaddle, BackendPayGate} {
		if have[name] {
			configured = append(configured, name)
		}
	}
	switch len(configured) {
	case 0:
		return nil
	case 1:
		cfg.BillingBackend = configured[0]
		return nil
	default:
		return fmt.Errorf("%s are all configured; set SITEBIN_BILLING to the one that may charge customers",
			strings.Join(configured, ", "))
	}
}

// validateBackendPrices refuses to start when a sellable tier is missing the
// price identifier the ACTIVE direct backend needs to sell it.
//
// The failure it removes is a customer clicking "Upgrade to Pro" and getting
// "Checkout unavailable": a direct backend's CheckoutURL can only fail there,
// at the click, because the price id lives in tiers.json and nothing looked at
// it until then. An operator can fix a boot error; a customer cannot.
//
// Only Stripe and Paddle are checked. PayGate needs no id — it is handed the
// amount and creates the product itself — and an unsellable tier there is a
// deliberate state (a free tier, a plan not yet priced), not a mistake.
func validateBackendPrices(cfg *Config) error {
	var field string
	var get func(*Price) string
	switch cfg.BillingBackend {
	case BackendStripe:
		field, get = "price.stripe", func(p *Price) string { return p.Stripe }
	case BackendPaddle:
		field, get = "price.paddle", func(p *Price) string { return p.Paddle }
	default:
		return nil
	}
	for _, t := range cfg.Tiers {
		// Paid() is what puts an Upgrade button on the dashboard, so it is
		// exactly the set that has to be sellable.
		if !t.Paid() {
			continue
		}
		if strings.TrimSpace(get(t.Price)) == "" {
			return fmt.Errorf("tiers config: tier %q is sellable but has no %s, and SITEBIN_BILLING=%s cannot sell it; add the id or remove the tier's price",
				t.ID, field, cfg.BillingBackend)
		}
	}
	return nil
}
