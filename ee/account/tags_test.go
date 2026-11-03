//go:build ee

package account

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func tagsFile(s *Store, id string) string { return filepath.Join(s.accountDir(id), "tags.json") }

func TestTagRegistryMissingFileIsEmpty(t *testing.T) {
	s := newStore(t)
	a, _ := s.CreateLocal("a@example.com", "h", "free")
	r, err := s.Tags(a.ID)
	if err != nil || len(r.Groups) != 0 || len(r.Tags) != 0 || r.Groups == nil || r.Tags == nil {
		t.Fatalf("empty registry = %+v %v", r, err)
	}
}

func TestUpdateTagsPersists(t *testing.T) {
	s := newStore(t)
	a, _ := s.CreateLocal("a@example.com", "h", "free")
	gid := NewTagGroupID()
	if !strings.HasPrefix(gid, "g_") {
		t.Fatalf("group id %q", gid)
	}
	err := s.UpdateTags(a.ID, func(r *TagRegistry) error {
		r.Groups = append(r.Groups, TagGroup{ID: gid, Name: "Kunde", Color: "blue"})
		r.Tags["urgent"] = TagMeta{Name: "Urgent", Group: gid, Color: "red"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Tags(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := TagRegistry{
		Groups: []TagGroup{{ID: gid, Name: "Kunde", Color: "blue"}},
		Tags:   map[string]TagMeta{"urgent": {Name: "Urgent", Group: gid, Color: "red"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if _, err := os.Stat(tagsFile(s, a.ID) + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp file left behind")
	}
}

func TestTagRegistryLimits(t *testing.T) {
	s := newStore(t)
	a, _ := s.CreateLocal("a@example.com", "h", "free")
	if err := s.UpdateTags(a.ID, func(r *TagRegistry) error {
		r.Groups = append(r.Groups, TagGroup{ID: "g_1", Name: "Kunde"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(tagsFile(s, a.ID))
	cases := []struct {
		name string
		want error
		fn   func(*TagRegistry) error
	}{
		{"21 groups", ErrTooManyGroups, func(r *TagRegistry) error {
			for i := 0; i < 20; i++ {
				r.Groups = append(r.Groups, TagGroup{ID: fmt.Sprintf("g_x%d", i), Name: fmt.Sprintf("G%d", i)})
			}
			return nil
		}},
		{"201 tags", ErrTooManyRegistryTags, func(r *TagRegistry) error {
			for i := 0; i < 201; i++ {
				n := fmt.Sprintf("t%d", i)
				r.Tags[n] = TagMeta{Name: n}
			}
			return nil
		}},
		{"dup group", ErrGroupNameTaken, func(r *TagRegistry) error {
			r.Groups = append(r.Groups, TagGroup{ID: "g_2", Name: "KUNDE"})
			return nil
		}},
		{"bad colour", ErrBadColor, func(r *TagRegistry) error {
			r.Groups[0].Color = "orange"
			return nil
		}},
		{"bad tag colour", ErrBadColor, func(r *TagRegistry) error {
			r.Tags["a"] = TagMeta{Name: "a", Color: "orange"}
			return nil
		}},
		{"unknown group", nil, func(r *TagRegistry) error {
			r.Tags["a"] = TagMeta{Name: "a", Group: "g_nope"}
			return nil
		}},
		{"key mismatch", nil, func(r *TagRegistry) error {
			r.Tags["b"] = TagMeta{Name: "a"}
			return nil
		}},
	}
	for _, c := range cases {
		err := s.UpdateTags(a.ID, c.fn)
		if err == nil || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
		after, _ := os.ReadFile(tagsFile(s, a.ID))
		if string(after) != string(before) {
			t.Errorf("%s: file changed", c.name)
		}
	}
	// A mutate error passes through and writes nothing.
	boom := errors.New("boom")
	if err := s.UpdateTags(a.ID, func(*TagRegistry) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("mutate error = %v", err)
	}
}

func TestColorOf(t *testing.T) {
	r := TagRegistry{
		Groups: []TagGroup{{ID: "g_1", Name: "K", Color: "green"}, {ID: "g_2", Name: "L"}},
		Tags: map[string]TagMeta{
			"own":     {Name: "own", Color: "red", Group: "g_1"},
			"inh":     {Name: "inh", Group: "g_1"},
			"none":    {Name: "none"},
			"gone":    {Name: "gone", Group: "g_9"},
			"weird":   {Name: "weird", Color: "orange"},
			"nogroup": {Name: "nogroup", Group: "g_2"},
		},
	}
	for k, want := range map[string]string{"own": "red", "inh": "green", "none": "grey", "gone": "grey", "weird": "grey", "nogroup": "grey", "unknown": "grey"} {
		if got := r.ColorOf(k); got != want {
			t.Errorf("ColorOf(%s) = %s, want %s", k, got, want)
		}
	}
	if g, ok := r.GroupOf("inh"); !ok || g.ID != "g_1" {
		t.Errorf("GroupOf inh = %v %v", g, ok)
	}
	for _, k := range []string{"none", "gone", "unknown"} {
		if _, ok := r.GroupOf(k); ok {
			t.Errorf("GroupOf(%s) should be false", k)
		}
	}
}

func TestTagsOfDeletedAccountIsNotFound(t *testing.T) {
	s := newStore(t)
	a, _ := s.CreateLocal("a@example.com", "h", "free")
	if err := s.UpdateTags(a.ID, func(r *TagRegistry) error { r.Tags["x"] = TagMeta{Name: "x"}; return nil }); err != nil {
		t.Fatal(err)
	}
	dir := s.accountDir(a.ID)
	if err := s.Delete(a, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTags(a.ID, func(r *TagRegistry) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateTags = %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("directory recreated")
	}
}
