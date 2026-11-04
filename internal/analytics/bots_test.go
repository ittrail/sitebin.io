package analytics

import (
	"net/http"
	"testing"
)

func TestIsBot(t *testing.T) {
	bots := []string{
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/126.0.0.0 Safari/537.36",
		"curl/8.5.0", "python-requests/2.31", "Go-http-client/2.0",
		"Mozilla/5.0 (compatible; UptimeRobot/2.0; http://www.uptimerobot.com/)",
	}
	for _, ua := range bots {
		if !IsBot(ua) {
			t.Errorf("IsBot(%q) = false", ua)
		}
	}
	humans := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1",
		"Mozilla/5.0 (Android 14; Mobile; rv:132.0) Gecko/132.0 Firefox/132.0",
	}
	for _, ua := range humans {
		if IsBot(ua) {
			t.Errorf("IsBot(%q) = true", ua)
		}
	}
}

func TestIsNavigation(t *testing.T) {
	h := func(kv ...string) http.Header {
		hd := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			hd.Set(kv[i], kv[i+1])
		}
		return hd
	}
	cases := []struct {
		name   string
		h      http.Header
		method string
		want   bool
	}{
		{"navigation", h("Accept", "text/html,application/xhtml+xml", "Sec-Fetch-Dest", "document", "Sec-Fetch-Mode", "navigate"), "GET", true},
		{"prefetch", h("Accept", "text/html", "Sec-Purpose", "prefetch;prerender"), "GET", false},
		{"purpose", h("Accept", "text/html", "Purpose", "prefetch"), "GET", false},
		{"image", h("Accept", "text/html", "Sec-Fetch-Dest", "image"), "GET", false},
		{"cors mode", h("Accept", "text/html", "Sec-Fetch-Mode", "cors"), "GET", false},
		{"post", h("Accept", "text/html"), "POST", false},
		{"any accept", h("Accept", "*/*"), "GET", false},
		{"old safari", h("Accept", "text/html"), "GET", true},
	}
	for _, c := range cases {
		if got := IsNavigation(c.h, c.method); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}
