// Package config loads Sitebin configuration from environment variables.
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration. See README for the full reference.
type Config struct {
	BaseDomain string // main domain; view subdomains are <id>.BaseDomain
	PublicPort int    // non-standard public port for URL construction (0 = default)
	DataDir    string

	ViewAccess string // how view URLs are served: subdomain (default) | path | both

	DNSProvider string // caddy dns module for the wildcard cert (cloudflare|hetzner|duckdns)
	DNSToken    string
	TLSSnippet  string // raw lines injected into the wildcard tls block (advanced providers)
	ACMEEmail   string
	HTTPOnly    bool // disable TLS entirely (local/testing or behind an external proxy)

	MaxSiteBytes  int64
	MaxFiles      int
	MaxExpiryDays int // 0 = unlimited
	WebDAVAllowed bool
	TrackViews    bool
	ReadOnly      bool

	PublicAddr   string // backend listener proxied by Caddy
	InternalAddr string // authz/tls-check/health listener (never proxied)
	BackendHost  string // host Caddy uses to reach the backend

	RateCreatePerHour int
	RateCreateBurst   int
	RateAuthPer5Min   int
	CleanupInterval   time.Duration

	// FTP (optional, off by default). Serves a site's files over FTP; login is
	// the edit UUID + edit password. Plaintext unless FTPS certs are set.
	FTPEnabled    bool
	FTPAddr       string // control-connection listener (default ":21")
	FTPPasvMin    int    // passive data-port range start
	FTPPasvMax    int    // passive data-port range end
	FTPPublicHost string // host advertised for passive mode (default: BaseDomain)
	FTPTLSCert    string // optional PEM cert for explicit FTPS (AUTH TLS)
	FTPTLSKey     string // optional PEM key
}

// View-access modes for serving site content.
const (
	ViewSubdomain = "subdomain" // <view-id>.base (default; needs a wildcard cert)
	ViewPath      = "path"      // base/v/<view-id> (no wildcard needed)
	ViewBoth      = "both"      // served on both
)

// SubdomainViews reports whether sites are served on random subdomains.
func (c Config) SubdomainViews() bool {
	return c.ViewAccess == ViewSubdomain || c.ViewAccess == ViewBoth
}

// PathViews reports whether sites are served under /v/<id> on the main domain.
func (c Config) PathViews() bool { return c.ViewAccess == ViewPath || c.ViewAccess == ViewBoth }

// singleTokenProviders are DNS modules configurable via SITEBIN_DNS_TOKEN alone.
var singleTokenProviders = map[string]bool{
	"cloudflare": true,
	"hetzner":    true,
	"duckdns":    true,
}

// Load reads configuration using getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		DataDir:           "/data",
		ViewAccess:        ViewSubdomain,
		MaxSiteBytes:      104857600,
		MaxFiles:          1000,
		WebDAVAllowed:     true,
		PublicAddr:        ":8080",
		InternalAddr:      ":9000",
		BackendHost:       "127.0.0.1",
		RateCreatePerHour: 30,
		RateCreateBurst:   10,
		RateAuthPer5Min:   10,
		CleanupInterval:   10 * time.Minute,
		FTPAddr:           ":21",
		FTPPasvMin:        21000,
		FTPPasvMax:        21010,
	}

	base := strings.ToLower(strings.TrimSpace(getenv("SITEBIN_BASE_DOMAIN")))
	base = strings.TrimSuffix(base, ".")
	if host, port, ok := strings.Cut(base, ":"); ok {
		p, err := strconv.Atoi(port)
		if err != nil {
			return cfg, fmt.Errorf("SITEBIN_BASE_DOMAIN: invalid port %q", port)
		}
		base, cfg.PublicPort = host, p
	}
	if base == "" {
		return cfg, fmt.Errorf("SITEBIN_BASE_DOMAIN is required (e.g. sitebin.example.com)")
	}
	cfg.BaseDomain = base

	if v := getenv("SITEBIN_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	cfg.DNSProvider = strings.ToLower(getenv("SITEBIN_DNS_PROVIDER"))
	cfg.DNSToken = getenv("SITEBIN_DNS_TOKEN")
	cfg.TLSSnippet = getenv("SITEBIN_TLS_SNIPPET")
	cfg.ACMEEmail = getenv("SITEBIN_ACME_EMAIL")
	if v := getenv("SITEBIN_BACKEND_HOST"); v != "" {
		cfg.BackendHost = v
	}
	if v := getenv("SITEBIN_PUBLIC_ADDR"); v != "" {
		cfg.PublicAddr = v
	}
	if v := getenv("SITEBIN_INTERNAL_ADDR"); v != "" {
		cfg.InternalAddr = v
	}
	if v := strings.ToLower(strings.TrimSpace(getenv("SITEBIN_VIEW_ACCESS"))); v != "" {
		switch v {
		case ViewSubdomain, ViewPath, ViewBoth:
			cfg.ViewAccess = v
		default:
			return cfg, fmt.Errorf("SITEBIN_VIEW_ACCESS %q is invalid (want subdomain|path|both)", v)
		}
	}

	var err error
	if cfg.HTTPOnly, err = boolVar(getenv, "SITEBIN_HTTP_ONLY", false); err != nil {
		return cfg, err
	}
	if cfg.WebDAVAllowed, err = boolVar(getenv, "SITEBIN_WEBDAV_ENABLED", true); err != nil {
		return cfg, err
	}
	if cfg.TrackViews, err = boolVar(getenv, "SITEBIN_TRACK_VIEWS", true); err != nil {
		return cfg, err
	}
	if cfg.ReadOnly, err = boolVar(getenv, "SITEBIN_READONLY", false); err != nil {
		return cfg, err
	}
	if cfg.MaxSiteBytes, err = int64Var(getenv, "SITEBIN_MAX_SITE_BYTES", cfg.MaxSiteBytes); err != nil {
		return cfg, err
	}
	if cfg.MaxFiles, err = intVar(getenv, "SITEBIN_MAX_FILES", cfg.MaxFiles); err != nil {
		return cfg, err
	}
	if cfg.MaxExpiryDays, err = intVar(getenv, "SITEBIN_MAX_EXPIRY_DAYS", 0); err != nil {
		return cfg, err
	}
	if cfg.RateCreatePerHour, err = intVar(getenv, "SITEBIN_RATE_CREATE_PER_HOUR", cfg.RateCreatePerHour); err != nil {
		return cfg, err
	}
	if cfg.RateCreateBurst, err = intVar(getenv, "SITEBIN_RATE_CREATE_BURST", cfg.RateCreateBurst); err != nil {
		return cfg, err
	}
	if cfg.RateAuthPer5Min, err = intVar(getenv, "SITEBIN_RATE_AUTH_PER_5MIN", cfg.RateAuthPer5Min); err != nil {
		return cfg, err
	}
	if v := getenv("SITEBIN_CLEANUP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("SITEBIN_CLEANUP_INTERVAL: %v", err)
		}
		cfg.CleanupInterval = d
	}

	if cfg.FTPEnabled, err = boolVar(getenv, "SITEBIN_FTP_ENABLED", false); err != nil {
		return cfg, err
	}
	if v := strings.TrimSpace(getenv("SITEBIN_FTP_ADDR")); v != "" {
		cfg.FTPAddr = v
	}
	if cfg.FTPPasvMin, err = intVar(getenv, "SITEBIN_FTP_PASV_PORT_MIN", cfg.FTPPasvMin); err != nil {
		return cfg, err
	}
	if cfg.FTPPasvMax, err = intVar(getenv, "SITEBIN_FTP_PASV_PORT_MAX", cfg.FTPPasvMax); err != nil {
		return cfg, err
	}
	cfg.FTPPublicHost = strings.TrimSpace(getenv("SITEBIN_FTP_PUBLIC_HOST"))
	if cfg.FTPPublicHost == "" {
		cfg.FTPPublicHost = cfg.BaseDomain
	}
	cfg.FTPTLSCert = strings.TrimSpace(getenv("SITEBIN_FTP_TLS_CERT"))
	cfg.FTPTLSKey = strings.TrimSpace(getenv("SITEBIN_FTP_TLS_KEY"))
	if cfg.FTPEnabled && cfg.FTPPasvMax < cfg.FTPPasvMin {
		return cfg, fmt.Errorf("SITEBIN_FTP_PASV_PORT_MAX (%d) must be >= MIN (%d)", cfg.FTPPasvMax, cfg.FTPPasvMin)
	}

	// The wildcard cert (and thus a DNS challenge) is only needed when sites are
	// served on random subdomains. Path-only mode works with a normal cert for
	// the single main domain.
	if !cfg.HTTPOnly && cfg.SubdomainViews() {
		switch {
		case cfg.TLSSnippet != "":
			// advanced escape hatch, accepted as-is
		case cfg.DNSProvider == "":
			return cfg, fmt.Errorf("the *.%s wildcard certificate needs a DNS challenge: set SITEBIN_DNS_PROVIDER + SITEBIN_DNS_TOKEN (or SITEBIN_TLS_SNIPPET, or SITEBIN_VIEW_ACCESS=path to serve sites as paths without a wildcard, or SITEBIN_HTTP_ONLY=true behind your own proxy)", cfg.BaseDomain)
		case !singleTokenProviders[cfg.DNSProvider]:
			return cfg, fmt.Errorf("SITEBIN_DNS_PROVIDER %q is not a built-in single-token provider (cloudflare, hetzner, duckdns); use SITEBIN_TLS_SNIPPET for other providers", cfg.DNSProvider)
		case cfg.DNSToken == "":
			return cfg, fmt.Errorf("SITEBIN_DNS_TOKEN is required with SITEBIN_DNS_PROVIDER=%s", cfg.DNSProvider)
		}
	}
	return cfg, nil
}

func boolVar(getenv func(string) string, name string, def bool) (bool, error) {
	v := strings.ToLower(strings.TrimSpace(getenv(name)))
	switch v {
	case "":
		return def, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return def, fmt.Errorf("%s: cannot parse %q as bool", name, v)
}

func intVar(getenv func(string) string, name string, def int) (int, error) {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, fmt.Errorf("%s: cannot parse %q as integer", name, v)
	}
	return n, nil
}

func int64Var(getenv func(string) string, name string, def int64) (int64, error) {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def, fmt.Errorf("%s: cannot parse %q as integer", name, v)
	}
	return n, nil
}

// SiteURL returns the public URL for a view id or custom domain host.
func (c Config) SiteURL(host string) string {
	return c.scheme() + "://" + host + c.portSuffix()
}

// ViewURL returns the primary public URL for a site's view id. Subdomain form
// is preferred when enabled; otherwise the path form is used.
func (c Config) ViewURL(viewID string) string {
	if c.SubdomainViews() {
		return c.SiteURL(viewID + "." + c.BaseDomain)
	}
	return c.PathViewURL(viewID)
}

// PathViewURL returns the /v/<id> path URL on the main domain.
func (c Config) PathViewURL(viewID string) string {
	return c.scheme() + "://" + c.BaseDomain + c.portSuffix() + "/v/" + viewID + "/"
}

// EditURL returns the public edit-page URL for an edit id.
func (c Config) EditURL(editID string) string {
	return c.scheme() + "://" + c.BaseDomain + c.portSuffix() + "/e/" + editID
}

// DAVURL returns the WebDAV mount URL for an edit id.
func (c Config) DAVURL(editID string) string {
	return c.scheme() + "://" + c.BaseDomain + c.portSuffix() + "/dav/" + editID + "/"
}

// FTPPort returns the numeric FTP control port parsed from FTPAddr.
func (c Config) FTPPort() int {
	_, port, err := net.SplitHostPort(c.FTPAddr)
	if err != nil {
		return 21
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return 21
	}
	return p
}

// FTPURL returns the FTP connection URL for an edit id (login = edit id).
func (c Config) FTPURL(editID string) string {
	scheme := "ftp"
	if c.FTPTLSCert != "" {
		scheme = "ftps"
	}
	host := c.FTPPublicHost
	if host == "" {
		host = c.BaseDomain
	}
	port := ""
	if p := c.FTPPort(); p != 21 {
		port = ":" + strconv.Itoa(p)
	}
	return scheme + "://" + editID + "@" + host + port + "/"
}

func (c Config) scheme() string {
	if c.HTTPOnly {
		return "http"
	}
	return "https"
}

func (c Config) portSuffix() string {
	if c.PublicPort == 0 {
		return ""
	}
	if c.HTTPOnly && c.PublicPort == 80 || !c.HTTPOnly && c.PublicPort == 443 {
		return ""
	}
	return ":" + strconv.Itoa(c.PublicPort)
}
