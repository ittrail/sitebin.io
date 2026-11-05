package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/analytics"
	"github.com/ittrail/sitebin.io/internal/config"
	"github.com/ittrail/sitebin.io/internal/httpapi"
	"github.com/ittrail/sitebin.io/internal/store"
	"github.com/ittrail/sitebin.io/web"
)

const importView = "aaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestParseImportUmami(t *testing.T) {
	ok, err := parseImportUmami([]string{"import-umami", importView, "ev.jsonl", "--until", "2026-03-05T00:00:00Z", "--reports", "rep.json"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (umamiImportArgs{site: importView, events: "ev.jsonl", reports: "rep.json", until: "2026-03-05T00:00:00Z"}); ok != want {
		t.Errorf("args = %+v, want %+v", ok, want)
	}
	for _, bad := range [][]string{
		nil,
		{"import"},
		{"import-umami", importView},
		{"import-umami", importView, "ev.jsonl", "extra"},
		{"import-umami", importView, "ev.jsonl", "--until", "yesterday"},
		{"import-umami", importView, "ev.jsonl", "--reports"},
		{"import-umami", importView, "ev.jsonl", "--force"},
	} {
		if _, err := parseImportUmami(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestImportUmamiCLI(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "events.jsonl")
	reports := filepath.Join(dir, "reports.json")
	os.WriteFile(events, []byte(`{"created_at":"2026-03-01T09:00:00Z","event_type":1}`+"\n"), 0o600)
	os.WriteFile(reports, []byte(`[]`), 0o600)

	var order []string
	got := map[string]string{}
	answer, code := `{"days":1,"records":1,"from":"2026-03-01","to":"2026-03-01"}`, 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/internal/analytics/import" {
			http.NotFound(w, r)
			return
		}
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		order = order[:0]
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			b, _ := io.ReadAll(p)
			got[p.FormName()] = string(b)
			order = append(order, p.FormName())
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, answer)
	}))
	defer srv.Close()

	args := umamiImportArgs{site: importView, events: events, reports: reports, until: "2026-03-05T00:00:00Z"}
	var out bytes.Buffer
	if err := importUmami(srv.URL, args, &out); err != nil {
		t.Fatal(err)
	}
	// The fields first, so the server knows the site before the events stream.
	if !reflect.DeepEqual(order, []string{"site", "until", "reports", "events"}) {
		t.Errorf("parts = %q", order)
	}
	if got["site"] != importView || got["until"] != args.until || got["reports"] != "[]" || !strings.HasPrefix(got["events"], `{"created_at"`) {
		t.Errorf("sent %q", got)
	}
	if !strings.Contains(out.String(), `"records": 1`) {
		t.Errorf("printed %q, want the report", out.String())
	}

	// A refusal is printed and fails the command.
	answer, code = `{"error":"the site is locked"}`, 409
	out.Reset()
	err := importUmami(srv.URL, umamiImportArgs{site: importView, events: events}, &out)
	if err == nil || !strings.Contains(err.Error(), "409") || !strings.Contains(out.String(), "the site is locked") {
		t.Errorf("refusal: err %v, printed %q", err, out.String())
	}

	// A missing file fails before the server is asked.
	if err := importUmami(srv.URL, umamiImportArgs{site: importView, events: filepath.Join(dir, "nope.jsonl")}, &out); err == nil {
		t.Error("a missing export was not refused")
	}

	// The server is down: a clear message, and a failure.
	down := httptest.NewServer(http.NotFoundHandler())
	addr := down.URL
	down.Close()
	err = importUmami(addr, args, &out)
	if err == nil || !strings.Contains(err.Error(), "running") {
		t.Errorf("server down: err = %v", err)
	}
}

// The CLI against the server's own handler: the parts it sends are the ones
// the route reads, and a refusal the server sends while the export is still
// streaming reaches the operator.
func TestImportUmamiThroughTheServer(t *testing.T) {
	vars := map[string]string{"SITEBIN_BASE_DOMAIN": "sitebin.example", "SITEBIN_HTTP_ONLY": "true", "SITEBIN_DATA_DIR": t.TempDir()}
	cfg, err := config.Load(func(k string) string { return vars[k] })
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(cfg.DataDir, cfg.BaseDomain, cfg.MaxSiteBytes, cfg.MaxFiles)
	if err != nil {
		t.Fatal(err)
	}
	api, err := httpapi.New(cfg, st, []byte("0123456789abcdef0123456789abcdef"), web.Assets)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Internal())
	defer srv.Close()
	site, _, err := st.Create()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	day := time.Now().UTC().AddDate(0, 0, -3)
	var export strings.Builder
	for i := range 3 {
		fmt.Fprintf(&export, `{"created_at":%q,"event_type":1,"hostname":"example.at","url_path":"/%d","session_id":"s%d","visit_id":"v%d"}`+"\n",
			day.Add(time.Duration(i)*time.Minute).Format(time.RFC3339), i, i, i)
	}
	events := filepath.Join(dir, "events.jsonl")
	os.WriteFile(events, []byte(export.String()), 0o600)
	reports := filepath.Join(dir, "reports.json")
	os.WriteFile(reports, []byte(`[{"type":"goal","name":"Kontakt","parameters":{"type":"path","value":"/kontakt"}}]`), 0o600)

	var out bytes.Buffer
	if err := importUmami(srv.URL, umamiImportArgs{site: site.ViewID, events: events, reports: reports}, &out); err != nil {
		t.Fatalf("import: %v\n%s", err, out.String())
	}
	var rep analytics.ImportReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("printed %q: %v", out.String(), err)
	}
	if rep.Records != 3 || rep.Days != 1 || !reflect.DeepEqual(rep.Goals, []string{"Kontakt"}) {
		t.Errorf("report = %+v", rep)
	}

	// A bad line early in a long export: the server stops reading there,
	// and its answer still reaches the operator.
	long := filepath.Join(dir, "long.jsonl")
	os.WriteFile(long, []byte("{nope\n"+strings.Repeat(export.String(), 20000)), 0o600)
	out.Reset()
	err = importUmami(srv.URL, umamiImportArgs{site: site.ViewID, events: long}, &out)
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(out.String(), "line 1") {
		t.Errorf("bad export: err %v, printed %q", err, out.String())
	}

	// A locked site is refused before the export is read at all.
	if _, err := st.SetLock(site, &store.SiteLock{By: store.LockByAdmin}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err = importUmami(srv.URL, umamiImportArgs{site: site.ViewID, events: long}, &out)
	if err == nil || !strings.Contains(err.Error(), "409") || !strings.Contains(out.String(), "locked") {
		t.Errorf("locked site: err %v, printed %q", err, out.String())
	}
}

func TestInternalURL(t *testing.T) {
	for addr, want := range map[string]string{
		":9000":          "http://127.0.0.1:9000",
		"127.0.0.1:9100": "http://127.0.0.1:9100",
	} {
		if got := internalURL(addr); got != want {
			t.Errorf("internalURL(%q) = %q, want %q", addr, got, want)
		}
	}
}
