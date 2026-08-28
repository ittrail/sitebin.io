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
  {{else}}
    <h1>Sign in</h1>
    <p class="muted">Access your account dashboard.</p>
  {{end}}
  {{if .LocalAuth}}
  <form method="post" action="{{if eq .Mode "signup"}}/account/signup{{else}}/account/login{{end}}">
    <label class="f" for="email">Email</label>
    <input type="email" id="email" name="email" required autocomplete="email" value="{{.Email}}">
    <label class="f" for="password">Password</label>
    <input type="password" id="password" name="password" required autocomplete="{{if eq .Mode "signup"}}new-password{{else}}current-password{{end}}">
    {{if .Error}}<div class="inline-status err" style="margin-top:10px">{{.Error}}</div>{{end}}
    <div style="margin-top:16px"><button class="btn primary" type="submit">{{if eq .Mode "signup"}}Create account{{else}}Sign in{{end}}</button></div>
  </form>
  {{else if .Error}}<div class="inline-status err" style="margin-top:10px">{{.Error}}</div>{{end}}
  {{if .Providers}}
  {{if .LocalAuth}}<div style="margin:18px 0;text-align:center;color:var(--ink-faint);font-size:13px">or continue with</div>{{end}}
  <div style="display:grid;gap:8px">
    {{range .Providers}}<a class="btn primary" style="justify-content:center" href="/account/auth/{{.ID}}">{{.Label}}</a>{{end}}
  </div>
  {{end}}
  {{if .LocalAuth}}
  <div class="switch-link">
    {{if eq .Mode "signup"}}Already have an account? <a href="/account/login">Sign in</a>{{else}}New here? <a href="/account/signup">Create an account</a>{{end}}
    {{if and (eq .Mode "login") .EmailEnabled}}<br><a href="/account/reset">Forgot your password?</a>{{end}}
  </div>
  {{end}}
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
    {{if .IsAdmin}}<a class="btn small" href="/account/admin">Instance register</a>{{end}}
    <form class="inline" method="post" action="/account/logout"><button class="btn small" type="submit">Sign out</button></form>
  </div>

  <div class="card">
    <h3>Your sites <span class="count">{{len .Sites}}</span></h3>
    {{if not .Sites}}<p class="muted">No sites yet. <a href="/">Publish one</a> — it will be owned by this account.</p>{{end}}
    {{range .Sites}}
    <div class="sitecard">
      <div class="grow">
        <a class="plain" href="{{.ViewURL}}" target="_blank" rel="noopener">{{.ViewURL}}</a>
        <div class="u">{{.Mode}} · {{.SizeText}} · {{.Files}} files · {{.ExpiryText}}</div>
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

  <div class="card">
    <h3>API tokens <span class="count">{{len .Tokens}}</span></h3>
    <p class="muted">Send one as <code>Authorization: Bearer &lt;token&gt;</code> to create sites and manage the ones this account owns — no per-site edit password needed. A token cannot change your account.</p>
    {{range .Tokens}}
    <div class="sitecard">
      <div class="grow">
        <strong>{{if .Name}}{{.Name}}{{else}}Unnamed token{{end}}</strong>
        <div class="u">{{.Prefix}}… · created {{.CreatedText}}</div>
      </div>
      <form class="inline" method="post" action="/account/tokens/{{.ID}}/delete">
        <input type="hidden" name="csrf" value="{{.CSRF}}">
        <button class="btn small danger" type="submit">Revoke</button>
      </form>
    </div>
    {{end}}
    <form method="post" action="/account/tokens" style="display:flex;gap:8px;align-items:flex-end;margin-top:14px;flex-wrap:wrap">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <div style="flex:1;min-width:200px">
        <label class="f" for="tokname">Name (optional)</label>
        <input id="tokname" type="text" name="name" maxlength="60" placeholder="e.g. build agent" style="width:100%;background:var(--bg-raise);color:var(--ink);border:1px solid var(--line);border-radius:9px;padding:9px 12px;font:14px var(--body)">
      </div>
      <button class="btn small primary" type="submit">Create token</button>
    </form>
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

// adminConsoleCSS is the console's own layout. It rides on the community
// app.css tokens like the rest of the dashboard; only the wide shell, the
// figure stubs and the table are page-specific.
const adminConsoleCSS = `
<style>
  .adm { width: min(1180px, 100%); margin: 5vh auto 8vh; padding: 0 18px; }
  .adm h1 { font: 650 26px var(--display); letter-spacing: -.02em; margin-bottom: 2px; }
  .adm .lede { color: var(--ink-dim); font-size: 14px; margin-bottom: 22px; }
  .adm .lede a { color: var(--ink-dim); }

  /* figures: ticket stubs, punched on the left like a torn-off counterfoil */
  .adm .figures { display: grid; grid-template-columns: repeat(auto-fit, minmax(148px, 1fr)); gap: 12px; margin-bottom: 26px; }
  .adm .fig {
    position: relative; padding: 14px 16px 13px 20px; border-radius: 10px;
    background: linear-gradient(160deg, #151e33, #101727);
    border: 1px dashed var(--line); overflow: hidden;
  }
  .adm .fig::before {
    content: ""; position: absolute; left: -7px; top: 50%; width: 14px; height: 14px;
    border-radius: 50%; background: var(--bg); transform: translateY(-50%);
    border: 1px dashed rgba(245,184,77,.35);
  }
  .adm .fig .k { display: block; font: 600 10px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-faint); }
  .adm .fig .v { display: block; font: 650 24px var(--display); letter-spacing: -.02em; margin-top: 3px; }
  .adm .fig.warn { border-color: rgba(245,184,77,.5); }
  .adm .fig.warn .v { color: var(--amber); }
  .adm .fig.warn::before { border-color: rgba(245,184,77,.55); }
  .adm .fig.alarm { border-color: rgba(242,109,109,.55); }
  .adm .fig.alarm .v { color: var(--danger); }
  .adm .fig.alarm::before { border-color: rgba(242,109,109,.6); }
  .adm .row .flag { display: block; margin-top: 3px; font: 11px var(--mono); color: var(--danger); word-break: break-all; }

  /* filter bar */
  .adm .bar { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; margin-bottom: 14px; }
  .adm .bar input[type=search], .adm .bar select {
    background: var(--bg-raise); color: var(--ink); border: 1px solid var(--line);
    border-radius: 9px; padding: 9px 12px; font: 13px var(--body);
  }
  .adm .bar input[type=search] { min-width: 260px; flex: 1; font-family: var(--mono); }
  .adm .bar .count { margin-left: auto; font: 12px var(--mono); color: var(--ink-faint); }

  .adm .flash { border: 1px dashed rgba(94,207,138,.5); color: var(--ok); border-radius: 9px; padding: 9px 13px; font: 12px var(--mono); margin-bottom: 14px; }

  /* the register */
  .adm .reg { border: 1px solid var(--line-soft); border-radius: var(--radius); overflow: hidden; background: var(--bg-card); }
  .adm .rowhead, .adm .row { display: grid; grid-template-columns: minmax(196px,1.9fr) minmax(132px,1.1fr) 84px 104px 92px minmax(96px,.8fr) 232px; gap: 12px; align-items: center; padding: 10px 16px; }
  .adm .rowhead { font: 600 10px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-faint); background: var(--bg-raise); border-bottom: 1px solid var(--line); }
  .adm .row { border-top: 1px solid var(--line-soft); font-size: 13px; }
  .adm .row:first-of-type { border-top: 0; }
  .adm .row:hover { background: rgba(91,140,255,.045); }
  .adm .row .id { font: 12px var(--mono); word-break: break-all; }
  .adm .row .id a { color: var(--ink); }
  .adm .row .dom { display: block; font: 11px var(--mono); color: var(--amber); margin-top: 3px; word-break: break-all; }
  .adm .row .own { font-size: 12px; color: var(--ink-dim); word-break: break-all; }
  .adm .row .own.anon { color: var(--ink-faint); font-style: italic; }
  .adm .row .num { font: 12px var(--mono); color: var(--ink-dim); }
  .adm .row .exp { font: 12px var(--mono); color: var(--ink-faint); white-space: nowrap; }
  .adm .row .exp.soon { color: var(--amber); }
  .adm .row .exp.soon::after { content: " ⚑"; }
  .adm .row .acts { display: flex; gap: 6px; align-items: center; justify-content: flex-end; flex-wrap: nowrap; }
  .adm .row .acts form.inline { display: flex; gap: 6px; align-items: center; }
  .adm .row .acts input[type=date] {
    background: var(--bg-raise); color: var(--ink); border: 1px solid var(--line);
    border-radius: 7px; padding: 4px 6px; font: 11px var(--mono); color-scheme: dark; width: 118px;
  }
  .adm .row.confirm { grid-template-columns: minmax(200px,1.1fr) 1fr auto; background: rgba(242,109,109,.07); box-shadow: inset 3px 0 0 var(--danger); }
  .adm .row.confirm .warnmsg { font-size: 12px; color: var(--ink-dim); }
  .adm .empty { padding: 40px 16px; text-align: center; color: var(--ink-faint); font: 13px var(--mono); }
  @media (max-width: 900px) {
    .adm .rowhead { display: none; }
    .adm .row { grid-template-columns: 1fr; gap: 6px; }
    .adm .row .acts { justify-content: flex-start; }
  }
</style>`

var adminTmpl = template.Must(template.New("admin").Parse(pageHead + adminConsoleCSS + `
<main class="adm">
  <h1>Instance register</h1>
  <p class="lede">Every site on this instance — yours, other accounts', and anonymous drops. Signed in as {{.Email}} · <a href="/account">back to your account</a></p>

  {{if eq .Flash "deleted"}}<p class="flash">Site deleted.</p>{{end}}
  {{if eq .Flash "expiry"}}<p class="flash">Expiry updated.</p>{{end}}

  <section class="figures">
    <div class="fig"><span class="k">Sites</span><span class="v">{{.Figures.Sites}}</span></div>
    <div class="fig"><span class="k">Account-owned</span><span class="v">{{.Figures.Owned}}</span></div>
    <div class="fig"><span class="k">Anonymous</span><span class="v">{{.Figures.Anonymous}}</span></div>
    <div class="fig"><span class="k">Stored</span><span class="v">{{.Figures.HumanBytes}}</span></div>
    <div class="fig"><span class="k">Files</span><span class="v">{{.Figures.Files}}</span></div>
    <div class="fig{{if .Figures.ExpiringSoon}} warn{{end}}"><span class="k">Due in 7 days</span><span class="v">{{.Figures.ExpiringSoon}}</span></div>
    <div class="fig{{if .Figures.Flagged}} alarm{{end}}"><span class="k">CSP-blocked</span><span class="v">{{.Figures.Flagged}}</span></div>
  </section>

  <form class="bar" method="get" action="/account/admin">
    <input type="search" name="q" value="{{.Query}}" placeholder="view id, owner email or domain" aria-label="Search sites">
    <select name="filter" aria-label="Filter sites">
      <option value=""{{if eq .Filter ""}} selected{{end}}>All sites</option>
      <option value="owned"{{if eq .Filter "owned"}} selected{{end}}>Account-owned</option>
      <option value="anon"{{if eq .Filter "anon"}} selected{{end}}>Anonymous</option>
      <option value="expiring"{{if eq .Filter "expiring"}} selected{{end}}>Expiring within 7 days</option>
      <option value="flagged"{{if eq .Filter "flagged"}} selected{{end}}>Blocked by CSP</option>
    </select>
    <button class="btn small" type="submit">Apply</button>
    <span class="count">{{.Shown}} shown</span>
  </form>

  <section class="reg">
    <div class="rowhead">
      <span>Site</span><span>Owner</span><span>Mode</span><span>Size</span><span>Created</span><span>Expiry</span><span style="text-align:right">Actions</span>
    </div>
    {{range .Rows}}
    {{if .Confirming}}
    <div class="row confirm">
      <span class="id">{{.ViewID}}{{if .DomainsText}}<span class="dom">{{.DomainsText}}</span>{{end}}</span>
      <span class="warnmsg">Delete this site permanently? Its {{.Files}} file(s) and any custom domain go with it. This cannot be undone.</span>
      <span class="acts">
        <form method="post" action="/account/admin/sites/{{.ViewID}}/delete{{if $.Params}}?{{$.ParamsQ}}{{end}}" class="inline">
          <input type="hidden" name="csrf" value="{{$.CSRF}}">
          <button class="btn small danger" type="submit">Yes, delete {{.ViewID}}</button>
        </form>
        <a class="btn small" href="/account/admin?{{$.ParamsQ}}">Cancel</a>
      </span>
    </div>
    {{else}}
    <div class="row">
      <span class="id"><a href="{{.ViewURL}}" rel="noreferrer noopener" target="_blank">{{.ViewID}}</a>{{if .DomainsText}}<span class="dom">{{.DomainsText}}</span>{{end}}</span>
      <span class="own{{if not .Owner}} anon{{end}}">{{.OwnerLabel}}{{if .Violations}}<span class="flag" title="{{.BlockedText}}">&#9888; {{.Violations}} blocked</span>{{end}}</span>
      <span class="num">{{.Mode}}</span>
      <span class="num">{{.SizeText}} · {{.Files}}f</span>
      <span class="num">{{.CreatedText}}</span>
      <span class="exp{{if .ExpiringNow}} soon{{end}}">{{.ExpiryText}}</span>
      <span class="acts">
        <form method="post" action="/account/admin/sites/{{.ViewID}}/expiry" class="inline">
          <input type="hidden" name="csrf" value="{{$.CSRF}}">
          <input type="date" name="expires" value="{{.ExpiryValue}}" aria-label="Expiry for {{.ViewID}}">
          <button class="btn small" type="submit">Set</button>
        </form>
        <a class="btn small danger" href="/account/admin?confirm={{.ViewID}}{{$.Params}}">Delete</a>
      </span>
    </div>
    {{end}}
    {{else}}
    <p class="empty">No site matches.</p>
    {{end}}
  </section>
</main>
` + pageFoot))
