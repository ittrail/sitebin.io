package analytics

import (
	"net/http"
	"strings"
)

// botMarkers are lower-case substrings of crawler, link-preview, monitor,
// headless-browser and HTTP-library user agents.
var botMarkers = []string{
	"bot", "crawl", "spider", "slurp", "facebookexternalhit", "whatsapp",
	"headlesschrome", "phantomjs", "lighthouse", "pagespeed", "curl", "wget",
	"python-", "go-http-client", "java/", "okhttp", "libwww", "httpclient",
	"axios", "node-fetch", "uptime", "monitor", "pingdom", "preview",
	"embedly", "mediapartners", "yandex", "baidu",
}

// IsBot reports whether the user agent belongs to a known non-browser.
func IsBot(ua string) bool {
	l := strings.ToLower(ua)
	for _, m := range botMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// IsNavigation reports whether the request looks like a browser page
// navigation: GET, Accept text/html, no prefetch/prerender purpose, and
// Sec-Fetch-Dest document / Sec-Fetch-Mode navigate when those are sent.
func IsNavigation(h http.Header, method string) bool {
	if method != http.MethodGet {
		return false
	}
	if !strings.Contains(strings.ToLower(h.Get("Accept")), "text/html") {
		return false
	}
	for _, name := range []string{"Sec-Purpose", "Purpose"} {
		p := strings.ToLower(h.Get(name))
		if strings.Contains(p, "prefetch") || strings.Contains(p, "prerender") {
			return false
		}
	}
	if d := h.Get("Sec-Fetch-Dest"); d != "" && !strings.EqualFold(d, "document") {
		return false
	}
	if m := h.Get("Sec-Fetch-Mode"); m != "" && !strings.EqualFold(m, "navigate") {
		return false
	}
	return true
}
