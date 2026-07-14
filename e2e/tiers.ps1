# Sitebin Enterprise — tiers/quota E2E against the real enterprise image.
#   powershell -File e2e\tiers.ps1 [-Image sitebin:dev-ee]
param([string]$Image = "sitebin:dev-ee", [int]$Port = 8088)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"; $origin = "http://${base}:$Port"
$name = "sitebin-tiers-e2e"; $vol = "sitebin-tiers-e2e-data"
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
# PS 5.1: quoted JSON to curl via a temp file
function JsonBodyT([string]$json) {
    $p = Join-Path $work ("jt" + (Get-Random) + ".json"); [IO.File]::WriteAllText($p, $json); return "@$p"
}

# free tier: 1 site max, 200-byte storage cap, no webdav, 0 custom domains.
# Written to a mounted file to avoid shell JSON-quoting issues (this is the
# documented production approach via SITEBIN_TIERS_FILE).
$tiers = '[{"id":"free","label":"Free","max_site_bytes":200,"max_files":5,"max_sites":1,"webdav":false,"custom_domains":0,"max_expiry_days":7}]'
[IO.File]::WriteAllText((Join-Path $work "tiers.json"), $tiers)
$workDocker = ($work -replace '\\', '/')

Write-Host "== starting enterprise image (tiers mode) on $Port" -ForegroundColor Cyan
docker rm -f $name 2>$null | Out-Null; docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" -v "${workDocker}:/cfg:ro" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_ACCOUNT_MODE=tiers" -e "SITEBIN_TIERS_FILE=/cfg/tiers.json" -e "SITEBIN_DEFAULT_TIER=free" `
    -e "SITEBIN_RATE_AUTH_PER_5MIN=200" $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; exit 1 }

$up = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break } }
Assert "container up (tiers mode)" $up
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; exit 1 }

$jar = Join-Path $work "tiers-cookies.txt"; Remove-Item $jar -ErrorAction SilentlyContinue
Req "POST" "$origin/account/signup" @("-c", $jar, "--data-urlencode", "email=t@example.com", "--data-urlencode", "password=password12345") | Out-Null

# small file within cap → ok
$small = Join-Path $work "small.txt"; [IO.File]::WriteAllText($small, "hello")
$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "files=@$small;filename=index.html")
Assert "first site within tier allowed" ($r.code -eq 201) "got $($r.code): $($r.body)"
$site = $null; if ($r.code -eq 201) { $site = $r.body | ConvertFrom-Json }

# usage max reflects the tier cap, not the global 100MB
if ($site) {
    $edit = ($site.edit_url -split "/e/")[1]
    $r = Req "GET" "$origin/api/sites/$edit" @("-H", "X-Edit-Password: $($site.edit_password)")
    $meta = $r.body | ConvertFrom-Json
    Assert "usage cap = tier cap (200 B)" ($meta.usage.max_bytes -eq 200) "got $($meta.usage.max_bytes)"

    # webdav is not available on the free tier
    $r = Req "PUT" "$origin/api/sites/$edit" @("-H", "X-Edit-Password: $($site.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBodyT '{"webdav_enabled":true}'))
    Assert "webdav blocked on free tier (403)" ($r.code -eq 403) "got $($r.code)"

    # custom domain blocked (cap 0)
    $r = Req "POST" "$origin/api/sites/$edit/domains" @("-H", "X-Edit-Password: $($site.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBodyT '{"domain":"x.example.org"}'))
    Assert "custom domain blocked on free tier" ($r.code -ge 400) "got $($r.code)"
}

# second site → blocked by max_sites=1
$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "files=@$small;filename=index.html")
Assert "second site blocked (max_sites, 403)" ($r.code -eq 403) "got $($r.code): $($r.body)"

# oversized upload → 413 (exceeds 200-byte tier cap)
$big = Join-Path $work "big.txt"; [IO.File]::WriteAllText($big, ("A" * 500))
$jar2 = Join-Path $work "tiers2.txt"; Remove-Item $jar2 -ErrorAction SilentlyContinue
Req "POST" "$origin/account/signup" @("-c", $jar2, "--data-urlencode", "email=t2@example.com", "--data-urlencode", "password=password12345") | Out-Null
$r = Req "POST" "$origin/api/sites" @("-b", $jar2, "-F", "files=@$big;filename=index.html")
Assert "oversized upload rejected (413)" ($r.code -eq 413) "got $($r.code): $($r.body)"

docker rm -f $name 2>$null | Out-Null; docker volume rm $vol 2>$null | Out-Null
Write-Host ""
Write-Host ("== tiers E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
