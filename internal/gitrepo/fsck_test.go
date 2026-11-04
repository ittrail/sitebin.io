package gitrepo

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Benign oddities that old or foreign tools wrote into real histories pass
// fsck on a push: receive.fsck.<id>=ignore for each.
func TestPushAcceptsBenignFsckOddities(t *testing.T) {
	NeedGit(t)
	const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	for _, c := range []struct{ id, kind string }{
		{"zeroPaddedFilemode", "tree"},
		{"badTimezone", "commit"},
		{"missingSpaceBeforeDate", "commit"},
	} {
		t.Run(c.id, func(t *testing.T) {
			r, dir := newBare(t)
			srv, _ := serveRepo(t, r, dir, 0)
			cl := newClient(t)
			wd := filepath.Join(t.TempDir(), "w")
			if err := os.MkdirAll(wd, 0o755); err != nil {
				t.Fatal(err)
			}
			cl.mustGit(wd, "init", "-q")
			literal := func(typ string, content []byte) string {
				t.Helper()
				cmd := exec.Command("git", "hash-object", "-t", typ, "--literally", "-w", "--stdin")
				cmd.Dir = wd
				cmd.Env = cl.env
				cmd.Stdin = bytes.NewReader(content)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("hash-object %s: %v\n%s", typ, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			if got := literal("tree", nil); got != emptyTree {
				t.Fatalf("empty tree = %s", got)
			}
			tree := emptyTree
			ident := "A U Thor <a@example.com> 1700000000 +0000"
			switch c.id {
			case "zeroPaddedFilemode":
				raw, _ := hex.DecodeString(emptyTree)
				tree = literal("tree", append([]byte("040000 dir\x00"), raw...))
			case "badTimezone":
				ident = "A U Thor <a@example.com> 1700000000 +01000"
			case "missingSpaceBeforeDate":
				ident = "A U Thor <a@example.com>1700000000 +0000"
			}
			commit := literal("commit", []byte("tree "+tree+"\nauthor "+ident+"\ncommitter C <c@example.com> 1700000000 +0000\n\nold history\n"))
			// The oddity is real: git's own fsck reports it.
			if out, err := cl.git(wd, "fsck", "--strict", "--no-dangling"); err == nil || !strings.Contains(out, c.id) {
				t.Fatalf("fixture: fsck does not report %s (err %v):\n%s", c.id, err, out)
			}
			cl.mustGit(wd, "push", srv.URL+"/repo.git", commit+":refs/heads/main")
			if refs, err := r.RefTargets(context.Background(), dir); err != nil || refs["refs/heads/main"] != commit {
				t.Errorf("main after the push = %v, %v; want %s", refs, err, commit)
			}
		})
	}
}
