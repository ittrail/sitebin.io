package zipstream

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestMethodStoresWhatIsCompressedAlready(t *testing.T) {
	for _, name := range []string{
		"img/a.webp", "b.JPG", "c.jpeg", "d.png", "e.gif", "f.avif",
		"v.mp4", "v.webm", "s.mp3", "font.woff2", "font.woff",
		"x.zip", "x.tar.gz", "x.tgz", "x.zst", "doc.pdf", "doc.docx", "app.AppImage",
	} {
		if got := Method(name); got != zip.Store {
			t.Errorf("Method(%q) = %d, want Store", name, got)
		}
	}
	for _, name := range []string{
		"index.html", "data/x.json", "app.js", "style.css", "a.svg", "README",
		"font.ttf", "x.wasm", "db.sqlite", "dir.webp/file", ".webp",
	} {
		if got := Method(name); got != zip.Deflate {
			t.Errorf("Method(%q) = %d, want Deflate", name, got)
		}
	}
}

func TestWriterRoundTrips(t *testing.T) {
	random := make([]byte, 300<<10)
	rand.Read(random)
	text := []byte(strings.Repeat(`{"key":"value","n":12345}`+"\n", 20000))
	when := time.Date(2026, 11, 19, 10, 45, 58, 0, time.UTC)
	files := []struct {
		name string
		body []byte
	}{
		{"a.webp", random},
		{"data/b.json", text},
		{"empty.txt", nil},
	}

	var buf bytes.Buffer
	zw := NewWriter(&buf)
	for _, f := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: Method(f.name), Modified: when})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != len(files) {
		t.Fatalf("%d entries, want %d", len(zr.File), len(files))
	}
	for i, zf := range zr.File {
		want := files[i]
		if zf.Name != want.name || zf.Method != Method(want.name) {
			t.Errorf("entry %d = %s method %d", i, zf.Name, zf.Method)
		}
		if !zf.Modified.Equal(when) {
			t.Errorf("%s modified %v, want %v", zf.Name, zf.Modified, when)
		}
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || !bytes.Equal(got, want.body) {
			t.Errorf("%s: %d bytes back, want %d (err %v)", zf.Name, len(got), len(want.body), err)
		}
	}
	// The text compressed: deflate ran on it.
	if zr.File[1].CompressedSize64 >= zr.File[1].UncompressedSize64/4 {
		t.Errorf("json not compressed: %d of %d", zr.File[1].CompressedSize64, zr.File[1].UncompressedSize64)
	}
}

// TestWriterWritesInLargeChunks: the response sees a few large writes, not
// one per 4 KiB.
func TestWriterWritesInLargeChunks(t *testing.T) {
	cw := &countingWriter{}
	zw := NewWriter(cw)
	body := make([]byte, 1<<20)
	rand.Read(body)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "a.webp", Method: zip.Store})
	w.Write(body)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if cw.writes > (1<<20)/(32<<10) {
		t.Errorf("%d writes for 1 MiB", cw.writes)
	}
}

type countingWriter struct{ writes int }

func (c *countingWriter) Write(p []byte) (int, error) { c.writes++; return len(p), nil }

// BenchmarkSiteZip packs a site shaped like PhysioPrint (mostly images, some
// JSON) with archive/zip's default and with this writer.
func BenchmarkSiteZip(b *testing.B) {
	type file struct {
		name string
		body []byte
	}
	var files []file
	img := make([]byte, 24<<10)
	for i := 0; i < 400; i++ {
		rand.Read(img)
		files = append(files, file{fmt.Sprintf("data/img%d.webp", i), append([]byte(nil), img...)})
	}
	for i := 0; i < 150; i++ {
		files = append(files, file{fmt.Sprintf("data/d%d.json", i), []byte(strings.Repeat(fmt.Sprintf(`{"id":%d,"title":"Übung","reps":12},`, i), 120))})
	}
	var total int64
	for _, f := range files {
		total += int64(len(f.body))
	}
	run := func(b *testing.B, mk func(io.Writer) *zip.Writer, method func(string) uint16) {
		b.SetBytes(total)
		for i := 0; i < b.N; i++ {
			zw := mk(io.Discard)
			for _, f := range files {
				w, _ := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: method(f.name)})
				w.Write(f.body)
			}
			zw.Close()
		}
	}
	b.Run("archive-zip-default", func(b *testing.B) {
		run(b, zip.NewWriter, func(string) uint16 { return zip.Deflate })
	})
	b.Run("zipstream", func(b *testing.B) { run(b, NewWriter, Method) })
}
