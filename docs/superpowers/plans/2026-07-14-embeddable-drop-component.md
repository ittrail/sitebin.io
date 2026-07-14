# Embeddable Drop Component Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract the startpage's drop/create area into a `<sitebin-drop>` custom element served at `/_sitebin/embed.js`, with cross-origin create gated by `SITEBIN_EMBED_ORIGINS` (Enterprise).

**Architecture:** One dependency-free JS file defines the shadow-DOM component; the startpage becomes its first consumer (event-only mode, keeps its own ticket). The Go side adds a config field, one Provider capability method, a script route with `ACAO:*`, and CORS handling on `POST/OPTIONS /api/sites` following the existing `CustomDomainsAllowed` gating pattern.

**Tech Stack:** Go 1.x (stdlib net/http, table-driven tests), vanilla JS custom elements, no build step.

## Global Constraints

- No new dependencies, no Node build step (`web/` is embedded via `go:embed`).
- Community binary must never emit create-CORS headers; the env var is ignored there with a startup warning.
- Behavior parity for the startpage: same options, same auto-mode suggestion, same `POST /api/sites` payloads.
- Spec: `docs/superpowers/specs/2026-07-14-embeddable-drop-component-design.md`.

---

### Task 1: Config — `SITEBIN_EMBED_ORIGINS`

**Files:**
- Modify: `internal/config/config.go` (Config struct + `Load`)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.EmbedOrigins []string` — normalized (trimmed, lowercased) origins, or `["*"]`.

- [ ] **Step 1: Failing test** (append to existing config tests, using the package's `getenv` map style):

```go
func TestEmbedOrigins(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"SITEBIN_BASE_DOMAIN":   "sitebin.example.com",
		"SITEBIN_HTTP_ONLY":     "true",
		"SITEBIN_EMBED_ORIGINS": " https://Sitebin.io ,https://www.sitebin.io,",
	})
	want := []string{"https://sitebin.io", "https://www.sitebin.io"}
	if !reflect.DeepEqual(cfg.EmbedOrigins, want) {
		t.Fatalf("EmbedOrigins = %v, want %v", cfg.EmbedOrigins, want)
	}
}
```

(Adapt the loader-helper name to what `config_test.go` actually uses; empty env → nil slice; a lone `*` → `["*"]`; entries must parse as `scheme://host[:port]` with scheme http/https, else `Load` errors.)

- [ ] **Step 2: Run** `go test ./internal/config/ -run TestEmbedOrigins` — expect FAIL (unknown field).
- [ ] **Step 3: Implement** — struct field `EmbedOrigins []string`; in `Load` after the FTP block:

```go
if v := strings.TrimSpace(getenv("SITEBIN_EMBED_ORIGINS")); v != "" {
	if v == "*" {
		cfg.EmbedOrigins = []string{"*"}
	} else {
		for _, part := range strings.Split(v, ",") {
			o := strings.ToLower(strings.TrimSpace(part))
			if o == "" {
				continue
			}
			u, err := url.Parse(o)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" {
				return cfg, fmt.Errorf("SITEBIN_EMBED_ORIGINS: %q is not an origin (want https://host[:port] or *)", part)
			}
			cfg.EmbedOrigins = append(cfg.EmbedOrigins, o)
		}
	}
}
```

- [ ] **Step 4: Run** the test — PASS. Run `go test ./internal/config/`.
- [ ] **Step 5: Commit** `feat: SITEBIN_EMBED_ORIGINS config`

### Task 2: Extension seam — `EmbedOriginsAllowed()`

**Files:**
- Modify: `internal/ext/ext.go` (Provider interface), `ee/provider.go`, `internal/httpapi/gate_test.go` (fakeProvider), any other Provider fakes (`grep -rn "AccountsEnabled() bool" --include=*_test.go`).

**Interfaces:**
- Produces: `Provider.EmbedOriginsAllowed() bool` — ee returns `true` (mirrors `CustomDomainsAllowed`).

- [ ] **Step 1:** Add to the `Provider` interface below `CustomDomainsAllowed`:

```go
	// EmbedOriginsAllowed reports whether SITEBIN_EMBED_ORIGINS is honored —
	// cross-origin embedding of the create flow is a premium feature.
	EmbedOriginsAllowed() bool
```

- [ ] **Step 2:** `go build ./...` (with and without `-tags ee`) — expect compile failures listing every implementer; add `func (p *provider) EmbedOriginsAllowed() bool { return true }` to `ee/provider.go` and `func (f *fakeProvider) EmbedOriginsAllowed() bool { return f.embedOK }` (+ `embedOK bool` field) to fakes.
- [ ] **Step 3:** `go vet ./... && go test ./... && go test -tags ee ./...` — PASS.
- [ ] **Step 4: Commit** `feat: ext seam capability for embed origins`

### Task 3: Serve `/_sitebin/embed.js`

**Files:**
- Create: `web/static/embed.js` (placeholder header comment only for now; Task 5 fills it)
- Modify: `internal/httpapi/server.go` (route), `internal/httpapi/pages.go` (handler)
- Test: `internal/httpapi/httpapi_test.go`

**Interfaces:**
- Produces: `GET /_sitebin/embed.js` → `text/javascript`, `Access-Control-Allow-Origin: *`, `Cache-Control: public, max-age=3600`.

- [ ] **Step 1: Failing test** (follow the package's existing `newTestAPI`-style helper):

```go
func TestEmbedScriptRoute(t *testing.T) {
	a := newTestAPI(t) // whatever helper the file already uses
	rr := httptest.NewRecorder()
	a.Public().ServeHTTP(rr, httptest.NewRequest("GET", "/_sitebin/embed.js", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("ACAO = %q, want *", got)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content-type = %q", ct)
	}
}
```

- [ ] **Step 2:** Run — FAIL (404).
- [ ] **Step 3: Implement** — in `server.go`: `mux.HandleFunc("GET /_sitebin/embed.js", a.embedScript)`; in `pages.go`:

```go
// embedScript serves the <sitebin-drop> component. CORS * because the script
// itself is public code — the gated surface is the create API, not the file.
func (a *API) embedScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFileFS(w, r, a.webFS, "static/embed.js")
}
```

- [ ] **Step 4:** Run — PASS.
- [ ] **Step 5: Commit** `feat: serve the embed component at /_sitebin/embed.js`

### Task 4: CORS on create (EE-gated)

**Files:**
- Modify: `internal/httpapi/server.go` (routes), new helper in `internal/httpapi/sites.go`
- Test: `internal/httpapi/httpapi_test.go`

**Interfaces:**
- Consumes: `cfg.EmbedOrigins`, `ext.Get()`, `Provider.EmbedOriginsAllowed()`.
- Produces: `a.createCORS(w, r) bool` (adds headers when origin allowed; reports allowed) and route `OPTIONS /api/sites`.

- [ ] **Step 1: Failing tests** — table-driven:

```go
func TestCreateCORS(t *testing.T) {
	cases := []struct {
		name     string
		provider bool   // register fakeProvider{embedOK:true}
		origins  []string
		origin   string // request Origin header
		wantACAO string
	}{
		{"community ignores allowlist", false, []string{"https://sitebin.io"}, "https://sitebin.io", ""},
		{"ee allowed origin", true, []string{"https://sitebin.io"}, "https://sitebin.io", "https://sitebin.io"},
		{"ee wildcard", true, []string{"*"}, "https://anything.example", "https://anything.example"},
		{"ee disallowed origin", true, []string{"https://sitebin.io"}, "https://evil.example", ""},
		{"no origin header", true, []string{"*"}, "", ""},
	}
	// for each: build API with cfg.EmbedOrigins=c.origins, optionally
	// ext.Register(&fakeProvider{embedOK:true}) + defer ext.Reset(),
	// POST /api/sites with an empty multipart body and Origin header,
	// assert rr.Header().Get("Access-Control-Allow-Origin") == c.wantACAO
	// and (when allowed) Vary contains Origin.
	// Also: OPTIONS /api/sites with allowed Origin → 204 with
	// Access-Control-Allow-Methods containing POST; community OPTIONS → 404.
}
```

- [ ] **Step 2:** Run — FAIL.
- [ ] **Step 3: Implement** in `sites.go`:

```go
// createCORS emits CORS headers for POST /api/sites when the request Origin
// is allowlisted via SITEBIN_EMBED_ORIGINS *and* the enterprise extension is
// active — cross-origin embedding is a premium capability. Reports whether
// the origin was allowed. Never allows credentials.
func (a *API) createCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || len(a.cfg.EmbedOrigins) == 0 {
		return false
	}
	if p, ok := ext.Get(); !ok || !p.EmbedOriginsAllowed() {
		return false
	}
	w.Header().Add("Vary", "Origin")
	lo := strings.ToLower(origin)
	for _, allowed := range a.cfg.EmbedOrigins {
		if allowed == "*" || allowed == lo {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			return true
		}
	}
	return false
}

func (a *API) createPreflight(w http.ResponseWriter, r *http.Request) {
	if !a.createCORS(w, r) {
		writeError(w, 404, "not found")
		return
	}
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(204)
}
```

Call `a.createCORS(w, r)` as the first line of `createSite`; register `mux.HandleFunc("OPTIONS /api/sites", a.createPreflight)` in `Public()`. Startup warning in `cmd/sitebin/main.go` (near the existing `ext.Get()` block): if `len(cfg.EmbedOrigins) > 0` and no provider (or `!EmbedOriginsAllowed()`) → `slog.Warn("SITEBIN_EMBED_ORIGINS is set but cross-origin embedding requires the enterprise edition; ignoring")`.

- [ ] **Step 4:** Run `go test ./internal/httpapi/` — PASS. `go test ./... && go test -tags ee ./...`.
- [ ] **Step 5: Commit** `feat: EE-gated CORS for cross-origin site creation`

### Task 5: The `<sitebin-drop>` component

**Files:**
- Create: `web/static/embed.js` (full implementation, replaces Task 3's stub)

**Interfaces (the public contract — keep exact):**
- Element `sitebin-drop`; attributes `instance`, `demo`, `no-domains`, `event-only`.
- Events (bubbling, composed): `sitebin-published` (`detail` = API response body), `sitebin-error` (`detail = {status, error}`).
- API call: `POST <instance>/api/sites` multipart — fields `mode`, optional `view_password`, `expires_at` (ISO), `webdav=true`, repeated `domain`, and `files` entries (path as filename) or `zip`.

Single IIFE, `customElements.define("sitebin-drop", …)`. Structure:

1. Template: shadow root with `<style>` (slab/options/chips/progress/ticket styles ported from `app.css`, tokens inlined; fonts inherit) + markup mirroring today's `#create` section plus a minimal built-in ticket section (view/edit/password rows + copy + warning + reset; no QR).
2. State + logic ported from `app.js` (`addFiles`, zip exclusivity, folder traversal via `webkitGetAsEntry` walk, `suggestMode`, chips render, domains list, XHR upload with progress) — all lookups via `this.shadowRoot`, no global ids.
3. `instance` resolution: attribute → `document.currentScript.src` origin captured at define time → `location.origin`.
4. `demo`: on publish, animate progress ~900 ms, then synthesize `{view_url, edit_url, edit_password, demo:true}` from the instance origin (`https://demo-a1b2c3.<host>`), show ticket with a "demo — nothing was uploaded" stamp.
5. `event-only`: skip built-in ticket; always dispatch events.
6. `no-domains`: hide the domains option block.

- [ ] Implement the file; syntax-check with `node --check web/static/embed.js` if node exists, else careful review.
- [ ] **Commit** `feat: <sitebin-drop> embeddable create component`

### Task 6: Startpage consumes the component

**Files:**
- Modify: `web/static/index.html` (replace slab/options/publish markup with `<sitebin-drop event-only>` + `<script src="/_sitebin/embed.js" defer>`), `web/static/app.js` (drop staging/upload code; keep toast, ticket, QR, copy handlers; add `document.addEventListener("sitebin-published", …)` → `showTicket(e.detail)`; wire `sitebin-error` → toast), `web/static/app.css` (remove moved slab/option/chip/progress rules; keep chrome + ticket).

- [ ] Refactor; keep `#done` ticket + `.foot` + `#api-hint` untouched.
- [ ] Manual check via Task 8's compose run (no unit infra for JS).
- [ ] **Commit** `refactor: startpage builds on <sitebin-drop>`

### Task 7: Docs

**Files:**
- Modify: `README.md` (new "Embed the drop area" section under *Using Sitebin*: script tag example, attributes, events, `SITEBIN_EMBED_ORIGINS` + edition note, config table row), `deploy/docker-compose.example.yml` (commented `SITEBIN_EMBED_ORIGINS` line).

- [ ] Write; **Commit** `docs: embeddable drop component`

### Task 8: Verification (superpowers:verification-before-completion)

- [ ] `go vet ./... && go test ./... && go test -tags ee ./...` — all PASS.
- [ ] `docker compose -f deploy/docker-compose.example.yml up -d --build` → `http://sitebin.localtest.me:8080/`.
- [ ] Browser: publish a real multi-file site through the refactored startpage; confirm ticket (QR, copies), options behave (mode auto-suggest, zip toggle appears).
- [ ] Cross-origin embed: serve a scratch page from another local origin embedding `<sitebin-drop instance="http://sitebin.localtest.me:8080">`; without EE origins → publish fails (response unreadable); demo mode works offline.
- [ ] Tear down; final commit if fixes were needed.
