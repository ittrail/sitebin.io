# Sitebin Git end-to-end suite -- exercises the real Docker image over HTTP and
# a real git client.
#
#   powershell -File e2e\git.ps1 [-Image sitebin:dev] [-KeepUp]
#
# Covers: the edit page's Git tab HTML; push and clone with the edit password
# (given to git through a GIT_ASKPASS script, never in a URL); fetch, force
# push and branch delete; the read routes under /api/sites/{id}/git; the
# storage-full refusal; delete repository; and that deleting the site removes
# its git/ folder. git runs hermetically (no system or global config, no
# credential helper).
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
$smallName = "sitebin-e2e-git-small"
$smallVol = "sitebin-e2e-git-small-data"
$smallPort = $Port + 1
$smallOrigin = "http://${base}:$smallPort"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

# a private temp dir for the git helpers: the askpass script, the empty global
# config and the working copies
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("sitebin-e2e-git-" + [Guid]::NewGuid().ToString("N").Substring(0, 8))
New-Item -ItemType Directory -Force $tmp | Out-Null
$emptyCfg = Join-Path $tmp "empty.gitconfig"
[IO.File]::WriteAllText($emptyCfg, "")
# GIT_ASKPASS is run by git's own sh, so this is a POSIX script with LF line
# endings. The password comes from the environment, never from a URL.
$askpass = (Join-Path $tmp "askpass.sh") -replace '\\', '/'
[IO.File]::WriteAllText($askpass, "#!/bin/sh`ncase `"`$1`" in`n  Username*) echo e2e ;;`n  *) echo `"`$SB_GIT_PASS`" ;;`nesac`n")

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

# Git runs git hermetically in $dir with $pass available to the askpass script.
# Returns @{ code; out } with stdout and stderr together.
function Git([string]$dir, [string]$pass, [string[]]$gargs) {
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = "git"
    $psi.WorkingDirectory = $dir
    $psi.UseShellExecute = $false
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.CreateNoWindow = $true
    $psi.EnvironmentVariables["GIT_CONFIG_NOSYSTEM"] = "1"
    $psi.EnvironmentVariables["GIT_CONFIG_GLOBAL"] = $emptyCfg
    $psi.EnvironmentVariables["GIT_ASKPASS"] = $askpass
    $psi.EnvironmentVariables["GIT_TERMINAL_PROMPT"] = "0"
    $psi.EnvironmentVariables["SB_GIT_PASS"] = $pass
    $all = @("-c", "credential.helper=", "-c", "user.name=E2E", "-c", "user.email=e2e@example.com", "-c", "init.defaultBranch=main", "-c", "core.autocrlf=false") + $gargs
    $psi.Arguments = (($all | ForEach-Object { '"' + ($_ -replace '"', '\"') + '"' }) -join " ")
    $p = [System.Diagnostics.Process]::Start($psi)
    $so = $p.StandardOutput.ReadToEndAsync()
    $se = $p.StandardError.ReadToEndAsync()
    $p.WaitForExit()
    return @{ code = $p.ExitCode; out = ($so.Result + $se.Result) }
}

function Write-File([string]$dir, [string]$rel, [string]$text) {
    $p = Join-Path $dir $rel
    New-Item -ItemType Directory -Force (Split-Path $p) | Out-Null
    [IO.File]::WriteAllText($p, $text)
}

function Start-Container([string]$cn, [string]$cv, [int]$cp, [string[]]$extraEnv) {
    docker rm -f $cn 2>$null | Out-Null
    docker volume rm $cv 2>$null | Out-Null
    $envArgs = @("-e", "SITEBIN_BASE_DOMAIN=${base}:$cp", "-e", "SITEBIN_HTTP_ONLY=true",
        "-e", "SITEBIN_RATE_CREATE_PER_HOUR=1000", "-e", "SITEBIN_RATE_CREATE_BURST=200") + $extraEnv
    docker run -d --name $cn -p "${cp}:80" -v "${cv}:/data" @envArgs $Image | Out-Null
    if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; return $false }
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Milliseconds 700
        if ((Req "GET" "http://${base}:$cp/").code -eq 200) { return $true }
    }
    return $false
}

function Cleanup {
    docker rm -f $name 2>$null | Out-Null
    docker volume rm $vol 2>$null | Out-Null
    docker rm -f $smallName 2>$null | Out-Null
    docker volume rm $smallVol 2>$null | Out-Null
}

Write-Host "== starting $Image on port $Port" -ForegroundColor Cyan
$up = Start-Container $name $vol $Port @()
Assert "container serves the landing page" $up "container did not come up"
if (-not $up) { docker logs $name; Cleanup; exit 1 }

# ---------- the edit page's tabs ----------

$idx = Join-Path $work "git-index.html"
[IO.File]::WriteAllText($idx, "<h1>git e2e</h1>")
$r = Req "POST" "$origin/api/sites" @("-F", "files=@$idx;filename=index.html")
Assert "site created" ($r.code -eq 201) "code $($r.code) $($r.body)"
$site = $r.body | ConvertFrom-Json
$editId = ($site.edit_url -split "/e/")[1]
$pw = $site.edit_password
$viewId = $site.id

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

$hdr = @("-H", "X-Edit-Password: $pw")
$api = "$origin/api/sites/$editId"
$r = Req "GET" "$api/git" $hdr
$info = $null; if ($r.code -eq 200) { $info = $r.body | ConvertFrom-Json }
Assert "git info before a push: exists false" ($null -ne $info -and $info.exists -eq $false) "code $($r.code) $($r.body)"
Assert "git info: available and writable" ($null -ne $info -and $info.available -eq $true -and $info.writable -eq $true) "$($r.body)"
Assert "git info: clone address has no credentials" ($null -ne $info -and $info.url -eq "$origin/git/$editId.git") "url $($info.url)"
$remote = "$origin/git/$editId.git"

# ---------- push and clone with the edit password ----------

$wcA = Join-Path $tmp "wcA"
New-Item -ItemType Directory -Force $wcA | Out-Null
$g = Git $wcA $pw @("init", "-q")
Assert "git init" ($g.code -eq 0) $g.out
Write-File $wcA "a.txt" "hello`n"
Write-File $wcA "sub/b.txt" "bee`n"
$g = Git $wcA $pw @("add", "-A"); $g = Git $wcA $pw @("commit", "-q", "-m", "first")
Assert "first commit" ($g.code -eq 0) $g.out
Write-File $wcA "a.txt" "hello again`n"
$g = Git $wcA $pw @("commit", "-q", "-am", "second")
Write-File $wcA "c.txt" "see`n"
$g = Git $wcA $pw @("add", "-A"); $g = Git $wcA $pw @("commit", "-q", "-m", "third")
$headSha = (Git $wcA $pw @("rev-parse", "HEAD")).out.Trim()
$firstSha = (Git $wcA $pw @("rev-parse", "HEAD~2")).out.Trim()

$g = Git $wcA "wrong-password" @("push", $remote, "main")
Assert "push with a wrong password is refused" ($g.code -ne 0 -and $g.out -match "(?i)authentication failed|could not read|403|401") "code $($g.code) $($g.out)"
$r = Req "GET" "$api/git" $hdr
Assert "a refused push created no repository" (($r.body | ConvertFrom-Json).exists -eq $false) $r.body

$g = Git $wcA $pw @("push", $remote, "main")
Assert "push with the edit password succeeds" ($g.code -eq 0) "code $($g.code) $($g.out)"
Assert "the password never appears in git's output" (-not $g.out.Contains($pw))

$wcB = Join-Path $tmp "wcB"
$g = Git $tmp $pw @("clone", "-q", $remote, $wcB)
Assert "clone with the edit password succeeds" ($g.code -eq 0) "code $($g.code) $($g.out)"
$cloned = (Git $wcB $pw @("rev-parse", "HEAD")).out.Trim()
Assert "the clone has the pushed head" ($cloned -eq $headSha) "$cloned vs $headSha"
Assert "the clone has the files" ((Test-Path (Join-Path $wcB "sub/b.txt")) -and ([IO.File]::ReadAllText((Join-Path $wcB "a.txt")) -eq "hello again`n"))
Assert "the clone has the whole history" ((Git $wcB $pw @("rev-list", "--count", "HEAD")).out.Trim() -eq "3")
$cfg = (Git $wcB $pw @("config", "--local", "--get", "remote.origin.url")).out.Trim()
Assert "the clone's remote URL carries no credentials" ($cfg -eq $remote) $cfg

$wcC = Join-Path $tmp "wcC"
$g = Git $tmp "wrong-password" @("clone", "-q", $remote, $wcC)
Assert "clone with a wrong password is refused" ($g.code -ne 0 -and -not (Test-Path (Join-Path $wcC ".git"))) "code $($g.code) $($g.out)"

# ---------- fetch, branch push, force push, branch delete ----------

Write-File $wcA "d.txt" "dee`n"
$g = Git $wcA $pw @("add", "-A"); $g = Git $wcA $pw @("commit", "-q", "-m", "fourth")
$fourthSha = (Git $wcA $pw @("rev-parse", "HEAD")).out.Trim()
$g = Git $wcA $pw @("push", $remote, "main")
Assert "a fast-forward push succeeds" ($g.code -eq 0) $g.out
$g = Git $wcB $pw @("fetch", "-q", "origin")
Assert "fetch succeeds" ($g.code -eq 0) $g.out
Assert "fetch brought the new commit" ((Git $wcB $pw @("rev-parse", "origin/main")).out.Trim() -eq $fourthSha)
$g = Git $wcB $pw @("pull", "-q", "--ff-only", "origin", "main")
Assert "pull fast-forwards the clone" ($g.code -eq 0 -and (Test-Path (Join-Path $wcB "d.txt"))) $g.out

$g = Git $wcA $pw @("push", $remote, "HEAD:refs/heads/feature")
Assert "a second branch can be pushed" ($g.code -eq 0) $g.out
$g = Git $wcA $pw @("push", $remote, "HEAD:refs/tags/v1")
Assert "a tag can be pushed" ($g.code -eq 0) $g.out
$r = Req "GET" "$api/git/refs" $hdr
$names = @(); if ($r.code -eq 200) { $names = @(($r.body | ConvertFrom-Json).refs | ForEach-Object { $_.name }) }
Assert "refs lists main, feature and v1" ($names -contains "main" -and $names -contains "feature" -and $names -contains "v1") "$($r.code) $($r.body)"

$g = Git $wcA $pw @("reset", "-q", "--hard", "HEAD~1")
$g = Git $wcA $pw @("push", $remote, "main")
Assert "a non-fast-forward push is refused without force" ($g.code -ne 0 -and $g.out -match "rejected|non-fast-forward|fetch first") "code $($g.code) $($g.out)"
$g = Git $wcA $pw @("push", "--force", $remote, "main")
Assert "a force push succeeds" ($g.code -eq 0) "code $($g.code) $($g.out)"
$r = Req "GET" "$api/git/log" $hdr
$log = $null; if ($r.code -eq 200) { $log = $r.body | ConvertFrom-Json }
Assert "after the force push the history is three commits" ($null -ne $log -and @($log.commits).Count -eq 3 -and $log.sha -eq $headSha) "$($r.code) $($r.body)"

$g = Git $wcA $pw @("push", $remote, "--delete", "feature")
Assert "a branch can be deleted" ($g.code -eq 0) "code $($g.code) $($g.out)"
$r = Req "GET" "$api/git/refs" $hdr
$names = @(); if ($r.code -eq 200) { $names = @(($r.body | ConvertFrom-Json).refs | ForEach-Object { $_.name }) }
Assert "refs no longer lists the deleted branch" ($names -contains "main" -and -not ($names -contains "feature")) "$($r.body)"
$g = Git $wcA $pw @("ls-remote", "--heads", $remote)
Assert "ls-remote agrees: only main" ($g.code -eq 0 -and $g.out -match "refs/heads/main" -and $g.out -notmatch "refs/heads/feature") $g.out

# ---------- the read routes ----------

$r = Req "GET" "$api/git" $hdr
$info = $r.body | ConvertFrom-Json
Assert "git info after a push: exists true, default branch main" ($r.code -eq 200 -and $info.exists -eq $true -and $info.default_branch -eq "main") "code $($r.code) $($r.body)"
Assert "git info reports bytes and the last push" ($info.bytes -gt 0 -and $null -ne $info.last_push) "$($r.body)"
$r = Req "GET" $api $hdr
Assert "site payload carries git_bytes" ($r.code -eq 200 -and ($r.body | ConvertFrom-Json).usage.git_bytes -gt 0) "code $($r.code)"

$r = Req "GET" "$api/git/log?skip=1" $hdr
$log2 = $null; if ($r.code -eq 200) { $log2 = $r.body | ConvertFrom-Json }
Assert "log with skip returns the older commits" ($null -ne $log2 -and @($log2.commits).Count -eq 2 -and @($log2.commits)[1].subject -eq "first") "$($r.code) $($r.body)"
$r = Req "GET" "$api/git/log?skip=-1" $hdr
Assert "log with a bad skip is 400" ($r.code -eq 400) "code $($r.code)"

$r = Req "GET" "$api/git/commit/$firstSha" $hdr
$cd = $null; if ($r.code -eq 200) { $cd = $r.body | ConvertFrom-Json }
Assert "commit route returns the message and changed files" ($null -ne $cd -and $cd.sha -eq $firstSha -and $cd.message -match "first" -and @($cd.files).Count -eq 2) "$($r.code) $($r.body)"
$r = Req "GET" "$api/git/commit/0000000000000000000000000000000000000000" $hdr
Assert "commit route: an unknown commit is 404" ($r.code -eq 404) "code $($r.code)"

$r = Req "GET" "$api/git/tree" $hdr
$tree = $null; if ($r.code -eq 200) { $tree = $r.body | ConvertFrom-Json }
$tn = @(); if ($tree) { $tn = @($tree.entries | ForEach-Object { $_.name + ":" + $_.type }) }
Assert "tree lists the root" ($tn -contains "a.txt:file" -and $tn -contains "sub:dir" -and $tn -contains "c.txt:file") "$($r.code) $($r.body)"
$r = Req "GET" "$api/git/tree?path=sub" $hdr
Assert "tree lists a folder" ($r.code -eq 200 -and @(($r.body | ConvertFrom-Json).entries).Count -eq 1 -and @(($r.body | ConvertFrom-Json).entries)[0].name -eq "b.txt") "$($r.code) $($r.body)"
$r = Req "GET" "$api/git/tree?path=nope" $hdr
Assert "tree of a missing folder is 404" ($r.code -eq 404) "code $($r.code)"
$r = Req "GET" "$api/git/tree?ref=$firstSha" $hdr
$tn1 = @(); if ($r.code -eq 200) { $tn1 = @(($r.body | ConvertFrom-Json).entries | ForEach-Object { $_.name }) }
Assert "tree at an older ref has no c.txt" ($r.code -eq 200 -and -not ($tn1 -contains "c.txt") -and ($tn1 -contains "a.txt")) "$($r.body)"

$r = Req "GET" "$api/git/blob?path=a.txt" $hdr
$bl = $null; if ($r.code -eq 200) { $bl = $r.body | ConvertFrom-Json }
Assert "blob returns the text" ($null -ne $bl -and $bl.binary -eq $false -and $bl.text -eq "hello again`n") "$($r.code) $($r.body)"
$r = Req "GET" "$api/git/blob?path=a.txt&download=1" $hdr
Assert "blob download returns the raw bytes" ($r.code -eq 200 -and $r.body -eq "hello again`n") "$($r.code) $($r.body)"
$r = Req "GET" "$api/git/blob?path=missing.txt" $hdr
Assert "blob of a missing file is 404" ($r.code -eq 404) "code $($r.code)"

$zip = Join-Path $work "git-archive.zip"
Remove-Item $zip -ErrorAction SilentlyContinue
$code = & curl.exe -s -o $zip -w "%{http_code}" --max-time 30 -H "X-Edit-Password: $pw" "$api/git/archive"
$magic = ""; if (Test-Path $zip) { $bytes = [IO.File]::ReadAllBytes($zip); if ($bytes.Length -ge 2) { $magic = [Text.Encoding]::ASCII.GetString($bytes, 0, 2) } }
Assert "archive is a ZIP" ([int]$code -eq 200 -and $magic -eq "PK") "code $code magic $magic"

$r = Req "GET" "$api/git/log"
Assert "read routes need the edit password" ($r.code -eq 401 -or $r.code -eq 403) "code $($r.code)"
$r = Req "GET" "$api/git/log" @("-H", "X-Edit-Password: wrong")
Assert "read routes refuse a wrong password" ($r.code -eq 401 -or $r.code -eq 403) "code $($r.code)"

# ---------- delete repository ----------

$r = Req "DELETE" "$api/git" $hdr
Assert "delete repository answers 204" ($r.code -eq 204) "code $($r.code) $($r.body)"
$r = Req "GET" "$api/git" $hdr
Assert "after delete: exists false" ($r.code -eq 200 -and ($r.body | ConvertFrom-Json).exists -eq $false) "$($r.body)"
$r = Req "GET" "$api/git/refs" $hdr
Assert "after delete: refs is 404" ($r.code -eq 404) "code $($r.code)"
Assert "deleting the repository leaves the site's files" ((Req "GET" "$($site.view_url)/").body -match "git e2e")

# ---------- site delete removes git/ ----------

$g = Git $wcA $pw @("push", "--force", $remote, "main")
Assert "the repository can be pushed again after a delete" ($g.code -eq 0) "code $($g.code) $($g.out)"
$dirs = docker exec $name sh -c "find /data -maxdepth 5 -type d -name git"
Assert "a git folder exists under the site" (@($dirs | Where-Object { $_ -match "$viewId" }).Count -ge 1) "find: $dirs"
$r = Req "DELETE" $api $hdr
Assert "site delete succeeds" ($r.code -eq 200) "code $($r.code) $($r.body)"
$dirs = docker exec $name sh -c "find /data -maxdepth 5 -type d -name git"
Assert "site delete removed the git folder" (-not $dirs) "find: $dirs"
$left = docker exec $name sh -c "find /data -path '*$viewId*'"
Assert "nothing of the site is left on the volume" (-not $left) "find: $left"
$g = Git $tmp $pw @("ls-remote", $remote)
Assert "the repository is gone with the site" ($g.code -ne 0) "code $($g.code) $($g.out)"

# ---------- storage full ----------

Write-Host "== starting a small-quota instance on port $smallPort" -ForegroundColor Cyan
$up2 = Start-Container $smallName $smallVol $smallPort @("-e", "SITEBIN_MAX_SITE_BYTES=2000")
Assert "small-quota container serves the landing page" $up2 "container did not come up"
if ($up2) {
    $fill = Join-Path $work "git-fill.html"
    [IO.File]::WriteAllText($fill, ("x" * 2000))
    $r = Req "POST" "$smallOrigin/api/sites" @("-F", "files=@$fill;filename=index.html")
    Assert "a site filling its cap is created" ($r.code -eq 201) "code $($r.code) $($r.body)"
    $s2 = $null; if ($r.code -eq 201) { $s2 = $r.body | ConvertFrom-Json }
    if ($s2) {
        $e2 = ($s2.edit_url -split "/e/")[1]
        $remote2 = "$smallOrigin/git/$e2.git"
        $wcD = Join-Path $tmp "wcD"
        New-Item -ItemType Directory -Force $wcD | Out-Null
        $g = Git $wcD "x" @("init", "-q")
        Write-File $wcD "a.txt" "a`n"
        $g = Git $wcD "x" @("add", "-A"); $g = Git $wcD "x" @("commit", "-q", "-m", "one")
        $g = Git $wcD $s2.edit_password @("push", $remote2, "main")
        Assert "a push to a full site is refused" ($g.code -ne 0) "code $($g.code) $($g.out)"
        Assert "the refusal says storage full" ($g.out -match "remote error: storage full") "$($g.out)"
        $r = Req "GET" "$smallOrigin/api/sites/$e2/git" @("-H", "X-Edit-Password: $($s2.edit_password)")
        Assert "the refused push created no repository" ($r.code -eq 200 -and ($r.body | ConvertFrom-Json).exists -eq $false) "$($r.body)"
    }
}

# ---------- teardown & summary ----------

if (-not $KeepUp) { Cleanup }
Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
Write-Host ""
Write-Host ("== Git E2E result: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
