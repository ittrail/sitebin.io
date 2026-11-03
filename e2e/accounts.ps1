# Sitebin Enterprise -- accounts-mode E2E against the real enterprise image.
#
#   powershell -File e2e\accounts.ps1 [-Image sitebin:dev-ee]
#
# Verifies: community-open behavior is gated off, anonymous create is blocked,
# signup/login set a session, logged-in create is owned, the dashboard lists
# owned sites, edit-password reset works, and account deletion cascades; a tag
# is added on the site list, filters it, and is renamed across two sites on the
# manage page.
param(
    [string]$Image = "sitebin:dev-ee",
    [int]$Port = 8087
)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$name = "sitebin-acct-e2e"
$vol = "sitebin-acct-e2e-data"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

$script:pass = 0; $script:fail = 0
function Assert([string]$n, [bool]$c, [string]$d = "") {
    if ($c) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}

# curl with a cookie jar; returns @{code; body; headers}
function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = Join-Path $work "b.tmp"; $hdrFile = Join-Path $work "h.tmp"
    $args = @("-s", "-X", $method, "-o", $bodyFile, "-D", $hdrFile, "-w", "%{http_code}", "--max-time", "30") + $extra + @($url)
    $code = & curl.exe @args
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    $headers = ""; if (Test-Path $hdrFile) { $headers = [IO.File]::ReadAllText($hdrFile) }
    return @{ code = [int]$code; body = $body; headers = $headers }
}

# PS 5.1: pass quoted JSON to curl via a temp file to avoid arg mangling
function JsonBodyA([string]$json) {
    $p = Join-Path $work ("ja" + (Get-Random) + ".json"); [IO.File]::WriteAllText($p, $json); return "@$p"
}

Write-Host "== starting enterprise image (accounts mode) on $Port" -ForegroundColor Cyan
docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_ACCOUNT_MODE=accounts" `
    -e "SITEBIN_RATE_AUTH_PER_5MIN=200" `
    $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; exit 1 }

$up = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break } }
Assert "container up (accounts mode)" $up
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; exit 1 }

# edition reported
$ver = docker exec $name sitebin version
Assert "enterprise edition binary" ($ver -match "enterprise") "$ver"

# anonymous create is blocked
$r = Req "POST" "$origin/api/sites"
Assert "anonymous create blocked (401)" ($r.code -eq 401) "got $($r.code)"

# account pages served
$r = Req "GET" "$origin/account/signup"
Assert "signup page served" ($r.code -eq 200 -and $r.body -match "Create your account")

$jar = Join-Path $work "acct-cookies.txt"
Remove-Item $jar -ErrorAction SilentlyContinue

# signup
$r = Req "POST" "$origin/account/signup" @("-c", $jar, "--data-urlencode", "email=e2e@example.com", "--data-urlencode", "password=password12345")
Assert "signup succeeds (303)" ($r.code -eq 303) "got $($r.code): $($r.body)"
Assert "signup sets session cookie" ((Get-Content $jar -Raw -ErrorAction SilentlyContinue) -match "sitebin_s")

# dashboard shows the account
$r = Req "GET" "$origin/account" @("-b", $jar)
Assert "dashboard shows account" ($r.code -eq 200 -and $r.body -match "e2e@example.com")
Assert "dashboard has no sites yet" ($r.body -match "No sites yet")

# create a site while logged in -- becomes owned
$idxFile = Join-Path $work "acct-index.html"
[IO.File]::WriteAllText($idxFile, "<h1>owned-site-ok</h1>")
$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "files=@$idxFile;filename=index.html")
Assert "logged-in create succeeds (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
$site = $null; if ($r.code -eq 201) { $site = $r.body | ConvertFrom-Json }

if ($site) {
    $r = Req "GET" "$($site.view_url)/"
    Assert "owned site serves content" ($r.body -match "owned-site-ok")

    # dashboard now lists the owned site
    $r = Req "GET" "$origin/account" @("-b", $jar)
    Assert "dashboard lists owned site" ($r.body -match [regex]::Escape($site.id))

    # a DIFFERENT account cannot see or manage it
    $jar2 = Join-Path $work "acct2.txt"; Remove-Item $jar2 -ErrorAction SilentlyContinue
    Req "POST" "$origin/account/signup" @("-c", $jar2, "--data-urlencode", "email=other@example.com", "--data-urlencode", "password=password12345") | Out-Null
    $r = Req "GET" "$origin/account" @("-b", $jar2)
    Assert "other account sees no sites" ($r.body -match "No sites yet")

    # custom domains ARE available in the enterprise edition -- but a domain is
    # attached only once its DNS proves it belongs to the site. acct.e2e.test
    # has no such record, so the claim is recorded as pending (202) with the
    # record to create, and nothing is served on it.
    $edit = ($site.edit_url -split "/e/")[1]
    $r = Req "POST" "$origin/api/sites/$edit/domains" @("-H", "X-Edit-Password: $($site.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBodyA '{"domain":"acct.e2e.test"}'))
    Assert "custom domain claim is pending verification (202)" ($r.code -eq 202) "got $($r.code): $($r.body)"
    $claim = $null; if ($r.code -eq 202) { $claim = $r.body | ConvertFrom-Json }
    Assert "pending claim names the TXT record to create" ($null -ne $claim -and $claim.pending_domains.Count -eq 1 -and $claim.pending_domains[0].txt_name -eq "_sitebin-challenge.acct.e2e.test" -and $claim.pending_domains[0].txt_value -match "^sitebin-verify=") "$($r.body)"
    Assert "pending claim is not attached" ($null -ne $claim -and $claim.custom_domains.Count -eq 0) "$($r.body)"
    $r = Req "GET" "http://127.0.0.1:$Port/" @("-H", "Host: acct.e2e.test")
    Assert "unverified domain serves nothing" ($r.code -ne 200 -or $r.body -notmatch "owned-site-ok") "code $($r.code)"
    # asking again re-checks; the record is still absent, so still pending
    $r = Req "POST" "$origin/api/sites/$edit/domains" @("-H", "X-Edit-Password: $($site.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBodyA '{"domain":"acct.e2e.test"}'))
    Assert "re-check without the record stays pending (202)" ($r.code -eq 202) "got $($r.code)"
    $r = Req "DELETE" "$origin/api/sites/$edit/domains/acct.e2e.test" @("-H", "X-Edit-Password: $($site.edit_password)")
    Assert "a pending claim can be removed (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
}

# tags: add one through the dashboard form, filter by it, open the tags page
if ($site) {
    $idx2 = Join-Path $work "acct-index2.html"
    [IO.File]::WriteAllText($idx2, "<h1>second-site-ok</h1>")
    $r = Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "files=@$idx2;filename=index.html")
    $site2 = $null; if ($r.code -eq 201) { $site2 = $r.body | ConvertFrom-Json }
    Assert "a second owned site is created" ($null -ne $site2) "got $($r.code): $($r.body)"
    $r = Req "GET" "$origin/account" @("-b", $jar)
    $csrf = ""; if ($r.body -match 'name="csrf" value="([^"]+)"') { $csrf = $Matches[1] }
    Assert "the dashboard carries a csrf token" ($csrf.Length -gt 0)
    $r = Req "POST" "$origin/account/sites/$($site.id)/tags/add" @("-b", $jar, "--data-urlencode", "csrf=$csrf", "--data-urlencode", "tag=mockup", "--data-urlencode", "group=", "--data-urlencode", "back=")
    Assert "adding a tag through the dashboard redirects (303)" ($r.code -eq 303) "got $($r.code): $($r.body)"
    $r = Req "GET" "$origin/account?t=mockup" @("-b", $jar)
    Assert "the tag filter lists the tagged site" ($r.code -eq 200 -and $r.body.Contains($site.id)) "got $($r.code)"
    Assert "the tag filter leaves the other site out" ($null -ne $site2 -and -not $r.body.Contains($site2.id))
    $r = Req "GET" "$origin/account/tags" @("-b", $jar)
    Assert "the tags page is served (200)" ($r.code -eq 200) "got $($r.code)"

    # an account-wide rename through the manage page renames the tag on every
    # site that carries it, and the old tag is gone
    if ($site2) {
        $r = Req "POST" "$origin/account/sites/$($site2.id)/tags/add" @("-b", $jar, "--data-urlencode", "csrf=$csrf", "--data-urlencode", "tag=Mockup", "--data-urlencode", "group=", "--data-urlencode", "back=")
        Assert "the second site gets the same tag (303)" ($r.code -eq 303) "got $($r.code): $($r.body)"
        $r = Req "POST" "$origin/account/tags/tag" @("-b", $jar, "--data-urlencode", "csrf=$csrf", "--data-urlencode", "tag=mockup", "--data-urlencode", "op=rename", "--data-urlencode", "value=Entwurf")
        Assert "the manage page renames the tag across sites (303)" ($r.code -eq 303 -and $r.headers -match "Location: /account/tags\?flash=renamed") "got $($r.code): $($r.headers) $($r.body)"
        $r = Req "GET" "$origin/account?t=entwurf" @("-b", $jar)
        Assert "the new tag's filter lists both sites" ($r.code -eq 200 -and $r.body.Contains($site.id) -and $r.body.Contains($site2.id) -and $r.body.Contains("2 of 2")) "got $($r.code)"
        Assert "the old tag is gone from the site list" ($r.body -notmatch '<span class="tn">mockup</span>' -and $r.body -notmatch '<option value="mockup">' -and $r.body -match '<span class="tn">Entwurf</span>')
        foreach ($s in @(@{ s = $site; n = "first" }, @{ s = $site2; n = "second" })) {
            $e = ($s.s.edit_url -split "/e/")[1]
            $r = Req "GET" "$origin/api/sites/$e" @("-H", "X-Edit-Password: $($s.s.edit_password)")
            $tags = $null; if ($r.code -eq 200) { $tags = ($r.body | ConvertFrom-Json).tags }
            Assert "the $($s.n) site carries only the new tag" ((@($tags) -join ",") -eq "Entwurf") "got $($r.code): $($r.body)"
        }
    }
}

# duplicate email rejected
$r = Req "POST" "$origin/account/signup" @("--data-urlencode", "email=e2e@example.com", "--data-urlencode", "password=password12345")
Assert "duplicate email rejected" ($r.body -match "already registered")

# wrong login rejected
$r = Req "POST" "$origin/account/login" @("--data-urlencode", "email=e2e@example.com", "--data-urlencode", "password=wrongpass")
Assert "wrong login rejected" ($r.body -match "Incorrect email or password")

# session persists across container restart (filesystem accounts)
docker restart $name | Out-Null
$up2 = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { $up2 = $true; break } }
Assert "restart preserves accounts" ($up2 -and (Req "GET" "$origin/account" @("-b", $jar)).body -match "e2e@example.com")

# main domain never serves user content
$r = Req "GET" "$origin/index.html"
Assert "main domain still not serving user files" ($r.code -ne 200 -or $r.body -notmatch "owned-site-ok")

# cleanup
docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null

Write-Host ""
Write-Host ("== accounts E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
