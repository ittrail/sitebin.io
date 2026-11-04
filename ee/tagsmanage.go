//go:build ee

package ee

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The tag manager, /account/tags: the account's groups with their colours,
// every tag it knows beneath its group, and the forms that recolour, regroup,
// rename and delete them. Like the dashboard it runs no script: every action
// is a plain POST form behind the CSRF token, and a confirmation is a page.
// Read "Manage tags" in docs/superpowers/specs/2026-11-03-site-tags-design.md.
//
// Group and colour changes write the registry only. A rename or a delete
// walks the account's sites first — each edit decided under the site lock
// on the list stored right then, so a concurrent API write is never lost —
// and writes the registry after them: a crash midway leaves sites carrying
// the new state, and the page simply shows it.

var (
	// errNoSuchTag refuses an operation on a tag the account neither keeps
	// in its registry nor carries on any site: a page from before a rename.
	errNoSuchTag = errors.New("that tag no longer exists")
	// errUnknownTagOp refuses a tag form with an op the page never sends.
	errUnknownTagOp = errors.New("unknown tag operation")
)

// tagsFlash are the notices after a change, by the code the redirect
// carries. The code from the URL is only ever a key: no text from a request
// reaches the page.
var tagsFlash = map[string]string{
	"group-created": "Group created.",
	"group-saved":   "Group saved.",
	"group-deleted": "Group deleted. Its tags are kept, without a group.",
	"saved":         "Tag saved.",
	"renamed":       "Tag renamed on every site that carries it.",
	"merged":        "Tags merged on every site that carried them.",
	"deleted":       "Tag deleted from every site that carried it.",
}

// manageTag is one tag row.
type manageTag struct {
	Key   string // the registry key, which the forms post
	Name  string
	Color string // the resolved palette id, for the chip
	Own   string // the tag's own colour; "" when it follows its group
	Group string // its group's id; "" when ungrouped
	Sites int    // sites of the account carrying it
}

// manageGroup is one block of the page: a group, or the "No group" block
// (ID "").
type manageGroup struct {
	ID    string
	Name  string
	Color string // the stored colour, "" for none: the checked dot
	Dot   string // the palette id the header's dot shows
	Tags  []manageTag
}

type tagsPageView struct {
	CSRF        string
	Flash       string
	Unreadable  bool               // the registry could not be read; tags show ungrouped
	Colors      []string           // the palette, in order
	Blocks      []manageGroup      // groups alphabetically, then No group when it has tags
	GroupOpts   []account.TagGroup // the group select, alphabetically
	Empty       bool               // no group and no tag at all
	NewColor    string             // the new-group form's preselected dot
	CanAddGroup bool
	MaxGroups   int
}

// accountSites is every site of the account the core still knows, with its
// tags.
func (p *provider) accountSites(acc *account.Account) ([]ext.SiteInfo, error) {
	return p.ownedSiteInfos(acc)
}

// tagUse maps each tag key the sites carry to its first spelling and to the
// number of sites carrying it.
func tagUse(sites []ext.SiteInfo) (inUse map[string]string, count map[string]int) {
	inUse, count = map[string]string{}, map[string]int{}
	for _, s := range sites {
		addInUse(inUse, s.Tags)
		seen := map[string]bool{}
		for _, t := range s.Tags {
			if k := store.TagKey(t); !seen[k] {
				seen[k] = true
				count[k]++
			}
		}
	}
	return inUse, count
}

func carriesTag(tags []string, key string) bool {
	for _, t := range tags {
		if store.TagKey(t) == key {
			return true
		}
	}
	return false
}

// validPalette reports whether c is one of the palette ids.
func validPalette(c string) bool { return c != "" && paletteID(c) == c }

func groupIndex(reg *account.TagRegistry, id string) int {
	for i, g := range reg.Groups {
		if id != "" && g.ID == id {
			return i
		}
	}
	return -1
}

func groupNameOf(reg account.TagRegistry, id string) string {
	for _, g := range reg.Groups {
		if g.ID == id {
			return g.Name
		}
	}
	return ""
}

// buildTagsPage lays the page out: every tag in the registry or on a site,
// under its group.
func buildTagsPage(reg account.TagRegistry, sites []ext.SiteInfo) tagsPageView {
	inUse, count := tagUse(sites)
	keys := make([]string, 0, len(reg.Tags)+len(inUse))
	for k := range reg.Tags {
		keys = append(keys, k)
	}
	for k := range inUse {
		if _, ok := reg.Tags[k]; !ok {
			keys = append(keys, k)
		}
	}
	sortKeysByName(keys, func(k string) string { return tagName(reg, inUse, k) })

	byGroup := map[string][]manageTag{}
	for _, k := range keys {
		t := manageTag{Key: k, Name: tagName(reg, inUse, k), Color: paletteID(reg.ColorOf(k)), Sites: count[k]}
		if g, ok := reg.GroupOf(k); ok {
			t.Group = g.ID
		}
		if c := reg.Tags[k].Color; validPalette(c) {
			t.Own = c
		}
		byGroup[t.Group] = append(byGroup[t.Group], t)
	}

	groups := sortedGroups(reg)
	v := tagsPageView{
		Colors:      account.TagColors,
		GroupOpts:   groups,
		Empty:       len(keys) == 0 && len(groups) == 0,
		CanAddGroup: len(groups) < account.MaxTagGroups,
		MaxGroups:   account.MaxTagGroups,
		NewColor:    account.TagColors[0],
	}
	used := map[string]bool{}
	for _, g := range groups {
		used[g.Color] = true
		b := manageGroup{ID: g.ID, Name: g.Name, Dot: paletteID(g.Color), Tags: byGroup[g.ID]}
		if validPalette(g.Color) {
			b.Color = g.Color
		}
		v.Blocks = append(v.Blocks, b)
	}
	if ts := byGroup[""]; len(ts) > 0 {
		v.Blocks = append(v.Blocks, manageGroup{Name: "No group", Dot: "grey", Tags: ts})
	}
	for _, c := range account.TagColors {
		if !used[c] {
			v.NewColor = c
			break
		}
	}
	return v
}

func (p *provider) handleTagsPage(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	sites, err := p.accountSites(acc)
	if err != nil {
		slog.Error("tags: could not list the account's sites", "account", acc.ID, "err", err)
		http.Error(w, "could not list your sites", http.StatusInternalServerError)
		return
	}
	reg, err := p.accounts.Tags(acc.ID)
	unreadable := err != nil
	if unreadable {
		// As on the dashboard: an unreadable registry shows the tags
		// ungrouped and grey, it never takes the page down.
		slog.Error("tags: the account's tag registry is unreadable; its tags show ungrouped", "account", acc.ID, "err", err)
		reg = account.TagRegistry{Tags: map[string]account.TagMeta{}}
	}
	v := buildTagsPage(reg, sites)
	v.CSRF = p.csrf(acc)
	v.Flash = tagsFlash[r.URL.Query().Get("flash")]
	v.Unreadable = unreadable
	p.securityHeaders(w)
	tagsTmpl.Execute(w, v)
}

// tagsForm opens a tag manager form: the signed-in account and its CSRF
// token. It answers the request itself when either is missing.
func (p *provider) tagsForm(w http.ResponseWriter, r *http.Request) (*account.Account, bool) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return nil, false
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return acc, true
}

// tagsRefused answers a change refused by a rule the person can act on.
// Every such message is written for people.
func (p *provider) tagsRefused(w http.ResponseWriter, title, why string) {
	p.securityHeaders(w)
	w.WriteHeader(http.StatusBadRequest)
	msgTmpl.Execute(w, msgView{Title: title, Body: "Sorry — " + why + ".", Back: "/account/tags"})
}

// registryFailed answers a tag change that failed: a 400 in the rule's
// words when one of the tag rules refused it (tagRuleText), and a logged 500
// that shows nothing of the error for anything else — a disk, a listing, an
// error nobody wrote for people, which may carry a path.
func (p *provider) registryFailed(w http.ResponseWriter, acc *account.Account, title string, err error) {
	if why, ok := tagRuleText(err); ok {
		p.tagsRefused(w, title, why)
		return
	}
	slog.Error("tags: the account's tag settings could not be saved", "account", acc.ID, "err", err)
	http.Error(w, "could not save the tag settings", http.StatusInternalServerError)
}

// ---- groups ----

// formColor reads a colour field: "" or a palette id.
func formColor(r *http.Request, field string) (string, error) {
	c := r.PostFormValue(field)
	if c != "" && !validPalette(c) {
		return "", account.ErrBadColor
	}
	return c, nil
}

// handleCreateTagGroup adds a group. The registry refuses a name another
// group has in any case, and the 21st group.
func (p *provider) handleCreateTagGroup(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.tagsForm(w, r)
	if !ok {
		return
	}
	name, err := store.CleanTag(r.PostFormValue("name"))
	if err != nil {
		p.tagsRefused(w, "That group was not created", "a group name follows the tag rule: "+err.Error())
		return
	}
	color, err := formColor(r, "color")
	if err != nil {
		p.tagsRefused(w, "That group was not created", err.Error())
		return
	}
	err = p.accounts.UpdateTags(acc.ID, func(reg *account.TagRegistry) error {
		reg.Groups = append(reg.Groups, account.TagGroup{ID: account.NewTagGroupID(), Name: name, Color: color})
		return nil
	})
	if err != nil {
		p.registryFailed(w, acc, "That group was not created", err)
		return
	}
	p.redirect(w, r, "/account/tags?flash=group-created")
}

// handleUpdateTagGroup renames or recolours a group: each field the form
// carries is applied, one it does not carry is kept.
func (p *provider) handleUpdateTagGroup(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.tagsForm(w, r)
	if !ok {
		return
	}
	gid := r.PathValue("gid")
	_, hasName := r.PostForm["name"]
	_, hasColor := r.PostForm["color"]
	var name, color string
	var err error
	if hasName {
		if name, err = store.CleanTag(r.PostFormValue("name")); err != nil {
			p.tagsRefused(w, "That group was not saved", "a group name follows the tag rule: "+err.Error())
			return
		}
	}
	if hasColor {
		if color, err = formColor(r, "color"); err != nil {
			p.tagsRefused(w, "That group was not saved", err.Error())
			return
		}
	}
	err = p.accounts.UpdateTags(acc.ID, func(reg *account.TagRegistry) error {
		i := groupIndex(reg, gid)
		if i < 0 {
			return errNoSuchGroup
		}
		if hasName {
			reg.Groups[i].Name = name
		}
		if hasColor {
			reg.Groups[i].Color = color
		}
		return nil
	})
	if err != nil {
		p.registryFailed(w, acc, "That group was not saved", err)
		return
	}
	p.redirect(w, r, "/account/tags?flash=group-saved")
}

// handleDeleteTagGroup removes a group and keeps its tags, ungrouped, each
// with its own colour if it has one: a tag that showed the group's colour is
// grey from now on. It asks first, saying how many tags the group holds; a
// group already gone is deleted.
func (p *provider) handleDeleteTagGroup(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.tagsForm(w, r)
	if !ok {
		return
	}
	gid := r.PathValue("gid")
	if r.PostFormValue("confirm") != "1" {
		p.confirmDeleteTagGroup(w, r, acc, gid)
		return
	}
	err := p.accounts.UpdateTags(acc.ID, func(reg *account.TagRegistry) error {
		i := groupIndex(reg, gid)
		if i < 0 {
			return nil
		}
		reg.Groups = append(reg.Groups[:i:i], reg.Groups[i+1:]...)
		for k, m := range reg.Tags {
			if m.Group == gid {
				m.Group = ""
				reg.Tags[k] = m
			}
		}
		return nil
	})
	if err != nil {
		p.registryFailed(w, acc, "That group was not deleted", err)
		return
	}
	p.redirect(w, r, "/account/tags?flash=group-deleted")
}

// confirmDeleteTagGroup is a group delete's confirmation: the group's name,
// how many tags it holds, and what becomes of them. Showing it changes
// nothing.
func (p *provider) confirmDeleteTagGroup(w http.ResponseWriter, r *http.Request, acc *account.Account, gid string) {
	reg, err := p.accounts.Tags(acc.ID)
	if err != nil {
		slog.Error("tags: the account's tag registry is unreadable", "account", acc.ID, "err", err)
		http.Error(w, "could not read your tag settings", http.StatusInternalServerError)
		return
	}
	i := groupIndex(&reg, gid)
	if i < 0 {
		p.redirect(w, r, "/account/tags?flash=group-deleted") // deleted already
		return
	}
	g := reg.Groups[i]
	n := 0
	for _, m := range reg.Tags {
		if m.Group == g.ID {
			n++
		}
	}
	line := "It holds no tags. No site is changed."
	if n > 0 {
		line = "It holds " + tagsText(n) + ", which are kept, without a group: a tag that shows the group's colour turns grey, one with its own colour keeps it. No site is changed."
	}
	p.renderTagStep(w, tagStepView{
		Title:  "Delete the group “" + g.Name + "”?",
		Lines:  []string{line},
		CSRF:   p.csrf(acc),
		Action: "/account/tags/groups/" + url.PathEscape(g.ID) + "/delete",
		Button: "Yes, delete “" + g.Name + "”", Danger: true,
	})
}

// tagsText is "1 tag" or "n tags".
func tagsText(n int) string {
	if n == 1 {
		return "1 tag"
	}
	return fmt.Sprintf("%d tags", n)
}

// ---- tags ----

// handleTagOp is every form of a tag row: op color, group, rename or delete
// on the tag whose key the form carries.
func (p *provider) handleTagOp(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.tagsForm(w, r)
	if !ok {
		return
	}
	key := r.PostFormValue("tag")
	if clean, err := store.CleanTag(key); err != nil || store.TagKey(clean) != key {
		p.tagsRefused(w, "Nothing was changed", errNoSuchTag.Error())
		return
	}
	switch r.PostFormValue("op") {
	case "color":
		p.tagSetColor(w, r, acc, key)
	case "group":
		p.tagSetGroup(w, r, acc, key)
	case "rename":
		p.tagRename(w, r, acc, key)
	case "delete":
		p.tagDelete(w, r, acc, key)
	default:
		p.tagsRefused(w, "Nothing was changed", errUnknownTagOp.Error())
	}
}

// editTagMeta changes the registry entry of key, creating it — in the
// spelling the sites carry — for a tag only in use. A tag the account does
// not know at all is errNoSuchTag.
func (p *provider) editTagMeta(acc *account.Account, key string, edit func(reg *account.TagRegistry, m *account.TagMeta) error) error {
	sites, err := p.accountSites(acc)
	if err != nil {
		return err
	}
	inUse, _ := tagUse(sites)
	return p.accounts.UpdateTags(acc.ID, func(reg *account.TagRegistry) error {
		m, ok := reg.Tags[key]
		if !ok {
			if inUse[key] == "" {
				return errNoSuchTag
			}
			m = account.TagMeta{Name: inUse[key]}
		}
		if err := edit(reg, &m); err != nil {
			return err
		}
		if !ok && m.Group == "" && m.Color == "" {
			return nil // nothing to remember: ungrouped and grey is no entry
		}
		if reg.Tags == nil {
			reg.Tags = map[string]account.TagMeta{}
		}
		reg.Tags[key] = m
		return nil
	})
}

// tagSetColor gives a tag its own colour, or with "" sends it back to its
// group's.
func (p *provider) tagSetColor(w http.ResponseWriter, r *http.Request, acc *account.Account, key string) {
	color, err := formColor(r, "value")
	if err != nil {
		p.tagsRefused(w, "That colour was not saved", err.Error())
		return
	}
	err = p.editTagMeta(acc, key, func(_ *account.TagRegistry, m *account.TagMeta) error {
		m.Color = color
		return nil
	})
	if err != nil {
		p.registryFailed(w, acc, "That colour was not saved", err)
		return
	}
	p.redirect(w, r, "/account/tags?flash=saved")
}

// tagSetGroup moves a tag into a group, or with "" out of every group. Its
// own colour, if it has one, stays.
func (p *provider) tagSetGroup(w http.ResponseWriter, r *http.Request, acc *account.Account, key string) {
	group := r.PostFormValue("value")
	err := p.editTagMeta(acc, key, func(reg *account.TagRegistry, m *account.TagMeta) error {
		if group != "" && groupIndex(reg, group) < 0 {
			return errNoSuchGroup
		}
		m.Group = group
		return nil
	})
	if err != nil {
		p.registryFailed(w, acc, "That group was not saved", err)
		return
	}
	p.redirect(w, r, "/account/tags?flash=saved")
}

// skippedSite is a site an account-wide change could not update.
type skippedSite struct {
	Label  string // its name, else its view id
	URL    string
	Locked bool // locked by the operator; otherwise the write failed
}

// retagSites applies edit to every site of the account that carries key,
// each under its site lock: edit receives the list stored right then and
// says whether it changed it. A locked site, or one whose write failed, is
// skipped and returned; a vanished one is ignored. Each changed site gets a
// line in its log, and is counted.
func (p *provider) retagSites(r *http.Request, acc *account.Account, sites []ext.SiteInfo, key string, edit func(cur []string) ([]string, bool)) (int, []skippedSite) {
	var skipped []skippedSite
	n := 0
	for _, s := range sites {
		if !carriesTag(s.Tags, key) {
			continue
		}
		changed := false
		err := p.host.Sites().UpdateTags(s.ViewID, func(cur []string) ([]string, error) {
			next, ok := edit(cur)
			changed = ok
			return next, nil
		})
		switch {
		case err == nil:
			if changed {
				n++
				p.recordSite(r, acc, s.ViewID, provenance.ActionTags, "")
			}
		case errors.Is(err, ext.ErrSiteGone):
		default:
			locked := errors.Is(err, ext.ErrSiteLocked)
			if !locked {
				slog.Error("tags: a site was not updated by an account-wide change", "account", acc.ID, "site", s.ViewID, "err", err)
			}
			label := s.Name
			if label == "" {
				label = s.ViewID
			}
			skipped = append(skipped, skippedSite{Label: label, URL: s.ViewURL, Locked: locked})
		}
	}
	return n, skipped
}

// renameIn replaces the tag key in cur by target, unless cur carries the
// target already (newKey), in which case the old tag only goes. It reports
// whether the list changed; one without the key comes back as it is.
func renameIn(cur []string, key, newKey, target string) ([]string, bool) {
	out := make([]string, 0, len(cur))
	had, hasTarget := false, false
	for _, t := range cur {
		switch store.TagKey(t) {
		case key:
			had = true
		case newKey:
			hasTarget = true
			out = append(out, t)
		default:
			out = append(out, t)
		}
	}
	if !had {
		return cur, false
	}
	if !hasTarget {
		out = append(out, target)
	}
	return out, !sameSpellings(out, cur)
}

// removeFrom drops the tag key from cur, reporting whether it was there.
func removeFrom(cur []string, key string) ([]string, bool) {
	out := make([]string, 0, len(cur))
	for _, t := range cur {
		if store.TagKey(t) != key {
			out = append(out, t)
		}
	}
	return out, len(out) != len(cur)
}

func sameSpellings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// renameEntry moves the registry entry of key to newKey. Onto an existing
// entry it merges: the existing entry's name, group and colour win, and an
// empty group or colour is filled from the renamed one. With no entry for
// key there is nothing to carry, and the registry is left as it is.
func renameEntry(reg *account.TagRegistry, key, newKey, newName string) {
	old, hasOld := reg.Tags[key]
	if !hasOld {
		return
	}
	if old.Group != "" && groupIndex(reg, old.Group) < 0 {
		old.Group = "" // its group went in the meantime
	}
	if newKey == key {
		old.Name = newName
		reg.Tags[key] = old
		return
	}
	delete(reg.Tags, key)
	if cur, ok := reg.Tags[newKey]; ok {
		if cur.Group == "" {
			cur.Group = old.Group
		}
		if cur.Color == "" {
			cur.Color = old.Color
		}
		reg.Tags[newKey] = cur
		return
	}
	reg.Tags[newKey] = account.TagMeta{Name: newName, Group: old.Group, Color: old.Color}
}

// sitesText is "1 site" or "n sites".
func sitesText(n int) string {
	if n == 1 {
		return "1 site"
	}
	return fmt.Sprintf("%d sites", n)
}

// keepsText says which settings a merged tag keeps.
func keepsText(reg account.TagRegistry, key, name string) string {
	group, color := "no group", "no colour"
	if m, ok := reg.Tags[key]; ok {
		if g := groupNameOf(reg, m.Group); g != "" {
			group = "the group “" + g + "”"
		}
		switch {
		case validPalette(m.Color):
			color = "its own colour " + m.Color
		case group != "no group":
			color = "the group's colour " + reg.ColorOf(key)
		}
	}
	return "The merged tag keeps the name “" + name + "”, " + group + " and " + color + "."
}

// tagRename renames a tag on every site of the account and in the registry.
// Onto a tag the account already knows it is a merge, confirmed first.
func (p *provider) tagRename(w http.ResponseWriter, r *http.Request, acc *account.Account, key string) {
	newName, err := store.CleanTag(r.PostFormValue("value"))
	if err != nil {
		p.tagsRefused(w, "That tag was not renamed", err.Error())
		return
	}
	newKey := store.TagKey(newName)
	sites, reg, ok := p.tagState(w, acc)
	if !ok {
		return
	}
	inUse, count := tagUse(sites)
	if _, registered := reg.Tags[key]; !registered && inUse[key] == "" {
		p.tagsRefused(w, "That tag was not renamed", errNoSuchTag.Error())
		return
	}
	_, targetRegistered := reg.Tags[newKey]
	merge := newKey != key && (targetRegistered || inUse[newKey] != "")
	target := newName
	if merge {
		target = tagName(reg, inUse, newKey) // the existing spelling wins
		if r.PostFormValue("confirm") != "1" {
			preview := account.TagRegistry{Groups: reg.Groups, Tags: map[string]account.TagMeta{}}
			for k, m := range reg.Tags {
				preview.Tags[k] = m
			}
			renameEntry(&preview, key, newKey, target)
			old := tagName(reg, inUse, key)
			p.renderTagStep(w, tagStepView{
				Title: "Merge “" + old + "” into “" + target + "”?",
				Lines: []string{
					"“" + target + "” already exists, on " + sitesText(count[newKey]) + ". Renaming “" + old + "” (on " + sitesText(count[key]) + ") to it merges the two: every site with “" + old + "” carries “" + target + "” instead, once.",
					keepsText(preview, newKey, target),
				},
				CSRF: p.csrf(acc), Tag: key, Op: "rename", Value: r.PostFormValue("value"),
				Button: "Yes, merge into “" + target + "”",
			})
			return
		}
	}

	changed, skipped := p.retagSites(r, acc, sites, key, func(cur []string) ([]string, bool) {
		return renameIn(cur, key, newKey, target)
	})
	err = p.accounts.UpdateTags(acc.ID, func(reg *account.TagRegistry) error {
		renameEntry(reg, key, newKey, target)
		return nil
	})
	if p.accountWideFailed(w, r, acc, "rename", changed, "That tag was not renamed", err) {
		return
	}
	flash, verb := "renamed", "renamed"
	if merge {
		flash, verb = "merged", "merged"
	}
	p.finishAccountWide(w, r, flash, "Tag "+verb, "“"+tagName(reg, inUse, key)+"”", skipped)
}

// tagDelete removes a tag from every site of the account, then from the
// registry, after a confirmation that says on how many sites it is.
func (p *provider) tagDelete(w http.ResponseWriter, r *http.Request, acc *account.Account, key string) {
	sites, reg, ok := p.tagState(w, acc)
	if !ok {
		return
	}
	inUse, count := tagUse(sites)
	_, registered := reg.Tags[key]
	if !registered && inUse[key] == "" {
		p.redirect(w, r, "/account/tags?flash=deleted") // deleted already
		return
	}
	name := tagName(reg, inUse, key)
	if r.PostFormValue("confirm") != "1" {
		line := "It is on " + sitesText(count[key]) + " of this account, and is removed from each of them. The sites themselves are not changed otherwise."
		if count[key] == 0 {
			line = "It is on 0 sites: only its group and colour are removed."
		}
		p.renderTagStep(w, tagStepView{
			Title: "Delete the tag “" + name + "”?",
			Lines: []string{line},
			CSRF:  p.csrf(acc), Tag: key, Op: "delete",
			Button: "Yes, delete “" + name + "”", Danger: true,
		})
		return
	}

	changed, skipped := p.retagSites(r, acc, sites, key, func(cur []string) ([]string, bool) {
		return removeFrom(cur, key)
	})
	err := p.accounts.UpdateTags(acc.ID, func(reg *account.TagRegistry) error {
		delete(reg.Tags, key)
		return nil
	})
	if p.accountWideFailed(w, r, acc, "delete", changed, "That tag was not deleted", err) {
		return
	}
	p.finishAccountWide(w, r, "deleted", "Tag deleted", "“"+name+"”", skipped)
}

// tagState reads what an account-wide change decides on: the account's
// sites and its registry. It answers the request itself when either cannot
// be read — a rename or delete must not run half-informed.
func (p *provider) tagState(w http.ResponseWriter, acc *account.Account) ([]ext.SiteInfo, account.TagRegistry, bool) {
	sites, err := p.accountSites(acc)
	if err != nil {
		slog.Error("tags: could not list the account's sites", "account", acc.ID, "err", err)
		http.Error(w, "could not list your sites", http.StatusInternalServerError)
		return nil, account.TagRegistry{}, false
	}
	reg, err := p.accounts.Tags(acc.ID)
	if err != nil {
		slog.Error("tags: the account's tag registry is unreadable", "account", acc.ID, "err", err)
		http.Error(w, "could not read your tag settings", http.StatusInternalServerError)
		return nil, account.TagRegistry{}, false
	}
	return sites, reg, true
}

// accountWideFailed records an account-wide rename or delete (op) in the
// account's log once it changed something — a site (changed counts them) or
// the registry — and answers the request itself when the registry write
// (err) failed, reporting whether it did. Sites changed before a failed
// registry write keep the change, so the log says it happened; with none
// changed nothing did, and nothing is recorded.
func (p *provider) accountWideFailed(w http.ResponseWriter, r *http.Request, acc *account.Account, op string, changed int, title string, err error) bool {
	if err == nil || changed > 0 {
		p.recordAccount(r, acc.ID, provenance.ActionTags, provenance.SurfaceDashboard, provenance.AuthSession, op)
	}
	switch {
	case err == nil:
		return false
	case changed == 0:
		p.registryFailed(w, acc, title, err)
	default:
		p.sitesDoneRegistryFailed(w, acc, err)
	}
	return true
}

// sitesDoneRegistryFailed answers an account-wide change whose sites were
// updated and whose registry write was not. The sites keep the change; the
// same operation again finishes it.
func (p *provider) sitesDoneRegistryFailed(w http.ResponseWriter, acc *account.Account, err error) {
	slog.Error("tags: sites updated, but the account's tag registry could not be written", "account", acc.ID, "err", err)
	p.securityHeaders(w)
	w.WriteHeader(http.StatusInternalServerError)
	msgTmpl.Execute(w, msgView{
		Title: "Only partly done",
		Body:  "Your sites were updated, but the tag's group and colour settings could not be saved. Do the same again to finish.",
		Back:  "/account/tags",
	})
}

// finishAccountWide ends a rename or delete: back to the page with a notice,
// or — when a site could not be updated — a page naming each one.
func (p *provider) finishAccountWide(w http.ResponseWriter, r *http.Request, flash, done, tag string, skipped []skippedSite) {
	if len(skipped) == 0 {
		p.redirect(w, r, "/account/tags?flash="+flash)
		return
	}
	p.renderTagStep(w, tagStepView{
		Title: done + ", except on " + sitesText(len(skipped)),
		Lines: []string{
			"The sites below could not be changed and still carry " + tag + ". A site locked by the operator cannot be changed while the lock is in place; once it is lifted, or for any other site, do the same again to finish.",
		},
		Skipped: skipped,
	})
}

// tagStepView is a confirmation (with the form to confirm) or the result of
// an account-wide change that skipped sites (with them listed). Showing it
// changes nothing.
type tagStepView struct {
	Title   string
	Lines   []string
	CSRF    string
	Action  string // where the confirm form posts; "" with an Op is a tag's form, "" without one means no form
	Tag     string // the tag form's fields
	Op      string
	Value   string
	Button  string
	Danger  bool
	Skipped []skippedSite
}

func (p *provider) renderTagStep(w http.ResponseWriter, v tagStepView) {
	if v.Action == "" && v.Op != "" {
		v.Action = "/account/tags/tag"
	}
	p.securityHeaders(w)
	tagStepTmpl.Execute(w, v)
}
