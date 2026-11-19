// Package zipstream is the ZIP writer of downloads: a site's files, a backup
// version. It packs as the response goes out, with as little work as the
// archive allows: formats that are compressed already are stored, the rest
// is deflated at BestSpeed from a pool, and the response gets 64 KiB writes.
// See docs/superpowers/specs/2026-11-19-streamed-downloads-design.md.
package zipstream

import (
	"archive/zip"
	"bufio"
	"compress/flate"
	"io"
	"path"
	"strings"
	"sync"
)

// bufferSize is the size of the writes a download makes to its response.
const bufferSize = 64 << 10

// stored are the extensions (lower case) of formats that are compressed
// already: deflating them costs CPU and saves next to nothing.
var stored = map[string]bool{}

func init() {
	for _, ext := range strings.Fields(`
		webp jpg jpeg png gif avif heic heif jxl
		mp4 m4v mov webm mkv mp3 m4a aac ogg oga opus flac
		woff woff2
		zip gz tgz bz2 xz zst br 7z rar lz4
		jar apk aab ipa nupkg whl vsix xpi crx epub
		docx xlsx pptx odt ods odp
		dmg msi msix appimage deb rpm snap
		pdf`) {
		stored[ext] = true
	}
}

// Method is the ZIP method for the file name: zip.Store for a format that is
// compressed already, zip.Deflate for everything else. Only the last
// element's extension counts, and a dotfile (".webp") has none.
func Method(name string) uint16 {
	base := path.Base(name)
	i := strings.LastIndexByte(base, '.')
	if i <= 0 || !stored[strings.ToLower(base[i+1:])] {
		return zip.Deflate
	}
	return zip.Store
}

// NewWriter returns a zip.Writer that writes to w through a 64 KiB buffer
// (archive/zip uses a bufio.Writer it is given as it is, and flushes it on
// Close) and deflates at BestSpeed with pooled compressors.
func NewWriter(w io.Writer) *zip.Writer {
	zw := zip.NewWriter(bufio.NewWriterSize(w, bufferSize))
	zw.RegisterCompressor(zip.Deflate, newFlateWriter)
	return zw
}

var flatePool sync.Pool

func newFlateWriter(w io.Writer) (io.WriteCloser, error) {
	fw, ok := flatePool.Get().(*flate.Writer)
	if ok {
		fw.Reset(w)
	} else {
		var err error
		if fw, err = flate.NewWriter(w, flate.BestSpeed); err != nil {
			return nil, err
		}
	}
	return &pooledFlate{fw: fw}, nil
}

// pooledFlate returns its compressor to the pool on Close.
type pooledFlate struct {
	mu sync.Mutex
	fw *flate.Writer
}

func (p *pooledFlate) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fw == nil {
		return 0, io.ErrClosedPipe
	}
	return p.fw.Write(b)
}

func (p *pooledFlate) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fw == nil {
		return nil
	}
	err := p.fw.Close()
	flatePool.Put(p.fw)
	p.fw = nil
	return err
}
