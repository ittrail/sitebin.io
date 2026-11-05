# Sitebin -- site analytics E2E against the real community image and Caddy.
#
#   powershell -File e2e\analytics.ps1 [-Image sitebin:dev]
#
# Checks, through the real Caddy: server mode counts three browser navigations
# and ignores a bot and a prefetch; script mode serves the Umami-compatible
# tracker and records a page view and an event sent to /_sitebin/analytics/send;
# the visitor's opt-out keeps a browser out of the count; a funnel answers; and
# with analytics off the script is a stub. A host/path (mounted) address is an
# enterprise feature, so the beacon under a mount is covered by the Go suite
# and not here.
param(
    [string]$Image = "sitebin:dev",
    [int]$Port = 8092
)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$name = "sitebin-analytics-e2e"
$vol = "sitebin-analytics-e2e-data"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null
$chrome = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

# Every assertion must run; the total is checked at the end. Update it when
# you add or remove one.
$ExpectedAssertions = 33
$script:pass = 0; $script:fail = 0
function Assert([string]$n, $c, [string]$d = "") {
    $ok = $false
    if ($null -ne $c) {
        if ($c -is [bool]) { $ok = $c }
        elseif ($c -is [array]) { $ok = ($c.Count -gt 0) }
        else { $ok = [bool]$c }
    }
    if ($ok) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}

function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = Join-Path $work "an.tmp"
    $hdrFile = Join-Path $work "an.hdr"
    Remove-Item $bodyFile, $hdrFile -ErrorAction SilentlyContinue
    $a = @("-s", "-X", $method, "-o", $bodyFile, "-D", $hdrFile, "-w", "%{http_code}", "--max-time", "30") + $extra + @($url)
    $out = "$(& curl.exe @a)"
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    $hdr = ""; if (Test-Path $hdrFile) { $hdr = [IO.File]::ReadAllText($hdrFile) }
    return @{ code = [int]$out; body = $body; headers = $hdr }
}

function JsonFile([string]$json) {
    $p = Join-Path $work ("aj" + (Get-Random) + ".json"); [IO.File]::WriteAllText($p, $json); return "@$p"
}

# A browser navigation, as Caddy and authz see one.
function Browse([string]$url, [string[]]$more = @()) {
    $h = @("-H", "User-Agent: $chrome", "-H", "Accept: text/html", "-H", "Sec-Fetch-Dest: document", "-H", "Sec-Fetch-Mode: navigate", "-H", "Sec-Fetch-Site: none")
    return Req "GET" $url ($h + $more)
}

# The tracker's beacon: text/plain, a payload whose url is on the request host.
function Send([string]$siteUrl, [string]$payload) {
    $h = @("-H", "User-Agent: $chrome", "-H", "Content-Type: text/plain", "-H", "Sec-Fetch-Site: same-origin", "--data-binary", (JsonFile $payload))
    return Req "POST" "$siteUrl/_sitebin/analytics/send" $h
}

function Cleanup {
    docker rm -f $name 2>$null | Out-Null
    docker volume rm $vol 2>$null | Out-Null
}

Write-Host "== starting $Image on $Port" -ForegroundColor Cyan
Cleanup
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; Cleanup; exit 1 }
$up = $false
for ($i = 0; $i -lt 40; $i++) {
    Start-Sleep -Milliseconds 700
    if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break }
}
Assert "container up" $up
if (-not $up) { docker logs $name; Cleanup; exit 1 }

Write-Host "== a site with analytics on" -ForegroundColor Cyan
$page = Join-Path $work "an-index.html"; [IO.File]::WriteAllText($page, "<h1>analytics e2e</h1>")
$r = Req "POST" "$origin/api/sites" @("-H", "Sec-Fetch-Site: same-origin", "-F", "files=@$page;filename=index.html")
Assert "site created" ($r.code -eq 201) "$($r.code) $($r.body)"
$site = $r.body | ConvertFrom-Json
$edit = ($site.edit_url -split "/e/")[1]
$pw = @("-H", "X-Edit-Password: $($site.edit_password)")
$json = @("-H", "Content-Type: application/json")
$siteOrigin = $site.view_url.TrimEnd("/")
$siteHost = ([uri]$siteOrigin).Authority
$api = "$origin/api/sites/$edit/analytics"

function Put([string]$body) { return Req "PUT" $api ($pw + $json + @("--data-binary", (JsonFile $body))) }
function Overview { $r = Req "GET" "$api/overview?range=last24h" $pw; return ($r.body | ConvertFrom-Json) }

$r = Req "GET" $api $pw
$state = $r.body | ConvertFrom-Json
Assert "analytics is off until the owner enables it" ($r.code -eq 200 -and $state.available -and -not $state.settings.enabled) "$($r.code) $($r.body)"
$r = Browse "$siteOrigin/"
Assert "a page served while off" ($r.code -eq 200) "$($r.code)"
$r = Put '{"enabled":true}'
Assert "analytics enabled (server mode)" ($r.code -eq 200) "$($r.code) $($r.body)"

Write-Host "== server mode counts navigations" -ForegroundColor Cyan
1..3 | ForEach-Object { Browse "$siteOrigin/" | Out-Null }
Req "GET" "$siteOrigin/" @("-H", "User-Agent: curl/8.4", "-H", "Accept: */*") | Out-Null
Browse "$siteOrigin/" @("-H", "Sec-Purpose: prefetch") | Out-Null
Start-Sleep -Seconds 2
$ov = Overview
Assert "three navigations count; a bot and a prefetch do not" ($ov.totals.views -eq 3) "views=$($ov.totals.views)"
Assert "they are one visitor" ($ov.totals.visitors -eq 1) "visitors=$($ov.totals.visitors)"
$r = Req "GET" "$api/table?dim=path&range=last24h" $pw
Assert "the path table lists /" (($r.body | ConvertFrom-Json).rows[0].value -eq "/") "$($r.body)"

Write-Host "== script mode" -ForegroundColor Cyan
$r = Put '{"enabled":true,"mode":"script"}'
Assert "script mode set" ($r.code -eq 200) "$($r.code) $($r.body)"
$r = Req "GET" "$siteOrigin/_sitebin/analytics.js"
Assert "the tracker is JavaScript with nosniff" ($r.code -eq 200 -and $r.headers -match "(?im)^content-type:\s*text/javascript" -and $r.headers -match "(?im)^x-content-type-options:\s*nosniff") "$($r.code)"
Assert "the tracker speaks Umami" ($r.body.Contains("umami"))
Assert "the tracker is cacheable for an hour" ($r.headers -match "(?im)^cache-control:.*max-age=3600")

$before = (Overview).totals.views
$r = Send $siteOrigin ('{"type":"event","payload":{"url":"http://' + $siteHost + '/","title":"Home","language":"de-AT","screen":"1920x1080"}}')
Assert "a page view beacon answers 204" ($r.code -eq 204) "$($r.code)"
$r = Send $siteOrigin ('{"type":"event","payload":{"url":"http://' + $siteHost + '/","name":"anfrage","data":{"seite":"/"}}}')
Assert "an event beacon answers 204" ($r.code -eq 204) "$($r.code)"
$r = Send $siteOrigin '{"type":"event","payload":{"url":"http://elsewhere.example/","name":"fremd"}}'
Assert "a beacon for another host answers 204 too" ($r.code -eq 204) "$($r.code)"
Start-Sleep -Seconds 2
$ov = Overview
Assert "the script's page view adds one view" ($ov.totals.views -eq $before + 1) "before=$before now=$($ov.totals.views)"
$r = Req "GET" "$api/table?dim=event&range=last24h" $pw
$ev = ($r.body | ConvertFrom-Json).rows
Assert "the event table has anfrage" ((@($ev) | Where-Object { $_.value -eq "anfrage" }).Count -eq 1) "$($r.body)"
Assert "a beacon for another host recorded nothing" ((@($ev) | Where-Object { $_.value -eq "fremd" }).Count -eq 0) "$($r.body)"
$r = Req "GET" "$api/event-data?name=anfrage&range=last24h" $pw
Assert "event data keeps the property" ($r.body.Contains("seite")) "$($r.body)"
$beforeNav = (Overview).totals.views
Browse "$siteOrigin/" | Out-Null
Start-Sleep -Seconds 2
Assert "script mode does not count navigations" ((Overview).totals.views -eq $beforeNav)

Write-Host "== the visitor's opt-out" -ForegroundColor Cyan
$r = Put '{"enabled":true,"mode":"server"}'
Assert "server mode again" ($r.code -eq 200) "$($r.code)"
$r = Req "GET" "$siteOrigin/_sitebin/analytics/opt-out"
Assert "the opt-out page cannot be framed" ($r.code -eq 200 -and $r.headers -match "(?im)^content-security-policy:.*frame-ancestors 'none'") "$($r.code)"
$r = Req "POST" "$siteOrigin/_sitebin/analytics/opt-out" @("-H", "Sec-Fetch-Site: cross-site", "--data", "action=out")
Assert "a cross-site opt-out is refused" ($r.code -eq 403 -and $r.headers -notmatch "(?im)^set-cookie:") "$($r.code)"
$r = Req "POST" "$siteOrigin/_sitebin/analytics/opt-out" @("-H", "Sec-Fetch-Site: same-origin", "--data", "action=out")
$cookie = [regex]::Match($r.headers, "(?im)^set-cookie:\s*(sitebin_analytics_optout=[^;]+)").Groups[1].Value
Assert "opting out sets the cookie" ($r.code -eq 200 -and $cookie -ne "") "$($r.code)"
$v0 = (Overview).totals.views
Browse "$siteOrigin/" @("-H", "Cookie: $cookie") | Out-Null
Start-Sleep -Seconds 2
Assert "an opted-out browser is not counted" ((Overview).totals.views -eq $v0)
Browse "$siteOrigin/" | Out-Null
Start-Sleep -Seconds 2
Assert "the same browser without the cookie is" ((Overview).totals.views -eq $v0 + 1)

Write-Host "== a funnel" -ForegroundColor Cyan
$settings = '{"enabled":true,"mode":"server","funnels":[{"id":"f1","name":"Home to request","window_minutes":60,"steps":[{"type":"path","value":"/"},{"type":"event","value":"anfrage"}]}]}'
$r = Put $settings
Assert "a funnel is saved" ($r.code -eq 200) "$($r.code) $($r.body)"
$r = Req "GET" "$api/funnels?range=last24h" $pw
$fun = ($r.body | ConvertFrom-Json).funnels
Assert "the funnel answers with a result per step" ($r.code -eq 200 -and @($fun[0].steps).Count -eq 2) "$($r.code) $($r.body)"
Assert "its first step has the visitor" ($fun[0].steps[0].visitors -ge 1) "$($r.body)"

Write-Host "== off again: the script is a stub" -ForegroundColor Cyan
$r = Put '{"enabled":false}'
Assert "analytics switched off" ($r.code -eq 200) "$($r.code) $($r.body)"
$r = Req "GET" "$siteOrigin/_sitebin/analytics.js"
Assert "the stub has track but no beacon" ($r.code -eq 200 -and $r.body.Contains("track") -and -not $r.body.Contains("/_sitebin/analytics/send"))
$r = Req "GET" "$api/overview?range=last24h" $pw
Assert "recorded data stays readable" ($r.code -eq 200) "$($r.code)"

$logs = (docker logs $name 2>&1 | Out-String)
Assert "logs carry no user agent" (-not $logs.Contains("Chrome/126"))

Cleanup
Write-Host ""
$total = $script:pass + $script:fail
if ($total -ne $ExpectedAssertions) {
    Write-Host ("  FAIL assertion count: ran {0}, expected {1} - an assertion was skipped or threw" -f $total, $ExpectedAssertions) -ForegroundColor Red
    $script:fail++
}
Write-Host ("== analytics E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
