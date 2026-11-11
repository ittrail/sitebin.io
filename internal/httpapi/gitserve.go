package httpapi

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ittrail/sitebin.io/internal/gitrepo"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The Git endpoint: every site answers as a Git remote at
// https://<base-domain>/git/<edit-id>.git, smart HTTP only — GET
// info/refs?service=…, POST git-upload-pack, POST git-receive-pack. Sitebin
// keeps authentication, quota, the push registry and provenance; git only
// speaks the protocol (internal/gitrepo). See
// docs/superpowers/specs/2026-11-03-site-git-design.md.
//
// Git prints nothing useful for a bare 4xx on a POST, and reads an ERR line
// only from an advertisement. So a refusal after authentication is answered
// on info/refs — the first request of every clone, fetch and push — as a
// readable refusal (gitrepo.WriteRefusal), which Git prints as
// "fatal: remote error: <message>". The RPCs repeat every check, because the
// site may have changed in between, and answer a refusal there with a plain
// status: they have no other way to.

// Request body bounds. A fetch's body is its want and have lines — kilobytes
// in practice — and is bounded on the wire and again once decompressed, so a
// small gzip body cannot unfold into gigabytes of have lines. A push's body
// is bounded at its allowance plus room for the command section, which
// gitrepo caps at 1 MiB; git itself refuses a pack over the allowance
// (receive.maxInputSize) first.
//
// The bounds are boundedBody, never http.MaxBytesReader: the body is read on
// ServeRPC's stdin goroutine, beside git's output going to the client, and
// MaxBytesReader past its limit writes to the server's ResponseWriter (its
// header map, its close flag) from there — a concurrent map write can end
// the process. Closing the connection is gitBody.stop's, on the handler's
// goroutine, once every read has stopped.
const (
	gitFetchBodyMax = 10 << 20
	gitPushSlack    = 2 << 20
)

// Refusals. The placeholders are the instance's account page
// (apiAccountHint), where a plan is changed.
const (
	msgGitDisabled        = "Git is turned off on this instance"
	msgGitUnavailableHere = "Git is not available on this instance"
	msgGitBusy            = "instance busy, try again in a minute"
	msgGitAnonymous       = "this site was created without an account, so it has no Git repository; create it while signed in at %s to use Git"
	msgGitPlan            = "this site's plan does not include Git; upgrade at %s to use it"
	msgGitReadOnly        = "this site's plan does not include Git, so its repository is read-only; upgrade at %s to push again"
	msgGitStopped         = "the push was stopped: the site was locked or deleted"
	msgGitRepoBusy        = "the repository is busy, try again in a moment"
	msgGitTooLarge        = "request too large"
	msgGitBadRequest      = "bad git request"
	msgGitContentType     = "not a git request"
	// msgGitStandingGone: the credential's standing on the site ended
	// between the gate and the push's registration (gitStandingHolds).
	msgGitStandingGone = "this credential no longer opens this site: it changed hands, its share ended or its edit password changed; sign in again or ask its owner"
)

// errGitNoRepo: a read of a site whose repository does not exist and may not
// be created. The state check refuses it first; this is the backstop.
var errGitNoRepo = errors.New("this site has no Git repository")

// errGitStandingGone: gitStandingHolds found the credential's standing gone.
var errGitStandingGone = errors.New(msgGitStandingGone)

// serveGit answers every Git request, in this order: the host, the site, the
// service and an RPC's content type, the credential (gitAuth), the site's
// state (gitStateOf), then the request itself.
func (a *API) serveGit(w http.ResponseWriter, r *http.Request) {
	// Nothing this endpoint answers — an advertisement, git's result, a
	// refusal, an error — is ever to be sniffed or rendered as a page.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	// The base domain only. A site's own address, a custom domain, the view
	// domain: those are user-content origins, never the place for this.
	if !strings.EqualFold(hostWithoutPort(r.Host), hostWithoutPort(a.cfg.BaseDomain)) {
		http.NotFound(w, r)
		return
	}
	editID, ok := strings.CutSuffix(r.PathValue("repo"), ".git")
	if !ok {
		http.NotFound(w, r)
		return
	}
	site, err := a.st.ByEditID(editID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.log.Error("git: load site", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
		return
	}

	info := r.Method == http.MethodGet
	name := path.Base(r.URL.Path)
	if info {
		name = r.URL.Query().Get("service")
	}
	svc, ok := gitrepo.ParseService(name)
	if !ok {
		http.Error(w, "unknown git service", http.StatusBadRequest)
		return
	}
	// An RPC carries git's own request type, as every Git client sends it.
	// A page's form cannot send this type, and a page's fetch() only after
	// a CORS preflight, which this endpoint never answers.
	if !info && !gitRequestType(r, svc) {
		http.Error(w, msgGitContentType, http.StatusUnsupportedMediaType)
		return
	}
	write := svc == gitrepo.ReceivePack

	act, d := a.gitAuth(r, site, write)
	if d != nil {
		writeGitDenial(w, svc, info, d)
		return
	}

	st := a.gitStateOf(site)
	if msg := a.gitStateRefusal(site, st, write); msg != "" {
		gitRefuse(w, svc, info, http.StatusForbidden, msg)
		return
	}

	switch {
	case info:
		a.gitAdvertise(w, r, site, svc, st, act)
	case write:
		a.gitPush(w, r, site, act)
	default:
		a.gitFetch(w, r, site, st, act)
	}
}

// gitRequestType reports whether r carries the content type of an RPC of
// svc: application/x-git-upload-pack-request or
// application/x-git-receive-pack-request.
func gitRequestType(r *http.Request, svc gitrepo.Service) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/x-"+string(svc)+"-request"
}

// writeGitDenial answers what gitAuth refused. A refusal is a 200 carrying
// an ERR line, so it is told apart before the status is used.
func writeGitDenial(w http.ResponseWriter, svc gitrepo.Service, info bool, d *gitDenial) {
	switch {
	case d.challenge:
		w.Header().Set("WWW-Authenticate", gitBasicChallenge)
		http.Error(w, d.msg, http.StatusUnauthorized)
	case d.refusal:
		gitRefuse(w, svc, info, http.StatusForbidden, d.msg)
	default:
		http.Error(w, d.msg, d.status)
	}
}

// gitRefuse answers a refusal: readable on info/refs, a plain status with
// the message as its body on an RPC.
func gitRefuse(w http.ResponseWriter, svc gitrepo.Service, info bool, status int, msg string) {
	if info {
		gitrepo.WriteRefusal(w, svc, msg)
		return
	}
	http.Error(w, msg, status)
}

// gitRefuseReadable answers a refusal git prints on every request: an
// advertisement's ERR line on info/refs, the RPC's own error shape on a POST
// (gitrepo.WriteRPCRefusal) — never a bare status, which git shows as "RPC
// failed" and "unexpected disconnect".
func gitRefuseReadable(w http.ResponseWriter, svc gitrepo.Service, info bool, msg string) {
	if info {
		gitrepo.WriteRefusal(w, svc, msg)
		return
	}
	gitrepo.WriteRPCRefusal(w, svc, msg)
}

// gitStandingHolds checks, inside a push registration (BeginGitWrite), that
// the standing act was let in with on site — the site as gitAuth read it —
// still holds in meta.json as it is now:
//   - an owner: the site still has that owner;
//   - an admin: the site has the same owner, and still lists the admin with
//     no end date or one ahead (store.Meta.LiveAdmin);
//   - the edit password: its hash has not changed (a rotation, a transfer).
//
// A push takes no site lock, so a transfer, a share ending or a rotation
// that lands between the gate and the registration would otherwise let it
// write into a site its credential no longer opens; one that lands after
// the registration stops the push itself (stopGitWrite). It reads meta.json
// only: the plan (SharingAllowed) was asked in gitAuth just before, and a
// delete or a lock waiting on this registration must never wait on a plan
// lookup. Called before Init and before git runs.
func (a *API) gitStandingHolds(site *store.Site, act actor) error {
	fresh, err := a.st.ByViewID(site.ViewID)
	if err != nil {
		return err
	}
	was, now := site.Meta, fresh.Meta
	var holds bool
	switch act.role {
	case roleOwner:
		holds = act.account != "" && now.OwnerAccountID == act.account
	case roleAdmin:
		holds = now.OwnerAccountID == was.OwnerAccountID && now.LiveAdmin(act.account, time.Now())
	case rolePassword:
		holds = now.EditPasswordHash == was.EditPasswordHash
	}
	if !holds {
		return errGitStandingGone
	}
	return nil
}

// gitStateRefusal is why st does not allow the request, or "". A plan
// without Git still reads an existing repository; it never pushes or
// creates one.
func (a *API) gitStateRefusal(site *store.Site, st gitState, write bool) string {
	if st.Writable || (st.Available && !write) {
		return ""
	}
	hint := a.apiAccountHint()
	switch st.Reason {
	case gitReasonDisabled:
		return msgGitDisabled
	case gitReasonAnonymous:
		return fmt.Sprintf(msgGitAnonymous, hint)
	case gitReasonLocked:
		return store.LockedMessage(site.Meta.Locked)
	case gitReasonPlan:
		if st.Available {
			return fmt.Sprintf(msgGitReadOnly, hint)
		}
		return fmt.Sprintf(msgGitPlan, hint)
	}
	return msgGitUnavailableHere
}

// gitAllowance is how many bytes a push to site may bring: what is left of
// its byte cap (files and repository share it), at most the instance's per
// push ceiling. refusal is set when nothing is left: git never runs for it.
func (a *API) gitAllowance(site *store.Site) (allowance int64, refusal string, err error) {
	u, err := a.st.SiteUsage(site)
	if err != nil {
		return 0, "", err
	}
	limit, account := a.st.ByteCap(site) // the owner's account may leave less
	allowance = min(limit-u.Bytes(), a.cfg.GitMaxPushBytes)
	if allowance <= 0 {
		if account {
			cap, others := a.st.AccountUsage(site)
			return 0, fmt.Sprintf("storage full: your sites hold %s of the %s your plan allows in total", store.HumanBytes(others+u.Bytes()), store.HumanBytes(cap)), nil
		}
		return 0, fmt.Sprintf("storage full: %s of %s used", store.HumanBytes(u.Bytes()), store.HumanBytes(limit)), nil
	}
	return allowance, "", nil
}

// gitRepoReady makes sure site's repository exists before a read serves it.
// A missing one is created only where the site may write, and only inside a
// push registration (BeginGitWrite), so a delete or a lock that lands at
// the same moment cancels it or is seen by it — never leaves an orphaned
// git/ behind. Inside it, the standing act was let in with is checked again
// before the Init (gitStandingHolds). The registration ends with the Init:
// the read itself is served outside it, beside any push.
func (a *API) gitRepoReady(site *store.Site, writable bool, act actor) error {
	dir := site.GitDir()
	if gitrepo.Exists(dir) {
		return nil
	}
	if !writable {
		return errGitNoRepo
	}
	end, err := a.st.BeginGitWrite(site, nil)
	if err != nil {
		// A push is running: it created the repository, or is about to.
		if errors.Is(err, store.ErrGitBusy) && gitrepo.Exists(dir) {
			return nil
		}
		return err
	}
	defer end()
	if err := a.gitStandingHolds(site, act); err != nil {
		return err
	}
	return a.git.Init(dir)
}

// gitFail answers err, which ended a request before git wrote anything.
func (a *API) gitFail(w http.ResponseWriter, r *http.Request, site *store.Site, svc gitrepo.Service, info bool, err error) {
	switch {
	case errors.Is(err, gitrepo.ErrBusy):
		gitRefuse(w, svc, info, http.StatusServiceUnavailable, msgGitBusy)
	case errors.Is(err, store.ErrGitBusy):
		// The registration is held — by a push, a first clone creating the
		// repository or the sweep; this request cannot tell which.
		gitRefuse(w, svc, info, http.StatusConflict, msgGitRepoBusy)
	case errors.Is(err, store.ErrLocked):
		// Locked after this request was let in.
		gitRefuse(w, svc, info, http.StatusForbidden, store.LockedMessage(nil))
	case errors.Is(err, errGitNoRepo):
		gitRefuse(w, svc, info, http.StatusNotFound, errGitNoRepo.Error())
	case errors.Is(err, errGitStandingGone):
		gitRefuseReadable(w, svc, info, msgGitStandingGone)
	case errors.Is(err, store.ErrNotFound):
		// Deleted after this request was let in.
		http.NotFound(w, r)
	case gitClientFault(err):
		a.log.Warn("git: bad request", "id", site.ViewID, "service", svc, "err", err)
		http.Error(w, msgGitBadRequest, http.StatusBadRequest)
	default:
		a.log.Error("git: request failed", "id", site.ViewID, "service", svc, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// gitClientFault tells a git failure the request caused from one that is the
// server's (a damaged repository, a full disk): ServeRPC's own refusal of a
// malformed request, or git's words for input that ended early or broke the
// protocol. Anything else is the server's — a 500, and an error in the log.
func gitClientFault(err error) bool {
	if errors.Is(err, gitrepo.ErrBadRequest) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"the remote end hung up unexpectedly", "protocol error"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// gitAdvertise answers GET info/refs. AdvertiseRefs writes the service line
// before git starts, so it is called only once the repository exists: a
// missing one is a refusal, never a truncated 200.
func (a *API) gitAdvertise(w http.ResponseWriter, r *http.Request, site *store.Site, svc gitrepo.Service, st gitState, act actor) {
	if svc == gitrepo.ReceivePack {
		// A push beside a registration is told so here: on its POST, the
		// 409 that BeginGitWrite answers is all Git would show. Only a look —
		// the POST's registration stays the guarantee. Who holds it (a push,
		// a first clone creating the repository, the sweep's collection) is
		// not known, so the message names none of them.
		if a.st.GitWriteRunning(site) {
			gitrepo.WriteRefusal(w, svc, msgGitRepoBusy)
			return
		}
		// A full site is told so before the push sends its pack, and
		// before git runs at all.
		_, refusal, err := a.gitAllowance(site)
		if err != nil {
			a.gitFail(w, r, site, svc, true, err)
			return
		}
		if refusal != "" {
			gitrepo.WriteRefusal(w, svc, refusal)
			return
		}
	}
	if err := a.gitRepoReady(site, st.Writable, act); err != nil {
		a.gitFail(w, r, site, svc, true, err)
		return
	}
	tw := &gitWriter{ResponseWriter: w}
	err := a.git.AdvertiseRefs(r.Context(), site.GitDir(), svc, gitrepo.ProtocolFromHeader(r.Header.Get("Git-Protocol")), tw)
	if err == nil {
		return
	}
	if !tw.wrote.Load() {
		a.gitFail(w, r, site, svc, true, err)
		return
	}
	if r.Context().Err() == nil {
		a.log.Warn("git: advertisement failed", "id", site.ViewID, "service", svc, "err", err)
	}
}

// gitFetch answers POST git-upload-pack. A fetch only reads: it runs beside
// a push, outside any registration.
func (a *API) gitFetch(w http.ResponseWriter, r *http.Request, site *store.Site, st gitState, act actor) {
	svc := gitrepo.UploadPack
	if err := a.gitRepoReady(site, st.Writable, act); err != nil {
		a.gitFail(w, r, site, svc, false, err)
		return
	}
	body, err := fetchBody(r)
	if err != nil {
		http.Error(w, msgGitBadRequest, http.StatusBadRequest)
		return
	}
	guard := newGitBody(body)
	defer guard.stop(w)
	tw := &gitWriter{ResponseWriter: w}
	_, err = a.git.ServeRPC(r.Context(), site.GitDir(), svc, guard, tw, gitrepo.RPCOptions{
		Protocol: gitrepo.ProtocolFromHeader(r.Header.Get("Git-Protocol")),
	})
	if err == nil || tw.wrote.Load() || r.Context().Err() != nil {
		return
	}
	switch {
	case body.over.Load():
		http.Error(w, msgGitTooLarge, http.StatusRequestEntityTooLarge)
	case body.failed.Load():
		http.Error(w, msgGitBadRequest, http.StatusBadRequest)
	default:
		a.gitFail(w, r, site, svc, false, err)
	}
}

// fetchBody is a fetch's body as git reads it: bounded on the wire and,
// when gzip, decoded here and bounded again, so the bound applies to what
// git reads and not just to what crossed the wire. A bound passed on either
// side shows as the returned reader's over. It is given no ResponseWriter,
// and must never be: see the bounds above.
func fetchBody(r *http.Request) (*boundedBody, error) {
	wire := &boundedBody{r: r.Body, left: gitFetchBodyMax}
	if !isGzip(r) {
		return wire, nil
	}
	zr, err := gzip.NewReader(wire)
	if err != nil {
		return nil, err
	}
	return &boundedBody{r: zr, left: gitFetchBodyMax}, nil
}

// gitPush answers POST git-receive-pack. It holds the site's push
// registration from before the repository is created (if it must be)
// through git's run to the bookkeeping that follows, and takes no site lock
// in between: a delete or a lock waits for the registration's end, and
// cancels ctx to get it. Once registered, and before the Init and git, it
// checks that the standing act was let in with still holds
// (gitStandingHolds).
func (a *API) gitPush(w http.ResponseWriter, r *http.Request, site *store.Site, act actor) {
	svc := gitrepo.ReceivePack
	allowance, refusal, err := a.gitAllowance(site)
	if err != nil {
		a.gitFail(w, r, site, svc, false, err)
		return
	}
	if refusal != "" {
		http.Error(w, refusal, http.StatusInsufficientStorage)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// The bookkeeping after git outlives the request — a client may hang up
	// as soon as it has git's report — but not the registration: a delete
	// or a lock cancels it with the push. Its deadline starts once git is
	// done (recordPush).
	bctx, bcancel := context.WithCancel(context.Background())
	defer bcancel()
	end, err := a.st.BeginGitWrite(site, func() { cancel(); bcancel() })
	if err != nil {
		a.gitFail(w, r, site, svc, false, err)
		return
	}
	defer end()
	if err := a.gitStandingHolds(site, act); err != nil {
		a.gitFail(w, r, site, svc, false, err)
		return
	}

	dir := site.GitDir()
	if err := a.git.Init(dir); err != nil {
		a.gitFail(w, r, site, svc, false, err)
		return
	}
	before, err := a.git.RefTargets(ctx, dir)
	if err != nil {
		a.gitFail(w, r, site, svc, false, err)
		return
	}

	bodyMax := allowance + gitPushSlack
	if bodyMax < allowance { // an allowance near the int64 limit
		bodyMax = math.MaxInt64
	}
	wire := &boundedBody{r: r.Body, left: bodyMax}
	guard := newGitBody(wire)
	defer guard.stop(w)
	tw := &gitWriter{ResponseWriter: w}
	res, err := a.git.ServeRPC(ctx, dir, svc, guard, tw, gitrepo.RPCOptions{
		Protocol:     gitrepo.ProtocolFromHeader(r.Header.Get("Git-Protocol")),
		Gzip:         isGzip(r),
		MaxInputSize: allowance,
	})
	if err != nil && !tw.wrote.Load() && r.Context().Err() == nil {
		switch {
		case ctx.Err() != nil:
			// The registry cancelled it: a delete or a lock.
			http.Error(w, msgGitStopped, http.StatusConflict)
		case wire.over.Load():
			http.Error(w, msgGitTooLarge, http.StatusRequestEntityTooLarge)
		case wire.failed.Load():
			http.Error(w, msgGitBadRequest, http.StatusBadRequest)
		default:
			a.gitFail(w, r, site, svc, false, err)
		}
	}
	// A git that did not end cleanly (killed, cancelled, cut off) may have
	// moved objects in that no ref reaches: marked, the repository gets the
	// sweep's next collection. That sweep also removes what a killed git
	// leaves in the object store (cleanup.CollectGit).
	if err != nil && res.Started {
		a.markPushed(site)
	}
	// What the push changed is recorded whatever git's exit status, and
	// before the registration ends: a delete waits for it.
	a.recordPush(bctx, r, site, act, before, res, err == nil)
}

func isGzip(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip")
}

// gitWriter notes whether anything reached the client: an error from git
// becomes a status only while nothing has. Atomic, because git's output is
// copied from its own goroutine.
type gitWriter struct {
	http.ResponseWriter
	wrote atomic.Bool
}

func (g *gitWriter) WriteHeader(code int) {
	g.wrote.Store(true)
	g.ResponseWriter.WriteHeader(code)
}

func (g *gitWriter) Write(p []byte) (int, error) {
	g.wrote.Store(true)
	return g.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the connection beneath: git's
// output is flushed as it comes, and a push needs full duplex.
func (g *gitWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// gitBodyStopWait bounds how long stop waits for a read it interrupted.
const gitBodyStopWait = 5 * time.Second

// errGitBodyClosed ends a read of a request body whose handler is done.
var errGitBodyClosed = errors.New("git request body read after its handler ended")

// gitBody is a request body as git's stdin copier reads it, and what the
// handler does about the body when it is done.
//
// ServeRPC can return while its copier still reads the body: git exited
// early (a refusal, the size limit), and the copier is abandoned until its
// next write to the dead stdin fails — or for as long as a stalled client
// keeps its read waiting. net/http forbids reading a body once the handler
// has returned: the server reads the connection's next request by then, and
// two reads at once panic ("invalid concurrent Body.Read call").
//
// A body left part read has a second trap. ServeRPC enables full duplex,
// and in that mode net/http reads the rest of such a body (up to 256 KiB)
// only after the handler, once it has stopped watching the connection: a
// rest that reaches its end there starts a background read nobody stops,
// and the connection's next request panics the same way (reproduced on
// go1.25; TestGitCutOffBodyKeepsServing). So a body not read to its end
// closes its connection after the response, and a read deadline in the past
// keeps that last read from waiting on a stalled client. The deadline never
// goes without the close: on a connection kept open, the server would read
// the rest of the body as the next request.
type gitBody struct {
	r      io.Reader
	sem    chan struct{} // held during a read
	closed atomic.Bool
	eof    atomic.Bool
}

func newGitBody(r io.Reader) *gitBody { return &gitBody{r: r, sem: make(chan struct{}, 1)} }

func (b *gitBody) Read(p []byte) (int, error) {
	b.sem <- struct{}{}
	defer func() { <-b.sem }()
	if b.closed.Load() {
		return 0, errGitBodyClosed
	}
	n, err := b.r.Read(p)
	if err == io.EOF {
		b.eof.Store(true)
	}
	return n, err
}

// stop returns once no read of the body runs and none can start. Deferred
// by the handler that handed the body to ServeRPC.
func (b *gitBody) stop(w http.ResponseWriter) {
	b.closed.Store(true)
	rc := http.NewResponseController(w)
	select {
	case b.sem <- struct{}{}:
	default:
		// A read is running; if it waits on the client, a deadline in the
		// past ends it. Unless that read reached the body's end (and left
		// nothing to misread), the connection is closed below.
		_ = rc.SetReadDeadline(time.Now())
		select {
		case b.sem <- struct{}{}:
		case <-time.After(gitBodyStopWait):
			// Only a writer that cannot set deadlines — never a server's
			// connection — gets here.
			return
		}
	}
	<-b.sem
	if !b.eof.Load() {
		_ = rc.SetReadDeadline(time.Now())
		closeAfterResponse(w)
	}
}

// closeAfterResponse has the server close the connection once this response
// is written. http.MaxBytesReader does that, as documented, for a body past
// its limit, through the server's own writer — so it is handed that writer,
// from beneath every wrapper, and a one-byte reader with a limit of zero.
// Elsewhere (HTTP/2, a test recorder) it does nothing.
func closeAfterResponse(w http.ResponseWriter) {
	for {
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		w = u.Unwrap()
	}
	var one [1]byte
	_, _ = http.MaxBytesReader(w, io.NopCloser(strings.NewReader("x")), 0).Read(one[:])
}

// errBodyTooLarge ends a fetch body read past gitFetchBodyMax.
var errBodyTooLarge = errors.New("git request body too large")

// boundedBody reads at most left bytes of r and fails after them, noting
// why it stopped: over for the bound (its own, or one beneath it), failed
// for any other read error. It touches nothing but r: it runs on ServeRPC's
// stdin goroutine. Atomic flags: the handler reads them after ServeRPC.
type boundedBody struct {
	r      io.Reader
	left   int64
	over   atomic.Bool
	failed atomic.Bool
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, errBodyTooLarge
	}
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.r.Read(p)
	if int64(n) > b.left {
		n = int(b.left)
		b.left = -1
		b.over.Store(true)
		return n, errBodyTooLarge
	}
	b.left -= int64(n)
	if err != nil && err != io.EOF {
		if errors.Is(err, errBodyTooLarge) {
			b.over.Store(true)
		} else {
			b.failed.Store(true)
		}
	}
	return n, err
}
