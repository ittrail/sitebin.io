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

// ---- cluster binding ----

const (
	clusterA = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	clusterB = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// boundKey mints a key whose licence names clusterID ("" = unbound).
func boundKey(t *testing.T, c *chain, clusterID string) string {
	t.Helper()
	lic, err := SignLicense(LicensePayload{
		AppID:      AppID,
		Holder:     "ACME GmbH",
		Plan:       "team",
		IssuedAt:   refTime.Add(-24 * time.Hour),
		ExpiresAt:  refTime.Add(30 * 24 * time.Hour),
		GraceUntil: refTime.Add(120 * 24 * time.Hour),
		ClusterID:  clusterID,
	}, c.appPriv)
	if err != nil {
		t.Fatal(err)
	}
	return Key(c.cert(t, AppID, refTime.Add(365*24*time.Hour)), lic)
}

// cachedBoundManager caches a key bound to keyCluster and loads it on a manager
// whose own id is ownCluster.
func cachedBoundManager(t *testing.T, keyCluster, ownCluster string) Status {
	t.Helper()
	c := newChain(t)
	m, dir := newTestManager(t, c, refTime)
	if err := os.MkdirAll(filepath.Join(dir, "license"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "license", cacheFile), []byte(boundKey(t, c, keyCluster)), 0o600); err != nil {
		t.Fatal(err)
	}
	m.SetClusterID(ownCluster)
	return m.Load("")
}

func TestManagerKeyBoundToOwnCluster(t *testing.T) {
	if st := cachedBoundManager(t, clusterA, clusterA); st.State != StateLicensed {
		t.Fatalf("state = %s, want licensed", st.State)
	}
}

func TestManagerKeyBoundToOtherCluster(t *testing.T) {
	st := cachedBoundManager(t, clusterB, clusterA)
	if st.State != StateNone || !errors.Is(st.Err, ErrOtherCluster) || !st.BoundElsewhere {
		t.Fatalf("state=%s err=%v boundElsewhere=%v, want none/ErrOtherCluster/true", st.State, st.Err, st.BoundElsewhere)
	}
	if !strings.Contains(st.Notice(), "bound to another installation") {
		t.Errorf("notice = %q", st.Notice())
	}
}

func TestManagerUnboundKeyUnaffected(t *testing.T) {
	if st := cachedBoundManager(t, "", clusterA); st.State != StateLicensed || st.BoundElsewhere {
		t.Fatalf("state=%s boundElsewhere=%v, want licensed/false", st.State, st.BoundElsewhere)
	}
}

func TestManagerNoClusterIDBoundKey(t *testing.T) {
	st := cachedBoundManager(t, clusterA, "")
	if st.State != StateNone || !st.BoundElsewhere {
		t.Fatalf("state=%s boundElsewhere=%v, want none/true (never expired)", st.State, st.BoundElsewhere)
	}
}

// ---- the refusal marker ----

func markerPath(dir string) string { return filepath.Join(dir, "license", boundMarkerFile) }

// seqFetcher answers each call from a list, repeating the last step.
type seqFetcher struct {
	steps []stubFetcher
	n     int
}

func (f *seqFetcher) Fetch(_ context.Context, _ string) (string, bool, error) {
	s := f.steps[f.n]
	if f.n < len(f.steps)-1 {
		f.n++
	}
	return s.key, s.ok, s.err
}

func TestRefreshBoundElsewhereThenSameKeyClears(t *testing.T) {
	c := newChain(t)
	m, dir := newTestManager(t, c, refTime)
	m.SetClusterID(clusterA)
	key := boundKey(t, c, "")
	if err := os.MkdirAll(filepath.Join(dir, "license"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "license", cacheFile), []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := m.Load(""); st.State != StateLicensed {
		t.Fatalf("setup: %s", st.State)
	}

	f := &seqFetcher{steps: []stubFetcher{{err: ErrBoundElsewhere}, {key: key, ok: true}}}
	m.refreshOnce(f)
	st := m.Status()
	if !st.BoundElsewhere || st.State != StateNone {
		t.Fatalf("after refusal: state=%s boundElsewhere=%v, want none/true", st.State, st.BoundElsewhere)
	}
	b, err := os.ReadFile(markerPath(dir))
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	if _, perr := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); perr != nil {
		t.Errorf("marker is not RFC3339: %q", b)
	}

	// The stack now returns the SAME key: any success clears the marker.
	m.refreshOnce(f)
	if st := m.Status(); st.BoundElsewhere || st.State != StateLicensed {
		t.Fatalf("after same key: state=%s boundElsewhere=%v, want licensed/false", st.State, st.BoundElsewhere)
	}
	if _, err := os.Stat(markerPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("marker still exists: %v", err)
	}
}

func TestBoundElsewhereMarkerSurvivesRestart(t *testing.T) {
	c := newChain(t)
	m, dir := newTestManager(t, c, refTime)
	m.SetClusterID(clusterA)
	key := boundKey(t, c, "")
	m.refreshOnce(&seqFetcher{steps: []stubFetcher{{err: ErrBoundElsewhere}}})
	if _, err := os.Stat(markerPath(dir)); err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "license", cacheFile), []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}

	m2 := NewManager(dir, c.roots(), AppID, func() time.Time { return refTime })
	m2.SetClusterID(clusterA)
	st := m2.Load("")
	if !st.BoundElsewhere || st.State != StateNone {
		t.Fatalf("after restart: state=%s boundElsewhere=%v, want none/true", st.State, st.BoundElsewhere)
	}
}

// The marker must not claim "unlicensed" for a licence bound to the own
// cluster that stays licensed.
func TestMarkerWithOwnBoundKeyStaysLicensed(t *testing.T) {
	c := newChain(t)
	m, dir := newTestManager(t, c, refTime)
	m.SetClusterID(clusterA)
	if err := os.MkdirAll(filepath.Join(dir, "license"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "license", cacheFile), []byte(boundKey(t, c, clusterA)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath(dir), []byte(refTime.Format(time.RFC3339)), 0o600); err != nil {
		t.Fatal(err)
	}
	st := m.Load("")
	if st.State != StateLicensed || st.BoundElsewhere || st.Notice() != "" {
		t.Fatalf("state=%s boundElsewhere=%v notice=%q, want licensed/false/empty", st.State, st.BoundElsewhere, st.Notice())
	}
}

func writeMarker(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "license"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath(dir), []byte(refTime.Format(time.RFC3339)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// An operator's explicit key wins: a stale marker must not void it, because
// Start never renews an env key and nothing would clear the marker.
func TestMarkerIgnoredForEnvKey(t *testing.T) {
	c := newChain(t)
	m, dir := newTestManager(t, c, refTime)
	m.SetClusterID(clusterA)
	writeMarker(t, dir)
	st := m.Load(boundKey(t, c, ""))
	if st.State != StateLicensed || st.BoundElsewhere || st.Notice() != "" {
		t.Fatalf("state=%s boundElsewhere=%v notice=%q, want licensed/false/empty", st.State, st.BoundElsewhere, st.Notice())
	}
}

// BoundElsewhere is only reported when it voided a held licence or the key
// itself names another cluster.
func TestMarkerWithoutVerifiedLicenceIsNotBoundElsewhere(t *testing.T) {
	c := newChain(t)
	m, dir := newTestManager(t, c, refTime)
	writeMarker(t, dir)
	st := m.Load("")
	if st.BoundElsewhere || st.State != StateNone || st.Restricted || st.Notice() != "" {
		t.Fatalf("no licence: state=%s boundElsewhere=%v notice=%q", st.State, st.BoundElsewhere, st.Notice())
	}

	m2, dir2 := newTestManager(t, c, refTime)
	writeMarker(t, dir2)
	if err := os.WriteFile(filepath.Join(dir2, "license", cacheFile), []byte("not.a.valid.key"), 0o600); err != nil {
		t.Fatal(err)
	}
	st = m2.Load("")
	if st.BoundElsewhere || st.State != StateNone {
		t.Fatalf("malformed key: state=%s boundElsewhere=%v", st.State, st.BoundElsewhere)
	}
}

func TestNoticeWithoutOwnClusterIDOmitsInstallation(t *testing.T) {
	if n := (Status{BoundElsewhere: true}).Notice(); n == "" || strings.Contains(n, "(installation") {
		t.Errorf("notice = %q", n)
	}
	if n := (Status{BoundElsewhere: true, ClusterID: clusterA}).Notice(); !strings.Contains(n, "(installation aaaaaa…)") {
		t.Errorf("notice = %q", n)
	}
}

func TestRefreshOtherClusterKeyLogsSpecificWarning(t *testing.T) {
	c := newChain(t)
	m, _ := newTestManager(t, c, refTime)
	m.SetClusterID(clusterA)
	m.Load(boundKey(t, c, clusterA))
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	m.refreshOnce(&stubFetcher{key: boundKey(t, c, clusterB), ok: true})
	if !strings.Contains(buf.String(), "bound to another installation") || strings.Contains(buf.String(), "cannot verify") {
		t.Errorf("log = %s", buf.String())
	}
	if st := m.Status(); st.State != StateLicensed || st.Restricted {
		t.Errorf("current licence not kept: %s", st.State)
	}
}
