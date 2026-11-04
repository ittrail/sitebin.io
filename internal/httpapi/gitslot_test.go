package httpapi

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/gitrepo"
)

// tinyWriteBuffers keeps the server's send buffer small, so a client that
// reads nothing blocks the server's writes after kilobytes.
type tinyWriteBuffers struct{ net.Listener }

func (l tinyWriteBuffers) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetWriteBuffer(4 << 10)
	}
	return c, err
}

// A client that asks for a large fetch and never reads the answer does not
// hold the instance's only git process slot: the response's write deadline
// stops git, and the next request is served.
func TestGitFetchNobodyReadsFreesTheSlot(t *testing.T) {
	e := newEnv(t, nil)
	r, err := gitrepo.New(gitrepo.Options{
		StateDir:     filepath.Join(e.cfg.DataDir, "gitstate"),
		MaxProcesses: 1,
		ResponseIdle: 300 * time.Millisecond,
	})
	if err != nil {
		t.Skipf("no git: %v", err)
	}
	e.api.git = r
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	remote := g.remote(edit, pw)
	sha := pushed(t, cl, cl.work(), remote, "big.bin", randomBytes(t, 16<<20))

	// A server of its own around the same API, with small socket buffers,
	// telling when the fetch's handler is done.
	ended := make(chan struct{}, 1)
	h := e.api.Public()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		req.Host = e.cfg.BaseDomain
		h.ServeHTTP(w, req)
		if strings.HasSuffix(req.URL.Path, "/git-upload-pack") {
			ended <- struct{}{}
		}
	}))
	srv.Listener = tinyWriteBuffers{srv.Listener}
	srv.Start()
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4 << 10)
	}
	reqBody := pktLine("want "+sha+"\n") + "0000" + pktLine("done\n")
	fmt.Fprintf(conn, "POST /git/%s.git/git-upload-pack HTTP/1.1\r\nHost: x\r\nAuthorization: Basic %s\r\nContent-Type: application/x-git-upload-pack-request\r\nContent-Length: %d\r\n\r\n%s",
		edit, base64.StdEncoding.EncodeToString([]byte("drop:"+pw)), len(reqBody), reqBody)
	// ... and read nothing.
	select {
	case <-ended:
	case <-time.After(15 * time.Second):
		t.Fatal("the fetch nobody reads still holds its slot")
	}
	// The only slot is free: a clone goes through (with the slot held it
	// would get "instance busy").
	cl.mustGit(t.TempDir(), "ls-remote", remote)
}
