//go:build ee

package ee

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/billing"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
)

// Local account deletion: a server-rendered confirmation, and the paid
// subscription cancelled BEFORE the account goes.
//
// The old form relied on onsubmit="return confirm(...)", which the dashboard's
// CSP (no 'unsafe-inline', no 'unsafe-hashes') never let run: one mis-click
// deleted the account and every site irreversibly -- and, with a direct
// billing backend, left the subscription charging a customer who no longer
// existed.

// fakeBilling is a direct-style backend that can cancel, and remembers what
// it was asked to cancel.
type fakeBilling struct {
	name      string
	cancelled []string
	cancelErr error
}

func (f *fakeBilling) Name() string { return f.name }
func (f *fakeBilling) CheckoutURL(context.Context, billing.Customer, eeconfig.Tier, string, string) (string, error) {
	return "https://pay.example/checkout", nil
}
func (f *fakeBilling) PortalURL(context.Context, billing.Customer, string) (string, error) {
	return "", nil
}
func (f *fakeBilling) CancelSubscription(_ context.Context, c billing.Customer) error {
	if f.cancelErr != nil {
		return f.cancelErr
	}
	f.cancelled = append(f.cancelled, c.Subscription)
	return nil
}

var _ billing.SubscriptionCanceller = (*fakeBilling)(nil)

// subscribed gives acc a live subscription with the named provider.
func subscribed(t *testing.T, p *provider, acc *account.Account, provider string) {
	t.Helper()
	err := p.accounts.Update(acc, func(cur *account.Account) error {
		cur.Billing = &account.Billing{Provider: provider, Customer: "cus_1", Subscription: "sub_1", Status: "active"}
		cur.Tier = "pro"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var inlineHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)

func TestDashboardHasNoInlineEventHandlers(t *testing.T) {
	p, host, mux := setupAccounts(t)
	acc, cookie := localUser(t, p, "local@example.com")
	host.sites.site("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	p.accounts.LinkSite(acc, "aaaaaaaaaaaaaaaaaaaaaaaaaa")
	for _, path := range []string{"/account", "/account/login"} {
		body := getAs(mux, path, cookie).Body.String()
		if m := inlineHandler.FindString(body); m != "" {
			t.Errorf("%s carries an inline event handler (%q); the CSP never runs it, so it is a confirm() that silently does not confirm", path, m)
		}
	}
	// And the confirmation step itself.
	w := postAs(mux, "/account/delete", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if m := inlineHandler.FindString(w.Body.String()); m != "" {
		t.Errorf("the confirmation page carries an inline event handler %q", m)
	}
}

func TestLocalDeletionIsATwoStepConfirmation(t *testing.T) {
	p, host, mux := setupAccounts(t)
	acc, cookie := localUser(t, p, "local@example.com")
	host.sites.site("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	p.accounts.LinkSite(acc, "aaaaaaaaaaaaaaaaaaaaaaaaaa")

	// Step one: the form on the dashboard posts to /account/delete, which
	// renders a confirmation and deletes NOTHING.
	body := getAs(mux, "/account", cookie).Body.String()
	if !strings.Contains(body, `action="/account/delete"`) {
		t.Fatal("dashboard has no delete form")
	}
	w := postAs(mux, "/account/delete", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusOK {
		t.Fatalf("delete step one = %d (%s)", w.Code, w.Body)
	}
	conf := w.Body.String()
	if !strings.Contains(conf, `action="/account/delete/confirm"`) || !strings.Contains(conf, "1 site") {
		t.Fatalf("step one did not render a confirmation naming what goes: %s", conf)
	}
	if _, err := p.accounts.ByID(acc.ID); err != nil {
		t.Fatal("step one deleted the account")
	}
	if len(host.sites.deleted) != 0 {
		t.Fatal("step one deleted sites")
	}
	// Without a CSRF token neither step does anything.
	if w := postAs(mux, "/account/delete/confirm", cookie, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("confirm without csrf = %d, want 403", w.Code)
	}
	// Step two deletes.
	w = postAs(mux, "/account/delete/confirm", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Account deleted") {
		t.Fatalf("delete step two = %d (%s)", w.Code, w.Body)
	}
	if _, err := p.accounts.ByID(acc.ID); err == nil {
		t.Error("the account survived the confirmed deletion")
	}
	if len(host.sites.deleted) != 1 {
		t.Errorf("sites deleted = %v", host.sites.deleted)
	}
}

func TestLocalDeletionCancelsTheSubscriptionFirst(t *testing.T) {
	p, host, mux := setupAccounts(t)
	fake := &fakeBilling{name: "fake"}
	p.billing = fake
	acc, cookie := localUser(t, p, "payer@example.com")
	subscribed(t, p, acc, "fake")
	host.sites.site("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	p.accounts.LinkSite(acc, "aaaaaaaaaaaaaaaaaaaaaaaaaa")

	// The confirmation says the subscription goes too.
	w := postAs(mux, "/account/delete", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if !strings.Contains(w.Body.String(), "subscription") {
		t.Errorf("the confirmation does not mention the subscription that will be cancelled: %s", w.Body)
	}
	w = postAs(mux, "/account/delete/confirm", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Account deleted") {
		t.Fatalf("confirm = %d (%s)", w.Code, w.Body)
	}
	if len(fake.cancelled) != 1 || fake.cancelled[0] != "sub_1" {
		t.Fatalf("subscription not cancelled: %v", fake.cancelled)
	}
	if _, err := p.accounts.ByID(acc.ID); err == nil {
		t.Error("the account survived")
	}
}

// Fail closed: if the provider will not cancel, the account stays, its sites
// stay, and the page says why. A customer must never be deleted while still
// being charged.
func TestLocalDeletionKeepsTheAccountWhenCancellationFails(t *testing.T) {
	p, host, mux := setupAccounts(t)
	fake := &fakeBilling{name: "fake", cancelErr: errors.New("provider down")}
	p.billing = fake
	acc, cookie := localUser(t, p, "payer@example.com")
	subscribed(t, p, acc, "fake")
	host.sites.site("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	p.accounts.LinkSite(acc, "aaaaaaaaaaaaaaaaaaaaaaaaaa")

	w := postAs(mux, "/account/delete/confirm", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code == http.StatusOK && strings.Contains(w.Body.String(), "Account deleted") {
		t.Fatal("the account was deleted although the subscription could not be cancelled")
	}
	if !strings.Contains(w.Body.String(), "subscription") {
		t.Errorf("the failure page does not say the subscription is the problem: %s", w.Body)
	}
	if _, err := p.accounts.ByID(acc.ID); err != nil {
		t.Error("the account is gone")
	}
	if len(host.sites.deleted) != 0 {
		t.Error("sites were deleted")
	}
}

// A subscription with a provider that is not the active backend cannot be
// cancelled from here, and that is also a reason to keep the account.
func TestLocalDeletionRefusesWhenNoBackendCanCancel(t *testing.T) {
	p, host, mux := setupAccounts(t)
	p.billing = &fakeBilling{name: "other"}
	acc, cookie := localUser(t, p, "payer@example.com")
	subscribed(t, p, acc, "stripe")
	host.sites.site("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	p.accounts.LinkSite(acc, "aaaaaaaaaaaaaaaaaaaaaaaaaa")

	w := postAs(mux, "/account/delete/confirm", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if strings.Contains(w.Body.String(), "Account deleted") {
		t.Fatal("deleted with a live subscription nobody could cancel")
	}
	if _, err := p.accounts.ByID(acc.ID); err != nil {
		t.Error("the account is gone")
	}
}

// A subscription already cancelled by webhook needs no call.
func TestLocalDeletionSkipsACancelledSubscription(t *testing.T) {
	p, _, mux := setupAccounts(t)
	fake := &fakeBilling{name: "fake"}
	p.billing = fake
	acc, cookie := localUser(t, p, "payer@example.com")
	subscribed(t, p, acc, "fake")
	p.accounts.Update(acc, func(cur *account.Account) error { cur.Billing.Status = "canceled"; return nil })

	w := postAs(mux, "/account/delete/confirm", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Account deleted") {
		t.Fatalf("confirm = %d (%s)", w.Code, w.Body)
	}
	if len(fake.cancelled) != 0 {
		t.Errorf("a cancelled subscription was cancelled again: %v", fake.cancelled)
	}
}
