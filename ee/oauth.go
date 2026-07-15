//go:build ee

package ee

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/authn"
	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/ids"
)

const oauthCookie = "sitebin_oauth"

// oauthRoutes adds the OAuth start + callback endpoints when any provider is
// configured.
func (p *provider) oauthRoutes(routes map[string]http.Handler) {
	if !p.cfg.OAuthEnabled() {
		return
	}
	routes["GET /account/auth/{provider}"] = http.HandlerFunc(p.handleOAuthStart)
	routes["GET /account/auth/{provider}/callback"] = http.HandlerFunc(p.handleOAuthCallback)
}

func (p *provider) oauthSigner() auth.TokenSigner { return auth.TokenSigner{Secret: p.secret} }

func (p *provider) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	prov := account.Provider(r.PathValue("provider"))
	if !p.oidc.Configured(prov) {
		http.NotFound(w, r)
		return
	}
	state, nonce := ids.New(), ids.New()
	// Bind state+nonce in a short-lived signed cookie to defeat CSRF/replay.
	token := p.oauthSigner().Sign(string(prov)+"|"+state+"|"+nonce, time.Now(), 10*time.Minute)
	http.SetCookie(w, &http.Cookie{
		Name: oauthCookie, Value: token, Path: "/account/auth", MaxAge: 600,
		HttpOnly: true, Secure: !p.host.HTTPOnly(), SameSite: http.SameSiteLaxMode,
	})
	url, err := p.oidc.AuthCodeURL(r.Context(), prov, state, nonce)
	if err != nil {
		p.renderMessage(w, msgView{Title: "Sign-in unavailable", Body: "This provider is temporarily unavailable. Try again shortly.", Back: "/account/login"})
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (p *provider) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	prov := account.Provider(r.PathValue("provider"))
	if !p.oidc.Configured(prov) {
		http.NotFound(w, r)
		return
	}
	c, err := r.Cookie(oauthCookie)
	if err != nil {
		p.oauthError(w, "The sign-in session expired. Please try again.")
		return
	}
	subject, ok := p.oauthSigner().Parse(c.Value, time.Now())
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: "", Path: "/account/auth", MaxAge: -1})
	if !ok {
		p.oauthError(w, "Invalid sign-in session. Please try again.")
		return
	}
	parts := strings.SplitN(subject, "|", 3)
	if len(parts) != 3 || parts[0] != string(prov) || r.URL.Query().Get("state") != parts[1] {
		p.oauthError(w, "Sign-in verification failed. Please try again.")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		p.oauthError(w, "The provider reported an error: "+e)
		return
	}
	id, err := p.oidc.Exchange(r.Context(), prov, r.URL.Query().Get("code"), parts[2])
	if err != nil {
		p.oauthError(w, "Could not complete sign-in. Please try again.")
		return
	}
	acc, err := p.linkOrCreateOAuth(id)
	if err != nil {
		p.oauthError(w, err.Error())
		return
	}
	http.SetCookie(w, p.sessions.Cookie(acc.ID, acc.TokenVersion))
	p.redirect(w, r, "/account")
}

func (p *provider) oauthError(w http.ResponseWriter, msg string) {
	p.renderMessage(w, msgView{Title: "Sign-in failed", Body: msg, Back: "/account/login"})
}

// linkOrCreateOAuth resolves an OAuth identity to an account: an existing
// linked identity logs in; otherwise a new account is created. A collision
// with an existing email is reported rather than silently linked.
func (p *provider) linkOrCreateOAuth(id authn.Identity) (*account.Account, error) {
	if acc, err := p.accounts.ByOAuth(id.Provider, id.Subject); err == nil {
		return acc, nil
	}
	acc, err := p.accounts.CreateOAuth(id.Provider, id.Subject, id.Email, p.tierForNewAccount())
	if err == account.ErrEmailTaken {
		return nil, fmt.Errorf("an account with %s already exists — sign in with your password instead", id.Email)
	}
	return acc, err
}
