# Sitebin — FTP access E2E (SITEBIN_FTP_ENABLED=true).
# Login je Site: user = edit-UUID, pass = edit-password. Test via curl (FTP).
#   powershell -File e2e\ftp.ps1 [-Image sitebin:dev]
param([string]$Image = "sitebin:dev", [int]$Port = 8092, [int]$FtpPort = 2121)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"; $origin = "http://${base}:$Port"
$name = "sitebin-ftp-e2e"; $vol = "sitebin-ftp-e2e-data"
$work = Join-Path $PSScriptRoot ".work"; $fixtures = Join-Path $PSScriptRoot "fixtures"
New-Item -ItemType Directory -Force $work | Out-Null

$script:pass = 0; $script:fail = 0
function Assert([string]$n, [bool]$c, [string]$d = "") {
    if ($c) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}
function Http([string]$m, [string]$u, [string[]]$x = @()) {
    $bf = Join-Path $work "b.tmp"
    $code = & curl.exe @(@("-s", "-X", $m, "-o", $bf, "-w", "%{http_code}", "--max-time", "30") + $x + @($u))
    $body = ""; if (Test-Path $bf) { $body = [IO.File]::ReadAllText($bf) }
    return @{ code = [int]$code; body = $body }
}

Write-Host "== starting $Image with FTP on control port $FtpPort" -ForegroundColor Cyan
docker rm -f $name 2>$null | Out-Null; docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -v "${vol}:/data" `
    -p "${Port}:80" -p "${FtpPort}:${FtpPort}" -p "21000-21010:21000-21010" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_FTP_ENABLED=true" -e "SITEBIN_FTP_ADDR=:$FtpPort" `
    -e "SITEBIN_FTP_PUBLIC_HOST=127.0.0.1" `
    -e "SITEBIN_RATE_AUTH_PER_5MIN=200" `
    $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; exit 1 }

$up = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Http "GET" "$origin/").code -eq 200) { $up = $true; break } }
Assert "container up (ftp enabled)" $up
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; exit 1 }

# create a site with FTP enabled
$r = Http "POST" "$origin/api/sites" @("-F", "ftp=true", "-F", "files=@$fixtures\site\index.html;filename=index.html")
Assert "create site with ftp=true" ($r.code -eq 201) "got $($r.code): $($r.body)"
$site = $null; if ($r.code -eq 201) { $site = $r.body | ConvertFrom-Json }

if ($site) {
    $id = ($site.edit_url -split "/e/")[1]
    $pw = $site.edit_password
    Assert "response advertises ftp_url" ($site.ftp_url -match "^ftp://$id@")

    $ftp = "ftp://127.0.0.1:$FtpPort"

    # LIST (login = edit uuid + edit password)
    $list = & curl.exe -s --max-time 20 --user "${id}:${pw}" "$ftp/"
    Assert "FTP login + LIST shows files" ($list -match "index.html") "list: $list"

    # wrong password rejected
    & curl.exe -s -o NUL --max-time 20 --user "${id}:wrongpass" "$ftp/" 2>$null
    Assert "FTP wrong password rejected" ($LASTEXITCODE -ne 0)

    # upload a file via FTP, then verify it is served on the site
    $upl = Join-Path $work "ftp-upload.txt"; [IO.File]::WriteAllText($upl, "hello-via-ftp")
    & curl.exe -s --max-time 20 -T $upl --user "${id}:${pw}" "$ftp/notes.txt"
    Assert "FTP upload succeeds" ($LASTEXITCODE -eq 0)
    $r = Http "GET" "$($site.view_url)/notes.txt"
    Assert "uploaded file served on the site" ($r.body -match "hello-via-ftp") "code $($r.code)"

    # download via FTP
    $dl = (& curl.exe -s --max-time 20 --user "${id}:${pw}" "$ftp/index.html") -join "`n"
    Assert "FTP download returns content" ($dl -match "sitebin-e2e-webserver-ok") "dl: $dl"

    # session is confined to the site: reading outside must not leak host files
    $esc = (& curl.exe -s --max-time 20 --user "${id}:${pw}" "$ftp/../../../../etc/passwd" 2>$null) -join "`n"
    Assert "FTP session confined (no host-file access)" (-not ($esc -match "root:")) "esc: $esc"

    # a DIFFERENT (ftp-disabled) site cannot log in
    $r2 = Http "POST" "$origin/api/sites" @("-F", "files=@$fixtures\site\index.html;filename=index.html")
    $site2 = $r2.body | ConvertFrom-Json
    $id2 = ($site2.edit_url -split "/e/")[1]
    & curl.exe -s -o NUL --max-time 20 --user "${id2}:$($site2.edit_password)" "$ftp/" 2>$null
    Assert "FTP-disabled site cannot log in" ($LASTEXITCODE -ne 0)
}

docker rm -f $name 2>$null | Out-Null; docker volume rm $vol 2>$null | Out-Null
Write-Host ""
Write-Host ("== FTP E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
