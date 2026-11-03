package store

import (
	"strings"
	"testing"
)

func basePathSite(t *testing.T, html string) (*Store, *Site) {
	t.Helper()
	s, sites := pathStore(t, 1)
	if html != "" {
		if err := s.SaveFile(sites[0], "index.html", strings.NewReader(html)); err != nil {
			t.Fatal(err)
		}
	}
	return s, sites[0]
}

func TestBasePathWarning(t *testing.T) {
	const addr = "m.at/demos/k1"
	cases := []struct {
		name, html, want string // want: "" for no warning, else a value it must name
	}{
		{"root-built asset", `<link rel="stylesheet" href="/_astro/a.css">`, "/_astro/a.css"},
		{"built for the path", `<link href="/demos/k1/_astro/a.css"><script src="/demos/k1/x.js"></script>`, ""},
		{"the path itself", `<a href="/demos/k1">home</a>`, ""},
		{"a sibling path", `<a href=/demos/k10/x>x</a>`, "/demos/k10/x"},
		{"protocol-relative", `<script src="//cdn.example/y.js"></script>`, ""},
		{"absolute URL", `<a href="https://x.example/a">x</a>`, ""},
		{"relative", `<a href="about/">about</a><img src="img/a.png">`, ""},
		{"srcset", `<img srcset="/demos/k1/a.png 1x, /b.png 2x">`, "/b.png"},
		{"form action", `<form action='/_sitebin/forms/contact'>`, "/_sitebin/forms/contact"},
		{"poster", `<video poster="/p.jpg"></video>`, "/p.jpg"},
		{"sitebin assets are global", `<script src="/_sitebin/altcha.js"></script><link href="/_sitebin/assets/x.css">`, ""},
		{"the drop and its QR library are global", `<script src="/_sitebin/embed.js" defer></script><script src="/_sitebin/qrcode.min.js"></script>`, ""},
	}
	for _, tc := range cases {
		s, site := basePathSite(t, "<!doctype html><html><head>"+tc.html+"</head></html>")
		got := s.BasePathWarning(site, addr)
		switch {
		case tc.want == "" && got != "":
			t.Errorf("%s: warning %q, want none", tc.name, got)
		case tc.want != "" && (!strings.Contains(got, tc.want) || !strings.Contains(got, "/demos/k1")):
			t.Errorf("%s: warning %q, want one naming %s and /demos/k1", tc.name, got, tc.want)
		}
	}
}

func TestBasePathWarningNotApplicable(t *testing.T) {
	s, site := basePathSite(t, `<link href="/_astro/a.css">`)
	if got := s.BasePathWarning(site, "m.at"); got != "" {
		t.Errorf("a plain host: %q, want none", got)
	}
	s2, empty := basePathSite(t, "")
	if got := s2.BasePathWarning(empty, "m.at/a"); got != "" {
		t.Errorf("no index.html: %q, want none", got)
	}
	if err := s.Update(site, func(m *Meta) error { m.Mode = ModeContainer; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := s.BasePathWarning(site, "m.at/a"); got != "" {
		t.Errorf("a container site: %q, want none", got)
	}
	if err := s.Update(site, func(m *Meta) error { m.Mode = ModeViewer; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := s.BasePathWarning(site, "m.at/a"); got != "" {
		t.Errorf("a file viewer (Sitebin's own relative wrapper): %q, want none", got)
	}
}

func TestBasePathWarningIsBounded(t *testing.T) {
	big := "<!doctype html>" + strings.Repeat("<p>filler</p>", 600*1024/13) + `<link href="/_astro/late.css">`
	s, site := basePathSite(t, big)
	if got := s.BasePathWarning(site, "m.at/a"); got != "" {
		t.Errorf("a link past the first 512 KiB was read: %q", got)
	}
}
