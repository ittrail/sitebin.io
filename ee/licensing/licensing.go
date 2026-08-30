//go:build ee

package licensing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// AppID is Sitebin's own app id on the stack. A licence minted for another app
// on the same stack must not work here — see Verify's step 5, which is the same
// reasoning as the MCP audience check.
const AppID = "sitebin"

// trustedRootsB64 is the comma-separated list of Ed25519 ROOT public keys
// (standard base64) this build trusts. The root is generated once at the
// stack's first bootstrap and its private half never leaves the stack.
//
// It is baked in at BUILD time, deliberately, and never fetched: a public key's
// worth is not secrecy but knowing it is the right one. Set it in the build
// pipeline with
//
//	-ldflags "-X github.com/ittrail/sitebin.io/ee/licensing.trustedRootsB64=<b64>[,<b64>…]"
//
// It is a LIST rather than a constant so a lost or compromised root can be
// rotated by honouring two roots for a while, instead of rebuilding and
// redistributing every binary. A list cannot be introduced later without the
// very rebuild it exists to avoid.
//
// An empty value means this build trusts nothing: every key is unverifiable and
// the instance falls back to the unlicensed (trial) state. It never refuses to
// start.
var trustedRootsB64 = ""

// rootsEnv is a DEVELOPMENT-ONLY override for the baked-in roots. It exists so
// a developer can point a local build at a throwaway root without relinking.
//
// It is NOT a supported production configuration and must never be documented
// as one: anything that can set this environment variable can also mint itself
// a perpetual licence, which is exactly the substitution the baked-in anchor
// exists to prevent. Circumventing the licence key check is in any case
// prohibited by ee/LICENSE (Elastic License 2.0).
const rootsEnv = "SITEBIN_LICENSE_ROOTS_DEV"

// CertPayload binds an app id to the signing public key the stack minted for
// that app. Signed by the ROOT.
type CertPayload struct {
	AppID     string    `json:"app_id"`
	PubKey    string    `json:"pubkey"` // base64 ed25519 public key
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"` // zero = no expiry
}

// LicensePayload is the licence proper. Signed by the APP key named in the
// certificate.
type LicensePayload struct {
	AppID     string    `json:"app_id"`
	Holder    string    `json:"holder"`
	Plan      string    `json:"plan"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"` // zero = perpetual
	// GraceUntil is ExpiresAt plus the grace period the stack configures. Kept
	// separate from ExpiresAt precisely so the instance can tell "valid" from
	// "in grace for ten weeks" and warn instead of surprising the operator.
	GraceUntil time.Time `json:"grace_until"`
	// Entitlements are the limits the PLAN carries, because a plan that names
	// itself and nothing else is honour-based: the customer self-hosts, so the
	// caps otherwise live in their own tiers.json and a Team licence holder
	// edits one number into Platform.
	Entitlements Entitlements `json:"entitlements"`
}

// Entitlements are the limits a licence imposes on the instance. It is an
// OBJECT rather than flat fields so a later limit can be added without
// reissuing every licence already in the field.
//
// Zero (or absent) means UNLIMITED throughout, which is what keeps "unknown is
// never restrictive" true for a licence issued before a limit existed.
type Entitlements struct {
	// MaxCustomDomains is the INSTANCE-WIDE total of custom domains this
	// deployment may serve — not a per-site number. The per-site tier
	// allowance still applies on top; the licence is a ceiling, never a grant.
	MaxCustomDomains int `json:"max_custom_domains,omitempty"`
}

// License is a fully verified chain: the certificate that was signed by a
// trusted root, and the licence that certificate's key signed.
type License struct {
	Cert CertPayload
	Lic  LicensePayload
}

// GraceEnd is when service actually stops mattering: grace_until when the
// issuer set one, else expires_at.
func (l License) GraceEnd() time.Time {
	if l.Lic.GraceUntil.IsZero() {
		return l.Lic.ExpiresAt
	}
	if l.Lic.GraceUntil.Before(l.Lic.ExpiresAt) {
		// A grace that ends before the subscription does is a stack bug; never
		// let it pull an expiry forward.
		return l.Lic.ExpiresAt
	}
	return l.Lic.GraceUntil
}

var (
	ErrMalformed  = errors.New("malformed license key")
	ErrCertSig    = errors.New("license certificate is not signed by a trusted root")
	ErrCertExpiry = errors.New("license certificate has expired")
	ErrLicSig     = errors.New("license signature does not match the certificate key")
	ErrAppID      = errors.New("license was issued for a different app")
	ErrNoRoots    = errors.New("this build trusts no license roots")
)

// TrustedRoots returns the roots this build verifies certificates against. An
// empty list is not an error: it means nothing can be verified, which resolves
// to the unlicensed state, never to "expired".
func TrustedRoots() ([]ed25519.PublicKey, error) {
	src := trustedRootsB64
	if dev := strings.TrimSpace(os.Getenv(rootsEnv)); dev != "" {
		// Development only — see rootsEnv.
		src = dev
	}
	return parseRoots(src)
}

func parseRoots(list string) ([]ed25519.PublicKey, error) {
	var out []ed25519.PublicKey
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		b, err := decodeKey(part)
		if err != nil {
			return nil, fmt.Errorf("trusted root %q: %w", truncate(part), err)
		}
		out = append(out, ed25519.PublicKey(b))
	}
	return out, nil
}

// decodeKey accepts standard or url base64, padded or not: the stack's admin
// portal offers the root public key for download and which alphabet a copy of
// it arrives in is not worth an unverifiable instance.
func decodeKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) != ed25519.PublicKeySize {
				return nil, fmt.Errorf("wrong length %d for an ed25519 public key", len(b))
			}
			return b, nil
		}
	}
	return nil, errors.New("not base64")
}

func truncate(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

// Verify checks a four-segment license key and returns the verified chain.
//
// Every step is mandatory and they run in this order:
//
//  1. the key splits into exactly four base64url segments;
//  2. certSig verifies over certPayload under ONE OF roots;
//  3. the certificate is not expired at now;
//  4. licSig verifies over licPayload under certPayload.pubkey;
//  5. licPayload.app_id == certPayload.app_id == appID.
//
// A licence being past its own expires_at is NOT a verification failure: that
// is a state (grace/expired), decided by StatusAt, not a reason to reject the
// key. Only the steps above can reject one, and every rejection resolves to the
// unlicensed state — a configuration mistake must not punish harder than having
// no licence at all.
func Verify(key string, roots []ed25519.PublicKey, appID string, now time.Time) (License, error) {
	if len(roots) == 0 {
		return License{}, ErrNoRoots
	}
	parts := strings.Split(strings.TrimSpace(key), ".")
	if len(parts) != 4 {
		return License{}, fmt.Errorf("%w: got %d segments, want 4", ErrMalformed, len(parts))
	}
	certRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return License{}, fmt.Errorf("%w: certificate payload: %v", ErrMalformed, err)
	}
	certSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(certSig) != ed25519.SignatureSize {
		return License{}, fmt.Errorf("%w: certificate signature", ErrMalformed)
	}
	licRaw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return License{}, fmt.Errorf("%w: license payload: %v", ErrMalformed, err)
	}
	licSig, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(licSig) != ed25519.SignatureSize {
		return License{}, fmt.Errorf("%w: license signature", ErrMalformed)
	}

	// 2. the certificate must be signed by one of the roots we shipped with.
	trusted := false
	for _, root := range roots {
		if ed25519.Verify(root, certRaw, certSig) {
			trusted = true
			break
		}
	}
	if !trusted {
		return License{}, ErrCertSig
	}

	var cert CertPayload
	if err := json.Unmarshal(certRaw, &cert); err != nil {
		return License{}, fmt.Errorf("%w: certificate payload is not JSON: %v", ErrMalformed, err)
	}

	// 3. an expired certificate is as good as no certificate.
	if !cert.ExpiresAt.IsZero() && now.After(cert.ExpiresAt) {
		return License{}, fmt.Errorf("%w on %s", ErrCertExpiry, cert.ExpiresAt.Format(time.RFC3339))
	}

	// 4. the licence must be signed by the key the certificate names.
	appPub, err := decodeKey(cert.PubKey)
	if err != nil {
		return License{}, fmt.Errorf("%w: certificate pubkey: %v", ErrMalformed, err)
	}
	if !ed25519.Verify(ed25519.PublicKey(appPub), licRaw, licSig) {
		return License{}, ErrLicSig
	}

	var lic LicensePayload
	if err := json.Unmarshal(licRaw, &lic); err != nil {
		return License{}, fmt.Errorf("%w: license payload is not JSON: %v", ErrMalformed, err)
	}

	// 5. the audience check. Both halves matter: a licence whose app_id differs
	// from its own certificate's is a licence the certificate does not vouch
	// for, and one for another app on the same stack is not ours to honour.
	if lic.AppID != cert.AppID {
		return License{}, fmt.Errorf("%w: license says %q, certificate says %q", ErrAppID, lic.AppID, cert.AppID)
	}
	if lic.AppID != appID {
		return License{}, fmt.Errorf("%w: license is for %q, this is %q", ErrAppID, lic.AppID, appID)
	}
	return License{Cert: cert, Lic: lic}, nil
}

// ---- issuing (the stack's job; here for tests and offline tooling) ----

// SignCert produces the first two segments of a key: a certificate binding an
// app id to its signing public key, signed by the root private key.
func SignCert(cert CertPayload, rootPriv ed25519.PrivateKey) (string, error) {
	return signSegment(cert, rootPriv)
}

// SignLicense produces the last two segments: the licence, signed by the app
// private key whose public half the certificate carries.
func SignLicense(lic LicensePayload, appPriv ed25519.PrivateKey) (string, error) {
	return signSegment(lic, appPriv)
}

func signSegment(v any, priv ed25519.PrivateKey) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// Key joins a signed certificate and a signed licence into the one opaque
// value that fits in an env var.
func Key(cert, lic string) string { return cert + "." + lic }
