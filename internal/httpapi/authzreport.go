package httpapi

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

// What authz answers "Site not found" to, and what it could not look up, so
// that an outage of custom domains is visible in the log. authz has no request
// log (it is Caddy's forward_auth, on every request to every site), and on
// 2026-11-14 every custom domain answered 404 for 70 seconds without a trace.
// See docs/superpowers/specs/2026-11-14-authz-lookup-errors-design.md.

const (
	maxReportHosts   = 1000             // distinct custom hosts per window; the rest count as others
	reportTopHosts   = 10               // hosts named in the report line
	authzErrLogEvery = 10 * time.Second // at most one lookup-error line per this
	authzReportEvery = time.Minute
	maxReportHostLen = 253 // a DNS name's length; a Host header may be anything
)

type hostMisses struct {
	n      int
	reason string // the latest reason the store gave for this host
}

// authzReport is the tally. It never blocks for longer than a map update and
// never does I/O: it is called on the serving path.
type authzReport struct {
	now func() time.Time

	mu         sync.Mutex
	since      time.Time
	hosts      map[string]*hostMisses
	custom     int // custom-domain misses, listed or not
	others     int // misses of hosts past maxReportHosts
	generated  int // generated addresses and path views: counted, never listed
	errs       int // lookup errors (answered 503)
	lastErrLog time.Time
	suppressed int // lookup errors not logged since the last line
}

func newAuthzReport(now func() time.Time) *authzReport {
	if now == nil {
		now = time.Now
	}
	return &authzReport{now: now, since: now(), hosts: map[string]*hostMisses{}}
}

// reportHost is the host as the log names it: lowercased, without a port or
// a trailing dot, at most a DNS name long.
func reportHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(h, ".")
	if len(h) > maxReportHostLen {
		h = h[:maxReportHostLen]
	}
	return h
}

// missReason is what the store said, without the "site not found: " every
// ErrNotFound starts with.
func missReason(err error) string {
	if err == nil {
		return store.ErrNotFound.Error()
	}
	if rest, ok := strings.CutPrefix(err.Error(), store.ErrNotFound.Error()+": "); ok && rest != "" {
		return rest
	}
	return err.Error()
}

// miss counts one "Site not found" answer. view is a generated address or a
// path view: bots probe random labels of the view domain, so those are only
// a number.
func (r *authzReport) miss(host string, view bool, err error) {
	var h, reason string
	if !view {
		h, reason = reportHost(host), missReason(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if view {
		r.generated++
		return
	}
	r.custom++
	if m, ok := r.hosts[h]; ok {
		m.n++
		m.reason = reason
		return
	}
	if len(r.hosts) >= maxReportHosts {
		r.others++
		return
	}
	r.hosts[h] = &hostMisses{n: 1, reason: reason}
}

// lookupError counts one lookup that could not read and says whether to log
// it now, with how many were not logged since the last line.
func (r *authzReport) lookupError() (logNow bool, suppressed int) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs++
	if !r.lastErrLog.IsZero() && now.Sub(r.lastErrLog) < authzErrLogEvery {
		r.suppressed++
		return false, 0
	}
	r.lastErrLog = now
	suppressed, r.suppressed = r.suppressed, 0
	return true, suppressed
}

// take ends the window and returns its report line's attributes, and false
// when there is nothing worth a line: no custom-domain miss and no lookup
// error. A window of generated misses alone is bot noise.
func (r *authzReport) take() ([]any, bool) {
	now := r.now()
	r.mu.Lock()
	since, hosts := r.since, r.hosts
	custom, others, generated, errs := r.custom, r.others, r.generated, r.errs
	r.since, r.hosts = now, map[string]*hostMisses{}
	r.custom, r.others, r.generated, r.errs = 0, 0, 0, 0
	r.mu.Unlock()
	if custom == 0 && errs == 0 {
		return nil, false
	}
	type entry struct {
		host string
		m    *hostMisses
	}
	list := make([]entry, 0, len(hosts))
	for h, m := range hosts {
		list = append(list, entry{h, m})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].m.n != list[j].m.n {
			return list[i].m.n > list[j].m.n
		}
		return list[i].host < list[j].host
	})
	if len(list) > reportTopHosts {
		list = list[:reportTopHosts]
	}
	named := make([]string, len(list))
	for i, e := range list {
		named[i] = fmt.Sprintf("%s=%d (%s)", e.host, e.m.n, e.m.reason)
	}
	return []any{
		"since", since.UTC().Format(time.RFC3339),
		"until", now.UTC().Format(time.RFC3339),
		"custom", custom,
		"distinct", len(hosts) + others,
		"hosts", strings.Join(named, ", "),
		"others", others,
		"generated", generated,
		"lookup_errors", errs,
	}, true
}

// RunAuthzReport logs the tally once a minute until ctx ends, and once more
// then. `sitebin run` starts it.
func (a *API) RunAuthzReport(ctx context.Context) {
	t := time.NewTicker(authzReportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.logAuthzReport()
			return
		case <-t.C:
			a.logAuthzReport()
		}
	}
}

func (a *API) logAuthzReport() {
	if attrs, ok := a.authzMisses.take(); ok {
		a.log.Info("authz: answered site not found", attrs...)
	}
}
