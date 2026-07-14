# Sitebin — path-mode view access E2E (SITEBIN_VIEW_ACCESS=path).
# Serves sites at <base>/v/<view-id>/ instead of subdomains (no wildcard needed).
#   powershell -File e2e\paths.ps1 [-Image sitebin:dev]
param([string]$Image = "sitebin:dev", [int]$Port = 8089)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"; $origin = "http://${base}:$Port"
$name = "sitebin-paths-e2e"; $vol = "sitebin-paths-e2e-data"
$work = Join-Path $PSScriptRoot ".work"; $fixtures = Join-Path $PSScriptRoot "fixtures"
New-Item -ItemType Directory -Force $work | Out-Null

$script:pass = 0; $script:fail = 0
function Assert([string]$n, [bool]$c, [string]$d = "") {
    if ($c) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}
function Req([string]$m, [string]$u, [string[]]$x = @()) {
    $bf = Join-Path $work "b.tmp"; $hf = Join-Path $work "h.tmp"
    $code = & curl.exe @(@("-s", "-X", $m, "-o", $bf, "-D", $hf, "-w", "%{http_code}", "--max-time", "30") + $x + @($u))
    $body = ""; if (Test-Path $bf) { $body = [IO.File]::ReadAllText($bf) }
    $hdr = ""; if (Test-Path $hf) { $hdr = [IO.File]::ReadAllText($hf) }
    return @{ code = [int]$code; body = $body; headers = $hdr }
}

Write-Host "== starting $Image in path mode on $Port" -ForegroundColor Cyan
docker rm -f $name 2>$null | Out-Null; docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_VIEW_ACCESS=path" `
    -e "SITEBIN_RATE_CREATE_PER_HOUR=1000" -e "SITEBIN_RATE_CREATE_BURST=200" -e "SITEBIN_RATE_AUTH_PER_5MIN=200" `
    $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; exit 1 }

$up = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break } }
Assert "container up (path mode)" $up
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; exit 1 }

# create a webserver site
$idx = Join-Path $work "p-index.html"; [IO.File]::WriteAllText($idx, '<link rel="stylesheet" href="style.css"><h1>path-mode-ok</h1>')
$css = Join-Path $work "p-style.css"; [IO.File]::WriteAllText($css, "h1{color:green}")
$r = Req "POST" "$origin/api/sites" @("-F", "files=@$idx;filename=index.html", "-F", "files=@$css;filename=style.css")
Assert "create site" ($r.code -eq 201) "got $($r.code): $($r.body)"
$site = $null; if ($r.code -eq 201) { $site = $r.body | ConvertFrom-Json }

if ($site) {
    # view_url is now a /v/<id>/ path
    Assert "view_url is a /v/ path" ($site.view_url -match "/v/[a-z2-7]{26}/$") "got $($site.view_url)"
    $r = Req "GET" $site.view_url
    Assert "path site serves index" ($r.body -match "path-mode-ok") "code $($r.code)"
    $r = Req "GET" "$($site.view_url)style.css"
    Assert "path site serves relative asset" ($r.body -match "color:green") "code $($r.code)"
    # bare /v/<id> (no trailing slash) redirects to add it
    $bare = $site.view_url.TrimEnd('/')
    $r = Req "GET" $bare
    Assert "bare /v/<id> redirects to trailing slash" (($r.headers -match "308") -or ($r.body -match "path-mode-ok")) "code $($r.code)"
    # main domain routes still work (not shadowed by /v/)
    $r = Req "GET" "$origin/"
    Assert "main landing still served" ($r.body -match "Drop") "code $($r.code)"
}

# viewer mode over a path
$md = Join-Path $work "p-notes.md"; [IO.File]::WriteAllText($md, "# path viewer`n`nhello")
$r = Req "POST" "$origin/api/sites" @("-F", "mode=viewer", "-F", "files=@$md;filename=notes.md")
$vsite = $null; if ($r.code -eq 201) { $vsite = $r.body | ConvertFrom-Json }
Assert "create viewer site (path)" ($null -ne $vsite)
if ($vsite) {
    $r = Req "GET" $vsite.view_url
    Assert "viewer wrapper served at path" ($r.body -match "sitebin-viewer-wrapper") "code $($r.code)"
    $r = Req "GET" "$($vsite.view_url)_raw/notes.md"
    Assert "raw file served under the path" ($r.body -match "path viewer") "code $($r.code)"
}

# password gate over a path
$pidx = Join-Path $work "p-secret.html"; [IO.File]::WriteAllText($pidx, "<h1>path-secret</h1>")
$r = Req "POST" "$origin/api/sites" @("-F", "view_password=sesame", "-F", "files=@$pidx;filename=index.html")
$psite = $null; if ($r.code -eq 201) { $psite = $r.body | ConvertFrom-Json }
Assert "create protected site (path)" ($null -ne $psite)
if ($psite) {
    $r = Req "GET" $psite.view_url
    Assert "protected path site gates (401)" ($r.code -eq 401) "got $($r.code)"
    Assert "gate carries the site id (path mode)" ($r.body -match 'name="site"')

    $vid = ($psite.view_url -replace ".*/v/", "" -replace "/$", "")
    $jar = Join-Path $work "path-cookies.txt"; Remove-Item $jar -ErrorAction SilentlyContinue
    $r = Req "POST" "$origin/_sitebin/unlock" @("-c", $jar, "--data-urlencode", "password=sesame", "--data-urlencode", "redirect=/v/$vid/", "--data-urlencode", "site=$vid")
    Assert "unlock succeeds (303)" ($r.code -eq 303) "got $($r.code)"
    # cookie is scoped to the site's /v/<id> path (not the whole main domain)
    $jarText = Get-Content $jar -Raw -ErrorAction SilentlyContinue
    Assert "cookie scoped to the site path" (($jarText -match "sitebin_v") -and ($jarText -match "/v/$vid"))
    $r = Req "GET" $psite.view_url @("-b", $jar)
    Assert "cookie unlocks path content" ($r.body -match "path-secret") "code $($r.code)"
}

docker rm -f $name 2>$null | Out-Null; docker volume rm $vol 2>$null | Out-Null
Write-Host ""
Write-Host ("== path-mode E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
