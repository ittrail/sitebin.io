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

// TestBaselineHeadersOnEveryContentOrigin: the baseline is hygiene that must
// reach every site in both editions, so it belongs on each content origin --
// the wildcard subdomain, the custom-domain listener, and path views.
func TestBaselineHeadersOnEveryContentOrigin(t *testing.T) {
	out := Generate(config.Config{
		BaseDomain: "sitebin.example", DataDir: "/data", HTTPOnly: true,
		ViewAccess: "both",
	})
	for _, want := range []string{
		"X-Content-Type-Options nosniff",
		"Referrer-Policy strict-origin-when-cross-origin",
		"Permissions-Policy",
		`Content-Security-Policy "object-src 'none'; base-uri 'self'"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("baseline header %q missing from the Caddyfile", want)
		}
	}
	// base-uri must not be 'none': Angular and friends ship <base href="/">,
	// and 'none' forbids the element outright.
	if strings.Contains(out, "base-uri 'none'") {
		t.Error("base-uri 'none' breaks frameworks that ship a <base> element; use 'self'")
	}
}

// TestStrictHeadersKeyOffTheAbsenceOfTheTrustMarker is the polarity assertion.
// A `file` matcher would harden only marked sites, leaving every unmarked one
// -- including one whose marker was lost -- served wide open.
func TestStrictHeadersKeyOffTheAbsenceOfTheTrustMarker(t *testing.T) {
	out := Generate(config.Config{
		BaseDomain: "sitebin.example", DataDir: "/data", HTTPOnly: true,
		ViewAccess: "both",
	})
	if !strings.Contains(out, "not file /"+store.TrustedMarker) {
		t.Fatalf("strict block must match on the ABSENCE of %s, so an unmarked site is hardened; got:\n%s", store.TrustedMarker, out)
	}
	for _, want := range []string{
		"form-action 'none'",
		"connect-src 'self'",
		"frame-ancestors 'none'",
		"/_sitebin/csp-report",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("strict policy missing %q", want)
		}
	}
}

// The marker must be hidden like the SPA marker, or file_server serves it and
// the directory listing advertises it.
func TestTrustMarkerIsHidden(t *testing.T) {
	out := Generate(config.Config{BaseDomain: "sitebin.example", DataDir: "/data", HTTPOnly: true})
	var hides int
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "hide ") {
			continue
		}
		hides++
		if !strings.Contains(line, store.TrustedMarker) {
			t.Errorf("hide directive omits the trust marker, so file_server would serve it: %q", line)
		}
	}
	if hides == 0 {
		t.Fatal("no hide directives found at all")
	}
}

func TestTrustedMarkerNameMatchesStore(t *testing.T) {
	if trustedMarkerName != store.TrustedMarker {
		t.Fatalf("caddygen trustedMarkerName %q != store.TrustedMarker %q", trustedMarkerName, store.TrustedMarker)
	}
}

// The two matchers must be exact complements, so every response takes exactly
// one complete policy. An earlier version layered two intersecting CSP headers
// and depended on directive order; the order did not hold on the live instance
// and sites came out half-hardened.
func TestTrustMatchersAreComplements(t *testing.T) {
	out := Generate(config.Config{BaseDomain: "sitebin.example", DataDir: "/data", HTTPOnly: true, ViewAccess: "both"})
	neg := strings.Count(out, "@untrusted not file /"+store.TrustedMarker)
	pos := strings.Count(out, "@trusted file /"+store.TrustedMarker)
	if neg == 0 || neg != pos {
		t.Fatalf("matchers are not paired complements: %d untrusted vs %d trusted\n%s", neg, pos, out)
	}
	if strings.Contains(out, "+Content-Security-Policy") {
		t.Error("no appended CSP: each branch must carry one complete policy")
	}
	// the untrusted policy must contain the baseline too, not only the extras
	if !strings.Contains(out, "object-src 'none'; base-uri 'self'; form-action 'none'") {
		t.Error("the untrusted policy dropped the baseline directives")
	}
}

// A separate view domain is the whole point of the split: user content must
// leave the app's registrable domain entirely, or browsers keep treating the
// two as one site.
func TestSeparateViewDomain(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN":  "app.sitebin.io",
		"SITEBIN_VIEW_DOMAIN":  "sitebin.app",
		"SITEBIN_DNS_PROVIDER": "hetzner",
		"SITEBIN_DNS_TOKEN":    "tok",
		"SITEBIN_ACME_EMAIL":   "ops@ittrail.at",
	})
	out := Generate(cfg)

	if !strings.Contains(out, "*.sitebin.app {") {
		t.Errorf("no wildcard block for the view domain:\n%s", out)
	}
	if strings.Contains(out, "*.app.sitebin.io {") {
		t.Error("content must no longer be served from the app's own domain")
	}
	// {labels.N} counts from the right: sitebin.app has two labels, so the view
	// id sits at labels.2 — taking it from the main domain would index the
	// wrong label and every site would 404.
	if !strings.Contains(out, "root * /data/sites/{labels.2}/files") {
		t.Errorf("label index not derived from the view domain:\n%s", out)
	}
	if !strings.Contains(out, "app.sitebin.io {") {
		t.Error("the app domain must still serve the UI and API")
	}
}

func TestSingleDomainInstallIsUnchanged(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN": "sitebin.example",
		"SITEBIN_HTTP_ONLY":   "true",
	})
	out := Generate(cfg)
	if !strings.Contains(out, "http://*.sitebin.example {") {
		t.Errorf("without SITEBIN_VIEW_DOMAIN the wildcard must stay on the base domain:\n%s", out)
	}
	if !strings.Contains(out, "{labels.2}") {
		t.Errorf("label index changed for a single-domain install:\n%s", out)
	}
}
