package config

import (
	"strings"
	"testing"
)

func TestFrontDoorConfig(t *testing.T) {
	cases := []struct {
		name    string
		extra   map[string]string
		wantErr string
	}{
		{"home unset", map[string]string{"SITEBIN_EMBED_ORIGINS": "*"},
			"SITEBIN_FRONT_DOOR needs SITEBIN_HOME_URL set to a site other than this instance"},
		{"home equals base", map[string]string{"SITEBIN_EMBED_ORIGINS": "*", "SITEBIN_HOME_URL": "http://sitebin.example"},
			"SITEBIN_FRONT_DOOR needs SITEBIN_HOME_URL set to a site other than this instance"},
		{"home same origin with path", map[string]string{"SITEBIN_EMBED_ORIGINS": "*", "SITEBIN_HOME_URL": "http://SITEBIN.example/x"},
			"SITEBIN_FRONT_DOOR needs SITEBIN_HOME_URL set to a site other than this instance"},
		// The base host under another scheme or port is still this instance:
		// behind a TLS terminator (SITEBIN_HTTP_ONLY) the browser's root is
		// https://<base>, and GET / would send it to itself.
		{"home is the base host over https", map[string]string{"SITEBIN_EMBED_ORIGINS": "*", "SITEBIN_HOME_URL": "https://sitebin.example"},
			"SITEBIN_FRONT_DOOR needs SITEBIN_HOME_URL set to a site other than this instance"},
		{"home is the base host on :443", map[string]string{"SITEBIN_EMBED_ORIGINS": "*", "SITEBIN_HOME_URL": "https://sitebin.example:443/"},
			"SITEBIN_FRONT_DOOR needs SITEBIN_HOME_URL set to a site other than this instance"},
		{"home is the base host on :80", map[string]string{"SITEBIN_EMBED_ORIGINS": "*", "SITEBIN_HOME_URL": "http://sitebin.example:80"},
			"SITEBIN_FRONT_DOOR needs SITEBIN_HOME_URL set to a site other than this instance"},
		{"home is the base host in another case", map[string]string{"SITEBIN_EMBED_ORIGINS": "*", "SITEBIN_HOME_URL": "https://SiteBin.Example/start"},
			"SITEBIN_FRONT_DOOR needs SITEBIN_HOME_URL set to a site other than this instance"},
		{"home not embedded", map[string]string{"SITEBIN_EMBED_ORIGINS": "https://other.example", "SITEBIN_HOME_URL": "https://sitebin.io"},
			"SITEBIN_FRONT_DOOR: the origin of SITEBIN_HOME_URL (https://sitebin.io) must be in SITEBIN_EMBED_ORIGINS"},
		{"ok", map[string]string{
			"SITEBIN_EMBED_ORIGINS": "https://sitebin.io,https://www.sitebin.io",
			"SITEBIN_HOME_URL":      "https://sitebin.io/landing"}, ""},
		{"ok wildcard", map[string]string{"SITEBIN_EMBED_ORIGINS": "*", "SITEBIN_HOME_URL": "https://sitebin.io"}, ""},
	}
	for _, c := range cases {
		c.extra["SITEBIN_FRONT_DOOR"] = "true"
		cfg, err := Load(sessEnv(c.extra))
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			} else if !cfg.FrontDoor || cfg.HomeOrigin() != "https://sitebin.io" {
				t.Errorf("%s: FrontDoor=%v HomeOrigin=%q", c.name, cfg.FrontDoor, cfg.HomeOrigin())
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
	}
	cfg, err := Load(sessEnv(nil))
	if err != nil || cfg.FrontDoor {
		t.Errorf("default: %v %v", cfg.FrontDoor, err)
	}
}

func TestCheckFrontDoor(t *testing.T) {
	c := Config{FrontDoor: true, HomeURL: "https://Sitebin.io/x", SessionOrigins: []string{"https://www.sitebin.io"}}
	if err := c.CheckFrontDoor(false, false); err == nil || err.Error() != "SITEBIN_FRONT_DOOR needs the enterprise edition (embedding the drop on another site)" {
		t.Errorf("not honoured: %v", err)
	}
	err := c.CheckFrontDoor(true, true)
	want := "SITEBIN_FRONT_DOOR: the origin of SITEBIN_HOME_URL (https://sitebin.io) must be in SITEBIN_SESSION_ORIGINS when accounts are on"
	if err == nil || err.Error() != want {
		t.Errorf("accounts on: %v", err)
	}
	if err := c.CheckFrontDoor(true, false); err != nil {
		t.Errorf("accounts off: %v", err)
	}
	c.SessionOrigins = append(c.SessionOrigins, "https://sitebin.io")
	if err := c.CheckFrontDoor(true, true); err != nil {
		t.Errorf("listed: %v", err)
	}
	c.FrontDoor = false
	if err := c.CheckFrontDoor(false, true); err != nil {
		t.Errorf("off: %v", err)
	}
}
