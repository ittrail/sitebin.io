# Sitebin — SPA fallback E2E. Verifies per-site index.html fallback for unknown paths.
#   powershell -File e2e\spa.ps1 [-Image sitebin:dev]
param([string]$Image = "sitebin:dev", [int]$Port = 8094)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"; $origin = "http://${base}:$Port"
$name = "sitebin-spa-e2e"; $vol = "sitebin-spa-e2e-data"
$work = Join-Path $PSScriptRoot ".work"; New-Item -ItemType Directory -Force $work | Out-Null

$script:pass = 0; $script:fail = 0
function Assert([string]$n, [bool]$c, [string]$d = "") {
    if ($c) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}
function Req([string]$m, [string]$u, [string[]]$x = @()) {
    $bf = Join-Path $work "b.tmp"
    $code = & curl.exe @(@("-s", "-X", $m, "-o", $bf, "-w", "%{http_code}", "--max-time", "30") + $x + @($u))
    $body = ""; if (Test-Path $bf) { $body = [IO.File]::ReadAllText($bf) }
    return @{ code = [int]$code; body = $body }
}
function JsonBody([string]$json) { $p = Join-Path $work ("j" + (Get-Random) + ".json"); [IO.File]::WriteAllText($p, $json); return "@$p" }

docker rm -f $name 2>$null | Out-Null; docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_RATE_CREATE_PER_HOUR=1000" -e "SITEBIN_RATE_CREATE_BURST=200" `
    $Image | Out-Null
$up = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break } }
Assert "container up (Caddyfile valid with SPA routes)" $up
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; exit 1 }

$idx = Join-Path $work "spa-index.html"; [IO.File]::WriteAllText($idx, '<link rel="stylesheet" href="app.css"><h1>spa-app-ok</h1>')
$css = Join-Path $work "spa-app.css"; [IO.File]::WriteAllText($css, "h1{color:teal}")

# --- SPA site ---
$r = Req "POST" "$origin/api/sites" @("-F", "files=@$idx;filename=index.html", "-F", "files=@$css;filename=app.css")
$site = if ($r.code -eq 201) { $r.body | ConvertFrom-Json } else { $null }
Assert "create site" ($null -ne $site)
if ($site) {
    $edit = ($site.edit_url -split "/e/")[1]
    $r = Req "PUT" "$origin/api/sites/$edit" @("-H", "X-Edit-Password: $($site.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBody '{"spa_fallback":true}'))
    Assert "enable SPA fallback" ($r.code -eq 200) "got $($r.code): $($r.body)"

    $r = Req "GET" "$($site.view_url)/deep/client/route"
    Assert "unknown path serves index.html (SPA)" ($r.body -match "spa-app-ok") "code $($r.code)"
    $r = Req "GET" "$($site.view_url)/"
    Assert "root still serves index" ($r.body -match "spa-app-ok")
    $r = Req "GET" "$($site.view_url)/app.css"
    Assert "real asset still served (not shadowed)" ($r.body -match "color:teal") "code $($r.code)"
    # marker not exposed
    $r = Req "GET" "$($site.view_url)/.sitebin-spa"
    Assert "SPA marker hidden" ($r.code -ne 200) "code $($r.code)"

    # turning it off restores 404 for unknown paths
    $r = Req "PUT" "$origin/api/sites/$edit" @("-H", "X-Edit-Password: $($site.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBody '{"spa_fallback":false}'))
    $r = Req "GET" "$($site.view_url)/deep/client/route"
    Assert "SPA off -> unknown path 404" ($r.code -eq 404) "code $($r.code)"
}

# --- non-SPA site: unknown path is a normal 404 ---
$r = Req "POST" "$origin/api/sites" @("-F", "files=@$idx;filename=index.html")
$plain = if ($r.code -eq 201) { $r.body | ConvertFrom-Json } else { $null }
if ($plain) {
    $r = Req "GET" "$($plain.view_url)/nope/here"
    Assert "non-SPA site unknown path 404" ($r.code -eq 404) "code $($r.code)"
}

docker rm -f $name 2>$null | Out-Null; docker volume rm $vol 2>$null | Out-Null
Write-Host ""
Write-Host ("== SPA E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
