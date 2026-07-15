//go:build ee

package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/eeconfig"
)

// Stripe integrates Stripe Checkout + webhooks via direct HTTP (no SDK).
type Stripe struct {
	cfg  eeconfig.StripeConfig
	http *http.Client
	// apiBase is overridable in tests.
	apiBase string
}

func NewStripe(cfg eeconfig.StripeConfig) *Stripe {
	return &Stripe{cfg: cfg, http: &http.Client{Timeout: 20 * time.Second}, apiBase: "https://api.stripe.com"}
}

// CheckoutURL creates a subscription Checkout Session for the given price and
// returns the hosted checkout URL to redirect the user to. accountID and
// tierID are stored in the session metadata for the webhook.
func (s *Stripe) CheckoutURL(ctx context.Context, priceID, accountID, tierID, email, successURL, cancelURL string) (string, error) {
	form := url.Values{}
	form.Set("mode", "subscription")
	form.Set("line_items[0][price]", priceID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("success_url", successURL)
	form.Set("cancel_url", cancelURL)
	form.Set("client_reference_id", accountID)
	if email != "" {
		form.Set("customer_email", email)
	}
	form.Set("metadata[account]", accountID)
	form.Set("metadata[tier]", tierID)
	form.Set("subscription_data[metadata][account]", accountID)
	form.Set("subscription_data[metadata][tier]", tierID)

	req, err := http.NewRequestWithContext(ctx, "POST", s.apiBase+"/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SecretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("stripe checkout: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("stripe checkout status %d: %s", resp.StatusCode, body)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.URL == "" {
		return "", fmt.Errorf("stripe checkout: no url in response")
	}
	return out.URL, nil
}

var errBadSignature = errors.New("invalid webhook signature")

// VerifyWebhook validates a Stripe-Signature header against the raw body and
// returns the parsed event, following Stripe's signing scheme:
// signed_payload = timestamp + "." + body, compared to the v1 hex HMAC.
func (s *Stripe) VerifyWebhook(sigHeader string, body []byte, now time.Time) (Update, error) {
	var ts string
	var v1s []string
	for _, part := range strings.Split(sigHeader, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			v1s = append(v1s, v)
		}
	}
	if ts == "" || len(v1s) == 0 {
		return Update{}, errBadSignature
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || now.Sub(time.Unix(tsInt, 0)) > 5*time.Minute {
		return Update{}, errBadSignature
	}
	expected := hmacSHA256Hex([]byte(s.cfg.WebhookSecret), []byte(ts+"."+string(body)))
	matched := false
	for _, v1 := range v1s {
		if constEq(v1, expected) {
			matched = true
			break
		}
	}
	if !matched {
		return Update{}, errBadSignature
	}
	return parseStripeEvent(body)
}

// parseStripeEvent maps the relevant Stripe events to an Update.
func parseStripeEvent(body []byte) (Update, error) {
	var ev struct {
		Type string `json:"type"`
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return Update{}, err
	}
	u := Update{Provider: "stripe"}
	switch ev.Type {
	case "checkout.session.completed":
		var o struct {
			ClientReferenceID string `json:"client_reference_id"`
			Customer          string `json:"customer"`
			Subscription      string `json:"subscription"`
			Metadata          struct {
				Account string `json:"account"`
				Tier    string `json:"tier"`
			} `json:"metadata"`
		}
		json.Unmarshal(ev.Data.Object, &o)
		u.AccountID = firstNonEmpty(o.Metadata.Account, o.ClientReferenceID)
		u.Customer = o.Customer
		u.Subscription = o.Subscription
		u.TierID = o.Metadata.Tier
		u.Status = "active"
	case "customer.subscription.updated":
		var o struct {
			Customer string `json:"customer"`
			Status   string `json:"status"`
			Metadata struct {
				Account string `json:"account"`
				Tier    string `json:"tier"`
			} `json:"metadata"`
		}
		json.Unmarshal(ev.Data.Object, &o)
		u.AccountID = o.Metadata.Account
		u.Customer = o.Customer
		u.Status = o.Status
		if o.Status == "active" {
			u.TierID = o.Metadata.Tier
		}
		if o.Status == "canceled" || o.Status == "unpaid" {
			u.Canceled = true
		}
	case "customer.subscription.deleted":
		var o struct {
			Customer string `json:"customer"`
			Metadata struct {
				Account string `json:"account"`
			} `json:"metadata"`
		}
		json.Unmarshal(ev.Data.Object, &o)
		u.AccountID = o.Metadata.Account
		u.Customer = o.Customer
		u.Status = "canceled"
		u.Canceled = true
	default:
		return Update{}, errIgnoredEvent
	}
	return u, nil
}

// errIgnoredEvent signals a well-formed but uninteresting webhook (return 200).
var errIgnoredEvent = errors.New("ignored event")

// IsIgnored reports whether err is a benign ignored-event signal.
func IsIgnored(err error) bool { return errors.Is(err, errIgnoredEvent) }

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
