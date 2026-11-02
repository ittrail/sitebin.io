package caddygen

import (
	"strings"
	"testing"
)

// The custom-domain block takes the folder and the prefix from authz's
// answer; see docs/superpowers/specs/2026-11-02-custom-domains-with-a-path-design.md.

// block is the text of the site block that starts with header.
func siteBlock(t *testing.T, out, header string) string {
	t.Helper()
	i := strings.Index(out, "\n"+header+" {")
	if i < 0 {
		t.Fatalf("no %q block in\n%s", header, out)
	}
	rest := out[i+1:]
	if j := strings.Index(rest, "\n}\n"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// inOrder fails unless every want appears in body, each after the one before.
func inOrder(t *testing.T, body string, wants ...string) {
	t.Helper()
	at := 0
	for _, w := range wants {
		i := strings.Index(body[at:], w)
		if i < 0 {
			t.Fatalf("%q missing (or out of order) after offset %d in\n%s", w, at, body)
		}
		at += i + len(w)
	}
}

func TestCustomDomainBlockRoutesMounts(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN":  "app.sitebin.io",
		"SITEBIN_VIEW_DOMAIN":  "sitebin.app",
		"SITEBIN_DNS_PROVIDER": "hetzner",
		"SITEBIN_DNS_TOKEN":    "x",
	})
	out := Generate(cfg)
	cd := siteBlock(t, out, "https://")
	inOrder(t, cd,
		"request_header -X-Sitebin-Site",
		"request_header -X-Sitebin-Strip",
		"request_header -X-Sitebin-Mount",
		"request_header -X-Forwarded-Prefix",
		"forward_auth 127.0.0.1:9000",
		"copy_headers Set-Cookie X-Sitebin-Upstream X-Sitebin-Cache X-Sitebin-Site X-Sitebin-Strip X-Sitebin-Mount",
		"root * /data/sites/{http.request.header.X-Sitebin-Site}/files",
		"@strip header X-Sitebin-Strip *",
		"uri @strip strip_prefix {http.request.header.X-Sitebin-Strip}",
		"@sitebinroute header X-Sitebin-Upstream 127.0.0.1:8080",
		"reverse_proxy @sitebinroute 127.0.0.1:8080",
		"@untrusted not file /.sitebin-trusted",
		"@proxiedmount {",
		"header X-Sitebin-Upstream *",
		"header X-Sitebin-Mount *",
		"reverse_proxy @proxiedmount {http.request.header.X-Sitebin-Upstream} {",
		"header_up X-Forwarded-Prefix {http.request.header.X-Sitebin-Mount}",
		"@proxied header X-Sitebin-Upstream *",
	)
	if strings.Contains(cd, "domain-index/{host}") {
		t.Error("the custom-domain root must come from authz, not from the host")
	}
	view := siteBlock(t, out, "*.sitebin.app")
	if !strings.Contains(view, "root * /data/sites/{labels.2}/files") || strings.Contains(view, "strip_prefix {http.request.header") {
		t.Errorf("the view wildcard must keep its label root and strip nothing:\n%s", view)
	}
	// Client copies of every internal header go before the gate in the view
	// block too.
	inOrder(t, view, "request_header -X-Sitebin-Mount", "request_header -X-Forwarded-Prefix", "forward_auth")
}

func TestBackendHandleStripsMount(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN":  "app.sitebin.io",
		"SITEBIN_VIEW_DOMAIN":  "sitebin.app",
		"SITEBIN_DNS_PROVIDER": "hetzner",
		"SITEBIN_DNS_TOKEN":    "x",
	})
	out := Generate(cfg)
	n := strings.Count(out, "handle @backend {")
	if n < 2 {
		t.Fatalf("handle @backend appears %d times", n)
	}
	for i, part := range strings.Split(out, "handle @backend {")[1:] {
		end := strings.Index(part, "}")
		if !strings.Contains(part[:end], "request_header -X-Sitebin-Mount") || strings.Index(part[:end], "request_header -X-Sitebin-Mount") > strings.Index(part[:end], "reverse_proxy") {
			t.Errorf("handle @backend #%d does not drop a client's X-Sitebin-Mount before proxying:\n%s", i+1, part[:end])
		}
	}
}

// A client's X-Sitebin-View would make authz check another site than the one
// whose folder the view wildcard serves (its root keys on the label): every
// content block removes it before the gate (review, 2026-11-02).
func TestContentBlocksStripTheViewHeader(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN":  "app.sitebin.io",
		"SITEBIN_VIEW_DOMAIN":  "sitebin.app",
		"SITEBIN_DNS_PROVIDER": "hetzner",
		"SITEBIN_DNS_TOKEN":    "x",
	})
	out := Generate(cfg)
	for _, h := range []string{"*.sitebin.app", "https://"} {
		inOrder(t, siteBlock(t, out, h), "request_header -X-Sitebin-View", "forward_auth")
	}
}
