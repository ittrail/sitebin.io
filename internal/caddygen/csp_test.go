package caddygen

import (
	"strings"
	"testing"
)

// wantUntrustedCSP is the whole untrusted policy with the default allowlists,
// spelled out: a directive that goes missing here goes missing on every drop
// and free site.
const wantUntrustedCSP = `object-src 'none'; base-uri 'self'; form-action 'none'; connect-src 'self'; frame-ancestors 'none'; frame-src 'none'` +
	`; img-src 'self' data: blob:` +
	`; script-src 'self' 'unsafe-inline' 'unsafe-eval' https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com https://cdn.tailwindcss.com https://code.jquery.com` +
	`; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com` +
	`; font-src 'self' data: https://fonts.gstatic.com https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com` +
	`; media-src 'self' data: blob:; worker-src 'self' blob:; manifest-src 'self'` +
	`; report-uri /_sitebin/csp-report; report-to csp`

// headerBlocks returns the bodies of every `header @<matcher> { … }` block.
func headerBlocks(out, matcher string) []string {
	var blocks []string
	for rest := out; ; {
		i := strings.Index(rest, "header @"+matcher+" {")
		if i < 0 {
			return blocks
		}
		rest = rest[i:]
		j := strings.Index(rest, "}")
		blocks = append(blocks, rest[:j])
		rest = rest[j:]
	}
}

// Every content origin — the view wildcard, the custom-domain catch-all and
// the path views — carries exactly this policy on its untrusted branch, and
// the trusted branch keeps the baseline. Each branch is ONE complete header
// (the Caddy trap of the 2026-08-27 design).
func TestUntrustedPolicyIsExact(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN": "sitebin.example",
		"SITEBIN_HTTP_ONLY":   "true",
		"SITEBIN_VIEW_ACCESS": "both",
	})
	out := Generate(cfg)
	untrusted, trusted := headerBlocks(out, "untrusted"), headerBlocks(out, "trusted")
	if len(untrusted) != 3 || len(trusted) != 3 {
		t.Fatalf("want 3 untrusted and 3 trusted header blocks, got %d and %d:\n%s", len(untrusted), len(trusted), out)
	}
	for _, b := range untrusted {
		if !strings.Contains(b, `Content-Security-Policy "`+wantUntrustedCSP+`"`) {
			t.Errorf("untrusted block does not carry the exact policy:\n%s\nwant %s", b, wantUntrustedCSP)
		}
		if strings.Count(b, "Content-Security-Policy") != 1 {
			t.Errorf("one complete policy per branch:\n%s", b)
		}
		for _, want := range []string{"Referrer-Policy no-referrer", `Reporting-Endpoints "csp=\"/_sitebin/csp-report\""`, "X-Content-Type-Options nosniff"} {
			if !strings.Contains(b, want) {
				t.Errorf("untrusted block lacks %q:\n%s", want, b)
			}
		}
	}
	for _, b := range trusted {
		if !strings.Contains(b, `Content-Security-Policy "object-src 'none'; base-uri 'self'"`) || strings.Contains(b, "script-src") || strings.Contains(b, "img-src") {
			t.Errorf("the trusted policy changed:\n%s", b)
		}
	}
}

func TestUntrustedPolicyFollowsTheConfiguredHosts(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN":      "sitebin.example",
		"SITEBIN_HTTP_ONLY":        "true",
		"SITEBIN_CSP_SCRIPT_HOSTS": "https://cdn.jsdelivr.net",
		"SITEBIN_CSP_STYLE_HOSTS":  "none",
		"SITEBIN_CSP_FONT_HOSTS":   "https://fonts.gstatic.com",
		"SITEBIN_CSP_IMG_HOSTS":    "https://images.example.org",
	})
	out := Generate(cfg)
	for _, want := range []string{
		"; img-src 'self' data: blob: https://images.example.org;",
		"; script-src 'self' 'unsafe-inline' 'unsafe-eval' https://cdn.jsdelivr.net;",
		"; style-src 'self' 'unsafe-inline';",
		"; font-src 'self' data: https://fonts.gstatic.com;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "unpkg.com") {
		t.Error("a default host survived an override")
	}
}
