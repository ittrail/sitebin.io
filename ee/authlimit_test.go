//go:build ee

package ee

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Every login attempt costs a 64 MiB Argon2id derivation, and the routes
// took them without limit: unlimited online guessing against any account,
// and a cheap way to pin the whole box. The local-auth routes now carry the
// same token-bucket limiter the site password gate has had all along.

func postFrom(mux http.Handler, path, ip string, v url.Values) *httptest.ResponseRecorder {
	r := form(v)
	r.URL.Path = path
	r.RemoteAddr = ip + ":12345"
	return serve(mux, r)
}

func TestLoginIsRateLimitedPerIP(t *testing.T) {
	p, _, mux := setupAccounts(t)
	if _, err := p.local.Signup("victim@example.com", "correct horse battery", ""); err != nil {
		t.Fatal(err)
	}
	var last *httptest.ResponseRecorder
	limited := false
	for i := 0; i < loginBurst+2; i++ {
		last = postFrom(mux, "/account/login", "203.0.113.5", url.Values{"email": {"victim@example.com"}, "password": {"wrong"}})
		if last.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("%d wrong passwords from one IP were all answered with %d; nothing throttled", loginBurst+2, last.Code)
	}
	if !strings.Contains(last.Body.String(), "Too many") {
		t.Errorf("the throttled page does not say so: %q", last.Body)
	}
	// The right password from the throttled IP is refused too: the limiter
	// answers before the hash is even computed, which is the point.
	w := postFrom(mux, "/account/login", "203.0.113.5", url.Values{"email": {"victim@example.com"}, "password": {"correct horse battery"}})
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("throttled IP with the right password = %d, want 429", w.Code)
	}
	// Another IP is unaffected by that one's budget, but the EMAIL has its
	// own: a distributed guess against one account is still throttled.
	w = postFrom(mux, "/account/login", "203.0.113.6", url.Values{"email": {"other@example.com"}, "password": {"wrong"}})
	if w.Code == http.StatusTooManyRequests {
		t.Error("a different IP and a different email were throttled by the first IP's budget")
	}
}

func TestLoginIsRateLimitedPerEmailAcrossIPs(t *testing.T) {
	_, _, mux := setupAccounts(t)
	limited := false
	for i := 0; i < loginBurst*2; i++ {
		ip := "198.51.100." + string(rune('1'+i%9)) // nine distinct sources
		w := postFrom(mux, "/account/login", ip, url.Values{"email": {"target@example.com"}, "password": {"guess"}})
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("guesses against one email from many IPs were never throttled")
	}
}

func TestSignupIsRateLimitedPerIP(t *testing.T) {
	_, _, mux := setupAccounts(t)
	limited := false
	for i := 0; i < signupBurst+2; i++ {
		email := "spam" + string(rune('a'+i)) + "@example.com"
		w := postFrom(mux, "/account/signup", "203.0.113.9", url.Values{"email": {email}, "password": {"password123"}})
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("account creation from one IP was never throttled")
	}
}

func TestPasswordResetIsRateLimited(t *testing.T) {
	p := setupEmail(t)
	mux := serveMux(p)
	if _, err := p.local.Signup("bomb@example.com", "password123", ""); err != nil {
		t.Fatal(err)
	}
	limited := false
	for i := 0; i < resetBurst+2; i++ {
		w := postFrom(mux, "/account/reset", "203.0.113.7", url.Values{"email": {"bomb@example.com"}})
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("reset mails for one address were never throttled")
	}
}

func TestResetConfirmIsRateLimited(t *testing.T) {
	p := setupEmail(t)
	mux := serveMux(p)
	limited := false
	for i := 0; i < loginBurst+2; i++ {
		w := postFrom(mux, "/account/reset/confirm", "203.0.113.8", url.Values{"token": {"garbage"}, "password": {"newpassword1"}})
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("reset-token guessing from one IP was never throttled")
	}
}
