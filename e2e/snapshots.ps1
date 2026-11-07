# Sitebin -- snapshots and restore E2E: the enterprise image with a
# `sitebin agent` beside it (dir source: a folder of `sitebin backup`
# archives), as on a host, with the Docker socket for the instance restore.
#
#   powershell -File e2e\snapshots.ps1 [-Image sitebin:dev-ee] [-Keep]
#
# Verifies: a site lists its versions and marks the current one, downloads
# one as a ZIP, restores one (a snapshot is taken first, so the replaced
# files become the newest version); the operator restores the whole instance
# to a snapshot (stop, swap, start, healthy) and switches back to the kept
# copy; copies are deleted. -Keep leaves the containers running for a look.
param(
    [string]$Image = "sitebin:dev-ee",
    [int]$Port = 8099,
    [switch]$Keep
)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$name = "sitebin-snap-e2e"
$agent = "sitebin-snap-agent"
$hostVol = "sitebin-snap-host"
$sockVol = "sitebin-snap-sock"
$bkVol = "sitebin-snap-backups"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null
$workDocker = ($work -replace '\\', '/')

$script:pass = 0; $script:fail = 0
function Assert([string]$n, [bool]$c, [string]$d = "") {
    if ($c) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}
function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = Join-Path $work "sb.tmp"; $hdrFile = Join-Path $work "sh.tmp"
    Remove-Item $bodyFile, $hdrFile -ErrorAction SilentlyContinue
    $args = @("-s", "-X", $method, "-o", $bodyFile, "-D", $hdrFile, "-w", "%{http_code}", "--max-time", "120") + $extra + @($url)
    $code = & curl.exe @args
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    return @{ code = [int]$code; body = $body; file = $bodyFile }
}
function Cleanup {
    docker rm -f $name $agent 2>$null | Out-Null
    docker volume rm $hostVol $sockVol $bkVol 2>$null | Out-Null
}

# backup-now: a `sitebin backup` of the data into the backups folder, named
# like the hourly archives of the real setup.
$script = "#!/bin/sh`nset -e`nsleep 1`nf=/backups/hourly-`$(date -u +%Y%m%dT%H%M%S).tar.gz`nSITEBIN_DATA_DIR=/host/data sitebin backup `"`$f.part`"`nmv `"`$f.part`" `"`$f`"`n"
[IO.File]::WriteAllText((Join-Path $work "backup-now.sh"), $script)
$tiers = '[{"id":"pro","label":"Pro","max_site_bytes":1073741824,"max_files":5000,"max_sites":10,"webdav":true,"custom_domains":5,"max_expiry_days":0,"trusted":true,"admin":true}]'
[IO.File]::WriteAllText((Join-Path $work "snap-tiers.json"), $tiers)

Write-Host "== starting the agent and the enterprise image on $Port" -ForegroundColor Cyan
Cleanup
docker run --rm --user 0 -v "${hostVol}:/host" --entrypoint sh $Image -c "mkdir -p /host/data && chown sitebin:sitebin /host/data" | Out-Null
docker run -d --name $agent --user 0 -v "${hostVol}:/host" -v "${sockVol}:/run/sitebin-agent" -v "${bkVol}:/backups" `
    -v "${workDocker}:/cfg:ro" -v "/var/run/docker.sock:/var/run/docker.sock" --entrypoint /usr/bin/sitebin $Image `
    agent -data /host/data -source dir -dir /backups -backup-now "sh /cfg/backup-now.sh" -container $name -state /host/agent-state | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "agent run failed" -ForegroundColor Red; exit 1 }
docker run -d --name $name -p "${Port}:80" --mount "type=volume,src=${hostVol},dst=/data,volume-subpath=data" `
    -v "${sockVol}:/run/sitebin-agent" -v "${workDocker}:/cfg:ro" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_ACCOUNT_MODE=tiers" -e "SITEBIN_TIERS_FILE=/cfg/snap-tiers.json" -e "SITEBIN_DEFAULT_TIER=pro" `
    -e "SITEBIN_DOMAIN_VERIFICATION=off" -e "SITEBIN_RATE_AUTH_PER_5MIN=500" -e "SITEBIN_ADMIN_ACCOUNTS=snap@example.com" `
    -e "SITEBIN_SNAPSHOTS_AGENT=/run/sitebin-agent/agent.sock" $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; Cleanup; exit 1 }

$up = $false
for ($i = 0; $i -lt 40; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -in 200, 303) { $up = $true; break } }
Assert "instance up" $up
$agentUp = (docker logs $agent 2>&1 | Out-String).Contains("sitebin agent up")
Assert "agent up" $agentUp
if (-not ($up -and $agentUp)) { docker logs $agent; docker logs $name; Cleanup; exit 1 }

$jar = Join-Path $work "snap-cookies.txt"; Remove-Item $jar -ErrorAction SilentlyContinue
Req "POST" "$origin/account/signup" @("-c", $jar, "--data-urlencode", "email=snap@example.com", "--data-urlencode", "password=password12345") | Out-Null

$idx = Join-Path $work "snap-index.html"; [IO.File]::WriteAllText($idx, "v1")
$abt = Join-Path $work "snap-about.txt"; [IO.File]::WriteAllText($abt, "about")
$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "files=@$idx;filename=index.html", "-F", "files=@$abt;filename=about.txt")
Assert "site created" ($r.code -eq 201) "got $($r.code): $($r.body)"
$site = $r.body | ConvertFrom-Json
$edit = ($site.edit_url -split "/e/")[1]; $pw = $site.edit_password
$view = "http://$($site.id).${base}:$Port"
Assert "the payload offers backups" ($r.body.Contains('"backups_available":true')) $r.body

function Api([string]$m, [string]$path, [string[]]$extra = @()) {
    return Req $m "$origin/api/sites/$edit$path" (@("-H", "X-Edit-Password: $pw") + $extra)
}
function Snap { docker exec $agent sh /cfg/backup-now.sh | Out-Null }
function Versions([int]$atLeast, [int]$seconds) {
    $deadline = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $deadline) {
        $r = Api "GET" "/backups"
        if ($r.code -eq 200) { $l = $r.body | ConvertFrom-Json; if (@($l.versions).Count -ge $atLeast -and $l.pending -eq 0) { return $l } }
        Start-Sleep -Seconds 5
    }
    return ($r.body | ConvertFrom-Json)
}

Snap
[IO.File]::WriteAllText($idx, "v2")
$r = Req "POST" "$origin/api/sites/${edit}/files?replace=true" @("-H", "X-Edit-Password: $pw", "-F", "files=@$idx;filename=index.html")
Assert "replaced with v2" ($r.code -eq 200) "got $($r.code)"
Snap

Write-Host "   (the agent indexes new snapshots once a minute)"
$l = Versions 2 150
$vs = @($l.versions)
Assert "two versions listed" ($vs.Count -eq 2) ($l | ConvertTo-Json -Depth 4)
Assert "the newest is the files now" ($vs.Count -eq 2 -and $vs[0].current -and -not $vs[1].current) ($l | ConvertTo-Json -Depth 4)
Assert "the older has both files (markers not counted)" ($vs.Count -eq 2 -and $vs[1].files -eq 2) "files $($vs[1].files)"
$old = $vs[1].snapshot; $new = $vs[0].snapshot

$zip = Join-Path $work "snap-old.zip"
& curl.exe -s -o $zip -H "X-Edit-Password: $pw" "$origin/api/sites/$edit/backups/$old/download"
Add-Type -AssemblyName System.IO.Compression.FileSystem
$names = @(); try { $z = [IO.Compression.ZipFile]::OpenRead($zip); $names = @($z.Entries | ForEach-Object { $_.FullName } | Sort-Object); $z.Dispose() } catch {}
Assert "download is a ZIP of that version's files" (($names -join ",") -eq "about.txt,index.html") ($names -join ",")

$r = Api "POST" "/backups/$old/restore"
Assert "site restore answers 200" ($r.code -eq 200 -and $r.body.Contains('"restored"')) "got $($r.code): $($r.body)"
$r = Req "GET" "$view/"
Assert "the site serves the restored index.html" ($r.body -eq "v1") "got $($r.code): $($r.body)"
$r = Req "GET" "$view/about.txt"
Assert "the restored about.txt is back" ($r.code -eq 200 -and $r.body -eq "about") "got $($r.code)"
# The snapshot taken before the restore holds the files it replaced (v2): it
# is the newest version now, merged with the earlier snapshot of the same files.
$l = Versions 2 30
$vs = @($l.versions)
$cur = @($vs | Where-Object { $_.current } | ForEach-Object { $_.snapshot })
Assert "a snapshot was taken first: the newest version is a new snapshot" ($vs.Count -ge 2 -and $vs[0].snapshot -ne $new -and $vs[0].first -ne $vs[0].at) ($l | ConvertTo-Json -Depth 4)
Assert "the restored version is the current one" ($cur -contains $old) ($cur -join ",")

# ---- the operator restores the whole instance to the snapshot with v2 ----
$st = (Req "GET" "$origin/account/api/state" @("-b", $jar)).body | ConvertFrom-Json
$csrf = $st.account.csrf
Assert "the operator's state offers the Backups view" ($st.account.backups -eq $true) "backups=$($st.account.backups)"
$b = Req "GET" "$origin/account/api/admin/backups" @("-b", $jar)
Assert "the operator reads the snapshots" ($b.code -eq 200 -and $b.body.Contains($new)) "got $($b.code)"
$r = Req "POST" "$origin/account/admin/backups/restore" @("-b", $jar, "-H", "X-Sitebin-UI: json", "--data-urlencode", "csrf=$csrf", "--data-urlencode", "snapshot=$new", "--data-urlencode", "confirm=no")
Assert "the restore needs the typed word" ($r.code -eq 400) "got $($r.code)"

function InstanceRestore([string]$field, [string]$value) {
    $r = Req "POST" "$origin/account/admin/backups/restore" @("-b", $jar, "-H", "X-Sitebin-UI: json", "--data-urlencode", "csrf=$csrf", "--data-urlencode", "$field=$value", "--data-urlencode", "confirm=restore")
    if ($r.code -ne 202) { return @{ state = "refused $($r.code): $($r.body)" } }
    $deadline = (Get-Date).AddSeconds(300)
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Seconds 4
        $x = Req "GET" "$origin/account/api/admin/backups" @("-b", $jar)
        if ($x.code -ne 200) { continue }
        $s = ($x.body | ConvertFrom-Json).status.restore
        if ($s -and $s.state -in "done", "failed", "rolled-back") { return $s }
    }
    return @{ state = "timeout" }
}
Write-Host "   (the instance stops, swaps its data and waits to be healthy)"
$s = InstanceRestore "snapshot" $new
Assert "instance restore done" ($s.state -eq "done") "$($s.state): $($s.message)"
$r = Req "GET" "$view/"
Assert "the instance serves the snapshot's v2 again" ($r.body -eq "v2") "got $($r.code): $($r.body)"
$copies = @(((Req "GET" "$origin/account/api/admin/backups" @("-b", $jar)).body | ConvertFrom-Json).copies)
Assert "the data from before is kept as a copy" ($copies.Count -eq 1 -and $copies[0].kind -eq "before-restore") ($copies | ConvertTo-Json)

$s = InstanceRestore "copy" $copies[0].name
Assert "switching back to the copy is done" ($s.state -eq "done") "$($s.state): $($s.message)"
$r = Req "GET" "$view/"
Assert "the site restore from before is back (v1)" ($r.body -eq "v1") "got $($r.code): $($r.body)"

$copies = @(((Req "GET" "$origin/account/api/admin/backups" @("-b", $jar)).body | ConvertFrom-Json).copies)
foreach ($c in $copies) {
    Req "POST" "$origin/account/admin/backups/copies/$($c.name)/delete" @("-b", $jar, "-H", "X-Sitebin-UI: json", "--data-urlencode", "csrf=$csrf") | Out-Null
}
$copies = @(((Req "GET" "$origin/account/api/admin/backups" @("-b", $jar)).body | ConvertFrom-Json).copies)
Assert "copies deleted" ($copies.Count -eq 0) ($copies | ConvertTo-Json)

Write-Host ""
Write-Host "passed $($script:pass), failed $($script:fail)" -ForegroundColor $(if ($script:fail) { "Red" } else { "Green" })
if ($Keep) { Write-Host "kept running: $origin (snap@example.com / password12345), agent $agent" } else { Cleanup }
if ($script:fail) { exit 1 }
