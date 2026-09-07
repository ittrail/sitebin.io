//go:build ee

package ee

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A signed webhook could be replayed: the timestamp check only refused OLD
// ones, so a captured event stamped in the future stayed valid for ever, and
// no event id was remembered, so a captured checkout.session.completed could
// re-upgrade an account after its subscription was cancelled.

func stripeSigned(secret, body string, at time.Time) string {
	ts := fmt.Sprintf("%d", at.Unix())
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "." + body))
	return "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

func postWebhook(mux http.Handler, body, sig string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/account/billing/stripe/webhook", strings.NewReader(body))
	r.Header.Set("Stripe-Signature", sig)
	return serve(mux, r)
}

func TestWebhookEventIsAppliedOnceOnly(t *testing.T) {
	p := setupBilling(t)
	mux := serveMux(p)
	acc, _ := p.local.Signup("replay@example.com", "password123", "free")

	upgrade := `{"id":"evt_up","type":"checkout.session.completed","data":{"object":{"client_reference_id":"` + acc.ID + `","customer":"cus_r","subscription":"sub_r","metadata":{"account":"` + acc.ID + `","tier":"pro"}}}}`
	sig := stripeSigned("whsec_test", upgrade, time.Now())
	if w := postWebhook(mux, upgrade, sig); w.Code != 200 {
		t.Fatalf("first delivery = %d %s", w.Code, w.Body)
	}
	if a, _ := p.accounts.ByID(acc.ID); a.Tier != "pro" {
		t.Fatalf("not upgraded: %q", a.Tier)
	}
	cancel := `{"id":"evt_cancel","type":"customer.subscription.deleted","data":{"object":{"customer":"cus_r","metadata":{"account":"` + acc.ID + `"}}}}`
	if w := postWebhook(mux, cancel, stripeSigned("whsec_test", cancel, time.Now())); w.Code != 200 {
		t.Fatalf("cancel = %d %s", w.Code, w.Body)
	}
	if a, _ := p.accounts.ByID(acc.ID); a.Tier != "free" {
		t.Fatalf("not downgraded: %q", a.Tier)
	}
	// The captured upgrade, replayed inside the signature window: acknowledged
	// (Stripe must not retry it) but NOT applied.
	if w := postWebhook(mux, upgrade, sig); w.Code != 200 {
		t.Fatalf("replay = %d %s", w.Code, w.Body)
	}
	if a, _ := p.accounts.ByID(acc.ID); a.Tier != "free" {
		t.Fatalf("a replayed checkout event re-upgraded the account to %q", a.Tier)
	}
}

func TestWebhookFromTheFutureIsRefused(t *testing.T) {
	p := setupBilling(t)
	mux := serveMux(p)
	body := `{"id":"evt_future","type":"customer.subscription.deleted","data":{"object":{"customer":"cus_x"}}}`
	if w := postWebhook(mux, body, stripeSigned("whsec_test", body, time.Now().Add(time.Hour))); w.Code != 400 {
		t.Fatalf("a webhook stamped an hour ahead = %d, want 400: a future timestamp is a replay that never expires", w.Code)
	}
}
