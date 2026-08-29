//go:build ee

package ee

import (
	"testing"

	"github.com/ittrail/sitebin.io/ee/billing"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
	"github.com/ittrail/sitebin.io/internal/ext"
)

func setupBilling(t *testing.T) *provider {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", tiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	t.Setenv("SITEBIN_STRIPE_SECRET_KEY", "sk_test")
	t.Setenv("SITEBIN_STRIPE_WEBHOOK_SECRET", "whsec_test")
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatal(err)
	}
	if p.billing == nil || p.billing.Name() != eeconfig.BackendStripe {
		t.Fatal("stripe backend not selected")
	}
	return p
}

func TestApplyBillingUpgradeAndCancel(t *testing.T) {
	p := setupBilling(t)
	acc, _ := p.local.Signup("pay@example.com", "password123", "free")
	if err := p.accounts.LinkSite(acc, "abcdefghijklmnopqrstuvwxyz"); err != nil {
		t.Fatal(err)
	}
	p.host.Sites().(*fakeSites).site("abcdefghijklmnopqrstuvwxyz")

	// upgrade to pro via a completed checkout
	err := p.applyBillingUpdate(billing.Update{
		Provider: "stripe", AccountID: acc.ID, Customer: "cus_1", Subscription: "sub_1",
		TierID: "pro", Status: "active",
	})
	if err != nil {
		t.Fatalf("apply upgrade: %v", err)
	}
	reload, _ := p.accounts.ByID(acc.ID)
	if reload.Tier != "pro" || reload.Billing == nil || reload.Billing.Status != "active" || reload.Billing.Customer != "cus_1" {
		t.Fatalf("account not upgraded: %+v", reload)
	}
	sites := p.host.Sites().(*fakeSites)
	g, ok := sites.quotas["abcdefghijklmnopqrstuvwxyz"]
	if !ok || g.MaxSiteBytes != 5000000 || g.MaxExpiryDays != 0 {
		t.Fatalf("site not restamped with pro caps: %+v", g)
	}

	// webhook by customer id alone (no account id) still resolves via index
	if _, err := p.accounts.ByBilling("stripe", "cus_1"); err != nil {
		t.Fatalf("billing index not written: %v", err)
	}
	err = p.applyBillingUpdate(billing.Update{Provider: "stripe", Customer: "cus_1", Canceled: true, Status: "canceled"})
	if err != nil {
		t.Fatalf("apply cancel: %v", err)
	}
	reload, _ = p.accounts.ByID(acc.ID)
	if reload.Tier != "free" {
		t.Errorf("cancellation should revert to default tier, got %q", reload.Tier)
	}
	if reload.Billing.Status != "canceled" {
		t.Errorf("billing status = %q", reload.Billing.Status)
	}
	g, ok = sites.quotas["abcdefghijklmnopqrstuvwxyz"]
	if !ok || g.MaxSiteBytes != 1000 || g.MaxExpiryDays != 7 {
		t.Fatalf("site not restamped with free caps after cancellation: %+v", g)
	}
}

func TestApplyBillingUnknownAccountIsAcknowledged(t *testing.T) {
	p := setupBilling(t)
	// unknown account/customer → no error (webhook acknowledged, nothing to do)
	if err := p.applyBillingUpdate(billing.Update{Provider: "stripe", Customer: "cus_ghost", TierID: "pro"}); err != nil {
		t.Fatalf("unknown account should be acknowledged, got %v", err)
	}
}
