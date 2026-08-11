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
<meta http-equiv="Content-Security-Policy" content="default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self' data:; worker-src 'self' blob:; object-src 'none'; frame-ancestors 'self'; base-uri 'none'">
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

type wrapperConfig struct {
	Entry    string           `json:"entry"`
	Renderer string           `json:"renderer"`
	Files    []store.FileInfo `json:"files"`
}

// Apply converts the site's files/ tree to viewer layout and (re)generates
// the wrapper. It returns the effective entry file so callers can persist it
// when the configured one was missing. Safe to call repeatedly.
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
	files, err := listRaw(site)
	if err != nil {
		return "", err
	}
	entry := site.Meta.EntryFile
	found := false
	for _, f := range files {
		if f.Path == entry {
			found = true
			break
		}
	}
	if !found {
		entry = ""
		if len(files) > 0 {
			entry = files[0].Path
		}
	}
	cfgJSON, err := json.Marshal(wrapperConfig{
		Entry:    entry,
		Renderer: RendererFor(entry),
		Files:    files,
	})
	if err != nil {
		return "", err
	}
	title := entry
	if title == "" {
		title = "Sitebin viewer"
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

// listRaw lists files under _raw with slash paths relative to _raw.
func listRaw(site *store.Site) ([]store.FileInfo, error) {
	root := filepath.Join(site.FilesDir(), "_raw")
	var out []store.FileInfo
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
		out = append(out, store.FileInfo{Path: filepath.ToSlash(rel), Size: fi.Size()})
		return nil
	})
	if os.IsNotExist(err) {
		return []store.FileInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []store.FileInfo{}
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
