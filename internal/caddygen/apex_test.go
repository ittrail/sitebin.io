package caddygen

import (
	"strings"
	"testing"
)

// block returns the site block that starts with header, up to its closing
// brace at column 0.
func block(out, header string) string {
	i := strings.Index(out, header)
	if i < 0 {
		return ""
	}
	j := strings.Index(out[i:], "\n}\n")
	if j < 0 {
		return out[i:]
	}
	return out[i : i+j+2]
}

// The view domain's apex and www answer with the info page: a block of their
// own, which Caddy sorts before the wildcard, with a certificate the wildcard
// does not cover — obtained the way the wildcard's is.
func TestViewApexBlock(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN":  "app.sitebin.io",
		"SITEBIN_VIEW_DOMAIN":  "sitebin.app",
		"SITEBIN_DNS_PROVIDER": "hetzner",
		"SITEBIN_DNS_TOKEN":    "tok",
	})
	out := Generate(cfg)
	b := block(out, "sitebin.app, www.sitebin.app {")
	if b == "" {
		t.Fatalf("no apex block:\n%s", out)
	}
	for _, want := range []string{
		"dns hetzner {env.SITEBIN_DNS_TOKEN}",
		"propagation_delay 60s",
		`Strict-Transport-Security "max-age=31536000; includeSubDomains"`,
		"reverse_proxy 127.0.0.1:8080",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("apex block lacks %q:\n%s", want, b)
		}
	}
	// It proxies the backend and nothing else: no files, no gate.
	for _, bad := range []string{"file_server", "forward_auth", "root *"} {
		if strings.Contains(b, bad) {
			t.Errorf("apex block carries %q:\n%s", bad, b)
		}
	}
	if strings.Count(out, "www.sitebin.app") != 1 {
		t.Errorf("www named more than once:\n%s", out)
	}
}

func TestViewApexUsesTheTLSSnippet(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN": "app.sitebin.io",
		"SITEBIN_VIEW_DOMAIN": "sitebin.app",
		"SITEBIN_TLS_SNIPPET": "dns route53\npropagation_delay 30s",
	})
	b := block(Generate(cfg), "sitebin.app, www.sitebin.app {")
	if !strings.Contains(b, "dns route53") || !strings.Contains(b, "propagation_delay 30s") {
		t.Fatalf("apex block ignores the snippet:\n%s", b)
	}
}

func TestViewApexHTTPOnly(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN": "app.sitebin.test",
		"SITEBIN_VIEW_DOMAIN": "sitebin-user.test",
		"SITEBIN_HTTP_ONLY":   "true",
	})
	b := block(Generate(cfg), "http://sitebin-user.test, http://www.sitebin-user.test {")
	if b == "" || strings.Contains(b, "tls") || strings.Contains(b, "Strict-Transport-Security") {
		t.Fatalf("http-only apex block:\n%s", Generate(cfg))
	}
}

// One domain for everything: the apex IS the app, and no block is added.
func TestNoApexBlockOnASingleDomain(t *testing.T) {
	cfg := mustLoad(t, map[string]string{"SITEBIN_BASE_DOMAIN": "sitebin.example", "SITEBIN_HTTP_ONLY": "true"})
	if out := Generate(cfg); strings.Contains(out, "www.sitebin.example") {
		t.Fatalf("an apex block on a single-domain install:\n%s", out)
	}
}

// A base domain that happens to be www.<view> keeps its own block; the apex
// block must not name it twice.
func TestApexNeverDuplicatesTheBaseDomain(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN": "www.example.test",
		"SITEBIN_VIEW_DOMAIN": "example.test",
		"SITEBIN_HTTP_ONLY":   "true",
	})
	out := Generate(cfg)
	if strings.Count(out, "www.example.test") != 1 || !strings.Contains(out, "http://example.test {") {
		t.Fatalf("apex block with a www base domain:\n%s", out)
	}
}

// The file server hides the two markers and nothing else, so an uploaded
// .well-known/security.txt is served like any file.
func TestFileServerHidesOnlyTheMarkers(t *testing.T) {
	cfg := mustLoad(t, map[string]string{"SITEBIN_BASE_DOMAIN": "sitebin.example", "SITEBIN_HTTP_ONLY": "true", "SITEBIN_VIEW_ACCESS": "both"})
	for _, line := range strings.Split(Generate(cfg), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "hide ") && line != "hide .sitebin-spa .sitebin-trusted" {
			t.Errorf("file_server hides more than the markers: %q", line)
		}
	}
}
