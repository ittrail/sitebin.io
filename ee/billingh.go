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
	if p.stripe != nil {
		routes["POST /account/billing/stripe/checkout"] = http.HandlerFunc(p.handleStripeCheckout)
		routes["POST /account/billing/stripe/webhook"] = http.HandlerFunc(p.handleStripeWebhook)
	}
	if p.paddle != nil {
		routes["POST /account/billing/paddle/checkout"] = http.HandlerFunc(p.handlePaddleCheckout)
		routes["POST /account/billing/paddle/webhook"] = http.HandlerFunc(p.handlePaddleWebhook)
	}
}

func (p *provider) handleStripeCheckout(w http.ResponseWriter, r *http.Request) {
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
	if !ok || tier.Price == nil || tier.Price.Stripe == "" {
		http.Error(w, "that plan is not purchasable", http.StatusBadRequest)
		return
	}
	url, err := p.stripe.CheckoutURL(r.Context(), tier.Price.Stripe, acc.ID, tier.ID, acc.Email,
		p.baseURL()+"/account?upgraded=1", p.baseURL()+"/account")
	if err != nil {
		slog.Error("stripe checkout", "err", err)
		p.renderMessage(w, msgView{Title: "Checkout unavailable", Body: "Could not start checkout. Please try again.", Back: "/account"})
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (p *provider) handlePaddleCheckout(w http.ResponseWriter, r *http.Request) {
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
	if !ok || tier.Price == nil || tier.Price.Paddle == "" {
		http.Error(w, "that plan is not purchasable", http.StatusBadRequest)
		return
	}
	url, err := p.paddle.CheckoutURL(r.Context(), tier.Price.Paddle, acc.ID, tier.ID, p.baseURL()+"/account?upgraded=1")
	if err != nil {
		slog.Error("paddle checkout", "err", err)
		p.renderMessage(w, msgView{Title: "Checkout unavailable", Body: "Could not start checkout. Please try again.", Back: "/account"})
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (p *provider) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	u, err := p.stripe.VerifyWebhook(r.Header.Get("Stripe-Signature"), body, time.Now())
	p.finishWebhook(w, u, err)
}

func (p *provider) handlePaddleWebhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	u, err := p.paddle.VerifyWebhook(r.Header.Get("Paddle-Signature"), body, time.Now())
	p.finishWebhook(w, u, err)
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
		if ids, err := p.accounts.ListSiteIDs(acc); err == nil {
			if t, ok := p.cfg.Tier(acc.Tier); ok {
				grant := grantFromTier(acc.ID, t)
				for _, id := range ids {
					if err := p.host.Sites().ApplyQuota(id, grant); err != nil {
						slog.Error("billing: could not restamp site", "account", acc.ID, "site", id, "err", err)
					}
				}
			}
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
