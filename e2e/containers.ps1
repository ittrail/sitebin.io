# Sitebin Enterprise -- container sites E2E against the real enterprise image
# and the host's real Docker Engine.
#   powershell -File e2e\containers.ps1 [-Image sitebin:dev-ee]
#
# Needs Docker with internet access: it pulls the two catalogue images and the
# fixture app runs npm install (its service has egress). The first run takes a
# few minutes for the pulls.
param([string]$Image = "sitebin:dev-ee", [int]$Port = 8097)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"; $origin = "http://${base}:$Port"
$name = "sitebin-containers-e2e"; $vol = "sitebin-containers-e2e-data"
$work = Join-Path $PSScriptRoot ".work"; New-Item -ItemType Directory -Force $work | Out-Null
$fx = Join-Path $PSScriptRoot "fixtures\containers"

$script:pass = 0; $script:fail = 0
function Assert([string]$n, [bool]$c, [string]$d = "") {
    if ($c) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}
function Req([string]$m, [string]$u, [string[]]$x = @()) {
    $bf = Join-Path $work "ct-b.tmp"
    Remove-Item $bf -ErrorAction SilentlyContinue
    $code = & curl.exe @(@("-s", "-X", $m, "-o", $bf, "-w", "%{http_code}", "--max-time", "60") + $x + @($u))
    $body = ""; if (Test-Path $bf) { $body = [IO.File]::ReadAllText($bf) }
    return @{ code = [int]$code; body = $body }
}
function JsonBodyT([string]$json) {
    $p = Join-Path $work ("ct" + (Get-Random) + ".json"); [IO.File]::WriteAllText($p, $json); return "@$p"
}
function Cleanup {
    docker rm -f $name 2>$null | Out-Null
    $ids = docker ps -aq --filter "label=io.sitebin.managed=true"
    if ($ids) { docker rm -f $ids 2>$null | Out-Null }
    $nets = docker network ls -q --filter "label=io.sitebin.managed=true"
    if ($nets) { docker network rm $nets 2>$null | Out-Null }
    docker volume rm $vol 2>$null | Out-Null
}
# Wait until $cond (a scriptblock over the site payload) holds, or time out.
function WaitSite([string]$edit, [string]$pw, [scriptblock]$cond, [int]$seconds) {
    $deadline = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $deadline) {
        $r = Req "GET" "$origin/api/sites/$edit" @("-H", "X-Edit-Password: $pw")
        if ($r.code -eq 200) {
            $s = $r.body | ConvertFrom-Json
            if (& $cond $s) { return $s }
        }
        Start-Sleep -Seconds 3
    }
    return $null
}
function WaitHost([string]$url, [string]$want, [int]$seconds) {
    $deadline = (Get-Date).AddSeconds($seconds)
    $last = $null
    while ((Get-Date) -lt $deadline) {
        $last = Req "GET" $url
        if ($last.code -eq 200 -and $last.body.Contains($want)) { return $last }
        Start-Sleep -Seconds 3
    }
    return $last
}

$tiers = '[{"id":"pro","label":"Pro","max_site_bytes":1073741824,"max_files":5000,"max_sites":10,"webdav":true,"custom_domains":5,"max_expiry_days":0,"max_containers":3,"trusted":true}]'
[IO.File]::WriteAllText((Join-Path $work "ct-tiers.json"), $tiers)
$workDocker = ($work -replace '\\', '/')

Write-Host "== starting enterprise image with SITEBIN_CONTAINERS=docker on $Port" -ForegroundColor Cyan
Cleanup
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" -v "${workDocker}:/cfg:ro" `
    -v "/var/run/docker.sock:/var/run/docker.sock" --group-add 0 `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_ACCOUNT_MODE=tiers" -e "SITEBIN_TIERS_FILE=/cfg/ct-tiers.json" -e "SITEBIN_DEFAULT_TIER=pro" `
    -e "SITEBIN_DOMAIN_VERIFICATION=off" -e "SITEBIN_RATE_AUTH_PER_5MIN=500" `
    -e "SITEBIN_CONTAINERS=docker" $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; exit 1 }

$up = $false
for ($i = 0; $i -lt 40; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break } }
Assert "container up" $up
if (-not $up) { docker logs $name; Cleanup; exit 1 }
$ready = $false
for ($i = 0; $i -lt 20; $i++) { if ((docker logs $name 2>&1 | Out-String).Contains("containers: runtime ready")) { $ready = $true; break }; Start-Sleep -Seconds 1 }
Assert "runtime found its own container and data volume" $ready
if (-not $ready) { docker logs $name 2>&1 | Select-String "containers" }

$jar = Join-Path $work "ct-cookies.txt"; Remove-Item $jar -ErrorAction SilentlyContinue
Req "POST" "$origin/account/signup" @("-c", $jar, "--data-urlencode", "email=ct@example.com", "--data-urlencode", "password=password12345") | Out-Null

# ---- a file site, for the header-spoofing check ----
$idx = Join-Path $work "ct-index.html"; [IO.File]::WriteAllText($idx, "static page")
$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "files=@$idx;filename=index.html")
$web = $r.body | ConvertFrom-Json
$r = Req "GET" "http://$($web.id).${base}:$Port/" @("-H", "X-Sitebin-Upstream: 127.0.0.1:9000")
Assert "a client-sent upstream header is ignored on a file site" ($r.code -eq 200 -and $r.body -eq "static page") "got $($r.code): $($r.body)"

# ---- the project ----
$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "mode=container",
    "-F", "files=@$fx\sitebin-container-compose.yaml;filename=sitebin-container-compose.yaml",
    "-F", "files=@$fx\app\package.json;filename=app/package.json",
    "-F", "files=@$fx\app\server.js;filename=app/server.js")
Assert "container site created" ($r.code -eq 201) "got $($r.code): $($r.body)"
if ($r.code -ne 201) { docker logs $name; Cleanup; exit 1 }
$site = $r.body | ConvertFrom-Json
$edit = ($site.edit_url -split "/e/")[1]; $pw = $site.edit_password
$view = "http://$($site.id).${base}:$Port/"
Assert "mode is container" ($site.mode -eq "container") "got $($site.mode)"

Write-Host "   (pulling images, installing the app, initialising MySQL -- this takes a while)"
$s = WaitSite $edit $pw { param($s) $s.container.status -eq "running" -or $s.container.status -eq "error" } 600
Assert "project running" ($s -and $s.container.status -eq "running") "status: $($s.container.status) $($s.container.message)"
if (-not $s -or $s.container.status -ne "running") { docker logs $name 2>&1 | Select-String "containers"; Cleanup; exit 1 }
Assert "three services reported" ($s.container.services.Count -eq 3)
$r = WaitHost $view "mysql=8.4.11" 300
Assert "the site's own address reaches the app, which reaches MySQL by service name" ($r.code -eq 200 -and $r.body.Contains("mysql=8.4.11")) "got $($r.code): $($r.body)"
Assert "the app runs as Sitebin's uid, not root" ($r.body.Contains("uid=1000")) "got $($r.body)"
Assert "the environment is passed verbatim" ($r.body.Contains("greeting=hello"))

$r = Req "GET" "http://second.localtest.me:$Port/"
Assert "a custom domain maps to a second port" ($r.code -eq 200 -and $r.body.Contains("second port")) "got $($r.code): $($r.body)"
$r = WaitHost "http://probe.localtest.me:$Port/" "egress" 60
Assert "a service without egress cannot reach the internet" ($r.body -eq "egress blocked") "got $($r.code): $($r.body)"

$r = Req "GET" "$origin/api/sites/$edit/containers/app/logs?tail=50" @("-H", "X-Edit-Password: $pw")
Assert "service logs are readable" ($r.code -eq 200) "got $($r.code)"

# ---- the site's files ARE the volumes ----
$r = Req "GET" "$origin/api/sites/$edit" @("-H", "X-Edit-Password: $pw")
$s = $r.body | ConvertFrom-Json
$paths = @($s.files | ForEach-Object { $_.path })
Assert "MySQL's data lands in the site's db folder" (@($paths | Where-Object { $_ -like "db/*" }).Count -gt 5)
Assert "npm's install lands in the site's app folder" (@($paths | Where-Object { $_ -like "app/node_modules/*" }).Count -gt 5)
$planted = (docker exec "sb-$($site.id)-app" readlink /usr/src/app/leak 2>&1 | Out-String).Trim()
Assert "the app really planted a link out of its volume (/data)" ($planted -eq "/data") "readlink: $planted"
Assert "the link the app planted is not listed" (-not ($paths -contains "app/leak"))
$r = Req "GET" "$origin/api/sites/$edit/content/app/leak/sites/$($site.id)/meta.json" @("-H", "X-Edit-Password: $pw")
Assert "the API does not follow a container's link out of the site" ($r.code -ne 200) "got $($r.code): $($r.body)"
$r = Req "POST" "$origin/api/sites/$edit/domains" @("-H", "X-Edit-Password: $pw", "-H", "Content-Type: application/json", "--data", (JsonBodyT '{"domain":"other.localtest.me"}'))
Assert "domains of a container site come from the compose file (409)" ($r.code -eq 409) "got $($r.code)"

# ---- a change to the compose file restarts the project ----
$compose = [IO.File]::ReadAllText("$fx\sitebin-container-compose.yaml").Replace("GREETING: hello", "GREETING: changed")
$c2 = Join-Path $work "ct-compose.yaml"; [IO.File]::WriteAllText($c2, $compose)
$r = Req "POST" "$origin/api/sites/$edit/files" @("-H", "X-Edit-Password: $pw", "-F", "files=@$c2;filename=sitebin-container-compose.yaml")
Assert "compose file replaced" ($r.code -eq 200) "got $($r.code)"
$r = WaitHost $view "greeting=changed" 180
Assert "the project restarted with the new environment" ($r.body.Contains("greeting=changed")) "got $($r.code): $($r.body)"

# ---- the plan's cap spans every project ----
$one = Join-Path $work "ct-one.yaml"; [IO.File]::WriteAllText($one, "services:`n  web:`n    image: alpine-node-22`n")
$r = Req "POST" "$origin/api/sites" @("-b", $jar, "-F", "mode=container", "-F", "files=@$one;filename=sitebin-container-compose.yaml")
$second = $r.body | ConvertFrom-Json
$e2 = ($second.edit_url -split "/e/")[1]
$s2 = WaitSite $e2 $second.edit_password { param($s) $s.container.status -eq "error" -or $s.container.status -eq "running" } 60
Assert "a fourth container is refused across projects" ($s2 -and $s2.container.status -eq "error" -and $s2.container.message.Contains("allows 3")) "got $($s2.container.status): $($s2.container.message)"

# ---- stop, start ----
$r = Req "POST" "$origin/api/sites/$edit/containers/stop" @("-H", "X-Edit-Password: $pw")
Assert "stop accepted" ($r.code -eq 202) "got $($r.code)"
$r = Req "GET" $view
Assert "a stopped project answers 503, never its files" ($r.code -eq 503) "got $($r.code)"
$r = Req "POST" "$origin/api/sites/$edit/containers/start" @("-H", "X-Edit-Password: $pw")
Assert "start accepted" ($r.code -eq 202) "got $($r.code)"
$r = WaitHost $view "mysql=8.4.11" 240
Assert "started again, with its data" ($r.code -eq 200) "got $($r.code): $($r.body)"

# ---- leaving container mode ----
$r = Req "PUT" "$origin/api/sites/$edit" @("-H", "X-Edit-Password: $pw", "-H", "Content-Type: application/json", "--data", (JsonBodyT '{"mode":"webserver"}'))
Assert "switch back to web server" ($r.code -eq 200) "got $($r.code): $($r.body)"
$left = docker ps -aq --filter "label=io.sitebin.site=$($site.id)"
Assert "the purge removed the planted link" ((docker logs $name 2>&1 | Out-String) -match "removed links left by containers")
Assert "its containers are gone" (-not $left)
$r = Req "GET" "${view}app/leak/"
Assert "the container's link was purged before files were served" ($r.code -eq 404) "got $($r.code)"
$r = Req "GET" "${view}app/server.js"
Assert "the files are served as a web site again" ($r.code -eq 200 -and $r.body.Contains("createServer"))

# ---- deleting a container site ----
$r = Req "PUT" "$origin/api/sites/$e2" @("-H", "X-Edit-Password: $($second.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBodyT '{"mode":"webserver"}'))
$r = Req "DELETE" "$origin/api/sites/$edit" @("-H", "X-Edit-Password: $pw")
Assert "site deleted" ($r.code -eq 200) "got $($r.code)"
$nets = docker network ls -q --filter "label=io.sitebin.site=$($site.id)"
Assert "its networks are gone" (-not $nets)

Cleanup
Write-Host ""
Write-Host ("== containers E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
