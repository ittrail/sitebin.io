//go:build ee

package ee

import (
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/billing"
)

// billingRoutes adds checkout + webhook endpoints for configured providers.
func (p *provider) billingRoutes(routes map[string]http.Handler) {
	if p.billing == nil {
		return
	}
	// Provider-neutral, because with PayGate the processor is the stack's
	// business: a customer must never click a URL that names it.
	routes["POST /account/upgrade"] = http.HandlerFunc(p.handleUpgrade)
	routes["POST /account/billing/portal"] = http.HandlerFunc(p.handlePortal)

	// The webhook path is the one place a provider's name belongs in a URL,
	// because the provider decides where it delivers. Only a direct backend
	// has one; PayGate takes its webhooks on the stack's side.
	if wr, ok := p.billing.(billing.WebhookReceiver); ok {
		routes["POST /account/billing/"+wr.WebhookPath()+"/webhook"] = p.webhookHandler(wr)
	}
}

// billingCustomer is what the active backend needs to know about acc. Subject
// is filled only for accounts the stack issued, mirroring paygateTier: a local
// account has no identity PayGate could recognise.
func (p *provider) billingCustomer(acc *account.Account) billing.Customer {
	c := billing.Customer{AccountID: acc.ID, Email: acc.Email}
	if acc.Provider == account.OIDCProv {
		c.Subject = acc.OAuthSubject
	}
	if acc.Billing != nil {
		c.Provider = acc.Billing.Provider
		c.Customer = acc.Billing.Customer
		c.Subscription = acc.Billing.Subscription
	}
	return c
}

// handleUpgrade starts a purchase with whichever backend is active.
func (p *provider) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	tier, ok := p.cfg.Tier(r.PostFormValue("tier"))
	if !ok || !tier.Paid() {
		http.Error(w, "that plan is not purchasable", http.StatusBadRequest)
		return
	}
	url, err := p.billing.CheckoutURL(r.Context(), p.billingCustomer(acc), tier,
		p.baseURL()+"/account?upgraded=1", p.baseURL()+"/account")
	if err != nil {
		// The backend's name is for us, not for the customer.
		slog.Error("checkout", "backend", p.billing.Name(), "tier", tier.ID, "err", err)
		p.renderMessage(w, msgView{Title: "Checkout unavailable", Body: "Could not start checkout. Please try again.", Back: "/account"})
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// handlePortal sends an existing subscriber to their backend's portal.
func (p *provider) handlePortal(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	url, err := p.billing.PortalURL(r.Context(), p.billingCustomer(acc), p.baseURL()+"/account")
	if err != nil {
		slog.Error("billing portal", "backend", p.billing.Name(), "err", err)
		p.renderMessage(w, msgView{Title: "Unavailable", Body: "Could not open your billing settings. Please try again.", Back: "/account"})
		return
	}
	if url == "" {
		// Never paid, so there is nothing to manage.
		p.redirect(w, r, "/account")
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// webhookHandler verifies an event with the backend and applies it. The
// billing package stops at verification so it never reaches into accounts.
func (p *provider) webhookHandler(wr billing.WebhookReceiver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		u, err := wr.VerifyWebhook(r.Header.Get(wr.SignatureHeader()), body, time.Now())
		p.finishWebhook(w, u, err)
	})
}

// finishWebhook applies a verified Update, translating errors to statuses.
func (p *provider) finishWebhook(w http.ResponseWriter, u billing.Update, err error) {
	if billing.IsIgnored(err) {
		w.WriteHeader(200) // acknowledge events we don't act on
		return
	}
	if err != nil {
		http.Error(w, "invalid webhook", http.StatusBadRequest)
		return
	}
	if err := p.applyBillingUpdate(u); err != nil {
		slog.Error("apply billing update", "provider", u.Provider, "err", err)
		http.Error(w, "processing error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(200)
}

// applyBillingUpdate resolves the account and updates its tier + billing state.
func (p *provider) applyBillingUpdate(u billing.Update) error {
	acc := p.resolveBillingAccount(u)
	if acc == nil {
		slog.Warn("billing webhook: unknown account", "provider", u.Provider, "customer", u.Customer)
		return nil // acknowledge; nothing to do
	}
	if u.Customer != "" {
		p.accounts.LinkBilling(acc, u.Provider, u.Customer)
	}
	changed := u.Canceled || u.TierID != ""
	if err := p.accounts.Update(acc, func(cur *account.Account) error {
		if cur.Billing == nil {
			cur.Billing = &account.Billing{}
		}
		cur.Billing.Provider = u.Provider
		if u.Customer != "" {
			cur.Billing.Customer = u.Customer
		}
		if u.Subscription != "" {
			cur.Billing.Subscription = u.Subscription
		}
		if u.Status != "" {
			cur.Billing.Status = u.Status
		}
		switch {
		case u.Canceled:
			cur.Tier = p.cfg.DefaultTier // downgrade to default on cancellation
		case u.TierID != "":
			cur.Tier = u.TierID
		}
		return nil
	}); err != nil {
		return err
	}
	if changed {
		// acc.Tier is now the new value (Update refreshes it in place), so
		// syncTier would see no difference; restamp directly.
		if t, ok := p.cfg.Tier(acc.Tier); ok {
			p.restampSites(acc, t)
		} else {
			slog.Error("billing: tier not found in config; sites not restamped", "account", acc.ID, "tier", acc.Tier)
		}
	}
	return nil
}

func (p *provider) resolveBillingAccount(u billing.Update) *account.Account {
	if u.AccountID != "" {
		if acc, err := p.accounts.ByID(u.AccountID); err == nil {
			return acc
		}
	}
	if u.Customer != "" {
		if acc, err := p.accounts.ByBilling(u.Provider, u.Customer); err == nil {
			return acc
		}
	}
	return nil
}
