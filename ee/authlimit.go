//go:build ee

package ee

import (
	"net/http"
	"strings"

	"github.com/ittrail/sitebin.io/internal/auth"
)

// Rate limits on the local-auth routes.
//
// Every login attempt costs a 64 MiB Argon2id derivation, by design, and the
// routes took them without limit: unlimited online guessing against any
// account, and twenty concurrent POSTs pinned the whole box. Signup filled
// the data dir with accounts; reset turned the operator's SMTP into a mail
// bomb. The limits are the same token bucket the site password gate has had
// from the start (auth.Limiter), keyed twice where two things need
// protecting: the source, and the account it is aimed at.
//
// They are constants rather than configuration. SITEBIN_RATE_AUTH_PER_5MIN
// governs site passwords, where a legitimate visitor may well try several;
// nobody legitimately signs in ten times in five minutes.
const (
	// loginBurst is how many attempts one source, or one email, gets in
	// quick succession; loginPerHour is the sustained rate after that.
	loginBurst   = 10
	loginPerHour = 30
	// signupBurst/signupPerHour bound account creation per source.
	signupBurst   = 5
	signupPerHour = 5
	// resetBurst/resetPerHour bound reset mails per address; the per-source
	// bucket has the login rate, since a reset is a sign-in that failed.
	resetBurst   = 3
	resetPerHour = 3
)

// authLimiters is the set the handlers consult.
type authLimiters struct {
	loginIP    *auth.Limiter // per source
	loginEmail *auth.Limiter // per target address
	signup     *auth.Limiter // per source
	resetEmail *auth.Limiter // per target address
	resetIP    *auth.Limiter // per source
	confirmIP  *auth.Limiter // per source: guessing a reset token
}

func newAuthLimiters() *authLimiters {
	return &authLimiters{
		loginIP:    auth.NewLimiter(loginPerHour, loginBurst),
		loginEmail: auth.NewLimiter(loginPerHour, loginBurst),
		signup:     auth.NewLimiter(signupPerHour, signupBurst),
		resetEmail: auth.NewLimiter(resetPerHour, resetBurst),
		resetIP:    auth.NewLimiter(loginPerHour, loginBurst),
		confirmIP:  auth.NewLimiter(loginPerHour, loginBurst),
	}
}

// emailKey normalizes an address for keying, so two spellings of one address
// share a bucket. An unparseable address keys on its raw form; it fails
// anyway.
func emailKey(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// allowLogin consumes one attempt for the source AND for the address, and
// reports whether both had one to give. Both are consumed even when one
// refuses: a throttled caller must not be able to keep the other bucket full.
func (l *authLimiters) allowLogin(r *http.Request, email string) bool {
	okIP := l.loginIP.Allow(auth.ClientIP(r))
	okEmail := l.loginEmail.Allow(emailKey(email))
	return okIP && okEmail
}

func (l *authLimiters) allowSignup(r *http.Request) bool {
	return l.signup.Allow(auth.ClientIP(r))
}

func (l *authLimiters) allowReset(r *http.Request, email string) bool {
	okIP := l.resetIP.Allow(auth.ClientIP(r))
	okEmail := l.resetEmail.Allow(emailKey(email))
	return okIP && okEmail
}

func (l *authLimiters) allowResetConfirm(r *http.Request) bool {
	return l.confirmIP.Allow(auth.ClientIP(r))
}

// tooManyAttempts is the throttled answer for the login and signup forms.
const tooManyAttempts = "Too many attempts. Please wait a few minutes and try again."

// renderThrottled answers a throttled auth form with a 429 and the form
// itself, so the person at the keyboard sees why rather than a blank error.
func (p *provider) renderThrottled(w http.ResponseWriter, mode, email string) {
	p.securityHeaders(w)
	w.Header().Set("Retry-After", "60")
	w.WriteHeader(http.StatusTooManyRequests)
	authTmpl.Execute(w, authView{
		Mode: mode, Email: email, Error: tooManyAttempts,
		Providers: p.oauthButtons(), EmailEnabled: p.mailer != nil,
		LocalAuth: p.cfg.LocalAuth,
	})
}

// renderThrottledMessage answers a throttled message-style route with a 429.
func (p *provider) renderThrottledMessage(w http.ResponseWriter, v msgView) {
	p.securityHeaders(w)
	w.Header().Set("Retry-After", "60")
	w.WriteHeader(http.StatusTooManyRequests)
	msgTmpl.Execute(w, v)
}
