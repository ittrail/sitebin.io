package ftp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// recGuard stands in for the site's abuse guard: it stages into the real
// directory and records what it was asked.
type recGuard struct {
	dir     string
	staged  []string
	renamed [][2]string
	holdOn  string // a rel whose Close reports a hold
}

func (g *recGuard) Stage(rel string, flag int, perm os.FileMode) (File, error) {
	g.staged = append(g.staged, rel)
	f, err := os.OpenFile(filepath.Join(g.dir, rel), flag, perm)
	if err != nil {
		return nil, err
	}
	if rel == g.holdOn {
		return heldFile{f}, nil
	}
	return f, nil
}

func (g *recGuard) Rename(o, n string) error {
	g.renamed = append(g.renamed, [2]string{o, n})
	return os.Rename(filepath.Join(g.dir, o), filepath.Join(g.dir, n))
}

var errHeld = errors.New("held for review")

type heldFile struct{ *os.File }

func (h heldFile) Close() error { h.File.Close(); return errHeld }

var _ afero.File = heldFile{}

// With a guard every write and every rename of a session goes through it:
// the FTP half of "every write is scanned before it is visible".
func TestSessionWritesGoThroughTheGuard(t *testing.T) {
	dir := t.TempDir()
	g := &recGuard{dir: dir, holdOn: "kit.html"}
	q := newQuotaFs(dir, 1<<20, 100)
	q.guard = g

	f, err := q.Create("/sub.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("x"))
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := q.Rename("/sub.txt", "/other.txt"); err != nil {
		t.Fatal(err)
	}
	f, _ = q.OpenFile("/kit.html", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	f.Write([]byte("kit"))
	if err := f.Close(); !errors.Is(err, errHeld) {
		t.Errorf("a hold must reach the transfer: %v", err)
	}
	// reads do not
	if r, err := q.Open("/other.txt"); err != nil {
		t.Fatal(err)
	} else {
		r.Close()
	}
	if len(g.staged) != 2 || g.staged[0] != "sub.txt" || g.staged[1] != "kit.html" {
		t.Errorf("staged %v", g.staged)
	}
	if len(g.renamed) != 1 || g.renamed[0] != [2]string{"sub.txt", "other.txt"} {
		t.Errorf("renamed %v", g.renamed)
	}
	if err := q.Rename("/", "/x"); err == nil {
		t.Error("renaming the root through the guard")
	}
}
