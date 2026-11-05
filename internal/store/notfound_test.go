package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanNotFoundPage(t *testing.T) {
	ok := map[string]string{
		"404.html":             "404.html",
		"/404.html":            "404.html",
		" errors/Missing.HTM ": "errors/Missing.HTM",
		"":                     "",
		"errors/./gone.html":   "errors/gone.html",
	}
	for in, want := range ok {
		got, err := CleanNotFoundPage(in)
		if err != nil || got != want {
			t.Errorf("CleanNotFoundPage(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"../x.html", "//x.html", "x.txt", "_sitebin/x.html", "_raw/x.html",
		".sitebin-trusted", "dir/", "a\\b.html", "x.html/..", "x"} {
		if got, err := CleanNotFoundPage(in); !errors.Is(err, ErrBadNotFoundPage) {
			t.Errorf("CleanNotFoundPage(%q) = %q, %v; want ErrBadNotFoundPage", in, got, err)
		}
	}
}

func TestNotFoundTarget(t *testing.T) {
	cases := []struct {
		name string
		m    Meta
		want string
	}{
		{"web server, the convention", Meta{Mode: ModeWebserver}, "/404.html"},
		{"web server, a named page", Meta{Mode: ModeWebserver, NotFoundPage: "e/m.html"}, "/e/m.html"},
		{"SPA fallback never 404s", Meta{Mode: ModeWebserver, SPAFallback: true}, ""},
		{"viewer", Meta{Mode: ModeViewer, NotFoundPage: "e/m.html"}, ""},
		{"container", Meta{Mode: ModeContainer}, ""},
	}
	for _, c := range cases {
		if got := c.m.NotFoundTarget(); got != c.want {
			t.Errorf("%s: NotFoundTarget() = %q, want %q", c.name, got, c.want)
		}
	}
}

// writeSiteFile puts a file into a site's served folder directly: these
// tests are about what lies there, not about how it arrived.
func writeSiteFile(t *testing.T, site *Site, rel, body string) {
	t.Helper()
	p := filepath.Join(site.FilesDir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNotFoundServes(t *testing.T) {
	s := newTestStore(t)
	site, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if got := NotFoundServes(site); got != "" {
		t.Errorf("no page yet: NotFoundServes = %q, want empty", got)
	}
	writeSiteFile(t, site, "404.html", "own")
	if got := NotFoundServes(site); got != "404.html" {
		t.Errorf("the convention: NotFoundServes = %q, want 404.html", got)
	}
	// A named page that does not exist: Sitebin's page, never the convention.
	site.Meta.NotFoundPage = "e/m.html"
	if got := NotFoundServes(site); got != "" {
		t.Errorf("missing named page: NotFoundServes = %q, want empty", got)
	}
	writeSiteFile(t, site, "e/m.html", "named")
	if got := NotFoundServes(site); got != "e/m.html" {
		t.Errorf("named page: NotFoundServes = %q, want e/m.html", got)
	}
	// A folder is not a page.
	site.Meta.NotFoundPage = "dir.html"
	if err := os.MkdirAll(filepath.Join(site.FilesDir(), "dir.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := NotFoundServes(site); got != "" {
		t.Errorf("folder: NotFoundServes = %q, want empty", got)
	}
	site.Meta.NotFoundPage = ""
	site.Meta.SPAFallback = true
	if got := NotFoundServes(site); got != "" {
		t.Errorf("SPA: NotFoundServes = %q, want empty", got)
	}
}

// A locked site's file names stay out of reach, the not-found page's too.
func TestNotFoundServesNothingOnALockedSite(t *testing.T) {
	s := newTestStore(t)
	site, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	writeSiteFile(t, site, "404.html", "own")
	site.Meta.Locked = &SiteLock{By: "operator"}
	if got := NotFoundServes(site); got != "" {
		t.Errorf("locked: NotFoundServes = %q, want empty", got)
	}
}

func TestMissingStartPage(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		name  string
		files map[string]string
		edit  func(*Meta)
		want  bool
	}{
		{"an empty site", nil, nil, false},
		{"only the trust marker", map[string]string{TrustedMarker: ""}, nil, false},
		{"files without an index", map[string]string{"a.pdf": "%PDF"}, nil, true},
		{"index.html", map[string]string{"a.pdf": "%PDF", "index.html": "<h1>"}, nil, false},
		{"index.txt is an index too", map[string]string{"a.pdf": "%PDF", "index.txt": "hi"}, nil, false},
		{"an index only in a sub-folder", map[string]string{"sub/index.html": "<h1>"}, nil, true},
		{"SPA fallback", map[string]string{"a.pdf": "%PDF"}, func(m *Meta) { m.SPAFallback = true }, false},
		{"viewer", map[string]string{"a.pdf": "%PDF"}, func(m *Meta) { m.Mode = ModeViewer }, false},
		{"locked", map[string]string{"a.pdf": "%PDF"}, func(m *Meta) { m.Locked = &SiteLock{By: "operator"} }, false},
	}
	for _, c := range cases {
		site, _, err := s.Create()
		if err != nil {
			t.Fatal(err)
		}
		for rel, body := range c.files {
			writeSiteFile(t, site, rel, body)
		}
		if c.edit != nil {
			c.edit(&site.Meta)
		}
		if got := MissingStartPage(site); got != c.want {
			t.Errorf("%s: MissingStartPage = %v, want %v", c.name, got, c.want)
		}
	}
}
