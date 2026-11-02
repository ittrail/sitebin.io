# Sitebin Enterprise -- custom domains with a path, E2E against the real
# enterprise image: the whole chain through the real Caddy.
#
#   powershell -File e2e\mounts.ps1 [-Image sitebin:dev-ee] [-Port 8199] [-NoContainers] [-KeepUp]
#
# Build the image with `docker build --build-arg EDITION=enterprise -t
# sitebin:dev-ee .`. Domain verification is off: this suite is about what an
# address with a path answers, not about DNS. Without -NoContainers it also
# drives the host's Docker Engine (like containers.ps1) and pulls
# alpine-node-22 on the first run.
#
# What it proves, by asking Caddy rather than the backend:
#
#   one host serves several sites of one account, each under its own path,
#   the longest path winning on segment boundaries; a file site under a path
#   is served from its own folder with the prefix stripped, and its bare
#   prefix redirects to the slash form; a file viewer works under a path
#   a view password under a path holds against path spellings, and its unlock
#   form and cookie live under the path
#   client-sent internal headers change nothing
#   an address with a slash is removed through the API
#   a host with only paths has no root
#   a container port under a path gets the full path and X-Forwarded-Prefix,
#   its root port keeps /foo/_sitebin/x, and a static site mounted beside it
#   is served by Sitebin, not by the app
param(
    [string]$Image = "sitebin:dev-ee",
    [int]$Port = 8199,
    [switch]$NoContainers,
    [switch]$KeepUp
)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$name = "sitebin-mounts-e2e"
$vol = "sitebin-mounts-e2e"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

# Every assertion must run; the count is checked at the end. Update it when
# you add or remove one.
$ExpectedAssertions = 28
if (-not $NoContainers) { $ExpectedAssertions += 8 }

$script:pass = 0; $script:fail = 0
function Assert([string]$n, $c, [string]$d = "") {
    $ok = $false
    if ($null -ne $c) {
        if ($c -is [bool]) { $ok = $c }
        else { $ok = [bool]$c }
    }
    if ($ok) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}

# Req never follows redirects, and also returns the response headers.
function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = Join-Path $work "mt.tmp"
    $headFile = Join-Path $work "mt.head"
    Remove-Item $bodyFile, $headFile -ErrorAction SilentlyContinue
    $a = @("-s", "--path-as-is", "-X", $method, "-o", $bodyFile, "-D", $headFile, "-w", "%{http_code}", "--max-time", "60") + $extra + @($url)
    $code = & curl.exe @a
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
    $p = Join-Path $work "mt$($script:jsonSeq).json"
    [IO.File]::WriteAllText($p, $json)
    return "@$p"
}

function Visit([string]$hostName, [string]$path = "/", [string[]]$extra = @()) {
    return Req "GET" "http://127.0.0.1:$Port$path" (@("-H", "Host: $hostName") + $extra)
}

function Cleanup {
    docker rm -f $name 2>$null | Out-Null
    $ids = docker ps -aq --filter "label=io.sitebin.managed=true"
    if ($ids) { docker rm -f $ids 2>$null | Out-Null }
    $nets = docker network ls -q --filter "label=io.sitebin.managed=true"
    if ($nets) { docker network rm $nets 2>$null | Out-Null }
    docker volume rm $vol 2>$null | Out-Null
}

$tiers = '[{"id":"pro","label":"Pro","max_site_bytes":1073741824,"max_files":5000,"max_sites":20,"webdav":true,"custom_domains":10,"max_expiry_days":0,"max_containers":3,"trusted":true}]'
[IO.File]::WriteAllText((Join-Path $work "mt-tiers.json"), $tiers)
$workDocker = ($work -replace '\\', '/')

Cleanup
$a = @("run", "-d", "--name", $name, "-p", "${Port}:80", "-v", "${vol}:/data", "-v", "${workDocker}:/cfg:ro",
    "-e", "SITEBIN_BASE_DOMAIN=${base}:$Port", "-e", "SITEBIN_HTTP_ONLY=true",
    "-e", "SITEBIN_ACCOUNT_MODE=tiers", "-e", "SITEBIN_TIERS_FILE=/cfg/mt-tiers.json", "-e", "SITEBIN_DEFAULT_TIER=pro",
    "-e", "SITEBIN_RATE_AUTH_PER_5MIN=500", "-e", "SITEBIN_DOMAIN_VERIFICATION=off")
if (-not $NoContainers) {
    $a += @("-v", "/var/run/docker.sock:/var/run/docker.sock", "--group-add", "0", "-e", "SITEBIN_CONTAINERS=docker")
}
$a += @($Image)
& docker @a | Out-Null
$up = $false
for ($i = 0; $i -lt 40; $i++) {
    Start-Sleep -Milliseconds 700
    if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break }
}
Assert "container up" $up
if (-not $up) { docker logs $name; Cleanup; exit 1 }

# ---------- one account, several sites ----------

$jar = Join-Path $work "mt.jar"
Remove-Item $jar -ErrorAction SilentlyContinue
Req "POST" "$origin/account/signup" @("-c", $jar,
    "--data-urlencode", "email=mounts@example.com",
    "--data-urlencode", "password=password12345") | Out-Null

function NewSite([hashtable]$files, [string[]]$fields = @()) {
    $x = @("-b", $jar) + $fields
    foreach ($k in $files.Keys) {
        $p = Join-Path $work ("mt-" + ($k -replace '[/\\]', '_'))
        [IO.File]::WriteAllText($p, $files[$k])
        $x += @("-F", "files=@${p};filename=$k")
    }
    $r = Req "POST" "$origin/api/sites" $x
    if ($r.code -ne 201) { Write-Host "create failed: $($r.code) $($r.body)" -ForegroundColor Red; return $null }
    $s = $r.body | ConvertFrom-Json
    return @{ id = $s.id; edit = ($s.edit_url -split "/e/")[1]; pw = $s.edit_password }
}
function Api($site, [string]$method, [string]$path, [string]$json = "") {
    $extra = @("-H", "X-Edit-Password: $($site.pw)")
    if ($json -ne "") { $extra += @("-H", "Content-Type: application/json", "--data", (JsonBody $json)) }
    return Req $method "$origin/api/sites/$($site.edit)$path" $extra
}
function Attach($site, [string]$addr, [string]$target = "") {
    $j = '{"domain":"' + $addr + '"'
    if ($target -ne "") { $j += ',"target":"' + $target + '"' }
    $j += '}'
    return Api $site "POST" "/domains" $j
}

$root = NewSite @{ "index.html" = "<h1>root-ok</h1>" }
$k1 = NewSite @{ "index.html" = '<link rel="stylesheet" href="/demos/k1/app.css"><h1>k1-ok</h1>'; "app.css" = "k1-css" } @("-F", "view_password=sesame")
$files = NewSite @{ "sub/readme.txt" = "readme-ok" } @("-F", "mode=viewer")
$warn = NewSite @{ "index.html" = '<link rel="stylesheet" href="/_astro/x.css">' }
$only = NewSite @{ "index.html" = "only-a" }

Write-Host "== attaching m.e2e.test and paths under it" -ForegroundColor Cyan
$r = Attach $root "m.e2e.test"
Assert "the root site takes the host (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = Attach $k1 "m.e2e.test/demos/k1"
Assert "a second site of the account takes a path on it (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = Attach $files "m.e2e.test/files"
Assert "a file viewer takes another path (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = Attach $warn "m.e2e.test/warn"
$p = $null; if ($r.code -eq 200) { $p = $r.body | ConvertFrom-Json }
Assert "a site built for the root is warned about under a path" ($null -ne $p -and "$($p.domain_warnings.'m.e2e.test/warn')" -match "/_astro/x.css") "got $($r.code): $($r.body)"

Write-Host "== serving" -ForegroundColor Cyan
$r = Visit "m.e2e.test" "/"
Assert "the root serves the root site" ($r.code -eq 200 -and $r.body -match "root-ok") "got $($r.code)"
$r = Visit "m.e2e.test" "/files/"
Assert "the file viewer serves its page under its path" ($r.code -eq 200 -and $r.body -match "_sitebin/assets/viewer") "got $($r.code)"
$r = Visit "m.e2e.test" "/files/_raw/sub/readme.txt"
Assert "the file viewer's raw files are under its path" ($r.code -eq 200 -and $r.body -eq "readme-ok") "got $($r.code): $($r.body)"
$r = Visit "m.e2e.test" "/files"
Assert "a file site's bare prefix redirects to the slash form" ($r.code -eq 308 -and (Header $r "Location") -eq "/files/") "got $($r.code) $(Header $r 'Location')"
$r = Visit "m.e2e.test" "/demos/k10"
Assert "a sibling path belongs to the root site" ($r.code -eq 404 -and $r.body -notmatch "k1-ok") "got $($r.code)"

Write-Host "== a view password under a path" -ForegroundColor Cyan
$r = Visit "m.e2e.test" "/demos/k1/"
Assert "the protected path shows the gate" ($r.code -eq 401 -and $r.body -match 'action="/demos/k1/_sitebin/unlock"') "got $($r.code)"
foreach ($spell in @("/demos/k1/app.css", "/demos/k1/%2e%2e/k1/app.css", "/demos//k1/app.css", "/x/../demos/k1/app.css", "/demos/%6b1/app.css")) {
    $r = Visit "m.e2e.test" $spell
    Assert "no spelling serves the protected file: $spell" ($r.body -notmatch "k1-css") "got $($r.code): $($r.body)"
}
$r = Visit "m.e2e.test" "/app.css" @("-H", "X-Sitebin-Site: $($k1.id)", "-H", "X-Sitebin-Strip: /demos/k1")
Assert "client-sent folder and prefix headers change nothing" ($r.body -notmatch "k1-css") "got $($r.code): $($r.body)"
$vjar = Join-Path $work "mt-view.jar"
Remove-Item $vjar -ErrorAction SilentlyContinue
$r = Req "POST" "http://127.0.0.1:$Port/demos/k1/_sitebin/unlock" @("-H", "Host: m.e2e.test", "-c", $vjar,
    "--data-urlencode", "password=sesame", "--data-urlencode", "redirect=/demos/k1/")
Assert "the unlock under the path accepts the password" ($r.code -eq 303 -and (Header $r "Location") -eq "/demos/k1/") "got $($r.code) $(Header $r 'Location')"
Assert "the view cookie is scoped to the path" ((Header $r "Set-Cookie") -match "Path=/demos/k1/") "got $(Header $r 'Set-Cookie')"
$r = Visit "m.e2e.test" "/demos/k1/" @("-b", $vjar)
Assert "with the cookie the site under the path is served" ($r.code -eq 200 -and $r.body -match "k1-ok") "got $($r.code)"
$r = Visit "m.e2e.test" "/demos/k1/app.css" @("-b", $vjar)
Assert "and its own files, from its own folder" ($r.code -eq 200 -and $r.body -eq "k1-css") "got $($r.code): $($r.body)"

Write-Host "== a host with only paths" -ForegroundColor Cyan
$r = Attach $only "p.e2e.test/a"
Assert "a path on a fresh host (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = Visit "p.e2e.test" "/"
Assert "the host's empty root answers 404" ($r.code -eq 404) "got $($r.code)"
$r = Visit "p.e2e.test" "/a/"
Assert "its path serves" ($r.code -eq 200 -and $r.body -eq "only-a") "got $($r.code): $($r.body)"

Write-Host "== removing an address with a slash" -ForegroundColor Cyan
$r = Api $k1 "DELETE" "/domains/m.e2e.test%2Fdemos%2Fk1"
Assert "removed through Caddy and the API (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
$r = Visit "m.e2e.test" "/demos/k1/" @("-b", $vjar)
Assert "the path then belongs to the root site again" ($r.body -notmatch "k1-ok") "got $($r.code)"
$r = Visit "m.e2e.test" "/"
Assert "the root site still serves" ($r.code -eq 200 -and $r.body -match "root-ok") "got $($r.code)"

$r = Req "GET" "$origin/_sitebin/assets/static/edit.js"
Assert "the edit page script knows addresses held through the host" ($r.code -eq 200 -and $r.body -match "domain_proofs") "got $($r.code)"

# ---------- a container port under a path ----------

if (-not $NoContainers) {
    Write-Host "== a container under a path (pulls alpine-node-22 on the first run)" -ForegroundColor Cyan
    $compose = @'
services:
  web:
    image: alpine-node-22
    command: >-
      node -e "for (const p of [3000, 3001]) require('http').createServer((q, s) =>
      s.end('port=' + p + ' url=' + q.url + ' prefix=' + (q.headers['x-forwarded-prefix'] || '-'))).listen(p)"
    http: [3000, 3001]
'@
    $ct = NewSite @{ "sitebin-container-compose.yaml" = $compose } @("-F", "mode=container")
    $deadline = (Get-Date).AddSeconds(600)
    $running = $false
    while ((Get-Date) -lt $deadline) {
        $r = Api $ct "GET" ""
        if ($r.code -eq 200) {
            $s = $r.body | ConvertFrom-Json
            if ($s.container.status -eq "running" -and @($s.addresses).Count -eq 2) { $running = $true; break }
            if ($s.container.status -eq "error") { break }
        }
        Start-Sleep -Seconds 3
    }
    Assert "the echo app runs" $running "status: $($r.body)"
    $r = Attach $ct "c.e2e.test" "web:3000"
    $r2 = Attach $ct "c.e2e.test/api" "web:3001"
    Assert "the app takes the host and a path on it (200, 200)" ($r.code -eq 200 -and $r2.code -eq 200) "got $($r.code) $($r2.code): $($r2.body)"
    $static = NewSite @{ "index.html" = "beside-the-app" }
    $r = Attach $static "c.e2e.test/demos"
    Assert "a static site takes a path beside the app (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"

    $r = $null
    $deadline = (Get-Date).AddSeconds(120)
    while ((Get-Date) -lt $deadline) {
        $r = Visit "c.e2e.test" "/api/x?y=1"
        if ($r.code -eq 200) { break }
        Start-Sleep -Seconds 2
    }
    Assert "the port under a path gets the full path and its prefix" ($r.code -eq 200 -and $r.body -eq "port=3001 url=/api/x?y=1 prefix=/api") "got $($r.code): $($r.body)"
    $r = Visit "c.e2e.test" "/x"
    Assert "the root port gets the rest, with no prefix" ($r.code -eq 200 -and $r.body -eq "port=3000 url=/x prefix=-") "got $($r.code): $($r.body)"
    $r = Visit "c.e2e.test" "/foo/_sitebin/x"
    Assert "a nested _sitebin path at the root is the app's" ($r.code -eq 200 -and $r.body -eq "port=3000 url=/foo/_sitebin/x prefix=-") "got $($r.code): $($r.body)"
    $r = Visit "c.e2e.test" "/x" @("-H", "X-Forwarded-Prefix: /evil")
    Assert "a client-sent X-Forwarded-Prefix never reaches the app" ($r.code -eq 200 -and $r.body -eq "port=3000 url=/x prefix=-") "got $($r.code): $($r.body)"
    $r = Visit "c.e2e.test" "/demos/"
    Assert "the static site beside the app is served by Sitebin" ($r.code -eq 200 -and $r.body -eq "beside-the-app") "got $($r.code): $($r.body)"
}

# ---------- cleanup ----------

if (-not $KeepUp) { Cleanup }

Write-Host ""
$total = $script:pass + $script:fail
if ($total -ne $ExpectedAssertions) {
    Write-Host ("  FAIL assertion count: ran {0}, expected {1} - an assertion was skipped or threw" -f $total, $ExpectedAssertions) -ForegroundColor Red
    $script:fail++
}
Write-Host ("== mounts E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
