package gitrepo

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bigCommit writes a commit holding one file of n random bytes straight
// into the bare repository dir, sets main to it and returns its sha.
func bigCommit(t *testing.T, r *Runner, dir string, n int) string {
	t.Helper()
	blob := make([]byte, n)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	run := func(stdin []byte, args ...string) string {
		t.Helper()
		cmd := r.command(context.Background(), dir, args...)
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		cmd.Stdin = bytes.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	sha := run(blob, "hash-object", "-w", "--stdin")
	tree := run([]byte("100644 blob "+sha+"\tbig.bin\n"), "mktree")
	commit := run(nil, "commit-tree", tree, "-m", "big")
	run(nil, "update-ref", "refs/heads/main", commit)
	return commit
}

// tinyBuffers keeps the server's socket send buffer small, so a client that
// stops reading blocks the server's writes after kilobytes, not megabytes.
type tinyBuffers struct{ net.Listener }

func (l tinyBuffers) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetWriteBuffer(4 << 10)
	}
	return c, err
}

// fetchServer serves dir's upload-pack and reports the end of every
// ServeRPC.
func fetchServer(t *testing.T, r *Runner, dir string) (addr string, ended <-chan error) {
	t.Helper()
	ch := make(chan error, 4)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, err := r.ServeRPC(req.Context(), dir, UploadPack, req.Body, w, RPCOptions{})
		ch <- err
	}))
	srv.Listener = tinyBuffers{srv.Listener}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), ch
}

// sendFetch asks for want over a raw connection with a small receive buffer
// and reads nothing of the answer itself.
func sendFetch(t *testing.T, addr, want string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4 << 10)
	}
	body := pkt("want "+want+"\n") + "0000" + pkt("done\n")
	fmt.Fprintf(conn, "POST /repo.git/git-upload-pack HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-git-upload-pack-request\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	return conn
}

func waitEnded(t *testing.T, ended <-chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-ended:
		return err
	case <-time.After(within):
		t.Fatalf("the fetch still runs after %v", within)
		return nil
	}
}

func slotFree(t *testing.T, r *Runner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := r.acquire(ctx)
	if err != nil {
		t.Fatalf("the process slot is still held: %v", err)
	}
	release()
}

func newDeadlineRunner(t *testing.T, idle, max time.Duration) (*Runner, string) {
	t.Helper()
	bin := NeedGit(t)
	r, err := New(Options{Bin: bin, StateDir: filepath.Join(t.TempDir(), "gitstate"), MaxProcesses: 1, ResponseIdle: idle, ResponseMax: max})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "git")
	if err := r.Init(dir); err != nil {
		t.Fatal(err)
	}
	return r, dir
}

// A client that stops reading a fetch's answer: the write that waits on it
// gives up after ResponseIdle, git is stopped and its slot is free.
func TestResponseIdleFreesTheSlot(t *testing.T) {
	r, dir := newDeadlineRunner(t, 300*time.Millisecond, time.Minute)
	want := bigCommit(t, r, dir, 16<<20)
	addr, ended := fetchServer(t, r, dir)
	start := time.Now()
	sendFetch(t, addr, want) // and never read
	err := waitEnded(t, ended, 15*time.Second)
	t.Logf("ended after %v: %v", time.Since(start), err)
	if err == nil {
		t.Error("a fetch nobody read reported success")
	}
	slotFree(t, r)
}

// A client that reads, but too slowly to finish within ResponseMax, is cut
// off at ResponseMax however recently it last made progress.
func TestResponseMaxBoundsASlowClient(t *testing.T) {
	r, dir := newDeadlineRunner(t, time.Minute, time.Second)
	want := bigCommit(t, r, dir, 16<<20)
	addr, ended := fetchServer(t, r, dir)
	start := time.Now()
	conn := sendFetch(t, addr, want)
	go func() {
		buf := make([]byte, 512)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	err := waitEnded(t, ended, 15*time.Second)
	took := time.Since(start)
	t.Logf("ended after %v: %v", took, err)
	if err == nil {
		t.Error("a fetch cut off at ResponseMax reported success")
	}
	if took < 900*time.Millisecond {
		t.Errorf("cut off after %v, before ResponseMax", took)
	}
	slotFree(t, r)
}

// A client that reads its answer gets all of it, and the connection keeps no
// write deadline for the next request on it.
func TestResponseDeadlinesLetAReaderFinish(t *testing.T) {
	r, dir := newDeadlineRunner(t, 2*time.Second, time.Minute)
	want := bigCommit(t, r, dir, 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/later" {
			time.Sleep(2500 * time.Millisecond) // past the last write's deadline
			io.WriteString(w, "still writable")
			return
		}
		if _, err := r.ServeRPC(req.Context(), dir, UploadPack, req.Body, w, RPCOptions{}); err != nil {
			t.Errorf("ServeRPC: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()
	body := pkt("want "+want+"\n") + "0000" + pkt("done\n")
	resp, err := client.Post(srv.URL+"/repo.git/git-upload-pack", "application/x-git-upload-pack-request", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || len(got) < 1<<20 {
		t.Fatalf("read %d bytes: %v", len(got), err)
	}
	// The same connection, reused: no deadline left over from the fetch.
	resp, err = client.Get(srv.URL + "/later")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "still writable" {
		t.Errorf("the next request on the connection got %q", b)
	}
}
