//go:build ee

package ee

import (
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The order a German reader expects: an umlaut sorts with its base letter,
// and case does not split the list.
var readerOrder = []string{"Ärztehaus", "Bäckerei", "mockup", "Müller GmbH", "Zahnarzt"}

func TestCompareTagNamesSortsLikeAReader(t *testing.T) {
	for _, in := range [][]string{
		{"Zahnarzt", "Müller GmbH", "mockup", "Bäckerei", "Ärztehaus"},
		{"mockup", "Ärztehaus", "Zahnarzt", "Bäckerei", "Müller GmbH"},
	} {
		got := slices.Clone(in)
		slices.SortFunc(got, compareTagNames)
		if !reflect.DeepEqual(got, readerOrder) {
			t.Errorf("sorted %q = %q, want %q", in, got, readerOrder)
		}
	}
	if compareTagNames("Müller", "Müller") != 0 {
		t.Error("a name does not compare equal to itself")
	}
	// Names the collation finds equal still have an order: never 0 for two
	// different strings, or a sort would not be stable across requests.
	if a, b := "Müller", "Müller"; compareTagNames(a, b) == 0 || compareTagNames(a, b) != -compareTagNames(b, a) {
		t.Errorf("composed and decomposed Müller compare %d / %d", compareTagNames(a, b), compareTagNames(b, a))
	}
}

// submatches is every first submatch of re in s, in order.
func submatches(s, re string) []string {
	var out []string
	for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	s = s[i+len(from):]
	if j := strings.Index(s, to); j >= 0 {
		return s[:j]
	}
	return s
}

// The pages show tags and groups in the reader's order: the dashboard's
// filter blocks and chips, a site's chips, the datalist and the group
// select, and the manage page's blocks, tags and group select. The sites'
// stored lists keep their byte order: no data changes.
func TestTagPagesSortLikeAReader(t *testing.T) {
	f := manageAccount(t,
		[]string{"Zahnarzt", "Müller GmbH", "mockup", "Bäckerei", "Ärztehaus"},
		[]string{"Zahnklinik", "Ärztezentrum"},
	)
	f.setRegistry([]account.TagGroup{
		{ID: "g_z", Name: "Zahnärzte", Color: "teal"},
		{ID: "g_ae", Name: "Ärzte", Color: "blue"},
	}, map[string]account.TagMeta{
		"zahnklinik":   {Name: "Zahnklinik", Group: "g_z"},
		"ärztezentrum": {Name: "Ärztezentrum", Group: "g_ae"},
	})
	if got, want := f.tags(0), "Bäckerei,mockup,Müller GmbH,Zahnarzt,Ärztehaus"; got != want {
		t.Fatalf("site 1 is stored as %q, want the byte order %q", got, want)
	}
	groups := []string{"Ärzte", "Zahnärzte"}

	w := getAs(f.mux, "/account", f.cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard = %d", w.Code)
	}
	dash := w.Body.String()
	if got, want := submatches(dash, `<div class="tfbh tag-[a-z]+"><span class="tdot"></span>([^<]+)`), append(slices.Clone(groups), "No group"); !reflect.DeepEqual(got, want) {
		t.Errorf("filter blocks = %q, want %q", got, want)
	}
	if got, want := submatches(dash, `<a class="tfchip tag-[a-z]+(?: on)?" href="[^"]*"><span class="tdot"></span>([^<]+) <small>`), append([]string{"Ärztezentrum", "Zahnklinik"}, readerOrder...); !reflect.DeepEqual(got, want) {
		t.Errorf("filter chips = %q, want %q", got, want)
	}
	var rows []string
	for _, card := range strings.Split(dash, `<div class="sitecard">`)[1:] {
		rows = append(rows, strings.Join(submatches(card, `<span class="tn">([^<]+)</span>`), ","))
	}
	if want := []string{strings.Join(readerOrder, ","), "Ärztezentrum,Zahnklinik"}; !reflect.DeepEqual(rows, want) {
		t.Errorf("site chips = %q, want %q", rows, want)
	}
	if got, want := submatches(between(dash, `<datalist id="tagopts">`, `</datalist>`), `<option value="([^"]+)">`), []string{"Ärztehaus", "Ärztezentrum", "Bäckerei", "mockup", "Müller GmbH", "Zahnarzt", "Zahnklinik"}; !reflect.DeepEqual(got, want) {
		t.Errorf("datalist = %q, want %q", got, want)
	}
	if got := submatches(between(dash, `<select name="group"`, `</select>`), `<option value="g_[^"]+">([^<]+)</option>`); !reflect.DeepEqual(got, groups) {
		t.Errorf("group select = %q, want %q", got, groups)
	}

	manage := f.page()
	if got, want := submatches(manage, `class="tmglbl(?: none)?">([^<]+)<`), append(slices.Clone(groups), "No group"); !reflect.DeepEqual(got, want) {
		t.Errorf("manage page blocks = %q, want %q", got, want)
	}
	if got, want := submatches(manage, `<span class="tmname"><span class="tchip tag-[a-z]+">([^<]+)</span>`), append([]string{"Ärztezentrum", "Zahnklinik"}, readerOrder...); !reflect.DeepEqual(got, want) {
		t.Errorf("manage page tags = %q, want %q", got, want)
	}
	if got := submatches(between(manage, `<select name="value"`, `</select>`), `<option value="g_[^"]+"[^>]*>([^<]+)</option>`); !reflect.DeepEqual(got, groups) {
		t.Errorf("manage page group select = %q, want %q", got, groups)
	}
}

// The summary's facets — groups, then ungrouped tags — and the tags inside
// a group's "or" follow the same order; the URL the selection restores to
// keeps its byte order, so a bookmark never changes.
func TestTagFilterSummarySortsLikeAReader(t *testing.T) {
	reg := account.TagRegistry{
		Groups: []account.TagGroup{{ID: "g_z", Name: "Zahnärzte"}, {ID: "g_ae", Name: "Ärzte"}},
		Tags:   map[string]account.TagMeta{},
	}
	for name, group := range map[string]string{
		"Ärztehaus": "g_ae", "Bäckerei": "g_ae", "mockup": "g_ae", "Zahnklinik": "g_z",
		"Zahnarzt": "", "Müller GmbH": "", "Ölmühle": "",
	} {
		reg.Tags[store.TagKey(name)] = account.TagMeta{Name: name, Group: group}
	}
	q := url.Values{"g": {"g_z"}, "t": {"zahnarzt", "ölmühle", "mockup", "ärztehaus", "müller gmbh", "bäckerei"}}
	sel := parseTagSelection(q, reg, tfInUse(reg))
	if got, want := sel.Summary(reg, 1, 9), "1 of 9 · (Ärztehaus or Bäckerei or mockup) and Zahnärzte and Müller GmbH and Ölmühle and Zahnarzt"; got != want {
		t.Errorf("summary = %q\n want %q", got, want)
	}
	want := "g=g_z"
	for _, k := range []string{"bäckerei", "mockup", "müller gmbh", "zahnarzt", "ärztehaus", "ölmühle"} {
		want += "&t=" + url.QueryEscape(k)
	}
	if got := sel.Query(); got != want {
		t.Errorf("query = %q, want the byte order %q", got, want)
	}
}
