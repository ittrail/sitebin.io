//go:build ee

// Command mintlicense issues a Sitebin Enterprise licence offline, for the E2E
// harness and for local development.
//
//	go run -tags ee ./e2e/mintlicense -expires 720h -grace 2160h -max-domains 2
//
// It does what the SaaS Stack does when it issues a licence — generate (or
// reuse) a ROOT keypair, mint an app signing keypair, have the root certify it,
// then sign the licence with the app key — so `e2e/license.ps1` is
// self-contained and needs no stack running. It is a build tool, never part of
// a shipped image: the real root's private half lives on the stack and never
// leaves it.
//
// It prints three lines, which is what makes it scriptable:
//
//	ROOT_PUBLIC=<std base64>    the value to bake into a build with
//	                            -ldflags "-X …/ee/licensing.trustedRootsB64=<this>"
//	ROOT_PRIVATE=<std base64>   feed back with -root-private to mint another
//	                            licence under the SAME root
//	LICENSE=<a.b.c.d>           the four-segment key for SITEBIN_LICENSE_KEY
//
// Every date is given as a duration from now and may be NEGATIVE, which is how
// the grace and expired states are produced without waiting a year:
//
//	licensed  -expires 720h   -grace 2880h
//	grace     -expires -24h   -grace 720h
//	expired   -expires -720h  -grace -24h
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ittrail/sitebin.io/ee/licensing"
)

func main() {
	var (
		holder      = flag.String("holder", "Sitebin E2E GmbH", "licence holder name")
		plan        = flag.String("plan", "team", "plan name")
		appID       = flag.String("app-id", licensing.AppID, "app id; anything but \"sitebin\" must be refused by the audience check")
		expiresIn   = flag.Duration("expires", 365*24*time.Hour, "expires_at, as a duration from now (may be negative)")
		graceIn     = flag.Duration("grace", 0, "grace_until, as a duration from now (may be negative); 0 means same as expires")
		certIn      = flag.Duration("cert-expires", 10*365*24*time.Hour, "certificate expiry, as a duration from now (may be negative)")
		maxDomains  = flag.Int("max-domains", 0, "entitlements.max_custom_domains; 0 means unlimited")
		rootPrivB64 = flag.String("root-private", "", "reuse this root private key (std base64) instead of generating one")
	)
	flag.Parse()

	rootPub, rootPriv, err := rootKey(*rootPrivB64)
	if err != nil {
		fail(err)
	}
	// The app's own signing keypair. The stack mints one per registered app and
	// the root certifies it; a fresh one per run is fine here because the
	// certificate travels inside the licence string.
	appPub, appPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fail(err)
	}

	now := time.Now().UTC()
	certSeg, err := licensing.SignCert(licensing.CertPayload{
		AppID:     *appID,
		PubKey:    base64.StdEncoding.EncodeToString(appPub),
		IssuedAt:  now,
		ExpiresAt: now.Add(*certIn),
	}, rootPriv)
	if err != nil {
		fail(err)
	}

	expires := now.Add(*expiresIn)
	grace := expires
	if *graceIn != 0 {
		grace = now.Add(*graceIn)
	}
	licSeg, err := licensing.SignLicense(licensing.LicensePayload{
		AppID:        *appID,
		Holder:       *holder,
		Plan:         *plan,
		IssuedAt:     now,
		ExpiresAt:    expires,
		GraceUntil:   grace,
		Entitlements: licensing.Entitlements{MaxCustomDomains: *maxDomains},
	}, appPriv)
	if err != nil {
		fail(err)
	}

	fmt.Printf("ROOT_PUBLIC=%s\n", base64.StdEncoding.EncodeToString(rootPub))
	fmt.Printf("ROOT_PRIVATE=%s\n", base64.StdEncoding.EncodeToString(rootPriv))
	fmt.Printf("LICENSE=%s\n", licensing.Key(certSeg, licSeg))
}

// rootKey generates a root keypair, or restores one from a base64 private key
// so several licences can be minted under a single baked-in root.
func rootKey(b64 string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	if b64 == "" {
		return ed25519.GenerateKey(rand.Reader)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, nil, fmt.Errorf("-root-private is not base64: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, nil, fmt.Errorf("-root-private is %d bytes, want %d", len(raw), ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(raw)
	return priv.Public().(ed25519.PublicKey), priv, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "mintlicense:", err)
	os.Exit(1)
}
