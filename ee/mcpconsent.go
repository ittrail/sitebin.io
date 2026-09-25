//go:build ee

package ee

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/ee/eeconfig"
)

// The consent lock.
//
// The stack's consent gate lives on its sign-in, and a client that follows
// Sitebin's metadata passes through it. Steering is not a lock, though: a
// client that ignores the metadata can send the browser to Keycloak's own
// endpoint and come back with a token the person never passed the gate for.
// So the resource server asks the stack itself, before it honours a token,
// whether the person has accepted every document the platform and Sitebin
// currently require. See
// docs/superpowers/specs/2026-09-25-mcp-oauth-consent-lazy-auth-design.md.
//
// It exists only where stack registration is configured — the instance that
// has a gate to ask about. A self-hosted instance pointed at another issuer
// has none, and its tokens are checked as before.

const (
	// consentTimeout bounds one question to the stack, request included.
	consentTimeout = 5 * time.Second
	// consentCacheTTL is how long a "complete" answer is trusted. It spares
	// the stack a request per MCP call and carries everyone already in
	// through a short outage; a document published meanwhile is asked for at
	// the latest this much later.
	consentCacheTTL = 10 * time.Minute
	// consentCacheMax bounds the cache, so a flood of distinct valid tokens
	// cannot grow it without end.
	consentCacheMax = 10000
)

// stackUserID is the shape of a stack user id — Keycloak's UUID, which is
// the OIDC subject. The subject goes into a URL path, so nothing else is let
// near one.
var stackUserID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// stackConsent asks the stack's consent-status endpoint with the admin key,
// and remembers only the answers that let someone in.
type stackConsent struct {
	base     string // the platform API, no trailing slash
	appID    string
	adminKey string
	client   *http.Client
	timeout  time.Duration
	now      func() time.Time
	max      int

	mu     sync.Mutex
	passed map[string]time.Time // subject → until when "complete" holds
}

func newStackConsent(reg *eeconfig.StackConfig) *stackConsent {
	return &stackConsent{
		base:     strings.TrimRight(reg.URL, "/"),
		appID:    reg.AppID,
		adminKey: reg.AdminKey,
		client: &http.Client{
			Timeout: consentTimeout,
			// The admin key acts on every app on the stack; it goes to the
			// configured stack and nowhere a redirect points. A redirect is
			// an answer that is not a verdict, and so a refusal.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		timeout: consentTimeout,
		now:     time.Now,
		max:     consentCacheMax,
		passed:  map[string]time.Time{},
	}
}

// complete reports whether the person has no required document outstanding.
//
// It fails closed. A stack that cannot answer — an error, a timeout, any
// status but 200, a body without a boolean verdict — means Sitebin cannot
// know, and a token is refused rather than waved through. Only a "complete"
// answer is cached: the first request after the person accepts must succeed,
// so a refusal is never remembered.
func (c *stackConsent) complete(ctx context.Context, subject string) bool {
	if !stackUserID.MatchString(subject) {
		slog.Warn("mcp oauth: consent: the token's subject is not a stack user id; refused", "subject", subject)
		return false
	}
	if c.cached(subject) {
		return true
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	u := c.base + "/api/v1/apps/" + url.PathEscape(c.appID) + "/users/" + subject + "/consent/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		slog.Warn("mcp oauth: consent: cannot build the request; refused", "err", err)
		return false
	}
	req.Header.Set("Authorization", "Bearer "+c.adminKey)
	req.Header.Set("Accept", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		slog.Warn("mcp oauth: consent: the stack did not answer; refused", "subject", subject, "err", err)
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		slog.Warn("mcp oauth: consent: the stack refused the question; refused", "subject", subject, "status", res.StatusCode)
		return false
	}
	var status struct {
		Complete    *bool  `json:"complete"`
		Gate        string `json:"gate"`
		Outstanding []struct {
			Scope    string `json:"scope"`
			Key      string `json:"key"`
			Version  string `json:"version"`
			Required bool   `json:"required"`
		} `json:"outstanding"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&status); err != nil || status.Complete == nil {
		slog.Warn("mcp oauth: consent: the stack's answer has no verdict; refused", "subject", subject, "err", err)
		return false
	}
	if !*status.Complete {
		keys := make([]string, 0, len(status.Outstanding))
		for _, o := range status.Outstanding {
			keys = append(keys, o.Scope+":"+o.Key+"@"+o.Version)
		}
		slog.Info("mcp oauth: consent outstanding; token refused until it is accepted",
			"subject", subject, "outstanding", strings.Join(keys, ","))
		return false
	}
	c.remember(subject)
	return true
}

func (c *stackConsent) cached(subject string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.passed[subject]
	if !ok {
		return false
	}
	if c.now().Before(until) {
		return true
	}
	delete(c.passed, subject)
	return false
}

// remember caches a "complete" answer. At the bound it first drops what has
// expired, then — if that was not enough — arbitrary entries: a dropped entry
// costs one more question to the stack, never a wrong answer.
func (c *stackConsent) remember(subject string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.passed) >= c.max {
		for s, until := range c.passed {
			if !now.Before(until) {
				delete(c.passed, s)
			}
		}
		for s := range c.passed {
			if len(c.passed) < c.max {
				break
			}
			delete(c.passed, s)
		}
	}
	c.passed[subject] = now.Add(consentCacheTTL)
}
