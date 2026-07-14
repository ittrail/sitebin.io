//go:build ee

package licensing

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestSignAndVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_700_000_000, 0)
	lic := License{Holder: "ACME Corp", Plan: "enterprise", IssuedAt: now, ExpiresAt: now.Add(365 * 24 * time.Hour)}

	key, err := Sign(lic, priv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(key, pub, now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Holder != "ACME Corp" || got.Plan != "enterprise" {
		t.Errorf("license contents wrong: %+v", got)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := Sign(License{Holder: "x"}, priv)
	if _, err := Verify(key, otherPub, time.Now()); err != ErrSignature {
		t.Fatalf("expected ErrSignature, got %v", err)
	}
}

func TestVerifyRejectsTampered(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := Sign(License{Holder: "x", Plan: "free"}, priv)
	if _, err := Verify(key+"x", pub, time.Now()); err == nil {
		t.Fatal("tampered key accepted")
	}
	if _, err := Verify("garbage", pub, time.Now()); err != ErrMalformed {
		t.Fatalf("garbage = %v", err)
	}
}

func TestVerifyExpired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_700_000_000, 0)
	key, _ := Sign(License{Holder: "x", ExpiresAt: now.Add(-time.Hour)}, priv)
	if _, err := Verify(key, pub, now); err != ErrExpired {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
}

func TestPerpetualLicense(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := Sign(License{Holder: "x"}, priv) // zero ExpiresAt = perpetual
	if _, err := Verify(key, pub, time.Now()); err != nil {
		t.Fatalf("perpetual license rejected: %v", err)
	}
}

func TestEmbeddedVendorKeyValid(t *testing.T) {
	if _, err := VendorKey(); err != nil {
		t.Fatalf("embedded vendor key invalid: %v", err)
	}
}
