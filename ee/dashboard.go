//go:build ee

package ee

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/authn"
	"github.com/ittrail/sitebin.io/ee/billing"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// PublicRoutes mounts the account dashboard and auth endpoints on the main
// domain. All handlers are enterprise-only.
func (p *provider) PublicRoutes() map[string]http.Handler {
	if !p.cfg.Enabled() {
		return nil
	}
	routes := map[string]http.Handler{
		"GET /account":                          http.HandlerFunc(p.handleRoot),
		"GET /account/login":                    http.HandlerFunc(p.handleLoginGet),
		"POST /account/login":                   http.HandlerFunc(p.handleLoginPost),
		"GET /account/signup":                   http.HandlerFunc(p.handleSignupGet),
		"POST /account/signup":                  http.HandlerFunc(p.handleSignupPost),
		"POST /account/logout":                  http.HandlerFunc(p.handleLogout),
		"POST /account/tier":                    http.HandlerFunc(p.handleSelectTier),
		"POST /account/sites/{id}/rotate":       http.HandlerFunc(p.handleRotate),
		"POST /account/sites/{id}/delete":       http.HandlerFunc(p.handleDeleteSite),
		"POST /account/delete":                  http.HandlerFunc(p.handleDeleteAccount),
		"POST /account/delete/confirm":          http.HandlerFunc(p.handleDeleteAccountConfirm),
		"POST /account/tokens":                  http.HandlerFunc(p.handleCreateToken),
		"POST /account/tokens/{id}/delete":      http.HandlerFunc(p.handleDeleteToken),
		"GET /account/admin":                    http.HandlerFunc(p.handleAdmin),
		"POST /account/admin/sites/{id}/delete": http.HandlerFunc(p.handleAdminDelete),
		"POST /account/admin/sites/{id}/expiry": http.HandlerFunc(p.handleAdminExpiry),
	}
	p.oauthRoutes(routes)
	p.emailRoutes(routes)
	p.billingRoutes(routes)
	p.gdprRoutes(routes)
	return routes
}

// canCheckout reports whether a backend is active and can sell a plan. It
// deliberately does not say WHICH: the dashboard posts to one neutral route,
// so the page cannot leak the processor — and there is no longer a preference
// to express, because eeconfig refuses to start with an ambiguous choice.
func (p *provider) canCheckout() bool { return p.billing != nil }

// providerButton is one sign-in option on the login/signup page.
type providerButton struct {
	ID    string // path segment: /account/auth/<id>
	Label string // button text
}

// oauthButtons returns the configured providers for the login/signup page,
// in a stable order with display labels (the generic issuer's label is
// operator-configured, e.g. the SSO product's name).
func (p *provider) oauthButtons() []providerButton {
	labels := map[account.Provider]string{
		account.Google:    "Google",
		account.Microsoft: "Microsoft",
	}
	if p.cfg.OIDC != nil {
		labels[account.OIDCProv] = p.cfg.OIDC.Label
	}
	var out []providerButton
	for _, prov := range []account.Provider{account.OIDCProv, account.Google, account.Microsoft} {
		if p.oidc.Configured(prov) {
			out = append(out, providerButton{ID: string(prov), Label: labels[prov]})
		}
	}
	return out
}

func (p *provider) handleRoot(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	// Sliding lifetime: a render re-issues the cookie for a full term.
	http.SetCookie(w, p.sessions.Cookie(acc.ID, acc.TokenVersion))
	p.renderDashboard(w, acc, "")
}

// ssoRedirect sends the visitor straight to the identity provider when local
// auth is off and exactly one provider is configured — no login mask at all.
// ?stay=1 renders the page instead (used by OAuth error pages to avoid a
// redirect loop).
func (p *provider) ssoRedirect(w http.ResponseWriter, r *http.Request) bool {
	if p.cfg.LocalAuth || r.URL.Query().Get("stay") == "1" {
		return false
	}
	if btns := p.oauthButtons(); len(btns) == 1 {
		p.redirect(w, r, "/account/auth/"+btns[0].ID)
		return true
	}
	return false
}

func (p *provider) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.currentAccount(r); ok {
		p.redirect(w, r, "/account")
		return
	}
	if p.ssoRedirect(w, r) {
		return
	}
	p.renderAuth(w, "login", "", "")
}

func (p *provider) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	if !p.cfg.LocalAuth {
		http.NotFound(w, r)
		return
	}
	email := r.PostFormValue("email")
	// Consulted BEFORE the password is looked at: the limiter is what keeps a
	// guess from costing a 64 MiB hash, so it has to answer first.
	if !p.limits.allowLogin(r, email) {
		p.renderThrottled(w, "login", email)
		return
	}
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
	if p.ssoRedirect(w, r) {
		return
	}
	p.renderAuth(w, "signup", "", "")
}

func (p *provider) handleSignupPost(w http.ResponseWriter, r *http.Request) {
	if !p.cfg.LocalAuth {
		http.NotFound(w, r)
		return
	}
	email := r.PostFormValue("email")
	if !p.limits.allowSignup(r) {
		p.renderThrottled(w, "signup", email)
		return
	}
	acc, err := p.local.Signup(email, r.PostFormValue("password"), p.tierForNewAccount())
	if err != nil {
		msg := "Could not create the account."
		switch err {
		case authn.ErrWeakPassword:
			msg = "Password must be at least 8 characters."
		case authn.ErrPasswordTooLong:
			msg = "Password must be at most 256 characters."
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

// handleLogout ends EVERY session of the account, not just the cookie that
// asked. Sessions are stateless signed cookies, so clearing one changes
// nothing a thief holds; bumping the account's token version — the number
// every cookie is checked against — is the only revocation there is, and
// "sign out everywhere" is what a person clicking Sign out on a shared
// machine means anyway. Behind the CSRF token, because a cross-site form
// signing people out is a nuisance an attacker should not have.
func (p *provider) handleLogout(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		http.SetCookie(w, p.sessions.Clear())
		p.redirect(w, r, "/account/login")
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := p.accounts.Update(acc, func(cur *account.Account) error { cur.TokenVersion++; return nil }); err != nil {
		slog.Error("logout: could not revoke sessions", "account", acc.ID, "err", err)
	}
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
	// acc.Tier is now t.ID, so syncTier would see no difference; restamp directly.
	p.restampSites(acc, t)
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

// deleteConfirmView is the confirmation step of a local account deletion:
// what goes, and whether a subscription has to be ended first.
type deleteConfirmView struct {
	Email        string
	CSRF         string
	Sites        int
	Tokens       int
	Subscription string // the provider's name when a live subscription will be cancelled first
}

// liveSubscription reports whether acc holds a subscription that is still
// charging — one a deletion has to end before the account may go.
func liveSubscription(acc *account.Account) bool {
	return acc.Billing != nil && acc.Billing.Subscription != "" && acc.Billing.Status != "canceled"
}

// handleDeleteAccount is step ONE of a local deletion: it renders the
// confirmation and deletes nothing. The dashboard's CSP has no
// 'unsafe-inline', so a confirm() dialog on the form would silently never
// run — the confirmation has to be a page the server renders, exactly as the
// admin console's is.
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
	// A stack identity is deleted where it lives. The console erases it and
	// the stack orders this instance to erase its half (handleGDPRDelete);
	// deleting here first would leave the identity behind, still a member of
	// this app, with nothing of its own left to come back to.
	if p.stackDeletion(acc) {
		http.Redirect(w, r, p.cfg.AccountConsoleURL(p.baseURL()+"/account"), http.StatusSeeOther)
		return
	}
	ids, _ := p.accounts.ListSiteIDs(acc)
	toks, _ := p.accounts.ListTokens(acc)
	v := deleteConfirmView{Email: acc.Email, CSRF: p.csrf(acc), Sites: len(ids), Tokens: len(toks)}
	if liveSubscription(acc) {
		v.Subscription = acc.Billing.Provider
	}
	p.securityHeaders(w)
	deleteConfirmTmpl.Execute(w, v)
}

// handleDeleteAccountConfirm is step TWO: the subscription is ended first,
// then the account, its sites and its tokens go. It fails closed on the
// subscription — an account that cannot stop being charged is not deleted,
// and the page says so.
func (p *provider) handleDeleteAccountConfirm(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if p.stackDeletion(acc) {
		http.Redirect(w, r, p.cfg.AccountConsoleURL(p.baseURL()+"/account"), http.StatusSeeOther)
		return
	}
	if liveSubscription(acc) {
		if err := p.cancelSubscription(r, acc); err != nil {
			slog.Error("account deletion: subscription could not be cancelled; keeping the account",
				"account", acc.ID, "provider", acc.Billing.Provider, "err", err)
			p.renderMessage(w, msgView{
				Title: "Account not deleted",
				Body:  "Your subscription could not be cancelled, so your account was kept: deleting it now would leave you paying for nothing. Cancel the subscription under Billing first, or try again in a few minutes.",
				Back:  "/account",
			})
			return
		}
	}
	err := p.accounts.Delete(acc, func(viewID string) error {
		err := p.host.Sites().Delete(viewID)
		if errors.Is(err, ext.ErrSiteGone) {
			return nil // a stale ownership marker is not a reason to keep the account
		}
		return err
	})
	if err != nil {
		slog.Error("account deletion failed", "account", acc.ID, "err", err)
		http.Error(w, "could not delete the account", http.StatusInternalServerError)
		return
	}
	slog.Info("account deleted by its owner", "account", acc.ID)
	http.SetCookie(w, p.sessions.Clear())
	p.renderMessage(w, msgView{
		Title: "Account deleted",
		Body:  "Your account and all its sites have been removed.",
		Back:  "/account/login",
	})
}

// cancelSubscription ends acc's live subscription through the active backend.
// The backend has to be the one that issued the subscription AND able to
// cancel: a subscription with a provider this instance no longer runs cannot
// be ended from here, and saying so beats deleting around it.
func (p *provider) cancelSubscription(r *http.Request, acc *account.Account) error {
	if p.billing == nil || p.billing.Name() != acc.Billing.Provider {
		return fmt.Errorf("subscription belongs to %q, which is not the active billing backend", acc.Billing.Provider)
	}
	c, ok := p.billing.(billing.SubscriptionCanceller)
	if !ok {
		return fmt.Errorf("the %s backend cannot cancel subscriptions from here", p.billing.Name())
	}
	if err := c.CancelSubscription(r.Context(), p.billingCustomer(acc)); err != nil {
		return err
	}
	// Recorded before the deletion, so a failure between the two leaves the
	// account saying what the provider now says.
	return p.accounts.Update(acc, func(cur *account.Account) error {
		if cur.Billing != nil {
			cur.Billing.Status = "canceled"
		}
		return nil
	})
}

// ---- rendering ----

func (p *provider) redirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// leaveTo answers a form submission whose destination is another origin.
// Not a redirect: see handoffTmpl for why the browser would drop one.
func (p *provider) leaveTo(w http.ResponseWriter, url, title, body string) {
	p.securityHeaders(w)
	w.WriteHeader(http.StatusOK)
	handoffTmpl.Execute(w, struct{ URL, Title, Body string }{url, title, body})
}

func (p *provider) securityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
			"script-src "+copyScriptCSP+"; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
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
	Providers    []providerButton
	EmailEnabled bool
	LocalAuth    bool
}

func (p *provider) renderAuth(w http.ResponseWriter, mode, email, errMsg string) {
	p.securityHeaders(w)
	authTmpl.Execute(w, authView{
		Mode: mode, Email: email, Error: errMsg,
		Providers: p.oauthButtons(), EmailEnabled: p.mailer != nil,
		LocalAuth: p.cfg.LocalAuth,
	})
}

type siteRow struct {
	ext.SiteInfo
	SizeText   string
	ExpiryText string
	CSRF       string
}

// expiryText is the site row's lifetime line. A downgrade puts a 30-day grace
// date on every site the account owns, and this is where the owner reads it —
// the pricing FAQ promises exactly that.
func expiryText(t *time.Time) string {
	if t == nil {
		return "no expiry"
	}
	return "expires " + t.Local().Format("2006-01-02")
}

type tierOption struct {
	ID      string
	Label   string
	Current bool
	Paid    bool
	Price   string
}

type dashView struct {
	// AccountURL is the identity provider's account console — password,
	// sessions, second factors, data export, account deletion — for an
	// account that signed in through the generic OIDC provider. Sitebin links
	// it and builds none of it. Empty for local accounts and for instances
	// with no such issuer.
	AccountURL string
	// StackDeletion says the account is deleted at the account console, not
	// here: the stack erases its identity there and orders this instance to
	// erase its own half through the GDPR webhook. Deleting locally instead
	// would leave a stack identity behind that still names this app.
	StackDeletion bool
	Email         string
	Tier          string
	Sites         []siteRow
	CSRF          string
	Base          string
	SelfSelect    bool
	// Checkout says a backend can sell a plan. It deliberately does not say
	// which: the page posts to one neutral route so a customer never sees the
	// processor's name, which is what lets the stack change providers.
	Checkout bool
	// Portal shows the "manage subscription" form when the active backend can
	// produce a portal link for this account.
	Portal bool
	Tiers  []tierOption
	// IsAdmin adds the one link to the instance register. Without it the
	// console is reachable only by typing the path, which is a poor secret and
	// a worse feature.
	IsAdmin bool
	Tokens  []tokenRow
	// License is the permanent, non-dismissable licence notice, or nil when
	// there is nothing to say. It is shown HERE and nowhere else: nothing is
	// ever injected into a served site.
	License *licenseNotice
	// MCPEndpoint is where an agent connects. Shown beside the tokens because
	// that is the credential it needs, and a user should not have to read the
	// README to find the URL.
	MCPEndpoint string
}

func (p *provider) renderDashboard(w http.ResponseWriter, acc *account.Account, flash string) {
	p.securityHeaders(w)
	token := p.csrf(acc)
	// Sync first: this render is the first moment a PayGate tier change becomes
	// visible, and it restamps every site's expiry. Reading the rows before it
	// would show the owner the pre-change dates on the one render that matters.
	p.syncTier(acc)
	ids, _ := p.accounts.ListSiteIDs(acc)
	rows := make([]siteRow, 0, len(ids))
	for _, id := range ids {
		if info, ok := p.host.Sites().Info(id); ok {
			rows = append(rows, siteRow{
				SiteInfo:   info,
				SizeText:   humanBytes(info.Bytes),
				ExpiryText: expiryText(info.ExpiresAt),
				CSRF:       token,
			})
		}
	}
	current := p.effectiveTier(acc)
	tier := current.Label
	if tier == "" {
		tier = current.ID
	}
	if tier == "" {
		tier = acc.Tier
	}
	checkout := p.canCheckout()
	var opts []tierOption
	// A backend that can sell can also show a portal, so an existing
	// subscriber gets a way back to their subscription. With PayGate that
	// portal is the stack's hosted plan page.
	portal := p.billing != nil
	if p.cfg.SelfSelect || checkout {
		for _, t := range p.cfg.Tiers {
			label := t.Label
			if label == "" {
				label = t.ID
			}
			// Label formats the amount when there is one and falls back to the
			// display string only when there is not. Reading Display outright
			// printed nothing for every PayGate tier, which carries amounts and
			// no display string: paid plans were offered at a blank price.
			opts = append(opts, tierOption{ID: t.ID, Label: label, Current: t.ID == current.ID, Paid: t.Paid(), Price: t.Price.Label()})
		}
	}
	var accountURL string
	if acc.Provider == account.OIDCProv {
		accountURL = p.cfg.AccountConsoleURL(p.baseURL() + "/account")
	}
	dashTmpl.Execute(w, dashView{
		MCPEndpoint: p.host.BaseURL() + "/mcp",
		Email:       acc.Email, Tier: tier, Sites: rows, CSRF: token, Base: p.baseURL(),
		SelfSelect: p.cfg.SelfSelect, Checkout: checkout, Portal: portal, Tiers: opts,
		AccountURL: accountURL, StackDeletion: p.stackDeletion(acc),
		IsAdmin: p.isAdmin(acc),
		Tokens:  p.tokenRows(acc, token),
		License: p.licenseNotice(),
	})
}

// stackDeletion reports whether acc is deleted at the stack's account console
// rather than here. Only an account the stack issued (OIDC), and only when the
// stack can order the local erasure back — see eeconfig.Config.StackDeletion.
func (p *provider) stackDeletion(acc *account.Account) bool {
	return acc.Provider == account.OIDCProv && p.cfg.StackDeletion()
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

func humanBytes(n int64) string { return store.HumanBytes(n) }
