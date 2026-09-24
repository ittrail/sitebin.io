package httpapi

import (
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

func intp(n int) *int { return &n }

func TestCreateStampsTheFormsCap(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxForms: intp(1)}})
	defer ext.Reset()
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.QuotaForms == nil || *site.Meta.QuotaForms != 1 {
		t.Fatalf("QuotaForms = %v, want 1 from the grant", site.Meta.QuotaForms)
	}
}

func TestSiteServiceApplyQuotaCarriesTheFormsCap(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	if err := (siteService{a: e.api}).ApplyQuota(c.ID, ext.CreateGrant{MaxForms: intp(2)}); err != nil {
		t.Fatal(err)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.QuotaForms == nil || *site.Meta.QuotaForms != 2 {
		t.Fatalf("QuotaForms = %v, want 2 after a tier change", site.Meta.QuotaForms)
	}
}
