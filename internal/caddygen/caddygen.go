// Package caddygen renders the Caddyfile that fronts Sitebin. Caddy does all
// TLS, routing, and static serving; the backend only answers proxied app
// routes plus the internal authz/tls-check subrequests. The file is fully
// determined by the environment configuration — operators never edit it.
package caddygen

import (
	"fmt"
	"strings"

	"github.com/ittrail/sitebin.io/internal/config"
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

	// ---- main domain: UI + API + WebDAV (+ optional /v/<id> path views) ----
	fmt.Fprintf(&b, "%s%s {\n", scheme, cfg.BaseDomain)
	b.WriteString("\tencode zstd gzip\n")
	if cfg.PathViews() {
		writePathViewRoutes(&b, backend, cfg.DataDir)
		fmt.Fprintf(&b, "\thandle {\n\t\treverse_proxy %s\n\t}\n", backend("8080"))
	} else {
		fmt.Fprintf(&b, "\treverse_proxy %s\n", backend("8080"))
	}
	b.WriteString("}\n\n")

	// ---- view subdomains: wildcard cert, pure static serving ----
	if cfg.SubdomainViews() {
		fmt.Fprintf(&b, "%s*.%s {\n", scheme, cfg.BaseDomain)
		if !cfg.HTTPOnly {
			b.WriteString("\ttls {\n")
			if cfg.TLSSnippet != "" {
				for _, line := range strings.Split(cfg.TLSSnippet, "\n") {
					fmt.Fprintf(&b, "\t\t%s\n", line)
				}
			} else {
				fmt.Fprintf(&b, "\t\tdns %s {env.SITEBIN_DNS_TOKEN}\n", cfg.DNSProvider)
				// Managed DNS backends publish zone changes with a delay, and
				// recursive resolvers negative-cache the challenge name; skip
				// the pre-check and give the record a fixed head start instead.
				b.WriteString("\t\tpropagation_delay 60s\n")
			}
			b.WriteString("\t}\n")
		}
		writeContentRoutes(&b, backend, fmt.Sprintf("%s/sites/{labels.%d}/files", cfg.DataDir, labelIdx))
		b.WriteString("}\n\n")
	}

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

// writePathViewRoutes emits main-domain routing for /v/<view-id> path views.
// A `route` block preserves directive order so forward_auth sees the full URI
// (for the gate redirect) before it is stripped for the file server.
func writePathViewRoutes(b *strings.Builder, backend func(string) string, dataDir string) {
	b.WriteString("\t# --- path-served sites: /v/<view-id>/ (SITEBIN_VIEW_ACCESS=path|both) ---\n")
	b.WriteString("\t@viewbare path_regexp vb ^/v/([a-z2-7]{26})$\n")
	b.WriteString("\tredir @viewbare /v/{re.vb.1}/ 308\n")
	b.WriteString("\t@view path_regexp view ^/v/([a-z2-7]{26})/.*$\n")
	b.WriteString("\thandle @view {\n")
	b.WriteString("\t\troute {\n")
	fmt.Fprintf(b, "\t\t\tforward_auth %s {\n\t\t\t\turi /internal/authz\n\t\t\t\theader_up X-Sitebin-View {re.view.1}\n\t\t\t\tcopy_headers Set-Cookie\n\t\t\t}\n", backend("9000"))
	b.WriteString("\t\t\turi strip_prefix /v/{re.view.1}\n")
	fmt.Fprintf(b, "\t\t\troot * %s/sites/{re.view.1}/files\n", dataDir)
	writeFileServing(b, "\t\t\t")
	b.WriteString("\t\t}\n")
	b.WriteString("\t}\n")
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
	writeFileServing(b, "\t\t")
	b.WriteString("\t}\n")
}

// writeFileServing emits SPA-aware static serving: when the site opted into SPA
// fallback (its .sitebin-spa marker exists in the root), unknown paths fall back
// to /index.html; otherwise normal serving with a directory listing.
func writeFileServing(b *strings.Builder, ind string) {
	fmt.Fprintf(b, "%s@spa file /%s\n", ind, spaMarkerName)
	fmt.Fprintf(b, "%shandle @spa {\n", ind)
	fmt.Fprintf(b, "%s\ttry_files {path} /index.html\n", ind)
	fmt.Fprintf(b, "%s\tfile_server {\n%s\t\thide %s\n%s\t}\n", ind, ind, spaMarkerName, ind)
	fmt.Fprintf(b, "%s}\n", ind)
	fmt.Fprintf(b, "%shandle {\n", ind)
	fmt.Fprintf(b, "%s\tfile_server {\n%s\t\tbrowse\n%s\t\thide %s\n%s\t}\n", ind, ind, ind, spaMarkerName, ind)
	fmt.Fprintf(b, "%s}\n", ind)
}

// spaMarkerName mirrors store.SPAMarker (kept local to avoid an import cycle
// risk; asserted equal by a test).
const spaMarkerName = ".sitebin-spa"
