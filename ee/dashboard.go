//go:build ee

package ee

import (
	"fmt"
	"net/http"

	"github.com/ittrail/sitebin/ee/account"
	"github.com/ittrail/sitebin/ee/authn"
	"github.com/ittrail/sitebin/internal/ext"
)

// PublicRoutes mounts the account dashboard and auth endpoints on the main
// domain. All handlers are enterprise-only.
func (p *provider) PublicRoutes() map[string]http.Handler {
	if !p.cfg.Enabled() {
		return nil
	}
	routes := map[string]http.Handler{
		"GET /account":                    http.HandlerFunc(p.handleRoot),
		"GET /account/login":              http.HandlerFunc(p.handleLoginGet),
		"POST /account/login":             http.HandlerFunc(p.handleLoginPost),
		"GET /account/signup":             http.HandlerFunc(p.handleSignupGet),
		"POST /account/signup":            http.HandlerFunc(p.handleSignupPost),
		"POST /account/logout":            http.HandlerFunc(p.handleLogout),
		"POST /account/tier":              http.HandlerFunc(p.handleSelectTier),
		"POST /account/sites/{id}/rotate": http.HandlerFunc(p.handleRotate),
		"POST /account/sites/{id}/delete": http.HandlerFunc(p.handleDeleteSite),
		"POST /account/delete":            http.HandlerFunc(p.handleDeleteAccount),
	}
	p.oauthRoutes(routes)
	p.emailRoutes(routes)
	p.billingRoutes(routes)
	return routes
}

// checkoutProvider returns the billing provider to use for upgrades ("stripe",
// "paddle", or "" if none), preferring Stripe when both are configured.
func (p *provider) checkoutProvider() string {
	if p.stripe != nil {
		return "stripe"
	}
	if p.paddle != nil {
		return "paddle"
	}
	return ""
}

// oauthButtons returns the configured providers for the login/signup page.
func (p *provider) oauthButtons() []string {
	var out []string
	for _, prov := range p.oidc.Providers() {
		out = append(out, string(prov))
	}
	return out
}

func (p *provider) handleRoot(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.renderAuth(w, "login", "", "")
		return
	}
	p.renderDashboard(w, acc, "")
}

func (p *provider) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.currentAccount(r); ok {
		p.redirect(w, r, "/account")
		return
	}
	p.renderAuth(w, "login", "", "")
}

func (p *provider) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	email := r.PostFormValue("email")
	acc, err := p.local.Login(email, r.PostFormValue("password"))
	if err != nil {
		p.renderAuth(w, "login", email, "Incorrect email or password.")
		return
	}
	http.SetCookie(w, p.sessions.Cookie(acc.ID, acc.TokenVersion))
	p.redirect(w, r, "/account")
}

func (p *provider) handleSignupGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.currentAccount(r); ok {
		p.redirect(w, r, "/account")
		return
	}
	p.renderAuth(w, "signup", "", "")
}

func (p *provider) handleSignupPost(w http.ResponseWriter, r *http.Request) {
	email := r.PostFormValue("email")
	acc, err := p.local.Signup(email, r.PostFormValue("password"), p.tierForNewAccount())
	if err != nil {
		msg := "Could not create the account."
		switch err {
		case authn.ErrWeakPassword:
			msg = "Password must be at least 8 characters."
		case account.ErrEmailTaken:
			msg = "That email is already registered."
		case account.ErrBadEmail:
			msg = "Please enter a valid email address."
		}
		p.renderAuth(w, "signup", email, msg)
		return
	}
	p.sendVerification(acc)
	http.SetCookie(w, p.sessions.Cookie(acc.ID, acc.TokenVersion))
	p.redirect(w, r, "/account")
}

func (p *provider) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, p.sessions.Clear())
	p.redirect(w, r, "/account/login")
}

// handleSelectTier lets a user switch to a free (non-paid) tier when
// self-select is enabled. Upgrading to a paid tier goes through billing.
func (p *provider) handleSelectTier(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	if !p.checkCSRF(r, acc) || !p.cfg.SelfSelect {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	t, ok := p.cfg.Tier(r.PostFormValue("tier"))
	if !ok {
		http.Error(w, "unknown tier", http.StatusBadRequest)
		return
	}
	if t.Paid() {
		http.Error(w, "upgrading to a paid tier requires checkout", http.StatusForbidden)
		return
	}
	if err := p.accounts.Update(acc, func(cur *account.Account) error { cur.Tier = t.ID; return nil }); err != nil {
		http.Error(w, "could not change tier", http.StatusInternalServerError)
		return
	}
	p.redirect(w, r, "/account")
}

func (p *provider) handleRotate(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	viewID := r.PathValue("id")
	if !p.checkCSRF(r, acc) || !p.owns(acc, viewID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	pw, err := p.host.Sites().RotateEditPassword(viewID)
	if err != nil {
		http.Error(w, "could not reset the edit password", http.StatusInternalServerError)
		return
	}
	p.renderMessage(w, msgView{
		Title:  "New edit password",
		Body:   "This is the only time it is shown — store it now.",
		Detail: pw,
		Back:   "/account",
	})
}

func (p *provider) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	viewID := r.PathValue("id")
	if !p.checkCSRF(r, acc) || !p.owns(acc, viewID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := p.host.Sites().Delete(viewID); err != nil {
		http.Error(w, "could not delete the site", http.StatusInternalServerError)
		return
	}
	p.accounts.UnlinkSite(acc, viewID)
	p.redirect(w, r, "/account")
}

func (p *provider) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	err := p.accounts.Delete(acc, func(viewID string) error { return p.host.Sites().Delete(viewID) })
	if err != nil {
		http.Error(w, "could not delete the account", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, p.sessions.Clear())
	p.renderMessage(w, msgView{
		Title: "Account deleted",
		Body:  "Your account and all its sites have been removed.",
		Back:  "/account/login",
	})
}

// ---- rendering ----

func (p *provider) redirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func (p *provider) securityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
			"script-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
}

type authView struct {
	Mode         string // login | signup
	Email        string
	Error        string
	Providers    []string
	EmailEnabled bool
}

func (p *provider) renderAuth(w http.ResponseWriter, mode, email, errMsg string) {
	p.securityHeaders(w)
	authTmpl.Execute(w, authView{
		Mode: mode, Email: email, Error: errMsg,
		Providers: p.oauthButtons(), EmailEnabled: p.mailer != nil,
	})
}

type siteRow struct {
	ext.SiteInfo
	SizeText string
	CSRF     string
}

type tierOption struct {
	ID      string
	Label   string
	Current bool
	Paid    bool
	Price   string
}

type dashView struct {
	Email      string
	Tier       string
	Sites      []siteRow
	CSRF       string
	Base       string
	SelfSelect bool
	Checkout   string // billing provider for paid upgrades ("" = none)
	Tiers      []tierOption
}

func (p *provider) renderDashboard(w http.ResponseWriter, acc *account.Account, flash string) {
	p.securityHeaders(w)
	token := p.csrf(acc)
	ids, _ := p.accounts.ListSiteIDs(acc)
	rows := make([]siteRow, 0, len(ids))
	for _, id := range ids {
		if info, ok := p.host.Sites().Info(id); ok {
			rows = append(rows, siteRow{SiteInfo: info, SizeText: humanBytes(info.Bytes), CSRF: token})
		}
	}
	tier := acc.Tier
	if l, ok := p.cfg.Tier(acc.Tier); ok && l.Label != "" {
		tier = l.Label
	}
	checkout := p.checkoutProvider()
	var opts []tierOption
	showPlans := p.cfg.SelfSelect || checkout != ""
	if showPlans {
		for _, t := range p.cfg.Tiers {
			label := t.Label
			if label == "" {
				label = t.ID
			}
			price := ""
			if t.Price != nil {
				price = t.Price.Display
			}
			opts = append(opts, tierOption{ID: t.ID, Label: label, Current: t.ID == acc.Tier, Paid: t.Paid(), Price: price})
		}
	}
	dashTmpl.Execute(w, dashView{
		Email: acc.Email, Tier: tier, Sites: rows, CSRF: token, Base: p.baseURL(),
		SelfSelect: p.cfg.SelfSelect, Checkout: checkout, Tiers: opts,
	})
}

type msgView struct {
	Title  string
	Body   string
	Detail string
	Back   string
}

func (p *provider) renderMessage(w http.ResponseWriter, v msgView) {
	p.securityHeaders(w)
	msgTmpl.Execute(w, v)
}

func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	const units = "KMGT"
	f := float64(n)
	i := -1
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if f >= 100 {
		return fmt.Sprintf("%.0f %cB", f, units[i])
	}
	return fmt.Sprintf("%.1f %cB", f, units[i])
}
