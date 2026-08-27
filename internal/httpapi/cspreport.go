package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

const (
	// maxReportBody caps what the endpoint will read. Reports are small; a
	// large body is either a broken client or someone using the endpoint as a
	// write amplifier.
	maxReportBody = 8 << 10
	// reportFlush is how often aggregated violations reach disk. A hostile page
	// controls how often it reports, so writes must not be per-request.
	reportFlush = time.Minute
)

// cspAggregator batches violation reports per site. What matters for spotting
// abuse is the destination — "this site tried to reach api.emailjs.com" — not
// an exact count, so reports accumulate in memory and are flushed on a timer.
type cspAggregator struct {
	mu      sync.Mutex
	pending map[string]*cspPending // by view id
	flushAt time.Time
	now     func() time.Time
}

type cspPending struct {
	site    *store.Site
	count   int
	blocked []string
}

func newCSPAggregator() *cspAggregator {
	return &cspAggregator{pending: map[string]*cspPending{}, now: time.Now}
}

// add records one report and returns the batches that are due to be written.
// Flushing is driven by arriving reports rather than a goroutine: an instance
// with no abuse does no work, and a burst still writes at most once per window.
func (c *cspAggregator) add(site *store.Site, blockedURI string) []*cspPending {
	c.mu.Lock()
	defer c.mu.Unlock()

	p := c.pending[site.ViewID]
	if p == nil {
		p = &cspPending{site: site}
		c.pending[site.ViewID] = p
	}
	p.count++
	if blockedURI != "" && len(p.blocked) < store.MaxBlockedURIs {
		seen := false
		for _, u := range p.blocked {
			if u == blockedURI {
				seen = true
				break
			}
		}
		if !seen {
			p.blocked = append(p.blocked, blockedURI)
		}
	}

	now := c.now()
	if c.flushAt.IsZero() {
		c.flushAt = now.Add(reportFlush)
		return nil
	}
	if now.Before(c.flushAt) {
		return nil
	}
	due := make([]*cspPending, 0, len(c.pending))
	for _, v := range c.pending {
		due = append(due, v)
	}
	c.pending = map[string]*cspPending{}
	c.flushAt = now.Add(reportFlush)
	return due
}

// cspReport is the subset of both report shapes we care about: the legacy
// report-uri body ({"csp-report":{...}}) and the Reporting API array
// ([{"type":"csp-violation","body":{...}}]).
type cspReport struct {
	CSPReport struct {
		BlockedURI string `json:"blocked-uri"`
	} `json:"csp-report"`
}

type reportingAPIEntry struct {
	Type string `json:"type"`
	Body struct {
		BlockedURL string `json:"blockedURL"`
		BlockedURI string `json:"blocked-uri"`
	} `json:"body"`
}

// handleCSPReport receives violation reports. It is public and unauthenticated
// by necessity — the browser sends it, with no credentials and no way to prove
// anything — so it does the minimum: resolve the site from the Host, extract the
// blocked destination, and hand it to the aggregator.
func (a *API) handleCSPReport(w http.ResponseWriter, r *http.Request) {
	// Answer before any work: a report is fire-and-forget and the browser does
	// not read the response.
	defer w.WriteHeader(http.StatusNoContent)

	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	site, err := a.siteByHost(host)
	if err != nil {
		return // a report about nothing we serve
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxReportBody))
	if err != nil {
		return
	}
	blocked := blockedURIFrom(body)
	for _, p := range a.csp.add(site, blocked) {
		a.st.RecordCSPViolation(p.site, p.count, p.blocked)
	}
}

// blockedURIFrom pulls the blocked destination out of either report format,
// returning "" when neither parses — a report that only tells us a violation
// happened is still worth counting.
func blockedURIFrom(body []byte) string {
	var legacy cspReport
	if json.Unmarshal(body, &legacy) == nil && legacy.CSPReport.BlockedURI != "" {
		return legacy.CSPReport.BlockedURI
	}
	var modern []reportingAPIEntry
	if json.Unmarshal(body, &modern) == nil {
		for _, e := range modern {
			if e.Body.BlockedURL != "" {
				return e.Body.BlockedURL
			}
			if e.Body.BlockedURI != "" {
				return e.Body.BlockedURI
			}
		}
	}
	return ""
}
