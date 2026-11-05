// Package viewer implements Sitebin's viewer mode without a smart server: at
// save time it moves the user's files into files/_raw/ and generates a static
// wrapper page as files/index.html. Caddy keeps serving plain files; the
// wrapper fetches /_raw/<entry> client-side and renders it with the shared
// assets exposed at /_sitebin/assets/ on every site origin.
package viewer

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ittrail/sitebin.io/internal/store"
)

// wrapperMarker identifies generated wrappers so mode switches never mistake
// a user's own index.html for ours. It is a meta tag because html/template
// strips HTML comments.
const wrapperMarker = `<meta name="generator" content="sitebin-viewer-wrapper">`

var rendererByExt = map[string]string{
	".pdf":      "pdf",
	".md":       "markdown",
	".markdown": "markdown",
	".mdown":    "markdown",
	".docx":     "docx",
	".csv":      "table",
	".tsv":      "table",
	".ipynb":    "notebook",
	".png":      "image",
	".jpg":      "image",
	".jpeg":     "image",
	".gif":      "image",
	".webp":     "image",
	".svg":      "image",
	".avif":     "image",
	".bmp":      "image",
	".ico":      "image",
	".mp4":      "video",
	".webm":     "video",
	".mov":      "video",
	".m4v":      "video",
	".mp3":      "audio",
	".wav":      "audio",
	".ogg":      "audio",
	".m4a":      "audio",
	".flac":     "audio",
	// Files the browser cannot show: offered as a download, never fetched
	// and printed as text. Anything else unknown still goes to the text
	// renderer, which reads at most 2 MiB and turns to the download view
	// itself when the bytes are binary.
	".exe": "download", ".msi": "download", ".dmg": "download", ".pkg": "download",
	".app": "download", ".apk": "download", ".ipa": "download", ".deb": "download",
	".rpm": "download", ".appimage": "download",
	".zip": "download", ".7z": "download", ".rar": "download", ".tar": "download",
	".gz": "download", ".tgz": "download", ".bz2": "download", ".xz": "download",
	".zst": "download", ".iso": "download", ".img": "download", ".bin": "download",
	".dll": "download", ".so": "download", ".dylib": "download", ".jar": "download",
	".wasm": "download", ".woff": "download", ".woff2": "download", ".ttf": "download",
	".otf": "download", ".eot": "download", ".psd": "download", ".ai": "download",
	".sketch": "download", ".fig": "download", ".blend": "download",
	".xlsx": "download", ".xls": "download", ".pptx": "download", ".ppt": "download",
	".doc": "download", ".odt": "download", ".ods": "download", ".odp": "download",
	".epub": "download", ".heic": "download", ".tif": "download", ".tiff": "download",
	".mkv": "download", ".avi": "download", ".wmv": "download", ".flv": "download",
	".sqlite": "download", ".db": "download",
}

// RendererFor maps a filename to the viewer renderer module that displays it.
func RendererFor(filename string) string {
	if r, ok := rendererByExt[strings.ToLower(path.Ext(filename))]; ok {
		return r
	}
	return "text"
}

var wrapperTmpl = template.Must(template.New("wrapper").Parse(`<!doctype html>
<html lang="en">
<head>
` + wrapperMarker + `
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="theme-color" content="#0a0e18">
<meta http-equiv="Content-Security-Policy" content="default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self' data:; worker-src 'self' blob:; object-src 'none'; base-uri 'none'">
<meta name="referrer" content="no-referrer">
<meta name="robots" content="noindex">
<title>{{.Title}}</title>
<link rel="icon" href="/_sitebin/assets/static/favicon.svg">
<link rel="stylesheet" href="/_sitebin/assets/viewer/viewer.css">
</head>
<body>
<script type="application/json" id="sitebin-config">{{.ConfigJSON}}</script>
<div id="viewer-app" data-loading>Loading viewer…</div>
<script type="module" src="/_sitebin/assets/viewer/viewer.js"></script>
</body>
</html>
`))

// wrapperConfig is what the wrapper page hands viewer.js. Entry "" opens the
// file list; otherwise the named file opens first.
type wrapperConfig struct {
	Entry    string        `json:"entry"`
	Renderer string        `json:"renderer"`
	Files    []wrapperFile `json:"files"`
}

// wrapperFile is one file of the list. It is the wrapper's own shape, not
// store.FileInfo, which is the API's and MCP's output.
type wrapperFile struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"` // Unix seconds
}

// Apply converts the site's files/ tree to viewer layout and (re)generates
// the wrapper. It returns the entry file callers persist: the effective one
// ("" is the file list), or the stored choice unchanged while the site has no
// visible file. Safe to call repeatedly.
func Apply(site *store.Site) (string, error) {
	filesDir := site.FilesDir()
	rawDir := filepath.Join(filesDir, "_raw")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		return "", fmt.Errorf("create _raw: %w", err)
	}

	entries, err := os.ReadDir(filesDir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name() == "_raw" {
			continue
		}
		// The trust marker belongs at the serving root in every mode: Caddy's
		// root is files/ and its matcher looks for /.sitebin-trusted there.
		// Sweeping it into _raw would silently harden every viewer site and,
		// worse, offer it up as the entry file.
		if e.Name() == store.TrustedMarker {
			continue
		}
		src := filepath.Join(filesDir, e.Name())
		if isGeneratedWrapper(src) {
			if err := os.Remove(src); err != nil {
				return "", err
			}
			continue
		}
		dst := filepath.Join(rawDir, e.Name())
		if err := os.Rename(src, dst); err != nil {
			return "", fmt.Errorf("move %s to _raw: %w", e.Name(), err)
		}
	}
	return generate(site)
}

// Regenerate rewrites the wrapper for the current _raw contents (after
// uploads or entry-file changes while already in viewer mode).
func Regenerate(site *store.Site) (string, error) {
	return Apply(site) // Apply is idempotent and also sweeps stray files
}

func generate(site *store.Site) (string, error) {
	all, err := listRaw(site)
	if err != nil {
		return "", err
	}
	files := make([]wrapperFile, 0, len(all))
	for _, f := range all {
		if !isHiddenPath(f.Path) {
			files = append(files, f)
		}
	}
	entry := startPage(site.Meta.EntryFile, all, files)
	renderer := ""
	if entry != "" {
		renderer = RendererFor(entry)
	}
	cfgJSON, err := json.Marshal(wrapperConfig{
		Entry:    entry,
		Renderer: renderer,
		Files:    files,
	})
	if err != nil {
		return "", err
	}
	title := path.Base(entry)
	if entry == "" {
		title = "Files"
	}
	var buf strings.Builder
	err = wrapperTmpl.Execute(&buf, map[string]any{
		"Title":      title,
		"ConfigJSON": template.JS(cfgJSON), // json.Marshal HTML-escapes <, >, &
	})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(site.FilesDir(), "index.html"), []byte(buf.String()), 0o644); err != nil {
		return "", fmt.Errorf("write wrapper: %w", err)
	}
	// With no visible file there is nothing to fall back to: the page shows
	// the (empty) list, but the stored choice stays as it was, so the first
	// upload decides again. Saving "" here would read later as the owner's
	// choice of the list, and a lone file would never open.
	if entry == "" && len(files) == 0 {
		return site.Meta.EntryFile, nil
	}
	return entry, nil
}

// Remove restores webserver layout: wrapper artifacts deleted, _raw contents
// moved back to files/.
func Remove(site *store.Site) error {
	filesDir := site.FilesDir()
	rawDir := filepath.Join(filesDir, "_raw")

	entries, err := os.ReadDir(filesDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "_raw" {
			continue
		}
		p := filepath.Join(filesDir, e.Name())
		if isGeneratedWrapper(p) {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	rawEntries, err := os.ReadDir(rawDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range rawEntries {
		if err := os.Rename(filepath.Join(rawDir, e.Name()), filepath.Join(filesDir, e.Name())); err != nil {
			return fmt.Errorf("restore %s: %w", e.Name(), err)
		}
	}
	return os.Remove(rawDir)
}

// startPage decides what the viewer opens on. "" is the file list: the
// owner's explicit choice, never replaced. A chosen file that exists opens
// first, even one the list hides. A choice that names no file (the creation
// default index.html, a deleted file) falls back: the one visible file when
// there is exactly one, else the list.
func startPage(chosen string, all, visible []wrapperFile) string {
	if chosen == "" {
		return ""
	}
	for _, f := range all {
		if f.Path == chosen {
			return chosen
		}
	}
	if len(visible) == 1 {
		return visible[0].Path
	}
	return ""
}

// isHiddenPath reports whether the list leaves a file out: any segment that
// is a dotfile or dot-folder (the .sitebin-* markers among them), or
// operating-system clutter. Hidden files are still served at /_raw/.
func isHiddenPath(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
		switch strings.ToLower(seg) {
		case "__macosx", "thumbs.db", "desktop.ini":
			return true
		}
	}
	return false
}

// listRaw lists files under _raw with slash paths relative to _raw.
func listRaw(site *store.Site) ([]wrapperFile, error) {
	root := filepath.Join(site.FilesDir(), "_raw")
	var out []wrapperFile
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, wrapperFile{Path: filepath.ToSlash(rel), Size: fi.Size(), Mtime: fi.ModTime().Unix()})
		return nil
	})
	if os.IsNotExist(err) {
		return []wrapperFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []wrapperFile{}
	}
	return out, nil
}

// isGeneratedWrapper sniffs the first bytes of a file for our marker.
func isGeneratedWrapper(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	return strings.Contains(string(buf[:n]), wrapperMarker)
}
