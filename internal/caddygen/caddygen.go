// Package caddygen renders the Caddyfile that fronts Sitebin. Caddy does all
// TLS, routing, and static serving; the backend only answers proxied app
// routes plus the internal authz/tls-check subrequests. The file is fully
// determined by the environment configuration — operators never edit it.
package caddygen

import (
	"fmt"
	"strings"

	"github.com/ittrail/sitebin/internal/config"
)

// Generate renders the complete Caddyfile for cfg.
func Generate(cfg config.Config) string {
	var b strings.Builder
	backend := func(port string) string { return cfg.BackendHost + ":" + port }
	labelIdx := len(strings.Split(cfg.BaseDomain, "."))

	// ---- global options ----
	b.WriteString("{\n\tadmin off\n")
	if cfg.ACMEEmail != "" {
		fmt.Fprintf(&b, "\temail %s\n", cfg.ACMEEmail)
	}
	fmt.Fprintf(&b, "\tstorage file_system %s/caddy\n", cfg.DataDir)
	if cfg.HTTPOnly {
		b.WriteString("\tauto_https off\n")
	} else {
		fmt.Fprintf(&b, "\ton_demand_tls {\n\t\task http://%s/internal/tls-check\n\t}\n", backend("9000"))
	}
	b.WriteString("}\n\n")

	scheme := ""
	if cfg.HTTPOnly {
		scheme = "http://"
	}

	// ---- main domain: UI + API + WebDAV, never user content ----
	fmt.Fprintf(&b, "%s%s {\n", scheme, cfg.BaseDomain)
	b.WriteString("\tencode zstd gzip\n")
	fmt.Fprintf(&b, "\treverse_proxy %s\n", backend("8080"))
	b.WriteString("}\n\n")

	// ---- view subdomains: wildcard cert, pure static serving ----
	fmt.Fprintf(&b, "%s*.%s {\n", scheme, cfg.BaseDomain)
	if !cfg.HTTPOnly {
		b.WriteString("\ttls {\n")
		if cfg.TLSSnippet != "" {
			for _, line := range strings.Split(cfg.TLSSnippet, "\n") {
				fmt.Fprintf(&b, "\t\t%s\n", line)
			}
		} else {
			fmt.Fprintf(&b, "\t\tdns %s {env.SITEBIN_DNS_TOKEN}\n", cfg.DNSProvider)
		}
		b.WriteString("\t}\n")
	}
	writeContentRoutes(&b, backend, fmt.Sprintf("%s/sites/{labels.%d}/files", cfg.DataDir, labelIdx))
	b.WriteString("}\n\n")

	// ---- custom domains: catch-all with on-demand TLS ----
	if cfg.HTTPOnly {
		b.WriteString("http://:80 {\n")
	} else {
		b.WriteString("https:// {\n\ttls {\n\t\ton_demand\n\t}\n")
	}
	writeContentRoutes(&b, backend, cfg.DataDir+"/domain-index/{host}/files")
	b.WriteString("}\n")

	if !cfg.HTTPOnly {
		// Redirect plain-HTTP custom-domain hits; ACME HTTP-01 challenges are
		// intercepted by Caddy before this site matches.
		b.WriteString("\nhttp:// {\n\tredir https://{host}{uri} permanent\n}\n")
	}
	return b.String()
}

// writeContentRoutes emits the shared routing shape of every content origin:
// /_sitebin/* goes to the backend (unlock endpoint + shared viewer assets),
// everything else passes the authz gate then hits the file server.
func writeContentRoutes(b *strings.Builder, backend func(string) string, root string) {
	b.WriteString("\tencode zstd gzip\n")
	b.WriteString("\t@backend path /_sitebin/*\n")
	fmt.Fprintf(b, "\thandle @backend {\n\t\treverse_proxy %s\n\t}\n", backend("8080"))
	b.WriteString("\thandle {\n")
	// Pin X-Forwarded-Host to Caddy's own {host} placeholder so authz resolves
	// the SAME site that file_server will serve. Without this, a client-supplied
	// X-Forwarded-Host could make the gate evaluate a different (open) site than
	// the one whose files are served — a password-gate bypass.
	fmt.Fprintf(b, "\t\tforward_auth %s {\n\t\t\turi /internal/authz\n\t\t\theader_up X-Forwarded-Host {host}\n\t\t\tcopy_headers Set-Cookie\n\t\t}\n", backend("9000"))
	fmt.Fprintf(b, "\t\troot * %s\n", root)
	b.WriteString("\t\tfile_server {\n\t\t\tbrowse\n\t\t}\n")
	b.WriteString("\t}\n")
}
