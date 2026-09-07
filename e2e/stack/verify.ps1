# Sitebin Enterprise on the SaaS Stack -- what the compose container in this
# directory is supposed to be, checked against the running stack.
#
#   powershell -File e2e\stack\verify.ps1
#
# Reads .env beside this file (the admin key, the GDPR secret, the port, the
# app id, the client id, the declared consents) and asserts, against the
# container `docker compose up` started here and the stack it is wired to:
#
#   the registration  GET /api/v1/apps/<app> holds the consents list with
#                     every declared document, the gdpr block with both URLs
#                     on THIS container's base URL, the licensing block, the
#                     tier catalogue with amounts on the paid tiers, the
#                     redirect URI and the MCP resource -- all on one port
#   sign-in           a throwaway user is stopped at the gate, accepts, and
#                     lands in /account
#   self-service      the dashboard links the stack's account console
#                     (accountUrl: <issuer>/account/?referrer=<client id>),
#                     sends deletion there, and the portal route answers a
#                     handoff page that refreshes to the stack's hosted plan
#                     page (planUrl: <issuer origin>/apps/<app>/plan) -- not
#                     a 303, which the dashboard's form-action would block
#   GDPR export       an order signed the way platform-api/src/routes/gdpr.ts
#                     signs it (sha256=HMAC(secret, "<ts>.<body>")) returns
#                     the account, the site the user just made, its tokens
#   GDPR refusals     a wrong secret and a stale timestamp are 401 and change
#                     nothing
#   GDPR delete       the same signature deletes the account: the site is
#                     gone, the session is dead, the export is empty, and a
#                     second order is still a 200
#
# The stack itself cannot place the GDPR calls on this testbed -- its SSRF
# guard refuses a webhook URL that resolves to a private address, and every
# *.localtest.me name does -- so they are signed here, exactly as it would.
#
# WHAT IT LEAVES BEHIND: nothing of the user. The throwaway identity is
# removed through the gateway at the end; its consent records survive, as the
# stack's contract says they must.
param(
    [string]$EnvFile = (Join-Path $PSScriptRoot ".env"),
    [string]$StackDomain = "saas.localtest.me:8080",
    [string]$Realm = "saas-stack",
    [switch]$KeepUser
)

$ErrorActionPreference = "Continue"
$work = Join-Path (Split-Path -Parent $PSScriptRoot) ".work"
New-Item -ItemType Directory -Force $work | Out-Null
Get-ChildItem $work -Filter "verify*.*" -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue

# ---------- .env ----------

if (-not (Test-Path $EnvFile)) { Write-Host "no $EnvFile -- copy .env.example and fill it in" -ForegroundColor Red; exit 1 }
$cfg = @{}
foreach ($line in [IO.File]::ReadAllLines($EnvFile)) {
    if ($line -match '^\s*#' -or $line -notmatch '=') { continue }
    $i = $line.IndexOf('=')
    $cfg[$line.Substring(0, $i).Trim()] = $line.Substring($i + 1).Trim()
}
function Setting([string]$k, [string]$default = "") {
    if ($cfg.ContainsKey($k) -and -not [string]::IsNullOrWhiteSpace($cfg[$k])) { return $cfg[$k] }
    if ($default -ne "") { return $default }
    Write-Host "$k is not set in $EnvFile" -ForegroundColor Red; exit 1
}
$AdminKey = Setting "SITEBIN_STACK_ADMIN_KEY"
$GdprSecret = Setting "SITEBIN_STACK_GDPR_SECRET"
$AppId = Setting "SITEBIN_STACK_APP_ID" "sitebin"
$ClientId = Setting "SITEBIN_OAUTH_OIDC_CLIENT_ID" "$AppId-app"
$BaseDomain = Setting "SITEBIN_BASE_DOMAIN" "sitebin.localtest.me:8091"
# Assigned first, then wrapped: PS 5.1's ConvertFrom-Json emits a JSON array
# as ONE pipeline object, so @(ConvertFrom-Json ...) is a one-element array
# holding the list, whatever the list holds. @() around a variable that
# already is an array returns it unchanged.
$declared = @()
try { $parsedConsents = ConvertFrom-Json -InputObject (Setting "SITEBIN_STACK_CONSENTS" "[]"); $declared = @($parsedConsents) } catch {}

$origin = "http://$BaseDomain"
$authGw = "http://auth-gw.saas-stack.$StackDomain"
$platform = "http://platform.saas-stack.$StackDomain"
$issuer = "http://auth.$StackDomain/realms/$Realm"
$issuerOrigin = "http://auth.$StackDomain"
$wantAccountURL = "$issuer/account/?referrer=$ClientId"
$wantPlanURL = "$issuerOrigin/apps/$AppId/plan"

$ExpectedAssertions = 40

$script:pass = 0; $script:fail = 0
function Assert([string]$n, $c, [string]$d = "") {
    $ok = $false
    if ($null -ne $c) {
        if ($c -is [bool]) { $ok = $c }
        elseif ($c -is [array]) { $ok = ($c.Count -gt 0) }
        else { $ok = [bool]$c }
    }
    if ($ok) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}
function Fatal([string]$m) {
    Write-Host "  ABORT $m" -ForegroundColor Red
    $script:fail++
    Cleanup
    Write-Host ("== stack verify: {0} passed, {1} failed (aborted)" -f $script:pass, $script:fail) -ForegroundColor Red
    exit 1
}

# ---------- plumbing ----------

$script:seq = 0
function TempFile([string]$ext) { $script:seq++; return (Join-Path $work "verify$($script:seq).$ext") }

function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = TempFile "out"; $hdrFile = TempFile "hdr"
    $a = @("-s", "-X", $method, "-o", $bodyFile, "-D", $hdrFile, "-w", "%{http_code}", "--max-time", "30") + $extra + @($url)
    $code = & curl.exe @a
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    $headers = ""; if (Test-Path $hdrFile) { $headers = [IO.File]::ReadAllText($hdrFile) }
    return @{ code = [int]$code; body = $body; headers = $headers }
}
function JsonBody([string]$json) {
    $p = TempFile "json"
    [IO.File]::WriteAllText($p, $json, (New-Object System.Text.UTF8Encoding($false)))
    return "@$p"
}
function Unescape([string]$s) { return $s.Replace("&amp;", "&") }
function Match1([string]$html, [string]$re) {
    $m = [regex]::Match($html, $re)
    if ($m.Success) { return Unescape($m.Groups[1].Value) }
    return ""
}
function MatchAll([string]$html, [string]$re) {
    $out = @()
    foreach ($m in [regex]::Matches($html, $re)) { $out += Unescape($m.Groups[1].Value) }
    return $out
}

$jar = Join-Path $work "verify-jar.txt"
Remove-Item -Force $jar -ErrorAction SilentlyContinue
function Browse([string]$url, [string[]]$form = @()) {
    $page = TempFile "html"
    $a = @("-s", "-L", "-c", $jar, "-b", $jar, "-o", $page, "-w", "%{url_effective}", "--max-time", "60")
    foreach ($f in $form) { $a += @("--data-urlencode", $f) }
    $a += @($url)
    $eff = & curl.exe @a
    $html = ""; if (Test-Path $page) { $html = [IO.File]::ReadAllText($page) }
    return @{ url = "$eff"; html = $html }
}

# Sign exactly as the stack does: sha256= + hex(HMAC-SHA256(secret,
# "<timestamp>.<body>")). The body bytes on the wire are the bytes signed, so
# the same file is both signed and sent.
function Sign([string]$secret, [string]$ts, [string]$body) {
    $h = New-Object System.Security.Cryptography.HMACSHA256
    $h.Key = [Text.Encoding]::UTF8.GetBytes($secret)
    $mac = $h.ComputeHash([Text.Encoding]::UTF8.GetBytes("$ts.$body"))
    return "sha256=" + (($mac | ForEach-Object { $_.ToString("x2") }) -join "")
}
function GdprOrder([string]$path, [string]$secret, [int64]$ts, [string]$body) {
    $sig = Sign $secret "$ts" $body
    return (Req "POST" "$origin$path" @("-H", "Content-Type: application/json", "-H", "X-Timestamp: $ts", "-H", "X-Signature: $sig", "--data-binary", (JsonBody $body)))
}
function NowUnix { return [int64]([DateTimeOffset]::UtcNow.ToUnixTimeSeconds()) }

$script:userId = ""
function Cleanup {
    if ($KeepUser) { return }
    if (-not [string]::IsNullOrWhiteSpace($script:userId)) {
        Req "DELETE" "$authGw/api/v1/$AppId/users/$($script:userId)" @("-H", "Authorization: Bearer $AdminKey") | Out-Null
    }
}

# ---------- the container ----------

Write-Host "== the container on $origin" -ForegroundColor Cyan
$r = Req "GET" "$origin/"
if ($r.code -ne 200) { Fatal "nothing answers on $origin ($($r.code)); docker compose up -d --build in e2e/stack first" }
Assert "sitebin answers on $origin" $true

# ---------- the registration the stack holds ----------

Write-Host "== the registration the stack holds for $AppId" -ForegroundColor Cyan
$r = Req "GET" "$platform/api/v1/apps/$AppId" @("-H", "Authorization: Bearer $AdminKey")
if ($r.code -ne 200) { Fatal "GET /api/v1/apps/$AppId said $($r.code): $($r.body)" }
$app = $null; try { $app = $r.body | ConvertFrom-Json } catch {}
if ($null -eq $app) { Fatal "the app record did not parse" }
$c = $app.config

$stored = @(); if ($null -ne $c.consents) { $stored = @($c.consents) }
Assert "consents holds every declared document ($($declared.Count))" ($declared.Count -gt 0 -and $stored.Count -eq $declared.Count) "got $($stored.Count), declared $($declared.Count)"
$sameOrder = ($stored.Count -eq $declared.Count)
for ($i = 0; $i -lt [Math]::Min($stored.Count, $declared.Count); $i++) {
    if ($stored[$i].key -ne $declared[$i].key -or $stored[$i].version -ne $declared[$i].version -or $stored[$i].url -ne $declared[$i].url) { $sameOrder = $false }
}
Assert "in the declared order, at the declared versions and URLs" $sameOrder "stored $(($stored | ForEach-Object { $_.key + '@' + $_.version }) -join ',')"
Assert "and no terms shorthand beside them" ($null -eq $c.terms) "got $($c.terms)"

Assert "gdpr.deleteUserUrl is this container's" ($null -ne $c.gdpr -and $c.gdpr.deleteUserUrl -eq "$origin/account/gdpr/delete") "got $($c.gdpr.deleteUserUrl)"
Assert "gdpr.exportUserDataUrl is this container's" ($null -ne $c.gdpr -and $c.gdpr.exportUserDataUrl -eq "$origin/account/gdpr/export") "got $($c.gdpr.exportUserDataUrl)"
Assert "gdpr.webhookSecret is held" ($null -ne $c.gdpr -and -not [string]::IsNullOrWhiteSpace($c.gdpr.webhookSecret)) ""
if ($null -ne $c.gdpr -and $c.gdpr.webhookSecret -eq $GdprSecret) {
    Write-Host "  note: the stack returns gdpr.webhookSecret IN CLEAR from GET /api/v1/apps/<id>; redaction is a stack-side item" -ForegroundColor Yellow
}
Assert "licensing is declared" ($null -ne $c.licensing -and $c.licensing.graceMonths -gt 0 -and $null -ne $c.licensing.plans) "got $($c.licensing)"

$tiers = @(); if ($null -ne $c.billing -and $null -ne $c.billing.tiers) { $tiers = @($c.billing.tiers) }
$paid = @($tiers | Where-Object { -not [string]::IsNullOrWhiteSpace($_.monthlyPrice) })
Assert "billing.tiers comes from tiers.json (4 tiers, 2 with amounts)" ($tiers.Count -eq 4 -and $paid.Count -eq 2) "got $($tiers.Count) tiers, $($paid.Count) priced"
Assert "tierAfterRegistration is the default tier" ($c.billing.tierAfterRegistration -eq (Setting "SITEBIN_DEFAULT_TIER" "free")) "got $($c.billing.tierAfterRegistration)"

$redirects = @(); if ($null -ne $c.auth -and $null -ne $c.auth.redirectUris) { $redirects = @($c.auth.redirectUris) }
Assert "the redirect URI is this container's callback, on the same port" ($redirects -contains "$origin/account/auth/oidc/callback") "got $($redirects -join ', ')"
Assert "and the MCP resource is on it too" ($null -ne $c.mcp -and $c.mcp.resourceUrl -eq "$origin/mcp") "got $($c.mcp.resourceUrl)"

# ---------- sign a throwaway user in through the gate ----------

Write-Host "== a throwaway user signs in through the gate" -ForegroundColor Cyan
$user = "sbverify" + (Get-Random -Minimum 100000 -Maximum 999999)
$email = "$user@e2e.invalid"
$pw = "Verify-E2E-" + [guid]::NewGuid().ToString("N").Substring(0, 12) + "-1!"
$mkUser = '{"email":"' + $email + '","password":"' + $pw + '","firstName":"Verify","lastName":"Throwaway","emailVerified":true}'
$r = Req "POST" "$authGw/api/v1/$AppId/users" @("-H", "Authorization: Bearer $AdminKey", "-H", "Content-Type: application/json", "--data-binary", (JsonBody $mkUser))
$made = $null; try { $made = $r.body | ConvertFrom-Json } catch {}
if ($null -eq $made -or $null -eq $made.data) { Fatal "could not create the throwaway user: $($r.code) $($r.body)" }
$script:userId = "$($made.data.userId)"
Assert "a throwaway user exists" ($script:userId.Length -gt 10)

$p = Browse "$origin/account/auth/oidc"
Assert "sign-in lands on the realm's login page" ($p.url -like "http://auth.$StackDomain/realms/$Realm/*") "got $($p.url)"
$action = Match1 $p.html 'id="kc-form-login"[^>]*action="([^"]+)"'
if ([string]::IsNullOrWhiteSpace($action)) { Fatal "could not find the realm login form" }
$p = Browse $action @("username=$email", "password=$pw", "credentialId=")
Assert "a first sign-in is stopped at the stack's consent page" ($p.url -like "*/api/v1/_consent*") "got $($p.url)"
$accepts = MatchAll $p.html 'name="accept" value="([^"]+)"'
Assert "which offers the platform's document plus every one Sitebin declared" ($accepts.Count -eq 1 + $declared.Count) "got $($accepts -join ', ')"
$gwOrigin = ""
try { $u = [uri]$p.url; $gwOrigin = $u.GetLeftPart([System.UriPartial]::Authority) } catch {}
$flow = Match1 $p.html 'name="flow" value="([^"]+)"'
# The gate binds the flow to the browser that started it: the cookie the
# gateway set at /auth rides in the jar, and the page carries a token the post
# has to return. Without both the gateway answers 400, whoever holds the flow id.
$gateCsrf = Match1 $p.html 'name="csrf" value="([^"]+)"'
$form = @("flow=$flow", "csrf=$gateCsrf", "decision=accept")
foreach ($a in $accepts) { $form += "accept=$a" }
$p = Browse "$gwOrigin/api/v1/_consent" $form
Assert "accepting them lands the user in Sitebin, signed in" ($p.url -eq "$origin/account" -and $p.html -match [regex]::Escape($email)) "got $($p.url)"
$dash = $p.html
$csrf = Match1 $dash 'name="csrf" value="([^"]+)"'
if ([string]::IsNullOrWhiteSpace($csrf)) { Fatal "no csrf token on the dashboard" }

# ---------- self-service on the stack's pages ----------

Write-Host "== self-service links" -ForegroundColor Cyan
Assert "the dashboard links the account console (accountUrl)" ($dash -match [regex]::Escape('href="' + $wantAccountURL + '"') -and $dash -match "Manage account") "want $wantAccountURL"
Assert "the danger zone sends deletion to the console, not to a local form" ($dash -match "Delete account in the account console" -and $dash -notmatch 'action="/account/delete"') ""
Assert "the billing card offers the portal route" ($dash -match 'action="/account/billing/portal"') ""

$r = Req "POST" "$origin/account/billing/portal" @("-b", $jar, "--data-urlencode", "csrf=$csrf")
$loc = Match1 $r.headers 'Location: (\S+)'
# Not a 303: the dashboard's CSP form-action 'self' makes Chrome drop a form
# redirect to another origin, so the route answers a handoff page instead.
Assert "POST /account/billing/portal answers 200 with a handoff page, not a redirect the CSP would block" ($r.code -eq 200 -and $r.headers -notmatch "(?im)^Location:") "got $($r.code)"
Assert "which refreshes to the stack's hosted plan page (planUrl)" ($r.body -match [regex]::Escape('http-equiv="refresh" content="0;url=' + $wantPlanURL + '"')) "want $wantPlanURL"

$r = Req "POST" "$origin/account/delete" @("-b", $jar, "--data-urlencode", "csrf=$csrf")
$loc = Match1 $r.headers 'Location: (\S+)'
Assert "POST /account/delete for a stack user goes to the console instead of deleting" ($r.code -eq 303 -and $loc -eq $wantAccountURL) "got $($r.code) $loc"
$r = Req "GET" "$origin/account" @("-b", $jar)
Assert "and the account is still there" ($r.code -eq 200 -and $r.body -match [regex]::Escape($email)) "got $($r.code)"

# ---------- something to export and delete ----------

Write-Host "== the user makes a site" -ForegroundColor Cyan
$idx = TempFile "html"
[IO.File]::WriteAllText($idx, "<h1>gdpr-verify-site</h1>")
$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "files=@$idx;filename=index.html")
Assert "a signed-in create succeeds (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
$site = $null; if ($r.code -eq 201) { try { $site = $r.body | ConvertFrom-Json } catch {} }
if ($null -eq $site) { Fatal "no site to export and delete" }
$r = Req "GET" "$($site.view_url)/"
Assert "and the site serves" ($r.code -eq 200 -and $r.body -match "gdpr-verify-site") "got $($r.code)"

# ---------- GDPR export ----------

Write-Host "== a stack-signed export" -ForegroundColor Cyan
$order = '{"userId":"' + $script:userId + '","email":"' + $email + '"}'
$r = GdprOrder "/account/gdpr/export" $GdprSecret (NowUnix) $order
Assert "the export answers 200" ($r.code -eq 200) "got $($r.code): $($r.body)"
$exp = $null; try { $exp = $r.body | ConvertFrom-Json } catch {}
Assert "it carries the account, by the stack's user id" ($null -ne $exp -and $null -ne $exp.account -and $exp.account.oauth_subject -eq $script:userId -and $exp.account.email -eq $email) "got $($r.body)"
$expSites = @(); if ($null -ne $exp -and $null -ne $exp.sites) { $expSites = @($exp.sites) }
Assert "and the site the user just made, with its metadata" ($expSites.Count -eq 1 -and $expSites[0].id -eq $site.id -and $expSites[0].view_url -eq $site.view_url) "got $($expSites | ConvertTo-Json -Compress)"
Assert "and the token list and the sessions statement" ($null -ne $exp -and $null -ne $exp.api_tokens -and $null -ne $exp.sessions -and $exp.sessions.stored -eq $false) ""

Write-Host "== refusals" -ForegroundColor Cyan
$r = GdprOrder "/account/gdpr/delete" "not-the-secret-0123456789abcdef0123456789" (NowUnix) $order
Assert "a wrong secret is refused (401)" ($r.code -eq 401) "got $($r.code)"
$r = GdprOrder "/account/gdpr/delete" $GdprSecret ((NowUnix) - 600) $order
Assert "a stale timestamp is refused (401), so a captured order cannot be replayed" ($r.code -eq 401) "got $($r.code)"
$r = Req "POST" "$origin/account/gdpr/delete" @("-H", "Content-Type: application/json", "--data-binary", (JsonBody $order))
Assert "and an unsigned order is refused (401)" ($r.code -eq 401) "got $($r.code)"
$r = Req "GET" "$origin/account" @("-b", $jar)
Assert "none of which deleted anything" ($r.code -eq 200 -and $r.body -match [regex]::Escape($email)) "got $($r.code)"

# ---------- GDPR delete ----------

Write-Host "== a stack-signed deletion" -ForegroundColor Cyan
$r = GdprOrder "/account/gdpr/delete" $GdprSecret (NowUnix) $order
Assert "the deletion answers 200" ($r.code -eq 200) "got $($r.code): $($r.body)"
$del = $null; try { $del = $r.body | ConvertFrom-Json } catch {}
Assert "reporting the account found and one site deleted" ($null -ne $del -and $del.status -eq "deleted" -and $del.found -eq $true -and $del.sites -eq 1) "got $($r.body)"
$r = Req "GET" "$($site.view_url)/"
Assert "the site is gone" ($r.code -ne 200 -or $r.body -notmatch "gdpr-verify-site") "got $($r.code)"
$r = Req "GET" "$origin/account" @("-b", $jar)
Assert "the session is dead" ($r.code -ne 200 -or $r.body -notmatch [regex]::Escape($email)) "got $($r.code)"
$r = GdprOrder "/account/gdpr/export" $GdprSecret (NowUnix) $order
$exp = $null; try { $exp = $r.body | ConvertFrom-Json } catch {}
Assert "an export now holds nothing" ($r.code -eq 200 -and $null -ne $exp -and $null -eq $exp.account -and @($exp.sites).Count -eq 0) "got $($r.body)"
$r = GdprOrder "/account/gdpr/delete" $GdprSecret (NowUnix) $order
$del = $null; try { $del = $r.body | ConvertFrom-Json } catch {}
Assert "and a second deletion is still a 200 (idempotent, not found)" ($r.code -eq 200 -and $null -ne $del -and $del.status -eq "deleted" -and $del.found -eq $false) "got $($r.code): $($r.body)"

# ---------- cleanup ----------

Cleanup

Write-Host ""
$total = $script:pass + $script:fail
if ($total -ne $ExpectedAssertions) {
    Write-Host ("  FAIL assertion count: ran {0}, expected {1} - an assertion was skipped or threw" -f $total, $ExpectedAssertions) -ForegroundColor Red
    $script:fail++
}
Write-Host ("== stack verify: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
