// Package analytics holds the pure logic of site analytics: the stored
// record, what a record may contain, and which requests count. It imports
// no httpapi code.
package analytics

import _ "time/tzdata" // zone names must resolve on images without a zoneinfo database

// Record is one analytics line. Keys are short because the files are the
// database; empty fields are omitted.
type Record struct {
	T  int64             `json:"t,omitempty"`  // Unix time, milliseconds
	K  string            `json:"k,omitempty"`  // "p" page view, "e" event
	S  string            `json:"s,omitempty"`  // "srv", "js", "um"
	A  string            `json:"a,omitempty"`  // address: host or host/path
	P  string            `json:"p,omitempty"`  // path
	Ti string            `json:"ti,omitempty"` // page title
	Q  map[string]string `json:"q,omitempty"`  // kept query parameters
	Ck []string          `json:"ck,omitempty"` // click-id names present
	R  string            `json:"r,omitempty"`  // external referrer host/path
	N  string            `json:"n,omitempty"`  // event name
	D  map[string]any    `json:"d,omitempty"`  // event data
	V  string            `json:"v,omitempty"`  // visitor id
	Vi string            `json:"vi,omitempty"` // visit id (imported only)
	B  string            `json:"b,omitempty"`  // browser
	O  string            `json:"o,omitempty"`  // OS
	Dv string            `json:"dv,omitempty"` // device
	Sc string            `json:"sc,omitempty"` // screen WxH
	L  string            `json:"l,omitempty"`  // language tag
	C  string            `json:"c,omitempty"`  // country
	Rg string            `json:"rg,omitempty"` // region
	Ct string            `json:"ct,omitempty"` // city
	Tg string            `json:"tg,omitempty"` // tag
	Vr string            `json:"vr,omitempty"` // variant, always "" in this version
}
