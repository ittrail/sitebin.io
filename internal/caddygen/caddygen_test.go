package caddygen

import (
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/config"
	"github.com/ittrail/sitebin.io/internal/store"
)

func TestSPAMarkerNameMatchesStore(t *testing.T) {
	if spaMarkerName != store.SPAMarker {
		t.Fatalf("caddygen spaMarkerName %q != store.SPAMarker %q", spaMarkerName, store.SPAMarker)
	}
}

func mustLoad(t *testing.T, env map[string]string) config.Config {
	t.Helper()
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestGenerateHTTPS(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN":  "sitebin.ittrail.cloud",
		"SITEBIN_DNS_PROVIDER": "cloudflare",
		"SITEBIN_DNS_TOKEN":    "secret-token",
		"SITEBIN_ACME_EMAIL":   "ops@ittrail.at",
	})
	out := Generate(cfg)

	for _, want := range []string{
		"email ops@ittrail.at",
		"storage file_system /data/caddy",
		"ask http://127.0.0.1:9000/internal/tls-check",
		"sitebin.ittrail.cloud {",
		"*.sitebin.ittrail.cloud {",
		"dns cloudflare {env.SITEBIN_DNS_TOKEN}",
		"root * /data/sites/{labels.3}/files",
		"root * /data/domain-index/{host}/files",
		"forward_auth 127.0.0.1:9000",
		"uri /internal/authz",
		"header_up X-Forwarded-Host {host}",
		"@spa file /.sitebin-spa",
		"try_files {path} /index.html",
		"hide .sitebin-spa",
		"reverse_proxy 127.0.0.1:8080",
		"path /_sitebin/*",
		"on_demand",
		"https:// {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated Caddyfile:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret-token") {
		t.Error("DNS token written verbatim into Caddyfile (must use env placeholder)")
	}
	if strings.Contains(out, "auto_https") {
		t.Error("auto_https must stay on in HTTPS mode")
	}
}

func TestGenerateHTTPSDeepBaseDomainLabelIndex(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN": "bin.apps.internal.example.com",
		"SITEBIN_HTTP_ONLY":   "true",
	})
	out := Generate(cfg)
	if !strings.Contains(out, "{labels.5}") {
		t.Errorf("expected labels.5 for 5-label base domain:\n%s", out)
	}
}

func TestGenerateHTTPOnly(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN":  "sitebin.localtest.me",
		"SITEBIN_HTTP_ONLY":    "true",
		"SITEBIN_BACKEND_HOST": "backend",
		"SITEBIN_DATA_DIR":     "/data",
	})
	out := Generate(cfg)
	for _, want := range []string{
		"auto_https off",
		"http://sitebin.localtest.me {",
		"http://*.sitebin.localtest.me {",
		"http://:80 {", // custom-domain catch-all
		"root * /data/sites/{labels.3}/files",
		"forward_auth backend:9000",
		"reverse_proxy backend:8080",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated Caddyfile:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tls ") || strings.Contains(out, "on_demand_tls") {
		t.Errorf("HTTP-only config must not configure TLS:\n%s", out)
	}
}

func TestGeneratePathMode(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN": "sitebin.example",
		"SITEBIN_HTTP_ONLY":   "true",
		"SITEBIN_VIEW_ACCESS": "path",
	})
	out := Generate(cfg)
	for _, want := range []string{
		"@view path_regexp view ^/v/([a-z2-7]{26})/.*$",
		"redir @viewbare /v/{re.vb.1}/ 308",
		"header_up X-Sitebin-View {re.view.1}",
		"uri strip_prefix /v/{re.view.1}",
		"root * /data/sites/{re.view.1}/files",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("path mode missing %q:\n%s", want, out)
		}
	}
	// path-only: no wildcard subdomain block
	if strings.Contains(out, "*.sitebin.example") {
		t.Errorf("path-only mode should not emit a wildcard block:\n%s", out)
	}
}

func TestGenerateBothMode(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN":  "sitebin.example",
		"SITEBIN_DNS_PROVIDER": "cloudflare",
		"SITEBIN_DNS_TOKEN":    "tok",
		"SITEBIN_VIEW_ACCESS":  "both",
	})
	out := Generate(cfg)
	if !strings.Contains(out, "*.sitebin.example {") {
		t.Errorf("both mode should keep the wildcard block:\n%s", out)
	}
	if !strings.Contains(out, "@view path_regexp view ^/v/") {
		t.Errorf("both mode should add path routes:\n%s", out)
	}
}

func TestGenerateSubdomainOnlyHasNoPathRoutes(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN": "sitebin.example",
		"SITEBIN_HTTP_ONLY":   "true",
	})
	out := Generate(cfg)
	if strings.Contains(out, "/v/") {
		t.Errorf("subdomain-only mode should not emit /v/ routes:\n%s", out)
	}
}

func TestGenerateTLSSnippet(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN": "sitebin.example",
		"SITEBIN_TLS_SNIPPET": "dns netcup {\n  customer_number 123\n}",
	})
	out := Generate(cfg)
	if !strings.Contains(out, "customer_number 123") {
		t.Errorf("TLS snippet not injected:\n%s", out)
	}
	if strings.Contains(out, "dns cloudflare") {
		t.Error("provider block should be replaced by snippet")
	}
}
