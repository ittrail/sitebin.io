//go:build ee

package ee

import (
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
)

func TestContainersOffByDefault(t *testing.T) {
	p := setupTiers(t)
	if p.Containers() != nil {
		t.Fatal("the runtime is on without SITEBIN_CONTAINERS")
	}
	var _ ext.ContainerProvider = p
}

// The cap is the account's current tier's max_containers, resolved strictly:
// the runtime starts and stops projects on the answer.
func TestContainerCapFollowsTheTier(t *testing.T) {
	t.Setenv("SITEBIN_TIERS", `[{"id":"free","max_sites":1},{"id":"pro","max_sites":50,"max_containers":3}]`)
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatal(err)
	}
	acc, err := p.local.Signup("c@example.com", "password123", "free")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := p.containerCap(acc.ID); err != nil || n != 0 {
		t.Errorf("free: %d, %v", n, err)
	}
	p.accounts.Update(acc, func(a *account.Account) error { a.Tier = "pro"; return nil })
	if n, err := p.containerCap(acc.ID); err != nil || n != 3 {
		t.Errorf("pro: %d, %v", n, err)
	}
	p.accounts.Update(acc, func(a *account.Account) error { a.Tier = "gone"; return nil })
	if _, err := p.containerCap(acc.ID); err == nil {
		t.Error("an account on an unknown tier got an answer")
	}
	if n, err := p.containerCap("nobody"); err != nil || n != 0 {
		t.Errorf("unknown account: %d, %v", n, err)
	}
}
