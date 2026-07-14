package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/ittrail/sitebin/internal/auth"
	"github.com/ittrail/sitebin/internal/store"
)

// tokenSubject binds view-session tokens to the site AND its current view
// password hash, so changing the password instantly invalidates old cookies.
func tokenSubject(site *store.Site) string {
	sum := sha256.Sum256([]byte(site.Meta.ViewPasswordHash))
	return site.ViewID + "|" + hex.EncodeToString(sum[:8])
}

// sanitizeRedirect keeps post-unlock redirects on the same origin.
func sanitizeRedirect(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return "/"
	}
	return p
}

// unlock handles the password-gate form posted on a site's own origin.
func (a *API) unlock(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		writeError(w, 400, "invalid form")
		return
	}
	redirect := sanitizeRedirect(r.PostFormValue("redirect"))

	// In path mode the form carries the site id (Host is the main domain, so it
	// can't be resolved from the Host header). The cookie is then scoped to the
	// site's /v/<id>/ path so protected path-sites don't share one cookie slot.
	var site *store.Site
	var err error
	cookiePath := "/"
	if sid := r.PostFormValue("site"); sid != "" {
		site, err = a.st.ByViewID(sid)
		if err == nil {
			cookiePath = "/v/" + site.ViewID + "/"
		}
	} else {
		site, err = a.siteByHost(r.Host)
	}
	if err != nil {
		a.msgPage(w, 404, "Site not found", "There is no site at this address.")
		return
	}
	gateSite := r.PostFormValue("site")
	if !site.Meta.ViewPasswordProtected {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}
	pw := r.PostFormValue("password")
	if !a.authLimiter.Allow(clientIP(r)+"|view:"+site.ViewID) || !a.targetLimiter.Allow("view:"+site.ViewID) {
		a.gatePage(w, 429, redirect, "Too many attempts — please wait a moment and try again.", gateSite)
		return
	}
	if pw == "" || !auth.VerifyPassword(site.Meta.ViewPasswordHash, pw) {
		a.gatePage(w, 401, redirect, "Wrong password, please try again.", gateSite)
		return
	}
	token := a.signer.Sign(tokenSubject(site), time.Now(), viewCookieTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     viewCookieName,
		Value:    token,
		Path:     cookiePath,
		MaxAge:   int(viewCookieTTL.Seconds()),
		HttpOnly: true,
		Secure:   !a.cfg.HTTPOnly,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// hasViewAccess reports whether the request carries a valid view-session
// cookie for the site.
func (a *API) hasViewAccess(r *http.Request, site *store.Site) bool {
	c, err := r.Cookie(viewCookieName)
	if err != nil {
		return false
	}
	return a.signer.Verify(c.Value, tokenSubject(site), time.Now())
}

// ---- pages (self-contained: served on user-site origins where only
// /_sitebin/* reaches the backend, so no external asset dependencies) ----

var basePageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Title}}</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; margin: 0; }
  body {
    min-height: 100dvh; display: grid; place-items: center;
    background: radial-gradient(1200px 800px at 20% -10%, #1c2740 0%, #0b0f1a 55%) #0b0f1a;
    color: #e8ecf4; font: 16px/1.6 ui-sans-serif, system-ui, "Segoe UI", Roboto, sans-serif;
    padding: 24px;
  }
  .card {
    width: min(420px, 100%); background: rgba(255,255,255,.045);
    border: 1px solid rgba(255,255,255,.09); border-radius: 16px;
    padding: 36px 32px; backdrop-filter: blur(12px);
    box-shadow: 0 24px 60px rgba(0,0,0,.45);
  }
  .badge {
    display: inline-flex; align-items: center; gap: 8px;
    font-size: 13px; letter-spacing: .08em; text-transform: uppercase;
    color: #8fa3c8; margin-bottom: 18px;
  }
  .badge svg { width: 18px; height: 18px; }
  h1 { font-size: 22px; margin-bottom: 8px; letter-spacing: -.01em; }
  p { color: #aab6cc; font-size: 15px; }
  form { margin-top: 22px; display: grid; gap: 12px; }
  input[type=password] {
    width: 100%; padding: 12px 14px; border-radius: 10px;
    border: 1px solid rgba(255,255,255,.14); background: rgba(0,0,0,.35);
    color: #e8ecf4; font-size: 16px; outline: none;
  }
  input[type=password]:focus { border-color: #5b8cff; box-shadow: 0 0 0 3px rgba(91,140,255,.25); }
  button {
    padding: 12px 14px; border: 0; border-radius: 10px; cursor: pointer;
    background: linear-gradient(135deg, #f5b84d, #d99a26); color: #1c1503;
    font-size: 15px; font-weight: 650;
  }
  button:hover { filter: brightness(1.07); }
  .err { color: #ff9d9d; font-size: 14px; }
  .code { font: 600 44px/1 ui-monospace, monospace; color: #f5b84d; margin-bottom: 10px; }
</style>
</head>
<body>
<main class="card">
  <span class="badge">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3z"/></svg>
    Sitebin
  </span>
  {{if .Code}}<div class="code">{{.Code}}</div>{{end}}
  <h1>{{.Title}}</h1>
  <p>{{.Message}}</p>
  {{if .ShowForm}}
  <form method="post" action="/_sitebin/unlock">
    <input type="hidden" name="redirect" value="{{.Redirect}}">
    {{if .Site}}<input type="hidden" name="site" value="{{.Site}}">{{end}}
    <input type="password" name="password" placeholder="Password" autofocus autocomplete="current-password" aria-label="View password">
    {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
    <button type="submit">Unlock site</button>
  </form>
  {{end}}
</main>
</body>
</html>
`))

type pageData struct {
	Title    string
	Message  string
	Code     string
	ShowForm bool
	Redirect string
	Error    string
	Site     string // view id (path mode only; empty on subdomains)
}

func (a *API) renderPage(w http.ResponseWriter, status int, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	basePageTmpl.Execute(w, d)
}

// gatePage renders the password prompt (also used as the 401 body relayed by
// Caddy's forward_auth). site is the view id for path-mode gates ("" on
// subdomains, where the site resolves from the Host).
func (a *API) gatePage(w http.ResponseWriter, status int, redirect, errMsg, site string) {
	a.renderPage(w, status, pageData{
		Title:    "This site is protected",
		Message:  "Enter the view password to continue.",
		ShowForm: true,
		Redirect: redirect,
		Error:    errMsg,
		Site:     site,
	})
}

func (a *API) msgPage(w http.ResponseWriter, status int, title, msg string) {
	code := ""
	switch status {
	case 404:
		code = "404"
	case 410:
		code = "410"
	}
	a.renderPage(w, status, pageData{Title: title, Message: msg, Code: code})
}
