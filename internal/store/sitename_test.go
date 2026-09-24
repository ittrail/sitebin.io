package store

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCleanSiteName(t *testing.T) {
	cases := []struct {
		in, want string
		bad      bool
	}{
		{in: "Client docs", want: "Client docs"},
		{in: "  Client docs  ", want: "Client docs"},
		{in: "", want: ""},
		{in: "   ", want: ""},
		{in: strings.Repeat("a", 60), want: strings.Repeat("a", 60)},
		// The cap counts characters, not bytes: 60 umlauts are 120 bytes.
		{in: strings.Repeat("ä", 60), want: strings.Repeat("ä", 60)},
		{in: strings.Repeat("a", 61), bad: true},
		{in: "two\nlines", bad: true},
		{in: "carriage\rreturn", bad: true},
		{in: "tab\there", bad: true},
		{in: "\xff\xfe", bad: true},
	}
	for _, c := range cases {
		got, err := CleanSiteName(c.in)
		if c.bad {
			if !errors.Is(err, ErrBadSiteName) {
				t.Errorf("CleanSiteName(%q) = %q, %v; want ErrBadSiteName", c.in, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("CleanSiteName(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

// An unnamed site must not write the field at all: meta.json is the database,
// and every site created before names existed reads back as unnamed.
func TestUnnamedSiteWritesNoNameField(t *testing.T) {
	st, err := New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	site, _, err := st.Create()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(metaPath(site.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"name"`) {
		t.Errorf("an unnamed site wrote a name field: %s", b)
	}
	if err := st.Update(site, func(m *Meta) error { m.Name = "Docs"; return nil }); err != nil {
		t.Fatal(err)
	}
	again, err := st.ByViewID(site.ViewID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Meta.Name != "Docs" {
		t.Errorf("name did not survive a reload: %q", again.Meta.Name)
	}
}
