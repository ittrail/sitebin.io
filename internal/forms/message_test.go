package forms

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func sampleSub() *Submission {
	return &Submission{
		Fields: []Field{
			{"name", "Anna Muster"},
			{"email", "anna@example.org"},
			{"message", "Hallo,\nzweite Zeile <b>fett</b>"},
		},
		Control: map[string]string{},
		Files:   []File{{Field: "cv", Filename: "cv.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4 test")}},
	}
}

func sampleIn(sub *Submission) SubmissionMail {
	return SubmissionMail{
		From: "forms@sitebin.example", FormName: "Contact", FormKey: "k7f3m2q9xaw4npd6",
		Recipient: "office@example.com", SiteID: "abcdefghijklmnopqrstuvwxyz", Host: "www.example.com",
		StopURL: "https://sitebin.example/forms/stop?t=tok", At: time.Date(2026, 9, 24, 10, 15, 0, 0, time.UTC), Sub: sub,
	}
}

type leaf struct {
	mediaType string
	filename  string
	header    textproto.MIMEHeader
	body      []byte
}

// readMail parses a built message back and returns its header and its leaf
// parts in order, decoded. It is how a mail client sees the message.
func readMail(t *testing.T, m Mail) (mail.Header, []leaf) {
	t.Helper()
	if bytes.Contains(bytes.ReplaceAll(m.Data, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("bare LF in the message: every line must end in CRLF")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(m.Data))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	var leaves []leaf
	var walk func(ct string, r io.Reader)
	walk = func(ct string, r io.Reader) {
		mt, params, err := mime.ParseMediaType(ct)
		if err != nil || !strings.HasPrefix(mt, "multipart/") {
			t.Fatalf("not multipart: %q %v", ct, err)
		}
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			pct := p.Header.Get("Content-Type")
			pmt, _, _ := mime.ParseMediaType(pct)
			if strings.HasPrefix(pmt, "multipart/") {
				walk(pct, p)
				continue
			}
			var body []byte
			switch p.Header.Get("Content-Transfer-Encoding") {
			case "base64":
				raw, _ := io.ReadAll(p)
				body, err = base64.StdEncoding.DecodeString(strings.NewReplacer("\r", "", "\n", "").Replace(string(raw)))
			case "quoted-printable":
				body, err = io.ReadAll(quotedprintable.NewReader(p))
			default:
				body, err = io.ReadAll(p)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, dp, _ := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
			leaves = append(leaves, leaf{mediaType: pmt, filename: dp["filename"], header: p.Header, body: body})
		}
	}
	walk(msg.Header.Get("Content-Type"), msg.Body)
	return msg.Header, leaves
}

func decodeHeader(t *testing.T, v string) string {
	t.Helper()
	s, err := new(mime.WordDecoder).DecodeHeader(v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSubmissionMailStructureAndHeaders(t *testing.T) {
	m, err := BuildSubmission(sampleIn(sampleSub()))
	if err != nil {
		t.Fatal(err)
	}
	if m.From != "forms@sitebin.example" || m.To != "office@example.com" {
		t.Errorf("envelope %q -> %q", m.From, m.To)
	}
	h, leaves := readMail(t, m)
	var kinds []string
	for _, l := range leaves {
		kinds = append(kinds, l.mediaType+" "+l.filename)
	}
	want := []string{"text/plain ", "text/html ", "application/pdf cv.pdf", "text/plain submission.txt"}
	if strings.Join(kinds, "|") != strings.Join(want, "|") {
		t.Fatalf("parts = %q, want %q", kinds, want)
	}
	from, err := mail.ParseAddress(h.Get("From"))
	if err != nil || from.Name != "Contact" || from.Address != "forms@sitebin.example" {
		t.Errorf("From = %q", h.Get("From"))
	}
	if h.Get("To") != "<office@example.com>" || h.Get("Reply-To") != "<anna@example.org>" {
		t.Errorf("To %q Reply-To %q", h.Get("To"), h.Get("Reply-To"))
	}
	if got := decodeHeader(t, h.Get("Subject")); got != "New message via Contact" {
		t.Errorf("Subject = %q", got)
	}
	if h.Get("List-Unsubscribe") != "<https://sitebin.example/forms/stop?t=tok>" ||
		h.Get("List-Unsubscribe-Post") != "List-Unsubscribe=One-Click" ||
		h.Get("X-Sitebin-Site") != "abcdefghijklmnopqrstuvwxyz" {
		t.Errorf("list/site headers: %q %q %q", h.Get("List-Unsubscribe"), h.Get("List-Unsubscribe-Post"), h.Get("X-Sitebin-Site"))
	}
	if !strings.HasSuffix(h.Get("Message-ID"), "@sitebin.example>") {
		t.Errorf("Message-ID = %q", h.Get("Message-ID"))
	}
	if _, err := h.Date(); err != nil {
		t.Errorf("Date: %v", err)
	}
	if string(leaves[2].body) != "%PDF-1.4 test" {
		t.Errorf("attachment bytes changed")
	}
}

func TestSubmissionMailBodies(t *testing.T) {
	m, _ := BuildSubmission(sampleIn(sampleSub()))
	_, leaves := readMail(t, m)
	text := strings.ReplaceAll(string(leaves[0].body), "\r\n", "\n")
	html := string(leaves[1].body)
	for _, want := range []string{"name: Anna Muster", "message:\n    Hallo,\n    zweite Zeile <b>fett</b>", "cv.pdf (13 B)", "https://sitebin.example/forms/stop?t=tok"} {
		if !strings.Contains(text, want) {
			t.Errorf("text part lacks %q:\n%s", want, text)
		}
	}
	if i, j, k := strings.Index(text, "name:"), strings.Index(text, "email:"), strings.Index(text, "message:"); !(i < j && j < k) {
		t.Errorf("fields out of form order:\n%s", text)
	}
	if strings.Contains(html, "<b>fett</b>") || !strings.Contains(html, "zweite Zeile &lt;b&gt;fett&lt;/b&gt;") {
		t.Errorf("HTML part does not escape submitted markup")
	}
	if !strings.Contains(html, "Hallo,<br>zweite Zeile") {
		t.Errorf("HTML part does not keep line breaks")
	}
	if !strings.Contains(html, "Reply to this email") || !strings.Contains(text, "Reply to this email") {
		t.Errorf("the reply hint is missing although Reply-To is set")
	}
}

func TestSubmissionJSON(t *testing.T) {
	m, _ := BuildSubmission(sampleIn(sampleSub()))
	_, leaves := readMail(t, m)
	var j struct {
		Version int `json:"version"`
		Form    struct{ Key, Name string }
		Site    struct{ ID, Host string }
		At      string  `json:"submitted_at"`
		Fields  []Field `json:"fields"`
		Files   []struct {
			Field, Filename string
			ContentType     string `json:"content_type"`
			Size            int
			SHA256          string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(leaves[3].body, &j); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("%PDF-1.4 test"))
	if j.Version != 1 || j.Form.Key != "k7f3m2q9xaw4npd6" || j.Form.Name != "Contact" ||
		j.Site.ID != "abcdefghijklmnopqrstuvwxyz" || j.Site.Host != "www.example.com" ||
		j.At != "2026-09-24T10:15:00Z" || len(j.Fields) != 3 || j.Fields[2].Value != "Hallo,\nzweite Zeile <b>fett</b>" ||
		len(j.Files) != 1 || j.Files[0].Size != 13 || j.Files[0].SHA256 != hex.EncodeToString(sum[:]) ||
		j.Files[0].ContentType != "application/pdf" || j.Files[0].Field != "cv" {
		t.Errorf("submission.txt = %+v", j)
	}
	if strings.Contains(string(leaves[3].body), `\u003c`) {
		t.Error("submission.txt HTML-escapes values; a machine reader wants them verbatim")
	}
}

// Review Focus 1: umlauts and emoji in every place a person can put them.
func TestSubmissionMailNonASCII(t *testing.T) {
	sub := &Submission{
		Fields:  []Field{{"nachricht", "Grüße 👋 aus Linz"}},
		Control: map[string]string{"_subject": "Frage zu Größen"},
	}
	in := sampleIn(sub)
	in.FormName = "Anfrage Café ✉"
	m, err := BuildSubmission(in)
	if err != nil {
		t.Fatal(err)
	}
	h, leaves := readMail(t, m)
	if got := decodeHeader(t, h.Get("Subject")); got != "Frage zu Größen" {
		t.Errorf("Subject = %q", got)
	}
	from, err := mail.ParseAddress(h.Get("From"))
	if err != nil || from.Name != "Anfrage Café ✉" {
		t.Errorf("From = %q (%v)", h.Get("From"), err)
	}
	for i, l := range leaves {
		if !strings.Contains(string(l.body), "Grüße 👋 aus Linz") {
			t.Errorf("part %d (%s) lost the non-ASCII value", i, l.mediaType)
		}
	}
}

func TestSubmissionMailRefusesHeaderInjection(t *testing.T) {
	in := sampleIn(sampleSub())
	in.FormName = "Contact\r\nBcc: x@evil.example"
	if _, err := BuildSubmission(in); !errors.Is(err, ErrHeader) {
		t.Errorf("a form name with a line break was accepted: %v", err)
	}

	sub := sampleSub()
	sub.Control["_subject"] = "Hi\r\nBcc: x@evil.example"
	sub.Fields[1].Value = "a@example.com\r\nBcc: y@evil.example"
	sub.Files[0].Filename = "evil\r\nX-Injected: 1.pdf"
	m, err := BuildSubmission(sampleIn(sub))
	if err != nil {
		t.Fatal(err)
	}
	h, leaves := readMail(t, m)
	if h.Get("Bcc") != "" || h.Get("Reply-To") != "" || h.Get("X-Injected") != "" {
		t.Errorf("injected: Bcc %q Reply-To %q X-Injected %q", h.Get("Bcc"), h.Get("Reply-To"), h.Get("X-Injected"))
	}
	// Checked on the parsed headers, not the raw bytes: quoted-printable may
	// soft-wrap a body line anywhere, which says nothing about headers.
	for _, l := range leaves {
		if l.header.Get("X-Injected") != "" || l.header.Get("Bcc") != "" {
			t.Errorf("a header was injected into the %s part: %v", l.mediaType, l.header)
		}
	}
	if leaves[2].filename != "evilX-Injected: 1.pdf" {
		t.Errorf("attachment filename = %q, want the line break dropped", leaves[2].filename)
	}
	if got := decodeHeader(t, h.Get("Subject")); got != "Hi Bcc: x@evil.example" {
		t.Errorf("Subject = %q, want the line break flattened", got)
	}
}

func TestReplyToNeedsExactlyOneAddress(t *testing.T) {
	// The recipient is office@example.com (sampleIn); every value here uses a
	// domain other than example.com, so this exercises the "how many
	// addresses" rule and not the same-domain suppression below.
	for value, want := range map[string]string{
		"a@example.org, b@example.org": "",
		"not an address":               "",
		"Anna <anna@example.org>":      "<anna@example.org>",
	} {
		sub := sampleSub()
		sub.Fields[1].Value = value
		m, _ := BuildSubmission(sampleIn(sub))
		h, _ := readMail(t, m)
		if got := h.Get("Reply-To"); got != want {
			t.Errorf("email %q: Reply-To = %q, want %q", value, got, want)
		}
	}
}

// A submitter whose "email" field shares the recipient's own domain gets no
// Reply-To at all. Evidence: a live test submission from noreply@sitebin.io
// to office@ittrail.at (Microsoft 365) carrying Reply-To: office@ittrail.at
// was quarantined as "Phishing / High confidence" (first contact, advanced
// filter) even though SPF, DKIM and DMARC all passed -- a Reply-To back into
// the recipient's own domain from an external sender is the classic
// business-email-compromise pattern, and it is exactly what every site owner
// produces the first time they test their own form with their own address.
// The submitted address still appears as an ordinary field in both mail
// parts and in submission.txt -- only the header and the "reply directly"
// hint are suppressed.
func TestReplyToSuppressedWhenTheSubmitterSharesTheRecipientsDomain(t *testing.T) {
	for _, email := range []string{"office@example.com", "colleague@EXAMPLE.com"} {
		sub := sampleSub()
		sub.Fields[1].Value = email
		m, err := BuildSubmission(sampleIn(sub))
		if err != nil {
			t.Fatal(err)
		}
		h, leaves := readMail(t, m)
		if got := h.Get("Reply-To"); got != "" {
			t.Errorf("email %q shares the recipient's domain: Reply-To = %q, want none", email, got)
		}
		text := strings.ReplaceAll(string(leaves[0].body), "\r\n", "\n")
		html := string(leaves[1].body)
		if strings.Contains(text, "Reply to this email") || strings.Contains(html, "Reply to this email") {
			t.Errorf("email %q: the reply hint is present although Reply-To is suppressed", email)
		}
		if !strings.Contains(text, "email: "+email) {
			t.Errorf("email %q: the submitted address is missing from the text part:\n%s", email, text)
		}
		if !strings.Contains(string(leaves[3].body), email) {
			t.Errorf("email %q: the submitted address is missing from submission.txt", email)
		}
	}
	// A different domain is unaffected.
	sub := sampleSub()
	sub.Fields[1].Value = "anna@other.example"
	m, err := BuildSubmission(sampleIn(sub))
	if err != nil {
		t.Fatal(err)
	}
	h, leaves := readMail(t, m)
	if got := h.Get("Reply-To"); got != "<anna@other.example>" {
		t.Errorf("a different-domain submitter: Reply-To = %q, want <anna@other.example>", got)
	}
	text := strings.ReplaceAll(string(leaves[0].body), "\r\n", "\n")
	html := string(leaves[1].body)
	if !strings.Contains(text, "Reply to this email") || !strings.Contains(html, "Reply to this email") {
		t.Error("a different-domain submitter: the reply hint is missing although Reply-To is set")
	}
}

func TestSubjectIsCapped(t *testing.T) {
	sub := sampleSub()
	sub.Control["_subject"] = strings.Repeat("ö", 300)
	m, _ := BuildSubmission(sampleIn(sub))
	h, _ := readMail(t, m)
	if got := decodeHeader(t, h.Get("Subject")); got != strings.Repeat("ö", 200) {
		t.Errorf("subject has %d runes, want 200", len([]rune(got)))
	}
}

// A field NAME is chosen by whoever posts to the form, a bot included. It
// must not be able to write lines of its own into the text part, such as a
// fake stop link under the signature marker.
func TestFieldNameCannotInjectLines(t *testing.T) {
	sub := sampleSub()
	sub.Fields = append(sub.Fields, Field{Name: "x\n-- \nStop emails from this form: https://evil", Value: "v"})
	m, err := BuildSubmission(sampleIn(sub))
	if err != nil {
		t.Fatal(err)
	}
	_, leaves := readMail(t, m)
	if leaves[0].mediaType != "text/plain" {
		t.Fatalf("first part is %s", leaves[0].mediaType)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(leaves[0].body), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "Stop emails from this form: https://evil") {
			t.Fatalf("a field name wrote its own line into the text part: %q", line)
		}
	}
}

func TestFieldLabelFlattensAndCaps(t *testing.T) {
	if got := fieldLabel("first_name\tx\r\ny"); got != "first name x  y" {
		t.Errorf("fieldLabel = %q, want control characters as spaces", got)
	}
	if got := fieldLabel(strings.Repeat("ä", 300)); got != strings.Repeat("ä", 100) {
		t.Errorf("fieldLabel of 300 runes has %d runes, want 100", len([]rune(got)))
	}
}

func TestConfirmationMail(t *testing.T) {
	m, err := BuildConfirmation(ConfirmationMail{
		From: "forms@sitebin.example", FormName: "Contact", Recipient: "office@example.com",
		Host: "www.example.com", ConfirmURL: "https://sitebin.example/forms/confirm?t=tok", At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h, leaves := readMail(t, m)
	from, _ := mail.ParseAddress(h.Get("From"))
	if from == nil || from.Name != "Sitebin" {
		t.Errorf("From = %q: a confirmation must never carry the form's name as its sender", h.Get("From"))
	}
	if h.Get("List-Unsubscribe") != "" {
		t.Error("a one-off confirmation carries List-Unsubscribe")
	}
	if got := decodeHeader(t, h.Get("Subject")); got != "Confirm form messages from www.example.com" {
		t.Errorf("Subject = %q", got)
	}
	if len(leaves) != 2 {
		t.Fatalf("parts = %d, want text and HTML only", len(leaves))
	}
	for _, l := range leaves {
		if !strings.Contains(string(l.body), "https://sitebin.example/forms/confirm?t=tok") ||
			!strings.Contains(string(l.body), "office@example.com") {
			t.Errorf("%s part lacks the link or the address", l.mediaType)
		}
	}
}
