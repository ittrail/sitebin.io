package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN": "sitebin.example",
		"SITEBIN_HTTP_ONLY":   "true",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BaseDomain != "sitebin.example" {
		t.Errorf("BaseDomain = %q", cfg.BaseDomain)
	}
	if cfg.DataDir != "/data" {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	if cfg.MaxSiteBytes != 104857600 {
		t.Errorf("MaxSiteBytes = %d", cfg.MaxSiteBytes)
	}
	if cfg.MaxFiles != 1000 {
		t.Errorf("MaxFiles = %d", cfg.MaxFiles)
	}
	if cfg.MaxExpiryDays != 0 {
		t.Errorf("MaxExpiryDays = %d", cfg.MaxExpiryDays)
	}
	if !cfg.WebDAVAllowed {
		t.Error("WebDAVAllowed should default true")
	}
	if cfg.ReadOnly {
		t.Error("ReadOnly should default false")
	}
	if cfg.PublicAddr != ":8080" || cfg.InternalAddr != ":9000" {
		t.Errorf("addrs = %q %q", cfg.PublicAddr, cfg.InternalAddr)
	}
	if cfg.BackendHost != "127.0.0.1" {
		t.Errorf("BackendHost = %q", cfg.BackendHost)
	}
	if cfg.RateCreatePerHour != 30 || cfg.RateCreateBurst != 10 || cfg.RateAuthPer5Min != 10 {
		t.Errorf("rates = %d %d %d", cfg.RateCreatePerHour, cfg.RateCreateBurst, cfg.RateAuthPer5Min)
	}
	if cfg.CleanupInterval != 10*time.Minute {
		t.Errorf("CleanupInterval = %v", cfg.CleanupInterval)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN":          "Sitebin.ITTrail.cloud.",
		"SITEBIN_DATA_DIR":             "/srv/data",
		"SITEBIN_DNS_PROVIDER":         "cloudflare",
		"SITEBIN_DNS_TOKEN":            "tok",
		"SITEBIN_ACME_EMAIL":           "ops@example.com",
		"SITEBIN_MAX_SITE_BYTES":       "1048576",
		"SITEBIN_MAX_FILES":            "5",
		"SITEBIN_MAX_EXPIRY_DAYS":      "30",
		"SITEBIN_WEBDAV_ENABLED":       "false",
		"SITEBIN_READONLY":             "true",
		"SITEBIN_RATE_CREATE_PER_HOUR": "60",
		"SITEBIN_RATE_CREATE_BURST":    "20",
		"SITEBIN_RATE_AUTH_PER_5MIN":   "3",
		"SITEBIN_CLEANUP_INTERVAL":     "1m",
		"SITEBIN_BACKEND_HOST":         "backend",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BaseDomain != "sitebin.ittrail.cloud" {
		t.Errorf("BaseDomain not normalized: %q", cfg.BaseDomain)
	}
	if cfg.DataDir != "/srv/data" || cfg.DNSProvider != "cloudflare" || cfg.DNSToken != "tok" {
		t.Error("string overrides not applied")
	}
	if cfg.MaxSiteBytes != 1048576 || cfg.MaxFiles != 5 || cfg.MaxExpiryDays != 30 {
		t.Error("numeric overrides not applied")
	}
	if cfg.WebDAVAllowed || !cfg.ReadOnly {
		t.Error("bool overrides not applied")
	}
	if cfg.RateCreatePerHour != 60 || cfg.RateCreateBurst != 20 || cfg.RateAuthPer5Min != 3 {
		t.Error("rate overrides not applied")
	}
	if cfg.CleanupInterval != time.Minute {
		t.Errorf("CleanupInterval = %v", cfg.CleanupInterval)
	}
	if cfg.BackendHost != "backend" {
		t.Errorf("BackendHost = %q", cfg.BackendHost)
	}
}

func TestLoadRequiresBaseDomain(t *testing.T) {
	_, err := Load(env(map[string]string{}))
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_BASE_DOMAIN") {
		t.Fatalf("expected base-domain error, got %v", err)
	}
}

func TestLoadRequiresTLSConfigUnlessHTTPOnly(t *testing.T) {
	_, err := Load(env(map[string]string{"SITEBIN_BASE_DOMAIN": "s.example"}))
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_DNS_PROVIDER") {
		t.Fatalf("expected TLS config error, got %v", err)
	}
	// TLS snippet is an accepted alternative.
	if _, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN": "s.example",
		"SITEBIN_TLS_SNIPPET": "dns netcup { customer_number 1 }",
	})); err != nil {
		t.Fatalf("snippet should satisfy TLS requirement: %v", err)
	}
}

func TestLoadRejectsUnknownProvider(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN":  "s.example",
		"SITEBIN_DNS_PROVIDER": "route53",
		"SITEBIN_DNS_TOKEN":    "x",
	}))
	if err == nil || !strings.Contains(err.Error(), "route53") {
		t.Fatalf("expected unknown-provider error, got %v", err)
	}
}

func TestLoadRejectsBadInt(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN":    "s.example",
		"SITEBIN_HTTP_ONLY":      "1",
		"SITEBIN_MAX_SITE_BYTES": "lots",
	}))
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_MAX_SITE_BYTES") {
		t.Fatalf("expected int parse error, got %v", err)
	}
}

func TestViewAccessDefaultSubdomain(t *testing.T) {
	cfg, err := Load(env(map[string]string{"SITEBIN_BASE_DOMAIN": "s.example", "SITEBIN_HTTP_ONLY": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ViewAccess != ViewSubdomain || !cfg.SubdomainViews() || cfg.PathViews() {
		t.Errorf("default view access = %q", cfg.ViewAccess)
	}
	if got := cfg.ViewURL("aaaaaaaaaaaaaaaaaaaaaaaaaa"); got != "http://aaaaaaaaaaaaaaaaaaaaaaaaaa.s.example" {
		t.Errorf("subdomain view url = %q", got)
	}
}

func TestViewAccessPath(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN": "s.example", "SITEBIN_HTTP_ONLY": "true",
		"SITEBIN_VIEW_ACCESS": "path",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SubdomainViews() || !cfg.PathViews() {
		t.Errorf("path mode flags wrong: sub=%v path=%v", cfg.SubdomainViews(), cfg.PathViews())
	}
	if got := cfg.ViewURL("aaaaaaaaaaaaaaaaaaaaaaaaaa"); got != "http://s.example/v/aaaaaaaaaaaaaaaaaaaaaaaaaa/" {
		t.Errorf("path view url = %q", got)
	}
}

func TestViewAccessBoth(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN": "s.example", "SITEBIN_HTTP_ONLY": "true",
		"SITEBIN_VIEW_ACCESS": "both",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SubdomainViews() || !cfg.PathViews() {
		t.Error("both mode should enable subdomain and path")
	}
}

func TestViewAccessInvalid(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN": "s.example", "SITEBIN_HTTP_ONLY": "true",
		"SITEBIN_VIEW_ACCESS": "wat",
	}))
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_VIEW_ACCESS") {
		t.Fatalf("expected view-access error, got %v", err)
	}
}

func TestPathModeNeedsNoDNSChallenge(t *testing.T) {
	// path-only mode over HTTPS needs no wildcard cert, so no DNS provider.
	cfg, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN": "s.example",
		"SITEBIN_VIEW_ACCESS": "path",
	}))
	if err != nil {
		t.Fatalf("path-only HTTPS should not require DNS provider: %v", err)
	}
	if cfg.SubdomainViews() {
		t.Error("path-only should not enable subdomains")
	}
}

func TestSubdomainModeStillNeedsDNSChallenge(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN": "s.example",
		"SITEBIN_VIEW_ACCESS": "both",
	}))
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_DNS_PROVIDER") {
		t.Fatalf("both mode still needs the wildcard cert, got %v", err)
	}
}

func TestBaseDomainPortStripped(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN": "sitebin.localtest.me:8085",
		"SITEBIN_HTTP_ONLY":   "yes",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BaseDomain != "sitebin.localtest.me" {
		t.Errorf("BaseDomain = %q", cfg.BaseDomain)
	}
	if cfg.PublicPort != 8085 {
		t.Errorf("PublicPort = %d", cfg.PublicPort)
	}
}

func TestEmbedOrigins(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN":   "sitebin.example",
		"SITEBIN_HTTP_ONLY":     "true",
		"SITEBIN_EMBED_ORIGINS": " https://Sitebin.io ,https://www.sitebin.io,",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"https://sitebin.io", "https://www.sitebin.io"}
	if len(cfg.EmbedOrigins) != len(want) {
		t.Fatalf("EmbedOrigins = %v, want %v", cfg.EmbedOrigins, want)
	}
	for i := range want {
		if cfg.EmbedOrigins[i] != want[i] {
			t.Fatalf("EmbedOrigins = %v, want %v", cfg.EmbedOrigins, want)
		}
	}
}

func TestEmbedOriginsWildcard(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN":   "sitebin.example",
		"SITEBIN_HTTP_ONLY":     "true",
		"SITEBIN_EMBED_ORIGINS": "*",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.EmbedOrigins) != 1 || cfg.EmbedOrigins[0] != "*" {
		t.Fatalf("EmbedOrigins = %v, want [*]", cfg.EmbedOrigins)
	}
}

func TestEmbedOriginsInvalid(t *testing.T) {
	for _, bad := range []string{"sitebin.io", "ftp://x.io", "https://x.io/path"} {
		_, err := Load(env(map[string]string{
			"SITEBIN_BASE_DOMAIN":   "sitebin.example",
			"SITEBIN_HTTP_ONLY":     "true",
			"SITEBIN_EMBED_ORIGINS": bad,
		}))
		if err == nil || !strings.Contains(err.Error(), "SITEBIN_EMBED_ORIGINS") {
			t.Errorf("%q: err = %v, want SITEBIN_EMBED_ORIGINS error", bad, err)
		}
	}
}

func TestEmbedOriginsUnset(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN": "sitebin.example",
		"SITEBIN_HTTP_ONLY":   "true",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EmbedOrigins != nil {
		t.Fatalf("EmbedOrigins = %v, want nil", cfg.EmbedOrigins)
	}
}

func TestViewDomainDefaultsToBaseDomain(t *testing.T) {
	cfg, err := Load(func(k string) string {
		return map[string]string{"SITEBIN_BASE_DOMAIN": "sitebin.example", "SITEBIN_HTTP_ONLY": "true"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ViewDomain != cfg.BaseDomain {
		t.Fatalf("ViewDomain = %q, want it to default to BaseDomain %q", cfg.ViewDomain, cfg.BaseDomain)
	}
}

// Path views serve from the main domain; combined with a separate view domain
// that would put uploaded HTML straight back onto the app's origin.
func TestViewDomainRefusesPathViews(t *testing.T) {
	_, err := Load(func(k string) string {
		return map[string]string{
			"SITEBIN_BASE_DOMAIN": "app.sitebin.io",
			"SITEBIN_VIEW_DOMAIN": "sitebin.app",
			"SITEBIN_VIEW_ACCESS": "both",
			"SITEBIN_HTTP_ONLY":   "true",
		}[k]
	})
	if err == nil || !strings.Contains(err.Error(), "pick one") {
		t.Fatalf("expected the contradiction to be refused, got %v", err)
	}
}

func TestViewURLUsesTheViewDomain(t *testing.T) {
	cfg, err := Load(func(k string) string {
		return map[string]string{
			"SITEBIN_BASE_DOMAIN": "app.sitebin.io",
			"SITEBIN_VIEW_DOMAIN": "sitebin.app",
			"SITEBIN_HTTP_ONLY":   "true",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.ViewURL("abcdefghijklmnopqrstuvwxyz")
	if !strings.Contains(got, "abcdefghijklmnopqrstuvwxyz.sitebin.app") {
		t.Errorf("ViewURL = %q, want it on the view domain", got)
	}
	if !strings.Contains(cfg.EditURL("x"), "app.sitebin.io") {
		t.Error("the edit page is app UI and must stay on the main domain")
	}
}
