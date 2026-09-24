# Sitebin -- forms E2E against the real community image and a real SMTP server.
#
#   powershell -File e2e\forms.ps1 [-Image sitebin:dev]
#
# Runs Mailpit as the SMTP server on a private Docker network and checks the
# whole path: a form created over the API, the confirmation mail, confirming
# through its link (GET shows a button, POST acts), a plain HTML post with an
# attachment, the delivered mail (From, Reply-To, both parts, submission.json,
# List-Unsubscribe), the JSON answer, the honeypot, a refused executable, the
# recipient's one-click stop, and logs free of submitted content. Captcha and
# plan caps are covered by the Go suite and tiers.ps1.
param(
    [string]$Image = "sitebin:dev",
    [int]$Port = 8091,
    [int]$MailPort = 8026
)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$mailApi = "http://localhost:$MailPort/api/v1"
$name = "sitebin-forms-e2e"
$mailName = "sitebin-forms-e2e-mail"
$net = "sitebin-forms-e2e-net"
$vol = "sitebin-forms-e2e-data"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

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
    $bodyFile = Join-Path $work "fb.tmp"
    Remove-Item $bodyFile -ErrorAction SilentlyContinue
    $a = @("-s", "-X", $method, "-o", $bodyFile, "-w", "%{http_code} %{redirect_url}", "--max-time", "30") + $extra + @($url)
    $out = "$(& curl.exe @a)"
    $parts = $out.Split(" ", 2)
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    $loc = ""; if ($parts.Count -gt 1) { $loc = $parts[1] }
    return @{ code = [int]$parts[0]; body = $body; location = $loc }
}

function JsonFile([string]$json) {
    $p = Join-Path $work ("fj" + (Get-Random) + ".json"); [IO.File]::WriteAllText($p, $json); return "@$p"
}

function MailsTo([string]$addr) {
    # The unary comma forces the array through the return boundary intact --
    # without it, PowerShell unrolls a single-element (or empty) array to a
    # bare scalar (or $null) on return, and ".Count" silently reads as $null.
    $r = Req "GET" "$mailApi/messages"
    if ($r.code -ne 200) { return , @() }
    return , @(($r.body | ConvertFrom-Json).messages | Where-Object { (@($_.To) | ForEach-Object { $_.Address }) -contains $addr })
}

function WaitMails([string]$addr, [int]$count) {
    for ($i = 0; $i -lt 20; $i++) {
        $m = MailsTo $addr
        if ($m.Count -ge $count) { return , $m }
        Start-Sleep -Milliseconds 500
    }
    return , (MailsTo $addr)
}

function Message([string]$id) { return ((Req "GET" "$mailApi/message/$id").body | ConvertFrom-Json) }

function Cleanup {
    docker rm -f $name $mailName 2>$null | Out-Null
    docker network rm $net 2>$null | Out-Null
    docker volume rm $vol 2>$null | Out-Null
}

Write-Host "== starting Mailpit and $Image on $Port" -ForegroundColor Cyan
Cleanup
docker network create $net | Out-Null
docker run -d --name $mailName --network $net -p "${MailPort}:8025" axllent/mailpit | Out-Null
docker run -d --name $name --network $net -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_FORMS_SMTP_HOST=$mailName" -e "SITEBIN_FORMS_SMTP_PORT=1025" `
    -e "SITEBIN_FORMS_SMTP_FROM=forms@localtest.me" `
    $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; Cleanup; exit 1 }
$up = $false
for ($i = 0; $i -lt 40; $i++) {
    Start-Sleep -Milliseconds 700
    if ((Req "GET" "$origin/").code -eq 200 -and (Req "GET" "$mailApi/messages").code -eq 200) { $up = $true; break }
}
Assert "containers up" $up
if (-not $up) { docker logs $name; Cleanup; exit 1 }

Write-Host "== a site and a form" -ForegroundColor Cyan
$page = Join-Path $work "forms-index.html"; [IO.File]::WriteAllText($page, "<h1>forms e2e</h1>")
$r = Req "POST" "$origin/api/sites" @("-H", "Sec-Fetch-Site: same-origin", "-F", "files=@$page;filename=index.html")
Assert "site created" ($r.code -eq 201) "$($r.code) $($r.body)"
$site = $r.body | ConvertFrom-Json
$edit = ($site.edit_url -split "/e/")[1]
$pw = @("-H", "X-Edit-Password: $($site.edit_password)")
$siteOrigin = $site.view_url.TrimEnd("/")
$post = "$siteOrigin/_sitebin/forms"

$r = Req "POST" "$origin/api/sites/$edit/forms" ($pw + @("-H", "Content-Type: application/json", "--data", (JsonFile '{"name":"Contact","recipient":"owner@example.test","files":true}')))
Assert "form created (201)" ($r.code -eq 201) "$($r.code) $($r.body)"
$forms = $r.body | ConvertFrom-Json
$key = $forms.forms[0].key
Assert "form is pending" ($forms.forms[0].status -eq "pending") "$($forms.forms[0].status)"
Assert "snippet posts to the site's own origin" ($forms.forms[0].snippet.Contains('action="/_sitebin/forms/' + $key + '"'))

Write-Host "== the recipient confirms" -ForegroundColor Cyan
$m = WaitMails "owner@example.test" 1
Assert "confirmation mail delivered" ($m.Count -eq 1) "$($m.Count)"
$conf = Message $m[0].ID
Assert "confirmation comes from Sitebin" ($conf.From.Name -eq "Sitebin") "$($conf.From.Name)"
$link = [regex]::Match($conf.Text, "http://\S+/forms/confirm\?t=\S+").Value
Assert "confirmation carries its link" ($link -ne "")

$r = Req "POST" "$post/$key" @("--data", "message=too+early")
Assert "a pending form refuses (403)" ($r.code -eq 403) "$($r.code)"
$r = Req "GET" $link
Assert "the link shows a button" ($r.code -eq 200 -and $r.body.Contains('action="/forms/confirm"')) "$($r.code)"
$r = Req "POST" "$post/$key" @("--data", "message=still+early")
Assert "opening the link did not confirm" ($r.code -eq 403) "$($r.code)"
$tok = [uri]::UnescapeDataString(($link -split "t=", 2)[1])
$r = Req "POST" "$origin/forms/confirm" @("--data-urlencode", "t=$tok")
Assert "the button confirms" ($r.code -eq 200 -and $r.body.Contains("Confirmed")) "$($r.code)"

Write-Host "== a plain HTML post with an attachment" -ForegroundColor Cyan
$att = Join-Path $work "hello.txt"; [IO.File]::WriteAllText($att, "hello attachment")
$r = Req "POST" "$post/$key" @("-F", "name=Anna", "-F", "email=anna@sender.test", "-F", "message=Hallo aus dem E2E", "-F", "cv=@$att;filename=hello.txt")
Assert "submission answers 303" ($r.code -eq 303) "$($r.code) $($r.body)"
Assert "303 goes to the thank-you page" ($r.location.EndsWith("/_sitebin/forms/$key/thanks")) "$($r.location)"
$m = WaitMails "owner@example.test" 2
Assert "submission mail delivered" ($m.Count -eq 2) "$($m.Count)"
$subId = ($m | Where-Object { $_.Subject -like "New message*" } | Select-Object -First 1).ID
$sub = Message $subId
Assert "From carries the form's name" ($sub.From.Name -eq "Contact" -and $sub.From.Address -eq "forms@localtest.me") "$($sub.From.Name) $($sub.From.Address)"
Assert "Reply-To is the submitter" ((@($sub.ReplyTo) | ForEach-Object { $_.Address }) -contains "anna@sender.test")
Assert "text part has the message" ($sub.Text.Contains("message: Hallo aus dem E2E"))
Assert "HTML part present" ($sub.HTML.Length -gt 500) "$($sub.HTML.Length)"
$files = @($sub.Attachments | ForEach-Object { $_.FileName })
Assert "attachment delivered" ($files -contains "hello.txt") "$($files -join ',')"
Assert "submission.json attached" ($files -contains "submission.json") "$($files -join ',')"
$jp = $sub.Attachments | Where-Object { $_.FileName -eq "submission.json" } | Select-Object -First 1
$j = (Req "GET" "$mailApi/message/$subId/part/$($jp.PartID)").body | ConvertFrom-Json
$order = (@($j.fields) | ForEach-Object { $_.name }) -join ","
Assert "submission.json keeps the form's order" ($order -eq "name,email,message") "$order"
Assert "submission.json names the form" ($j.form.key -eq $key -and $j.version -eq 1)
$h = (Req "GET" "$mailApi/message/$subId/headers").body | ConvertFrom-Json
$unsub = ([string]@($h.'List-Unsubscribe')[0]).Trim("<", ">")
Assert "List-Unsubscribe points at the stop page" ($unsub -match "/forms/stop\?t=") "$unsub"

Write-Host "== answers, honeypot, refusals" -ForegroundColor Cyan
$r = Req "POST" "$post/$key" @("-H", "Accept: application/json", "--data", "message=json")
Assert "JSON answer" ($r.code -eq 200 -and $r.body.Trim() -eq '{"ok":true}') "$($r.code) $($r.body)"
$before = (WaitMails "owner@example.test" 3).Count
$r = Req "POST" "$post/$key" @("--data", "message=spam&_gotcha=http%3A%2F%2Fspam.example")
Assert "honeypot looks like a success" ($r.code -eq 303) "$($r.code)"
Start-Sleep -Seconds 1
Assert "honeypot sends nothing" ((MailsTo "owner@example.test").Count -eq $before)
$exe = Join-Path $work "evil.exe"; [IO.File]::WriteAllText($exe, "MZ")
$r = Req "POST" "$post/$key" @("-F", "message=x", "-F", "cv=@$exe;filename=evil.exe")
Assert "an executable is refused (400)" ($r.code -eq 400) "$($r.code)"

Write-Host "== the recipient stops the form" -ForegroundColor Cyan
$r = Req "POST" $unsub @("--data", "List-Unsubscribe=One-Click")
Assert "one-click stop answers 200" ($r.code -eq 200) "$($r.code)"
$r = Req "POST" "$post/$key" @("--data", "message=after+stop")
Assert "a stopped form refuses (403)" ($r.code -eq 403) "$($r.code)"
$r = Req "GET" "$origin/api/sites/$edit/forms" $pw
Assert "the owner sees it stopped" ((($r.body | ConvertFrom-Json).forms[0].status) -eq "stopped")

$logs = (docker logs $name 2>&1 | Out-String)
Assert "logs carry no submitted value" (-not $logs.Contains("Hallo aus dem E2E") -and -not $logs.Contains("anna@sender.test"))
Assert "logs carry no recipient" (-not $logs.Contains("owner@example.test"))

Cleanup
Write-Host ""
$total = $script:pass + $script:fail
if ($total -ne $ExpectedAssertions) {
    Write-Host ("  FAIL assertion count: ran {0}, expected {1} - an assertion was skipped or threw" -f $total, $ExpectedAssertions) -ForegroundColor Red
    $script:fail++
}
Write-Host ("== forms E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
