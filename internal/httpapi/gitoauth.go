package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/mcp"
)

const (
	gitOAuthTTL       = time.Hour
	gitOAuthFailedTTL = time.Minute
	gitOAuthMaxBody   = 64 << 10
	// gitOAuthFetchTimeout bounds one discovery, whoever asked for it.
	gitOAuthFetchTimeout = 5 * time.Second
)

// gitOAuth is what the Git tab hands to the Git Credential Manager so a
// person can sign in with their account. The JSON names are the contract
// with web/static/edit-git.js.
type gitOAuth struct {
	ClientID          string   `json:"client_id"`
	AuthorizeEndpoint string   `json:"authorize_endpoint"`
	TokenEndpoint     string   `json:"token_endpoint"`
	DeviceEndpoint    string   `json:"device_endpoint"`
	Scopes            []string `json:"scopes"`
}

// gitOAuthCache holds the last discovery result, success or failure, until
// it expires. A failure is logged once per failure window, because the next
// attempt only happens after the window. One discovery runs at a time
// (pending), on its own context and outside the mutex: callers wait for it,
// each as long as its own request lasts, and none of them can cut it short.
type gitOAuthCache struct {
	mu      sync.Mutex
	info    *gitOAuth
	until   time.Time
	checked bool
	pending chan struct{} // closed when the running discovery is done; nil when none runs
}

var gitOAuthClient = &http.Client{
	Timeout: gitOAuthFetchTimeout,
	// Never follow a redirect: the steered document is served by the
	// issuer's own host, and a redirect elsewhere is not discovery.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// gitOAuthScopes are what the CLI client asks for: the same site scopes the
// MCP endpoint grants.
func gitOAuthScopes() []string {
	return append([]string{"openid", "offline_access"}, mcp.AllScopes...)
}

// gitOAuthInfo is nil unless sign-in with the account is offered: an
// extension provider is registered (it verifies the tokens), a CLI client id
// is configured, the MCP OAuth issuer is set and its discovery answered. A
// caller whose ctx ends while discovery runs gets nil; the discovery goes on
// and its answer is cached for the next one.
func (a *API) gitOAuthInfo(ctx context.Context) *gitOAuth {
	if _, ok := ext.Get(); !ok || a.cfg.GitOAuthClientID == "" || a.cfg.MCPOAuthIssuer == "" {
		return nil
	}
	c := &a.gitOAuthCache
	c.mu.Lock()
	if c.checked && time.Now().Before(c.until) {
		info := c.info
		c.mu.Unlock()
		return info
	}
	done := c.pending
	if done == nil {
		done = make(chan struct{})
		c.pending = done
		go a.refreshGitOAuth(done)
	}
	c.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

// refreshGitOAuth runs one discovery under a context of its own and caches
// its answer, then closes done.
func (a *API) refreshGitOAuth(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), gitOAuthFetchTimeout)
	defer cancel()
	info, err := discoverGitOAuth(ctx, gitOAuthClient, a.cfg.MCPOAuthIssuer, a.cfg.GitOAuthClientID)
	c := &a.gitOAuthCache
	c.mu.Lock()
	c.checked = true
	if err != nil {
		a.log.Warn("git: OAuth discovery failed; the Git tab offers no account sign-in", slog.Any("err", err))
		c.info, c.until = nil, time.Now().Add(gitOAuthFailedTTL)
	} else {
		c.info, c.until = info, time.Now().Add(gitOAuthTTL)
	}
	c.pending = nil
	c.mu.Unlock()
	close(done)
}

// discoverGitOAuth reads the steered authorization-server document by RFC 8414
// path insertion: https://host/realms/x becomes
// https://host/.well-known/oauth-authorization-server/realms/x. The request
// uses the issuer's own scheme, so it is https unless the issuer is http.
//
// The document must name the configured issuer (RFC 8414 section 3.3: a
// document for another issuer is not this one's), and its endpoints must be
// https unless the issuer itself is http: the Git tab hands them to a
// credential manager that sends a person's sign-in there.
func discoverGitOAuth(ctx context.Context, client *http.Client, issuer, clientID string) (*gitOAuth, error) {
	u, err := url.Parse(issuer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("the OAuth issuer is not an http(s) URL")
	}
	doc := (&url.URL{Scheme: u.Scheme, Host: u.Host,
		Path: "/.well-known/oauth-authorization-server" + strings.TrimRight(u.Path, "/")}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("discovery answered " + resp.Status)
	}
	var md struct {
		Issuer    string `json:"issuer"`
		Authorize string `json:"authorization_endpoint"`
		Token     string `json:"token_endpoint"`
		Device    string `json:"device_authorization_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, gitOAuthMaxBody)).Decode(&md); err != nil {
		return nil, err
	}
	if strings.TrimRight(md.Issuer, "/") != strings.TrimRight(issuer, "/") {
		return nil, fmt.Errorf("discovery names the issuer %q, not the configured %q", md.Issuer, issuer)
	}
	for _, e := range []string{md.Authorize, md.Token, md.Device} {
		eu, err := url.Parse(e)
		if err != nil || (eu.Scheme != "https" && eu.Scheme != "http") || eu.Host == "" {
			return nil, errors.New("discovery lacks an authorization, token or device endpoint")
		}
		if eu.Scheme != "https" && u.Scheme == "https" {
			return nil, fmt.Errorf("discovery names a plain http endpoint %q for an https issuer", e)
		}
	}
	return &gitOAuth{
		ClientID:          clientID,
		AuthorizeEndpoint: md.Authorize,
		TokenEndpoint:     md.Token,
		DeviceEndpoint:    md.Device,
		Scopes:            gitOAuthScopes(),
	}, nil
}
