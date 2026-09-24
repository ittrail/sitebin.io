package forms

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxFields     = 50
	MaxValueRunes = 10000
	// MaxTextBytes bounds everything that is not a file: every field name and
	// value together.
	MaxTextBytes = 256 << 10

	maxFilenameRunes = 200
)

// Field is one submitted name/value pair, in the order the form sent it.
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// File is one uploaded attachment.
type File struct {
	Field       string
	Filename    string
	ContentType string
	Data        []byte
}

// Submission is a parsed form post.
type Submission struct {
	Fields  []Field           // forwarded to the recipient, in form order
	Control map[string]string // _-prefixed fields and altcha: never forwarded
	Files   []File
}

// Honeypot reports whether the off-screen _gotcha field was filled in, which
// only a bot does.
func (s *Submission) Honeypot() bool { return strings.TrimSpace(s.Control["_gotcha"]) != "" }

// HasContent reports whether any forwarded field has a non-blank value.
func (s *Submission) HasContent() bool {
	for _, f := range s.Fields {
		if strings.TrimSpace(f.Value) != "" {
			return true
		}
	}
	return false
}

// Limits are what one form accepts.
type Limits struct {
	AllowFiles   bool  // the form has attachments switched on
	MaxFiles     int   // SITEBIN_FORMS_MAX_FILES
	MaxFileBytes int64 // SITEBIN_FORMS_MAX_FILE_BYTES
}

// ParseError is a refusal shown to the person submitting, with its status.
type ParseError struct {
	Status int
	Msg    string
}

func (e *ParseError) Error() string { return e.Msg }

func refuse(status int, msg string) error { return &ParseError{Status: status, Msg: msg} }

// blockedExt are extensions the big mailbox providers reject a whole message
// over; accepting them would only turn into a failed delivery.
var blockedExt = map[string]bool{
	".exe": true, ".com": true, ".bat": true, ".cmd": true, ".scr": true, ".pif": true,
	".msi": true, ".msp": true, ".jar": true, ".js": true, ".jse": true, ".vbs": true,
	".vbe": true, ".wsf": true, ".wsh": true, ".ps1": true, ".psm1": true, ".hta": true,
	".cpl": true, ".lnk": true, ".reg": true, ".dll": true, ".app": true, ".apk": true,
}

// Parse reads a form post, keeping the order of its fields. r.Body must
// already be capped by the caller (http.MaxBytesReader); Parse enforces the
// per-field and per-file rules. Every error is a *ParseError.
func Parse(r *http.Request, lim Limits) (*Submission, error) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, refuse(415, "send the form as application/x-www-form-urlencoded or multipart/form-data")
	}
	sub := &Submission{Control: map[string]string{}}
	switch mt {
	case "application/x-www-form-urlencoded":
		err = parseURLEncoded(r.Body, sub)
	case "multipart/form-data":
		if params["boundary"] == "" {
			return nil, refuse(400, "the form data is malformed")
		}
		err = parseMultipart(multipart.NewReader(r.Body, params["boundary"]), sub, lim)
	default:
		return nil, refuse(415, "send the form as application/x-www-form-urlencoded or multipart/form-data")
	}
	if err != nil {
		return nil, bodyError(err)
	}
	return sub, nil
}

// bodyError maps a reader failure onto a refusal. A ParseError passes
// through; the request cap is 413; anything else is malformed data.
func bodyError(err error) error {
	var pe *ParseError
	if errors.As(err, &pe) {
		return pe
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return refuse(413, "the submission is too large")
	}
	return refuse(400, "the form data is malformed")
}

// parseURLEncoded splits the body itself: url.ParseQuery collects into a map
// and loses the order the form had.
func parseURLEncoded(body io.Reader, sub *Submission) error {
	raw, err := io.ReadAll(io.LimitReader(body, MaxTextBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > MaxTextBytes {
		return refuse(413, "the message is too long")
	}
	for _, pair := range strings.Split(string(raw), "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		name, err1 := url.QueryUnescape(k)
		value, err2 := url.QueryUnescape(v)
		if err1 != nil || err2 != nil {
			return refuse(400, "the form data is malformed")
		}
		if err := sub.add(name, value); err != nil {
			return err
		}
	}
	return nil
}

func parseMultipart(mr *multipart.Reader, sub *Submission, lim Limits) error {
	text := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := part.FormName()
		_, dp, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		filename, isFile := dp["filename"]
		if !isFile {
			v, err := io.ReadAll(io.LimitReader(part, int64(MaxTextBytes-text)+1))
			if err != nil {
				return err
			}
			if text += len(name) + len(v); text > MaxTextBytes {
				return refuse(413, "the message is too long")
			}
			if err := sub.add(name, string(v)); err != nil {
				return err
			}
			continue
		}
		if !lim.AllowFiles || lim.MaxFiles <= 0 {
			// An untouched file input is an empty part: not an attachment,
			// and not a reason to refuse.
			var one [1]byte
			if n, _ := io.ReadFull(part, one[:]); n > 0 {
				return refuse(400, "this form does not accept files")
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, lim.MaxFileBytes+1))
		if err != nil {
			return err
		}
		if len(data) == 0 {
			continue
		}
		if int64(len(data)) > lim.MaxFileBytes {
			return refuse(413, "each file may be at most "+limitLabel(lim.MaxFileBytes))
		}
		if len(sub.Files) >= lim.MaxFiles {
			return refuse(400, fmt.Sprintf("at most %d files can be attached", lim.MaxFiles))
		}
		fname := cleanFilename(filename)
		ext := strings.ToLower(path.Ext(fname))
		if blockedExt[ext] {
			return refuse(400, "files of this type cannot be sent by email: "+fname)
		}
		ct := mime.TypeByExtension(ext)
		if ct == "" {
			ct = "application/octet-stream"
		}
		sub.Files = append(sub.Files, File{Field: strings.ToValidUTF8(name, "\uFFFD"), Filename: fname, ContentType: ct, Data: data})
	}
}

// add files one name/value pair as a forwarded field or a control field.
func (s *Submission) add(name, value string) error {
	name = strings.ToValidUTF8(name, "\uFFFD")
	value = strings.ToValidUTF8(value, "\uFFFD")
	if name == "" {
		return nil
	}
	if name == "altcha" || strings.HasPrefix(name, "_") {
		if _, seen := s.Control[name]; !seen {
			s.Control[name] = value
		}
		return nil
	}
	if len(s.Fields) >= MaxFields {
		return refuse(400, fmt.Sprintf("the form has more than %d fields", MaxFields))
	}
	if utf8.RuneCountInString(value) > MaxValueRunes {
		return refuse(400, fmt.Sprintf("a field is longer than %d characters", MaxValueRunes))
	}
	s.Fields = append(s.Fields, Field{Name: name, Value: value})
	return nil
}

// cleanFilename keeps only the last path element (old browsers sent
// C:\fakepath\...), drops control characters, and keeps the tail of an
// overlong name so its extension survives.
func cleanFilename(s string) string {
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "\uFFFD"))
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxFilenameRunes {
		s = string(r[len(r)-maxFilenameRunes:])
	}
	if s == "" || s == "." || s == ".." {
		s = "attachment"
	}
	return s
}

// limitLabel states a configured byte limit exactly.
func limitLabel(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// SizeLabel is a file size for people.
func SizeLabel(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
