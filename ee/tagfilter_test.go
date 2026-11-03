//go:build ee

package ee

import (
	"net/url"
	"sort"
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/store"
)

func tfRegistry() account.TagRegistry {
	reg := account.TagRegistry{
		Groups: []account.TagGroup{{ID: "g_kunde", Name: "Kunde"}, {ID: "g_typ", Name: "Typ"}},
		Tags:   map[string]account.TagMeta{},
	}
	add := func(name, group string) {
		reg.Tags[store.TagKey(name)] = account.TagMeta{Name: name, Group: group}
	}
	for _, n := range []string{"Müller GmbH", "Bäckerei Huber", "Stadtwerke"} {
		add(n, "g_kunde")
	}
	for _, n := range []string{"live", "mockup", "demo"} {
		add(n, "g_typ")
	}
	add("privat", "")
	add("archiv", "")
	return reg
}

func tfInUse(reg account.TagRegistry) map[string]string {
	m := map[string]string{}
	for k, v := range reg.Tags {
		m[k] = v.Name
	}
	return m
}

var tfSites = [][]string{
	{"Müller GmbH", "mockup"},              // 0
	{"Müller GmbH", "live"},                // 1
	{"Bäckerei Huber", "mockup", "privat"}, // 2
	{"Bäckerei Huber", "privat", "archiv"}, // 3
	{"Stadtwerke", "demo"},                 // 4
	{},                                     // 5
	{"privat"},                             // 6
	nil,                                    // 7
}

func tfRun(t *testing.T, reg account.TagRegistry, query string) []int {
	t.Helper()
	q, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	sel := parseTagSelection(q, reg, tfInUse(reg))
	var got []int
	for i, s := range tfSites {
		if sel.Matches(s, reg) {
			got = append(got, i)
		}
	}
	sort.Ints(got)
	return got
}

func eqInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTagFilterSemantics(t *testing.T) {
	reg := tfRegistry()
	orphan := tfRegistry()
	orphan.Groups = []account.TagGroup{{ID: "g_typ", Name: "Typ"}} // Kunde deleted
	cases := []struct {
		name  string
		reg   account.TagRegistry
		query string
		want  []int
	}{
		{"all", reg, "", []int{0, 1, 2, 3, 4, 5, 6, 7}},
		{"untagged", reg, "untagged=1", []int{5, 7}},
		{"untagged beats tags", reg, "untagged=1&t=mockup", []int{5, 7}},
		{"union within group", reg, "t=müller+gmbh&t=bäckerei+huber", []int{0, 1, 2, 3}},
		{"or then and", reg, "t=müller+gmbh&t=bäckerei+huber&t=mockup", []int{0, 2}},
		{"whole group", reg, "g=g_kunde", []int{0, 1, 2, 3, 4}},
		{"whole group plus its tag", reg, "g=g_kunde&t=müller+gmbh", []int{0, 1, 2, 3, 4}},
		{"ungrouped and", reg, "t=privat&t=archiv", []int{3}},
		{"group and ungrouped", reg, "g=g_kunde&t=privat", []int{2, 3}},
		{"case-insensitive key", reg, "t=MOCKUP", []int{0, 2}},
		{"deleted group is ungrouped", orphan, "t=müller+gmbh&t=bäckerei+huber", []int{}},
		{"deleted group, one tag", orphan, "t=müller+gmbh", []int{0, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tfRun(t, c.reg, c.query)
			if got == nil {
				got = []int{}
			}
			if !eqInts(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestTagSelectionDropsStaleEntries(t *testing.T) {
	reg := tfRegistry()
	q, _ := url.ParseQuery("t=gone&g=g_gone")
	sel := parseTagSelection(q, reg, tfInUse(reg))
	if sel.Query() != "" || sel.Untagged || len(sel.Tags) != 0 || len(sel.Groups) != 0 {
		t.Fatalf("not All: %+v", sel)
	}
	if !sel.Matches([]string{"live"}, reg) || !sel.Matches(nil, reg) {
		t.Fatal("All must match everything")
	}
}

func TestTagSelectionKeepsUnregisteredInUseTag(t *testing.T) {
	reg := tfRegistry()
	q, _ := url.ParseQuery("t=wild")
	sel := parseTagSelection(q, reg, map[string]string{"wild": "Wild"})
	if !sel.Tags["wild"] {
		t.Fatalf("dropped: %+v", sel)
	}
	if got := sel.Summary(reg, 1, 8); got != "1 of 8 · Wild" {
		t.Fatalf("summary %q", got)
	}
}

func TestTagSelectionRoundTripsNonASCII(t *testing.T) {
	reg := tfRegistry()
	inUse := map[string]string{"müller gmbh": "Müller GmbH"}
	enc := tagSelection{}.ToggleTag("müller gmbh").Query()
	q, err := url.ParseQuery(enc)
	if err != nil {
		t.Fatal(err)
	}
	sel := parseTagSelection(q, reg, inUse)
	if len(sel.Tags) != 1 || !sel.Tags["müller gmbh"] {
		t.Fatalf("got %+v from %q", sel, enc)
	}
}

func TestTagSelectionToggles(t *testing.T) {
	s := tagSelection{}.ToggleTag("a").ToggleTag("b").ToggleGroup("g_x")
	if s.Query() != "g=g_x&t=a&t=b" {
		t.Fatalf("query %q", s.Query())
	}
	s2 := s.ToggleTag("a")
	if !s.Tags["a"] || s2.Tags["a"] {
		t.Fatal("toggle must not mutate the receiver and must remove")
	}
	u := s.WithUntagged()
	if u.Query() != "untagged=1" || len(u.Tags) != 0 || len(u.Groups) != 0 {
		t.Fatalf("untagged %+v", u)
	}
	if v := u.ToggleTag("a"); v.Untagged || !v.Tags["a"] {
		t.Fatalf("tag must clear untagged: %+v", v)
	}
	if v := u.ToggleGroup("g_x"); v.Untagged || !v.Groups["g_x"] {
		t.Fatalf("group must clear untagged: %+v", v)
	}
}

func TestTagFilterSummary(t *testing.T) {
	reg := tfRegistry()
	parse := func(qs string) tagSelection {
		q, _ := url.ParseQuery(qs)
		return parseTagSelection(q, reg, tfInUse(reg))
	}
	cases := []struct {
		query   string
		matched int
		want    string
	}{
		{"", 8, ""},
		{"untagged=1", 3, "3 of 8 · untagged"},
		{"t=müller+gmbh&t=bäckerei+huber&t=mockup", 2, "2 of 8 · (Bäckerei Huber or Müller GmbH) and mockup"},
		{"g=g_kunde", 5, "5 of 8 · Kunde"},
		{"g=g_kunde&t=müller+gmbh&t=live", 2, "2 of 8 · Kunde and live"},
		{"t=privat&t=archiv&g=g_typ", 1, "1 of 8 · Typ and archiv and privat"},
		{"t=mockup", 2, "2 of 8 · mockup"},
	}
	for _, c := range cases {
		if got := parse(c.query).Summary(reg, c.matched, 8); got != c.want {
			t.Errorf("%q: got %q want %q", c.query, got, c.want)
		}
	}
}
