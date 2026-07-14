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
	"strings"
	"time"

	"github.com/ittrail/sitebin/internal/auth"
	"github.com/ittrail/sitebin/internal/config"
	"github.com/ittrail/sitebin/internal/ext"
	"github.com/ittrail/sitebin/internal/store"
)

const (
	viewCookieName = "sitebin_v"
	viewCookieTTL  = 12 * time.Hour
)

// API holds the wired-up handler state.
type API struct {
	cfg    config.Config
	st     *store.Store
	signer auth.TokenSigner
	webFS  fs.FS
	log    *slog.Logger

	verifyCache    *auth.VerifyCache
	createLimiter  *auth.Limiter
	reportLimiter  *auth.Limiter
	authLimiter    *auth.Limiter // per (ip, target)
	targetLimiter  *auth.Limiter // per target, any source
	davLockSystems *davLocks
}

// New wires the API. secret signs view-session cookies; webFS provides the
// embedded UI/viewer assets (the web package's FS in production).
func New(cfg config.Config, st *store.Store, secret []byte, webFS fs.FS) (*API, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("httpapi: secret too short")
	}
	return &API{
		cfg:            cfg,
		st:             st,
		signer:         auth.TokenSigner{Secret: secret},
		webFS:          webFS,
		log:            slog.Default(),
		verifyCache:    auth.NewVerifyCache(5 * time.Minute),
		createLimiter:  auth.NewLimiter(float64(cfg.RateCreatePerHour), cfg.RateCreateBurst),
		reportLimiter:  auth.NewLimiter(20, 5),
		authLimiter:    auth.NewLimiter(float64(cfg.RateAuthPer5Min)*12, cfg.RateAuthPer5Min), // per-5min → per-hour
		targetLimiter:  auth.NewLimiter(float64(cfg.RateAuthPer5Min)*12*6, cfg.RateAuthPer5Min*6),
		davLockSystems: newDavLocks(),
	}, nil
}

// Public returns the handler Caddy proxies: main-domain UI + API + WebDAV,
// and /_sitebin/* on site origins.
func (a *API) Public() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", a.landingPage)
	mux.HandleFunc("GET /e/{editID}", a.editPage)

	mux.HandleFunc("POST /api/sites", a.createSite)
	mux.HandleFunc("POST /api/report", a.report)
	mux.HandleFunc("GET /api/sites/{editID}", a.withEditAuth(a.getSite))
	mux.HandleFunc("GET /api/sites/{editID}/download", a.withEditAuth(a.downloadSite))
	mux.HandleFunc("GET /api/sites/{editID}/content/{path...}", a.withEditAuth(a.getFileContent))
	mux.HandleFunc("PUT /api/sites/{editID}", a.withEditAuth(a.updateSite))
	mux.HandleFunc("DELETE /api/sites/{editID}", a.withEditAuth(a.deleteSite))
	mux.HandleFunc("POST /api/sites/{editID}/files", a.withEditAuth(a.uploadFiles))
	mux.HandleFunc("DELETE /api/sites/{editID}/files/{path...}", a.withEditAuth(a.deleteFile))
	mux.HandleFunc("POST /api/sites/{editID}/domains", a.withEditAuth(a.addDomain))
	mux.HandleFunc("DELETE /api/sites/{editID}/domains/{domain}", a.withEditAuth(a.removeDomain))

	mux.Handle("/dav/", http.HandlerFunc(a.webdav))

	mux.HandleFunc("GET /_sitebin/assets/{path...}", a.asset)
	mux.HandleFunc("POST /_sitebin/unlock", a.unlock)

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
	return a.logMiddleware(mux)
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

// ---- helpers ----

// clientIP returns the caller's IP. Behind Caddy the last X-Forwarded-For
// entry is the peer Caddy actually saw; earlier entries are client-supplied
// and untrustworthy.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func hostWithoutPort(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.ToLower(h)
}

// siteByHost resolves a request Host (subdomain or custom domain) to a site.
func (a *API) siteByHost(host string) (*store.Site, error) {
	h := strings.ToLower(hostWithoutPort(host))
	if label, ok := strings.CutSuffix(h, "."+a.cfg.BaseDomain); ok {
		if strings.Contains(label, ".") {
			return nil, store.ErrNotFound
		}
		return a.st.ByViewID(label)
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
	case errors.Is(err, store.ErrBadDomain):
		writeError(w, 400, err.Error())
	default:
		slog.Error("internal error", "err", err)
		writeError(w, 500, "internal error")
	}
}

// withEditAuth authenticates edit operations: the edit id in the path plus
// the edit password from X-Edit-Password (or Basic auth), rate limited and
// cached to keep Argon2 work off the hot path.
func (a *API) withEditAuth(next func(http.ResponseWriter, *http.Request, *store.Site)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		editID := r.PathValue("editID")
		site, err := a.st.ByEditID(editID)
		if err != nil {
			storeError(w, err)
			return
		}
		pw := r.Header.Get("X-Edit-Password")
		if pw == "" {
			if _, p, ok := r.BasicAuth(); ok {
				pw = p
			}
		}
		if pw == "" {
			writeError(w, 401, "edit password required (X-Edit-Password header)")
			return
		}
		switch a.verifyEdit(r, site, pw) {
		case verifyOK:
			next(w, r, site)
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
	sum := sha256.Sum256([]byte(pw))
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
