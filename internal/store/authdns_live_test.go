package store

import (
	"context"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestAuthDNSLive asks real nameservers. Opt in with SITEBIN_LIVE_DNS=1; the
// suite never depends on the network.
func TestAuthDNSLive(t *testing.T) {
	if os.Getenv("SITEBIN_LIVE_DNS") != "1" {
		t.Skip("set SITEBIN_LIVE_DNS=1 to ask real nameservers")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a := newAuthDNS()
	zone, srv, err := a.servers(ctx, "_sitebin-zone.nothing-here.sitebin.io")
	t.Logf("servers of %s = %v %v", zone, srv, err)
	if err != nil || len(srv) == 0 {
		t.Fatal("no authoritative servers found for sitebin.io")
	}
	if _, err := a.LookupTXT(ctx, "_sitebin-zone.nothing-here.sitebin.io"); !isNotFound(err) {
		t.Errorf("absent TXT = %v, want not found", err)
	}
	// www.sitebin.io has an A record and no CNAME: a definitive "not found".
	if _, err := a.LookupCNAME(ctx, "www.sitebin.io"); !isNotFound(err) {
		t.Errorf("www.sitebin.io CNAME = %v, want not found", err)
	}
	if got, err := a.LookupTXT(ctx, "sitebin.io"); err != nil && !isNotFound(err) {
		t.Errorf("apex TXT: %v", err)
	} else {
		t.Logf("sitebin.io TXT = %q", got)
	}
}

// TestAuthDNSLiveThroughTheRegistry replays 2026-09-28: the system resolver
// denies a freshly registered domain exists, and its TXT record must still be
// found on its own servers through the TLD's delegation. Opt in with
// SITEBIN_LIVE_DNS=1; SITEBIN_LIVE_TXT names the record to look up and
// SITEBIN_LIVE_WANT the value it must carry.
func TestAuthDNSLiveThroughTheRegistry(t *testing.T) {
	if os.Getenv("SITEBIN_LIVE_DNS") != "1" {
		t.Skip("set SITEBIN_LIVE_DNS=1 to ask real nameservers")
	}
	name, want := os.Getenv("SITEBIN_LIVE_TXT"), os.Getenv("SITEBIN_LIVE_WANT")
	if name == "" {
		t.Skip("set SITEBIN_LIVE_TXT (and SITEBIN_LIVE_WANT) to a record to look up")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a := newAuthDNS()
	sys := a.lookupNS
	a.lookupNS = func(ctx context.Context, n string) ([]*net.NS, error) {
		if strings.Contains(strings.TrimSuffix(n, "."), ".") { // only the TLD is known
			return nil, &net.DNSError{Err: "no such host", Name: n, IsNotFound: true}
		}
		return sys(ctx, n)
	}
	got, err := a.LookupTXT(ctx, name)
	t.Logf("%s TXT = %q %v", name, got, err)
	if err != nil || (want != "" && !slices.Contains(got, want)) {
		t.Errorf("want %q", want)
	}
}

// TestDNSVerifierLive runs the production verifier against real DNS.
// Opt in with SITEBIN_LIVE_DNS=1 and name the claim with SITEBIN_LIVE_DOMAIN,
// SITEBIN_LIVE_TOKEN and SITEBIN_LIVE_VIEWHOST.
func TestDNSVerifierLive(t *testing.T) {
	if os.Getenv("SITEBIN_LIVE_DNS") != "1" {
		t.Skip("set SITEBIN_LIVE_DNS=1 to ask real nameservers")
	}
	d := os.Getenv("SITEBIN_LIVE_DOMAIN")
	if d == "" {
		t.Skip("set SITEBIN_LIVE_DOMAIN, SITEBIN_LIVE_TOKEN and SITEBIN_LIVE_VIEWHOST")
	}
	ok, err := NewDNSVerifier().Verify(context.Background(), d, os.Getenv("SITEBIN_LIVE_TOKEN"), os.Getenv("SITEBIN_LIVE_VIEWHOST"))
	t.Logf("Verify(%s) = %v %v", d, ok, err)
	if !ok || err != nil {
		t.Error("not proven")
	}
}
