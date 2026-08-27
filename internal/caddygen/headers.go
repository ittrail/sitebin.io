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

// baselineCSP is hygiene every site gets, in both editions.
//
// base-uri is 'self' rather than 'none': Angular and other frameworks ship
// <base href="/"> as a matter of course, and 'none' forbids the element
// outright. 'self' still stops a <base> that repoints every relative URL at an
// attacker's host.
const baselineCSP = `object-src 'none'; base-uri 'self'`

// strictCSP is the layer untrusted content gets on top. Multiple
// Content-Security-Policy headers INTERSECT — each is enforced independently —
// so this can only narrow the baseline, and an uploaded page cannot loosen
// either with a <meta> policy of its own.
//
// form-action is its own directive because connect-src does NOT cover form
// submissions. Without it, <form action="https://evil"> stays wide open, which
// is the oldest credential-exfiltration trick there is.
const strictCSP = `form-action 'none'; connect-src 'self'; frame-ancestors 'none'; frame-src 'none'; report-uri ` +
	cspReportPath + `; report-to csp`

const permissionsPolicy = `geolocation=(), camera=(), microphone=(), payment=(), usb=(), midi=()`

// writeSecurityHeaders emits both layers for one content origin. It must come
// after `root`, because the trust matcher is a file test resolved against the
// site root.
// Both header directives live in a `route` block. Caddy does not guarantee the
// order of multiple header directives outside one, and unordered they lose the
// fields the two blocks share: the baseline's Referrer-Policy wins over the
// strict one, and the appended CSP never lands at all. `route` is the same
// remedy writePathViewRoutes already uses to keep forward_auth ahead of the
// URI rewrite.
func writeSecurityHeaders(b *strings.Builder, ind string) {
	fmt.Fprintf(b, "%s@untrusted not file /%s\n", ind, trustedMarkerName)
	fmt.Fprintf(b, "%sroute {\n", ind)
	ind += "\t"
	fmt.Fprintf(b, "%sheader {\n", ind)
	fmt.Fprintf(b, "%s\tX-Content-Type-Options nosniff\n", ind)
	fmt.Fprintf(b, "%s\tReferrer-Policy strict-origin-when-cross-origin\n", ind)
	fmt.Fprintf(b, "%s\tPermissions-Policy %q\n", ind, permissionsPolicy)
	fmt.Fprintf(b, "%s\tContent-Security-Policy %q\n", ind, baselineCSP)
	fmt.Fprintf(b, "%s}\n", ind)

	// Keyed on the ABSENCE of the trust marker. That polarity is the point: a
	// marker that was never written, or was lost in a restore, leaves a site
	// served too strictly — never unprotected.
	fmt.Fprintf(b, "%sheader @untrusted {\n", ind)
	fmt.Fprintf(b, "%s\tReferrer-Policy no-referrer\n", ind)
	// A leading + adds a second CSP header instead of replacing the baseline.
	// The two then intersect, which is what makes the strict layer unable to
	// widen anything the baseline set.
	fmt.Fprintf(b, "%s\t+Content-Security-Policy %q\n", ind, strictCSP)
	fmt.Fprintf(b, "%s\tReporting-Endpoints %q\n", ind, `csp="`+cspReportPath+`"`)
	fmt.Fprintf(b, "%s}\n", ind)
	fmt.Fprintf(b, "%s}\n", strings.TrimSuffix(ind, "\t"))
}
