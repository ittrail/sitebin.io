package forms

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
)

// fastCaptcha keeps the real protocol but makes the work trivial, so the
// suite solves challenges in milliseconds.
func fastCaptcha(secret string) *Captcha {
	c := NewCaptcha([]byte(secret))
	c.cost, c.counterMin, c.counterSpan = 10, 5, 5
	return c
}

// solve does what the widget does: solve, then encode the form field.
func solve(t *testing.T, ch any) string {
	t.Helper()
	challenge := ch.(altcha.Challenge)
	sol, err := altcha.SolveChallenge(altcha.SolveChallengeOptions{Challenge: challenge, DeriveKey: altcha.DeriveKeyPBKDF2()})
	if err != nil || sol == nil {
		t.Fatalf("solve: %v", err)
	}
	b, _ := json.Marshal(map[string]any{
		"challenge": map[string]any{"parameters": challenge.Parameters, "signature": challenge.Signature},
		"solution":  sol,
	})
	return base64.StdEncoding.EncodeToString(b)
}

const capSecret = "0123456789abcdef0123456789abcdef"

func TestCaptchaAcceptsASolutionOnce(t *testing.T) {
	c := fastCaptcha(capSecret)
	ch, err := c.Challenge("site1", "form1")
	if err != nil {
		t.Fatal(err)
	}
	field := solve(t, ch)
	if err := c.Verify(field, "site1", "form1"); err != nil {
		t.Fatalf("a genuine solution was refused: %v", err)
	}
	if err := c.Verify(field, "site1", "form1"); !errors.Is(err, ErrCaptcha) {
		t.Fatal("a solution was accepted twice: the replay memory is missing")
	}
}

func TestCaptchaIsBoundToSiteAndForm(t *testing.T) {
	c := fastCaptcha(capSecret)
	ch, _ := c.Challenge("site1", "form1")
	field := solve(t, ch)
	if err := c.Verify(field, "site1", "form2"); !errors.Is(err, ErrCaptcha) {
		t.Error("a solution for form1 was accepted on form2")
	}
	if err := c.Verify(field, "site2", "form1"); !errors.Is(err, ErrCaptcha) {
		t.Error("a solution for site1 was accepted on site2")
	}
}

func TestCaptchaRefusesAnExpiredChallenge(t *testing.T) {
	c := fastCaptcha(capSecret)
	c.now = func() time.Time { return time.Now().Add(-10 * time.Minute) }
	ch, _ := c.Challenge("site1", "form1")
	c.now = time.Now
	if err := c.Verify(solve(t, ch), "site1", "form1"); !errors.Is(err, ErrCaptcha) {
		t.Error("a challenge older than 5 minutes was accepted")
	}
}

func TestCaptchaRefusesAnotherInstancesChallenge(t *testing.T) {
	a, b := fastCaptcha(capSecret), fastCaptcha(strings.Repeat("z", 32))
	ch, _ := a.Challenge("site1", "form1")
	if err := b.Verify(solve(t, ch), "site1", "form1"); !errors.Is(err, ErrCaptcha) {
		t.Error("a challenge signed by another instance secret was accepted")
	}
}

func TestCaptchaRefusesJunk(t *testing.T) {
	c := fastCaptcha(capSecret)
	testMode := base64.StdEncoding.EncodeToString([]byte(`{"challenge":null,"solution":null,"test":true}`))
	for _, junk := range []string{"", "%%%not-base64", base64.StdEncoding.EncodeToString([]byte("{}")), testMode} {
		if err := c.Verify(junk, "site1", "form1"); !errors.Is(err, ErrCaptcha) {
			t.Errorf("junk %q was accepted", junk)
		}
	}
	// A right challenge with a wrong solution: the proof of work is checked,
	// not just the signature (the library's nil-DeriveKey trap).
	ch, _ := c.Challenge("site1", "form1")
	var p map[string]any
	raw, _ := base64.StdEncoding.DecodeString(solve(t, ch))
	json.Unmarshal(raw, &p)
	p["solution"].(map[string]any)["derivedKey"] = strings.Repeat("00", 32)
	forged, _ := json.Marshal(p)
	if err := c.Verify(base64.StdEncoding.EncodeToString(forged), "site1", "form1"); !errors.Is(err, ErrCaptcha) {
		t.Error("a forged derived key was accepted")
	}
}

func TestCaptchaChallengeShape(t *testing.T) {
	ch, _ := NewCaptcha([]byte(capSecret)).Challenge("site1", "form1")
	b, _ := json.Marshal(ch)
	var got struct {
		Parameters struct {
			Algorithm string         `json:"algorithm"`
			ExpiresAt int64          `json:"expiresAt"`
			Data      map[string]any `json:"data"`
			Cost      int            `json:"cost"`
		} `json:"parameters"`
		Signature string `json:"signature"`
	}
	json.Unmarshal(b, &got)
	if got.Parameters.Algorithm != "PBKDF2/SHA-256" || got.Parameters.ExpiresAt == 0 || got.Signature == "" ||
		got.Parameters.Data["form"] != "form1" || got.Parameters.Data["site"] != "site1" || got.Parameters.Cost != captchaCost {
		t.Errorf("challenge JSON = %s", b)
	}
}
