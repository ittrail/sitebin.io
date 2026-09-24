package forms

import (
	"strconv"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/auth"
)

// ConfirmTTL is how long a confirmation link works.
const ConfirmTTL = 7 * 24 * time.Hour

// stopTTL is effectively forever: a stop link in a months-old mail must still
// work. The signer has no "never expires" mode and does not need one.
const stopTTL = 100 * 365 * 24 * time.Hour

// Links mints and reads the recipient's two links. Each has its own signer
// purpose, so one can never be presented as the other.
type Links struct {
	confirm, stop auth.TokenSigner
}

func NewLinks(secret []byte) Links {
	return Links{
		confirm: auth.TokenSigner{Secret: secret, Purpose: "forms:confirm"},
		stop:    auth.TokenSigner{Secret: secret, Purpose: "forms:stop"},
	}
}

// ConfirmClaim is what a confirmation link vouches for. Seq ties it to the
// form's state when it was minted (see store.Form.Seq).
type ConfirmClaim struct {
	ViewID    string
	Key       string
	Recipient string
	Seq       int
}

// StopClaim is what a stop link vouches for. It carries no seq on purpose.
type StopClaim struct {
	ViewID    string
	Key       string
	Recipient string
}

// The recipient goes last in every subject: view ids, keys and numbers never
// contain '|', but an address's local part may.

func (l Links) ConfirmToken(c ConfirmClaim, now time.Time) string {
	return l.confirm.Sign(c.ViewID+"|"+c.Key+"|"+strconv.Itoa(c.Seq)+"|"+c.Recipient, now, ConfirmTTL)
}

func (l Links) ParseConfirm(tok string, now time.Time) (ConfirmClaim, bool) {
	subj, ok := l.confirm.Parse(tok, now)
	if !ok {
		return ConfirmClaim{}, false
	}
	p := strings.SplitN(subj, "|", 4)
	if len(p) != 4 {
		return ConfirmClaim{}, false
	}
	seq, err := strconv.Atoi(p[2])
	if err != nil {
		return ConfirmClaim{}, false
	}
	return ConfirmClaim{ViewID: p[0], Key: p[1], Seq: seq, Recipient: p[3]}, true
}

func (l Links) StopToken(c StopClaim, now time.Time) string {
	return l.stop.Sign(c.ViewID+"|"+c.Key+"|"+c.Recipient, now, stopTTL)
}

func (l Links) ParseStop(tok string, now time.Time) (StopClaim, bool) {
	subj, ok := l.stop.Parse(tok, now)
	if !ok {
		return StopClaim{}, false
	}
	p := strings.SplitN(subj, "|", 3)
	if len(p) != 3 {
		return StopClaim{}, false
	}
	return StopClaim{ViewID: p[0], Key: p[1], Recipient: p[2]}, true
}
