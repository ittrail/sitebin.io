# Sitebin Enterprise -- the SaaS Stack's CONSENT GATE, end to end.
#
#   powershell -File e2e\consent.ps1 -AdminKey <PLATFORM_ADMIN_KEY>
#
# Needs an ALREADY RUNNING IT-Trail SaaS Stack (see e2e/stack/README.md). It
# brings up nothing of the stack and cannot: the stack is a separate product
# with its own repo and its own bootstrap.
#
# WHAT IT PROVES
#
# The stack gates sign-in on two documents -- the IT-Trail PLATFORM terms once
# per user, and the APP's own terms once per app -- on a page the stack hosts
# inside the sign-in flow. An app only gets the gate if its OIDC discovery
# points at the AUTH GATEWAY: the gateway serves the realm's document with
# `authorization_endpoint` changed to itself, and that one field is the whole
# mechanism. Point discovery at the identity provider instead and the gate is
# bypassed SILENTLY -- sign-in works, and nobody is ever asked anything.
#
# So this script asserts, against the live stack and a real container:
#
#   the split       SITEBIN_OAUTH_OIDC_ISSUER stays the realm (what `iss`
#                   says); SITEBIN_OAUTH_OIDC_DISCOVERY_URL is the gateway.
#                   go-oidc refuses a document whose issuer is not the URL it
#                   came from, so without the split this cannot even start.
#   the declaration Sitebin's own registration carries the `terms` block from
#                   SITEBIN_STACK_TERMS, and the stack stores it
#   the gate        a FIRST registration is stopped and shown BOTH documents,
#                   naming the platform's version and Sitebin's declared one
#   signed in       accepting both lands the user in Sitebin's own /account
#   once only       a second sign-in is not asked again
#   the control     with discovery pointed at the identity provider, the
#                   authorization redirect goes there and the gate is gone
#   tightened       a discovery document advertising some OTHER issuer is
#                   still refused; the split loosens nothing that matters
#
# The flow is driven with curl.exe and a cookie jar -- real HTTP, real
# redirects, real Keycloak forms, real gateway forms. Playwright is not a
# Sitebin dependency and adding one for this would be disproportionate.
#
# WHAT IT LEAVES BEHIND: nothing. It creates a THROWAWAY app and a THROWAWAY
# user, and deletes both at the end. The user is created through the gateway's
# own admin API rather than through the realm's self-registration form for
# exactly that reason -- a user the gateway created is one the gateway can
# delete again, and a realm identity is not otherwise removable without
# realm-admin credentials this script has no business holding. It makes no
# difference to what is being tested: the gate evaluates consent on every
# sign-in, so a brand-new user has BOTH documents outstanding at their first
# one, however the identity came to exist.
#
# The consent RECORDS the run writes are append-only by design and survive; so
# does the anonymous count. That is the stack's contract, not a leak.
param(
    [string]$StackDomain = "saas.localtest.me:8080",
    [string]$Realm = "saas-stack",
    [string]$AdminKey = $env:PLATFORM_ADMIN_KEY,
    [string]$Network = "docker_compose_pkg_dev_default",
    [string]$Image = "sitebin:consent-e2e",
    [string]$AppId = "sitebin-consent-e2e",
    [int]$Port = 8093,
    [string]$TermsVersion = "e2e-2026-09-01",
    [string]$TermsURL = "https://sitebin.io/terms",
    [switch]$SkipBuild,
    [switch]$KeepUp
)

$ErrorActionPreference = "Continue"
$repo = Split-Path -Parent $PSScriptRoot
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null
# Wipe the scratch files. curl leaves -o alone when it cannot connect, so a
# stale page from the previous run would be read as this run's answer -- which
# looks like a passing assertion against a server that never replied.
Get-ChildItem $work -Filter "consent*.*" -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue

$authGw = "http://auth-gw.saas-stack.$StackDomain"
$platform = "http://platform.saas-stack.$StackDomain"
$issuer = "http://auth.$StackDomain/realms/$Realm"
$discovery = "$authGw/api/v1/$AppId"
$base = "sitebin-consent.localtest.me"
$origin = "http://${base}:$Port"
$name = "sitebin-consent-e2e"
$vol = "sitebin-consent-e2e-data"
$callback = "$origin/account/auth/oidc/callback"

# Every assertion in this script must run. A throw or an early return leaves the
# totals looking healthy while the run is silently smaller, so the count is
# checked at the end against this number. Update it when you add or remove one.
$ExpectedAssertions = 27

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
    Write-Host ("== consent E2E: {0} passed, {1} failed (aborted)" -f $script:pass, $script:fail) -ForegroundColor Red
    exit 1
}

if ([string]::IsNullOrWhiteSpace($AdminKey)) {
    Write-Host "-AdminKey (or PLATFORM_ADMIN_KEY) is required: it is the stack's platform admin key." -ForegroundColor Red
    exit 1
}

# ---------- plumbing ----------

$script:seq = 0
function TempFile([string]$ext) { $script:seq++; return (Join-Path $work "consent$($script:seq).$ext") }

# Req is a plain API call. Extra args go to curl verbatim.
function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = TempFile "out"
    $a = @("-s", "-X", $method, "-o", $bodyFile, "-w", "%{http_code}", "--max-time", "30") + $extra + @($url)
    $code = & curl.exe @a
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    return @{ code = [int]$code; body = $body }
}

# Redirect returns the Location of a single non-followed response. Headers go
# to a FILE: `-D -` writes them to stdout, where they join curl's -w output and
# turn the status code into an array.
function Redirect([string]$url) {
    $h = TempFile "hdr"
    & curl.exe -s -o (TempFile "out") -D $h --max-time 30 $url | Out-Null
    if (-not (Test-Path $h)) { return "" }
    return (Match1 ([IO.File]::ReadAllText($h)) 'Location: (\S+)')
}

# PS 5.1 mangles quoted JSON handed to a native executable; give curl a file.
function JsonBody([string]$json) {
    $p = TempFile "json"
    [IO.File]::WriteAllText($p, $json)
    return "@$p"
}

# --- the browser half: one cookie jar, redirects followed, forms posted ---
$jar = Join-Path $work "consent-jar.txt"
function NewBrowser { Remove-Item -Force $jar -ErrorAction SilentlyContinue }
# Browse returns @{ url = <where it ended up>; html = <the page> }.
function Browse([string]$url, [string[]]$form = @()) {
    $page = TempFile "html"
    $a = @("-s", "-L", "-c", $jar, "-b", $jar, "-o", $page, "-w", "%{url_effective}", "--max-time", "60")
    foreach ($f in $form) { $a += @("--data-urlencode", $f) }
    $a += @($url)
    $eff = & curl.exe @a
    $html = ""; if (Test-Path $page) { $html = [IO.File]::ReadAllText($page) }
    return @{ url = "$eff"; html = $html }
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

function StartSitebin([string]$discoveryURL, [string]$issuerURL, [string]$secret) {
    docker rm -f $name 2>$null | Out-Null
    docker volume rm $vol 2>$null | Out-Null
    $terms = '{"version":"' + $TermsVersion + '","url":"' + $TermsURL +
    '","title":{"en":"Sitebin Terms of Service","de":"Sitebin Nutzungsbedingungen"}}'
    # The JSON-valued variables go through an env FILE, not -e. PowerShell 5.1
    # rewrites quotes on their way to a native executable, and the value that
    # reaches the container is JSON with its quotes stripped -- which fails at
    # startup with a parse error naming a character nobody typed.
    $envFile = Join-Path $work "consent.env"
    [IO.File]::WriteAllLines($envFile, @(
            'SITEBIN_TIERS=[{"id":"free","label":"Free","max_site_bytes":10485760,"max_files":50,"max_sites":3,"max_expiry_days":7}]',
            "SITEBIN_STACK_TERMS=$terms"
        ), (New-Object System.Text.UTF8Encoding($false)))
    $a = @("run", "-d", "--name", $name, "--env-file", $envFile,
        # Discovery through the gateway hands back Keycloak's INTERNAL
        # back-channel URLs, so the container has to be on the stack's network
        # to reach the token and JWKS endpoints. extra_hosts covers only the
        # front-channel names, which resolve to 127.0.0.1 = this container.
        "--network", $Network,
        "--add-host", "auth.$($StackDomain.Split(':')[0]):host-gateway",
        "--add-host", "auth-gw.saas-stack.$($StackDomain.Split(':')[0]):host-gateway",
        "--add-host", "platform.saas-stack.$($StackDomain.Split(':')[0]):host-gateway",
        "-p", "${Port}:80", "-v", "${vol}:/data",
        "-e", "SITEBIN_BASE_DOMAIN=${base}:$Port",
        "-e", "SITEBIN_HTTP_ONLY=true",
        "-e", "SITEBIN_ACCOUNT_MODE=tiers",
        "-e", "SITEBIN_LOCAL_AUTH=false",
        "-e", "SITEBIN_DEFAULT_TIER=free",
        "-e", "SITEBIN_RATE_AUTH_PER_5MIN=500",
        # THE SPLIT. The issuer is the realm; the discovery URL is where the
        # document is fetched.
        "-e", "SITEBIN_OAUTH_OIDC_ISSUER=$issuerURL",
        "-e", "SITEBIN_OAUTH_OIDC_DISCOVERY_URL=$discoveryURL",
        "-e", "SITEBIN_OAUTH_OIDC_CLIENT_ID=$AppId-app",
        "-e", "SITEBIN_OAUTH_OIDC_CLIENT_SECRET=$secret",
        "-e", "SITEBIN_OAUTH_OIDC_LABEL=Sign in with IT-Trail",
        # Self-registration, which is what carries the terms declaration.
        "-e", "SITEBIN_STACK_URL=$platform",
        "-e", "SITEBIN_STACK_APP_ID=$AppId",
        "-e", "SITEBIN_STACK_ADMIN_KEY=$AdminKey",
        $Image)
    & docker @a | Out-Null
    if ($LASTEXITCODE -ne 0) { return $false }
    for ($i = 0; $i -lt 40; $i++) {
        Start-Sleep -Milliseconds 700
        if ((Req "GET" "$origin/").code -eq 200) { return $true }
    }
    return $false
}

$script:userId = ""
function Cleanup {
    if ($KeepUp) { return }
    # The user first: deleting the app takes the route that can delete it.
    if (-not [string]::IsNullOrWhiteSpace($script:userId)) {
        Req "DELETE" "$authGw/api/v1/$AppId/users/$($script:userId)" @("-H", "Authorization: Bearer $AdminKey") | Out-Null
    }
    docker rm -f $name 2>$null | Out-Null
    docker volume rm $vol 2>$null | Out-Null
    # Hard delete, purging the licence signing key registration minted: this is
    # a throwaway app that never issued a licence to anybody. Verified rather
    # than fired and forgotten: an app left behind on the stack holds a
    # `terms` version, and a version is immutable once recorded.
    $del = Req "DELETE" "$platform/api/v1/apps/${AppId}?hard=true&purge_licensing=true" @("-H", "Authorization: Bearer $AdminKey")
    $gone = Req "GET" "$platform/api/v1/apps/$AppId" @("-H", "Authorization: Bearer $AdminKey")
    if ($gone.code -ne 404) {
        Write-Host ("  note: could not remove the throwaway app {0} (delete said {1}, it is still there): {2}" -f $AppId, $del.code, $del.body) -ForegroundColor Yellow
    }
}

# ---------- the image ----------

if (-not $SkipBuild) {
    Write-Host "== building $Image (go vet + both suites run inside)" -ForegroundColor Cyan
    $buildLog = Join-Path $work "consent-build.log"
    & docker build --build-arg EDITION=enterprise -t $Image $repo *> $buildLog
    $built = ($LASTEXITCODE -eq 0)
    if (-not $built) { Get-Content $buildLog | Select-Object -Last 40 | ForEach-Object { Write-Host $_ } }
    Assert "enterprise image built" $built "see $buildLog"
    if (-not $built) { exit 1 }
}
else {
    Assert "skipping the build (-SkipBuild)" $true
}

# ---------- register the throwaway app ----------

Write-Host "== registering the throwaway app $AppId" -ForegroundColor Cyan

# Start from nothing. A leftover row from an interrupted run would already hold
# the terms this run is about to prove Sitebin declared.
Req "DELETE" "$platform/api/v1/apps/${AppId}?hard=true&purge_licensing=true" @("-H", "Authorization: Bearer $AdminKey") | Out-Null

# A skeleton registration first, only to obtain the OIDC client secret: it is
# returned once, and Sitebin needs it in its environment before it can boot and
# make its OWN declaration. Deliberately declares NO terms, so what the stack
# ends up holding can only have come from Sitebin.
$skeleton = '{"app_id":"' + $AppId + '","display_name":"Sitebin (consent e2e)","domain":"' + $base + ':' + $Port +
'","auth":{"redirectUris":["' + $callback + '"],"webOrigins":["' + $origin + '"]}}'
$r = Req "POST" "$platform/api/v1/apps" @("-H", "Authorization: Bearer $AdminKey", "-H", "Content-Type: application/json", "--data-binary", (JsonBody $skeleton))
$reg = $null
try { $reg = $r.body | ConvertFrom-Json } catch {}
$secret = ""
if ($null -ne $reg -and $null -ne $reg.auth -and $null -ne $reg.auth.client) { $secret = "$($reg.auth.client.secret)" }
if ([string]::IsNullOrWhiteSpace($secret)) { Fatal "registration did not return an OIDC client secret: $($r.code) $($r.body)" }
Assert "the throwaway app registered and returned a client secret" ($secret.Length -gt 10)

$r = Req "GET" "$platform/api/v1/apps/$AppId" @("-H", "Authorization: Bearer $AdminKey")
$app = $null; try { $app = $r.body | ConvertFrom-Json } catch {}
Assert "it declares no terms yet" ($null -eq $app.config.terms) "got $($app.config.terms)"

# ---------- the stack side of the split ----------

Write-Host "== the gateway's discovery document" -ForegroundColor Cyan

# Asked only AFTER the app is registered. The gateway serves a per-app
# document, and asking for one it has never heard of gets a placeholder whose
# authorization endpoint is not this stack's -- which then looks exactly like
# the gate being missing.

$r = Req "GET" "$discovery/.well-known/openid-configuration"
if ($r.code -ne 200) { Fatal "the stack's auth gateway is not answering at $discovery ($($r.code)). Start the stack first." }
$doc = $null
try { $doc = $r.body | ConvertFrom-Json } catch {}
if ($null -eq $doc) { Fatal "the gateway's discovery document did not parse" }

# This is the whole mechanism, seen from the stack's side: one document, whose
# `issuer` is the realm's but whose `authorization_endpoint` is the gateway's.
Assert "the gateway advertises the realm as the issuer" ($doc.issuer -eq $issuer) "got $($doc.issuer), want $issuer"

# The origin the browser will actually be sent to is the document's, not the
# one this script fetched from: the gateway advertises AUTH_GATEWAY_PUBLIC_URL,
# which can legitimately differ in scheme and port from the address an app
# reaches it on. Everything below follows the document, the way Sitebin does.
$authz = "$($doc.authorization_endpoint)"
$gwOrigin = ""
$gwHost = ""
try { $u = [uri]$authz; $gwOrigin = $u.GetLeftPart([System.UriPartial]::Authority); $gwHost = $u.Host } catch {}
Assert "the gateway advertises ITSELF as the authorization endpoint" ($gwHost -eq ([uri]$authGw).Host) "got $authz"
Assert "so the document's URL and its issuer disagree, which go-oidc refuses by default" ($discovery -ne $doc.issuer)

# A public URL nobody can reach breaks the gate for every app on the stack, and
# it breaks it in the shape of a Sitebin bug: sign-in redirects somewhere that
# does not answer. Say what it is before anything downstream fails.
if ((Req "GET" "$gwOrigin/api/v1/$AppId/.well-known/openid-configuration").code -ne 200) {
    Fatal ("the gateway advertises {0}, which does not answer. That is AUTH_GATEWAY_PUBLIC_URL on the stack's auth-gateway: it must name a scheme, host and port a BROWSER can reach." -f $gwOrigin)
}

# ---------- Sitebin declares its own terms ----------

Write-Host "== starting Sitebin behind the gateway" -ForegroundColor Cyan

$up = StartSitebin $discovery $issuer $secret
if (-not $up) {
    docker logs $name 2>&1 | Select-Object -Last 30 | ForEach-Object { Write-Host $_ }
    Fatal "sitebin did not come up on $origin"
}
Assert "sitebin is up on $origin" $true

Start-Sleep -Seconds 2
$logs = (docker logs $name 2>&1 | Out-String)
Assert "it logged a successful self-registration naming the terms version" ($logs -match "registered with the saas stack" -and $logs -match "terms=$([regex]::Escape($TermsVersion))") "see docker logs $name"

$r = Req "GET" "$platform/api/v1/apps/$AppId" @("-H", "Authorization: Bearer $AdminKey")
$app = $null; try { $app = $r.body | ConvertFrom-Json } catch {}
$stored = $null; if ($null -ne $app) { $stored = $app.config.terms }
Assert "the stack now stores the version Sitebin declared" ($null -ne $stored -and $stored.version -eq $TermsVersion) "got $($stored.version)"
Assert "and the URL" ($null -ne $stored -and $stored.url -eq $TermsURL) "got $($stored.url)"
# The title is a locale map, and a heading in the user's own language is the
# visible half of the declaration.
Assert "and the localized heading" ($null -ne $stored -and $stored.title.de -eq "Sitebin Nutzungsbedingungen") "got $($stored.title)"

# ---------- the redirect that decides everything ----------

Write-Host "== where sign-in actually sends the browser" -ForegroundColor Cyan

$loc = Redirect "$origin/account/auth/oidc"
# This is the payoff of the split: Sitebin fetched the GATEWAY's document, so
# the browser goes to the gateway, where the gate is. It could not have got
# here without InsecureIssuerURLContext -- go-oidc would have refused the
# document outright.
Assert "sign-in redirects to the GATEWAY's authorization endpoint" ($loc -like "$gwOrigin/*") "got $loc"

# ---------- a brand-new user is stopped at the gate ----------

Write-Host "== first sign-in: both documents" -ForegroundColor Cyan

# Created through the gateway so it can be deleted again at the end. It has
# accepted nothing, which is all the gate cares about.
$user = "sbconsent" + (Get-Random -Minimum 100000 -Maximum 999999)
$email = "$user@e2e.invalid"
$pw = "Consent-E2E-" + [guid]::NewGuid().ToString("N").Substring(0, 12) + "-1!"
$mkUser = '{"email":"' + $email + '","password":"' + $pw +
'","firstName":"Consent","lastName":"Throwaway","emailVerified":true}'
$r = Req "POST" "$authGw/api/v1/$AppId/users" @("-H", "Authorization: Bearer $AdminKey", "-H", "Content-Type: application/json", "--data-binary", (JsonBody $mkUser))
$made = $null; try { $made = $r.body | ConvertFrom-Json } catch {}
if ($null -eq $made -or $null -eq $made.data) { Fatal "could not create the throwaway user: $($r.code) $($r.body)" }
$script:userId = "$($made.data.userId)"
Assert "a throwaway user exists and has accepted nothing" ($script:userId.Length -gt 10)

NewBrowser
$p = Browse "$origin/account/auth/oidc"
Assert "the browser lands on the realm's login page" ($p.url -like "http://auth.$StackDomain/realms/$Realm/*") "got $($p.url)"

$action = Match1 $p.html 'id="kc-form-login"[^>]*action="([^"]+)"'
if ([string]::IsNullOrWhiteSpace($action)) { Fatal "could not find the realm login form" }
$p = Browse $action @("username=$email", "password=$pw", "credentialId=")

# The whole point of the feature: correct credentials are NOT enough. The user
# is authenticated and still not signed in to Sitebin.
Assert "a first sign-in is stopped at the stack's consent page" ($p.url -like "$gwOrigin/api/v1/_consent*") "got $($p.url)"
Assert "which Sitebin does not render (it is the gateway's origin)" ($p.url -notlike "$origin/*") "got $($p.url)"

$accepts = MatchAll $p.html 'name="accept" value="([^"]+)"'
Assert "it offers exactly TWO documents" ($accepts.Count -eq 2) "got $($accepts -join ', ')"
Assert "one of them is the PLATFORM's, which no app can declare" (@($accepts | Where-Object { $_ -like "platform:*" }).Count -eq 1) "got $($accepts -join ', ')"
Assert "the other is Sitebin's own, at the version it declared" (@($accepts | Where-Object { $_ -eq "${AppId}:$TermsVersion" }).Count -eq 1) "got $($accepts -join ', ')"
Assert "and the page names Sitebin's version in its text" ($p.html -match [regex]::Escape($TermsVersion)) ""

$flow = Match1 $p.html 'name="flow" value="([^"]+)"'
$form = @("flow=$flow")
foreach ($a in $accepts) { $form += "accept=$a" }
$p = Browse "$gwOrigin/api/v1/_consent" $form

Assert "accepting both lands the user in Sitebin, signed in" ($p.url -eq "$origin/account" -and $p.html -match [regex]::Escape($email)) "got $($p.url)"

# ---------- and only once ----------

Write-Host "== second sign-in: asked nothing" -ForegroundColor Cyan

Browse "$origin/account/logout" | Out-Null
$p = Browse "$origin/account/auth/oidc"
Assert "the second sign-in is NOT stopped at the consent page" ($p.url -notlike "$gwOrigin/api/v1/_consent*") "got $($p.url)"
# The stack's session is still live, so it offers the account rather than the
# login form. Follow it the way a person would click it.
$card = Match1 $p.html 'href="([^"]+)" class="account-card"'
if (-not [string]::IsNullOrWhiteSpace($card)) { $p = Browse $card }
Assert "and lands signed in again" ($p.url -eq "$origin/account" -and $p.html -match [regex]::Escape($email)) "got $($p.url)"

# ---------- the control: discovery at the identity provider ----------

Write-Host "== control: discovery pointed at the identity provider" -ForegroundColor Cyan

# Same app, same terms, same everything -- one setting changed. This is the
# configuration every earlier release of the docs recommended, and the reason
# the gate needed the discovery URL at all.
$up = StartSitebin $issuer $issuer $secret
Assert "sitebin still comes up pointed straight at the realm" $up
$loc = Redirect "$origin/account/auth/oidc"
Assert "but sign-in now goes to the realm, not the gateway: the gate is bypassed" ($loc -like "http://auth.$StackDomain/*" -and $loc -notlike "$gwOrigin/*") "got $loc"

# ---------- the check the split does NOT loosen ----------

Write-Host "== control: a discovery document advertising another issuer" -ForegroundColor Cyan

# InsecureIssuerURLContext gives up "the document lived at the issuer's URL".
# It must NOT give up "the document says the issuer I configured" -- otherwise
# a discovery URL would be a way to accept tokens from a realm nobody chose.
$up = StartSitebin $discovery "http://auth.$StackDomain/realms/some-other-realm" $secret
Assert "sitebin still starts: discovery is lazy and never blocks the boot" $up
$loc = Redirect "$origin/account/auth/oidc"
Assert "but sign-in refuses rather than trusting the mismatched document" ([string]::IsNullOrWhiteSpace($loc) -or $loc -notlike "$gwOrigin/*") "got $loc"

# ---------- cleanup ----------

Cleanup

Write-Host ""
$total = $script:pass + $script:fail
if ($total -ne $ExpectedAssertions) {
    Write-Host ("  FAIL assertion count: ran {0}, expected {1} - an assertion was skipped or threw" -f $total, $ExpectedAssertions) -ForegroundColor Red
    $script:fail++
}
Write-Host ("== consent E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
