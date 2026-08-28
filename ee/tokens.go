//go:build ee

package ee

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/ittrail/sitebin.io/ee/account"
)

// maxTokenNameLen bounds the label. It is rendered back into the dashboard, so
// it is escaped by the template; the cap is about keeping the list readable.
const maxTokenNameLen = 60

type tokenRow struct {
	account.Token
	CreatedText string
	CSRF        string
}

func (p *provider) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if len(name) > maxTokenNameLen {
		name = name[:maxTokenNameLen]
	}
	tok, secret, err := p.accounts.CreateToken(acc, name)
	if err != nil {
		if err == account.ErrTooManyTokens {
			p.renderMessage(w, msgView{
				Title: "Too many tokens",
				Body:  "This account already has the maximum number of API tokens. Revoke one you no longer use, then create another.",
				Back:  "/account",
			})
			return
		}
		http.Error(w, "could not create the token", http.StatusInternalServerError)
		return
	}
	slog.Info("api token created", "account", acc.ID, "token", tok.ID)
	// Shown once and never again: the secret is not stored, only its hash.
	p.renderMessage(w, msgView{
		Title:  "Your new API token",
		Body:   "Copy it now — it is not stored and cannot be shown again. Send it as an Authorization: Bearer header.",
		Detail: secret,
		Back:   "/account",
	})
}

func (p *provider) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if err := p.accounts.DeleteToken(acc, id); err != nil {
		http.Error(w, "could not revoke the token", http.StatusInternalServerError)
		return
	}
	slog.Info("api token revoked", "account", acc.ID, "token", id)
	p.redirect(w, r, "/account")
}

// tokenRows builds the dashboard's list. A read failure yields an empty list
// rather than breaking the whole page — the account's sites matter more.
func (p *provider) tokenRows(acc *account.Account, csrf string) []tokenRow {
	toks, err := p.accounts.ListTokens(acc)
	if err != nil {
		slog.Error("list api tokens", "account", acc.ID, "err", err)
		return nil
	}
	rows := make([]tokenRow, 0, len(toks))
	for _, t := range toks {
		rows = append(rows, tokenRow{
			Token:       t,
			CreatedText: t.CreatedAt.Local().Format("2006-01-02"),
			CSRF:        csrf,
		})
	}
	return rows
}
