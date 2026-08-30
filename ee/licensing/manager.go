//go:build ee

package licensing

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultRefresh is how often a running instance re-collects its licence from
// the stack. Daily is plenty: a licence carries dates, so the state it implies
// changes on its own; the fetch only exists so a RENEWED licence reaches a
// running container without a restart or a pasted email.
const DefaultRefresh = 24 * time.Hour

// Fetcher collects the current licence for this instance from the stack. It is
// an interface so the HTTP call is swappable and testable, and because the
// stack endpoint is the one part of this that is not verified offline.
type Fetcher interface {
	// Fetch returns the current licence key, or ok=false when the stack has
	// none for this instance. An error means "we do not know", never "expired".
	Fetch(ctx context.Context) (key string, ok bool, err error)
}

// Manager holds the instance's licence: it loads the key, keeps the trial
// marker, refreshes from the stack and hands out the derived Status. It never
// fails startup and never restricts anything on a failure.
type Manager struct {
	mu    sync.RWMutex
	snap  Snapshot
	roots []ed25519.PublicKey
	appID string
	dir   string
	now   func() time.Time

	refresh  time.Duration
	stopOnce sync.Once
	stopc    chan struct{}
	// fetched is closed after the first refresh attempt completes; tests wait
	// on it instead of sleeping.
	fetched chan struct{}
	once    sync.Once
}

// NewManager builds a manager storing its trial marker and licence cache under
// dataDir. now may be nil, meaning time.Now.
func NewManager(dataDir string, roots []ed25519.PublicKey, appID string, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{
		roots:   roots,
		appID:   appID,
		dir:     filepath.Join(dataDir, "license"),
		now:     now,
		refresh: DefaultRefresh,
		stopc:   make(chan struct{}),
		fetched: make(chan struct{}),
	}
}

const (
	trialFile = "trial-start"
	cacheFile = "license.key"
)

// Load reads the trial marker and resolves a key, without touching the network.
// envKey (SITEBIN_LICENSE_KEY) WINS when set — that is what makes an air-gapped
// install work — and suppresses the cached copy entirely.
//
// It cannot fail: every problem it meets resolves to the unlicensed or unknown
// state and is logged.
func (m *Manager) Load(envKey string) Status {
	trial := m.trialStart()
	snap := Snapshot{Loaded: true, TrialStart: trial}

	key, source := strings.TrimSpace(envKey), "env"
	if key == "" {
		if cached, err := m.readCache(); err != nil {
			slog.Warn("license: could not read the cached license", "err", err)
			key, source = "", ""
		} else {
			key, source = cached, "cache"
		}
	}
	if key == "" {
		source = ""
	} else {
		snap.Source, snap.raw = source, key
		lic, err := Verify(key, m.roots, m.appID, m.now())
		if err != nil {
			// Reported, never enforced on: an unverifiable key is "none".
			snap.Err = err
		} else {
			snap.License = &lic
		}
	}
	m.mu.Lock()
	m.snap = snap
	m.mu.Unlock()
	return snap.StatusAt(m.now())
}

// Status is the derived state right now. Callers must read it per use rather
// than caching it: the state moves with the clock.
func (m *Manager) Status() Status {
	m.mu.RLock()
	snap := m.snap
	m.mu.RUnlock()
	return snap.StatusAt(m.now())
}

// Start begins periodic collection of the licence from the stack and applies
// what it gets WITHOUT a restart. It returns immediately; the first fetch runs
// on its own goroutine, so a stack that is down or slow cannot delay startup.
//
// It is a no-op when there is no fetcher, and when SITEBIN_LICENSE_KEY supplied
// the key — an operator who pasted a key has said which one to use, and an
// air-gapped install has nothing to ask.
func (m *Manager) Start(f Fetcher) {
	if f == nil {
		return
	}
	m.mu.RLock()
	fromEnv := m.snap.Source == "env"
	m.mu.RUnlock()
	if fromEnv {
		slog.Info("license: SITEBIN_LICENSE_KEY is set; not collecting from the stack")
		return
	}
	go func() {
		m.refreshOnce(f)
		t := time.NewTicker(m.refresh)
		defer t.Stop()
		for {
			select {
			case <-m.stopc:
				return
			case <-t.C:
				m.refreshOnce(f)
			}
		}
	}()
}

// Stop ends the refresh loop. Used by tests; the running server keeps it for
// the process lifetime.
func (m *Manager) Stop() { m.stopOnce.Do(func() { close(m.stopc) }) }

// Waitc is closed once the first refresh attempt has finished. Test helper.
func (m *Manager) Waitc() <-chan struct{} { return m.fetched }

// refreshOnce collects a licence and applies it if — and only if — it verifies.
// A fetch that fails, returns nothing, or returns a key that does not check out
// leaves the current licence exactly where it is. Nothing here can restrict.
func (m *Manager) refreshOnce(f Fetcher) {
	defer m.once.Do(func() { close(m.fetched) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key, ok, err := f.Fetch(ctx)
	if err != nil {
		slog.Warn("license: could not collect the license from the stack; keeping the current one", "err", err)
		return
	}
	if !ok || strings.TrimSpace(key) == "" {
		return
	}
	key = strings.TrimSpace(key)

	m.mu.RLock()
	same := m.snap.raw == key
	m.mu.RUnlock()
	if same {
		return
	}

	lic, verr := Verify(key, m.roots, m.appID, m.now())
	if verr != nil {
		slog.Warn("license: the stack returned a license this build cannot verify; keeping the current one", "err", verr)
		return
	}
	if err := m.writeCache(key); err != nil {
		// A cache we could not write costs a fetch on the next start, nothing
		// more. The licence still applies to the running process.
		slog.Warn("license: could not cache the license", "err", err)
	}
	m.mu.Lock()
	m.snap.License = &lic
	m.snap.Err = nil
	m.snap.Source = "stack"
	m.snap.raw = key
	m.mu.Unlock()
	slog.Info("license: applied a license collected from the stack",
		m.Status().LogArgs()...)
}

// ---- on-disk state ----

// trialStart returns when this instance first ran, writing the marker on the
// first start. A zero return means "we could not tell", which keeps the trial
// UNKNOWN rather than elapsed — nothing is restricted on it.
func (m *Manager) trialStart() time.Time {
	path := filepath.Join(m.dir, trialFile)
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		t, perr := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
		if perr != nil {
			slog.Warn("license: trial marker is unreadable; treating the trial as unknown", "path", path, "err", perr)
			return time.Time{}
		}
		return t
	case !errors.Is(err, fs.ErrNotExist):
		slog.Warn("license: could not read the trial marker; treating the trial as unknown", "path", path, "err", err)
		return time.Time{}
	}
	now := m.now()
	if err := m.write(trialFile, []byte(now.Format(time.RFC3339))); err != nil {
		slog.Warn("license: could not write the trial marker; treating the trial as unknown", "path", path, "err", err)
		return time.Time{}
	}
	return now
}

func (m *Manager) readCache() (string, error) {
	b, err := os.ReadFile(filepath.Join(m.dir, cacheFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (m *Manager) writeCache(key string) error { return m.write(cacheFile, []byte(key)) }

func (m *Manager) write(name string, data []byte) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(m.dir, name+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(m.dir, name))
}
