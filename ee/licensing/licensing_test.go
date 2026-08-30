//go:build ee

package licensing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

var refTime = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

// chain mints a full four-segment key the way the stack would: a root-signed
// certificate for an app key, and a licence signed by that app key.
type chain struct {
	rootPub  ed25519.PublicKey
	rootPriv ed25519.PrivateKey
	appPub   ed25519.PublicKey
	appPriv  ed25519.PrivateKey
}

func newChain(t *testing.T) *chain {
	t.Helper()
	rpub, rpriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	apub, apriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &chain{rpub, rpriv, apub, apriv}
}

func (c *chain) cert(t *testing.T, appID string, expires time.Time) string {
	t.Helper()
	s, err := SignCert(CertPayload{
		AppID:     appID,
		PubKey:    base64.StdEncoding.EncodeToString(c.appPub),
		IssuedAt:  refTime.Add(-24 * time.Hour),
		ExpiresAt: expires,
	}, c.rootPriv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (c *chain) lic(t *testing.T, appID string, expires, grace time.Time) string {
	t.Helper()
	s, err := SignLicense(LicensePayload{
		AppID:      appID,
		Holder:     "ACME GmbH",
		Plan:       "enterprise",
		IssuedAt:   refTime.Add(-24 * time.Hour),
		ExpiresAt:  expires,
		GraceUntil: grace,
	}, c.appPriv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// key is the happy path: everything matches and nothing has expired.
func (c *chain) key(t *testing.T) string {
	t.Helper()
	return Key(
		c.cert(t, AppID, refTime.Add(365*24*time.Hour)),
		c.lic(t, AppID, refTime.Add(30*24*time.Hour), refTime.Add(120*24*time.Hour)),
	)
}

func (c *chain) roots() []ed25519.PublicKey { return []ed25519.PublicKey{c.rootPub} }

func TestVerifyValidChain(t *testing.T) {
	c := newChain(t)
	lic, err := Verify(c.key(t), c.roots(), AppID, refTime)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if lic.Lic.Holder != "ACME GmbH" || lic.Lic.Plan != "enterprise" {
		t.Errorf("license contents wrong: %+v", lic.Lic)
	}
	if lic.Cert.AppID != AppID {
		t.Errorf("cert app_id = %q", lic.Cert.AppID)
	}
}

// The root list is a LIST: any one of the roots may vouch for the certificate,
// which is what makes rotating a root possible without a rebuild.
func TestVerifyAcceptsAnyTrustedRoot(t *testing.T) {
	c := newChain(t)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	roots := []ed25519.PublicKey{other, c.rootPub}
	if _, err := Verify(c.key(t), roots, AppID, refTime); err != nil {
		t.Fatalf("a key signed by the second trusted root was refused: %v", err)
	}
}

// The whole point of baking the root in: a certificate minted by someone else's
// root must not verify, however well-formed the rest of the chain is.
func TestVerifyRejectsUntrustedRoot(t *testing.T) {
	c := newChain(t)
	stranger, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err := Verify(c.key(t), []ed25519.PublicKey{stranger}, AppID, refTime)
	if !errors.Is(err, ErrCertSig) {
		t.Fatalf("expected ErrCertSig, got %v", err)
	}
}

func TestVerifyRejectsNoRoots(t *testing.T) {
	c := newChain(t)
	if _, err := Verify(c.key(t), nil, AppID, refTime); !errors.Is(err, ErrNoRoots) {
		t.Fatalf("expected ErrNoRoots, got %v", err)
	}
}

// The audience check, both halves.
func TestVerifyRejectsAppIDMismatchWithCert(t *testing.T) {
	c := newChain(t)
	key := Key(
		c.cert(t, AppID, refTime.Add(365*24*time.Hour)),
		c.lic(t, "other-app", refTime.Add(30*24*time.Hour), refTime.Add(120*24*time.Hour)),
	)
	if _, err := Verify(key, c.roots(), AppID, refTime); !errors.Is(err, ErrAppID) {
		t.Fatalf("expected ErrAppID, got %v", err)
	}
}

func TestVerifyRejectsLicenseForAnotherApp(t *testing.T) {
	c := newChain(t)
	// Internally consistent, signed by a trusted root — but minted for a
	// different app on the same stack.
	key := Key(
		c.cert(t, "helpdesk", refTime.Add(365*24*time.Hour)),
		c.lic(t, "helpdesk", refTime.Add(30*24*time.Hour), refTime.Add(120*24*time.Hour)),
	)
	if _, err := Verify(key, c.roots(), AppID, refTime); !errors.Is(err, ErrAppID) {
		t.Fatalf("expected ErrAppID, got %v", err)
	}
}

func TestVerifyRejectsExpiredCert(t *testing.T) {
	c := newChain(t)
	key := Key(
		c.cert(t, AppID, refTime.Add(-time.Hour)),
		c.lic(t, AppID, refTime.Add(30*24*time.Hour), refTime.Add(120*24*time.Hour)),
	)
	if _, err := Verify(key, c.roots(), AppID, refTime); !errors.Is(err, ErrCertExpiry) {
		t.Fatalf("expected ErrCertExpiry, got %v", err)
	}
}

// A licence signed by a key the certificate does not name — the segment-4 check.
func TestVerifyRejectsLicenseSignedByAStrangerKey(t *testing.T) {
	c := newChain(t)
	_, strangerPriv, _ := ed25519.GenerateKey(rand.Reader)
	bad, err := SignLicense(LicensePayload{AppID: AppID, Holder: "x"}, strangerPriv)
	if err != nil {
		t.Fatal(err)
	}
	key := Key(c.cert(t, AppID, refTime.Add(365*24*time.Hour)), bad)
	if _, err := Verify(key, c.roots(), AppID, refTime); !errors.Is(err, ErrLicSig) {
		t.Fatalf("expected ErrLicSig, got %v", err)
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	c := newChain(t)
	full := c.key(t)
	cases := map[string]string{
		"empty":          "",
		"two segments":   strings.Join(strings.Split(full, ".")[:2], "."),
		"five segments":  full + ".extra",
		"not base64":     "a.b.c.d",
		"tampered lic":   mutate(full, 2),
		"tampered cert":  mutate(full, 0),
		"garbage string": "garbage",
	}
	for name, key := range cases {
		if _, err := Verify(key, c.roots(), AppID, refTime); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// mutate flips a character in one segment so the payload no longer matches its
// signature.
func mutate(key string, seg int) string {
	parts := strings.Split(key, ".")
	b := []byte(parts[seg])
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	parts[seg] = string(b)
	return strings.Join(parts, ".")
}

func TestParseRootsAcceptsAListAndBothAlphabets(t *testing.T) {
	a, _, _ := ed25519.GenerateKey(rand.Reader)
	b, _, _ := ed25519.GenerateKey(rand.Reader)
	list := base64.StdEncoding.EncodeToString(a) + " , " + base64.RawURLEncoding.EncodeToString(b)
	got, err := parseRoots(list)
	if err != nil {
		t.Fatalf("parseRoots: %v", err)
	}
	if len(got) != 2 || !got[0].Equal(a) || !got[1].Equal(b) {
		t.Fatalf("parsed %d roots, want the two supplied", len(got))
	}
	if got, err := parseRoots(""); err != nil || len(got) != 0 {
		t.Fatalf("empty list = %v, %v; want no roots and no error", got, err)
	}
	if _, err := parseRoots("not-a-key"); err == nil {
		t.Error("a junk root parsed")
	}
}

// The dev-only override exists so a local build can point at a throwaway root.
func TestTrustedRootsDevOverride(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv(rootsEnv, base64.StdEncoding.EncodeToString(pub))
	roots, err := TrustedRoots()
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || !roots[0].Equal(pub) {
		t.Fatalf("dev override not honoured: %v", roots)
	}
}

// TestIssuerInteropVector verifies a licence produced by the STACK's issuer
// against the root that issued it, so the two implementations of this format
// cannot drift apart. Point SITEBIN_LICENSE_VECTOR at a JSON file holding
// {root_public_key_b64, licence} to run it; without one there is nothing to
// check and the test skips.
func TestIssuerInteropVector(t *testing.T) {
	path := os.Getenv("SITEBIN_LICENSE_VECTOR")
	if path == "" {
		t.Skip("SITEBIN_LICENSE_VECTOR is not set; no issuer vector to check against")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the issuer vector: %v", err)
	}
	var v struct {
		Root    string `json:"root_public_key_b64"`
		Licence string `json:"licence"`
		License string `json:"license"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parsing the issuer vector: %v", err)
	}
	key := v.Licence
	if key == "" {
		key = v.License
	}
	roots, err := parseRoots(v.Root)
	if err != nil {
		t.Fatalf("the vector's root public key does not parse: %v", err)
	}
	lic, err := Verify(key, roots, AppID, time.Now())
	if err != nil {
		t.Fatalf("the issuer's licence did not verify: %v", err)
	}
	t.Logf("issuer vector verified: holder=%q plan=%q expires=%s grace=%s max_custom_domains=%d",
		lic.Lic.Holder, lic.Lic.Plan, lic.Lic.ExpiresAt, lic.Lic.GraceUntil,
		lic.Lic.Entitlements.MaxCustomDomains)
}
