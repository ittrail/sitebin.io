package config

import (
	"fmt"
	"net/url"
	"strings"
)

// embedsOrigin reports whether SITEBIN_EMBED_ORIGINS allows o (a lowercased
// origin): listed there, or the list is "*".
func (c Config) embedsOrigin(o string) bool {
	for _, e := range c.EmbedOrigins {
		if e == "*" || e == o {
			return true
		}
	}
	return false
}

// originOf returns the lowercased scheme://host[:port] of a URL, "" if it
// has none.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// hostnameOf returns the lowercased host name of a URL, without its port,
// "" if it has none.
func hostnameOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// HomeOrigin is the scheme://host[:port] of HomeURL, lowercased.
func (c Config) HomeOrigin() string { return originOf(c.HomeURL) }

// checkFrontDoorStatic holds the rules that need nothing but the environment;
// Load runs it once HomeURL is known.
func (c Config) checkFrontDoorStatic() error {
	if !c.FrontDoor {
		return nil
	}
	home := c.HomeOrigin()
	// The home URL must be on another HOST NAME than the instance, whatever
	// its scheme and port: behind a TLS terminator (SITEBIN_HTTP_ONLY) the
	// base URL reads http://<base> while browsers reach https://<base>, and a
	// home URL there would send GET / to itself, as would <base>:443 (the
	// reasoning of originIsOwnHost in httpapi, which ignores the scheme).
	if home == "" || hostnameOf(c.HomeURL) == hostnameOf(c.SiteURL(c.BaseDomain)) {
		return fmt.Errorf("SITEBIN_FRONT_DOOR needs SITEBIN_HOME_URL set to a site other than this instance")
	}
	if !c.embedsOrigin(home) {
		return fmt.Errorf("SITEBIN_FRONT_DOOR: the origin of SITEBIN_HOME_URL (%s) must be in SITEBIN_EMBED_ORIGINS", home)
	}
	return nil
}

// CheckFrontDoor holds the rules that need the running edition: the front
// door embeds the drop on another site (embedHonoured), and with accounts on
// that site must also be allowed to use the session.
func (c Config) CheckFrontDoor(embedHonoured, accountsOn bool) error {
	if !c.FrontDoor {
		return nil
	}
	if !embedHonoured {
		return fmt.Errorf("SITEBIN_FRONT_DOOR needs the enterprise edition (embedding the drop on another site)")
	}
	if accountsOn {
		home := c.HomeOrigin()
		for _, o := range c.SessionOrigins {
			if o == home {
				return nil
			}
		}
		return fmt.Errorf("SITEBIN_FRONT_DOOR: the origin of SITEBIN_HOME_URL (%s) must be in SITEBIN_SESSION_ORIGINS when accounts are on", home)
	}
	return nil
}
