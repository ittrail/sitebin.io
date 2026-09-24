package forms

import (
	"bytes"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
)

func urlencoded(body string) *http.Request {
	r := httptest.NewRequest("POST", "/_sitebin/forms/k", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

type mpart struct {
	name, filename, body string
	file                 bool
}

func multipartReq(parts ...mpart) *http.Request {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p.file {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.name, p.filename))
			h.Set("Content-Type", "application/octet-stream")
			w, _ := mw.CreatePart(h)
			w.Write([]byte(p.body))
		} else {
			mw.WriteField(p.name, p.body)
		}
	}
	mw.Close()
	r := httptest.NewRequest("POST", "/_sitebin/forms/k", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

var withFiles = Limits{AllowFiles: true, MaxFiles: 5, MaxFileBytes: 2 << 20}

func wantRefusal(t *testing.T, err error, status int) {
	t.Helper()
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Status != status {
		t.Fatalf("err = %v, want a %d refusal", err, status)
	}
}

func TestParseURLEncodedKeepsOrderAndRepeats(t *testing.T) {
	sub, err := Parse(urlencoded("name=Anna+M%C3%BCller&topics=a&email=a%40example.com&topics=b&_subject=Hi&_gotcha=&altcha=xyz"), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Field{{"name", "Anna Müller"}, {"topics", "a"}, {"email", "a@example.com"}, {"topics", "b"}}
	if fmt.Sprint(sub.Fields) != fmt.Sprint(want) {
		t.Errorf("fields = %v, want %v", sub.Fields, want)
	}
	if sub.Control["_subject"] != "Hi" || sub.Control["altcha"] != "xyz" {
		t.Errorf("control = %v", sub.Control)
	}
	if sub.Honeypot() || !sub.HasContent() {
		t.Errorf("honeypot=%v content=%v", sub.Honeypot(), sub.HasContent())
	}
}

func TestParseMultipartKeepsOrder(t *testing.T) {
	sub, err := Parse(multipartReq(mpart{name: "z", body: "1"}, mpart{name: "a", body: "2"}, mpart{name: "m", body: "3"}), withFiles)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sub.Fields) != fmt.Sprint([]Field{{"z", "1"}, {"a", "2"}, {"m", "3"}}) {
		t.Errorf("fields = %v", sub.Fields)
	}
}

func TestParseHoneypotAndEmptiness(t *testing.T) {
	sub, _ := Parse(urlencoded("name=bot&_gotcha=http%3A%2F%2Fspam"), Limits{})
	if !sub.Honeypot() {
		t.Error("a filled _gotcha is a bot")
	}
	sub, _ = Parse(urlencoded("name=&message=+"), Limits{})
	if sub.HasContent() {
		t.Error("blank fields are no content")
	}
}

func TestParseFieldLimits(t *testing.T) {
	var many []string
	for i := 0; i <= MaxFields; i++ {
		many = append(many, fmt.Sprintf("f%d=x", i))
	}
	_, err := Parse(urlencoded(strings.Join(many, "&")), Limits{})
	wantRefusal(t, err, 400)

	if _, err := Parse(urlencoded("m="+strings.Repeat("ä", MaxValueRunes)), Limits{}); err != nil {
		t.Errorf("a value of exactly %d characters is allowed: %v", MaxValueRunes, err)
	}
	_, err = Parse(urlencoded("m="+strings.Repeat("a", MaxValueRunes+1)), Limits{})
	wantRefusal(t, err, 400)
}

func TestParseInvalidUTF8IsReplaced(t *testing.T) {
	sub, err := Parse(urlencoded("name=%FFok"), Limits{})
	if err != nil || sub.Fields[0].Value != "\uFFFDok" {
		t.Fatalf("got %v, %v", sub, err)
	}
}

func TestParseRefusesOtherContentTypes(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":1}`))
	r.Header.Set("Content-Type", "application/json")
	_, err := Parse(r, Limits{})
	wantRefusal(t, err, 415)
	r = httptest.NewRequest("POST", "/", strings.NewReader("a=1"))
	_, err = Parse(r, Limits{})
	wantRefusal(t, err, 415)
}

func TestParseTextTooLarge(t *testing.T) {
	_, err := Parse(urlencoded("m="+strings.Repeat("a", MaxTextBytes)), Limits{})
	wantRefusal(t, err, 413)
}

func TestParseRequestCapIs413(t *testing.T) {
	r := multipartReq(mpart{name: "f", filename: "a.pdf", body: strings.Repeat("a", 5000), file: true})
	r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, 1000)
	_, err := Parse(r, withFiles)
	wantRefusal(t, err, 413)
}

func TestParseFilesRefusedWhenOff(t *testing.T) {
	_, err := Parse(multipartReq(mpart{name: "cv", filename: "cv.pdf", body: "%PDF", file: true}), Limits{AllowFiles: false, MaxFiles: 5, MaxFileBytes: 100})
	wantRefusal(t, err, 400)
	_, err = Parse(multipartReq(mpart{name: "cv", filename: "cv.pdf", body: "%PDF", file: true}), Limits{AllowFiles: true, MaxFiles: 0, MaxFileBytes: 100})
	wantRefusal(t, err, 400)
}

// Review Focus 2: a browser sends an untouched file input as a part with an
// empty filename and no bytes. That is "no file", on any form.
func TestParseEmptyFileInputIgnored(t *testing.T) {
	for _, lim := range []Limits{withFiles, {}} {
		sub, err := Parse(multipartReq(mpart{name: "message", body: "hi"}, mpart{name: "cv", filename: "", body: "", file: true}), lim)
		if err != nil {
			t.Fatalf("limits %+v: %v", lim, err)
		}
		if len(sub.Files) != 0 || len(sub.Fields) != 1 {
			t.Errorf("limits %+v: files=%d fields=%v", lim, len(sub.Files), sub.Fields)
		}
	}
}

func TestParseFileCountAndSize(t *testing.T) {
	lim := Limits{AllowFiles: true, MaxFiles: 2, MaxFileBytes: 10}
	three := []mpart{
		{name: "f", filename: "a.txt", body: "a", file: true},
		{name: "f", filename: "b.txt", body: "b", file: true},
		{name: "f", filename: "c.txt", body: "c", file: true},
	}
	_, err := Parse(multipartReq(three...), lim)
	wantRefusal(t, err, 400)
	_, err = Parse(multipartReq(mpart{name: "f", filename: "big.txt", body: strings.Repeat("x", 11), file: true}), lim)
	wantRefusal(t, err, 413)
	sub, err := Parse(multipartReq(mpart{name: "f", filename: "ok.txt", body: strings.Repeat("x", 10), file: true}), lim)
	if err != nil || len(sub.Files) != 1 {
		t.Fatalf("a file of exactly the cap: %v", err)
	}
}

func TestParseBlockedExtensions(t *testing.T) {
	for _, name := range []string{"Setup.EXE", "invoice.pdf.exe", "run.ps1", "x.js"} {
		_, err := Parse(multipartReq(mpart{name: "f", filename: name, body: "x", file: true}), withFiles)
		wantRefusal(t, err, 400)
	}
	if _, err := Parse(multipartReq(mpart{name: "f", filename: "archive.exe.pdf", body: "x", file: true}), withFiles); err != nil {
		t.Errorf("only the last extension counts: %v", err)
	}
}

func TestParseFilenameAndType(t *testing.T) {
	sub, err := Parse(multipartReq(mpart{name: "cv", filename: `C:\fakepath\Lebenslauf.pdf`, body: "%PDF", file: true}), withFiles)
	if err != nil {
		t.Fatal(err)
	}
	f := sub.Files[0]
	if f.Field != "cv" || f.Filename != "Lebenslauf.pdf" || f.ContentType != "application/pdf" || string(f.Data) != "%PDF" {
		t.Errorf("file = %+v", f)
	}
}

func TestSizeLabel(t *testing.T) {
	for n, want := range map[int64]string{512: "512 B", 183244: "179 KB", 2 << 20: "2.0 MB"} {
		if got := SizeLabel(n); got != want {
			t.Errorf("SizeLabel(%d) = %q, want %q", n, got, want)
		}
	}
}
