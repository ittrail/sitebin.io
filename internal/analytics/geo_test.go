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
	if g.Attribution() == "" {
		t.Error("attribution empty")
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
	if g.Attribution() != "" {
		t.Fatal("nil Geo attribution")
	}
}

func TestOpenGeoMissingFile(t *testing.T) {
	if _, err := OpenGeo("testdata/nope.mmdb"); err == nil {
		t.Fatal("expected error")
	}
}
