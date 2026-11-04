package cleanup

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/gitrepo"
	"github.com/ittrail/sitebin.io/internal/store"
)

func gitBytes(t *testing.T, st *store.Store, site *store.Site) int64 {
	t.Helper()
	u, err := st.SiteUsage(site)
	if err != nil {
		t.Fatal(err)
	}
	return u.GitBytes
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	for _, pat := range []string{"objects/tmp_objdir-*", "objects/incoming-*", "objects/pack/tmp_*"} {
		m, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pat)))
		if err != nil {
			t.Fatal(err)
		}
		found = append(found, m...)
	}
	return found
}

// What an interrupted push leaves in the object store is removed by the
// next collection round, gc due or not, and the site's bytes are what they
// were before the push.
func TestCollectGitRemovesLeftovers(t *testing.T) {
	st, r := collectFixture(t)
	site := gitSite(t, st, r)
	CollectGit(context.Background(), st, r) // the push's collection
	before := gitBytes(t, st, site)
	for rel, n := range map[string]int{
		"objects/tmp_objdir-incoming-x1/pack/tmp_pack_a": 2 << 20,
		"objects/incoming-y2/pack/tmp_pack_b":            1 << 20,
		"objects/pack/tmp_pack_c":                        1 << 20,
	} {
		p := filepath.Join(site.GitDir(), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := gitBytes(t, st, site); got != before+4<<20 {
		t.Fatalf("setup: %d bytes, want %d", got, before+4<<20)
	}
	CollectGit(context.Background(), st, r)
	if l := leftovers(t, site.GitDir()); len(l) != 0 {
		t.Errorf("left after the collection: %v", l)
	}
	if got := gitBytes(t, st, site); got != before {
		t.Errorf("GitBytes = %d after the collection, want %d as before the push", got, before)
	}
}

// A push killed while it streams its pack (a client hanging up, a delete,
// a lock, the read timeout): git cannot remove its quarantine, and the next
// collection does. The leftovers exist where the kill reaches git's whole
// process group, which is Linux (the image); on Windows only receive-pack
// dies and its index-pack ends by itself.
func TestCollectGitRemovesAKilledPushsLeftovers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a killed git leaves its children running on Windows")
	}
	st, r := collectFixture(t)
	site := gitSite(t, st, r)
	CollectGit(context.Background(), st, r)
	before := gitBytes(t, st, site)

	pack, sha := randomPack(t, 200, 32<<10) // over 100 objects: index-pack, writing as it receives
	cmd := strings.Repeat("0", 40) + " " + sha + " refs/heads/main\x00report-status\n"
	cmds := fmt.Sprintf("%04x%s0000", len(cmd)+4, cmd)
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	go func() { _, _ = pw.Write(append([]byte(cmds), pack[:len(pack)/2]...)) }() // then silence
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan gitrepo.RPCResult, 1)
	go func() {
		res, _ := r.ServeRPC(ctx, site.GitDir(), gitrepo.ReceivePack, pr, httptest.NewRecorder(), gitrepo.RPCOptions{MaxInputSize: 100 << 20})
		done <- res
	}()
	deadline := time.Now().Add(20 * time.Second)
	for len(leftovers(t, site.GitDir())) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the push never started receiving")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // let index-pack write what it has
	cancel()
	var res gitrepo.RPCResult
	select {
	case res = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the cancelled push never returned")
	}
	if !res.Started {
		t.Error("RPCResult.Started is false for a push git ran")
	}
	if got := gitBytes(t, st, site); got <= before {
		t.Fatalf("the killed push left nothing (%d bytes, %d before): nothing to test", got, before)
	}
	t.Logf("left by the killed push: %v (%d bytes)", leftovers(t, site.GitDir()), gitBytes(t, st, site)-before)

	CollectGit(context.Background(), st, r)
	if l := leftovers(t, site.GitDir()); len(l) != 0 {
		t.Errorf("left after the collection: %v", l)
	}
	if got := gitBytes(t, st, site); got != before {
		t.Errorf("GitBytes = %d after the collection, want %d as before the push", got, before)
	}
}

// randomPack is a pack of n random blobs of size bytes each, and the sha
// a push of it would name (any object name: the push never gets that far).
func randomPack(t *testing.T, n, size int) ([]byte, string) {
	t.Helper()
	wd := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + wd, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(wd, "none")}
	git := func(stdin io.Reader, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env, cmd.Stdin = wd, env, stdin
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return string(out)
	}
	git(nil, "init", "-q", "--bare", ".")
	var shas bytes.Buffer
	blob := make([]byte, size)
	for i := 0; i < n; i++ {
		if _, err := rand.Read(blob); err != nil {
			t.Fatal(err)
		}
		shas.WriteString(git(bytes.NewReader(blob), "hash-object", "-w", "--stdin"))
	}
	sha := strings.TrimSpace(strings.SplitN(shas.String(), "\n", 2)[0])
	return []byte(git(&shas, "pack-objects", "--stdout", "-q")), sha
}
