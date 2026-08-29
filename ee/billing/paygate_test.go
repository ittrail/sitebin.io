//go:build ee

package billing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/eeconfig"
)

func pgServer(t *testing.T, hits *atomic.Int32, tier, status string, code int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/api/v1/sitebin/users/u-1/subscription" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ssk_test_k" {
			t.Errorf("auth = %q", got)
		}
		if code != 200 {
			w.WriteHeader(code)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"tier": tier, "status": status},
		})
	}))
}

func pg(url string, ttl time.Duration) *PayGate {
	return NewPayGate(eeconfig.PayGateConfig{URL: url, AppID: "sitebin", APIKey: "ssk_test_k", CacheTTL: ttl})
}

func TestPayGateTierFor(t *testing.T) {
	cases := []struct {
		name, tier, status string
		wantTier           string
		wantOK             bool
	}{
		{"active pro", "pro", "active", "pro", true},
		{"trialing", "pro", "trialing", "pro", true},
		{"past due keeps access", "pro", "past_due", "pro", true},
		{"canceled not honored", "pro", "canceled", "", false},
		{"expired not honored", "pro", "expired", "", false},
		{"free user", "free", "active", "free", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := pgServer(t, &hits, c.tier, c.status, 200)
			defer srv.Close()
			tier, ok, err := pg(srv.URL, time.Minute).TierFor(context.Background(), "u-1")
			if err != nil {
				t.Fatal(err)
			}
			if tier != c.wantTier || ok != c.wantOK {
				t.Fatalf("TierFor = (%q, %v), want (%q, %v)", tier, ok, c.wantTier, c.wantOK)
			}
		})
	}
}

func TestPayGateCaches(t *testing.T) {
	var hits atomic.Int32
	srv := pgServer(t, &hits, "pro", "active", 200)
	defer srv.Close()
	g := pg(srv.URL, time.Minute)
	for i := 0; i < 3; i++ {
		if _, _, err := g.TierFor(context.Background(), "u-1"); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("server hits = %d, want 1 (cached)", hits.Load())
	}

	// expire the cache → refetch
	g.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, _, err := g.TierFor(context.Background(), "u-1"); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("server hits after expiry = %d, want 2", hits.Load())
	}
}

func TestPayGateFailureIsCachedBriefly(t *testing.T) {
	var hits atomic.Int32
	srv := pgServer(t, &hits, "", "", 500)
	defer srv.Close()
	g := pg(srv.URL, time.Minute)
	if _, _, err := g.TierFor(context.Background(), "u-1"); err == nil {
		t.Fatal("want error on 500")
	}
	// immediate retry served from the negative cache
	if _, _, err := g.TierFor(context.Background(), "u-1"); err == nil {
		t.Fatal("want cached error")
	}
	if hits.Load() != 1 {
		t.Fatalf("server hits = %d, want 1 (failure cached)", hits.Load())
	}
	// after the failure TTL the lookup is retried
	g.now = func() time.Time { return time.Now().Add(failTTL + time.Second) }
	g.TierFor(context.Background(), "u-1")
	if hits.Load() != 2 {
		t.Fatalf("server hits after failure TTL = %d, want 2", hits.Load())
	}
}

func TestPayGateNotFoundNotHonored(t *testing.T) {
	// a 404 (user unknown to PayGate) is a definitive "no subscription", not an
	// outage: ok=false without error, cached for the normal TTL.
	var hits atomic.Int32
	srv := pgServer(t, &hits, "", "", 404)
	defer srv.Close()
	tier, ok, err := pg(srv.URL, time.Minute).TierFor(context.Background(), "u-1")
	if err != nil || ok || tier != "" {
		t.Fatalf("TierFor on 404 = (%q, %v, %v), want (\"\", false, nil)", tier, ok, err)
	}
}

// TestPayGateCheckoutSellsByTierName is the test that guards the whole point of
// this backend: Sitebin must never send a provider price id, because it must
// never know one. If the stack switches processors, nothing here may change.
func TestPayGateCheckoutSellsByTierName(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"checkoutId":"chk_1","checkoutUrl":"https://pay.example/c/1"}}`)
	}))
	defer srv.Close()

	g := NewPayGate(eeconfig.PayGateConfig{URL: srv.URL, AppID: "sitebin", APIKey: "ssk_test"})
	tier := eeconfig.Tier{ID: "pro", Price: &eeconfig.Price{
		Monthly: "9.00", Currency: "EUR",
		// Present, and it must NOT be sent: these belong to an operator's own
		// direct integration, not to the stack.
		Stripe: "price_should_not_be_sent", Paddle: "pri_should_not_be_sent",
	}}
	c := Customer{AccountID: "acct-1", Email: "u@example.com", Subject: "stack-user-9"}

	url, err := g.CheckoutURL(context.Background(), c, tier, "https://sitebin.example/ok", "https://sitebin.example/no")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://pay.example/c/1" {
		t.Errorf("checkout url = %q", url)
	}
	if want := "/api/v1/sitebin/users/stack-user-9/checkout"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotAuth != "Bearer ssk_test" {
		t.Errorf("auth = %q: paygate is called with the app key, never a user token", gotAuth)
	}
	if !strings.Contains(gotBody, `"tier":"pro"`) {
		t.Errorf("body must sell by tier name: %s", gotBody)
	}
	for _, leak := range []string{"price_should_not_be_sent", "pri_should_not_be_sent", "stripe", "paddle"} {
		if strings.Contains(gotBody, leak) {
			t.Errorf("body leaks the payment provider (%q): %s", leak, gotBody)
		}
	}
}

// A local account has no identity the stack knows, so there is nothing to sell
// it — and that must be an error rather than a request PayGate cannot answer.
func TestPayGateCheckoutRefusesAccountWithoutStackIdentity(t *testing.T) {
	g := NewPayGate(eeconfig.PayGateConfig{URL: "https://pg.example", AppID: "sitebin", APIKey: "ssk"})
	_, err := g.CheckoutURL(context.Background(), Customer{AccountID: "acct-1"},
		eeconfig.Tier{ID: "pro"}, "https://s.example/ok", "")
	if err == nil {
		t.Fatal("expected an error for an account with no stack identity")
	}
}
