# Sitebin Enterprise -- licensing E2E against a real enterprise image.
#
#   powershell -File e2e\license.ps1 [-Image sitebin:e2e-license] [-Port 8090] [-SkipBuild]
#
# Self-contained: no SaaS Stack, no network, no secrets. `e2e/mintlicense`
# generates a THROWAWAY root, mints certificates and licences under it, and the
# image is built with that root baked in through the Dockerfile's LICENSE_ROOTS
# build arg -- the same -ldflags path the release pipeline uses. Nothing here can
# verify a real licence and nothing here can produce one.
#
# What it proves, by running containers rather than by unit-testing the parser:
#
#   licensed          creating a site works
#   grace             creating still works AND the account UI says so, loudly
#   expired           a NEW site is refused 402, an EXISTING one is STILL SERVED
#                     and can still be updated  <-- the load-bearing half
#   none              a fresh instance creates freely for the 90-day trial
#   untrusted root    behaves as `none`, NOT as `expired`, even though the
#   malformed key     licence inside it expired long ago  <-- also load-bearing
#   entitlement       max_custom_domains is an instance-wide ceiling: it refuses
#                     the NEXT domain and never disturbs the ones configured
#
# The refusal text matters as much as the status code. A customer who paid for
# custom domains and is told they are "an enterprise feature" has been given the
# wrong answer, so the entitlement refusal is asserted to name the licence.
param(
    [string]$Image = "sitebin:e2e-license",
    [int]$Port = 8090,
    [switch]$SkipBuild,
    [switch]$KeepUp
)

$ErrorActionPreference = "Continue"
$repo = Split-Path -Parent $PSScriptRoot
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$name = "sitebin-license-e2e"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

# Every assertion in this script must run. A throw or an early return leaves the
# totals looking healthy while the run is silently smaller, so the count is
# checked at the end against this number. Update it when you add or remove one.
# (mcp.ps1 grew this guard after exactly that happened.)
$ExpectedAssertions = 54

$script:pass = 0; $script:fail = 0
# $c is deliberately untyped: with [bool], PowerShell throws on anything it
# cannot coerce, and a thrown assertion never runs, never counts and never
# fails the script.
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

function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = Join-Path $work "lb.tmp"
    $args = @("-s", "-X", $method, "-o", $bodyFile, "-w", "%{http_code}", "--max-time", "30") + $extra + @($url)
    $code = & curl.exe @args
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    return @{ code = [int]$code; body = $body }
}

# PS 5.1 mangles quoted JSON handed to a native executable; give curl a file.
$script:jsonSeq = 0
function JsonBody([string]$json) {
    $script:jsonSeq++
    $p = Join-Path $work "lic$($script:jsonSeq).json"
    [IO.File]::WriteAllText($p, $json)
    return "@$p"
}

# ---------- minting ----------

# Mint runs the in-repo issuer and returns its ROOT_PUBLIC / ROOT_PRIVATE /
# LICENSE lines as a hashtable.
function Mint([string[]]$mintArgs) {
    Push-Location $repo
    $out = & go run -tags ee ./e2e/mintlicense @mintArgs 2>&1
    $rc = $LASTEXITCODE
    Pop-Location
    $h = @{}
    foreach ($line in @($out)) {
        $s = "$line"
        if ($s -match '^(ROOT_PUBLIC|ROOT_PRIVATE|LICENSE)=(.+)$') { $h[$Matches[1]] = $Matches[2].Trim() }
    }
    if ($rc -ne 0 -or -not $h.ContainsKey("LICENSE")) {
        Write-Host "mintlicense failed: $out" -ForegroundColor Red
        exit 1
    }
    return $h
}

Write-Host "== minting test licences (throwaway root)" -ForegroundColor Cyan
# The trusted root. Every licence below except the untrusted one is signed under
# it, because the image can only be built with one set of roots baked in.
$licensed = Mint @("-holder", "Licensed GmbH", "-plan", "team", "-expires", "8760h", "-grace", "17520h")
$root = $licensed.ROOT_PUBLIC
$rootPriv = $licensed.ROOT_PRIVATE
Assert "minted a root public key" ($root.Length -gt 20) "$root"
Assert "minted a four-segment licence" (($licensed.LICENSE -split '\.').Count -eq 4) "$($licensed.LICENSE)"

# expired 1 day ago, in grace for another 30.
$grace = Mint @("-root-private", $rootPriv, "-holder", "Grace GmbH", "-expires", "-24h", "-grace", "720h")
# expired 90 days ago, grace ended yesterday.
$expired = Mint @("-root-private", $rootPriv, "-holder", "Expired GmbH", "-expires", "-2160h", "-grace", "-24h")
# valid, but entitled to only two custom domains instance-wide.
$capped = Mint @("-root-private", $rootPriv, "-holder", "Capped GmbH", "-expires", "8760h", "-grace", "17520h", "-max-domains", "2")
# Signed under a DIFFERENT root this build knows nothing about, and long
# expired on top. It must land in `none`, never in `expired`.
$untrusted = Mint @("-holder", "Untrusted GmbH", "-expires", "-2160h", "-grace", "-24h")
Assert "the untrusted licence uses a different root" ($untrusted.ROOT_PUBLIC -ne $root)

# ---------- the image ----------

if (-not $SkipBuild) {
    Write-Host "== building $Image with the test root baked in (go vet + both suites run inside)" -ForegroundColor Cyan
    $buildLog = Join-Path $work "license-build.log"
    & docker build --build-arg EDITION=enterprise --build-arg "LICENSE_ROOTS=$root" -t $Image $repo *> $buildLog
    $built = ($LASTEXITCODE -eq 0)
    if (-not $built) { Get-Content $buildLog | Select-Object -Last 40 | ForEach-Object { Write-Host $_ } }
    Assert "enterprise image built with LICENSE_ROOTS" $built "see $buildLog"
    if (-not $built) { exit 1 }
}
else {
    Assert "skipping the build (-SkipBuild)" $true
}

# ---------- container plumbing ----------

$script:vol = ""
function StartInstance([string]$vol, [string[]]$envArgs, [switch]$Keep) {
    docker rm -f $name 2>$null | Out-Null
    if (-not $Keep) { docker volume rm $vol 2>$null | Out-Null }
    $script:vol = $vol
    $a = @("run", "-d", "--name", $name, "-p", "${Port}:80", "-v", "${vol}:/data",
        "-e", "SITEBIN_BASE_DOMAIN=${base}:$Port", "-e", "SITEBIN_HTTP_ONLY=true",
        "-e", "SITEBIN_ACCOUNT_MODE=accounts", "-e", "SITEBIN_RATE_AUTH_PER_5MIN=200") + $envArgs + @($Image)
    & docker @a | Out-Null
    if ($LASTEXITCODE -ne 0) { return $false }
    for ($i = 0; $i -lt 40; $i++) {
        Start-Sleep -Milliseconds 700
        if ((Req "GET" "$origin/").code -eq 200) { return $true }
    }
    docker logs $name
    return $false
}

function Logs() { return ((docker logs $name 2>&1) | Out-String) }

# SignUp creates an account and returns its cookie jar. Anonymous creation is
# blocked in accounts mode with 401, which would mask the licence's own 402, so
# every creation here is done signed in.
function SignUp([string]$tag) {
    $jar = Join-Path $work "lic-$tag.jar"
    Remove-Item $jar -ErrorAction SilentlyContinue
    Req "POST" "$origin/account/signup" @("-c", $jar,
        "--data-urlencode", "email=$tag@example.com",
        "--data-urlencode", "password=password12345") | Out-Null
    return $jar
}

$indexFile = Join-Path $work "lic-index.html"
[IO.File]::WriteAllText($indexFile, "<h1>license-e2e-ok</h1>")

function CreateSite([string]$jar) {
    return Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "files=@${indexFile};filename=index.html")
}

function Dashboard([string]$jar) { return Req "GET" "$origin/account" @("-b", $jar) }

# ---------- licensed ----------

Write-Host "== licensed" -ForegroundColor Cyan
$up = StartInstance "sitebin-lic-e2e-ok" @("-e", "SITEBIN_LICENSE_KEY=$($licensed.LICENSE)")
Assert "container up (licensed)" $up
if (-not $up) { docker rm -f $name | Out-Null; exit 1 }
Assert "licensed: startup logs state=licensed" ((Logs) -match "state=licensed") ""

$jar = SignUp "licensed"
$r = CreateSite $jar
Assert "licensed: creating a site is allowed (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
$site = $null; if ($r.code -eq 201) { $site = $r.body | ConvertFrom-Json }
$r = Req "GET" $site.view_url
Assert "licensed: the site serves (200)" ($r.code -eq 200 -and $r.body -match "license-e2e-ok") "got $($r.code)"
$r = Dashboard $jar
Assert "licensed: the dashboard shows no licence notice" ($r.code -eq 200 -and $r.body -notmatch "Sitebin Enterprise licence") ""

# ---------- grace ----------

Write-Host "== grace (past expires_at, before grace_until)" -ForegroundColor Cyan
$graceVol = "sitebin-lic-e2e-grace"
$up = StartInstance $graceVol @("-e", "SITEBIN_LICENSE_KEY=$($grace.LICENSE)")
Assert "container up (grace)" $up
if (-not $up) { docker rm -f $name | Out-Null; exit 1 }
Assert "grace: startup logs state=grace" ((Logs) -match "state=grace") ""
Assert "grace: nothing is restricted" ((Logs) -match "restricts_creation=false") ""

$jar = SignUp "grace"
$r = CreateSite $jar
Assert "grace: creating a site is still allowed (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
$graceSite = $null; if ($r.code -eq 201) { $graceSite = $r.body | ConvertFrom-Json }
$r = Req "GET" $graceSite.view_url
Assert "grace: the created site serves (200)" ($r.code -eq 200) "got $($r.code)"

$r = Dashboard $jar
Assert "grace: the dashboard carries the licence notice" ($r.code -eq 200 -and $r.body -match "Sitebin Enterprise licence") ""
Assert "grace: the notice says service continues until grace_until" ($r.body -match "Service continues until") ""
Assert "grace: the notice is a warning, not an error" ($r.body -match "licnotice warn") ""

# The served site must be untouched by any of it -- the design forbids injecting
# anything into user content.
$r = Req "GET" $graceSite.view_url
Assert "grace: nothing is injected into the served site" ($r.body -notmatch "licence" -and $r.body -notmatch "Sitebin Enterprise") "$($r.body)"

# ---------- expired: the same instance, the same volume, the same site ----------

Write-Host "== expired (past grace_until) -- on the SAME data as the grace run" -ForegroundColor Cyan
$up = StartInstance $graceVol @("-e", "SITEBIN_LICENSE_KEY=$($expired.LICENSE)") -Keep
Assert "container up (expired)" $up
if (-not $up) { docker rm -f $name | Out-Null; exit 1 }
Assert "expired: startup logs state=expired" ((Logs) -match "state=expired") ""
Assert "expired: creation is restricted" ((Logs) -match "restricts_creation=true") ""

$r = CreateSite $jar
Assert "expired: a NEW site is refused (402)" ($r.code -eq 402) "got $($r.code): $($r.body)"
Assert "expired: the refusal names the licence" ($r.body -match "license has expired") "$($r.body)"

# The half that matters. An expired licence must never reach an existing site.
$r = Req "GET" $graceSite.view_url
Assert "expired: the site created earlier is STILL SERVED (200)" ($r.code -eq 200 -and $r.body -match "license-e2e-ok") "got $($r.code): $($r.body)"

$edit = ($graceSite.edit_url -split "/e/")[1]
$pw = $graceSite.edit_password
$more = Join-Path $work "lic-about.html"
[IO.File]::WriteAllText($more, "<p>still-editable</p>")
$r = Req "POST" "$origin/api/sites/$edit/files" @("-H", "X-Edit-Password: $pw", "-F", "files=@${more};filename=about.html")
Assert "expired: an existing site can STILL be updated (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = Req "GET" (($graceSite.view_url.TrimEnd("/")) + "/about.html")
Assert "expired: the update actually serves" ($r.code -eq 200 -and $r.body -match "still-editable") "got $($r.code)"

$r = Dashboard $jar
Assert "expired: the dashboard carries the expiry notice" ($r.code -eq 200 -and $r.body -match "grace period ended") ""
Assert "expired: the notice is styled as an error" ($r.body -match "licnotice err") ""

# ---------- none, on a fresh instance ----------

Write-Host "== none (no key at all, fresh instance: the 90-day trial)" -ForegroundColor Cyan
$up = StartInstance "sitebin-lic-e2e-none" @()
Assert "container up (no licence)" $up
if (-not $up) { docker rm -f $name | Out-Null; exit 1 }
Assert "none: startup logs state=none" ((Logs) -match "state=none") ""
Assert "none: the trial is not restricted" ((Logs) -match "restricts_creation=false") ""

$jar = SignUp "none"
$r = CreateSite $jar
Assert "none: creating a site is allowed during the trial (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
$r = Dashboard $jar
Assert "none: no notice during the trial" ($r.code -eq 200 -and $r.body -notmatch "Sitebin Enterprise licence") ""

# ---------- untrusted root: `none`, never `expired` ----------

Write-Host "== untrusted root (a long-expired licence under a root this build does not trust)" -ForegroundColor Cyan
$up = StartInstance "sitebin-lic-e2e-untrusted" @("-e", "SITEBIN_LICENSE_KEY=$($untrusted.LICENSE)")
Assert "container up (untrusted root)" $up
if (-not $up) { docker rm -f $name | Out-Null; exit 1 }
$logs = Logs
Assert "untrusted root: the instance still starts" ($logs -match "enterprise license") ""
Assert "untrusted root: it is reported as none, NOT expired" ($logs -match "state=none") "$logs"
Assert "untrusted root: the rejection is logged" ($logs -match "not signed by a trusted root") "$logs"

$jar = SignUp "untrusted"
$r = CreateSite $jar
Assert "untrusted root: creating a site is allowed (201) -- a config mistake is not a punishment" ($r.code -eq 201) "got $($r.code): $($r.body)"

# ---------- malformed key ----------

Write-Host "== malformed key" -ForegroundColor Cyan
$up = StartInstance "sitebin-lic-e2e-malformed" @("-e", "SITEBIN_LICENSE_KEY=not-a-license")
Assert "container up (malformed key)" $up
if (-not $up) { docker rm -f $name | Out-Null; exit 1 }
$logs = Logs
Assert "malformed key: it is reported as none, NOT expired" ($logs -match "state=none") "$logs"
Assert "malformed key: the parse failure is logged" ($logs -match "malformed license key") "$logs"

$jar = SignUp "malformed"
$r = CreateSite $jar
Assert "malformed key: creating a site is allowed (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"

# ---------- the custom-domain entitlement ----------

Write-Host "== entitlements.max_custom_domains = 2 (an instance-wide ceiling)" -ForegroundColor Cyan
$up = StartInstance "sitebin-lic-e2e-domains" @("-e", "SITEBIN_LICENSE_KEY=$($capped.LICENSE)")
Assert "container up (capped licence)" $up
if (-not $up) { docker rm -f $name | Out-Null; exit 1 }
Assert "entitlement: the cap is logged" ((Logs) -match "max_custom_domains=2") ""

$jar = SignUp "domains"
$r = CreateSite $jar
Assert "entitlement: site created (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
$dsite = $null; if ($r.code -eq 201) { $dsite = $r.body | ConvertFrom-Json }
$dedit = ($dsite.edit_url -split "/e/")[1]
$dpw = $dsite.edit_password

function AddDomain([string]$d) {
    return Req "POST" "$origin/api/sites/$dedit/domains" @(
        "-H", "X-Edit-Password: $dpw", "-H", "Content-Type: application/json",
        "--data", (JsonBody ('{"domain":"' + $d + '"}')))
}

$r = AddDomain "one.lic-e2e.test"
Assert "entitlement: the first domain is added (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = AddDomain "two.lic-e2e.test"
Assert "entitlement: the second domain is added (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = AddDomain "three.lic-e2e.test"
Assert "entitlement: the third is refused (403)" ($r.code -eq 403) "got $($r.code): $($r.body)"
Assert "entitlement: the refusal cites the licence" ($r.body -match "license covers 2 custom domains") "$($r.body)"
Assert "entitlement: it does NOT claim custom domains are an enterprise feature" ($r.body -notmatch "enterprise feature") "$($r.body)"

# Already-configured domains are untouched: the ceiling refuses the next one and
# nothing else.
$r = Req "GET" "$origin/api/sites/$dedit" @("-H", "X-Edit-Password: $dpw")
$meta = $null; if ($r.code -eq 200) { $meta = $r.body | ConvertFrom-Json }
Assert "entitlement: both domains are still configured" ($null -ne $meta -and $meta.custom_domains.Count -eq 2) "$($r.body)"
$r = Req "GET" "http://127.0.0.1:$Port/" @("-H", "Host: one.lic-e2e.test")
Assert "entitlement: the first domain still serves (200)" ($r.code -eq 200 -and $r.body -match "license-e2e-ok") "got $($r.code): $($r.body)"
$r = Req "GET" "http://127.0.0.1:$Port/" @("-H", "Host: two.lic-e2e.test")
Assert "entitlement: the second domain still serves (200)" ($r.code -eq 200 -and $r.body -match "license-e2e-ok") "got $($r.code): $($r.body)"
$r = Req "GET" "http://127.0.0.1:$Port/" @("-H", "Host: three.lic-e2e.test")
Assert "entitlement: the refused domain serves nothing" ($r.code -ne 200) "got $($r.code)"

# ---------- cleanup ----------

if (-not $KeepUp) {
    docker rm -f $name 2>$null | Out-Null
    foreach ($v in @("sitebin-lic-e2e-ok", "sitebin-lic-e2e-grace", "sitebin-lic-e2e-none",
            "sitebin-lic-e2e-untrusted", "sitebin-lic-e2e-malformed", "sitebin-lic-e2e-domains")) {
        docker volume rm $v 2>$null | Out-Null
    }
}

Write-Host ""
$total = $script:pass + $script:fail
if ($total -ne $ExpectedAssertions) {
    Write-Host ("  FAIL assertion count: ran {0}, expected {1} - an assertion was skipped or threw" -f $total, $ExpectedAssertions) -ForegroundColor Red
    $script:fail++
}
Write-Host ("== licensing E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
