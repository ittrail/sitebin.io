# Sitebin Enterprise -- domain redirects and the generated address, E2E
# against a real enterprise image: the whole chain through the real Caddy.
#
#   powershell -File e2e\redirects.ps1 [-Image sitebin:e2e-redirects] [-Port 8197] [-SkipBuild]
#
# Build the image with `docker build --build-arg EDITION=enterprise -t
# sitebin:e2e-redirects .` (or omit -SkipBuild and this script does it).
# Domain verification is off: this suite is about what an attached domain
# answers, not about DNS.
#
# What it proves, by asking Caddy rather than the backend:
#
#   a redirecting domain answers the 3xx itself, with the path and query kept
#   and the cache header, and the target still serves the site
#   the generated address can be off (404) or redirect
#   removing the target domain makes everything serve again
#   a container site's addresses cannot redirect (409 from the API)
param(
    [string]$Image = "sitebin:e2e-redirects",
    [int]$Port = 8197,
    [switch]$SkipBuild,
    [switch]$KeepUp
)

$ErrorActionPreference = "Continue"
$repo = Split-Path -Parent $PSScriptRoot
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$name = "sitebin-redirects-e2e"
$vol = "sitebin-redirects-e2e"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

# Every assertion must run; the count is checked at the end. Update it when
# you add or remove one.
$ExpectedAssertions = 24

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

# Req never follows redirects, and also returns the response headers.
function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = Join-Path $work "rd.tmp"
    $headFile = Join-Path $work "rd.head"
    Remove-Item $bodyFile, $headFile -ErrorAction SilentlyContinue
    $args = @("-s", "-X", $method, "-o", $bodyFile, "-D", $headFile, "-w", "%{http_code}", "--max-time", "30") + $extra + @($url)
    $code = & curl.exe @args
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    $head = ""; if (Test-Path $headFile) { $head = [IO.File]::ReadAllText($headFile) }
    return @{ code = [int]$code; body = $body; head = $head }
}

function Header($r, [string]$h) {
    foreach ($line in ($r.head -split "`r?`n")) {
        if ($line -match ("^" + [regex]::Escape($h) + ":\s*(.*)$")) { return $Matches[1].Trim() }
    }
    return ""
}

# PS 5.1 mangles quoted JSON handed to a native executable; give curl a file.
$script:jsonSeq = 0
function JsonBody([string]$json) {
    $script:jsonSeq++
    $p = Join-Path $work "rd$($script:jsonSeq).json"
    [IO.File]::WriteAllText($p, $json)
    return "@$p"
}

if (-not $SkipBuild) {
    Write-Host "== building $Image (enterprise)" -ForegroundColor Cyan
    docker build --build-arg EDITION=enterprise -t $Image $repo
    if ($LASTEXITCODE -ne 0) { Write-Host "build failed" -ForegroundColor Red; exit 1 }
}

docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null
$a = @("run", "-d", "--name", $name, "-p", "${Port}:80", "-v", "${vol}:/data",
    "-e", "SITEBIN_BASE_DOMAIN=${base}:$Port", "-e", "SITEBIN_HTTP_ONLY=true",
    "-e", "SITEBIN_ACCOUNT_MODE=accounts", "-e", "SITEBIN_RATE_AUTH_PER_5MIN=200",
    "-e", "SITEBIN_DOMAIN_VERIFICATION=off", $Image)
& docker @a | Out-Null
$up = $false
for ($i = 0; $i -lt 40; $i++) {
    Start-Sleep -Milliseconds 700
    if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break }
}
Assert "container up" $up
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; exit 1 }

# ---------- a signed-in owner with one site and two domains ----------

$jar = Join-Path $work "rd.jar"
Remove-Item $jar -ErrorAction SilentlyContinue
Req "POST" "$origin/account/signup" @("-c", $jar,
    "--data-urlencode", "email=redirects@example.com",
    "--data-urlencode", "password=password12345") | Out-Null
$indexFile = Join-Path $work "rd-index.html"
[IO.File]::WriteAllText($indexFile, "<h1>redirects-e2e-ok</h1>")
$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "files=@${indexFile};filename=index.html")
Assert "site created (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
$site = $null; if ($r.code -eq 201) { $site = $r.body | ConvertFrom-Json }
$edit = ($site.edit_url -split "/e/")[1]
$pw = $site.edit_password
$viewHost = "$($site.id).$base"

function Api([string]$method, [string]$path, [string]$json = "") {
    $extra = @("-H", "X-Edit-Password: $pw")
    if ($json -ne "") { $extra += @("-H", "Content-Type: application/json", "--data", (JsonBody $json)) }
    return Req $method "$origin/api/sites/$edit$path" $extra
}
function Visit([string]$hostName, [string]$path = "/") {
    return Req "GET" "http://127.0.0.1:$Port$path" @("-H", "Host: $hostName")
}

$r = Api "POST" "/domains" '{"domain":"shop.at.e2e.test"}'
Assert "first domain attached (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = Api "POST" "/domains" '{"domain":"shop.de.e2e.test"}'
Assert "second domain attached (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = Visit "shop.de.e2e.test"
Assert "before any rule the second domain serves the site" ($r.code -eq 200 -and $r.body -match "redirects-e2e-ok") "got $($r.code)"

# ---------- a domain that redirects ----------

Write-Host "== shop.de -> shop.at, 301" -ForegroundColor Cyan
$r = Api "PUT" "/domains/shop.de.e2e.test/redirect" '{"to":"shop.at.e2e.test","code":301}'
Assert "redirect set (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = Visit "shop.de.e2e.test" "/preise/?x=1"
Assert "Caddy answers 301 on the redirecting domain" ($r.code -eq 301) "got $($r.code): $($r.head)"
Assert "Location keeps path and query" ((Header $r "Location") -eq "http://shop.at.e2e.test:$Port/preise/?x=1") "got $(Header $r 'Location')"
Assert "a permanent redirect is cached for an hour" ((Header $r "Cache-Control") -eq "public, max-age=3600") "got $(Header $r 'Cache-Control')"
Assert "the site's content is not on the redirect" ($r.body -notmatch "redirects-e2e-ok") "$($r.body)"
$r = Visit "shop.at.e2e.test"
Assert "the target serves the site" ($r.code -eq 200 -and $r.body -match "redirects-e2e-ok") "got $($r.code)"

$r = Api "PUT" "/domains/shop.de.e2e.test/redirect" '{"to":"shop.at.e2e.test","code":307}'
$r = Visit "shop.de.e2e.test" "/a"
Assert "307 answers 307, not cached" ($r.code -eq 307 -and (Header $r "Cache-Control") -eq "no-store") "got $($r.code) $(Header $r 'Cache-Control')"

$r = Api "PUT" "/domains/shop.at.e2e.test/redirect" '{"to":"shop.de.e2e.test"}'
Assert "a chain is refused (400)" ($r.code -eq 400) "got $($r.code): $($r.body)"

# ---------- the generated address ----------

Write-Host "== the generated address" -ForegroundColor Cyan
$r = Visit $viewHost
Assert "the generated address serves by default" ($r.code -eq 200 -and $r.body -match "redirects-e2e-ok") "got $($r.code)"
$r = Api "PUT" "/view-address" '{"mode":"off"}'
Assert "off set (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = Visit $viewHost
Assert "the generated address is off (404)" ($r.code -eq 404 -and $r.body -notmatch "redirects-e2e-ok") "got $($r.code)"
$r = Api "PUT" "/view-address" '{"mode":"redirect","to":"shop.at.e2e.test","code":308}'
$r = Visit $viewHost "/docs/"
Assert "the generated address redirects with 308" ($r.code -eq 308 -and (Header $r "Location") -eq "http://shop.at.e2e.test:$Port/docs/") "got $($r.code) $(Header $r 'Location')"
$r = Api "GET" ""
$meta = $null; if ($r.code -eq 200) { $meta = $r.body | ConvertFrom-Json }
Assert "the payload names the primary URL" ($null -ne $meta -and $meta.primary_url -eq "http://shop.at.e2e.test:$Port") "got $($meta.primary_url)"
Assert "the payload carries the rules" ($null -ne $meta -and $meta.view_address.mode -eq "redirect" -and $meta.domain_redirects.'shop.de.e2e.test'.to -eq "shop.at.e2e.test") "$($r.body)"

# ---------- removing the target ----------

Write-Host "== removing the target domain" -ForegroundColor Cyan
$r = Api "DELETE" "/domains/shop.at.e2e.test"
Assert "target removed (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = Visit "shop.de.e2e.test"
Assert "the former redirect now serves the site" ($r.code -eq 200 -and $r.body -match "redirects-e2e-ok") "got $($r.code)"
$r = Visit $viewHost
Assert "the generated address serves again" ($r.code -eq 200 -and $r.body -match "redirects-e2e-ok") "got $($r.code)"
$r = Visit "shop.at.e2e.test"
Assert "the removed domain serves nothing" ($r.code -ne 200) "got $($r.code)"

# ---------- the edit page ships the controls ----------

$r = Req "GET" "$origin/_sitebin/assets/static/edit.js"
Assert "the edit page script has the address controls" ($r.code -eq 200 -and $r.body -match "view-address" -and $r.body -match "/redirect") "got $($r.code)"

# ---------- cleanup ----------

if (-not $KeepUp) {
    docker rm -f $name 2>$null | Out-Null
    docker volume rm $vol 2>$null | Out-Null
}

Write-Host ""
$total = $script:pass + $script:fail
if ($total -ne $ExpectedAssertions) {
    Write-Host ("  FAIL assertion count: ran {0}, expected {1} - an assertion was skipped or threw" -f $total, $ExpectedAssertions) -ForegroundColor Red
    $script:fail++
}
Write-Host ("== redirects E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
