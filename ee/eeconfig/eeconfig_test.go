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
