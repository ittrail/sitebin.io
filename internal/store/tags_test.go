package store

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCleanTag(t *testing.T) {
	cases := []struct {
		in, want string
		bad      bool
	}{
		{in: "  Müller   GmbH ", want: "Müller GmbH"},
		{in: strings.Repeat("ü", 30), want: strings.Repeat("ü", 30)},
		{in: strings.Repeat("ü", 31), bad: true},
		{in: "", bad: true},
		{in: "   ", bad: true},
		{in: "a,b", bad: true},
		{in: "a\nb", bad: true},
		{in: "a\tb", bad: true},
		{in: "\xff\xfe", bad: true},
	}
	for _, c := range cases {
		got, err := CleanTag(c.in)
		if c.bad {
			if !errors.Is(err, ErrBadTag) {
				t.Errorf("CleanTag(%q) = %q, %v; want ErrBadTag", c.in, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("CleanTag(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestCleanTags(t *testing.T) {
	got, err := CleanTags([]string{"mockup", "Demo", "demo", "Müller GmbH"})
	want := []string{"Demo", "mockup", "Müller GmbH"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("CleanTags = %q, %v; want %q", got, err, want)
	}

	var eleven []string
	for i := 0; i < 11; i++ {
		eleven = append(eleven, fmt.Sprintf("t%02d", i))
	}
	if _, err := CleanTags(eleven); !errors.Is(err, ErrTooManyTags) {
		t.Errorf("11 distinct tags: err = %v; want ErrTooManyTags", err)
	}

	merged := append([]string{}, eleven[:10]...)
	merged = append(merged, "T00")
	got, err = CleanTags(merged)
	if err != nil || len(got) != 10 {
		t.Errorf("11 entries merging to 10: %q, %v", got, err)
	}

	if _, err := CleanTags([]string{"ok", "a,b"}); !errors.Is(err, ErrBadTag) {
		t.Errorf("bad entry: err = %v; want ErrBadTag", err)
	}

	got, err = CleanTags(nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("CleanTags(nil) = %#v, %v; want empty non-nil", got, err)
	}
}

// An untagged site must not write the field: meta.json is the database.
func TestUntaggedSiteWritesNoTagsField(t *testing.T) {
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
	if strings.Contains(string(b), `"tags"`) {
		t.Errorf("an untagged site wrote a tags field: %s", b)
	}
	if err := st.Update(site, func(m *Meta) error { m.Tags = []string{"a"}; return nil }); err != nil {
		t.Fatal(err)
	}
	again, err := st.ByViewID(site.ViewID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Meta.Tags, []string{"a"}) {
		t.Errorf("tags did not survive a reload: %q", again.Meta.Tags)
	}
}
