//go:build ee

package ee

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/session"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// The stack's GDPR orchestration.
//
// A data subject asks the STACK — in the account console, or an operator does
// it for them in the stack's user directory — and the stack calls every app
// the person belongs to: export first (Art. 20), and for a deletion (Art. 17)
// the app BEFORE the identity, so that a name and an email never outlive the
// only thing that made the app's data findable. These two endpoints are
// Sitebin's side of that, and they are the only calls the stack ever makes
// INTO a Sitebin instance.
//
// Three rules:
//
//   - **The signature is the whole authentication.** The stack signs
//     `<X-Timestamp>.<body>` with the secret the instance declared, and
//     nothing else — no session, no admin key, no API token — is ever
//     accepted here. A request that fails verification is refused before its
//     body is even parsed.
//   - **A verified deletion IS the instruction.** CLAUDE.md's rule that
//     nothing acts destructively on an unknown error still holds: a site that
//     cannot be deleted stops the whole order with a 5xx, and the stack then
//     leaves the identity in place so the operator can retry. What it does
//     not do is second-guess the order itself.
//   - **Deletion is idempotent, and an unknown user is 200.** The stack
//     treats every non-2xx — 404 included — as "the app still holds the data"
//     and aborts, so an endpoint that answered 404 for a user it has already
//     erased could never let a deletion complete.
//
// The account is looked up by the stack's user id, which is the OIDC subject:
// the stack knows nothing about local accounts and never orders anything for
// them.

const (
	gdprDeletePath = "/account/gdpr/delete"
	gdprExportPath = "/account/gdpr/export"

	// gdprMaxSkew is the freshness window, in EITHER direction. The stack's
	// guide rejects requests older than five minutes; a timestamp from the
	// future is refused too, because a signed request minted for later is a
	// replay waiting to happen and no honest clock is that far ahead.
	gdprMaxSkew = 5 * time.Minute
	gdprMaxBody = 1 << 20
)

// gdprRoutes mounts the two endpoints, and only when there is a secret to
// verify a caller with: a route the stack could call but nothing could check
// would be an unauthenticated deletion endpoint.
func (p *provider) gdprRoutes(routes map[string]http.Handler) {
	if p.cfg.GDPRSecret == "" {
		return
	}
	routes["POST "+gdprDeletePath] = http.HandlerFunc(p.handleGDPRDelete)
	routes["POST "+gdprExportPath] = http.HandlerFunc(p.handleGDPRExport)
}

// gdprSignature is what X-Signature must carry for body at timestamp:
// `sha256=` + hex(HMAC-SHA256(secret, "<timestamp>.<body>")). The timestamp is
// INSIDE the MAC, which is what stops a captured request being replayed with a
// fresh one; signing the body alone is the documented way to get every request
// refused.
func gdprSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// gdprOrder is the body of both calls.
type gdprOrder struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

// verifyGDPROrder authenticates a request from the stack and decodes its
// order. It writes the refusal itself and reports ok=false; the caller then
// simply returns. Every refusal to authenticate is the same 401 to the caller
// — the reason goes to the log, where the operator is — so a probe learns
// nothing about which check it failed.
func (p *provider) verifyGDPROrder(w http.ResponseWriter, r *http.Request, now time.Time) (gdprOrder, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, gdprMaxBody+1))
	if err != nil {
		http.Error(w, "could not read the request", http.StatusBadRequest)
		return gdprOrder{}, false
	}
	if len(body) > gdprMaxBody {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return gdprOrder{}, false
	}

	refuse := func(reason string) (gdprOrder, bool) {
		slog.Warn("gdpr: refused an unverified order", "path", r.URL.Path, "reason", reason)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid signature"})
		return gdprOrder{}, false
	}

	ts := r.Header.Get("X-Timestamp")
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || unix <= 0 {
		return refuse("missing or malformed X-Timestamp")
	}
	skew := now.Sub(time.Unix(unix, 0))
	if skew > gdprMaxSkew {
		return refuse("stale timestamp")
	}
	if -skew > gdprMaxSkew {
		return refuse("timestamp from the future")
	}
	// hmac.Equal is constant-time. The header is compared against the whole
	// expected string, prefix included, so a bare digest or a different
	// algorithm label is simply not equal.
	if !hmac.Equal([]byte(r.Header.Get("X-Signature")), []byte(gdprSignature(p.cfg.GDPRSecret, ts, body))) {
		return refuse("signature mismatch")
	}

	var order gdprOrder
	if err := json.Unmarshal(body, &order); err != nil || order.UserID == "" {
		// Verified, so it IS the stack — and the stack sent something this
		// instance cannot act on. That is a 400 and worth the detail.
		http.Error(w, "the order must carry a userId", http.StatusBadRequest)
		return gdprOrder{}, false
	}
	return order, true
}

// gdprAccount is the account record as exported: everything stored on it
// except the password hash, which is not the subject's data but a
// verifier of it, and useless to them.
type gdprAccount struct {
	ID            string           `json:"id"`
	Provider      account.Provider `json:"provider"`
	Email         string           `json:"email"`
	EmailVerified bool             `json:"email_verified"`
	OAuthSubject  string           `json:"oauth_subject,omitempty"`
	Tier          string           `json:"tier,omitempty"`
	QuotaOverride *int64           `json:"quota_override,omitempty"`
	Billing       *account.Billing `json:"billing,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

type gdprSite struct {
	ID        string     `json:"id"`
	ViewURL   string     `json:"view_url"`
	Mode      string     `json:"mode"`
	Domains   []string   `json:"custom_domains,omitempty"`
	Origin    string     `json:"origin,omitempty"`
	Bytes     int64      `json:"bytes"`
	Files     int        `json:"files"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// gdprToken is the token's METADATA. The secret is never stored, only its
// hash, so there is nothing to export even if one wanted to.
type gdprToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"created_at"`
}

// gdprSessions says what Sitebin holds about sign-ins, which is: nothing.
// Sessions are signed cookies the browser keeps; the instance stores no
// session list and can name no device. What it does hold is the revocation
// counter every cookie is checked against.
type gdprSessions struct {
	Stored       bool   `json:"stored"`
	Note         string `json:"note"`
	Lifetime     string `json:"lifetime"`
	TokenVersion int    `json:"token_version"`
}

type gdprExport struct {
	Account   *gdprAccount  `json:"account"`
	Sites     []gdprSite    `json:"sites"`
	APITokens []gdprToken   `json:"api_tokens"`
	Sessions  *gdprSessions `json:"sessions"`
}

// handleGDPRExport answers with everything this instance holds about the
// user: the account record, the metadata of every site it owns, the metadata
// of every API token, and what is (not) held about sessions. An unknown user
// gets an export of nothing rather than an error: the stack folds this into a
// combined document and a 4xx here would only mark that document incomplete.
func (p *provider) handleGDPRExport(w http.ResponseWriter, r *http.Request) {
	order, ok := p.verifyGDPROrder(w, r, time.Now())
	if !ok {
		return
	}
	out := gdprExport{Sites: []gdprSite{}, APITokens: []gdprToken{}}
	acc, err := p.accounts.ByOAuth(account.OIDCProv, order.UserID)
	switch {
	case errors.Is(err, account.ErrNotFound):
		slog.Info("gdpr: export ordered for a user with no account here", "subject", order.UserID)
	case err != nil:
		slog.Error("gdpr: could not load the account for an export", "subject", order.UserID, "err", err)
		http.Error(w, "could not read the account", http.StatusInternalServerError)
		return
	default:
		out.Account = &gdprAccount{
			ID: acc.ID, Provider: acc.Provider, Email: acc.Email, EmailVerified: acc.EmailVerified,
			OAuthSubject: acc.OAuthSubject, Tier: acc.Tier, QuotaOverride: acc.QuotaOverride,
			Billing: acc.Billing, CreatedAt: acc.CreatedAt, UpdatedAt: acc.UpdatedAt,
		}
		ids, err := p.accounts.ListSiteIDs(acc)
		if err != nil {
			slog.Error("gdpr: could not list the account's sites for an export", "account", acc.ID, "err", err)
			http.Error(w, "could not list the account's sites", http.StatusInternalServerError)
			return
		}
		for _, id := range ids {
			info, ok := p.host.Sites().Info(id)
			if !ok {
				continue // a dangling ownership marker names nothing that exists
			}
			out.Sites = append(out.Sites, gdprSite{
				ID: info.ViewID, ViewURL: info.ViewURL, Mode: info.Mode, Domains: info.Domains,
				Origin: info.Origin, Bytes: info.Bytes, Files: info.Files,
				CreatedAt: info.CreatedAt, ExpiresAt: info.ExpiresAt,
			})
		}
		toks, err := p.accounts.ListTokens(acc)
		if err != nil {
			slog.Error("gdpr: could not list the account's tokens for an export", "account", acc.ID, "err", err)
			http.Error(w, "could not list the account's tokens", http.StatusInternalServerError)
			return
		}
		for _, t := range toks {
			out.APITokens = append(out.APITokens, gdprToken{ID: t.ID, Name: t.Name, Prefix: t.Prefix, CreatedAt: t.CreatedAt})
		}
		out.Sessions = &gdprSessions{
			Stored:       false,
			Note:         "sessions are signed cookies held by the browser; the instance stores no session list and can name no device",
			Lifetime:     session.DefaultTTL.String(),
			TokenVersion: acc.TokenVersion,
		}
		slog.Info("gdpr: exported an account", "account", acc.ID, "sites", len(out.Sites), "tokens", len(out.APITokens))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
}

// handleGDPRDelete erases the account, its sites, its ownership markers, its
// API tokens and — by removing the record every cookie is validated against —
// its sessions. It is idempotent: a user with no account here is already in
// the desired end state and gets a 200 saying so.
//
// A site that cannot be deleted fails the whole order with a 500 and leaves
// the account in place, so the stack keeps the identity and the operator can
// retry. Sites deleted before the failure stay deleted; a retry finds them
// gone (ErrSiteGone), drops their markers and carries on.
func (p *provider) handleGDPRDelete(w http.ResponseWriter, r *http.Request) {
	order, ok := p.verifyGDPROrder(w, r, time.Now())
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	acc, err := p.accounts.ByOAuth(account.OIDCProv, order.UserID)
	switch {
	case errors.Is(err, account.ErrNotFound):
		slog.Info("gdpr: deletion ordered for a user with no account here; nothing to do", "subject", order.UserID)
		json.NewEncoder(w).Encode(map[string]any{
			"status": "deleted", "found": false, "deletedResources": []string{},
		})
		return
	case err != nil:
		slog.Error("gdpr: could not load the account for a deletion", "subject", order.UserID, "err", err)
		http.Error(w, `{"error":"could not read the account"}`, http.StatusInternalServerError)
		return
	}

	sites := 0
	err = p.accounts.Delete(acc, func(viewID string) error {
		err := p.host.Sites().Delete(viewID)
		if errors.Is(err, ext.ErrSiteGone) {
			return nil // a marker for a site that is already gone
		}
		if err == nil {
			sites++
		}
		return err
	})
	if err != nil {
		slog.Error("gdpr: deletion failed; the account is kept so the stack keeps the identity and the order can be retried",
			"account", acc.ID, "subject", order.UserID, "err", err)
		http.Error(w, `{"error":"could not delete the account's sites"}`, http.StatusInternalServerError)
		return
	}
	slog.Info("gdpr: deleted an account on the stack's order", "account", acc.ID, "subject", order.UserID, "sites", sites)
	json.NewEncoder(w).Encode(map[string]any{
		"status": "deleted", "found": true,
		"deletedResources": []string{"account", "sites", "api_tokens", "sessions"},
		"sites":            sites,
	})
}
