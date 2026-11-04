package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// An RPC is answered only with git's own request type; anything else is a
// 415 before any credential is looked at. Every answer of the endpoint is
// marked never to be sniffed or rendered.
func TestGitRPCNeedsGitsContentType(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	cl.mustGit(t.TempDir(), "ls-remote", g.remote(edit, pw)) // creates the repository

	marked := func(what string, h http.Header) {
		t.Helper()
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox" {
			t.Errorf("%s: X-Content-Type-Options %q, Content-Security-Policy %q; want nosniff and sandbox",
				what, h.Get("X-Content-Type-Options"), h.Get("Content-Security-Policy"))
		}
	}
	do := func(method, path, ctype string, auth bool) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, g.url("/git/"+edit+".git"+path), strings.NewReader("0000"))
		if err != nil {
			t.Fatal(err)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		if auth {
			req.SetBasicAuth("drop", pw)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}

	for _, svc := range []string{"git-upload-pack", "git-receive-pack"} {
		other := "git-upload-pack"
		if svc == other {
			other = "git-receive-pack"
		}
		for _, ctype := range []string{"", "application/x-www-form-urlencoded", "text/plain", "multipart/form-data; boundary=x", "application/x-" + other + "-request", "application/x-" + svc + "-result"} {
			for _, auth := range []bool{false, true} {
				resp := do("POST", "/"+svc, ctype, auth)
				if resp.StatusCode != http.StatusUnsupportedMediaType {
					t.Errorf("%s with %q (auth %v) = %d, want 415", svc, ctype, auth, resp.StatusCode)
				}
				marked("415", resp.Header)
			}
		}
	}
	// The right type passes, with parameters too.
	if resp := do("POST", "/git-upload-pack", "application/x-git-upload-pack-request; charset=binary", true); resp.StatusCode != http.StatusOK {
		t.Errorf("upload-pack with its type = %d, want 200", resp.StatusCode)
	} else {
		marked("result", resp.Header)
	}
	marked("advertisement", do("GET", "/info/refs?service=git-upload-pack", "", true).Header)
	marked("401", do("GET", "/info/refs?service=git-upload-pack", "", false).Header)

	site := siteByEdit(t, e, edit)
	if end, err := e.st.BeginGitWrite(site, nil); err != nil {
		t.Fatal(err)
	} else {
		resp := do("GET", "/info/refs?service=git-receive-pack", "", true)
		end()
		marked("refusal", resp.Header)
	}
}
