package viewer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ittrail/sitebin/internal/store"
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
