package abuse

import (
	"bytes"
	"strings"
	"testing"
)

// bs is a backslash, for the four-digit \u escapes: written out, an editor or
// a tool on the way may decode them before Go ever sees the file.
const bs = `\`

// A kit can write a signature with one character encoded: the browser
// decodes it before it follows the link, so the scanner has to see the
// decoded text too.
func TestEncodedSignaturesAreFound(t *testing.T) {
	for name, c := range map[string]struct{ path, content, rule string }{
		"decimal entity":       {"index.html", `<button data-deeplink="tez&#58;//upi/pay?pa=x@ybl">Pay</button>`, "wallet-deeplink"},
		"hex entity":           {"index.html", `<a href="tez&#X3A;//upi/pay?pa=x@ybl">Pay</a>`, "wallet-deeplink"},
		"named entity":         {"index.html", `<a href="phonepe&colon;//pay?pa=x@ybl">Pay</a>`, "wallet-deeplink"},
		"entity, no semicolon": {"index.html", `<a href="gpay&#58//upi/pay?pa=x@ybl">Pay</a>`, "wallet-deeplink"},
		"padded entity":        {"index.html", `<a href="tez&#00000000000000000000000000000000000058;//upi/pay">Pay</a>`, "wallet-deeplink"},
		"js hex escape":        {"pay.js", `location.href = "tez\x3a//upi/pay?pa=x@ybl";`, "wallet-deeplink"},
		"js unicode escape":    {"pay.js", `location.href = "paytmmp` + bs + `u003a//pay?pa=x@ybl";`, "wallet-deeplink"},
		"js code point escape": {"pay.js", `location.href = "tez\u{3a}//upi/pay?pa=x@ybl";`, "wallet-deeplink"},
		"percent in the path":  {"x.js", `fetch("https://api.telegram.org/%62ot1:a/sendMessage")`, "telegram-bot-api"},
		"percent in the host":  {"x.js", `fetch("https://api%2Etelegram.org/bot1:a/sendMessage")`, "telegram-bot-api"},
		"entity in a webhook":  {"a.html", `<form action="https://discord.com/api&#47;webhooks/1/abc">`, "chat-webhook"},
	} {
		r := scanString(t, c.path, c.content)
		if !hasHit(r, c.rule) {
			t.Errorf("%s: %s not found; hits %v", name, c.rule, hitIDs(r))
			continue
		}
		if !r.Blocks() {
			t.Errorf("%s: a decoded block hit must block like a plain one", name)
		}
	}
}

// The register shows the excerpt; a signature that was only there once
// decoded says so, because hiding it is a signal of its own.
func TestDecodedHitIsMarked(t *testing.T) {
	excerptOf := func(r Result, rule string) string {
		for _, h := range r.Hits {
			if h.Rule == rule {
				return h.Excerpt
			}
		}
		return ""
	}
	r := scanString(t, "index.html", `<a href="tez&#58;//upi/pay?pa=x@ybl">Pay</a>`)
	if ex := excerptOf(r, "wallet-deeplink"); !strings.HasPrefix(ex, "decoded: ") || !strings.Contains(ex, "tez://upi") {
		t.Errorf("decoded excerpt %q", ex)
	}
	r = scanString(t, "index.html", upiKit)
	if ex := excerptOf(r, "wallet-deeplink"); ex == "" || strings.HasPrefix(ex, "decoded: ") {
		t.Errorf("a plain hit is not marked decoded: %q", ex)
	}
}

// An entity-encoded angle bracket is text, never markup: that is how every
// page shows code. JavaScript escapes and percent-encoding do become markup,
// through document.write, innerHTML and unescape.
func TestEncodedAngleBracketsStayTextOnlyInHTML(t *testing.T) {
	docs := `<pre>&lt;script src="https://ipfs.io/ipfs/x.js"&gt;&lt;/script&gt;</pre>` +
		`<p>&#60;script src=https://dweb.link/x.js&#62;</p>`
	if r := scanString(t, "docs/index.html", docs); len(r.Hits) > 0 {
		t.Errorf("a code sample in a page is not a script: %v", hitIDs(r))
	}
	for _, js := range []string{
		`document.write("\x3cscript src='https://ipfs.io/ipfs/x.js'>\x3c/script>")`,
		`document.write(decodeURIComponent("%3Cscript src='https://ipfs.io/ipfs/x.js'>"))`,
	} {
		if r := scanString(t, "loader.js", js); !hasHit(r, "ipfs-script") {
			t.Errorf("%s: a written script tag is markup: %v", js, hitIDs(r))
		}
	}
}

// A backslash escaped by a backslash is a backslash: "\\x3a" is the four
// characters \x3a in a JavaScript string, not a colon.
func TestEscapedBackslashIsNotAnEscape(t *testing.T) {
	if r := scanString(t, "pay.js", `var doc = "tez\\x3a//upi";`); len(r.Hits) > 0 {
		t.Errorf("an escaped backslash was decoded: %v", hitIDs(r))
	}
}

// Decoding finds what was hidden; it must not invent hits on the entities
// and percent-encoding every ordinary page is full of.
func TestOrdinaryEncodingDoesNotTrip(t *testing.T) {
	page := `<p>Caf&eacute; &amp; Bar &#8212; 100&#37; organic &copy; 2026</p>` +
		`<a href="https://maps.google.com/?q=Caf%C3%A9%20%26%20Bar">Map</a>` +
		`<script>var s = "` + bs + `u00e9t` + bs + `u00e9 \x41\x42";</script>`
	if r := scanString(t, "index.html", page); len(r.Hits) > 0 {
		t.Errorf("ordinary encoding tripped: %v", hitIDs(r))
	}
}

// The operations docs on sitebin.io quote the rules split by <wbr>, which a
// browser shows and copies as one word. Matching does not join text across
// tags; if it ever does, those docs need another way to quote.
func TestTagSplitSignatureIsNotJoined(t *testing.T) {
	r := scanString(t, "docs/operations/index.html",
		`<code>tez:<wbr>//upi</code> <code>api.<wbr>telegram.org/bot</code> <code>eval(<wbr>atob(</code>`)
	if len(r.Hits) > 0 {
		t.Errorf("a tag-split quote matched: %v", hitIDs(r))
	}
}

func TestEntitySpansChunkBoundary(t *testing.T) {
	s := Defaults().NewScan("a.html")
	// the first chunk ends right after the entity's "&"
	s.Write(bytes.Repeat([]byte(" "), chunkSize-6))
	s.Write([]byte(`x tez&#58;//upi/pay?pa=x@ybl`))
	if r := s.Result(); !hasHit(r, "wallet-deeplink") {
		t.Fatalf("an entity across the chunk edge hid the link: %v", hitIDs(r))
	}
}

// The CSP tripwire verifies a report by looking for the destination in the
// site's files with the same scanner: an encoded reference counts there too.
func TestEncodedDestinationIsVerified(t *testing.T) {
	d, ok := Defaults().Destination("https://api.telegram.org/bot1:a/sendMessage")
	if !ok {
		t.Fatal("telegram is a destination")
	}
	s := DestinationRules(d).NewScan("x.js")
	s.Write([]byte(`fetch("https://api.telegram.org/%62ot1:a/sendMessage")`))
	if r := s.Result(); len(r.Hits) != 1 {
		t.Errorf("encoded destination not verified: %v", hitIDs(r))
	}
}
