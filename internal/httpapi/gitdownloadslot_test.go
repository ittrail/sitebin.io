package httpapi

import (
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

// A client that asks for a large file or ZIP download and never reads it does
// not hold the instance's only git process slot: each write of the download
// has the same idle bound as a smart HTTP response, git is stopped, and the
// next request is served.
func TestGitDownloadNobodyReadsFreesTheSlot(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"blob", "/blob?ref=main&path=big.bin&download=1"},
		{"archive", "/archive?ref=main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			pushed(t, cl, cl.work(), remote, "big.bin", randomBytes(t, 16<<20))

			ended := make(chan struct{}, 1)
			h := e.api.Public()
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				req.Host = e.cfg.BaseDomain
				h.ServeHTTP(w, req)
				if strings.Contains(req.URL.Path, "/git/") && !strings.HasSuffix(req.URL.Path, "/git") {
					select {
					case ended <- struct{}{}:
					default:
					}
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
			fmt.Fprintf(conn, "GET /api/sites/%s/git%s HTTP/1.1\r\nHost: x\r\nX-Edit-Password: %s\r\n\r\n", edit, tc.path, pw)
			// ... and read nothing.
			select {
			case <-ended:
			case <-time.After(20 * time.Second):
				t.Fatal("the download nobody reads still holds its slot")
			}
			// The only slot is free (with it held this would be "instance busy").
			cl.mustGit(t.TempDir(), "ls-remote", remote)
		})
	}
}
