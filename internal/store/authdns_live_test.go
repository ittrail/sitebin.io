package store

import (
	"context"
	"os"
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
	srv, err := a.servers(ctx, "_sitebin-zone.nothing-here.sitebin.io")
	t.Logf("servers = %v %v", srv, err)
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
