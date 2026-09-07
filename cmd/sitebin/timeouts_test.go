package main

import (
	"net/http"
	"testing"
	"time"
)

// Both listeners carry timeouts: a client that opens a connection and never
// finishes its headers must not hold a goroutine and a file descriptor for
// ever. Caddy fronts the public one, but it proxies bodies through, and the
// internal one answers Caddy's own subrequests on a listener nothing else
// should be able to wedge.
func TestHTTPServersHaveTimeouts(t *testing.T) {
	for name, srv := range map[string]*http.Server{
		"public":   newPublicServer(":0", http.NotFoundHandler()),
		"internal": newInternalServer(":0", http.NotFoundHandler()),
	} {
		if srv.ReadHeaderTimeout <= 0 || srv.ReadHeaderTimeout > 30*time.Second {
			t.Errorf("%s ReadHeaderTimeout = %v", name, srv.ReadHeaderTimeout)
		}
		if srv.IdleTimeout <= 0 {
			t.Errorf("%s IdleTimeout unset", name)
		}
		if srv.ReadTimeout <= 0 {
			t.Errorf("%s ReadTimeout unset", name)
		}
	}
	// uploads are slow on purpose: the public read timeout has to allow a
	// full-size site over a slow link
	if newPublicServer(":0", nil).ReadTimeout < 5*time.Minute {
		t.Error("public ReadTimeout is too short for a large upload")
	}
}
