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
	if !cfg.MCPEnabled {
		t.Error("MCPEnabled should default true")
	}
	if cfg.ReadOnly {
		t.Error("ReadOnly should default false")
	}
	if cfg.PublicAddr != ":8080" || cfg.InternalAddr != ":9000" {
		t.Errorf("addrs = %q %q", cfg.PublicAddr, cfg.InternalAddr)
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
		"SITEBIN_MCP_ENABLED":          "false",
		"SITEBIN_READONLY":             "true",
		"SITEBIN_RATE_CREATE_PER_HOUR": "60",
		"SITEBIN_RATE_CREATE_BURST":    "20",
		"SITEBIN_RATE_AUTH_PER_5MIN":   "3",
		"SITEBIN_CLEANUP_INTERVAL":     "1m",
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
	if cfg.WebDAVAllowed || !cfg.ReadOnly || cfg.MCPEnabled {
		t.Error("bool overrides not applied")
	}
	if cfg.RateCreatePerHour != 60 || cfg.RateCreateBurst != 20 || cfg.RateAuthPer5Min != 3 {
		t.Error("rate overrides not applied")
	}
	if cfg.CleanupInterval != time.Minute {
		t.Errorf("CleanupInterval = %v", cfg.CleanupInterval)
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

func TestDomainVerificationSetting(t *testing.T) {
	base := map[string]string{"SITEBIN_BASE_DOMAIN": "sitebin.example", "SITEBIN_HTTP_ONLY": "true"}
	cfg, err := Load(env(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DomainVerification != DomainVerifyDNS {
		t.Errorf("default = %q, want dns: ownership is proven unless an operator opts out", cfg.DomainVerification)
	}
	base["SITEBIN_DOMAIN_VERIFICATION"] = "OFF"
	if cfg, err := Load(env(base)); err != nil || cfg.DomainVerification != DomainVerifyOff {
		t.Errorf("off: %q %v", cfg.DomainVerification, err)
	}
	base["SITEBIN_DOMAIN_VERIFICATION"] = "sometimes"
	if _, err := Load(env(base)); err == nil || !strings.Contains(err.Error(), "SITEBIN_DOMAIN_VERIFICATION") {
		t.Errorf("an unknown value must be refused: %v", err)
	}
}

func TestOperatorDomains(t *testing.T) {
	base := map[string]string{"SITEBIN_BASE_DOMAIN": "app.example.com", "SITEBIN_VIEW_DOMAIN": "sites.example", "SITEBIN_HTTP_ONLY": "true"}
	with := func(v string) map[string]string {
		m := map[string]string{"SITEBIN_OPERATOR_DOMAINS": v}
		for k, x := range base {
			m[k] = x
		}
		return m
	}
	cfg, err := Load(env(with(" *.App.Ittrail.dev , apps.example.org ,")))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.OperatorDomains) != 2 || cfg.OperatorDomains[0] != "app.ittrail.dev" || cfg.OperatorDomains[1] != "apps.example.org" {
		t.Errorf("OperatorDomains = %v", cfg.OperatorDomains)
	}
	for _, bad := range []string{"localhost", "bad_zone.example", "example.com", "sites.example", "x.sites.example"} {
		if _, err := Load(env(with(bad))); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	cfg, _ = Load(env(base))
	if len(cfg.OperatorDomains) != 0 {
		t.Errorf("default = %v", cfg.OperatorDomains)
	}
}

func TestZoneNamesPerHour(t *testing.T) {
	base := map[string]string{"SITEBIN_BASE_DOMAIN": "app.example.com", "SITEBIN_HTTP_ONLY": "true"}
	cfg, err := Load(env(base))
	if err != nil || cfg.ZoneNamesPerHour != 50 {
		t.Errorf("default = %d, %v; want 50", cfg.ZoneNamesPerHour, err)
	}
	for v, want := range map[string]int{"0": 0, "200": 200} {
		m := map[string]string{"SITEBIN_ZONE_NAMES_PER_HOUR": v}
		for k, x := range base {
			m[k] = x
		}
		if cfg, err := Load(env(m)); err != nil || cfg.ZoneNamesPerHour != want {
			t.Errorf("%s = %d, %v", v, cfg.ZoneNamesPerHour, err)
		}
	}
	for _, bad := range []string{"-1", "many"} {
		m := map[string]string{"SITEBIN_ZONE_NAMES_PER_HOUR": bad}
		for k, x := range base {
			m[k] = x
		}
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func formsBase() map[string]string {
	return map[string]string{"SITEBIN_BASE_DOMAIN": "sitebin.example", "SITEBIN_HTTP_ONLY": "true"}
}

func TestFormsOffByDefault(t *testing.T) {
	cfg, err := Load(env(formsBase()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FormsSMTP != nil {
		t.Errorf("FormsSMTP = %+v, want nil: forms are off until SITEBIN_FORMS_SMTP_HOST is set", cfg.FormsSMTP)
	}
	if cfg.FormsMaxPerSite != nil {
		t.Errorf("FormsMaxPerSite = %d, want unset", *cfg.FormsMaxPerSite)
	}
	if cfg.FormsMaxFiles != 5 || cfg.FormsMaxFileBytes != 2097152 || cfg.FormsPerIPHour != 10 || cfg.FormsPerFormHour != 60 {
		t.Errorf("defaults = files %d, bytes %d, ip %d, form %d", cfg.FormsMaxFiles, cfg.FormsMaxFileBytes, cfg.FormsPerIPHour, cfg.FormsPerFormHour)
	}
}

func TestFormsSMTPFullySet(t *testing.T) {
	vars := formsBase()
	for k, v := range map[string]string{
		"SITEBIN_FORMS_SMTP_HOST":      "smtp.example.com",
		"SITEBIN_FORMS_SMTP_PORT":      "465",
		"SITEBIN_FORMS_SMTP_USER":      "u",
		"SITEBIN_FORMS_SMTP_PASS":      "p",
		"SITEBIN_FORMS_SMTP_FROM":      "forms@example.com",
		"SITEBIN_FORMS_SMTP_TLS":       "true",
		"SITEBIN_FORMS_MAX_PER_SITE":   "0",
		"SITEBIN_FORMS_MAX_FILES":      "0",
		"SITEBIN_FORMS_MAX_FILE_BYTES": "1000",
		"SITEBIN_FORMS_PER_IP_HOUR":    "3",
		"SITEBIN_FORMS_PER_FORM_HOUR":  "4",
	} {
		vars[k] = v
	}
	cfg, err := Load(env(vars))
	if err != nil {
		t.Fatal(err)
	}
	want := FormsSMTP{Host: "smtp.example.com", Port: 465, User: "u", Pass: "p", From: "forms@example.com", TLS: true}
	if cfg.FormsSMTP == nil || *cfg.FormsSMTP != want {
		t.Errorf("FormsSMTP = %+v, want %+v", cfg.FormsSMTP, want)
	}
	// An explicit 0 is a decision ("no forms here"), not "unset".
	if cfg.FormsMaxPerSite == nil || *cfg.FormsMaxPerSite != 0 {
		t.Errorf("FormsMaxPerSite = %v, want an explicit 0", cfg.FormsMaxPerSite)
	}
	if cfg.FormsMaxFiles != 0 || cfg.FormsMaxFileBytes != 1000 || cfg.FormsPerIPHour != 3 || cfg.FormsPerFormHour != 4 {
		t.Errorf("limits = %d %d %d %d", cfg.FormsMaxFiles, cfg.FormsMaxFileBytes, cfg.FormsPerIPHour, cfg.FormsPerFormHour)
	}
}

func TestFormsSMTPDefaults(t *testing.T) {
	vars := formsBase()
	vars["SITEBIN_FORMS_SMTP_HOST"] = "smtp.example.com"
	vars["SITEBIN_FORMS_SMTP_FROM"] = "forms@example.com"
	cfg, err := Load(env(vars))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FormsSMTP.Port != 587 || cfg.FormsSMTP.TLS {
		t.Errorf("port %d tls %v, want 587 and STARTTLS", cfg.FormsSMTP.Port, cfg.FormsSMTP.TLS)
	}
}

func TestFormsConfigRefusals(t *testing.T) {
	withHost := func(extra map[string]string) map[string]string {
		m := map[string]string{"SITEBIN_FORMS_SMTP_HOST": "smtp.example.com", "SITEBIN_FORMS_SMTP_FROM": "forms@example.com"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := map[string]map[string]string{
		"from missing":           {"SITEBIN_FORMS_SMTP_HOST": "smtp.example.com"},
		"from with display name": withHost(map[string]string{"SITEBIN_FORMS_SMTP_FROM": "Forms <forms@example.com>"}),
		"from in angle brackets": withHost(map[string]string{"SITEBIN_FORMS_SMTP_FROM": "<forms@example.com>"}),
		"from not an address":    withHost(map[string]string{"SITEBIN_FORMS_SMTP_FROM": "forms"}),
		"port out of range":      withHost(map[string]string{"SITEBIN_FORMS_SMTP_PORT": "99999"}),
		"tls not a bool":         withHost(map[string]string{"SITEBIN_FORMS_SMTP_TLS": "maybe"}),
		"negative per-site":      {"SITEBIN_FORMS_MAX_PER_SITE": "-1"},
		"per-site not a number":  {"SITEBIN_FORMS_MAX_PER_SITE": "ten"},
		"negative max files":     {"SITEBIN_FORMS_MAX_FILES": "-1"},
		"zero file bytes":        {"SITEBIN_FORMS_MAX_FILE_BYTES": "0"},
		"zero per-ip":            {"SITEBIN_FORMS_PER_IP_HOUR": "0"},
		"zero per-form":          {"SITEBIN_FORMS_PER_FORM_HOUR": "0"},
	}
	for name, extra := range cases {
		vars := formsBase()
		for k, v := range extra {
			vars[k] = v
		}
		if _, err := Load(env(vars)); err == nil {
			t.Errorf("%s: accepted, want a startup error", name)
		}
	}
}
