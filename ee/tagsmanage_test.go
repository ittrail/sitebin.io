//go:build ee

package ee

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// manageFixture is a signed-in account with a few sites, on a SiteService
// that keeps each site's provenance log.
type manageFixture struct {
	t      *testing.T
	p      *provider
	host   *provHost
	mux    http.Handler
	acc    *account.Account
	cookie *http.Cookie
	ids    []string // the sites, "Site 1", "Site 2", ... in the order given
}

const (
	mgKunde = "g_kunde"
	mgTyp   = "g_typ"
)

var mgGroups = []account.TagGroup{
	{ID: mgKunde, Name: "Kunde", Color: "blue"},
	{ID: mgTyp, Name: "Typ", Color: "green"},
}

// manageAccount signs an account up and gives it one site per tag list.
func manageAccount(t *testing.T, sites ...[]string) *manageFixture {
	t.Helper()
	p, host, mux := setupProvAccounts(t)
	cookie := signedUpUser(t, mux)
	acc, err := p.accounts.ByEmail("user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	f := &manageFixture{t: t, p: p, host: host, mux: mux, acc: acc, cookie: cookie}
	for i, tags := range sites {
		id := strings.Repeat(string(rune('a'+i)), 26)
		if err := p.accounts.LinkSite(acc, id); err != nil {
			t.Fatal(err)
		}
		clean, err := store.CleanTags(tags)
		if err != nil {
			t.Fatal(err)
		}
		if len(clean) == 0 {
			clean = nil
		}
		host.sites.infos[id] = ext.SiteInfo{
			ViewID: id, Mode: "webserver", Name: fmt.Sprintf("Site %d", i+1), Tags: clean,
			ViewURL: "http://" + id + ".sitebin.example",
		}
		f.ids = append(f.ids, id)
	}
	return f
}

func (f *manageFixture) setRegistry(groups []account.TagGroup, tags map[string]account.TagMeta) {
	f.t.Helper()
	err := f.p.accounts.UpdateTags(f.acc.ID, func(r *account.TagRegistry) error {
		r.Groups = append([]account.TagGroup(nil), groups...)
		r.Tags = map[string]account.TagMeta{}
		for k, m := range tags {
			r.Tags[k] = m
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *manageFixture) registry() account.TagRegistry {
	f.t.Helper()
	r, err := f.p.accounts.Tags(f.acc.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

// tags is site i's tags, joined by commas.
func (f *manageFixture) tags(i int) string {
	return strings.Join(f.host.sites.infos[f.ids[i]].Tags, ",")
}

func (f *manageFixture) allTags() []string {
	out := make([]string, len(f.ids))
	for i := range f.ids {
		out[i] = f.tags(i)
	}
	return out
}

// post posts v with the account's CSRF token.
func (f *manageFixture) post(path string, v url.Values) *httptest.ResponseRecorder {
	v.Set("csrf", f.p.csrf(f.acc))
	return postAs(f.mux, path, f.cookie, v)
}

func (f *manageFixture) tagOp(v url.Values) *httptest.ResponseRecorder {
	return f.post("/account/tags/tag", v)
}

func (f *manageFixture) page() string {
	f.t.Helper()
	w := getAs(f.mux, "/account/tags", f.cookie)
	if w.Code != http.StatusOK {
		f.t.Fatalf("GET /account/tags = %d %s", w.Code, w.Body)
	}
	return w.Body.String()
}

// siteTagLines counts the dashboard's tag lines in a site's log.
func (f *manageFixture) siteTagLines(i int) int {
	logs, _ := f.host.sites.SiteProvenance(f.ids[i])
	n := 0
	for _, e := range logs {
		if e.Action == provenance.ActionTags && e.Surface == provenance.SurfaceDashboard && e.Account == f.acc.ID {
			n++
		}
	}
	return n
}

// accountTagLines counts the account-log lines for an account-wide op.
func (f *manageFixture) accountTagLines(detail string) int {
	n := 0
	for _, e := range accountLog(f.t, f.p, f.acc.ID) {
		if e.Action == provenance.ActionTags && e.Detail == detail && e.Surface == provenance.SurfaceDashboard && e.Auth == provenance.AuthSession {
			n += e.Times()
		}
	}
	return n
}

func wantRedirect(t *testing.T, w *httptest.ResponseRecorder, loc string) {
	t.Helper()
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != loc {
		t.Fatalf("got %d %q (%s), want 303 to %s", w.Code, w.Header().Get("Location"), w.Body, loc)
	}
}

func TestManageTagsRenamesAcrossSites(t *testing.T) {
	f := manageAccount(t, []string{"mueller", "live"}, []string{"mueller"}, []string{"Mueller"}, []string{"live"})
	f.setRegistry(mgGroups, map[string]account.TagMeta{"mueller": {Name: "mueller", Group: mgKunde, Color: "red"}})

	w := f.tagOp(url.Values{"tag": {"mueller"}, "op": {"rename"}, "value": {"  Müller   GmbH "}})
	wantRedirect(t, w, "/account/tags?flash=renamed")
	if got, want := f.allTags(), []string{"live,Müller GmbH", "Müller GmbH", "Müller GmbH", "live"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sites after the rename = %q, want %q", got, want)
	}
	reg := f.registry()
	if _, ok := reg.Tags["mueller"]; ok {
		t.Error("the old registry key is still there")
	}
	if m := reg.Tags["müller gmbh"]; m != (account.TagMeta{Name: "Müller GmbH", Group: mgKunde, Color: "red"}) {
		t.Errorf("the registry entry did not move with its settings: %+v", m)
	}
	// One site line per changed site, none for the untouched one, and one
	// account line for the whole operation.
	for i, want := range []int{1, 1, 1, 0} {
		if got := f.siteTagLines(i); got != want {
			t.Errorf("site %d has %d tag lines, want %d", i+1, got, want)
		}
	}
	if n := f.accountTagLines("rename"); n != 1 {
		t.Errorf("account log has %d rename lines, want 1", n)
	}

	// The same tag in another spelling is a respelling: no confirmation.
	w = f.tagOp(url.Values{"tag": {"müller gmbh"}, "op": {"rename"}, "value": {"MÜLLER GmbH"}})
	wantRedirect(t, w, "/account/tags?flash=renamed")
	if got, want := f.allTags(), []string{"live,MÜLLER GmbH", "MÜLLER GmbH", "MÜLLER GmbH", "live"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sites after the respelling = %q, want %q", got, want)
	}
	if m := f.registry().Tags["müller gmbh"]; m != (account.TagMeta{Name: "MÜLLER GmbH", Group: mgKunde, Color: "red"}) {
		t.Errorf("the registry entry was not respelled: %+v", m)
	}

	// A name the rule refuses changes nothing, and the page says the rule.
	before := f.allTags()
	w = f.tagOp(url.Values{"tag": {"müller gmbh"}, "op": {"rename"}, "value": {"a,b"}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "without commas") {
		t.Errorf("a refused name = %d %s", w.Code, w.Body)
	}
	if !reflect.DeepEqual(f.allTags(), before) {
		t.Error("a refused rename changed a site")
	}
	// A tag the account no longer knows.
	w = f.tagOp(url.Values{"tag": {"mueller"}, "op": {"rename"}, "value": {"x"}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no longer exists") {
		t.Errorf("renaming an unknown tag = %d %s", w.Code, w.Body)
	}
}

func TestManageTagsMergeNeedsConfirm(t *testing.T) {
	f := manageAccount(t, []string{"mueller", "Müller GmbH"}, []string{"mueller"}, []string{"mueller", "live"}, []string{"Müller GmbH"})
	f.setRegistry(nil, map[string]account.TagMeta{"müller gmbh": {Name: "Müller GmbH"}})
	sites, reg := f.allTags(), f.registry()

	w := f.tagOp(url.Values{"tag": {"mueller"}, "op": {"rename"}, "value": {"MÜLLER GMBH"}})
	if w.Code != http.StatusOK {
		t.Fatalf("merge without confirm = %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, s := range []string{"mueller", "Müller GmbH", "3 sites"} {
		if !strings.Contains(body, s) {
			t.Errorf("the confirmation does not name %q", s)
		}
	}
	for _, s := range []string{`action="/account/tags/tag"`, `name="confirm" value="1"`, `name="csrf" value="` + f.p.csrf(f.acc) + `"`, `name="op" value="rename"`} {
		if !strings.Contains(body, s) {
			t.Errorf("the confirmation form lacks %s", s)
		}
	}
	if strings.Contains(body, "<script") {
		t.Error("the confirmation carries a script")
	}
	if !reflect.DeepEqual(f.allTags(), sites) || !reflect.DeepEqual(f.registry(), reg) {
		t.Fatal("asking for confirmation changed something")
	}

	w = f.tagOp(url.Values{"tag": {"mueller"}, "op": {"rename"}, "value": {"MÜLLER GMBH"}, "confirm": {"1"}})
	wantRedirect(t, w, "/account/tags?flash=merged")
	// The existing spelling wins, and a site that had both has it once.
	if got, want := f.allTags(), []string{"Müller GmbH", "Müller GmbH", "live,Müller GmbH", "Müller GmbH"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sites after the merge = %q, want %q", got, want)
	}
	if got := f.registry().Tags; len(got) != 1 || got["müller gmbh"].Name != "Müller GmbH" {
		t.Errorf("registry after the merge = %+v", got)
	}
	if got, want := []int{f.siteTagLines(0), f.siteTagLines(1), f.siteTagLines(2), f.siteTagLines(3)}, []int{1, 1, 1, 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("site tag lines = %v, want %v", got, want)
	}
}

func TestManageTagsMergeExistingWins(t *testing.T) {
	t.Run("gaps are filled from the renamed tag", func(t *testing.T) {
		f := manageAccount(t, []string{"mueller"}, []string{"Müller GmbH"})
		f.setRegistry(mgGroups, map[string]account.TagMeta{
			"mueller":     {Name: "mueller", Group: mgKunde, Color: "red"},
			"müller gmbh": {Name: "Müller GmbH"}, // ungrouped and grey
		})
		w := f.tagOp(url.Values{"tag": {"mueller"}, "op": {"rename"}, "value": {"Müller GmbH"}})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "Kunde") || !strings.Contains(body, "red") {
			t.Errorf("the confirmation does not say the merged tag takes Kunde and red: %d %s", w.Code, body)
		}
		w = f.tagOp(url.Values{"tag": {"mueller"}, "op": {"rename"}, "value": {"Müller GmbH"}, "confirm": {"1"}})
		wantRedirect(t, w, "/account/tags?flash=merged")
		reg := f.registry()
		if _, ok := reg.Tags["mueller"]; ok {
			t.Error("the renamed entry is still there")
		}
		if m := reg.Tags["müller gmbh"]; m != (account.TagMeta{Name: "Müller GmbH", Group: mgKunde, Color: "red"}) {
			t.Errorf("merged entry = %+v, want Müller GmbH in Kunde, red", m)
		}
		if got, want := f.allTags(), []string{"Müller GmbH", "Müller GmbH"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sites = %q", got)
		}
	})
	t.Run("the existing tag keeps its own group and colour", func(t *testing.T) {
		f := manageAccount(t, []string{"alpha"}, []string{"Beta"})
		f.setRegistry(mgGroups, map[string]account.TagMeta{
			"alpha": {Name: "alpha", Group: mgKunde, Color: "red"},
			"beta":  {Name: "Beta", Group: mgTyp, Color: "violet"},
		})
		w := f.tagOp(url.Values{"tag": {"alpha"}, "op": {"rename"}, "value": {"beta"}})
		if body := w.Body.String(); w.Code != http.StatusOK || !strings.Contains(body, "Typ") || !strings.Contains(body, "violet") {
			t.Errorf("the confirmation does not say the merged tag keeps Typ and violet: %d %s", w.Code, body)
		}
		w = f.tagOp(url.Values{"tag": {"alpha"}, "op": {"rename"}, "value": {"beta"}, "confirm": {"1"}})
		wantRedirect(t, w, "/account/tags?flash=merged")
		reg := f.registry()
		if m := reg.Tags["beta"]; m != (account.TagMeta{Name: "Beta", Group: mgTyp, Color: "violet"}) || len(reg.Tags) != 1 {
			t.Errorf("registry = %+v, want only Beta in Typ, violet", reg.Tags)
		}
		if got, want := f.allTags(), []string{"Beta", "Beta"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sites = %q", got)
		}
	})
}

func TestManageTagsDeleteSkipsLockedSite(t *testing.T) {
	f := manageAccount(t, []string{"mockup", "live"}, []string{"mockup"}, []string{"mockup"}, []string{"live"})
	locked := f.host.sites.infos[f.ids[1]]
	locked.Name = "Huber Speisekarte"
	locked.Locked = &ext.SiteLock{At: time.Now(), By: ext.LockByAdmin}
	f.host.sites.infos[f.ids[1]] = locked
	f.setRegistry(mgGroups, map[string]account.TagMeta{
		"mockup": {Name: "mockup", Group: mgTyp},
		"archiv": {Name: "archiv", Group: mgKunde}, // on no site
	})
	sites, reg := f.allTags(), f.registry()

	w := f.tagOp(url.Values{"tag": {"mockup"}, "op": {"delete"}})
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "3 sites") || !strings.Contains(body, `name="confirm" value="1"`) {
		t.Fatalf("delete without confirm = %d %s", w.Code, body)
	}
	if !reflect.DeepEqual(f.allTags(), sites) || !reflect.DeepEqual(f.registry(), reg) {
		t.Fatal("asking for confirmation changed something")
	}

	w = f.tagOp(url.Values{"tag": {"mockup"}, "op": {"delete"}, "confirm": {"1"}})
	body = w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("delete with a locked site = %d %s", w.Code, body)
	}
	if !strings.Contains(body, "Huber Speisekarte") || !strings.Contains(body, "http://"+f.ids[1]+".sitebin.example") {
		t.Errorf("the result does not name the skipped site: %s", body)
	}
	if strings.Contains(body, "Site 1") || strings.Contains(body, "Site 3") {
		t.Error("the result names a site that was changed")
	}
	if got, want := f.allTags(), []string{"live", "mockup", "", "live"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sites after the delete = %q, want %q", got, want)
	}
	if _, ok := f.registry().Tags["mockup"]; ok {
		t.Error("the registry entry outlived the delete")
	}
	if got, want := []int{f.siteTagLines(0), f.siteTagLines(1), f.siteTagLines(2), f.siteTagLines(3)}, []int{1, 0, 1, 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("site tag lines = %v, want %v", got, want)
	}
	if n := f.accountTagLines("delete"); n != 1 {
		t.Errorf("account log has %d delete lines, want 1", n)
	}

	// A tag only the registry knows: 0 sites, and only its entry goes.
	w = f.tagOp(url.Values{"tag": {"archiv"}, "op": {"delete"}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "0 sites") {
		t.Errorf("delete an unused tag without confirm = %d %s", w.Code, w.Body)
	}
	w = f.tagOp(url.Values{"tag": {"archiv"}, "op": {"delete"}, "confirm": {"1"}})
	wantRedirect(t, w, "/account/tags?flash=deleted")
	if _, ok := f.registry().Tags["archiv"]; ok {
		t.Error("the unused tag's entry was not deleted")
	}
	if got, want := f.allTags(), []string{"live", "mockup", "", "live"}; !reflect.DeepEqual(got, want) {
		t.Errorf("deleting an unused tag changed a site: %q", got)
	}
}

func TestManageTagsGroupLifecycle(t *testing.T) {
	f := manageAccount(t, []string{"inh", "ovr"})

	wantRedirect(t, f.post("/account/tags/groups", url.Values{"name": {"  Kunde "}, "color": {"blue"}}), "/account/tags?flash=group-created")
	reg := f.registry()
	if len(reg.Groups) != 1 || reg.Groups[0].Name != "Kunde" || reg.Groups[0].Color != "blue" || !strings.HasPrefix(reg.Groups[0].ID, "g_") {
		t.Fatalf("groups after create = %+v", reg.Groups)
	}
	gid := reg.Groups[0].ID

	// The same name in another case, a name the tag rule refuses, a colour
	// outside the palette: refused, with the reason.
	for _, c := range []struct {
		v    url.Values
		want string
	}{
		{url.Values{"name": {"KUNDE"}, "color": {"green"}}, "already exists"},
		{url.Values{"name": {"a,b"}, "color": {"green"}}, "without commas"},
		{url.Values{"name": {"Farbe"}, "color": {"magenta"}}, "colour"},
	} {
		w := f.post("/account/tags/groups", c.v)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("create %v = %d %s, want 400 with %q", c.v, w.Code, w.Body, c.want)
		}
	}
	if n := len(f.registry().Groups); n != 1 {
		t.Fatalf("a refused create left %d groups", n)
	}

	// Recolour, then rename: each field alone, the other kept.
	wantRedirect(t, f.post("/account/tags/groups/"+gid, url.Values{"color": {"teal"}}), "/account/tags?flash=group-saved")
	wantRedirect(t, f.post("/account/tags/groups/"+gid, url.Values{"name": {"Kunden"}}), "/account/tags?flash=group-saved")
	if g := f.registry().Groups[0]; g != (account.TagGroup{ID: gid, Name: "Kunden", Color: "teal"}) {
		t.Errorf("group after recolour and rename = %+v", g)
	}
	if w := f.post("/account/tags/groups/g_gone", url.Values{"color": {"red"}}); w.Code != http.StatusBadRequest {
		t.Errorf("recolour an unknown group = %d", w.Code)
	}

	// Delete: its tags stay, ungrouped. The one that inherited the colour is
	// grey now; the one with its own keeps it.
	if err := f.p.accounts.UpdateTags(f.acc.ID, func(r *account.TagRegistry) error {
		r.Tags["inh"] = account.TagMeta{Name: "inh", Group: gid}
		r.Tags["ovr"] = account.TagMeta{Name: "ovr", Group: gid, Color: "amber"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if c := f.registry().ColorOf("inh"); c != "teal" {
		t.Fatalf("inh before the delete = %s, want the group's teal", c)
	}
	wantRedirect(t, f.post("/account/tags/groups/"+gid+"/delete", url.Values{"confirm": {"1"}}), "/account/tags?flash=group-deleted")
	reg = f.registry()
	if len(reg.Groups) != 0 {
		t.Errorf("groups after delete = %+v", reg.Groups)
	}
	if m := reg.Tags["inh"]; m != (account.TagMeta{Name: "inh"}) || reg.ColorOf("inh") != "grey" {
		t.Errorf("inh after the group's delete = %+v (%s), want ungrouped and grey", m, reg.ColorOf("inh"))
	}
	if m := reg.Tags["ovr"]; m != (account.TagMeta{Name: "ovr", Color: "amber"}) || reg.ColorOf("ovr") != "amber" {
		t.Errorf("ovr after the group's delete = %+v, want ungrouped and amber", m)
	}
	if got := f.tags(0); got != "inh,ovr" {
		t.Errorf("deleting a group changed a site: %q", got)
	}
	// Deleting it again is done already.
	wantRedirect(t, f.post("/account/tags/groups/"+gid+"/delete", url.Values{"confirm": {"1"}}), "/account/tags?flash=group-deleted")
}

func TestManageTagsColourResetFollowsGroup(t *testing.T) {
	f := manageAccount(t, []string{"demo", "Intern"})
	f.setRegistry(mgGroups, map[string]account.TagMeta{"demo": {Name: "demo", Group: mgKunde, Color: "red"}})
	// row is the tag row whose chip reads name.
	row := func(body, name string) string {
		t.Helper()
		for _, r := range strings.Split(body, `<div class="tmtag">`)[1:] {
			if strings.Contains(r, `">`+name+`</span>`) {
				return r
			}
		}
		t.Fatalf("no row for %s in the page", name)
		return ""
	}
	if r := row(f.page(), "demo"); !strings.Contains(r, "own colour") || !strings.Contains(r, "back to group") {
		t.Errorf("demo's row does not show its own colour and the way back: %s", r)
	}

	wantRedirect(t, f.tagOp(url.Values{"tag": {"demo"}, "op": {"color"}, "value": {""}}), "/account/tags?flash=saved")
	if reg := f.registry(); reg.Tags["demo"].Color != "" || reg.ColorOf("demo") != "blue" {
		t.Errorf("after back to group: %+v resolves %s, want the group's blue", reg.Tags["demo"], reg.ColorOf("demo"))
	}
	if r := row(f.page(), "demo"); !strings.Contains(r, "group colour") || strings.Contains(r, "back to group") {
		t.Errorf("demo's row does not show the group colour: %s", r)
	}

	// The colour follows the group it moves to.
	wantRedirect(t, f.tagOp(url.Values{"tag": {"demo"}, "op": {"group"}, "value": {mgTyp}}), "/account/tags?flash=saved")
	if c := f.registry().ColorOf("demo"); c != "green" {
		t.Errorf("demo in Typ = %s, want green", c)
	}
	wantRedirect(t, f.tagOp(url.Values{"tag": {"demo"}, "op": {"color"}, "value": {"violet"}}), "/account/tags?flash=saved")
	if c := f.registry().ColorOf("demo"); c != "violet" {
		t.Errorf("demo with its own colour = %s, want violet", c)
	}
	if w := f.tagOp(url.Values{"tag": {"demo"}, "op": {"color"}, "value": {"magenta"}}); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown colour = %d", w.Code)
	}
	if w := f.tagOp(url.Values{"tag": {"demo"}, "op": {"group"}, "value": {"g_gone"}}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no longer exists") {
		t.Errorf("an unknown group = %d %s", w.Code, w.Body)
	}
	if m := f.registry().Tags["demo"]; m != (account.TagMeta{Name: "demo", Group: mgTyp, Color: "violet"}) {
		t.Errorf("a refused change changed demo: %+v", m)
	}
	// Ungrouped, the own colour stays.
	wantRedirect(t, f.tagOp(url.Values{"tag": {"demo"}, "op": {"group"}, "value": {""}}), "/account/tags?flash=saved")
	if m := f.registry().Tags["demo"]; m != (account.TagMeta{Name: "demo", Color: "violet"}) {
		t.Errorf("demo ungrouped = %+v", m)
	}

	// A tag only in use gets an entry, in the spelling its sites carry — but
	// only once there is something to keep in it.
	wantRedirect(t, f.tagOp(url.Values{"tag": {"intern"}, "op": {"color"}, "value": {""}}), "/account/tags?flash=saved")
	if m, ok := f.registry().Tags["intern"]; ok {
		t.Errorf("back to group wrote an empty entry for a tag only in use: %+v", m)
	}
	wantRedirect(t, f.tagOp(url.Values{"tag": {"intern"}, "op": {"color"}, "value": {"teal"}}), "/account/tags?flash=saved")
	if m := f.registry().Tags["intern"]; m != (account.TagMeta{Name: "Intern", Color: "teal"}) {
		t.Errorf("intern's new entry = %+v", m)
	}
	if w := f.tagOp(url.Values{"tag": {"nope"}, "op": {"color"}, "value": {"teal"}}); w.Code != http.StatusBadRequest {
		t.Errorf("colour of an unknown tag = %d", w.Code)
	}
	if w := f.tagOp(url.Values{"tag": {"demo"}, "op": {"paint"}}); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown op = %d", w.Code)
	}
	if got := f.tags(0); got != "demo,Intern" {
		t.Errorf("colour and group changes touched a site: %q", got)
	}
}

func TestManageTagsRequiresCSRF(t *testing.T) {
	f := manageAccount(t, []string{"mockup", "live"})
	f.setRegistry(mgGroups, map[string]account.TagMeta{"mockup": {Name: "mockup", Group: mgTyp}})
	sites, reg := f.allTags(), f.registry()

	posts := []struct {
		path string
		v    url.Values
	}{
		{"/account/tags/groups", url.Values{"name": {"Neu"}, "color": {"blue"}}},
		{"/account/tags/groups/" + mgKunde, url.Values{"name": {"X"}, "color": {"red"}}},
		{"/account/tags/groups/" + mgKunde + "/delete", url.Values{"confirm": {"1"}}},
		{"/account/tags/tag", url.Values{"tag": {"mockup"}, "op": {"color"}, "value": {"red"}}},
		{"/account/tags/tag", url.Values{"tag": {"mockup"}, "op": {"group"}, "value": {""}}},
		{"/account/tags/tag", url.Values{"tag": {"mockup"}, "op": {"rename"}, "value": {"Entwurf"}, "confirm": {"1"}}},
		{"/account/tags/tag", url.Values{"tag": {"mockup"}, "op": {"delete"}, "confirm": {"1"}}},
	}
	for _, c := range posts {
		for _, token := range []string{"", "forged"} {
			v := url.Values{}
			for k, vs := range c.v {
				v[k] = vs
			}
			if token != "" {
				v.Set("csrf", token)
			}
			if w := postAs(f.mux, c.path, f.cookie, v); w.Code != http.StatusForbidden {
				t.Errorf("%s %v with csrf %q = %d, want 403", c.path, c.v, token, w.Code)
			}
		}
		// Signed out: sent to sign in.
		r := form(c.v)
		r.URL.Path = c.path
		if w := serve(f.mux, r); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/login" {
			t.Errorf("%s signed out = %d %q", c.path, w.Code, w.Header().Get("Location"))
		}
	}
	if !reflect.DeepEqual(f.allTags(), sites) || !reflect.DeepEqual(f.registry(), reg) {
		t.Error("a refused POST changed something")
	}
	if w := getAs(f.mux, "/account/tags", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/login" {
		t.Errorf("GET /account/tags signed out = %d %q", w.Code, w.Header().Get("Location"))
	}
}

// interleaveSites lands an API write on one site between the rename's read of
// the account's sites and its own write: the first time the rename asks to
// edit that site, "api" is added to it first.
type interleaveSites struct {
	*provSites
	target string
	done   bool
}

func (s *interleaveSites) UpdateTags(id string, edit func([]string) ([]string, error)) error {
	if id == s.target && !s.done {
		s.done = true
		if err := s.provSites.UpdateTags(id, func(cur []string) ([]string, error) { return append(cur, "api"), nil }); err != nil {
			return err
		}
	}
	return s.provSites.UpdateTags(id, edit)
}

type interleaveHost struct {
	*provHost
	sites ext.SiteService
}

func (h *interleaveHost) Sites() ext.SiteService { return h.sites }

// Review Focus 5: the rename decides on the list under the site lock, so an
// API write that lands between its read and its write is kept.
func TestManageTagsRenameKeepsConcurrentAPIWrite(t *testing.T) {
	f := manageAccount(t, []string{"mueller", "live"}, []string{"mueller"}, []string{"mueller"})
	wrapped := &interleaveSites{provSites: f.host.sites, target: f.ids[1]}
	f.p.host = &interleaveHost{provHost: f.host, sites: wrapped}

	wantRedirect(t, f.tagOp(url.Values{"tag": {"mueller"}, "op": {"rename"}, "value": {"Müller GmbH"}}), "/account/tags?flash=renamed")
	if !wrapped.done {
		t.Fatal("the rename never edited the interleaved site")
	}
	if got := f.tags(1); got != "api,Müller GmbH" {
		t.Errorf("the interleaved site = %q, want the API's tag and the new name", got)
	}
	if got, want := []string{f.tags(0), f.tags(2)}, []string{"live,Müller GmbH", "Müller GmbH"}; !reflect.DeepEqual(got, want) {
		t.Errorf("other sites = %q, want %q", got, want)
	}
}

// The page itself: groups alphabetically with their tags, then No group;
// every form a POST with the CSRF token; colours only as palette classes;
// no script, no style attribute; names escaped.
func TestManageTagsPageListsGroupsAndTags(t *testing.T) {
	f := manageAccount(t,
		[]string{"Müller GmbH", "demo"},
		[]string{"Müller GmbH", "privat"},
		[]string{"<b>x</b>"},
	)
	f.setRegistry([]account.TagGroup{
		{ID: mgTyp, Name: "Typ", Color: "green"},
		{ID: "g_leer", Name: "Leer", Color: "pink"},
		{ID: mgKunde, Name: "Kunde", Color: "blue"},
	}, map[string]account.TagMeta{
		"müller gmbh": {Name: "Müller GmbH", Group: mgKunde},
		"archiv":      {Name: "archiv", Group: mgKunde}, // on no site
		"demo":        {Name: "demo", Group: mgTyp, Color: "red"},
		"privat":      {Name: "privat", Color: "amber"},
	})
	w := getAs(f.mux, "/account/tags", f.cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("page = %d", w.Code)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src "+copyScriptCSP+";") {
		t.Errorf("the page's CSP changed: %s", csp)
	}
	body := w.Body.String()
	main := body[strings.Index(body, "<main"):]

	if strings.Contains(body, "<script") {
		t.Error("the page carries a script")
	}
	if strings.Contains(main, "style=") {
		t.Error("the page uses a style attribute")
	}
	for _, m := range regexp.MustCompile(`tag-([a-z]+)`).FindAllStringSubmatch(main, -1) {
		if paletteID(m[1]) != m[1] {
			t.Errorf("a colour class outside the palette: %s", m[0])
		}
	}
	// Every form posts, with the token.
	forms := regexp.MustCompile(`(?s)<form([^>]*)>(.*?)</form>`).FindAllStringSubmatch(main, -1)
	if len(forms) < 10 {
		t.Fatalf("only %d forms on the page", len(forms))
	}
	token := `<input type="hidden" name="csrf" value="` + f.p.csrf(f.acc) + `">`
	for _, m := range forms {
		if !strings.Contains(m[1], `method="post"`) || !strings.Contains(m[2], token) {
			t.Errorf("a form without POST or token: <form%s>%s", m[1], m[2])
		}
	}
	// Groups alphabetically, then No group.
	k, l, ty, ng := strings.Index(main, `class="tmglbl">Kunde<`), strings.Index(main, `class="tmglbl">Leer<`), strings.Index(main, `class="tmglbl">Typ<`), strings.Index(main, `class="tmglbl none">No group<`)
	if k < 0 || l < 0 || ty < 0 || ng < 0 || !(k < l && l < ty && ty < ng) {
		t.Errorf("blocks out of order: Kunde %d, Leer %d, Typ %d, No group %d", k, l, ty, ng)
	}
	// Each group's palette is 8 radio dots with its colour checked; so is
	// the new-group form's.
	if n := strings.Count(main, `type="radio" name="color"`); n != 8*4 {
		t.Errorf("%d group colour radios, want 32", n)
	}
	if !regexp.MustCompile(`action="/account/tags/groups/` + mgKunde + `">(?s:.*?)value="blue" checked`).MatchString(main) {
		t.Error("Kunde's palette does not check blue")
	}
	// Tags: counts, colour state, the 0-site entry, the in-use-only one.
	for _, s := range []string{"2 sites", "1 site", "0 sites", "group colour", "own colour", "back to group", "+ New group", `href="/account"`} {
		if !strings.Contains(main, s) {
			t.Errorf("the page lacks %q", s)
		}
	}
	if n := strings.Count(main, `type="radio" name="value"`); n != 8*5 {
		t.Errorf("%d tag colour radios, want 40 (5 tags)", n)
	}
	if !strings.Contains(main, `<option value="`+mgKunde+`" selected>Kunde</option>`) {
		t.Error("Müller's group select does not select Kunde")
	}
	// Escaped, as text and as a form value.
	if strings.Contains(body, "<b>x</b>") || !strings.Contains(body, "&lt;b&gt;x&lt;/b&gt;") {
		t.Error("the tag <b>x</b> is not escaped")
	}
	// The flash is a fixed text chosen by code; anything else shows nothing.
	if b := getAs(f.mux, "/account/tags?flash=renamed", f.cookie).Body.String(); !strings.Contains(b, `class="flash"`) {
		t.Error("the renamed flash is not shown")
	}
	if b := getAs(f.mux, "/account/tags?flash=%3Ci%3Ehi", f.cookie).Body.String(); strings.Contains(b, `class="flash"`) || strings.Contains(b, "<i>hi") {
		t.Error("an unknown flash code reached the page")
	}
}

// Only a rule's refusal reaches the page, as a 400 in the rule's words;
// anything else is the instance's trouble: a logged 500 that shows none of
// its text, which may carry paths. The manage page and the dashboard's add
// form decide alike.
func TestTagRefusalsShowOnlyRuleErrors(t *testing.T) {
	f := manageAccount(t)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	answer := map[string]func(err error) *httptest.ResponseRecorder{
		"manage page": func(err error) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			f.p.registryFailed(w, f.acc, "Nothing was saved", err)
			return w
		},
		"add form": func(err error) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			f.p.tagAddedWithoutGroup(w, httptest.NewRequest(http.MethodPost, "/account/sites/x/tags/add", nil), f.acc, "neu", err)
			return w
		},
	}
	known := []error{
		account.ErrTooManyGroups, account.ErrTooManyRegistryTags, account.ErrGroupNameTaken,
		account.ErrBadColor, account.ErrBadGroupID, account.ErrUnknownGroup, store.ErrBadTag,
		errNoSuchGroup, errNoSuchTag,
	}
	for where, do := range answer {
		for _, rule := range known {
			w := do(fmt.Errorf("update tags: %w", rule))
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), rule.Error()) {
				t.Errorf("%s, %q = %d %s, want 400 with its text", where, rule, w.Code, w.Body)
			}
			if strings.Contains(w.Body.String(), "update tags") {
				t.Errorf("%s, %q: the wrapping reached the page", where, rule)
			}
		}
		logs.Reset()
		for _, other := range []error{
			errors.New("tags: secret-detail went wrong"),
			fmt.Errorf("list sites: %w", errors.New("readdir C:/data/secret-detail")),
			&fs.PathError{Op: "rename", Path: "/data/accounts/secret-detail", Err: errors.New("denied")},
			account.ErrNotFound,
		} {
			w := do(other)
			if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "secret-detail") || strings.Contains(w.Body.String(), other.Error()) {
				t.Errorf("%s, %q = %d %s, want a 500 without its text", where, other, w.Code, w.Body)
			}
		}
		if n := strings.Count(logs.String(), "level=ERROR"); n != 4 || !strings.Contains(logs.String(), "secret-detail") {
			t.Errorf("%s: %d errors logged, want 4 with their text: %s", where, n, logs.String())
		}
	}
}

// breakRegistryWrites makes every later registry write fail while reads
// still work: the temporary file's name is taken by a directory.
func (f *manageFixture) breakRegistryWrites() {
	f.t.Helper()
	if err := os.Mkdir(filepath.Join(f.host.dir, "accounts", f.acc.ID, "tags.json.tmp"), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

// An account-wide change whose sites were changed and whose registry write
// failed still happened on those sites: the account log says so.
func TestManageTagsRecordsAccountLineWhenRegistryFails(t *testing.T) {
	for _, c := range []struct {
		op    string
		v     url.Values
		after []string
	}{
		{"rename", url.Values{"tag": {"mockup"}, "op": {"rename"}, "value": {"Entwurf"}}, []string{"Entwurf,live", "Entwurf", "live"}},
		{"delete", url.Values{"tag": {"mockup"}, "op": {"delete"}, "confirm": {"1"}}, []string{"live", "", "live"}},
	} {
		t.Run(c.op, func(t *testing.T) {
			f := manageAccount(t, []string{"mockup", "live"}, []string{"mockup"}, []string{"live"})
			f.setRegistry(mgGroups, map[string]account.TagMeta{"mockup": {Name: "mockup", Group: mgTyp}})
			f.breakRegistryWrites()

			w := f.tagOp(c.v)
			if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "Only partly done") {
				t.Fatalf("%s with a failing registry = %d %s", c.op, w.Code, w.Body)
			}
			if got := f.allTags(); !reflect.DeepEqual(got, c.after) {
				t.Errorf("sites = %q, want %q", got, c.after)
			}
			if n := f.accountTagLines(c.op); n != 1 {
				t.Errorf("account log has %d %s lines, want 1", n, c.op)
			}
		})
	}

	// Nothing changed on any site, and the registry write failed: nothing
	// happened, and nothing is recorded.
	f := manageAccount(t, []string{"live"})
	f.setRegistry(mgGroups, map[string]account.TagMeta{"archiv": {Name: "archiv", Group: mgKunde}})
	f.breakRegistryWrites()
	w := f.tagOp(url.Values{"tag": {"archiv"}, "op": {"delete"}, "confirm": {"1"}})
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "sites were updated") {
		t.Errorf("a registry-only delete that failed = %d %s", w.Code, w.Body)
	}
	if n := f.accountTagLines("delete"); n != 0 {
		t.Errorf("account log has %d delete lines for a change that did not happen", n)
	}
}

// Deleting a group asks first, like deleting a tag: the page names the group
// and how many tags it holds, which stay without a group. Only the
// confirmation's own POST deletes.
func TestManageTagsGroupDeleteAsksFirst(t *testing.T) {
	f := manageAccount(t, []string{"Müller GmbH", "live"})
	f.setRegistry(mgGroups, map[string]account.TagMeta{
		"müller gmbh": {Name: "Müller GmbH", Group: mgKunde},
		"archiv":      {Name: "archiv", Group: mgKunde}, // on no site
		"live":        {Name: "live", Group: mgTyp},
	})
	sites, reg := f.allTags(), f.registry()

	w := f.post("/account/tags/groups/"+mgKunde+"/delete", url.Values{})
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("group delete without confirm = %d %s", w.Code, body)
	}
	for _, s := range []string{"Kunde", "2 tags", "without a group"} {
		if !strings.Contains(body, s) {
			t.Errorf("the confirmation does not say %q: %s", s, body)
		}
	}
	for _, s := range []string{`action="/account/tags/groups/` + mgKunde + `/delete"`, `name="confirm" value="1"`, `name="csrf" value="` + f.p.csrf(f.acc) + `"`, `href="/account/tags"`} {
		if !strings.Contains(body, s) {
			t.Errorf("the confirmation form lacks %s", s)
		}
	}
	if strings.Contains(body, `name="op"`) || strings.Contains(body, `name="tag"`) {
		t.Error("the group's confirmation carries a tag form's fields")
	}
	if strings.Contains(body, "<script") {
		t.Error("the confirmation carries a script")
	}
	if !reflect.DeepEqual(f.allTags(), sites) || !reflect.DeepEqual(f.registry(), reg) {
		t.Fatal("asking for confirmation changed something")
	}
	// One tag is "1 tag".
	if b := f.post("/account/tags/groups/"+mgTyp+"/delete", url.Values{}).Body.String(); !strings.Contains(b, "1 tag") || strings.Contains(b, "1 tags") {
		t.Errorf("the confirmation for a group of one tag: %s", b)
	}

	wantRedirect(t, f.post("/account/tags/groups/"+mgKunde+"/delete", url.Values{"confirm": {"1"}}), "/account/tags?flash=group-deleted")
	after := f.registry()
	if len(after.Groups) != 1 || after.Groups[0].ID != mgTyp {
		t.Errorf("groups after the confirmed delete = %+v", after.Groups)
	}
	if m := after.Tags["archiv"]; m != (account.TagMeta{Name: "archiv"}) {
		t.Errorf("archiv after its group's delete = %+v, want kept without a group", m)
	}
	if !reflect.DeepEqual(f.allTags(), sites) {
		t.Error("deleting a group changed a site")
	}
	// A group that is gone already needs no confirmation.
	wantRedirect(t, f.post("/account/tags/groups/"+mgKunde+"/delete", url.Values{}), "/account/tags?flash=group-deleted")
}
