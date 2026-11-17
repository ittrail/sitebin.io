# Plan: form recipients the account already knows

Design: `docs/superpowers/specs/2026-11-17-form-recipients-known-to-the-account-design.md`.
Tests first for every task; run `go test ./...` and `go test -tags ee ./...`.

1. **Store** (`internal/store/forms.go`, `forms_test.go`)
   - `Form.ConfirmedVia`, constants `FormViaSignIn = "sign_in"`, `FormViaAccount = "account"`.
   - `FormShortcut{Owner, Via}`; `AddForm(site, spec, limit, sc *FormShortcut)`,
     `FormPatch.Shortcut`, `RequestConfirmation(site, key, sc *FormShortcut)`: active at once only
     while `meta.json` names `sc.Owner`; a resend only for a `pending` form without a stop.
   - `RequestConfirmation` keeps `StoppedAt`; `ConfirmForm` clears it and `ConfirmedVia`.
   - Tests: shortcut activates; owner mismatch stays pending; stopped form never shortcuts;
     stop survives a resend and clears on confirm; recipient change clears `ConfirmedVia`.
2. **Seam** (`internal/ext/ext.go`): optional `FormAccounts`.
3. **Core look-up** (`internal/httpapi/forms.go`): `formCaller`, `knownRecipient`, wire into
   `addForm`, `updateForm`, `resendConfirmation`; skip throttle and mail when active; API callers
   (`formsapi.go`) and MCP (`mcpops.go`); `confirmed_via` in `formJSON`.
   - Tests (`formsknown_test.go`): sign-in address; earlier form on another own site; same site;
     admin, edit password and stranger never; a stop anywhere blocks; a site transferred in does
     not vouch; a locked site does not vouch; extension errors mean mail; unverified sign-in
     address means mail; provider without `FormAccounts` unchanged; resend of pending activates,
     of stopped mails; MCP add_form with the owner's token is active with no mail.
4. **ee** (`ee/formaccounts.go`, test): `VerifiedEmail`, `OwnedSiteIDs`.
5. **MCP descriptions** (`internal/mcp/server.go`, `ops.go`).
6. **Edit page** (`web/static/edit.js`, `edit.html`): toasts, status hint.
7. **README** forms section.
8. **Website** (`Sitebin-Website/public/docs/forms/`, `/docs/mcp/`), shipped after the product.
