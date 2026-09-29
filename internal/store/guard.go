package store

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/abuse"
)

// The abuse guard: every file a site receives is scanned for kit signatures
// before it becomes visible, and a kit on an untrusted site locks the site
// first. The rules are internal/abuse's; this file is the policy and the
// places it is applied. See docs/superpowers/specs/2026-09-29-abuse-detection.md.
//
// Every write path funnels into one of three places, and each applies the
// verdict to meta.json BEFORE the file is renamed into the content root,
// under the site lock — authz reads the lock from meta.json, so a held file
// is never served, not even between two syscalls:
//
//   - writeFileIn (SaveFile, ExtractZip: the API, MCP, upload tokens, zips),
//   - Replacement.Commit (staged replaces),
//   - StagedFile.Close and RenameChecked (WebDAV and FTP).

// LockByScanner marks a lock the abuse guard placed: an upload matched a
// blocking rule, or a CSP report proved an exfiltration attempt. Like an
// account lock it is placed only on an unlocked site and never replaces one.
const LockByScanner = "scanner"

// ErrHeld reports that a write matched a blocking abuse rule and the site is
// now locked for review. The file is kept, as evidence, in a site nobody is
// served. Surfaces answer it like any lock (403), with HeldError's message.
var ErrHeld = errors.New("the site is held for review")

// HeldError is ErrHeld with the lock the guard placed.
type HeldError struct{ Lock *SiteLock }

func (e *HeldError) Error() string        { return LockedMessage(e.Lock) }
func (e *HeldError) Is(target error) bool { return target == ErrHeld }

// Where a finding came from.
const (
	FindingUpload = "upload"
	FindingCSP    = "csp"
	FindingScan   = "scan"
)

const (
	// maxFindings bounds what meta.json keeps: the newest, one per rule and
	// file. A kit is found by its first few hits; a hundred more add nothing.
	maxFindings = 20
	// maxReviewed bounds the reviewed fingerprints, oldest dropped first.
	maxReviewed = 200
)

// AbuseState is what the guard found in a site and what the operator has
// since cleared. Nil on every site that never had a hit.
type AbuseState struct {
	// Findings are the hits not yet reviewed, newest last.
	Findings []Finding `json:"findings,omitempty"`
	// Reviewed are the SHA-256 fingerprints of file contents the operator
	// released (by unlocking the site or dismissing its findings). The same
	// bytes are never recorded or held again; any other content is scanned
	// as usual.
	Reviewed []string `json:"reviewed,omitempty"`
}

// Finding is one rule that matched one file.
type Finding struct {
	Rule     string    `json:"rule"`
	Severity string    `json:"severity"`
	Path     string    `json:"path"`
	Excerpt  string    `json:"excerpt,omitempty"`
	SHA256   string    `json:"sha256"`
	Source   string    `json:"source"`
	At       time.Time `json:"at"`
}

// Decisions, as logged and reported to the hook.
const (
	DecisionHeld       = "held"
	DecisionFlagged    = "flagged"
	DecisionTrusted    = "exempt: trusted"
	DecisionContainer  = "exempt: container"
	DecisionOperator   = "exempt: operator"
	DecisionLocked     = "already locked"
	DecisionReviewed   = "reviewed"
	DecisionUnverified = "unverified"
)

// ScanEvent is one decision of the guard, handed to the scan hook (alerts)
// after it has been recorded.
type ScanEvent struct {
	ViewID string
	Owner  string
	// Source is FindingUpload, FindingCSP or FindingScan.
	Source string
	// Paths are the files the decision is about.
	Paths []string
	// Blocked is the URL a CSP report named (FindingCSP only).
	Blocked string
	// Findings are what was recorded; empty for a reviewed or unverified
	// event.
	Findings []Finding
	Decision string
	// Lock is the lock on the site after the decision, if any.
	Lock *SiteLock
}

// Held reports whether this decision locked the site.
func (e ScanEvent) Held() bool { return e.Decision == DecisionHeld }

// SetScanner replaces the rule source; New starts with the rules file in the
// data directory. Nil switches scanning off (tests only).
func (s *Store) SetScanner(l *abuse.Loader) { s.scanner = l }

// SetScanHook installs the function every guard decision is reported to. It
// is called with the site lock held, so it must not take that lock again —
// it should record, log and hand anything slow (a mail) to a goroutine.
func (s *Store) SetScanHook(fn func(ScanEvent)) { s.scanHook = fn }

func (s *Store) rules() *abuse.RuleSet {
	if s.scanner == nil {
		return nil
	}
	return s.scanner.Rules()
}

// newScan starts a scan of one file, or returns nil when there is nothing to
// scan it for.
func (s *Store) newScan(rel string) *abuse.Scan {
	rs := s.rules()
	if rs == nil {
		return nil
	}
	return rs.NewScan(rel)
}

func (s *Store) emit(ev ScanEvent) {
	if ev.Decision == "" || s.scanHook == nil {
		return
	}
	s.scanHook(ev)
}

// judge decides what scan results mean for a site and records it in meta,
// which the caller read under the site lock and writes back when changed is
// true. blocked is the CSP report's URL for a tripwire check, "" otherwise.
//
// The policy: a file whose fingerprint was reviewed is ignored; every other
// hit is recorded as a finding (a block hit outside an active file as a
// flag); and the site is held — locked By scanner — when a block finding
// exists and the site is lockable: unlocked, untrusted, not a container
// site, not the operator's.
func (s *Store) judge(site *Site, meta *Meta, results []abuse.Result, source, blocked string) (ev ScanEvent, changed bool) {
	ev = ScanEvent{ViewID: site.ViewID, Owner: meta.OwnerAccountID, Source: source, Blocked: blocked}
	now := time.Now().UTC()
	var found []Finding
	reviewed := 0
	for _, r := range results {
		if len(r.Hits) == 0 {
			continue
		}
		ev.Paths = append(ev.Paths, r.Path)
		if meta.Abuse != nil && r.SHA256 != "" && slices.Contains(meta.Abuse.Reviewed, r.SHA256) {
			reviewed++
			continue
		}
		for _, h := range r.Hits {
			sev := h.Severity
			if sev == abuse.Block && !r.Active {
				sev = abuse.Flag
			}
			ex := h.Excerpt
			if blocked != "" {
				ex = abuse.Clean(blocked)
			}
			found = append(found, Finding{Rule: h.Rule, Severity: string(sev), Path: r.Path, Excerpt: ex, SHA256: r.SHA256, Source: source, At: now})
		}
	}
	if len(ev.Paths) == 0 {
		return ScanEvent{}, false
	}
	if len(found) == 0 {
		ev.Decision = DecisionReviewed
		ev.Lock = meta.Locked
		s.logDecision(ev)
		return ev, false
	}
	ev.Findings = found
	var first *Finding
	for i := range found {
		if found[i].Severity == string(abuse.Block) {
			first = &found[i]
			break
		}
	}
	switch {
	case first == nil:
		ev.Decision = DecisionFlagged
	case meta.Locked != nil:
		ev.Decision = DecisionLocked
	case s.Trusted(site):
		ev.Decision = DecisionTrusted
	case meta.Mode == ModeContainer:
		ev.Decision = DecisionContainer
	case meta.OwnerAccountID != "" && s.isOperator != nil && s.isOperator(meta.OwnerAccountID):
		ev.Decision = DecisionOperator
	default:
		ev.Decision = DecisionHeld
		reason := "held for review: " + first.Rule + " in " + first.Path
		if blocked != "" {
			reason = "held for review: a CSP report blocked " + abuse.Clean(blocked) + " (" + first.Rule + ")"
		}
		meta.Locked = &SiteLock{At: now, By: LockByScanner, Reason: CleanLockReason(reason)}
	}
	if meta.Abuse == nil {
		meta.Abuse = &AbuseState{}
	}
	meta.Abuse.Findings = mergeFindings(meta.Abuse.Findings, found)
	ev.Lock = meta.Locked
	s.logDecision(ev)
	return ev, true
}

func (s *Store) logDecision(ev ScanEvent) {
	var rules []string
	for _, f := range ev.Findings {
		if !slices.Contains(rules, f.Rule) {
			rules = append(rules, f.Rule)
		}
	}
	slog.Info("abuse guard", "site", ev.ViewID, "owner", ev.Owner, "source", ev.Source,
		"paths", strings.Join(ev.Paths, ","), "rules", strings.Join(rules, ","),
		"blocked", ev.Blocked, "decision", ev.Decision)
}

// mergeFindings adds found to have, one finding per rule and file (the newer
// wins), keeping the newest maxFindings.
func mergeFindings(have, found []Finding) []Finding {
	out := make([]Finding, 0, len(have)+len(found))
	for _, h := range have {
		if !slices.ContainsFunc(found, func(f Finding) bool { return f.Rule == h.Rule && f.Path == h.Path }) {
			out = append(out, h)
		}
	}
	out = append(out, found...)
	if len(out) > maxFindings {
		out = out[len(out)-maxFindings:]
	}
	return out
}

// reviewMeta moves every finding's fingerprint into the reviewed set and
// clears the findings: the operator has looked, and these bytes are fine.
func reviewMeta(m *Meta) bool {
	if m.Abuse == nil || len(m.Abuse.Findings) == 0 {
		return false
	}
	for _, f := range m.Abuse.Findings {
		if f.SHA256 != "" && !slices.Contains(m.Abuse.Reviewed, f.SHA256) {
			m.Abuse.Reviewed = append(m.Abuse.Reviewed, f.SHA256)
		}
	}
	if len(m.Abuse.Reviewed) > maxReviewed {
		m.Abuse.Reviewed = m.Abuse.Reviewed[len(m.Abuse.Reviewed)-maxReviewed:]
	}
	m.Abuse.Findings = nil
	return true
}

// ClearFindings is the operator dismissing a site's findings without
// unlocking it: their fingerprints become reviewed. It reports whether there
// was anything to clear.
func (s *Store) ClearFindings(site *Site) (cleared bool, err error) {
	err = s.Update(site, func(m *Meta) error {
		cleared = reviewMeta(m)
		return nil
	})
	return cleared, err
}

// settleLocked applies the verdict on results to the site's meta.json. The
// caller holds the site lock. It returns the event (for emit) and, when the
// site was held by this verdict, a *HeldError.
func (s *Store) settleLocked(site *Site, results []abuse.Result, source, blocked string) (ScanEvent, error) {
	meta, err := readMeta(site.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return ScanEvent{}, ErrNotFound
		}
		return ScanEvent{}, err
	}
	ev, changed := s.judge(site, &meta, results, source, blocked)
	if changed {
		meta.UpdatedAt = time.Now().UTC()
		if err := writeMeta(site.dir, meta); err != nil {
			return ScanEvent{}, err
		}
		site.Meta = meta
	}
	if ev.Held() {
		return ev, &HeldError{Lock: meta.Locked}
	}
	return ev, nil
}

// liveGuard settles each file of a live write as it completes, before it
// is renamed into place. The caller holds the site lock.
type liveGuard struct {
	s    *Store
	site *Site
}

func (g liveGuard) scan(rel string) *abuse.Scan { return g.s.newScan(rel) }

func (g liveGuard) settle(res abuse.Result) error {
	if len(res.Hits) == 0 {
		return nil
	}
	ev, err := g.s.settleLocked(g.site, []abuse.Result{res}, FindingUpload, "")
	g.s.emit(ev)
	return err
}

// ---- scanning what is already on disk: the tripwire and the CLI ----

const (
	// A tripwire check reads at most this much of one site. It runs on an
	// unauthenticated report, so its cost is bounded whatever the site holds.
	exfilMaxFiles = 5000
	exfilMaxBytes = 64 << 20
)

// scanContent scans the site's content files with rs, up to the given
// bounds (0 = none), and returns the results that have hits. It reads
// through the content root, like every other file surface.
func (s *Store) scanContent(site *Site, rs *abuse.RuleSet, maxFiles int, maxBytes int64) ([]abuse.Result, error) {
	dir := site.ContentDir()
	root, err := openContent(site)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var out []abuse.Result
	files := 0
	var total int64
	errStop := errors.New("stop")
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() || d.Name() == SPAMarker || d.Name() == TrustedMarker {
			return nil
		}
		if maxFiles > 0 && files >= maxFiles || maxBytes > 0 && total >= maxBytes {
			return errStop
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		sc := rs.NewScan(slash)
		if sc == nil {
			return nil
		}
		files++
		f, err := root.Open(rel)
		if err != nil {
			return nil // vanished or swapped since the walk saw it
		}
		defer f.Close()
		if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		n, _ := feed(sc, f)
		total += n
		if r := sc.Result(); len(r.Hits) > 0 {
			out = append(out, r)
		}
		return nil
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return out, err
}

// feed copies r into sc, stopping early once the scan has decided the file
// is binary: an image is not read to its end to be ignored.
func feed(sc *abuse.Scan, r io.Reader) (int64, error) {
	buf := make([]byte, 64<<10)
	var n int64
	for {
		k, err := r.Read(buf)
		if k > 0 {
			sc.Write(buf[:k])
			n += int64(k)
			if sc.Skipped() {
				return n, nil
			}
		}
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// CheckExfil is the tripwire's verification. A CSP report named destination
// d as blocked on this site; reports are unauthenticated, so the site is
// only acted on when its own files reference the destination. Those files
// become findings (source csp) and, when d's action is lock and the site is
// lockable, the site is held. With no referencing file the event is
// DecisionUnverified and nothing is recorded.
func (s *Store) CheckExfil(site *Site, d abuse.Destination, blocked string) (ScanEvent, error) {
	results, err := s.scanContent(site, abuse.DestinationRules(d), exfilMaxFiles, exfilMaxBytes)
	if err != nil {
		return ScanEvent{}, err
	}
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()
	if len(results) == 0 {
		meta, err := readMeta(site.dir)
		if err != nil {
			return ScanEvent{}, err
		}
		ev := ScanEvent{ViewID: site.ViewID, Owner: meta.OwnerAccountID, Source: FindingCSP, Blocked: blocked,
			Decision: DecisionUnverified, Lock: meta.Locked}
		s.logDecision(ev)
		s.emit(ev)
		return ev, nil
	}
	ev, err := s.settleLocked(site, results, FindingCSP, blocked)
	if err != nil && !errors.Is(err, ErrHeld) {
		return ScanEvent{}, err
	}
	s.emit(ev)
	return ev, nil
}

// ScanSite scans every file of the site with the current rules and returns
// the results that have hits. It writes nothing.
func (s *Store) ScanSite(site *Site) ([]abuse.Result, error) {
	rs := s.rules()
	if rs == nil {
		return nil, nil
	}
	return s.scanContent(site, rs, 0, 0)
}

// ApplyScan records results from ScanSite the way an upload would have:
// findings, and a hold where the policy says so.
func (s *Store) ApplyScan(site *Site, results []abuse.Result) (ScanEvent, error) {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()
	ev, err := s.settleLocked(site, results, FindingScan, "")
	if err != nil && !errors.Is(err, ErrHeld) {
		return ScanEvent{}, err
	}
	s.emit(ev)
	return ev, nil
}
