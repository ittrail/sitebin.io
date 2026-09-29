package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// showProvenance is `sitebin provenance <view-id|edit-id|domain|ip|cidr>`: a
// site's whole log, or every site whose log names an address in the range.
// Account logs are the extension's data and are read in the admin console.
func showProvenance(st *store.Store, out io.Writer, key string) error {
	if m, ok := provenance.ParseMatch(key); ok {
		return provenanceByAddress(st, out, m)
	}
	site, err := findSite(st, key)
	if err != nil {
		return err
	}
	es, err := st.Provenance(site)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s  owner=%s  created %s\n", site.ViewID, orDash(site.Meta.OwnerAccountID), site.Meta.CreatedAt.Format("2006-01-02 15:04:05"))
	if len(es) == 0 {
		fmt.Fprintln(out, "  nothing recorded (or all of it older than 90 days)")
		return nil
	}
	for _, e := range es {
		printEntry(out, "  ", e)
	}
	return nil
}

func provenanceByAddress(st *store.Store, out io.Writer, m provenance.Match) error {
	sites, err := st.AllSites()
	if err != nil {
		return err
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].Meta.CreatedAt.Before(sites[j].Meta.CreatedAt) })
	n := 0
	for _, site := range sites {
		es, err := st.Provenance(site)
		if err != nil {
			continue
		}
		hit := m.Filter(es)
		if len(hit) == 0 {
			continue
		}
		n++
		fmt.Fprintf(out, "%s  owner=%s\n", site.ViewID, orDash(site.Meta.OwnerAccountID))
		for _, e := range hit {
			printEntry(out, "  ", e)
		}
	}
	fmt.Fprintf(out, "\n%d site(s) seen from %s.\n", n, m)
	return nil
}

// printEntry prints one entry on one line, the client on a second.
func printEntry(out io.Writer, ind string, e provenance.Entry) {
	when := e.Time.Format("2006-01-02 15:04:05")
	if e.N > 1 {
		when += fmt.Sprintf(" (x%d until %s)", e.N, e.Latest().Format("15:04:05"))
	}
	what := e.Detail
	if e.Files > 0 {
		what = strings.TrimSpace(fmt.Sprintf("%d file(s) %s", e.Files, e.Detail))
	}
	fmt.Fprintf(out, "%s%s  %-13s %-22s %-15s account=%s  %s\n", ind, when, e.Action, e.Surface+"/"+e.Auth, orDash(e.IP), orDash(e.Account), what)
	if e.UA != "" {
		fmt.Fprintf(out, "%s    %s\n", ind, e.UA)
	}
}

// creatorIP is the address a site was created from, or "-".
func creatorIP(st *store.Store, site *store.Site) string {
	es, err := st.Provenance(site)
	if err != nil || len(es) == 0 || es[0].Action != provenance.ActionCreate || es[0].IP == "" {
		return "-"
	}
	return es[0].IP
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
