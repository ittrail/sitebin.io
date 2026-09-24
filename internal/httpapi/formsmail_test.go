package httpapi

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/ittrail/sitebin.io/internal/forms"
)

// recSender records what would have been mailed.
type recSender struct {
	mu   sync.Mutex
	sent []forms.Mail
	err  error
}

func (s *recSender) Send(_ context.Context, m forms.Mail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, m)
	return nil
}

func (s *recSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *recSender) last(t *testing.T) forms.Mail {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		t.Fatal("nothing was mailed")
	}
	return s.sent[len(s.sent)-1]
}

// formsEnv is a community instance with forms on and a recording mailer.
func formsEnv(t *testing.T, over map[string]string) (*env, *recSender) {
	t.Helper()
	vars := map[string]string{"SITEBIN_FORMS_SMTP_HOST": "smtp.test", "SITEBIN_FORMS_SMTP_FROM": "forms@sitebin.example"}
	for k, v := range over {
		vars[k] = v
	}
	e := newEnv(t, vars)
	rs := &recSender{}
	e.api.forms.send = rs
	return e, rs
}

// mailText returns a built mail's decoded text/plain part.
func mailText(t *testing.T, m forms.Mail) string {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(m.Data))
	if err != nil {
		t.Fatal(err)
	}
	var find func(ct string, r io.Reader) string
	find = func(ct string, r io.Reader) string {
		_, params, _ := mime.ParseMediaType(ct)
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err != nil {
				return ""
			}
			pct := p.Header.Get("Content-Type")
			if strings.HasPrefix(pct, "multipart/") {
				if s := find(pct, p); s != "" {
					return s
				}
				continue
			}
			if strings.HasPrefix(pct, "text/plain") {
				b, _ := io.ReadAll(quotedprintable.NewReader(p))
				return string(b)
			}
		}
	}
	return find(msg.Header.Get("Content-Type"), msg.Body)
}

var confirmTokenRe = regexp.MustCompile(`/forms/confirm\?t=([A-Za-z0-9_.%-]+)`)

// confirmToken pulls the token out of a confirmation mail.
func confirmToken(t *testing.T, m forms.Mail) string {
	t.Helper()
	sm := confirmTokenRe.FindStringSubmatch(mailText(t, m))
	if sm == nil {
		t.Fatalf("no confirmation link in:\n%s", mailText(t, m))
	}
	return sm[1]
}
