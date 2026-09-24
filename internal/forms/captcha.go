package forms

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
)

// ErrCaptcha is every captcha refusal. The person is asked to try again. Why
// it failed is of no use to them and of real use to a bot.
var ErrCaptcha = errors.New("the captcha was not solved")

const (
	captchaTTL       = 5 * time.Minute
	captchaAlgorithm = "PBKDF2/SHA-256"
	// Deterministic mode: the server picks the counter and signs the key it
	// derives there, so the work is predictable and verifying is one HMAC.
	// At cost 1000 one derivation measured about 0.1 ms natively, so a
	// counter of 1000–1999 is about 0.2 s on one core, spread by the widget
	// over up to four workers. The rollout checks it on a real phone.
	captchaCost        = 1000
	captchaCounterMin  = 1000
	captchaCounterSpan = 1000
)

// Captcha issues and verifies ALTCHA v2 challenges, each bound to one site's
// form.
type Captcha struct {
	sigSecret, keySecret          string
	cost, counterMin, counterSpan int
	now                           func() time.Time

	mu   sync.Mutex
	used map[string]time.Time // spent challenge signature → when it expires
}

// NewCaptcha derives the captcha's two HMAC keys from the instance secret
// under their own labels; the raw secret is never used directly.
func NewCaptcha(instanceSecret []byte) *Captcha {
	return &Captcha{
		sigSecret:   deriveKey(instanceSecret, "forms:captcha:challenge"),
		keySecret:   deriveKey(instanceSecret, "forms:captcha:key"),
		cost:        captchaCost,
		counterMin:  captchaCounterMin,
		counterSpan: captchaCounterSpan,
		now:         time.Now,
		used:        map[string]time.Time{},
	}
}

func deriveKey(secret []byte, label string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(label))
	return hex.EncodeToString(m.Sum(nil))
}

// Challenge returns a fresh challenge for viewID's form formKey. Its data is
// ASCII on purpose: the widget encodes the payload with btoa, which mangles
// anything else.
func (c *Captcha) Challenge(viewID, formKey string) (any, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(c.counterSpan)))
	if err != nil {
		return nil, err
	}
	counter := c.counterMin + int(n.Int64())
	exp := c.now().Add(captchaTTL)
	ch, err := altcha.CreateChallenge(altcha.CreateChallengeOptions{
		Algorithm:              captchaAlgorithm,
		Cost:                   c.cost,
		Counter:                &counter,
		DeriveKey:              altcha.DeriveKeyPBKDF2(),
		ExpiresAt:              &exp,
		Data:                   map[string]interface{}{"site": viewID, "form": formKey},
		HMACSignatureSecret:    c.sigSecret,
		HMACKeySignatureSecret: c.keySecret,
	})
	return ch, err
}

// Verify checks the widget's altcha field for viewID's form formKey. A
// verified challenge is spent: the library does not track replays, so this
// does.
func (c *Captcha) Verify(field, viewID, formKey string) error {
	raw, err := base64.StdEncoding.DecodeString(field)
	if field == "" || err != nil {
		return ErrCaptcha
	}
	var p altcha.Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return ErrCaptcha
	}
	// DeriveKey is passed on every call: without it the library accepts on
	// the signature alone and never checks the work.
	res, err := altcha.VerifySolution(altcha.VerifySolutionOptions{
		Challenge:              p.Challenge,
		Solution:               p.Solution,
		DeriveKey:              altcha.DeriveKeyPBKDF2(),
		HMACSignatureSecret:    c.sigSecret,
		HMACKeySignatureSecret: c.keySecret,
	})
	if err != nil || !res.Verified {
		return ErrCaptcha
	}
	params := p.Challenge.Parameters
	if params.ExpiresAt == 0 || params.Data["site"] != viewID || params.Data["form"] != formKey {
		return ErrCaptcha
	}
	if !c.spend(p.Challenge.Signature, time.Unix(params.ExpiresAt, 0)) {
		return ErrCaptcha
	}
	return nil
}

// Release undoes exactly the spend a prior Verify recorded for field, so a
// later refusal that has nothing to do with the captcha (an empty form, a
// rate limit, a failed send) does not force the visitor to reload for a
// fresh challenge. It decodes field exactly as Verify does; a field that
// does not decode, or a signature that was never spent, is a no-op. Release
// does no verification of its own — it only ever removes a map entry Verify
// put there, so it can never make an unverified or forged payload
// acceptable.
//
// Caller rule: call Release(field) only after Verify(field, ...) returned
// nil for this exact field in this same request, at most once, and only
// when that verified solution did not end up mailed. Because Release does
// no verification of its own, handing it a field whose solution was never
// actually spent by this request — one that arrived already used, or one
// nothing here just verified — silently un-spends whatever signature it
// happens to decode to, which is someone else's spend, not this caller's.
func (c *Captcha) Release(field string) {
	raw, err := base64.StdEncoding.DecodeString(field)
	if field == "" || err != nil {
		return
	}
	var p altcha.Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.used, p.Challenge.Signature)
}

// spend records a verified challenge until it expires and reports whether it
// was fresh. A restart forgets them, which reopens at most a 5-minute window.
func (c *Captcha) spend(sig string, exp time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, e := range c.used {
		if now.After(e) {
			delete(c.used, k)
		}
	}
	if _, seen := c.used[sig]; seen {
		return false
	}
	// The library accepts a challenge through its expiry second.
	c.used[sig] = exp.Add(time.Second)
	return true
}
