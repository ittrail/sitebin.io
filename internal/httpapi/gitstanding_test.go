package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/gitrepo"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// ---- Git and the admin standing ----
//
// An admin pushes with their own credential. And because a push holds no site
// lock, the standing that let it in is checked again from meta.json once it
// is registered (BeginGitWrite) and before git runs: a site that changed
// hands, a share that ended, an edit password that changed in between stop
// it with a refusal git prints — never a bare status.

// standingGoneText is part of the refusal of a push whose standing ended.
const standingGoneText = "no longer opens this site"

func gitSharing(t *testing.T) (*env, *sharingFake, *gitServer) {
	t.Helper()
	e := newEnv(t, nil)
	yes := true
	f := registerSharing(t, ext.CreateGrant{Trusted: true, Git: &yes})
	return e, f, newGitServer(t, e)
}

func TestAdminPushesOverGitWithOwnToken(t *testing.T) {
	e, f, g := gitSharing(t)
	edit, _, viewID := sharedSite(t, e, f)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	wd := cl.work()

	pushed(t, cl, wd, g.remote(edit, "sbp_admin"), "a.txt", []byte("a"))
	cl.commit(wd, "b.txt", []byte("b"))
	cl.mustGit(wd, "push", "-q", g.remote(edit, jwtAdmin), "HEAD:main")
	ps := gitPushes(t, e, site)
	if len(ps) != 2 {
		t.Fatalf("push entries = %+v, want two", ps)
	}
	for i, auth := range []string{provenance.AuthToken, provenance.AuthOAuth} {
		if p := ps[i]; p.Auth != auth || p.Account != "acct-2" || p.Role != provenance.RoleAdmin {
			t.Errorf("push %d = %+v, want the admin acct-2 by %s, role admin", i, p, auth)
		}
	}

	out := cl.failGit(wd, "ls-remote", g.remote(edit, "sbp_other"))
	wantOutput(t, out, "Authentication failed", "a stranger's token")

	past := time.Now().Add(-time.Minute)
	shareWith(t, e, viewID, store.SiteAdmin{Account: "acct-2", Until: &past})
	out = cl.failGit(wd, "ls-remote", g.remote(edit, "sbp_admin"))
	wantOutput(t, out, "Authentication failed", "a share that ended")
}

// updateOnDisk changes the site's meta.json through a site object of its
// own, as another request would: the caller's copy keeps what it read. It
// reports through t.Error, never t.Fatal: hooks run on a server goroutine.
func updateOnDisk(t *testing.T, e *env, viewID string, mutate func(*store.Meta)) {
	site, err := e.st.ByViewID(viewID)
	if err == nil {
		err = e.st.Update(site, func(m *store.Meta) error { mutate(m); return nil })
	}
	if err != nil {
		t.Errorf("update %s: %v", viewID, err)
	}
}

func TestGitPushRechecksStandingAfterRegistering(t *testing.T) {
	t.Run("owner transferred", func(t *testing.T) {
		e, f, g := gitSharing(t)
		edit, _, viewID := sharedSite(t, e, f)
		cl := newGitClient(t)
		wd := cl.work()
		remote := g.remote(edit, "sbp_owner")
		cl.mustGit(wd, "ls-remote", remote) // the repository exists

		// The site changes hands between the push's gate and its
		// registration: the extension's token lookup is that moment.
		var once sync.Once
		f.mu.Lock()
		f.onBearer = func(r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
				once.Do(func() { updateOnDisk(t, e, viewID, func(m *store.Meta) { m.OwnerAccountID = "acct-3" }) })
			}
		}
		f.mu.Unlock()
		cl.commit(wd, "a.txt", []byte("a"))
		out := cl.failGit(wd, "push", remote, "HEAD:main")
		wantOutput(t, out, standingGoneText, "a push whose owner changed hands")
		if strings.Contains(out, "unexpected disconnect") || strings.Contains(out, "bad band") {
			t.Errorf("the refusal is not one git reads:\n%s", out)
		}
		site := siteByEdit(t, e, edit)
		if refs := cl.stdoutGit(wd, "ls-remote", g.remote(edit, "sbp_other")); refs != "" { // acct-3 owns it now
			t.Errorf("refs after the refused push: %q", refs)
		}
		if ps := gitPushes(t, e, site); len(ps) != 0 {
			t.Errorf("a refused push was recorded: %+v", ps)
		}
	})

	t.Run("admin's share ended", func(t *testing.T) {
		e, f, g := gitSharing(t)
		edit, _, viewID := sharedSite(t, e, f)
		site := siteByEdit(t, e, edit)
		cl := newGitClient(t)
		wd := cl.work()
		sha := cl.commit(wd, "a.txt", []byte("a"))
		body := pktLine(zeroSHA+" "+sha+" refs/heads/main\x00report-status side-band-64k\n") + "0000" + string(packOf(t, cl, wd, sha))

		// The plan answer is the last thing the gate asks; the share ends
		// right after it.
		var once sync.Once
		f.mu.Lock()
		f.onAllowed = func(id string) {
			if id == "acct-2" {
				once.Do(func() { updateOnDisk(t, e, viewID, func(m *store.Meta) { m.Admins = nil }) })
			}
		}
		f.mu.Unlock()
		code, resp := postRPC(t, g, edit, "sbp_admin", "git-receive-pack", strings.NewReader(body), nil)
		if code != 200 || !strings.Contains(resp, standingGoneText) {
			t.Fatalf("push = %d %q, want a readable refusal", code, resp)
		}
		if gitrepo.Exists(site.GitDir()) {
			t.Error("the refused push created the repository: Init ran before the re-check")
		}
	})

	t.Run("edit password changed", func(t *testing.T) {
		e, f, _ := gitSharing(t)
		edit, pw, _ := sharedSite(t, e, f)
		stale := siteByEdit(t, e, edit)
		req := httptest.NewRequest("POST", "/git/"+edit+".git/git-receive-pack", strings.NewReader("0000"))
		req.SetBasicAuth("drop", pw)
		req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
		act, d := e.api.gitAuth(req, stale, true)
		if d != nil {
			t.Fatalf("gate: %+v", *d)
		}
		// A rotation (or a transfer, which sets a new password) lands before
		// the registration.
		setEditPassword(t, e, siteByEdit(t, e, edit), "another-password-0123456")

		rec := httptest.NewRecorder()
		e.api.gitPush(rec, req, stale, act)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), standingGoneText) {
			t.Fatalf("push = %d %q, want a readable refusal", rec.Code, rec.Body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/x-git-receive-pack-result" {
			t.Errorf("content type = %q, want git's result type", ct)
		}
		if gitrepo.Exists(stale.GitDir()) {
			t.Error("Init ran before the re-check")
		}
	})
}

// A read of a writable site without a repository creates it inside a brief
// registration; the standing is checked again there too, before the Init.
func TestGitInitRechecksStanding(t *testing.T) {
	for _, svc := range []string{"git-receive-pack", "git-upload-pack"} {
		t.Run(svc, func(t *testing.T) {
			e, f, _ := gitSharing(t)
			edit, _, viewID := sharedSite(t, e, f)
			site := siteByEdit(t, e, edit)
			var once sync.Once
			f.mu.Lock()
			f.onBearer = func(r *http.Request) {
				once.Do(func() { updateOnDisk(t, e, viewID, func(m *store.Meta) { m.OwnerAccountID = "acct-3" }) })
			}
			f.mu.Unlock()
			req := httptest.NewRequest("GET", "/git/"+edit+".git/info/refs?service="+svc, nil)
			req.SetBasicAuth("drop", "sbp_owner")
			w := e.public(t, req)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "ERR ") || !strings.Contains(w.Body.String(), standingGoneText) {
				t.Fatalf("info/refs = %d %q, want an advertisement carrying the refusal", w.Code, w.Body)
			}
			if gitrepo.Exists(site.GitDir()) {
				t.Error("the repository was created for a standing that ended")
			}
		})
	}
}
