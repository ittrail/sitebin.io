//go:build ee

package eeconfig

import (
	"errors"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func noFile(string) ([]byte, error) { return nil, errors.New("no file") }

const twoTiers = `[
  {"id":"free","label":"Free","max_site_bytes":104857600,"max_files":100,"max_sites":3,"webdav":false,"custom_domains":0,"max_expiry_days":30},
  {"id":"pro","label":"Pro","max_site_bytes":5368709120,"max_files":5000,"max_sites":100,"webdav":true,"custom_domains":10,"max_expiry_days":0,"price":{"stripe":"price_1","paddle":"pri_1","display":"€9/mo"}}
]`

func TestLoadDefaultsToOpen(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), noFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeOpen || cfg.Enabled() {
		t.Errorf("default mode = %q, enabled=%v", cfg.Mode, cfg.Enabled())
	}
}

func TestLoadAccountsMode(t *testing.T) {
	cfg, err := Load(env(map[string]string{"SITEBIN_ACCOUNT_MODE": "accounts"}), noFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeAccounts || !cfg.Enabled() {
		t.Errorf("mode = %q", cfg.Mode)
	}
}

func TestLoadInvalidMode(t *testing.T) {
	_, err := Load(env(map[string]string{"SITEBIN_ACCOUNT_MODE": "wat"}), noFile)
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_ACCOUNT_MODE") {
		t.Fatalf("expected mode error, got %v", err)
	}
}

func TestLoadTiersInline(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE":     "tiers",
		"SITEBIN_TIERS":            twoTiers,
		"SITEBIN_DEFAULT_TIER":     "free",
		"SITEBIN_ANON_TIER":        "free",
		"SITEBIN_TIER_SELF_SELECT": "true",
	}), noFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Tiers) != 2 {
		t.Fatalf("tiers = %d", len(cfg.Tiers))
	}
	pro, ok := cfg.Tier("pro")
	if !ok || !pro.WebDAV || pro.MaxSites != 100 || pro.Price == nil || pro.Price.Stripe != "price_1" {
		t.Errorf("pro tier wrong: %+v", pro)
	}
	if !cfg.SelfSelect {
		t.Error("self-select not parsed")
	}
	if cfg.DefaultTier != "free" || cfg.AnonTier != "free" {
		t.Errorf("default/anon = %q/%q", cfg.DefaultTier, cfg.AnonTier)
	}
}

func TestLoadTiersFromFile(t *testing.T) {
	read := func(p string) ([]byte, error) {
		if p != "/etc/sitebin/tiers.json" {
			return nil, errors.New("unexpected path " + p)
		}
		return []byte(twoTiers), nil
	}
	cfg, err := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE": "tiers",
		"SITEBIN_TIERS_FILE":   "/etc/sitebin/tiers.json",
		"SITEBIN_DEFAULT_TIER": "pro",
	}), read)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Tier("free"); !ok {
		t.Error("file tiers not loaded")
	}
}

func TestTiersModeRequiresTiers(t *testing.T) {
	_, err := Load(env(map[string]string{"SITEBIN_ACCOUNT_MODE": "tiers"}), noFile)
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("expected tiers-required error, got %v", err)
	}
}

func TestTiersModeRequiresValidDefault(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE": "tiers",
		"SITEBIN_TIERS":        twoTiers,
		"SITEBIN_DEFAULT_TIER": "enterprise",
	}), noFile)
	if err == nil || !strings.Contains(err.Error(), "DEFAULT_TIER") {
		t.Fatalf("expected default-tier error, got %v", err)
	}
}

func TestAnonTierMustExist(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE": "tiers",
		"SITEBIN_TIERS":        twoTiers,
		"SITEBIN_DEFAULT_TIER": "free",
		"SITEBIN_ANON_TIER":    "ghost",
	}), noFile)
	if err == nil || !strings.Contains(err.Error(), "ANON_TIER") {
		t.Fatalf("expected anon-tier error, got %v", err)
	}
}

func TestDuplicateTierRejected(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE": "accounts",
		"SITEBIN_TIERS":        `[{"id":"a"},{"id":"a"}]`,
	}), noFile)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestOAuthParsing(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE":                  "accounts",
		"SITEBIN_OAUTH_GOOGLE_CLIENT_ID":        "gid",
		"SITEBIN_OAUTH_GOOGLE_CLIENT_SECRET":    "gsecret",
		"SITEBIN_OAUTH_MICROSOFT_CLIENT_ID":     "mid",
		"SITEBIN_OAUTH_MICROSOFT_CLIENT_SECRET": "msecret",
		"SITEBIN_OAUTH_MICROSOFT_TENANT":        "my-tenant",
	}), noFile)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.OAuthEnabled() {
		t.Fatal("OAuth should be enabled")
	}
	if cfg.Google == nil || cfg.Google.ClientID != "gid" || cfg.Google.ClientSecret != "gsecret" {
		t.Errorf("google config wrong: %+v", cfg.Google)
	}
	if cfg.Microsoft == nil || cfg.Microsoft.ClientID != "mid" || cfg.Microsoft.Tenant != "my-tenant" {
		t.Errorf("microsoft config wrong: %+v", cfg.Microsoft)
	}
}

func TestOAuthDefaultsTenantCommon(t *testing.T) {
	cfg, _ := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE":              "accounts",
		"SITEBIN_OAUTH_MICROSOFT_CLIENT_ID": "mid",
	}), noFile)
	if cfg.Microsoft == nil || cfg.Microsoft.Tenant != "common" {
		t.Errorf("tenant default = %+v", cfg.Microsoft)
	}
}

func TestOAuthDisabledByDefault(t *testing.T) {
	cfg, _ := Load(env(map[string]string{"SITEBIN_ACCOUNT_MODE": "accounts"}), noFile)
	if cfg.OAuthEnabled() {
		t.Error("OAuth should be off with no config")
	}
}

func TestBadTierJSON(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE": "accounts",
		"SITEBIN_TIERS":        `{not json`,
	}), noFile)
	if err == nil {
		t.Fatal("expected json error")
	}
}

func TestLoadGenericOIDC(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE":             "accounts",
		"SITEBIN_OAUTH_OIDC_ISSUER":        "https://auth.stack.example/api/v1/sitebin",
		"SITEBIN_OAUTH_OIDC_CLIENT_ID":     "sitebin",
		"SITEBIN_OAUTH_OIDC_CLIENT_SECRET": "s3cret",
		"SITEBIN_OAUTH_OIDC_LABEL":         "IT-Trail SSO",
	}), noFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OIDC == nil {
		t.Fatal("OIDC not parsed")
	}
	if cfg.OIDC.Issuer != "https://auth.stack.example/api/v1/sitebin" ||
		cfg.OIDC.ClientID != "sitebin" || cfg.OIDC.ClientSecret != "s3cret" ||
		cfg.OIDC.Label != "IT-Trail SSO" {
		t.Fatalf("OIDC = %+v", cfg.OIDC)
	}
	if !cfg.OAuthEnabled() {
		t.Fatal("OAuthEnabled should include generic OIDC")
	}
}

func TestLoadGenericOIDCDefaultLabel(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_OAUTH_OIDC_ISSUER":    "https://idp.example",
		"SITEBIN_OAUTH_OIDC_CLIENT_ID": "cid",
	}), noFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OIDC.Label != "SSO" {
		t.Fatalf("default label = %q, want SSO", cfg.OIDC.Label)
	}
}

func TestLoadGenericOIDCInvalid(t *testing.T) {
	// missing client id
	_, err := Load(env(map[string]string{
		"SITEBIN_OAUTH_OIDC_ISSUER": "https://idp.example",
	}), noFile)
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_OAUTH_OIDC_CLIENT_ID") {
		t.Fatalf("want client-id error, got %v", err)
	}
	// bad issuer
	_, err = Load(env(map[string]string{
		"SITEBIN_OAUTH_OIDC_ISSUER":    "idp.example",
		"SITEBIN_OAUTH_OIDC_CLIENT_ID": "cid",
	}), noFile)
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_OAUTH_OIDC_ISSUER") {
		t.Fatalf("want issuer error, got %v", err)
	}
}

func TestLoadPayGate(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE":       "tiers",
		"SITEBIN_TIERS":              twoTiers,
		"SITEBIN_DEFAULT_TIER":       "free",
		"SITEBIN_PAYGATE_URL":        "https://paygate.stack.example/",
		"SITEBIN_PAYGATE_APP_ID":     "sitebin",
		"SITEBIN_PAYGATE_API_KEY":    "ssk_live_x",
		"SITEBIN_PAYGATE_CACHE_TTL":  "2m",
		"SITEBIN_PAYGATE_MANAGE_URL": "https://stack.example/account",
	}), noFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PayGate == nil {
		t.Fatal("PayGate not parsed")
	}
	if cfg.PayGate.URL != "https://paygate.stack.example" || cfg.PayGate.AppID != "sitebin" ||
		cfg.PayGate.APIKey != "ssk_live_x" || cfg.PayGate.CacheTTL.Minutes() != 2 ||
		cfg.PayGate.ManageURL != "https://stack.example/account" {
		t.Fatalf("PayGate = %+v", cfg.PayGate)
	}
}

func TestLoadPayGateDefaultsAndErrors(t *testing.T) {
	// default TTL 5m
	cfg, err := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE":    "tiers",
		"SITEBIN_TIERS":           twoTiers,
		"SITEBIN_DEFAULT_TIER":    "free",
		"SITEBIN_PAYGATE_URL":     "https://paygate.stack.example",
		"SITEBIN_PAYGATE_APP_ID":  "sitebin",
		"SITEBIN_PAYGATE_API_KEY": "ssk_live_x",
	}), noFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PayGate.CacheTTL.Minutes() != 5 {
		t.Fatalf("default TTL = %v", cfg.PayGate.CacheTTL)
	}

	// incomplete trio
	_, err = Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE": "tiers",
		"SITEBIN_TIERS":        twoTiers,
		"SITEBIN_DEFAULT_TIER": "free",
		"SITEBIN_PAYGATE_URL":  "https://paygate.stack.example",
	}), noFile)
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_PAYGATE") {
		t.Fatalf("want incomplete-paygate error, got %v", err)
	}

	// paygate requires tiers mode
	_, err = Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE":    "accounts",
		"SITEBIN_PAYGATE_URL":     "https://paygate.stack.example",
		"SITEBIN_PAYGATE_APP_ID":  "sitebin",
		"SITEBIN_PAYGATE_API_KEY": "ssk_live_x",
	}), noFile)
	if err == nil || !strings.Contains(err.Error(), "tiers") {
		t.Fatalf("want tiers-mode error, got %v", err)
	}

	// bad URL
	_, err = Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE":    "tiers",
		"SITEBIN_TIERS":           twoTiers,
		"SITEBIN_DEFAULT_TIER":    "free",
		"SITEBIN_PAYGATE_URL":     "paygate.stack.example",
		"SITEBIN_PAYGATE_APP_ID":  "sitebin",
		"SITEBIN_PAYGATE_API_KEY": "ssk_live_x",
	}), noFile)
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_PAYGATE_URL") {
		t.Fatalf("want url error, got %v", err)
	}
}

func TestLoadLocalAuthDisabled(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE":         "accounts",
		"SITEBIN_LOCAL_AUTH":           "false",
		"SITEBIN_OAUTH_OIDC_ISSUER":    "https://idp.example",
		"SITEBIN_OAUTH_OIDC_CLIENT_ID": "cid",
	}), noFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LocalAuth {
		t.Fatal("LocalAuth should be disabled")
	}
	// default: enabled
	cfg2, _ := Load(env(map[string]string{"SITEBIN_ACCOUNT_MODE": "accounts"}), noFile)
	if !cfg2.LocalAuth {
		t.Fatal("LocalAuth should default to enabled")
	}
	// disabling without any OAuth provider locks everyone out → error
	_, err = Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE": "accounts",
		"SITEBIN_LOCAL_AUTH":   "false",
	}), noFile)
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_LOCAL_AUTH") {
		t.Fatalf("want lockout error, got %v", err)
	}
}

// TestBillingBackendSelection pins the rule that decides who may charge a
// customer. The ambiguous case is the one that matters: two configured
// providers used to mean "prefer Stripe", a silent guess nobody stated.
func TestBillingBackendSelection(t *testing.T) {
	stripe := &BillingConfig{Stripe: &StripeConfig{SecretKey: "sk"}}
	both := &BillingConfig{Stripe: &StripeConfig{SecretKey: "sk"}, Paddle: &PaddleConfig{APIKey: "pk"}}
	pg := &PayGateConfig{URL: "https://pg.example", AppID: "sitebin", APIKey: "ssk"}

	cases := []struct {
		name    string
		billing *BillingConfig
		paygate *PayGateConfig
		want    string
		wantErr bool
	}{
		{name: "nothing configured", want: ""},
		{name: "one provider is inferred", billing: stripe, want: BackendStripe},
		{name: "paygate alone is inferred", paygate: pg, want: BackendPayGate},
		{name: "two providers refuse to guess", billing: both, wantErr: true},
		{name: "provider plus paygate refuses too", billing: stripe, paygate: pg, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Billing: tc.billing, PayGate: tc.paygate}
			err := resolveBillingBackend(&cfg, "")
			if tc.wantErr {
				if err == nil {
					t.Fatal("ambiguous billing config must not start")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.BillingBackend != tc.want {
				t.Errorf("backend = %q, want %q", cfg.BillingBackend, tc.want)
			}
		})
	}

	t.Run("explicit choice wins over ambiguity", func(t *testing.T) {
		cfg := Config{Billing: both, PayGate: pg}
		if err := resolveBillingBackend(&cfg, BackendPaddle); err != nil {
			t.Fatal(err)
		}
		if cfg.BillingBackend != BackendPaddle {
			t.Errorf("backend = %q, want paddle", cfg.BillingBackend)
		}
	})

	t.Run("explicit choice must be configured", func(t *testing.T) {
		cfg := Config{Billing: stripe}
		if err := resolveBillingBackend(&cfg, BackendPayGate); err == nil {
			t.Error("selecting an unconfigured backend must fail at startup, not at a customer's click")
		}
	})
}

// A PayGate catalogue carries amounts and no display string. Printing
// Price.Display then showed a blank price on every paid plan's Upgrade button.
func TestPriceLabelPrefersTheAmount(t *testing.T) {
	cases := []struct {
		name  string
		price *Price
		want  string
	}{
		{name: "nil price sells nothing", price: nil, want: ""},
		{name: "amount alone", price: &Price{Monthly: "6.00", Currency: "EUR"}, want: "€6.00/mo"},
		{name: "amount defaults to euro", price: &Price{Monthly: "6.00"}, want: "€6.00/mo"},
		{name: "dollars", price: &Price{Monthly: "7.00", Currency: "usd"}, want: "$7.00/mo"},
		{name: "pounds", price: &Price{Monthly: "5.00", Currency: "GBP"}, want: "£5.00/mo"},
		{name: "an unsold currency keeps its code", price: &Price{Monthly: "9.00", Currency: "CHF"}, want: "9.00 CHF/mo"},
		// The rule the design doc states: a shown price must never be able to
		// disagree with the charged one.
		{name: "the amount wins over a stale display string", price: &Price{Monthly: "6.00", Currency: "EUR", Display: "€9/mo"}, want: "€6.00/mo"},
		{name: "display is the fallback with no amount", price: &Price{Stripe: "price_1", Display: "€9/mo"}, want: "€9/mo"},
		{name: "a price id is not money", price: &Price{Stripe: "price_1"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.price.Label(); got != tc.want {
				t.Errorf("Label() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every tier in the shipped PayGate catalogue that is Paid() must show a price.
func TestPayGateCatalogueTiersAllPriced(t *testing.T) {
	const catalogue = `[
  {"id":"free","label":"Free","max_sites":10},
  {"id":"pro","label":"Pro","max_sites":100,"featured":true,"price":{"monthly":"6.00","annual":"60.00","currency":"EUR"}},
  {"id":"studio","label":"Studio","max_sites":500,"price":{"monthly":"19.00","annual":"190.00","currency":"EUR"}}
]`
	cfg, err := Load(env(map[string]string{
		"SITEBIN_ACCOUNT_MODE": "tiers",
		"SITEBIN_TIERS":        catalogue,
		"SITEBIN_DEFAULT_TIER": "free",
	}), noFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, tr := range cfg.Tiers {
		if tr.Paid() && tr.Price.Label() == "" {
			t.Errorf("tier %q is sellable but shows no price", tr.ID)
		}
	}
	pro, _ := cfg.Tier("pro")
	if !pro.Featured {
		t.Error("featured must survive the tier catalogue")
	}
	if free, _ := cfg.Tier("free"); free.Featured {
		t.Error("featured must not be invented for a tier that did not declare it")
	}
}

func TestBillingBackendIsCaseInsensitive(t *testing.T) {
	// SITEBIN_ACCOUNT_MODE and SITEBIN_VIEW_ACCESS both lower-case their input;
	// this one used to compare as typed, so "Stripe" was a hard boot failure.
	for _, v := range []string{"stripe", "Stripe", "STRIPE", " stripe "} {
		cfg, err := Load(env(map[string]string{
			"SITEBIN_ACCOUNT_MODE":      "tiers",
			"SITEBIN_TIERS":             twoTiers,
			"SITEBIN_DEFAULT_TIER":      "free",
			"SITEBIN_BILLING":           v,
			"SITEBIN_STRIPE_SECRET_KEY": "sk_test",
		}), noFile)
		if err != nil {
			t.Fatalf("SITEBIN_BILLING=%q: %v", v, err)
		}
		if cfg.BillingBackend != BackendStripe {
			t.Errorf("SITEBIN_BILLING=%q gave backend %q", v, cfg.BillingBackend)
		}
	}
}

// The design doc promised this check at startup so it cannot land at a
// customer's click on Upgrade, where the only honest answer is "unavailable".
func TestSellableTierMustCarryTheActiveBackendsPriceID(t *testing.T) {
	const noStripeID = `[
  {"id":"free","label":"Free","max_sites":3},
  {"id":"pro","label":"Pro","max_sites":100,"price":{"monthly":"9.00","currency":"EUR"}}
]`
	base := map[string]string{
		"SITEBIN_ACCOUNT_MODE": "tiers",
		"SITEBIN_DEFAULT_TIER": "free",
	}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	_, err := Load(env(with(map[string]string{
		"SITEBIN_TIERS":             noStripeID,
		"SITEBIN_STRIPE_SECRET_KEY": "sk_test",
	})), noFile)
	if err == nil || !strings.Contains(err.Error(), "price.stripe") {
		t.Fatalf("a stripe instance with an unsellable paid tier must refuse to start, got %v", err)
	}

	_, err = Load(env(with(map[string]string{
		"SITEBIN_TIERS":          noStripeID,
		"SITEBIN_PADDLE_API_KEY": "pk_test",
	})), noFile)
	if err == nil || !strings.Contains(err.Error(), "price.paddle") {
		t.Fatalf("a paddle instance with an unsellable paid tier must refuse to start, got %v", err)
	}

	// PayGate is handed the AMOUNT and creates the product itself, so the same
	// catalogue is perfectly sellable there.
	if _, err := Load(env(with(map[string]string{
		"SITEBIN_TIERS":           noStripeID,
		"SITEBIN_PAYGATE_URL":     "https://pg.example",
		"SITEBIN_PAYGATE_APP_ID":  "sitebin",
		"SITEBIN_PAYGATE_API_KEY": "ssk_test",
	})), noFile); err != nil {
		t.Fatalf("paygate needs no provider price id: %v", err)
	}

	// And the ids that ARE there still start.
	if _, err := Load(env(with(map[string]string{
		"SITEBIN_TIERS":             twoTiers,
		"SITEBIN_STRIPE_SECRET_KEY": "sk_test",
	})), noFile); err != nil {
		t.Fatalf("a fully priced catalogue must start: %v", err)
	}
}

func TestLoadStackLicensing(t *testing.T) {
	stack := map[string]string{
		"SITEBIN_STACK_URL":       "https://platform.example/",
		"SITEBIN_STACK_APP_ID":    "sitebin",
		"SITEBIN_STACK_ADMIN_KEY": "padm_test",
	}
	with := func(v string) func(string) string {
		m := map[string]string{}
		for k, vv := range stack {
			m[k] = vv
		}
		if v != "" {
			m["SITEBIN_STACK_LICENSING"] = v
		}
		return env(m)
	}

	t.Run("absent declares nothing", func(t *testing.T) {
		cfg, err := Load(with(""), noFile)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.StackRegistration.Licensing != nil {
			t.Error("an undeclared block must stay nil: the stack merges, and an empty one erases")
		}
	})

	t.Run("parsed in the stack's own shape", func(t *testing.T) {
		cfg, err := Load(with(`{"graceMonths":3,"plans":{"team":{"max_custom_domains":25},"platform":{}}}`), noFile)
		if err != nil {
			t.Fatal(err)
		}
		lic := cfg.StackRegistration.Licensing
		if lic == nil || lic.GraceMonths != 3 {
			t.Fatalf("licensing = %+v", lic)
		}
		if got := lic.Plans["team"]["max_custom_domains"]; got != 25 {
			t.Errorf("team max_custom_domains = %d, want 25", got)
		}
		// An empty plan is not zero entitlements, it is unlimited; it must
		// survive as an empty map rather than be dropped.
		if _, ok := lic.Plans["platform"]; !ok {
			t.Error("a plan declared with no entitlements must still be declared")
		}
	})

	t.Run("rejects garbage and empty declarations", func(t *testing.T) {
		for _, bad := range []string{`{`, `{}`, `{"plans":{}}`, `{"graceMonths":-1}`} {
			if _, err := Load(with(bad), noFile); err == nil {
				t.Errorf("SITEBIN_STACK_LICENSING=%q must not start", bad)
			}
		}
	})
}

// The discovery URL is where the document is FETCHED; the issuer stays the
// value that must appear in `iss`. Pointing an app at the SaaS Stack's Auth
// Gateway is exactly this split, and it is what puts the app behind the
// stack's consent gate.
func TestLoadGenericOIDCDiscoveryURL(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_OAUTH_OIDC_ISSUER":        "https://auth.stack.example/realms/saas-stack",
		"SITEBIN_OAUTH_OIDC_DISCOVERY_URL": "https://auth-gw.saas-stack.example/api/v1/sitebin",
		"SITEBIN_OAUTH_OIDC_CLIENT_ID":     "sitebin-app",
	}), noFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OIDC.Issuer != "https://auth.stack.example/realms/saas-stack" {
		t.Errorf("the discovery URL must not change what the issuer means: %q", cfg.OIDC.Issuer)
	}
	if cfg.OIDC.DiscoveryURL != "https://auth-gw.saas-stack.example/api/v1/sitebin" {
		t.Errorf("DiscoveryURL = %q", cfg.OIDC.DiscoveryURL)
	}
}

// Either form is accepted, because both are what people copy: the base, and
// the full well-known URL a browser was just pointed at.
func TestLoadGenericOIDCDiscoveryURLTrimsWellKnown(t *testing.T) {
	for _, in := range []string{
		"https://gw.example/api/v1/sitebin/.well-known/openid-configuration",
		"https://gw.example/api/v1/sitebin/",
	} {
		cfg, err := Load(env(map[string]string{
			"SITEBIN_OAUTH_OIDC_ISSUER":        "https://idp.example",
			"SITEBIN_OAUTH_OIDC_DISCOVERY_URL": in,
			"SITEBIN_OAUTH_OIDC_CLIENT_ID":     "cid",
		}), noFile)
		if err != nil {
			t.Fatalf("Load(%q): %v", in, err)
		}
		if cfg.OIDC.DiscoveryURL != "https://gw.example/api/v1/sitebin" {
			t.Errorf("Load(%q) -> %q", in, cfg.OIDC.DiscoveryURL)
		}
	}
}

func TestLoadGenericOIDCDiscoveryURLInvalid(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SITEBIN_OAUTH_OIDC_ISSUER":        "https://idp.example",
		"SITEBIN_OAUTH_OIDC_DISCOVERY_URL": "gw.example",
		"SITEBIN_OAUTH_OIDC_CLIENT_ID":     "cid",
	}), noFile)
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_OAUTH_OIDC_DISCOVERY_URL") {
		t.Fatalf("want discovery-url error, got %v", err)
	}
}

// Unset, nothing changes: discovery comes from the issuer, which is what every
// plain OIDC provider wants and what every existing deployment already does.
func TestLoadGenericOIDCDiscoveryURLDefaultsToEmpty(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_OAUTH_OIDC_ISSUER":    "https://idp.example",
		"SITEBIN_OAUTH_OIDC_CLIENT_ID": "cid",
	}), noFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDC.DiscoveryURL != "" {
		t.Errorf("DiscoveryURL = %q, want empty", cfg.OIDC.DiscoveryURL)
	}
}

func TestLoadStackTerms(t *testing.T) {
	base := func(extra map[string]string) map[string]string {
		m := map[string]string{
			"SITEBIN_STACK_URL":       "https://platform.example/",
			"SITEBIN_STACK_APP_ID":    "sitebin",
			"SITEBIN_STACK_ADMIN_KEY": "padm_test",
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	t.Run("parsed", func(t *testing.T) {
		cfg, err := Load(env(base(map[string]string{
			"SITEBIN_STACK_TERMS": `{"version":"2026-09-01","url":"https://sitebin.io/terms","title":{"en":"Sitebin Terms of Service"}}`,
		})), noFile)
		if err != nil {
			t.Fatal(err)
		}
		got := cfg.StackRegistration.Terms
		if got == nil || got.Version != "2026-09-01" || got.URL != "https://sitebin.io/terms" ||
			got.Title["en"] != "Sitebin Terms of Service" {
			t.Fatalf("terms = %+v", got)
		}
	})

	t.Run("absent means declare nothing", func(t *testing.T) {
		cfg, err := Load(env(base(nil)), noFile)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.StackRegistration.Terms != nil {
			t.Fatalf("terms = %+v, want nil", cfg.StackRegistration.Terms)
		}
	})

	// A half-filled block would register terms the gate cannot show, and the
	// failure would happen in a background goroutine at boot.
	for name, bad := range map[string]string{
		"no version": `{"url":"https://sitebin.io/terms"}`,
		"no url":     `{"version":"2026-09-01"}`,
		"blank url":  `{"version":"2026-09-01","url":"   "}`,
		"not a url":  `{"version":"2026-09-01","url":"sitebin.io/terms"}`,
		"not json":   `{`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(env(base(map[string]string{"SITEBIN_STACK_TERMS": bad})), noFile); err == nil {
				t.Fatalf("SITEBIN_STACK_TERMS=%q must not start", bad)
			}
		})
	}
}
