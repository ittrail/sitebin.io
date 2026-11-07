# Sitebin Enterprise -- tiers/quota E2E against the real enterprise image.
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

# ---- git helpers (hermetic git; the secret goes through GIT_ASKPASS, never a URL)
$gitTmp = Join-Path ([IO.Path]::GetTempPath()) ("sitebin-e2e-" + [Guid]::NewGuid().ToString("N").Substring(0, 8))
New-Item -ItemType Directory -Force $gitTmp | Out-Null
$gitEmptyCfg = Join-Path $gitTmp "empty.gitconfig"; [IO.File]::WriteAllText($gitEmptyCfg, "")
$gitAskpass = (Join-Path $gitTmp "askpass.sh") -replace '\\', '/'
[IO.File]::WriteAllText($gitAskpass, "#!/bin/sh`ncase `"`$1`" in`n  Username*) echo e2e ;;`n  *) echo `"`$SB_GIT_PASS`" ;;`nesac`n")
function Git([string]$dir, [string]$pass, [string[]]$gargs) {
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = "git"; $psi.WorkingDirectory = $dir; $psi.UseShellExecute = $false
    $psi.RedirectStandardOutput = $true; $psi.RedirectStandardError = $true; $psi.CreateNoWindow = $true
    $psi.EnvironmentVariables["GIT_CONFIG_NOSYSTEM"] = "1"
    $psi.EnvironmentVariables["GIT_CONFIG_GLOBAL"] = $gitEmptyCfg
    $psi.EnvironmentVariables["GIT_ASKPASS"] = $gitAskpass
    $psi.EnvironmentVariables["GIT_TERMINAL_PROMPT"] = "0"
    $psi.EnvironmentVariables["SB_GIT_PASS"] = $pass
    $all = @("-c", "credential.helper=", "-c", "user.name=E2E", "-c", "user.email=e2e@example.com", "-c", "init.defaultBranch=main", "-c", "core.autocrlf=false") + $gargs
    $psi.Arguments = (($all | ForEach-Object { '"' + ($_ -replace '"', '\"') + '"' }) -join " ")
    $p = [System.Diagnostics.Process]::Start($psi)
    $so = $p.StandardOutput.ReadToEndAsync(); $se = $p.StandardError.ReadToEndAsync()
    $p.WaitForExit()
    return @{ code = $p.ExitCode; out = ($so.Result + $se.Result) }
}
# a one-commit working copy; returns its path
function New-GitWork([string]$leaf) {
    $d = Join-Path $gitTmp $leaf
    New-Item -ItemType Directory -Force $d | Out-Null
    Git $d "x" @("init", "-q") | Out-Null
    [IO.File]::WriteAllText((Join-Path $d "a.txt"), "hello`n")
    Git $d "x" @("add", "-A") | Out-Null
    Git $d "x" @("commit", "-q", "-m", "first") | Out-Null
    return $d
}

# free tier: 1 site max, 200-byte storage cap, no webdav, 0 custom domains,
# 1 form. Trusted, because forms need a trusted tier when accounts are on.
# Written to a mounted file to avoid shell JSON-quoting issues (this is the
# documented production approach via SITEBIN_TIERS_FILE).
$tiers = '[{"id":"free","label":"Free","max_site_bytes":200,"max_files":5,"max_sites":1,"webdav":false,"custom_domains":0,"max_expiry_days":7,"max_forms":1,"trusted":true}]'
[IO.File]::WriteAllText((Join-Path $work "tiers.json"), $tiers)
$workDocker = ($work -replace '\\', '/')

Write-Host "== starting enterprise image (tiers mode) on $Port" -ForegroundColor Cyan
docker rm -f $name 2>$null | Out-Null; docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" -v "${workDocker}:/cfg:ro" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_ACCOUNT_MODE=tiers" -e "SITEBIN_DEFAULT_UI=classic" -e "SITEBIN_TIERS_FILE=/cfg/tiers.json" -e "SITEBIN_DEFAULT_TIER=free" `
    -e "SITEBIN_RATE_AUTH_PER_5MIN=200" -e "SITEBIN_CLEANUP_INTERVAL=5s" `
    -e "SITEBIN_FORMS_SMTP_HOST=127.0.0.1" -e "SITEBIN_FORMS_SMTP_PORT=1" -e "SITEBIN_FORMS_SMTP_FROM=forms@localtest.me" `
    $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; exit 1 }

$up = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break } }
Assert "container up (tiers mode)" $up
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; exit 1 }

$jar = Join-Path $work "tiers-cookies.txt"; Remove-Item $jar -ErrorAction SilentlyContinue
Req "POST" "$origin/account/signup" @("-c", $jar, "--data-urlencode", "email=t@example.com", "--data-urlencode", "password=password12345") | Out-Null

# small file within cap -> ok
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

    # forms: the tier allows one per site. The SMTP server is deliberately
    # unreachable, so the form is created, pending, with a warning.
    $r = Req "POST" "$origin/api/sites/$edit/forms" @("-H", "X-Edit-Password: $($site.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBodyT '{"name":"A","recipient":"a@example.test"}'))
    Assert "first form within max_forms (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
    Assert "an unsent confirmation is a warning" ($r.body -match '"warnings"') "$($r.body)"
    $r = Req "POST" "$origin/api/sites/$edit/forms" @("-H", "X-Edit-Password: $($site.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBodyT '{"name":"B","recipient":"b@example.test"}'))
    Assert "second form over max_forms=1 (403)" ($r.code -eq 403) "got $($r.code): $($r.body)"
}

# second site -> blocked by max_sites=1
$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "files=@$small;filename=index.html")
Assert "second site blocked (max_sites, 403)" ($r.code -eq 403) "got $($r.code): $($r.body)"

# oversized upload -> 413 (exceeds 200-byte tier cap)
$big = Join-Path $work "big.txt"; [IO.File]::WriteAllText($big, ("A" * 500))
$jar2 = Join-Path $work "tiers2.txt"; Remove-Item $jar2 -ErrorAction SilentlyContinue
Req "POST" "$origin/account/signup" @("-c", $jar2, "--data-urlencode", "email=t2@example.com", "--data-urlencode", "password=password12345") | Out-Null
$r = Req "POST" "$origin/api/sites" @("-b", $jar2, "-F", "files=@$big;filename=index.html")
Assert "oversized upload rejected (413)" ($r.code -eq 413) "got $($r.code): $($r.body)"



# ---------- git: the plan decides who may push ----------
# The free tier above has no "git" flag, so the site was stamped without Git.
# A repository is never deleted by a downgrade: it stays readable.
if ($site) {
    $remoteG = "$origin/git/$edit.git"
    $pwG = $site.edit_password
    $wcG = New-GitWork "wcG"
    $g = Git $wcG $pwG @("push", $remoteG, "main")
    Assert "a tier without git: the push is refused" ($g.code -ne 0) "code $($g.code) $($g.out)"
    Assert "the refusal carries the upgrade message" ($g.out -match "does not include Git" -and $g.out -match "upgrade") "$($g.out)"
    $r = Req "GET" "$origin/api/sites/$edit/git" @("-H", "X-Edit-Password: $pwG")
    $gi = $null; if ($r.code -eq 200) { $gi = $r.body | ConvertFrom-Json }
    Assert "git info: not writable, reason plan, no repository" ($null -ne $gi -and $gi.writable -eq $false -and $gi.reason -eq "plan" -and $gi.exists -eq $false) "$($r.body)"

}

# A site's byte cap is stamped at creation (the sweep follows a plan change for
# git only), so the sweep scenario uses a roomier tier from the start: restart
# on it without git, create a new account's site, then switch git on.
if ($site) {
    $tiersRoomy = '[{"id":"free","label":"Free","max_site_bytes":1000000,"max_files":5,"max_sites":1,"webdav":false,"custom_domains":0,"max_expiry_days":7,"max_forms":1,"trusted":true}]'
    [IO.File]::WriteAllText((Join-Path $work "tiers.json"), $tiersRoomy)
    docker restart $name | Out-Null
    for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { break } }
    $jar3 = Join-Path $work "tiers3.txt"; Remove-Item $jar3 -ErrorAction SilentlyContinue
    Req "POST" "$origin/account/signup" @("-c", $jar3, "--data-urlencode", "email=t3@example.com", "--data-urlencode", "password=password12345") | Out-Null
    $r = Req "POST" "$origin/api/sites" @("-b", $jar3, "-F", "files=@$small;filename=index.html")
    Assert "a site on the roomier tier without git is created" ($r.code -eq 201) "got $($r.code): $($r.body)"
    $site = $r.body | ConvertFrom-Json
    $edit = ($site.edit_url -split "/e/")[1]
    $pwG = $site.edit_password
    $remoteG = "$origin/git/$edit.git"
    $wcG = New-GitWork "wcG-roomy"
    $g = Git $wcG $pwG @("push", $remoteG, "main")
    Assert "roomier tier without git: the push is still refused" ($g.code -ne 0 -and $g.out -match "does not include Git") "$($g.out)"

    # Give the tier git, restart so the instance reads the file, and let the
    # sweep (every 5 s) restamp the site.
    $tiersGit = '[{"id":"free","label":"Free","max_site_bytes":1000000,"max_files":5,"max_sites":1,"webdav":false,"custom_domains":0,"max_expiry_days":7,"max_forms":1,"trusted":true,"git":true}]'
    [IO.File]::WriteAllText((Join-Path $work "tiers.json"), $tiersGit)
    docker restart $name | Out-Null
    $upG = $false
    for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { $upG = $true; break } }
    Assert "container back up with git: true" $upG
    $pushed = $false; $g = $null
    for ($i = 0; $i -lt 8 -and -not $pushed; $i++) {
        Start-Sleep -Seconds 3
        $g = Git $wcG $pwG @("push", $remoteG, "main")
        if ($g.code -eq 0) { $pushed = $true }
    }
    Assert "with git: true, after a sweep, the push works" $pushed "$($g.out)"
    $wcG2 = Join-Path $gitTmp "wcG2"
    $g = Git $gitTmp $pwG @("clone", "-q", $remoteG, $wcG2)
    Assert "the pushed repository clones" ($g.code -eq 0 -and (Test-Path (Join-Path $wcG2 "a.txt"))) "$($g.out)"

    # Downgrade again: the repository stays, read-only.
    $tiersNoGit = '[{"id":"free","label":"Free","max_site_bytes":1000000,"max_files":5,"max_sites":1,"webdav":false,"custom_domains":0,"max_expiry_days":7,"max_forms":1,"trusted":true}]'
    [IO.File]::WriteAllText((Join-Path $work "tiers.json"), $tiersNoGit)
    docker restart $name | Out-Null
    for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { break } }
    $ro = $false
    for ($i = 0; $i -lt 8 -and -not $ro; $i++) {
        Start-Sleep -Seconds 3
        $r = Req "GET" "$origin/api/sites/$edit/git" @("-H", "X-Edit-Password: $pwG")
        if ($r.code -eq 200 -and ($r.body | ConvertFrom-Json).writable -eq $false) { $ro = $true }
    }
    Assert "after the downgrade the sweep makes the repository read-only" $ro "$($r.body)"
    $gi = $r.body | ConvertFrom-Json
    Assert "downgraded: the repository still exists and is available" ($gi.exists -eq $true -and $gi.available -eq $true -and $gi.reason -eq "plan") "$($r.body)"
    $wcG3 = Join-Path $gitTmp "wcG3"
    $g = Git $gitTmp $pwG @("clone", "-q", $remoteG, $wcG3)
    Assert "a tier without git: the clone of an existing repository works" ($g.code -eq 0 -and (Test-Path (Join-Path $wcG3 "a.txt"))) "$($g.out)"
    [IO.File]::WriteAllText((Join-Path $wcG "b.txt"), "bee`n")
    Git $wcG "x" @("add", "-A") | Out-Null
    Git $wcG "x" @("commit", "-q", "-m", "second") | Out-Null
    $g = Git $wcG $pwG @("push", $remoteG, "main")
    Assert "downgraded: the push is refused as read-only with the upgrade message" ($g.code -ne 0 -and $g.out -match "read-only" -and $g.out -match "upgrade") "code $($g.code) $($g.out)"
}
Remove-Item -Recurse -Force $gitTmp -ErrorAction SilentlyContinue

docker rm -f $name 2>$null | Out-Null; docker volume rm $vol 2>$null | Out-Null
Write-Host ""
Write-Host ("== tiers E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
