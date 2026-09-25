//go:build ee

package ee

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/eeconfig"
)

// consentStack stands in for the stack's consent-status endpoint. It answers
// what the test sets and records what it was asked.
type consentStack struct {
	srv      *httptest.Server
	requests atomic.Int32

	mu     sync.Mutex
	status int
	body   string
	delay  time.Duration
	paths  []string
	auths  []string
}

func newConsentStack(t *testing.T) *consentStack {
	t.Helper()
	cs := &consentStack{status: 200, body: completeBody}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.requests.Add(1)
		cs.mu.Lock()
		cs.paths = append(cs.paths, r.URL.Path)
		cs.auths = append(cs.auths, r.Header.Get("Authorization"))
		status, body, delay := cs.status, cs.body, cs.delay
		cs.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(cs.srv.Close)
	return cs
}

func (cs *consentStack) answer(status int, body string) {
	cs.mu.Lock()
	cs.status, cs.body = status, body
	cs.mu.Unlock()
}

const (
	completeBody    = `{"appId":"sitebin","userId":"` + testSubject + `","gate":"enabled","complete":true,"outstanding":[]}`
	outstandingBody = `{"appId":"sitebin","userId":"` + testSubject + `","gate":"enabled","complete":false,` +
		`"outstanding":[{"scope":"app","key":"terms","version":"2026-09","required":true},{"scope":"platform","key":"privacy","version":"3","required":true}]}`
)

func (cs *consentStack) checker() *stackConsent {
	return newStackConsent(&eeconfig.StackConfig{URL: cs.srv.URL, AppID: "sitebin", AdminKey: "stack-admin-key"})
}

func TestConsentCompleteAllows(t *testing.T) {
	cs := newConsentStack(t)
	if !cs.checker().complete(context.Background(), testSubject) {
		t.Fatal("complete consent was refused")
	}
	if want := "/api/v1/apps/sitebin/users/" + testSubject + "/consent/status"; len(cs.paths) != 1 || cs.paths[0] != want {
		t.Errorf("asked %v, want %s", cs.paths, want)
	}
	if cs.auths[0] != "Bearer stack-admin-key" {
		t.Errorf("Authorization = %q", cs.auths[0])
	}
}

func TestConsentOutstandingRefuses(t *testing.T) {
	cs := newConsentStack(t)
	cs.answer(200, outstandingBody)
	if cs.checker().complete(context.Background(), testSubject) {
		t.Fatal("a person with outstanding documents was let through")
	}
}

// Fails closed: a stack that cannot answer means Sitebin cannot know, and a
// token is refused rather than waved through.
func TestConsentFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"server error", 500, `{"error":"db down"}`},
		{"not found", 404, `{"error":"no such user"}`},
		{"unauthorized", 401, `{"error":"bad key"}`},
		{"not json", 200, `<html>proxy error</html>`},
		{"no verdict", 200, `{"appId":"sitebin","userId":"` + testSubject + `","gate":"enabled"}`},
		{"verdict not a boolean", 200, `{"complete":"true"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cs := newConsentStack(t)
			cs.answer(c.status, c.body)
			if cs.checker().complete(context.Background(), testSubject) {
				t.Fatal("an unusable answer let the token through")
			}
		})
	}

	t.Run("unreachable", func(t *testing.T) {
		cs := newConsentStack(t)
		c := cs.checker()
		cs.srv.Close()
		if c.complete(context.Background(), testSubject) {
			t.Fatal("an unreachable stack let the token through")
		}
	})

	t.Run("too slow", func(t *testing.T) {
		cs := newConsentStack(t)
		cs.mu.Lock()
		cs.delay = 2 * time.Second
		cs.mu.Unlock()
		c := cs.checker()
		c.timeout = 100 * time.Millisecond
		start := time.Now()
		if c.complete(context.Background(), testSubject) {
			t.Fatal("a stack that did not answer in time let the token through")
		}
		if waited := time.Since(start); waited > 1500*time.Millisecond {
			t.Errorf("waited %v for a stack past its timeout", waited)
		}
	})

	// The admin key acts on every app on the stack; it is never carried to
	// wherever a redirect points.
	t.Run("redirect", func(t *testing.T) {
		var leaked atomic.Bool
		elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				leaked.Store(true)
			}
			w.Write([]byte(completeBody))
		}))
		t.Cleanup(elsewhere.Close)
		redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
		}))
		t.Cleanup(redirecting.Close)
		c := newStackConsent(&eeconfig.StackConfig{URL: redirecting.URL, AppID: "sitebin", AdminKey: "stack-admin-key"})
		if c.complete(context.Background(), testSubject) {
			t.Error("a redirect was followed to a verdict")
		}
		if leaked.Load() {
			t.Error("the admin key was sent to the redirect target")
		}
	})
}

// The subject goes into the URL, so it must be a stack user id and nothing
// that could walk the path.
func TestConsentRefusesASubjectThatIsNotAUUID(t *testing.T) {
	cs := newConsentStack(t)
	c := cs.checker()
	for _, sub := range []string{"", "auth0|12345", "../../../apps/other", testSubject + "/../x", "7c9e6679742540de944be07fc1f90ae7"} {
		if c.complete(context.Background(), sub) {
			t.Errorf("subject %q was let through", sub)
		}
	}
	if n := cs.requests.Load(); n != 0 {
		t.Errorf("%d requests for subjects that are not stack user ids", n)
	}
}

// Positive answers are cached: the stack is not asked on every MCP call, and
// a short outage does not lock out everyone already in. Negative answers never
// are: the first request after the person accepts must succeed.
func TestConsentCachesOnlyPositiveAnswers(t *testing.T) {
	cs := newConsentStack(t)
	c := cs.checker()
	now := time.Now()
	c.now = func() time.Time { return now }

	cs.answer(200, outstandingBody)
	for i := 0; i < 2; i++ {
		if c.complete(context.Background(), testSubject) {
			t.Fatal("outstanding consent was let through")
		}
	}
	if n := cs.requests.Load(); n != 2 {
		t.Fatalf("%d requests for two refusals, want 2: a refusal was cached", n)
	}

	cs.answer(200, completeBody)
	if !c.complete(context.Background(), testSubject) {
		t.Fatal("the request right after accepting was refused")
	}
	cs.answer(500, `{}`) // the stack goes down; the answer already given stands
	for i := 0; i < 3; i++ {
		if !c.complete(context.Background(), testSubject) {
			t.Fatal("a cached positive answer was not used")
		}
	}
	if n := cs.requests.Load(); n != 3 {
		t.Errorf("%d requests, want 3: the positive answer was not cached", n)
	}

	now = now.Add(consentCacheTTL + time.Second)
	if c.complete(context.Background(), testSubject) {
		t.Error("a positive answer outlived its ten minutes")
	}
}

func TestConsentCacheIsBounded(t *testing.T) {
	cs := newConsentStack(t)
	c := cs.checker()
	c.max = 8
	for i := 0; i < 20; i++ {
		sub := fmt.Sprintf("7c9e6679-7425-40de-944b-%012d", i)
		if !c.complete(context.Background(), sub) {
			t.Fatalf("subject %d refused", i)
		}
	}
	c.mu.Lock()
	n := len(c.passed)
	c.mu.Unlock()
	if n > 8 {
		t.Errorf("the cache holds %d subjects, over its bound of 8", n)
	}
}
