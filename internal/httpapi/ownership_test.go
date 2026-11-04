package httpapi

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

const noSuchSite = "zzzzzzzzzzzzzzzzzzzzzzzzzz"

// own is the core's site service as the extension sees it: asserted, because
// OwnershipSites is optional.
func (e *env) own(t *testing.T) ext.OwnershipSites {
	t.Helper()
	o, ok := e.api.SiteService().(ext.OwnershipSites)
	if !ok {
		t.Fatal("the core's site service does not implement ext.OwnershipSites")
	}
	return o
}

// siteStatus is the status of GET /api/sites/{edit} with one credential
// header ("Authorization" values are sent as bearers).
func (e *env) siteStatus(t *testing.T, edit, header, value string) int {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/sites/"+edit, nil)
	if header == "Authorization" {
		value = "Bearer " + value
	}
	req.Header.Set(header, value)
	return e.public(t, req).Code
}

func (e *env) siteOf(t *testing.T, viewID string) *store.Site {
	t.Helper()
	site, err := e.st.ByViewID(viewID)
	if err != nil {
		t.Fatal(err)
	}
	return site
}

func provLog(t *testing.T, e *env, site *store.Site) []provenance.Entry {
	t.Helper()
	es, err := e.st.Provenance(site)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestTransferSiteRevokesOldCredentials(t *testing.T) {
	fp := &fakeProvider{enabled: true, owner: "acct-1", bearer: map[string]string{"sbp_old": "acct-1", "sbp_new": "acct-2"}}
	ext.Register(fp)
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	upload, _, err := e.api.uploads.issue(c.ID, edit)
	if err != nil {
		t.Fatal(err)
	}
	// This request caches the old password; the cache is what a transfer
	// must not leave behind.
	if code := e.siteStatus(t, edit, "X-Edit-Password", c.EditPassword); code != 200 {
		t.Fatalf("old password before: %d", code)
	}
	if code := e.siteStatus(t, edit, "Authorization", "sbp_old"); code != 200 {
		t.Fatalf("old owner's token before: %d", code)
	}

	g := ext.CreateGrant{MaxSiteBytes: 5000, MaxFiles: 9, MaxExpiryDays: 30}
	by := ext.ChangeBy{Account: "acct-1", IP: "203.0.113.7", UserAgent: "test-agent"}
	pw, err := e.own(t).TransferSite(c.ID, "acct-1", "acct-2", g, by)
	if err != nil || pw == "" || pw == c.EditPassword {
		t.Fatalf("TransferSite = %q, %v", pw, err)
	}

	if _, ok := e.api.uploads.begin(upload, edit); ok {
		t.Error("an upload token issued before the transfer still opens the site")
	}
	if code := e.siteStatus(t, edit, "X-Edit-Password", c.EditPassword); code != 401 {
		t.Errorf("old password after = %d, want 401 (it was cached before)", code)
	}
	if code := e.siteStatus(t, edit, "X-Edit-Password", pw); code != 200 {
		t.Errorf("new password after = %d, want 200", code)
	}
	if code := e.siteStatus(t, edit, "Authorization", "sbp_old"); code != 401 {
		t.Errorf("old owner's account token after = %d, want 401", code)
	}
	if code := e.siteStatus(t, edit, "Authorization", "sbp_new"); code != 200 {
		t.Errorf("new owner's account token = %d, want 200", code)
	}

	site := e.siteOf(t, c.ID)
	if site.Meta.OwnerAccountID != "acct-2" || site.Meta.QuotaBytes != 5000 || site.Meta.QuotaFiles != 9 {
		t.Errorf("owner %q, caps %d/%d: not restamped for the recipient", site.Meta.OwnerAccountID, site.Meta.QuotaBytes, site.Meta.QuotaFiles)
	}
	var named bool
	for _, en := range provLog(t, e, site) {
		if en.Action == provenance.ActionTransfer {
			named = strings.Contains(en.Detail, "acct-1") && strings.Contains(en.Detail, "acct-2") &&
				en.Account == "acct-1" && en.IP == "203.0.113.7" && en.UA == "test-agent"
		}
	}
	if !named {
		t.Errorf("no transfer line naming both accounts: %+v", provLog(t, e, site))
	}

	// Refusals map onto the seam's sentinels.
	if _, err := e.own(t).TransferSite(c.ID, "acct-1", "acct-3", g, by); !errors.Is(err, ext.ErrNotOwner) {
		t.Errorf("the old owner transferring again: %v", err)
	}
	if _, err := e.own(t).TransferSite(c.ID, "acct-2", "", g, by); !errors.Is(err, ext.ErrNotOwner) {
		t.Errorf("a transfer to nobody: %v", err)
	}
	if _, err := e.own(t).TransferSite(noSuchSite, "acct-1", "acct-2", g, by); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("an unknown site: %v", err)
	}
}

func TestAdoptSiteStampsLikeCreate(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	two, one := 2, 1
	by := ext.ChangeBy{Account: "acct-9", IP: "203.0.113.8", UserAgent: "ua"}
	g := ext.CreateGrant{MaxSiteBytes: 1000, MaxFiles: 5, MaxExpiryDays: 7, MaxCustomDomain: &two, MaxForms: &one, Trusted: true}
	if err := e.own(t).AdoptSite(c.ID, "acct-9", g, by); err != nil {
		t.Fatal(err)
	}
	site := e.siteOf(t, c.ID)
	m := site.Meta
	if m.OwnerAccountID != "acct-9" || m.QuotaBytes != 1000 || m.QuotaFiles != 5 || m.QuotaExpiryDays != 7 || m.QuotaDomains == nil || *m.QuotaDomains != 2 || m.QuotaForms == nil || *m.QuotaForms != 1 {
		t.Errorf("not stamped: %+v", m)
	}
	if m.ExpiresAt == nil || !m.ExpiryFromTier || time.Until(*m.ExpiresAt) < 6*24*time.Hour {
		t.Errorf("the lifetime is not the plan's: %v from tier %v", m.ExpiresAt, m.ExpiryFromTier)
	}
	if !e.st.Trusted(site) {
		t.Error("the trust marker was not applied")
	}
	var adopted bool
	for _, en := range provLog(t, e, site) {
		adopted = adopted || (en.Action == provenance.ActionAdopt && strings.Contains(en.Detail, "acct-9") && en.IP == "203.0.113.8")
	}
	if !adopted {
		t.Error("no adopt line in the site's log")
	}
	if err := e.own(t).AdoptSite(c.ID, "acct-8", g, by); !errors.Is(err, ext.ErrNotAdoptable) {
		t.Errorf("adopting an owned site: %v", err)
	}
	anon := e.createSite(t, nil, map[string]string{"index.html": "x"})
	if err := e.own(t).AdoptSite(anon.ID, "", g, by); !errors.Is(err, ext.ErrNotAdoptable) {
		t.Errorf("adopting for nobody: %v", err)
	}
	if err := e.own(t).AdoptSite(noSuchSite, "acct-9", g, by); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("an unknown site: %v", err)
	}
}

func TestCheckEditPasswordRateLimited(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	o := e.own(t)
	if ok, wait := o.CheckEditPassword(c.ID, "wrong", "203.0.113.2"); ok || wait != 0 {
		t.Fatalf("a wrong password: ok=%v wait=%v", ok, wait)
	}
	var wait time.Duration
	for i := 0; i < 100 && wait == 0; i++ {
		_, wait = o.CheckEditPassword(c.ID, "wrong"+string(rune('a'+i%26)), "203.0.113.3")
	}
	if wait <= 0 {
		t.Fatal("guessing from one address was never throttled")
	}
	if ok, w := o.CheckEditPassword(c.ID, c.EditPassword, "203.0.113.3"); ok || w <= 0 {
		t.Errorf("a throttled address got through with the right password: ok=%v wait=%v", ok, w)
	}
	// The right password was never cached, so the limit applies to it too.
	if ok, _ := o.CheckEditPassword(c.ID, c.EditPassword, "203.0.113.1"); !ok {
		t.Fatal("the right password was refused from a fresh address")
	}
	if ok, _ := o.CheckEditPassword(noSuchSite, "x", "203.0.113.4"); ok {
		t.Error("an unknown site verified")
	}
	if ok, _ := o.CheckEditPassword(c.ID, "sbu_whatever", "203.0.113.5"); ok {
		t.Error("an upload-token shape verified as a password")
	}
}

func TestResolveSiteForms(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	o := e.own(t)
	for name, in := range map[string]string{
		"view url": e.cfg.ViewURL(c.ID), "edit url": e.cfg.EditURL(edit),
		"view id": c.ID, "edit id": edit, "upper view id": strings.ToUpper(c.ID),
		"padded": "  " + c.ID + " ",
	} {
		if id, ok := o.ResolveSite(in); !ok || id != c.ID {
			t.Errorf("%s %q = %q, %v", name, in, id, ok)
		}
	}
	for _, junk := range []string{"", "nope", "https://example.org/x", e.cfg.EditURL(noSuchSite), "../../etc", "a b c"} {
		if id, ok := o.ResolveSite(junk); ok {
			t.Errorf("junk %q resolved to %q", junk, id)
		}
	}
}

func TestWillNotMove(t *testing.T) {
	e := newEnv(t, nil)
	e.st.SetZoneCheck(func(string) (int, error) { return 1, nil })
	e.st.SetOperatorZones([]string{"ops.example"})
	e.st.SetOperatorCheck(func(id string) bool { return id == "acct" })
	if _, ok, err := e.api.SiteService().ClaimZone("acct", "kunde.example"); err != nil || !ok {
		t.Fatalf("ClaimZone: %v %v", ok, err)
	}
	site, _, err := e.st.Create()
	if err != nil {
		t.Fatal(err)
	}
	e.st.Update(site, func(m *store.Meta) error { m.OwnerAccountID = "acct"; return nil })
	for _, d := range []string{"shop.kunde.example", "x.ops.example", "self.example", "via.example", "shop.kunde.example/blog"} {
		if err := e.st.AddDomain(site, d); err != nil {
			t.Fatalf("AddDomain %s: %v", d, err)
		}
	}
	// Mark one address as held through its host, as vouchFor would have.
	err = e.st.Update(site, func(m *store.Meta) error {
		for i := range m.DomainClaims {
			if m.DomainClaims[i].Domain == "via.example" {
				m.DomainClaims[i].ViaHost = true
				return nil
			}
		}
		now := time.Now()
		m.DomainClaims = append(m.DomainClaims, store.DomainClaim{Domain: "via.example", ViaHost: true, VerifiedAt: &now})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := e.own(t).WillNotMove(site.ViewID, "acct")
	want := map[string]bool{"shop.kunde.example": true, "x.ops.example": true, "via.example": true, "shop.kunde.example/blog": true}
	if len(got) != len(want) {
		t.Fatalf("WillNotMove = %v, want %v", got, want)
	}
	for _, d := range got {
		if !want[d] {
			t.Errorf("%q listed but a self-proven address moves", d)
		}
	}
	for _, d := range e.own(t).WillNotMove(site.ViewID, "someone-else") {
		if d == "shop.kunde.example" {
			t.Error("a zone of another account was listed")
		}
	}
	if got := e.own(t).WillNotMove(noSuchSite, "acct"); len(got) != 0 {
		t.Errorf("an unknown site: %v", got)
	}
}

// TransferSite decides what stays with the sender itself, under the site
// lock, by the rules WillNotMove shows: the sender's zone names go, with their
// redirects; an address that proves itself moves. A zone it cannot read
// refuses the transfer with nothing written.
func TestTransferSiteDetachesWhatStaysWithTheSender(t *testing.T) {
	e := newEnv(t, nil)
	e.st.SetZoneCheck(func(string) (int, error) { return 1, nil })
	if _, ok, err := e.api.SiteService().ClaimZone("acct", "kunde.example"); err != nil || !ok {
		t.Fatalf("ClaimZone: %v %v", ok, err)
	}
	newSite := func(domains ...string) *store.Site {
		site, _, err := e.st.Create()
		if err != nil {
			t.Fatal(err)
		}
		e.st.Update(site, func(m *store.Meta) error { m.OwnerAccountID = "acct"; return nil })
		for _, d := range domains {
			if err := e.st.AddDomain(site, d); err != nil {
				t.Fatalf("AddDomain %s: %v", d, err)
			}
		}
		return site
	}
	site := newSite("shop.kunde.example", "self.example")
	if err := e.st.SetDomainRedirect(site, "self.example", "shop.kunde.example", 301); err != nil {
		t.Fatal(err)
	}
	if _, err := e.own(t).TransferSite(site.ViewID, "acct", "acct-2", ext.CreateGrant{}, ext.ChangeBy{Account: "acct-2"}); err != nil {
		t.Fatal(err)
	}
	m := e.siteOf(t, site.ViewID).Meta
	if strings.Join(m.CustomDomains, ",") != "self.example" || len(m.DomainRedirects) != 0 {
		t.Errorf("after the transfer: domains %v, redirects %v; want only self.example, no redirect", m.CustomDomains, m.DomainRedirects)
	}
	if _, err := e.st.ByDomain("shop.kunde.example"); err == nil {
		t.Error("the zone name is still indexed")
	}

	other := newSite("blog.kunde.example", "other.example")
	if err := os.WriteFile(filepath.Join(e.st.Root(), "zones", "kunde.example.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.own(t).TransferSite(other.ViewID, "acct", "acct-2", ext.CreateGrant{}, ext.ChangeBy{Account: "acct-2"}); err == nil {
		t.Fatal("a transfer went ahead with an unreadable zone")
	}
	if m := e.siteOf(t, other.ViewID).Meta; m.OwnerAccountID != "acct" || len(m.CustomDomains) != 2 {
		t.Errorf("a refused transfer wrote: owner %q, domains %v", m.OwnerAccountID, m.CustomDomains)
	}
}

func TestSiteLockedReadsMetaOnly(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	o := e.own(t)
	if locked, err := o.SiteLocked(c.ID); err != nil || locked {
		t.Fatalf("SiteLocked of a fresh site = %v, %v", locked, err)
	}
	if _, err := e.st.SetLock(e.siteOf(t, c.ID), &store.SiteLock{At: time.Now(), By: "operator"}); err != nil {
		t.Fatal(err)
	}
	if locked, err := o.SiteLocked(c.ID); err != nil || !locked {
		t.Errorf("SiteLocked of a locked site = %v, %v", locked, err)
	}
	if _, err := o.SiteLocked(noSuchSite); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("SiteLocked of a missing site = %v, want ErrSiteGone", err)
	}
}

func TestHostConflicts(t *testing.T) {
	e := newEnv(t, nil)
	mk := func(owner string) *store.Site {
		s, _, err := e.st.Create()
		if err != nil {
			t.Fatal(err)
		}
		e.st.Update(s, func(m *store.Meta) error { m.OwnerAccountID = owner; return nil })
		return s
	}
	a, b, c := mk("acct"), mk("acct"), mk("acct")
	for site, addr := range map[*store.Site]string{a: "m.example/a", b: "m.example/b", c: "other.example"} {
		if err := e.st.AddDomain(site, addr); err != nil {
			t.Fatalf("AddDomain %s: %v", addr, err)
		}
	}
	o := e.own(t)
	got := o.HostConflicts([]string{a.ViewID}, "acct")
	if len(got) != 1 || len(got["m.example"]) != 1 || got["m.example"][0] != b.ViewID {
		t.Fatalf("HostConflicts = %v, want m.example -> [%s]", got, b.ViewID)
	}
	if got := o.HostConflicts([]string{a.ViewID, b.ViewID}, "acct"); len(got) != 0 {
		t.Errorf("the whole host moves together, yet: %v", got)
	}
	if got := o.HostConflicts([]string{c.ViewID}, "acct"); len(got) != 0 {
		t.Errorf("a host of its own: %v", got)
	}
	if got := o.HostConflicts([]string{a.ViewID}, "someone-else"); len(got) != 0 {
		t.Errorf("another sender's sites counted: %v", got)
	}
}

func TestSiteBytesIsUsage(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": strings.Repeat("f", 100)})
	site := e.siteOf(t, c.ID)
	repo := filepath.Join(site.GitDir(), "objects", "pack")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "p.pack"), []byte(strings.Repeat("g", 50)), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := e.st.SiteUsage(site)
	if err != nil {
		t.Fatal(err)
	}
	n, files, err := e.own(t).SiteBytes(c.ID)
	if err != nil || n != u.Bytes() || n != 150 || files != u.Files || files != 1 {
		t.Fatalf("SiteBytes = %d, %d, %v; SiteUsage = %+v (want 150 bytes, 1 file)", n, files, err, u)
	}
	if _, _, err := e.own(t).SiteBytes(noSuchSite); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("an unknown site: %v", err)
	}
}

func TestFormRecipients(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site := e.siteOf(t, c.ID)
	for _, r := range []string{"a@example.com", "b@example.com", "a@example.com", "gone@example.com"} {
		if _, err := e.st.AddForm(site, store.Form{Name: "f", Recipient: r}, 10); err != nil {
			t.Fatal(err)
		}
	}
	e.st.Update(site, func(m *store.Meta) error {
		m.Forms[1].Status = store.FormActive
		m.Forms[3].Status = store.FormStopped
		return nil
	})
	got := e.own(t).FormRecipients(c.ID)
	if len(got) != 2 || got[0] != "a@example.com" || got[1] != "b@example.com" {
		t.Errorf("FormRecipients = %v, want a and b once each, the stopped one left out", got)
	}
	if got := e.own(t).FormRecipients(noSuchSite); len(got) != 0 {
		t.Errorf("an unknown site: %v", got)
	}
}

func TestSetAdminsAndInfo(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	by := ext.ChangeBy{Account: "acct-1", IP: "203.0.113.9"}
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	err := e.own(t).SetAdmins(c.ID, by, func(cur []ext.SiteAdmin) ([]ext.SiteAdmin, error) {
		if len(cur) != 0 {
			t.Errorf("start with %v", cur)
		}
		return []ext.SiteAdmin{{Account: "b", Until: &until}, {Account: "b"}, {Account: "c"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	info, ok := e.api.SiteService().Info(c.ID)
	if !ok || len(info.Admins) != 2 || info.Admins[0].Account != "b" || info.Admins[0].Until == nil || !info.Admins[0].Until.Equal(until) || info.Admins[1].Account != "c" {
		t.Fatalf("Info.Admins = %+v", info.Admins)
	}
	mine := errors.New("mine")
	if err := e.own(t).SetAdmins(c.ID, by, func([]ext.SiteAdmin) ([]ext.SiteAdmin, error) { return nil, mine }); err != mine {
		t.Errorf("edit's own error came back as %v", err)
	}
	many := func([]ext.SiteAdmin) ([]ext.SiteAdmin, error) {
		var out []ext.SiteAdmin
		for i := 0; i < store.MaxAdmins+1; i++ {
			out = append(out, ext.SiteAdmin{Account: "x" + string(rune('a'+i))})
		}
		return out, nil
	}
	if err := e.own(t).SetAdmins(c.ID, by, many); !errors.Is(err, ext.ErrTooManyAdmins) {
		t.Errorf("too many: %v", err)
	}
	if err := e.own(t).SetAdmins(noSuchSite, by, many); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("unknown site: %v", err)
	}
	site := e.siteOf(t, c.ID)
	var logged bool
	for _, en := range provLog(t, e, site) {
		logged = logged || (en.Action == provenance.ActionAdmins && en.IP == "203.0.113.9" && en.Account == "acct-1")
	}
	if !logged {
		t.Error("no admins line in the site's log")
	}
	if _, err := e.st.SetLock(site, &store.SiteLock{At: time.Now(), By: "operator"}); err != nil {
		t.Fatal(err)
	}
	if err := e.own(t).SetAdmins(c.ID, by, func(cur []ext.SiteAdmin) ([]ext.SiteAdmin, error) { return nil, nil }); !errors.Is(err, ext.ErrSiteLocked) {
		t.Errorf("locked: %v", err)
	}
}

// A cached verification is bound to the hash it was checked against: after a
// transfer the old password fails for a fresh reader, even when a request
// that loaded the old site ran after the cache Drop and re-Put it.
func TestVerifyCacheIsBoundToThePasswordHash(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	e.st.Update(e.siteOf(t, c.ID), func(m *store.Meta) error { m.OwnerAccountID = "acct-1"; return nil })
	stale := e.siteOf(t, c.ID) // a request that loaded the meta before the write
	if e.api.verifyEditIP("203.0.113.1", stale, c.EditPassword) != verifyOK {
		t.Fatal("the password did not verify before")
	}
	pw, err := e.own(t).TransferSite(c.ID, "acct-1", "acct-2", ext.CreateGrant{}, ext.ChangeBy{Account: "acct-1"})
	if err != nil {
		t.Fatal(err)
	}
	// Window B: verification against the stale object after the Drop.
	if e.api.verifyEditIP("203.0.113.2", stale, c.EditPassword) != verifyOK {
		t.Fatal("the stale object should still verify its own hash")
	}
	fresh := e.siteOf(t, c.ID)
	if got := e.api.verifyEditIP("203.0.113.3", fresh, c.EditPassword); got == verifyOK {
		t.Fatal("the old password verifies for a fresh reader (stale cache entry)")
	}
	if e.api.verifyEditIP("203.0.113.4", fresh, pw) != verifyOK {
		t.Fatal("the new password was refused")
	}
}

// A transfer the extension's background loop finishes (no request, so no
// address and no user agent) is logged as the background surface with no
// credential of its own, not as a dashboard session.
func TestTransferSiteRecordsTheSurfaceItCameThrough(t *testing.T) {
	e := newEnv(t, nil)
	for _, tc := range []struct {
		by   ext.ChangeBy
		want string
	}{
		{ext.ChangeBy{Account: "acct-2", IP: "203.0.113.9", UserAgent: "ua"}, provenance.SurfaceDashboard + "/" + provenance.AuthSession},
		{ext.ChangeBy{Account: "acct-2", Surface: provenance.SurfaceBackground}, provenance.SurfaceBackground + "/" + provenance.AuthNone},
	} {
		c := e.createSite(t, nil, map[string]string{"index.html": "x"})
		e.st.Update(e.siteOf(t, c.ID), func(m *store.Meta) error { m.OwnerAccountID = "acct-1"; return nil })
		if _, err := e.own(t).TransferSite(c.ID, "acct-1", "acct-2", ext.CreateGrant{}, tc.by); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, en := range provLog(t, e, e.siteOf(t, c.ID)) {
			if en.Action == provenance.ActionTransfer {
				got = append(got, en.Surface+"/"+en.Auth+"/"+en.Account+"/"+en.IP)
			}
		}
		if want := tc.want + "/acct-2/" + tc.by.IP; len(got) != 1 || got[0] != want {
			t.Errorf("transfer lines = %v, want [%s]", got, want)
		}
	}
}

func TestSiteOwnerReadsMetaOnly(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	o := e.own(t)
	if got, err := o.SiteOwner(c.ID); err != nil || got != "acct-1" {
		t.Errorf("SiteOwner = %q, %v; want acct-1", got, err)
	}
	if _, err := o.SiteOwner(noSuchSite); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("SiteOwner of a missing site = %v, want ErrSiteGone", err)
	}
}

func TestSiteAdminsReadsMetaOnly(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	o := e.own(t)
	if got, err := o.SiteAdmins(c.ID); err != nil || len(got) != 0 {
		t.Fatalf("SiteAdmins of a fresh site = %v, %v; want none", got, err)
	}
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	by := ext.ChangeBy{Account: "acct-1"}
	err := o.SetAdmins(c.ID, by, func([]ext.SiteAdmin) ([]ext.SiteAdmin, error) {
		return []ext.SiteAdmin{{Account: "b", Until: &until}, {Account: "c"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := o.SiteAdmins(c.ID)
	if err != nil || len(got) != 2 || got[0].Account != "b" || got[0].Until == nil || !got[0].Until.Equal(until) || got[1].Account != "c" || got[1].Until != nil {
		t.Fatalf("SiteAdmins = %+v, %v", got, err)
	}
	if _, err := o.SiteAdmins(noSuchSite); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("SiteAdmins of a missing site = %v, want ErrSiteGone", err)
	}
}

func TestDeleteSiteIfOwner(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	o := e.own(t)

	for _, who := range []string{"acct-2", ""} {
		if err := o.DeleteSiteIfOwner(c.ID, who); !errors.Is(err, ext.ErrNotOwner) {
			t.Errorf("DeleteSiteIfOwner(%q) = %v, want ErrNotOwner", who, err)
		}
	}
	if _, err := e.st.ByViewID(c.ID); err != nil {
		t.Fatalf("a refused delete removed the site: %v", err)
	}

	// The store re-checks under the lock: a stale *Site that still names
	// acct-1 must not delete a site meta.json has given to acct-2.
	stale := e.siteOf(t, c.ID)
	if _, err := e.st.TransferOwner(e.siteOf(t, c.ID), "acct-1", "acct-2", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteIfOwner(stale, "acct-1"); !errors.Is(err, store.ErrNotOwner) {
		t.Errorf("store.DeleteIfOwner on a stale handle = %v, want ErrNotOwner", err)
	}
	if _, err := e.st.ByViewID(c.ID); err != nil {
		t.Fatalf("the site is gone after a refused delete: %v", err)
	}

	// A locked site is refused.
	if err := e.api.SiteService().SetLock(c.ID, &ext.SiteLock{At: time.Now(), By: ext.LockByAdmin, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := o.DeleteSiteIfOwner(c.ID, "acct-2"); !errors.Is(err, ext.ErrSiteLocked) {
		t.Errorf("locked site = %v, want ErrSiteLocked", err)
	}
	if _, err := e.st.ByViewID(c.ID); err != nil {
		t.Errorf("a locked site was deleted: %v", err)
	}
	if _, err := e.api.SiteService().ReleaseLock(c.ID, ext.LockByAdmin); err != nil {
		t.Fatal(err)
	}
	if err := o.DeleteSiteIfOwner(c.ID, "acct-2"); err != nil {
		t.Errorf("owner delete = %v", err)
	}
	if err := o.DeleteSiteIfOwner(c.ID, "acct-2"); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("second delete = %v, want ErrSiteGone", err)
	}
}

// A refused DeleteSiteIfOwner returns before any teardown: the site's upload
// tokens, cached edit verification and containers are as they were.
func TestDeleteSiteIfOwnerRefusalLeavesTheLiveStateAlone(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	upload, _, err := e.api.uploads.issue(c.ID, edit)
	if err != nil {
		t.Fatal(err)
	}
	if code := e.siteStatus(t, edit, "X-Edit-Password", c.EditPassword); code != 200 {
		t.Fatalf("password before: %d", code)
	}
	if err := e.own(t).DeleteSiteIfOwner(c.ID, "acct-2"); !errors.Is(err, ext.ErrNotOwner) {
		t.Fatalf("DeleteSiteIfOwner = %v, want ErrNotOwner", err)
	}
	if _, ok := e.api.uploads.begin(upload, edit); !ok {
		t.Error("a refused delete revoked the site's upload token")
	}
	if code := e.siteStatus(t, edit, "X-Edit-Password", c.EditPassword); code != 200 {
		t.Errorf("password after a refused delete: %d", code)
	}
}
