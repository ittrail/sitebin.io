//go:build ee

package ee

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// tagFixture is the mockup's account: groups Kunde (blue) and Typ (green),
// an empty group Leer (pink), an ungrouped amber tag, and eight sites, two of
// them untagged.
type tagFixture struct {
	p      *provider
	host   *fakeHost
	mux    http.Handler
	acc    *account.Account
	cookie *http.Cookie
	ids    []string // the eight sites, in the order of tagFixtureSites
}

const (
	gKunde = "g_kunde"
	gTyp   = "g_typ"
	gLeer  = "g_leer"
)

var tagFixtureSites = []struct {
	name string
	tags []string
}{
	{"Müller Relaunch Entwurf", []string{"Müller GmbH", "mockup"}},
	{"Huber Speisekarte Entwurf", []string{"Bäckerei Huber", "mockup"}},
	{"Müller Shop", []string{"Müller GmbH", "live"}},
	{"Müller Messe", []string{"demo", "Müller GmbH"}},
	{"Huber Website", []string{"Bäckerei Huber", "live", "privat"}},
	{"Stadtwerke Portal", []string{"mockup", "Stadtwerke"}},
	{"Notizen", nil},
	{"Spielwiese", nil},
}

func taggedAccount(t *testing.T) tagFixture {
	t.Helper()
	p, host, mux, acc, cookie := namedAccount(t)
	f := tagFixture{p: p, host: host, mux: mux, acc: acc, cookie: cookie}
	err := p.accounts.UpdateTags(acc.ID, func(r *account.TagRegistry) error {
		// Deliberately not in alphabetical order: the page sorts.
		r.Groups = []account.TagGroup{
			{ID: gTyp, Name: "Typ", Color: "green"},
			{ID: gLeer, Name: "Leer", Color: "pink"},
			{ID: gKunde, Name: "Kunde", Color: "blue"},
		}
		r.Tags = map[string]account.TagMeta{
			"müller gmbh":    {Name: "Müller GmbH", Group: gKunde},
			"bäckerei huber": {Name: "Bäckerei Huber", Group: gKunde},
			"stadtwerke":     {Name: "Stadtwerke", Group: gKunde},
			"live":           {Name: "live", Group: gTyp},
			"mockup":         {Name: "mockup", Group: gTyp},
			"demo":           {Name: "demo", Group: gTyp, Color: "red"},
			"privat":         {Name: "privat", Color: "amber"},
			"archiv":         {Name: "archiv", Group: gLeer}, // on no site
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range tagFixtureSites {
		id := strings.Repeat(string(rune('a'+i)), 26)
		if err := p.accounts.LinkSite(acc, id); err != nil {
			t.Fatal(err)
		}
		tags, err := store.CleanTags(s.tags)
		if err != nil {
			t.Fatal(err)
		}
		if len(tags) == 0 {
			tags = nil
		}
		host.sites.infos[id] = ext.SiteInfo{
			ViewID: id, Mode: "webserver", Name: s.name, Tags: tags,
			ViewURL: "http://" + id + ".sitebin.example", EditURL: "http://sitebin.example/e/" + id,
		}
		f.ids = append(f.ids, id)
	}
	return f
}

func (f tagFixture) post(path string, v url.Values) *http.Response {
	w := postAs(f.mux, path, f.cookie, v)
	return w.Result()
}

func (f tagFixture) tags(id string) []string { return f.host.sites.infos[id].Tags }

// siteChipColour finds the colour class of the chip naming tag in a site row.
func siteChipColour(t *testing.T, body, tag string) string {
	t.Helper()
	re := regexp.MustCompile(`class="tchip tag-([a-z]+)"[^>]*>\s*<span class="tn">` + regexp.QuoteMeta(tag) + `</span>`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no site chip for %q in the page", tag)
	}
	return m[1]
}

func TestDashboardShowsTagChipsWithColours(t *testing.T) {
	f := taggedAccount(t)
	w := getAs(f.mux, "/account", f.cookie)
	if w.Code != 200 {
		t.Fatalf("dashboard = %d", w.Code)
	}
	body := w.Body.String()

	for tag, want := range map[string]string{
		"Müller GmbH": "blue",  // the group's colour
		"live":        "green", // the other group's
		"demo":        "red",   // its own colour overrides the group's
		"privat":      "amber", // ungrouped, with a colour of its own
	} {
		if got := siteChipColour(t, body, tag); got != want {
			t.Errorf("chip %q is %s, want %s", tag, got, want)
		}
	}
	// The palette lives in CSS, as custom properties; data never reaches a
	// style attribute.
	if !strings.Contains(body, "--tag-blue: #5b8cff") || !strings.Contains(body, ".tag-blue") {
		t.Error("the page does not define the blue palette entry")
	}
	if strings.Contains(body, `style="--c`) || strings.Contains(body, "style=\"background:#5b8cff") {
		t.Error("a colour reached a style attribute")
	}
	// The filter block of a group carries its colour dot.
	if !regexp.MustCompile(`class="tfbh tag-blue"><span class="tdot"></span>Kunde`).MatchString(body) {
		t.Error("the Kunde block has no blue dot")
	}
	// A site's chips go by group, then name: Kunde before Typ.
	row := body[strings.Index(body, "Müller Messe"):]
	if i, j := strings.Index(row, ">Müller GmbH<"), strings.Index(row, ">demo<"); i < 0 || j < 0 || i > j {
		t.Error("a site's chips are not ordered by group (Kunde before Typ)")
	}
	if !strings.Contains(body, `href="/account/tags"`) || !strings.Contains(body, "Manage tags") {
		t.Error("the site list does not link the tag manager")
	}
	// A datalist offers the account's tags; the group select its groups.
	if !strings.Contains(body, `<datalist id="tagopts">`) || !strings.Contains(body, `<option value="Bäckerei Huber">`) {
		t.Error("the add form has no datalist of existing tags")
	}
	if !strings.Contains(body, `<option value="`+gKunde+`">Kunde</option>`) || !strings.Contains(body, `<option value="">No group</option>`) {
		t.Error("the add form has no group select")
	}
	if strings.Contains(body, "<script") {
		t.Error("the dashboard carries a script")
	}
}

func TestDashboardFiltersByTags(t *testing.T) {
	f := taggedAccount(t)
	listed := func(body string) []bool {
		out := make([]bool, len(f.ids))
		for i, id := range f.ids {
			out[i] = strings.Contains(body, "http://"+id+".sitebin.example")
		}
		return out
	}
	expect := func(t *testing.T, q string, want ...int) string {
		t.Helper()
		w := getAs(f.mux, "/account"+q, f.cookie)
		if w.Code != 200 {
			t.Fatalf("%s = %d", q, w.Code)
		}
		body := w.Body.String()
		got := listed(body)
		in := map[int]bool{}
		for _, i := range want {
			in[i] = true
		}
		for i := range f.ids {
			if got[i] != in[i] {
				t.Errorf("%s: site %d (%s) listed = %v, want %v", q, i, tagFixtureSites[i].name, got[i], in[i])
			}
		}
		return body
	}

	body := expect(t, "?t=mockup", 0, 1, 5)
	if !strings.Contains(body, "3 of 8 · mockup") {
		t.Error("no summary line for ?t=mockup")
	}
	if !strings.Contains(body, `<span class="count">8</span>`) {
		t.Error("the heading should count every site, not the filtered ones")
	}
	// The selected chip is marked and links to the selection without it.
	if !regexp.MustCompile(`<a class="tfchip tag-green on" href="/account"><span class="tdot"></span>mockup <small>3</small></a>`).MatchString(body) {
		t.Error("the selected mockup chip is not marked or does not clear itself")
	}
	// Counts are totals over the account, not over the result.
	if !strings.Contains(body, "Müller GmbH <small>3</small>") || !strings.Contains(body, "All <small>8</small>") || !strings.Contains(body, "Untagged <small>2</small>") {
		t.Error("the chips do not count over the whole account")
	}
	// A chip adds itself to the selection.
	if !strings.Contains(body, `href="/account?t=live&amp;t=mockup"`) {
		t.Error("the live chip does not link to mockup and live")
	}
	if !regexp.MustCompile(`class="tfsum">[^<]*<a href="/account">`).MatchString(body) {
		t.Error("the summary has no link that clears the filter")
	}
	// The forms carry the filter, so an add or remove comes back to it.
	if !strings.Contains(body, `name="back" value="?t=mockup"`) {
		t.Error("the forms do not carry the current filter")
	}

	body = expect(t, "?"+url.Values{"t": {"müller gmbh", "bäckerei huber", "mockup"}}.Encode(), 0, 1)
	if !strings.Contains(body, "2 of 8 · (Bäckerei Huber or Müller GmbH) and mockup") {
		t.Error("no summary for (Müller or Huber) and mockup")
	}
	if !strings.Contains(body, `class="tfblock part"`) {
		t.Error("a block with a selection is not marked")
	}

	body = expect(t, "?untagged=1", 6, 7)
	if !strings.Contains(body, "2 of 8 · untagged") || !strings.Contains(body, `<a class="tfchip meta on" href="/account">`) {
		t.Error("untagged is not shown as selected")
	}

	body = expect(t, "?g="+gKunde, 0, 1, 2, 3, 4, 5)
	if !regexp.MustCompile(`<a class="tfall on" href="/account"[^>]*>whole group</a>`).MatchString(body) {
		t.Error("the whole-group toggle is not marked on")
	}
	if !strings.Contains(body, "6 of 8 · Kunde") {
		t.Error("no summary for the whole group")
	}
	// In a whole group every chip shows selected, and clicking one deselects
	// it: the group becomes its other tags.
	if !strings.Contains(body, `<a class="tfchip tag-blue on" href="/account?t=b%C3%A4ckerei&#43;huber&amp;t=stadtwerke"><span class="tdot"></span>Müller GmbH`) {
		t.Error("a chip of a whole group does not deselect itself")
	}

	// No filter: everything, no summary, All marked.
	body = expect(t, "", 0, 1, 2, 3, 4, 5, 6, 7)
	if strings.Contains(body, `class="tfsum"`) || !strings.Contains(body, `<a class="tfchip on" href="/account">All`) {
		t.Error("the unfiltered page has a summary or does not mark All")
	}
	// Blocks: alphabetical, then No group; a group with no tag in use hidden.
	k, ty, ng := strings.Index(body, ">Kunde<a"), strings.Index(body, ">Typ<a"), strings.Index(body, ">No group</div>")
	if k < 0 || ty < 0 || ng < 0 || !(k < ty && ty < ng) {
		t.Errorf("blocks out of order: Kunde %d, Typ %d, No group %d", k, ty, ng)
	}
	if strings.Contains(body, `class="tfbh tag-pink"`) {
		t.Error("the Leer group, with no tag in use, has a block")
	}

	// A filter that matches nothing says so, rather than "no sites yet".
	f.host.sites.infos[f.ids[6]] = ext.SiteInfo{ViewID: f.ids[6], Mode: "webserver", Tags: []string{"privat"}}
	body = expect(t, "?"+url.Values{"t": {"privat", "demo"}}.Encode())
	if !strings.Contains(body, "No site matches") || strings.Contains(body, "No sites yet") {
		t.Error("an empty result is not explained")
	}
}

func TestDashboardAddsATag(t *testing.T) {
	f := taggedAccount(t)
	csrf := f.p.csrf(f.acc)
	add := func(id string, v url.Values) *http.Response {
		if v.Get("csrf") == "-" {
			v.Del("csrf")
		} else if !v.Has("csrf") {
			v.Set("csrf", csrf)
		}
		return f.post("/account/sites/"+id+"/tags/add", v)
	}
	reg := func() account.TagRegistry {
		r, err := f.p.accounts.Tags(f.acc.ID)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	// A new tag with a group: on the site, and in the registry with the group.
	res := add(f.ids[0], url.Values{"tag": {"  Konditorei   Maier "}, "group": {gKunde}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/account" {
		t.Fatalf("add = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if got := f.tags(f.ids[0]); strings.Join(got, ",") != "Konditorei Maier,mockup,Müller GmbH" {
		t.Errorf("tags after add = %q", got)
	}
	if m := reg().Tags["konditorei maier"]; m.Name != "Konditorei Maier" || m.Group != gKunde {
		t.Errorf("registry entry = %+v, want Konditorei Maier in Kunde", m)
	}

	// An existing tag, typed in another case, keeps its spelling — and its
	// group: the select is for new tags only.
	if res := add(f.ids[1], url.Values{"tag": {"MÜLLER GMBH"}, "group": {gTyp}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("add existing = %d", res.StatusCode)
	}
	if got := f.tags(f.ids[1]); strings.Join(got, ",") != "Bäckerei Huber,mockup,Müller GmbH" {
		t.Errorf("tags after adding MÜLLER GMBH = %q", got)
	}
	if m := reg().Tags["müller gmbh"]; m.Group != gKunde || m.Name != "Müller GmbH" {
		t.Errorf("an existing tag's registry entry changed: %+v", m)
	}

	// A tag only in use (no registry entry) also lends its spelling.
	f.host.sites.infos[f.ids[6]] = ext.SiteInfo{ViewID: f.ids[6], Mode: "webserver", Tags: []string{"Intern"}}
	add(f.ids[7], url.Values{"tag": {"intern"}, "group": {""}})
	if got := f.tags(f.ids[7]); len(got) != 1 || got[0] != "Intern" {
		t.Errorf("tags after adding intern = %q, want the in-use spelling", got)
	}

	// A new tag without a group gets no registry entry: ungrouped and grey by
	// definition.
	add(f.ids[7], url.Values{"tag": {"Entwurf"}})
	if _, ok := reg().Tags["entwurf"]; ok {
		t.Error("a new tag without a group was written to the registry")
	}
	if got := f.tags(f.ids[7]); strings.Join(got, ",") != "Entwurf,Intern" {
		t.Errorf("tags = %q", got)
	}

	// No CSRF token, or a site the account does not own: refused, unchanged.
	before := strings.Join(f.tags(f.ids[2]), ",")
	if res := add(f.ids[2], url.Values{"csrf": {"-"}, "tag": {"hijack"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("add without csrf = %d, want 403", res.StatusCode)
	}
	const foreign = "zzzzzzzzzzzzzzzzzzzzzzzzzz"
	f.host.sites.site(foreign)
	if res := add(foreign, url.Values{"tag": {"mine"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("add to a foreign site = %d, want 403", res.StatusCode)
	}
	if strings.Join(f.tags(f.ids[2]), ",") != before || len(f.tags(foreign)) != 0 {
		t.Error("a refused add changed a site")
	}

	// The rules, as the rules.
	body := func(r *http.Response) string {
		var b bytes.Buffer
		b.ReadFrom(r.Body)
		return b.String()
	}
	if res := add(f.ids[2], url.Values{"tag": {strings.Repeat("ä", 31)}}); res.StatusCode != http.StatusBadRequest || !strings.Contains(body(res), "30 characters") {
		t.Errorf("31 runes = %d, want 400 with the rule", res.StatusCode)
	}
	ten := make([]string, 10)
	for i := range ten {
		ten[i] = "t" + string(rune('a'+i))
	}
	f.host.sites.infos[f.ids[2]] = ext.SiteInfo{ViewID: f.ids[2], Mode: "webserver", Tags: ten}
	if res := add(f.ids[2], url.Values{"tag": {"eleventh"}}); res.StatusCode != http.StatusBadRequest || !strings.Contains(body(res), "at most 10") {
		t.Errorf("11th tag = %d, want 400 with the rule", res.StatusCode)
	}
	if len(f.tags(f.ids[2])) != 10 {
		t.Error("the 11th tag was stored")
	}

	// back is a query string for /account, never a URL.
	for back, want := range map[string]string{
		"//evil.example":       "/account",
		"https://evil.example": "/account",
		"?/evil.example":       "/account",
		"?t=x\r\nSet-Cookie:a": "/account",
		"?t=mockup":            "/account?t=mockup",
		"":                     "/account",
	} {
		res := add(f.ids[3], url.Values{"tag": {"live"}, "back": {back}})
		if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != want {
			t.Errorf("back=%q: %d %q, want %q", back, res.StatusCode, res.Header.Get("Location"), want)
		}
	}

	// A group that is gone by the time the form is posted: the tag is on the
	// site, and the page says it has no group.
	res = add(f.ids[4], url.Values{"tag": {"Neu"}, "group": {"g_gone"}})
	if b := body(res); res.StatusCode != http.StatusBadRequest || !strings.Contains(b, "without its group") {
		t.Errorf("add with a stale group = %d %s", res.StatusCode, b)
	}
	if got := strings.Join(f.tags(f.ids[4]), ","); !strings.Contains(got, "Neu") {
		t.Errorf("the tag was not added when its group was refused: %q", got)
	}
	if _, ok := reg().Tags["neu"]; ok {
		t.Error("a tag with an unknown group reached the registry")
	}
}

func TestDashboardRemovesATag(t *testing.T) {
	f := taggedAccount(t)
	csrf := f.p.csrf(f.acc)

	res := f.post("/account/sites/"+f.ids[0]+"/tags/remove", url.Values{"csrf": {csrf}, "tag": {"MOCKUP"}, "back": {"?t=mockup"}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/account?t=mockup" {
		t.Fatalf("remove = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if got := f.tags(f.ids[0]); len(got) != 1 || got[0] != "Müller GmbH" {
		t.Errorf("tags after remove = %q", got)
	}
	// The registry keeps the tag: other sites carry it, and an unused entry
	// stays until it is deleted on the manage page.
	if r, _ := f.p.accounts.Tags(f.acc.ID); r.Tags["mockup"].Group != gTyp {
		t.Error("removing a tag from one site changed the registry")
	}

	// Removing the last tag leaves the site untagged.
	f.post("/account/sites/"+f.ids[0]+"/tags/remove", url.Values{"csrf": {csrf}, "tag": {"Müller GmbH"}})
	if got := f.tags(f.ids[0]); len(got) != 0 {
		t.Errorf("tags after removing the last = %q", got)
	}

	if res := f.post("/account/sites/"+f.ids[1]+"/tags/remove", url.Values{"tag": {"mockup"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("remove without csrf = %d, want 403", res.StatusCode)
	}
	const foreign = "zzzzzzzzzzzzzzzzzzzzzzzzzz"
	f.host.sites.infos[foreign] = ext.SiteInfo{ViewID: foreign, Tags: []string{"mockup"}}
	if res := f.post("/account/sites/"+foreign+"/tags/remove", url.Values{"csrf": {csrf}, "tag": {"mockup"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("remove from a foreign site = %d, want 403", res.StatusCode)
	}
	if len(f.tags(f.ids[1])) != 2 || len(f.tags(foreign)) != 1 {
		t.Error("a refused remove changed a site")
	}
}

func TestDashboardEscapesTagNames(t *testing.T) {
	f := taggedAccount(t)
	f.host.sites.infos[f.ids[6]] = ext.SiteInfo{ViewID: f.ids[6], Mode: "webserver", Tags: []string{"<b>x</b>", `"a"`}}
	body := getAs(f.mux, "/account", f.cookie).Body.String()
	if !strings.Contains(body, "&lt;b&gt;x&lt;/b&gt;") {
		t.Error("the tag <b>x</b> is not shown as text")
	}
	if strings.Contains(body, "<b>x</b>") {
		t.Error("the tag <b>x</b> reached the page as markup")
	}
	if strings.Contains(body, `value=""a""`) || !strings.Contains(body, `value="&#34;a&#34;"`) {
		t.Error(`the tag "a" is not escaped in its form field`)
	}
	// Its filter link is query-escaped.
	if !strings.Contains(body, "t=%3Cb%3Ex%3C%2Fb%3E") {
		t.Error("the filter link of <b>x</b> is not query-escaped")
	}
}

// Review Focus 4: a corrupt registry must not take the dashboard down.
func TestDashboardSurvivesUnreadableRegistry(t *testing.T) {
	f := taggedAccount(t)
	if err := os.WriteFile(filepath.Join(f.host.dir, "accounts", f.acc.ID, "tags.json"), []byte("garbage{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	w := getAs(f.mux, "/account", f.cookie)
	if w.Code != 200 {
		t.Fatalf("dashboard with a corrupt registry = %d", w.Code)
	}
	body := w.Body.String()
	for _, tag := range []string{"Müller GmbH", "demo", "privat"} {
		if got := siteChipColour(t, body, tag); got != "grey" {
			t.Errorf("chip %q is %s, want grey with no registry", tag, got)
		}
	}
	if strings.Contains(body, ">Kunde<") {
		t.Error("a group from the unreadable registry is shown")
	}
	if !strings.Contains(logs.String(), f.acc.ID) || !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("the unreadable registry was not logged: %s", logs.String())
	}

	// Adding a tag without a group still works.
	res := f.post("/account/sites/"+f.ids[6]+"/tags/add", url.Values{"csrf": {f.p.csrf(f.acc)}, "tag": {"neu"}})
	if res.StatusCode != http.StatusSeeOther || len(f.tags(f.ids[6])) != 1 {
		t.Errorf("add with a corrupt registry = %d, tags %q", res.StatusCode, f.tags(f.ids[6]))
	}
}
