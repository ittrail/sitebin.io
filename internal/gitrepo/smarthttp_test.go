package gitrepo

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const zeros = "0000000000000000000000000000000000000000"

// rpcLog collects what ServeRPC reported.
type rpcLog struct {
	mu      sync.Mutex
	updates []RefUpdate
	bytesIn []int64
	errs    []error
}

func (l *rpcLog) add(res RPCResult, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.updates = append(l.updates, res.Updates...)
	l.bytesIn = append(l.bytesIn, res.BytesIn)
	if err != nil {
		l.errs = append(l.errs, err)
	}
}

// serveRepo serves dir (a bare repository) over smart HTTP, with no auth.
func serveRepo(t *testing.T, r *Runner, dir string, maxInput int64) (*httptest.Server, *rpcLog) {
	t.Helper()
	log := &rpcLog{}
	mux := http.NewServeMux()
	mux.HandleFunc("/repo.git/info/refs", func(w http.ResponseWriter, req *http.Request) {
		svc, ok := ParseService(req.URL.Query().Get("service"))
		if !ok {
			http.Error(w, "bad service", http.StatusBadRequest)
			return
		}
		if err := r.AdvertiseRefs(req.Context(), dir, svc, ProtocolFromHeader(req.Header.Get("Git-Protocol")), w); err != nil {
			t.Logf("advertise: %v", err)
		}
	})
	rpc := func(svc Service) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			res, err := r.ServeRPC(req.Context(), dir, svc, req.Body, w, RPCOptions{
				Protocol:     ProtocolFromHeader(req.Header.Get("Git-Protocol")),
				Gzip:         req.Header.Get("Content-Encoding") == "gzip",
				MaxInputSize: maxInput,
			})
			log.add(res, err)
		}
	}
	mux.HandleFunc("/repo.git/git-upload-pack", rpc(UploadPack))
	mux.HandleFunc("/repo.git/git-receive-pack", rpc(ReceivePack))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, log
}

// client runs a hermetic git client.
type client struct {
	t    *testing.T
	home string
	env  []string
}

func newClient(t *testing.T) *client {
	t.Helper()
	home := t.TempDir()
	empty := filepath.Join(home, "empty.gitconfig")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + empty,
		"GIT_TERMINAL_PROMPT=0",
	}
	if runtime.GOOS == "windows" {
		env = append(env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"), "TEMP="+os.TempDir(), "TMP="+os.TempDir())
	}
	return &client{t: t, home: home, env: env}
}

func (c *client) git(dir string, args ...string) (string, error) {
	c.t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main", "-c", "credential.helper="}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = c.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (c *client) mustGit(dir string, args ...string) string {
	c.t.Helper()
	out, err := c.git(dir, args...)
	if err != nil {
		c.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(out)
}

func newBare(t *testing.T) (*Runner, string) {
	r, _ := newRunner(t, 4)
	dir := filepath.Join(t.TempDir(), "git")
	if err := r.Init(dir); err != nil {
		t.Fatal(err)
	}
	return r, dir
}

func commitFile(t *testing.T, c *client, wd, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wd, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	c.mustGit(wd, "add", name)
	c.mustGit(wd, "commit", "-m", "add "+name)
	return c.mustGit(wd, "rev-parse", "HEAD")
}

func TestCloneEmptyThenPushThenClone(t *testing.T) {
	NeedGit(t)
	for _, proto := range []string{"0", "2"} {
		t.Run("v"+proto, func(t *testing.T) {
			r, dir := newBare(t)
			srv, _ := serveRepo(t, r, dir, 0)
			c := newClient(t)
			url := srv.URL + "/repo.git"
			wd := filepath.Join(t.TempDir(), "w")
			c.mustGit(filepath.Dir(wd), "-c", "protocol.version="+proto, "clone", url, wd)
			sha := commitFile(t, c, wd, "a.txt", "hello")
			c.mustGit(wd, "-c", "protocol.version="+proto, "push", "origin", "main")
			wd2 := filepath.Join(t.TempDir(), "w2")
			c.mustGit(filepath.Dir(wd2), "-c", "protocol.version="+proto, "clone", url, wd2)
			if got := c.mustGit(wd2, "rev-parse", "HEAD"); got != sha {
				t.Errorf("clone HEAD = %s, want %s", got, sha)
			}
		})
	}
}

func TestPushRecordsRefUpdates(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	srv, log := serveRepo(t, r, dir, 0)
	c := newClient(t)
	wd := filepath.Join(t.TempDir(), "w")
	c.mustGit(filepath.Dir(wd), "clone", srv.URL+"/repo.git", wd)
	sha := commitFile(t, c, wd, "a.txt", "x")
	c.mustGit(wd, "push", "origin", "main")
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.updates) != 1 {
		t.Fatalf("updates = %+v", log.updates)
	}
	u := log.updates[0]
	if u.Ref != "refs/heads/main" || u.Old != zeros || u.New != sha {
		t.Errorf("update = %+v, want main %s -> %s", u, zeros, sha)
	}
	// The last request is the push; its pack alone is over 100 bytes.
	if n := len(log.bytesIn); n == 0 || log.bytesIn[n-1] < 100 {
		t.Errorf("BytesIn = %v, want a push of a real size last", log.bytesIn)
	}
}

func TestPushFromShallowClone(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	srv, log := serveRepo(t, r, dir, 0)
	c := newClient(t)
	url := srv.URL + "/repo.git"
	wd := filepath.Join(t.TempDir(), "w")
	c.mustGit(filepath.Dir(wd), "clone", url, wd)
	commitFile(t, c, wd, "a.txt", "1")
	commitFile(t, c, wd, "b.txt", "2")
	commitFile(t, c, wd, "c.txt", "3")
	c.mustGit(wd, "push", "origin", "main")
	shallow := filepath.Join(t.TempDir(), "s")
	c.mustGit(filepath.Dir(shallow), "clone", "--depth", "1", url, shallow)
	if got := c.mustGit(shallow, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Fatalf("not shallow: %s", got)
	}
	sha := commitFile(t, c, shallow, "d.txt", "4")
	c.mustGit(shallow, "push", "origin", "main")
	log.mu.Lock()
	defer log.mu.Unlock()
	if n := len(log.updates); n != 2 || log.updates[1].New != sha {
		t.Errorf("updates = %+v", log.updates)
	}
	if len(log.errs) != 0 {
		t.Errorf("errors: %v", log.errs)
	}
}

func TestForcePushAndBranchDelete(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	srv, _ := serveRepo(t, r, dir, 0)
	c := newClient(t)
	wd := filepath.Join(t.TempDir(), "w")
	c.mustGit(filepath.Dir(wd), "clone", srv.URL+"/repo.git", wd)
	commitFile(t, c, wd, "a.txt", "1")
	c.mustGit(wd, "push", "origin", "main")
	c.mustGit(wd, "push", "origin", "main:feature")
	c.mustGit(wd, "commit", "--amend", "-m", "rewritten")
	c.mustGit(wd, "push", "-f", "origin", "main")
	c.mustGit(wd, "push", "origin", ":feature")
	if out := c.mustGit(wd, "ls-remote", "origin"); strings.Contains(out, "feature") {
		t.Errorf("feature still there:\n%s", out)
	}
}

func dirSize(t *testing.T, dir string) (int64, []string) {
	t.Helper()
	var n int64
	var names []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		names = append(names, p)
		if !d.IsDir() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			n += fi.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n, names
}

func TestPushOverMaxInputSizeIsRefusedWithoutResidue(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	srv, _ := serveRepo(t, r, dir, 1<<20)
	c := newClient(t)
	wd := filepath.Join(t.TempDir(), "w")
	c.mustGit(filepath.Dir(wd), "clone", srv.URL+"/repo.git", wd)
	blob := make([]byte, 2<<20)
	// A cheap deterministic stream that does not deflate.
	x := uint32(2463534242)
	for i := range blob {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		blob[i] = byte(x)
	}
	commitFile(t, c, wd, "big.bin", string(blob))
	before, _ := dirSize(t, dir)
	out, err := c.git(wd, "push", "origin", "main")
	if err == nil {
		t.Fatalf("push succeeded:\n%s", out)
	}
	if !strings.Contains(out, "pack exceeds maximum allowed size") {
		t.Errorf("client output lacks the size message:\n%s", out)
	}
	after, names := dirSize(t, dir)
	if before != after {
		t.Errorf("repository size changed: %d -> %d", before, after)
	}
	for _, n := range names {
		b := filepath.Base(n)
		if strings.HasPrefix(b, "incoming-") || strings.HasPrefix(b, "tmp_pack_") || strings.HasPrefix(b, "tmp_obj_") {
			t.Errorf("residue: %s", n)
		}
	}
	// git reports the refusal in-band and exits 0, so ServeRPC has no error
	// to report here; the client's failure above is the contract.
}

func TestHooksNeverRun(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	ran := filepath.Join(dir, "ran.txt")
	script := "#!/bin/sh\ntouch \"" + filepath.ToSlash(ran) + "\"\n"
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"post-receive", "pre-receive", "update"} {
		if err := os.WriteFile(filepath.Join(dir, "hooks", h), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := os.OpenFile(filepath.Join(dir, "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.WriteString("[core]\n\thooksPath = hooks\n"); err != nil {
		t.Fatal(err)
	}
	cfg.Close()
	srv, _ := serveRepo(t, r, dir, 0)
	c := newClient(t)
	wd := filepath.Join(t.TempDir(), "w")
	c.mustGit(filepath.Dir(wd), "clone", srv.URL+"/repo.git", wd)
	commitFile(t, c, wd, "a.txt", "x")
	c.mustGit(wd, "push", "origin", "main")
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("a hook ran")
	}
	if got := c.mustGit(wd, "ls-remote", "origin"); !strings.Contains(got, "refs/heads/main") {
		t.Fatalf("push did not land:\n%s", got)
	}
}

func TestRefusalPrintsRemoteError(t *testing.T) {
	NeedGit(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		svc, ok := ParseService(req.URL.Query().Get("service"))
		if !ok {
			svc = UploadPack
		}
		WriteRefusal(w, svc, "storage full")
	}))
	defer srv.Close()
	c := newClient(t)
	url := srv.URL + "/repo.git"
	for _, proto := range []string{"0", "2"} {
		wd := t.TempDir()
		c.mustGit(wd, "init", "-q")
		c.mustGit(wd, "commit", "-q", "--allow-empty", "-m", "x")
		for _, args := range [][]string{
			{"ls-remote", url},
			{"clone", url, filepath.Join(t.TempDir(), "c")},
			{"push", url, "HEAD:refs/heads/main"},
		} {
			out, err := c.git(wd, append([]string{"-c", "protocol.version=" + proto}, args...)...)
			if err == nil {
				t.Errorf("v%s %v succeeded", proto, args)
			}
			if !strings.Contains(out, "fatal: remote error: storage full") {
				t.Errorf("v%s %v output:\n%s", proto, args, out)
			}
		}
	}
}

func TestProtocolFromHeader(t *testing.T) {
	for in, want := range map[string]string{
		"version=2":                      "version=2",
		"version=2:object-format=sha256": "",
		"version=1":                      "",
		"x":                              "",
		"":                               "",
	} {
		if got := ProtocolFromHeader(in); got != want {
			t.Errorf("ProtocolFromHeader(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseService(t *testing.T) {
	if s, ok := ParseService("git-upload-pack"); !ok || s != UploadPack {
		t.Error("upload-pack")
	}
	if s, ok := ParseService("git-receive-pack"); !ok || s != ReceivePack {
		t.Error("receive-pack")
	}
	if _, ok := ParseService("git-foo"); ok {
		t.Error("git-foo accepted")
	}
}

func TestAdvertisementShape(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	rec := httptest.NewRecorder()
	if err := r.AdvertiseRefs(t.Context(), dir, UploadPack, "", rec); err != nil {
		t.Fatal(err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-git-upload-pack-advertisement" {
		t.Errorf("content type %q", ct)
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Error("no Cache-Control")
	}
	if !strings.HasPrefix(rec.Body.String(), pkt("# service=git-upload-pack\n")+"0000") {
		t.Errorf("v0 body = %q", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	if err := r.AdvertiseRefs(t.Context(), dir, UploadPack, "version=2", rec); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "# service=") {
		t.Errorf("v2 body carries the service line: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "version 2") {
		t.Errorf("v2 body lacks capability advertisement: %q", rec.Body.String())
	}
}

func TestGzipRequestBody(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	srv, log := serveRepo(t, r, dir, 0)
	c := newClient(t)
	wd := filepath.Join(t.TempDir(), "w")
	c.mustGit(filepath.Dir(wd), "clone", srv.URL+"/repo.git", wd)
	commitFile(t, c, wd, "a.txt", "x")
	c.mustGit(wd, "push", "origin", "main")
	// Hand-built gzip v0 upload-pack request: want HEAD, done.
	head := c.mustGit(wd, "rev-parse", "HEAD")
	var raw bytes.Buffer
	_ = writePkt(&raw, "want "+head+" ofs-delta\n")
	raw.WriteString("0000")
	_ = writePkt(&raw, "done\n")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(raw.Bytes())
	zw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/repo.git/git-upload-pack", &gz)
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte("PACK")) {
		t.Fatalf("status %d, body %q", resp.StatusCode, body)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.errs) != 0 {
		t.Errorf("errors: %v", log.errs)
	}
}

// An RPC's POST refused with WriteRPCRefusal: a real git prints the message,
// for a fetch (protocol v0 and v2) and a push alike, and never as a bare
// HTTP failure or a lost advertisement line.
func TestRPCRefusalPrintsMessage(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	plain, _ := serveRepo(t, r, dir, 0)
	c := newClient(t)
	wd := filepath.Join(t.TempDir(), "w")
	c.mustGit(filepath.Dir(wd), "clone", plain.URL+"/repo.git", wd)
	commitFile(t, c, wd, "a.txt", "x")
	c.mustGit(wd, "push", "origin", "main")

	const msg = "this credential no longer opens this site"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			svc, _ := ParseService(req.URL.Query().Get("service"))
			_ = r.AdvertiseRefs(req.Context(), dir, svc, ProtocolFromHeader(req.Header.Get("Git-Protocol")), w)
			return
		}
		_, _ = io.Copy(io.Discard, req.Body)
		svc, _ := ParseService(filepath.Base(req.URL.Path))
		WriteRPCRefusal(w, svc, msg)
	}))
	defer srv.Close()
	url := srv.URL + "/repo.git"
	commitFile(t, c, wd, "b.txt", "y")
	for _, args := range [][]string{
		{"-c", "protocol.version=0", "clone", url, filepath.Join(t.TempDir(), "c0")},
		{"-c", "protocol.version=2", "clone", url, filepath.Join(t.TempDir(), "c2")},
		{"push", url, "HEAD:refs/heads/main"},
	} {
		out, err := c.git(wd, args...)
		if err == nil {
			t.Errorf("%v succeeded", args)
		}
		if !strings.Contains(out, msg) || strings.Contains(out, "bad band") || strings.Contains(out, "RPC failed") {
			t.Errorf("%v output does not carry the refusal:\n%s", args, out)
		}
	}
}
