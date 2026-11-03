//go:build ee

package account

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/ittrail/sitebin.io/internal/ids"
	"github.com/ittrail/sitebin.io/internal/store"
)

// An account's tag registry — its groups, and the colour and group of each
// tag it has named — lives in tags.json beside account.json, so deleting the
// account deletes it. The sites' own tags are in their meta.json; this file
// only decorates them.

const (
	// MaxTagGroups caps the groups one account keeps.
	MaxTagGroups = 20
	// MaxRegistryTags caps the entries of the registry: the tags with a
	// group or a colour, or created ahead of use. The tags the sites carry
	// are not counted; store.MaxSiteTags caps those per site.
	MaxRegistryTags = 200
)

// TagColors are the palette ids a group or tag may carry (hex lives in CSS).
var TagColors = []string{"blue", "green", "amber", "red", "violet", "teal", "pink", "grey"}

// The registry's refusals. Every message is written for the person who
// made the change, so a page may show it as it is.
var (
	ErrTooManyGroups       = errors.New("an account has at most 20 tag groups")
	ErrTooManyRegistryTags = errors.New("an account can give at most 200 tags a group or colour")
	ErrGroupNameTaken      = errors.New("a tag group with that name already exists")
	ErrBadColor            = errors.New("unknown tag colour")
	ErrBadGroupID          = errors.New("a tag group needs a unique id")
	ErrUnknownGroup        = errors.New("unknown tag group")
)

// TagGroup is a named, optionally coloured bundle of tags.
type TagGroup struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// TagMeta decorates one tag. The registry key is store.TagKey(Name).
type TagMeta struct {
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
	Color string `json:"color,omitempty"`
}

// TagRegistry is the account's groups and tag decorations.
type TagRegistry struct {
	Groups []TagGroup         `json:"groups"`
	Tags   map[string]TagMeta `json:"tags"`
}

// NewTagGroupID returns a fresh group id.
func NewTagGroupID() string { return "g_" + ids.New() }

func validColor(c string) bool {
	for _, k := range TagColors {
		if c == k {
			return true
		}
	}
	return false
}

func (r *TagRegistry) normalise() {
	if r.Groups == nil {
		r.Groups = []TagGroup{}
	}
	if r.Tags == nil {
		r.Tags = map[string]TagMeta{}
	}
}

// ColorOf resolves a tag's colour: its own, else its group's, else grey.
func (r TagRegistry) ColorOf(key string) string {
	m, ok := r.Tags[key]
	if !ok {
		return "grey"
	}
	if validColor(m.Color) {
		return m.Color
	}
	if g, ok := r.GroupOf(key); ok && validColor(g.Color) {
		return g.Color
	}
	return "grey"
}

// GroupOf returns the tag's group; false when ungrouped, unknown, or the
// group no longer exists.
func (r TagRegistry) GroupOf(key string) (TagGroup, bool) {
	m, ok := r.Tags[key]
	if !ok || m.Group == "" {
		return TagGroup{}, false
	}
	for _, g := range r.Groups {
		if g.ID == m.Group {
			return g, true
		}
	}
	return TagGroup{}, false
}

func (r *TagRegistry) validate() error {
	if len(r.Groups) > MaxTagGroups {
		return ErrTooManyGroups
	}
	if len(r.Tags) > MaxRegistryTags {
		return ErrTooManyRegistryTags
	}
	groupIDs := map[string]bool{}
	names := map[string]bool{}
	for i, g := range r.Groups {
		name, err := store.CleanTag(g.Name)
		if err != nil {
			return err
		}
		r.Groups[i].Name = name
		if g.ID == "" || groupIDs[g.ID] {
			return ErrBadGroupID
		}
		groupIDs[g.ID] = true
		if k := store.TagKey(name); names[k] {
			return ErrGroupNameTaken
		} else {
			names[k] = true
		}
		if g.Color != "" && !validColor(g.Color) {
			return ErrBadColor
		}
	}
	for k, m := range r.Tags {
		name, err := store.CleanTag(m.Name)
		if err != nil || k != store.TagKey(name) {
			return store.ErrBadTag
		}
		m.Name = name
		r.Tags[k] = m
		if m.Color != "" && !validColor(m.Color) {
			return ErrBadColor
		}
		if m.Group != "" && !groupIDs[m.Group] {
			return ErrUnknownGroup
		}
	}
	return nil
}

func (s *Store) tagsPath(id string) string {
	return filepath.Join(s.accountDir(id), "tags.json")
}

// readTags reads the registry; a missing file is an empty registry. The
// caller holds the account's lock.
func (s *Store) readTags(id string) (TagRegistry, error) {
	var r TagRegistry
	b, err := os.ReadFile(s.tagsPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			r.normalise()
			return r, nil
		}
		return TagRegistry{}, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return TagRegistry{}, err
	}
	r.normalise()
	return r, nil
}

// Tags returns the account's registry; no file is an empty registry.
func (s *Store) Tags(accountID string) (TagRegistry, error) {
	if !ids.ValidID(accountID) {
		return TagRegistry{}, ErrNotFound
	}
	l := s.lock(accountID)
	l.Lock()
	defer l.Unlock()
	return s.readTags(accountID)
}

// UpdateTags reads the registry, applies mutate, validates the result and
// writes it, all under the account's lock. Nothing is written when mutate or
// validation fails. Like RecordProvenance it refuses an account whose record
// is gone, so a deleted account's folder is never recreated.
func (s *Store) UpdateTags(accountID string, mutate func(*TagRegistry) error) error {
	if !ids.ValidID(accountID) {
		return ErrNotFound
	}
	l := s.lock(accountID)
	l.Lock()
	defer l.Unlock()
	if _, err := os.Stat(filepath.Join(s.accountDir(accountID), "account.json")); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	r, err := s.readTags(accountID)
	if err != nil {
		return err
	}
	if err := mutate(&r); err != nil {
		return err
	}
	r.normalise()
	if err := r.validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	dst := s.tagsPath(accountID)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return replaceFile(tmp, dst)
}
