//go:build ee

package ee

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The stack's suspension order.
//
// The stack's "Suspend user" disables the Keycloak user, ends their Keycloak
// sessions, and then calls every member app that declared a suspendUserUrl.
// This is Sitebin's side: the account is marked suspended, every way in is
// refused, and every site it owns is locked — served to nobody, frozen, and
// kept past its expiry as evidence. Lifting the suspension lifts only the
// locks it placed; a site the operator locked by hand stays locked. See
// docs/superpowers/specs/2026-09-28-site-lock-and-account-suspension.md.
//
// It is authenticated exactly like the GDPR orders (verifyGDPRRequest): the
// signature over "<X-Timestamp>.<body>" with SITEBIN_STACK_GDPR_SECRET, and
// nothing else. Like them it is idempotent, and a user with no account here
// is a 200 — the stack reads any other status as "not done".

const gdprSuspendPath = "/account/gdpr/suspend"

// msgSuspendedAccount is what a suspended account is told when it tries to
// sign in anyway.
const msgSuspendedAccount = "This account has been suspended. Contact the operator of this instance."

// suspendOrder is the body of the stack's call. Suspended is a pointer so an
// order that does not say which way is refused rather than read as false:
// guessing "lift" would unlock a phishing operator's sites.
type suspendOrder struct {
	UserID    string `json:"userId"`
	Email     string `json:"email"`
	Suspended *bool  `json:"suspended"`
	Reason    string `json:"reason"`
}

// handleSuspend applies a verified suspension order. The account's own state
// is written first, so the refusals take effect before any site is touched
// and a retry after a partial failure converges; a lock or unlock that fails
// is a 500, and the stack retries.
func (p *provider) handleSuspend(w http.ResponseWriter, r *http.Request) {
	body, ok := p.verifyGDPRRequest(w, r, time.Now())
	if !ok {
		return
	}
	var order suspendOrder
	if err := json.Unmarshal(body, &order); err != nil || order.UserID == "" || order.Suspended == nil {
		http.Error(w, `the order must carry a userId and "suspended": true or false`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	acc, err := p.accounts.ByOAuth(account.OIDCProv, order.UserID)
	switch {
	case errors.Is(err, account.ErrNotFound):
		slog.Info("suspension: ordered for a user with no account here; nothing to do", "subject", order.UserID, "suspended", *order.Suspended)
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "found": false})
		return
	case err != nil:
		slog.Error("suspension: could not load the account", "subject", order.UserID, "err", err)
		http.Error(w, `{"error":"could not read the account"}`, http.StatusInternalServerError)
		return
	}
	reason := store.CleanLockReason(order.Reason)
	if *order.Suspended {
		p.suspend(w, acc, reason)
	} else {
		p.unsuspend(w, acc)
	}
}

func (p *provider) suspend(w http.ResponseWriter, acc *account.Account, reason string) {
	now := time.Now().UTC()
	err := p.accounts.Update(acc, func(cur *account.Account) error {
		if cur.SuspendedAt == nil {
			cur.SuspendedAt = &now // a repeated order keeps the first date
		}
		if reason != "" || cur.SuspendedReason == "" {
			cur.SuspendedReason = reason
		}
		// Every browser session and CSRF token is bound to the version, so
		// this ends them all; currentAccount refuses a suspended account as
		// well. Tokens are not bound to it — BearerCredential asks.
		cur.TokenVersion++
		return nil
	})
	if err != nil {
		slog.Error("suspension: could not mark the account suspended", "account", acc.ID, "err", err)
		http.Error(w, `{"error":"could not suspend the account"}`, http.StatusInternalServerError)
		return
	}
	lockReason := "account suspended"
	if reason != "" {
		lockReason += ": " + reason
	}
	lock := &ext.SiteLock{At: now, Reason: store.CleanLockReason(lockReason), By: ext.LockByAccount}
	n, failed := p.eachSite(acc, func(id string) (bool, error) {
		// An account lock never replaces a lock that is there already.
		before, _ := p.host.Sites().Info(id)
		if err := p.host.Sites().SetLock(id, lock); err != nil {
			return false, err
		}
		return before.Locked == nil, nil
	})
	if failed > 0 {
		http.Error(w, `{"error":"could not lock every site of the account"}`, http.StatusInternalServerError)
		return
	}
	slog.Info("suspension: account suspended on the stack's order; its sites are locked",
		"account", acc.ID, "subject", acc.OAuthSubject, "locked", n, "reason", reason)
	json.NewEncoder(w).Encode(map[string]any{"status": "suspended", "found": true, "sitesLocked": n})
}

func (p *provider) unsuspend(w http.ResponseWriter, acc *account.Account) {
	err := p.accounts.Update(acc, func(cur *account.Account) error {
		cur.SuspendedAt = nil
		cur.SuspendedReason = ""
		return nil
	})
	if err != nil {
		slog.Error("suspension: could not lift the account's suspension", "account", acc.ID, "err", err)
		http.Error(w, `{"error":"could not lift the suspension"}`, http.StatusInternalServerError)
		return
	}
	n, failed := p.eachSite(acc, func(id string) (bool, error) {
		return p.host.Sites().ReleaseLock(id, ext.LockByAccount)
	})
	if failed > 0 {
		http.Error(w, `{"error":"could not unlock every site of the account"}`, http.StatusInternalServerError)
		return
	}
	slog.Info("suspension: lifted on the stack's order; the sites it locked are unlocked",
		"account", acc.ID, "subject", acc.OAuthSubject, "unlocked", n)
	json.NewEncoder(w).Encode(map[string]any{"status": "active", "found": true, "sitesUnlocked": n})
}

// eachSite applies fn to every site the account owns and counts the ones it
// changed. A site that no longer exists has its stale ownership marker
// dropped and is no failure; every other failure is logged and counted, and
// the other sites are still tried.
func (p *provider) eachSite(acc *account.Account, fn func(viewID string) (changed bool, err error)) (changed, failed int) {
	ids, err := p.accounts.ListSiteIDs(acc)
	if err != nil {
		slog.Error("suspension: could not list the account's sites", "account", acc.ID, "err", err)
		return 0, 1
	}
	for _, id := range ids {
		ok, err := fn(id)
		switch {
		case errors.Is(err, ext.ErrSiteGone):
			p.accounts.UnlinkSite(acc, id)
		case err != nil:
			slog.Error("suspension: could not change a site's lock", "account", acc.ID, "site", id, "err", err)
			failed++
		case ok:
			changed++
		}
	}
	return changed, failed
}

// suspendedOwners maps each suspended owner in sites to the badge's hover
// text — the date and the stack's reason — for the register. Owners that are
// active, or no longer exist, are absent.
func (p *provider) suspendedOwners(sites []ext.SiteInfo) map[string]string {
	out := map[string]string{}
	seen := map[string]bool{}
	for _, s := range sites {
		if s.Owner == "" || seen[s.Owner] {
			continue
		}
		seen[s.Owner] = true
		acc, err := p.accounts.ByID(s.Owner)
		if err != nil || !acc.Suspended() {
			continue
		}
		text := "suspended " + acc.SuspendedAt.Local().Format("2006-01-02 15:04")
		if r := strings.TrimSpace(acc.SuspendedReason); r != "" {
			text += ": " + r
		}
		out[s.Owner] = text
	}
	return out
}
