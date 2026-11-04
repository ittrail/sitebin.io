package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/gitrepo"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The JSON routes of a site's repository, for the edit page's Git tab:
//
//	GET    /api/sites/{editID}/git                 state, address, size, last push
//	GET    /api/sites/{editID}/git/refs            branches and tags
//	GET    /api/sites/{editID}/git/log             one page of history
//	GET    /api/sites/{editID}/git/commit/{sha}    one commit
//	GET    /api/sites/{editID}/git/tree            one folder
//	GET    /api/sites/{editID}/git/blob            one file (or its download)
//	GET    /api/sites/{editID}/git/archive         the commit as a ZIP
//	DELETE /api/sites/{editID}/git                 "Delete repository"
//
// All of them go through withEditAuth, like every per-site route: the edit
// password, an account API token or the owner's session. An OAuth token is
// refused there; it acts on /mcp and /git/ only.

const (
	msgGitNotFound = "not found"
	msgGitReadBusy = "busy, try again in a moment"
	msgGitInternal = "could not read the repository"
)

type gitLastPush struct {
	At     time.Time `json:"at"`
	Detail string    `json:"detail,omitempty"`
	Auth   string    `json:"auth,omitempty"`
}

type gitCredentials struct {
	Password bool `json:"password"`
	Token    bool `json:"token"`
	OAuth    any  `json:"oauth"`
}

type gitInfoPayload struct {
	Available     bool           `json:"available"`
	Writable      bool           `json:"writable"`
	Reason        string         `json:"reason"`
	URL           string         `json:"url"`
	Exists        bool           `json:"exists"`
	Bytes         int64          `json:"bytes"`
	DefaultBranch string         `json:"default_branch"`
	LastPush      *gitLastPush   `json:"last_push"`
	ExpiresAt     *time.Time     `json:"expires_at"`
	Credentials   gitCredentials `json:"credentials"`
	// Role is the caller's, as on the site payload (owner, admin or
	// password): the Git tab offers "Delete repository" only where it is
	// not refused (an admin's).
	Role string `json:"role,omitempty"`
}

// gitOAuthCredentials is what the info route says about signing in with
// OAuth; nil (JSON null) unless the sign-in is offered.
func (a *API) gitOAuthCredentials(ctx context.Context) any {
	if info := a.gitOAuthInfo(ctx); info != nil {
		return info
	}
	return nil
}

// gitReadError answers a failed read without echoing git's message, which
// may carry filesystem paths.
func (a *API) gitReadError(w http.ResponseWriter, site *store.Site, err error) {
	switch {
	case errors.Is(err, gitrepo.ErrBadRef), errors.Is(err, gitrepo.ErrNotFound):
		writeError(w, 404, msgGitNotFound)
	case errors.Is(err, gitrepo.ErrBusy):
		writeError(w, 503, msgGitReadBusy)
	default:
		a.log.Error("git: read failed", slog.String("id", site.ViewID), slog.Any("err", err))
		writeError(w, 500, msgGitInternal)
	}
}

// gitReadable answers 404 and reports false when the site has no repository
// to read.
func (a *API) gitReadable(w http.ResponseWriter, site *store.Site) bool {
	if a.git == nil || !a.gitStateOf(site).Available || !gitrepo.Exists(site.GitDir()) {
		writeError(w, 404, msgGitNotFound)
		return false
	}
	return true
}

func (a *API) gitInfo(w http.ResponseWriter, r *http.Request, site *store.Site) {
	st := a.gitStateOf(site)
	out := gitInfoPayload{
		Available: st.Available,
		Writable:  st.Writable,
		Reason:    st.Reason,
		URL:       a.baseURL() + "/git/" + r.PathValue("editID") + ".git",
		Credentials: gitCredentials{
			Password: true,
			Token:    a.accountsEnabled(),
			OAuth:    a.gitOAuthCredentials(r.Context()),
		},
		ExpiresAt: site.Meta.ExpiresAt,
		Role:      a.actorOf(r).role,
	}
	dir := site.GitDir()
	if a.git != nil && gitrepo.Exists(dir) {
		out.Exists = true
		if u, err := a.st.SiteUsage(site); err == nil {
			out.Bytes = u.GitBytes
		} else {
			a.log.Warn("git: could not measure the repository", "id", site.ViewID, "err", err)
		}
		if b, err := a.git.DefaultBranch(r.Context(), dir); err == nil {
			out.DefaultBranch = b
		} else if errors.Is(err, gitrepo.ErrBusy) {
			writeError(w, 503, msgGitReadBusy)
			return
		} else {
			a.log.Warn("git: could not read the default branch", "id", site.ViewID, "err", err)
		}
		if at, ok := gitrepo.LastPush(dir); ok {
			// The marker is for the sweep: a push cut off after git started
			// moves it too, though it changed nothing. The time shown is the
			// recorded push's, beside its own refs; the marker's only when no
			// record is left (retention) or readable.
			lp := &gitLastPush{At: at}
			if es, err := a.st.Provenance(site); err == nil {
				for i := len(es) - 1; i >= 0; i-- {
					if es[i].Action == provenance.ActionGitPush {
						lp.At, lp.Detail, lp.Auth = es[i].Time, es[i].Detail, es[i].Auth
						break
					}
				}
			}
			out.LastPush = lp
		}
	}
	writeJSON(w, 200, out)
}

func (a *API) gitRefs(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if !a.gitReadable(w, site) {
		return
	}
	refs, err := a.git.Refs(r.Context(), site.GitDir())
	if err != nil {
		a.gitReadError(w, site, err)
		return
	}
	writeJSON(w, 200, map[string]any{"refs": refs})
}

// resolveRef is the ref query parameter as a commit sha; the default is the
// default branch. An empty repository has no commit to resolve: not found.
func (a *API) resolveRef(r *http.Request, site *store.Site) (string, error) {
	dir := site.GitDir()
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		b, err := a.git.DefaultBranch(r.Context(), dir)
		if err != nil {
			return "", err
		}
		ref = b
	}
	return a.git.Resolve(r.Context(), dir, ref)
}

func (a *API) gitLog(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if !a.gitReadable(w, site) {
		return
	}
	skip := 0
	if v := r.URL.Query().Get("skip"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, 400, "bad skip")
			return
		}
		skip = n
	}
	sha, err := a.resolveRef(r, site)
	if err != nil {
		a.gitReadError(w, site, err)
		return
	}
	dir := site.GitDir()
	commits, err := a.git.Log(r.Context(), dir, sha, skip, gitrepo.LogPage)
	if err != nil {
		a.gitReadError(w, site, err)
		return
	}
	var next *int
	if len(commits) == gitrepo.LogPage {
		more, err := a.git.Log(r.Context(), dir, sha, skip+len(commits), 1)
		if err != nil {
			a.gitReadError(w, site, err)
			return
		}
		if len(more) > 0 {
			n := skip + len(commits)
			next = &n
		}
	}
	writeJSON(w, 200, map[string]any{"sha": sha, "commits": commits, "next_skip": next})
}

func (a *API) gitCommit(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if !a.gitReadable(w, site) {
		return
	}
	d, err := a.git.Commit(r.Context(), site.GitDir(), r.PathValue("sha"))
	if err != nil {
		a.gitReadError(w, site, err)
		return
	}
	writeJSON(w, 200, d)
}

func (a *API) gitTree(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if !a.gitReadable(w, site) {
		return
	}
	sha, err := a.resolveRef(r, site)
	if err != nil {
		a.gitReadError(w, site, err)
		return
	}
	t, err := a.git.Tree(r.Context(), site.GitDir(), sha, r.URL.Query().Get("path"))
	if err != nil {
		a.gitReadError(w, site, err)
		return
	}
	writeJSON(w, 200, struct {
		gitrepo.Tree
		SHA string `json:"sha"`
	}{t, sha})
}

func (a *API) gitBlob(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if !a.gitReadable(w, site) {
		return
	}
	sha, err := a.resolveRef(r, site)
	if err != nil {
		a.gitReadError(w, site, err)
		return
	}
	p := r.URL.Query().Get("path")
	dir := site.GitDir()
	if r.URL.Query().Get("download") != "1" {
		b, err := a.git.Blob(r.Context(), dir, sha, p)
		if err != nil {
			a.gitReadError(w, site, err)
			return
		}
		writeJSON(w, 200, struct {
			gitrepo.Blob
			SHA string `json:"sha"`
		}{b, sha})
		return
	}
	dl := &gitDownload{w: w, name: path.Base(p)}
	if err := a.git.WriteBlob(r.Context(), dir, sha, p, dl); err != nil {
		a.gitDownloadError(dl, site, err)
		return
	}
	dl.finish()
}

func (a *API) gitArchive(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if !a.gitReadable(w, site) {
		return
	}
	sha, err := a.resolveRef(r, site)
	if err != nil {
		a.gitReadError(w, site, err)
		return
	}
	dl := &gitDownload{w: w, name: site.ViewID + "-" + sha[:7] + ".zip"}
	if err := a.git.Archive(r.Context(), site.GitDir(), sha, dl); err != nil {
		a.gitDownloadError(dl, site, err)
		return
	}
	dl.finish()
}

// gitDownloadError answers a failed download with JSON when nothing has been
// sent yet; once the body has started there is only the log.
func (a *API) gitDownloadError(dl *gitDownload, site *store.Site, err error) {
	if dl.started {
		a.log.Warn("git: download cut off", "id", site.ViewID, "err", err)
		return
	}
	a.gitReadError(dl.w, site, err)
}

// gitDownload is the response writer of a download: its headers go out with
// the first byte, so a failure before it can still be an error answer.
type gitDownload struct {
	w       http.ResponseWriter
	name    string
	started bool
}

func (d *gitDownload) start() {
	if d.started {
		return
	}
	d.started = true
	h := d.w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", attachmentDisposition(d.name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "no-store")
	d.w.WriteHeader(200)
}

func (d *gitDownload) Write(p []byte) (int, error) {
	d.start()
	return d.w.Write(p)
}

// finish completes a download that succeeded: an empty file never called
// Write, and still gets its headers.
func (d *gitDownload) finish() { d.start() }

// SetWriteDeadline lets gitrepo bound each write of the download, so a client
// that stops reading frees its git process slot.
func (d *gitDownload) SetWriteDeadline(t time.Time) error {
	return http.NewResponseController(d.w).SetWriteDeadline(t)
}

var _ gitrepo.WriteDeadliner = (*gitDownload)(nil)

// attachmentDisposition is a Content-Disposition value for name that cannot
// break out of the header: control characters and separators become "_",
// and a non-ASCII name travels as filename*=UTF-8”....
func attachmentDisposition(name string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == 0x7f, r == '"', r == '\\', r == '/', r == ';':
			return '_'
		}
		return r
	}, name)
	if clean == "" || clean == "." || clean == ".." {
		clean = "download"
	}
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": clean}); v != "" {
		return v
	}
	return "attachment"
}

func (a *API) deleteGitRepo(w http.ResponseWriter, r *http.Request, site *store.Site) {
	// "Delete repository" is the owner's: an admin is refused (the edit
	// password, the owner's credential, is not).
	if a.refuseUnlessOwner(w, r) {
		return
	}
	if err := a.st.DeleteGitRepo(site); err != nil {
		storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
