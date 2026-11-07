//go:build ee

package ee

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ittrail/sitebin.io/internal/snapshots"
)

// fakeAgent answers the agent API the console reads, and records restores.
type fakeAgent struct {
	mu       sync.Mutex
	restores []string
	deleted  []string
}

func (f *fakeAgent) handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"source":"borg","snapshots":2,"indexed":2,"runs":{"hourly":{"mode":"hourly","rc":0}},"restore":{"state":"idle"}}`))
	})
	m.HandleFunc("GET /v1/snapshots", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"hourly-2","at":"2026-11-07T09:00:00Z","kind":"hourly","indexed":true,"sites":3,"bytes":99}]`))
	})
	m.HandleFunc("GET /v1/instance/copies", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"name":"data.before-restore-20261107T090000Z","kind":"before-restore","at":"2026-11-07T09:00:00Z"}]`))
	})
	m.HandleFunc("POST /v1/instance/restore", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		if b["snapshot"] == "missing" {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		f.mu.Lock()
		f.restores = append(f.restores, b["snapshot"]+b["copy"])
		f.mu.Unlock()
		w.WriteHeader(202)
		w.Write([]byte(`{"state":"preparing","snapshot":"` + b["snapshot"] + `"}`))
	})
	m.HandleFunc("DELETE /v1/instance/copies/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deleted = append(f.deleted, r.PathValue("name"))
		f.mu.Unlock()
		w.Write([]byte(`{"status":"deleted"}`))
	})
	return m
}

func setupBackups(t *testing.T) (*provider, http.Handler, *http.Cookie, *fakeAgent) {
	t.Helper()
	p, _, mux := setupAdmin(t, "boss@example.com")
	boss, _ := adminUser(t, p, mux, "boss@example.com", "admin")
	fa := &fakeAgent{}
	srv := httptest.NewServer(fa.handler())
	t.Cleanup(srv.Close)
	p.snap = snapshots.NewClientURL(srv.URL, srv.Client())
	return p, mux, boss, fa
}

func TestBackupsConsoleIsTheOperatorsOnly(t *testing.T) {
	p, mux, boss, fa := setupBackups(t)
	nobody, _ := adminUser(t, p, mux, "nobody@example.com", "free")
	for _, c := range []*http.Cookie{nobody, nil} {
		if w := getAs(mux, "/account/api/admin/backups", c); w.Code != 404 {
			t.Errorf("not an admin (%v): GET %d", c != nil, w.Code)
		}
	}
	nobodyAcc, _ := p.accounts.ByEmail("nobody@example.com")
	if w := postAs(mux, "/account/admin/backups/restore", nobody, url.Values{"csrf": {p.csrf(nobodyAcc)}, "snapshot": {"hourly-2"}, "confirm": {"restore"}}); w.Code != 404 {
		t.Errorf("not an admin: restore %d", w.Code)
	}
	if len(fa.restores) != 0 {
		t.Fatal("a non-admin started a restore")
	}
	w := getAs(mux, "/account/api/admin/backups", boss)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":true`) || !strings.Contains(w.Body.String(), `"hourly-2"`) || !strings.Contains(w.Body.String(), "before-restore") {
		t.Fatalf("admin: %d %s", w.Code, w.Body)
	}
}

func TestInstanceRestoreNeedsTheTypedWordAndTheToken(t *testing.T) {
	p, mux, boss, fa := setupBackups(t)
	acc, _ := p.accounts.ByEmail("boss@example.com")
	csrf := p.csrf(acc)
	if w := postAs(mux, "/account/admin/backups/restore", boss, url.Values{"csrf": {"nope"}, "snapshot": {"hourly-2"}, "confirm": {"restore"}}); w.Code != 403 {
		t.Errorf("bad csrf: %d", w.Code)
	}
	if w := postAs(mux, "/account/admin/backups/restore", boss, url.Values{"csrf": {csrf}, "snapshot": {"hourly-2"}, "confirm": {"yes"}}); w.Code != 400 {
		t.Errorf("without the typed word: %d", w.Code)
	}
	if w := postAs(mux, "/account/admin/backups/restore", boss, url.Values{"csrf": {csrf}, "confirm": {"restore"}}); w.Code != 400 {
		t.Errorf("without a snapshot: %d", w.Code)
	}
	if len(fa.restores) != 0 {
		t.Fatal("a refused request reached the agent")
	}
	w := postAs(mux, "/account/admin/backups/restore", boss, url.Values{"csrf": {csrf}, "snapshot": {"hourly-2"}, "confirm": {" Restore "}})
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"preparing"`) {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if w := postAs(mux, "/account/admin/backups/restore", boss, url.Values{"csrf": {csrf}, "snapshot": {"missing"}, "confirm": {"restore"}}); w.Code != 404 {
		t.Errorf("unknown snapshot: %d", w.Code)
	}
	if w := postAs(mux, "/account/admin/backups/restore", boss, url.Values{"csrf": {csrf}, "copy": {"data.before-restore-20261107T090000Z"}, "confirm": {"restore"}}); w.Code != 202 {
		t.Errorf("switch back: %d", w.Code)
	}
	if strings.Join(fa.restores, ",") != "hourly-2,data.before-restore-20261107T090000Z" {
		t.Errorf("restores = %v", fa.restores)
	}
	if w := postAs(mux, "/account/admin/backups/copies/data.before-restore-20261107T090000Z/delete", boss, url.Values{"csrf": {csrf}}); w.Code != 200 || len(fa.deleted) != 1 {
		t.Errorf("delete copy: %d %v", w.Code, fa.deleted)
	}
}

func TestBackupsConsoleWithoutAnAgent(t *testing.T) {
	p, _, mux := setupAdmin(t, "boss@example.com")
	boss, _ := adminUser(t, p, mux, "boss@example.com", "admin")
	w := getAs(mux, "/account/api/admin/backups", boss)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatalf("no agent: %d %s", w.Code, w.Body)
	}
	acc, _ := p.accounts.ByEmail("boss@example.com")
	if w := postAs(mux, "/account/admin/backups/restore", boss, url.Values{"csrf": {p.csrf(acc)}, "snapshot": {"x"}, "confirm": {"restore"}}); w.Code != 404 {
		t.Errorf("restore without an agent: %d", w.Code)
	}
}
