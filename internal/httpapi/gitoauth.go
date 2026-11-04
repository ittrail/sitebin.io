package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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
// attempt only happens after the window.
type gitOAuthCache struct {
	mu      sync.Mutex
	info    *gitOAuth
	until   time.Time
	checked bool
}

var gitOAuthClient = &http.Client{
	Timeout: 5 * time.Second,
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
// is configured, the MCP OAuth issuer is set and its discovery answered.
func (a *API) gitOAuthInfo(ctx context.Context) *gitOAuth {
	if _, ok := ext.Get(); !ok || a.cfg.GitOAuthClientID == "" || a.cfg.MCPOAuthIssuer == "" {
		return nil
	}
	c := &a.gitOAuthCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.checked && time.Now().Before(c.until) {
		return c.info
	}
	info, err := discoverGitOAuth(ctx, a.cfg.MCPOAuthIssuer, a.cfg.GitOAuthClientID)
	c.checked = true
	if err != nil {
		a.log.Warn("git: OAuth discovery failed; the Git tab offers no account sign-in", slog.Any("err", err))
		c.info, c.until = nil, time.Now().Add(gitOAuthFailedTTL)
		return nil
	}
	c.info, c.until = info, time.Now().Add(gitOAuthTTL)
	return info
}

// discoverGitOAuth reads the steered authorization-server document by RFC 8414
// path insertion: https://host/realms/x becomes
// https://host/.well-known/oauth-authorization-server/realms/x. The request
// uses the issuer's own scheme, so it is https unless the issuer is http.
func discoverGitOAuth(ctx context.Context, issuer, clientID string) (*gitOAuth, error) {
	u, err := url.Parse(issuer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("the OAuth issuer is not an http(s) URL")
	}
	doc := (&url.URL{Scheme: u.Scheme, Host: u.Host,
		Path: "/.well-known/oauth-authorization-server" + strings.TrimRight(u.Path, "/")}).String()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := gitOAuthClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("discovery answered " + resp.Status)
	}
	var md struct {
		Authorize string `json:"authorization_endpoint"`
		Token     string `json:"token_endpoint"`
		Device    string `json:"device_authorization_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, gitOAuthMaxBody)).Decode(&md); err != nil {
		return nil, err
	}
	for _, e := range []string{md.Authorize, md.Token, md.Device} {
		if eu, err := url.Parse(e); err != nil || (eu.Scheme != "https" && eu.Scheme != "http") || eu.Host == "" {
			return nil, errors.New("discovery lacks an authorization, token or device endpoint")
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
