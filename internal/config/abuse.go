package config

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
)

// The content-security policy of untrusted sites names the hosts a page may
// load scripts, styles, fonts and images from (see
// internal/caddygen/headers.go). The lists are instance configuration, so the
// operator can widen or narrow them without a release.
var (
	DefaultCSPScriptHosts = []string{"https://cdnjs.cloudflare.com", "https://cdn.jsdelivr.net", "https://unpkg.com", "https://cdn.tailwindcss.com", "https://code.jquery.com"}
	DefaultCSPStyleHosts  = []string{"https://fonts.googleapis.com", "https://cdnjs.cloudflare.com", "https://cdn.jsdelivr.net", "https://unpkg.com"}
	DefaultCSPFontHosts   = []string{"https://fonts.gstatic.com", "https://cdnjs.cloudflare.com", "https://cdn.jsdelivr.net", "https://unpkg.com"}
	DefaultCSPImgHosts    = []string{}
)

// hostSourceRe is a CSP host-source and nothing else: an optional http(s)
// scheme, a host (a leading "*." allowed), an optional port and an optional
// plain path. No keyword, no quote, no separator, no bare scheme: the value
// is written into the Caddyfile and into a response header, and a list that
// could carry "; script-src *" or "https:" would undo the policy it extends.
var hostSourceRe = regexp.MustCompile(`^(https?://)?(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+(:([0-9]{1,5}|\*))?(/[A-Za-z0-9._~%/-]*)?$`)

// cspHosts reads one allowlist: unset keeps def, "none" empties it, anything
// else is a comma- or space-separated list of host sources.
func cspHosts(getenv func(string) string, name string, def []string) ([]string, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return append([]string{}, def...), nil
	}
	if strings.EqualFold(raw, "none") || raw == "'none'" {
		return []string{}, nil
	}
	if strings.ContainsAny(raw, ";'\"\r\n") {
		return nil, fmt.Errorf("%s: %q holds a character no host source may", name, raw)
	}
	out := []string{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		scheme, rest, ok := strings.Cut(part, "://")
		if ok {
			part = strings.ToLower(scheme) + "://" + lowerHost(rest)
		} else {
			part = lowerHost(part)
		}
		if !hostSourceRe.MatchString(part) {
			return nil, fmt.Errorf("%s: %q is not a host source (want https://cdn.example.com, https://*.example.com or none)", name, part)
		}
		out = append(out, part)
	}
	return out, nil
}

// lowerHost lowercases the host part of host[:port][/path], leaving the path.
func lowerHost(s string) string {
	host, path, found := strings.Cut(s, "/")
	if found {
		return strings.ToLower(host) + "/" + path
	}
	return strings.ToLower(host)
}

// loadAbuse reads the CSP allowlists and the public abuse contacts
// (SITEBIN_ABUSE_CONTACT, SITEBIN_ABUSE_REPORT_URL, SITEBIN_HOME_URL). It runs
// after the base domain and HTTP mode are known, which the defaults need.
func loadAbuse(getenv func(string) string, cfg *Config) error {
	var err error
	if cfg.CSPScriptHosts, err = cspHosts(getenv, "SITEBIN_CSP_SCRIPT_HOSTS", DefaultCSPScriptHosts); err != nil {
		return err
	}
	if cfg.CSPStyleHosts, err = cspHosts(getenv, "SITEBIN_CSP_STYLE_HOSTS", DefaultCSPStyleHosts); err != nil {
		return err
	}
	if cfg.CSPFontHosts, err = cspHosts(getenv, "SITEBIN_CSP_FONT_HOSTS", DefaultCSPFontHosts); err != nil {
		return err
	}
	if cfg.CSPImgHosts, err = cspHosts(getenv, "SITEBIN_CSP_IMG_HOSTS", DefaultCSPImgHosts); err != nil {
		return err
	}

	if v := strings.TrimSpace(getenv("SITEBIN_ABUSE_CONTACT")); v != "" {
		// A bare address: it becomes a mailto: link and a security.txt line.
		if a, perr := mail.ParseAddress(v); perr != nil || a.Name != "" || a.Address != v {
			return fmt.Errorf("SITEBIN_ABUSE_CONTACT: %q must be a bare address such as abuse@example.com", v)
		}
		cfg.AbuseContact = v
	}
	base := cfg.SiteURL(cfg.BaseDomain)
	cfg.AbuseReportURL = base + "/report"
	if v := strings.TrimSpace(getenv("SITEBIN_ABUSE_REPORT_URL")); v != "" {
		if strings.EqualFold(v, "none") {
			cfg.AbuseReportURL = ""
		} else if cfg.AbuseReportURL, err = absoluteURL("SITEBIN_ABUSE_REPORT_URL", v); err != nil {
			return err
		}
	}
	cfg.HomeURL = base
	if v := strings.TrimSpace(getenv("SITEBIN_HOME_URL")); v != "" {
		if cfg.HomeURL, err = absoluteURL("SITEBIN_HOME_URL", v); err != nil {
			return err
		}
	}
	return nil
}

func absoluteURL(name, v string) (string, error) {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.ContainsAny(v, "\"'<> \r\n") {
		return "", fmt.Errorf("%s: %q is not an absolute http(s) URL", name, v)
	}
	return v, nil
}
