//go:build ee

package eeconfig

import (
	"encoding/json"
	"fmt"
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

// Price maps a paid tier to the provider price/product identifiers.
type Price struct {
	Stripe  string `json:"stripe"`
	Paddle  string `json:"paddle"`
	Display string `json:"display"`
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
	Price         *Price `json:"price,omitempty"`
}

// Paid reports whether the tier requires payment to activate.
func (t Tier) Paid() bool { return t.Price != nil && (t.Price.Stripe != "" || t.Price.Paddle != "") }

// OAuthProvider holds one OIDC provider's credentials.
type OAuthProvider struct {
	ClientID     string
	ClientSecret string
	Tenant       string // Microsoft only (default "common")
}

// GenericOIDC configures sign-in against any spec-compliant OIDC issuer —
// Keycloak, Okta, Authentik, or the IT-Trail SaaS Stack's Auth Gateway
// (issuer https://auth.<stack-domain>/api/v1/<app-id>).
type GenericOIDC struct {
	Issuer       string
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
	AllowAnon   bool   // in accounts mode, still allow anonymous creation
	LocalAuth   bool   // email+password auth (default true; false = SSO only)

	Google    *OAuthProvider // nil = not configured
	Microsoft *OAuthProvider
	OIDC      *GenericOIDC // generic issuer (saas-stack, Keycloak, Okta, …)

	SMTP    *SMTPConfig    // nil = email disabled
	Billing *BillingConfig // nil = billing disabled
	PayGate *PayGateConfig // nil = built-in billing / stored tiers only

	byID map[string]Tier
}

// PayGateConfig points Sitebin at a SaaS-Stack PayGate as the subscription
// source of truth: accounts signed in via the generic OIDC provider get their
// tier from PayGate (stack tier ids must match tiers.json ids).
type PayGateConfig struct {
	URL       string        // PayGate base URL, no trailing slash
	AppID     string        // the stack app id Sitebin is onboarded as
	APIKey    string        // stack app API key (ssk_…)
	CacheTTL  time.Duration // per-user tier cache (default 5m)
	ManageURL string        // optional dashboard "manage subscription" link
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

// BillingEnabled reports whether any payment provider is configured.
func (c Config) BillingEnabled() bool {
	return c.Billing != nil && (c.Billing.Stripe != nil || c.Billing.Paddle != nil)
}

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
	cfg.AllowAnon = boolish(getenv("SITEBIN_ALLOW_ANON_CREATE"))
	cfg.LocalAuth = true
	if v := strings.ToLower(strings.TrimSpace(getenv("SITEBIN_LOCAL_AUTH"))); v != "" {
		cfg.LocalAuth = boolish(v)
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
		cfg.OIDC = &GenericOIDC{
			Issuer: strings.TrimRight(issuer, "/"), ClientID: clientID,
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
			CacheTTL: ttl, ManageURL: strings.TrimSpace(getenv("SITEBIN_PAYGATE_MANAGE_URL")),
		}
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
