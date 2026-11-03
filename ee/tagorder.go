//go:build ee

package ee

import (
	"slices"
	"strings"
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// Tags and groups are SHOWN in the order people read a list in, not in byte
// order: "Ärztehaus" sorts with the a's, not after "Zahnarzt". The order is
// the Unicode root collation (CLDR), language.Und. That is exactly German's
// standard order — CLDR has no German tailoring of it (ä sorts as a, as in
// DIN 5007-1; the phonebook's ä = ae is not what a list of tags wants) —
// and, unlike a German collator, it claims no language for an instance
// whose accounts may write in any. Only what a page shows is ordered so: a
// site's stored list (store.CleanTags) and the filter's URL
// (tagSelection.Query) keep their byte order, so no data and no bookmark
// changes.

// collators lends out root collators: a collate.Collator keeps buffers and
// is not safe for concurrent use.
var collators = sync.Pool{New: func() any { return collate.New(language.Und) }}

// compareTagNames orders two tag or group names for display. Names the
// collation holds equal (a composed and a decomposed ü) fall back to byte
// order, so the order is total and the same on every request.
func compareTagNames(a, b string) int {
	c := collators.Get().(*collate.Collator)
	r := c.CompareString(a, b)
	collators.Put(c)
	if r != 0 {
		return r
	}
	return strings.Compare(a, b)
}

// sortKeysByName orders tag keys by the names name gives them.
func sortKeysByName(keys []string, name func(key string) string) {
	slices.SortFunc(keys, func(a, b string) int {
		if c := compareTagNames(name(a), name(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
}
