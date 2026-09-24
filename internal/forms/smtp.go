package forms

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Sender delivers a built message. httpapi holds one; its tests swap in a
// recording fake.
type Sender interface {
	Send(ctx context.Context, m Mail) error
}

// SMTPSender delivers over SMTP: implicit TLS when ImplicitTLS is set,
// otherwise STARTTLS whenever the server offers it. net/smtp refuses to send
// credentials over an unencrypted connection to anything but localhost, which
// is the right default and is kept.
//
// Every exchange has one deadline, from dial to QUIT. smtp.SendMail has none,
// and a stuck server would otherwise hold a visitor's request open forever.
type SMTPSender struct {
	Host        string
	Port        int
	User, Pass  string
	ImplicitTLS bool
	Timeout     time.Duration // whole exchange; 0 means 30 seconds
	TLSConfig   *tls.Config   // nil verifies Host; tests supply their own
}

func (s *SMTPSender) Send(ctx context.Context, m Mail) error {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	conn.SetDeadline(deadline)
	tlsCfg := s.TLSConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: s.Host}
	}
	if s.ImplicitTLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fmt.Errorf("smtp tls: %w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer c.Close()
	if err := c.Hello(heloName(m.From)); err != nil {
		return fmt.Errorf("smtp EHLO: %w", err)
	}
	if !s.ImplicitTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("smtp STARTTLS: %w", err)
			}
		}
	}
	if s.User != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("smtp: the server offers no AUTH, but SITEBIN_FORMS_SMTP_USER is set")
		}
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Pass, s.Host)); err != nil {
			return fmt.Errorf("smtp AUTH: %w", err)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(m.Data); err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	return c.Quit()
}

// heloName greets with the sender's own domain; "localhost", net/smtp's
// default, is refused by some servers.
func heloName(from string) string {
	if i := strings.LastIndexByte(from, '@'); i >= 0 && i < len(from)-1 {
		return from[i+1:]
	}
	return "localhost"
}
