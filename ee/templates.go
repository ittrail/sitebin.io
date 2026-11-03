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
  .acct .sitecard .sname { display: block; font: 600 15px var(--body); color: var(--ink); margin-bottom: 2px; overflow-wrap: anywhere; }
  .acct .sitecard .doms { display: flex; flex-wrap: wrap; gap: 2px 14px; margin-top: 3px; }
  .acct .sitecard .doms a { font: 12px var(--mono); color: var(--amber); word-break: break-all; }
  .acct .sitecard .doms .pend { font: 12px var(--mono); color: rgba(245,184,77,.5); word-break: break-all; }
  .acct .sitecard .doms .redir { font: 12px var(--mono); color: var(--ink-faint); word-break: break-all; }
  .acct .sitecard .viewoff { font: 13px var(--mono); color: var(--ink-faint); word-break: break-all; }
  .acct .sitecard .locked { margin-top: 4px; font: 12px var(--mono); color: var(--danger); overflow-wrap: anywhere; }
  .acct .sitecard details.rename { margin-top: 6px; }
  .acct .sitecard details.rename summary { display: inline-block; cursor: pointer; list-style: none; font: 12px var(--mono); color: var(--ink-dim); }
  .acct .sitecard details.rename summary::-webkit-details-marker { display: none; }
  .acct .sitecard details.rename[open] summary { color: var(--ink); }
  .acct .sitecard details.rename form { display: flex; gap: 8px; margin-top: 8px; flex-wrap: wrap; align-items: center; }
  .acct .sitecard details.rename input[type=text] { flex: 1; min-width: 180px; background: var(--bg-raise); color: var(--ink); border: 1px solid var(--line); border-radius: 9px; padding: 7px 11px; font: 14px var(--body); }
  /* Tags. The palette is here and only here: data names a palette id, which
     becomes a class (tag-blue ...), never a value in a style attribute. */
  :root { --tag-blue: #5b8cff; --tag-green: #5ecf8a; --tag-amber: #f5b84d; --tag-red: #f26d6d; --tag-violet: #a98bff; --tag-teal: #3fc6c6; --tag-pink: #ff86c8; --tag-grey: #97a3b8; }
  .acct .tag-blue { --c: var(--tag-blue); } .acct .tag-green { --c: var(--tag-green); } .acct .tag-amber { --c: var(--tag-amber); } .acct .tag-red { --c: var(--tag-red); }
  .acct .tag-violet { --c: var(--tag-violet); } .acct .tag-teal { --c: var(--tag-teal); } .acct .tag-pink { --c: var(--tag-pink); } .acct .tag-grey { --c: var(--tag-grey); }
  .acct .tdot { width: 9px; height: 9px; border-radius: 50%; display: inline-block; flex: none; background: var(--c, var(--tag-grey)); }
  .acct .card > h3 .tagmanage { margin-left: auto; }
  .acct .card > h3.sitesh { flex-wrap: wrap; }
  .acct .tagfilter { margin-bottom: 8px; }
  .acct .tftop { display: flex; gap: 6px; flex-wrap: wrap; margin-bottom: 10px; }
  .acct a.tfchip { display: inline-flex; align-items: center; gap: 6px; font: 12px var(--body); color: var(--ink-dim); background: var(--bg-raise); border: 1px solid var(--line); border-radius: 20px; padding: 3px 10px; text-decoration: none; overflow-wrap: anywhere; }
  .acct a.tfchip:hover { border-color: #334160; color: var(--ink); }
  .acct a.tfchip small { font: 11px var(--mono); color: var(--ink-faint); }
  .acct a.tfchip.meta { border-style: dashed; }
  .acct a.tfchip.on { border-color: var(--amber); box-shadow: 0 0 0 1px var(--amber) inset; color: var(--ink); }
  .acct .tfblocks { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 10px; }
  .acct .tfblock { background: var(--bg-raise); border: 1px solid var(--line-soft); border-radius: 10px; padding: 9px 10px; min-width: 0; }
  .acct .tfblock.part { border-color: rgba(245,184,77,.5); }
  .acct .tfbh { display: flex; align-items: center; gap: 7px; margin-bottom: 7px; font: 600 12px var(--body); color: var(--ink); text-transform: uppercase; letter-spacing: .04em; overflow-wrap: anywhere; }
  .acct .tfbh a.tfall { margin-left: auto; flex: none; font: 500 11px var(--body); text-transform: none; letter-spacing: 0; color: var(--ink-dim); border: 1px solid var(--line); border-radius: 20px; padding: 1px 8px; text-decoration: none; }
  .acct .tfbh a.tfall:hover { color: var(--ink); border-color: #334160; }
  .acct .tfbh a.tfall.on { color: var(--bg); background: var(--amber); border-color: var(--amber); }
  .acct .tfbt { display: flex; flex-wrap: wrap; gap: 5px; }
  .acct .tfsum { margin: 12px 0 0; font: 12px var(--mono); color: var(--ink-dim); overflow-wrap: anywhere; }
  .acct .sitecard .stags { display: flex; flex-wrap: wrap; align-items: center; gap: 5px; margin-top: 6px; }
  .acct .tchip { display: inline-flex; align-items: center; gap: 1px; margin: 0; padding: 1px 7px; border-radius: 6px; font: 11.5px var(--body); overflow-wrap: anywhere;
    border: 1px solid var(--line); color: var(--ink);
    border-color: color-mix(in srgb, var(--c, var(--tag-grey)) 50%, transparent);
    background: color-mix(in srgb, var(--c, var(--tag-grey)) 14%, transparent);
    color: color-mix(in srgb, var(--c, var(--tag-grey)) 65%, #fff); }
  .acct form.tchip { padding-right: 1px; }
  .acct .tchip button { border: 0; background: none; color: inherit; opacity: .55; cursor: pointer; font: 13px/1 var(--body); padding: 0 4px; border-radius: 4px; }
  .acct .tchip button:hover, .acct .tchip button:focus-visible { opacity: 1; color: var(--danger); background: rgba(242,109,109,.12); }
  .acct .sitecard details.addtag summary { display: inline-flex; cursor: pointer; list-style: none; font: 11.5px var(--body); color: var(--ink-dim); border: 1px dashed var(--line); border-radius: 6px; padding: 1px 7px; }
  .acct .sitecard details.addtag summary::-webkit-details-marker { display: none; }
  .acct .sitecard details.addtag summary:hover, .acct .sitecard details.addtag[open] summary { color: var(--ink); border-color: var(--amber); }
  .acct .sitecard details.addtag[open] { flex-basis: 100%; }
  .acct .sitecard details.addtag form { display: flex; gap: 8px; margin-top: 8px; flex-wrap: wrap; align-items: center; }
  .acct .sitecard details.addtag input[type=text] { flex: 1; min-width: 160px; background: var(--bg-raise); color: var(--ink); border: 1px solid var(--line); border-radius: 9px; padding: 7px 11px; font: 14px var(--body); }
  .acct .sitecard details.addtag select { background: var(--bg-raise); color: var(--ink); border: 1px solid var(--line); border-radius: 9px; padding: 7px 9px; font: 14px var(--body); max-width: 100%; }
  .acct .authwrap { width: min(420px, 100%); margin: 10vh auto; }
  .acct .switch-link { text-align: center; margin-top: 14px; font-size: 14px; }
  .acct label.f { display:block; font-size: 13px; color: var(--ink-dim); margin: 12px 0 6px; }
  .acct .secretrow .v { word-break: break-all; overflow-wrap: anywhere; }
  .acct .secretout { margin-top: 16px; }
  .acct .secretout .val {
    display: block; font: 13px var(--mono); color: var(--ink); user-select: all;
    background: var(--bg-raise); border: 1px dashed var(--line); border-radius: 9px;
    padding: 12px 14px; word-break: break-all; overflow-wrap: anywhere; line-height: 1.5;
  }
  .acct .secretout .row { display: flex; gap: 10px; align-items: center; margin-top: 10px; flex-wrap: wrap; }
  /* Permanent licence notice. No dismiss control, deliberately: it is the
     operator's only warning and it belongs to the account UI alone. */
  .acct .licnotice { border-left: 4px solid var(--accent, #F5B84D); }
  .acct .licnotice.err { border-left-color: #E5534B; }
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
  {{/* One click stays one click; this asks the provider who is signing in
       instead of letting the session it holds answer (fresh=1). */}}
  <div class="switch-link">{{if eq (len .Providers) 1}}{{with index .Providers 0}}<a href="/account/auth/{{.ID}}?fresh=1">Use a different account</a>{{end}}{{else}}Use a different account: {{range $i, $b := .Providers}}{{if $i}} · {{end}}<a href="/account/auth/{{$b.ID}}?fresh=1">{{$b.Label}}</a>{{end}}{{end}}</div>
  {{end}}
  {{if .LocalAuth}}
  <div class="switch-link">
    {{if eq .Mode "signup"}}Already have an account? <a href="/account/login">Sign in</a>{{else}}New here? <a href="/account/signup">Create an account</a>{{end}}
    {{if and (eq .Mode "login") .EmailEnabled}}<br><a href="/account/reset">Forgot your password?</a>{{end}}
  </div>
  {{end}}
</div></div></main>
` + pageFoot))

// signedOutTmpl is where every sign-out ends. No script, no refresh: it must
// never send the browser anywhere by itself (see handleSignedOut). With one
// provider and no password form it is a single "Sign in"; otherwise each
// provider, and the password form where there is one.
var signedOutTmpl = template.Must(template.New("signedout").Parse(pageHead + `
<main class="acct"><div class="authwrap"><div class="card">
  <h1>You are signed out</h1>
  <p class="muted">Your Sitebin session has ended on every device. Sign in again to come back — as yourself or with a different account.</p>
  <div style="display:grid;gap:8px;margin-top:18px">
  {{if and (eq (len .Providers) 1) (not .LocalAuth)}}
    {{with index .Providers 0}}<a class="btn primary" style="justify-content:center" href="/account/auth/{{.ID}}?fresh=1">Sign in</a>{{end}}
  {{else}}
    {{range .Providers}}<a class="btn primary" style="justify-content:center" href="/account/auth/{{.ID}}?fresh=1">{{.Label}}</a>{{end}}
    {{if .LocalAuth}}<a class="btn" style="justify-content:center" href="/account/login">Sign in with email and password</a>{{end}}
  {{end}}
  </div>
  {{if .Providers}}<p class="muted" style="margin-top:14px">You will be asked which account to use.</p>{{end}}
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
    {{if .AccountURL}}<a class="btn small" href="{{.AccountURL}}" rel="noopener" title="Password, sign-in methods, sessions, data export and account deletion">Manage account</a>{{end}}
    <form class="inline" method="post" action="/account/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="btn small" type="submit">Sign out</button></form>
  </div>

  {{with .License}}
  <div class="card licnotice {{.Severity}}" role="alert">
    <strong>Sitebin Enterprise licence</strong>
    <p class="muted" style="margin-top:6px">{{.Text}}</p>
  </div>
  {{end}}

  <div class="card">
    {{/* The publish link is always here: with a front door, "/" sends a
         signed-in visitor straight back to this page. */}}
    <h3 class="sitesh">Your sites <span class="count">{{.SitesTotal}}</span><a class="btn small tagmanage" href="/account/tags">Manage tags</a><a class="btn small primary" href="{{.PublishURL}}">Publish a new site</a></h3>
    {{/* The tag filter: every chip and toggle is a link to the next filter
         state, so it works without script. */}}
    {{with .Filter}}{{if .Show}}
    <nav class="tagfilter" aria-label="Filter by tag">
      <div class="tftop"><a class="tfchip{{if .AllSelected}} on{{end}}" href="{{.AllHref}}">All <small>{{.All}}</small></a><a class="tfchip meta{{if .UntaggedSelected}} on{{end}}" href="{{.UntaggedHref}}">Untagged <small>{{.Untagged}}</small></a></div>
      {{if .Blocks}}<div class="tfblocks">
        {{range .Blocks}}<div class="tfblock{{if .Partial}} part{{end}}">
          <div class="tfbh tag-{{.Color}}"><span class="tdot"></span>{{.Name}}{{if .WholeHref}}<a class="tfall{{if .Whole}} on{{end}}" href="{{.WholeHref}}" title="Any tag of this group">whole group</a>{{end}}</div>
          <div class="tfbt">{{range .Chips}}<a class="tfchip tag-{{.Color}}{{if .Selected}} on{{end}}" href="{{.Href}}"><span class="tdot"></span>{{.Name}} <small>{{.Count}}</small></a>{{end}}</div>
        </div>
        {{end}}
      </div>{{end}}
      {{if .Summary}}<p class="tfsum">{{.Summary}} · <a href="{{.ClearHref}}">Clear filter</a></p>{{end}}
    </nav>
    {{end}}{{end}}
    {{if not .SitesTotal}}<p class="muted">No sites yet. <a href="{{.PublishURL}}">Publish one</a> — it will be owned by this account.</p>{{else if not .Sites}}<p class="muted">No site matches this filter. <a href="/account">Show all sites</a></p>{{end}}
    {{if .Sites}}<datalist id="tagopts">{{range .TagOptions}}<option value="{{.}}">{{end}}</datalist>{{end}}
    {{range .Sites}}{{$site := .}}
    <div class="sitecard">
      <div class="grow">
        {{if .Name}}<span class="sname">{{.Name}}</span>{{end}}
        {{if .ViewOff}}<span class="viewoff" title="The generated address does not serve this site">{{.ViewURL}} · {{if .ViewRedirectTo}}&rarr; {{.ViewRedirectTo}}{{else}}off{{end}}</span>{{else}}<a class="plain" href="{{.ViewURL}}" target="_blank" rel="noopener">{{.ViewURL}}</a>{{end}}
        {{if .DomainLinks}}<div class="doms">{{range .DomainLinks}}{{if .Pending}}<span class="pend" title="Claimed, waiting for its DNS record">{{.Domain}} · pending DNS</span>{{else if .RedirectTo}}<span class="redir" title="Redirects to {{.RedirectTo}}"><a href="{{.URL}}" target="_blank" rel="noopener">{{.Domain}}</a> &rarr; {{.RedirectTo}}</span>{{else}}<a href="{{.URL}}" target="_blank" rel="noopener">{{.Domain}}</a>{{end}}{{end}}</div>{{end}}
        <div class="u">{{.Mode}} · {{.SizeText}} · {{.Files}} files · {{.ExpiryText}}</div>
        {{if .Locked}}
        {{if .Tags}}<div class="stags">{{range .Tags}}<span class="tchip tag-{{.Color}}"><span class="tn">{{.Name}}</span></span>{{end}}</div>{{end}}
        <div class="locked">Locked by the operator on {{.LockedText}}{{if .Locked.Reason}}: {{.Locked.Reason}}{{end}}. It is not served, and it cannot be changed or deleted.</div>
      </div>
      {{else}}
        <div class="stags">
          {{range .Tags}}<form class="tchip tag-{{.Color}}" method="post" action="/account/sites/{{$site.ViewID}}/tags/remove"><span class="tn">{{.Name}}</span><input type="hidden" name="csrf" value="{{$site.CSRF}}"><input type="hidden" name="tag" value="{{.Name}}"><input type="hidden" name="back" value="{{$.Back}}"><button type="submit" title="Remove this tag" aria-label="Remove the tag {{.Name}}">&times;</button></form>{{end}}
          <details class="addtag">
            <summary>+ Tag</summary>
            <form method="post" action="/account/sites/{{.ViewID}}/tags/add">
              <input type="hidden" name="csrf" value="{{.CSRF}}">
              <input type="hidden" name="back" value="{{$.Back}}">
              <input type="text" name="tag" maxlength="30" required list="tagopts" autocomplete="off" placeholder="New or existing tag" aria-label="Tag for {{.ViewID}}">
              {{if $.Groups}}<select name="group" aria-label="Group, for a new tag" title="Used only when the tag is new"><option value="">No group</option>{{range $.Groups}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select>{{end}}
              <button class="btn small primary" type="submit">Add</button>
            </form>
          </details>
        </div>
        <details class="rename">
          <summary>&#9998; {{if .Name}}Rename{{else}}Add a name{{end}}</summary>
          <form method="post" action="/account/sites/{{.ViewID}}/name">
            <input type="hidden" name="csrf" value="{{.CSRF}}">
            <input type="text" name="name" maxlength="60" value="{{.Name}}" placeholder="e.g. Client docs" aria-label="Name for {{.ViewID}}">
            <button class="btn small primary" type="submit">Save</button>
          </form>
        </details>
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
      {{end}}
    </div>
    {{end}}
  </div>

  <div class="card">
    <h3>API tokens <span class="count">{{len .Tokens}}</span></h3>
    <p class="muted">Send one as <code>Authorization: Bearer &lt;token&gt;</code> to create sites and manage the ones this account owns — no per-site edit password needed. A token cannot change your account.</p>
    <p class="muted">The same token connects an AI agent: point any MCP client at <code>{{.MCPEndpoint}}</code> with that header. See <a href="https://sitebin.io/docs/mcp/" rel="noreferrer noopener" target="_blank">the MCP docs</a>.</p>
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
        <input id="tokname" type="text" name="name" maxlength="60" placeholder="what is it for?" style="width:100%;background:var(--bg-raise);color:var(--ink);border:1px solid var(--line);border-radius:9px;padding:9px 12px;font:14px var(--body)">
      </div>
      <button class="btn small primary" type="submit">Create token</button>
    </form>
  </div>

  {{if or .Zones .ZonesMax}}
  <div class="card" id="zones">
    <h3>Zones <span class="count">{{len .Zones}}{{if .ZonesMax}} / {{.ZonesMax}}{{end}}</span></h3>
    <p class="muted">A zone is a domain you own and point at Sitebin as a whole — <code>*.example.com</code>. Prove it once, and every name under it attaches to your sites at once, with no DNS record per name and no limit on how many. Nobody else can use a name in your zone.</p>
    {{range .Zones}}
    <div class="sitecard">
      <div class="grow">
        <strong>{{.Zone}}</strong>
        {{if .Verified}}
          {{if .FailingSince}}<span class="inline-status err"> · record missing</span>{{else}}<span class="inline-status"> · verified</span>{{end}}
          <div class="u">{{len .Names}} name{{if ne (len .Names) 1}}s{{end}} in use{{range .Names}} · {{.Domain}}{{end}}</div>
          {{if .FailingSince}}<div class="u">The TXT record below is gone. Put it back, or the zone is released three days after it was first missed.</div>{{end}}
        {{else}}
          <span class="muted"> · pending</span>
          {{if .Conflicts}}<div class="u">Another account holds a verified domain inside this zone: {{range $i, $c := .Conflicts}}{{if $i}}, {{end}}{{$c}}{{end}}. The zone verifies once that domain is gone.</div>{{end}}
        {{end}}
        <div class="u">TXT <code>{{.TXTName}}</code> = <code>{{.TXTValue}}</code></div>
        {{if not .Verified}}<div class="u">Point the zone here too: <code>*.{{.Zone}}</code> as a CNAME to this instance, or an A record to its address. Once the TXT record exists, press Check now (your DNS provider may take a minute to publish it); the zone is also checked automatically every few minutes. Unproven claims are dropped after 7 days.</div>{{end}}
      </div>
      {{if not .Verified}}
      <form class="inline" method="post" action="/account/zones">
        <input type="hidden" name="csrf" value="{{.CSRF}}">
        <input type="hidden" name="zone" value="{{.Zone}}">
        <button class="btn small" type="submit">Check now</button>
      </form>
      {{end}}
      <form class="inline" method="post" action="/account/zones/{{.Zone}}/delete">
        <input type="hidden" name="csrf" value="{{.CSRF}}">
        <button class="btn small danger" type="submit">Remove</button>
      </form>
    </div>
    {{end}}
    {{if .ZonesMax}}
    <form method="post" action="/account/zones" style="display:flex;gap:8px;align-items:flex-end;margin-top:14px;flex-wrap:wrap">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <div style="flex:1;min-width:200px">
        <label class="f" for="zonename">Zone</label>
        <input id="zonename" type="text" name="zone" maxlength="253" placeholder="example.com" style="width:100%;background:var(--bg-raise);color:var(--ink);border:1px solid var(--line);border-radius:9px;padding:9px 12px;font:14px var(--body)">
      </div>
      <button class="btn small primary" type="submit">Add zone</button>
    </form>
    <p class="muted">Removing a zone detaches nothing at once: names that relied on it need their own DNS proof within three days. See <a href="https://sitebin.io/docs/custom-domains/#zones" rel="noreferrer noopener" target="_blank">the docs</a>.</p>
    {{else}}
    <p class="muted">Your current plan does not include zones. What you hold keeps working; new zones and new names need a plan that includes them.</p>
    {{end}}
  </div>
  {{end}}

  {{if .Portal}}
  <div class="card">
    <h3>Billing</h3>
    <p class="muted">Your plan, payment method, invoices and cancellation.</p>
    <form class="inline" method="post" action="/account/billing/portal">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <button class="btn small" type="submit">Manage subscription</button>
    </form>
    {{if .CancelURL}}<a class="btn small" href="{{.CancelURL}}" rel="noopener">Cancel contracts here</a>{{end}}
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
        <form class="inline" method="post" action="/account/upgrade">
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
    {{if .StackDeletion}}
    <p class="muted">Your account is deleted where your sign-in lives. When it is, every site, token and record this instance holds for you is removed as well.</p>
    <a class="btn danger" href="{{.DeletionURL}}" rel="noopener">Delete account</a>
    {{else}}
    <p class="muted">Removes this account, every site it owns and every API token. You will be asked to confirm on the next page.</p>
    <form method="post" action="/account/delete">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <button class="btn danger" type="submit">Delete account and all sites</button>
    </form>
    {{end}}
  </div>
  {{if .CancelURL}}<p class="muted" style="text-align:center;margin-top:22px"><a href="{{.CancelURL}}" rel="noopener">Cancel contracts here</a></p>{{end}}
</main>
` + pageFoot))

// deleteConfirmTmpl is the second step of a local account deletion. It is a
// page, not a confirm() dialog, because the dashboard's CSP has no
// 'unsafe-inline' and an inline handler would silently never run.
var deleteConfirmTmpl = template.Must(template.New("deleteconfirm").Parse(pageHead + `
<main class="acct"><div class="authwrap"><div class="card" style="border-color:rgba(242,109,109,.4)">
  <h1 style="color:var(--danger)">Delete your account?</h1>
  <p class="muted">This removes the account <strong>{{.Email}}</strong> together with
    <strong>{{.Sites}} site{{if ne .Sites 1}}s{{end}}</strong> and
    <strong>{{.Tokens}} API token{{if ne .Tokens 1}}s{{end}}</strong>. Every site URL and custom domain stops working immediately. There is no undo.</p>
  {{if .Subscription}}<p class="muted">Your paid subscription will be <strong>cancelled first</strong>, with immediate effect. If it cannot be cancelled, the account is kept and you are told why.</p>{{end}}
  <form method="post" action="/account/delete/confirm" style="margin-top:18px;display:flex;gap:10px;flex-wrap:wrap">
    <input type="hidden" name="csrf" value="{{.CSRF}}">
    <button class="btn danger" type="submit">Yes, delete my account and all its sites</button>
    <a class="btn" href="/account">Cancel</a>
  </form>
</div></div></main>
` + pageFoot))

// handoffTmpl is the page a POST answers with when its destination is
// another origin -- the stack's plan page, the backend's checkout. The
// dashboard's CSP says form-action 'self', and the browser applies that to
// the REDIRECT a form submission is answered with, so a 303 off this origin
// was dropped without a word and the customer stayed where they were. A page
// that navigates on its own is not a form action; the link is for a browser
// that does not follow the refresh.
var handoffTmpl = template.Must(template.New("handoff").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta http-equiv="refresh" content="0;url={{.URL}}">
<title>{{.Title}}</title>
<link rel="stylesheet" href="/_sitebin/assets/static/app.css">
</head><body>
<main class="acct"><div class="authwrap"><div class="card">
  <h1>{{.Title}}</h1>
  <p class="muted">{{.Body}}</p>
  <div style="margin-top:18px"><a class="btn" href="{{.URL}}" rel="noopener">Continue</a></div>
</div></div></main>
</body></html>
`))

var msgTmpl = template.Must(template.New("msg").Parse(pageHead + `
<main class="acct"><div class="authwrap"><div class="card">
  <h1>{{.Title}}</h1>
  <p class="muted">{{.Body}}</p>
  {{if .Detail}}
  <div class="secretout">
    <code class="val" id="secretval">{{.Detail}}</code>
    <div class="row">
      <button class="btn small" id="copybtn" type="button" hidden>Copy</button>
      <a class="btn small" href="{{.Back}}">Back</a>
    </div>
  </div>
  <script>` + copyScript + `</script>
  {{else}}
  <div style="margin-top:18px"><a class="btn" href="{{.Back}}">Back</a></div>
  {{end}}
</div></div></main>
` + pageFoot))

// tagsManageCSS is the tag manager's own layout, on the dashboard's tokens
// and the palette classes in pageHead (a colour is only ever a class name).
const tagsManageCSS = `
<style>
  .acct.tagsmgr { width: min(820px, 100%); }
  .acct .tmhead { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; margin-bottom: 4px; }
  .acct .tmhead h1 { margin: 0; }
  .acct .tmhead .btn { margin-left: auto; }
  .acct .tmlede { margin-bottom: 16px; }
  .acct .flash { border: 1px dashed rgba(94,207,138,.5); color: var(--ok); border-radius: 9px; padding: 9px 13px; font: 12px var(--mono); margin-bottom: 14px; }
  .acct .tmwarn { border: 1px dashed rgba(242,109,109,.5); color: var(--danger); border-radius: 9px; padding: 9px 13px; font: 12px var(--mono); margin-bottom: 14px; }
  .acct .tmgroup { border-top: 1px solid var(--line-soft); padding: 12px 0 8px; }
  .acct .tmgroup:first-child { border-top: 0; padding-top: 0; }
  .acct .tmgh, .acct .tmtag { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 12px; }
  .acct .tmgh { min-height: 30px; }
  .acct .tmglbl { font: 600 12.5px var(--body); text-transform: uppercase; letter-spacing: .04em; color: var(--ink); min-width: 120px; overflow-wrap: anywhere; }
  .acct .tmglbl.none { color: var(--ink-dim); }
  .acct .tmuse { font: 11px var(--mono); color: var(--ink-faint); min-width: 58px; }
  .acct .tmtag { padding: 5px 0 5px 19px; font-size: 12px; }
  .acct .tmname { min-width: 150px; }
  .acct .tminh { font-size: 11.5px; color: var(--ink-faint); min-width: 170px; }
  .acct .tminh.own { color: var(--amber); }
  .acct .tmfirst { margin-left: auto; }
  .acct .tmempty { padding: 4px 0 4px 19px; font-size: 12px; color: var(--ink-faint); }
  .acct .tmhint { margin-top: 6px; font-size: 11.5px; color: var(--ink-faint); }
  /* The palette: eight radio inputs, each drawn as a dot in its class's colour. */
  .acct .pal { display: inline-flex; align-items: center; gap: 6px; flex-wrap: wrap; }
  .acct .pal label { position: relative; display: inline-flex; align-items: center; justify-content: center; width: 18px; height: 18px; cursor: pointer; }
  .acct .pal input { position: absolute; inset: 0; width: 100%; height: 100%; margin: 0; opacity: 0; cursor: pointer; }
  .acct .pal label span { width: 14px; height: 14px; border-radius: 50%; background: var(--c); opacity: .45; transition: opacity .12s, box-shadow .12s; }
  .acct .pal label:hover span { opacity: .8; }
  .acct .pal input:checked + span { opacity: 1; box-shadow: 0 0 0 2px var(--bg-card), 0 0 0 4px var(--c); }
  .acct .pal input:focus-visible + span { outline: 2px solid var(--blue); outline-offset: 4px; }
  .acct .pal .btn { margin-left: 6px; }
  /* A group's "Set colour" shows once another dot is picked, where :has() is known. */
  .acct form.pal:has(input.cur:checked) .btn { visibility: hidden; }
  /* Row actions: disclosures that open a small form beneath the row. */
  .acct details.tmact summary, .acct button.linkbtn { display: inline-block; cursor: pointer; list-style: none; border: 0; background: none; padding: 0; font: 11.5px var(--body); color: var(--ink-dim); text-decoration: underline; text-decoration-color: var(--line); text-underline-offset: 3px; }
  .acct details.tmact summary::-webkit-details-marker { display: none; }
  .acct details.tmact summary:hover, .acct details.tmact[open] summary, .acct button.linkbtn:hover, .acct button.linkbtn:focus-visible { color: var(--ink); text-decoration-color: var(--amber); }
  .acct button.linkbtn.danger:hover, .acct button.linkbtn.danger:focus-visible { color: var(--danger); text-decoration-color: var(--danger); }
  .acct .tminh button.linkbtn { color: var(--amber); }
  .acct details.tmact[open] { flex-basis: 100%; order: 1; padding: 2px 0 6px; }
  .acct details.tmact form, .acct details.tmnew form { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 10px; margin-top: 8px; }
  .acct.tagsmgr input[type=text], .acct.tagsmgr select { width: auto; min-width: 180px; max-width: 100%; padding: 6px 10px; font: 13px var(--body); border-radius: 8px; background: var(--bg-raise); }
  .acct details.tmnew { margin-top: 10px; border-top: 1px solid var(--line-soft); padding-top: 12px; }
  .acct details.tmnew summary { display: inline-block; cursor: pointer; list-style: none; font: 600 13px var(--body); color: var(--amber); }
  .acct details.tmnew summary::-webkit-details-marker { display: none; }
  .acct p.tmnew { margin-top: 10px; border-top: 1px solid var(--line-soft); padding-top: 12px; }
  /* The confirmation and result steps. */
  .acct .card p.muted + p.muted { margin-top: 10px; }
  .acct .card.tmdanger { border-color: rgba(242,109,109,.4); }
  .acct .card.tmdanger h1 { color: var(--danger); }
  .acct .tmconfirm { margin-top: 18px; display: flex; gap: 10px; flex-wrap: wrap; }
  .acct ul.tmskipped { margin: 12px 0 0 18px; font-size: 13px; color: var(--ink-dim); }
  .acct ul.tmskipped li { margin: 5px 0; overflow-wrap: anywhere; }
  .acct ul.tmskipped strong { color: var(--ink); }
  @media (max-width: 640px) {
    .acct .tmfirst { margin-left: 0; }
    .acct .tmname, .acct .tminh, .acct .tmglbl { min-width: 0; }
  }
</style>`

// tagsTmpl is the tag manager: the groups alphabetically, each with its
// colour as eight radio dots and its tags beneath, then "No group". Every
// action is a POST form with the CSRF token; there is no script.
var tagsTmpl = template.Must(template.New("tags").Parse(pageHead + tagsManageCSS + `
<main class="acct tagsmgr">
  <div class="tmhead">
    <h1>Manage tags</h1>
    <a class="btn small" href="/account">&larr; Your sites</a>
  </div>
  <p class="muted tmlede">A group gives its tags a colour, and a tag can have its own instead. Renaming or deleting a tag changes every site of this account that carries it; groups and colours change no site.</p>
  {{with .Flash}}<p class="flash" role="status">{{.}}</p>{{end}}
  {{if .Unreadable}}<p class="tmwarn" role="alert">Your tag settings could not be read, so your tags are shown without groups or colours, and changes to them cannot be saved.</p>{{end}}
  <div class="card">
    {{if .Empty}}<p class="muted">No tags yet. Tag a site on <a href="/account">your site list</a>, or start with a group.</p>{{end}}
    {{range .Blocks}}{{$g := .}}
    <section class="tmgroup">
      <div class="tmgh tag-{{.Dot}}">
        <span class="tdot"></span>
        {{if .ID}}
        <span class="tmglbl">{{.Name}}</span>
        <span class="tmuse">{{len .Tags}} tag{{if ne (len .Tags) 1}}s{{end}}</span>
        <form class="pal" method="post" action="/account/tags/groups/{{.ID}}">
          <input type="hidden" name="csrf" value="{{$.CSRF}}">
          {{range $.Colors}}<label class="tag-{{.}}" title="{{.}}"><input type="radio" name="color" value="{{.}}"{{if eq . $g.Color}} checked class="cur"{{end}} required aria-label="{{.}} for the group {{$g.Name}}"><span></span></label>{{end}}
          <button class="btn small" type="submit">Set colour</button>
        </form>
        <details class="tmact tmfirst"><summary>Rename</summary>
          <form method="post" action="/account/tags/groups/{{.ID}}">
            <input type="hidden" name="csrf" value="{{$.CSRF}}">
            <input type="text" name="name" value="{{.Name}}" maxlength="30" required aria-label="New name for the group {{.Name}}">
            <button class="btn small primary" type="submit">Save</button>
          </form>
        </details>
        <form class="inline" method="post" action="/account/tags/groups/{{.ID}}/delete">
          <input type="hidden" name="csrf" value="{{$.CSRF}}">
          <button class="linkbtn danger" type="submit" title="Asks first; its tags stay, without a group">Delete</button>
        </form>
        {{else}}
        <span class="tmglbl none">{{.Name}}</span>
        <span class="tmuse">{{len .Tags}} tag{{if ne (len .Tags) 1}}s{{end}}</span>
        {{end}}
      </div>
      {{range .Tags}}{{$t := .}}
      <div class="tmtag">
        <span class="tmname"><span class="tchip tag-{{.Color}}">{{.Name}}</span></span>
        <span class="tmuse">{{.Sites}} site{{if ne .Sites 1}}s{{end}}</span>
        <div class="tminh{{if .Own}} own{{end}}">{{if .Own}}own colour{{if .Group}} · <form class="inline" method="post" action="/account/tags/tag"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="tag" value="{{.Key}}"><input type="hidden" name="op" value="color"><input type="hidden" name="value" value=""><button class="linkbtn" type="submit">back to group</button></form>{{end}}{{else if .Group}}group colour{{else}}no colour{{end}}</div>
        <details class="tmact tmfirst"><summary>Colour</summary>
          <form method="post" action="/account/tags/tag">
            <input type="hidden" name="csrf" value="{{$.CSRF}}">
            <input type="hidden" name="tag" value="{{.Key}}">
            <input type="hidden" name="op" value="color">
            <span class="pal">{{range $.Colors}}<label class="tag-{{.}}" title="{{.}}"><input type="radio" name="value" value="{{.}}"{{if eq . $t.Own}} checked{{end}} required aria-label="{{.}} for {{$t.Name}}"><span></span></label>{{end}}</span>
            <button class="btn small primary" type="submit">Set colour</button>
          </form>
        </details>
        <details class="tmact"><summary>Group</summary>
          <form method="post" action="/account/tags/tag">
            <input type="hidden" name="csrf" value="{{$.CSRF}}">
            <input type="hidden" name="tag" value="{{.Key}}">
            <input type="hidden" name="op" value="group">
            <select name="value" aria-label="Group for {{.Name}}"><option value="">No group</option>{{range $.GroupOpts}}<option value="{{.ID}}"{{if eq .ID $t.Group}} selected{{end}}>{{.Name}}</option>{{end}}</select>
            <button class="btn small primary" type="submit">Move</button>
          </form>
        </details>
        <details class="tmact"><summary>Rename</summary>
          <form method="post" action="/account/tags/tag">
            <input type="hidden" name="csrf" value="{{$.CSRF}}">
            <input type="hidden" name="tag" value="{{.Key}}">
            <input type="hidden" name="op" value="rename">
            <input type="text" name="value" value="{{.Name}}" maxlength="30" required aria-label="New name for {{.Name}}">
            <button class="btn small primary" type="submit">Rename</button>
          </form>
          <p class="tmhint">Every site that carries it is renamed too. The name of another tag merges the two, after a confirmation.</p>
        </details>
        <form class="inline" method="post" action="/account/tags/tag">
          <input type="hidden" name="csrf" value="{{$.CSRF}}">
          <input type="hidden" name="tag" value="{{.Key}}">
          <input type="hidden" name="op" value="delete">
          <button class="linkbtn danger" type="submit" title="Asks first">Delete</button>
        </form>
      </div>
      {{else}}<p class="tmempty">No tags in this group yet.</p>
      {{end}}
    </section>
    {{end}}
    {{if .CanAddGroup}}
    <details class="tmnew"{{if .Empty}} open{{end}}><summary>+ New group</summary>
      <form method="post" action="/account/tags/groups">
        <input type="hidden" name="csrf" value="{{.CSRF}}">
        <input type="text" name="name" maxlength="30" required placeholder="e.g. Client" aria-label="Name of the new group">
        <span class="pal">{{range .Colors}}<label class="tag-{{.}}" title="{{.}}"><input type="radio" name="color" value="{{.}}"{{if eq . $.NewColor}} checked{{end}} required aria-label="{{.}} for the new group"><span></span></label>{{end}}</span>
        <button class="btn small primary" type="submit">Create group</button>
      </form>
    </details>
    {{else}}<p class="muted tmnew">An account has at most {{.MaxGroups}} tag groups.</p>{{end}}
  </div>
</main>
` + pageFoot))

// tagStepTmpl is a confirmation — a tag's rename or delete, a group's
// delete — with the form that confirms it, or the result of a change that
// could not change every site, with those sites. Rendering it changes
// nothing.
var tagStepTmpl = template.Must(template.New("tagstep").Parse(pageHead + tagsManageCSS + `
<main class="acct"><div class="authwrap"><div class="card{{if .Danger}} tmdanger{{end}}">
  <h1>{{.Title}}</h1>
  {{range .Lines}}<p class="muted">{{.}}</p>{{end}}
  {{if .Skipped}}<ul class="tmskipped">{{range .Skipped}}<li><strong>{{.Label}}</strong>{{if .URL}} · <a class="plain" href="{{.URL}}" target="_blank" rel="noopener">{{.URL}}</a>{{end}} · {{if .Locked}}locked by the operator{{else}}could not be saved{{end}}</li>{{end}}</ul>{{end}}
  {{if .Action}}
  <form class="tmconfirm" method="post" action="{{.Action}}">
    <input type="hidden" name="csrf" value="{{.CSRF}}">
    {{if .Tag}}<input type="hidden" name="tag" value="{{.Tag}}">{{end}}
    {{if .Op}}<input type="hidden" name="op" value="{{.Op}}">{{end}}
    {{if .Value}}<input type="hidden" name="value" value="{{.Value}}">{{end}}
    <input type="hidden" name="confirm" value="1">
    <button class="btn {{if .Danger}}danger{{else}}primary{{end}}" type="submit">{{.Button}}</button>
    <a class="btn" href="/account/tags">Cancel</a>
  </form>
  {{else}}
  <div class="tmconfirm"><a class="btn" href="/account/tags">Back to your tags</a></div>
  {{end}}
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
  /* the abuse guard's hits: what matched, where, and the text around it */
  .adm .row .hits { display: block; margin-top: 4px; }
  .adm .row .hit { display: block; font: 11px var(--mono); color: var(--amber); overflow-wrap: anywhere; }
  .adm .row .hits form.inline { display: inline-block; margin-top: 4px; }

  .adm .tabs { display: flex; gap: 6px; margin-bottom: 18px; }
  .adm .tabs a { font: 600 12px var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-dim); text-decoration: none; padding: 7px 12px; border: 1px dashed var(--line); border-radius: 8px; }
  .adm .tabs a.on { color: var(--ink); border-color: rgba(245,184,77,.55); border-style: solid; }

  /* the Reports tab */
  .adm .rep { border-top: 1px solid var(--line-soft); padding: 12px 16px; font-size: 13px; }
  .adm .rep:first-of-type { border-top: 0; }
  .adm .rep .rephead { display: flex; gap: 12px; flex-wrap: wrap; align-items: baseline; }
  .adm .rep .when { font: 12px var(--mono); color: var(--ink-faint); }
  .adm .rep .why { font-weight: 650; color: var(--ink); }
  .adm .rep .via { font: 11px var(--mono); color: var(--ink-faint); margin-left: auto; }
  .adm .rep .tgt { font: 12px var(--mono); color: var(--amber); overflow-wrap: anywhere; margin-top: 3px; }
  .adm .rep .det { white-space: pre-wrap; color: var(--ink-dim); margin-top: 4px; overflow-wrap: anywhere; }
  .adm .rep .con, .adm .rep .site { margin-top: 4px; color: var(--ink-dim); font-size: 12px; overflow-wrap: anywhere; }
  .adm .rep .site .lock { display: inline; margin-left: 6px; font: 11px var(--mono); color: var(--danger); }
  .adm .rep .site .lock b { padding: 0 6px; margin-right: 6px; border: 1px solid var(--danger); border-radius: 4px; }
  .adm .rep .site form.inline { display: inline-flex; gap: 6px; margin-left: 8px; align-items: center; }
  .adm .rep .site input[type=text] { background: var(--bg-raise); color: var(--ink); border: 1px solid var(--line); border-radius: 7px; padding: 4px 8px; font: 12px var(--body); width: 260px; }

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
  .adm .rowhead, .adm .row { display: grid; grid-template-columns: minmax(184px,1.8fr) minmax(126px,1.05fr) 62px 78px 100px 90px minmax(92px,.75fr) 286px; gap: 12px; align-items: center; padding: 10px 16px; }
  .adm .rowhead { font: 600 10px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-faint); background: var(--bg-raise); border-bottom: 1px solid var(--line); }
  .adm .row { border-top: 1px solid var(--line-soft); font-size: 13px; }
  .adm .row:first-of-type { border-top: 0; }
  .adm .row:hover { background: rgba(91,140,255,.045); }
  .adm .row .id { font: 12px var(--mono); word-break: break-all; }
  .adm .row .id a { color: var(--ink); }
  .adm .row .nm { display: block; font: 600 13px var(--body); color: var(--ink); margin-bottom: 2px; overflow-wrap: anywhere; }
  .adm .row .dom { display: block; font: 11px var(--mono); color: var(--amber); margin-top: 3px; word-break: break-all; }
  /* The operator's hold: a stamped tag, so a locked row reads as handled
     evidence rather than as a live site. */
  .adm .row .lock { display: block; margin-top: 4px; font: 11px var(--mono); color: var(--danger); overflow-wrap: anywhere; }
  .adm .row .lock b { display: inline-block; padding: 0 6px; margin-right: 6px; border: 1px solid var(--danger); border-radius: 4px; font-weight: 700; letter-spacing: .08em; }
  .adm .row.locked { background: rgba(242,109,109,.035); }
  /* The lock retention: when the sweep purges the site, or the evidence
     hold that keeps it while a case is open. */
  .adm .row .ret { display: block; margin-top: 2px; font: 11px var(--mono); color: var(--ink-faint); overflow-wrap: anywhere; }
  .adm .row .ret.held { color: var(--amber); }
  .adm .row .ret.held::before { content: "⚖ "; }
  .adm .row .susp { display: inline-block; margin-left: 6px; padding: 0 5px; border: 1px solid var(--danger); border-radius: 4px; font: 700 10px var(--mono); letter-spacing: .08em; color: var(--danger); text-transform: uppercase; }
  .adm .row.confirm input[type=text] {
    background: var(--bg-raise); color: var(--ink); border: 1px solid var(--line);
    border-radius: 7px; padding: 5px 8px; font: 12px var(--body); width: 220px;
  }
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
  /* Provenance: "mcp" means an agent created it. Only ever a label — nothing
     in Sitebin gates on it — but an operator investigating abuse needs to be
     able to see and filter it. */
  .adm .orig { text-transform: uppercase; letter-spacing: .04em; font-size: 11px; color: var(--muted); }
  .adm .row.confirm { grid-template-columns: minmax(200px,1.1fr) 1fr auto; background: rgba(242,109,109,.07); box-shadow: inset 3px 0 0 var(--danger); }
  .adm .row.confirm .warnmsg { font-size: 12px; color: var(--ink-dim); }
  .adm .empty { padding: 40px 16px; text-align: center; color: var(--ink-faint); font: 13px var(--mono); }
  /* Provenance: where the site came from and its latest write. */
  .adm .row .prov { display: block; margin-top: 3px; font: 11px var(--mono); color: var(--ink-faint); overflow-wrap: break-word; }
  .adm .row .prov a { color: var(--ink-dim); }
  /* The address search: accounts seen from the address, above the sites. */
  .adm .ipseen { border: 1px dashed rgba(245,184,77,.45); border-radius: var(--radius); padding: 14px 16px; margin-bottom: 16px; background: linear-gradient(160deg, #151e33, #101727); }
  .adm .ipseen h2 { font: 650 15px var(--display); margin-bottom: 8px; }
  .adm .ipseen .seen { padding: 8px 0; border-top: 1px dashed var(--line-soft); font: 12px var(--mono); color: var(--ink-dim); }
  .adm .ipseen .seen:first-of-type { border-top: 0; }
  .adm .ipseen .seen a.who { color: var(--ink); font-weight: 600; }
  .adm .ipseen .seen .ev { display: block; margin-top: 2px; overflow-wrap: anywhere; }
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
  <nav class="tabs"><a class="on" href="/account/admin">Sites</a><a href="/account/admin/reports">Reports{{if .Figures.Reports}} ({{.Figures.Reports}}){{end}}</a>{{if .Containers}}<a href="/account/admin/containers">Containers</a>{{end}}</nav>

  {{if eq .Flash "deleted"}}<p class="flash">Site deleted.</p>{{end}}
  {{if eq .Flash "expiry"}}<p class="flash">Expiry updated.</p>{{end}}
  {{if eq .Flash "locked"}}<p class="flash">Site locked: it is served to nobody, frozen for its owner, and kept past its expiry{{if .RetentionDays}} — for {{.RetentionDays}} days from the lock, unless you place an evidence hold{{end}}.</p>{{end}}
  {{if eq .Flash "unlocked"}}<p class="flash">Site unlocked: it is served again, and its expiry applies again.</p>{{end}}
  {{if eq .Flash "reviewed"}}<p class="flash">Findings dismissed: the same content is not flagged or held again.</p>{{end}}
  {{if eq .Flash "held"}}<p class="flash">Evidence hold placed: the site is kept past the lock retention until you release the hold.</p>{{end}}
  {{if eq .Flash "unheld"}}<p class="flash">Evidence hold released: the lock retention applies again.</p>{{end}}

  <section class="figures">
    <div class="fig"><span class="k">Sites</span><span class="v">{{.Figures.Sites}}</span></div>
    <div class="fig"><span class="k">Account-owned</span><span class="v">{{.Figures.Owned}}</span></div>
    <div class="fig"><span class="k">Anonymous</span><span class="v">{{.Figures.Anonymous}}</span></div>
    <div class="fig"><span class="k">Stored</span><span class="v">{{.Figures.HumanBytes}}</span></div>
    <div class="fig"><span class="k">Files</span><span class="v">{{.Figures.Files}}</span></div>
    <div class="fig{{if .Figures.ExpiringSoon}} warn{{end}}"><span class="k">Due in 7 days</span><span class="v">{{.Figures.ExpiringSoon}}</span></div>
    <div class="fig{{if .Figures.Flagged}} alarm{{end}}"><span class="k">CSP-blocked</span><span class="v">{{.Figures.Flagged}}</span></div>
    <div class="fig{{if .Figures.Scanner}} alarm{{end}}"><span class="k">Scanner hits</span><span class="v">{{.Figures.Scanner}}</span></div>
    <div class="fig"><span class="k">Locked</span><span class="v">{{.Figures.Locked}}</span></div>
  </section>

  <form class="bar" method="get" action="/account/admin">
    <input type="search" name="q" value="{{.Query}}" placeholder="name, view id, owner email, domain — or an IP / CIDR" aria-label="Search sites">
    <select name="filter" aria-label="Filter sites">
      <option value=""{{if eq .Filter ""}} selected{{end}}>All sites</option>
      <option value="owned"{{if eq .Filter "owned"}} selected{{end}}>Account-owned</option>
      <option value="anon"{{if eq .Filter "anon"}} selected{{end}}>Anonymous</option>
      <option value="expiring"{{if eq .Filter "expiring"}} selected{{end}}>Expiring within 7 days</option>
      <option value="mcp"{{if eq .Filter "mcp"}} selected{{end}}>Created by an agent (MCP)</option>
      <option value="flagged"{{if eq .Filter "flagged"}} selected{{end}}>Flagged (scanner or CSP)</option>
      <option value="locked"{{if eq .Filter "locked"}} selected{{end}}>Locked</option>
    </select>
    <button class="btn small" type="submit">Apply</button>
    <span class="count">{{.Shown}} shown</span>
  </form>

  {{with .IP}}
  <section class="ipseen">
    <h2>Seen from {{.Query}}: {{.Sites}} site(s), {{len .Accounts}} account(s)</h2>
    {{range .Accounts}}<div class="seen"><a class="who" href="/account/admin/accounts/{{.ID}}">{{.Email}}</a>{{if .Suspended}}<span class="susp">Suspended</span>{{end}}
      {{range .Rows}}<span class="ev">{{.When}} · {{.Action}}{{if .Site}} {{.Site}}{{end}}{{if .What}} · {{.What}}{{end}} · {{.IP}}{{if .UA}} · {{.UA}}{{end}}</span>{{end}}
    </div>{{else}}<p class="lede">No account was seen from this address.</p>{{end}}
  </section>
  {{end}}

  <section class="reg">
    <div class="rowhead">
      <span>Site</span><span>Owner</span><span>Origin</span><span>Mode</span><span>Size</span><span>Created</span><span>Expiry</span><span style="text-align:right">Actions</span>
    </div>
    {{range .Rows}}
    {{if .Locking}}
    <div class="row confirm">
      <span class="id">{{if .Name}}<span class="nm">{{.Name}}</span>{{end}}{{.ViewID}}{{if .DomainsText}}<span class="dom">{{.DomainsText}}</span>{{end}}</span>
      <span class="warnmsg">{{if .Locked}}Keep this site locked as your own lock? A lock placed by a suspension is lifted when the owner is unsuspended; yours stays until you unlock it. It keeps its date, and the lock retention with it: {{.RetentionText}}.{{else}}Lock this site? It stops being served at once, its owner can no longer change, download or delete it, and it is kept past its expiry as evidence — {{if $.RetentionDays}}the cleanup sweep purges it {{$.RetentionDays}} days after the lock unless you place an evidence hold while a case is open{{else}}nothing is deleted until you unlock or delete it{{end}}.{{end}}</span>
      <span class="acts">
        <form method="post" action="/account/admin/sites/{{.ViewID}}/lock{{if $.Params}}?{{$.ParamsQ}}{{end}}" class="inline">
          <input type="hidden" name="csrf" value="{{$.CSRF}}">
          <input type="text" name="reason" maxlength="200" value="{{if .Locked}}{{.Locked.Reason}}{{end}}" placeholder="Reason — shown to the owner" aria-label="Reason for locking {{.ViewID}}">
          <button class="btn small danger" type="submit">Yes, lock {{.ViewID}}</button>
        </form>
        <a class="btn small" href="/account/admin?{{$.ParamsQ}}">Cancel</a>
      </span>
    </div>
    {{else if .Unlocking}}
    <div class="row confirm">
      <span class="id">{{if .Name}}<span class="nm">{{.Name}}</span>{{end}}{{.ViewID}}{{if .DomainsText}}<span class="dom">{{.DomainsText}}</span>{{end}}</span>
      <span class="warnmsg">Unlock this site? It is served again at once, its owner can change it again, and its expiry{{if .ExpiryValue}} ({{.ExpiryValue}}){{end}} applies again — a date already past means the next sweep deletes it.{{if .Held}} Its evidence hold ends with the lock.{{end}}{{if .FindingLines}} Its scanner findings become reviewed: the same content is not held again.{{end}}</span>
      <span class="acts">
        <form method="post" action="/account/admin/sites/{{.ViewID}}/unlock{{if $.Params}}?{{$.ParamsQ}}{{end}}" class="inline">
          <input type="hidden" name="csrf" value="{{$.CSRF}}">
          <button class="btn small" type="submit">Yes, unlock {{.ViewID}}</button>
        </form>
        <a class="btn small" href="/account/admin?{{$.ParamsQ}}">Cancel</a>
      </span>
    </div>
    {{else if .Holding}}
    <div class="row confirm">
      <span class="id">{{if .Name}}<span class="nm">{{.Name}}</span>{{end}}{{.ViewID}}{{if .DomainsText}}<span class="dom">{{.DomainsText}}</span>{{end}}</span>
      <span class="warnmsg">Place an evidence hold on this site? Do it while a case, investigation or proceeding about it is still open: the site and its records are kept past the {{$.RetentionDays}}-day lock retention{{if .PurgeDate}} (purge due {{.PurgeDate}}){{end}}, and its owner's provenance with them, until you release the hold. An unsuspension does not lift a held lock — it becomes your own. Unlocking the site ends the hold too.</span>
      <span class="acts">
        <form method="post" action="/account/admin/sites/{{.ViewID}}/hold{{if $.Params}}?{{$.ParamsQ}}{{end}}" class="inline">
          <input type="hidden" name="csrf" value="{{$.CSRF}}">
          <button class="btn small" type="submit">Yes, hold {{.ViewID}}</button>
        </form>
        <a class="btn small" href="/account/admin?{{$.ParamsQ}}">Cancel</a>
      </span>
    </div>
    {{else if .Unholding}}
    <div class="row confirm">
      <span class="id">{{if .Name}}<span class="nm">{{.Name}}</span>{{end}}{{.ViewID}}{{if .DomainsText}}<span class="dom">{{.DomainsText}}</span>{{end}}</span>
      <span class="warnmsg">Release the evidence hold? Do it once the case is closed: the lock retention applies again{{if .PurgeDate}}{{if .PurgePast}}, and the lock is older than it ({{.PurgeDate}}) — the next sweep purges the site, its files and its records{{else}} and the site is purged on {{.PurgeDate}}{{end}}{{end}}. It stays locked either way.</span>
      <span class="acts">
        <form method="post" action="/account/admin/sites/{{.ViewID}}/unhold{{if $.Params}}?{{$.ParamsQ}}{{end}}" class="inline">
          <input type="hidden" name="csrf" value="{{$.CSRF}}">
          <button class="btn small danger" type="submit">Yes, release {{.ViewID}}</button>
        </form>
        <a class="btn small" href="/account/admin?{{$.ParamsQ}}">Cancel</a>
      </span>
    </div>
    {{else if .Confirming}}
    <div class="row confirm">
      <span class="id">{{if .Name}}<span class="nm">{{.Name}}</span>{{end}}{{.ViewID}}{{if .DomainsText}}<span class="dom">{{.DomainsText}}</span>{{end}}</span>
      <span class="warnmsg">{{if .Locked}}This site is LOCKED — held as evidence. {{end}}Delete this site permanently? Its {{.Files}} file(s) and any custom domain go with it. This cannot be undone.</span>
      <span class="acts">
        <form method="post" action="/account/admin/sites/{{.ViewID}}/delete{{if $.Params}}?{{$.ParamsQ}}{{end}}" class="inline">
          <input type="hidden" name="csrf" value="{{$.CSRF}}">
          <button class="btn small danger" type="submit">Yes, delete {{.ViewID}}</button>
        </form>
        <a class="btn small" href="/account/admin?{{$.ParamsQ}}">Cancel</a>
      </span>
    </div>
    {{else}}
    <div class="row{{if .Locked}} locked{{end}}">
      <span class="id">{{if .Name}}<span class="nm">{{.Name}}</span>{{end}}<a href="{{.ViewURL}}" rel="noreferrer noopener" target="_blank">{{.ViewID}}</a>{{if .DomainsText}}<span class="dom">{{.DomainsText}}</span>{{end}}{{if .LockText}}<span class="lock"><b>LOCKED</b>{{.LockText}}</span><span class="ret{{if .Held}} held{{end}}">{{.RetentionText}}</span>{{end}}{{if .FindingLines}}<span class="hits">{{range .FindingLines}}<span class="hit">&#9873; {{.}}</span>{{end}}{{if .MoreFindings}}<span class="hit">… and {{.MoreFindings}} more</span>{{end}}<form method="post" action="/account/admin/sites/{{.ViewID}}/review{{if $.Params}}?{{$.ParamsQ}}{{end}}" class="inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button class="btn small" type="submit" title="You have looked: the same content is not flagged or held again">Dismiss</button></form></span>{{end}}{{with index $.Prov .ViewID}}<span class="prov">{{if .CreatedIP}}from <a href="/account/admin?q={{.CreatedIP}}" title="{{.CreatedUA}}">{{.CreatedIP}}</a> · {{.CreatedText}}{{end}}{{if .LastText}}{{if .CreatedIP}}<br>{{end}}last {{.LastText}}{{if .LastIP}} · <a href="/account/admin?q={{.LastIP}}" title="{{.LastUA}}">{{.LastIP}}</a>{{end}}{{end}} · <a href="{{.Trail}}">trail &rarr;</a></span>{{end}}</span>
      <span class="own{{if not .Owner}} anon{{end}}">{{.OwnerLabel}}{{if .OwnerSuspended}}<span class="susp" title="{{.OwnerSuspended}}">Suspended</span>{{end}}{{if .Violations}}<span class="flag" title="{{.BlockedText}}">&#9888; {{.Violations}} blocked{{if .Reporters}} &middot; {{.Reporters}} source{{if ne .Reporters 1}}s{{end}}{{end}}</span>{{end}}</span>
      <span class="num orig">{{if .Origin}}{{.Origin}}{{else}}&mdash;{{end}}</span>
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
        {{if .Locked}}<a class="btn small" href="/account/admin?unlock={{.ViewID}}{{$.Params}}">Unlock</a>{{if .LockedByMachine}}<a class="btn small" href="/account/admin?lock={{.ViewID}}{{$.Params}}" title="Make it your own lock">Keep</a>{{end}}{{if .Held}}<a class="btn small" href="/account/admin?unhold={{.ViewID}}{{$.Params}}" title="The case is closed: the lock retention applies again">Release hold</a>{{else if $.RetentionDays}}<a class="btn small" href="/account/admin?hold={{.ViewID}}{{$.Params}}" title="A case is open: keep the site past the lock retention">Hold</a>{{end}}{{else}}<a class="btn small danger" href="/account/admin?lock={{.ViewID}}{{$.Params}}">Lock</a>{{end}}
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

var reportsTmpl = template.Must(template.New("reports").Parse(pageHead + adminConsoleCSS + `
<main class="adm">
  <h1>Abuse reports</h1>
  <p class="lede">Filed through the report page and POST /api/report, newest first, and kept for 14 days. Signed in as {{.Email}} · <a href="/account">back to your account</a></p>
  <nav class="tabs"><a href="/account/admin">Sites</a><a class="on" href="/account/admin/reports">Reports ({{.Total}})</a>{{if .Containers}}<a href="/account/admin/containers">Containers</a>{{end}}</nav>
  {{if eq .Flash "locked"}}<p class="flash">Site locked: it is served to nobody, frozen for its owner, and kept past its expiry.</p>{{end}}
  <section class="reg">
    {{range .Rows}}
    <div class="rep">
      <div class="rephead"><span class="when">{{.TimeText}}</span><span class="why">{{.Reason}}</span><span class="via">{{.Via}}{{if .Source}} · {{.Source}}{{end}}</span></div>
      <div class="tgt">{{.Target}}</div>
      {{if .Details}}<div class="det">{{.Details}}</div>{{end}}
      {{if .Contact}}<div class="con">contact: <a href="mailto:{{.Contact}}">{{.Contact}}</a></div>{{end}}
      <div class="site">{{if .Site}}site <a href="/account/admin?q={{.Site.ViewID}}">{{.Site.ViewID}}</a> · {{.OwnerLabel}}{{if .LockText}}<span class="lock"><b>LOCKED</b>{{.LockText}}</span>{{else}}<form method="post" action="/account/admin/sites/{{.Site.ViewID}}/lock?return=reports" class="inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="text" name="reason" maxlength="200" value="{{.LockReason}}" aria-label="Reason for locking {{.Site.ViewID}}, shown to its owner" title="Shown to the owner"><button class="btn small danger" type="submit">Lock site</button></form>{{end}}{{else if .ViewID}}site {{.ViewID}} no longer exists{{else}}not resolved to a site on this instance{{end}}</div>
    </div>
    {{else}}
    <p class="empty">No reports on file.</p>
    {{end}}
  </section>
</main>
` + pageFoot))
