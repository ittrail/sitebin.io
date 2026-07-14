//go:build ee

package ee

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ittrail/sitebin/ee/account"
)

// emailRoutes adds verification + password-reset endpoints when SMTP is on.
func (p *provider) emailRoutes(routes map[string]http.Handler) {
	if p.mailer == nil {
		return
	}
	routes["GET /account/verify"] = http.HandlerFunc(p.handleVerify)
	routes["GET /account/reset"] = http.HandlerFunc(p.handleResetGet)
	routes["POST /account/reset"] = http.HandlerFunc(p.handleResetPost)
	routes["GET /account/reset/confirm"] = http.HandlerFunc(p.handleResetConfirmGet)
	routes["POST /account/reset/confirm"] = http.HandlerFunc(p.handleResetConfirmPost)
}

// makeToken signs a purpose-scoped, time-limited token bound to an account +
// version. parseToken reverses it.
func (p *provider) makeToken(purpose, accountID string, ver int, ttl time.Duration) string {
	return p.oauthSigner().Sign(purpose+"|"+accountID+"|"+strconv.Itoa(ver), time.Now(), ttl)
}

func (p *provider) parseToken(purpose, token string) (accountID string, ver int, ok bool) {
	subj, ok := p.oauthSigner().Parse(token, time.Now())
	if !ok {
		return "", 0, false
	}
	parts := strings.SplitN(subj, "|", 3)
	if len(parts) != 3 || parts[0] != purpose {
		return "", 0, false
	}
	v, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", 0, false
	}
	return parts[1], v, true
}

// sendVerification emails a verification link (best-effort; logs on failure).
func (p *provider) sendVerification(acc *account.Account) {
	if p.mailer == nil || acc.Email == "" {
		return
	}
	link := p.baseURL() + "/account/verify?token=" + p.makeToken("verify", acc.ID, 0, 24*time.Hour)
	if err := p.mailer.SendVerification(acc.Email, link); err != nil {
		slog.Error("send verification email", "account", acc.ID, "err", err)
	}
}

func (p *provider) handleVerify(w http.ResponseWriter, r *http.Request) {
	id, _, ok := p.parseToken("verify", r.URL.Query().Get("token"))
	if !ok {
		p.renderMessage(w, msgView{Title: "Verification failed", Body: "This link is invalid or has expired.", Back: "/account"})
		return
	}
	acc, err := p.accounts.ByID(id)
	if err != nil {
		p.renderMessage(w, msgView{Title: "Verification failed", Body: "Account not found.", Back: "/account"})
		return
	}
	if !acc.EmailVerified {
		p.accounts.Update(acc, func(cur *account.Account) error { cur.EmailVerified = true; return nil })
	}
	p.renderMessage(w, msgView{Title: "Email verified", Body: "Your email address is confirmed.", Back: "/account"})
}

func (p *provider) handleResetGet(w http.ResponseWriter, r *http.Request) {
	p.securityHeaders(w)
	resetReqTmpl.Execute(w, nil)
}

func (p *provider) handleResetPost(w http.ResponseWriter, r *http.Request) {
	email := r.PostFormValue("email")
	// Best-effort + uniform response: never reveal whether the email exists.
	if acc, err := p.accounts.ByEmail(email); err == nil && acc.PasswordHash != "" {
		link := p.baseURL() + "/account/reset/confirm?token=" + p.makeToken("reset", acc.ID, acc.TokenVersion, time.Hour)
		if err := p.mailer.SendPasswordReset(acc.Email, link); err != nil {
			slog.Error("send reset email", "account", acc.ID, "err", err)
		}
	}
	p.renderMessage(w, msgView{
		Title: "Check your email",
		Body:  "If an account exists for that address, a password-reset link is on its way.",
		Back:  "/account/login",
	})
}

func (p *provider) handleResetConfirmGet(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	id, ver, ok := p.parseToken("reset", token)
	if !ok {
		p.renderMessage(w, msgView{Title: "Reset link invalid", Body: "This link is invalid or has expired.", Back: "/account/reset"})
		return
	}
	acc, err := p.accounts.ByID(id)
	if err != nil || acc.TokenVersion != ver {
		p.renderMessage(w, msgView{Title: "Reset link expired", Body: "This link has already been used or has expired.", Back: "/account/reset"})
		return
	}
	p.securityHeaders(w)
	resetConfirmTmpl.Execute(w, map[string]string{"Token": token, "Error": ""})
}

func (p *provider) handleResetConfirmPost(w http.ResponseWriter, r *http.Request) {
	token := r.PostFormValue("token")
	id, ver, ok := p.parseToken("reset", token)
	if !ok {
		p.renderMessage(w, msgView{Title: "Reset link invalid", Body: "This link is invalid or has expired.", Back: "/account/reset"})
		return
	}
	acc, err := p.accounts.ByID(id)
	if err != nil || acc.TokenVersion != ver {
		p.renderMessage(w, msgView{Title: "Reset link expired", Body: "This link has already been used or has expired.", Back: "/account/reset"})
		return
	}
	// ChangePassword bumps token_version, making this token single-use.
	if err := p.local.ChangePassword(acc, r.PostFormValue("password")); err != nil {
		p.securityHeaders(w)
		resetConfirmTmpl.Execute(w, map[string]string{"Token": token, "Error": "Password must be at least 8 characters."})
		return
	}
	p.renderMessage(w, msgView{Title: "Password updated", Body: "You can now sign in with your new password.", Back: "/account/login"})
}
