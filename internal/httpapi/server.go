// Package httpapi implements Sitebin's backend HTTP surface: the public API
// and UI (proxied by Caddy on the main domain plus /_sitebin/* on every site
// origin), per-site WebDAV, and the internal listener that answers Caddy's
// forward-auth and on-demand-TLS subrequests.
package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/config"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/forms"
	"github.com/ittrail/sitebin.io/internal/gitrepo"
	"github.com/ittrail/sitebin.io/internal/ids"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

const (
	viewCookieName = "sitebin_v"
	viewCookieTTL  = 12 * time.Hour
)

// Version is the build's version string, reported to MCP clients at
// initialize. cmd/sitebin sets it from the same -ldflags value the CLI prints.
var Version = "dev"

// API holds the wired-up handler state.
type API struct {
	gitOAuthCache gitOAuthCache
	cfg           config.Config
	st            *store.Store
	signer        auth.TokenSigner
	webFS         fs.FS
	log           *slog.Logger

	verifyCache    *auth.VerifyCache
	createLimiter  *auth.Limiter
	reportLimiter  *auth.Limiter // abuse reports per source
	reportGlobal   *auth.Limiter // abuse reports instance-wide
	reportDedupe   *auth.Limiter // one report per (target, source) per day
	cspLimiter     *auth.Limiter // CSP reports per source
	authLimiter    *auth.Limiter // per (ip, target)
	targetLimiter  *auth.Limiter // per target, any source
	domainChecks   *auth.Limiter // DNS checks an owner asks for, per claim
	domainSites    *auth.Limiter // DNS checks an owner asks for, per site
	davLockSystems *davLocks
	uploads        *uploadTokens
	assets         assetCache
	csp            *cspAggregator
	forms          *formsState     // nil when the instance has no forms
	alerts         *alerter        // abuse mail to the operator
	tripLimiter    *auth.Limiter   // CSP tripwire verifications per site
	tripSlots      chan struct{}   // CSP tripwire verifications running at once
	reportKey      []byte          // signs the report page's form tickets
	git            *gitrepo.Runner // nil: Git is turned off, or no git binary
	gitInspect     pushInspector   // nil: git; a test's stand-in for the push bookkeeping
}

// New wires the API. secret signs view-session cookies; webFS provides the
// embedded UI/viewer assets (the web package's FS in production).
func New(cfg config.Config, st *store.Store, secret []byte, webFS fs.FS) (*API, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("httpapi: secret too short")
	}
	a := &API{
		csp:            newCSPAggregator(),
		cfg:            cfg,
		st:             st,
		signer:         auth.TokenSigner{Secret: secret, Purpose: "view"},
		webFS:          webFS,
		log:            slog.Default(),
		verifyCache:    auth.NewVerifyCache(5 * time.Minute),
		createLimiter:  auth.NewLimiter(float64(cfg.RateCreatePerHour), cfg.RateCreateBurst),
		reportLimiter:  auth.NewLimiter(20, 5),
		reportGlobal:   auth.NewLimiter(200, 50),
		reportDedupe:   auth.NewLimiter(1.0/24, 1),
		cspLimiter:     auth.NewLimiter(cspPerHour, cspBurst),
		authLimiter:    auth.NewLimiter(float64(cfg.RateAuthPer5Min)*12, cfg.RateAuthPer5Min), // per-5min → per-hour
		targetLimiter:  auth.NewLimiter(float64(cfg.RateAuthPer5Min)*12*6, cfg.RateAuthPer5Min*6),
		domainChecks:   auth.NewLimiter(domainChecksPerHour, domainChecksBurst),
		domainSites:    auth.NewLimiter(domainSiteChecksPerHour, domainSiteChecksBurst),
		davLockSystems: newDavLocks(),
		uploads:        newUploadTokens(time.Now),
		forms:          newFormsState(cfg, secret),
		tripLimiter:    auth.NewLimiter(tripPerHour, tripBurst),
		tripSlots:      make(chan struct{}, tripSlots),
		reportKey:      reportTicketKey(secret),
	}
	var send forms.Sender
	if s := cfg.FormsSMTP; s != nil {
		send = &forms.SMTPSender{Host: s.Host, Port: s.Port, User: s.User, Pass: s.Pass, ImplicitTLS: s.TLS, Timeout: alertTimeout}
	}
	a.alerts = newAlerter(cfg, send, a.log)
	// Every decision of the store's abuse guard comes here: lock side
	// effects and the operator's mail.
	st.SetScanHook(a.onScan)
	// Every retention purge of a locked site (the cleanup sweep, in this
	// process) becomes a line in the operator's digest.
	st.SetPurgeHook(a.onLockPurged)
	a.git = newGitRunner(cfg, a.log)
	return a, nil
}

// GitRunner is the runner the Git endpoint uses; nil when git is unavailable.
// The cleanup sweep collects through the same one, so both share its slots.
func (a *API) GitRunner() *gitrepo.Runner { return a.git }

// newGitRunner finds the git binary for the Git endpoint. Git never stops
// the instance from starting: without a binary (or a state directory) it is
// off, logged once here, and every site says "not available".
func newGitRunner(cfg config.Config, log *slog.Logger) *gitrepo.Runner {
	if !cfg.GitEnabled {
		return nil
	}
	r, err := gitrepo.New(gitrepo.Options{
		StateDir:     filepath.Join(cfg.DataDir, "gitstate"),
		MaxProcesses: cfg.GitMaxProcesses,
	})
	if err != nil {
		log.Warn("git: not available", "err", err)
		return nil
	}
	return r
}

// Public returns the handler Caddy proxies: main-domain UI + API + WebDAV,
// and /_sitebin/* on site origins.
func (a *API) Public() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", a.landingPage)
	mux.HandleFunc("GET "+securityTxtPath, a.securityTxt)
	mux.HandleFunc("GET /e/{editID}", a.editPage)

	mux.HandleFunc("POST /api/sites", a.createSite)
	mux.HandleFunc("OPTIONS /api/sites", a.createPreflight)
	mux.HandleFunc("POST /api/report", a.report)
	mux.HandleFunc("GET /report", a.reportPage)
	mux.HandleFunc("POST /report", a.reportSubmit)
	mux.HandleFunc("GET /api/sites/{editID}", a.withEditAuthEvenLocked(a.getSite))
	mux.HandleFunc("GET /api/sites/{editID}/download", a.withEditAuth(a.downloadSite))
	mux.HandleFunc("GET /api/sites/{editID}/content/{path...}", a.withEditAuth(a.getFileContent))
	mux.HandleFunc("GET /api/sites/{editID}/dir", a.withEditAuth(a.listDir))
	mux.HandleFunc("PUT /api/sites/{editID}", a.withEditAuth(a.recorded(provenance.ActionSettings, a.updateSite)))
	mux.HandleFunc("DELETE /api/sites/{editID}", a.withEditAuth(a.recorded(provenance.ActionSiteDelete, a.deleteSite)))
	mux.HandleFunc("POST /api/sites/{editID}/files", a.withUploadAuth(a.recorded(provenance.ActionUpload, a.uploadFiles)))
	mux.HandleFunc("DELETE /api/sites/{editID}/files/{path...}", a.withEditAuth(a.recorded(provenance.ActionDeleteFile, a.deleteFile)))
	mux.HandleFunc("POST /api/sites/{editID}/domains", a.withEditAuth(a.recorded(provenance.ActionDomainAdd, a.addDomain)))
	mux.HandleFunc("DELETE /api/sites/{editID}/domains/{domain}", a.withEditAuth(a.recorded(provenance.ActionDomainRemove, a.removeDomain)))
	mux.HandleFunc("PUT /api/sites/{editID}/domains/{domain}/redirect", a.withEditAuth(a.recorded(provenance.ActionDomainRedirect, a.putDomainRedirect)))
	mux.HandleFunc("DELETE /api/sites/{editID}/domains/{domain}/redirect", a.withEditAuth(a.recorded(provenance.ActionDomainRedirect, a.deleteDomainRedirect)))
	mux.HandleFunc("PUT /api/sites/{editID}/domains/{domain}/target", a.withEditAuth(a.recorded(provenance.ActionDomainTarget, a.putDomainTarget)))
	mux.HandleFunc("PUT /api/sites/{editID}/view-address", a.withEditAuth(a.recorded(provenance.ActionViewAddress, a.putViewAddress)))
	mux.HandleFunc("POST /api/sites/{editID}/containers/{action}", a.withEditAuth(a.recorded(provenance.ActionContainer, a.containerAction)))
	mux.HandleFunc("GET /api/sites/{editID}/containers/{service}/logs", a.withEditAuth(a.containerLogs))
	mux.HandleFunc("GET /api/sites/{editID}/containers/stats", a.withEditAuth(a.containerStats))

	mux.HandleFunc("GET /api/sites/{editID}/forms", a.withEditAuth(a.listForms))
	mux.HandleFunc("POST /api/sites/{editID}/forms", a.withEditAuth(a.recorded(provenance.ActionFormAdd, a.createForm)))
	mux.HandleFunc("PUT /api/sites/{editID}/forms/{key}", a.withEditAuth(a.recorded(provenance.ActionFormUpdate, a.patchForm)))
	mux.HandleFunc("DELETE /api/sites/{editID}/forms/{key}", a.withEditAuth(a.recorded(provenance.ActionFormRemove, a.removeForm)))
	mux.HandleFunc("POST /api/sites/{editID}/forms/{key}/confirmation", a.withEditAuth(a.recorded(provenance.ActionFormResend, a.resendFormConfirmation)))

	mux.HandleFunc("GET /api/sites/{editID}/git", a.withEditAuth(a.gitInfo))
	mux.HandleFunc("GET /api/sites/{editID}/git/refs", a.withEditAuth(a.gitRefs))
	mux.HandleFunc("GET /api/sites/{editID}/git/log", a.withEditAuth(a.gitLog))
	mux.HandleFunc("GET /api/sites/{editID}/git/commit/{sha}", a.withEditAuth(a.gitCommit))
	mux.HandleFunc("GET /api/sites/{editID}/git/tree", a.withEditAuth(a.gitTree))
	mux.HandleFunc("GET /api/sites/{editID}/git/blob", a.withEditAuth(a.gitBlob))
	mux.HandleFunc("GET /api/sites/{editID}/git/archive", a.withEditAuth(a.gitArchive))
	mux.HandleFunc("DELETE /api/sites/{editID}/git", a.withEditAuth(a.recorded(provenance.ActionGitDelete, a.deleteGitRepo)))

	mux.Handle("/dav/", http.HandlerFunc(a.webdav))

	// Every site's repository, /git/<edit-id>.git, smart HTTP only. The
	// handler answers on the base domain alone (see serveGit).
	mux.HandleFunc("GET /git/{repo}/info/refs", a.serveGit)
	mux.HandleFunc("POST /git/{repo}/git-upload-pack", a.serveGit)
	mux.HandleFunc("POST /git/{repo}/git-receive-pack", a.serveGit)

	// The Model Context Protocol endpoint. Mounted on the main domain, which
	// Caddy already proxies wholesale, so it needs no Caddy configuration. Not
	// mounted at all when disabled, so /mcp then 404s like any unknown path.
	if a.cfg.MCPEnabled {
		mux.Handle("/mcp", a.mcpHandler())
		a.mcpDiscoveryRoutes(mux)
	}

	mux.HandleFunc("GET /_sitebin/assets/{path...}", a.asset)
	mux.HandleFunc("GET /_sitebin/embed.js", a.embedScript)
	mux.HandleFunc("GET /_sitebin/qrcode.min.js", a.qrScript)
	mux.HandleFunc("POST /_sitebin/unlock", a.unlock)
	mux.HandleFunc("POST /_sitebin/csp-report", a.handleCSPReport)
	mux.HandleFunc("POST /_sitebin/forms/{key}", a.submitForm)
	mux.HandleFunc("GET /_sitebin/forms/{key}/challenge", a.formChallenge)
	mux.HandleFunc("GET /_sitebin/forms/{key}/thanks", a.formThanks)
	mux.HandleFunc("GET /_sitebin/altcha.js", a.altchaScript)

	mux.HandleFunc("GET /forms/confirm", a.formConfirmPage)
	mux.HandleFunc("POST /forms/confirm", a.formConfirm)
	mux.HandleFunc("GET /forms/stop", a.formStopPage)
	mux.HandleFunc("POST /forms/stop", a.formStop)

	mux.HandleFunc("GET /account/status", a.accountStatus)

	// Enterprise: mount the account dashboard + auth routes when a provider is
	// active. Guarded so a provider can only ever add routes on the main domain.
	if p, ok := ext.Get(); ok {
		for pattern, h := range p.PublicRoutes() {
			mux.Handle(pattern, h)
		}
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		a.notFoundPage(w, r)
	})
	return a.logMiddleware(a.viewApexGuard(mux))
}

// Internal returns the handler for Caddy's subrequests plus health checks.
// It must never be exposed publicly; it binds its own listener.
func (a *API) Internal() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/authz", a.authz)
	mux.HandleFunc("GET /internal/tls-check", a.tlsCheck)
	mux.HandleFunc("GET /internal/health", a.health)
	return mux
}

func (a *API) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		a.log.Info("http",
			"method", r.Method, "host", r.Host, "path", r.URL.Path,
			"status", sw.code, "ms", time.Since(start).Milliseconds(), "ip", clientIP(r))
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap hands http.ResponseController the writer underneath, so a handler
// can still flush through this one. Embedding the interface does not promote
// Flush, and without this every Flush below was a silent no-op: short
// responses never noticed, but an MCP subscriptions/listen stream held its
// first event in the buffer for as long as it stayed open.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ---- helpers ----

// clientIP returns the caller's IP. Behind Caddy the last X-Forwarded-For
// entry is the peer Caddy actually saw; earlier entries are client-supplied
// and untrustworthy.
func clientIP(r *http.Request) string { return auth.ClientIP(r) }

func hostWithoutPort(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.ToLower(h)
}

// siteByHost resolves a request Host (subdomain or custom domain) to a site.
func (a *API) siteByHost(host string) (*store.Site, error) {
	h := strings.ToLower(hostWithoutPort(host))
	if label, ok := strings.CutSuffix(h, "."+a.cfg.ViewDomain); ok {
		if strings.Contains(label, ".") {
			return nil, store.ErrNotFound
		}
		// A site's own address, else another generated address of a
		// container site (one per HTTP port).
		site, err := a.st.ByViewID(label)
		if errors.Is(err, store.ErrNotFound) {
			return a.st.ByAddress(label)
		}
		return site, err
	}
	return a.st.ByDomain(h)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// msgReplaceBusy answers a replace of a site while another is still running,
// on the API and over MCP alike.
const msgReplaceBusy = "another replace of this site is still running — wait for it to finish, then try again"

// storeError maps store sentinel errors onto HTTP responses.
func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "not found")
	case errors.Is(err, store.ErrDomainTaken):
		writeError(w, 409, "domain already in use by another site")
	case errors.Is(err, store.ErrTooLarge):
		writeError(w, 413, "site size limit exceeded")
	case errors.Is(err, store.ErrTooManyFiles):
		writeError(w, 413, "file count limit exceeded")
	case errors.Is(err, store.ErrTooManyDomain):
		writeError(w, 409, err.Error())
	case errors.Is(err, store.ErrBadPath):
		writeError(w, 400, "invalid file path")
	case errors.Is(err, store.ErrUnreadable):
		writeError(w, 403, err.Error())
	case errors.Is(err, store.ErrBadArchive):
		writeError(w, 400, err.Error())
	case errors.Is(err, errUploadCutOff):
		writeError(w, 400, errUploadCutOff.Error())
	case errors.Is(err, store.ErrBadDomain):
		writeError(w, 400, err.Error())
	case errors.Is(err, store.ErrFormNotFound):
		writeError(w, 404, err.Error())
	case errors.Is(err, store.ErrTooManyForms):
		writeError(w, 403, err.Error())
	case errors.Is(err, store.ErrFormActive):
		writeError(w, 409, err.Error())
	case errors.Is(err, store.ErrFormStale):
		writeError(w, 410, err.Error())
	case errors.Is(err, store.ErrReplaceBusy):
		writeError(w, 409, msgReplaceBusy)
	case errors.Is(err, store.ErrGitBusy):
		writeError(w, 409, store.ErrGitBusy.Error())
	case errors.As(err, new(*store.HeldError)):
		// The abuse guard locked the site on this very write.
		var held *store.HeldError
		errors.As(err, &held)
		writeError(w, 403, held.Error())
	case errors.Is(err, store.ErrLocked):
		// A lock placed while the request was on its way (a replace commit,
		// a delete): the gate let it in, the store held the line.
		writeError(w, 403, store.LockedMessage(nil))
	default:
		slog.Error("internal error", "err", err)
		writeError(w, 500, "internal error")
	}
}

// standing is what an account is to a site: nothing, its owner, or an admin
// the owner shared it with. An admin may do everything the owner may with the
// site, except the owner-only actions (refuseUnlessOwner).
type standing int

const (
	standNone standing = iota
	standOwner
	standAdmin
)

// role is the standing as the actor records it and the payloads name it;
// standNone has none (the caller then authenticated otherwise, if at all).
func (s standing) role() string {
	switch s {
	case standOwner:
		return roleOwner
	case standAdmin:
		return roleAdmin
	}
	return ""
}

// accountStanding is what accountID is to site — the ONE rule by which the
// core's access gates compare an account with a site: tokenAccount (an
// account token, on the JSON API and the Git endpoint), sessionStanding (a
// browser session on the edit page), MCP's openSiteAs (the connection's
// account) and gitAuth's OAuth branch. None of them compares an account with
// the owner or the admins itself, and nothing else may treat an account as
// standing on a site.
//
//   - Owner: the site's OwnerAccountID. An anonymous site (no owner) has
//     nobody standing on it.
//   - Admin: an account the site's meta lists as a live admin
//     (store.Meta.LiveAdmin: no end date, or one still ahead) while the
//     extension's SharingAllowed is true for BOTH the owner and that
//     account. Without a provider implementing ext.SharingAccounts there is
//     no admin standing at all. A SharingAllowed error refuses this one
//     request and changes nothing stored: the share stays.
//
// The plan is asked only for an account the meta lists, so an owner, a
// stranger and every site nobody shares cost no lookup.
//
// Attribution follows the standing a gate found, never a second comparison:
// the actor's role (editAuth, gitAuth, mcpActor from openSiteAs, an upload
// token's issuer). The enterprise dashboard's provider.owns (ee/provider.go)
// is the extension's own decision over the account's own site list.
func (a *API) accountStanding(site *store.Site, accountID string) standing {
	m := site.Meta
	if accountID == "" || m.OwnerAccountID == "" {
		return standNone
	}
	if accountID == m.OwnerAccountID {
		return standOwner
	}
	if !m.LiveAdmin(accountID, time.Now()) {
		return standNone
	}
	sa, ok := sharingProvider()
	if !ok {
		return standNone
	}
	for _, id := range []string{m.OwnerAccountID, accountID} {
		allowed, err := sa.SharingAllowed(id)
		if err != nil {
			a.log.Warn("sharing: plan unknown, admin standing refused for this request", "id", site.ViewID, "account", id, "err", err)
			return standNone
		}
		if !allowed {
			return standNone
		}
	}
	return standAdmin
}

// sharingProvider returns the extension's sharing answers, when there is a
// provider with accounts enabled that has them.
func sharingProvider() (ext.SharingAccounts, bool) {
	p, ok := ext.Get()
	if !ok || !p.AccountsEnabled() {
		return nil, false
	}
	sa, ok := p.(ext.SharingAccounts)
	return sa, ok
}

// tokenAccount resolves the request's account API token (Authorization:
// Bearer) to the account it acts for and that account's standing on site
// (accountStanding); standNone, with no account, when it has none. The check
// lives here, in the core, because the core is the side that holds the site's
// metadata; the extension only says which account the token belongs to.
//
// An OAuth access token never counts, whatever the extension hands back. Its
// audience is the MCP resource, and the JSON API has no scope check to hold
// it to what it was granted: honoured here, a read-only grant could overwrite
// and delete sites. MCP reads its OAuth credentials in mcpOps.Authenticate and
// the Git endpoint in gitAuth's own OAuth branch, never through this.
func (a *API) tokenAccount(r *http.Request, site *store.Site) (string, standing) {
	cred, ok := bearerCredential(r)
	if !ok || cred.OAuth {
		return "", standNone
	}
	st := a.accountStanding(site, cred.AccountID)
	if st == standNone {
		return "", standNone
	}
	return cred.AccountID, st
}

// bearerCredential asks the extension what the request's bearer is. Without a
// provider nothing is a credential.
func bearerCredential(r *http.Request) (ext.Credential, bool) {
	p, ok := ext.Get()
	if !ok {
		return ext.Credential{}, false
	}
	return p.BearerCredential(r)
}

// sessionHeader marks a request from Sitebin's own edit page that asks for
// the browser's account session to be honoured. See sessionStanding.
const sessionHeader = "X-Sitebin-Session"

// sessionStanding resolves the request's browser session to its account and
// that account's standing on site (accountStanding) — the standing an account
// token has, reached from the owner's or an admin's own browser; standNone,
// with no account, when it has none. See
// docs/superpowers/specs/2026-09-25-owner-session-edit-design.md.
//
// THIS IS A SECURITY BOUNDARY, unlike fromOwnBrowser. A cookie rides along on
// any request the browser makes, where an edit password or a bearer token
// never does, so the session is honoured only on a request carrying
// sessionHeader: a page on another origin cannot add a custom header without
// a CORS preflight, and no per-site route answers one. SameSite=Lax alone
// would not do — the marketing apex serves uploaded content and is same-site
// with the app — so a Sec-Fetch-Site the browser did send must also say
// same-origin. Never relax this into fromOwnBrowser's forgeable heuristics.
func (a *API) sessionStanding(r *http.Request, site *store.Site) (string, standing) {
	if r.Header.Get(sessionHeader) != "1" {
		return "", standNone
	}
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
		return "", standNone
	}
	sa, ok := a.sessionProvider()
	if !ok {
		return "", standNone
	}
	id, ok := sa.SessionAccount(r)
	if !ok {
		return "", standNone
	}
	st := a.accountStanding(site, id)
	if st == standNone {
		return "", standNone
	}
	return id, st
}

// sessionProvider returns the extension's session lookup, when there is a
// provider with accounts enabled that has one.
func (a *API) sessionProvider() (ext.SessionAccounts, bool) {
	p, ok := ext.Get()
	if !ok || !p.AccountsEnabled() {
		return nil, false
	}
	sa, ok := p.(ext.SessionAccounts)
	return sa, ok
}

// withEditAuth authenticates edit operations: the edit id in the path plus
// the edit password from X-Edit-Password (or Basic auth), rate limited and
// cached to keep Argon2 work off the hot path.
//
// A locked site is refused with 403 once the caller has authenticated —
// whichever credential it used — so every per-site route is frozen by the
// lock without restating the rule in each handler.
func (a *API) withEditAuth(next func(http.ResponseWriter, *http.Request, *store.Site)) http.HandlerFunc {
	return a.editAuth(next, false)
}

// withEditAuthEvenLocked is withEditAuth for the one route that answers a
// locked site: GET /api/sites/{editID}, the edit page's load, which is how
// the owner learns about the lock. Its payload then carries no file list.
func (a *API) withEditAuthEvenLocked(next func(http.ResponseWriter, *http.Request, *store.Site)) http.HandlerFunc {
	return a.editAuth(next, true)
}

func (a *API) editAuth(next func(http.ResponseWriter, *http.Request, *store.Site), showLocked bool) http.HandlerFunc {
	if !showLocked {
		handler := next
		next = func(w http.ResponseWriter, r *http.Request, site *store.Site) {
			if refuseLocked(w, site) {
				return
			}
			handler(w, r, site)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// An upload token opens exactly one route, which withUploadAuth
		// guards. Everywhere else it is refused before any password work, so
		// it neither burns the rate limit nor reads as a wrong password.
		if uploadCredential(r) != "" {
			writeError(w, 403, msgUploadTokenOnlyUploads)
			return
		}
		editID := r.PathValue("editID")
		site, err := a.st.ByEditID(editID)
		if err != nil {
			storeError(w, err)
			return
		}
		// An account API token stands in for the edit password on sites that
		// account owns or administers. That is the whole point of tokens: a
		// script should not have to carry one secret per site to manage the
		// sites it created. The actor carries the standing as its role, which
		// is what refuseUnlessOwner and payloadFor read.
		if account, st := a.tokenAccount(r, site); st != standNone {
			next(w, withActor(r, actor{surface: a.apiSurface(r), auth: provenance.AuthToken, account: account, role: st.role()}), site)
			return
		}
		// The owner or an admin, signed in, on Sitebin's own edit page: the
		// same standing, through the browser session. It is asked before the
		// edit password the page may send beside it, so a signed-in admin who
		// also holds the password is still an admin (R16).
		if account, st := a.sessionStanding(r, site); st != standNone {
			next(w, withActor(r, actor{surface: a.apiSurface(r), auth: provenance.AuthSession, account: account, role: st.role()}), site)
			return
		}
		pw := r.Header.Get("X-Edit-Password")
		if pw == "" {
			if _, p, ok := r.BasicAuth(); ok {
				pw = p
			}
		}
		if pw == "" {
			body := map[string]string{"error": "edit password required (X-Edit-Password header), or an account API token as Authorization: Bearer"}
			// Where accounts sign in, the edit page offers that instead of
			// the password: the owner's session opens the site.
			if _, ok := a.sessionProvider(); ok {
				body["account_url"] = a.apiAccountHint()
			}
			writeJSON(w, 401, body)
			return
		}
		switch a.verifyEdit(r, site, pw) {
		case verifyOK:
			// The API is an account feature. An anonymous site keeps its claim
			// ticket and its edit page; it just cannot be scripted.
			if !a.apiAllowedFor(site, r) {
				writeError(w, 403, "this site was created without an account, so it has no API — create it while signed in at "+a.apiAccountHint()+" to script it")
				return
			}
			next(w, withActor(r, actor{surface: a.apiSurface(r), auth: provenance.AuthPassword, role: rolePassword}), site)
		case verifyThrottled:
			writeError(w, 429, "too many password attempts, slow down")
		default:
			writeError(w, 401, "wrong edit password")
		}
	}
}

type verifyResult int

const (
	verifyOK verifyResult = iota
	verifyFailed
	verifyThrottled
)

// verifyEdit checks an edit password with rate limiting and caching. Callers
// render their own error responses (JSON for the API, Basic challenge for
// WebDAV).
func (a *API) verifyEdit(r *http.Request, site *store.Site, pw string) verifyResult {
	return a.verifyEditIP(clientIP(r), site, pw)
}

// verifyEditIP is verifyEdit keyed by a client IP string (used by non-HTTP
// callers such as the FTP server).
func (a *API) verifyEditIP(clientIP string, site *store.Site, pw string) verifyResult {
	// An upload token is never an edit password; refusing it here keeps it
	// from spending the password rate limits on any surface — API, WebDAV,
	// FTP, MCP.
	if strings.HasPrefix(pw, ids.UploadTokenPrefix) {
		return verifyFailed
	}
	// The entry is bound to the hash it was checked against: a reader of a
	// newer hash (a transfer, a rotation) never matches an entry made against
	// the old one, and a late Put against a stale site object never matches a
	// later read. The EditID prefix stays, so Drop(EditID + ":") still works.
	sum := sha256.Sum256([]byte(site.Meta.EditPasswordHash + "|" + pw)) // the hash holds no "|", so the join is unambiguous
	cacheKey := site.EditID + ":" + hex.EncodeToString(sum[:])
	if a.verifyCache.Check(cacheKey) {
		return verifyOK
	}
	if !a.authLimiter.Allow(clientIP+"|"+site.EditID) || !a.targetLimiter.Allow(site.EditID) {
		return verifyThrottled
	}
	if !auth.VerifyPassword(site.Meta.EditPasswordHash, pw) {
		return verifyFailed
	}
	a.verifyCache.Put(cacheKey)
	return verifyOK
}
