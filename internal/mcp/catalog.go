package mcp

import (
	"encoding/json"
	"slices"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolSpec is everything about a tool that is decided before its handler
// runs: which scope it needs, whether an edit password can stand in for an
// account, and what a client is told about it.
//
// It lives in one table, next to the tools, because the HTTP layer reads it
// too — to challenge a call that needs an account and to answer a missing
// scope with a step-up 403 — and two copies of "which scope does delete_site
// need" are how the challenge and the tool would come to disagree.
type toolSpec struct {
	// scope is the OAuth scope a restricted credential must hold. It is stated
	// here rather than derived from the read-only hint: a hint is advice to a
	// model, a scope is an authorization decision.
	scope string
	// perSite marks a tool that addresses one site by edit_id and so takes
	// that site's edit_password as an alternative to an account.
	perSite bool
	// title is the human name clients show; the connector directories
	// require one on every tool.
	title string
	// annotations are the behaviour hints. MCP defaults an absent
	// destructiveHint and openWorldHint to TRUE, so every write tool states
	// both rather than leaving them to the default.
	annotations sdk.ToolAnnotations
}

// reads is every read tool's hint set: it changes nothing, and it reaches
// nothing beyond this instance.
func reads() sdk.ToolAnnotations {
	return sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
}

// writes spells out a write tool's hints. Destructive means it can delete,
// replace or overwrite something; open world means it publishes to the public
// web, claims a domain or mails a third party; idempotent means a repeat with
// the same arguments changes nothing further.
func writes(destructive, openWorld, idempotent bool) sdk.ToolAnnotations {
	return sdk.ToolAnnotations{DestructiveHint: ptr(destructive), OpenWorldHint: ptr(openWorld), IdempotentHint: idempotent}
}

// catalog is the tool table. Renaming a key is a contract change: every
// connector configuration people have saved names these tools.
var catalog = map[string]toolSpec{
	// A new public site: nothing is replaced, and a repeat makes another one.
	"create_site": {scope: ScopeWrite, title: "Publish a new site", annotations: writes(false, true, false)},
	"list_sites":  {scope: ScopeRead, title: "List my sites", annotations: reads()},
	"get_site":    {scope: ScopeRead, perSite: true, title: "Get a site", annotations: reads()},
	// Overwrites settings, and can lift a view password or change what the
	// site serves.
	"update_site": {scope: ScopeWrite, perSite: true, title: "Change a site's settings", annotations: writes(true, true, true)},
	"list_files":  {scope: ScopeRead, perSite: true, title: "List a site's files", annotations: reads()},
	"read_file":   {scope: ScopeRead, perSite: true, title: "Read a file", annotations: reads()},
	// Overwrites files, and with replace removes the rest; what it writes is
	// live at once.
	"write_files": {scope: ScopeWrite, perSite: true, title: "Write files to a site", annotations: writes(true, true, true)},
	// Removals take something off the web but publish nothing new.
	"delete_file": {scope: ScopeWrite, perSite: true, title: "Delete a file", annotations: writes(true, false, true)},
	"delete_site": {scope: ScopeWrite, perSite: true, title: "Delete a site", annotations: writes(true, false, true)},
	// Claims a domain and checks its DNS; attaching adds, it replaces nothing.
	"add_domain":    {scope: ScopeWrite, perSite: true, title: "Add a custom domain", annotations: writes(false, true, true)},
	"remove_domain": {scope: ScopeWrite, perSite: true, title: "Remove a custom domain", annotations: writes(true, false, true)},
	"download_site": {scope: ScopeRead, perSite: true, title: "Download a site as a zip", annotations: reads()},
	// Issues a credential and publishes nothing itself; each call is a new token.
	"open_upload": {scope: ScopeWrite, perSite: true, title: "Open a large-file upload", annotations: writes(false, false, false)},
	"list_forms":  {scope: ScopeRead, perSite: true, title: "List a site's forms", annotations: reads()},
	// Mails the recipient a confirmation; each call adds another form.
	"add_form": {scope: ScopeWrite, perSite: true, title: "Add an email form", annotations: writes(false, true, false)},
	// Overwrites a form's settings, and a new recipient is mailed.
	"update_form": {scope: ScopeWrite, perSite: true, title: "Change an email form", annotations: writes(true, true, false)},
	"remove_form": {scope: ScopeWrite, perSite: true, title: "Remove an email form", annotations: writes(true, false, true)},
	// Mails the recipient again on every call.
	"resend_form_confirmation": {scope: ScopeWrite, perSite: true, title: "Resend a form confirmation", annotations: writes(false, true, false)},
}

// ToolScope returns the scope a tool needs. ok=false means the catalog does
// not know the name.
func ToolScope(name string) (scope string, ok bool) {
	spec, ok := catalog[name]
	return spec.scope, ok
}

// AnonymousCallAllowed reports whether a tools/call carrying no
// Authorization header may reach the tool when OAuth is on, rather than be
// answered with a sign-in challenge:
//
//   - a tool the catalog does not know passes: the SDK reports it, and signing
//     in would not make it exist;
//   - a per-site tool passes with a non-empty edit_password, which the tool
//     verifies exactly as it always has;
//   - create_site passes where the instance has no accounts, since there is
//     nothing to sign in to;
//   - everything else — list_sites, a per-site tool without a password,
//     create_site on an account instance — needs an account.
//
// It decides only whether to challenge. The tool still authenticates the call
// itself, so a wrong answer here costs a needless sign-in or a tool error,
// never access. The password is looked up by its exact key, as the tool's
// schema names it.
func AnonymousCallAllowed(name string, arguments json.RawMessage, accountsEnabled bool) bool {
	spec, ok := catalog[name]
	if !ok {
		return true
	}
	if name == "create_site" && !accountsEnabled {
		return true
	}
	if !spec.perSite {
		return false
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &args); err != nil {
		return false
	}
	var pw string
	if err := json.Unmarshal(args["edit_password"], &pw); err != nil {
		return false
	}
	return pw != ""
}

// HeldScopes returns the Sitebin scopes among a credential's scopes, in
// catalog order. Anything else a token carries — openid, email, another
// application's scopes, the extension's "no scope" placeholder — is left out,
// because this is what an agent is told it holds and what a 403 challenge
// names.
func HeldScopes(scopes []string) []string {
	var held []string
	for _, s := range AllScopes {
		if slices.Contains(scopes, s) {
			held = append(held, s)
		}
	}
	return held
}

// Allows reports whether a credential may use scope. An account API token
// carries no scopes and is unrestricted, which is what it has always meant.
// An OAuth access token is restricted by its scopes even when it carries
// none: the issuer telling us nothing must never read as "everything".
func Allows(scopes []string, oauth bool, scope string) bool {
	if !oauth && len(scopes) == 0 {
		return true
	}
	return slices.Contains(scopes, scope)
}
