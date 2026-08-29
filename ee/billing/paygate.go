//go:build ee

package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/ee/eeconfig"
)

// failTTL is how long a failed PayGate lookup is remembered, so an outage
// neither hammers PayGate nor stalls every site creation on a slow timeout.
const failTTL = 30 * time.Second

// activeStatuses are the subscription states whose tier Sitebin honors.
// past_due keeps access during dunning; cancellation/expiry falls back.
var activeStatuses = map[string]bool{"active": true, "trialing": true, "past_due": true}

// PayGate resolves a user's subscription tier from a SaaS-Stack PayGate via
// the admin-by-user-id endpoint (API key only, no user JWTs). Results are
// cached per user.
type PayGate struct {
	cfg  eeconfig.PayGateConfig
	http *http.Client
	now  func() time.Time // injectable for tests

	mu    sync.Mutex
	cache map[string]pgEntry
}

type pgEntry struct {
	tier  string
	ok    bool
	err   error
	until time.Time
}

// NewPayGate builds the client. cfg is assumed validated by eeconfig.Load.
func NewPayGate(cfg eeconfig.PayGateConfig) *PayGate {
	return &PayGate{
		cfg:   cfg,
		http:  &http.Client{Timeout: 5 * time.Second},
		now:   time.Now,
		cache: map[string]pgEntry{},
	}
}

// Name identifies the backend in logs. It is never shown to a customer: which
// processor the stack charges through is the stack's business, and surfacing it
// would create exactly the dependency this backend exists to avoid.
func (g *PayGate) Name() string { return eeconfig.BackendPayGate }

// ManageURL returns the configured "manage subscription" link ("" if unset).
// It is only a fallback for an instance with no backend; a PayGate instance
// gets a real portal from PortalURL.
func (g *PayGate) ManageURL() string { return g.cfg.ManageURL }

// CheckoutURL sells a tier by NAME. Sitebin never sends a provider price id,
// because it must not know one: the stack resolves the tier to whatever the
// processor it currently uses calls that price.
func (g *PayGate) CheckoutURL(ctx context.Context, c Customer, tier eeconfig.Tier, successURL, cancelURL string) (string, error) {
	if c.Subject == "" {
		// PayGate knows people by the identity the stack issued. A locally
		// authenticated account has no subscription there and never will.
		return "", fmt.Errorf("paygate checkout: account %s has no stack identity", c.AccountID)
	}
	body := map[string]string{
		"tier":        tier.ID,
		"success_url": successURL,
	}
	if cancelURL != "" {
		body["cancel_url"] = cancelURL
	}
	if c.Email != "" {
		body["customer_email"] = c.Email
	}
	var out struct {
		Data struct {
			CheckoutURL string `json:"checkoutUrl"`
		} `json:"data"`
	}
	if err := g.post(ctx, "checkout", c.Subject, body, &out); err != nil {
		return "", err
	}
	if out.Data.CheckoutURL == "" {
		return "", fmt.Errorf("paygate checkout: no checkout url in response")
	}
	return out.Data.CheckoutURL, nil
}

// PortalURL returns the stack's billing portal for an existing subscriber, or
// "" when they have never paid -- which is not an error, it just means the
// dashboard shows the plans instead.
func (g *PayGate) PortalURL(ctx context.Context, c Customer, returnURL string) (string, error) {
	if c.Subject == "" {
		return "", nil
	}
	body := map[string]string{}
	if returnURL != "" {
		body["return_url"] = returnURL
	}
	var out struct {
		Data struct {
			PortalURL string `json:"portal_url"`
		} `json:"data"`
	}
	if err := g.post(ctx, "billing-portal", c.Subject, body, &out); err != nil {
		return "", err
	}
	return out.Data.PortalURL, nil
}

// post calls one of the app-authenticated per-user endpoints. Sitebin acts for
// a user with its app API key; it never holds a user's stack token.
func (g *PayGate) post(ctx context.Context, action, subject string, body map[string]string, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/api/v1/%s/users/%s/%s",
		g.cfg.URL, url.PathEscape(g.cfg.AppID), url.PathEscape(subject), action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.cfg.APIKey)

	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("paygate %s: %w", action, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var buf bytes.Buffer
		buf.ReadFrom(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("paygate %s: status %d: %s", action, resp.StatusCode, strings.TrimSpace(buf.String()))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// TierFor returns the stack tier id for a user. ok=false with err=nil means
// PayGate answered but the subscription must not be honored (canceled,
// expired, or user unknown); err != nil means the lookup failed and the
// caller should fall back to its stored state.
func (g *PayGate) TierFor(ctx context.Context, userID string) (tier string, ok bool, err error) {
	g.mu.Lock()
	if e, hit := g.cache[userID]; hit && g.now().Before(e.until) {
		g.mu.Unlock()
		return e.tier, e.ok, e.err
	}
	g.mu.Unlock()

	tier, ok, err = g.fetch(ctx, userID)
	ttl := g.cfg.CacheTTL
	if err != nil {
		ttl = failTTL
	}
	g.mu.Lock()
	g.cache[userID] = pgEntry{tier: tier, ok: ok, err: err, until: g.now().Add(ttl)}
	g.mu.Unlock()
	return tier, ok, err
}

func (g *PayGate) fetch(ctx context.Context, userID string) (string, bool, error) {
	u := fmt.Sprintf("%s/api/v1/%s/users/%s/subscription",
		g.cfg.URL, url.PathEscape(g.cfg.AppID), url.PathEscape(userID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+g.cfg.APIKey)
	resp, err := g.http.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("paygate: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", false, nil // user unknown to PayGate → no subscription
	case resp.StatusCode != http.StatusOK:
		return "", false, fmt.Errorf("paygate: unexpected status %d", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Tier   string `json:"tier"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, fmt.Errorf("paygate: decode: %w", err)
	}
	if body.Data.Tier == "" || !activeStatuses[body.Data.Status] {
		return "", false, nil
	}
	return body.Data.Tier, true, nil
}
