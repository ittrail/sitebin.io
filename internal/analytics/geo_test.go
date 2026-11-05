package analytics

import "testing"

func TestGeoLookupTestDB(t *testing.T) {
	g, err := OpenGeo("testdata/geo-test.mmdb")
	if err != nil || g == nil {
		t.Fatalf("open: %v", err)
	}
	cases := []struct{ ip, c, rg, ct string }{
		{"216.160.83.56", "US", "US-WA", "Milton"},
		{"81.2.69.142", "GB", "GB-ENG", "London"},
		{"::ffff:216.160.83.56", "US", "US-WA", "Milton"},
	}
	for _, tc := range cases {
		c, rg, ct := g.Lookup(tc.ip)
		if c != tc.c || rg != tc.rg || ct != tc.ct {
			t.Errorf("%s = %q %q %q", tc.ip, c, rg, ct)
		}
	}
	if c, rg, ct := g.Lookup("10.0.0.1"); c != "" || rg != "" || ct != "" {
		t.Errorf("private ip = %q %q %q", c, rg, ct)
	}
	if c, _, _ := g.Lookup("not an ip"); c != "" {
		t.Errorf("bad ip = %q", c)
	}
	// MaxMind's test database is no DB-IP one: its own description, no link.
	if text, link := g.Attribution(); text == "" || link != "" {
		t.Errorf("attribution = %q %q", text, link)
	}
}

// DB-IP Lite's licence (CC BY 4.0) asks for an attribution with a link, and
// its metadata only says "DB-IP.com - IP to City": a DB-IP database gets the
// fixed attribution and DB-IP's address; any other its own description.
func TestGeoAttributionFor(t *testing.T) {
	for _, c := range []struct {
		typ        string
		desc       map[string]string
		text, link string
	}{
		{"DBIP-City-Lite", map[string]string{"en": "DB-IP.com - IP to City", "fr": "DB-IP.com - IP vers Ville"}, "IP geolocation by DB-IP", "https://db-ip.com"},
		{"DBIP-Country-Lite", map[string]string{"en": "DB-IP.com - IP to Country"}, "IP geolocation by DB-IP", "https://db-ip.com"},
		{"dbip-location", nil, "IP geolocation by DB-IP", "https://db-ip.com"},
		{"Custom", map[string]string{"en": "Built from DB-IP data"}, "IP geolocation by DB-IP", "https://db-ip.com"},
		{"GeoIP2-City", map[string]string{"en": "GeoIP2 City database"}, "GeoIP2 City database", ""},
		{"GeoLite2-Country", map[string]string{"": "no language"}, "no language", ""},
		{"Other", nil, "", ""},
	} {
		if text, link := attributionFor(c.typ, c.desc); text != c.text || link != c.link {
			t.Errorf("%s %v: %q %q, want %q %q", c.typ, c.desc, text, link, c.text, c.link)
		}
	}
}

func TestGeoNilSafe(t *testing.T) {
	g, err := OpenGeo("")
	if g != nil || err != nil {
		t.Fatalf("OpenGeo empty = %v, %v", g, err)
	}
	if c, rg, ct := g.Lookup("216.160.83.56"); c != "" || rg != "" || ct != "" {
		t.Fatal("nil Geo must find nothing")
	}
	if text, link := g.Attribution(); text != "" || link != "" {
		t.Fatal("nil Geo attribution")
	}
}

func TestOpenGeoMissingFile(t *testing.T) {
	if _, err := OpenGeo("testdata/nope.mmdb"); err == nil {
		t.Fatal("expected error")
	}
}
