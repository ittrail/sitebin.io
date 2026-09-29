package abuse

import (
	"log/slog"
	"os"
	"sync"
	"time"
)

// checkInterval is how often, at most, the rules file is looked at. The
// check is a stat, made by whichever scan asks first after the interval: no
// goroutine, and an instance that receives no uploads does no work.
const checkInterval = 10 * time.Second

// Loader serves the instance's rule set: the built-in defaults merged with
// the rules file, reloaded when the file's modification time or size
// changes. A file that does not parse is logged and ignored — the last good
// rules stay in force — because a typo in a hot-reloaded file must not
// switch the scanner off or crash anything. A file that disappears means the
// defaults again.
type Loader struct {
	path string
	now  func() time.Time

	mu      sync.Mutex
	cur     *RuleSet
	checked time.Time
	present bool
	mtime   time.Time
	size    int64
}

// NewLoader serves the rules in the file at path, which need not exist. An
// empty path serves the defaults only.
func NewLoader(path string) *Loader {
	return &Loader{path: path, now: time.Now, cur: Defaults()}
}

// Path is the rules file the loader watches.
func (l *Loader) Path() string { return l.path }

// Rules returns the current rule set, re-reading the file if it changed.
func (l *Loader) Rules() *RuleSet {
	if l == nil {
		return Defaults()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path == "" {
		return l.cur
	}
	now := l.now()
	if !l.checked.IsZero() && now.Sub(l.checked) < checkInterval {
		return l.cur
	}
	l.checked = now
	fi, err := os.Stat(l.path)
	if err != nil {
		if l.present {
			slog.Info("abuse rules file removed; using the built-in rules", "path", l.path)
		}
		l.present, l.mtime, l.size = false, time.Time{}, 0
		l.cur = Defaults()
		return l.cur
	}
	if l.present && fi.ModTime().Equal(l.mtime) && fi.Size() == l.size {
		return l.cur
	}
	// Remember this version whatever it holds, so a broken file is reported
	// once, not every ten seconds until someone fixes it.
	l.present, l.mtime, l.size = true, fi.ModTime(), fi.Size()
	b, err := os.ReadFile(l.path)
	if err == nil {
		var rs *RuleSet
		if rs, err = Parse(b); err == nil {
			l.cur = rs
			slog.Info("abuse rules loaded", "path", l.path, "rules", rs.Rules(), "destinations", rs.Destinations())
			return l.cur
		}
	}
	slog.Error("abuse rules file rejected; the previous rules stay in force", "path", l.path, "err", err)
	return l.cur
}
