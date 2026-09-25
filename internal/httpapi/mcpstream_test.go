package httpapi

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A subscriptions/listen stream (MCP 2026-07-28) stays open for as long as
// the client listens, and its first event — the acknowledgement — is what a
// client waits for before it sends anything else. Behind a response writer
// that cannot flush, that event sat in a buffer until the stream ended:
// Claude Code gave up after 25 seconds, cancelled the stream, and only then
// listed the tools, so every connection to the server took 25 seconds.
//
// This runs through Public() on a real listener, middleware included,
// because the loss happened in the middleware and a recorder never shows it.
func TestMCPListenStreamAcknowledgesAtOnce(t *testing.T) {
	e := newEnv(t, nil)
	srv := httptest.NewServer(e.api.Public())
	defer srv.Close()

	const body = `{"jsonrpc":"2.0","id":"listen:0","method":"subscriptions/listen","params":{` +
		`"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
		`"io.modelcontextprotocol/clientInfo":{"name":"stream-test","version":"1"},` +
		`"io.modelcontextprotocol/clientCapabilities":{}},` +
		`"notifications":{"toolsListChanged":true}}}`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "subscriptions/listen")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("no response headers within 3s — the stream is not being flushed: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}

	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "notifications/subscriptions/acknowledged") {
			return
		}
	}
	t.Fatalf("the acknowledgement never arrived while the stream was open: %v", sc.Err())
}
