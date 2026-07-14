//go:build ee

package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ittrail/sitebin/ee/eeconfig"
)

// Paddle integrates Paddle Billing checkout + webhooks via direct HTTP.
type Paddle struct {
	cfg     eeconfig.PaddleConfig
	http    *http.Client
	apiBase string
}

func NewPaddle(cfg eeconfig.PaddleConfig) *Paddle {
	base := "https://api.paddle.com"
	if cfg.Sandbox {
		base = "https://sandbox-api.paddle.com"
	}
	return &Paddle{cfg: cfg, http: &http.Client{Timeout: 20 * time.Second}, apiBase: base}
}

// CheckoutURL creates a Paddle transaction for the given price and returns its
// hosted checkout URL. account + tier are stored in custom_data for the
// webhook.
func (p *Paddle) CheckoutURL(ctx context.Context, priceID, accountID, tierID, successURL string) (string, error) {
	payload := map[string]any{
		"items":       []map[string]any{{"price_id": priceID, "quantity": 1}},
		"custom_data": map[string]string{"account": accountID, "tier": tierID},
		"checkout":    map[string]string{"url": successURL},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", p.apiBase+"/transactions", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("paddle checkout: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("paddle checkout status %d: %s", resp.StatusCode, rb)
	}
	var out struct {
		Data struct {
			Checkout struct {
				URL string `json:"url"`
			} `json:"checkout"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rb, &out); err != nil || out.Data.Checkout.URL == "" {
		return "", fmt.Errorf("paddle checkout: no url in response")
	}
	return out.Data.Checkout.URL, nil
}

// VerifyWebhook validates a Paddle-Signature header ("ts=...;h1=...") against
// the raw body: signed_payload = ts + ":" + body, compared to the h1 hex HMAC.
func (p *Paddle) VerifyWebhook(sigHeader string, body []byte, now time.Time) (Update, error) {
	var ts, h1 string
	for _, part := range strings.Split(sigHeader, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "ts":
			ts = v
		case "h1":
			h1 = v
		}
	}
	if ts == "" || h1 == "" {
		return Update{}, errBadSignature
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || now.Sub(time.Unix(tsInt, 0)) > 5*time.Minute {
		return Update{}, errBadSignature
	}
	expected := hmacSHA256Hex([]byte(p.cfg.WebhookSecret), []byte(ts+":"+string(body)))
	if !constEq(h1, expected) {
		return Update{}, errBadSignature
	}
	return parsePaddleEvent(body)
}

func parsePaddleEvent(body []byte) (Update, error) {
	var ev struct {
		EventType string `json:"event_type"`
		Data      struct {
			ID         string `json:"id"`
			CustomerID string `json:"customer_id"`
			Status     string `json:"status"`
			CustomData struct {
				Account string `json:"account"`
				Tier    string `json:"tier"`
			} `json:"custom_data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return Update{}, err
	}
	u := Update{
		Provider:     "paddle",
		AccountID:    ev.Data.CustomData.Account,
		Customer:     ev.Data.CustomerID,
		Subscription: ev.Data.ID,
		Status:       ev.Data.Status,
	}
	switch ev.EventType {
	case "subscription.activated", "subscription.created", "subscription.updated":
		if ev.Data.Status == "active" || ev.Data.Status == "trialing" {
			u.TierID = ev.Data.CustomData.Tier
		}
		if ev.Data.Status == "canceled" || ev.Data.Status == "past_due" {
			u.Canceled = ev.Data.Status == "canceled"
		}
	case "subscription.canceled":
		u.Canceled = true
		u.Status = "canceled"
	default:
		return Update{}, errIgnoredEvent
	}
	return u, nil
}
