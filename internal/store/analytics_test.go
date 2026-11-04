package store

import (
	"strings"
	"testing"
)

func validAnalytics() Analytics {
	return Analytics{
		Enabled:   true,
		Mode:      "script",
		Timezone:  "Europe/Vienna",
		Addresses: []string{"a.sitebin.app"},
		Exclude:   []string{"/admin/**"},
		Query:     []string{"utm_source"},
		Funnels: []AnalyticsFunnel{{ID: "f1", Name: "Signup", WindowMinutes: 60,
			Steps: []AnalyticsStep{{"path", "/a"}, {"event", "signup"}}}},
		Goals: []AnalyticsGoal{{ID: "g1", Name: "Signed up", Type: "event", Value: "signup"}},
	}
}

func TestAnalyticsValidate(t *testing.T) {
	addrs := []string{"a.sitebin.app", "b.example"}
	many := func(n int, s string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = s
		}
		return out
	}
	tests := []struct {
		name  string
		mod   func(a *Analytics)
		field string
	}{
		{"valid", func(a *Analytics) {}, ""},
		{"empty mode ok", func(a *Analytics) { a.Mode = "" }, ""},
		{"bad mode", func(a *Analytics) { a.Mode = "pixel" }, "mode"},
		{"Local zone", func(a *Analytics) { a.Timezone = "Local" }, "timezone"},
		{"bad zone", func(a *Analytics) { a.Timezone = "Mars/Olympus" }, "timezone"},
		{"foreign address", func(a *Analytics) { a.Addresses = []string{"evil.example"} }, "addresses"},
		{"51 excludes", func(a *Analytics) { a.Exclude = many(51, "/x") }, "exclude"},
		{"exclude no slash", func(a *Analytics) { a.Exclude = []string{"admin"} }, "exclude"},
		{"query with space", func(a *Analytics) { a.Query = []string{"a b"} }, "query"},
		{"21 funnels", func(a *Analytics) {
			a.Funnels = nil
			for i := 0; i < 21; i++ {
				a.Funnels = append(a.Funnels, AnalyticsFunnel{ID: string(rune('a' + i)), Name: "n", WindowMinutes: 60,
					Steps: []AnalyticsStep{{"path", "/a"}, {"path", "/b"}}})
			}
		}, "funnels"},
		{"one step", func(a *Analytics) { a.Funnels[0].Steps = a.Funnels[0].Steps[:1] }, "funnels"},
		{"window 0", func(a *Analytics) { a.Funnels[0].WindowMinutes = 0 }, "funnels"},
		{"window 1441", func(a *Analytics) { a.Funnels[0].WindowMinutes = 1441 }, "funnels"},
		{"window 1440", func(a *Analytics) { a.Funnels[0].WindowMinutes = 1440 }, ""},
		{"step type x", func(a *Analytics) { a.Funnels[0].Steps[0].Type = "x" }, "funnels"},
		{"bad glob step", func(a *Analytics) { a.Funnels[0].Steps[0].Value = "nope" }, "funnels"},
		{"31 goals", func(a *Analytics) {
			a.Goals = nil
			for i := 0; i < 31; i++ {
				a.Goals = append(a.Goals, AnalyticsGoal{ID: string(rune('a' + i)), Name: "n", Type: "event", Value: "e"})
			}
		}, "goals"},
		{"goal bad type", func(a *Analytics) { a.Goals[0].Type = "x" }, "goals"},
		{"goal empty name", func(a *Analytics) { a.Goals[0].Name = "" }, "goals"},
		{"funnel id upper", func(a *Analytics) { a.Funnels[0].ID = "F1" }, "funnels"},
		{"funnel id 41", func(a *Analytics) { a.Funnels[0].ID = strings.Repeat("a", 41) }, "funnels"},
		{"funnel name 101", func(a *Analytics) { a.Funnels[0].Name = strings.Repeat("n", 101) }, "funnels"},
		{"funnel name 100", func(a *Analytics) { a.Funnels[0].Name = strings.Repeat("n", 100) }, ""},
		{"goal id space", func(a *Analytics) { a.Goals[0].ID = "a b" }, "goals"},
		{"goal name 101", func(a *Analytics) { a.Goals[0].Name = strings.Repeat("n", 101) }, "goals"},
		{"dup funnel id", func(a *Analytics) { a.Funnels = append(a.Funnels, a.Funnels[0]) }, "funnels"},
		{"dup goal id", func(a *Analytics) { a.Goals = append(a.Goals, a.Goals[0]) }, "goals"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := validAnalytics()
			tc.mod(&a)
			field, err := a.Validate(addrs)
			if field != tc.field || (field == "") != (err == nil) {
				t.Fatalf("got (%q, %v), want field %q", field, err, tc.field)
			}
		})
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pat, path string
		want      bool
	}{
		{"/kinder/**", "/kinder/a/b", true},
		{"/kinder/**", "/kinder/", true},
		{"/kinder/**", "/kinderx", false},
		{"/*/kids/**", "/en/kids/x", true},
		{"/*/kids/**", "/en/x/kids/x", false},
		{"/app-entwickeln-lassen/*", "/app-entwickeln-lassen/", true},
		{"/app-entwickeln-lassen/*", "/app-entwickeln-lassen/x", true},
		{"/app-entwickeln-lassen/*", "/app-entwickeln-lassen/x/y", false},
		{"/kinder/", "/kinder/", true},
		{"/kinder/", "/kinder", false},
		{"/a.b", "/axb", false},
	}
	for _, tc := range tests {
		if got := GlobMatch(tc.pat, tc.path); got != tc.want {
			t.Errorf("GlobMatch(%q, %q) = %v, want %v", tc.pat, tc.path, got, tc.want)
		}
		g, ok := CompileGlob(tc.pat)
		if !ok || g.Match(tc.path) != tc.want {
			t.Errorf("CompileGlob(%q).Match(%q) disagrees (ok=%v)", tc.pat, tc.path, ok)
		}
	}
	if GlobOK("kinder") || GlobOK("") || !GlobOK("/x/**") {
		t.Error("GlobOK wrong")
	}
	if GlobMatch("nope", "nope") {
		t.Error("an invalid pattern must not match")
	}
}

func TestWithStatsLockMissingSite(t *testing.T) {
	st := newTestStore(t)
	called := false
	found, err := st.WithStatsLock("doesnotexist", func(string) error { called = true; return nil })
	if found || err != nil || called {
		t.Fatalf("found=%v err=%v called=%v", found, err, called)
	}
}

func TestWithStatsLockExistingSiteAndSetters(t *testing.T) {
	st := newTestStore(t)
	site, _, err := st.Create()
	if err != nil {
		t.Fatal(err)
	}
	var dir string
	found, err := st.WithStatsLock(site.ViewID, func(d string) error { dir = d; return nil })
	if !found || err != nil || dir != site.Dir() {
		t.Fatalf("found=%v err=%v dir=%q", found, err, dir)
	}
	a := validAnalytics()
	if err := st.SetAnalytics(site, &a); err != nil {
		t.Fatal(err)
	}
	if site.Meta.Analytics == nil || !site.Meta.Analytics.Enabled {
		t.Fatal("analytics not stored")
	}
	if err := st.SetAnalytics(site, nil); err != nil || site.Meta.Analytics != nil {
		t.Fatalf("clear: %v", err)
	}
	v := true
	if err := st.SetQuotaAnalytics(site, &v); err != nil || site.Meta.QuotaAnalytics == nil || !*site.Meta.QuotaAnalytics {
		t.Fatalf("quota set: %v", err)
	}
	if err := st.SetQuotaAnalytics(site, nil); err != nil || site.Meta.QuotaAnalytics != nil {
		t.Fatalf("quota clear: %v", err)
	}
}
