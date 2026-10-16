# Sitebin -- browser caching E2E against a real image: the Cache-Control a
# visitor actually receives, through the real Caddy.
#
#   powershell -File e2e\caching.ps1 [-Image sitebin:e2e-caching] [-Port 8198] [-SkipBuild]
#
# Community image (caching is core). Build it with
# `docker build -t sitebin:e2e-caching .`, or omit -SkipBuild.
#
# What it proves:
#
#   auto: a page revalidates, a hashed bundle is immutable for a year, a
#         hand-named file revalidates, and a 404 carries no Cache-Control
#   a conditional request (304) carries the header too
#   a client cannot choose the header itself
#   a fixed time applies to files, not to pages; off sends nothing
#   the SPA fallback's index.html under a hashed name is never cached long
param(
    [string]$Image = "sitebin:e2e-caching",
    [int]$Port = 8198,
    [switch]$SkipBuild,
    [switch]$KeepUp
)

$ErrorActionPreference = "Continue"
$repo = Split-Path -Parent $PSScriptRoot
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$name = "sitebin-caching-e2e"
$vol = "sitebin-caching-e2e"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

# Every assertion must run; the count is checked at the end.
$ExpectedAssertions = 15

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

function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = Join-Path $work "ca.tmp"
    $headFile = Join-Path $work "ca.head"
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

$script:jsonSeq = 0
function JsonBody([string]$json) {
    $script:jsonSeq++
    $p = Join-Path $work "ca$($script:jsonSeq).json"
    [IO.File]::WriteAllText($p, $json)
    return "@$p"
}

if (-not $SkipBuild) {
    docker build -t $Image $repo
    if ($LASTEXITCODE -ne 0) { Write-Host "build failed" -ForegroundColor Red; exit 1 }
}

docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_RATE_AUTH_PER_5MIN=200" $Image | Out-Null
$up = $false
for ($i = 0; $i -lt 40; $i++) {
    Start-Sleep -Milliseconds 700
    if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break }
}
Assert "container up" $up
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; exit 1 }

# ---------- a site with a page, a hashed bundle and a hand-named file ----------

$files = @{ "index.html" = "<h1>caching-e2e</h1>"; "index-BqCz7v8e.js" = "console.log(1)"; "style.css" = "body{}" }
$formArgs = @("-H", "Sec-Fetch-Site: same-origin")
foreach ($k in $files.Keys) {
    $p = Join-Path $work "ca-$k"
    [IO.File]::WriteAllText($p, $files[$k])
    $formArgs += @("-F", "files=@${p};filename=$k")
}
$r = Req "POST" "$origin/api/sites" $formArgs
Assert "site created (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
$site = $null; if ($r.code -eq 201) { $site = $r.body | ConvertFrom-Json }
$edit = ($site.edit_url -split "/e/")[1]
$pw = $site.edit_password
$view = "http://$($site.id).${base}:$Port"
function Settings([string]$json) {
    return Req "PUT" "$origin/api/sites/$edit" @("-H", "X-Edit-Password: $pw", "-H", "Content-Type: application/json", "--data", (JsonBody $json))
}

Write-Host "== auto (the default)" -ForegroundColor Cyan
$r = Req "GET" "$view/"
Assert "the page revalidates" ((Header $r "Cache-Control") -eq "public, max-age=0, must-revalidate") "got '$(Header $r 'Cache-Control')'"
$r = Req "GET" "$view/index-BqCz7v8e.js"
Assert "the hashed bundle is immutable for a year" ((Header $r "Cache-Control") -eq "public, max-age=31536000, immutable") "got '$(Header $r 'Cache-Control')'"
# If-Modified-Since, not If-None-Match: PowerShell 5.1 drops the quotes of an
# ETag handed to a native executable.
$lastMod = Header $r "Last-Modified"
$r = Req "GET" "$view/index-BqCz7v8e.js" @("-H", "If-Modified-Since: $lastMod")
Assert "a 304 carries the header too" ($r.code -eq 304 -and (Header $r "Cache-Control") -eq "public, max-age=31536000, immutable") "got $($r.code) '$(Header $r 'Cache-Control')'"
$r = Req "GET" "$view/style.css"
Assert "a hand-named file revalidates" ((Header $r "Cache-Control") -eq "public, max-age=0, must-revalidate") "got '$(Header $r 'Cache-Control')'"
$r = Req "GET" "$view/missing-Zz9Yy8Xx.js"
Assert "a 404 carries no Cache-Control" ($r.code -eq 404 -and (Header $r "Cache-Control") -eq "") "got $($r.code) '$(Header $r 'Cache-Control')'"
$r = Req "GET" "$view/style.css" @("-H", "X-Sitebin-Cache: public, max-age=99999999")
Assert "a client cannot choose the header" ((Header $r "Cache-Control") -eq "public, max-age=0, must-revalidate") "got '$(Header $r 'Cache-Control')'"

Write-Host "== day" -ForegroundColor Cyan
$r = Settings '{"cache":"day"}'
Assert "policy set (200)" ($r.code -eq 200 -and $r.body -match '"cache":"day"') "got $($r.code): $($r.body)"
$r = Req "GET" "$view/style.css"
Assert "a file is cached for a day" ((Header $r "Cache-Control") -eq "public, max-age=86400") "got '$(Header $r 'Cache-Control')'"
$r = Req "GET" "$view/index.html"
Assert "a page still revalidates" ((Header $r "Cache-Control") -eq "public, max-age=0, must-revalidate") "got '$(Header $r 'Cache-Control')'"

Write-Host "== SPA fallback" -ForegroundColor Cyan
Settings '{"cache":"auto","spa_fallback":true}' | Out-Null
$r = Req "GET" "$view/assets/index-Aa1Bb2Cc.js"
Assert "the fallback's index.html under a hashed name is not cached long" ($r.code -eq 200 -and $r.body -match "caching-e2e" -and (Header $r "Cache-Control") -eq "public, max-age=0, must-revalidate") "got $($r.code) '$(Header $r 'Cache-Control')'"

Write-Host "== off" -ForegroundColor Cyan
Settings '{"cache":"off"}' | Out-Null
$r = Req "GET" "$view/index-BqCz7v8e.js"
Assert "off sends no Cache-Control" ($r.code -eq 200 -and (Header $r "Cache-Control") -eq "") "got $($r.code) '$(Header $r 'Cache-Control')'"

Write-Host "== a view password keeps shared caches out" -ForegroundColor Cyan
Settings '{"cache":"auto","view_password":"sesame"}' | Out-Null
$jar = Join-Path $work "ca.jar"
Remove-Item $jar -ErrorAction SilentlyContinue
Req "POST" "$view/_sitebin/unlock" @("-c", $jar, "--data", "password=sesame&redirect=%2F") | Out-Null
$r = Req "GET" "$view/index-BqCz7v8e.js" @("-b", $jar)
Assert "private, not public" ((Header $r "Cache-Control") -eq "private, max-age=31536000, immutable") "got $($r.code) '$(Header $r 'Cache-Control')'"
$r = Req "GET" "$view/index-BqCz7v8e.js"
Assert "the gate page keeps its own no-store" ($r.code -eq 401 -and (Header $r "Cache-Control") -eq "no-store") "got $($r.code) '$(Header $r 'Cache-Control')'"

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
Write-Host ("== caching E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
