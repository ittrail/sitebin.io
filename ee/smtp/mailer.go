//go:build ee

package smtp

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/ittrail/sitebin/ee/eeconfig"
)

// Mailer sends transactional email. The low-level delivery function is
// injectable for testing.
type Mailer struct {
	cfg eeconfig.SMTPConfig
	// deliver sends a fully-formed message; overridden in tests.
	deliver func(cfg eeconfig.SMTPConfig, to, msg string) error
}

// New builds a Mailer for the given SMTP config.
func New(cfg eeconfig.SMTPConfig) *Mailer {
	return &Mailer{cfg: cfg, deliver: deliverSMTP}
}

// Send delivers a plain-text email.
func (m *Mailer) Send(to, subject, body string) error {
	if to == "" {
		return fmt.Errorf("empty recipient")
	}
	msg := buildMessage(m.cfg.From, to, subject, body)
	return m.deliver(m.cfg, to, msg)
}

// SendVerification emails an account-verification link.
func (m *Mailer) SendVerification(to, link string) error {
	return m.Send(to, "Verify your Sitebin account",
		"Welcome to Sitebin.\n\nConfirm your email address by opening this link:\n\n"+link+
			"\n\nIf you did not create an account, you can ignore this email.")
}

// SendPasswordReset emails a password-reset link.
func (m *Mailer) SendPasswordReset(to, link string) error {
	return m.Send(to, "Reset your Sitebin password",
		"We received a request to reset your Sitebin password.\n\nOpen this link to choose a new one (valid for 1 hour):\n\n"+link+
			"\n\nIf you did not request this, you can safely ignore this email.")
}

func buildMessage(from, to, subject, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return b.String()
}

// deliverSMTP sends via STARTTLS (default) or implicit TLS (cfg.TLS, port 465).
func deliverSMTP(cfg eeconfig.SMTPConfig, to, msg string) error {
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	var authMech smtp.Auth
	if cfg.User != "" {
		authMech = smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)
	}
	if !cfg.TLS {
		// STARTTLS path (net/smtp negotiates TLS if the server advertises it).
		return smtp.SendMail(addr, authMech, cfg.From, []string{to}, []byte(msg))
	}
	// Implicit TLS (SMTPS).
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, &tls.Config{ServerName: cfg.Host})
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()
	if authMech != nil {
		if err := c.Auth(authMech); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write([]byte(msg)); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}
