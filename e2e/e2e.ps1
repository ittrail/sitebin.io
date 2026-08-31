# Sitebin end-to-end suite -- exercises the real Docker image over HTTP.
#
#   powershell -File e2e\e2e.ps1 [-Image sitebin:dev] [-KeepUp]
#
# Uses sitebin.localtest.me (public DNS that resolves to 127.0.0.1) so the
# wildcard-subdomain routing is tested for real, over the HTTP-only mode.
param(
    [string]$Image = "sitebin:dev",
    [int]$Port = 8085,
    [switch]$KeepUp
)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$name = "sitebin-e2e"
$vol = "sitebin-e2e-data"
$work = Join-Path $PSScriptRoot ".work"
$fixtures = Join-Path $PSScriptRoot "fixtures"
New-Item -ItemType Directory -Force $work | Out-Null

$script:pass = 0
$script:fail = 0
function Assert([string]$name, [bool]$cond, [string]$detail = "") {
    if ($cond) { $script:pass++; Write-Host ("  ok   " + $name) -ForegroundColor Green }
    else { $script:fail++; Write-Host ("  FAIL " + $name + "  " + $detail) -ForegroundColor Red }
}

# Req -> @{code=...; body=...; headers=...}
function Req([string]$method, [string]$url, [string[]]$curlArgs = @()) {
    $bodyFile = Join-Path $work "body.tmp"
    $hdrFile = Join-Path $work "hdr.tmp"
    $args = @("-s", "-X", $method, "-o", $bodyFile, "-D", $hdrFile, "-w", "%{http_code}", "--max-time", "30") + $curlArgs + @($url)
    $code = & curl.exe @args
    $body = ""
    if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    $headers = ""
    if (Test-Path $hdrFile) { $headers = [IO.File]::ReadAllText($hdrFile) }
    return @{ code = [int]$code; body = $body; headers = $headers }
}

function CreateSite([string[]]$formArgs) {
    $r = Req "POST" "$origin/api/sites" $formArgs
    if ($r.code -ne 201) { return $null }
    return $r.body | ConvertFrom-Json
}

# PowerShell 5.1 mangles quoted JSON passed to native executables; hand curl a
# file instead.
$script:jsonSeq = 0
function JsonBody([string]$json) {
    $script:jsonSeq++
    $p = Join-Path $work "json$($script:jsonSeq).json"
    [IO.File]::WriteAllText($p, $json)
    return "@$p"
}

function EditId($site) { return ($site.edit_url -split "/e/")[1] }

# ---------- fixture generation (binary formats built here, not in git) ----------

# 1x1 red PNG
$pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
[IO.File]::WriteAllBytes((Join-Path $work "logo.png"), [Convert]::FromBase64String($pngB64))

# minimal valid single-page PDF
$pdf = @"
%PDF-1.4
1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj
2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj
3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >> endobj
4 0 obj << /Length 60 >> stream
BT /F1 18 Tf 24 96 Td (sitebin e2e pdf fixture) Tj ET
endstream
endobj
5 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj
trailer << /Root 1 0 R /Size 6 >>
%%EOF
"@
[IO.File]::WriteAllText((Join-Path $work "sample.pdf"), $pdf)

# minimal DOCX (a zip with the required parts)
$docxDir = Join-Path $work "docx-src"
Remove-Item -Recurse -Force $docxDir -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force "$docxDir\_rels", "$docxDir\word" | Out-Null
[IO.File]::WriteAllText("$docxDir\[Content_Types].xml", @'
<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>
'@)
[IO.File]::WriteAllText("$docxDir\_rels\.rels", @'
<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>
'@)
[IO.File]::WriteAllText("$docxDir\word\document.xml", @'
<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body><w:p><w:r><w:t>sitebin e2e docx fixture</w:t></w:r></w:p></w:body>
</w:document>
'@)
$docxPath = Join-Path $work "sample.docx"
Remove-Item $docxPath -ErrorAction SilentlyContinue
Add-Type -AssemblyName System.IO.Compression.FileSystem
[IO.Compression.ZipFile]::CreateFromDirectory($docxDir, $docxPath)

# site.zip from the static fixture site
$zipPath = Join-Path $work "site.zip"
Remove-Item $zipPath -ErrorAction SilentlyContinue
[IO.Compression.ZipFile]::CreateFromDirectory((Join-Path $fixtures "site"), $zipPath)

# ---------- container lifecycle ----------

Write-Host "== starting $Image on port $Port" -ForegroundColor Cyan
docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" `
    -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_RATE_CREATE_PER_HOUR=1000" -e "SITEBIN_RATE_CREATE_BURST=200" `
    -e "SITEBIN_RATE_AUTH_PER_5MIN=200" `
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

# ---------- basics ----------

$r = Req "GET" "$origin/"
Assert "landing page has content" ($r.body -match "Drop") "body: $($r.body.Substring(0, [Math]::Min(120, $r.body.Length)))"
Assert "landing page sets CSP" ($r.headers -match "Content-Security-Policy")

$r = Req "GET" "$origin/_sitebin/assets/static/app.css"
Assert "shared assets served" ($r.code -eq 200)

$r = Req "GET" "$origin/internal/health"
Assert "internal endpoints NOT public" ($r.code -eq 404) "got $($r.code)"
$r = Req "GET" "$origin/internal/authz"
Assert "authz NOT public" ($r.code -eq 404) "got $($r.code)"

$who = docker exec $name whoami
Assert "container runs as non-root" ($who.Trim() -eq "sitebin") "whoami: $who"
$hc = docker exec $name sitebin healthcheck
Assert "healthcheck subcommand" ($hc -match "ok") "$hc"

# ---------- webserver mode ----------

$site = CreateSite @(
    "-F", "files=@$fixtures\site\index.html;filename=index.html",
    "-F", "files=@$fixtures\site\css\style.css;filename=css/style.css"
)
Assert "create webserver site" ($null -ne $site)
if ($site) {
    Assert "create returns edit password (22 chars)" ($site.edit_password.Length -eq 22)
    Assert "view_url shape" ($site.view_url -match "^http://[a-z2-7]{26}\.$([regex]::Escape($base)):$Port$")
    Assert "edit_url shape" ($site.edit_url -match "/e/[a-z2-7]{26}$")

    $r = Req "GET" "$($site.view_url)/"
    Assert "view: index.html served" ($r.body -match "sitebin-e2e-webserver-ok") "code $($r.code)"
    $r = Req "GET" "$($site.view_url)/css/style.css"
    Assert "view: nested asset served" ($r.body -match "sitebin-e2e-css-ok") "code $($r.code)"
    $r = Req "GET" "$($site.view_url)/nope.html"
    Assert "view: missing file 404" ($r.code -eq 404)

    # user content never on the main domain
    $r = Req "GET" "$origin/css/style.css"
    Assert "main domain never serves user files" ($r.code -eq 404)

    # edit API auth
    $edit = EditId $site
    $r = Req "GET" "$origin/api/sites/$edit"
    Assert "edit API requires password" ($r.code -eq 401)
    $r = Req "GET" "$origin/api/sites/$edit" @("-H", "X-Edit-Password: wrong")
    Assert "edit API rejects wrong password" ($r.code -eq 401)
    $r = Req "GET" "$origin/api/sites/$edit" @("-H", "X-Edit-Password: $($site.edit_password)")
    Assert "edit API accepts password" ($r.code -eq 200)
    Assert "settings never leak hashes" (-not ($r.body -match "argon2"))

    # upload + delete file via API
    $r = Req "POST" "$origin/api/sites/$edit/files" @("-H", "X-Edit-Password: $($site.edit_password)", "-F", "files=@$fixtures\sample.txt;filename=notes/readme.txt")
    Assert "file upload via API" ($r.code -eq 200)
    $r = Req "GET" "$($site.view_url)/notes/readme.txt"
    Assert "uploaded file served" ($r.body -match "sitebin-e2e-text-fixture")
    $r = Req "DELETE" "$origin/api/sites/$edit/files/notes/readme.txt" @("-H", "X-Edit-Password: $($site.edit_password)")
    Assert "file delete via API" ($r.code -eq 200)
    $r = Req "GET" "$($site.view_url)/notes/readme.txt"
    Assert "deleted file gone" ($r.code -eq 404)

    # traversal refused
    $r = Req "POST" "$origin/api/sites/$edit/files" @("-H", "X-Edit-Password: $($site.edit_password)", "-F", "files=@$fixtures\sample.txt;filename=../evil.txt")
    Assert "traversal upload refused" ($r.code -eq 400) "got $($r.code)"
}

# ---------- zip upload ----------

$zsite = CreateSite @("-F", "zip=@$zipPath;filename=site.zip")
Assert "create site from zip" ($null -ne $zsite)
if ($zsite) {
    $r = Req "GET" "$($zsite.view_url)/css/style.css"
    Assert "zip extracted with nested paths" ($r.body -match "sitebin-e2e-css-ok") "code $($r.code)"
}

# ---------- viewer mode ----------

$vsite = CreateSite @(
    "-F", "mode=viewer",
    "-F", "files=@$work\sample.pdf;filename=report.pdf",
    "-F", "files=@$fixtures\sample.md;filename=notes.md",
    "-F", "files=@$work\logo.png;filename=logo.png",
    "-F", "entry_file=report.pdf"
)
Assert "create viewer site" ($null -ne $vsite)
if ($vsite) {
    $r = Req "GET" "$($vsite.view_url)/"
    Assert "viewer wrapper served" ($r.body -match "sitebin-viewer-wrapper") "code $($r.code)"
    Assert "wrapper references shared assets" ($r.body -match "/_sitebin/assets/viewer/viewer.js")
    Assert "wrapper config lists files" ($r.body -match "notes.md" -and $r.body -match "report.pdf")
    $r = Req "GET" "$($vsite.view_url)/_raw/report.pdf"
    Assert "raw file fetchable" ($r.code -eq 200 -and $r.body -match "%PDF")
    $r = Req "GET" "$($vsite.view_url)/_sitebin/assets/vendor/pdf.min.mjs"
    Assert "viewer vendor assets on site origin" ($r.code -eq 200)

    # switch back to webserver restores plain serving
    $vedit = EditId $vsite
    $r = Req "PUT" "$origin/api/sites/$vedit" @("-H", "X-Edit-Password: $($vsite.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBody '{"mode":"webserver"}'))
    Assert "switch to webserver mode" ($r.code -eq 200) "got $($r.code): $($r.body)"
    $r = Req "GET" "$($vsite.view_url)/report.pdf"
    Assert "raw files restored at root" ($r.code -eq 200 -and $r.body -match "%PDF")
    $r = Req "PUT" "$origin/api/sites/$vedit" @("-H", "X-Edit-Password: $($vsite.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBody '{"mode":"viewer","entry_file":"notes.md"}'))
    Assert "switch back to viewer with new entry" ($r.code -eq 200) "got $($r.code): $($r.body)"
    $r = Req "GET" "$($vsite.view_url)/"
    Assert "wrapper regenerated for markdown entry" ($r.body -match '"entry":"notes.md"') "body: $($r.body.Substring(0,[Math]::Min(200,$r.body.Length)))"
}

# ---------- docx viewer ----------

$dsite = CreateSite @("-F", "mode=viewer", "-F", "files=@$docxPath;filename=brief.docx")
Assert "create docx viewer site" ($null -ne $dsite)
if ($dsite) {
    $r = Req "GET" "$($dsite.view_url)/"
    Assert "docx wrapper entry" ($r.body -match '"renderer":"docx"')
}

# ---------- view password gate ----------

$psite = CreateSite @("-F", "view_password=sesame", "-F", "files=@$fixtures\site\index.html;filename=index.html")
Assert "create protected site" ($null -ne $psite)
if ($psite) {
    $r = Req "GET" "$($psite.view_url)/"
    Assert "protected site gates with 401" ($r.code -eq 401) "got $($r.code)"
    Assert "gate page includes form" ($r.body -match "_sitebin/unlock")

    $r = Req "POST" "$($psite.view_url)/_sitebin/unlock" @("--data", "password=wrong&redirect=/")
    Assert "unlock rejects wrong password" ($r.code -eq 401)

    $cookieJar = Join-Path $work "cookies.txt"
    Remove-Item $cookieJar -ErrorAction SilentlyContinue
    $r = Req "POST" "$($psite.view_url)/_sitebin/unlock" @("--data", "password=sesame&redirect=/", "-c", $cookieJar)
    Assert "unlock accepts password (303)" ($r.code -eq 303) "got $($r.code)"
    Assert "unlock sets cookie" ((Get-Content $cookieJar -Raw -ErrorAction SilentlyContinue) -match "sitebin_v")
    $r = Req "GET" "$($psite.view_url)/" @("-b", $cookieJar)
    Assert "cookie unlocks content" ($r.body -match "sitebin-e2e-webserver-ok") "code $($r.code)"

    # gate must not be bypassable by spoofing X-Forwarded-Host at Caddy
    if ($site) {
        $r = Req "GET" "$($psite.view_url)/" @("-H", "X-Forwarded-Host: $(([uri]$site.view_url).Host)")
        Assert "spoofed X-Forwarded-Host cannot bypass gate" ($r.code -eq 401) "got $($r.code)"
    }
}

# ---------- expiry ----------

$past = (Get-Date).ToUniversalTime().AddHours(-2).ToString("yyyy-MM-ddTHH:mm:ssZ")
$xsite = CreateSite @("-F", "expires_at=$past", "-F", "files=@$fixtures\site\index.html;filename=index.html")
Assert "create pre-expired site" ($null -ne $xsite)
if ($xsite) {
    $r = Req "GET" "$($xsite.view_url)/"
    Assert "expired site serves 410" ($r.code -eq 410) "got $($r.code)"
}

# ---------- custom domains are an enterprise feature (gated in community) ----------

if ($site) {
    $edit = EditId $site
    $r = Req "POST" "$origin/api/sites/$edit/domains" @("-H", "X-Edit-Password: $($site.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBody '{"domain":"client.e2e.test"}'))
    Assert "custom domains gated in community (403)" ($r.code -eq 403) "got $($r.code): $($r.body)"
    # tls-check still guards the (empty) domain index
    $tls = docker exec $name sh -c "wget -q -O- 'http://127.0.0.1:9000/internal/tls-check?domain=client.e2e.test' >/dev/null 2>&1 && echo YES || echo NO"
    Assert "tls-check refuses unregistered domain" ($tls.Trim() -eq "NO") "$tls"
}

# ---------- WebDAV ----------

$wsite = CreateSite @("-F", "webdav=true", "-F", "files=@$fixtures\site\index.html;filename=index.html")
Assert "create webdav site" ($null -ne $wsite)
if ($wsite) {
    $wedit = EditId $wsite
    $davURL = "$origin/dav/$wedit/"
    $r = Req "PROPFIND" $davURL @("-u", "e2e:$($wsite.edit_password)", "-H", "Depth: 1")
    Assert "PROPFIND lists files (207)" ($r.code -eq 207 -and $r.body -match "index.html") "got $($r.code)"
    $r = Req "PUT" "${davURL}dav-upload.txt" @("-u", "e2e:$($wsite.edit_password)", "--data", "via-webdav")
    Assert "PUT creates file (201)" ($r.code -eq 201) "got $($r.code)"
    $r = Req "GET" "$($wsite.view_url)/dav-upload.txt"
    Assert "webdav file served on site" ($r.body -match "via-webdav")
    $r = Req "DELETE" "${davURL}dav-upload.txt" @("-u", "e2e:$($wsite.edit_password)")
    Assert "DELETE removes file (204)" ($r.code -eq 204) "got $($r.code)"
    $r = Req "PROPFIND" $davURL @()
    Assert "unauthenticated DAV challenged (401)" ($r.code -eq 401 -and $r.headers -match "WWW-Authenticate")
    $r = Req "PROPFIND" $davURL @("-u", "e2e:wrongpassword")
    Assert "wrong DAV password rejected" ($r.code -eq 401)

    # toggling off stops serving
    $r = Req "PUT" "$origin/api/sites/$wedit" @("-H", "X-Edit-Password: $($wsite.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBody '{"webdav_enabled":false}'))
    $r = Req "PROPFIND" $davURL @("-u", "e2e:$($wsite.edit_password)", "-H", "Depth: 1")
    Assert "disabled webdav returns 404" ($r.code -eq 404) "got $($r.code)"
}

# ---------- rate limits (separate container with tiny limits) ----------

# Derived from -Port rather than hardcoded, so a busy 8086 on the host is
# something you can move off with -Port instead of a mystery "did not start".
$rlPort = $Port + 1
Write-Host "== rate-limit container on port $rlPort" -ForegroundColor Cyan
docker rm -f sitebin-e2e-rl 2>$null | Out-Null
docker run -d --name sitebin-e2e-rl -p "${rlPort}:80" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$rlPort" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_RATE_CREATE_PER_HOUR=2" -e "SITEBIN_RATE_CREATE_BURST=2" `
    -e "SITEBIN_RATE_AUTH_PER_5MIN=2" $Image | Out-Null
$rlUp = $false
for ($i = 0; $i -lt 30; $i++) {
    Start-Sleep -Milliseconds 700
    if ((Req "GET" "http://${base}:$rlPort/").code -eq 200) { $rlUp = $true; break }
}
if ($rlUp) {
    $rl1 = Req "POST" "http://${base}:$rlPort/api/sites"
    $rl2 = Req "POST" "http://${base}:$rlPort/api/sites"
    $rl3 = Req "POST" "http://${base}:$rlPort/api/sites"
    Assert "create rate limit kicks in" ($rl1.code -eq 201 -and $rl2.code -eq 201 -and $rl3.code -eq 429) "codes: $($rl1.code) $($rl2.code) $($rl3.code)"
    $rlSite = $rl1.body | ConvertFrom-Json
    $rlEdit = EditId $rlSite
    $a1 = Req "GET" "http://${base}:$rlPort/api/sites/$rlEdit" @("-H", "X-Edit-Password: bad1")
    $a2 = Req "GET" "http://${base}:$rlPort/api/sites/$rlEdit" @("-H", "X-Edit-Password: bad2")
    $a3 = Req "GET" "http://${base}:$rlPort/api/sites/$rlEdit" @("-H", "X-Edit-Password: bad3")
    Assert "password attempts throttled (429)" ($a3.code -eq 429) "codes: $($a1.code) $($a2.code) $($a3.code)"
} else {
    Assert "rate-limit container up" $false "did not start"
}
docker rm -f sitebin-e2e-rl 2>$null | Out-Null

# ---------- persistence across container recreation ----------

if ($site) {
    Write-Host "== recreate container, same volume" -ForegroundColor Cyan
    docker rm -f $name | Out-Null
    docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
        -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
        -e "SITEBIN_RATE_CREATE_PER_HOUR=1000" -e "SITEBIN_RATE_CREATE_BURST=200" `
        -e "SITEBIN_RATE_AUTH_PER_5MIN=200" $Image | Out-Null
    $up2 = $false
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Milliseconds 700
        if ((Req "GET" "$origin/").code -eq 200) { $up2 = $true; break }
    }
    Assert "container restarts on existing volume" $up2
    $r = Req "GET" "$($site.view_url)/"
    Assert "sites survive recreation" ($r.body -match "sitebin-e2e-webserver-ok") "code $($r.code)"
}

# ---------- graceful shutdown ----------

docker stop -t 20 $name | Out-Null
$exit = docker inspect $name --format "{{.State.ExitCode}}"
Assert "graceful shutdown (exit 0 on SIGTERM)" ($exit.Trim() -eq "0") "exit code: $exit"

# ---------- teardown & summary ----------

if (-not $KeepUp) {
    docker rm -f $name 2>$null | Out-Null
    docker volume rm $vol 2>$null | Out-Null
} else {
    docker start $name | Out-Null
    Write-Host "== left running at $origin" -ForegroundColor Cyan
}

Write-Host ""
Write-Host ("== E2E result: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
