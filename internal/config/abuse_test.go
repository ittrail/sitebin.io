package config

import (
	"strings"
	"testing"
)

func load(t *testing.T, extra map[string]string) (Config, error) {
	t.Helper()
	m := map[string]string{"SITEBIN_BASE_DOMAIN": "app.sitebin.example", "SITEBIN_HTTP_ONLY": "true"}
	for k, v := range extra {
		m[k] = v
	}
	return Load(env(m))
}

func TestCSPHostDefaults(t *testing.T) {
	cfg, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.CSPScriptHosts, " ") != "https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com https://cdn.tailwindcss.com https://code.jquery.com" {
		t.Errorf("script hosts = %v", cfg.CSPScriptHosts)
	}
	if strings.Join(cfg.CSPStyleHosts, " ") != "https://fonts.googleapis.com https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com" {
		t.Errorf("style hosts = %v", cfg.CSPStyleHosts)
	}
	if strings.Join(cfg.CSPFontHosts, " ") != "https://fonts.gstatic.com https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com" {
		t.Errorf("font hosts = %v", cfg.CSPFontHosts)
	}
	if len(cfg.CSPImgHosts) != 0 {
		t.Errorf("img hosts = %v, want none by default", cfg.CSPImgHosts)
	}
}

func TestCSPHostOverrides(t *testing.T) {
	cfg, err := load(t, map[string]string{
		"SITEBIN_CSP_SCRIPT_HOSTS": " https://cdn.jsdelivr.net,  https://*.example.com:8443 ",
		"SITEBIN_CSP_STYLE_HOSTS":  "none",
		"SITEBIN_CSP_FONT_HOSTS":   "https://fonts.gstatic.com",
		"SITEBIN_CSP_IMG_HOSTS":    "https://images.example.org/static/ https://cdn.example.net",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.CSPScriptHosts, " ") != "https://cdn.jsdelivr.net https://*.example.com:8443" {
		t.Errorf("script hosts = %v", cfg.CSPScriptHosts)
	}
	if cfg.CSPStyleHosts == nil || len(cfg.CSPStyleHosts) != 0 {
		t.Errorf("none should empty the list: %#v", cfg.CSPStyleHosts)
	}
	if len(cfg.CSPImgHosts) != 2 {
		t.Errorf("img hosts = %v", cfg.CSPImgHosts)
	}
}

// The value is written into the Caddyfile and into a header: anything but a
// host source is refused at startup, keywords and separators above all.
func TestCSPHostsRefuseInjection(t *testing.T) {
	for _, bad := range []string{
		"https://cdn.example.com; script-src *",
		"'unsafe-inline'",
		"*",
		"https:",
		"data:",
		`https://cdn.example.com"`,
		"https://cdn.example.com\nheader",
		"javascript:alert(1)",
		"https://exa mple.com",
		"https://example.com/a'b",
	} {
		if _, err := load(t, map[string]string{"SITEBIN_CSP_SCRIPT_HOSTS": bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestAbuseContactDefaults(t *testing.T) {
	cfg, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AbuseContact != "" {
		t.Errorf("contact = %q, want none by default", cfg.AbuseContact)
	}
	if cfg.AbuseReportURL != "http://app.sitebin.example/report" {
		t.Errorf("report url = %q", cfg.AbuseReportURL)
	}
	if cfg.HomeURL != "http://app.sitebin.example" {
		t.Errorf("home url = %q", cfg.HomeURL)
	}
}

func TestAbuseContactOverrides(t *testing.T) {
	cfg, err := load(t, map[string]string{
		"SITEBIN_ABUSE_CONTACT":    "abuse@sitebin.example",
		"SITEBIN_ABUSE_REPORT_URL": "https://sitebin.example/abuse/",
		"SITEBIN_HOME_URL":         "https://sitebin.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AbuseContact != "abuse@sitebin.example" || cfg.AbuseReportURL != "https://sitebin.example/abuse/" || cfg.HomeURL != "https://sitebin.example" {
		t.Fatalf("%q %q %q", cfg.AbuseContact, cfg.AbuseReportURL, cfg.HomeURL)
	}
	cfg, err = load(t, map[string]string{"SITEBIN_ABUSE_REPORT_URL": "none"})
	if err != nil || cfg.AbuseReportURL != "" {
		t.Fatalf("none should drop the report url: %q %v", cfg.AbuseReportURL, err)
	}
	for k, bad := range map[string]string{
		"SITEBIN_ABUSE_CONTACT":    "Abuse <abuse@sitebin.example>",
		"SITEBIN_ABUSE_REPORT_URL": "javascript:alert(1)",
		"SITEBIN_HOME_URL":         "sitebin.example",
	} {
		if _, err := load(t, map[string]string{k: bad}); err == nil {
			t.Errorf("%s accepted %q", k, bad)
		}
	}
}
