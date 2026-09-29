package main

import (
	"fmt"
	"io"

	"github.com/ittrail/sitebin.io/internal/store"
)

// scanSites runs the abuse rules over sites already on disk (`sitebin scan`):
// the content that predates the scanner, or a site after a new rule. It
// reports only, writing nothing, unless lock is set; then it records the
// findings and holds exactly the sites an upload of the same content would
// have held — never a trusted one. The CLI does not know who the operator
// is (no extension runs here); the operator's sites are on a trusted tier,
// and that exempts them. See docs/superpowers/specs/2026-09-29-abuse-detection.md.
func scanSites(st *store.Store, out io.Writer, key string, all, lock bool) error {
	var sites []*store.Site
	if all {
		var err error
		if sites, err = st.AllSites(); err != nil {
			return err
		}
	} else {
		site, err := findSite(st, key)
		if err != nil {
			return err
		}
		sites = []*store.Site{site}
	}
	if rs := st.AbuseRules(); rs != nil {
		fmt.Fprintf(out, "scanning %d site(s) with %d rules (built-in + %s if present)\n\n", len(sites), rs.Rules(), store.AbuseRulesFile)
	}
	hit, held := 0, 0
	for _, site := range sites {
		results, err := st.ScanSite(site)
		if err != nil {
			fmt.Fprintf(out, "%s  error: %v\n", site.ViewID, err)
			continue
		}
		if len(results) == 0 {
			continue
		}
		hit++
		trust := "untrusted"
		if st.Trusted(site) {
			trust = "trusted"
		}
		owner := site.Meta.OwnerAccountID
		if owner == "" {
			owner = "anonymous"
		}
		fmt.Fprintf(out, "%s  owner=%s  %s  mode=%s\n", site.ViewID, owner, trust, site.Meta.Mode)
		for _, r := range results {
			for _, h := range r.Hits {
				fmt.Fprintf(out, "    %-24s %-5s %s\n        %s\n", h.Rule, h.Severity, r.Path, h.Excerpt)
			}
		}
		var ev store.ScanEvent
		if lock {
			ev, err = st.ApplyScan(site, results)
		} else {
			ev, err = st.Preview(site, results)
		}
		if err != nil {
			fmt.Fprintf(out, "    error: %v\n", err)
			continue
		}
		verb := "would be"
		if lock {
			verb = "now"
		}
		switch {
		case ev.Held():
			held++
			fmt.Fprintf(out, "    => %s HELD (locked by the scanner)\n", verb)
		default:
			fmt.Fprintf(out, "    => %s\n", ev.Decision)
		}
	}
	fmt.Fprintf(out, "\n%d site(s) scanned, %d with hits, %d ", len(sites), hit, held)
	if lock {
		fmt.Fprintln(out, "held.")
	} else {
		fmt.Fprintln(out, "would be held. Nothing was changed; run with --lock to record and hold.")
	}
	return nil
}
