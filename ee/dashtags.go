//go:build ee

package ee

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Tags on the account dashboard: the filter above "Your sites", each site's
// chips, and the two forms that add and remove one tag. Everything is a link
// or a plain POST form behind the CSRF token and the ownership check — the
// dashboard runs no script. Read docs/superpowers/specs/2026-11-03-site-tags-design.md.

// tagChip is one tag as the page draws it. Color is a palette id, checked
// against account.TagColors, and only ever becomes a class name.
type tagChip struct{ Name, Key, Color string }

// tagFilterChip is a chip in the filter: a link to the next filter state.
type tagFilterChip struct {
	tagChip
	Href     string
	Count    int // sites of the account carrying the tag, whatever is filtered
	Selected bool
}

// tagFilterBlock is one group of the filter, or the "No group" block.
type tagFilterBlock struct {
	Name      string
	Color     string // the dot's palette id
	WholeHref string // toggles "whole group"; "" for No group
	Whole     bool   // the whole group is selected
	Partial   bool   // something in the block is selected
	Chips     []tagFilterChip
}

// tagFilterView is the filter area. Counts are totals over the account.
type tagFilterView struct {
	Show             bool // the account uses tags, or a filter is active
	All              int
	AllHref          string
	AllSelected      bool
	Untagged         int
	UntaggedHref     string
	UntaggedSelected bool
	Blocks           []tagFilterBlock // groups alphabetically, then No group
	Summary          string
	ClearHref        string
}

// paletteID is c when it is a palette id, else grey: a colour from data never
// reaches the page as anything but one of eight class names.
func paletteID(c string) string {
	for _, k := range account.TagColors {
		if c == k {
			return c
		}
	}
	return "grey"
}

// dashHref is the dashboard's address for a filter query.
func dashHref(q string) string {
	if q == "" {
		return "/account"
	}
	return "/account?" + q
}

// tagRegistry is the account's tag registry, or an empty one when it cannot
// be read: a corrupt tags.json shows the tags ungrouped and grey, it never
// takes the site list down (Review Focus 4).
func (p *provider) tagRegistry(acc *account.Account) account.TagRegistry {
	reg, err := p.accounts.Tags(acc.ID)
	if err != nil {
		slog.Error("tags: the account's tag registry is unreadable; its tags show ungrouped", "account", acc.ID, "err", err)
		return account.TagRegistry{Tags: map[string]account.TagMeta{}}
	}
	return reg
}

// tagsInUse maps every tag key the account's sites carry to the first
// spelling found.
func (p *provider) tagsInUse(acc *account.Account) map[string]string {
	inUse := map[string]string{}
	ids, _ := p.ownedSiteIDs(acc)
	for _, id := range ids {
		if info, ok := p.host.Sites().Info(id); ok {
			addInUse(inUse, info.Tags)
		}
	}
	return inUse
}

func addInUse(inUse map[string]string, tags []string) {
	for _, t := range tags {
		if k := store.TagKey(t); inUse[k] == "" {
			inUse[k] = t
		}
	}
}

// tagName is the spelling the account knows a tag by: the registry's, else
// the one its sites carry.
func tagName(reg account.TagRegistry, inUse map[string]string, key string) string {
	if m, ok := reg.Tags[key]; ok && m.Name != "" {
		return m.Name
	}
	if n := inUse[key]; n != "" {
		return n
	}
	return key
}

// compareGroups orders groups by name (compareTagNames), then by id.
func compareGroups(a, b account.TagGroup) int {
	if c := compareTagNames(a.Name, b.Name); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// sortedGroups is the registry's groups by name.
func sortedGroups(reg account.TagRegistry) []account.TagGroup {
	gs := append([]account.TagGroup(nil), reg.Groups...)
	slices.SortStableFunc(gs, compareGroups)
	return gs
}

// siteChips orders a site's tags by group (by name, ungrouped last), then by
// name, keeping the site's own spelling.
func siteChips(tags []string, reg account.TagRegistry) []tagChip {
	type sortable struct {
		tagChip
		group   account.TagGroup
		grouped bool
	}
	out := make([]sortable, 0, len(tags))
	for _, t := range tags {
		k := store.TagKey(t)
		s := sortable{tagChip: tagChip{Name: t, Key: k, Color: paletteID(reg.ColorOf(k))}}
		s.group, s.grouped = reg.GroupOf(k)
		out = append(out, s)
	}
	slices.SortStableFunc(out, func(a, b sortable) int {
		if a.grouped != b.grouped {
			if a.grouped {
				return -1
			}
			return 1
		}
		if a.grouped {
			if c := compareGroups(a.group, b.group); c != 0 {
				return c
			}
		}
		return compareTagNames(a.Name, b.Name)
	})
	chips := make([]tagChip, len(out))
	for i, s := range out {
		chips[i] = s.tagChip
	}
	return chips
}

// tagOptions is the datalist: every tag the account knows, by registry or by
// use, once, by name.
func tagOptions(reg account.TagRegistry, inUse map[string]string) []string {
	keys := make([]string, 0, len(reg.Tags)+len(inUse))
	seen := map[string]bool{}
	for k := range reg.Tags {
		keys, seen[k] = append(keys, k), true
	}
	for k := range inUse {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sortKeysByName(keys, func(k string) string { return tagName(reg, inUse, k) })
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = tagName(reg, inUse, k)
	}
	return out
}

// buildTagFilter lays out the filter for sel over the account's sites
// (siteTags, one entry per site). A block shows when one of its tags is in
// use or something in it is selected.
func buildTagFilter(sel tagSelection, reg account.TagRegistry, inUse map[string]string, siteTags [][]string, matched int) tagFilterView {
	count := map[string]int{}
	v := tagFilterView{All: len(siteTags)}
	for _, tags := range siteTags {
		if len(tags) == 0 {
			v.Untagged++
		}
		for _, t := range tags {
			count[store.TagKey(t)]++
		}
	}
	all := sel.Query() == ""
	v.AllHref, v.AllSelected = "/account", all
	v.UntaggedSelected = sel.Untagged
	if sel.Untagged {
		v.UntaggedHref = "/account"
	} else {
		v.UntaggedHref = dashHref(sel.WithUntagged().Query())
	}
	v.Summary = sel.Summary(reg, matched, len(siteTags))
	if v.Summary != "" {
		v.ClearHref = "/account"
	}
	v.Show = len(inUse) > 0 || !all

	// The tags each block lists: those in use, and those selected.
	byGroup := map[string][]string{} // group id ("" = No group) → keys
	listed := map[string]bool{}
	list := func(k string) {
		if listed[k] {
			return
		}
		listed[k] = true
		g, _ := reg.GroupOf(k)
		byGroup[g.ID] = append(byGroup[g.ID], k)
	}
	for k := range inUse {
		list(k)
	}
	for k, on := range sel.Tags {
		if on {
			list(k)
		}
	}

	block := func(g account.TagGroup, keys []string) tagFilterBlock {
		sortKeysByName(keys, func(k string) string { return tagName(reg, inUse, k) })
		b := tagFilterBlock{Name: g.Name, Color: paletteID(g.Color), Whole: g.ID != "" && sel.Groups[g.ID]}
		if g.ID != "" {
			b.WholeHref = dashHref(sel.ToggleGroup(g.ID).Query())
		} else {
			b.Name = "No group"
		}
		for _, k := range keys {
			c := tagFilterChip{
				tagChip:  tagChip{Name: tagName(reg, inUse, k), Key: k, Color: paletteID(reg.ColorOf(k))},
				Count:    count[k],
				Selected: b.Whole || sel.Tags[k],
			}
			c.Href = dashHref(chipToggle(sel, g.ID, b.Whole, k, keys).Query())
			b.Partial = b.Partial || c.Selected
			b.Chips = append(b.Chips, c)
		}
		b.Partial = b.Partial || b.Whole
		return b
	}
	for _, g := range sortedGroups(reg) {
		if keys := byGroup[g.ID]; len(keys) > 0 || sel.Groups[g.ID] {
			v.Blocks = append(v.Blocks, block(g, keys))
		}
	}
	if keys := byGroup[""]; len(keys) > 0 {
		v.Blocks = append(v.Blocks, block(account.TagGroup{}, keys))
	}
	return v
}

// chipToggle is the selection a chip links to. In a group selected as a
// whole every chip shows selected, so clicking one deselects it: the group
// becomes its other listed tags.
func chipToggle(sel tagSelection, groupID string, whole bool, key string, keys []string) tagSelection {
	if !whole {
		return sel.ToggleTag(key)
	}
	next := sel.ToggleGroup(groupID)
	for _, k := range keys {
		if (k != key) != next.Tags[k] {
			next = next.ToggleTag(k)
		}
	}
	return next
}

// tagBack is where a tag form returns: /account with the filter it was
// posted from. Only a query string is taken, never a URL or a path.
func tagBack(r *http.Request) string {
	b := r.PostFormValue("back")
	if len(b) < 2 || len(b) > 2048 || b[0] != '?' || b[1] == '/' || strings.ContainsAny(b, "#\\") {
		return "/account"
	}
	for _, c := range b {
		if c < 0x20 || c == 0x7f {
			return "/account"
		}
	}
	return "/account" + b
}

// errNoSuchGroup refuses a new tag's group that is not (or no longer) one of
// the account's.
var errNoSuchGroup = errors.New("that tag group no longer exists")

// tagRules are the refusals whose messages are written for the person who
// made the change: the registry's rules, the tag rule, and the forms' own
// checks. Only these ever reach a page; any other error is the instance's
// trouble and may carry a path.
var tagRules = []error{
	account.ErrTooManyGroups, account.ErrTooManyRegistryTags, account.ErrGroupNameTaken,
	account.ErrBadColor, account.ErrBadGroupID, account.ErrUnknownGroup,
	store.ErrBadTag, errNoSuchGroup, errNoSuchTag,
}

// tagRuleText is the message of the rule that refused err, and whether one
// did. It is the rule's own text, never err's, which may wrap it in more.
func tagRuleText(err error) (string, bool) {
	for _, rule := range tagRules {
		if errors.Is(err, rule) {
			return rule.Error(), true
		}
	}
	return "", false
}

// tagFormError answers a tag form refused by the tag rules, and reports
// whether it did. The rules' messages are written for people.
func (p *provider) tagFormError(w http.ResponseWriter, r *http.Request, acc *account.Account, viewID string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrBadTag), errors.Is(err, store.ErrTooManyTags):
		p.securityHeaders(w)
		w.WriteHeader(http.StatusBadRequest)
		msgTmpl.Execute(w, msgView{Title: "That tag was not saved", Body: "Sorry — " + err.Error() + ".", Back: tagBack(r)})
	case errors.Is(err, ext.ErrSiteLocked):
		p.renderLocked(w)
	case errors.Is(err, ext.ErrSiteGone):
		http.Error(w, "site not found", http.StatusNotFound)
	default:
		slog.Error("site tags", "account", acc.ID, "site", viewID, "err", err)
		http.Error(w, "could not change the site's tags", http.StatusInternalServerError)
	}
	return true
}

// tagForm opens a tag form: the signed-in owner, the CSRF token, the site
// they own. It answers the request itself when one of them is missing.
func (p *provider) tagForm(w http.ResponseWriter, r *http.Request) (*account.Account, string, bool) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return nil, "", false
	}
	viewID := r.PathValue("id")
	if !p.checkCSRF(r, acc) || !p.owns(acc, viewID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, "", false
	}
	return acc, viewID, true
}

// handleAddSiteTag adds one tag to a site. A tag the account already knows,
// in the registry or on any of its sites, keeps that spelling and its group
// whatever case was typed and whichever group was chosen; only a new one with
// a group gets a registry entry in that group.
func (p *provider) handleAddSiteTag(w http.ResponseWriter, r *http.Request) {
	acc, viewID, ok := p.tagForm(w, r)
	if !ok {
		return
	}
	clean, err := store.CleanTag(r.PostFormValue("tag"))
	if p.tagFormError(w, r, acc, viewID, err) {
		return
	}
	key := store.TagKey(clean)
	reg := p.tagRegistry(acc)
	inUse := p.tagsInUse(acc)
	name := clean
	if reg.Tags[key].Name != "" || inUse[key] != "" {
		name = tagName(reg, inUse, key)
	}
	changed := false
	err = p.host.Sites().UpdateTags(viewID, func(cur []string) ([]string, error) {
		for _, t := range cur {
			if store.TagKey(t) == key {
				return cur, nil // already there, in the site's own spelling
			}
		}
		changed = true
		return append(cur, name), nil
	})
	if p.tagFormError(w, r, acc, viewID, err) {
		return
	}
	if changed {
		p.recordSite(r, acc, viewID, provenance.ActionTags, "")
	}

	// The group is for a NEW tag only: one in neither the registry nor use on
	// any of the account's sites. An existing tag keeps its (un)grouped state
	// wherever it is added; grouping it is the manage page's job.
	if group := r.PostFormValue("group"); group != "" {
		if _, registered := reg.Tags[key]; !registered && inUse[key] == "" {
			if err := p.registerTag(acc, key, name, group); err != nil {
				p.tagAddedWithoutGroup(w, r, acc, name, err)
				return
			}
		}
	}
	p.redirect(w, r, tagBack(r))
}

// registerTag gives a new tag its registry entry in group. A tag registered
// in the meantime is left as it is.
func (p *provider) registerTag(acc *account.Account, key, name, group string) error {
	return p.accounts.UpdateTags(acc.ID, func(reg *account.TagRegistry) error {
		if _, ok := reg.Tags[key]; ok {
			return nil
		}
		found := false
		for _, g := range reg.Groups {
			found = found || g.ID == group
		}
		if !found {
			return errNoSuchGroup
		}
		if reg.Tags == nil {
			reg.Tags = map[string]account.TagMeta{}
		}
		reg.Tags[key] = account.TagMeta{Name: name, Group: group}
		return nil
	})
}

// tagAddedWithoutGroup answers an add whose site write succeeded and whose
// registry write did not. The site keeps the tag; the page says what is
// missing rather than undoing what worked.
func (p *provider) tagAddedWithoutGroup(w http.ResponseWriter, r *http.Request, acc *account.Account, name string, err error) {
	status, why := http.StatusInternalServerError, "the account's tag settings could not be saved"
	if rule, ok := tagRuleText(err); ok {
		status, why = http.StatusBadRequest, rule
	} else {
		slog.Error("tags: a new tag's registry entry could not be written", "account", acc.ID, "err", err)
	}
	p.securityHeaders(w)
	w.WriteHeader(status)
	msgTmpl.Execute(w, msgView{
		Title: "Tag added without its group",
		Body:  "The tag “" + name + "” is on the site, but its group was not saved: " + why + ". You can group it under Manage tags.",
		Back:  tagBack(r),
	})
}

// handleRemoveSiteTag removes one tag, by key, from a site. The registry
// keeps its entry: other sites may carry the tag, and an unused entry stays
// until it is deleted on the manage page.
func (p *provider) handleRemoveSiteTag(w http.ResponseWriter, r *http.Request) {
	acc, viewID, ok := p.tagForm(w, r)
	if !ok {
		return
	}
	clean, err := store.CleanTag(r.PostFormValue("tag"))
	if p.tagFormError(w, r, acc, viewID, err) {
		return
	}
	key := store.TagKey(clean)
	changed := false
	err = p.host.Sites().UpdateTags(viewID, func(cur []string) ([]string, error) {
		out := cur[:0:0]
		for _, t := range cur {
			if store.TagKey(t) == key {
				changed = true
				continue
			}
			out = append(out, t)
		}
		return out, nil
	})
	if p.tagFormError(w, r, acc, viewID, err) {
		return
	}
	if changed {
		p.recordSite(r, acc, viewID, provenance.ActionTags, "")
	}
	p.redirect(w, r, tagBack(r))
}

// dashTags is what renderDashboard needs from the tags: the filtered rows
// with their chips, and the filter area.
type dashTags struct {
	rows    []siteRow
	filter  tagFilterView
	options []string
	groups  []account.TagGroup
	back    string // the current filter as a query string, "" for All
}

// applyTags filters rows by the request's tag query and gives each row its
// chips. rows are every site of the account.
func (p *provider) applyTags(r *http.Request, acc *account.Account, rows []siteRow) dashTags {
	reg := p.tagRegistry(acc)
	inUse := map[string]string{}
	siteTags := make([][]string, len(rows))
	for i, row := range rows {
		addInUse(inUse, row.SiteInfo.Tags)
		siteTags[i] = row.SiteInfo.Tags
	}
	sel := parseTagSelection(r.URL.Query(), reg, inUse)
	shown := rows[:0:0]
	for _, row := range rows {
		if sel.Matches(row.SiteInfo.Tags, reg) {
			row.Tags = siteChips(row.SiteInfo.Tags, reg)
			shown = append(shown, row)
		}
	}
	d := dashTags{
		rows:    shown,
		filter:  buildTagFilter(sel, reg, inUse, siteTags, len(shown)),
		options: tagOptions(reg, inUse),
		groups:  sortedGroups(reg),
	}
	if q := sel.Query(); q != "" {
		d.back = "?" + q
	}
	return d
}
