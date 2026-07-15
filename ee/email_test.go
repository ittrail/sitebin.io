//go:build ee

package ee

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
)

func setupEmail(t *testing.T) *provider {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	// point SMTP at a closed port so best-effort sends fail fast without hanging
	t.Setenv("SITEBIN_SMTP_HOST", "127.0.0.1")
	t.Setenv("SITEBIN_SMTP_PORT", "1")
	t.Setenv("SITEBIN_SMTP_FROM", "no-reply@sitebin.example")
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatal(err)
	}
	if p.mailer == nil {
		t.Fatal("mailer not initialized")
	}
	return p
}

func TestVerifyEmailFlow(t *testing.T) {
	p := setupEmail(t)
	mux := serveMux(p)
	acc, _ := p.local.Signup("v@example.com", "password123", "")
	if acc.EmailVerified {
		t.Fatal("new local account should be unverified")
	}
	token := p.makeToken("verify", acc.ID, 0, 24*time.Hour)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/account/verify?token="+token, nil))
	if !strings.Contains(w.Body.String(), "Email verified") {
		t.Fatalf("verify = %d, body %q", w.Code, w.Body)
	}
	reload, _ := p.accounts.ByID(acc.ID)
	if !reload.EmailVerified {
		t.Error("email not marked verified")
	}

	// tampered token rejected
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/account/verify?token=garbage", nil))
	if !strings.Contains(w.Body.String(), "Verification failed") {
		t.Error("garbage verify token accepted")
	}
}

func TestPasswordResetFlow(t *testing.T) {
	p := setupEmail(t)
	mux := serveMux(p)
	acc, _ := p.local.Signup("r@example.com", "oldpassword", "")

	token := p.makeToken("reset", acc.ID, acc.TokenVersion, time.Hour)

	// confirm form renders for a valid token
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/account/reset/confirm?token="+token, nil))
	if !strings.Contains(w.Body.String(), "Choose a new password") {
		t.Fatalf("confirm form = %d", w.Code)
	}

	// submit new password
	body := url.Values{"token": {token}, "password": {"newpassword1"}}
	req := httptest.NewRequest("POST", "/account/reset/confirm", strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "Password updated") {
		t.Fatalf("reset confirm = %d, body %q", w.Code, w.Body)
	}

	// new password works, old does not
	if _, err := p.local.Login("r@example.com", "newpassword1"); err != nil {
		t.Errorf("login with new password: %v", err)
	}
	if _, err := p.local.Login("r@example.com", "oldpassword"); err == nil {
		t.Error("old password still works")
	}

	// token is single-use (token_version bumped by the reset)
	req = httptest.NewRequest("POST", "/account/reset/confirm", strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "already been used") && !strings.Contains(w.Body.String(), "expired") {
		t.Fatalf("reused reset token accepted: %q", w.Body)
	}
}

func TestResetRequestUniform(t *testing.T) {
	p := setupEmail(t)
	mux := serveMux(p)
	p.local.Signup("exists@example.com", "password123", "")

	post := func(email string) string {
		body := url.Values{"email": {email}}
		req := httptest.NewRequest("POST", "/account/reset", strings.NewReader(body.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Body.String()
	}
	// same uniform response whether or not the email exists (no enumeration)
	if !strings.Contains(post("exists@example.com"), "on its way") {
		t.Error("existing-email reset response wrong")
	}
	if !strings.Contains(post("ghost@example.com"), "on its way") {
		t.Error("unknown-email reset response should be identical")
	}
}
