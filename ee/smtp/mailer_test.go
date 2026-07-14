//go:build ee

package smtp

import (
	"strings"
	"testing"

	"github.com/ittrail/sitebin/ee/eeconfig"
)

func TestSendBuildsMessage(t *testing.T) {
	var gotTo, gotMsg string
	m := New(eeconfig.SMTPConfig{Host: "smtp.example", Port: 587, From: "no-reply@sitebin.example"})
	m.deliver = func(cfg eeconfig.SMTPConfig, to, msg string) error {
		gotTo, gotMsg = to, msg
		return nil
	}
	if err := m.Send("user@example.com", "Hello", "line one\nline two"); err != nil {
		t.Fatal(err)
	}
	if gotTo != "user@example.com" {
		t.Errorf("to = %q", gotTo)
	}
	for _, want := range []string{
		"From: no-reply@sitebin.example\r\n",
		"To: user@example.com\r\n",
		"Subject: Hello\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"line one\r\nline two",
	} {
		if !strings.Contains(gotMsg, want) {
			t.Errorf("message missing %q:\n%s", want, gotMsg)
		}
	}
}

func TestVerificationAndResetTemplates(t *testing.T) {
	var msg string
	m := New(eeconfig.SMTPConfig{From: "x@y"})
	m.deliver = func(_ eeconfig.SMTPConfig, _ string, msgArg string) error { msg = msgArg; return nil }

	m.SendVerification("u@example.com", "https://sitebin.example/account/verify?token=abc")
	if !strings.Contains(msg, "verify?token=abc") || !strings.Contains(msg, "Subject: Verify") {
		t.Errorf("verification message wrong:\n%s", msg)
	}
	m.SendPasswordReset("u@example.com", "https://sitebin.example/account/reset/confirm?token=xyz")
	if !strings.Contains(msg, "confirm?token=xyz") || !strings.Contains(msg, "Subject: Reset") {
		t.Errorf("reset message wrong:\n%s", msg)
	}
}

func TestSendEmptyRecipient(t *testing.T) {
	m := New(eeconfig.SMTPConfig{From: "x@y"})
	m.deliver = func(eeconfig.SMTPConfig, string, string) error { return nil }
	if err := m.Send("", "s", "b"); err == nil {
		t.Error("empty recipient should error")
	}
}
