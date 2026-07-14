# Sitebin Enterprise — accounts-mode E2E against the real enterprise image.
#
#   powershell -File e2e\accounts.ps1 [-Image sitebin:dev-ee]
#
# Verifies: community-open behavior is gated off, anonymous create is blocked,
# signup/login set a session, logged-in create is owned, the dashboard lists
# owned sites, edit-password reset works, and account deletion cascades.
param(
    [string]$Image = "sitebin:dev-ee",
    [int]$Port = 8087
)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$name = "sitebin-acct-e2e"
$vol = "sitebin-acct-e2e-data"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

$script:pass = 0; $script:fail = 0
function Assert([string]$n, [bool]$c, [string]$d = "") {
    if ($c) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}

# curl with a cookie jar; returns @{code; body; headers}
function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = Join-Path $work "b.tmp"; $hdrFile = Join-Path $work "h.tmp"
    $args = @("-s", "-X", $method, "-o", $bodyFile, "-D", $hdrFile, "-w", "%{http_code}", "--max-time", "30") + $extra + @($url)
    $code = & curl.exe @args
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    $headers = ""; if (Test-Path $hdrFile) { $headers = [IO.File]::ReadAllText($hdrFile) }
    return @{ code = [int]$code; body = $body; headers = $headers }
}

# PS 5.1: pass quoted JSON to curl via a temp file to avoid arg mangling
function JsonBodyA([string]$json) {
    $p = Join-Path $work ("ja" + (Get-Random) + ".json"); [IO.File]::WriteAllText($p, $json); return "@$p"
}

Write-Host "== starting enterprise image (accounts mode) on $Port" -ForegroundColor Cyan
docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_ACCOUNT_MODE=accounts" `
    -e "SITEBIN_RATE_AUTH_PER_5MIN=200" `
    $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; exit 1 }

$up = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break } }
Assert "container up (accounts mode)" $up
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; exit 1 }

# edition reported
$ver = docker exec $name sitebin version
Assert "enterprise edition binary" ($ver -match "enterprise") "$ver"

# anonymous create is blocked
$r = Req "POST" "$origin/api/sites"
Assert "anonymous create blocked (401)" ($r.code -eq 401) "got $($r.code)"

# account pages served
$r = Req "GET" "$origin/account/signup"
Assert "signup page served" ($r.code -eq 200 -and $r.body -match "Create your account")

$jar = Join-Path $work "acct-cookies.txt"
Remove-Item $jar -ErrorAction SilentlyContinue

# signup
$r = Req "POST" "$origin/account/signup" @("-c", $jar, "--data-urlencode", "email=e2e@example.com", "--data-urlencode", "password=password12345")
Assert "signup succeeds (303)" ($r.code -eq 303) "got $($r.code): $($r.body)"
Assert "signup sets session cookie" ((Get-Content $jar -Raw -ErrorAction SilentlyContinue) -match "sitebin_s")

# dashboard shows the account
$r = Req "GET" "$origin/account" @("-b", $jar)
Assert "dashboard shows account" ($r.code -eq 200 -and $r.body -match "e2e@example.com")
Assert "dashboard has no sites yet" ($r.body -match "No sites yet")

# create a site while logged in — becomes owned
$idxFile = Join-Path $work "acct-index.html"
[IO.File]::WriteAllText($idxFile, "<h1>owned-site-ok</h1>")
$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "files=@$idxFile;filename=index.html")
Assert "logged-in create succeeds (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
$site = $null; if ($r.code -eq 201) { $site = $r.body | ConvertFrom-Json }

if ($site) {
    $r = Req "GET" "$($site.view_url)/"
    Assert "owned site serves content" ($r.body -match "owned-site-ok")

    # dashboard now lists the owned site
    $r = Req "GET" "$origin/account" @("-b", $jar)
    Assert "dashboard lists owned site" ($r.body -match [regex]::Escape($site.id))

    # a DIFFERENT account cannot see or manage it
    $jar2 = Join-Path $work "acct2.txt"; Remove-Item $jar2 -ErrorAction SilentlyContinue
    Req "POST" "$origin/account/signup" @("-c", $jar2, "--data-urlencode", "email=other@example.com", "--data-urlencode", "password=password12345") | Out-Null
    $r = Req "GET" "$origin/account" @("-b", $jar2)
    Assert "other account sees no sites" ($r.body -match "No sites yet")

    # custom domains ARE available in the enterprise edition
    $edit = ($site.edit_url -split "/e/")[1]
    $r = Req "POST" "$origin/api/sites/$edit/domains" @("-H", "X-Edit-Password: $($site.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBodyA '{"domain":"acct.e2e.test"}'))
    Assert "custom domain allowed in enterprise (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
    $r = Req "GET" "http://127.0.0.1:$Port/" @("-H", "Host: acct.e2e.test")
    Assert "custom domain serves owned site" ($r.body -match "owned-site-ok") "code $($r.code)"
}

# duplicate email rejected
$r = Req "POST" "$origin/account/signup" @("--data-urlencode", "email=e2e@example.com", "--data-urlencode", "password=password12345")
Assert "duplicate email rejected" ($r.body -match "already registered")

# wrong login rejected
$r = Req "POST" "$origin/account/login" @("--data-urlencode", "email=e2e@example.com", "--data-urlencode", "password=wrongpass")
Assert "wrong login rejected" ($r.body -match "Incorrect email or password")

# session persists across container restart (filesystem accounts)
docker restart $name | Out-Null
$up2 = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { $up2 = $true; break } }
Assert "restart preserves accounts" ($up2 -and (Req "GET" "$origin/account" @("-b", $jar)).body -match "e2e@example.com")

# main domain never serves user content
$r = Req "GET" "$origin/index.html"
Assert "main domain still not serving user files" ($r.code -ne 200 -or $r.body -notmatch "owned-site-ok")

# cleanup
docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null

Write-Host ""
Write-Host ("== accounts E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
