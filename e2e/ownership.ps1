# Sitebin Enterprise -- site ownership E2E against the real enterprise image.
#
#   powershell -File e2e\ownership.ps1 [-Image sitebin:dev-ee] [-Port 8090] [-MailPort 8029]
#
# Runs Mailpit as the account mailer's SMTP server (SITEBIN_SMTP_*) on a
# private Docker network. Tiers mode: "free" (no sharing), "pro" (sharing) and
# the anonymous tier "drop"; plans are switched through the dashboard
# (SITEBIN_TIER_SELF_SELECT). Three local accounts A, B and C sign up and are
# verified through the link Mailpit received; A and B switch to pro, C stays
# on free. Verifies:
#   1. Adopt: A takes an anonymous drop into the account with its view URL and
#      edit password. It is listed on A's dashboard, the edit password still
#      works, and a wrong password answers "URL or password wrong.".
#   2. Transfer: A offers two sites to B's address and gets the never-reveal
#      answer. B's dashboard shows "Ready to take over"; B accepts both at
#      once and the answer shows the two new edit passwords. Each old password
#      fails (401), each new one works, and A's dashboard no longer lists them.
#   3. Share: B invites C. C, on free, sees that the plan cannot and is
#      refused; C switches to pro and accepts. C changes a setting through the
#      session (X-Sitebin-Session: 1, same-origin), as the edit page does;
#      C's DELETE answers 403 "only the site's owner can do this"; C leaves,
#      and C's session has no standing on the site any more.
#   4. Free: C switches back to free. B's offer to C's address gets the
#      never-reveal answer (so does A's to an address without an account), and
#      C's accept is refused with the plan text.
#   5. Mail: Mailpit received verification, offer, accepted, invitation, share
#      accepted and share ended, each by the recipient it is for, and nothing
#      went to an address without a verified account.
# Cleanup always runs: no container, network or volume is left behind.
param(
    [string]$Image = "sitebin:dev-ee",
    [int]$Port = 8090,
    [int]$MailPort = 8029
)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$mailApi = "http://localhost:$MailPort/api/v1"
$name = "sitebin-own-e2e"
$mailName = "sitebin-own-e2e-mail"
$net = "sitebin-own-e2e-net"
$vol = "sitebin-own-e2e-data"
$work = Join-Path $PSScriptRoot ".work"
$cfgDir = Join-Path $work "own-cfg"
New-Item -ItemType Directory -Force $cfgDir | Out-Null

$emailA = "own-a@example.com"
$emailB = "own-b@example.com"
$emailC = "own-c@example.com"
$ghost = "own-ghost@example.com"
$jarA = Join-Path $work "own-a.txt"
$jarB = Join-Path $work "own-b.txt"
$jarC = Join-Path $work "own-c.txt"

# the never-reveal answer to an offer or an invitation (its ASCII start)
$neverReveal = "If an account with this address exists, it has been notified."
$acceptBy = (Get-Date).ToUniversalTime().AddDays(7).ToString("yyyy-MM-dd")

$script:pass = 0; $script:fail = 0
function Assert([string]$n, [bool]$c, [string]$d = "") {
    if ($c) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}

# curl; returns @{code; body; headers; location}
function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = Join-Path $work "own-b.tmp"; $hdrFile = Join-Path $work "own-h.tmp"
    Remove-Item $bodyFile, $hdrFile -ErrorAction SilentlyContinue
    $a = @("-s", "-X", $method, "-o", $bodyFile, "-D", $hdrFile, "-w", "%{http_code}", "--max-time", "30") + $extra + @($url)
    $code = & curl.exe @a
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    $headers = ""; if (Test-Path $hdrFile) { $headers = [IO.File]::ReadAllText($hdrFile) }
    $loc = ""; if ($headers -match "(?im)^Location:\s*(\S+)") { $loc = $Matches[1] }
    return @{ code = [int]$code; body = $body; headers = $headers; location = $loc }
}

# PS 5.1: quoted JSON to curl via a temp file
function JsonFile([string]$json) {
    $p = Join-Path $work ("own-j" + (Get-Random) + ".json"); [IO.File]::WriteAllText($p, $json); return "@$p"
}

# a signed-in browser: the jar is read and written on every request
function Browser([string]$jar) { return @("-b", $jar, "-c", $jar) }

function Dashboard([string]$jar) { return Req "GET" "$origin/account" (Browser $jar) }

function Csrf([string]$jar) {
    $r = Dashboard $jar
    if ($r.body -match 'name="csrf" value="([^"]+)"') { return $Matches[1] }
    return ""
}

# a dashboard form post: csrf plus the given fields
function Post([string]$jar, [string]$path, [string[]]$fields = @()) {
    $x = (Browser $jar) + @("--data-urlencode", "csrf=$(Csrf $jar)")
    foreach ($f in $fields) { $x += @("--data-urlencode", $f) }
    return Req "POST" "$origin$path" $x
}

# the API as the edit page calls it with the signed-in session
$sessionHdr = @("-H", "X-Sitebin-Session: 1", "-H", "Sec-Fetch-Site: same-origin", "-H", "Origin: $origin")

function SetTier([string]$jar, [string]$tier) { return Post $jar "/account/tier" @("tier=$tier") }

function Upload([string]$text) {
    $p = Join-Path $work ("own-index" + (Get-Random) + ".html"); [IO.File]::WriteAllText($p, $text); return $p
}

function EditIdOf($site) { return ($site.edit_url -split "/e/")[1].TrimEnd("/") }

# ---- Mailpit ----
function AllMails {
    # The unary comma keeps a one-element or empty array whole across return.
    $r = Req "GET" "$mailApi/messages?limit=500"
    if ($r.code -ne 200) { return , @() }
    return , @(($r.body | ConvertFrom-Json).messages)
}
function FindMails([string]$to, [string]$subject) {
    $all = AllMails
    return , @($all | Where-Object { $_.Subject -eq $subject -and ((@($_.To) | ForEach-Object { $_.Address }) -contains $to) })
}
function WaitMails([string]$to, [string]$subject, [int]$count = 1) {
    for ($i = 0; $i -lt 30; $i++) {
        $m = FindMails $to $subject
        if ($m.Count -ge $count) { return , $m }
        Start-Sleep -Milliseconds 500
    }
    return , (FindMails $to $subject)
}
function MailText([string]$id) { return ((Req "GET" "$mailApi/message/$id").body | ConvertFrom-Json).Text }

function PortFree([int]$p) {
    $l = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Any, $p)
    try { $l.Start(); $l.Stop(); return $true } catch { return $false }
}

function Cleanup {
    docker rm -f $name $mailName 2>$null | Out-Null
    docker network rm $net 2>$null | Out-Null
    docker volume rm $vol 2>$null | Out-Null
    Remove-Item -Recurse -Force $cfgDir -ErrorAction SilentlyContinue
    Remove-Item $jarA, $jarB, $jarC -ErrorAction SilentlyContinue
    Get-ChildItem $work -Filter "own-*" -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
}

# free: no sharing. pro: sharing, generous caps. drop: the anonymous tier.
$tiers = '[' +
'{"id":"free","label":"Free","max_site_bytes":1000000,"max_files":50,"max_sites":5,"webdav":false,"custom_domains":0,"max_expiry_days":7,"max_forms":0},' +
'{"id":"pro","label":"Pro","max_site_bytes":10000000,"max_files":500,"max_sites":50,"webdav":true,"custom_domains":3,"max_expiry_days":0,"max_forms":2,"trusted":true,"sharing":true},' +
'{"id":"drop","label":"Drop","max_site_bytes":1000000,"max_files":50,"max_sites":0,"webdav":false,"custom_domains":0,"max_expiry_days":1,"max_forms":0}' +
']'

try {
    Write-Host "== starting Mailpit and $Image (tiers mode) on $Port" -ForegroundColor Cyan
    Cleanup
    New-Item -ItemType Directory -Force $cfgDir | Out-Null
    [IO.File]::WriteAllText((Join-Path $cfgDir "tiers.json"), $tiers)
    $cfgDocker = ($cfgDir -replace '\\', '/')
    if (-not (PortFree $Port)) { throw "port $Port is busy: pass -Port <free port>" }
    if (-not (PortFree $MailPort)) { throw "port $MailPort is busy: pass -MailPort <free port>" }

    docker network create $net | Out-Null
    docker run -d --name $mailName --network $net -p "${MailPort}:8025" axllent/mailpit | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "docker run (mailpit) failed" }
    docker run -d --name $name --network $net -p "${Port}:80" -v "${vol}:/data" -v "${cfgDocker}:/cfg:ro" `
        -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
        -e "SITEBIN_ACCOUNT_MODE=tiers" -e "SITEBIN_TIERS_FILE=/cfg/tiers.json" `
        -e "SITEBIN_DEFAULT_TIER=free" -e "SITEBIN_ANON_TIER=drop" -e "SITEBIN_TIER_SELF_SELECT=true" `
        -e "SITEBIN_LOCAL_AUTH=true" -e "SITEBIN_DOMAIN_VERIFICATION=off" `
        -e "SITEBIN_RATE_AUTH_PER_5MIN=200" `
        -e "SITEBIN_SMTP_HOST=$mailName" -e "SITEBIN_SMTP_PORT=1025" -e "SITEBIN_SMTP_FROM=accounts@localtest.me" `
        $Image | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "docker run (sitebin) failed" }

    $up = $false
    for ($i = 0; $i -lt 40; $i++) {
        Start-Sleep -Milliseconds 700
        if ((Req "GET" "$origin/account/login").code -eq 200 -and (Req "GET" "$mailApi/messages").code -eq 200) { $up = $true; break }
    }
    Assert "containers up (tiers mode, mailpit)" $up
    if (-not $up) { throw "the containers did not come up" }

    # ---------- three verified accounts ----------
    Write-Host "== accounts A, B, C: sign up and verify through the mailed link" -ForegroundColor Cyan
    foreach ($acc in @(@{ n = "A"; e = $emailA; j = $jarA }, @{ n = "B"; e = $emailB; j = $jarB }, @{ n = "C"; e = $emailC; j = $jarC })) {
        Remove-Item $acc.j -ErrorAction SilentlyContinue
        $r = Req "POST" "$origin/account/signup" @("-c", $acc.j, "--data-urlencode", "email=$($acc.e)", "--data-urlencode", "password=password12345")
        Assert "$($acc.n): signup succeeds (303)" ($r.code -eq 303) "got $($r.code): $($r.body)"
        $m = WaitMails $acc.e "Verify your Sitebin account"
        Assert "$($acc.n): a verification mail arrived" ($m.Count -eq 1) "got $($m.Count)"
        $link = ""
        if ($m.Count -ge 1) { $link = [regex]::Match((MailText $m[0].ID), "http://\S+/account/verify\?token=\S+").Value }
        $r = Req "GET" $link
        Assert "$($acc.n): the verify link confirms the address" ($link -ne "" -and $r.code -eq 200 -and $r.body -match "Email verified") "link '$link' got $($r.code)"
    }
    foreach ($acc in @(@{ n = "A"; j = $jarA }, @{ n = "B"; j = $jarB })) {
        $r = SetTier $acc.j "pro"
        Assert "$($acc.n): switches to pro (303)" ($r.code -eq 303) "got $($r.code): $($r.body)"
        Assert "$($acc.n): the dashboard says Pro is current" ((Dashboard $acc.j).body -match "Pro \(current\)")
    }
    Assert "C: stays on Free" ((Dashboard $jarC).body -match "Free \(current\)")

    # ---------- 1. adopt ----------
    Write-Host "== 1. adopt: A takes an anonymous drop into the account" -ForegroundColor Cyan
    $r = Req "POST" "$origin/api/sites" @("-H", "Sec-Fetch-Site: same-origin", "-F", "files=@$(Upload '<h1>own-drop</h1>');filename=index.html")
    Assert "an anonymous drop is created (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
    $drop = $null; if ($r.code -eq 201) { $drop = $r.body | ConvertFrom-Json }
    Assert "the drop is anonymous" ($null -ne $drop -and $drop.owned -eq $false) "$($r.body)"
    if (-not $drop) { throw "no drop to adopt" }
    $dropEdit = EditIdOf $drop

    $r = Post $jarA "/account/adopt" @("site=$($drop.view_url)", "password=$($drop.edit_password)")
    Assert "A adopts the drop by its view URL and edit password (303 to /account)" ($r.code -eq 303 -and $r.location -eq "/account") "got $($r.code) '$($r.location)': $($r.body)"
    $r = Dashboard $jarA
    Assert "the drop is listed on A's dashboard" ($r.body.Contains($drop.id)) "got $($r.code)"
    Assert "the dashboard says it was adopted, once" ($r.body -match "It is a site of this account now") "got $($r.code)"
    $r = Req "GET" "$origin/api/sites/$dropEdit" @("-H", "X-Edit-Password: $($drop.edit_password)")
    Assert "the drop's edit password still works (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"

    $r = Req "POST" "$origin/api/sites" @("-H", "Sec-Fetch-Site: same-origin", "-F", "files=@$(Upload '<h1>own-drop-2</h1>');filename=index.html")
    $drop2 = $null; if ($r.code -eq 201) { $drop2 = $r.body | ConvertFrom-Json }
    Assert "a second anonymous drop is created (201)" ($null -ne $drop2) "got $($r.code): $($r.body)"
    if ($drop2) {
        $r = Post $jarA "/account/adopt" @("site=$($drop2.view_url)", "password=wrong-password-123")
        Assert "a wrong password is refused (400)" ($r.code -eq 400) "got $($r.code)"
        Assert "... with exactly 'URL or password wrong.'" ($r.body -match '<div class="inline-status err" role="alert"[^>]*>URL or password wrong\.</div>') "$($r.body)"
        $r = Dashboard $jarA
        Assert "... and the drop is not A's" (-not $r.body.Contains($drop2.id))
    }

    # ---------- 2. transfer ----------
    Write-Host "== 2. transfer: A offers two sites to B, B accepts both" -ForegroundColor Cyan
    $bundle = @()
    foreach ($n in 1, 2) {
        $r = Req "POST" "$origin/api/sites" ((Browser $jarA) + @("-F", "files=@$(Upload "<h1>own-site-$n</h1>");filename=index.html"))
        $s = $null; if ($r.code -eq 201) { $s = $r.body | ConvertFrom-Json }
        Assert "A creates site $n, owned (201)" ($null -ne $s -and $s.owned -eq $true) "got $($r.code): $($r.body)"
        if ($s) { $bundle += $s }
    }
    if ($bundle.Count -ne 2) { throw "A has no bundle to offer" }
    foreach ($s in $bundle) {
        $r = Req "GET" "$origin/api/sites/$(EditIdOf $s)" @("-H", "X-Edit-Password: $($s.edit_password)")
        Assert "before the transfer A's edit password of $($s.id) works (200)" ($r.code -eq 200) "got $($r.code)"
    }

    $r = Post $jarA "/account/transfer" @("site=$($bundle[0].id)", "site=$($bundle[1].id)", "email=$emailB", "accept_by=$acceptBy", "step=create")
    Assert "A offers both sites to B's address (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
    Assert "... and is told only the never-reveal answer" ($r.body.Contains($neverReveal)) "$($r.body)"

    $r = Dashboard $jarB
    Assert "B's dashboard shows the 'Ready to take over' card" ($r.body -match "<h3>Ready to take over</h3>") "got $($r.code)"
    Assert "... naming A and both sites" ($r.body.Contains("$emailA offers you 2 sites") -and $r.body.Contains($bundle[0].id) -and $r.body.Contains($bundle[1].id))
    $offerB = ""; if ($r.body -match 'action="/account/transfer/([^/"]+)/accept"') { $offerB = $Matches[1] }
    Assert "... with an accept button" ($offerB -ne "") "no accept form"

    $r = Post $jarB "/account/transfer/$offerB/accept" @("site=$($bundle[0].id)", "site=$($bundle[1].id)")
    Assert "B accepts the bundle (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
    Assert "... the answer says both were taken over" ($r.body -match "You took over 2 sites") "$($r.body)"
    $newPw = @{}
    foreach ($mm in [regex]::Matches($r.body, '<span class="sname">([^<]*)</span>\s*<div class="secretout"><span class="muted">New edit password</span> <code class="val">([^<]+)</code>')) {
        foreach ($s in $bundle) { if ($mm.Groups[1].Value.Contains($s.id)) { $newPw[$s.id] = $mm.Groups[2].Value } }
    }
    Assert "... and shows the two new edit passwords" ($newPw.Count -eq 2 -and $newPw[$bundle[0].id] -ne $bundle[0].edit_password -and $newPw[$bundle[1].id] -ne $bundle[1].edit_password) "found $($newPw.Count)"
    foreach ($s in $bundle) {
        $e = EditIdOf $s
        $r = Req "GET" "$origin/api/sites/$e" @("-H", "X-Edit-Password: $($s.edit_password)")
        Assert "the old edit password of $($s.id) fails (401)" ($r.code -eq 401) "got $($r.code)"
        $r = Req "GET" "$origin/api/sites/$e" @("-H", "X-Edit-Password: $($newPw[$s.id])")
        Assert "the new edit password of $($s.id) works (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
    }
    $r = Dashboard $jarA
    Assert "A's dashboard no longer lists the two sites" ($r.code -eq 200 -and -not $r.body.Contains($bundle[0].id) -and -not $r.body.Contains($bundle[1].id)) "got $($r.code)"
    Assert "... but still the adopted drop" ($r.body.Contains($drop.id))
    $r = Dashboard $jarB
    Assert "B's dashboard lists both" ($r.body.Contains($bundle[0].id) -and $r.body.Contains($bundle[1].id))
    Assert "... and the card is gone" ($r.body -notmatch "<h3>Ready to take over</h3>")

    # ---------- 3. share ----------
    Write-Host "== 3. share: B shares site 1 with C" -ForegroundColor Cyan
    $shared = $bundle[0]
    $sharedEdit = EditIdOf $shared
    $r = Post $jarB "/account/sites/$($shared.id)/share" @("email=$emailC", "until=")
    Assert "B invites C (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
    Assert "... and is told only the never-reveal answer" ($r.body.Contains($neverReveal)) "$($r.body)"

    $r = Dashboard $jarC
    Assert "C (free) sees the invitation" ($r.body -match 'id="invitations"' -and $r.body.Contains($shared.id)) "got $($r.code)"
    Assert "... with the plan notice and no accept button" ($r.body -match "Your plan cannot be an admin of a shared site\." -and $r.body -notmatch 'action="/account/invites/[^/"]+/accept"')
    $invite = ""; if ($r.body -match 'action="/account/invites/([^/"]+)/decline"') { $invite = $Matches[1] }
    Assert "... the invitation has an id" ($invite -ne "")
    $r = Post $jarC "/account/invites/$invite/accept"
    Assert "C (free) is refused the accept (403)" ($r.code -eq 403 -and $r.body -match "<h1>Your plan cannot be an admin</h1>") "got $($r.code): $($r.body)"

    $r = SetTier $jarC "pro"
    Assert "C switches to pro (303)" ($r.code -eq 303) "got $($r.code): $($r.body)"
    $r = Dashboard $jarC
    Assert "C's dashboard now offers the accept" ($r.body -match "Pro \(current\)" -and $r.body.Contains("/account/invites/$invite/accept"))
    $r = Post $jarC "/account/invites/$invite/accept"
    Assert "C accepts the invitation (303 to /account)" ($r.code -eq 303 -and $r.location -eq "/account") "got $($r.code): $($r.body)"
    $r = Dashboard $jarC
    Assert "C's dashboard lists the site as shared by B" ($r.body.Contains($shared.id) -and $r.body.Contains("shared by $emailB"))
    Assert "... with its Manage link" ($r.body.Contains("/e/$sharedEdit"))
    # the owner's row names the admin: "<email> <middle dot> Admin"
    Assert "B's dashboard names C as an admin of the site" ((Dashboard $jarB).body -match ([regex]::Escape($emailC) + '\s+\S+\s+Admin'))

    $r = Req "PUT" "$origin/api/sites/$sharedEdit" ((Browser $jarC) + @("-H", "Content-Type: application/json", "--data", (JsonFile '{"name":"Managed by C"}')))
    Assert "without X-Sitebin-Session C's session does not count (401)" ($r.code -eq 401) "got $($r.code): $($r.body)"
    $r = Req "PUT" "$origin/api/sites/$sharedEdit" ((Browser $jarC) + $sessionHdr + @("-H", "Content-Type: application/json", "--data", (JsonFile '{"name":"Managed by C"}')))
    Assert "C changes the name through the session (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
    $r = Req "GET" "$origin/api/sites/$sharedEdit" @("-H", "X-Edit-Password: $($newPw[$shared.id])")
    $nm = ""; if ($r.code -eq 200) { $nm = ($r.body | ConvertFrom-Json).name }
    Assert "... and the owner sees the change" ($nm -eq "Managed by C") "got '$nm'"

    $r = Req "DELETE" "$origin/api/sites/$sharedEdit" ((Browser $jarC) + $sessionHdr)
    $err = ""; if ($r.body -match '^\s*\{') { $err = ($r.body | ConvertFrom-Json).error }
    Assert "C's DELETE is refused (403)" ($r.code -eq 403) "got $($r.code): $($r.body)"
    Assert "... with 'only the site's owner can do this'" ($err -eq "only the site's owner can do this") "got '$err'"
    $r = Req "GET" "$origin/api/sites/$sharedEdit" @("-H", "X-Edit-Password: $($newPw[$shared.id])")
    Assert "... and the site is still there" ($r.code -eq 200) "got $($r.code)"

    $r = Post $jarC "/account/sites/$($shared.id)/leave"
    Assert "C leaves the site (303 to /account)" ($r.code -eq 303 -and $r.location -eq "/account") "got $($r.code): $($r.body)"
    $r = Req "PUT" "$origin/api/sites/$sharedEdit" ((Browser $jarC) + $sessionHdr + @("-H", "Content-Type: application/json", "--data", (JsonFile '{"name":"After leaving"}')))
    Assert "after leaving, C's session has no standing (401/403)" ($r.code -eq 401 -or $r.code -eq 403) "got $($r.code): $($r.body)"
    $r = Req "GET" "$origin/api/sites/$sharedEdit" ((Browser $jarC) + $sessionHdr)
    Assert "... not even to read it (401/403)" ($r.code -eq 401 -or $r.code -eq 403) "got $($r.code)"
    Assert "... and C's dashboard no longer lists it" (-not (Dashboard $jarC).body.Contains($shared.id))
    Assert "... and B's dashboard no longer names C" (-not (Dashboard $jarB).body.Contains($emailC))

    # ---------- 4. free ----------
    Write-Host "== 4. free: an account without sharing cannot take a transfer over" -ForegroundColor Cyan
    $r = SetTier $jarC "free"
    Assert "C switches back to free (303)" ($r.code -eq 303 -and (Dashboard $jarC).body -match "Free \(current\)") "got $($r.code)"
    $kept = $bundle[1]
    $r = Post $jarB "/account/transfer" @("site=$($kept.id)", "email=$emailC", "accept_by=$acceptBy", "step=create")
    Assert "B offers a site to C's address (200)" ($r.code -eq 200) "got $($r.code): $($r.body)"
    Assert "... with the never-reveal answer" ($r.body.Contains($neverReveal)) "$($r.body)"
    $toKnown = $r
    $r = Post $jarA "/account/transfer" @("site=$($drop.id)", "email=$ghost", "accept_by=$acceptBy", "step=create")
    Assert "an offer to an address without an account gets the same status and answer" ($r.code -eq $toKnown.code -and $r.body.Contains($neverReveal)) "got $($r.code): $($r.body)"

    $r = Dashboard $jarC
    Assert "C (free) sees the card" ($r.body -match "<h3>Ready to take over</h3>" -and $r.body.Contains($kept.id))
    Assert "... with the plan notice and no accept button" ($r.body -match "Your plan cannot take over sites\." -and $r.body -notmatch 'action="/account/transfer/[^/"]+/accept"')
    $offerC = ""; if ($r.body -match 'action="/account/transfer/([^/"]+)/decline"') { $offerC = $Matches[1] }
    Assert "... the offer has an id" ($offerC -ne "")
    $r = Post $jarC "/account/transfer/$offerC/accept" @("site=$($kept.id)")
    Assert "C's accept is refused (403)" ($r.code -eq 403) "got $($r.code): $($r.body)"
    Assert "... with the plan text" ($r.body -match "<h1>Your plan cannot take over sites</h1>" -and $r.body -match "needs a plan that includes it") "$($r.body)"
    $r = Req "GET" "$origin/api/sites/$(EditIdOf $kept)" @("-H", "X-Edit-Password: $($newPw[$kept.id])")
    Assert "... and the site stays B's, its password unchanged (200)" ($r.code -eq 200 -and (Dashboard $jarB).body.Contains($kept.id)) "got $($r.code)"

    # ---------- 5. mail ----------
    Write-Host "== 5. mail: what Mailpit received" -ForegroundColor Cyan
    $expect = @(
        @{ to = $emailB; s = "A Sitebin transfer is offered to you"; n = 1; what = "offer, to B" },
        @{ to = $emailC; s = "A Sitebin transfer is offered to you"; n = 1; what = "offer, to C (free, verified)" },
        @{ to = $emailA; s = "Your Sitebin transfer was accepted"; n = 1; what = "accepted, to A" },
        @{ to = $emailB; s = "You took over Sitebin sites"; n = 1; what = "accepted, to B" },
        @{ to = $emailC; s = "You are invited to help manage a Sitebin site"; n = 1; what = "invitation, to C" },
        @{ to = $emailB; s = "Your Sitebin invitation was accepted"; n = 1; what = "share accepted, to B" },
        @{ to = $emailB; s = "A Sitebin share has ended"; n = 1; what = "share ended, to B" },
        @{ to = $emailC; s = "A Sitebin share has ended"; n = 1; what = "share ended, to C" }
    )
    foreach ($x in $expect) {
        $m = WaitMails $x.to $x.s $x.n
        Assert "mail: $($x.what) ('$($x.s)')" ($m.Count -eq $x.n) "got $($m.Count)"
    }
    foreach ($e in @($emailA, $emailB, $emailC)) {
        Assert "mail: one verification to $e" ((FindMails $e "Verify your Sitebin account").Count -eq 1)
    }
    $m = WaitMails $emailC "You are invited to help manage a Sitebin site"
    $txt = ""; if ($m.Count -ge 1) { $txt = MailText $m[0].ID }
    Assert "mail: the invitation names the owner and the site, and no password" ($txt.Contains($emailB) -and $txt.Contains($shared.id) -and -not $txt.Contains($newPw[$shared.id])) "$txt"
    $m = WaitMails $emailB "A Sitebin transfer is offered to you"
    $txt = ""; if ($m.Count -ge 1) { $txt = MailText $m[0].ID }
    Assert "mail: the offer names the sender and both sites, and no password" ($txt.Contains($emailA) -and $txt.Contains($bundle[0].id) -and $txt.Contains($bundle[1].id) -and -not $txt.Contains($bundle[0].edit_password)) "$txt"

    Start-Sleep -Seconds 2 # anything still in flight lands before the census
    $all = AllMails
    $rcpts = @($all | ForEach-Object { @($_.To) | ForEach-Object { $_.Address.ToLower() } } | Sort-Object -Unique)
    Assert "mail: nothing went to the address without an account" (-not ($rcpts -contains $ghost)) "recipients: $($rcpts -join ', ')"
    $strangers = @($rcpts | Where-Object { @($emailA, $emailB, $emailC) -notcontains $_ })
    Assert "mail: every recipient is a verified account (A, B or C)" ($strangers.Count -eq 0) "others: $($strangers -join ', ')"
}
catch {
    $script:fail++
    Write-Host "  FAIL aborted: $_" -ForegroundColor Red
}
finally {
    if ($script:fail -gt 0) {
        Write-Host "== last log lines of $name" -ForegroundColor Yellow
        docker logs --tail 40 $name 2>&1 | ForEach-Object { Write-Host "  $_" }
    }
    Cleanup
}

Write-Host ""
Write-Host ("== ownership E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
