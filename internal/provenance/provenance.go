// Package provenance records who made a site or an account change, and from
// where: the client address, its user agent, the surface it came through and
// the credential that authorised it. It exists because "was this the same
// person?" is unanswerable after the fact if it was never written down — the
// 2026-09 phishing incident had two abusers, one minting an API token in the
// minute its account was created and re-uploading a deleted page hours later,
// and nothing linked any of it to an address.
//
// A log is a small JSONL file kept next to the data it describes
// (sites/<id>/provenance.jsonl, accounts/<id>/provenance.jsonl), so deleting
// the subject deletes its trail. It is bounded (MaxEntries, bursts merged),
// and the cleanup sweep purges entries older than Retention. See
// docs/superpowers/specs/2026-09-29-provenance-csp-apex.md.
//
// The package does no locking of its own: callers serialise the records of one
// file (the store takes the site's stats lock, the account store the
// account's lock), which is also what keeps a record from recreating the
// folder of a subject being deleted.
package provenance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// FileName is the log's name inside its subject's folder.
const FileName = "provenance.jsonl"

const (
	// Retention is how long an entry is kept: 90 days, the period the privacy
	// policy states. Legitimate interest (GDPR Art. 6(1)(f)): detecting and
	// proving abuse of the service.
	Retention = 90 * 24 * time.Hour
	// MaxEntries bounds one log. The oldest go first, except an origin entry
	// in first place (the creation, the sign-up), which is what the register
	// shows about where a subject came from.
	MaxEntries = 100
	// MergeWindow is how close in time two otherwise identical events must be
	// to become one line.
	MergeWindow = 10 * time.Minute
	// MaxUA and MaxDetail cap what a client controls.
	MaxUA     = 200
	MaxDetail = 200
)

// Actions. Site logs use the first group, account logs the second.
const (
	ActionCreate         = "create"
	ActionUpload         = "upload"
	ActionReplace        = "replace"
	ActionDeleteFile     = "delete-file"
	ActionMkdir          = "mkdir"
	ActionMove           = "move"
	ActionCopy           = "copy"
	ActionProps          = "proppatch"
	ActionSettings       = "settings"
	ActionDomainAdd      = "domain-add"
	ActionDomainRemove   = "domain-remove"
	ActionFormAdd        = "form-add"
	ActionFormUpdate     = "form-update"
	ActionFormRemove     = "form-remove"
	ActionFormResend     = "form-resend"
	ActionContainer      = "container"
	ActionOpenUpload     = "open-upload"
	ActionRename         = "rename"
	ActionRotatePassword = "rotate-password"

	ActionSignup     = "signup"
	ActionSignin     = "signin"
	ActionTokenMint  = "token-mint"
	ActionSiteCreate = "site-create"
	ActionSiteDelete = "site-delete"
)

// Surfaces: where a change came in.
const (
	SurfaceUI          = "ui" // Sitebin's own pages: a browser-shaped request (a label, not a check)
	SurfaceAPI         = "api"
	SurfaceMCP         = "mcp"
	SurfaceUploadToken = "upload-token"
	SurfaceWebDAV      = "webdav"
	SurfaceFTP         = "ftp"
	SurfaceDashboard   = "dashboard"
	SurfaceOIDC        = "oidc"  // sign-in through the configured identity provider
	SurfaceLocal       = "local" // email + password sign-in
)

// Credentials: what authorised a change.
const (
	AuthNone        = "none"
	AuthPassword    = "password"
	AuthToken       = "token"
	AuthOAuth       = "oauth"
	AuthSession     = "session"
	AuthUploadToken = "upload-token"
)

// Entry is one event. The JSON names are short because a log is read in bulk.
type Entry struct {
	Time time.Time `json:"t"`
	// Last and N describe a merged burst: the latest time and how many events
	// the line stands for. Both are absent on a single event.
	Last *time.Time `json:"last,omitempty"`
	N    int        `json:"n,omitempty"`

	Action  string `json:"action"`
	Surface string `json:"surface,omitempty"`
	Auth    string `json:"auth,omitempty"`
	// Account is the acting account; empty for an anonymous caller and for
	// one that authenticated with a site's edit password.
	Account string `json:"account,omitempty"`
	IP      string `json:"ip,omitempty"`
	UA      string `json:"ua,omitempty"`
	// Site is the site an account-log entry is about.
	Site   string `json:"site,omitempty"`
	Files  int    `json:"files,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Times is how many events the entry stands for.
func (e Entry) Times() int {
	if e.N > 1 {
		return e.N
	}
	return 1
}

// Latest is the entry's most recent time.
func (e Entry) Latest() time.Time {
	if e.Last != nil {
		return *e.Last
	}
	return e.Time
}

// origin reports whether an action starts a subject's history. It is kept
// past the cap and never merged.
func origin(action string) bool { return action == ActionCreate || action == ActionSignup }

// standalone actions are never merged: "minted twice" is a different fact
// from "minted once".
func standalone(action string) bool {
	switch action {
	case ActionCreate, ActionSignup, ActionTokenMint, ActionSiteCreate, ActionSiteDelete, ActionOpenUpload, ActionRotatePassword:
		return true
	}
	return false
}

func mergeable(last, e Entry) bool {
	if standalone(e.Action) || last.Action != e.Action || last.Surface != e.Surface || last.Auth != e.Auth ||
		last.Account != e.Account || last.IP != e.IP || last.UA != e.UA || last.Site != e.Site {
		return false
	}
	gap := e.Time.Sub(last.Latest())
	return gap >= 0 && gap <= MergeWindow
}

// Record adds e to the log at path, merging it into the last line when it
// continues a burst and dropping the oldest lines past MaxEntries. The file
// is rewritten atomically, so a crash leaves the old log or the new one. The
// folder must exist: a record never creates the subject it describes.
func Record(path string, e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	e.Time = e.Time.UTC()
	e.UA = CleanUA(e.UA)
	e.Detail = CleanDetail(e.Detail)
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return err
	}
	entries, err := Read(path)
	if err != nil {
		return err
	}
	if n := len(entries); n > 0 && mergeable(entries[n-1], e) {
		last := &entries[n-1]
		last.N = last.Times() + 1
		last.Files += e.Files
		t := e.Time
		last.Last = &t
		last.Detail = mergeDetail(last.Detail, e.Detail)
	} else {
		entries = append(entries, e)
	}
	return write(path, capEntries(entries))
}

// capEntries keeps the newest MaxEntries lines, and an origin line in first
// place with them.
func capEntries(es []Entry) []Entry {
	if len(es) <= MaxEntries {
		return es
	}
	if origin(es[0].Action) {
		return append([]Entry{es[0]}, es[len(es)-MaxEntries+1:]...)
	}
	return es[len(es)-MaxEntries:]
}

func mergeDetail(a, b string) string {
	if b == "" || a == b || strings.Contains(a, b) {
		return a
	}
	if a == "" {
		return b
	}
	return CleanDetail(a + ", " + b)
}

// Read returns the log's entries, oldest first. A missing log is empty, not
// an error; a damaged line is skipped rather than costing the whole log.
func Read(path string) ([]Entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Entry
		if json.Unmarshal(line, &e) == nil && e.Action != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

// Purge drops the entries whose latest time is before `before`, removes a log
// left empty, and reports how many went.
func Purge(path string, before time.Time) (int, error) {
	entries, err := Read(path)
	if err != nil || len(entries) == 0 {
		return 0, err
	}
	kept := entries[:0:0]
	for _, e := range entries {
		if !e.Latest().Before(before) {
			kept = append(kept, e)
		}
	}
	gone := len(entries) - len(kept)
	if gone == 0 {
		return 0, nil
	}
	if len(kept) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		return gone, nil
	}
	return gone, write(path, kept)
}

func write(path string, es []Entry) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, e := range es {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// CleanUA makes a client-supplied user agent safe to store and show: valid
// UTF-8, no control characters, at most MaxUA bytes.
func CleanUA(s string) string { return clean(s, MaxUA) }

// CleanDetail is CleanUA's rule for the detail line, at MaxDetail bytes.
func CleanDetail(s string) string { return clean(s, MaxDetail) }

func clean(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError || r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > max {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// Match is an address query: one address, or a CIDR range.
type Match struct{ prefix netip.Prefix }

var errNotAnAddress = errors.New("not an address or a CIDR range")

// ParseMatch reads q as an IP address or a CIDR range. ok=false means q is
// neither — an email, a site id — and the caller should search it as text.
func ParseMatch(q string) (Match, bool) {
	m, err := parseMatch(strings.TrimSpace(q))
	return m, err == nil
}

func parseMatch(q string) (Match, error) {
	if q == "" {
		return Match{}, errNotAnAddress
	}
	if strings.Contains(q, "/") {
		p, err := netip.ParsePrefix(q)
		if err != nil {
			return Match{}, err
		}
		if p.Addr().Is4In6() {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		return Match{p.Masked()}, nil
	}
	a, err := netip.ParseAddr(q)
	if err != nil {
		return Match{}, err
	}
	a = a.Unmap()
	return Match{netip.PrefixFrom(a, a.BitLen())}, nil
}

// String is the query in canonical form.
func (m Match) String() string {
	if !m.prefix.IsValid() {
		return ""
	}
	if m.prefix.IsSingleIP() {
		return m.prefix.Addr().String()
	}
	return m.prefix.String()
}

// Matches reports whether ip lies in the query.
func (m Match) Matches(ip string) bool {
	if !m.prefix.IsValid() || ip == "" {
		return false
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	return m.prefix.Contains(a.Unmap())
}

// Filter returns the entries whose address matches.
func (m Match) Filter(es []Entry) []Entry {
	var out []Entry
	for _, e := range es {
		if m.Matches(e.IP) {
			out = append(out, e)
		}
	}
	return out
}
