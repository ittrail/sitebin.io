package httpapi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

type stepClock struct{ t time.Time }

func (c *stepClock) now() time.Time          { return c.t }
func (c *stepClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newStepClock() *stepClock               { return &stepClock{t: time.Date(2026, 11, 14, 18, 29, 0, 0, time.UTC)} }
func attrMap(attrs []any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(attrs); i += 2 {
		m[attrs[i].(string)] = attrs[i+1]
	}
	return m
}

func nothingAt(host string) error {
	return fmt.Errorf("%w: nothing is attached at %s", store.ErrNotFound, host)
}

func TestAuthzReportListsCustomHosts(t *testing.T) {
	c := newStepClock()
	r := newAuthzReport(c.now)
	r.miss("A.Example:443", false, nothingAt("a.example"))
	r.miss("a.example", false, nothingAt("a.example"))
	r.miss("b.example", false, fmt.Errorf("%w: the index link of b.example leads to no site", store.ErrNotFound))
	r.miss("x1.sitebin.example", true, store.ErrNotFound)
	r.miss("x2.sitebin.example", true, store.ErrNotFound)
	c.advance(time.Minute)
	attrs, ok := r.take()
	if !ok {
		t.Fatal("a window with custom-domain misses must be reported")
	}
	m := attrMap(attrs)
	if m["custom"] != 3 || m["generated"] != 2 || m["others"] != 0 || m["lookup_errors"] != 0 {
		t.Errorf("counts = %v", m)
	}
	want := "a.example=2 (nothing is attached at a.example), b.example=1 (the index link of b.example leads to no site)"
	if m["hosts"] != want {
		t.Errorf("hosts = %q\nwant    %q", m["hosts"], want)
	}
	if m["since"] != "2026-11-14T18:29:00Z" || m["until"] != "2026-11-14T18:30:00Z" {
		t.Errorf("window = %v .. %v", m["since"], m["until"])
	}
	if strings.Contains(fmt.Sprint(m["hosts"]), "sitebin.example") {
		t.Error("generated addresses must be counted, never listed")
	}
	if _, ok := r.take(); ok {
		t.Error("take must start a new, empty window")
	}
}

func TestAuthzReportSilentForGeneratedOnly(t *testing.T) {
	r := newAuthzReport(newStepClock().now)
	for i := 0; i < 50; i++ {
		r.miss(fmt.Sprintf("bot%d.sitebin.example", i), true, store.ErrNotFound)
	}
	if attrs, ok := r.take(); ok {
		t.Fatalf("generated misses alone are bot noise and must not log: %v", attrs)
	}
	// ...and they do not leak into the next window's count.
	r.miss("a.example", false, nothingAt("a.example"))
	attrs, _ := r.take()
	if m := attrMap(attrs); m["generated"] != 0 {
		t.Errorf("generated = %v, want 0 after a silent window", m["generated"])
	}
}

func TestAuthzReportBoundsHosts(t *testing.T) {
	r := newAuthzReport(newStepClock().now)
	for i := 0; i < maxReportHosts+5; i++ {
		h := fmt.Sprintf("h%d.example", i)
		r.miss(h, false, nothingAt(h))
	}
	r.miss("h0.example", false, nothingAt("h0.example")) // a known host still counts
	m := attrMap(func() []any { a, _ := r.take(); return a }())
	if m["others"] != 5 || m["custom"] != maxReportHosts+6 {
		t.Errorf("others = %v custom = %v, want 5 and %d", m["others"], m["custom"], maxReportHosts+6)
	}
	if n := strings.Count(m["hosts"].(string), "), ") + 1; n != reportTopHosts {
		t.Errorf("listed %d hosts, want the top %d", n, reportTopHosts)
	}
	if !strings.HasPrefix(m["hosts"].(string), "h0.example=2 ") {
		t.Errorf("the most frequent host must come first: %q", m["hosts"])
	}
	long := strings.Repeat("a", 300) + ".example"
	r.miss(long, false, store.ErrNotFound)
	m = attrMap(func() []any { a, _ := r.take(); return a }())
	listed, _, _ := strings.Cut(m["hosts"].(string), "=")
	if len(listed) != 253 {
		t.Errorf("a host is listed at most 253 characters long, got %d", len(listed))
	}
	if !strings.HasSuffix(m["hosts"].(string), "=1 (site not found)") {
		t.Errorf("a bare ErrNotFound is listed as itself: %q", m["hosts"])
	}
}

func TestAuthzLookupErrorsAreRateLimited(t *testing.T) {
	c := newStepClock()
	r := newAuthzReport(c.now)
	if logNow, suppressed := r.lookupError(); !logNow || suppressed != 0 {
		t.Fatalf("first error: log=%v suppressed=%d, want true 0", logNow, suppressed)
	}
	c.advance(time.Second)
	if logNow, _ := r.lookupError(); logNow {
		t.Fatal("a second error within 10 s must not log")
	}
	c.advance(2 * time.Second)
	r.lookupError()
	c.advance(authzErrLogEvery)
	if logNow, suppressed := r.lookupError(); !logNow || suppressed != 2 {
		t.Fatalf("after 10 s: log=%v suppressed=%d, want true 2", logNow, suppressed)
	}
	attrs, ok := r.take()
	if !ok {
		t.Fatal("a window with lookup errors must be reported even without misses")
	}
	if m := attrMap(attrs); m["lookup_errors"] != 4 || m["custom"] != 0 {
		t.Errorf("counts = %v", m)
	}
}
