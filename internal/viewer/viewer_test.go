package viewer

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/store"
	"github.com/ittrail/sitebin.io/web"
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

// A start page that names no file (the creation default index.html, or a
// deleted file) opens on the file list when the site holds several files.
func TestApplyListsSeveralFilesWithoutEntry(t *testing.T) {
	s, site := makeSite(t)
	setViewer(t, s, site, "index.html") // creation default, doesn't exist here
	entry, err := Apply(site)
	if err != nil {
		t.Fatal(err)
	}
	if entry != "" {
		t.Errorf("fallback entry = %q, want the list (\"\")", entry)
	}
	if cfg := wrapperCfg(t, site); cfg.Entry != "" {
		t.Errorf("wrapper entry = %q, want \"\"", cfg.Entry)
	}
}

// A lone file opens at once: a list of one would only add a click.
func TestApplyOpensTheOnlyFile(t *testing.T) {
	s, site := makeSite(t)
	if err := s.DeleteFile(site, "notes.md"); err != nil {
		t.Fatal(err)
	}
	setViewer(t, s, site, "index.html")
	entry, err := Apply(site)
	if err != nil {
		t.Fatal(err)
	}
	if entry != "report.pdf" {
		t.Errorf("entry = %q, want the only file", entry)
	}
}

// "" is the owner's choice of the list, never replaced by a fallback.
func TestApplyKeepsAnExplicitList(t *testing.T) {
	s, site := makeSite(t)
	if err := s.DeleteFile(site, "notes.md"); err != nil {
		t.Fatal(err)
	}
	setViewer(t, s, site, "")
	entry, err := Apply(site)
	if err != nil {
		t.Fatal(err)
	}
	if entry != "" {
		t.Errorf("entry = %q, want the list kept", entry)
	}
}

// The list shows when each file changed, and never names dotfiles or
// operating-system clutter: they are left out of the page itself.
func TestWrapperCarriesMtimeAndLeavesHiddenOut(t *testing.T) {
	s, site := makeSite(t)
	for _, p := range []string{".env", "a/.git/config", "__MACOSX/y.pdf", "pics/Thumbs.db", "Desktop.ini", "docs/.hidden/z.md", "docs/visible.txt"} {
		writeRaw(t, site, p, "x")
	}
	setViewer(t, s, site, "")
	if _, err := Apply(site); err != nil {
		t.Fatal(err)
	}
	cfg := wrapperCfg(t, site)
	got := map[string]wrapperFile{}
	for _, f := range cfg.Files {
		got[f.Path] = f
	}
	for _, want := range []string{"report.pdf", "notes.md", "docs/visible.txt"} {
		f, ok := got[want]
		if !ok {
			t.Errorf("%s missing from the list", want)
			continue
		}
		if f.Mtime <= 0 {
			t.Errorf("%s has no mtime", want)
		}
	}
	if len(got) != 3 {
		t.Errorf("listed %v, want only the three visible files", cfg.Files)
	}
	html := wrapperHTML(t, site)
	for _, hidden := range []string{".env", ".git", "__MACOSX", "Thumbs.db", "Desktop.ini", ".hidden"} {
		if strings.Contains(html, hidden) {
			t.Errorf("wrapper names hidden %q", hidden)
		}
	}
}

// An entry the owner chose explicitly is honoured even when the list hides it.
func TestHiddenEntryIsHonoured(t *testing.T) {
	s, site := makeSite(t)
	writeRaw(t, site, ".notes.md", "# dot")
	setViewer(t, s, site, ".notes.md")
	entry, err := Apply(site)
	if err != nil {
		t.Fatal(err)
	}
	if entry != ".notes.md" {
		t.Errorf("entry = %q, want the chosen hidden file", entry)
	}
}

func setViewer(t *testing.T, s *store.Store, site *store.Site, entry string) {
	t.Helper()
	if err := s.Update(site, func(m *store.Meta) error {
		m.Mode = store.ModeViewer
		m.EntryFile = entry
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// writeRaw puts a file straight into the content folder, as an upload of any
// name would, so the test does not depend on what the store's own checks allow.
func writeRaw(t *testing.T, site *store.Site, rel, body string) {
	t.Helper()
	p := filepath.Join(site.FilesDir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wrapperHTML(t *testing.T, site *store.Site) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(site.FilesDir(), "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wrapperCfg(t *testing.T, site *store.Site) wrapperConfig {
	t.Helper()
	html := wrapperHTML(t, site)
	m := regexp.MustCompile(`(?s)<script type="application/json" id="sitebin-config">(.*?)</script>`).FindStringSubmatch(html)
	if m == nil {
		t.Fatal("no config in wrapper")
	}
	var cfg wrapperConfig
	if err := json.Unmarshal([]byte(m[1]), &cfg); err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
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
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "viewer", "files.js"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	start := strings.Index(s, "const rendererByExt = {")
	end := strings.Index(s[start:], "};")
	if start < 0 || end < 0 {
		t.Fatal("no rendererByExt table in files.js")
	}
	js := map[string]string{}
	// A key may be quoted ("7z": a bare 7z is not a JavaScript name).
	for _, m := range regexp.MustCompile(`"?([a-z0-9]+)"?:\s*"([a-z]+)"`).FindAllStringSubmatch(s[start:start+end], -1) {
		js["."+m[1]] = m[2]
	}
	for ext, kind := range rendererByExt {
		if js[ext] != kind {
			t.Errorf("files.js: %s = %q, Go says %q", ext, js[ext], kind)
		}
	}
	for ext, kind := range js {
		if rendererByExt[ext] != kind {
			t.Errorf("files.js has %s = %q, Go says %q", ext, kind, rendererByExt[ext])
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

// The file list ships as two modules beside viewer.js, which imports both.
// They are embedded with the rest of the viewer; a missing one would leave
// every viewer site on a blank page.
func TestViewerModulesShip(t *testing.T) {
	for _, name := range []string{"viewer/files.js", "viewer/browser.js"} {
		if _, err := fs.ReadFile(web.Assets, name); err != nil {
			t.Errorf("%s not embedded: %v", name, err)
		}
	}
	src, err := fs.ReadFile(web.Assets, "viewer/viewer.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range []string{`from "./files.js"`, `from "./browser.js"`} {
		if !strings.Contains(string(src), imp) {
			t.Errorf("viewer.js does not import %s", imp)
		}
	}
}

// A viewer site with no visible file yet has nothing to fall back to. Its
// stored choice must stay open, or the first upload of a single file would
// find "" saved — the owner's list — and never open that file.
func TestApplyKeepsTheChoiceOpenWhileEmpty(t *testing.T) {
	s, site := makeSite(t)
	for _, f := range []string{"report.pdf", "notes.md"} {
		if err := s.DeleteFile(site, f); err != nil {
			t.Fatal(err)
		}
	}
	setViewer(t, s, site, "index.html") // creation default
	entry, err := Apply(site)
	if err != nil {
		t.Fatal(err)
	}
	if entry != "index.html" {
		t.Errorf("empty site saved entry %q, want the stored choice kept", entry)
	}
	if cfg := wrapperCfg(t, site); cfg.Entry != "" {
		t.Errorf("empty site's wrapper entry = %q, want the (empty) list", cfg.Entry)
	}

	// The first upload is a single file: it opens.
	writeRaw(t, site, "_raw/setup.exe", "MZ")
	entry, err = Apply(site)
	if err != nil {
		t.Fatal(err)
	}
	if entry != "setup.exe" {
		t.Errorf("after the first upload entry = %q, want the only file", entry)
	}
}

// A site whose only files are hidden opens on the (empty) list and names
// none of them.
func TestOnlyHiddenFilesOpenAnEmptyList(t *testing.T) {
	s, site := makeSite(t)
	for _, f := range []string{"report.pdf", "notes.md"} {
		if err := s.DeleteFile(site, f); err != nil {
			t.Fatal(err)
		}
	}
	writeRaw(t, site, ".DS_Store", "x")
	writeRaw(t, site, "__MACOSX/._a.pdf", "x")
	setViewer(t, s, site, "index.html")
	if _, err := Apply(site); err != nil {
		t.Fatal(err)
	}
	cfg := wrapperCfg(t, site)
	if cfg.Entry != "" || len(cfg.Files) != 0 {
		t.Errorf("config = %+v, want an empty list", cfg)
	}
}

// files.js hides the same names as isHiddenPath, so a wrapper written before
// the list existed (which still names them) shows none of them either.
func TestViewerJSHidesTheSameNames(t *testing.T) {
	src, err := fs.ReadFile(web.Assets, "viewer/files.js")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "export function isHiddenPath(") {
		t.Fatal("files.js has no isHiddenPath")
	}
	for _, name := range []string{`"__macosx"`, `"thumbs.db"`, `"desktop.ini"`, `startsWith(".")`} {
		if !strings.Contains(s, name) {
			t.Errorf("files.js isHiddenPath does not know %s", name)
		}
	}
	for _, p := range []string{".env", "a/.git/x", "__MACOSX/y", "p/Thumbs.db", "Desktop.ini"} {
		if !isHiddenPath(p) {
			t.Errorf("Go isHiddenPath(%q) = false", p)
		}
	}
}
