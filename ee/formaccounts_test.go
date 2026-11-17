//go:build ee

package ee

import (
	"errors"
	"slices"
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
)

func TestFormAccountsVerifiedEmail(t *testing.T) {
	p, host, _ := setupStackInstance(t, "")
	acc, _ := stackUser(t, p, host, "subject-verified", "owner@example.com")
	if got, err := p.VerifiedEmail(acc.ID); err != nil || got != "owner@example.com" {
		t.Errorf("verified account = %q, %v; want its address", got, err)
	}

	unverified, err := p.accounts.CreateOAuth(account.OIDCProv, "subject-unverified", "new@example.com", false, "free")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := p.VerifiedEmail(unverified.ID); err != nil || got != "" {
		t.Errorf("unverified account = %q, %v; want \"\"", got, err)
	}

	if _, err := p.VerifiedEmail("no-such-account"); err == nil {
		t.Error("an unknown account answered without an error")
	}
}

func TestFormAccountsOwnedSiteIDs(t *testing.T) {
	p, host, _ := setupStackInstance(t, "")
	acc, _ := stackUser(t, p, host, "subject-owner", "owner@example.com")
	ids, err := p.OwnedSiteIDs(acc.ID)
	if err != nil || !slices.Contains(ids, "aaaaaaaaaaaaaaaaaaaaaaaaaa") || !slices.Contains(ids, "bbbbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Fatalf("owned sites = %v, %v", ids, err)
	}
	// A site whose owner cannot be read makes the list incomplete: an error.
	host.sites.ownerErrs = map[string]error{"aaaaaaaaaaaaaaaaaaaaaaaaaa": errors.New("meta.json is damaged")}
	if _, err := p.OwnedSiteIDs(acc.ID); err == nil {
		t.Error("an incomplete list came without an error")
	}
	if _, err := p.OwnedSiteIDs("no-such-account"); err == nil {
		t.Error("an unknown account answered without an error")
	}
}
