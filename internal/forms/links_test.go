package forms

import (
	"strings"
	"testing"
	"time"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func TestConfirmTokenRoundTrip(t *testing.T) {
	l := NewLinks(testSecret)
	now := time.Now()
	c := ConfirmClaim{ViewID: "v1", Key: "k1", Recipient: "odd|name@example.com", Seq: 3}
	got, ok := l.ParseConfirm(l.ConfirmToken(c, now), now.Add(time.Hour))
	if !ok || got != c {
		t.Fatalf("round trip = %+v %v, want %+v", got, ok, c)
	}
	if _, ok := l.ParseConfirm(l.ConfirmToken(c, now), now.Add(ConfirmTTL+time.Minute)); ok {
		t.Error("a confirmation link must expire after 7 days")
	}
}

func TestStopTokenOutlivesYears(t *testing.T) {
	l := NewLinks(testSecret)
	now := time.Now()
	c := StopClaim{ViewID: "v1", Key: "k1", Recipient: "a@example.com"}
	got, ok := l.ParseStop(l.StopToken(c, now), now.Add(10*365*24*time.Hour))
	if !ok || got != c {
		t.Fatalf("a stop link from an old mail must keep working: %+v %v", got, ok)
	}
}

func TestLinkTokensArePurposeBound(t *testing.T) {
	l := NewLinks(testSecret)
	now := time.Now()
	stop := l.StopToken(StopClaim{ViewID: "v1", Key: "k1", Recipient: "a@example.com"}, now)
	if _, ok := l.ParseConfirm(stop, now); ok {
		t.Error("a stop token was accepted as a confirmation")
	}
	conf := l.ConfirmToken(ConfirmClaim{ViewID: "v1", Key: "k1", Recipient: "a@example.com"}, now)
	if _, ok := l.ParseStop(conf, now); ok {
		t.Error("a confirmation token was accepted as a stop")
	}
	if _, ok := l.ParseConfirm(conf[:len(conf)-2]+"xx", now); ok {
		t.Error("a tampered token was accepted")
	}
	other := NewLinks([]byte(strings.Repeat("z", 32)))
	if _, ok := other.ParseConfirm(conf, now); ok {
		t.Error("a token from another instance was accepted")
	}
}
