//go:build ee

package ee

import "html/template"

// Dashboard pages. They link the community app.css (served at /_sitebin/assets
// on the main domain) so the enterprise UI matches the rest of Sitebin, with a
// little page-specific layout inline.

const pageHead = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{block "title" .}}Account — Sitebin{{end}}</title>
<link rel="icon" href="/_sitebin/assets/static/favicon.svg" type="image/svg+xml">
<link rel="stylesheet" href="/_sitebin/assets/static/app.css">
<style>
  .acct { width: min(720px, 100%); margin: 6vh auto; padding: 0 18px; }
  .acct .card { margin-bottom: 18px; }
  .acct h1 { font: 650 26px "Space Grotesk", system-ui, sans-serif; letter-spacing: -.02em; margin-bottom: 4px; }
  .acct .muted { color: var(--ink-dim); font-size: 14px; }
  .acct form.inline { display: inline; }
  .acct .sitecard { display: flex; align-items: center; gap: 12px; padding: 12px 0; border-top: 1px solid var(--line-soft); flex-wrap: wrap; }
  .acct .sitecard:first-of-type { border-top: 0; }
  .acct .sitecard .grow { flex: 1; min-width: 220px; }
  .acct .sitecard .u { font: 12px var(--mono); color: var(--ink-faint); }
  .acct a.plain { font: 13px var(--mono); word-break: break-all; }
  .acct .authwrap { width: min(420px, 100%); margin: 10vh auto; }
  .acct .switch-link { text-align: center; margin-top: 14px; font-size: 14px; }
  .acct label.f { display:block; font-size: 13px; color: var(--ink-dim); margin: 12px 0 6px; }
</style>
</head><body>
<header class="topbar">
  <a class="wordmark" href="/"><svg viewBox="0 0 64 64" aria-hidden="true"><rect x="4" y="4" width="56" height="56" rx="14" fill="#101725" stroke="#2A3550" stroke-width="2"/><rect x="16" y="16" width="32" height="6" rx="3" fill="#F5B84D"/><path d="M32 30v14M25 38l7 7 7-7" stroke="#5B8CFF" stroke-width="4.5" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg> sitebin</a>
  <span class="tag">account</span><span class="spacer"></span>
</header>
`

const pageFoot = `</body></html>`

var authTmpl = template.Must(template.New("auth").Parse(pageHead + `
<main class="acct"><div class="authwrap"><div class="card">
  {{if eq .Mode "signup"}}
    <h1>Create your account</h1>
    <p class="muted">Sign up to publish and manage your sites.</p>
    <form method="post" action="/account/signup">
  {{else}}
    <h1>Sign in</h1>
    <p class="muted">Access your account dashboard.</p>
    <form method="post" action="/account/login">
  {{end}}
    <label class="f" for="email">Email</label>
    <input type="email" id="email" name="email" required autocomplete="email" value="{{.Email}}">
    <label class="f" for="password">Password</label>
    <input type="password" id="password" name="password" required autocomplete="{{if eq .Mode "signup"}}new-password{{else}}current-password{{end}}">
    {{if .Error}}<div class="inline-status err" style="margin-top:10px">{{.Error}}</div>{{end}}
    <div style="margin-top:16px"><button class="btn primary" type="submit">{{if eq .Mode "signup"}}Create account{{else}}Sign in{{end}}</button></div>
  </form>
  {{if .Providers}}
  <div style="margin:18px 0;text-align:center;color:var(--ink-faint);font-size:13px">or continue with</div>
  <div style="display:grid;gap:8px">
    {{range .Providers}}<a class="btn" style="justify-content:center" href="/account/auth/{{.ID}}">{{.Label}}</a>{{end}}
  </div>
  {{end}}
  <div class="switch-link">
    {{if eq .Mode "signup"}}Already have an account? <a href="/account/login">Sign in</a>{{else}}New here? <a href="/account/signup">Create an account</a>{{end}}
    {{if and (eq .Mode "login") .EmailEnabled}}<br><a href="/account/reset">Forgot your password?</a>{{end}}
  </div>
</div></div></main>
` + pageFoot))

var resetReqTmpl = template.Must(template.New("resetreq").Parse(pageHead + `
<main class="acct"><div class="authwrap"><div class="card">
  <h1>Reset your password</h1>
  <p class="muted">Enter your email and we'll send a reset link.</p>
  <form method="post" action="/account/reset">
    <label class="f" for="email">Email</label>
    <input type="email" id="email" name="email" required autocomplete="email" autofocus>
    <div style="margin-top:16px"><button class="btn primary" type="submit">Send reset link</button></div>
  </form>
  <div class="switch-link"><a href="/account/login">Back to sign in</a></div>
</div></div></main>
` + pageFoot))

var resetConfirmTmpl = template.Must(template.New("resetconfirm").Parse(pageHead + `
<main class="acct"><div class="authwrap"><div class="card">
  <h1>Choose a new password</h1>
  <form method="post" action="/account/reset/confirm">
    <input type="hidden" name="token" value="{{.Token}}">
    <label class="f" for="password">New password</label>
    <input type="password" id="password" name="password" required autocomplete="new-password" autofocus>
    {{if .Error}}<div class="inline-status err" style="margin-top:10px">{{.Error}}</div>{{end}}
    <div style="margin-top:16px"><button class="btn primary" type="submit">Update password</button></div>
  </form>
</div></div></main>
` + pageFoot))

var dashTmpl = template.Must(template.New("dash").Parse(pageHead + `
<main class="acct">
  <div style="display:flex;align-items:baseline;gap:12px;flex-wrap:wrap;margin-bottom:18px">
    <h1 style="margin:0">Your account</h1>
    <span class="muted">{{.Email}}{{if .Tier}} · {{.Tier}} tier{{end}}</span>
    <span class="spacer" style="flex:1"></span>
    <form class="inline" method="post" action="/account/logout"><button class="btn small" type="submit">Sign out</button></form>
  </div>

  <div class="card">
    <h3>Your sites <span class="count">{{len .Sites}}</span></h3>
    {{if not .Sites}}<p class="muted">No sites yet. <a href="/">Publish one</a> — it will be owned by this account.</p>{{end}}
    {{range .Sites}}
    <div class="sitecard">
      <div class="grow">
        <a class="plain" href="{{.ViewURL}}" target="_blank" rel="noopener">{{.ViewURL}}</a>
        <div class="u">{{.Mode}} · {{.SizeText}} · {{.Files}} files</div>
      </div>
      <a class="btn small" href="{{.EditURL}}">Manage</a>
      <form class="inline" method="post" action="/account/sites/{{.ViewID}}/rotate">
        <input type="hidden" name="csrf" value="{{.CSRF}}">
        <button class="btn small" type="submit" title="Issue a new edit password">Reset edit password</button>
      </form>
      <form class="inline" method="post" action="/account/sites/{{.ViewID}}/delete">
        <input type="hidden" name="csrf" value="{{.CSRF}}">
        <button class="btn small danger" type="submit">Delete</button>
      </form>
    </div>
    {{end}}
  </div>

  {{if .ManageURL}}
  <div class="card">
    <h3>Plan</h3>
    <p class="muted">Your plan is managed through your organization's subscription.</p>
    <a class="btn small" href="{{.ManageURL}}" target="_blank" rel="noopener">Manage subscription</a>
  </div>
  {{end}}

  {{if .Tiers}}
  <div class="card">
    <h3>Plan</h3>
    <div style="display:flex;gap:10px;flex-wrap:wrap">
    {{range .Tiers}}
      {{if .Current}}
        <button class="btn small primary" type="button" disabled>{{.Label}}{{if .Price}} · {{.Price}}{{end}} (current)</button>
      {{else if and .Paid $.Checkout}}
        <form class="inline" method="post" action="/account/billing/{{$.Checkout}}/checkout">
          <input type="hidden" name="csrf" value="{{$.CSRF}}">
          <input type="hidden" name="tier" value="{{.ID}}">
          <button class="btn small" type="submit">Upgrade to {{.Label}}{{if .Price}} · {{.Price}}{{end}}</button>
        </form>
      {{else if and (not .Paid) $.SelfSelect}}
        <form class="inline" method="post" action="/account/tier">
          <input type="hidden" name="csrf" value="{{$.CSRF}}">
          <input type="hidden" name="tier" value="{{.ID}}">
          <button class="btn small" type="submit">Switch to {{.Label}}</button>
        </form>
      {{else}}
        <button class="btn small" type="button" disabled>{{.Label}}{{if .Price}} · {{.Price}}{{end}}</button>
      {{end}}
    {{end}}
    </div>
  </div>
  {{end}}

  <div class="card" style="border-color:rgba(242,109,109,.25)">
    <h3 style="color:var(--danger)">Danger zone</h3>
    <form method="post" action="/account/delete" onsubmit="return confirm('Delete your account AND all its sites? This cannot be undone.')">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <button class="btn danger" type="submit">Delete account and all sites</button>
    </form>
  </div>
</main>
` + pageFoot))

var msgTmpl = template.Must(template.New("msg").Parse(pageHead + `
<main class="acct"><div class="authwrap"><div class="card">
  <h1>{{.Title}}</h1>
  <p class="muted">{{.Body}}</p>
  {{if .Detail}}<div class="secretrow pass" style="margin-top:16px"><span class="k">Value</span><span class="v" style="user-select:all">{{.Detail}}</span></div>{{end}}
  <div style="margin-top:18px"><a class="btn" href="{{.Back}}">Back</a></div>
</div></div></main>
` + pageFoot))
