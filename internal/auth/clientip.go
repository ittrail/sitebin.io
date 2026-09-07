package auth

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP is the address rate limiters key on. Behind Caddy every request
// carries X-Forwarded-For, and the LAST entry is the one Caddy itself
// appended — the only one a client cannot choose. Without the header it is
// the connection's own peer.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
