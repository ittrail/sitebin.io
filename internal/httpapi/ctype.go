package httpapi

import (
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/ittrail/sitebin.io/internal/store"
)

// Content types Caddy cannot name. Caddy's file server types a file by its
// extension from the system's table (mailcap's /etc/mime.types in the image)
// and, finding none, sends NO Content-Type — on purpose, so no browser sniffs
// an upload into a page. That left release files (.dmg, .yml, .AppImage,
// .msix …) and every file without an extension (SHA256SUMS, a Linux binary)
// without a type, beside nosniff. authz names those few in typeHeader, which
// Caddy sets as Content-Type on a 2xx. Everything the system's table already
// types stays Caddy's, unchanged. See
// docs/superpowers/specs/2026-11-11-release-hosting-design.md.

// typeHeader carries the type from authz to Caddy. Caddy strips any copy a
// client sent before the gate runs, as it does for the cache header.
const typeHeader = "X-Sitebin-Type"

const textPlain = "text/plain; charset=utf-8"

// extraTypes are the extensions the image's table lacks that a release or a
// download page carries. Keys are lower case. None is a type a browser runs
// or renders as a page.
var extraTypes = map[string]string{
	".dmg":          "application/x-apple-diskimage",
	".yml":          "application/yaml", // RFC 9512
	".yaml":         "application/yaml",
	".appimage":     "application/vnd.appimage",
	".msix":         "application/msix",
	".msixbundle":   "application/msixbundle",
	".appx":         "application/appx",
	".appxbundle":   "application/appxbundle",
	".appinstaller": "application/appinstaller",
	".msi":          "application/x-msi",
	".blockmap":     "application/octet-stream", // electron-updater's differential map
	".7z":           "application/x-7z-compressed",
	".sha256":       textPlain,
	".sha512":       textPlain,
	".minisig":      textPlain,
}

// fileType is the type authz names for the file at rel: an extension from
// extraTypes, or — for a name without an extension — plain text when the
// file's first bytes (head) are text, else application/octet-stream. Never
// anything a browser would render from a sniff: an HTML file without an
// extension is a download. "" leaves the type to Caddy.
func fileType(rel string, head func() []byte) string {
	if rel == "" || strings.HasSuffix(rel, "/") {
		return ""
	}
	ext := path.Ext(path.Base(rel))
	if ext != "" {
		return extraTypes[strings.ToLower(ext)]
	}
	if t := http.DetectContentType(head()); strings.HasPrefix(t, "text/plain") {
		return t
	}
	return "application/octet-stream"
}

// typeValue is fileType for a request authz admitted: rel is the path the
// file server will serve, relative to the served folder, and isFile whether
// it is an existing regular file there. Only a file without an extension is
// read, and only its first 512 bytes (what http.DetectContentType looks at),
// through the served folder's os.Root.
func (a *API) typeValue(site *store.Site, rel string, isFile bool) string {
	if !isFile {
		return ""
	}
	return fileType(rel, func() []byte {
		root, err := store.OpenServedRoot(site)
		if err != nil {
			return nil
		}
		defer root.Close()
		f, err := root.Open(rel)
		if err != nil {
			return nil
		}
		defer f.Close()
		buf := make([]byte, 512)
		n, _ := io.ReadFull(f, buf)
		return buf[:n]
	})
}
