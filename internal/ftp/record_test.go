package ftp

import (
	"os"
	"strings"
	"testing"
)

// Every successful write is reported for the site's provenance log; a read,
// a refused write and a failed one are not.
func TestWritesAreReported(t *testing.T) {
	q := newQuotaFs(t.TempDir(), 1000, 3)
	var got []string
	q.onWrite = func(action, path string) { got = append(got, action+" "+path) }

	f, err := q.Create("/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("x"))
	f.Close()
	if err := q.Mkdir("/dir", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := q.Rename("/a.txt", "/dir/b.txt"); err != nil {
		t.Fatal(err)
	}
	if r, err := q.OpenFile("/dir/b.txt", os.O_RDONLY, 0); err == nil {
		r.Close()
	}
	if err := q.Remove("/missing.txt"); err == nil {
		t.Fatal("removing a missing file succeeded")
	}
	if _, err := q.Create("/_sitebin/x"); err == nil {
		t.Fatal("a reserved name was written")
	}
	if err := q.Remove("/dir/b.txt"); err != nil {
		t.Fatal(err)
	}

	want := "upload a.txt|mkdir dir|move a.txt → dir/b.txt|delete-file dir/b.txt"
	if strings.Join(got, "|") != want {
		t.Fatalf("reported %q\nwant     %q", strings.Join(got, "|"), want)
	}
}
