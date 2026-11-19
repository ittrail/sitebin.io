package store

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestZipContentRoundTrips: a downloaded ZIP uploads again. Sitebin's own
// markers stay out of it — an upload refuses every top-level ".sitebin-"
// name, so one marker in the archive failed the whole re-upload.
func TestZipContentRoundTrips(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if err := s.SaveFile(site, "index.html", strings.NewReader("<p>hi</p>")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFile(site, "sub/.sitebin-notes", strings.NewReader("a user's own file")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTrusted(site, true); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSPAMarker(site); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.ZipContent(site, &buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "index.html,sub/.sitebin-notes" {
		t.Errorf("zip holds %v, want the user's files only", names)
	}
	other, _, _ := s.Create()
	if err := s.ExtractZip(other, bytes.NewReader(buf.Bytes()), int64(buf.Len())); err != nil {
		t.Errorf("the downloaded ZIP does not upload again: %v", err)
	}
}

// TestZipContentStoresMediaAndKeepsTimes: the download stores formats that
// are compressed already, deflates the rest, and keeps each file's time.
func TestZipContentStoresMediaAndKeepsTimes(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if err := s.SaveFile(site, "img/a.webp", strings.NewReader("RIFF....WEBPVP8 not really")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFile(site, "data/b.json", strings.NewReader(strings.Repeat(`{"a":1}`, 500))); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, p := range []string{"img/a.webp", "data/b.json"} {
		if err := os.Chtimes(filepath.Join(site.ContentDir(), filepath.FromSlash(p)), when, when); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	if err := s.ZipContent(site, &buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]uint16{}
	for _, f := range zr.File {
		methods[f.Name] = f.Method
		if !f.Modified.Equal(when) {
			t.Errorf("%s modified %v, want %v", f.Name, f.Modified, when)
		}
	}
	if methods["img/a.webp"] != zip.Store || methods["data/b.json"] != zip.Deflate {
		t.Errorf("methods = %v, want webp stored and json deflated", methods)
	}
}
