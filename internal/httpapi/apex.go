package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The view domain's apex and www (sitebin.app, www.sitebin.app on the hosted
// instance) serve no site: they say whose domain this is and how to report a
// site on it, and publish security.txt. Before this, the apex failed its TLS
// handshake and www answered 404, and nobody outside could learn who to tell
// about a phishing page. Caddy proxies both hosts here (caddygen's apex block);
// see docs/superpowers/specs/2026-09-29-provenance-csp-apex.md.

// securityTxtLifetime is how far ahead security.txt's Expires lies. RFC 9116
// asks for less than a year; the file is generated, so it never goes stale.
const securityTxtLifetime = 180 * 24 * time.Hour

// viewApexHost reports whether host is the separate view domain's apex or its
// www — never a site: view ids are 26 base32 characters.
func (a *API) viewApexHost(host string) bool {
	if a.cfg.ViewDomain == a.cfg.BaseDomain || !a.cfg.SubdomainViews() {
		return false
	}
	h := strings.ToLower(hostWithoutPort(host))
	if h == a.cfg.BaseDomain {
		return false
	}
	return h == a.cfg.ViewDomain || h == "www."+a.cfg.ViewDomain
}

// viewApexGuard answers the view apex hosts with the info page and
// security.txt, and everything else there with a 404 — the app's routes
// (accounts, API, MCP, WebDAV) belong to the base domain and never answer on
// the domain that serves user content.
func (a *API) viewApexGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.viewApexHost(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		switch {
		case r.URL.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			a.viewApexPage(w)
		case r.URL.Path == securityTxtPath && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			a.securityTxt(w, r)
		default:
			a.msgPage(w, 404, "Page not found", "There is nothing here. The sites on this domain live at their own addresses.")
		}
	})
}

// abuseLinks is how the system pages say where to report abuse.
type abuseLinks struct {
	ReportURL   string
	ReportLabel string
	Email       string
}

// abuse returns the instance's abuse contacts, or nil when it has none.
func (a *API) abuse() *abuseLinks {
	if a.cfg.AbuseContact == "" && a.cfg.AbuseReportURL == "" {
		return nil
	}
	l := &abuseLinks{ReportURL: a.cfg.AbuseReportURL, Email: a.cfg.AbuseContact}
	if u, err := url.Parse(a.cfg.AbuseReportURL); err == nil {
		l.ReportLabel = strings.TrimSuffix(u.Host+u.Path, "/")
	}
	return l
}

// viewApexPage is the info page: static, noindex, nothing loaded from
// anywhere, in the gate page's look.
func (a *API) viewApexPage(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Robots-Tag", "noindex")
	home, homeLabel := a.cfg.HomeURL, ""
	if u, err := url.Parse(home); err == nil {
		homeLabel = strings.TrimPrefix(u.Host, "www.")
	}
	a.renderPage(w, 200, pageData{
		Title: "User content on " + a.cfg.ViewDomain,
		Message: a.cfg.ViewDomain + " serves the websites people publish with Sitebin on " + a.cfg.BaseDomain +
			". Every address under it is a site made by one of its users — third parties, not the operator of this service.",
		More: []string{
			"Sites go live the moment they are uploaded and are not reviewed first. If a site here is phishing, malware or spam, or otherwise abusive, please report it so the operator can take it down.",
		},
		Abuse:     a.abuse(),
		Home:      home,
		HomeLabel: homeLabel,
	})
}

const securityTxtPath = "/.well-known/security.txt"

// securityTxt answers RFC 9116's file on the base domain and the view apex.
// It names the abuse mailbox and the report page; with neither configured
// there is no valid file (Contact is required), so it is a 404.
func (a *API) securityTxt(w http.ResponseWriter, r *http.Request) {
	if !a.viewApexHost(r.Host) && strings.ToLower(hostWithoutPort(r.Host)) != a.cfg.BaseDomain {
		a.notFoundPage(w, r)
		return
	}
	if a.cfg.AbuseContact == "" && a.cfg.AbuseReportURL == "" {
		a.notFoundPage(w, r)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Security and abuse contact for %s", a.cfg.BaseDomain)
	if a.cfg.ViewDomain != a.cfg.BaseDomain {
		fmt.Fprintf(&b, " and the user content it serves on %s", a.cfg.ViewDomain)
	}
	b.WriteString(".\n")
	if a.cfg.AbuseContact != "" {
		fmt.Fprintf(&b, "Contact: mailto:%s\n", a.cfg.AbuseContact)
	}
	if a.cfg.AbuseReportURL != "" {
		fmt.Fprintf(&b, "Contact: %s\n", a.cfg.AbuseReportURL)
	}
	expires := time.Now().UTC().Add(securityTxtLifetime).Truncate(24 * time.Hour)
	fmt.Fprintf(&b, "Expires: %s\n", expires.Format(time.RFC3339))
	b.WriteString("Preferred-Languages: en, de\n")
	for _, h := range a.securityTxtHosts() {
		fmt.Fprintf(&b, "Canonical: %s%s\n", a.cfg.SiteURL(h), securityTxtPath)
	}
	if a.cfg.AbuseReportURL != "" {
		fmt.Fprintf(&b, "Policy: %s\n", a.cfg.AbuseReportURL)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte(b.String()))
}

// securityTxtHosts are the hosts the file is served on, each its canonical
// location: one body for all of them.
func (a *API) securityTxtHosts() []string {
	hosts := []string{a.cfg.BaseDomain}
	if a.cfg.ViewDomain != a.cfg.BaseDomain && a.cfg.SubdomainViews() {
		for _, h := range []string{a.cfg.ViewDomain, "www." + a.cfg.ViewDomain} {
			if h != a.cfg.BaseDomain {
				hosts = append(hosts, h)
			}
		}
	}
	return hosts
}
