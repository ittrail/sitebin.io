package forms

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
	"unicode"
)

//go:embed templates/*.html
var templateFS embed.FS

var mailTmpl = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// Mail is a message ready for SMTP: the envelope and the RFC 5322 bytes.
type Mail struct {
	From string // envelope sender: the instance's address
	To   string // envelope recipient
	Data []byte // CRLF line endings throughout
}

// SubmissionMail is everything a forwarded submission is built from.
type SubmissionMail struct {
	From      string // SITEBIN_FORMS_SMTP_FROM
	FormName  string
	FormKey   string
	Recipient string
	SiteID    string // the site's view id
	Host      string // the host the form was submitted on
	StopURL   string
	At        time.Time
	Sub       *Submission
}

// ConfirmationMail asks a recipient to accept a form's submissions.
type ConfirmationMail struct {
	From       string
	FormName   string
	Recipient  string
	Host       string
	ConfirmURL string
	At         time.Time
}

// ErrHeader refuses a header value that carries a line break.
var ErrHeader = errors.New("a header value contains a line break")

const maxSubjectRunes = 200

func headerSafe(vals ...string) error {
	for _, v := range vals {
		if strings.ContainsAny(v, "\r\n") {
			return ErrHeader
		}
	}
	return nil
}

type header struct{ k, v string }

type attachment struct {
	name, contentType string
	data              []byte
}

// mailView is what the HTML templates and the text builders read.
type mailView struct {
	Preheader  string
	FormName   string
	Host       string
	Recipient  string
	Fields     []viewField
	Files      []viewFile
	At         string
	StopURL    string
	ConfirmURL string
	ReplyHint  bool
}

type viewField struct {
	Label string
	Lines []string
	Empty bool
}

type viewFile struct{ Name, Size string }

// BuildSubmission builds the mail that forwards one submission.
func BuildSubmission(in SubmissionMail) (Mail, error) {
	if err := headerSafe(in.From, in.FormName, in.Recipient, in.SiteID, in.StopURL); err != nil {
		return Mail{}, err
	}
	subject := "New message via " + in.FormName
	if s := cleanSubject(in.Sub.Control["_subject"]); s != "" {
		subject = s
	}
	replyTo := replyAddress(in.Sub.Fields)
	v := mailView{
		FormName:  in.FormName,
		Host:      in.Host,
		At:        in.At.UTC().Format("2 Jan 2006, 15:04 UTC"),
		StopURL:   in.StopURL,
		ReplyHint: replyTo != "",
		Preheader: preheader(in.Sub.Fields),
	}
	for _, f := range in.Sub.Fields {
		lines := strings.Split(strings.ReplaceAll(f.Value, "\r\n", "\n"), "\n")
		v.Fields = append(v.Fields, viewField{Label: fieldLabel(f.Name), Lines: lines, Empty: strings.TrimSpace(f.Value) == ""})
	}
	for _, f := range in.Sub.Files {
		v.Files = append(v.Files, viewFile{Name: f.Filename, Size: SizeLabel(int64(len(f.Data)))})
	}
	var html bytes.Buffer
	if err := mailTmpl.ExecuteTemplate(&html, "submission.html", v); err != nil {
		return Mail{}, err
	}
	js, err := submissionJSON(in)
	if err != nil {
		return Mail{}, err
	}
	hs := []header{
		{"From", (&mail.Address{Name: in.FormName, Address: in.From}).String()},
		{"To", (&mail.Address{Address: in.Recipient}).String()},
	}
	if replyTo != "" {
		hs = append(hs, header{"Reply-To", (&mail.Address{Address: replyTo}).String()})
	}
	hs = append(hs,
		header{"Subject", mime.QEncoding.Encode("utf-8", subject)},
		header{"Date", in.At.UTC().Format(time.RFC1123Z)},
		header{"Message-ID", messageID(in.From)},
		header{"List-Unsubscribe", "<" + in.StopURL + ">"},
		header{"List-Unsubscribe-Post", "List-Unsubscribe=One-Click"},
		header{"X-Sitebin-Site", in.SiteID},
	)
	atts := make([]attachment, 0, len(in.Sub.Files)+1)
	for _, f := range in.Sub.Files {
		atts = append(atts, attachment{name: f.Filename, contentType: f.ContentType, data: f.Data})
	}
	atts = append(atts, attachment{name: "submission.json", contentType: "application/json", data: js})
	data, err := compose(hs, submissionText(v), html.String(), atts)
	return Mail{From: in.From, To: in.Recipient, Data: data}, err
}

// BuildConfirmation builds the one mail that asks a recipient to accept a
// form's submissions. Its sender is always "Sitebin": the person who created
// the form does not get to choose who this mail appears to come from.
func BuildConfirmation(in ConfirmationMail) (Mail, error) {
	if err := headerSafe(in.From, in.FormName, in.Recipient, in.Host, in.ConfirmURL); err != nil {
		return Mail{}, err
	}
	v := mailView{
		FormName:   in.FormName,
		Host:       in.Host,
		Recipient:  in.Recipient,
		ConfirmURL: in.ConfirmURL,
		At:         in.At.UTC().Format("2 Jan 2006, 15:04 UTC"),
		Preheader:  in.Host + " would like to email you the messages from one of its forms.",
	}
	var html bytes.Buffer
	if err := mailTmpl.ExecuteTemplate(&html, "confirm.html", v); err != nil {
		return Mail{}, err
	}
	hs := []header{
		{"From", (&mail.Address{Name: "Sitebin", Address: in.From}).String()},
		{"To", (&mail.Address{Address: in.Recipient}).String()},
		{"Subject", mime.QEncoding.Encode("utf-8", "Confirm form messages from "+in.Host)},
		{"Date", in.At.UTC().Format(time.RFC1123Z)},
		{"Message-ID", messageID(in.From)},
	}
	data, err := compose(hs, confirmText(v), html.String(), nil)
	return Mail{From: in.From, To: in.Recipient, Data: data}, err
}

func submissionText(v mailView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "New message via %s on %s\n\n", v.FormName, v.Host)
	for _, f := range v.Fields {
		if len(f.Lines) == 1 {
			fmt.Fprintf(&b, "%s: %s\n", f.Label, f.Lines[0])
			continue
		}
		fmt.Fprintf(&b, "%s:\n", f.Label)
		for _, l := range f.Lines {
			fmt.Fprintf(&b, "    %s\n", l)
		}
	}
	if len(v.Files) > 0 {
		b.WriteString("\nAttachments:\n")
		for _, f := range v.Files {
			fmt.Fprintf(&b, "    %s (%s)\n", f.Name, f.Size)
		}
	}
	b.WriteString("\n-- \n")
	fmt.Fprintf(&b, "Sent %s through the form \"%s\" on %s.\n", v.At, v.FormName, v.Host)
	if v.ReplyHint {
		b.WriteString("Reply to this email to answer the sender directly.\n")
	}
	fmt.Fprintf(&b, "Stop emails from this form: %s\n", v.StopURL)
	return b.String()
}

func confirmText(v mailView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s would like to send the messages of its form \"%s\" to this address (%s).\n\n", v.Host, v.FormName, v.Recipient)
	fmt.Fprintf(&b, "To agree, open this link within 7 days:\n%s\n\n", v.ConfirmURL)
	b.WriteString("If you did not expect this email, ignore it. Nothing is sent to you unless you confirm,\n")
	b.WriteString("and every message you receive carries a link to stop them.\n\n-- \nSitebin\n")
	return b.String()
}

type jsonForm struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type jsonSite struct {
	ID   string `json:"id"`
	Host string `json:"host"`
}

type jsonFile struct {
	Field       string `json:"field"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	SHA256      string `json:"sha256"`
}

type jsonSubmission struct {
	Version     int        `json:"version"`
	Form        jsonForm   `json:"form"`
	Site        jsonSite   `json:"site"`
	SubmittedAt time.Time  `json:"submitted_at"`
	Fields      []Field    `json:"fields"`
	Files       []jsonFile `json:"files"`
}

// submissionJSON is the machine-readable copy. The client IP is deliberately
// not in it: the owner gets what the person typed, nothing more.
func submissionJSON(in SubmissionMail) ([]byte, error) {
	j := jsonSubmission{
		Version:     1,
		Form:        jsonForm{Key: in.FormKey, Name: in.FormName},
		Site:        jsonSite{ID: in.SiteID, Host: in.Host},
		SubmittedAt: in.At.UTC().Truncate(time.Second),
		Fields:      in.Sub.Fields,
		Files:       []jsonFile{},
	}
	if j.Fields == nil {
		j.Fields = []Field{}
	}
	for _, f := range in.Sub.Files {
		sum := sha256.Sum256(f.Data)
		j.Files = append(j.Files, jsonFile{Field: f.Field, Filename: f.Filename, ContentType: f.ContentType, Size: len(f.Data), SHA256: hex.EncodeToString(sum[:])})
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(j); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// compose assembles the MIME tree: multipart/mixed holding a
// multipart/alternative (text, then HTML: clients show the last one they can
// render) followed by the attachments.
func compose(hs []header, text, html string, atts []attachment) ([]byte, error) {
	var alt bytes.Buffer
	altw := multipart.NewWriter(&alt)
	if err := writeQP(altw, "text/plain; charset=utf-8", text); err != nil {
		return nil, err
	}
	if err := writeQP(altw, "text/html; charset=utf-8", html); err != nil {
		return nil, err
	}
	if err := altw.Close(); err != nil {
		return nil, err
	}

	var body bytes.Buffer
	mixed := multipart.NewWriter(&body)
	p, err := mixed.CreatePart(textproto.MIMEHeader{
		"Content-Type": {mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": altw.Boundary()})},
	})
	if err != nil {
		return nil, err
	}
	if _, err := p.Write(alt.Bytes()); err != nil {
		return nil, err
	}
	for _, a := range atts {
		name := safeFilename(a.name)
		ct := mime.FormatMediaType(a.contentType, map[string]string{"name": name})
		if ct == "" {
			ct = mime.FormatMediaType("application/octet-stream", map[string]string{"name": name})
		}
		w, err := mixed.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {ct},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": name})},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeBase64(w, a.data); err != nil {
			return nil, err
		}
	}
	if err := mixed.Close(); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	for _, h := range hs {
		writeHeader(&out, h.k, h.v)
	}
	writeHeader(&out, "MIME-Version", "1.0")
	writeHeader(&out, "Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mixed.Boundary()}))
	out.WriteString("\r\n")
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

// writeQP writes one text part, quoted-printable. In text mode the encoder
// turns every line break into CRLF, so the templates may use plain \n.
func writeQP(mw *multipart.Writer, contentType, s string) error {
	w, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {contentType},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return err
	}
	qp := quotedprintable.NewWriter(w)
	if _, err := io.WriteString(qp, s); err != nil {
		return err
	}
	return qp.Close()
}

// writeBase64 writes data base64-encoded in 76-character lines.
func writeBase64(w io.Writer, data []byte) error {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		if _, err := io.WriteString(w, enc[:76]+"\r\n"); err != nil {
			return err
		}
		enc = enc[76:]
	}
	_, err := io.WriteString(w, enc+"\r\n")
	return err
}

// writeHeader writes one header line, folded at spaces past 76 columns.
// Folding inserts CRLF plus a space and never removes a character, so the
// value unfolds to exactly what was given.
func writeHeader(b *bytes.Buffer, k, v string) {
	b.WriteString(k + ":")
	col := len(k) + 1
	for _, word := range strings.Split(v, " ") {
		if col+1+len(word) > 76 && col > len(k)+1 {
			b.WriteString("\r\n")
			col = 0
		}
		b.WriteString(" " + word)
		col += 1 + len(word)
	}
	b.WriteString("\r\n")
}

func messageID(from string) string {
	var r [16]byte
	rand.Read(r[:])
	domain := "sitebin.invalid"
	if i := strings.LastIndexByte(from, '@'); i >= 0 {
		domain = from[i+1:]
	}
	return "<" + hex.EncodeToString(r[:]) + "@" + domain + ">"
}

// cleanSubject flattens a submitted _subject into one line of at most 200
// characters. Control characters become spaces, CR and LF above all.
func cleanSubject(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "\uFFFD"))
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxSubjectRunes {
		s = string(r[:maxSubjectRunes])
	}
	return s
}

// replyAddress is the submitted "email" field when it is exactly one address.
// Only the bare address is used; a display name typed into a form is not
// worth carrying into a header.
func replyAddress(fields []Field) string {
	for _, f := range fields {
		if strings.EqualFold(f.Name, "email") {
			a, err := mail.ParseAddress(strings.TrimSpace(f.Value))
			if err != nil || strings.ContainsAny(a.Address, "\r\n") {
				return ""
			}
			return a.Address
		}
	}
	return ""
}

// safeFilename drops control characters from a filename for the MIME
// headers. The parser already does this; a mail is built from other inputs
// too (the preview tool, tests), and a header must never depend on that.
func safeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return "attachment"
	}
	return s
}

const maxLabelRunes = 100

// fieldLabel turns a field name into a label: first_name → first name. The
// name is whatever the poster sent, so control characters become spaces (a
// line break must not start a line of its own in the text part) and the
// label is capped.
func fieldLabel(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r), r == '_', r == '-':
			return ' '
		}
		return r
	}, name)
	if r := []rune(name); len(r) > maxLabelRunes {
		name = string(r[:maxLabelRunes])
	}
	return name
}

// preheader is the line an inbox shows under the subject: the start of the
// message field if there is one, else of the first field.
func preheader(fields []Field) string {
	pick := ""
	for _, f := range fields {
		if strings.EqualFold(f.Name, "message") {
			pick = f.Value
			break
		}
	}
	if pick == "" && len(fields) > 0 {
		pick = fields[0].Value
	}
	pick = strings.Join(strings.Fields(pick), " ")
	if r := []rune(pick); len(r) > 110 {
		pick = string(r[:110]) + "…"
	}
	return pick
}
