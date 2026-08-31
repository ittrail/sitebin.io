//go:build ee

package licensing

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type stubFetcher struct {
	key  string
	ok   bool
	err  error
	hits int
	// sawCurrent records the licence the manager offered as the credential,
	// so a test can assert the renewal actually presents what it holds.
	sawCurrent string
}

func (f *stubFetcher) Fetch(_ context.Context, current string) (string, bool, error) {
	f.hits++
	f.sawCurrent = current
	return f.key, f.ok, f.err
}

func newTestManager(t *testing.T, c *chain, now time.Time) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m := NewManager(dir, c.roots(), AppID, func() time.Time { return now })
	t.Cleanup(m.Stop)
	return m, dir
}

func TestManagerLoadsEnvKey(t *testing.T) {
	c := newChain(t)
	m, dir := newTestManager(t, c, refTime)
	st := m.Load(c.key(t))
	if st.State != StateLicensed || st.Source != "env" {
		t.Fatalf("state=%s source=%s, want licensed/env", st.State, st.Source)
	}
	// The env key is never cached: it is supplied on every start by definition.
	if _, err := os.Stat(filepath.Join(dir, "license", cacheFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the env key was written to the cache")
	}
}

// SITEBIN_LICENSE_KEY wins over the cache, which is what makes an air-gapped
// install work.
func TestManagerEnvKeyWinsOverCache(t *testing.T) {
	c := newChain(t)
	m, dir := newTestManager(t, c, refTime)

	cached := Key(c.cert(t, AppID, refTime.Add(365*24*time.Hour)),
		c.lic(t, AppID, refTime.Add(24*time.Hour), time.Time{}))
	if err := os.MkdirAll(filepath.Join(dir, "license"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "license", cacheFile), []byte(cached), 0o600); err != nil {
		t.Fatal(err)
	}

	envKey := Key(c.cert(t, AppID, refTime.Add(365*24*time.Hour)),
		c.lic(t, AppID, refTime.Add(500*24*time.Hour), time.Time{}))
	st := m.Load(envKey)
	if st.Source != "env" || !st.ExpiresAt.Equal(refTime.Add(500*24*time.Hour)) {
		t.Fatalf("the cached key won: source=%s expires=%s", st.Source, st.ExpiresAt)
	}

	// With no env key the cached one is used.
	m2, _ := newTestManager(t, c, refTime)
	m2.dir = filepath.Join(dir, "license")
	st = m2.Load("")
	if st.Source != "cache" || !st.ExpiresAt.Equal(refTime.Add(24*time.Hour)) {
		t.Fatalf("the cache was not used: source=%s expires=%s", st.Source, st.ExpiresAt)
	}
}

// The trial marker is written on the first start and never moved afterwards —
// otherwise a restart would reset the 90 days.
func TestTrialMarkerIsWrittenOnceAndKept(t *testing.T) {
	c := newChain(t)
	dir := t.TempDir()
	first := NewManager(dir, c.roots(), AppID, func() time.Time { return refTime })
	st := first.Load("")
	if st.State != StateNone || !st.TrialEndsAt.Equal(refTime.Add(TrialPeriod)) {
		t.Fatalf("first start: %s trial ends %s", st.State, st.TrialEndsAt)
	}

	later := refTime.Add(30 * 24 * time.Hour)
	second := NewManager(dir, c.roots(), AppID, func() time.Time { return later })
	st = second.Load("")
	if !st.TrialEndsAt.Equal(refTime.Add(TrialPeriod)) {
		t.Fatalf("a restart moved the trial end to %s", st.TrialEndsAt)
	}
	if st.Restricted {
		t.Error("restricted inside the trial")
	}

	after := refTime.Add(TrialPeriod + time.Hour)
	third := NewManager(dir, c.roots(), AppID, func() time.Time { return after })
	if st := third.Load(""); !st.Restricted || st.State != StateNone {
		t.Fatalf("past the trial: %s restricted=%v", st.State, st.Restricted)
	}
}

// A build with no roots verifies nothing, which is "none" — never "expired",
// and never a refusal to start.
func TestNoRootsIsNone(t *testing.T) {
	c := newChain(t)
	m := NewManager(t.TempDir(), nil, AppID, func() time.Time { return refTime })
	t.Cleanup(m.Stop)
	st := m.Load(c.key(t))
	if st.State != StateNone {
		t.Fatalf("state = %s, want none", st.State)
	}
	if !errors.Is(st.Err, ErrNoRoots) {
		t.Errorf("err = %v", st.Err)
	}
}

// The point of the fetch: a renewed licence reaches a RUNNING instance and is
// applied without a restart.
func TestRefreshAppliesANewLicenseWithoutRestart(t *testing.T) {
	c := newChain(t)
	m, dir := newTestManager(t, c, refTime)
	if st := m.Load(""); st.State != StateNone {
		t.Fatalf("expected an unlicensed start, got %s", st.State)
	}

	renewed := Key(c.cert(t, AppID, refTime.Add(365*24*time.Hour)),
		c.lic(t, AppID, refTime.Add(300*24*time.Hour), time.Time{}))
	m.Start(&stubFetcher{key: renewed, ok: true})
	<-m.Waitc()

	st := m.Status()
	if st.State != StateLicensed || st.Source != "stack" {
		t.Fatalf("state=%s source=%s, want licensed/stack", st.State, st.Source)
	}
	// And it is cached, so the next start does not need the stack at all.
	b, err := os.ReadFile(filepath.Join(dir, "license", cacheFile))
	if err != nil || string(b) != renewed {
		t.Fatalf("the licence was not cached: %v", err)
	}
}

// A fetch that fails, or returns a key this build cannot verify, must leave the
// current licence exactly where it is. It can never restrict.
func TestRefreshNeverRestrictsOnFailure(t *testing.T) {
	c := newChain(t)
	good := c.key(t)

	for name, f := range map[string]*stubFetcher{
		"network error": {err: errors.New("stack unreachable")},
		"no licence":    {ok: false},
		"unverifiable":  {key: "not.a.valid.key", ok: true},
	} {
		m, _ := newTestManager(t, c, refTime)
		if st := m.Load(good); st.State != StateLicensed {
			t.Fatalf("%s: setup: %s", name, st.State)
		}
		// Start() skips an env-sourced licence, so drive the refresh directly.
		m.refreshOnce(f)
		if st := m.Status(); st.State != StateLicensed || st.Restricted {
			t.Errorf("%s: a bad fetch changed the state to %s (restricted=%v)", name, st.State, st.Restricted)
		}
	}
}

// An operator who pasted a key has said which one to use; nothing collects over
// the top of it.
func TestEnvKeySuppressesFetching(t *testing.T) {
	c := newChain(t)
	m, _ := newTestManager(t, c, refTime)
	m.Load(c.key(t))
	f := &stubFetcher{key: c.key(t), ok: true}
	m.Start(f)
	select {
	case <-m.Waitc():
		t.Fatal("a fetch ran while SITEBIN_LICENSE_KEY was set")
	case <-time.After(50 * time.Millisecond):
	}
	if f.hits != 0 {
		t.Errorf("fetcher was called %d times", f.hits)
	}
}

// A key with a typo in it costs the instance twice: it falls to the unlicensed
// trial arm AND stops collecting renewals forever, because Source is stamped
// "env" before the key is verified. That combination is the likeliest way a
// paying customer ends up restricted, so it has to be loud.
func TestUnverifiableEnvKeySaysItAlsoStopsCollecting(t *testing.T) {
	c := newChain(t)
	m, _ := newTestManager(t, c, refTime)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	st := m.Load("not.a.license.key")
	if st.State != StateNone {
		t.Fatalf("an unverifiable key must be %q, not %q", StateNone, st.State)
	}

	f := &stubFetcher{key: c.key(t), ok: true}
	m.Start(f)
	select {
	case <-m.Waitc():
		t.Fatal("a fetch ran while SITEBIN_LICENSE_KEY was set")
	case <-time.After(50 * time.Millisecond):
	}
	if f.hits != 0 {
		t.Errorf("fetcher was called %d times", f.hits)
	}

	log := buf.String()
	if !strings.Contains(log, "level=ERROR") {
		t.Errorf("a bad key that also disables collection must be logged at ERROR, got:\n%s", log)
	}
	// Both halves of the trap have to be in the one message; either alone reads
	// as a smaller problem than it is.
	for _, want := range []string{"SITEBIN_LICENSE_KEY", "UNLICENSED", "NOT collect"} {
		if !strings.Contains(log, want) {
			t.Errorf("the startup log never mentions %q:\n%s", want, log)
		}
	}
}
