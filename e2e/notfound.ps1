# Sitebin -- not-found pages E2E against a real image: what a visitor gets
# for an address that does not exist, through the real Caddy.
#
#   powershell -File e2e\notfound.ps1 [-Image sitebin:dev] [-Port 8196] [-SkipBuild] [-KeepUp]
#
# Community image (core). Build it with `docker build -t sitebin:dev .`, or
# omit -SkipBuild. See docs/superpowers/specs/2026-11-05-not-found-pages-design.md.
#
# What it proves:
#
#   no folder is ever listed: a folder without an index is a 404, with or
#   without its trailing slash
#   a 404 shows the site's own 404.html, or the page the owner named, or
#   Sitebin's page -- always status 404 and Cache-Control: no-store
#   a 404 carries the same security headers as the site's pages (trusted and
#   untrusted alike)
#   conditional and range requests still get the whole page; HEAD gets none
#   a client cannot choose the page itself
#   path views behave the same; a viewer site's /_raw/ is a 404, its files
#   are not; a password gate still answers first; SPA sites are unchanged
#   a web server site without a start page says so through the API
param(
    [string]$Image = "sitebin:dev",
    [int]$Port = 8196,
    [switch]$SkipBuild,
    [switch]$KeepUp
)

$ErrorActionPreference = "Continue"
$repo = Split-Path -Parent $PSScriptRoot
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$name = "sitebin-notfound-e2e"
$vol = "sitebin-notfound-e2e"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

# Every assertion must run; the count is checked at the end.
$ExpectedAssertions = 29

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
    $bodyFile = Join-Path $work "nf.tmp"
    $headFile = Join-Path $work "nf.head"
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

# NewSite creates a site from @{ path = content } and form fields, and returns
# the create response with .edit and .view filled in.
$script:fileSeq = 0
function NewSite([hashtable]$files, [hashtable]$fields = @{}) {
    $formArgs = @("-H", "Sec-Fetch-Site: same-origin")
    foreach ($k in $fields.Keys) { $formArgs += @("-F", "$k=$($fields[$k])") }
    foreach ($k in $files.Keys) {
        $script:fileSeq++
        $p = Join-Path $work "nf-$($script:fileSeq).bin"
        [IO.File]::WriteAllText($p, $files[$k])
        $formArgs += @("-F", "files=@${p};filename=$k")
    }
    $r = Req "POST" "$origin/api/sites" $formArgs
    if ($r.code -ne 201) { Write-Host "  create failed: $($r.code) $($r.body)" -ForegroundColor Red; return $null }
    $s = $r.body | ConvertFrom-Json
    $s | Add-Member -NotePropertyName edit -NotePropertyValue (($s.edit_url -split "/e/")[1])
    $s | Add-Member -NotePropertyName view -NotePropertyValue "http://$($s.id).${base}:$Port"
    return $s
}

if (-not $SkipBuild) {
    docker build -t $Image $repo
    if ($LASTEXITCODE -ne 0) { Write-Host "build failed" -ForegroundColor Red; exit 1 }
}

docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" -e "SITEBIN_VIEW_ACCESS=both" `
    -e "SITEBIN_RATE_AUTH_PER_5MIN=200" -e "SITEBIN_RATE_CREATE_PER_HOUR=200" $Image | Out-Null
$up = $false
for ($i = 0; $i -lt 40; $i++) {
    Start-Sleep -Milliseconds 700
    if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break }
}
Assert "container up" $up
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; exit 1 }

$own = "<h1>own-404</h1>"
$A = NewSite @{ "index.html" = "<h1>home-a</h1>"; "404.html" = $own; "assets/a.css" = "body{}" }
$B = NewSite @{ "index.html" = "<h1>home-b</h1>" }
$C = NewSite @{ "a.pdf" = "%PDF-1.4 not really" }
$D = NewSite @{ "index.html" = "<h1>home-d</h1>"; "404.html" = $own; "errors/gone.html" = "<h1>named-404</h1>" } @{ "not_found_page" = "errors/gone.html" }
$E = NewSite @{ "a.pdf" = "%PDF-1.4 not really" } @{ "mode" = "viewer" }
$F = NewSite @{ "index.html" = "<h1>home-f</h1>"; "404.html" = $own } @{ "view_password" = "sesame" }
$S = NewSite @{ "index.html" = "<h1>spa-home</h1>" } @{ "spa" = "true" }
# Community sites are trusted; B is made untrusted, as a free account's site is.
docker exec $name rm -f "/data/sites/$($B.id)/files/.sitebin-trusted" | Out-Null

Write-Host "== the site's own 404.html" -ForegroundColor Cyan
$r = Req "GET" "$($A.view)/missing"
Assert "a missing page shows the site's 404.html with 404" ($r.code -eq 404 -and $r.body -eq $own) "got $($r.code): $($r.body)"
Assert "it is never cached" ((Header $r "Cache-Control") -eq "no-store") "got '$(Header $r 'Cache-Control')'"
Assert "it is served as HTML" ((Header $r "Content-Type") -match "^text/html") "got '$(Header $r 'Content-Type')'"
$page = Req "GET" "$($A.view)/"
Assert "it carries the same CSP as the site's pages" ($page.code -eq 200 -and (Header $r "Content-Security-Policy") -eq (Header $page "Content-Security-Policy") -and (Header $r "Content-Security-Policy") -ne "") "404 '$(Header $r 'Content-Security-Policy')' vs page '$(Header $page 'Content-Security-Policy')'"

Write-Host "== no folder is listed" -ForegroundColor Cyan
$r = Req "GET" "$($A.view)/assets/"
Assert "a folder without an index is a 404 page" ($r.code -eq 404 -and $r.body -eq $own) "got $($r.code): $($r.body)"
$r = Req "GET" "$($A.view)/assets"
Assert "so is the folder without its slash" ($r.code -eq 404 -and $r.body -eq $own) "got $($r.code): $($r.body)"
$r = Req "GET" "$($A.view)/assets/a.css"
Assert "the folder's files are still served" ($r.code -eq 200) "got $($r.code)"

Write-Host "== Sitebin's page, untrusted" -ForegroundColor Cyan
$r = Req "GET" "$($B.view)/missing"
Assert "a site without a page of its own gets Sitebin's" ($r.code -eq 404 -and $r.body -match "Page not found") "got $($r.code): $($r.body.Substring(0, [Math]::Min(200, $r.body.Length)))"
Assert "Sitebin's page is never cached" ((Header $r "Cache-Control") -eq "no-store") "got '$(Header $r 'Cache-Control')'"
Assert "an untrusted site's 404 carries the untrusted policy" ((Header $r "Content-Security-Policy") -match "form-action 'none'") "got '$(Header $r 'Content-Security-Policy')'"
Assert "a trusted site's 404 does not" ((Header (Req "GET" "$($A.view)/missing") "Content-Security-Policy") -notmatch "form-action 'none'")

Write-Host "== a web server site without a start page" -ForegroundColor Cyan
Assert "creation warns and flags it" ($C.start_page_missing -eq $true -and (($C.warnings -join " ") -match "no index.html at its root")) "got start_page_missing=$($C.start_page_missing) warnings=$($C.warnings)"
$r = Req "GET" "$($C.view)/"
Assert "its root is a 404 page, not a list" ($r.code -eq 404 -and $r.body -match "Page not found" -and $r.body -notmatch "a\.pdf") "got $($r.code)"
Assert "its file is served" ((Req "GET" "$($C.view)/a.pdf").code -eq 200)

Write-Host "== a page the owner named" -ForegroundColor Cyan
$r = Req "GET" "$($D.view)/nothing/here"
Assert "the named page wins over 404.html" ($r.code -eq 404 -and $r.body -eq "<h1>named-404</h1>") "got $($r.code): $($r.body)"
$del = Req "DELETE" "$origin/api/sites/$($D.edit)/files/errors/gone.html" @("-H", "X-Edit-Password: $($D.edit_password)")
$r = Req "GET" "$($D.view)/nothing/here"
Assert "once it is gone, Sitebin's page answers" ($del.code -eq 200 -and $r.code -eq 404 -and $r.body -match "Page not found") "delete $($del.code), got $($r.code): $($r.body)"

Write-Host "== viewer, gate and SPA" -ForegroundColor Cyan
$r = Req "GET" "$($E.view)/_raw/"
Assert "a viewer site's _raw folder is a 404 page" ($r.code -eq 404 -and $r.body -match "Page not found") "got $($r.code)"
Assert "its files are still served" ((Req "GET" "$($E.view)/_raw/a.pdf").code -eq 200)
$r = Req "GET" "$($F.view)/missing"
Assert "a protected site answers the gate first" ($r.code -eq 401) "got $($r.code)"
$r = Req "GET" "$($S.view)/deep/link"
Assert "an SPA site still falls back to its index" ($r.code -eq 200 -and $r.body -match "spa-home") "got $($r.code)"

Write-Host "== conditional, range and HEAD requests" -ForegroundColor Cyan
$lastMod = Header (Req "GET" "$($A.view)/404.html") "Last-Modified"
# If-Modified-Since, not If-None-Match: PowerShell 5.1 drops the quotes of an
# ETag handed to a native executable.
$r = Req "GET" "$($A.view)/missing" @("-H", "If-Modified-Since: $lastMod")
Assert "a conditional request gets the whole page" ($r.code -eq 404 -and $r.body -eq $own) "got $($r.code): '$($r.body)'"
$r = Req "GET" "$($A.view)/missing" @("-H", "Range: bytes=0-3")
Assert "a range request gets the whole page" ($r.code -eq 404 -and $r.body -eq $own) "got $($r.code): '$($r.body)'"
# --head writes the headers where the body would go, so "no page" is the check.
$r = Req "HEAD" "$($A.view)/missing" @("--head")
Assert "HEAD gets the status and no page" ($r.code -eq 404 -and $r.body -notmatch "own-404" -and (Header $r "Cache-Control") -eq "no-store") "got $($r.code) '$(Header $r 'Cache-Control')': $($r.body)"

Write-Host "== a client cannot choose the page" -ForegroundColor Cyan
$r = Req "GET" "$($A.view)/missing" @("-H", "X-Sitebin-NotFound: /index.html")
Assert "a client's X-Sitebin-NotFound changes nothing" ($r.code -eq 404 -and $r.body -eq $own) "got $($r.code): $($r.body)"

Write-Host "== path views" -ForegroundColor Cyan
$r = Req "GET" "$origin/v/$($A.id)/missing"
Assert "a path view shows the site's 404.html" ($r.code -eq 404 -and $r.body -eq $own) "got $($r.code): $($r.body)"
$r = Req "GET" "$origin/v/$($B.id)/assets/"
Assert "a path view without one shows Sitebin's page" ($r.code -eq 404 -and $r.body -match "Page not found") "got $($r.code)"

Write-Host "== the API" -ForegroundColor Cyan
$r = Req "GET" "$origin/api/sites/$($A.edit)" @("-H", "X-Edit-Password: $($A.edit_password)")
$p = $null; if ($r.code -eq 200) { $p = $r.body | ConvertFrom-Json }
Assert "the payload names the page a 404 shows" ($p -and $p.not_found_serves -eq "404.html" -and $p.start_page_missing -eq $false) "got $($r.code): $($r.body)"
$r = Req "PUT" "$origin/api/sites/$($A.edit)" @("-H", "X-Edit-Password: $($A.edit_password)", "-H", "Content-Type: application/json", "--data", '{\"not_found_page\":\"x.txt\"}')
Assert "a page that is not HTML is refused" ($r.code -eq 400 -and $r.body -match "not_found_page") "got $($r.code): $($r.body)"

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
Write-Host ("== not-found E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
