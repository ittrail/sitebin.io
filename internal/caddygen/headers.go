package caddygen

import (
	"fmt"
	"strings"
)

// cspReportPath is where violation reports go. It lives under /_sitebin/, which
// Caddy already routes straight to the backend without the authz subrequest — a
// report about a gated or expired site still has to arrive.
const cspReportPath = "/_sitebin/csp-report"

// trustedMarkerName mirrors store.TrustedMarker (kept local for the same reason
// spaMarkerName is; asserted equal by a test).
const trustedMarkerName = ".sitebin-trusted"

// baselineCSP is what every site gets, in both editions. Nothing here breaks a
// working site.
//
// base-uri is 'self' rather than 'none': Angular and other frameworks ship
// <base href="/"> as a matter of course, and 'none' forbids the element
// outright. 'self' still stops a <base> that repoints every relative URL at an
// attacker's host.
const baselineCSP = `object-src 'none'; base-uri 'self'`

// untrustedCSP is the whole policy for untrusted content — the baseline with
// the exfiltration blocks folded in, not a second header layered on top.
//
// An earlier version emitted two Content-Security-Policy headers and relied on
// them intersecting. That is correct per spec, but getting Caddy to emit both
// reliably meant depending on the order of two `header` directives, and the
// order did not hold: measured against the live instance, one layer's
// Reporting-Endpoints arrived while its Referrer-Policy and appended CSP did
// not. A site can be half-hardened that way and still look configured. One
// complete policy per case cannot.
//
// form-action is its own directive because connect-src does NOT cover form
// submissions. Without it, <form action="https://evil"> stays wide open, which
// is the oldest credential-exfiltration trick there is.
const untrustedCSP = baselineCSP +
	`; form-action 'none'; connect-src 'self'; frame-ancestors 'none'; frame-src 'none'` +
	`; report-uri ` + cspReportPath + `; report-to csp`

const permissionsPolicy = `geolocation=(), camera=(), microphone=(), payment=(), usb=(), midi=()`

// writeSecurityHeaders emits one header block per trust state. The two matchers
// are exact complements, so every response takes exactly one of them and no
// ordering between them can matter.
//
// It must come after `root`, because the trust matcher is a file test resolved
// against the site root.
func writeSecurityHeaders(b *strings.Builder, ind string) {
	// Keyed on the ABSENCE of the trust marker. That polarity is the point: a
	// marker that was never written, or was lost in a restore, leaves a site
	// served too strictly — never unprotected.
	fmt.Fprintf(b, "%s@untrusted not file /%s\n", ind, trustedMarkerName)
	fmt.Fprintf(b, "%sheader @untrusted {\n", ind)
	writeCommonHeaders(b, ind+"\t", "no-referrer", untrustedCSP)
	fmt.Fprintf(b, "%s\tReporting-Endpoints %q\n", ind, `csp="`+cspReportPath+`"`)
	fmt.Fprintf(b, "%s}\n", ind)

	fmt.Fprintf(b, "%s@trusted file /%s\n", ind, trustedMarkerName)
	fmt.Fprintf(b, "%sheader @trusted {\n", ind)
	writeCommonHeaders(b, ind+"\t", "strict-origin-when-cross-origin", baselineCSP)
	fmt.Fprintf(b, "%s}\n", ind)
}

func writeCommonHeaders(b *strings.Builder, ind, referrer, csp string) {
	fmt.Fprintf(b, "%sX-Content-Type-Options nosniff\n", ind)
	fmt.Fprintf(b, "%sReferrer-Policy %s\n", ind, referrer)
	fmt.Fprintf(b, "%sPermissions-Policy %q\n", ind, permissionsPolicy)
	fmt.Fprintf(b, "%sContent-Security-Policy %q\n", ind, csp)
}
