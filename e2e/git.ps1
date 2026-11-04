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

# ---------- the Git tab ----------

Assert "panel skeleton carries the lifecycle sentence" ($r.body.Contains('This repository belongs to this site. Deleting the site deletes the repository.'))
Assert "panel skeleton has the file browser" ($r.body.Contains('id="git-tree"') -and $r.body.Contains('id="git-zip"'))
Assert "edit page loads the Git tab script" ($r.body.Contains('edit-git.js'))
$js = Req "GET" "$origin/_sitebin/assets/static/edit-git.js"
Assert "the Git tab script is served" ($js.code -eq 200) "code $($js.code)"

$hdr = @("-H", "X-Edit-Password: $($site.edit_password)")
$r = Req "GET" "$origin/api/sites/$editId/git" $hdr
Assert "git info before a push: exists false" ($r.code -eq 200 -and ($r.body | ConvertFrom-Json).exists -eq $false) "code $($r.code) $($r.body)"

$repo = Join-Path $work "git-tab-repo"
if (Test-Path $repo) { Remove-Item -Recurse -Force $repo }
New-Item -ItemType Directory -Force $repo | Out-Null
git -C $repo init -q -b main
git -C $repo config user.email "e2e@example.com"
git -C $repo config user.name "E2E"
[IO.File]::WriteAllText((Join-Path $repo "a.txt"), "hello")
git -C $repo add -A
git -C $repo commit -q -m "first"
$remote = "$($origin -replace '^http://', "http://$editId`:$($site.edit_password)@")/git/$editId.git"
git -C $repo push -q $remote main 2>$null
Assert "git push to the site succeeds" ($LASTEXITCODE -eq 0)

$r = Req "GET" "$origin/api/sites/$editId/git" $hdr
Assert "git info after a push: exists true" ($r.code -eq 200 -and ($r.body | ConvertFrom-Json).exists -eq $true) "code $($r.code) $($r.body)"
$r = Req "GET" "$origin/api/sites/$editId" $hdr
Assert "site payload carries git_bytes" ($r.code -eq 200 -and ($r.body | ConvertFrom-Json).usage.git_bytes -gt 0) "code $($r.code)"

# ---------- teardown & summary ----------

if (-not $KeepUp) {
    docker rm -f $name 2>$null | Out-Null
    docker volume rm $vol 2>$null | Out-Null
}
Write-Host ""
Write-Host ("== Git E2E result: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
