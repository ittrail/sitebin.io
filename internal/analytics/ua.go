package analytics

import (
	"strconv"
	"strings"
)

// ParseUA names the browser, operating system and device class of a request.
// chMobile and chPlatform are the low-entropy client hints
// (Sec-CH-UA-Mobile, Sec-CH-UA-Platform); when present they win over the
// user agent. The device is desktop, tablet or mobile.
func ParseUA(ua, chMobile, chPlatform string) (browser, os, device string) {
	browser = uaBrowser(ua)
	os = uaOS(ua)
	if p := platformFromHint(chPlatform); p != "" {
		os = p
	}
	device = uaDevice(ua, os)
	if strings.TrimSpace(chMobile) == "?1" {
		device = "mobile"
	}
	return browser, os, device
}

func uaBrowser(ua string) string {
	has := func(s string) bool { return strings.Contains(ua, s) }
	switch {
	case has("SamsungBrowser/"):
		return "Samsung Internet"
	case has("Edg/") || has("EdgA/") || has("EdgiOS/") || has("Edge/"):
		return "Edge"
	case has("OPR/") || has("OPT/") || has("Opera"):
		return "Opera"
	case has("YaBrowser/"):
		return "Yandex"
	case has("CriOS/"):
		return "Chrome (iOS)"
	case has("FxiOS/"):
		return "Firefox (iOS)"
	case has("Firefox/"):
		return "Firefox"
	case has("Android") && has("; wv)"):
		return "Android WebView"
	case has("Chrome/") || has("Chromium/"):
		return "Chrome"
	case has("Safari/") && has("Version/"):
		return "Safari"
	}
	return "Other"
}

func uaOS(ua string) string {
	has := func(s string) bool { return strings.Contains(ua, s) }
	switch {
	case has("CrOS"):
		return "ChromeOS"
	case has("iPhone") || has("iPod"):
		return "iOS"
	case has("iPad"):
		return "iPadOS"
	case has("Android"):
		return "Android"
	case has("Windows"):
		return "Windows"
	case has("Macintosh") || has("Mac OS X"):
		return "macOS"
	case has("Linux") || has("X11"):
		return "Linux"
	}
	return "Other"
}

func platformFromHint(p string) string {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(p), `"`)) {
	case "windows":
		return "Windows"
	case "macos":
		return "macOS"
	case "ios":
		return "iOS"
	case "android":
		return "Android"
	case "linux":
		return "Linux"
	case "chrome os", "chromeos":
		return "ChromeOS"
	}
	return ""
}

func uaDevice(ua, os string) string {
	has := func(s string) bool { return strings.Contains(ua, s) }
	switch {
	case os == "iPadOS" || has("iPad") || has("Tablet"):
		return "tablet"
	case os == "iOS" || has("iPhone") || has("iPod"):
		return "mobile"
	case os == "Android":
		if has("Mobile") {
			return "mobile"
		}
		return "tablet"
	case has("Mobile"):
		return "mobile"
	}
	return "desktop"
}

// DeviceFromScreen is the script-mode device rule (as Umami): a mobile OS
// gives mobile (width < 768) or tablet, any other OS laptop (width <= 1440)
// or desktop. It returns "" when the screen is missing or malformed.
func DeviceFromScreen(os, screen string) string {
	w, _, ok := strings.Cut(screen, "x")
	if !ok {
		return ""
	}
	width, err := strconv.Atoi(w)
	if err != nil || width <= 0 {
		return ""
	}
	switch os {
	case "iOS", "iPadOS", "Android":
		if width < 768 {
			return "mobile"
		}
		return "tablet"
	}
	if width <= 1440 {
		return "laptop"
	}
	return "desktop"
}
