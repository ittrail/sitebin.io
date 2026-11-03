package httpapi

import (
	"net/http/httptest"
	"testing"
)

func statusGet(t *testing.T, e *env, origin, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/account/status", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != "" {
		req.Header.Set("Cookie", "sb_test="+cookie)
	}
	return e.public(t, req)
}

func TestAccountStatus(t *testing.T) {
	e, _ := frontDoorEnv(t)
	for _, c := range []struct{ name, cookie, want string }{
		{"signed in", "owner-cookie", `{"signed_in":true}`},
		{"no cookie", "", `{"signed_in":false}`},
		{"invalid cookie", "bogus", `{"signed_in":false}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := statusGet(t, e, "", c.cookie)
			if w.Code != 200 || w.Body.String() != c.want {
				t.Fatalf("= %d %q, want %s", w.Code, w.Body, c.want)
			}
			for k, v := range map[string]string{
				"Content-Type":           "application/json",
				"Cache-Control":          "no-store",
				"Vary":                   "Origin",
				"X-Content-Type-Options": "nosniff",
			} {
				if got := w.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
			if sc := w.Header().Values("Set-Cookie"); len(sc) != 0 {
				t.Errorf("Set-Cookie = %v", sc)
			}
		})
	}
}

func TestAccountStatusCORS(t *testing.T) {
	e, _ := frontDoorEnv(t)
	w := statusGet(t, e, frontDoorOrigin, "owner-cookie")
	if w.Header().Get("Access-Control-Allow-Origin") != frontDoorOrigin || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("session origin headers: %v", w.Header())
	}
	for _, o := range []string{"https://evil.example", plainEmbedOrigin, ""} {
		w := statusGet(t, e, o, "owner-cookie")
		if w.Code != 200 || w.Body.String() != `{"signed_in":true}` {
			t.Errorf("origin %q: %d %s", o, w.Code, w.Body)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("origin %q got CORS headers: %v", o, w.Header())
		}
		if w.Header().Get("Vary") != "Origin" {
			t.Errorf("origin %q Vary = %q", o, w.Header().Get("Vary"))
		}
	}
}

func TestAccountStatusCommunity404(t *testing.T) {
	e := newEnv(t, nil)
	if w := statusGet(t, e, "", "owner-cookie"); w.Code != 404 {
		t.Errorf("community = %d, want 404", w.Code)
	}
}
