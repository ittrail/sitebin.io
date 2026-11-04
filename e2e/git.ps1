# Sitebin Git end-to-end suite -- exercises the real Docker image over HTTP.
#
#   powershell -File e2e\git.ps1 [-Image sitebin:dev] [-KeepUp]
#
# Grows with the site-git work; for now only the edit page's tab HTML.
param(
    [string]$Image = "sitebin:dev",
    [int]$Port = 8097,
    [switch]$KeepUp
)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$name = "sitebin-e2e-git"
$vol = "sitebin-e2e-git-data"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

$script:pass = 0
$script:fail = 0
function Assert([string]$name, [bool]$cond, [string]$detail = "") {
    if ($cond) { $script:pass++; Write-Host ("  ok   " + $name) -ForegroundColor Green }
    else { $script:fail++; Write-Host ("  FAIL " + $name + "  " + $detail) -ForegroundColor Red }
}

function Req([string]$method, [string]$url, [string[]]$curlArgs = @()) {
    $bodyFile = Join-Path $work "git-body.tmp"
    $args = @("-s", "-X", $method, "-o", $bodyFile, "-w", "%{http_code}", "--max-time", "30") + $curlArgs + @($url)
    $code = & curl.exe @args
    $body = ""
    if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    return @{ code = [int]$code; body = $body }
}

Write-Host "== starting $Image on port $Port" -ForegroundColor Cyan
docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" `
    -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_RATE_CREATE_PER_HOUR=1000" -e "SITEBIN_RATE_CREATE_BURST=200" `
    $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; exit 1 }

$up = $false
for ($i = 0; $i -lt 30; $i++) {
    Start-Sleep -Milliseconds 700
    $r = Req "GET" "$origin/"
    if ($r.code -eq 200) { $up = $true; break }
}
Assert "container serves the landing page" $up "container did not come up"
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; exit 1 }

# ---------- the edit page's tabs ----------

$idx = Join-Path $work "git-index.html"
[IO.File]::WriteAllText($idx, "<h1>git e2e</h1>")
$r = Req "POST" "$origin/api/sites" @("-F", "files=@$idx;filename=index.html")
Assert "site created" ($r.code -eq 201) "code $($r.code) $($r.body)"
$site = $r.body | ConvertFrom-Json
$editId = ($site.edit_url -split "/e/")[1]

$r = Req "GET" "$origin/e/$editId"
Assert "edit page serves" ($r.code -eq 200) "code $($r.code)"
Assert "edit page has the tab bar" ($r.body.Contains('id="tabs"'))
Assert "edit page has the Git panel" ($r.body.Contains('id="panel-git"'))
Assert "edit page keeps the Live panel" ($r.body.Contains('id="panel-live"'))

# ---------- teardown & summary ----------

if (-not $KeepUp) {
    docker rm -f $name 2>$null | Out-Null
    docker volume rm $vol 2>$null | Out-Null
}
Write-Host ""
Write-Host ("== Git E2E result: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
