//go:build ee

package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/ittrail/sitebin/ee/eeconfig"
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

// ManageURL returns the configured "manage subscription" link ("" if unset).
func (g *PayGate) ManageURL() string { return g.cfg.ManageURL }

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
