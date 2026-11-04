package analytics

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Dims are the dimensions Table answers. "query:<name>" tables the values
// of one kept query parameter as well; filters also take "data:<key>", an
// event data property.
var Dims = []string{"path", "entry", "exit", "title", "referrer", "channel", "utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "click_id", "browser", "os", "device", "screen", "country", "region", "city", "language", "address", "tag", "event"}

// DirectReferrer is the referrer row, and the referrer filter value, of the
// visits that came with no external referrer. No stored referrer can take
// this form: a referrer is a host and a path, and a host has no "(".
const DirectReferrer = "(direct)"

// ErrBadQuery wraps every refusal of a query: an unknown dimension or
// unit, or an empty range.
var ErrBadQuery = errors.New("analytics: bad query")

type dimKind uint8

const (
	// Record-level: a filter selects records.
	dimPath dimKind = iota
	dimTitle
	dimTag
	dimEvent
	dimData // "data:<key>", filters only
	// Visit-level: a filter selects whole visits. Keep dimEntry first.
	dimEntry
	dimExit
	dimReferrer
	dimChannel
	dimQuery // utm_* and "query:<name>", the parameter in param
	dimClickID
	dimBrowser
	dimOS
	dimDevice
	dimScreen
	dimCountry
	dimRegion
	dimCity
	dimLanguage
	dimAddress
)

type dim struct {
	kind  dimKind
	param string // the query parameter or the event data key
}

func (d dim) visitLevel() bool { return d.kind >= dimEntry }

var dimKinds = map[string]dimKind{
	"path": dimPath, "title": dimTitle, "tag": dimTag, "event": dimEvent,
	"entry": dimEntry, "exit": dimExit, "referrer": dimReferrer, "channel": dimChannel,
	"click_id": dimClickID, "browser": dimBrowser, "os": dimOS, "device": dimDevice,
	"screen": dimScreen, "country": dimCountry, "region": dimRegion, "city": dimCity,
	"language": dimLanguage, "address": dimAddress,
}

func parseDim(s string) (dim, error) {
	if k, ok := dimKinds[s]; ok {
		return dim{kind: k}, nil
	}
	if slices.Contains(utmNames, s) {
		return dim{kind: dimQuery, param: s}, nil
	}
	if name, ok := strings.CutPrefix(s, "query:"); ok && name != "" {
		return dim{kind: dimQuery, param: name}, nil
	}
	if key, ok := strings.CutPrefix(s, "data:"); ok && key != "" {
		return dim{kind: dimData, param: key}, nil
	}
	return dim{}, fmt.Errorf("%w: unknown dimension %q", ErrBadQuery, s)
}

// VisitLevel reports whether a filter on dim selects whole visits: their
// entry and exit, their attribution (referrer, channel, utm_*, click id,
// query:<name>) and where and with what they came. The rest (path, title,
// tag, event, data:<key>) select records.
func VisitLevel(d string) bool {
	p, err := parseDim(d)
	return err == nil && p.visitLevel()
}

// field is a dimension's value on one record, for the dimensions a record
// carries itself.
func field(r *Record, k dimKind) string {
	switch k {
	case dimPath:
		return r.P
	case dimTitle:
		return r.Ti
	case dimTag:
		return r.Tg
	case dimBrowser:
		return r.B
	case dimOS:
		return r.O
	case dimDevice:
		return r.Dv
	case dimScreen:
		return r.Sc
	case dimCountry:
		return r.C
	case dimRegion:
		return r.Rg
	case dimCity:
		return r.Ct
	case dimLanguage:
		return r.L
	case dimAddress:
		return r.A
	}
	return ""
}

// appendData appends an event data value as tables and filters show it: a
// string as it is, a number in its shortest form, a boolean as true or
// false.
func appendData(b []byte, v any) []byte {
	switch x := v.(type) {
	case string:
		return append(b, x...)
	case float64:
		return strconv.AppendFloat(b, x, 'f', -1, 64)
	case bool:
		return strconv.AppendBool(b, x)
	}
	return fmt.Append(b, v)
}

type filter struct {
	dim   dim
	value string
}

// matchRecord applies a record-level filter. buf is scratch space.
func (f *filter) matchRecord(r *Record, buf *[]byte) bool {
	switch f.dim.kind {
	case dimEvent:
		return r.K == "e" && r.N == f.value
	case dimData:
		if r.K != "e" {
			return false
		}
		v, ok := r.D[f.dim.param]
		if !ok {
			return false
		}
		if s, ok := v.(string); ok {
			return s == f.value
		}
		*buf = appendData((*buf)[:0], v)
		return string(*buf) == f.value
	}
	return field(r, f.dim.kind) == f.value
}
