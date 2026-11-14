package httpapi

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// captureLog points the API's logger at a buffer.
func captureLog(e *env) *bytes.Buffer {
	var buf bytes.Buffer
	e.api.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &buf
}

// breakSiteMeta makes a site's meta.json unreadable as a site, as a torn
// write or a disk fault would.
func breakSiteMeta(t *testing.T, e *env, viewID string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.st.Root(), "sites", viewID, "meta.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A site that is attached but cannot be read is "temporarily unavailable",
// never "Site not found": an uptime monitor must not be told the site is
// gone, and the log must say what happened (the 2026-11-14 outage).
func TestAuthzLookupErrorIsTemporary(t *testing.T) {
	ext.Register(&fakeProvider{domainsOK: true})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/domains", strings.NewReader(`{"domain":"client.example.org"}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("add domain: %d", w.Code)
	}
	logs := captureLog(e)
	breakSiteMeta(t, e, c.ID)

	for _, host := range []string{"client.example.org", c.ID + ".sitebin.example"} {
		w := e.internal(t, authzReq(host, "/", ""))
		if w.Code != 503 {
			t.Fatalf("%s: authz on an unreadable site = %d, want 503\n%s", host, w.Code, w.Body)
		}
		if w.Header().Get("Retry-After") != "10" || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Retry-After %q, Cache-Control %q", host, w.Header().Get("Retry-After"), w.Header().Get("Cache-Control"))
		}
		if strings.Contains(w.Body.String(), "Site not found") || !strings.Contains(w.Body.String(), "Temporarily unavailable") {
			t.Errorf("%s: the page must say temporarily unavailable:\n%s", host, w.Body)
		}
	}
	// The path view answers the same.
	pv := httptest.NewRequest("GET", "/internal/authz", nil)
	pv.Header.Set("X-Sitebin-View", c.ID)
	pv.Header.Set("X-Forwarded-Uri", "/v/"+c.ID+"/")
	if w := e.internal(t, pv); w.Code != 503 {
		t.Errorf("path view on an unreadable site = %d, want 503", w.Code)
	}

	out := logs.String()
	if n := strings.Count(out, "authz: site lookup failed"); n != 1 {
		t.Errorf("want exactly one ERROR line within 10 s, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "host=client.example.org") || !strings.Contains(out, "meta.json") {
		t.Errorf("the line must name the host and the store's error:\n%s", out)
	}
	attrs, ok := e.api.authzMisses.take()
	if !ok {
		t.Fatal("lookup errors must be reported in the minute line")
	}
	if m := attrMap(attrs); m["lookup_errors"] != 3 || m["custom"] != 0 {
		t.Errorf("tally = %v, want 3 lookup errors and no misses", m)
	}
}

// An unknown host stays a 404, and the miss is counted with its reason; a
// generated address that names no site is counted but never listed.
func TestAuthzMissIsCounted(t *testing.T) {
	e := newEnv(t, nil)
	logs := captureLog(e)
	if w := e.internal(t, authzReq("Nope.Example.org:443", "/", "")); w.Code != 404 {
		t.Fatalf("unknown custom host = %d, want 404", w.Code)
	}
	if w := e.internal(t, authzReq("aaaaaaaaaaaaaaaaaaaaaaaaaa.sitebin.example", "/", "")); w.Code != 404 {
		t.Fatalf("unknown generated address = %d, want 404", w.Code)
	}
	pv := httptest.NewRequest("GET", "/internal/authz", nil)
	pv.Header.Set("X-Sitebin-View", "bbbbbbbbbbbbbbbbbbbbbbbbbb")
	if w := e.internal(t, pv); w.Code != 404 {
		t.Fatalf("unknown path view = %d, want 404", w.Code)
	}
	if logs.Len() != 0 {
		t.Errorf("a miss is never logged per request:\n%s", logs)
	}
	attrs, ok := e.api.authzMisses.take()
	if !ok {
		t.Fatal("a custom-domain miss must be reported")
	}
	m := attrMap(attrs)
	if m["custom"] != 1 || m["generated"] != 2 || m["lookup_errors"] != 0 {
		t.Errorf("tally = %v", m)
	}
	if m["hosts"] != "nope.example.org=1 (nothing is attached at nope.example.org)" {
		t.Errorf("hosts = %q", m["hosts"])
	}
	e.api.logAuthzReport() // an empty window logs nothing
	if logs.Len() != 0 {
		t.Errorf("an empty window must not log:\n%s", logs)
	}
}
