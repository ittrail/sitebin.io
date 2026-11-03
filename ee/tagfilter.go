//go:build ee

package ee

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/store"
)

// tagSelection is the dashboard's tag filter. It is pure data: the URL query
// in, a yes/no per site and a one-line summary out.
//
// Within one group the selected tags are OR'd, across groups they are AND'd,
// a whole group means "any tag of this group", and every ungrouped tag is a
// facet of its own. Untagged is exclusive.
type tagSelection struct {
	Untagged bool
	Tags     map[string]bool // tag keys (store.TagKey)
	Groups   map[string]bool // whole groups, by id

	// names holds display spellings of tags that have no registry entry, from
	// the caller's in-use list; the Summary falls back to it.
	names map[string]string
}

// parseTagSelection reads t=, g= and untagged= from q. inUse maps the tag
// keys carried by the account's sites to their display spelling. Tag keys
// that are neither in use nor registered, and group ids the registry does not
// hold, are dropped; untagged=1 beside anything else means only Untagged.
func parseTagSelection(q url.Values, reg account.TagRegistry, inUse map[string]string) tagSelection {
	if q.Get("untagged") == "1" {
		return tagSelection{Untagged: true}
	}
	s := tagSelection{Tags: map[string]bool{}, Groups: map[string]bool{}}
	for _, raw := range q["t"] {
		key := store.TagKey(raw)
		display, used := inUse[key]
		if _, registered := reg.Tags[key]; !used && !registered {
			continue
		}
		s.Tags[key] = true
		if used && display != "" {
			if s.names == nil {
				s.names = map[string]string{}
			}
			s.names[key] = display
		}
	}
	for _, id := range q["g"] {
		for _, g := range reg.Groups {
			if g.ID == id {
				s.Groups[id] = true
				break
			}
		}
	}
	return s
}

func (s tagSelection) clone() tagSelection {
	c := tagSelection{Untagged: s.Untagged, Tags: map[string]bool{}, Groups: map[string]bool{}, names: s.names}
	for k := range s.Tags {
		c.Tags[k] = true
	}
	for k := range s.Groups {
		c.Groups[k] = true
	}
	return c
}

// Query is the URL query that restores the selection: "" for All, otherwise
// untagged=1 alone, or group ids then tag keys, each sorted.
func (s tagSelection) Query() string {
	if s.Untagged {
		return "untagged=1"
	}
	v := url.Values{}
	for _, id := range sortedKeys(s.Groups) {
		v.Add("g", id)
	}
	for _, k := range sortedKeys(s.Tags) {
		v.Add("t", k)
	}
	return v.Encode() // keys sorted: g before t
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, on := range m {
		if on {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ToggleTag adds or removes one tag; choosing a tag clears Untagged.
func (s tagSelection) ToggleTag(key string) tagSelection {
	key = store.TagKey(key)
	c := s.clone()
	c.Untagged = false
	if c.Tags[key] {
		delete(c.Tags, key)
	} else {
		c.Tags[key] = true
	}
	return c
}

// ToggleGroup adds or removes a whole group; it clears Untagged.
func (s tagSelection) ToggleGroup(id string) tagSelection {
	c := s.clone()
	c.Untagged = false
	if c.Groups[id] {
		delete(c.Groups, id)
	} else {
		c.Groups[id] = true
	}
	return c
}

// WithUntagged selects only Untagged, clearing every tag and group.
func (s tagSelection) WithUntagged() tagSelection {
	return tagSelection{Untagged: true, names: s.names}
}

// facet is one AND-term: any of tag keys (of one group), or a whole group.
type facet struct {
	label string // sort key and display of the group, or of the lone tag
	whole string // group id when the whole group is selected
	group string // group id for a tag facet ("" for an ungrouped tag)
	keys  []string
}

// facets builds the AND-terms in summary order: groups alphabetically, then
// ungrouped tags.
func (s tagSelection) facets(reg account.TagRegistry) []facet {
	byGroup := map[string]*facet{}
	var ungrouped []facet
	for _, g := range reg.Groups {
		if s.Groups[g.ID] {
			byGroup[g.ID] = &facet{label: g.Name, whole: g.ID, group: g.ID}
		}
	}
	for _, key := range sortedKeys(s.Tags) {
		g, ok := reg.GroupOf(key)
		if !ok {
			ungrouped = append(ungrouped, facet{label: s.display(reg, key), keys: []string{key}})
			continue
		}
		f := byGroup[g.ID]
		if f == nil {
			f = &facet{label: g.Name, group: g.ID}
			byGroup[g.ID] = f
		}
		if f.whole == "" {
			f.keys = append(f.keys, key)
		}
	}
	var out []facet
	for _, f := range byGroup {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := store.TagKey(out[i].label), store.TagKey(out[j].label)
		if a != b {
			return a < b
		}
		return out[i].group < out[j].group
	})
	sort.Slice(ungrouped, func(i, j int) bool {
		return store.TagKey(ungrouped[i].label) < store.TagKey(ungrouped[j].label)
	})
	return append(out, ungrouped...)
}

// Matches reports whether a site carrying siteTags (display spellings) passes
// the filter: every facet must match.
func (s tagSelection) Matches(siteTags []string, reg account.TagRegistry) bool {
	if s.Untagged {
		return len(siteTags) == 0
	}
	have := make(map[string]bool, len(siteTags))
	haveGroup := map[string]bool{}
	for _, t := range siteTags {
		k := store.TagKey(t)
		have[k] = true
		if g, ok := reg.GroupOf(k); ok {
			haveGroup[g.ID] = true
		}
	}
	for _, f := range s.facets(reg) {
		if f.whole != "" {
			if !haveGroup[f.whole] {
				return false
			}
			continue
		}
		hit := false
		for _, k := range f.keys {
			if have[k] {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

func (s tagSelection) display(reg account.TagRegistry, key string) string {
	if m, ok := reg.Tags[key]; ok && m.Name != "" {
		return m.Name
	}
	if n := s.names[key]; n != "" {
		return n
	}
	return key
}

// Summary is the line above the list, "2 of 8 · (A or B) and mockup"; ""
// for All.
func (s tagSelection) Summary(reg account.TagRegistry, matched, total int) string {
	if s.Untagged {
		return fmt.Sprintf("%d of %d · untagged", matched, total)
	}
	var parts []string
	for _, f := range s.facets(reg) {
		switch {
		case f.whole != "" || len(f.keys) == 1 && f.group == "":
			parts = append(parts, f.label)
		case len(f.keys) == 1:
			parts = append(parts, s.display(reg, f.keys[0]))
		default:
			names := make([]string, len(f.keys))
			for i, k := range f.keys {
				names[i] = s.display(reg, k)
			}
			sort.Slice(names, func(i, j int) bool { return store.TagKey(names[i]) < store.TagKey(names[j]) })
			parts = append(parts, "("+strings.Join(names, " or ")+")")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d · %s", matched, total, strings.Join(parts, " and "))
}
