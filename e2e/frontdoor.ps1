# Sitebin Enterprise -- front door E2E against the real enterprise image.
#
#   powershell -File e2e\frontdoor.ps1 [-Image sitebin:dev-ee] [-Port 8089]
#
# Verifies: GET / leaves for the home URL when signed out and for the dashboard
# when signed in; /account/status answers signed_in true|false and speaks CORS
# only to a session origin (browser-shaped creates carry Sec-Fetch-Site, as a
# browser sends it); a create from the session origin with the
# X-Sitebin-Session header and the cookie is owned, without the header or from
# another embed origin it is not; the preflight allows the session header.
param(
    [string]$Image = "sitebin:dev-ee",
    [int]$Port = 8089
)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$door = "http://door.localtest.me"
$other = "http://other.localtest.me"
$name = "sitebin-door-e2e"
$vol = "sitebin-door-e2e-data"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

$script:pass = 0; $script:fail = 0
function Assert([string]$n, [bool]$c, [string]$d = "") {
    if ($c) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}

# curl; returns @{code; body; headers}
function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = Join-Path $work "b.tmp"; $hdrFile = Join-Path $work "h.tmp"
    $args = @("-s", "-X", $method, "-o", $bodyFile, "-D", $hdrFile, "-w", "%{http_code}", "--max-time", "30") + $extra + @($url)
    $code = & curl.exe @args
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    $headers = ""; if (Test-Path $hdrFile) { $headers = [IO.File]::ReadAllText($hdrFile) }
    return @{ code = [int]$code; body = $body; headers = $headers }
}

Write-Host "== starting enterprise image (front door) on $Port" -ForegroundColor Cyan
docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_ACCOUNT_MODE=accounts" -e "SITEBIN_ALLOW_ANON_CREATE=true" `
    -e "SITEBIN_RATE_AUTH_PER_5MIN=200" `
    -e "SITEBIN_EMBED_ORIGINS=$door,$other" `
    -e "SITEBIN_SESSION_ORIGINS=$door" `
    -e "SITEBIN_HOME_URL=$door" `
    -e "SITEBIN_FRONT_DOOR=true" `
    $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; exit 1 }

$up = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/account/login").code -eq 200) { $up = $true; break } }
Assert "container up (front door)" $up
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; docker volume rm $vol | Out-Null; exit 1 }

# signed out: GET / leaves for the home URL
$r = Req "GET" "$origin/"
Assert "signed out: GET / is 302 (got $($r.code))" ($r.code -eq 302) "got $($r.code)"
Assert "signed out: GET / goes to the home URL" ($r.headers -match "(?im)^Location:\s*$([regex]::Escape($door))/?\s*$") "$($r.headers)"

# sign up, keep the cookie
$jar = Join-Path $work "door-cookies.txt"
Remove-Item $jar -ErrorAction SilentlyContinue
$r = Req "POST" "$origin/account/signup" @("-c", $jar, "--data-urlencode", "email=door@example.com", "--data-urlencode", "password=password12345")
Assert "signup succeeds (303)" ($r.code -eq 303) "got $($r.code): $($r.body)"

# signed in: GET / goes to the dashboard
$r = Req "GET" "$origin/" @("-b", $jar)
Assert "signed in: GET / is 303" ($r.code -eq 303) "got $($r.code)"
Assert "signed in: GET / goes to /account" ($r.headers -match "(?im)^Location:\s*/account\s*$") "$($r.headers)"

# the status call
$r = Req "GET" "$origin/account/status" @("-b", $jar)
Assert "status with the cookie: signed_in true" ($r.code -eq 200 -and $r.body -match '"signed_in"\s*:\s*true') "got $($r.code): $($r.body)"
$r = Req "GET" "$origin/account/status"
Assert "status without the cookie: signed_in false" ($r.code -eq 200 -and $r.body -match '"signed_in"\s*:\s*false') "got $($r.code): $($r.body)"

$r = Req "GET" "$origin/account/status" @("-b", $jar, "-H", "Origin: $door")
Assert "status from the session origin: ACAO names it" ($r.headers -match "(?im)^Access-Control-Allow-Origin:\s*$([regex]::Escape($door))\s*$") "$($r.headers)"
Assert "status from the session origin: credentials allowed" ($r.headers -match "(?im)^Access-Control-Allow-Credentials:\s*true\s*$") "$($r.headers)"
$r = Req "GET" "$origin/account/status" @("-b", $jar, "-H", "Origin: $other")
Assert "status from another origin: no ACAO" ($r.headers -notmatch "(?im)^Access-Control-Allow-Origin:") "$($r.headers)"

# creates
$idx = Join-Path $work "door-index.html"
[IO.File]::WriteAllText($idx, "<h1>door-ok</h1>")
$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-H", "Origin: $door", "-H", "X-Sitebin-Session: 1", "-F", "files=@$idx;filename=index.html")
Assert "create from the session origin with the header (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
Assert "... is owned" ($r.body -match '"owned"\s*:\s*true') "$($r.body)"

$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-H", "Origin: $door", "-H", "Sec-Fetch-Site: cross-site", "-F", "files=@$idx;filename=index.html")
Assert "create from the session origin without the header (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
Assert "... is not owned" ($r.body -match '"owned"\s*:\s*false') "$($r.body)"

$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-H", "Origin: $other", "-H", "Sec-Fetch-Site: cross-site", "-H", "X-Sitebin-Session: 1", "-F", "files=@$idx;filename=index.html")
Assert "create from another embed origin with the header (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
Assert "... is not owned" ($r.body -match '"owned"\s*:\s*false') "$($r.body)"

# preflight
$r = Req "OPTIONS" "$origin/api/sites" @("-H", "Origin: $door", "-H", "Access-Control-Request-Method: POST", "-H", "Access-Control-Request-Headers: x-sitebin-session")
Assert "preflight from the session origin (204)" ($r.code -eq 204) "got $($r.code)"
Assert "preflight allows X-Sitebin-Session" ($r.headers -match "(?im)^Access-Control-Allow-Headers:.*X-Sitebin-Session") "$($r.headers)"

# cleanup
docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null

Write-Host ""
Write-Host ("== front door E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
