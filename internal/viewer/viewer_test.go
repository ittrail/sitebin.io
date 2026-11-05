package viewer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/store"
)

func TestRendererFor(t *testing.T) {
	cases := map[string]string{
		"doc.pdf": "pdf", "DOC.PDF": "pdf",
		"readme.md": "markdown", "notes.markdown": "markdown",
		"report.docx": "docx",
		"data.csv":    "table", "sheet.TSV": "table",
		"analysis.ipynb": "notebook",
		"logo.png":       "image", "photo.JPEG": "image", "art.svg": "image", "a.webp": "image",
		"clip.mp4": "video", "clip.webm": "video",
		"song.mp3": "audio", "voice.wav": "audio",
		"main.go": "text", "data.json": "text", "page.html": "text", "no-extension": "text",
	}
	for name, want := range cases {
		if got := RendererFor(name); got != want {
			t.Errorf("RendererFor(%q) = %q, want %q", name, got, want)
		}
	}
}

func makeSite(t *testing.T) (*store.Store, *store.Site) {
	t.Helper()
	s, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	site, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFile(site, "report.pdf", strings.NewReader("%PDF-fake")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFile(site, "notes.md", strings.NewReader("# hi")); err != nil {
		t.Fatal(err)
	}
	return s, site
}

func TestApplyAndRemove(t *testing.T) {
	s, site := makeSite(t)
	if err := s.Update(site, func(m *store.Meta) error {
		m.Mode = store.ModeViewer
		m.EntryFile = "report.pdf"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	entry, err := Apply(site)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if entry != "report.pdf" {
		t.Errorf("entry = %q", entry)
	}
	// originals moved into files/_raw
	if _, err := os.Stat(filepath.Join(site.FilesDir(), "_raw", "report.pdf")); err != nil {
		t.Errorf("raw file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(site.FilesDir(), "_raw", "notes.md")); err != nil {
		t.Errorf("second raw file missing: %v", err)
	}
	// wrapper written with config
	b, err := os.ReadFile(filepath.Join(site.FilesDir(), "index.html"))
	if err != nil {
		t.Fatalf("wrapper missing: %v", err)
	}
	html := string(b)
	if !strings.Contains(html, wrapperMarker) {
		t.Error("wrapper missing marker")
	}
	if !strings.Contains(html, `"report.pdf"`) || !strings.Contains(html, `"pdf"`) {
		t.Errorf("wrapper config incomplete: %s", html)
	}
	if !strings.Contains(html, "/_sitebin/assets/") {
		t.Error("wrapper does not reference shared assets")
	}

	// idempotent: apply again, wrapper must not get swallowed into _raw
	if _, err := Apply(site); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(site.FilesDir(), "_raw", "index.html")); !os.IsNotExist(err) {
		t.Error("wrapper leaked into _raw on re-apply")
	}

	// switch back restores the original tree
	if err := s.Update(site, func(m *store.Meta) error { m.Mode = store.ModeWebserver; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := Remove(site); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(site.FilesDir(), "report.pdf")); err != nil {
		t.Errorf("file not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(site.FilesDir(), "_raw")); !os.IsNotExist(err) {
		t.Error("_raw dir left behind")
	}
	if b, err := os.ReadFile(filepath.Join(site.FilesDir(), "notes.md")); err != nil || string(b) != "# hi" {
		t.Errorf("restored content wrong: %q %v", b, err)
	}
	files, _ := s.ListFiles(site)
	if len(files) != 2 {
		t.Errorf("restored files = %+v", files)
	}
}

func TestApplyPicksEntryWhenUnset(t *testing.T) {
	s, site := makeSite(t)
	if err := s.Update(site, func(m *store.Meta) error {
		m.Mode = store.ModeViewer
		m.EntryFile = "index.html" // creation default, doesn't exist here
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	entry, err := Apply(site)
	if err != nil {
		t.Fatal(err)
	}
	if entry != "notes.md" && entry != "report.pdf" {
		t.Errorf("fallback entry = %q", entry)
	}
}

// A file the viewer cannot show is offered as a download, never fetched and
// shown as text: a 20 MB installer must not arrive as 2 MB of noise.
func TestRendererForBinaries(t *testing.T) {
	cases := map[string]string{
		"setup.EXE": "download", "a.tar.gz": "download", "font.woff2": "download",
		"book.xlsx": "download", "disk.iso": "download",
		"main.go": "text", "notes.txt": "text", "a.pdf": "pdf",
	}
	for name, want := range cases {
		if got := RendererFor(name); got != want {
			t.Errorf("RendererFor(%q) = %q, want %q", name, got, want)
		}
	}
}

// The browser decides with its own copy of the table (web/viewer/viewer.js);
// the two must never drift apart.
func TestViewerJSMirrorsRenderers(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "viewer", "viewer.js"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	start := strings.Index(s, "const rendererByExt = {")
	end := strings.Index(s[start:], "};")
	if start < 0 || end < 0 {
		t.Fatal("no rendererByExt table in viewer.js")
	}
	js := map[string]string{}
	// A key may be quoted ("7z": a bare 7z is not a JavaScript name).
	for _, m := range regexp.MustCompile(`"?([a-z0-9]+)"?:\s*"([a-z]+)"`).FindAllStringSubmatch(s[start:start+end], -1) {
		js["."+m[1]] = m[2]
	}
	for ext, kind := range rendererByExt {
		if js[ext] != kind {
			t.Errorf("viewer.js: %s = %q, Go says %q", ext, js[ext], kind)
		}
	}
	for ext, kind := range js {
		if rendererByExt[ext] != kind {
			t.Errorf("viewer.js has %s = %q, Go says %q", ext, kind, rendererByExt[ext])
		}
	}
}

// The drop form suggests the File viewer when every file is a document or a
// download (no page to serve). Its DOWNLOAD_EXTS list must name exactly the
// extensions the viewer offers as a download, or a lone installer lands in
// web server mode and its front page is a 404.
func TestDropFormSuggestsViewerForEveryDownload(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "embed.js"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`const DOWNLOAD_EXTS = /\\.\(([a-z0-9|]+)\)\$/i;`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no DOWNLOAD_EXTS list in embed.js")
	}
	js := map[string]bool{}
	for _, ext := range strings.Split(string(m[1]), "|") {
		js["."+ext] = true
	}
	for ext, kind := range rendererByExt {
		if kind == "download" && !js[ext] {
			t.Errorf("embed.js DOWNLOAD_EXTS lacks %s, which the viewer offers as a download", ext)
		}
	}
	for ext := range js {
		if rendererByExt[ext] != "download" {
			t.Errorf("embed.js DOWNLOAD_EXTS has %s, which the viewer does not offer as a download (%q)", ext, rendererByExt[ext])
		}
	}
}
