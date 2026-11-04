package analytics

import "testing"

func TestParseUA(t *testing.T) {
	cases := []struct {
		name, ua, mobile, platform string
		browser, os, device        string
	}{
		{"chrome windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "", "", "Chrome", "Windows", "desktop"},
		{"edge windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0", "", "", "Edge", "Windows", "desktop"},
		{"firefox windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0", "", "", "Firefox", "Windows", "desktop"},
		{"safari macos", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15", "", "", "Safari", "macOS", "desktop"},
		{"iphone safari", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1", "", "", "Safari", "iOS", "mobile"},
		{"ipad safari", "Mozilla/5.0 (iPad; CPU OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1", "", "", "Safari", "iPadOS", "tablet"},
		{"chrome ios", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/130.0.6723.37 Mobile/15E148 Safari/604.1", "", "", "Chrome (iOS)", "iOS", "mobile"},
		{"firefox ios", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/132.0 Mobile/15E148 Safari/605.1.15", "", "", "Firefox (iOS)", "iOS", "mobile"},
		{"samsung", "Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/26.0 Chrome/122.0.0.0 Mobile Safari/537.36", "", "", "Samsung Internet", "Android", "mobile"},
		{"android chrome", "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36", "", "", "Chrome", "Android", "mobile"},
		{"android tablet", "Mozilla/5.0 (Linux; Android 13; SM-X710) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "", "", "Chrome", "Android", "tablet"},
		{"opera", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 OPR/115.0.0.0", "", "", "Opera", "Windows", "desktop"},
		{"webview", "Mozilla/5.0 (Linux; Android 14; Pixel 8 Build/AP2A; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/130.0.0.0 Mobile Safari/537.36", "", "", "Android WebView", "Android", "mobile"},
		{"chromeos", "Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "", "", "Chrome", "ChromeOS", "desktop"},
		{"linux firefox", "Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0", "", "", "Firefox", "Linux", "desktop"},
		{"yandex", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 YaBrowser/24.10.0.0 Safari/537.36", "", "", "Yandex", "Windows", "desktop"},
		{"unknown", "SomethingElse/1.0", "", "", "Other", "Other", "desktop"},
		{"hint mobile wins", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "?1", "", "Chrome", "Linux", "mobile"},
		{"hint platform wins", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "?0", `"Windows"`, "Chrome", "Windows", "desktop"},
		{"hint chrome os", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "", `"Chrome OS"`, "Chrome", "ChromeOS", "desktop"},
	}
	for _, c := range cases {
		b, o, d := ParseUA(c.ua, c.mobile, c.platform)
		if b != c.browser || o != c.os || d != c.device {
			t.Errorf("%s: got %q/%q/%q, want %q/%q/%q", c.name, b, o, d, c.browser, c.os, c.device)
		}
	}
}

func TestDeviceFromScreen(t *testing.T) {
	cases := []struct{ os, screen, want string }{
		{"iOS", "390x844", "mobile"},
		{"iOS", "820x1180", "tablet"},
		{"Android", "360x800", "mobile"},
		{"Windows", "1366x768", "laptop"},
		{"Windows", "1920x1080", "desktop"},
		{"macOS", "1440x900", "laptop"},
		{"Windows", "", ""},
		{"Windows", "bogus", ""},
	}
	for _, c := range cases {
		if got := DeviceFromScreen(c.os, c.screen); got != c.want {
			t.Errorf("DeviceFromScreen(%q,%q)=%q want %q", c.os, c.screen, got, c.want)
		}
	}
}
