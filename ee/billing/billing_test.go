//go:build ee

package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/eeconfig"
)

func sign(secret, payload string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

func TestStripeWebhookVerifiesAndParses(t *testing.T) {
	s := NewStripe(eeconfig.StripeConfig{WebhookSecret: "whsec_test"})
	now := time.Unix(1_700_000_000, 0)
	body := `{"type":"checkout.session.completed","data":{"object":{"client_reference_id":"acct-1","customer":"cus_1","subscription":"sub_1","metadata":{"account":"acct-1","tier":"pro"}}}}`
	ts := fmt.Sprintf("%d", now.Unix())
	sig := "t=" + ts + ",v1=" + sign("whsec_test", ts+"."+body)

	u, err := s.VerifyWebhook(sig, []byte(body), now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if u.AccountID != "acct-1" || u.TierID != "pro" || u.Customer != "cus_1" || u.Status != "active" {
		t.Errorf("update wrong: %+v", u)
	}
}

func TestStripeWebhookRejectsBadSignature(t *testing.T) {
	s := NewStripe(eeconfig.StripeConfig{WebhookSecret: "whsec_test"})
	now := time.Unix(1_700_000_000, 0)
	body := `{"type":"checkout.session.completed","data":{"object":{}}}`
	ts := fmt.Sprintf("%d", now.Unix())
	// signature computed with the WRONG secret
	sig := "t=" + ts + ",v1=" + sign("whsec_WRONG", ts+"."+body)
	if _, err := s.VerifyWebhook(sig, []byte(body), now); err == nil {
		t.Fatal("bad signature accepted")
	}
	// stale timestamp
	good := "t=" + ts + ",v1=" + sign("whsec_test", ts+"."+body)
	if _, err := s.VerifyWebhook(good, []byte(body), now.Add(10*time.Minute)); err == nil {
		t.Fatal("stale timestamp accepted")
	}
}

func TestStripeSubscriptionDeletedCancels(t *testing.T) {
	s := NewStripe(eeconfig.StripeConfig{WebhookSecret: "whsec_test"})
	now := time.Unix(1_700_000_000, 0)
	body := `{"type":"customer.subscription.deleted","data":{"object":{"customer":"cus_9","metadata":{"account":"acct-9"}}}}`
	ts := fmt.Sprintf("%d", now.Unix())
	sig := "t=" + ts + ",v1=" + sign("whsec_test", ts+"."+body)
	u, err := s.VerifyWebhook(sig, []byte(body), now)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Canceled || u.AccountID != "acct-9" {
		t.Errorf("cancel update wrong: %+v", u)
	}
}

func TestStripeIgnoredEvent(t *testing.T) {
	s := NewStripe(eeconfig.StripeConfig{WebhookSecret: "whsec_test"})
	now := time.Unix(1_700_000_000, 0)
	body := `{"type":"invoice.paid","data":{"object":{}}}`
	ts := fmt.Sprintf("%d", now.Unix())
	sig := "t=" + ts + ",v1=" + sign("whsec_test", ts+"."+body)
	_, err := s.VerifyWebhook(sig, []byte(body), now)
	if !IsIgnored(err) {
		t.Fatalf("expected ignored-event, got %v", err)
	}
}

func TestPaddleWebhookVerifiesAndParses(t *testing.T) {
	p := NewPaddle(eeconfig.PaddleConfig{WebhookSecret: "pdl_secret"})
	now := time.Unix(1_700_000_000, 0)
	body := `{"event_type":"subscription.activated","data":{"id":"sub_1","customer_id":"ctm_1","status":"active","custom_data":{"account":"acct-2","tier":"pro"}}}`
	ts := fmt.Sprintf("%d", now.Unix())
	sig := "ts=" + ts + ";h1=" + sign("pdl_secret", ts+":"+body)

	u, err := p.VerifyWebhook(sig, []byte(body), now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if u.AccountID != "acct-2" || u.TierID != "pro" || u.Customer != "ctm_1" || u.Subscription != "sub_1" {
		t.Errorf("update wrong: %+v", u)
	}
}

func TestPaddleWebhookRejectsBadSignature(t *testing.T) {
	p := NewPaddle(eeconfig.PaddleConfig{WebhookSecret: "pdl_secret"})
	now := time.Unix(1_700_000_000, 0)
	body := `{"event_type":"subscription.activated","data":{}}`
	ts := fmt.Sprintf("%d", now.Unix())
	sig := "ts=" + ts + ";h1=" + sign("WRONG", ts+":"+body)
	if _, err := p.VerifyWebhook(sig, []byte(body), now); err == nil {
		t.Fatal("bad paddle signature accepted")
	}
}

func TestPaddleCancellation(t *testing.T) {
	p := NewPaddle(eeconfig.PaddleConfig{WebhookSecret: "pdl_secret"})
	now := time.Unix(1_700_000_000, 0)
	body := `{"event_type":"subscription.canceled","data":{"id":"sub_2","customer_id":"ctm_2","status":"canceled","custom_data":{"account":"acct-3"}}}`
	ts := fmt.Sprintf("%d", now.Unix())
	sig := "ts=" + ts + ";h1=" + sign("pdl_secret", ts+":"+body)
	u, err := p.VerifyWebhook(sig, []byte(body), now)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Canceled {
		t.Errorf("cancel update wrong: %+v", u)
	}
}

// Cancellation goes to the provider's own endpoint with immediate effect, and
// a subscription the provider has already dropped counts as cancelled.
func TestStripeCancelSubscription(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/v1/subscriptions/sub_gone":
			w.WriteHeader(404)
		case "/v1/subscriptions/sub_bad":
			w.WriteHeader(402)
			w.Write([]byte(`{"error":{"message":"nope"}}`))
		default:
			w.Write([]byte(`{"id":"sub_1","status":"canceled"}`))
		}
	}))
	defer srv.Close()
	s := NewStripe(eeconfig.StripeConfig{SecretKey: "sk_test"})
	s.apiBase = srv.URL
	if err := s.CancelSubscription(context.Background(), Customer{Subscription: "sub_1"}); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" || gotPath != "/v1/subscriptions/sub_1" || gotAuth != "Bearer sk_test" {
		t.Errorf("cancel request = %s %s %q", gotMethod, gotPath, gotAuth)
	}
	if err := s.CancelSubscription(context.Background(), Customer{Subscription: "sub_gone"}); err != nil {
		t.Errorf("an already-gone subscription must count as cancelled: %v", err)
	}
	if err := s.CancelSubscription(context.Background(), Customer{Subscription: "sub_bad"}); err == nil {
		t.Error("a provider refusal must be an error")
	}
	if err := s.CancelSubscription(context.Background(), Customer{}); err == nil {
		t.Error("no subscription id must be an error, not a silent success")
	}
}

func TestPaddleCancelSubscription(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotMethod, gotPath, gotBody = r.Method, r.URL.Path, string(b)
		if r.URL.Path == "/subscriptions/sub_gone/cancel" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"data":{"id":"sub_1","status":"canceled"}}`))
	}))
	defer srv.Close()
	p := NewPaddle(eeconfig.PaddleConfig{APIKey: "pdl"})
	p.apiBase = srv.URL
	if err := p.CancelSubscription(context.Background(), Customer{Subscription: "sub_1"}); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/subscriptions/sub_1/cancel" || !strings.Contains(gotBody, "immediately") {
		t.Errorf("cancel request = %s %s %s", gotMethod, gotPath, gotBody)
	}
	if err := p.CancelSubscription(context.Background(), Customer{Subscription: "sub_gone"}); err != nil {
		t.Errorf("an already-gone subscription must count as cancelled: %v", err)
	}
}
