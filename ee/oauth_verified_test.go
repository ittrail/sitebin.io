//go:build ee

package ee

import (
	"errors"
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/authn"
)

// email_verified from the identity provider used to be ignored: any email an
// IdP asserted claimed the email index, verified or not. With an IdP that does
// not require verification (Keycloak, unless the realm says so) an attacker
// could register victim@corp.example first, and the victim's own signup was
// then refused as "already registered".

func TestUnverifiedOAuthEmailClaimsNothing(t *testing.T) {
	p := setupOAuth(t)
	unverified := authn.Identity{Provider: account.Google, Subject: "attacker", Email: "victim@corp.example", EmailVerified: false}
	acc, err := p.linkOrCreateOAuth(unverified)
	if err != nil {
		t.Fatal(err)
	}
	if acc.EmailVerified {
		t.Error("an unverified IdP email was recorded as verified")
	}
	if acc.Email != "victim@corp.example" {
		t.Errorf("the address itself should still be stored for display: %q", acc.Email)
	}
	if _, err := p.accounts.ByEmail("victim@corp.example"); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("an unverified email is resolvable through the index: %v", err)
	}
	// The real owner can still sign up locally with their own address …
	if _, err := p.local.Signup("victim@corp.example", "password123", ""); err != nil {
		t.Fatalf("the rightful owner was refused: %v", err)
	}
	// … and the attacker's identity still logs into ITS account, nothing else.
	again, err := p.linkOrCreateOAuth(unverified)
	if err != nil || again.ID != acc.ID {
		t.Fatalf("relogin of the unverified identity: %v %q", err, again.ID)
	}
	if again.EmailVerified {
		t.Error("relogin verified the email")
	}
}

func TestVerifiedOAuthEmailStillCollides(t *testing.T) {
	p := setupOAuth(t)
	if _, err := p.local.Signup("owner@corp.example", "password123", ""); err != nil {
		t.Fatal(err)
	}
	verified := authn.Identity{Provider: account.Google, Subject: "someone", Email: "owner@corp.example", EmailVerified: true}
	if _, err := p.linkOrCreateOAuth(verified); err == nil {
		t.Fatal("a verified email that a local account holds must still be reported as a collision, not silently linked")
	}
}
