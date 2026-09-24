package forms

import (
	"context"
	"encoding/base64"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is just enough of an SMTP server to watch a client talk to it.
type fakeSMTP struct {
	offerAuth bool
	rcptReply string // "" = 250
	silent    bool   // accept the connection and never greet
	quitFails bool   // close the connection on QUIT instead of replying 221

	mu             sync.Mutex
	from, to, auth string
	helo           string
	data           string
}

func (f *fakeSMTP) start(t *testing.T) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	if f.silent {
		time.Sleep(3 * time.Second)
		return
	}
	tp := textproto.NewConn(c)
	tp.PrintfLine("220 fake ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		up := strings.ToUpper(line)
		f.mu.Lock()
		switch {
		case strings.HasPrefix(up, "EHLO "):
			f.helo = line[5:]
			if f.offerAuth {
				tp.PrintfLine("250-fake")
				tp.PrintfLine("250 AUTH PLAIN")
			} else {
				tp.PrintfLine("250 fake")
			}
		case strings.HasPrefix(up, "AUTH PLAIN "):
			f.auth = line[len("AUTH PLAIN "):]
			tp.PrintfLine("235 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			f.from = line[len("MAIL FROM:"):]
			tp.PrintfLine("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			f.to = line[len("RCPT TO:"):]
			if f.rcptReply != "" {
				tp.PrintfLine("%s", f.rcptReply)
			} else {
				tp.PrintfLine("250 ok")
			}
		case up == "DATA":
			tp.PrintfLine("354 go ahead")
			f.mu.Unlock()
			b, err := tp.ReadDotBytes()
			f.mu.Lock()
			if err != nil {
				f.mu.Unlock()
				return
			}
			f.data = string(b)
			tp.PrintfLine("250 queued")
		case up == "QUIT":
			if f.quitFails {
				f.mu.Unlock()
				return // close without a reply: the client sees QUIT fail
			}
			tp.PrintfLine("221 bye")
			f.mu.Unlock()
			return
		default:
			tp.PrintfLine("502 unknown")
		}
		f.mu.Unlock()
	}
}

var testMail = Mail{From: "forms@sitebin.example", To: "office@example.com", Data: []byte("Subject: hi\r\n\r\nhello\r\n")}

func TestSMTPSenderDelivers(t *testing.T) {
	f := &fakeSMTP{}
	host, port := f.start(t)
	if err := (&SMTPSender{Host: host, Port: port}).Send(context.Background(), testMail); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.from != "<forms@sitebin.example>" || f.to != "<office@example.com>" {
		t.Errorf("envelope %q -> %q", f.from, f.to)
	}
	if !strings.Contains(f.data, "Subject: hi") || !strings.Contains(f.data, "hello") {
		t.Errorf("data = %q", f.data)
	}
	if f.helo != "sitebin.example" {
		t.Errorf("EHLO %q, want the sender's domain", f.helo)
	}
}

func TestSMTPSenderAuthenticates(t *testing.T) {
	f := &fakeSMTP{offerAuth: true}
	host, port := f.start(t)
	if err := (&SMTPSender{Host: host, Port: port, User: "u", Pass: "p"}).Send(context.Background(), testMail); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	got, _ := base64.StdEncoding.DecodeString(f.auth)
	if string(got) != "\x00u\x00p" {
		t.Errorf("AUTH PLAIN carried %q", got)
	}
}

func TestSMTPSenderRefusesAuthTheServerLacks(t *testing.T) {
	f := &fakeSMTP{}
	host, port := f.start(t)
	err := (&SMTPSender{Host: host, Port: port, User: "u", Pass: "p"}).Send(context.Background(), testMail)
	if err == nil || !strings.Contains(err.Error(), "AUTH") {
		t.Fatalf("err = %v, want a clear AUTH error", err)
	}
}

func TestSMTPSenderReportsARefusedRecipient(t *testing.T) {
	f := &fakeSMTP{rcptReply: "550 no such user"}
	host, port := f.start(t)
	err := (&SMTPSender{Host: host, Port: port}).Send(context.Background(), testMail)
	if err == nil || !strings.Contains(err.Error(), "RCPT") {
		t.Fatalf("err = %v, want a RCPT error", err)
	}
}

// The server has already accepted the message once DATA's closing "."
// succeeded; a QUIT that fails afterwards (here, by dropping the connection
// instead of answering 221) says nothing about that acceptance and must not
// be reported as a failed send. Reporting it as one would be a false 502
// that invites a retry, and since a 502 now releases the form's captcha
// solution, that retry would resend and duplicate a message the server
// already has.
func TestSMTPSenderIgnoresAFailedQuitAfterDataWasAccepted(t *testing.T) {
	f := &fakeSMTP{quitFails: true}
	host, port := f.start(t)
	if err := (&SMTPSender{Host: host, Port: port}).Send(context.Background(), testMail); err != nil {
		t.Fatalf("a failed QUIT after accepted DATA was reported as a failed send: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(f.data, "hello") {
		t.Error("the message was not recorded by the fake server despite Send succeeding")
	}
}

func TestSMTPSenderGivesUpAtTheDeadline(t *testing.T) {
	f := &fakeSMTP{silent: true}
	host, port := f.start(t)
	start := time.Now()
	err := (&SMTPSender{Host: host, Port: port, Timeout: 300 * time.Millisecond}).Send(context.Background(), testMail)
	if err == nil {
		t.Fatal("a server that never answers was reported as a delivery")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("gave up after %v; the deadline was 300ms", d)
	}
}
