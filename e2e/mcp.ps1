# Sitebin — MCP endpoint E2E against the real image.
#
#   powershell -File e2e\mcp.ps1 [-Image sitebin:latest]
#
# Verifies the Model Context Protocol surface end to end over HTTP: the
# handshake, the tool catalog, publishing a site with create_site, that the
# published site actually serves, that meta.json records origin=mcp, and that
# the tools honour the edit password. Runs the community image, where MCP is
# open — the gated paths are covered by the Go suite, which can register a
# provider without standing up an accounts instance.
param(
    [string]$Image = "sitebin:latest",
    [int]$Port = 8089
)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$mcp = "$origin/mcp"
$name = "sitebin-mcp-e2e"
$vol = "sitebin-mcp-e2e-data"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

# Every assertion in this script must run. If one is skipped — a throw, an
# early return — the totals still look healthy, so the count is checked at the
# end against this number. Update it when you add or remove an assertion.
$ExpectedAssertions = 37
$script:pass = 0; $script:fail = 0
# $c is deliberately untyped. With [bool], PowerShell throws on anything it
# cannot coerce — an array from a multi-line command substitution, say — and a
# thrown assertion never runs, never counts, and never fails the script. That
# silently shrinks the total while the run still reports success.
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
    $bodyFile = Join-Path $work "b.tmp"
    $args = @("-s", "-X", $method, "-o", $bodyFile, "-w", "%{http_code}", "--max-time", "30") + $extra + @($url)
    $code = & curl.exe @args
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    return @{ code = [int]$code; body = $body }
}

# Rpc posts one JSON-RPC message and returns the parsed result. The server is
# stateless, so every call stands alone — there is no session id to carry.
# Responses are SSE-framed ("data: {...}"), which is normal for streamable HTTP.
$script:rpcID = 0
function Rpc([string]$method, $params) {
    $script:rpcID++
    $msg = @{ jsonrpc = "2.0"; id = $script:rpcID; method = $method }
    if ($null -ne $params) { $msg.params = $params }
    $json = $msg | ConvertTo-Json -Depth 20 -Compress
    $p = Join-Path $work ("rpc" + $script:rpcID + ".json")
    [IO.File]::WriteAllText($p, $json)
    $r = Req "POST" $mcp @(
        "-H", "Content-Type: application/json",
        "-H", "Accept: application/json, text/event-stream",
        "-H", "MCP-Protocol-Version: 2025-06-18",
        "--data-binary", "@$p")
    $payload = $r.body
    # Strip SSE framing if present.
    $line = ($payload -split "`n" | Where-Object { $_ -match '^data: ' } | Select-Object -First 1)
    if ($line) { $payload = $line.Substring(6) }
    $parsed = $null
    try { $parsed = $payload | ConvertFrom-Json } catch {}
    return @{ code = $r.code; raw = $r.body; msg = $parsed }
}

function CallTool([string]$tool, $arguments) {
    return Rpc "tools/call" @{ name = $tool; arguments = $arguments }
}

# ToolResult digs the structured payload out of a tools/call response.
function ToolStruct($r) {
    if ($null -eq $r.msg -or $null -eq $r.msg.result) { return $null }
    return $r.msg.result.structuredContent
}

# ToolAnswered reports whether the call completed at the protocol level at all,
# regardless of whether the tool then refused. Every refusal assertion has to
# check this first: without it, a server that is completely broken — /mcp not
# mounted, a 404, a transport error — makes every "must be refused" test pass
# vacuously, which is exactly the false green this script exists to prevent.
function ToolAnswered($r) {
    return ($null -ne $r.msg -and $null -ne $r.msg.result)
}

# ToolIsError reports a tool-level refusal. It is only meaningful when
# ToolAnswered is true; see above.
function ToolIsError($r) {
    if (-not (ToolAnswered $r)) { return $false }
    return [bool]$r.msg.result.isError
}

# ToolRefused is what the refusal assertions should use: the call reached the
# tool AND the tool said no.
function ToolRefused($r) {
    return ((ToolAnswered $r) -and (ToolIsError $r))
}
function ToolText($r) {
    if ($null -eq $r.msg -or $null -eq $r.msg.result -or $null -eq $r.msg.result.content) { return "" }
    return ($r.msg.result.content | ForEach-Object { $_.text }) -join ""
}

Write-Host "== starting image on $Port" -ForegroundColor Cyan
docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_RATE_AUTH_PER_5MIN=200" `
    $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; exit 1 }

$up = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break } }
Assert "container up" $up
if (-not $up) { docker logs $name; docker rm -f $name | Out-Null; exit 1 }

Write-Host "== handshake" -ForegroundColor Cyan
$r = Rpc "initialize" @{
    protocolVersion = "2025-06-18"
    capabilities    = @{}
    clientInfo      = @{ name = "sitebin-e2e"; version = "1" }
}
Assert "initialize answers 200" ($r.code -eq 200) "$($r.code) $($r.raw)"
Assert "server identifies itself" ($null -ne $r.msg -and $r.msg.result.serverInfo.name -eq "sitebin") "$($r.raw)"
Assert "instructions briefed the client" ($r.msg.result.instructions -match "edit_password")
Assert "tools capability advertised" ($null -ne $r.msg.result.capabilities.tools)

Write-Host "== tool catalog" -ForegroundColor Cyan
$r = Rpc "tools/list" @{}
$tools = @()
if ($null -ne $r.msg -and $null -ne $r.msg.result) { $tools = $r.msg.result.tools | ForEach-Object { $_.name } }
foreach ($t in @("create_site", "list_sites", "get_site", "update_site", "list_files",
        "read_file", "write_files", "delete_file", "delete_site",
        "add_domain", "remove_domain", "download_site")) {
    Assert "tool $t present" ($tools -contains $t)
}

Write-Host "== publish through create_site" -ForegroundColor Cyan
$r = CallTool "create_site" @{
    files = @(
        @{ path = "index.html"; text = "<h1>mcp-e2e-ok</h1>" },
        @{ path = "assets/app.js"; text = "console.log(1)" }
    )
}
Assert "create_site succeeded" (-not (ToolIsError $r)) (ToolText $r)
$site = ToolStruct $r
Assert "returned a view url" ($null -ne $site -and $site.view_url -match "http")
Assert "returned an edit id" ($null -ne $site -and $site.edit_id.Length -gt 0)
Assert "returned the one-time edit password" ($null -ne $site -and $site.edit_password.Length -gt 0)

$editID = $site.edit_id
$pw = $site.edit_password

# The site must actually be on the web — a tool result that says "published"
# while nothing serves is the failure this whole script exists to catch.
$viewURL = $site.view_url
if ($viewURL -notmatch ":$Port") { $viewURL = $viewURL -replace "//([^/]+)", "//`$1:$Port" }
$r2 = Req "GET" $viewURL
Assert "published site serves its content" ($r2.code -eq 200 -and $r2.body -match "mcp-e2e-ok") "$($r2.code) $($r2.body)"

Write-Host "== provenance" -ForegroundColor Cyan
# Read the field straight out of the site's own meta.json. The site id is
# known, so there is no globbing and nothing reads stdin.
$viewID = $site.id
$meta = (docker exec $name sh -c "cat /data/sites/$viewID/meta.json") -join "`n"
Assert "meta.json records origin=mcp" ($meta -match '"origin":\s*"mcp"') "$meta"

Write-Host "== reading back" -ForegroundColor Cyan
$r = CallTool "list_files" @{ edit_id = $editID; edit_password = $pw }
Assert "list_files sees both files" ((ToolText $r) -match "index.html" -and (ToolText $r) -match "assets/app.js") (ToolText $r)

$r = CallTool "read_file" @{ edit_id = $editID; edit_password = $pw; path = "assets/app.js" }
Assert "read_file returns the text" ((ToolStruct $r).text -eq "console.log(1)") (ToolText $r)

$r = CallTool "get_site" @{ edit_id = $editID; edit_password = $pw }
Assert "get_site reports the file count" ((ToolStruct $r).file_count -ge 2) (ToolText $r)

Write-Host "== the password gate" -ForegroundColor Cyan
$r = CallTool "get_site" @{ edit_id = $editID; edit_password = "wrong-password" }
Assert "wrong edit_password refused" (ToolRefused $r) (ToolText $r)

$r = CallTool "get_site" @{ edit_id = $editID }
Assert "missing edit_password refused" (ToolRefused $r) (ToolText $r)

$r = CallTool "get_site" @{ edit_id = "definitelynotasite"; edit_password = "x" }
Assert "unknown edit_id refused" (ToolRefused $r) (ToolText $r)

Write-Host "== writing" -ForegroundColor Cyan
$r = CallTool "write_files" @{
    edit_id = $editID; edit_password = $pw
    files   = @(@{ path = "about.html"; text = "<p>about</p>" })
}
Assert "write_files added a file" (-not (ToolIsError $r)) (ToolText $r)
$aboutURL = ($viewURL.TrimEnd("/")) + "/about.html"
Assert "the new file serves" ((Req "GET" $aboutURL).body -match "about") ""

$r = CallTool "write_files" @{
    edit_id = $editID; edit_password = $pw; replace = $true
    files   = @(@{ path = "index.html"; text = "<h1>replaced</h1>" })
}
Assert "replace emptied the site first" (-not (ToolIsError $r) -and (ToolStruct $r).file_count -eq 1) (ToolText $r)

Write-Host "== custom domains are enterprise-only here" -ForegroundColor Cyan
$r = CallTool "add_domain" @{ edit_id = $editID; edit_password = $pw; domain = "docs.example.com" }
Assert "add_domain refused in the community image" (ToolRefused $r) (ToolText $r)

Write-Host "== deleting" -ForegroundColor Cyan
$r = CallTool "delete_site" @{ edit_id = $editID; edit_password = $pw }
Assert "delete_site succeeded" (-not (ToolIsError $r)) (ToolText $r)
Assert "the site stops serving" ((Req "GET" $viewURL).code -ne 200)

Write-Host "== the switch" -ForegroundColor Cyan
docker rm -f $name 2>$null | Out-Null
docker run -d --name $name -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_MCP_ENABLED=false" `
    $Image | Out-Null
$up = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 700; if ((Req "GET" "$origin/").code -eq 200) { $up = $true; break } }
Assert "container up with MCP disabled" $up
$r = Rpc "initialize" @{ protocolVersion = "2025-06-18"; capabilities = @{}; clientInfo = @{ name = "x"; version = "1" } }
Assert "SITEBIN_MCP_ENABLED=false unmounts /mcp" ($r.code -eq 404) "$($r.code) $($r.raw)"

# cleanup
docker rm -f $name 2>$null | Out-Null
docker volume rm $vol 2>$null | Out-Null

Write-Host ""
$total = $script:pass + $script:fail
if ($total -ne $ExpectedAssertions) {
    Write-Host ("  FAIL assertion count: ran {0}, expected {1} - an assertion was skipped or threw" -f $total, $ExpectedAssertions) -ForegroundColor Red
    $script:fail++
}
Write-Host ("== MCP E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
