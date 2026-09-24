# Site forms Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sites hosted on Sitebin get email form submissions: a plain HTML form
posts to `/_sitebin/forms/<key>` on the site's own origin, and Sitebin mails
it to a recipient who confirmed once. There is an optional ALTCHA captcha,
optional attachments, and a machine-readable `submission.json` on every mail.
The forms are configured through the edit page, the JSON API and MCP.

**Architecture:** Everything lives in the MIT core. A new `internal/forms`
package holds the pure logic: rules, tokens, the ordered body parser, message
building, SMTP delivery and the captcha. `internal/store` persists the forms
in `meta.json`. `internal/httpapi` wires the public endpoint, the recipient's
confirm/stop pages, the JSON API and the MCP adapter. The per-site cap is
stamped into `meta.json` from the tier (`max_forms`) like every other quota,
so a submission never asks the extension. The only `ee/` change is the tier
field and the grant.

**Tech Stack:** Go 1.25 (stdlib `net/smtp`, `mime/multipart`,
`mime/quotedprintable`, `html/template`), `github.com/altcha-org/altcha-lib-go/v2`
(MIT), the vendored ALTCHA widget, vanilla JS on the edit page, PowerShell +
Docker + Mailpit for the E2E, and static HTML on the website.

**Spec:** `docs/superpowers/specs/2026-09-24-site-forms-design.md`. Read it
before any task. Every task argues from it.

## Global Constraints

- Module `github.com/ittrail/sitebin.io`, Go 1.25. `go vet ./...`, `go test ./...` **and** `go test -tags ee ./...` must pass after every task.
- Nothing under `internal/` imports `ee/`. The `ee/` change is limited to `eeconfig.Tier.MaxForms` and `grantFromTier`.
- `meta.json` fields are `omitempty`. A missing field reads as its zero value, and no migration step exists.
- Env var names and defaults, verbatim: `SITEBIN_FORMS_SMTP_HOST`, `SITEBIN_FORMS_SMTP_PORT` (587), `SITEBIN_FORMS_SMTP_USER`, `SITEBIN_FORMS_SMTP_PASS`, `SITEBIN_FORMS_SMTP_FROM` (required with host, bare address), `SITEBIN_FORMS_SMTP_TLS` (false), `SITEBIN_FORMS_MAX_PER_SITE` (unset → 10 without a provider, 0 with one), `SITEBIN_FORMS_MAX_FILES` (5), `SITEBIN_FORMS_MAX_FILE_BYTES` (2097152), `SITEBIN_FORMS_PER_IP_HOUR` (10), `SITEBIN_FORMS_PER_FORM_HOUR` (60).
- Tier field `max_forms`: 0 or absent means **none**.
- Submission contents (field values, filenames, recipient) are **never** logged.
- Mail language is English. The confirmation mail's `From` display name is `Sitebin`.
- `e2e/*.ps1` are **pure ASCII**. Write `--`, never an em dash.
- Commits use lowercase conventional prefixes (`feat:`, `fix:`, `test:`, `docs:`, `content:`), and the subject is a sentence about behaviour. Every commit message ends with:
  ```
  Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01YMdzYYrJygp11Rq3KF1B3P
  ```
- Product work happens on branch `feat/site-forms` in `Sitebin/`. Website work happens on branch `content/site-forms` in `Sitebin-Website/`. **Never push the website repo**: every push to its `main` deploys. The legal texts ship only after the operator's legal review.

## Review Focus

These are the inputs a real person will send that the spec implies but does
not spell out as tests. Each has a test in the task named after it.

1. **Non-ASCII everywhere** (umlauts and emoji in field values, the form name and `_subject`). The mail must show them correctly in the subject, both body parts and `submission.json`. → Task 6, `TestSubmissionMailNonASCII`.
2. **An empty optional file input** on a form with attachments on (the browser sends a part with `filename=""` and no bytes). It must be neither a file nor an error. → Task 5, `TestParseEmptyFileInputIgnored`.
3. **Submitting on a verified custom domain** rather than the view host. It must work with the same key, and the same key on another site's host must be a 404. → Task 10, `TestSubmitOnCustomDomain`.
4. **A confirmation link opened after the owner deleted the form or changed its recipient.** It must show a clear "no longer valid" page, not a 500 and not a confirmation. → Task 11, `TestConfirmAfterDeleteOrRecipientChange`.
5. **A thank-you path with a query string or fragment** (`/danke.html?sent=1#top`). It is accepted and redirected to verbatim. → Task 4, `TestCleanRedirect`, and Task 10, `TestSubmitRedirectsToConfiguredPath`.

## File Map

| File | Responsibility | Task |
|---|---|---|
| `internal/config/config.go` (+test) | `SITEBIN_FORMS_*` | 1 |
| `internal/store/forms.go` (+test) | `Form` record, add, update, delete, confirm, stop | 2 |
| `internal/store/meta.go`, `expiry.go` | `Forms`, `QuotaForms`, `Quota.Forms` | 2, 3 |
| `internal/ext/ext.go` | `CreateGrant.MaxForms` | 3 |
| `internal/httpapi/sites.go`, `siteservice.go` | stamp and restamp `QuotaForms` | 3 |
| `internal/cleanup/cleanup.go` (+test) | reconcile passes `Forms` | 3 |
| `ee/eeconfig/eeconfig.go`, `ee/provider.go` (+tests) | `max_forms` → grant | 3 |
| `internal/forms/rules.go` (+test) | name, recipient and redirect rules, `Snippet` | 4 |
| `internal/forms/links.go` (+test) | confirm and stop tokens | 4 |
| `internal/forms/parse.go` (+test) | ordered body parser | 5 |
| `internal/forms/message.go`, `templates/*.html` (+test) | MIME building | 6 |
| `internal/forms/smtp.go` (+test) | SMTP delivery with a deadline | 7 |
| `internal/forms/captcha.go` (+test), `web/vendor/altcha.*` | ALTCHA v2 | 8 |
| `internal/httpapi/forms.go` | forms state, cap resolution, shared helpers | 9 |
| `internal/httpapi/formsapi.go` (+test) | JSON API | 9 |
| `internal/httpapi/formsubmit.go` (+test) | public endpoint, challenge, thanks, widget | 10 |
| `internal/httpapi/formconsent.go` (+test), `gate.go` | confirm and stop pages | 11 |
| `internal/mcp/ops.go`, `server.go` (+test), `internal/httpapi/mcpops.go` (+test) | five tools | 12 |
| `web/static/edit.html`, `edit.js`, `app.css` | Forms card | 13 |
| `internal/forms/preview/main.go`, `README.md`, `CLAUDE.md` | mail preview, docs | 14 |
| `e2e/forms.ps1`, `e2e/tiers.ps1` | E2E | 15 |
| `Sitebin-Website/public/docs/forms/`, `pricing/`, `docs/{mcp,api,configuration}/`, `sitemap.xml` | website | 16 |
| `Sitebin-Website/public/{privacy,dpa,terms}/` | legal drafts | 17 |

---
### Task 1: Forms configuration

**Files:**
- Modify: `internal/config/config.go`: `Config` fields, `FormsSMTP` type, the `Load` block, and `formsSMTP()`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.FormsSMTP{Host string; Port int; User, Pass, From string; TLS bool}` and these `Config` fields: `FormsSMTP *FormsSMTP` (nil = forms off), `FormsMaxPerSite *int` (nil = unset), `FormsMaxFiles int`, `FormsMaxFileBytes int64`, `FormsPerIPHour int`, `FormsPerFormHour int`.

- [ ] **Step 1: Write the failing tests** (append to `internal/config/config_test.go`)

```go
func formsBase() map[string]string {
	return map[string]string{"SITEBIN_BASE_DOMAIN": "sitebin.example", "SITEBIN_HTTP_ONLY": "true"}
}

func TestFormsOffByDefault(t *testing.T) {
	cfg, err := Load(env(formsBase()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FormsSMTP != nil {
		t.Errorf("FormsSMTP = %+v, want nil: forms are off until SITEBIN_FORMS_SMTP_HOST is set", cfg.FormsSMTP)
	}
	if cfg.FormsMaxPerSite != nil {
		t.Errorf("FormsMaxPerSite = %d, want unset", *cfg.FormsMaxPerSite)
	}
	if cfg.FormsMaxFiles != 5 || cfg.FormsMaxFileBytes != 2097152 || cfg.FormsPerIPHour != 10 || cfg.FormsPerFormHour != 60 {
		t.Errorf("defaults = files %d, bytes %d, ip %d, form %d", cfg.FormsMaxFiles, cfg.FormsMaxFileBytes, cfg.FormsPerIPHour, cfg.FormsPerFormHour)
	}
}

func TestFormsSMTPFullySet(t *testing.T) {
	vars := formsBase()
	for k, v := range map[string]string{
		"SITEBIN_FORMS_SMTP_HOST":      "smtp.example.com",
		"SITEBIN_FORMS_SMTP_PORT":      "465",
		"SITEBIN_FORMS_SMTP_USER":      "u",
		"SITEBIN_FORMS_SMTP_PASS":      "p",
		"SITEBIN_FORMS_SMTP_FROM":      "forms@example.com",
		"SITEBIN_FORMS_SMTP_TLS":       "true",
		"SITEBIN_FORMS_MAX_PER_SITE":   "0",
		"SITEBIN_FORMS_MAX_FILES":      "0",
		"SITEBIN_FORMS_MAX_FILE_BYTES": "1000",
		"SITEBIN_FORMS_PER_IP_HOUR":    "3",
		"SITEBIN_FORMS_PER_FORM_HOUR":  "4",
	} {
		vars[k] = v
	}
	cfg, err := Load(env(vars))
	if err != nil {
		t.Fatal(err)
	}
	want := FormsSMTP{Host: "smtp.example.com", Port: 465, User: "u", Pass: "p", From: "forms@example.com", TLS: true}
	if cfg.FormsSMTP == nil || *cfg.FormsSMTP != want {
		t.Errorf("FormsSMTP = %+v, want %+v", cfg.FormsSMTP, want)
	}
	// An explicit 0 is a decision ("no forms here"), not "unset".
	if cfg.FormsMaxPerSite == nil || *cfg.FormsMaxPerSite != 0 {
		t.Errorf("FormsMaxPerSite = %v, want an explicit 0", cfg.FormsMaxPerSite)
	}
	if cfg.FormsMaxFiles != 0 || cfg.FormsMaxFileBytes != 1000 || cfg.FormsPerIPHour != 3 || cfg.FormsPerFormHour != 4 {
		t.Errorf("limits = %d %d %d %d", cfg.FormsMaxFiles, cfg.FormsMaxFileBytes, cfg.FormsPerIPHour, cfg.FormsPerFormHour)
	}
}

func TestFormsSMTPDefaults(t *testing.T) {
	vars := formsBase()
	vars["SITEBIN_FORMS_SMTP_HOST"] = "smtp.example.com"
	vars["SITEBIN_FORMS_SMTP_FROM"] = "forms@example.com"
	cfg, err := Load(env(vars))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FormsSMTP.Port != 587 || cfg.FormsSMTP.TLS {
		t.Errorf("port %d tls %v, want 587 and STARTTLS", cfg.FormsSMTP.Port, cfg.FormsSMTP.TLS)
	}
}

func TestFormsConfigRefusals(t *testing.T) {
	withHost := func(extra map[string]string) map[string]string {
		m := map[string]string{"SITEBIN_FORMS_SMTP_HOST": "smtp.example.com", "SITEBIN_FORMS_SMTP_FROM": "forms@example.com"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := map[string]map[string]string{
		"from missing":            {"SITEBIN_FORMS_SMTP_HOST": "smtp.example.com"},
		"from with display name":  withHost(map[string]string{"SITEBIN_FORMS_SMTP_FROM": "Forms <forms@example.com>"}),
		"from in angle brackets":  withHost(map[string]string{"SITEBIN_FORMS_SMTP_FROM": "<forms@example.com>"}),
		"from not an address":     withHost(map[string]string{"SITEBIN_FORMS_SMTP_FROM": "forms"}),
		"port out of range":       withHost(map[string]string{"SITEBIN_FORMS_SMTP_PORT": "99999"}),
		"tls not a bool":          withHost(map[string]string{"SITEBIN_FORMS_SMTP_TLS": "maybe"}),
		"negative per-site":       {"SITEBIN_FORMS_MAX_PER_SITE": "-1"},
		"per-site not a number":   {"SITEBIN_FORMS_MAX_PER_SITE": "ten"},
		"negative max files":      {"SITEBIN_FORMS_MAX_FILES": "-1"},
		"zero file bytes":         {"SITEBIN_FORMS_MAX_FILE_BYTES": "0"},
		"zero per-ip":             {"SITEBIN_FORMS_PER_IP_HOUR": "0"},
		"zero per-form":           {"SITEBIN_FORMS_PER_FORM_HOUR": "0"},
	}
	for name, extra := range cases {
		vars := formsBase()
		for k, v := range extra {
			vars[k] = v
		}
		if _, err := Load(env(vars)); err == nil {
			t.Errorf("%s: accepted, want a startup error", name)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestForms' -v`
Expected: build failure, `cfg.FormsSMTP undefined`.

- [ ] **Step 3: Implement**

In `internal/config/config.go`, add `"net/mail"` to the imports. Add the fields at the end of `Config`, after `EmbedOrigins`:

```go
	// Forms (SITEBIN_FORMS_*): email form submissions for hosted sites. See
	// docs/superpowers/specs/2026-09-24-site-forms-design.md. FormsSMTP nil
	// means the instance has no forms at all.
	FormsSMTP *FormsSMTP
	// FormsMaxPerSite is the forms cap for a site with no stamped
	// quota_forms. Nil means unset: httpapi then uses 10 with no extension
	// provider and 0 with one, so existing sites on a tiers instance do not
	// all gain forms the day this ships.
	FormsMaxPerSite   *int
	FormsMaxFiles     int   // attachments per submission; 0 = none instance-wide
	FormsMaxFileBytes int64 // bytes per attachment
	FormsPerIPHour    int   // submissions per client IP per hour, all forms
	FormsPerFormHour  int   // submissions per form per hour
```

Add the type after the `Config` struct:

```go
// FormsSMTP is the forms mailer's server (SITEBIN_FORMS_SMTP_*). It is
// deliberately separate from the enterprise account mailer (SITEBIN_SMTP_*):
// the two send different mail to different people and may use different
// accounts.
type FormsSMTP struct {
	Host string
	Port int
	User string
	Pass string
	From string // a bare address; each form supplies the display name
	TLS  bool   // implicit TLS (port 465); otherwise STARTTLS when offered
}
```

In `Load`, directly after the `SITEBIN_ZONE_NAMES_PER_HOUR` block:

```go
	if cfg.FormsSMTP, err = formsSMTP(getenv); err != nil {
		return cfg, err
	}
	if v := strings.TrimSpace(getenv("SITEBIN_FORMS_MAX_PER_SITE")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 0 {
			return cfg, fmt.Errorf("SITEBIN_FORMS_MAX_PER_SITE: %q is not a non-negative integer", v)
		}
		cfg.FormsMaxPerSite = &n
	}
	if cfg.FormsMaxFiles, err = intVar(getenv, "SITEBIN_FORMS_MAX_FILES", 5); err != nil {
		return cfg, err
	}
	if cfg.FormsMaxFiles < 0 {
		return cfg, fmt.Errorf("SITEBIN_FORMS_MAX_FILES must not be negative")
	}
	if cfg.FormsMaxFileBytes, err = int64Var(getenv, "SITEBIN_FORMS_MAX_FILE_BYTES", 2<<20); err != nil {
		return cfg, err
	}
	if cfg.FormsMaxFileBytes <= 0 {
		return cfg, fmt.Errorf("SITEBIN_FORMS_MAX_FILE_BYTES must be positive")
	}
	if cfg.FormsPerIPHour, err = intVar(getenv, "SITEBIN_FORMS_PER_IP_HOUR", 10); err != nil {
		return cfg, err
	}
	if cfg.FormsPerFormHour, err = intVar(getenv, "SITEBIN_FORMS_PER_FORM_HOUR", 60); err != nil {
		return cfg, err
	}
	if cfg.FormsPerIPHour < 1 || cfg.FormsPerFormHour < 1 {
		return cfg, fmt.Errorf("SITEBIN_FORMS_PER_IP_HOUR and SITEBIN_FORMS_PER_FORM_HOUR must be at least 1")
	}
```

Add the helper next to `boolVar`:

```go
// formsSMTP reads SITEBIN_FORMS_SMTP_*. It returns nil and no error when the
// host is unset: that is how an instance runs without forms.
func formsSMTP(getenv func(string) string) (*FormsSMTP, error) {
	host := strings.TrimSpace(getenv("SITEBIN_FORMS_SMTP_HOST"))
	if host == "" {
		return nil, nil
	}
	s := &FormsSMTP{Host: host, User: getenv("SITEBIN_FORMS_SMTP_USER"), Pass: getenv("SITEBIN_FORMS_SMTP_PASS")}
	var err error
	if s.Port, err = intVar(getenv, "SITEBIN_FORMS_SMTP_PORT", 587); err != nil {
		return nil, err
	}
	if s.Port < 1 || s.Port > 65535 {
		return nil, fmt.Errorf("SITEBIN_FORMS_SMTP_PORT: %d is not a port", s.Port)
	}
	if s.TLS, err = boolVar(getenv, "SITEBIN_FORMS_SMTP_TLS", false); err != nil {
		return nil, err
	}
	from := strings.TrimSpace(getenv("SITEBIN_FORMS_SMTP_FROM"))
	if from == "" {
		return nil, fmt.Errorf("SITEBIN_FORMS_SMTP_FROM is required when SITEBIN_FORMS_SMTP_HOST is set")
	}
	// A bare address only: the display name is each form's own, and the
	// address must be exactly what SPF/DKIM cover.
	if a, perr := mail.ParseAddress(from); perr != nil || a.Name != "" || a.Address != from {
		return nil, fmt.Errorf("SITEBIN_FORMS_SMTP_FROM: %q must be a bare address such as forms@example.com; each form supplies the display name", from)
	}
	s.From = from
	return s, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS. The existing config tests still pass too.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: forms read their own SMTP server and limits from SITEBIN_FORMS_*"
```

(Every commit in this plan gets the two trailer lines from Global Constraints. Use `git commit -F -` with a heredoc.)

---

### Task 2: Forms in the store

**Files:**
- Create: `internal/store/forms.go`
- Modify: `internal/store/meta.go`: add `Forms` to `Meta`
- Modify: `internal/httpapi/server.go`: `storeError` maps the new sentinels
- Test: `internal/store/forms_test.go`

**Interfaces:**
- Produces (package `store`):
  - constants `FormPending`, `FormActive`, `FormStopped`, and `FormKeyLen = 16`
  - `type Form struct{ Key, Name, Recipient string; Captcha, Files bool; Redirect, Status string; Seq int; CreatedAt time.Time; ConfirmedAt, StoppedAt *time.Time }`
  - `type FormPatch struct{ Name, Recipient, Redirect *string; Captcha, Files *bool }`
  - errors `ErrFormNotFound`, `ErrTooManyForms`, `ErrFormStale`, `ErrFormActive`
  - `func FindForm(m Meta, key string) (Form, int, bool)`
  - `func FormPaused(index, limit int) bool`
  - `func (s *Store) AddForm(site *Site, spec Form, limit int) (Form, error)`: reads only Name, Recipient, Captcha, Files and Redirect from `spec`
  - `func (s *Store) UpdateForm(site *Site, key string, p FormPatch) (Form, bool, error)`: the bool is `recipientChanged`
  - `func (s *Store) DeleteForm(site *Site, key string) error`
  - `func (s *Store) ConfirmForm(site *Site, key, recipient string, seq int) (Form, error)`
  - `func (s *Store) StopForm(site *Site, key, recipient string) (Form, bool, error)`: the bool is `changed`
  - `func (s *Store) RequestConfirmation(site *Site, key string) (Form, error)`
- Validation of names, addresses and paths is **not** here. `internal/forms` owns it (Task 4), and the callers validate before they store.

- [ ] **Step 1: Write the failing tests** (`internal/store/forms_test.go`)

```go
package store

import (
	"errors"
	"testing"
)

func formSite(t *testing.T) (*Store, *Site) {
	t.Helper()
	s := newTestStore(t)
	site, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	return s, site
}

func addForm(t *testing.T, s *Store, site *Site, name, to string) Form {
	t.Helper()
	f, err := s.AddForm(site, Form{Name: name, Recipient: to}, 10)
	if err != nil {
		t.Fatalf("AddForm: %v", err)
	}
	return f
}

func TestAddFormIsPendingWithAKey(t *testing.T) {
	s, site := formSite(t)
	f, err := s.AddForm(site, Form{Name: "Contact", Recipient: "a@example.com", Captcha: true, Redirect: "/thanks.html",
		// fields AddForm must ignore:
		Key: "chosen", Status: FormActive, Seq: 9}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Key) != FormKeyLen || f.Key == "chosen" {
		t.Errorf("key %q: want a fresh %d-character key", f.Key, FormKeyLen)
	}
	if f.Status != FormPending || f.Seq != 0 || f.CreatedAt.IsZero() || f.ConfirmedAt != nil {
		t.Errorf("new form = %+v, want pending, seq 0, created now", f)
	}
	if !f.Captcha || f.Redirect != "/thanks.html" || f.Name != "Contact" {
		t.Errorf("settings not kept: %+v", f)
	}
	again, _ := s.ByViewID(site.ViewID)
	if len(again.Meta.Forms) != 1 || again.Meta.Forms[0].Key != f.Key {
		t.Errorf("not persisted: %+v", again.Meta.Forms)
	}
}

func TestAddFormRefusesAtTheLimit(t *testing.T) {
	s, site := formSite(t)
	if _, err := s.AddForm(site, Form{Name: "A", Recipient: "a@example.com"}, 1); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddForm(site, Form{Name: "B", Recipient: "b@example.com"}, 1)
	if !errors.Is(err, ErrTooManyForms) {
		t.Fatalf("second form at limit 1: err = %v, want ErrTooManyForms", err)
	}
	if _, err := s.AddForm(site, Form{Name: "C", Recipient: "c@example.com"}, 0); !errors.Is(err, ErrTooManyForms) {
		t.Errorf("limit 0 must refuse: %v", err)
	}
}

func TestFormKeysAreUniqueWithinASite(t *testing.T) {
	s, site := formSite(t)
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		f := addForm(t, s, site, "F", "a@example.com")
		if seen[f.Key] {
			t.Fatalf("duplicate key %q", f.Key)
		}
		seen[f.Key] = true
	}
}

func TestUpdateFormRecipientResetsConsent(t *testing.T) {
	s, site := formSite(t)
	f := addForm(t, s, site, "Contact", "a@example.com")
	if _, err := s.ConfirmForm(site, f.Key, "a@example.com", 0); err != nil {
		t.Fatal(err)
	}
	to := "b@example.com"
	g, changed, err := s.UpdateForm(site, f.Key, FormPatch{Recipient: &to})
	if err != nil || !changed {
		t.Fatalf("UpdateForm: changed=%v err=%v", changed, err)
	}
	if g.Status != FormPending || g.Seq != 1 || g.ConfirmedAt != nil || g.Recipient != to {
		t.Errorf("after recipient change = %+v, want pending, seq 1, unconfirmed", g)
	}
}

func TestUpdateFormOtherFieldsKeepConsent(t *testing.T) {
	s, site := formSite(t)
	f := addForm(t, s, site, "Contact", "a@example.com")
	s.ConfirmForm(site, f.Key, "a@example.com", 0)
	name, same, yes := "Kontakt", "a@example.com", true
	g, changed, err := s.UpdateForm(site, f.Key, FormPatch{Name: &name, Recipient: &same, Files: &yes})
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v, want no recipient change", changed, err)
	}
	if g.Status != FormActive || g.Seq != 0 || g.Name != "Kontakt" || !g.Files {
		t.Errorf("after settings change = %+v", g)
	}
}

func TestDeleteForm(t *testing.T) {
	s, site := formSite(t)
	a := addForm(t, s, site, "A", "a@example.com")
	b := addForm(t, s, site, "B", "b@example.com")
	if err := s.DeleteForm(site, a.Key); err != nil {
		t.Fatal(err)
	}
	if len(site.Meta.Forms) != 1 || site.Meta.Forms[0].Key != b.Key {
		t.Errorf("forms after delete = %+v", site.Meta.Forms)
	}
	if err := s.DeleteForm(site, a.Key); !errors.Is(err, ErrFormNotFound) {
		t.Errorf("deleting twice: %v, want ErrFormNotFound", err)
	}
}

func TestConfirmForm(t *testing.T) {
	s, site := formSite(t)
	f := addForm(t, s, site, "Contact", "a@example.com")
	if _, err := s.ConfirmForm(site, f.Key, "a@example.com", 1); !errors.Is(err, ErrFormStale) {
		t.Errorf("wrong seq: %v, want ErrFormStale", err)
	}
	if _, err := s.ConfirmForm(site, f.Key, "x@example.com", 0); !errors.Is(err, ErrFormStale) {
		t.Errorf("wrong recipient: %v, want ErrFormStale", err)
	}
	g, err := s.ConfirmForm(site, f.Key, "a@example.com", 0)
	if err != nil || g.Status != FormActive || g.ConfirmedAt == nil {
		t.Fatalf("confirm = %+v, %v", g, err)
	}
	// A second click on the same link is a success, not an error.
	if h, err := s.ConfirmForm(site, f.Key, "a@example.com", 0); err != nil || h.Status != FormActive {
		t.Errorf("second confirm = %+v, %v", h, err)
	}
	if _, err := s.ConfirmForm(site, "nosuchkey", "a@example.com", 0); !errors.Is(err, ErrFormNotFound) {
		t.Errorf("unknown key: %v", err)
	}
}

func TestStopFormAndReconfirm(t *testing.T) {
	s, site := formSite(t)
	f := addForm(t, s, site, "Contact", "a@example.com")
	s.ConfirmForm(site, f.Key, "a@example.com", 0)

	if g, changed, err := s.StopForm(site, f.Key, "someone-else@example.com"); err != nil || changed || g.Status != FormActive {
		t.Errorf("stop by a former address = %+v changed=%v err=%v, want no change", g, changed, err)
	}
	g, changed, err := s.StopForm(site, f.Key, "a@example.com")
	if err != nil || !changed || g.Status != FormStopped || g.StoppedAt == nil || g.Seq != 1 {
		t.Fatalf("stop = %+v changed=%v err=%v", g, changed, err)
	}
	if _, changed, _ := s.StopForm(site, f.Key, "a@example.com"); changed {
		t.Error("stopping twice reported a change")
	}
	// The pre-stop confirmation link (seq 0) can never re-activate the form.
	if _, err := s.ConfirmForm(site, f.Key, "a@example.com", 0); !errors.Is(err, ErrFormStale) {
		t.Errorf("old link after stop: %v, want ErrFormStale", err)
	}
	h, err := s.RequestConfirmation(site, f.Key)
	if err != nil || h.Status != FormPending || h.Seq != 1 || h.StoppedAt != nil {
		t.Fatalf("request after stop = %+v, %v", h, err)
	}
	if k, err := s.ConfirmForm(site, f.Key, "a@example.com", 1); err != nil || k.Status != FormActive {
		t.Errorf("confirm with the new link = %+v, %v", k, err)
	}
	if _, err := s.RequestConfirmation(site, f.Key); !errors.Is(err, ErrFormActive) {
		t.Errorf("request on an active form: %v, want ErrFormActive", err)
	}
}

func TestFindFormAndPaused(t *testing.T) {
	s, site := formSite(t)
	a := addForm(t, s, site, "A", "a@example.com")
	b := addForm(t, s, site, "B", "b@example.com")
	if _, i, ok := FindForm(site.Meta, b.Key); !ok || i != 1 {
		t.Errorf("FindForm(b) = %d %v", i, ok)
	}
	if _, _, ok := FindForm(site.Meta, "nope"); ok {
		t.Error("found a missing key")
	}
	_, ia, _ := FindForm(site.Meta, a.Key)
	if FormPaused(ia, 1) || !FormPaused(1, 1) || !FormPaused(0, 0) {
		t.Error("FormPaused: the first N forms run, the rest pause")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run 'Form' -v`
Expected: build failure, `undefined: Form`.

- [ ] **Step 3: Implement**

In `internal/store/meta.go`, add to `Meta` after `Container`:

```go
	// Forms are the site's email forms, in creation order: the order decides
	// which forms a smaller plan pauses. See forms.go.
	Forms []Form `json:"forms,omitempty"`
```

Create `internal/store/forms.go`:

```go
package store

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ittrail/sitebin.io/internal/ids"
)

// Form states. The state a person also sees, "paused", is computed from the
// site's cap and never stored (FormPaused).
// Design: docs/superpowers/specs/2026-09-24-site-forms-design.md.
const (
	FormPending = "pending" // waiting for the recipient to confirm
	FormActive  = "active"  // the recipient confirmed; submissions are mailed
	FormStopped = "stopped" // the recipient used the stop link
)

// FormKeyLen is the length of a form key, drawn from the id alphabet.
const FormKeyLen = 16

// Form is one form a site's pages post to. Its key is public (it sits in the
// page's HTML), so nothing here is a secret.
type Form struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Recipient string `json:"recipient"`
	Captcha   bool   `json:"captcha,omitempty"`
	Files     bool   `json:"files,omitempty"`
	Redirect  string `json:"redirect,omitempty"`
	Status    string `json:"status"`
	// Seq moves when the recipient changes and when the form is stopped. A
	// confirmation link carries the seq it was minted for, so both kill every
	// earlier link. Confirming does not move it: a second click on the same
	// link is still a success.
	Seq         int        `json:"seq,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	StoppedAt   *time.Time `json:"stopped_at,omitempty"`
}

// FormPatch changes a form's settings. A nil field is left alone.
type FormPatch struct {
	Name      *string
	Recipient *string
	Captcha   *bool
	Files     *bool
	Redirect  *string
}

var (
	ErrFormNotFound = errors.New("no form with that key on this site")
	ErrTooManyForms = errors.New("form limit reached")
	// ErrFormStale refuses a confirmation link minted before the form's
	// recipient changed or the form was stopped.
	ErrFormStale = errors.New("this link is no longer valid for this form")
	// ErrFormActive refuses a new confirmation for a form whose recipient
	// already confirmed.
	ErrFormActive = errors.New("this form's recipient has already confirmed")

	errFormUnchanged = errors.New("unchanged") // internal: skip the write
)

// FindForm returns the form with key and its position in the site's list.
func FindForm(m Meta, key string) (Form, int, bool) {
	for i, f := range m.Forms {
		if f.Key == key {
			return f, i, true
		}
	}
	return Form{}, -1, false
}

// FormPaused reports whether the form at index is beyond the site's cap. A
// smaller plan pauses the newest forms; nothing is ever deleted over a cap.
func FormPaused(index, limit int) bool { return index >= limit }

// AddForm appends a new pending form. Only the settings are taken from spec:
// the key, state and timestamps are the store's. limit is the site's forms
// cap, resolved by the caller, and it is compared under the site lock, so
// two concurrent adds cannot both take the last slot.
func (s *Store) AddForm(site *Site, spec Form, limit int) (Form, error) {
	var out Form
	err := s.Update(site, func(m *Meta) error {
		if len(m.Forms) >= limit {
			return fmt.Errorf("%w: this site's plan allows %d form(s)", ErrTooManyForms, limit)
		}
		out = Form{
			Key:       newFormKey(m.Forms),
			Name:      spec.Name,
			Recipient: spec.Recipient,
			Captcha:   spec.Captcha,
			Files:     spec.Files,
			Redirect:  spec.Redirect,
			Status:    FormPending,
			CreatedAt: time.Now().UTC(),
		}
		m.Forms = append(m.Forms, out)
		return nil
	})
	return out, err
}

func newFormKey(existing []Form) string {
	for {
		k := ids.New()[:FormKeyLen]
		if !slices.ContainsFunc(existing, func(f Form) bool { return f.Key == k }) {
			return k
		}
	}
}

// UpdateForm applies p. A new recipient puts the form back to pending and
// moves its seq, so every earlier confirmation link dies; recipientChanged
// tells the caller to send a fresh one.
func (s *Store) UpdateForm(site *Site, key string, p FormPatch) (f Form, recipientChanged bool, err error) {
	err = s.Update(site, func(m *Meta) error {
		_, i, ok := FindForm(*m, key)
		if !ok {
			return ErrFormNotFound
		}
		g := &m.Forms[i]
		if p.Name != nil {
			g.Name = *p.Name
		}
		if p.Captcha != nil {
			g.Captcha = *p.Captcha
		}
		if p.Files != nil {
			g.Files = *p.Files
		}
		if p.Redirect != nil {
			g.Redirect = *p.Redirect
		}
		if p.Recipient != nil && *p.Recipient != g.Recipient {
			g.Recipient = *p.Recipient
			g.Status = FormPending
			g.Seq++
			g.ConfirmedAt, g.StoppedAt = nil, nil
			recipientChanged = true
		}
		f = *g
		return nil
	})
	return f, recipientChanged, err
}

// DeleteForm removes a form. Its outstanding links find nothing afterwards.
func (s *Store) DeleteForm(site *Site, key string) error {
	return s.Update(site, func(m *Meta) error {
		_, i, ok := FindForm(*m, key)
		if !ok {
			return ErrFormNotFound
		}
		m.Forms = slices.Delete(m.Forms, i, i+1)
		return nil
	})
}

// ConfirmForm records the recipient's consent. The link carries the recipient
// and seq it was minted for; if either differs, the link predates a recipient
// change or a stop and is refused. Confirming an active form again is not an
// error.
func (s *Store) ConfirmForm(site *Site, key, recipient string, seq int) (Form, error) {
	var out Form
	err := s.Update(site, func(m *Meta) error {
		_, i, ok := FindForm(*m, key)
		if !ok {
			return ErrFormNotFound
		}
		g := &m.Forms[i]
		if g.Recipient != recipient || g.Seq != seq || g.Status == FormStopped {
			return ErrFormStale
		}
		if g.Status == FormActive {
			out = *g
			return errFormUnchanged
		}
		now := time.Now().UTC()
		g.Status, g.ConfirmedAt = FormActive, &now
		out = *g
		return nil
	})
	if errors.Is(err, errFormUnchanged) {
		err = nil
	}
	return out, err
}

// StopForm is the recipient withdrawing consent. A stop link carries no seq,
// because it must keep working from an old mail, so it is checked against the
// address alone. A link for an address that is no longer the recipient
// changes nothing. changed is false then, and when the form was already
// stopped.
func (s *Store) StopForm(site *Site, key, recipient string) (f Form, changed bool, err error) {
	err = s.Update(site, func(m *Meta) error {
		_, i, ok := FindForm(*m, key)
		if !ok {
			return ErrFormNotFound
		}
		g := &m.Forms[i]
		if g.Recipient != recipient || g.Status == FormStopped {
			f = *g
			return errFormUnchanged
		}
		now := time.Now().UTC()
		g.Status, g.StoppedAt = FormStopped, &now
		g.Seq++
		f, changed = *g, true
		return nil
	})
	if errors.Is(err, errFormUnchanged) {
		err = nil
	}
	return f, changed, err
}

// RequestConfirmation readies a form for a new confirmation mail. A stopped
// form goes back to pending (its seq already moved at the stop), a pending
// one stays as it is, and an active one is refused.
func (s *Store) RequestConfirmation(site *Site, key string) (Form, error) {
	var out Form
	err := s.Update(site, func(m *Meta) error {
		_, i, ok := FindForm(*m, key)
		if !ok {
			return ErrFormNotFound
		}
		g := &m.Forms[i]
		switch g.Status {
		case FormActive:
			return ErrFormActive
		case FormStopped:
			g.Status, g.StoppedAt = FormPending, nil
		}
		out = *g
		return nil
	})
	return out, err
}
```

In `internal/httpapi/server.go`, extend `storeError`'s switch before `default`:

```go
	case errors.Is(err, store.ErrFormNotFound):
		writeError(w, 404, err.Error())
	case errors.Is(err, store.ErrTooManyForms):
		writeError(w, 403, err.Error())
	case errors.Is(err, store.ErrFormActive):
		writeError(w, 409, err.Error())
	case errors.Is(err, store.ErrFormStale):
		writeError(w, 410, err.Error())
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/store/ ./internal/httpapi/ -v -run 'Form|TestCreate'`
Expected: PASS. Then run the whole suite: `go test ./... && go test -tags ee ./...`, which must also PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/forms.go internal/store/forms_test.go internal/store/meta.go internal/httpapi/server.go
git commit -m "feat: a site stores its forms with the recipient's consent state"
```

---

### Task 3: The per-site forms cap is stamped from the tier

**Files:**
- Modify: `internal/ext/ext.go`: `CreateGrant.MaxForms`
- Modify: `internal/store/meta.go`: `QuotaForms`
- Modify: `internal/store/expiry.go`: `Quota.Forms`, and `ApplyQuota` writes it
- Modify: `internal/httpapi/sites.go`: `createSiteWith` stamps it
- Modify: `internal/httpapi/siteservice.go`: `quotaFromGrant` passes it
- Modify: `internal/cleanup/cleanup.go`: `reconcile` passes it
- Modify: `ee/eeconfig/eeconfig.go`: `Tier.MaxForms`
- Modify: `ee/provider.go`: `grantFromTier`
- Test: `internal/store/forms_test.go`, `internal/cleanup/cleanup_test.go`, `internal/httpapi/formsquota_test.go` (new), `ee/tiers_test.go`

**Interfaces:**
- Consumes: Task 2 (`store.Meta` exists with `Forms`).
- Produces: `ext.CreateGrant.MaxForms *int`, `store.Meta.QuotaForms *int` (`json:"quota_forms,omitempty"`), `store.Quota.Forms *int`, and `eeconfig.Tier.MaxForms int` (`json:"max_forms,omitempty"`).

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/forms_test.go`:

```go
func TestApplyQuotaStampsForms(t *testing.T) {
	s, site := formSite(t)
	three := 3
	if err := s.ApplyQuota(site, Quota{Forms: &three}, 0); err != nil {
		t.Fatal(err)
	}
	if site.Meta.QuotaForms == nil || *site.Meta.QuotaForms != 3 {
		t.Fatalf("QuotaForms = %v, want 3", site.Meta.QuotaForms)
	}
	// ApplyQuota writes every field it is given — which is exactly why every
	// caller has to pass Forms (see the cleanup and siteservice tests).
	if err := s.ApplyQuota(site, Quota{}, 0); err != nil {
		t.Fatal(err)
	}
	if site.Meta.QuotaForms != nil {
		t.Errorf("QuotaForms = %d after a grant without it, want nil", *site.Meta.QuotaForms)
	}
}
```

Append to `internal/cleanup/cleanup_test.go`:

```go
func TestSweepReconcileKeepsTheFormsCap(t *testing.T) {
	ten := 10
	p := &stubProvider{grant: ext.CreateGrant{MaxExpiryDays: 0, MaxForms: &ten}, ok: true}
	ext.Register(p)
	defer ext.Reset()
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	site := expiredOwnedSite(t, st, now, "acct-1", 7, true)
	if _, err := Sweep(st, now); err != nil {
		t.Fatal(err)
	}
	got, err := st.ByViewID(site.ViewID)
	if err != nil {
		t.Fatalf("site deleted: %v", err)
	}
	if got.Meta.QuotaForms == nil || *got.Meta.QuotaForms != 10 {
		t.Fatalf("QuotaForms = %v after reconcile, want the plan's 10", got.Meta.QuotaForms)
	}
}
```

Create `internal/httpapi/formsquota_test.go`:

```go
package httpapi

import (
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

func intp(n int) *int { return &n }

func TestCreateStampsTheFormsCap(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxForms: intp(1)}})
	defer ext.Reset()
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.QuotaForms == nil || *site.Meta.QuotaForms != 1 {
		t.Fatalf("QuotaForms = %v, want 1 from the grant", site.Meta.QuotaForms)
	}
}

func TestSiteServiceApplyQuotaCarriesTheFormsCap(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	if err := (siteService{a: e.api}).ApplyQuota(c.ID, ext.CreateGrant{MaxForms: intp(2)}); err != nil {
		t.Fatal(err)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.QuotaForms == nil || *site.Meta.QuotaForms != 2 {
		t.Fatalf("QuotaForms = %v, want 2 after a tier change", site.Meta.QuotaForms)
	}
}
```

Check that `siteService` is constructed as `siteService{a: ...}`: run `grep -n "type siteService" internal/httpapi/siteservice.go`. If its field has another name, use that name.

Append to `ee/tiers_test.go`:

```go
func TestGrantFromTierCarriesMaxForms(t *testing.T) {
	g := grantFromTier("acct", eeconfig.Tier{ID: "studio", MaxForms: 10})
	if g.MaxForms == nil || *g.MaxForms != 10 {
		t.Fatalf("MaxForms = %v, want 10", g.MaxForms)
	}
	// A tier without the field stamps an explicit 0 — "none" — never nil,
	// which would fall back to the instance default.
	g = grantFromTier("acct", eeconfig.Tier{ID: "free"})
	if g.MaxForms == nil || *g.MaxForms != 0 {
		t.Fatalf("MaxForms = %v for a tier without max_forms, want an explicit 0", g.MaxForms)
	}
	var tier eeconfig.Tier
	if err := json.Unmarshal([]byte(`{"id":"pro","max_forms":1}`), &tier); err != nil || tier.MaxForms != 1 {
		t.Fatalf("max_forms not parsed: %+v %v", tier, err)
	}
}
```

Add `"encoding/json"` and `"github.com/ittrail/sitebin.io/ee/eeconfig"` to the imports of `ee/tiers_test.go` if they are not there yet.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ ./internal/cleanup/ ./internal/httpapi/ -run 'Forms|FormsCap' && go test -tags ee ./ee/ -run TestGrantFromTierCarriesMaxForms`
Expected: build failures, `unknown field Forms in struct literal of type Quota`, `unknown field MaxForms`.

- [ ] **Step 3: Implement**

`internal/ext/ext.go`: in `CreateGrant`, after `WebDAV`:

```go
	// MaxForms caps the site's email forms (tier max_forms). nil = inherit the
	// instance default; a value, 0 included, is an explicit cap.
	MaxForms *int
```

`internal/store/meta.go`: after `QuotaWebDAV`:

```go
	QuotaForms      *int       `json:"quota_forms,omitempty"` // nil = instance default; value (incl. 0) = explicit cap
```

`internal/store/expiry.go`: add `Forms *int` to `Quota` after `WebDAV`. In `ApplyQuota`, after `m.QuotaWebDAV = q.WebDAV`:

```go
		m.QuotaForms = q.Forms
```

`internal/httpapi/sites.go`: in `createSiteWith`, inside `if gated {`, after `m.QuotaWebDAV = grant.WebDAV`:

```go
				m.QuotaForms = grant.MaxForms
```

`internal/httpapi/siteservice.go`: in `quotaFromGrant`, add `Forms: g.MaxForms,`.

`internal/cleanup/cleanup.go`: in `reconcile`, add `Forms: grant.MaxForms,` to the `store.Quota{...}` literal.

`ee/eeconfig/eeconfig.go`: in `Tier`, after `MaxZones`:

```go
	// MaxForms caps each site's email forms. 0 / absent means NONE, the same
	// polarity as custom_domains: a free tier that forgets the field must not
	// send mail. Only meaningful with SITEBIN_FORMS_SMTP_HOST set.
	MaxForms int `json:"max_forms,omitempty"`
```

`ee/provider.go`: in `grantFromTier`, add `forms := t.MaxForms` next to `domains := t.CustomDomains`, and `MaxForms: &forms,` to the returned literal.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./... && go test -tags ee ./...`
Expected: PASS, including `TestSweepReconcileKeepsTheFormsCap`. That test fails if `reconcile` does not pass `Forms`. Check this by temporarily deleting the line: the test must go red.

- [ ] **Step 5: Commit**

```bash
git add internal/ext/ext.go internal/store/meta.go internal/store/expiry.go internal/store/forms_test.go \
  internal/httpapi/sites.go internal/httpapi/siteservice.go internal/httpapi/formsquota_test.go \
  internal/cleanup/cleanup.go internal/cleanup/cleanup_test.go \
  ee/eeconfig/eeconfig.go ee/provider.go ee/tiers_test.go
git commit -m "feat: a tier's max_forms is stamped on every site and follows tier changes"
```

---
### Task 4: Form rules, snippet and the recipient's link tokens

**Files:**
- Create: `internal/forms/rules.go`, `internal/forms/links.go`
- Test: `internal/forms/rules_test.go`, `internal/forms/links_test.go`

**Interfaces:**
- Produces (package `forms`):
  - `func CleanName(s string) (string, error)`, `func CleanRecipient(s string) (string, error)` and `func CleanRedirect(s string) (string, error)`, with errors `ErrBadName`, `ErrBadRecipient` and `ErrBadRedirect`, whose messages are shown to the owner verbatim
  - `type SnippetOptions struct{ Key string; Captcha, Files bool; SiteQuery string }` and `func Snippet(o SnippetOptions) string`
  - `type Links struct{…}`, `func NewLinks(secret []byte) Links`
  - `type ConfirmClaim struct{ ViewID, Key, Recipient string; Seq int }` and `type StopClaim struct{ ViewID, Key, Recipient string }`
  - `func (l Links) ConfirmToken(c ConfirmClaim, now time.Time) string` and `func (l Links) ParseConfirm(tok string, now time.Time) (ConfirmClaim, bool)`
  - `func (l Links) StopToken(c StopClaim, now time.Time) string` and `func (l Links) ParseStop(tok string, now time.Time) (StopClaim, bool)`
  - const `ConfirmTTL = 7 * 24 * time.Hour`

- [ ] **Step 1: Write the failing tests**

`internal/forms/rules_test.go`:

```go
package forms

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanName(t *testing.T) {
	for _, ok := range []string{"Contact", "  Kontakt – Anfrage ✉  ", strings.Repeat("ä", 60)} {
		if got, err := CleanName(ok); err != nil || got != strings.TrimSpace(ok) {
			t.Errorf("CleanName(%q) = %q, %v", ok, got, err)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("a", 61), "Contact\r\nBcc: x@evil.example", "a\x00b", "tab\there"} {
		if _, err := CleanName(bad); !errors.Is(err, ErrBadName) {
			t.Errorf("CleanName(%q) accepted", bad)
		}
	}
}

func TestCleanRecipient(t *testing.T) {
	if got, err := CleanRecipient("  office@example.com "); err != nil || got != "office@example.com" {
		t.Errorf("plain address = %q, %v", got, err)
	}
	for _, bad := range []string{"", "office", "Office <office@example.com>", "<office@example.com>",
		"a@example.com, b@example.com", "a@example.com\r\nBcc: x@evil.example"} {
		if _, err := CleanRecipient(bad); !errors.Is(err, ErrBadRecipient) {
			t.Errorf("CleanRecipient(%q) accepted", bad)
		}
	}
}

// Review Focus 5: a thank-you path with a query string or fragment is a
// perfectly ordinary thing to want.
func TestCleanRedirect(t *testing.T) {
	for _, ok := range []string{"", "/thanks.html", "/danke.html?sent=1#top", "/de/kontakt/danke/"} {
		if got, err := CleanRedirect(ok); err != nil || got != ok {
			t.Errorf("CleanRedirect(%q) = %q, %v", ok, got, err)
		}
	}
	for _, bad := range []string{"thanks.html", "//evil.example/x", "https://evil.example/", "/\\evil.example",
		"/a\nb", "/" + strings.Repeat("a", 512)} {
		if _, err := CleanRedirect(bad); !errors.Is(err, ErrBadRedirect) {
			t.Errorf("CleanRedirect(%q) accepted", bad)
		}
	}
}

func TestSnippetPlain(t *testing.T) {
	s := Snippet(SnippetOptions{Key: "k7f3m2q9xaw4npd6"})
	for _, want := range []string{`action="/_sitebin/forms/k7f3m2q9xaw4npd6"`, `method="post"`, `name="email"`, `name="_gotcha"`} {
		if !strings.Contains(s, want) {
			t.Errorf("snippet lacks %s:\n%s", want, s)
		}
	}
	for _, not := range []string{"enctype", "altcha", `type="file"`, " hidden"} {
		if strings.Contains(s, not) {
			t.Errorf("plain snippet contains %s:\n%s", not, s)
		}
	}
}

func TestSnippetWithEverything(t *testing.T) {
	s := Snippet(SnippetOptions{Key: "k1", Captcha: true, Files: true, SiteQuery: "?_site=v1"})
	for _, want := range []string{
		`action="/_sitebin/forms/k1?_site=v1"`,
		`enctype="multipart/form-data"`,
		`type="file" multiple`,
		`<altcha-widget challenge="/_sitebin/forms/k1/challenge?_site=v1"></altcha-widget>`,
		`<script type="module" src="/_sitebin/altcha.js"></script>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("snippet lacks %s:\n%s", want, s)
		}
	}
}
```

`internal/forms/links_test.go`:

```go
package forms

import (
	"strings"
	"testing"
	"time"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func TestConfirmTokenRoundTrip(t *testing.T) {
	l := NewLinks(testSecret)
	now := time.Now()
	c := ConfirmClaim{ViewID: "v1", Key: "k1", Recipient: "odd|name@example.com", Seq: 3}
	got, ok := l.ParseConfirm(l.ConfirmToken(c, now), now.Add(time.Hour))
	if !ok || got != c {
		t.Fatalf("round trip = %+v %v, want %+v", got, ok, c)
	}
	if _, ok := l.ParseConfirm(l.ConfirmToken(c, now), now.Add(ConfirmTTL+time.Minute)); ok {
		t.Error("a confirmation link must expire after 7 days")
	}
}

func TestStopTokenOutlivesYears(t *testing.T) {
	l := NewLinks(testSecret)
	now := time.Now()
	c := StopClaim{ViewID: "v1", Key: "k1", Recipient: "a@example.com"}
	got, ok := l.ParseStop(l.StopToken(c, now), now.Add(10*365*24*time.Hour))
	if !ok || got != c {
		t.Fatalf("a stop link from an old mail must keep working: %+v %v", got, ok)
	}
}

func TestLinkTokensArePurposeBound(t *testing.T) {
	l := NewLinks(testSecret)
	now := time.Now()
	stop := l.StopToken(StopClaim{ViewID: "v1", Key: "k1", Recipient: "a@example.com"}, now)
	if _, ok := l.ParseConfirm(stop, now); ok {
		t.Error("a stop token was accepted as a confirmation")
	}
	conf := l.ConfirmToken(ConfirmClaim{ViewID: "v1", Key: "k1", Recipient: "a@example.com"}, now)
	if _, ok := l.ParseStop(conf, now); ok {
		t.Error("a confirmation token was accepted as a stop")
	}
	if _, ok := l.ParseConfirm(conf[:len(conf)-2]+"xx", now); ok {
		t.Error("a tampered token was accepted")
	}
	other := NewLinks([]byte(strings.Repeat("z", 32)))
	if _, ok := other.ParseConfirm(conf, now); ok {
		t.Error("a token from another instance was accepted")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/forms/ -v`
Expected: build failure, `undefined: CleanName`.

- [ ] **Step 3: Implement**

`internal/forms/rules.go`:

```go
// Package forms is the logic of Sitebin's email forms, with no HTTP handlers
// and no store access: the rules for a form's settings, the recipient's link
// tokens, the ordered body parser, message building, SMTP delivery and the
// captcha. internal/httpapi wires it to requests and to the store.
//
// Design: docs/superpowers/specs/2026-09-24-site-forms-design.md.
package forms

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxNameRunes caps a form name, which is also the From display name.
const MaxNameRunes = 60

const maxRedirectLen = 512

var (
	ErrBadName      = errors.New("the form name must be 1 to 60 characters of plain text")
	ErrBadRecipient = errors.New("the recipient must be one email address, such as office@example.com")
	ErrBadRedirect  = errors.New("the thank-you page must be a path on this site, such as /thanks.html")
)

func hasControl(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }

// CleanName validates a form name. It becomes the From display name of every
// submission, so control characters, CR and LF above all, are refused rather
// than stripped: a stripped name would silently differ from what the owner
// typed.
func CleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n == 0 || n > MaxNameRunes || !utf8.ValidString(s) || hasControl(s) {
		return "", ErrBadName
	}
	return s, nil
}

// CleanRecipient validates a recipient: exactly one bare address.
func CleanRecipient(s string) (string, error) {
	s = strings.TrimSpace(s)
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s || hasControl(s) {
		return "", ErrBadRecipient
	}
	return s, nil
}

// CleanRedirect validates a thank-you path; empty means the default page. It
// must be a path on the same site, so the endpoint can never redirect a
// visitor anywhere else.
func CleanRedirect(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if len(s) > maxRedirectLen || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") ||
		strings.ContainsRune(s, '\\') || hasControl(s) {
		return "", ErrBadRedirect
	}
	return s, nil
}

// SnippetOptions describes the form whose HTML Snippet writes.
type SnippetOptions struct {
	Key     string
	Captcha bool
	Files   bool
	// SiteQuery is "?_site=<view id>" on a path-view instance, where the page
	// lives on the main domain and the endpoint cannot tell sites apart by
	// host. Empty everywhere else.
	SiteQuery string
}

// Snippet is the HTML a site owner pastes into a page. The edit page, the API
// and MCP all hand out this one string.
//
// The honeypot is moved off-screen rather than marked hidden: form bots skip
// hidden inputs and fill the visible-looking ones.
func Snippet(o SnippetOptions) string {
	action := "/_sitebin/forms/" + o.Key
	var b strings.Builder
	enctype := ""
	if o.Files {
		enctype = ` enctype="multipart/form-data"`
	}
	fmt.Fprintf(&b, "<form action=\"%s%s\" method=\"post\"%s>\n", action, o.SiteQuery, enctype)
	b.WriteString("  <label>Name <input name=\"name\" required></label>\n")
	b.WriteString("  <label>Email <input name=\"email\" type=\"email\" required></label>\n")
	b.WriteString("  <label>Message <textarea name=\"message\" required></textarea></label>\n")
	if o.Files {
		b.WriteString("  <label>Attachments <input name=\"attachments\" type=\"file\" multiple></label>\n")
	}
	b.WriteString("  <input name=\"_gotcha\" tabindex=\"-1\" autocomplete=\"off\" aria-hidden=\"true\" style=\"position:absolute;left:-9999px\">\n")
	if o.Captcha {
		fmt.Fprintf(&b, "  <altcha-widget challenge=\"%s/challenge%s\"></altcha-widget>\n", action, o.SiteQuery)
	}
	b.WriteString("  <button type=\"submit\">Send</button>\n</form>\n")
	if o.Captcha {
		b.WriteString("<script type=\"module\" src=\"/_sitebin/altcha.js\"></script>\n")
	}
	return b.String()
}
```

`internal/forms/links.go`:

```go
package forms

import (
	"strconv"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/auth"
)

// ConfirmTTL is how long a confirmation link works.
const ConfirmTTL = 7 * 24 * time.Hour

// stopTTL is effectively forever: a stop link in a months-old mail must still
// work. The signer has no "never expires" mode and does not need one.
const stopTTL = 100 * 365 * 24 * time.Hour

// Links mints and reads the recipient's two links. Each has its own signer
// purpose, so one can never be presented as the other.
type Links struct {
	confirm, stop auth.TokenSigner
}

func NewLinks(secret []byte) Links {
	return Links{
		confirm: auth.TokenSigner{Secret: secret, Purpose: "forms:confirm"},
		stop:    auth.TokenSigner{Secret: secret, Purpose: "forms:stop"},
	}
}

// ConfirmClaim is what a confirmation link vouches for. Seq ties it to the
// form's state when it was minted (see store.Form.Seq).
type ConfirmClaim struct {
	ViewID    string
	Key       string
	Recipient string
	Seq       int
}

// StopClaim is what a stop link vouches for. It carries no seq on purpose.
type StopClaim struct {
	ViewID    string
	Key       string
	Recipient string
}

// The recipient goes last in every subject: view ids, keys and numbers never
// contain '|', but an address's local part may.

func (l Links) ConfirmToken(c ConfirmClaim, now time.Time) string {
	return l.confirm.Sign(c.ViewID+"|"+c.Key+"|"+strconv.Itoa(c.Seq)+"|"+c.Recipient, now, ConfirmTTL)
}

func (l Links) ParseConfirm(tok string, now time.Time) (ConfirmClaim, bool) {
	subj, ok := l.confirm.Parse(tok, now)
	if !ok {
		return ConfirmClaim{}, false
	}
	p := strings.SplitN(subj, "|", 4)
	if len(p) != 4 {
		return ConfirmClaim{}, false
	}
	seq, err := strconv.Atoi(p[2])
	if err != nil {
		return ConfirmClaim{}, false
	}
	return ConfirmClaim{ViewID: p[0], Key: p[1], Seq: seq, Recipient: p[3]}, true
}

func (l Links) StopToken(c StopClaim, now time.Time) string {
	return l.stop.Sign(c.ViewID+"|"+c.Key+"|"+c.Recipient, now, stopTTL)
}

func (l Links) ParseStop(tok string, now time.Time) (StopClaim, bool) {
	subj, ok := l.stop.Parse(tok, now)
	if !ok {
		return StopClaim{}, false
	}
	p := strings.SplitN(subj, "|", 3)
	if len(p) != 3 {
		return StopClaim{}, false
	}
	return StopClaim{ViewID: p[0], Key: p[1], Recipient: p[2]}, true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/forms/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/forms/rules.go internal/forms/rules_test.go internal/forms/links.go internal/forms/links_test.go
git commit -m "feat: forms validate their settings, write their own snippet and sign the recipient's links"
```

---

### Task 5: The ordered body parser

**Files:**
- Create: `internal/forms/parse.go`
- Test: `internal/forms/parse_test.go`

**Interfaces:**
- Produces:
  - `type Field struct{ Name string \`json:"name"\`; Value string \`json:"value"\` }`
  - `type File struct{ Field, Filename, ContentType string; Data []byte }`
  - `type Submission struct{ Fields []Field; Control map[string]string; Files []File }` with `(*Submission).Honeypot() bool` and `(*Submission).HasContent() bool`
  - `type Limits struct{ AllowFiles bool; MaxFiles int; MaxFileBytes int64 }`
  - `type ParseError struct{ Status int; Msg string }`
  - `func Parse(r *http.Request, lim Limits) (*Submission, error)`: every error it returns is a `*ParseError`
  - consts `MaxFields = 50`, `MaxValueRunes = 10000`, `MaxTextBytes = 256 << 10`
  - `func SizeLabel(n int64) string`, used by the mail (Task 6)
- The caller wraps `r.Body` in `http.MaxBytesReader` with the request cap: `int64(maxFiles)*maxFileBytes + MaxTextBytes`.

- [ ] **Step 1: Write the failing tests** (`internal/forms/parse_test.go`)

```go
package forms

import (
	"bytes"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
)

func urlencoded(body string) *http.Request {
	r := httptest.NewRequest("POST", "/_sitebin/forms/k", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

type mpart struct {
	name, filename, body string
	file                 bool
}

func multipartReq(parts ...mpart) *http.Request {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p.file {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.name, p.filename))
			h.Set("Content-Type", "application/octet-stream")
			w, _ := mw.CreatePart(h)
			w.Write([]byte(p.body))
		} else {
			mw.WriteField(p.name, p.body)
		}
	}
	mw.Close()
	r := httptest.NewRequest("POST", "/_sitebin/forms/k", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

var withFiles = Limits{AllowFiles: true, MaxFiles: 5, MaxFileBytes: 2 << 20}

func wantRefusal(t *testing.T, err error, status int) {
	t.Helper()
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Status != status {
		t.Fatalf("err = %v, want a %d refusal", err, status)
	}
}

func TestParseURLEncodedKeepsOrderAndRepeats(t *testing.T) {
	sub, err := Parse(urlencoded("name=Anna+M%C3%BCller&topics=a&email=a%40example.com&topics=b&_subject=Hi&_gotcha=&altcha=xyz"), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Field{{"name", "Anna Müller"}, {"topics", "a"}, {"email", "a@example.com"}, {"topics", "b"}}
	if fmt.Sprint(sub.Fields) != fmt.Sprint(want) {
		t.Errorf("fields = %v, want %v", sub.Fields, want)
	}
	if sub.Control["_subject"] != "Hi" || sub.Control["altcha"] != "xyz" {
		t.Errorf("control = %v", sub.Control)
	}
	if sub.Honeypot() || !sub.HasContent() {
		t.Errorf("honeypot=%v content=%v", sub.Honeypot(), sub.HasContent())
	}
}

func TestParseMultipartKeepsOrder(t *testing.T) {
	sub, err := Parse(multipartReq(mpart{name: "z", body: "1"}, mpart{name: "a", body: "2"}, mpart{name: "m", body: "3"}), withFiles)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sub.Fields) != fmt.Sprint([]Field{{"z", "1"}, {"a", "2"}, {"m", "3"}}) {
		t.Errorf("fields = %v", sub.Fields)
	}
}

func TestParseHoneypotAndEmptiness(t *testing.T) {
	sub, _ := Parse(urlencoded("name=bot&_gotcha=http%3A%2F%2Fspam"), Limits{})
	if !sub.Honeypot() {
		t.Error("a filled _gotcha is a bot")
	}
	sub, _ = Parse(urlencoded("name=&message=+"), Limits{})
	if sub.HasContent() {
		t.Error("blank fields are no content")
	}
}

func TestParseFieldLimits(t *testing.T) {
	var many []string
	for i := 0; i <= MaxFields; i++ {
		many = append(many, fmt.Sprintf("f%d=x", i))
	}
	_, err := Parse(urlencoded(strings.Join(many, "&")), Limits{})
	wantRefusal(t, err, 400)

	if _, err := Parse(urlencoded("m="+strings.Repeat("ä", MaxValueRunes)), Limits{}); err != nil {
		t.Errorf("a value of exactly %d characters is allowed: %v", MaxValueRunes, err)
	}
	_, err = Parse(urlencoded("m="+strings.Repeat("a", MaxValueRunes+1)), Limits{})
	wantRefusal(t, err, 400)
}

func TestParseInvalidUTF8IsReplaced(t *testing.T) {
	sub, err := Parse(urlencoded("name=%FFok"), Limits{})
	if err != nil || sub.Fields[0].Value != "\uFFFDok" {
		t.Fatalf("got %v, %v", sub, err)
	}
}

func TestParseRefusesOtherContentTypes(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":1}`))
	r.Header.Set("Content-Type", "application/json")
	_, err := Parse(r, Limits{})
	wantRefusal(t, err, 415)
	r = httptest.NewRequest("POST", "/", strings.NewReader("a=1"))
	_, err = Parse(r, Limits{})
	wantRefusal(t, err, 415)
}

func TestParseTextTooLarge(t *testing.T) {
	_, err := Parse(urlencoded("m="+strings.Repeat("a", MaxTextBytes)), Limits{})
	wantRefusal(t, err, 413)
}

func TestParseRequestCapIs413(t *testing.T) {
	r := multipartReq(mpart{name: "f", filename: "a.pdf", body: strings.Repeat("a", 5000), file: true})
	r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, 1000)
	_, err := Parse(r, withFiles)
	wantRefusal(t, err, 413)
}

func TestParseFilesRefusedWhenOff(t *testing.T) {
	_, err := Parse(multipartReq(mpart{name: "cv", filename: "cv.pdf", body: "%PDF", file: true}), Limits{AllowFiles: false, MaxFiles: 5, MaxFileBytes: 100})
	wantRefusal(t, err, 400)
	_, err = Parse(multipartReq(mpart{name: "cv", filename: "cv.pdf", body: "%PDF", file: true}), Limits{AllowFiles: true, MaxFiles: 0, MaxFileBytes: 100})
	wantRefusal(t, err, 400)
}

// Review Focus 2: a browser sends an untouched file input as a part with an
// empty filename and no bytes. That is "no file", on any form.
func TestParseEmptyFileInputIgnored(t *testing.T) {
	for _, lim := range []Limits{withFiles, {}} {
		sub, err := Parse(multipartReq(mpart{name: "message", body: "hi"}, mpart{name: "cv", filename: "", body: "", file: true}), lim)
		if err != nil {
			t.Fatalf("limits %+v: %v", lim, err)
		}
		if len(sub.Files) != 0 || len(sub.Fields) != 1 {
			t.Errorf("limits %+v: files=%d fields=%v", lim, len(sub.Files), sub.Fields)
		}
	}
}

func TestParseFileCountAndSize(t *testing.T) {
	lim := Limits{AllowFiles: true, MaxFiles: 2, MaxFileBytes: 10}
	three := []mpart{
		{name: "f", filename: "a.txt", body: "a", file: true},
		{name: "f", filename: "b.txt", body: "b", file: true},
		{name: "f", filename: "c.txt", body: "c", file: true},
	}
	_, err := Parse(multipartReq(three...), lim)
	wantRefusal(t, err, 400)
	_, err = Parse(multipartReq(mpart{name: "f", filename: "big.txt", body: strings.Repeat("x", 11), file: true}), lim)
	wantRefusal(t, err, 413)
	sub, err := Parse(multipartReq(mpart{name: "f", filename: "ok.txt", body: strings.Repeat("x", 10), file: true}), lim)
	if err != nil || len(sub.Files) != 1 {
		t.Fatalf("a file of exactly the cap: %v", err)
	}
}

func TestParseBlockedExtensions(t *testing.T) {
	for _, name := range []string{"Setup.EXE", "invoice.pdf.exe", "run.ps1", "x.js"} {
		_, err := Parse(multipartReq(mpart{name: "f", filename: name, body: "x", file: true}), withFiles)
		wantRefusal(t, err, 400)
	}
	if _, err := Parse(multipartReq(mpart{name: "f", filename: "archive.exe.pdf", body: "x", file: true}), withFiles); err != nil {
		t.Errorf("only the last extension counts: %v", err)
	}
}

func TestParseFilenameAndType(t *testing.T) {
	sub, err := Parse(multipartReq(mpart{name: "cv", filename: `C:\fakepath\Lebenslauf.pdf`, body: "%PDF", file: true}), withFiles)
	if err != nil {
		t.Fatal(err)
	}
	f := sub.Files[0]
	if f.Field != "cv" || f.Filename != "Lebenslauf.pdf" || f.ContentType != "application/pdf" || string(f.Data) != "%PDF" {
		t.Errorf("file = %+v", f)
	}
}

func TestSizeLabel(t *testing.T) {
	for n, want := range map[int64]string{512: "512 B", 183244: "179 KB", 2 << 20: "2.0 MB"} {
		if got := SizeLabel(n); got != want {
			t.Errorf("SizeLabel(%d) = %q, want %q", n, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/forms/ -run 'Parse|SizeLabel' -v`
Expected: build failure, `undefined: Parse`.

- [ ] **Step 3: Implement** (`internal/forms/parse.go`)

```go
package forms

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxFields     = 50
	MaxValueRunes = 10000
	// MaxTextBytes bounds everything that is not a file: every field name and
	// value together.
	MaxTextBytes = 256 << 10

	maxFilenameRunes = 200
)

// Field is one submitted name/value pair, in the order the form sent it.
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// File is one uploaded attachment.
type File struct {
	Field       string
	Filename    string
	ContentType string
	Data        []byte
}

// Submission is a parsed form post.
type Submission struct {
	Fields  []Field           // forwarded to the recipient, in form order
	Control map[string]string // _-prefixed fields and altcha: never forwarded
	Files   []File
}

// Honeypot reports whether the off-screen _gotcha field was filled in, which
// only a bot does.
func (s *Submission) Honeypot() bool { return strings.TrimSpace(s.Control["_gotcha"]) != "" }

// HasContent reports whether any forwarded field has a non-blank value.
func (s *Submission) HasContent() bool {
	for _, f := range s.Fields {
		if strings.TrimSpace(f.Value) != "" {
			return true
		}
	}
	return false
}

// Limits are what one form accepts.
type Limits struct {
	AllowFiles   bool  // the form has attachments switched on
	MaxFiles     int   // SITEBIN_FORMS_MAX_FILES
	MaxFileBytes int64 // SITEBIN_FORMS_MAX_FILE_BYTES
}

// ParseError is a refusal shown to the person submitting, with its status.
type ParseError struct {
	Status int
	Msg    string
}

func (e *ParseError) Error() string { return e.Msg }

func refuse(status int, msg string) error { return &ParseError{Status: status, Msg: msg} }

// blockedExt are extensions the big mailbox providers reject a whole message
// over; accepting them would only turn into a failed delivery.
var blockedExt = map[string]bool{
	".exe": true, ".com": true, ".bat": true, ".cmd": true, ".scr": true, ".pif": true,
	".msi": true, ".msp": true, ".jar": true, ".js": true, ".jse": true, ".vbs": true,
	".vbe": true, ".wsf": true, ".wsh": true, ".ps1": true, ".psm1": true, ".hta": true,
	".cpl": true, ".lnk": true, ".reg": true, ".dll": true, ".app": true, ".apk": true,
}

// Parse reads a form post, keeping the order of its fields. r.Body must
// already be capped by the caller (http.MaxBytesReader); Parse enforces the
// per-field and per-file rules. Every error is a *ParseError.
func Parse(r *http.Request, lim Limits) (*Submission, error) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, refuse(415, "send the form as application/x-www-form-urlencoded or multipart/form-data")
	}
	sub := &Submission{Control: map[string]string{}}
	switch mt {
	case "application/x-www-form-urlencoded":
		err = parseURLEncoded(r.Body, sub)
	case "multipart/form-data":
		if params["boundary"] == "" {
			return nil, refuse(400, "the form data is malformed")
		}
		err = parseMultipart(multipart.NewReader(r.Body, params["boundary"]), sub, lim)
	default:
		return nil, refuse(415, "send the form as application/x-www-form-urlencoded or multipart/form-data")
	}
	if err != nil {
		return nil, bodyError(err)
	}
	return sub, nil
}

// bodyError maps a reader failure onto a refusal. A ParseError passes
// through; the request cap is 413; anything else is malformed data.
func bodyError(err error) error {
	var pe *ParseError
	if errors.As(err, &pe) {
		return pe
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return refuse(413, "the submission is too large")
	}
	return refuse(400, "the form data is malformed")
}

// parseURLEncoded splits the body itself: url.ParseQuery collects into a map
// and loses the order the form had.
func parseURLEncoded(body io.Reader, sub *Submission) error {
	raw, err := io.ReadAll(io.LimitReader(body, MaxTextBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > MaxTextBytes {
		return refuse(413, "the message is too long")
	}
	for _, pair := range strings.Split(string(raw), "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		name, err1 := url.QueryUnescape(k)
		value, err2 := url.QueryUnescape(v)
		if err1 != nil || err2 != nil {
			return refuse(400, "the form data is malformed")
		}
		if err := sub.add(name, value); err != nil {
			return err
		}
	}
	return nil
}

func parseMultipart(mr *multipart.Reader, sub *Submission, lim Limits) error {
	text := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := part.FormName()
		_, dp, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		filename, isFile := dp["filename"]
		if !isFile {
			v, err := io.ReadAll(io.LimitReader(part, int64(MaxTextBytes-text)+1))
			if err != nil {
				return err
			}
			if text += len(name) + len(v); text > MaxTextBytes {
				return refuse(413, "the message is too long")
			}
			if err := sub.add(name, string(v)); err != nil {
				return err
			}
			continue
		}
		if !lim.AllowFiles || lim.MaxFiles <= 0 {
			// An untouched file input is an empty part: not an attachment,
			// and not a reason to refuse.
			var one [1]byte
			if n, _ := io.ReadFull(part, one[:]); n > 0 {
				return refuse(400, "this form does not accept files")
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, lim.MaxFileBytes+1))
		if err != nil {
			return err
		}
		if len(data) == 0 {
			continue
		}
		if int64(len(data)) > lim.MaxFileBytes {
			return refuse(413, "each file may be at most "+limitLabel(lim.MaxFileBytes))
		}
		if len(sub.Files) >= lim.MaxFiles {
			return refuse(400, fmt.Sprintf("at most %d files can be attached", lim.MaxFiles))
		}
		fname := cleanFilename(filename)
		ext := strings.ToLower(path.Ext(fname))
		if blockedExt[ext] {
			return refuse(400, "files of this type cannot be sent by email: "+fname)
		}
		ct := mime.TypeByExtension(ext)
		if ct == "" {
			ct = "application/octet-stream"
		}
		sub.Files = append(sub.Files, File{Field: strings.ToValidUTF8(name, "\uFFFD"), Filename: fname, ContentType: ct, Data: data})
	}
}

// add files one name/value pair as a forwarded field or a control field.
func (s *Submission) add(name, value string) error {
	name = strings.ToValidUTF8(name, "\uFFFD")
	value = strings.ToValidUTF8(value, "\uFFFD")
	if name == "" {
		return nil
	}
	if name == "altcha" || strings.HasPrefix(name, "_") {
		if _, seen := s.Control[name]; !seen {
			s.Control[name] = value
		}
		return nil
	}
	if len(s.Fields) >= MaxFields {
		return refuse(400, fmt.Sprintf("the form has more than %d fields", MaxFields))
	}
	if utf8.RuneCountInString(value) > MaxValueRunes {
		return refuse(400, fmt.Sprintf("a field is longer than %d characters", MaxValueRunes))
	}
	s.Fields = append(s.Fields, Field{Name: name, Value: value})
	return nil
}

// cleanFilename keeps only the last path element (old browsers sent
// C:\fakepath\...), drops control characters, and keeps the tail of an
// overlong name so its extension survives.
func cleanFilename(s string) string {
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "\uFFFD"))
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxFilenameRunes {
		s = string(r[len(r)-maxFilenameRunes:])
	}
	if s == "" || s == "." || s == ".." {
		s = "attachment"
	}
	return s
}

// limitLabel states a configured byte limit exactly.
func limitLabel(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// SizeLabel is a file size for people.
func SizeLabel(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/forms/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/forms/parse.go internal/forms/parse_test.go
git commit -m "feat: form posts are parsed in the order the form sent them, with capped fields and attachments"
```

---
### Task 6: Building the mails

**Files:**
- Create: `internal/forms/message.go`, `internal/forms/templates/submission.html`, `internal/forms/templates/confirm.html`
- Test: `internal/forms/message_test.go`

**Interfaces:**
- Consumes: `Field`, `File`, `Submission`, `SizeLabel` (Task 5).
- Produces:
  - `type Mail struct{ From, To string; Data []byte }`
  - `type SubmissionMail struct{ From, FormName, FormKey, Recipient, SiteID, Host, StopURL string; At time.Time; Sub *Submission }`
  - `type ConfirmationMail struct{ From, FormName, Recipient, Host, ConfirmURL string; At time.Time }`
  - `func BuildSubmission(in SubmissionMail) (Mail, error)` and `func BuildConfirmation(in ConfirmationMail) (Mail, error)`
  - `var ErrHeader`

- [ ] **Step 1: Write the failing tests** (`internal/forms/message_test.go`)

```go
package forms

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func sampleSub() *Submission {
	return &Submission{
		Fields: []Field{
			{"name", "Anna Muster"},
			{"email", "anna@example.com"},
			{"message", "Hallo,\nzweite Zeile <b>fett</b>"},
		},
		Control: map[string]string{},
		Files:   []File{{Field: "cv", Filename: "cv.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4 test")}},
	}
}

func sampleIn(sub *Submission) SubmissionMail {
	return SubmissionMail{
		From: "forms@sitebin.example", FormName: "Contact", FormKey: "k7f3m2q9xaw4npd6",
		Recipient: "office@example.com", SiteID: "abcdefghijklmnopqrstuvwxyz", Host: "www.example.com",
		StopURL: "https://sitebin.example/forms/stop?t=tok", At: time.Date(2026, 9, 24, 10, 15, 0, 0, time.UTC), Sub: sub,
	}
}

type leaf struct {
	mediaType string
	filename  string
	header    textproto.MIMEHeader
	body      []byte
}

// readMail parses a built message back and returns its header and its leaf
// parts in order, decoded. It is how a mail client sees the message.
func readMail(t *testing.T, m Mail) (mail.Header, []leaf) {
	t.Helper()
	if bytes.Contains(bytes.ReplaceAll(m.Data, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("bare LF in the message: every line must end in CRLF")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(m.Data))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	var leaves []leaf
	var walk func(ct string, r io.Reader)
	walk = func(ct string, r io.Reader) {
		mt, params, err := mime.ParseMediaType(ct)
		if err != nil || !strings.HasPrefix(mt, "multipart/") {
			t.Fatalf("not multipart: %q %v", ct, err)
		}
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			pct := p.Header.Get("Content-Type")
			pmt, _, _ := mime.ParseMediaType(pct)
			if strings.HasPrefix(pmt, "multipart/") {
				walk(pct, p)
				continue
			}
			var body []byte
			switch p.Header.Get("Content-Transfer-Encoding") {
			case "base64":
				raw, _ := io.ReadAll(p)
				body, err = base64.StdEncoding.DecodeString(strings.NewReplacer("\r", "", "\n", "").Replace(string(raw)))
			case "quoted-printable":
				body, err = io.ReadAll(quotedprintable.NewReader(p))
			default:
				body, err = io.ReadAll(p)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, dp, _ := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
			leaves = append(leaves, leaf{mediaType: pmt, filename: dp["filename"], header: p.Header, body: body})
		}
	}
	walk(msg.Header.Get("Content-Type"), msg.Body)
	return msg.Header, leaves
}

func decodeHeader(t *testing.T, v string) string {
	t.Helper()
	s, err := new(mime.WordDecoder).DecodeHeader(v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSubmissionMailStructureAndHeaders(t *testing.T) {
	m, err := BuildSubmission(sampleIn(sampleSub()))
	if err != nil {
		t.Fatal(err)
	}
	if m.From != "forms@sitebin.example" || m.To != "office@example.com" {
		t.Errorf("envelope %q -> %q", m.From, m.To)
	}
	h, leaves := readMail(t, m)
	var kinds []string
	for _, l := range leaves {
		kinds = append(kinds, l.mediaType+" "+l.filename)
	}
	want := []string{"text/plain ", "text/html ", "application/pdf cv.pdf", "application/json submission.json"}
	if strings.Join(kinds, "|") != strings.Join(want, "|") {
		t.Fatalf("parts = %q, want %q", kinds, want)
	}
	from, err := mail.ParseAddress(h.Get("From"))
	if err != nil || from.Name != "Contact" || from.Address != "forms@sitebin.example" {
		t.Errorf("From = %q", h.Get("From"))
	}
	if h.Get("To") != "<office@example.com>" || h.Get("Reply-To") != "<anna@example.com>" {
		t.Errorf("To %q Reply-To %q", h.Get("To"), h.Get("Reply-To"))
	}
	if got := decodeHeader(t, h.Get("Subject")); got != "New message via Contact" {
		t.Errorf("Subject = %q", got)
	}
	if h.Get("List-Unsubscribe") != "<https://sitebin.example/forms/stop?t=tok>" ||
		h.Get("List-Unsubscribe-Post") != "List-Unsubscribe=One-Click" ||
		h.Get("X-Sitebin-Site") != "abcdefghijklmnopqrstuvwxyz" {
		t.Errorf("list/site headers: %q %q %q", h.Get("List-Unsubscribe"), h.Get("List-Unsubscribe-Post"), h.Get("X-Sitebin-Site"))
	}
	if !strings.HasSuffix(h.Get("Message-ID"), "@sitebin.example>") {
		t.Errorf("Message-ID = %q", h.Get("Message-ID"))
	}
	if _, err := h.Date(); err != nil {
		t.Errorf("Date: %v", err)
	}
	if string(leaves[2].body) != "%PDF-1.4 test" {
		t.Errorf("attachment bytes changed")
	}
}

func TestSubmissionMailBodies(t *testing.T) {
	m, _ := BuildSubmission(sampleIn(sampleSub()))
	_, leaves := readMail(t, m)
	text := strings.ReplaceAll(string(leaves[0].body), "\r\n", "\n")
	html := string(leaves[1].body)
	for _, want := range []string{"name: Anna Muster", "message:\n    Hallo,\n    zweite Zeile <b>fett</b>", "cv.pdf (13 B)", "https://sitebin.example/forms/stop?t=tok"} {
		if !strings.Contains(text, want) {
			t.Errorf("text part lacks %q:\n%s", want, text)
		}
	}
	if i, j, k := strings.Index(text, "name:"), strings.Index(text, "email:"), strings.Index(text, "message:"); !(i < j && j < k) {
		t.Errorf("fields out of form order:\n%s", text)
	}
	if strings.Contains(html, "<b>fett</b>") || !strings.Contains(html, "zweite Zeile &lt;b&gt;fett&lt;/b&gt;") {
		t.Errorf("HTML part does not escape submitted markup")
	}
	if !strings.Contains(html, "Hallo,<br>zweite Zeile") {
		t.Errorf("HTML part does not keep line breaks")
	}
	if !strings.Contains(html, "Reply to this email") || !strings.Contains(text, "Reply to this email") {
		t.Errorf("the reply hint is missing although Reply-To is set")
	}
}

func TestSubmissionJSON(t *testing.T) {
	m, _ := BuildSubmission(sampleIn(sampleSub()))
	_, leaves := readMail(t, m)
	var j struct {
		Version int `json:"version"`
		Form    struct{ Key, Name string }
		Site    struct{ ID, Host string }
		At      string  `json:"submitted_at"`
		Fields  []Field `json:"fields"`
		Files   []struct {
			Field, Filename string
			ContentType     string `json:"content_type"`
			Size            int
			SHA256          string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(leaves[3].body, &j); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("%PDF-1.4 test"))
	if j.Version != 1 || j.Form.Key != "k7f3m2q9xaw4npd6" || j.Form.Name != "Contact" ||
		j.Site.ID != "abcdefghijklmnopqrstuvwxyz" || j.Site.Host != "www.example.com" ||
		j.At != "2026-09-24T10:15:00Z" || len(j.Fields) != 3 || j.Fields[2].Value != "Hallo,\nzweite Zeile <b>fett</b>" ||
		len(j.Files) != 1 || j.Files[0].Size != 13 || j.Files[0].SHA256 != hex.EncodeToString(sum[:]) ||
		j.Files[0].ContentType != "application/pdf" || j.Files[0].Field != "cv" {
		t.Errorf("submission.json = %+v", j)
	}
	if strings.Contains(string(leaves[3].body), `\u003c`) {
		t.Error("submission.json HTML-escapes values; a machine reader wants them verbatim")
	}
}

// Review Focus 1: umlauts and emoji in every place a person can put them.
func TestSubmissionMailNonASCII(t *testing.T) {
	sub := &Submission{
		Fields:  []Field{{"nachricht", "Grüße 👋 aus Linz"}},
		Control: map[string]string{"_subject": "Frage zu Größen"},
	}
	in := sampleIn(sub)
	in.FormName = "Anfrage Café ✉"
	m, err := BuildSubmission(in)
	if err != nil {
		t.Fatal(err)
	}
	h, leaves := readMail(t, m)
	if got := decodeHeader(t, h.Get("Subject")); got != "Frage zu Größen" {
		t.Errorf("Subject = %q", got)
	}
	from, err := mail.ParseAddress(h.Get("From"))
	if err != nil || from.Name != "Anfrage Café ✉" {
		t.Errorf("From = %q (%v)", h.Get("From"), err)
	}
	for i, l := range leaves {
		if !strings.Contains(string(l.body), "Grüße 👋 aus Linz") {
			t.Errorf("part %d (%s) lost the non-ASCII value", i, l.mediaType)
		}
	}
}

func TestSubmissionMailRefusesHeaderInjection(t *testing.T) {
	in := sampleIn(sampleSub())
	in.FormName = "Contact\r\nBcc: x@evil.example"
	if _, err := BuildSubmission(in); !errors.Is(err, ErrHeader) {
		t.Errorf("a form name with a line break was accepted: %v", err)
	}

	sub := sampleSub()
	sub.Control["_subject"] = "Hi\r\nBcc: x@evil.example"
	sub.Fields[1].Value = "a@example.com\r\nBcc: y@evil.example"
	sub.Files[0].Filename = "evil\r\nX-Injected: 1.pdf"
	m, err := BuildSubmission(sampleIn(sub))
	if err != nil {
		t.Fatal(err)
	}
	h, leaves := readMail(t, m)
	if h.Get("Bcc") != "" || h.Get("Reply-To") != "" || h.Get("X-Injected") != "" {
		t.Errorf("injected: Bcc %q Reply-To %q X-Injected %q", h.Get("Bcc"), h.Get("Reply-To"), h.Get("X-Injected"))
	}
	// Checked on the parsed headers, not the raw bytes: quoted-printable may
	// soft-wrap a body line anywhere, which says nothing about headers.
	for _, l := range leaves {
		if l.header.Get("X-Injected") != "" || l.header.Get("Bcc") != "" {
			t.Errorf("a header was injected into the %s part: %v", l.mediaType, l.header)
		}
	}
	if leaves[2].filename != "evilX-Injected: 1.pdf" {
		t.Errorf("attachment filename = %q, want the line break dropped", leaves[2].filename)
	}
	if got := decodeHeader(t, h.Get("Subject")); got != "Hi Bcc: x@evil.example" {
		t.Errorf("Subject = %q, want the line break flattened", got)
	}
}

func TestReplyToNeedsExactlyOneAddress(t *testing.T) {
	for value, want := range map[string]string{
		"a@example.com, b@example.com": "",
		"not an address":               "",
		"Anna <anna@example.com>":      "<anna@example.com>",
	} {
		sub := sampleSub()
		sub.Fields[1].Value = value
		m, _ := BuildSubmission(sampleIn(sub))
		h, _ := readMail(t, m)
		if got := h.Get("Reply-To"); got != want {
			t.Errorf("email %q: Reply-To = %q, want %q", value, got, want)
		}
	}
}

func TestSubjectIsCapped(t *testing.T) {
	sub := sampleSub()
	sub.Control["_subject"] = strings.Repeat("ö", 300)
	m, _ := BuildSubmission(sampleIn(sub))
	h, _ := readMail(t, m)
	if got := decodeHeader(t, h.Get("Subject")); got != strings.Repeat("ö", 200) {
		t.Errorf("subject has %d runes, want 200", len([]rune(got)))
	}
}

func TestConfirmationMail(t *testing.T) {
	m, err := BuildConfirmation(ConfirmationMail{
		From: "forms@sitebin.example", FormName: "Contact", Recipient: "office@example.com",
		Host: "www.example.com", ConfirmURL: "https://sitebin.example/forms/confirm?t=tok", At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h, leaves := readMail(t, m)
	from, _ := mail.ParseAddress(h.Get("From"))
	if from == nil || from.Name != "Sitebin" {
		t.Errorf("From = %q: a confirmation must never carry the form's name as its sender", h.Get("From"))
	}
	if h.Get("List-Unsubscribe") != "" {
		t.Error("a one-off confirmation carries List-Unsubscribe")
	}
	if got := decodeHeader(t, h.Get("Subject")); got != "Confirm form messages from www.example.com" {
		t.Errorf("Subject = %q", got)
	}
	if len(leaves) != 2 {
		t.Fatalf("parts = %d, want text and HTML only", len(leaves))
	}
	for _, l := range leaves {
		if !strings.Contains(string(l.body), "https://sitebin.example/forms/confirm?t=tok") ||
			!strings.Contains(string(l.body), "office@example.com") {
			t.Errorf("%s part lacks the link or the address", l.mediaType)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/forms/ -run 'Mail|JSON|ReplyTo|Subject' -v`
Expected: build failure, `undefined: BuildSubmission`.

- [ ] **Step 3: Implement**

`internal/forms/message.go`:

```go
package forms

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
	"unicode"
)

//go:embed templates/*.html
var templateFS embed.FS

var mailTmpl = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// Mail is a message ready for SMTP: the envelope and the RFC 5322 bytes.
type Mail struct {
	From string // envelope sender: the instance's address
	To   string // envelope recipient
	Data []byte // CRLF line endings throughout
}

// SubmissionMail is everything a forwarded submission is built from.
type SubmissionMail struct {
	From      string // SITEBIN_FORMS_SMTP_FROM
	FormName  string
	FormKey   string
	Recipient string
	SiteID    string // the site's view id
	Host      string // the host the form was submitted on
	StopURL   string
	At        time.Time
	Sub       *Submission
}

// ConfirmationMail asks a recipient to accept a form's submissions.
type ConfirmationMail struct {
	From       string
	FormName   string
	Recipient  string
	Host       string
	ConfirmURL string
	At         time.Time
}

// ErrHeader refuses a header value that carries a line break.
var ErrHeader = errors.New("a header value contains a line break")

const maxSubjectRunes = 200

func headerSafe(vals ...string) error {
	for _, v := range vals {
		if strings.ContainsAny(v, "\r\n") {
			return ErrHeader
		}
	}
	return nil
}

type header struct{ k, v string }

type attachment struct {
	name, contentType string
	data              []byte
}

// mailView is what the HTML templates and the text builders read.
type mailView struct {
	Preheader  string
	FormName   string
	Host       string
	Recipient  string
	Fields     []viewField
	Files      []viewFile
	At         string
	StopURL    string
	ConfirmURL string
	ReplyHint  bool
}

type viewField struct {
	Label string
	Lines []string
	Empty bool
}

type viewFile struct{ Name, Size string }

// BuildSubmission builds the mail that forwards one submission.
func BuildSubmission(in SubmissionMail) (Mail, error) {
	if err := headerSafe(in.From, in.FormName, in.Recipient, in.SiteID, in.StopURL); err != nil {
		return Mail{}, err
	}
	subject := "New message via " + in.FormName
	if s := cleanSubject(in.Sub.Control["_subject"]); s != "" {
		subject = s
	}
	replyTo := replyAddress(in.Sub.Fields)
	v := mailView{
		FormName:  in.FormName,
		Host:      in.Host,
		At:        in.At.UTC().Format("2 Jan 2006, 15:04 UTC"),
		StopURL:   in.StopURL,
		ReplyHint: replyTo != "",
		Preheader: preheader(in.Sub.Fields),
	}
	for _, f := range in.Sub.Fields {
		lines := strings.Split(strings.ReplaceAll(f.Value, "\r\n", "\n"), "\n")
		v.Fields = append(v.Fields, viewField{Label: fieldLabel(f.Name), Lines: lines, Empty: strings.TrimSpace(f.Value) == ""})
	}
	for _, f := range in.Sub.Files {
		v.Files = append(v.Files, viewFile{Name: f.Filename, Size: SizeLabel(int64(len(f.Data)))})
	}
	var html bytes.Buffer
	if err := mailTmpl.ExecuteTemplate(&html, "submission.html", v); err != nil {
		return Mail{}, err
	}
	js, err := submissionJSON(in)
	if err != nil {
		return Mail{}, err
	}
	hs := []header{
		{"From", (&mail.Address{Name: in.FormName, Address: in.From}).String()},
		{"To", (&mail.Address{Address: in.Recipient}).String()},
	}
	if replyTo != "" {
		hs = append(hs, header{"Reply-To", (&mail.Address{Address: replyTo}).String()})
	}
	hs = append(hs,
		header{"Subject", mime.QEncoding.Encode("utf-8", subject)},
		header{"Date", in.At.UTC().Format(time.RFC1123Z)},
		header{"Message-ID", messageID(in.From)},
		header{"List-Unsubscribe", "<" + in.StopURL + ">"},
		header{"List-Unsubscribe-Post", "List-Unsubscribe=One-Click"},
		header{"X-Sitebin-Site", in.SiteID},
	)
	atts := make([]attachment, 0, len(in.Sub.Files)+1)
	for _, f := range in.Sub.Files {
		atts = append(atts, attachment{name: f.Filename, contentType: f.ContentType, data: f.Data})
	}
	atts = append(atts, attachment{name: "submission.json", contentType: "application/json", data: js})
	data, err := compose(hs, submissionText(v), html.String(), atts)
	return Mail{From: in.From, To: in.Recipient, Data: data}, err
}

// BuildConfirmation builds the one mail that asks a recipient to accept a
// form's submissions. Its sender is always "Sitebin": the person who created
// the form does not get to choose who this mail appears to come from.
func BuildConfirmation(in ConfirmationMail) (Mail, error) {
	if err := headerSafe(in.From, in.FormName, in.Recipient, in.Host, in.ConfirmURL); err != nil {
		return Mail{}, err
	}
	v := mailView{
		FormName:   in.FormName,
		Host:       in.Host,
		Recipient:  in.Recipient,
		ConfirmURL: in.ConfirmURL,
		At:         in.At.UTC().Format("2 Jan 2006, 15:04 UTC"),
		Preheader:  in.Host + " would like to email you the messages from one of its forms.",
	}
	var html bytes.Buffer
	if err := mailTmpl.ExecuteTemplate(&html, "confirm.html", v); err != nil {
		return Mail{}, err
	}
	hs := []header{
		{"From", (&mail.Address{Name: "Sitebin", Address: in.From}).String()},
		{"To", (&mail.Address{Address: in.Recipient}).String()},
		{"Subject", mime.QEncoding.Encode("utf-8", "Confirm form messages from "+in.Host)},
		{"Date", in.At.UTC().Format(time.RFC1123Z)},
		{"Message-ID", messageID(in.From)},
	}
	data, err := compose(hs, confirmText(v), html.String(), nil)
	return Mail{From: in.From, To: in.Recipient, Data: data}, err
}

func submissionText(v mailView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "New message via %s on %s\n\n", v.FormName, v.Host)
	for _, f := range v.Fields {
		if len(f.Lines) == 1 {
			fmt.Fprintf(&b, "%s: %s\n", f.Label, f.Lines[0])
			continue
		}
		fmt.Fprintf(&b, "%s:\n", f.Label)
		for _, l := range f.Lines {
			fmt.Fprintf(&b, "    %s\n", l)
		}
	}
	if len(v.Files) > 0 {
		b.WriteString("\nAttachments:\n")
		for _, f := range v.Files {
			fmt.Fprintf(&b, "    %s (%s)\n", f.Name, f.Size)
		}
	}
	b.WriteString("\n-- \n")
	fmt.Fprintf(&b, "Sent %s through the form \"%s\" on %s.\n", v.At, v.FormName, v.Host)
	if v.ReplyHint {
		b.WriteString("Reply to this email to answer the sender directly.\n")
	}
	fmt.Fprintf(&b, "Stop emails from this form: %s\n", v.StopURL)
	return b.String()
}

func confirmText(v mailView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s would like to send the messages of its form \"%s\" to this address (%s).\n\n", v.Host, v.FormName, v.Recipient)
	fmt.Fprintf(&b, "To agree, open this link within 7 days:\n%s\n\n", v.ConfirmURL)
	b.WriteString("If you did not expect this email, ignore it. Nothing is sent to you unless you confirm,\n")
	b.WriteString("and every message you receive carries a link to stop them.\n\n-- \nSitebin\n")
	return b.String()
}

type jsonForm struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type jsonSite struct {
	ID   string `json:"id"`
	Host string `json:"host"`
}

type jsonFile struct {
	Field       string `json:"field"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	SHA256      string `json:"sha256"`
}

type jsonSubmission struct {
	Version     int        `json:"version"`
	Form        jsonForm   `json:"form"`
	Site        jsonSite   `json:"site"`
	SubmittedAt time.Time  `json:"submitted_at"`
	Fields      []Field    `json:"fields"`
	Files       []jsonFile `json:"files"`
}

// submissionJSON is the machine-readable copy. The client IP is deliberately
// not in it: the owner gets what the person typed, nothing more.
func submissionJSON(in SubmissionMail) ([]byte, error) {
	j := jsonSubmission{
		Version:     1,
		Form:        jsonForm{Key: in.FormKey, Name: in.FormName},
		Site:        jsonSite{ID: in.SiteID, Host: in.Host},
		SubmittedAt: in.At.UTC().Truncate(time.Second),
		Fields:      in.Sub.Fields,
		Files:       []jsonFile{},
	}
	if j.Fields == nil {
		j.Fields = []Field{}
	}
	for _, f := range in.Sub.Files {
		sum := sha256.Sum256(f.Data)
		j.Files = append(j.Files, jsonFile{Field: f.Field, Filename: f.Filename, ContentType: f.ContentType, Size: len(f.Data), SHA256: hex.EncodeToString(sum[:])})
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(j); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// compose assembles the MIME tree: multipart/mixed holding a
// multipart/alternative (text, then HTML: clients show the last one they can
// render) followed by the attachments.
func compose(hs []header, text, html string, atts []attachment) ([]byte, error) {
	var alt bytes.Buffer
	altw := multipart.NewWriter(&alt)
	if err := writeQP(altw, "text/plain; charset=utf-8", text); err != nil {
		return nil, err
	}
	if err := writeQP(altw, "text/html; charset=utf-8", html); err != nil {
		return nil, err
	}
	if err := altw.Close(); err != nil {
		return nil, err
	}

	var body bytes.Buffer
	mixed := multipart.NewWriter(&body)
	p, err := mixed.CreatePart(textproto.MIMEHeader{
		"Content-Type": {mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": altw.Boundary()})},
	})
	if err != nil {
		return nil, err
	}
	if _, err := p.Write(alt.Bytes()); err != nil {
		return nil, err
	}
	for _, a := range atts {
		name := safeFilename(a.name)
		ct := mime.FormatMediaType(a.contentType, map[string]string{"name": name})
		if ct == "" {
			ct = mime.FormatMediaType("application/octet-stream", map[string]string{"name": name})
		}
		w, err := mixed.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {ct},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": name})},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeBase64(w, a.data); err != nil {
			return nil, err
		}
	}
	if err := mixed.Close(); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	for _, h := range hs {
		writeHeader(&out, h.k, h.v)
	}
	writeHeader(&out, "MIME-Version", "1.0")
	writeHeader(&out, "Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mixed.Boundary()}))
	out.WriteString("\r\n")
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

// writeQP writes one text part, quoted-printable. In text mode the encoder
// turns every line break into CRLF, so the templates may use plain \n.
func writeQP(mw *multipart.Writer, contentType, s string) error {
	w, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {contentType},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return err
	}
	qp := quotedprintable.NewWriter(w)
	if _, err := io.WriteString(qp, s); err != nil {
		return err
	}
	return qp.Close()
}

// writeBase64 writes data base64-encoded in 76-character lines.
func writeBase64(w io.Writer, data []byte) error {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		if _, err := io.WriteString(w, enc[:76]+"\r\n"); err != nil {
			return err
		}
		enc = enc[76:]
	}
	_, err := io.WriteString(w, enc+"\r\n")
	return err
}

// writeHeader writes one header line, folded at spaces past 76 columns.
// Folding inserts CRLF plus a space and never removes a character, so the
// value unfolds to exactly what was given.
func writeHeader(b *bytes.Buffer, k, v string) {
	b.WriteString(k + ":")
	col := len(k) + 1
	for _, word := range strings.Split(v, " ") {
		if col+1+len(word) > 76 && col > len(k)+1 {
			b.WriteString("\r\n")
			col = 0
		}
		b.WriteString(" " + word)
		col += 1 + len(word)
	}
	b.WriteString("\r\n")
}

func messageID(from string) string {
	var r [16]byte
	rand.Read(r[:])
	domain := "sitebin.invalid"
	if i := strings.LastIndexByte(from, '@'); i >= 0 {
		domain = from[i+1:]
	}
	return "<" + hex.EncodeToString(r[:]) + "@" + domain + ">"
}

// cleanSubject flattens a submitted _subject into one line of at most 200
// characters. Control characters become spaces, CR and LF above all.
func cleanSubject(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "\uFFFD"))
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxSubjectRunes {
		s = string(r[:maxSubjectRunes])
	}
	return s
}

// replyAddress is the submitted "email" field when it is exactly one address.
// Only the bare address is used; a display name typed into a form is not
// worth carrying into a header.
func replyAddress(fields []Field) string {
	for _, f := range fields {
		if strings.EqualFold(f.Name, "email") {
			a, err := mail.ParseAddress(strings.TrimSpace(f.Value))
			if err != nil || strings.ContainsAny(a.Address, "\r\n") {
				return ""
			}
			return a.Address
		}
	}
	return ""
}

// safeFilename drops control characters from a filename for the MIME
// headers. The parser already does this; a mail is built from other inputs
// too (the preview tool, tests), and a header must never depend on that.
func safeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return "attachment"
	}
	return s
}

// fieldLabel turns a field name into a label: first_name → first name.
func fieldLabel(name string) string { return strings.NewReplacer("_", " ", "-", " ").Replace(name) }

// preheader is the line an inbox shows under the subject: the start of the
// message field if there is one, else of the first field.
func preheader(fields []Field) string {
	pick := ""
	for _, f := range fields {
		if strings.EqualFold(f.Name, "message") {
			pick = f.Value
			break
		}
	}
	if pick == "" && len(fields) > 0 {
		pick = fields[0].Value
	}
	pick = strings.Join(strings.Fields(pick), " ")
	if r := []rune(pick); len(r) > 110 {
		pick = string(r[:110]) + "…"
	}
	return pick
}
```

`internal/forms/templates/submission.html` (the claim-ticket look, built for mail clients: tables, inline styles, a light card, no web fonts or remote images):

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light only">
<meta name="supported-color-schemes" content="light">
<title>{{.FormName}}</title>
</head>
<body style="margin:0;padding:0;background:#eef0f5;-webkit-text-size-adjust:100%;">
<div style="display:none;font-size:1px;line-height:1px;max-height:0;max-width:0;opacity:0;overflow:hidden;">{{.Preheader}}</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#eef0f5;">
  <tr>
    <td align="center" style="padding:28px 12px 36px;">
      <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:600px;">
        <tr>
          <td style="padding:0 6px 12px;font-family:'SFMono-Regular',Consolas,'Liberation Mono',Menlo,monospace;font-size:11px;line-height:16px;letter-spacing:2px;text-transform:uppercase;color:#6b7386;">Sitebin &middot; form message</td>
        </tr>
        <tr>
          <td style="background:#fffdf8;border:2px dashed #c9cfdb;border-radius:14px;">
            <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">
              <tr>
                <td style="padding:26px 28px 20px;">
                  <span style="display:inline-block;padding:6px 10px;border:2px solid #d99a26;border-radius:6px;font-family:'SFMono-Regular',Consolas,'Liberation Mono',Menlo,monospace;font-size:12px;line-height:14px;font-weight:700;letter-spacing:2px;text-transform:uppercase;color:#a8740f;">{{.FormName}}</span>
                  <div style="margin-top:14px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:20px;line-height:28px;font-weight:600;color:#141a26;">New message from {{.Host}}</div>
                </td>
              </tr>
              <tr>
                <td style="padding:0 28px;"><div style="border-top:2px dashed #dfe3ea;height:0;line-height:0;font-size:0;">&nbsp;</div></td>
              </tr>
              {{range .Fields}}
              <tr>
                <td style="padding:18px 28px 0;">
                  <div style="font-family:'SFMono-Regular',Consolas,'Liberation Mono',Menlo,monospace;font-size:11px;line-height:16px;letter-spacing:1.5px;text-transform:uppercase;color:#7b8497;">{{.Label}}</div>
                  <div style="margin-top:4px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:15px;line-height:23px;color:#141a26;word-break:break-word;overflow-wrap:anywhere;">{{if .Empty}}<span style="color:#b0b7c5;">&mdash;</span>{{else}}{{range $i, $l := .Lines}}{{if $i}}<br>{{end}}{{$l}}{{end}}{{end}}</div>
                </td>
              </tr>
              {{end}}
              {{if .Files}}
              <tr>
                <td style="padding:22px 28px 0;">
                  <div style="font-family:'SFMono-Regular',Consolas,'Liberation Mono',Menlo,monospace;font-size:11px;line-height:16px;letter-spacing:1.5px;text-transform:uppercase;color:#7b8497;">Attachments</div>
                  {{range .Files}}
                  <div style="margin-top:6px;font-family:'SFMono-Regular',Consolas,'Liberation Mono',Menlo,monospace;font-size:13px;line-height:20px;color:#141a26;">{{.Name}} <span style="color:#7b8497;">&middot; {{.Size}}</span></div>
                  {{end}}
                </td>
              </tr>
              {{end}}
              <tr><td style="padding:0 0 26px;font-size:0;line-height:0;">&nbsp;</td></tr>
            </table>
          </td>
        </tr>
        <tr>
          <td style="padding:16px 8px 0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:12px;line-height:19px;color:#7b8497;">
            Sent {{.At}} through the form &ldquo;{{.FormName}}&rdquo; on {{.Host}}.{{if .ReplyHint}}<br>Reply to this email to answer the sender directly.{{end}}<br>
            <a href="{{.StopURL}}" style="color:#7b8497;text-decoration:underline;">Stop emails from this form</a>
          </td>
        </tr>
      </table>
    </td>
  </tr>
</table>
</body>
</html>
```

`internal/forms/templates/confirm.html`:

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light only">
<meta name="supported-color-schemes" content="light">
<title>Confirm form messages</title>
</head>
<body style="margin:0;padding:0;background:#eef0f5;-webkit-text-size-adjust:100%;">
<div style="display:none;font-size:1px;line-height:1px;max-height:0;max-width:0;opacity:0;overflow:hidden;">{{.Preheader}}</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#eef0f5;">
  <tr>
    <td align="center" style="padding:28px 12px 36px;">
      <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:600px;">
        <tr>
          <td style="padding:0 6px 12px;font-family:'SFMono-Regular',Consolas,'Liberation Mono',Menlo,monospace;font-size:11px;line-height:16px;letter-spacing:2px;text-transform:uppercase;color:#6b7386;">Sitebin &middot; confirm recipient</td>
        </tr>
        <tr>
          <td style="background:#fffdf8;border:2px dashed #c9cfdb;border-radius:14px;padding:26px 28px 28px;">
            <span style="display:inline-block;padding:6px 10px;border:2px solid #d99a26;border-radius:6px;font-family:'SFMono-Regular',Consolas,'Liberation Mono',Menlo,monospace;font-size:12px;line-height:14px;font-weight:700;letter-spacing:2px;text-transform:uppercase;color:#a8740f;">{{.FormName}}</span>
            <div style="margin-top:14px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:20px;line-height:28px;font-weight:600;color:#141a26;">{{.Host}} would like to email you its form messages</div>
            <div style="margin-top:12px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:15px;line-height:23px;color:#3a4254;">The form &ldquo;{{.FormName}}&rdquo; on {{.Host}} wants to send the messages people submit to <strong>{{.Recipient}}</strong>. Nothing is sent to you until you agree.</div>
            <table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin-top:22px;">
              <tr>
                <td style="border-radius:10px;background:#f5b84d;">
                  <a href="{{.ConfirmURL}}" style="display:inline-block;padding:13px 22px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:15px;line-height:18px;font-weight:700;color:#221902;text-decoration:none;border-radius:10px;">Confirm this address</a>
                </td>
              </tr>
            </table>
            <div style="margin-top:18px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:13px;line-height:20px;color:#7b8497;">The button works for 7 days. If it does not open, copy this link into your browser:<br><a href="{{.ConfirmURL}}" style="color:#7b8497;word-break:break-all;">{{.ConfirmURL}}</a></div>
          </td>
        </tr>
        <tr>
          <td style="padding:16px 8px 0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:12px;line-height:19px;color:#7b8497;">
            If you did not expect this email, ignore it. Every message you receive later carries a link to stop them.
          </td>
        </tr>
      </table>
    </td>
  </tr>
</table>
</body>
</html>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/forms/ -v`
Expected: PASS. If `TestSubmissionMailBodies` fails on `Hallo,<br>zweite Zeile`, check that the template's `range` over `.Lines` keeps `{{if $i}}<br>{{end}}{{$l}}` on one line: whitespace between them would show up in the output.

- [ ] **Step 5: Commit**

```bash
git add internal/forms/message.go internal/forms/message_test.go internal/forms/templates
git commit -m "feat: a submission becomes a text, HTML and JSON mail in the claim-ticket look"
```

---
### Task 7: SMTP delivery with a deadline

**Files:**
- Create: `internal/forms/smtp.go`
- Test: `internal/forms/smtp_test.go`

**Interfaces:**
- Consumes: `Mail` (Task 6).
- Produces: `type Sender interface{ Send(ctx context.Context, m Mail) error }` and `type SMTPSender struct{ Host string; Port int; User, Pass string; ImplicitTLS bool; Timeout time.Duration; TLSConfig *tls.Config }`, which implements `Sender`.

- [ ] **Step 1: Write the failing tests** (`internal/forms/smtp_test.go`)

```go
package forms

import (
	"context"
	"encoding/base64"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is just enough of an SMTP server to watch a client talk to it.
type fakeSMTP struct {
	offerAuth bool
	rcptReply string // "" = 250
	silent    bool   // accept the connection and never greet

	mu               sync.Mutex
	from, to, auth   string
	helo             string
	data             string
}

func (f *fakeSMTP) start(t *testing.T) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	if f.silent {
		time.Sleep(3 * time.Second)
		return
	}
	tp := textproto.NewConn(c)
	tp.PrintfLine("220 fake ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		up := strings.ToUpper(line)
		f.mu.Lock()
		switch {
		case strings.HasPrefix(up, "EHLO "):
			f.helo = line[5:]
			if f.offerAuth {
				tp.PrintfLine("250-fake")
				tp.PrintfLine("250 AUTH PLAIN")
			} else {
				tp.PrintfLine("250 fake")
			}
		case strings.HasPrefix(up, "AUTH PLAIN "):
			f.auth = line[len("AUTH PLAIN "):]
			tp.PrintfLine("235 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			f.from = line[len("MAIL FROM:"):]
			tp.PrintfLine("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			f.to = line[len("RCPT TO:"):]
			if f.rcptReply != "" {
				tp.PrintfLine("%s", f.rcptReply)
			} else {
				tp.PrintfLine("250 ok")
			}
		case up == "DATA":
			tp.PrintfLine("354 go ahead")
			f.mu.Unlock()
			b, err := tp.ReadDotBytes()
			f.mu.Lock()
			if err != nil {
				f.mu.Unlock()
				return
			}
			f.data = string(b)
			tp.PrintfLine("250 queued")
		case up == "QUIT":
			tp.PrintfLine("221 bye")
			f.mu.Unlock()
			return
		default:
			tp.PrintfLine("502 unknown")
		}
		f.mu.Unlock()
	}
}

var testMail = Mail{From: "forms@sitebin.example", To: "office@example.com", Data: []byte("Subject: hi\r\n\r\nhello\r\n")}

func TestSMTPSenderDelivers(t *testing.T) {
	f := &fakeSMTP{}
	host, port := f.start(t)
	if err := (&SMTPSender{Host: host, Port: port}).Send(context.Background(), testMail); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.from != "<forms@sitebin.example>" || f.to != "<office@example.com>" {
		t.Errorf("envelope %q -> %q", f.from, f.to)
	}
	if !strings.Contains(f.data, "Subject: hi") || !strings.Contains(f.data, "hello") {
		t.Errorf("data = %q", f.data)
	}
	if f.helo != "sitebin.example" {
		t.Errorf("EHLO %q, want the sender's domain", f.helo)
	}
}

func TestSMTPSenderAuthenticates(t *testing.T) {
	f := &fakeSMTP{offerAuth: true}
	host, port := f.start(t)
	if err := (&SMTPSender{Host: host, Port: port, User: "u", Pass: "p"}).Send(context.Background(), testMail); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	got, _ := base64.StdEncoding.DecodeString(f.auth)
	if string(got) != "\x00u\x00p" {
		t.Errorf("AUTH PLAIN carried %q", got)
	}
}

func TestSMTPSenderRefusesAuthTheServerLacks(t *testing.T) {
	f := &fakeSMTP{}
	host, port := f.start(t)
	err := (&SMTPSender{Host: host, Port: port, User: "u", Pass: "p"}).Send(context.Background(), testMail)
	if err == nil || !strings.Contains(err.Error(), "AUTH") {
		t.Fatalf("err = %v, want a clear AUTH error", err)
	}
}

func TestSMTPSenderReportsARefusedRecipient(t *testing.T) {
	f := &fakeSMTP{rcptReply: "550 no such user"}
	host, port := f.start(t)
	err := (&SMTPSender{Host: host, Port: port}).Send(context.Background(), testMail)
	if err == nil || !strings.Contains(err.Error(), "RCPT") {
		t.Fatalf("err = %v, want a RCPT error", err)
	}
}

func TestSMTPSenderGivesUpAtTheDeadline(t *testing.T) {
	f := &fakeSMTP{silent: true}
	host, port := f.start(t)
	start := time.Now()
	err := (&SMTPSender{Host: host, Port: port, Timeout: 300 * time.Millisecond}).Send(context.Background(), testMail)
	if err == nil {
		t.Fatal("a server that never answers was reported as a delivery")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("gave up after %v; the deadline was 300ms", d)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/forms/ -run SMTP -v`
Expected: build failure, `undefined: SMTPSender`.

- [ ] **Step 3: Implement** (`internal/forms/smtp.go`)

```go
package forms

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Sender delivers a built message. httpapi holds one; its tests swap in a
// recording fake.
type Sender interface {
	Send(ctx context.Context, m Mail) error
}

// SMTPSender delivers over SMTP: implicit TLS when ImplicitTLS is set,
// otherwise STARTTLS whenever the server offers it. net/smtp refuses to send
// credentials over an unencrypted connection to anything but localhost, which
// is the right default and is kept.
//
// Every exchange has one deadline, from dial to QUIT. smtp.SendMail has none,
// and a stuck server would otherwise hold a visitor's request open forever.
type SMTPSender struct {
	Host        string
	Port        int
	User, Pass  string
	ImplicitTLS bool
	Timeout     time.Duration // whole exchange; 0 means 30 seconds
	TLSConfig   *tls.Config   // nil verifies Host; tests supply their own
}

func (s *SMTPSender) Send(ctx context.Context, m Mail) error {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	conn.SetDeadline(deadline)
	tlsCfg := s.TLSConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: s.Host}
	}
	if s.ImplicitTLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fmt.Errorf("smtp tls: %w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer c.Close()
	if err := c.Hello(heloName(m.From)); err != nil {
		return fmt.Errorf("smtp EHLO: %w", err)
	}
	if !s.ImplicitTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("smtp STARTTLS: %w", err)
			}
		}
	}
	if s.User != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("smtp: the server offers no AUTH, but SITEBIN_FORMS_SMTP_USER is set")
		}
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Pass, s.Host)); err != nil {
			return fmt.Errorf("smtp AUTH: %w", err)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(m.Data); err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	return c.Quit()
}

// heloName greets with the sender's own domain; "localhost", net/smtp's
// default, is refused by some servers.
func heloName(from string) string {
	if i := strings.LastIndexByte(from, '@'); i >= 0 && i < len(from)-1 {
		return from[i+1:]
	}
	return "localhost"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/forms/ -run SMTP -v -race`
Expected: PASS, with no race reports.

- [ ] **Step 5: Commit**

```bash
git add internal/forms/smtp.go internal/forms/smtp_test.go
git commit -m "feat: form mail is delivered over SMTP with one deadline from dial to quit"
```

---

### Task 8: The ALTCHA captcha

**Files:**
- Create: `internal/forms/captcha.go`
- Create: `web/vendor/altcha.min.js` (vendored), `web/vendor/altcha.LICENSE`
- Modify: `go.mod`, `go.sum`
- Test: `internal/forms/captcha_test.go`

**Interfaces:**
- Produces: `type Captcha`, `func NewCaptcha(instanceSecret []byte) *Captcha`, `func (c *Captcha) Challenge(viewID, formKey string) (any, error)` (the value marshals to the JSON the widget expects), `func (c *Captcha) Verify(field, viewID, formKey string) error`, and `var ErrCaptcha`.
- Facts this task relies on (verified 2026-09-24 by running the library):
  - Pin: `github.com/altcha-org/altcha-lib-go/v2@v2.0.0-20260923082747-352eeeca913a`. That is the commit tagged `v2/v2.2.0`, a tag Go cannot resolve.
  - The HMAC signature covers **all** challenge parameters, including `data` and `expiresAt`.
  - **Trap:** `VerifySolution` with a nil `DeriveKey` accepts on the signature alone, so always pass `altcha.DeriveKeyPBKDF2()`.
  - **Trap:** no replay protection, so we record spent signatures.
  - Expiry is checked with `time.Now()` inside the library, at one-second granularity.
  - The widget field is `base64.StdEncoding(JSON{challenge:{parameters,signature}, solution})` = `altcha.Payload`.
  - Widget: `altcha@3.2.3`, `dist/main/altcha.min.js`, 115,652 bytes, sha256 `102bb89eb6ee4556068e2514880b7755495b23d90438c751809cb4f0ecbd4efb`. The element is `<altcha-widget challenge="URL">` and the field is `altcha`.

- [ ] **Step 1: Add the dependency and vendor the widget**

```bash
go get github.com/altcha-org/altcha-lib-go/v2@v2.0.0-20260923082747-352eeeca913a
curl -fsSL -o web/vendor/altcha.min.js https://cdn.jsdelivr.net/npm/altcha@3.2.3/dist/main/altcha.min.js
sha256sum web/vendor/altcha.min.js
```
Expected hash: `102bb89eb6ee4556068e2514880b7755495b23d90438c751809cb4f0ecbd4efb`. **Stop if it differs.**

Create `web/vendor/altcha.LICENSE`:

```
The ALTCHA widget (web/vendor/altcha.min.js, npm altcha@3.2.3) and the ALTCHA
Go library (github.com/altcha-org/altcha-lib-go/v2, whose module archive
carries no licence file) are used under the MIT License:

Copyright (c) 2023-2026 Daniel Regeci, BAU Software s.r.o.
Copyright (c) 2023 Daniel Regeci

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

- [ ] **Step 2: Write the failing tests** (`internal/forms/captcha_test.go`)

```go
package forms

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
)

// fastCaptcha keeps the real protocol but makes the work trivial, so the
// suite solves challenges in milliseconds.
func fastCaptcha(secret string) *Captcha {
	c := NewCaptcha([]byte(secret))
	c.cost, c.counterMin, c.counterSpan = 10, 5, 5
	return c
}

// solve does what the widget does: solve, then encode the form field.
func solve(t *testing.T, ch any) string {
	t.Helper()
	challenge := ch.(altcha.Challenge)
	sol, err := altcha.SolveChallenge(altcha.SolveChallengeOptions{Challenge: challenge, DeriveKey: altcha.DeriveKeyPBKDF2()})
	if err != nil || sol == nil {
		t.Fatalf("solve: %v", err)
	}
	b, _ := json.Marshal(map[string]any{
		"challenge": map[string]any{"parameters": challenge.Parameters, "signature": challenge.Signature},
		"solution":  sol,
	})
	return base64.StdEncoding.EncodeToString(b)
}

const capSecret = "0123456789abcdef0123456789abcdef"

func TestCaptchaAcceptsASolutionOnce(t *testing.T) {
	c := fastCaptcha(capSecret)
	ch, err := c.Challenge("site1", "form1")
	if err != nil {
		t.Fatal(err)
	}
	field := solve(t, ch)
	if err := c.Verify(field, "site1", "form1"); err != nil {
		t.Fatalf("a genuine solution was refused: %v", err)
	}
	if err := c.Verify(field, "site1", "form1"); !errors.Is(err, ErrCaptcha) {
		t.Fatal("a solution was accepted twice: the replay memory is missing")
	}
}

func TestCaptchaIsBoundToSiteAndForm(t *testing.T) {
	c := fastCaptcha(capSecret)
	ch, _ := c.Challenge("site1", "form1")
	field := solve(t, ch)
	if err := c.Verify(field, "site1", "form2"); !errors.Is(err, ErrCaptcha) {
		t.Error("a solution for form1 was accepted on form2")
	}
	if err := c.Verify(field, "site2", "form1"); !errors.Is(err, ErrCaptcha) {
		t.Error("a solution for site1 was accepted on site2")
	}
}

func TestCaptchaRefusesAnExpiredChallenge(t *testing.T) {
	c := fastCaptcha(capSecret)
	c.now = func() time.Time { return time.Now().Add(-10 * time.Minute) }
	ch, _ := c.Challenge("site1", "form1")
	c.now = time.Now
	if err := c.Verify(solve(t, ch), "site1", "form1"); !errors.Is(err, ErrCaptcha) {
		t.Error("a challenge older than 5 minutes was accepted")
	}
}

func TestCaptchaRefusesAnotherInstancesChallenge(t *testing.T) {
	a, b := fastCaptcha(capSecret), fastCaptcha(strings.Repeat("z", 32))
	ch, _ := a.Challenge("site1", "form1")
	if err := b.Verify(solve(t, ch), "site1", "form1"); !errors.Is(err, ErrCaptcha) {
		t.Error("a challenge signed by another instance secret was accepted")
	}
}

func TestCaptchaRefusesJunk(t *testing.T) {
	c := fastCaptcha(capSecret)
	testMode := base64.StdEncoding.EncodeToString([]byte(`{"challenge":null,"solution":null,"test":true}`))
	for _, junk := range []string{"", "%%%not-base64", base64.StdEncoding.EncodeToString([]byte("{}")), testMode} {
		if err := c.Verify(junk, "site1", "form1"); !errors.Is(err, ErrCaptcha) {
			t.Errorf("junk %q was accepted", junk)
		}
	}
	// A right challenge with a wrong solution: the proof of work is checked,
	// not just the signature (the library's nil-DeriveKey trap).
	ch, _ := c.Challenge("site1", "form1")
	var p map[string]any
	raw, _ := base64.StdEncoding.DecodeString(solve(t, ch))
	json.Unmarshal(raw, &p)
	p["solution"].(map[string]any)["derivedKey"] = strings.Repeat("00", 32)
	forged, _ := json.Marshal(p)
	if err := c.Verify(base64.StdEncoding.EncodeToString(forged), "site1", "form1"); !errors.Is(err, ErrCaptcha) {
		t.Error("a forged derived key was accepted")
	}
}

func TestCaptchaChallengeShape(t *testing.T) {
	ch, _ := NewCaptcha([]byte(capSecret)).Challenge("site1", "form1")
	b, _ := json.Marshal(ch)
	var got struct {
		Parameters struct {
			Algorithm string         `json:"algorithm"`
			ExpiresAt int64          `json:"expiresAt"`
			Data      map[string]any `json:"data"`
			Cost      int            `json:"cost"`
		} `json:"parameters"`
		Signature string `json:"signature"`
	}
	json.Unmarshal(b, &got)
	if got.Parameters.Algorithm != "PBKDF2/SHA-256" || got.Parameters.ExpiresAt == 0 || got.Signature == "" ||
		got.Parameters.Data["form"] != "form1" || got.Parameters.Data["site"] != "site1" || got.Parameters.Cost != captchaCost {
		t.Errorf("challenge JSON = %s", b)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/forms/ -run Captcha -v`
Expected: build failure, `undefined: NewCaptcha`.

- [ ] **Step 4: Implement** (`internal/forms/captcha.go`)

```go
package forms

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
)

// ErrCaptcha is every captcha refusal. The person is asked to try again. Why
// it failed is of no use to them and of real use to a bot.
var ErrCaptcha = errors.New("the captcha was not solved")

const (
	captchaTTL       = 5 * time.Minute
	captchaAlgorithm = "PBKDF2/SHA-256"
	// Deterministic mode: the server picks the counter and signs the key it
	// derives there, so the work is predictable and verifying is one HMAC.
	// At cost 1000 one derivation measured about 0.1 ms natively, so a
	// counter of 1000–1999 is about 0.2 s on one core, spread by the widget
	// over up to four workers. The rollout checks it on a real phone.
	captchaCost        = 1000
	captchaCounterMin  = 1000
	captchaCounterSpan = 1000
)

// Captcha issues and verifies ALTCHA v2 challenges, each bound to one site's
// form.
type Captcha struct {
	sigSecret, keySecret          string
	cost, counterMin, counterSpan int
	now                           func() time.Time

	mu   sync.Mutex
	used map[string]time.Time // spent challenge signature → when it expires
}

// NewCaptcha derives the captcha's two HMAC keys from the instance secret
// under their own labels; the raw secret is never used directly.
func NewCaptcha(instanceSecret []byte) *Captcha {
	return &Captcha{
		sigSecret:   deriveKey(instanceSecret, "forms:captcha:challenge"),
		keySecret:   deriveKey(instanceSecret, "forms:captcha:key"),
		cost:        captchaCost,
		counterMin:  captchaCounterMin,
		counterSpan: captchaCounterSpan,
		now:         time.Now,
		used:        map[string]time.Time{},
	}
}

func deriveKey(secret []byte, label string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(label))
	return hex.EncodeToString(m.Sum(nil))
}

// Challenge returns a fresh challenge for viewID's form formKey. Its data is
// ASCII on purpose: the widget encodes the payload with btoa, which mangles
// anything else.
func (c *Captcha) Challenge(viewID, formKey string) (any, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(c.counterSpan)))
	if err != nil {
		return nil, err
	}
	counter := c.counterMin + int(n.Int64())
	exp := c.now().Add(captchaTTL)
	ch, err := altcha.CreateChallenge(altcha.CreateChallengeOptions{
		Algorithm:              captchaAlgorithm,
		Cost:                   c.cost,
		Counter:                &counter,
		DeriveKey:              altcha.DeriveKeyPBKDF2(),
		ExpiresAt:              &exp,
		Data:                   map[string]interface{}{"site": viewID, "form": formKey},
		HMACSignatureSecret:    c.sigSecret,
		HMACKeySignatureSecret: c.keySecret,
	})
	return ch, err
}

// Verify checks the widget's altcha field for viewID's form formKey. A
// verified challenge is spent: the library does not track replays, so this
// does.
func (c *Captcha) Verify(field, viewID, formKey string) error {
	raw, err := base64.StdEncoding.DecodeString(field)
	if field == "" || err != nil {
		return ErrCaptcha
	}
	var p altcha.Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return ErrCaptcha
	}
	// DeriveKey is passed on every call: without it the library accepts on
	// the signature alone and never checks the work.
	res, err := altcha.VerifySolution(altcha.VerifySolutionOptions{
		Challenge:              p.Challenge,
		Solution:               p.Solution,
		DeriveKey:              altcha.DeriveKeyPBKDF2(),
		HMACSignatureSecret:    c.sigSecret,
		HMACKeySignatureSecret: c.keySecret,
	})
	if err != nil || !res.Verified {
		return ErrCaptcha
	}
	params := p.Challenge.Parameters
	if params.ExpiresAt == 0 || params.Data["site"] != viewID || params.Data["form"] != formKey {
		return ErrCaptcha
	}
	if !c.spend(p.Challenge.Signature, time.Unix(params.ExpiresAt, 0)) {
		return ErrCaptcha
	}
	return nil
}

// spend records a verified challenge until it expires and reports whether it
// was fresh. A restart forgets them, which reopens at most a 5-minute window.
func (c *Captcha) spend(sig string, exp time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, e := range c.used {
		if now.After(e) {
			delete(c.used, k)
		}
	}
	if _, seen := c.used[sig]; seen {
		return false
	}
	// The library accepts a challenge through its expiry second.
	c.used[sig] = exp.Add(time.Second)
	return true
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go mod tidy && go test ./internal/forms/ -run Captcha -v && go vet ./... && go test ./... && go test -tags ee ./...`
Expected: PASS. `go.mod` gains exactly one direct require for `altcha-lib-go/v2` at the pseudo-version.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/forms/captcha.go internal/forms/captcha_test.go web/vendor/altcha.min.js web/vendor/altcha.LICENSE
git commit -m "feat: forms can require an ALTCHA proof of work bound to the site and form"
```

---
### Task 9: The forms JSON API, caps and confirmation mail

**Files:**
- Create: `internal/httpapi/forms.go` (state, cap resolution, shared operations), `internal/httpapi/formsapi.go` (handlers)
- Modify: `internal/httpapi/server.go`: an `API.forms` field, `New` builds it, and the routes
- Test: `internal/httpapi/formsapi_test.go`, `internal/httpapi/formsmail_test.go` (test helpers)

**Interfaces:**
- Consumes: Tasks 1–8 (`config.FormsSMTP`, the `store` form methods, `forms.*`).
- Produces (package `httpapi`, used by Tasks 10–12):
  - `type formsState struct{ send forms.Sender; links forms.Links; captcha *forms.Captcha; perIP, perForm, challenges, confirmSite, confirmAddr *auth.Limiter }`, held as `API.forms`, which is nil when forms are off
  - `func (a *API) formsLimit(site *store.Site) int`
  - `func (a *API) stampFormsQuota(site *store.Site) error`
  - `func (a *API) formsListing(site *store.Site) formsJSON`, and `func (a *API) listFormsFor(site *store.Site) formsJSON` (the listing after the one-time plan lookup; the API's GET and MCP's `list_forms` use it)
  - `type formInput struct{ Name, Recipient, Redirect *string; Captcha, Files *bool }`
  - `func (a *API) addForm(ctx context.Context, site *store.Site, in formInput) (formsJSON, error)`
  - `func (a *API) updateForm(ctx context.Context, site *store.Site, key string, in formInput) (formsJSON, error)`
  - `func (a *API) deleteForm(site *store.Site, key string) (formsJSON, error)`
  - `func (a *API) resendConfirmation(ctx context.Context, site *store.Site, key string) (formsJSON, error)`
  - `func (a *API) formHost(site *store.Site) string`, `func (a *API) formSiteQuery(site *store.Site) string` and `func (a *API) baseURL() string`
  - `type formsJSON struct{ Enabled bool; Limit, Used, MaxFiles int; Forms []formJSON; Warnings []string }`, where `formJSON` carries `status` (with `paused`) and `snippet`
  - test helpers: `recSender`, `formsEnv(t, over)` and `mailText(t, m)`

- [ ] **Step 1: Write the test helpers** (`internal/httpapi/formsmail_test.go`)

```go
package httpapi

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/ittrail/sitebin.io/internal/forms"
)

// recSender records what would have been mailed.
type recSender struct {
	mu   sync.Mutex
	sent []forms.Mail
	err  error
}

func (s *recSender) Send(_ context.Context, m forms.Mail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, m)
	return nil
}

func (s *recSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *recSender) last(t *testing.T) forms.Mail {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		t.Fatal("nothing was mailed")
	}
	return s.sent[len(s.sent)-1]
}

// formsEnv is a community instance with forms on and a recording mailer.
func formsEnv(t *testing.T, over map[string]string) (*env, *recSender) {
	t.Helper()
	vars := map[string]string{"SITEBIN_FORMS_SMTP_HOST": "smtp.test", "SITEBIN_FORMS_SMTP_FROM": "forms@sitebin.example"}
	for k, v := range over {
		vars[k] = v
	}
	e := newEnv(t, vars)
	rs := &recSender{}
	e.api.forms.send = rs
	return e, rs
}

// mailText returns a built mail's decoded text/plain part.
func mailText(t *testing.T, m forms.Mail) string {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(m.Data))
	if err != nil {
		t.Fatal(err)
	}
	var find func(ct string, r io.Reader) string
	find = func(ct string, r io.Reader) string {
		_, params, _ := mime.ParseMediaType(ct)
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err != nil {
				return ""
			}
			pct := p.Header.Get("Content-Type")
			if strings.HasPrefix(pct, "multipart/") {
				if s := find(pct, p); s != "" {
					return s
				}
				continue
			}
			if strings.HasPrefix(pct, "text/plain") {
				b, _ := io.ReadAll(quotedprintable.NewReader(p))
				return string(b)
			}
		}
	}
	return find(msg.Header.Get("Content-Type"), msg.Body)
}

var confirmTokenRe = regexp.MustCompile(`/forms/confirm\?t=([A-Za-z0-9_.%-]+)`)

// confirmToken pulls the token out of a confirmation mail.
func confirmToken(t *testing.T, m forms.Mail) string {
	t.Helper()
	sm := confirmTokenRe.FindStringSubmatch(mailText(t, m))
	if sm == nil {
		t.Fatalf("no confirmation link in:\n%s", mailText(t, m))
	}
	return sm[1]
}
```

- [ ] **Step 2: Write the failing tests** (`internal/httpapi/formsapi_test.go`)

```go
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

type formsResp struct {
	Enabled  bool     `json:"enabled"`
	Limit    int      `json:"limit"`
	Used     int      `json:"used"`
	MaxFiles int      `json:"max_files"`
	Warnings []string `json:"warnings"`
	Forms    []struct {
		Key, Name, Recipient, Redirect, Status, Snippet string
		Captcha, Files                                  bool
	} `json:"forms"`
}

func (e *env) formsCall(t *testing.T, method, editID, pw, path string, body any) (*httptest.ResponseRecorder, formsResp) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "/api/sites/"+editID+"/forms"+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin") // the edit page's own fetch
	w := e.public(t, authed(req, pw))
	var out formsResp
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func newFormSite(t *testing.T, e *env) (editID, pw, viewID string) {
	t.Helper()
	c := e.createSite(t, nil, map[string]string{"index.html": "<h1>hi</h1>"})
	return editIDFrom(t, c.EditURL), c.EditPassword, c.ID
}

func TestFormsOffOnTheInstance(t *testing.T) {
	e := newEnv(t, nil)
	id, pw, _ := newFormSite(t, e)
	w, out := e.formsCall(t, "GET", id, pw, "", nil)
	if w.Code != 200 || out.Enabled {
		t.Fatalf("GET = %d enabled=%v, want 200 and enabled=false", w.Code, out.Enabled)
	}
	w, _ = e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "Contact", "recipient": "a@example.com"})
	if w.Code != 409 {
		t.Fatalf("POST with forms off = %d, want 409", w.Code)
	}
}

func TestAddFormSendsAConfirmation(t *testing.T) {
	e, rs := formsEnv(t, nil)
	id, pw, viewID := newFormSite(t, e)
	w, out := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "Contact", "recipient": "office@example.com", "captcha": true, "redirect": "/danke.html"})
	if w.Code != 201 {
		t.Fatalf("POST = %d %s", w.Code, w.Body)
	}
	if len(out.Forms) != 1 || out.Forms[0].Status != "pending" || !out.Forms[0].Captcha || out.Forms[0].Redirect != "/danke.html" {
		t.Fatalf("forms = %+v", out.Forms)
	}
	key := out.Forms[0].Key
	if !strings.Contains(out.Forms[0].Snippet, `action="/_sitebin/forms/`+key+`"`) || !strings.Contains(out.Forms[0].Snippet, "altcha-widget") {
		t.Errorf("snippet = %s", out.Forms[0].Snippet)
	}
	m := rs.last(t)
	if m.To != "office@example.com" || m.From != "forms@sitebin.example" {
		t.Errorf("confirmation envelope %s -> %s", m.From, m.To)
	}
	tok, _ := url.QueryUnescape(confirmToken(t, m))
	c, ok := e.api.forms.links.ParseConfirm(tok, time.Now())
	if !ok || c.ViewID != viewID || c.Key != key || c.Recipient != "office@example.com" || c.Seq != 0 {
		t.Errorf("confirmation token = %+v %v", c, ok)
	}
}

func TestAddFormValidates(t *testing.T) {
	e, rs := formsEnv(t, nil)
	id, pw, _ := newFormSite(t, e)
	for _, body := range []map[string]any{
		{"recipient": "a@example.com"},
		{"name": "Contact"},
		{"name": "Contact\r\nBcc: x", "recipient": "a@example.com"},
		{"name": "Contact", "recipient": "Office <a@example.com>"},
		{"name": "Contact", "recipient": "a@example.com", "redirect": "https://evil.example/"},
	} {
		if w, _ := e.formsCall(t, "POST", id, pw, "", body); w.Code != 400 {
			t.Errorf("%v: %d, want 400", body, w.Code)
		}
	}
	if rs.count() != 0 {
		t.Error("an invalid form mailed someone")
	}
}

func TestFormsCapCommunityDefault(t *testing.T) {
	e, _ := formsEnv(t, nil)
	id, pw, _ := newFormSite(t, e)
	for i := 0; i < 10; i++ {
		if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "F", "recipient": fmt.Sprintf("r%d@example.com", i)}); w.Code != 201 {
			t.Fatalf("form %d: %d %s", i+1, w.Code, w.Body)
		}
	}
	w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "F", "recipient": "r10@example.com"})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "10 form") {
		t.Fatalf("11th form = %d %s, want 403 naming the cap of 10", w.Code, w.Body)
	}
}

func TestFormsCapFromTheEnvironment(t *testing.T) {
	e, _ := formsEnv(t, map[string]string{"SITEBIN_FORMS_MAX_PER_SITE": "1"})
	id, pw, _ := newFormSite(t, e)
	e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "B", "recipient": "b@example.com"}); w.Code != 403 {
		t.Fatalf("second form with SITEBIN_FORMS_MAX_PER_SITE=1 = %d", w.Code)
	}
}

// With a provider, a site without a stamped cap has none. Without this rule
// every Drop and Free site on the hosted instance would gain 10 forms the day
// the feature ships.
func TestFormsCapIsZeroForAnUnstampedSiteWithAProvider(t *testing.T) {
	e, _ := formsEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()
	id, pw, _ := newFormSite(t, e)
	w, out := e.formsCall(t, "GET", id, pw, "", nil)
	if w.Code != 200 || out.Limit != 0 {
		t.Fatalf("GET = %d limit=%d, want limit 0", w.Code, out.Limit)
	}
	if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"}); w.Code != 403 {
		t.Fatalf("POST = %d, want 403", w.Code)
	}
}

// A Pro site created before forms existed has no stamp; its plan is asked once.
func TestFormsCapIsStampedFromThePlanWhenFirstNeeded(t *testing.T) {
	e, _ := formsEnv(t, nil)
	fp := &fakeProvider{enabled: true, owner: "acct-1", quota: ext.CreateGrant{MaxForms: intp(1)}, quotaOK: true}
	ext.Register(fp)
	defer ext.Reset()
	id, pw, viewID := newFormSite(t, e)
	site, _ := e.st.ByViewID(viewID)
	if site.Meta.QuotaForms != nil {
		t.Fatal("precondition: the grant carried no MaxForms, so nothing is stamped at creation")
	}
	_, out := e.formsCall(t, "GET", id, pw, "", nil)
	if out.Limit != 1 {
		t.Fatalf("listing limit = %d, want the plan's 1", out.Limit)
	}
	site, _ = e.st.ByViewID(viewID)
	if site.Meta.QuotaForms == nil || *site.Meta.QuotaForms != 1 {
		t.Fatalf("QuotaForms = %v after the first listing, want 1 stamped", site.Meta.QuotaForms)
	}
	e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "B", "recipient": "b@example.com"}); w.Code != 403 {
		t.Fatalf("second form on a 1-form plan = %d", w.Code)
	}
}

func TestFormsPlanLookupErrorRefusesTheAdd(t *testing.T) {
	e, rs := formsEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", quotaErr: errors.New("paygate down")})
	defer ext.Reset()
	id, pw, viewID := newFormSite(t, e)
	if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"}); w.Code != 503 {
		t.Fatalf("POST = %d, want 503 while the plan is unknown", w.Code)
	}
	site, _ := e.st.ByViewID(viewID)
	if site.Meta.QuotaForms != nil || len(site.Meta.Forms) != 0 || rs.count() != 0 {
		t.Error("a failed plan lookup changed something")
	}
}

func TestUpdateFormRecipientConfirmsAgain(t *testing.T) {
	e, rs := formsEnv(t, nil)
	id, pw, viewID := newFormSite(t, e)
	_, out := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	key := out.Forms[0].Key
	site, _ := e.st.ByViewID(viewID)
	e.st.ConfirmForm(site, key, "a@example.com", 0)

	w, out := e.formsCall(t, "PUT", id, pw, "/"+key, map[string]any{"name": "Renamed"})
	if w.Code != 200 || out.Forms[0].Status != "active" || out.Forms[0].Name != "Renamed" || rs.count() != 1 {
		t.Fatalf("rename: %d %+v mails=%d", w.Code, out.Forms, rs.count())
	}
	w, out = e.formsCall(t, "PUT", id, pw, "/"+key, map[string]any{"recipient": "b@example.com"})
	if w.Code != 200 || out.Forms[0].Status != "pending" || rs.count() != 2 || rs.last(t).To != "b@example.com" {
		t.Fatalf("new recipient: %d %+v mails=%d", w.Code, out.Forms, rs.count())
	}
}

func TestDeleteForm(t *testing.T) {
	e, _ := formsEnv(t, nil)
	id, pw, _ := newFormSite(t, e)
	_, out := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	if w, _ := e.formsCall(t, "DELETE", id, pw, "/"+out.Forms[0].Key, nil); w.Code != 204 {
		t.Fatalf("DELETE = %d", w.Code)
	}
	if _, out = e.formsCall(t, "GET", id, pw, "", nil); len(out.Forms) != 0 {
		t.Errorf("forms after delete = %+v", out.Forms)
	}
	if w, _ := e.formsCall(t, "DELETE", id, pw, "/nosuchkey", nil); w.Code != 404 {
		t.Errorf("DELETE unknown = %d", w.Code)
	}
}

func TestResendConfirmation(t *testing.T) {
	e, rs := formsEnv(t, nil)
	id, pw, viewID := newFormSite(t, e)
	_, out := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	key := out.Forms[0].Key
	if w, _ := e.formsCall(t, "POST", id, pw, "/"+key+"/confirmation", nil); w.Code != 202 || rs.count() != 2 {
		t.Fatalf("resend pending = %d mails=%d", w.Code, rs.count())
	}
	site, _ := e.st.ByViewID(viewID)
	e.st.ConfirmForm(site, key, "a@example.com", 0)
	if w, _ := e.formsCall(t, "POST", id, pw, "/"+key+"/confirmation", nil); w.Code != 409 {
		t.Fatalf("resend active = %d, want 409", w.Code)
	}
}

func TestConfirmationMailsAreThrottledPerAddress(t *testing.T) {
	e, rs := formsEnv(t, nil)
	id, pw, _ := newFormSite(t, e)
	for i := 0; i < 3; i++ {
		if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "F", "recipient": "same@example.com"}); w.Code != 201 {
			t.Fatalf("form %d = %d", i+1, w.Code)
		}
	}
	w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "F", "recipient": "same@example.com"})
	if w.Code != 429 || rs.count() != 3 {
		t.Fatalf("4th mail to one address today = %d (mails %d), want 429 and nothing sent", w.Code, rs.count())
	}
	_, out := e.formsCall(t, "GET", id, pw, "", nil)
	if len(out.Forms) != 3 {
		t.Errorf("a throttled add still created a form: %d forms", len(out.Forms))
	}
}

func TestConfirmationMailFailureIsAWarning(t *testing.T) {
	e, rs := formsEnv(t, nil)
	rs.err = errors.New("dial tcp: connection refused")
	id, pw, _ := newFormSite(t, e)
	w, out := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	if w.Code != 201 || len(out.Warnings) == 0 || out.Forms[0].Status != "pending" {
		t.Fatalf("POST = %d warnings=%v forms=%+v", w.Code, out.Warnings, out.Forms)
	}
}

func TestFormsAttachmentsNeedTheInstanceToAllowThem(t *testing.T) {
	e, _ := formsEnv(t, map[string]string{"SITEBIN_FORMS_MAX_FILES": "0"})
	id, pw, _ := newFormSite(t, e)
	if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com", "files": true}); w.Code != 400 {
		t.Fatalf("files on an instance without attachments = %d, want 400", w.Code)
	}
}

func TestFormsBeyondTheCapArePaused(t *testing.T) {
	e, _ := formsEnv(t, nil)
	id, pw, viewID := newFormSite(t, e)
	e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "B", "recipient": "b@example.com"})
	site, _ := e.st.ByViewID(viewID)
	e.st.Update(site, func(m *store.Meta) error { m.QuotaForms = intp(1); return nil }) // a downgrade
	_, out := e.formsCall(t, "GET", id, pw, "", nil)
	if out.Forms[0].Status != "pending" || out.Forms[1].Status != "paused" || out.Limit != 1 || out.Used != 2 {
		t.Fatalf("after a downgrade: %+v limit %d used %d", out.Forms, out.Limit, out.Used)
	}
}

func TestFormsSnippetOnPathViews(t *testing.T) {
	e, _ := formsEnv(t, map[string]string{"SITEBIN_VIEW_ACCESS": "path"})
	id, pw, viewID := newFormSite(t, e)
	_, out := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	if !strings.Contains(out.Forms[0].Snippet, "?_site="+viewID) {
		t.Errorf("path-view snippet lacks the site: %s", out.Forms[0].Snippet)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/httpapi/ -run 'Forms|Form' -v`
Expected: build failure, `e.api.forms undefined`.

- [ ] **Step 4: Implement**

`internal/httpapi/forms.go`:

```go
package httpapi

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/config"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/forms"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Email forms: the core half. internal/forms holds the logic; this file wires
// it to the store, the extension seam and HTTP. Design:
// docs/superpowers/specs/2026-09-24-site-forms-design.md.

const (
	// Confirmation mails are throttled per site and per address. Constants,
	// not configuration: they protect the instance's mail reputation, not a
	// plan.
	confirmPerSitePerDay = 10
	confirmPerAddrPerDay = 3
	formSendTimeout      = 30 * time.Second
	// formsDefaultNoProvider is the cap of a site with no stamped quota on an
	// instance with no extension, unless SITEBIN_FORMS_MAX_PER_SITE says
	// otherwise.
	formsDefaultNoProvider = 10
)

// formsState exists only when SITEBIN_FORMS_SMTP_HOST is set; API.forms is
// nil otherwise, and every forms route answers as if there were no forms.
type formsState struct {
	send        forms.Sender
	links       forms.Links
	captcha     *forms.Captcha
	perIP       *auth.Limiter // submissions per client IP, all forms
	perForm     *auth.Limiter // submissions per form
	challenges  *auth.Limiter // captcha challenges per client IP
	confirmSite *auth.Limiter // confirmation mails per site
	confirmAddr *auth.Limiter // confirmation mails per address
}

func newFormsState(cfg config.Config, secret []byte) *formsState {
	if cfg.FormsSMTP == nil {
		return nil
	}
	s := cfg.FormsSMTP
	return &formsState{
		send:        &forms.SMTPSender{Host: s.Host, Port: s.Port, User: s.User, Pass: s.Pass, ImplicitTLS: s.TLS, Timeout: formSendTimeout},
		links:       forms.NewLinks(secret),
		captcha:     forms.NewCaptcha(secret),
		perIP:       auth.NewLimiter(float64(cfg.FormsPerIPHour), cfg.FormsPerIPHour),
		perForm:     auth.NewLimiter(float64(cfg.FormsPerFormHour), cfg.FormsPerFormHour),
		challenges:  auth.NewLimiter(float64(3*cfg.FormsPerIPHour), 3*cfg.FormsPerIPHour),
		confirmSite: auth.NewLimiter(confirmPerSitePerDay/24.0, confirmPerSitePerDay),
		confirmAddr: auth.NewLimiter(confirmPerAddrPerDay/24.0, confirmPerAddrPerDay),
	}
}

var (
	errFormsOff         = &apiError{409, "forms are not enabled on this instance"}
	errConfirmThrottled = &apiError{429, "too many confirmation emails for this site or address today — try again tomorrow"}
	errPlanUnknown      = &apiError{503, "this site's plan could not be determined right now — try again shortly"}
)

const confirmNotSent = "the confirmation email could not be sent; the form stays pending until you resend it"

func (a *API) baseURL() string { return a.cfg.SiteURL(a.cfg.BaseDomain) }

// formsLimit is the site's forms cap: the stamped value, else the instance's.
func (a *API) formsLimit(site *store.Site) int {
	if site.Meta.QuotaForms != nil {
		return *site.Meta.QuotaForms
	}
	if a.cfg.FormsMaxPerSite != nil {
		return *a.cfg.FormsMaxPerSite
	}
	if _, ok := ext.Get(); ok {
		// Fail closed: with accounts in play, a site nobody stamped gets no
		// forms rather than the community build's generous default.
		return 0
	}
	return formsDefaultNoProvider
}

// stampFormsQuota asks the owner's plan once for a site created before forms
// existed, and writes the answer down. It runs where forms are added or
// listed, never on a submission. An error means the plan is unknown.
func (a *API) stampFormsQuota(site *store.Site) error {
	if site.Meta.QuotaForms != nil || site.Meta.OwnerAccountID == "" {
		return nil
	}
	p, ok := ext.Get()
	if !ok {
		return nil
	}
	g, found, err := p.QuotaFor(site.Meta.OwnerAccountID)
	if err != nil {
		return errPlanUnknown
	}
	if !found || g.MaxForms == nil {
		return nil
	}
	return a.st.Update(site, func(m *store.Meta) error {
		if m.QuotaForms == nil {
			m.QuotaForms = g.MaxForms
		}
		return nil
	})
}

// formHost is how the recipient's mails name the site: its first verified
// custom domain, which the owner's visitors know, else its own address.
func (a *API) formHost(site *store.Site) string {
	if len(site.Meta.CustomDomains) > 0 {
		return site.Meta.CustomDomains[0]
	}
	u := a.cfg.ViewURL(site.ViewID)
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.TrimSuffix(u, "/")
}

// formSiteQuery is what every /_sitebin/forms URL carries on an instance
// that serves sites under /v/<id>/ on the main domain.
func (a *API) formSiteQuery(site *store.Site) string {
	if !a.cfg.PathViews() {
		return ""
	}
	return "?_site=" + site.ViewID
}

type formJSON struct {
	Key         string     `json:"key"`
	Name        string     `json:"name"`
	Recipient   string     `json:"recipient"`
	Captcha     bool       `json:"captcha"`
	Files       bool       `json:"files"`
	Redirect    string     `json:"redirect,omitempty"`
	Status      string     `json:"status"` // pending | active | stopped | paused
	CreatedAt   time.Time  `json:"created_at"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	StoppedAt   *time.Time `json:"stopped_at,omitempty"`
	Snippet     string     `json:"snippet"`
}

type formsJSON struct {
	Enabled  bool       `json:"enabled"`
	Limit    int        `json:"limit"`
	Used     int        `json:"used"`
	MaxFiles int        `json:"max_files"`
	Forms    []formJSON `json:"forms"`
	Warnings []string   `json:"warnings,omitempty"`
}

// formsListing is the site's forms as the API, MCP and the edit page see
// them. Tokens never appear here.
func (a *API) formsListing(site *store.Site) formsJSON {
	limit := a.formsLimit(site)
	out := formsJSON{Enabled: a.forms != nil, Limit: limit, Used: len(site.Meta.Forms), MaxFiles: a.cfg.FormsMaxFiles, Forms: []formJSON{}}
	for i, f := range site.Meta.Forms {
		status := f.Status
		if store.FormPaused(i, limit) {
			status = "paused"
		}
		out.Forms = append(out.Forms, formJSON{
			Key: f.Key, Name: f.Name, Recipient: f.Recipient, Captcha: f.Captcha, Files: f.Files,
			Redirect: f.Redirect, Status: status, CreatedAt: f.CreatedAt,
			ConfirmedAt: f.ConfirmedAt, StoppedAt: f.StoppedAt,
			Snippet: forms.Snippet(forms.SnippetOptions{Key: f.Key, Captcha: f.Captcha, Files: f.Files, SiteQuery: a.formSiteQuery(site)}),
		})
	}
	return out
}

// listFormsFor is formsListing after the one-time plan lookup. A failed lookup
// shows the instance value and is retried on the next listing.
func (a *API) listFormsFor(site *store.Site) formsJSON {
	if err := a.stampFormsQuota(site); err != nil {
		a.log.Warn("forms: plan lookup failed", "id", site.ViewID, "err", err)
	}
	return a.formsListing(site)
}

// formInput is a form's settings as the API and MCP take them. A nil field is
// left alone on update; name and recipient are required on create.
type formInput struct {
	Name      *string `json:"name"`
	Recipient *string `json:"recipient"`
	Captcha   *bool   `json:"captcha"`
	Files     *bool   `json:"files"`
	Redirect  *string `json:"redirect"`
}

func (a *API) cleanFormInput(in formInput, create bool) (store.FormPatch, error) {
	var p store.FormPatch
	if in.Name != nil {
		n, err := forms.CleanName(*in.Name)
		if err != nil {
			return p, &apiError{400, err.Error()}
		}
		p.Name = &n
	} else if create {
		return p, &apiError{400, "name is required"}
	}
	if in.Recipient != nil {
		r, err := forms.CleanRecipient(*in.Recipient)
		if err != nil {
			return p, &apiError{400, err.Error()}
		}
		p.Recipient = &r
	} else if create {
		return p, &apiError{400, "recipient is required"}
	}
	if in.Redirect != nil {
		r, err := forms.CleanRedirect(*in.Redirect)
		if err != nil {
			return p, &apiError{400, err.Error()}
		}
		p.Redirect = &r
	}
	if in.Files != nil && *in.Files && a.cfg.FormsMaxFiles == 0 {
		return p, &apiError{400, "this instance accepts no attachments (SITEBIN_FORMS_MAX_FILES=0)"}
	}
	p.Captcha, p.Files = in.Captcha, in.Files
	return p, nil
}

func (a *API) allowConfirmation(site *store.Site, addr string) bool {
	return a.forms.confirmSite.Allow(site.ViewID) && a.forms.confirmAddr.Allow(strings.ToLower(addr))
}

// addForm creates a pending form and mails its recipient. The cap and the
// mail throttles are checked first, so a refusal creates nothing; a mail that
// fails after the form exists is a warning, and the owner can resend.
func (a *API) addForm(ctx context.Context, site *store.Site, in formInput) (formsJSON, error) {
	if a.forms == nil {
		return formsJSON{}, errFormsOff
	}
	p, err := a.cleanFormInput(in, true)
	if err != nil {
		return formsJSON{}, err
	}
	if err := a.stampFormsQuota(site); err != nil {
		return formsJSON{}, err
	}
	limit := a.formsLimit(site)
	if len(site.Meta.Forms) >= limit {
		return formsJSON{}, &apiError{403, tooManyFormsMsg(limit)}
	}
	if !a.allowConfirmation(site, *p.Recipient) {
		return formsJSON{}, errConfirmThrottled
	}
	spec := store.Form{Name: *p.Name, Recipient: *p.Recipient}
	if p.Captcha != nil {
		spec.Captcha = *p.Captcha
	}
	if p.Files != nil {
		spec.Files = *p.Files
	}
	if p.Redirect != nil {
		spec.Redirect = *p.Redirect
	}
	f, err := a.st.AddForm(site, spec, limit)
	if errors.Is(err, store.ErrTooManyForms) {
		return formsJSON{}, &apiError{403, tooManyFormsMsg(limit)}
	}
	if err != nil {
		return formsJSON{}, err
	}
	a.log.Info("form added", "id", site.ViewID, "form", f.Key)
	out := a.formsListing(site)
	if err := a.sendConfirmation(ctx, site, f); err != nil {
		out.Warnings = append(out.Warnings, confirmNotSent)
	}
	return out, nil
}

func tooManyFormsMsg(limit int) string {
	if limit == 0 {
		return "this site's plan includes no forms"
	}
	if limit == 1 {
		return "this site's plan allows 1 form"
	}
	return "this site's plan allows " + strconv.Itoa(limit) + " forms"
}

// updateForm changes settings. A new recipient is throttled like a new form,
// goes back to pending and gets its own confirmation mail.
func (a *API) updateForm(ctx context.Context, site *store.Site, key string, in formInput) (formsJSON, error) {
	if a.forms == nil {
		return formsJSON{}, errFormsOff
	}
	p, err := a.cleanFormInput(in, false)
	if err != nil {
		return formsJSON{}, err
	}
	cur, _, ok := store.FindForm(site.Meta, key)
	if !ok {
		return formsJSON{}, store.ErrFormNotFound
	}
	if p.Recipient != nil && *p.Recipient != cur.Recipient && !a.allowConfirmation(site, *p.Recipient) {
		return formsJSON{}, errConfirmThrottled
	}
	f, changed, err := a.st.UpdateForm(site, key, p)
	if err != nil {
		return formsJSON{}, err
	}
	out := a.formsListing(site)
	if changed {
		a.log.Info("form recipient changed", "id", site.ViewID, "form", f.Key)
		if err := a.sendConfirmation(ctx, site, f); err != nil {
			out.Warnings = append(out.Warnings, confirmNotSent)
		}
	}
	return out, nil
}

func (a *API) deleteForm(site *store.Site, key string) (formsJSON, error) {
	if err := a.st.DeleteForm(site, key); err != nil {
		return formsJSON{}, err
	}
	a.log.Info("form deleted", "id", site.ViewID, "form", key)
	return a.formsListing(site), nil
}

// resendConfirmation mails the recipient again, for a pending form or one the
// recipient stopped. A mail that fails here is an error: sending it is the
// whole request.
func (a *API) resendConfirmation(ctx context.Context, site *store.Site, key string) (formsJSON, error) {
	if a.forms == nil {
		return formsJSON{}, errFormsOff
	}
	cur, _, ok := store.FindForm(site.Meta, key)
	if !ok {
		return formsJSON{}, store.ErrFormNotFound
	}
	if cur.Status == store.FormActive {
		return formsJSON{}, store.ErrFormActive
	}
	if !a.allowConfirmation(site, cur.Recipient) {
		return formsJSON{}, errConfirmThrottled
	}
	f, err := a.st.RequestConfirmation(site, key)
	if err != nil {
		return formsJSON{}, err
	}
	if err := a.sendConfirmation(ctx, site, f); err != nil {
		return formsJSON{}, &apiError{502, "the confirmation email could not be sent — try again later"}
	}
	return a.formsListing(site), nil
}

// sendConfirmation mails f's recipient the link that activates the form.
func (a *API) sendConfirmation(ctx context.Context, site *store.Site, f store.Form) error {
	now := time.Now()
	tok := a.forms.links.ConfirmToken(forms.ConfirmClaim{ViewID: site.ViewID, Key: f.Key, Recipient: f.Recipient, Seq: f.Seq}, now)
	m, err := forms.BuildConfirmation(forms.ConfirmationMail{
		From:       a.cfg.FormsSMTP.From,
		FormName:   f.Name,
		Recipient:  f.Recipient,
		Host:       a.formHost(site),
		ConfirmURL: a.baseURL() + "/forms/confirm?t=" + url.QueryEscape(tok),
		At:         now,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, formSendTimeout)
	defer cancel()
	if err := a.forms.send.Send(ctx, m); err != nil {
		a.log.Error("form confirmation not sent", "id", site.ViewID, "form", f.Key, "err", redact(err, f.Recipient))
		return err
	}
	return nil
}

// redact keeps an address out of the log: SMTP servers like to quote the
// recipient in their refusals, and the log must never hold one.
func redact(err error, addr string) string {
	return strings.ReplaceAll(err.Error(), addr, "<recipient>")
}
```

`internal/httpapi/formsapi.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/ittrail/sitebin.io/internal/store"
)

func decodeFormInput(w http.ResponseWriter, r *http.Request, in *formInput) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(in); err != nil {
		writeError(w, 400, `body must be a JSON object such as {"name": "Contact", "recipient": "office@example.com"}`)
		return false
	}
	return true
}

func (a *API) listForms(w http.ResponseWriter, r *http.Request, site *store.Site) {
	writeJSON(w, 200, a.listFormsFor(site))
}

func (a *API) createForm(w http.ResponseWriter, r *http.Request, site *store.Site) {
	var in formInput
	if !decodeFormInput(w, r, &in) {
		return
	}
	out, err := a.addForm(r.Context(), site, in)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 201, out)
}

func (a *API) patchForm(w http.ResponseWriter, r *http.Request, site *store.Site) {
	var in formInput
	if !decodeFormInput(w, r, &in) {
		return
	}
	out, err := a.updateForm(r.Context(), site, r.PathValue("key"), in)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *API) removeForm(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if _, err := a.deleteForm(site, r.PathValue("key")); err != nil {
		respondErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func (a *API) resendFormConfirmation(w http.ResponseWriter, r *http.Request, site *store.Site) {
	out, err := a.resendConfirmation(r.Context(), site, r.PathValue("key"))
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 202, out)
}
```

`internal/httpapi/server.go`:
- Add `forms *formsState // nil when the instance has no forms` to `API`.
- In `New`, add `forms: newFormsState(cfg, secret),` to the literal.
- In `Public()`, after the `containers` routes:

```go
	mux.HandleFunc("GET /api/sites/{editID}/forms", a.withEditAuth(a.listForms))
	mux.HandleFunc("POST /api/sites/{editID}/forms", a.withEditAuth(a.createForm))
	mux.HandleFunc("PUT /api/sites/{editID}/forms/{key}", a.withEditAuth(a.patchForm))
	mux.HandleFunc("DELETE /api/sites/{editID}/forms/{key}", a.withEditAuth(a.removeForm))
	mux.HandleFunc("POST /api/sites/{editID}/forms/{key}/confirmation", a.withEditAuth(a.resendFormConfirmation))
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/httpapi/ -v -run 'Form' && go test ./... && go test -tags ee ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/forms.go internal/httpapi/formsapi.go internal/httpapi/formsapi_test.go internal/httpapi/formsmail_test.go internal/httpapi/server.go
git commit -m "feat: the API adds, changes and removes a site's forms within its plan and mails the recipient to confirm"
```

---
### Task 10: The public submission endpoint, captcha challenge, thank-you page and widget

**Files:**
- Create: `internal/httpapi/formsubmit.go`
- Modify: `internal/httpapi/gate.go`: `pageData` gains `Back`, `Action`, `Token` and `Button`, and the template renders them
- Modify: `internal/httpapi/server.go`: routes
- Modify: `internal/httpapi/httpapi_test.go`: add `"vendor/altcha.min.js"` to `testFS`
- Test: `internal/httpapi/formsubmit_test.go`

**Interfaces:**
- Consumes: Task 9 (`a.forms`, `formsLimit`, `baseURL`, `redact`, `formSendTimeout`, the test helpers `formsEnv`, `recSender` and `mailText`).
- Produces: `func (a *API) formSite(r *http.Request) (*store.Site, bool, error)`, `func (a *API) formPage(w, r, status int, title, msg string)` and `func sentence(s string) string`, plus the test helpers `activeForm(t, e, spec) (*store.Site, store.Form)`, `viewHost(site) string`, `post(...)` and `submit(...)` for Task 11.
- `pageData` fields used by Task 11: `Action` (the form's POST target), `Token` (sent as the hidden field `t`), and `Button`.

- [ ] **Step 1: Write the failing tests** (`internal/httpapi/formsubmit_test.go`)

First add `"vendor/altcha.min.js": {Data: []byte("// altcha")},` to the `testFS` map in `internal/httpapi/httpapi_test.go`.

```go
package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"

	"github.com/ittrail/sitebin.io/internal/store"
)

const urlenc = "application/x-www-form-urlencoded"

// activeForm creates a site with one form whose recipient has confirmed.
func activeForm(t *testing.T, e *env, spec store.Form) (*store.Site, store.Form) {
	t.Helper()
	c := e.createSite(t, nil, map[string]string{"index.html": "<h1>hi</h1>"})
	site, _ := e.st.ByViewID(c.ID)
	if spec.Name == "" {
		spec.Name = "Contact"
	}
	if spec.Recipient == "" {
		spec.Recipient = "office@example.com"
	}
	f, err := e.st.AddForm(site, spec, 10)
	if err != nil {
		t.Fatal(err)
	}
	if f, err = e.st.ConfirmForm(site, f.Key, f.Recipient, f.Seq); err != nil {
		t.Fatal(err)
	}
	return site, f
}

func viewHost(site *store.Site) string { return site.ViewID + ".sitebin.example" }

func post(t *testing.T, e *env, host, target, contentType string, body io.Reader, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", target, body)
	req.Host = host
	req.Header.Set("Content-Type", contentType)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return e.public(t, req)
}

func submit(t *testing.T, e *env, host, key, form string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return post(t, e, host, "/_sitebin/forms/"+key, urlenc, strings.NewReader(form), hdr)
}

func get(t *testing.T, e *env, host, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return e.public(t, req)
}

func TestSubmitMailsTheRecipient(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	w := submit(t, e, viewHost(site), f.Key, "name=Anna&email=anna%40example.com&message=Hallo", nil)
	if w.Code != 303 || w.Header().Get("Location") != "/_sitebin/forms/"+f.Key+"/thanks" {
		t.Fatalf("submit = %d Location %q: %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	m := rs.last(t)
	if m.To != "office@example.com" || !strings.Contains(mailText(t, m), "message: Hallo") {
		t.Fatalf("mail to %s:\n%s", m.To, mailText(t, m))
	}
	msg, _ := mail.ReadMessage(bytes.NewReader(m.Data))
	u, _ := url.Parse(strings.Trim(msg.Header.Get("List-Unsubscribe"), "<>"))
	c, ok := e.api.forms.links.ParseStop(u.Query().Get("t"), time.Now())
	if !ok || c.ViewID != site.ViewID || c.Key != f.Key || c.Recipient != f.Recipient || u.Path != "/forms/stop" {
		t.Errorf("stop link %q = %+v %v", u, c, ok)
	}
}

func TestSubmitAnswersJSON(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	w := submit(t, e, viewHost(site), f.Key, "message=hi", map[string]string{"Accept": "application/json"})
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"ok":true}` {
		t.Fatalf("JSON submit = %d %s", w.Code, w.Body)
	}
	w = submit(t, e, viewHost(site), "nosuchkey", "message=hi", map[string]string{"Accept": "application/json"})
	if w.Code != 404 || !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("JSON refusal = %d %s", w.Code, w.Body)
	}
}

func TestSubmitRefusals(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	pending, _ := e.st.AddForm(site, store.Form{Name: "P", Recipient: "p@example.com"}, 10)
	stopped, _ := e.st.AddForm(site, store.Form{Name: "S", Recipient: "s@example.com"}, 10)
	e.st.ConfirmForm(site, stopped.Key, "s@example.com", 0)
	e.st.StopForm(site, stopped.Key, "s@example.com")
	for _, c := range []struct {
		name, key, body, ct string
		want                int
	}{
		{"unknown key", "nosuchkey", "a=1", urlenc, 404},
		{"pending", pending.Key, "a=1", urlenc, 403},
		{"stopped", stopped.Key, "a=1", urlenc, 403},
		{"empty", f.Key, "name=&message=+", urlenc, 400},
		{"json body", f.Key, `{"a":1}`, "application/json", 415},
	} {
		if w := post(t, e, viewHost(site), "/_sitebin/forms/"+c.key, c.ct, strings.NewReader(c.body), nil); w.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
		}
	}
	if rs.count() != 0 {
		t.Error("a refused submission sent mail")
	}
}

func TestSubmitPausedAndExpired(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	e.st.Update(site, func(m *store.Meta) error { m.QuotaForms = intp(0); return nil })
	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 403 || !strings.Contains(w.Body.String(), "paused") {
		t.Errorf("paused form = %d", w.Code)
	}
	past := time.Now().Add(-time.Hour)
	e.st.Update(site, func(m *store.Meta) error { m.QuotaForms = nil; m.ExpiresAt = &past; return nil })
	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 410 {
		t.Errorf("expired site = %d, want 410", w.Code)
	}
}

func TestSubmitWithFormsOff(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	site, _ := e.st.ByViewID(c.ID)
	if w := submit(t, e, viewHost(site), "anykey", "message=hi", nil); w.Code != 404 {
		t.Fatalf("forms off = %d, want 404", w.Code)
	}
}

func TestSubmitHoneypotSendsNothing(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	w := submit(t, e, viewHost(site), f.Key, "message=buy+now&_gotcha=http%3A%2F%2Fspam.example", nil)
	if w.Code != 303 || rs.count() != 0 {
		t.Fatalf("honeypot = %d mails=%d, want a normal-looking 303 and nothing sent", w.Code, rs.count())
	}
}

func TestSubmitRateLimits(t *testing.T) {
	e, _ := formsEnv(t, map[string]string{"SITEBIN_FORMS_PER_IP_HOUR": "2"})
	site, f := activeForm(t, e, store.Form{})
	for i := 0; i < 2; i++ {
		submit(t, e, viewHost(site), f.Key, "message=hi", nil)
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 429 {
		t.Fatalf("3rd submission = %d, want 429", w.Code)
	}
}

// Review Focus 3.
func TestSubmitOnCustomDomain(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	if err := e.st.AddDomain(site, "www.kunde.example"); err != nil {
		t.Fatal(err)
	}
	if w := submit(t, e, "www.kunde.example", f.Key, "message=hi", nil); w.Code != 303 {
		t.Fatalf("submit on the custom domain = %d %s", w.Code, w.Body)
	}
	if !strings.Contains(mailText(t, rs.last(t)), "on www.kunde.example") {
		t.Error("the mail does not name the host the form was submitted on")
	}
	other, _ := activeForm(t, e, store.Form{Name: "Other"})
	if w := submit(t, e, viewHost(other), f.Key, "message=hi", nil); w.Code != 404 {
		t.Fatalf("a key used on another site's host = %d, want 404", w.Code)
	}
}

// Review Focus 5.
func TestSubmitRedirectsToConfiguredPath(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{Redirect: "/danke.html?sent=1#top"})
	w := submit(t, e, viewHost(site), f.Key, "message=hi", nil)
	if w.Code != 303 || w.Header().Get("Location") != "/danke.html?sent=1#top" {
		t.Fatalf("Location = %q", w.Header().Get("Location"))
	}
}

func TestSubmitThroughPathViews(t *testing.T) {
	e, _ := formsEnv(t, map[string]string{"SITEBIN_VIEW_ACCESS": "both"})
	site, f := activeForm(t, e, store.Form{})
	w := post(t, e, "sitebin.example", "/_sitebin/forms/"+f.Key+"?_site="+site.ViewID, urlenc, strings.NewReader("message=hi"), nil)
	if w.Code != 303 || w.Header().Get("Location") != "/_sitebin/forms/"+f.Key+"/thanks?_site="+site.ViewID {
		t.Fatalf("path view = %d Location %q", w.Code, w.Header().Get("Location"))
	}
	site2, g := activeForm(t, e, store.Form{Redirect: "/danke.html"})
	w = post(t, e, "sitebin.example", "/_sitebin/forms/"+g.Key+"?_site="+site2.ViewID, urlenc, strings.NewReader("message=hi"), nil)
	if w.Header().Get("Location") != "/v/"+site2.ViewID+"/danke.html" {
		t.Fatalf("path-view redirect = %q", w.Header().Get("Location"))
	}
	// _site is ignored on a site's own host.
	w = post(t, e, viewHost(site2), "/_sitebin/forms/"+f.Key+"?_site="+site.ViewID, urlenc, strings.NewReader("message=hi"), nil)
	if w.Code != 404 {
		t.Fatalf("_site on a site host = %d, want 404", w.Code)
	}
}

func multipartBody(t *testing.T, fields map[string]string, field, filename, content string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
	p, _ := mw.CreatePart(h)
	p.Write([]byte(content))
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestSubmitWithAttachment(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{Files: true})
	body, ct := multipartBody(t, map[string]string{"message": "see attached"}, "cv", "cv.pdf", "%PDF-1.4")
	if w := post(t, e, viewHost(site), "/_sitebin/forms/"+f.Key, ct, body, nil); w.Code != 303 {
		t.Fatalf("with attachment = %d %s", w.Code, w.Body)
	}
	if !strings.Contains(string(rs.last(t).Data), "cv.pdf") {
		t.Error("the attachment is missing from the mail")
	}
	site2, g := activeForm(t, e, store.Form{})
	body, ct = multipartBody(t, map[string]string{"message": "see attached"}, "cv", "cv.pdf", "%PDF-1.4")
	if w := post(t, e, viewHost(site2), "/_sitebin/forms/"+g.Key, ct, body, nil); w.Code != 400 || rs.count() != 1 {
		t.Fatalf("attachment on a form without files = %d mails=%d", w.Code, rs.count())
	}
}

func TestSubmitCaptcha(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{Captcha: true})
	w := get(t, e, viewHost(site), "/_sitebin/forms/"+f.Key+"/challenge", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("challenge = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	var ch altcha.Challenge
	if err := json.Unmarshal(w.Body.Bytes(), &ch); err != nil {
		t.Fatal(err)
	}
	sol, err := altcha.SolveChallenge(altcha.SolveChallengeOptions{Challenge: ch, DeriveKey: altcha.DeriveKeyPBKDF2()})
	if err != nil || sol == nil {
		t.Fatalf("solve: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"challenge": map[string]any{"parameters": ch.Parameters, "signature": ch.Signature}, "solution": sol})
	field := url.QueryEscape(base64.StdEncoding.EncodeToString(payload))

	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 403 {
		t.Fatalf("without the captcha = %d, want 403", w.Code)
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field, nil); w.Code != 303 || rs.count() != 1 {
		t.Fatalf("with a solved captcha = %d mails=%d", w.Code, rs.count())
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field, nil); w.Code != 403 {
		t.Fatalf("a replayed solution = %d, want 403", w.Code)
	}
	plainSite, g := activeForm(t, e, store.Form{})
	if w := get(t, e, viewHost(plainSite), "/_sitebin/forms/"+g.Key+"/challenge", nil); w.Code != 404 {
		t.Fatalf("challenge for a form without captcha = %d, want 404", w.Code)
	}
}

func TestSubmitSMTPFailureIs502(t *testing.T) {
	e, rs := formsEnv(t, nil)
	rs.err = fmt.Errorf("550 5.1.1 <office@example.com>: Recipient address rejected")
	site, f := activeForm(t, e, store.Form{})
	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 502 {
		t.Fatalf("SMTP failure = %d, want 502", w.Code)
	}
}

func TestThanksPageGoesBackToTheForm(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	w := get(t, e, viewHost(site), "/_sitebin/forms/"+f.Key+"/thanks", map[string]string{"Referer": "http://" + viewHost(site) + "/kontakt.html"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Thank you") || !strings.Contains(w.Body.String(), `href="/kontakt.html"`) {
		t.Fatalf("thanks = %d %s", w.Code, w.Body)
	}
	w = get(t, e, viewHost(site), "/_sitebin/forms/"+f.Key+"/thanks", map[string]string{"Referer": "https://evil.example/x"})
	if !strings.Contains(w.Body.String(), `href="/"`) {
		t.Fatalf("a foreign Referer must not become the Back link: %s", w.Body)
	}
}

func TestAltchaScriptServed(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, _ := activeForm(t, e, store.Form{})
	w := get(t, e, viewHost(site), "/_sitebin/altcha.js", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || w.Body.String() != "// altcha" {
		t.Fatalf("altcha.js = %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/httpapi/ -run 'Submit|Thanks|Altcha' -v`
Expected: FAIL. The routes do not exist yet, so submissions reach the not-found page.

- [ ] **Step 3: Implement**

`internal/httpapi/gate.go`: extend `pageData`:

```go
type pageData struct {
	Title    string
	Message  string
	Code     string
	ShowForm bool
	Redirect string
	Error    string
	Site     string // view id (path mode only; empty on subdomains)
	// Back links to the page a visitor came from (forms' result pages).
	Back string
	// Action, Token and Button render a one-button form that POSTs t=Token to
	// Action: the recipient's confirm and stop pages.
	Action string
	Token  string
	Button string
}
```

In `basePageTmpl`, directly after the `{{end}}` that closes `{{if .ShowForm}}`:

```
  {{if .Action}}
  <form method="post" action="{{.Action}}">
    <input type="hidden" name="t" value="{{.Token}}">
    <button type="submit">{{.Button}}</button>
  </form>
  {{end}}
  {{if .Back}}<a class="back" href="{{.Back}}">&larr; Back</a>{{end}}
```

and in its `<style>`, after the `.code` rule:

```
  .back { display: inline-block; margin-top: 20px; color: #f5b84d; font-size: 14px; text-decoration: none; }
  .back:hover { text-decoration: underline; }
```

`internal/httpapi/formsubmit.go`:

```go
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ittrail/sitebin.io/internal/forms"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The public half of forms: what a visitor's browser talks to, on the site's
// own origin under /_sitebin/, which Caddy proxies here without the authz
// subrequest on every content origin (view hosts and custom domains alike).

const noForm = "There is no form at this address."

// formSite resolves the site a /_sitebin/forms request is for, and whether it
// was addressed through a path view (?_site= on the main domain). _site is
// honoured on the main domain only; on a site's own host it is ignored, so it
// can never point a key at another site.
func (a *API) formSite(r *http.Request) (*store.Site, bool, error) {
	if id := r.URL.Query().Get("_site"); id != "" && a.cfg.PathViews() && strings.EqualFold(hostWithoutPort(r.Host), a.cfg.BaseDomain) {
		site, err := a.st.ByViewID(id)
		return site, true, err
	}
	site, err := a.siteByHost(r.Host)
	return site, false, err
}

// submitForm is POST /_sitebin/forms/{key}. The checks run cheapest first,
// and sending is last; see the spec's "Submitting".
func (a *API) submitForm(w http.ResponseWriter, r *http.Request) {
	wantJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	fail := func(status int, msg string) {
		if wantJSON {
			writeError(w, status, msg)
			return
		}
		a.formPage(w, r, status, "Your message was not sent", msg)
	}
	if a.forms == nil {
		fail(404, noForm)
		return
	}
	site, viaPath, err := a.formSite(r)
	if err != nil {
		fail(404, noForm)
		return
	}
	f, i, ok := store.FindForm(site.Meta, r.PathValue("key"))
	switch {
	case !ok:
		fail(404, noForm)
		return
	case site.Meta.Expired(time.Now()):
		fail(410, "This site has expired.")
		return
	case store.FormPaused(i, a.formsLimit(site)):
		fail(403, "This form is paused: the site's plan does not include it at the moment.")
		return
	case f.Status == store.FormPending:
		fail(403, "This form is not active yet: its recipient has not confirmed it.")
		return
	case f.Status == store.FormStopped:
		fail(403, "This form no longer accepts messages.")
		return
	}
	if !a.forms.perIP.Allow(clientIP(r)) || !a.forms.perForm.Allow(site.ViewID+"/"+f.Key) {
		fail(429, "Too many messages were sent through this form. Please try again later.")
		return
	}
	var fileRoom int64
	if f.Files {
		fileRoom = int64(a.cfg.FormsMaxFiles) * a.cfg.FormsMaxFileBytes
	}
	// The text allowance, plus 64 KiB for multipart headers and boundaries.
	r.Body = http.MaxBytesReader(w, r.Body, fileRoom+forms.MaxTextBytes+64<<10)
	sub, err := forms.Parse(r, forms.Limits{AllowFiles: f.Files, MaxFiles: a.cfg.FormsMaxFiles, MaxFileBytes: a.cfg.FormsMaxFileBytes})
	if err != nil {
		var pe *forms.ParseError
		if !errors.As(err, &pe) {
			pe = &forms.ParseError{Status: 400, Msg: "the form data is malformed"}
		}
		fail(pe.Status, sentence(pe.Msg))
		return
	}
	if sub.Honeypot() {
		// Exactly what a success looks like: a bot learns nothing.
		a.formSucceeded(w, r, site, f, viaPath, wantJSON)
		return
	}
	if f.Captcha && a.forms.captcha.Verify(sub.Control["altcha"], site.ViewID, f.Key) != nil {
		fail(403, "The captcha was not solved. Please go back and try again.")
		return
	}
	if !sub.HasContent() {
		fail(400, "The form was empty.")
		return
	}
	now := time.Now()
	stop := a.forms.links.StopToken(forms.StopClaim{ViewID: site.ViewID, Key: f.Key, Recipient: f.Recipient}, now)
	m, err := forms.BuildSubmission(forms.SubmissionMail{
		From:      a.cfg.FormsSMTP.From,
		FormName:  f.Name,
		FormKey:   f.Key,
		Recipient: f.Recipient,
		SiteID:    site.ViewID,
		Host:      strings.ToLower(hostWithoutPort(r.Host)),
		StopURL:   a.baseURL() + "/forms/stop?t=" + url.QueryEscape(stop),
		At:        now,
		Sub:       sub,
	})
	if err != nil {
		a.log.Error("form mail not built", "id", site.ViewID, "form", f.Key, "err", err)
		fail(500, "The message could not be sent.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), formSendTimeout)
	defer cancel()
	if err := a.forms.send.Send(ctx, m); err != nil {
		a.log.Error("form mail not sent", "id", site.ViewID, "form", f.Key, "err", redact(err, f.Recipient))
		fail(502, "The message could not be sent right now. Please try again in a moment.")
		return
	}
	// Never the values, the filenames or the recipient.
	a.log.Info("form submitted", "id", site.ViewID, "form", f.Key, "bytes", len(m.Data), "files", len(sub.Files))
	a.formSucceeded(w, r, site, f, viaPath, wantJSON)
}

// formSucceeded answers a delivered (or honeypotted) submission: JSON for a
// script, else a 303 to the form's thank-you path or the default page.
func (a *API) formSucceeded(w http.ResponseWriter, r *http.Request, site *store.Site, f store.Form, viaPath, wantJSON bool) {
	if wantJSON {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	target := "/_sitebin/forms/" + f.Key + "/thanks"
	if viaPath {
		target += "?_site=" + site.ViewID
	}
	if f.Redirect != "" {
		target = f.Redirect
		if viaPath {
			target = "/v/" + site.ViewID + f.Redirect
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// formThanks is the default thank-you page.
func (a *API) formThanks(w http.ResponseWriter, r *http.Request) {
	if a.forms == nil {
		a.notFoundPage(w, r)
		return
	}
	a.formPage(w, r, 200, "Thank you", "Your message has been sent.")
}

// formPage is the small page a visitor sees after posting without
// JavaScript: the thanks, or why it failed.
func (a *API) formPage(w http.ResponseWriter, r *http.Request, status int, title, msg string) {
	code := ""
	if status >= 400 {
		code = strconv.Itoa(status)
	}
	a.renderPage(w, status, pageData{Title: title, Message: msg, Code: code, Back: sameHostReferer(r)})
}

// sameHostReferer is the path of the page the form was on, when the browser
// said which one and it is on this host; "/" otherwise.
func sameHostReferer(r *http.Request) string {
	u, err := url.Parse(r.Referer())
	if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
		return "/"
	}
	return sanitizeRedirect(u.RequestURI())
}

// sentence makes a parser refusal ("the message is too long") read as one.
func sentence(s string) string {
	if s == "" {
		return s
	}
	c, n := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(c)) + s[n:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// formChallenge is GET /_sitebin/forms/{key}/challenge, the widget's
// challenge URL. Only an active, unpaused form with the captcha on has one.
func (a *API) formChallenge(w http.ResponseWriter, r *http.Request) {
	if a.forms == nil {
		http.NotFound(w, r)
		return
	}
	site, _, err := a.formSite(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, i, ok := store.FindForm(site.Meta, r.PathValue("key"))
	if !ok || !f.Captcha || f.Status != store.FormActive || store.FormPaused(i, a.formsLimit(site)) {
		http.NotFound(w, r)
		return
	}
	if !a.forms.challenges.Allow(clientIP(r)) {
		writeError(w, 429, "too many captcha requests, try again later")
		return
	}
	ch, err := a.forms.captcha.Challenge(site.ViewID, f.Key)
	if err != nil {
		a.log.Error("captcha challenge", "err", err)
		writeError(w, 500, "internal error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, ch)
}

// altchaScript serves the vendored widget at a short, stable URL on every
// site origin, so a page loads it same-origin with one script tag.
func (a *API) altchaScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFileFS(w, r, a.webFS, "vendor/altcha.min.js")
}
```

`internal/httpapi/server.go`: after `mux.HandleFunc("POST /_sitebin/csp-report", a.handleCSPReport)`:

```go
	mux.HandleFunc("POST /_sitebin/forms/{key}", a.submitForm)
	mux.HandleFunc("GET /_sitebin/forms/{key}/challenge", a.formChallenge)
	mux.HandleFunc("GET /_sitebin/forms/{key}/thanks", a.formThanks)
	mux.HandleFunc("GET /_sitebin/altcha.js", a.altchaScript)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/httpapi/ -v -run 'Submit|Thanks|Altcha' && go test ./... && go test -tags ee ./...`
Expected: PASS. `TestSubmitCaptcha` does real proof-of-work at the production cost, which takes about 0.2–0.5 s.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/formsubmit.go internal/httpapi/formsubmit_test.go internal/httpapi/gate.go internal/httpapi/server.go internal/httpapi/httpapi_test.go
git commit -m "feat: a site's pages post forms to /_sitebin/forms on their own origin and the recipient gets the mail"
```

---

### Task 11: The recipient's confirm and stop pages

**Files:**
- Create: `internal/httpapi/formconsent.go`
- Modify: `internal/httpapi/server.go`: routes
- Test: `internal/httpapi/formconsent_test.go`

**Interfaces:**
- Consumes: Task 9 (`a.forms.links`, `formHost`), Task 10 (`pageData.Action/Token/Button` and the test helpers `activeForm` and `submit`), Task 2 (`ConfirmForm`, `StopForm`).
- Produces: `GET`/`POST /forms/confirm` and `GET`/`POST /forms/stop` on the main domain.

- [ ] **Step 1: Write the failing tests** (`internal/httpapi/formconsent_test.go`)

```go
package httpapi

import (
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/forms"
	"github.com/ittrail/sitebin.io/internal/store"
)

func consent(t *testing.T, e *env, method, path, tok string, oneClick bool) *httptest.ResponseRecorder {
	t.Helper()
	target := path
	var body io.Reader
	switch {
	case method == "GET":
		target += "?t=" + url.QueryEscape(tok)
	case oneClick: // RFC 8058: the token is in the List-Unsubscribe URL
		target += "?t=" + url.QueryEscape(tok)
		body = strings.NewReader("List-Unsubscribe=One-Click")
	default:
		body = strings.NewReader("t=" + url.QueryEscape(tok))
	}
	req := httptest.NewRequest(method, target, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return e.public(t, req)
}

func pendingForm(t *testing.T, e *env) (*store.Site, store.Form) {
	t.Helper()
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	site, _ := e.st.ByViewID(c.ID)
	f, err := e.st.AddForm(site, store.Form{Name: "Contact", Recipient: "office@example.com"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	return site, f
}

func confirmTok(e *env, site *store.Site, f store.Form) string {
	return e.api.forms.links.ConfirmToken(forms.ConfirmClaim{ViewID: site.ViewID, Key: f.Key, Recipient: f.Recipient, Seq: f.Seq}, time.Now())
}

func stopTok(e *env, site *store.Site, key, addr string) string {
	return e.api.forms.links.StopToken(forms.StopClaim{ViewID: site.ViewID, Key: key, Recipient: addr}, time.Now())
}

func formStatus(t *testing.T, e *env, site *store.Site, key string) store.Form {
	t.Helper()
	s, _ := e.st.ByViewID(site.ViewID)
	f, _, ok := store.FindForm(s.Meta, key)
	if !ok {
		t.Fatalf("form %s is gone", key)
	}
	return f
}

func TestConfirmFlow(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := pendingForm(t, e)
	tok := confirmTok(e, site, f)
	w := consent(t, e, "GET", "/forms/confirm", tok, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `action="/forms/confirm"`) {
		t.Fatalf("GET = %d %s", w.Code, w.Body)
	}
	if formStatus(t, e, site, f.Key).Status != store.FormPending {
		t.Fatal("GET confirmed the form: a mail scanner would do the same")
	}
	w = consent(t, e, "POST", "/forms/confirm", tok, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Confirmed") || formStatus(t, e, site, f.Key).Status != store.FormActive {
		t.Fatalf("POST = %d %s", w.Code, w.Body)
	}
	if w = consent(t, e, "POST", "/forms/confirm", tok, false); w.Code != 200 {
		t.Fatalf("a second click = %d, want 200", w.Code)
	}
}

// Review Focus 4.
func TestConfirmAfterDeleteOrRecipientChange(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := pendingForm(t, e)
	tok := confirmTok(e, site, f)
	to := "new@example.com"
	e.st.UpdateForm(site, f.Key, store.FormPatch{Recipient: &to})
	w := consent(t, e, "POST", "/forms/confirm", tok, false)
	if w.Code != 410 || !strings.Contains(w.Body.String(), "no longer valid") {
		t.Fatalf("old link after a recipient change = %d %s", w.Code, w.Body)
	}
	if g := formStatus(t, e, site, f.Key); g.Status != store.FormPending || g.Recipient != to {
		t.Fatalf("the old link changed the form: %+v", g)
	}

	site2, g := pendingForm(t, e)
	tok2 := confirmTok(e, site2, g)
	e.st.DeleteForm(site2, g.Key)
	for _, m := range []string{"GET", "POST"} {
		if w := consent(t, e, m, "/forms/confirm", tok2, false); w.Code != 410 || !strings.Contains(w.Body.String(), "no longer exists") {
			t.Fatalf("%s after delete = %d %s", m, w.Code, w.Body)
		}
	}
}

func TestConfirmRefusesBadTokens(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := pendingForm(t, e)
	if w := consent(t, e, "GET", "/forms/confirm", "garbage", false); w.Code != 400 {
		t.Errorf("garbage = %d", w.Code)
	}
	if w := consent(t, e, "POST", "/forms/confirm", stopTok(e, site, f.Key, f.Recipient), false); w.Code != 400 {
		t.Errorf("a stop token used to confirm = %d", w.Code)
	}
}

func TestStopFlow(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	tok := stopTok(e, site, f.Key, f.Recipient)
	w := consent(t, e, "GET", "/forms/stop", tok, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `action="/forms/stop"`) || formStatus(t, e, site, f.Key).Status != store.FormActive {
		t.Fatalf("GET = %d, and it must change nothing", w.Code)
	}
	w = consent(t, e, "POST", "/forms/stop", tok, false)
	if w.Code != 200 || formStatus(t, e, site, f.Key).Status != store.FormStopped {
		t.Fatalf("POST = %d %s", w.Code, w.Body)
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 403 {
		t.Fatalf("submission after stop = %d, want 403", w.Code)
	}
}

func TestStopOneClick(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	w := consent(t, e, "POST", "/forms/stop", stopTok(e, site, f.Key, f.Recipient), true)
	if w.Code != 200 || formStatus(t, e, site, f.Key).Status != store.FormStopped {
		t.Fatalf("one-click = %d %s", w.Code, w.Body)
	}
}

func TestStopLinkOfAFormerRecipientChangesNothing(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	old := stopTok(e, site, f.Key, f.Recipient)
	to := "new@example.com"
	g, _, _ := e.st.UpdateForm(site, f.Key, store.FormPatch{Recipient: &to})
	e.st.ConfirmForm(site, f.Key, to, g.Seq)
	if w := consent(t, e, "POST", "/forms/stop", old, false); w.Code != 200 {
		t.Fatalf("old stop link = %d", w.Code)
	}
	if h := formStatus(t, e, site, f.Key); h.Status != store.FormActive || h.Recipient != to {
		t.Fatalf("a former recipient's link stopped the new one: %+v", h)
	}
}

func TestConsentPagesNeedForms(t *testing.T) {
	e := newEnv(t, nil)
	if w := consent(t, e, "GET", "/forms/confirm", "x", false); w.Code != 404 {
		t.Fatalf("confirm page with forms off = %d", w.Code)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/httpapi/ -run 'Confirm|Stop|Consent' -v`
Expected: FAIL (404 from the not-found handler).

- [ ] **Step 3: Implement** (`internal/httpapi/formconsent.go`)

```go
package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

// The recipient's half of forms: the confirm and stop links from their mail,
// on the main domain, never user content. A GET only ever shows a page with a
// button, because mail scanners fetch every link in a message; the POST the
// button sends is what acts. The one exception is RFC 8058's one-click
// unsubscribe, a POST by design, which can only ever stop.

func (a *API) consentPage(w http.ResponseWriter, status int, title, msg, action, token, button string) {
	code := ""
	if status >= 400 {
		code = strconv.Itoa(status)
	}
	a.renderPage(w, status, pageData{Title: title, Message: msg, Code: code, Action: action, Token: token, Button: button})
}

func (a *API) pageBadLink(w http.ResponseWriter) {
	a.consentPage(w, 400, "This link is not valid", "It may be incomplete, or older than 7 days. Ask the site's owner to send a new one.", "", "", "")
}

func (a *API) pageFormGone(w http.ResponseWriter) {
	a.consentPage(w, 410, "This form no longer exists", "Its owner deleted it. Nothing will be sent to you.", "", "", "")
}

func (a *API) pageStaleLink(w http.ResponseWriter) {
	a.consentPage(w, 410, "This link is no longer valid", "The form's recipient changed, or its messages were stopped, after this link was sent. Ask the site's owner for a new one.", "", "", "")
}

// consentForm loads the site and form a link names; ok=false if either is gone.
func (a *API) consentForm(viewID, key string) (*store.Site, store.Form, bool) {
	site, err := a.st.ByViewID(viewID)
	if err != nil {
		return nil, store.Form{}, false
	}
	f, _, ok := store.FindForm(site.Meta, key)
	return site, f, ok
}

func (a *API) formConfirmPage(w http.ResponseWriter, r *http.Request) {
	if a.forms == nil {
		a.notFoundPage(w, r)
		return
	}
	tok := r.URL.Query().Get("t")
	c, ok := a.forms.links.ParseConfirm(tok, time.Now())
	if !ok {
		a.pageBadLink(w)
		return
	}
	site, f, ok := a.consentForm(c.ViewID, c.Key)
	switch {
	case !ok:
		a.pageFormGone(w)
	case f.Recipient != c.Recipient || f.Seq != c.Seq || f.Status == store.FormStopped:
		a.pageStaleLink(w)
	case f.Status == store.FormActive:
		a.consentPage(w, 200, "Already confirmed",
			fmt.Sprintf("Messages from the form “%s” on %s reach %s.", f.Name, a.formHost(site), f.Recipient), "", "", "")
	default:
		a.consentPage(w, 200, "Confirm form messages",
			fmt.Sprintf("%s would like to send the messages of its form “%s” to %s.", a.formHost(site), f.Name, f.Recipient),
			"/forms/confirm", tok, "Confirm")
	}
}

func (a *API) formConfirm(w http.ResponseWriter, r *http.Request) {
	if a.forms == nil {
		a.notFoundPage(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	c, ok := a.forms.links.ParseConfirm(r.PostFormValue("t"), time.Now())
	if !ok {
		a.pageBadLink(w)
		return
	}
	site, err := a.st.ByViewID(c.ViewID)
	if err != nil {
		a.pageFormGone(w)
		return
	}
	f, err := a.st.ConfirmForm(site, c.Key, c.Recipient, c.Seq)
	switch {
	case errors.Is(err, store.ErrFormNotFound):
		a.pageFormGone(w)
	case errors.Is(err, store.ErrFormStale):
		a.pageStaleLink(w)
	case err != nil:
		a.log.Error("form confirm", "id", site.ViewID, "form", c.Key, "err", err)
		a.consentPage(w, 500, "Something went wrong", "Please try again in a moment.", "", "", "")
	default:
		a.log.Info("form confirmed", "id", site.ViewID, "form", f.Key)
		a.consentPage(w, 200, "Confirmed",
			fmt.Sprintf("Messages sent through the form “%s” on %s will now reach you. Every one of them carries a link to stop them.", f.Name, a.formHost(site)),
			"", "", "")
	}
}

func (a *API) formStopPage(w http.ResponseWriter, r *http.Request) {
	if a.forms == nil {
		a.notFoundPage(w, r)
		return
	}
	tok := r.URL.Query().Get("t")
	c, ok := a.forms.links.ParseStop(tok, time.Now())
	if !ok {
		a.consentPage(w, 400, "This link is not valid", "It may be incomplete. Every message from the form carries a working one.", "", "", "")
		return
	}
	site, f, ok := a.consentForm(c.ViewID, c.Key)
	switch {
	case !ok:
		a.pageFormGone(w)
	case f.Recipient != c.Recipient || f.Status == store.FormStopped:
		a.consentPage(w, 200, "Already stopped",
			fmt.Sprintf("This address receives nothing from the form “%s” on %s.", f.Name, a.formHost(site)), "", "", "")
	default:
		a.consentPage(w, 200, "Stop these emails?",
			fmt.Sprintf("You receive the messages of the form “%s” on %s at %s.", f.Name, a.formHost(site), f.Recipient),
			"/forms/stop", tok, "Stop emails")
	}
}

func (a *API) formStop(w http.ResponseWriter, r *http.Request) {
	if a.forms == nil {
		a.notFoundPage(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	tok := r.PostFormValue("t")
	if tok == "" {
		tok = r.URL.Query().Get("t") // RFC 8058 one-click: the token is in the URL
	}
	c, ok := a.forms.links.ParseStop(tok, time.Now())
	if !ok {
		a.consentPage(w, 400, "This link is not valid", "It may be incomplete. Every message from the form carries a working one.", "", "", "")
		return
	}
	stopped := func(name, host string) {
		a.consentPage(w, 200, "Stopped",
			fmt.Sprintf("You will not receive messages from the form “%s” on %s anymore. The site's owner can ask you to confirm again.", name, host),
			"", "", "")
	}
	site, err := a.st.ByViewID(c.ViewID)
	if err != nil {
		stopped("", "this site") // gone already: the goal is met
		return
	}
	f, changed, err := a.st.StopForm(site, c.Key, c.Recipient)
	switch {
	case errors.Is(err, store.ErrFormNotFound):
		stopped("", a.formHost(site))
	case err != nil:
		a.log.Error("form stop", "id", site.ViewID, "form", c.Key, "err", err)
		a.consentPage(w, 500, "Something went wrong", "Please try again in a moment.", "", "", "")
	default:
		if changed {
			a.log.Info("form stopped by its recipient", "id", site.ViewID, "form", f.Key)
		}
		stopped(f.Name, a.formHost(site))
	}
}
```

`internal/httpapi/server.go`: after the `/_sitebin/forms` routes:

```go
	mux.HandleFunc("GET /forms/confirm", a.formConfirmPage)
	mux.HandleFunc("POST /forms/confirm", a.formConfirm)
	mux.HandleFunc("GET /forms/stop", a.formStopPage)
	mux.HandleFunc("POST /forms/stop", a.formStop)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/httpapi/ -v -run 'Confirm|Stop|Consent' && go test ./... && go test -tags ee ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/formconsent.go internal/httpapi/formconsent_test.go internal/httpapi/server.go
git commit -m "feat: a recipient confirms a form, or stops it, with a button behind the link in their mail"
```

---
### Task 12: Forms over MCP

**Files:**
- Modify: `internal/mcp/ops.go`: types `FormInput`, `FormResult` and `FormsResult`, plus five `Ops` methods
- Modify: `internal/mcp/server.go`: five tools, argument types, and `formsOut`
- Modify: `internal/mcp/server_test.go`: `fakeOps` methods, the catalog, the scope table and a new test
- Modify: `internal/httpapi/mcpops.go`: the adapter methods and `mcpError` cases
- Test: `internal/httpapi/mcpops_test.go`

**Interfaces:**
- Consumes: Task 9 (`listFormsFor`, `addForm`, `updateForm`, `deleteForm`, `resendConfirmation`, `formInput`, `formsJSON`).
- Produces: MCP tools `list_forms` (read), `add_form`, `update_form`, `remove_form` (destructive) and `resend_form_confirmation` (write). `Ops` gains:
  ```go
  ListForms(ctx context.Context, a Auth, ref SiteRef) (*FormsResult, error)
  AddForm(ctx context.Context, a Auth, ref SiteRef, in FormInput) (*FormsResult, error)
  UpdateForm(ctx context.Context, a Auth, ref SiteRef, key string, in FormInput) (*FormsResult, error)
  RemoveForm(ctx context.Context, a Auth, ref SiteRef, key string) (*FormsResult, error)
  ResendFormConfirmation(ctx context.Context, a Auth, ref SiteRef, key string) (*FormsResult, error)
  ```

- [ ] **Step 1: Write the failing tests**

In `internal/mcp/server_test.go`:
- Add `gotForm FormInput` and `gotKey string` to `fakeOps`.
- Add these methods:

```go
func (f *fakeOps) forms() *FormsResult {
	return &FormsResult{Enabled: true, Limit: 1, Forms: []FormResult{}}
}

func (f *fakeOps) ListForms(_ context.Context, a Auth, ref SiteRef) (*FormsResult, error) {
	f.note("list_forms", a, ref)
	return f.forms(), f.err
}

func (f *fakeOps) AddForm(_ context.Context, a Auth, ref SiteRef, in FormInput) (*FormsResult, error) {
	f.note("add_form", a, ref)
	f.gotForm = in
	return f.forms(), f.err
}

func (f *fakeOps) UpdateForm(_ context.Context, a Auth, ref SiteRef, key string, in FormInput) (*FormsResult, error) {
	f.note("update_form", a, ref)
	f.gotKey, f.gotForm = key, in
	return f.forms(), f.err
}

func (f *fakeOps) RemoveForm(_ context.Context, a Auth, ref SiteRef, key string) (*FormsResult, error) {
	f.note("remove_form", a, ref)
	f.gotKey = key
	return f.forms(), f.err
}

func (f *fakeOps) ResendFormConfirmation(_ context.Context, a Auth, ref SiteRef, key string) (*FormsResult, error) {
	f.note("resend_form_confirmation", a, ref)
	f.gotKey = key
	return f.forms(), f.err
}
```

- In `TestToolCatalog`'s `want` map, add `"list_forms": true, "add_form": true, "update_form": true, "remove_form": true, "resend_form_confirmation": true`.
- In the scope test's `argsFor` map (search for `argsFor := map`), add:

```go
		"list_forms":               {"edit_id": "e1"},
		"add_form":                 {"edit_id": "e1", "form": map[string]any{"name": "A", "recipient": "a@example.com"}},
		"update_form":              {"edit_id": "e1", "key": "k1", "form": map[string]any{}},
		"remove_form":              {"edit_id": "e1", "key": "k1"},
		"resend_form_confirmation": {"edit_id": "e1", "key": "k1"},
```

- Add this test:

```go
func TestFormToolsPassTheirArguments(t *testing.T) {
	ops := &fakeOps{}
	cs := connect(t, ops, nil)
	res := call(t, cs, "add_form", map[string]any{"edit_id": "e1", "edit_password": "pw",
		"form": map[string]any{"name": "Contact", "recipient": "a@example.com", "captcha": true}})
	if res.IsError || ops.gotRef.EditID != "e1" || ops.gotForm.Name == nil || *ops.gotForm.Name != "Contact" ||
		ops.gotForm.Captcha == nil || !*ops.gotForm.Captcha || ops.gotForm.Files != nil {
		t.Fatalf("add_form passed %+v (%s)", ops.gotForm, resultText(res))
	}
	call(t, cs, "update_form", map[string]any{"edit_id": "e1", "key": "k9", "form": map[string]any{"recipient": "b@example.com"}})
	if ops.gotKey != "k9" || ops.gotForm.Recipient == nil || *ops.gotForm.Recipient != "b@example.com" || ops.gotForm.Name != nil {
		t.Fatalf("update_form passed key %q form %+v", ops.gotKey, ops.gotForm)
	}
}
```

In `internal/httpapi/mcpops_test.go`:

```go
func TestMCPFormsLifecycle(t *testing.T) {
	e, rs := formsEnv(t, nil)
	cs := mcpClient(t, e, nil)
	id, pw := mcpCreate(t, cs, "<h1>hi</h1>")
	forms := func(res *sdk.CallToolResult) []map[string]any {
		t.Helper()
		if res.IsError {
			t.Fatalf("tool error: %s", mcpText(res))
		}
		m, _ := res.StructuredContent.(map[string]any)
		var out []map[string]any
		list, _ := m["forms"].([]any)
		for _, f := range list {
			out = append(out, f.(map[string]any))
		}
		return out
	}
	site := map[string]any{"edit_id": id, "edit_password": pw}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range site {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	fs := forms(mcpCall(t, cs, "add_form", with(map[string]any{"form": map[string]any{"name": "Contact", "recipient": "office@example.com", "captcha": true}})))
	if len(fs) != 1 || fs[0]["status"] != "pending" || !strings.Contains(fs[0]["snippet"].(string), "altcha-widget") || rs.count() != 1 {
		t.Fatalf("add_form = %v, mails %d", fs, rs.count())
	}
	key := fs[0]["key"].(string)
	fs = forms(mcpCall(t, cs, "update_form", with(map[string]any{"key": key, "form": map[string]any{"name": "Kontakt"}})))
	if fs[0]["name"] != "Kontakt" {
		t.Fatalf("update_form = %v", fs)
	}
	forms(mcpCall(t, cs, "resend_form_confirmation", with(map[string]any{"key": key})))
	if rs.count() != 2 {
		t.Fatalf("resend sent %d mails in total, want 2", rs.count())
	}
	if fs = forms(mcpCall(t, cs, "list_forms", site)); len(fs) != 1 {
		t.Fatalf("list_forms = %v", fs)
	}
	if fs = forms(mcpCall(t, cs, "remove_form", with(map[string]any{"key": key}))); len(fs) != 0 {
		t.Fatalf("remove_form left %v", fs)
	}
	res := mcpCall(t, cs, "remove_form", with(map[string]any{"key": key}))
	if !res.IsError || !strings.Contains(mcpText(res), "list_forms") {
		t.Fatalf("removing twice = %s", mcpText(res))
	}
}

func TestMCPAddFormWithFormsOff(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	id, pw := mcpCreate(t, cs, "<h1>hi</h1>")
	res := mcpCall(t, cs, "add_form", map[string]any{"edit_id": id, "edit_password": pw, "form": map[string]any{"name": "A", "recipient": "a@example.com"}})
	if !res.IsError || !strings.Contains(mcpText(res), "not enabled") {
		t.Fatalf("add_form with forms off = %s", mcpText(res))
	}
}

func TestMCPFormsNeedTheEditPassword(t *testing.T) {
	e, _ := formsEnv(t, nil)
	cs := mcpClient(t, e, nil)
	id, _ := mcpCreate(t, cs, "<h1>hi</h1>")
	res := mcpCall(t, cs, "list_forms", map[string]any{"edit_id": id})
	if !res.IsError || !strings.Contains(mcpText(res), "edit_password") {
		t.Fatalf("list_forms without a password = %s", mcpText(res))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/mcp/ ./internal/httpapi/ -run 'Form|Catalog|Scope' -v`
Expected: build failure. `*fakeOps` does not implement `Ops` until the interface grows, then the unknown tools fail.

- [ ] **Step 3: Implement**

`internal/mcp/ops.go`: add after `CreateInput`:

```go
// FormInput is a form's settings as add_form and update_form take them. In
// update_form a field left out is left alone.
type FormInput struct {
	Name      *string `json:"name,omitempty" jsonschema:"the form's name, 1-60 characters; recipients see it as the sender of every message"`
	Recipient *string `json:"recipient,omitempty" jsonschema:"the one email address that receives the submissions; it must confirm by email before the form works"`
	Captcha   *bool   `json:"captcha,omitempty" jsonschema:"require an ALTCHA proof-of-work captcha; the snippet then includes the widget"`
	Files     *bool   `json:"files,omitempty" jsonschema:"accept file attachments; the snippet then posts multipart/form-data"`
	Redirect  *string `json:"redirect,omitempty" jsonschema:"a path on this site to send visitors to after a successful submission, e.g. /thanks.html; empty for the default page"`
}

// FormResult is one form.
type FormResult struct {
	Key         string     `json:"key" jsonschema:"the form's key, part of its action URL"`
	Name        string     `json:"name"`
	Recipient   string     `json:"recipient"`
	Captcha     bool       `json:"captcha"`
	Files       bool       `json:"files"`
	Redirect    string     `json:"redirect,omitempty"`
	Status      string     `json:"status" jsonschema:"pending (the recipient has not confirmed: submissions are refused), active, stopped (the recipient stopped the emails), or paused (beyond the site's plan)"`
	CreatedAt   time.Time  `json:"created_at"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	StoppedAt   *time.Time `json:"stopped_at,omitempty"`
	Snippet     string     `json:"snippet" jsonschema:"the HTML to paste into a page of this site"`
}

// FormsResult is what every form tool returns: the site's forms after the call.
type FormsResult struct {
	Enabled  bool         `json:"enabled" jsonschema:"whether this instance sends form mail at all"`
	Limit    int          `json:"limit" jsonschema:"how many forms this site's plan allows"`
	Used     int          `json:"used"`
	Forms    []FormResult `json:"forms"`
	Warnings []string     `json:"warnings,omitempty"`
}
```

Add the five methods from **Interfaces** to `Ops`, after `DownloadSite`.

`internal/mcp/server.go`: before `return s` in `newServer`:

```go
	sdk.AddTool(s, &sdk.Tool{
		Name: "list_forms",
		Description: "List a site's email forms with their status and the HTML snippet for each. A form mails what visitors " +
			"submit to one recipient, who confirmed by email.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in siteArgs) (*sdk.CallToolResult, *FormsResult, error) {
		if err := authorize(auth, ScopeRead); err != nil {
			return nil, nil, err
		}
		return formsOut(ops.ListForms(ctx, auth, in.ref()))
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "add_form",
		Description: "Add an email form to a site. The recipient gets one email to confirm; until they click it the form is " +
			"pending and refuses every submission, so do not tell the user it works before then. Paste the returned " +
			"snippet into a page of the site with write_files. Needs an instance with form mail configured; plans limit " +
			"the forms per site.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in formArgs) (*sdk.CallToolResult, *FormsResult, error) {
		if err := authorize(auth, ScopeWrite); err != nil {
			return nil, nil, err
		}
		return formsOut(ops.AddForm(ctx, auth, in.ref(), in.Form))
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "update_form",
		Description: "Change a form's name, recipient, captcha, attachments or thank-you page. A new recipient has to " +
			"confirm by email again before the form works.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in formUpdateArgs) (*sdk.CallToolResult, *FormsResult, error) {
		if err := authorize(auth, ScopeWrite); err != nil {
			return nil, nil, err
		}
		return formsOut(ops.UpdateForm(ctx, auth, in.ref(), in.Key, in.Form))
	})

	sdk.AddTool(s, &sdk.Tool{
		Name:        "remove_form",
		Description: "Delete a form. Pages that still post to it get an error.",
		Annotations: destructive,
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in formKeyArgs) (*sdk.CallToolResult, *FormsResult, error) {
		if err := authorize(auth, ScopeWrite); err != nil {
			return nil, nil, err
		}
		return formsOut(ops.RemoveForm(ctx, auth, in.ref(), in.Key))
	})

	sdk.AddTool(s, &sdk.Tool{
		Name:        "resend_form_confirmation",
		Description: "Email a form's recipient the confirmation link again, for a form that is pending or that its recipient stopped.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in formKeyArgs) (*sdk.CallToolResult, *FormsResult, error) {
		if err := authorize(auth, ScopeWrite); err != nil {
			return nil, nil, err
		}
		return formsOut(ops.ResendFormConfirmation(ctx, auth, in.ref(), in.Key))
	})
```

Add these argument types, next to the other `...Args` types:

```go
type formArgs struct {
	EditID       string    `json:"edit_id" jsonschema:"the site's edit id"`
	EditPassword string    `json:"edit_password,omitempty" jsonschema:"the site's edit password; not needed with an owning account API token"`
	Form         FormInput `json:"form" jsonschema:"the form's settings; name and recipient are required"`
}

func (a formArgs) ref() SiteRef { return SiteRef{EditID: a.EditID, EditPassword: a.EditPassword} }

type formKeyArgs struct {
	EditID       string `json:"edit_id" jsonschema:"the site's edit id"`
	EditPassword string `json:"edit_password,omitempty" jsonschema:"the site's edit password; not needed with an owning account API token"`
	Key          string `json:"key" jsonschema:"the form's key, as list_forms returns it"`
}

func (a formKeyArgs) ref() SiteRef { return SiteRef{EditID: a.EditID, EditPassword: a.EditPassword} }

type formUpdateArgs struct {
	EditID       string    `json:"edit_id" jsonschema:"the site's edit id"`
	EditPassword string    `json:"edit_password,omitempty" jsonschema:"the site's edit password; not needed with an owning account API token"`
	Key          string    `json:"key" jsonschema:"the form's key, as list_forms returns it"`
	Form         FormInput `json:"form" jsonschema:"the settings to change; omitted fields are left alone"`
}

func (a formUpdateArgs) ref() SiteRef { return SiteRef{EditID: a.EditID, EditPassword: a.EditPassword} }
```

The comment above `out` speaks of "the twelve tool bodies"; make it "the tool bodies", since there are now seventeen.

Add this next to `out`:

```go
func formsOut(r *FormsResult, err error) (*sdk.CallToolResult, *FormsResult, error) {
	if err != nil {
		return nil, nil, err
	}
	return nil, r, nil
}
```

`internal/httpapi/mcpops.go`:

```go
// ---- forms: the same helpers as the JSON API ----

func toFormInput(in mcp.FormInput) formInput {
	return formInput{Name: in.Name, Recipient: in.Recipient, Captcha: in.Captcha, Files: in.Files, Redirect: in.Redirect}
}

func toFormsResult(v formsJSON) *mcp.FormsResult {
	out := &mcp.FormsResult{Enabled: v.Enabled, Limit: v.Limit, Used: v.Used, Forms: make([]mcp.FormResult, 0, len(v.Forms)), Warnings: v.Warnings}
	for _, f := range v.Forms {
		out.Forms = append(out.Forms, mcp.FormResult{
			Key: f.Key, Name: f.Name, Recipient: f.Recipient, Captcha: f.Captcha, Files: f.Files,
			Redirect: f.Redirect, Status: f.Status, CreatedAt: f.CreatedAt,
			ConfirmedAt: f.ConfirmedAt, StoppedAt: f.StoppedAt, Snippet: f.Snippet,
		})
	}
	return out
}

func (o mcpOps) ListForms(_ context.Context, auth mcp.Auth, ref mcp.SiteRef) (*mcp.FormsResult, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	return toFormsResult(o.a.listFormsFor(site)), nil
}

func (o mcpOps) AddForm(ctx context.Context, auth mcp.Auth, ref mcp.SiteRef, in mcp.FormInput) (*mcp.FormsResult, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	v, err := o.a.addForm(ctx, site, toFormInput(in))
	if err != nil {
		return nil, o.mcpError(err)
	}
	return toFormsResult(v), nil
}

func (o mcpOps) UpdateForm(ctx context.Context, auth mcp.Auth, ref mcp.SiteRef, key string, in mcp.FormInput) (*mcp.FormsResult, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	v, err := o.a.updateForm(ctx, site, key, toFormInput(in))
	if err != nil {
		return nil, o.mcpError(err)
	}
	return toFormsResult(v), nil
}

func (o mcpOps) RemoveForm(_ context.Context, auth mcp.Auth, ref mcp.SiteRef, key string) (*mcp.FormsResult, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	v, err := o.a.deleteForm(site, key)
	if err != nil {
		return nil, o.mcpError(err)
	}
	return toFormsResult(v), nil
}

func (o mcpOps) ResendFormConfirmation(ctx context.Context, auth mcp.Auth, ref mcp.SiteRef, key string) (*mcp.FormsResult, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	v, err := o.a.resendConfirmation(ctx, site, key)
	if err != nil {
		return nil, o.mcpError(err)
	}
	return toFormsResult(v), nil
}
```

In `mcpError`, add before `default`:

```go
	case errors.Is(err, store.ErrFormNotFound):
		return errors.New("no form with that key on this site — list_forms shows the keys")
	case errors.Is(err, store.ErrFormActive):
		return errors.New("this form's recipient has already confirmed; there is nothing to resend")
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/mcp/ ./internal/httpapi/ -v -run 'Form|Catalog|Scope|MCP' && go test ./... && go test -tags ee ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/ops.go internal/mcp/server.go internal/mcp/server_test.go internal/httpapi/mcpops.go internal/httpapi/mcpops_test.go
git commit -m "feat: agents list, add, change and remove a site's forms over MCP"
```

---

### Task 13: The Forms card on the edit page

**Files:**
- Modify: `web/static/edit.html`: a new card after `card-domains`
- Modify: `web/static/edit.js`: `loadForms`, `renderForms` and the editor
- Modify: `web/static/app.css`: form rows and status badges

**Interfaces:**
- Consumes: the JSON API from Task 9. `GET /forms` returns `{enabled, limit, used, max_files, forms:[{key,name,recipient,captcha,files,redirect,status,snippet,…}]}`. `POST` creates, `PUT /forms/{key}` updates, `DELETE /forms/{key}` deletes, and `POST /forms/{key}/confirmation` resends.

- [ ] **Step 1: Add the card** (`web/static/edit.html`, directly after the closing `</section>` of `card-domains`)

```html
  <section class="card hidden" id="card-forms">
    <h3>Forms <span class="count" id="forms-count"></span></h3>
    <p class="sub hidden" id="forms-none">This site's plan includes no forms.</p>
    <div id="formrows"></div>
    <div class="formedit" id="form-edit">
      <div class="row">
        <input type="text" id="f-name" placeholder="Form name, e.g. Contact" maxlength="60" style="max-width:220px">
        <input type="email" id="f-recipient" placeholder="recipient@example.com" style="max-width:260px">
      </div>
      <div class="row" style="margin-top:10px">
        <label class="chk"><input type="checkbox" id="f-captcha"> Captcha</label>
        <label class="chk" id="f-files-wrap"><input type="checkbox" id="f-files"> Attachments</label>
        <input type="text" id="f-redirect" placeholder="Thank-you page, e.g. /thanks.html (optional)" style="max-width:300px">
      </div>
      <div class="row" style="margin-top:10px">
        <button class="btn small" id="f-save">Add form</button>
        <button class="btn small hidden" id="f-cancel">Cancel</button>
      </div>
    </div>
    <div class="dnshint">
      A form mails what visitors submit to its recipient, who confirms once by email before the form goes live.
      Paste its snippet into any page of this site: it is plain HTML and needs no script unless the captcha is on.
      Every mail lays out the fields, carries the attachments, and includes a <code>submission.json</code> for machines.
    </div>
  </section>
```

- [ ] **Step 2: Add the script** (`web/static/edit.js`, a new section before `// ---- danger ----`)

```js
// ---- forms ----

let formsData = null;
let editingKey = null;

const FORM_STATUS = {
  pending: ["Awaiting confirmation", "The recipient has an email with a link to confirm. Until then, submissions are refused."],
  active: ["Active", ""],
  stopped: ["Stopped by recipient", "The recipient stopped these emails. Resend the confirmation to ask again."],
  paused: ["Paused — plan limit", "This site's plan allows fewer forms. It resumes when the plan allows it again."],
};

async function loadForms() {
  try {
    formsData = await api("GET", "/forms");
  } catch {
    formsData = null;
  }
  renderForms();
}

function smallButton(label, onClick) {
  const b = document.createElement("button");
  b.className = "btn small";
  b.textContent = label;
  b.addEventListener("click", onClick);
  return b;
}

// armedButton asks for a second click within four seconds before it acts.
function armedButton(label, armedLabel, onConfirm) {
  const b = smallButton(label, async () => {
    if (!b.dataset.armed) {
      b.dataset.armed = "1";
      b.textContent = armedLabel;
      b.classList.add("danger");
      setTimeout(() => {
        delete b.dataset.armed;
        b.textContent = label;
        b.classList.remove("danger");
      }, 4000);
      return;
    }
    await onConfirm();
  });
  return b;
}

function showSnippet(row, snippet) {
  let pre = row.querySelector("pre.snippet");
  if (!pre) {
    pre = document.createElement("pre");
    pre.className = "snippet";
    row.append(pre);
  }
  pre.textContent = snippet;
}

function renderForms() {
  const card = $("card-forms");
  if (!formsData || !formsData.enabled) {
    card.classList.add("hidden");
    return;
  }
  card.classList.remove("hidden");
  $("forms-count").textContent = formsData.used + " of " + formsData.limit;
  $("forms-none").classList.toggle("hidden", formsData.limit > 0 || formsData.forms.length > 0);
  $("form-edit").classList.toggle("hidden", !editingKey && formsData.forms.length >= formsData.limit);
  $("f-files-wrap").classList.toggle("hidden", formsData.max_files === 0);

  const rows = $("formrows");
  rows.innerHTML = "";
  for (const f of formsData.forms) {
    const [label, hint] = FORM_STATUS[f.status] || [f.status, ""];
    const row = document.createElement("div");
    row.className = "formrow";
    const head = document.createElement("div");
    head.className = "formhead";
    const name = document.createElement("span");
    name.className = "fname";
    name.textContent = f.name;
    const tag = document.createElement("span");
    tag.className = "fstatus " + f.status;
    tag.textContent = label;
    const to = document.createElement("span");
    to.className = "fto";
    to.textContent = "→ " + f.recipient;
    head.append(name, tag, to);
    row.append(head);
    if (hint) {
      const p = document.createElement("div");
      p.className = "sub";
      p.textContent = hint;
      row.append(p);
    }
    const actions = document.createElement("div");
    actions.className = "row";
    actions.append(
      smallButton("Copy snippet", async () => {
        try {
          await navigator.clipboard.writeText(f.snippet);
          toast("Snippet copied — paste it into a page");
        } catch {
          showSnippet(row, f.snippet);
        }
      }),
      smallButton("Edit", () => startFormEdit(f)),
    );
    if (f.status === "pending" || f.status === "stopped") {
      actions.append(smallButton("Resend confirmation", async () => {
        try {
          formsData = await api("POST", "/forms/" + f.key + "/confirmation");
          renderForms();
          toast("Confirmation sent to " + f.recipient);
        } catch (err) { toast(err.message, true); }
      }));
    }
    actions.append(armedButton("Delete", "Click again to delete", async () => {
      try {
        await api("DELETE", "/forms/" + f.key);
        if (editingKey === f.key) resetFormEditor();
        await loadForms();
        toast("Deleted " + f.name);
      } catch (err) { toast(err.message, true); }
    }));
    row.append(actions);
    rows.appendChild(row);
  }
}

function startFormEdit(f) {
  editingKey = f.key;
  $("f-name").value = f.name;
  $("f-recipient").value = f.recipient;
  $("f-captcha").checked = f.captcha;
  $("f-files").checked = f.files;
  $("f-redirect").value = f.redirect || "";
  $("f-save").textContent = "Save form";
  $("f-cancel").classList.remove("hidden");
  renderForms();
  $("f-name").focus();
}

function resetFormEditor() {
  editingKey = null;
  for (const id of ["f-name", "f-recipient", "f-redirect"]) $(id).value = "";
  $("f-captcha").checked = false;
  $("f-files").checked = false;
  $("f-save").textContent = "Add form";
  $("f-cancel").classList.add("hidden");
}

$("f-save").addEventListener("click", async () => {
  const body = {
    name: $("f-name").value.trim(),
    recipient: $("f-recipient").value.trim(),
    captcha: $("f-captcha").checked,
    files: $("f-files").checked,
    redirect: $("f-redirect").value.trim(),
  };
  const wasEditing = editingKey;
  try {
    formsData = wasEditing
      ? await api("PUT", "/forms/" + wasEditing, body)
      : await api("POST", "/forms", body);
    resetFormEditor();
    renderForms();
    if (formsData.warnings && formsData.warnings.length) toast(formsData.warnings[0], true);
    else toast(wasEditing ? "Saved" : "Added — " + body.recipient + " has an email to confirm");
  } catch (err) { toast(err.message, true); }
});
$("f-cancel").addEventListener("click", () => { resetFormEditor(); renderForms(); });
```

Then, in the unlock `submit` handler **and** in `boot()`, add `loadForms();` on the line after each `render();`.

- [ ] **Step 3: Add the styles** (`web/static/app.css`, at the end)

```css
/* ---- forms card ---- */
.formrow { padding: 14px 0; border-top: 1px dashed var(--line); display: grid; gap: 8px; }
.formrow:first-child { border-top: 0; padding-top: 4px; }
.formhead { display: flex; flex-wrap: wrap; align-items: baseline; gap: 10px; }
.fname { font-weight: 600; }
.fto { color: var(--ink-dim); font-family: var(--mono); font-size: 13px; overflow-wrap: anywhere; }
.fstatus {
  font: 600 11px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase;
  padding: 5px 8px; border-radius: 6px; border: 1px solid var(--line); color: var(--ink-dim);
}
.fstatus.active { color: var(--ok); border-color: rgba(94, 207, 138, .45); }
.fstatus.pending { color: var(--amber); border-color: rgba(245, 184, 77, .5); border-style: dashed; }
.fstatus.stopped, .fstatus.paused { color: var(--danger); border-color: rgba(242, 109, 109, .45); }
.formedit { margin-top: 14px; padding-top: 14px; border-top: 1px dashed var(--line); }
pre.snippet {
  white-space: pre-wrap; word-break: break-all; font: 12px/1.5 var(--mono);
  background: var(--bg); border: 1px solid var(--line); border-radius: 10px; padding: 12px;
}
label.chk { display: inline-flex; gap: 6px; align-items: center; color: var(--ink-dim); font-size: 14px; }
```

- [ ] **Step 4: Verify in a browser**

Build and run the community image with a Mailpit container. Task 15 uses the same setup:

```bash
docker build -t sitebin:dev .
docker network create sitebin-forms-dev
docker run -d --name mailpit-dev --network sitebin-forms-dev -p 8025:8025 axllent/mailpit
docker run -d --name sitebin-forms-dev --network sitebin-forms-dev -p 8090:80 \
  -e SITEBIN_BASE_DOMAIN=sitebin.localtest.me:8090 -e SITEBIN_HTTP_ONLY=true \
  -e SITEBIN_FORMS_SMTP_HOST=mailpit-dev -e SITEBIN_FORMS_SMTP_PORT=1025 \
  -e SITEBIN_FORMS_SMTP_FROM=forms@sitebin.localtest.me sitebin:dev
```

Create a site at `http://sitebin.localtest.me:8090/` and open its edit page. Check the following, and take a screenshot of the card in each state:
- the Forms card shows `0 of 10`;
- adding a form lists it as *Awaiting confirmation*;
- the confirmation mail is in Mailpit at `http://localhost:8025`;
- after you confirm through its link, the form reads *Active*;
- *Copy snippet* copies HTML that posts successfully when pasted into the site's `index.html`;
- *Edit* changes the name;
- *Delete* needs two clicks.

Clean up with `docker rm -f sitebin-forms-dev mailpit-dev && docker network rm sitebin-forms-dev`.

- [ ] **Step 5: Commit**

```bash
git add web/static/edit.html web/static/edit.js web/static/app.css
git commit -m "feat: the edit page adds, edits and removes forms and hands out their snippet"
```

---

### Task 14: Mail preview, README and CLAUDE.md

**Files:**
- Create: `internal/forms/preview/main.go`
- Modify: `README.md`, `CLAUDE.md`

**Interfaces:**
- Consumes: `forms.BuildSubmission` and `forms.BuildConfirmation`.

- [ ] **Step 1: Write the preview tool** (`internal/forms/preview/main.go`)

```go
// Command preview writes sample form mails as .eml and .html, so their look
// can be checked in real mail clients before a change ships:
//
//	go run ./internal/forms/preview [-out DIR]
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/forms"
)

func main() {
	out := flag.String("out", filepath.Join(os.TempDir(), "sitebin-mail-preview"), "directory to write the samples to")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	sub := &forms.Submission{
		Fields: []forms.Field{
			{Name: "name", Value: "Anna Muster"},
			{Name: "email", Value: "anna@example.com"},
			{Name: "phone", Value: "+43 660 1234567"},
			{Name: "topic", Value: "Hosting"},
			{Name: "topic", Value: "Domains"},
			{Name: "message", Value: "Hallo!\n\nIch hätte gerne ein Angebot für eine Website mit drei Unterseiten.\nGrüße aus Linz 👋"},
			{Name: "newsletter", Value: ""},
		},
		Control: map[string]string{},
		Files:   []forms.File{{Field: "attachments", Filename: "Skizze Startseite.pdf", ContentType: "application/pdf", Data: make([]byte, 183244)}},
	}
	sm, err := forms.BuildSubmission(forms.SubmissionMail{
		From: "forms@sitebin.io", FormName: "Kontakt", FormKey: "k7f3m2q9xaw4npd6", Recipient: "office@example.com",
		SiteID: "abcdefghijklmnopqrstuvwxyz", Host: "www.example.com",
		StopURL: "https://app.sitebin.io/forms/stop?t=preview", At: time.Now(), Sub: sub,
	})
	if err != nil {
		log.Fatal(err)
	}
	cm, err := forms.BuildConfirmation(forms.ConfirmationMail{
		From: "forms@sitebin.io", FormName: "Kontakt", Recipient: "office@example.com",
		Host: "www.example.com", ConfirmURL: "https://app.sitebin.io/forms/confirm?t=preview", At: time.Now(),
	})
	if err != nil {
		log.Fatal(err)
	}
	write(*out, "submission", sm)
	write(*out, "confirmation", cm)
}

func write(dir, name string, m forms.Mail) {
	eml := filepath.Join(dir, name+".eml")
	if err := os.WriteFile(eml, m.Data, 0o644); err != nil {
		log.Fatal(err)
	}
	html := filepath.Join(dir, name+".html")
	if err := os.WriteFile(html, htmlPart(m.Data), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", eml, "and", html)
}

// htmlPart digs the decoded text/html part out of a message.
func htmlPart(data []byte) []byte {
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		log.Fatal(err)
	}
	var find func(ct string, r io.Reader) []byte
	find = func(ct string, r io.Reader) []byte {
		_, params, _ := mime.ParseMediaType(ct)
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err != nil {
				return nil
			}
			pct := p.Header.Get("Content-Type")
			if strings.HasPrefix(pct, "multipart/") {
				if b := find(pct, p); b != nil {
					return b
				}
				continue
			}
			if strings.HasPrefix(pct, "text/html") {
				b, _ := io.ReadAll(quotedprintable.NewReader(p))
				return b
			}
		}
	}
	return find(msg.Header.Get("Content-Type"), msg.Body)
}
```

Run it: `go run ./internal/forms/preview`. Expected: four files written in the temp directory.

- [ ] **Step 2: Operator checkpoint: the mail's look**

Show the operator both `.html` files (screenshots, or a private Artifact page with both side by side) and send both `.eml` files to a Gmail, an Outlook and an Apple Mail inbox, for example by forwarding them through Mailpit's relay or by importing the `.eml` into each client. **Wait for the operator's approval of the look.** Change the templates in `internal/forms/templates/` until they approve, re-running the tool each time. If the templates change, `go test ./internal/forms/` must still pass.

- [ ] **Step 3: README**

Add these rows to the **Configuration reference** table, after `SITEBIN_INTERNAL_ADDR`:

```markdown
| `SITEBIN_FORMS_SMTP_HOST` | — | SMTP server for [forms](#forms). Unset: the instance has no forms. Separate from the account mailer's `SITEBIN_SMTP_*`. |
| `SITEBIN_FORMS_SMTP_PORT` | `587` | |
| `SITEBIN_FORMS_SMTP_USER` / `SITEBIN_FORMS_SMTP_PASS` | — | Optional SMTP AUTH (PLAIN; sent only over TLS, or to localhost). |
| `SITEBIN_FORMS_SMTP_FROM` | — | **Required** with the host. A bare address such as `forms@example.com`; each form supplies the display name. SPF/DKIM for its domain must cover the SMTP server. |
| `SITEBIN_FORMS_SMTP_TLS` | `false` | Implicit TLS (port 465). Otherwise STARTTLS whenever the server offers it. |
| `SITEBIN_FORMS_MAX_PER_SITE` | `10` (community) / `0` (with accounts) | Forms per site when no plan says otherwise. With accounts, a tier's `max_forms` decides. |
| `SITEBIN_FORMS_MAX_FILES` / `SITEBIN_FORMS_MAX_FILE_BYTES` | `5` / `2097152` | Attachments per submission, and bytes per attachment. `0` files turns attachments off. |
| `SITEBIN_FORMS_PER_IP_HOUR` / `SITEBIN_FORMS_PER_FORM_HOUR` | `10` / `60` | Submissions per visitor IP (all forms) and per form. |
```

Add a `### Forms` section under **Using Sitebin**, directly before `### View access modes`:

````markdown
### Forms

A site's pages can post plain HTML forms to Sitebin, which mails each
submission to one recipient: no backend, and no script unless you want a
captcha. Add a form on the edit page (or with the API or MCP). Its recipient
gets **one email to confirm**; until they click it, the form refuses
submissions. Then paste its snippet into any page:

```html
<form action="/_sitebin/forms/k7f3m2q9xaw4npd6" method="post">
  <label>Name <input name="name" required></label>
  <label>Email <input name="email" type="email" required></label>
  <label>Message <textarea name="message" required></textarea></label>
  <input name="_gotcha" tabindex="-1" autocomplete="off" aria-hidden="true" style="position:absolute;left:-9999px">
  <button type="submit">Send</button>
</form>
```

- The form posts to its own site's origin (view host or custom domain), so no
  CORS is involved and a key works only on the site it belongs to.
- `email` becomes the mail's `Reply-To`, and `_subject` its subject. Fields
  starting with `_` are never forwarded. `_gotcha` is a honeypot: fill it and
  nothing is sent, while the bot is told it worked.
- **Captcha:** switch it on and the snippet gains
  `<altcha-widget challenge="/_sitebin/forms/<key>/challenge">` plus
  `<script type="module" src="/_sitebin/altcha.js">`, an ALTCHA proof of work
  served by Sitebin itself. A page with its own strict CSP needs
  `worker-src blob:`.
- **Attachments:** switch them on and the form posts `multipart/form-data`.
  Executables are refused.
- Every mail has an HTML and a text part, the attachments, and
  `submission.json`: the fields in form order, the files' sizes and SHA-256
  hashes, and the form and site.
- Without JavaScript the browser is sent to the form's thank-you path (or a
  default page). Send `Accept: application/json` to get `{"ok":true}` instead.
- Each mail carries a stop link and `List-Unsubscribe`: the recipient can
  stop a form at any time, and only their own click re-activates it.
- With accounts, a tier's `max_forms` caps the forms **per site** (0 or absent
  means none). A smaller plan pauses the newest forms and never deletes them.
````

In the API listing (the block with `# custom domains`), add:

```bash
# forms: list / add / change / delete / resend the recipient's confirmation
curl -H "X-Edit-Password: $PW" "$BASE/api/sites/$EDIT/forms"
curl -H "X-Edit-Password: $PW" -H "Content-Type: application/json" \
     -d '{"name":"Contact","recipient":"office@example.com","captcha":true}' "$BASE/api/sites/$EDIT/forms"
curl -X PUT -H "X-Edit-Password: $PW" -H "Content-Type: application/json" -d '{"redirect":"/thanks.html"}' "$BASE/api/sites/$EDIT/forms/$KEY"
curl -X DELETE -H "X-Edit-Password: $PW" "$BASE/api/sites/$EDIT/forms/$KEY"
curl -X POST -H "X-Edit-Password: $PW" "$BASE/api/sites/$EDIT/forms/$KEY/confirmation"
```

Match the variable names that block already uses; if it uses different ones, rename these.

In the MCP tool table, add `| list_forms / add_form / update_form / remove_form / resend_form_confirmation | Email forms; a new form works once its recipient confirms |`. In the scope table, add `list_forms` to `sitebin:sites:read` and the other four to `sitebin:sites:write`.

In the tier notes ("an unlimited tier needs explicit large caps"), change the sentence to: `` `custom_domains: 0` means *no* custom domains, and `max_containers: 0`, `max_zones: 0` and `max_forms: 0` mean none. ``

- [ ] **Step 4: CLAUDE.md**

Add a section after `## Container sites`:

```markdown
## Site forms

A site's pages post plain HTML forms to `/_sitebin/forms/<key>` on their own
origin, and the core mails them to a recipient. Read
`docs/superpowers/specs/2026-09-24-site-forms-design.md` first.

- **All core, own mailer.** `internal/forms` is pure logic (rules, tokens,
  parser, MIME, SMTP, captcha); `internal/httpapi` wires it. The forms mailer
  (`SITEBIN_FORMS_SMTP_*`) is deliberately separate from `ee/smtp`: the two
  send different mail to different people.
- **The recipient consents, by POST.** A form is `pending` until its
  recipient confirms; GET on a confirm or stop link only ever shows a button,
  because mail scanners fetch every link. `seq` moves on a recipient change
  and on a stop, and that is what kills older confirmation links.
- **The cap is stamped, like custom_domains.** Submissions read `quota_forms`
  from `meta.json` and never ask the extension. With a provider, an unstamped
  site has **0** forms (never the community default of 10), and every
  constructor of `store.Quota` must pass `Forms`, or `ApplyQuota` resets it.
- **Nothing is stored or logged.** Submissions are mailed synchronously (a
  failure is a 502 the visitor can retry) and never written down. Logs carry
  site, key, size and file count, never values, filenames or the recipient.
- **ALTCHA traps.** Always pass `DeriveKey` to `VerifySolution` (without it
  the library accepts on the signature alone), and keep the replay memory
  (the library has none). The widget is `web/vendor/altcha.min.js`; bump it
  and the Go library together.
```

Also add `forms` to the list of `internal/` packages in **Layout**. In **Commands**, the paragraph "A full pass is all ten run by hand" becomes "all eleven". Add `forms.ps1` to the community-image scripts, with a note that it pulls `axllent/mailpit` as its SMTP server.

- [ ] **Step 5: Commit**

```bash
git add internal/forms/preview README.md CLAUDE.md
git commit -m "docs: forms are documented in the README and CLAUDE.md, and a preview tool renders their mails"
```

---
### Task 15: End-to-end: `forms.ps1` against Mailpit, and the plan cap in `tiers.ps1`

**Files:**
- Create: `e2e/forms.ps1`
- Modify: `e2e/tiers.ps1`

**Interfaces:**
- Consumes: the whole product (Tasks 1–14), built as `sitebin:dev` (community) and `sitebin:dev-ee` (`--build-arg EDITION=enterprise`).
- The Mailpit API used here is `GET /api/v1/messages`, `GET /api/v1/message/{ID}` (fields `From`, `ReplyTo`, `Text`, `HTML`, `Attachments[].FileName/PartID`), `GET /api/v1/message/{ID}/part/{PartID}` and `GET /api/v1/message/{ID}/headers`. **Before writing the assertions**, start Mailpit, send it one mail (`go run ./internal/forms/preview` does not send; use the Task 13 setup and add a form), and check those shapes with `curl http://localhost:8025/api/v1/messages`. If a field is named differently, use the real name.

- [ ] **Step 1: Write the script** (`e2e/forms.ps1`, **pure ASCII**)

```powershell
# Sitebin -- forms E2E against the real community image and a real SMTP server.
#
#   powershell -File e2e\forms.ps1 [-Image sitebin:dev]
#
# Runs Mailpit as the SMTP server on a private Docker network and checks the
# whole path: a form created over the API, the confirmation mail, confirming
# through its link (GET shows a button, POST acts), a plain HTML post with an
# attachment, the delivered mail (From, Reply-To, both parts, submission.json,
# List-Unsubscribe), the JSON answer, the honeypot, a refused executable, the
# recipient's one-click stop, and logs free of submitted content. Captcha and
# plan caps are covered by the Go suite and tiers.ps1.
param(
    [string]$Image = "sitebin:dev",
    [int]$Port = 8091,
    [int]$MailPort = 8026
)

$ErrorActionPreference = "Continue"
$base = "sitebin.localtest.me"
$origin = "http://${base}:$Port"
$mailApi = "http://localhost:$MailPort/api/v1"
$name = "sitebin-forms-e2e"
$mailName = "sitebin-forms-e2e-mail"
$net = "sitebin-forms-e2e-net"
$vol = "sitebin-forms-e2e-data"
$work = Join-Path $PSScriptRoot ".work"
New-Item -ItemType Directory -Force $work | Out-Null

# Every assertion must run; the total is checked at the end. Update it when
# you add or remove one.
$ExpectedAssertions = 33
$script:pass = 0; $script:fail = 0
function Assert([string]$n, $c, [string]$d = "") {
    $ok = $false
    if ($null -ne $c) {
        if ($c -is [bool]) { $ok = $c }
        elseif ($c -is [array]) { $ok = ($c.Count -gt 0) }
        else { $ok = [bool]$c }
    }
    if ($ok) { $script:pass++; Write-Host "  ok   $n" -ForegroundColor Green }
    else { $script:fail++; Write-Host "  FAIL $n  $d" -ForegroundColor Red }
}

function Req([string]$method, [string]$url, [string[]]$extra = @()) {
    $bodyFile = Join-Path $work "fb.tmp"
    Remove-Item $bodyFile -ErrorAction SilentlyContinue
    $a = @("-s", "-X", $method, "-o", $bodyFile, "-w", "%{http_code} %{redirect_url}", "--max-time", "30") + $extra + @($url)
    $out = "$(& curl.exe @a)"
    $parts = $out.Split(" ", 2)
    $body = ""; if (Test-Path $bodyFile) { $body = [IO.File]::ReadAllText($bodyFile) }
    $loc = ""; if ($parts.Count -gt 1) { $loc = $parts[1] }
    return @{ code = [int]$parts[0]; body = $body; location = $loc }
}

function JsonFile([string]$json) {
    $p = Join-Path $work ("fj" + (Get-Random) + ".json"); [IO.File]::WriteAllText($p, $json); return "@$p"
}

function MailsTo([string]$addr) {
    $r = Req "GET" "$mailApi/messages"
    if ($r.code -ne 200) { return @() }
    return @(($r.body | ConvertFrom-Json).messages | Where-Object { (@($_.To) | ForEach-Object { $_.Address }) -contains $addr })
}

function WaitMails([string]$addr, [int]$count) {
    for ($i = 0; $i -lt 20; $i++) {
        $m = MailsTo $addr
        if ($m.Count -ge $count) { return $m }
        Start-Sleep -Milliseconds 500
    }
    return MailsTo $addr
}

function Message([string]$id) { return ((Req "GET" "$mailApi/message/$id").body | ConvertFrom-Json) }

function Cleanup {
    docker rm -f $name $mailName 2>$null | Out-Null
    docker network rm $net 2>$null | Out-Null
    docker volume rm $vol 2>$null | Out-Null
}

Write-Host "== starting Mailpit and $Image on $Port" -ForegroundColor Cyan
Cleanup
docker network create $net | Out-Null
docker run -d --name $mailName --network $net -p "${MailPort}:8025" axllent/mailpit | Out-Null
docker run -d --name $name --network $net -p "${Port}:80" -v "${vol}:/data" `
    -e "SITEBIN_BASE_DOMAIN=${base}:$Port" -e "SITEBIN_HTTP_ONLY=true" `
    -e "SITEBIN_FORMS_SMTP_HOST=$mailName" -e "SITEBIN_FORMS_SMTP_PORT=1025" `
    -e "SITEBIN_FORMS_SMTP_FROM=forms@localtest.me" `
    $Image | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "docker run failed" -ForegroundColor Red; Cleanup; exit 1 }
$up = $false
for ($i = 0; $i -lt 40; $i++) {
    Start-Sleep -Milliseconds 700
    if ((Req "GET" "$origin/").code -eq 200 -and (Req "GET" "$mailApi/messages").code -eq 200) { $up = $true; break }
}
Assert "containers up" $up
if (-not $up) { docker logs $name; Cleanup; exit 1 }

Write-Host "== a site and a form" -ForegroundColor Cyan
$page = Join-Path $work "forms-index.html"; [IO.File]::WriteAllText($page, "<h1>forms e2e</h1>")
$r = Req "POST" "$origin/api/sites" @("-H", "Sec-Fetch-Site: same-origin", "-F", "files=@$page;filename=index.html")
Assert "site created" ($r.code -eq 201) "$($r.code) $($r.body)"
$site = $r.body | ConvertFrom-Json
$edit = ($site.edit_url -split "/e/")[1]
$pw = @("-H", "X-Edit-Password: $($site.edit_password)")
$siteOrigin = $site.view_url.TrimEnd("/")
$post = "$siteOrigin/_sitebin/forms"

$r = Req "POST" "$origin/api/sites/$edit/forms" ($pw + @("-H", "Content-Type: application/json", "--data", (JsonFile '{"name":"Contact","recipient":"owner@example.test","files":true}')))
Assert "form created (201)" ($r.code -eq 201) "$($r.code) $($r.body)"
$forms = $r.body | ConvertFrom-Json
$key = $forms.forms[0].key
Assert "form is pending" ($forms.forms[0].status -eq "pending") "$($forms.forms[0].status)"
Assert "snippet posts to the site's own origin" ($forms.forms[0].snippet.Contains('action="/_sitebin/forms/' + $key + '"'))

Write-Host "== the recipient confirms" -ForegroundColor Cyan
$m = WaitMails "owner@example.test" 1
Assert "confirmation mail delivered" ($m.Count -eq 1) "$($m.Count)"
$conf = Message $m[0].ID
Assert "confirmation comes from Sitebin" ($conf.From.Name -eq "Sitebin") "$($conf.From.Name)"
$link = [regex]::Match($conf.Text, "http://\S+/forms/confirm\?t=\S+").Value
Assert "confirmation carries its link" ($link -ne "")

$r = Req "POST" "$post/$key" @("--data", "message=too+early")
Assert "a pending form refuses (403)" ($r.code -eq 403) "$($r.code)"
$r = Req "GET" $link
Assert "the link shows a button" ($r.code -eq 200 -and $r.body.Contains('action="/forms/confirm"')) "$($r.code)"
$r = Req "POST" "$post/$key" @("--data", "message=still+early")
Assert "opening the link did not confirm" ($r.code -eq 403) "$($r.code)"
$tok = [uri]::UnescapeDataString(($link -split "t=", 2)[1])
$r = Req "POST" "$origin/forms/confirm" @("--data-urlencode", "t=$tok")
Assert "the button confirms" ($r.code -eq 200 -and $r.body.Contains("Confirmed")) "$($r.code)"

Write-Host "== a plain HTML post with an attachment" -ForegroundColor Cyan
$att = Join-Path $work "hello.txt"; [IO.File]::WriteAllText($att, "hello attachment")
$r = Req "POST" "$post/$key" @("-F", "name=Anna", "-F", "email=anna@example.test", "-F", "message=Hallo aus dem E2E", "-F", "cv=@$att;filename=hello.txt")
Assert "submission answers 303" ($r.code -eq 303) "$($r.code) $($r.body)"
Assert "303 goes to the thank-you page" ($r.location.EndsWith("/_sitebin/forms/$key/thanks")) "$($r.location)"
$m = WaitMails "owner@example.test" 2
Assert "submission mail delivered" ($m.Count -eq 2) "$($m.Count)"
$subId = ($m | Where-Object { $_.Subject -like "New message*" } | Select-Object -First 1).ID
$sub = Message $subId
Assert "From carries the form's name" ($sub.From.Name -eq "Contact" -and $sub.From.Address -eq "forms@localtest.me") "$($sub.From.Name) $($sub.From.Address)"
Assert "Reply-To is the submitter" ((@($sub.ReplyTo) | ForEach-Object { $_.Address }) -contains "anna@example.test")
Assert "text part has the message" ($sub.Text.Contains("message: Hallo aus dem E2E"))
Assert "HTML part present" ($sub.HTML.Length -gt 500) "$($sub.HTML.Length)"
$files = @($sub.Attachments | ForEach-Object { $_.FileName })
Assert "attachment delivered" ($files -contains "hello.txt") "$($files -join ',')"
Assert "submission.json attached" ($files -contains "submission.json") "$($files -join ',')"
$jp = $sub.Attachments | Where-Object { $_.FileName -eq "submission.json" } | Select-Object -First 1
$j = (Req "GET" "$mailApi/message/$subId/part/$($jp.PartID)").body | ConvertFrom-Json
$order = (@($j.fields) | ForEach-Object { $_.name }) -join ","
Assert "submission.json keeps the form's order" ($order -eq "name,email,message") "$order"
Assert "submission.json names the form" ($j.form.key -eq $key -and $j.version -eq 1)
$h = (Req "GET" "$mailApi/message/$subId/headers").body | ConvertFrom-Json
$unsub = ([string]@($h.'List-Unsubscribe')[0]).Trim("<", ">")
Assert "List-Unsubscribe points at the stop page" ($unsub -match "/forms/stop\?t=") "$unsub"

Write-Host "== answers, honeypot, refusals" -ForegroundColor Cyan
$r = Req "POST" "$post/$key" @("-H", "Accept: application/json", "--data", "message=json")
Assert "JSON answer" ($r.code -eq 200 -and $r.body.Trim() -eq '{"ok":true}') "$($r.code) $($r.body)"
$before = (WaitMails "owner@example.test" 3).Count
$r = Req "POST" "$post/$key" @("--data", "message=spam&_gotcha=http%3A%2F%2Fspam.example")
Assert "honeypot looks like a success" ($r.code -eq 303) "$($r.code)"
Start-Sleep -Seconds 1
Assert "honeypot sends nothing" ((MailsTo "owner@example.test").Count -eq $before)
$exe = Join-Path $work "evil.exe"; [IO.File]::WriteAllText($exe, "MZ")
$r = Req "POST" "$post/$key" @("-F", "message=x", "-F", "cv=@$exe;filename=evil.exe")
Assert "an executable is refused (400)" ($r.code -eq 400) "$($r.code)"

Write-Host "== the recipient stops the form" -ForegroundColor Cyan
$r = Req "POST" $unsub @("--data", "List-Unsubscribe=One-Click")
Assert "one-click stop answers 200" ($r.code -eq 200) "$($r.code)"
$r = Req "POST" "$post/$key" @("--data", "message=after+stop")
Assert "a stopped form refuses (403)" ($r.code -eq 403) "$($r.code)"
$r = Req "GET" "$origin/api/sites/$edit/forms" $pw
Assert "the owner sees it stopped" ((($r.body | ConvertFrom-Json).forms[0].status) -eq "stopped")

$logs = (docker logs $name 2>&1 | Out-String)
Assert "logs carry no submitted value" (-not $logs.Contains("Hallo aus dem E2E") -and -not $logs.Contains("anna@example.test"))
Assert "logs carry no recipient" (-not $logs.Contains("owner@example.test"))

Cleanup
Write-Host ""
$total = $script:pass + $script:fail
if ($total -ne $ExpectedAssertions) {
    Write-Host ("  FAIL assertion count: ran {0}, expected {1} - an assertion was skipped or threw" -f $total, $ExpectedAssertions) -ForegroundColor Red
    $script:fail++
}
Write-Host ("== forms E2E: {0} passed, {1} failed" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
exit $(if ($script:fail -eq 0) { 0 } else { 1 })
```

Count the `Assert` calls in the finished script and set `$ExpectedAssertions` to that number. As written above there are 33.

Check that the script is pure ASCII: `LC_ALL=C grep -nP '[^\x00-\x7F]' e2e/forms.ps1` must print nothing.

- [ ] **Step 2: Extend `e2e/tiers.ps1`**

- Change the free tier in `$tiers` to add `"max_forms":1`, so it ends `…"custom_domains":0,"max_expiry_days":7,"max_forms":1}]`.
- Add `-e "SITEBIN_FORMS_SMTP_HOST=127.0.0.1" -e "SITEBIN_FORMS_SMTP_PORT=1" -e "SITEBIN_FORMS_SMTP_FROM=forms@localtest.me"` to its `docker run`. Nothing listens on port 1, so every confirmation mail fails fast. That is on purpose: this script tests the cap, not delivery.
- Inside the `if ($site) { … }` block, after the custom-domain assertion:

```powershell
    # forms: the tier allows one per site. The SMTP server is deliberately
    # unreachable, so the form is created, pending, with a warning.
    $r = Req "POST" "$origin/api/sites/$edit/forms" @("-H", "X-Edit-Password: $($site.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBodyT '{"name":"A","recipient":"a@example.test"}'))
    Assert "first form within max_forms (201)" ($r.code -eq 201) "got $($r.code): $($r.body)"
    Assert "an unsent confirmation is a warning" ($r.body -match '"warnings"') "$($r.body)"
    $r = Req "POST" "$origin/api/sites/$edit/forms" @("-H", "X-Edit-Password: $($site.edit_password)", "-H", "Content-Type: application/json", "--data", (JsonBodyT '{"name":"B","recipient":"b@example.test"}'))
    Assert "second form over max_forms=1 (403)" ($r.code -eq 403) "got $($r.code): $($r.body)"
```

- [ ] **Step 3: Build both images and run the scripts**

```bash
docker build -t sitebin:dev .
docker build --build-arg EDITION=enterprise -t sitebin:dev-ee .
powershell -File e2e/forms.ps1
powershell -File e2e/tiers.ps1
powershell -File e2e/e2e.ps1 -Image sitebin:dev
powershell -File e2e/mcp.ps1 -Image sitebin:dev
```

Expected: all four report 0 failed. `e2e.ps1` and `mcp.ps1` prove that nothing else regressed. If `mcp.ps1` checks the tool list against a fixed set, add the five form tools to it.

- [ ] **Step 4: Commit**

```bash
git add e2e/forms.ps1 e2e/tiers.ps1
git commit -m "test: forms are driven end to end against a real SMTP server, and the plan cap in tiers mode"
```

---
### Task 16: Website: the forms docs page, pricing, and the docs that restate the product

**Repo:** `Sitebin-Website/`. Work on a new branch: `git -C Sitebin-Website switch -c content/site-forms`. **Never push:** every push to `main` deploys.

**Files:**
- Create: `public/docs/forms/index.html`
- Modify: every `public/docs/*/index.html` and `public/docs/index.html` (the docs nav), plus `public/docs/index.html` (the guide list)
- Modify: `public/pricing/index.html`, `public/docs/mcp/index.html`, `public/docs/api/index.html`, `public/docs/configuration/index.html`, `public/docs/enterprise/index.html` and `public/sitemap.xml`

**Interfaces:**
- Consumes: the product as built (Tasks 1–15). Every statement on these pages must match it. The snippet is `forms.Snippet`'s output, the limits are the env defaults, and the tool names are the five from Task 12.
- Workspace coupling (`../CLAUDE.md`): the pricing page must match the hosted instance's `tiers.json` (`max_forms` Pro 1 and Studio 10, which the rollout sets), and `/docs/mcp/` must list the tools by name.

- [ ] **Step 1: The docs page**

Copy `public/docs/containers/index.html` to `public/docs/forms/index.html`, then:
- set `<title>Forms — Sitebin docs</title>`;
- set the description to `Email forms for the sites Sitebin hosts: a plain HTML form, a recipient who confirms once, optional ALTCHA captcha and attachments, and a machine-readable submission.json on every mail.`;
- point the canonical link at `https://sitebin.io/docs/forms/`;
- in its docnav, change `<li><a href="./">Containers</a></li>` to `<li><a href="../containers/">Containers</a></li>` and add `<li><a href="./">Forms</a></li>` after it;
- replace everything inside `<article class="doc">` with this:

```html
      <h1>Forms</h1>
      <p class="lead">A contact form on a static site, with no backend and no
        script: the form posts to Sitebin, and Sitebin emails each submission to
        one recipient, who agreed to receive them.</p>

      <div class="note info">Forms are part of Pro (1 per site) and Studio (10 per
        site) on the hosted service. Self-hosted instances have them as soon as
        <code>SITEBIN_FORMS_SMTP_HOST</code> is set — see
        <a href="#self-hosting">Self-hosting</a>.</div>

      <h2>Add a form</h2>
      <p>On the site's edit page, open <strong>Forms</strong> and give the form a
        name and a recipient. The name is what the recipient sees as the sender of
        every message. The recipient gets <strong>one email to confirm</strong>;
        until they click it, the form refuses submissions. That confirmation is
        what keeps a form from ever mailing someone who did not ask for it.</p>
      <p>Then copy the form's snippet into any page of the site:</p>
      <div class="copywrap">
        <pre class="code">&lt;form action="/_sitebin/forms/k7f3m2q9xaw4npd6" method="post"&gt;
  &lt;label&gt;Name &lt;input name="name" required&gt;&lt;/label&gt;
  &lt;label&gt;Email &lt;input name="email" type="email" required&gt;&lt;/label&gt;
  &lt;label&gt;Message &lt;textarea name="message" required&gt;&lt;/textarea&gt;&lt;/label&gt;
  &lt;input name="_gotcha" tabindex="-1" autocomplete="off" aria-hidden="true" style="position:absolute;left:-9999px"&gt;
  &lt;button type="submit"&gt;Send&lt;/button&gt;
&lt;/form&gt;</pre>
        <button class="copybtn" type="button">Copy</button>
      </div>
      <p>The form posts to its own site — the site's address or your custom
        domain — so there is nothing to allow and nothing to load. Style it and
        add fields as you like: every field is forwarded, in the order the form
        has them.</p>

      <h2>Fields with a meaning</h2>
      <div class="tablewrap">
        <table class="compare">
          <thead><tr><th scope="col">Field</th><th scope="col">What it does</th></tr></thead>
          <tbody>
            <tr><th scope="row"><code>email</code></th><td>Becomes the mail's <code>Reply-To</code>, so answering the mail answers the visitor. Only when it holds exactly one address.</td></tr>
            <tr><th scope="row"><code>_subject</code></th><td>The mail's subject (one line, up to 200 characters). Without it: <em>New message via &lt;form name&gt;</em>.</td></tr>
            <tr><th scope="row"><code>_gotcha</code></th><td>A honeypot, moved off-screen. People never fill it; bots do. A submission with it filled is answered like a success and sends nothing.</td></tr>
            <tr><th scope="row"><code>_…</code></th><td>Any field starting with an underscore is for you and the form, and is never forwarded.</td></tr>
          </tbody>
        </table>
      </div>

      <h2>Captcha</h2>
      <p>Switch on <strong>Captcha</strong> and the snippet gains two lines: an
        <a href="https://altcha.org" rel="noopener">ALTCHA</a> widget and its script.
        The visitor's browser solves a small proof-of-work puzzle — no images, no
        tracking, no third party: the challenge and the script come from Sitebin
        itself. A solution is valid for five minutes, for this form only, once.</p>
      <div class="copywrap">
        <pre class="code">&lt;altcha-widget challenge="/_sitebin/forms/k7f3m2q9xaw4npd6/challenge"&gt;&lt;/altcha-widget&gt;
&lt;script type="module" src="/_sitebin/altcha.js"&gt;&lt;/script&gt;</pre>
        <button class="copybtn" type="button">Copy</button>
      </div>
      <p>If your page sets its own Content-Security-Policy, allow
        <code>worker-src blob:</code>: the widget solves in web workers.</p>

      <h2>Attachments</h2>
      <p>Switch on <strong>Attachments</strong> and the form posts
        <code>multipart/form-data</code> with a file field. Up to 5 files of
        2&nbsp;MB each travel with the mail. Executable files
        (<code>.exe</code>, <code>.js</code>, <code>.bat</code>, <code>.msi</code>
        and the like) are refused: mail providers reject whole messages over them.</p>

      <h2>What arrives</h2>
      <p>One email per submission, from the form's name, with an HTML and a
        plain-text part that lay out the fields in order, the attachments, and a
        stop link. Every mail also carries <code>submission.json</code>, for
        anything that reads mail by machine:</p>
      <div class="copywrap">
        <pre class="code">{
  "version": 1,
  "form": { "key": "k7f3m2q9xaw4npd6", "name": "Contact" },
  "site": { "id": "…", "host": "www.example.com" },
  "submitted_at": "2026-09-24T10:15:00Z",
  "fields": [
    { "name": "name", "value": "Anna Muster" },
    { "name": "email", "value": "anna@example.com" },
    { "name": "message", "value": "Hello!" }
  ],
  "files": [
    { "field": "cv", "filename": "cv.pdf", "content_type": "application/pdf",
      "size": 183244, "sha256": "…" }
  ]
}</pre>
        <button class="copybtn" type="button">Copy</button>
      </div>
      <p><code>fields</code> is a list, so repeated names (a group of checkboxes)
        and the order survive. The visitor's IP address is not included.
        Sitebin <strong>keeps no copy</strong>: a submission is mailed and gone.</p>

      <h2>After sending</h2>
      <p>Without JavaScript the browser lands on the form's thank-you page — a
        path on the same site you set, such as <code>/thanks.html</code> — or a
        plain default page. If something is wrong (the form is not confirmed yet,
        a file is too large, too many messages in a short time), the visitor sees
        why, with a way back.</p>
      <p>Submitting with your own <code>fetch</code>? Send
        <code>Accept: application/json</code> and get <code>{"ok":true}</code>, or
        <code>{"error":"…"}</code> with the status: <code>403</code> for a form that
        is not active or a failed captcha, <code>413</code> for too large,
        <code>429</code> for too many, <code>502</code> when the mail could not be
        sent — try again.</p>

      <h2>The recipient decides</h2>
      <p>Every mail has a <strong>Stop emails from this form</strong> link, and mail
        clients show their own unsubscribe button for it. A stopped form refuses
        submissions until the site's owner resends the confirmation and the
        recipient accepts again. Changing the recipient asks the new address
        first.</p>

      <h2>Limits</h2>
      <ul>
        <li>Forms per site: Pro 1, Studio 10. When a plan shrinks, the newest forms
          pause — nothing is deleted — and resume when the plan allows again.</li>
        <li>10 submissions per visitor per hour across all forms, and 60 per form
          per hour.</li>
        <li>50 fields of up to 10,000 characters; 5 files of 2&nbsp;MB.</li>
      </ul>

      <h2>API and agents</h2>
      <p>Forms are managed with the site's edit password or an account token:
        <code>GET</code>/<code>POST /api/sites/{edit_id}/forms</code>,
        <code>PUT</code>/<code>DELETE …/forms/{key}</code>, and
        <code>POST …/forms/{key}/confirmation</code> to resend — see the
        <a href="../api/#forms">API</a>. Agents have the same through
        <a href="../mcp/">MCP</a>: <code>list_forms</code>, <code>add_form</code>,
        <code>update_form</code>, <code>remove_form</code> and
        <code>resend_form_confirmation</code>.</p>

      <h2 id="self-hosting">Self-hosting</h2>
      <p>Forms are open source, in every edition. Point
        <code>SITEBIN_FORMS_SMTP_HOST</code> at an SMTP server and set
        <code>SITEBIN_FORMS_SMTP_FROM</code> to the address mail leaves from; each
        form supplies the display name. This mailer is separate from the one that
        sends account mail. All variables are on the
        <a href="../configuration/#forms">configuration</a> page.</p>

      <div class="docpager">
        <a href="../containers/">← Containers</a>
        <a href="../embed/">Embed component →</a>
      </div>
```

Check the class names against the page you copied: `docpager` must be whatever `containers/index.html` uses for its previous/next links. Keep that page's own markup where it differs.

Then fix the pager links around the new page. `containers/index.html`'s "next" link becomes `../forms/` ("Forms →"), and `embed/index.html`'s "previous" link becomes `../forms/` ("← Forms").

- [ ] **Step 2: The docs nav on every page**

```bash
cd Sitebin-Website
for f in public/docs/*/index.html; do
  case "$f" in public/docs/forms/*|public/docs/containers/*) continue;; esac
  sed -i 's|^\(\s*\)<li><a href="\.\./containers/">Containers</a></li>|&\n\1<li><a href="../forms/">Forms</a></li>|' "$f"
done
sed -i 's|^\(\s*\)<li><a href="\./">Containers</a></li>|&\n\1<li><a href="../forms/">Forms</a></li>|' public/docs/containers/index.html
sed -i 's|^\(\s*\)<li><a href="containers/">Containers</a></li>|&\n\1<li><a href="forms/">Forms</a></li>|' public/docs/index.html
grep -c 'forms/">Forms</a>' public/docs/*/index.html public/docs/index.html
```

Expected: `1` for every file. In `public/docs/index.html`, add after the Containers entry in the guide list:

```html
        <li><a href="forms/">Forms</a> — email what visitors submit on your site's forms to a confirmed recipient, with captcha, attachments and a JSON copy <em>(Pro and Studio)</em>.</li>
```

Add `  <url><loc>https://sitebin.io/docs/forms/</loc></url>` to `public/sitemap.xml` after the containers entry.

- [ ] **Step 3: Pricing**

In `public/pricing/index.html`:
- Pro card, after the containers `<li>`: `<li><strong>1 form</strong> per site <span class="dim">— submissions by email</span></li>`
- Studio card, after the containers `<li>`: `<li><strong>10 forms</strong> per site <span class="dim">— submissions by email</span></li>`
- The comparison table, after the Containers row: `<tr><th scope="row">Forms per site <span class="dim">(by email)</span></th><td class="na">—</td><td class="na">—</td><td>1</td><td>10</td></tr>`
- In the FAQ, after "What are containers?":

```html
        <details>
          <summary>How do forms work?</summary>
          <p>On Pro and Studio a site's pages can have forms — contact, quote
            request, sign-up — without any backend. Add one on the edit page, paste
            its snippet into a page, and every submission arrives as an email with
            the fields laid out, the attachments, and a JSON copy. The recipient
            confirms once before anything is sent, and can stop it at any time.
            Sitebin keeps no copy of what is submitted. Pro has 1 form per site,
            Studio 10. <a href="../docs/forms/">How forms work</a>.</p>
        </details>
```

- [ ] **Step 4: MCP, API, configuration and enterprise docs**

`public/docs/mcp/index.html`, in the tool table after `download_site`:

```html
            <tr><th scope="row"><code>list_forms</code></th><td>The site's email forms, each with its status and the HTML snippet to paste.</td></tr>
            <tr><th scope="row"><code>add_form</code></th><td>Add a form. Its recipient confirms by email first; until then the form is <code>pending</code> and refuses submissions. <em>Needs form mail on the instance; Pro and Studio on the hosted service.</em></td></tr>
            <tr><th scope="row"><code>update_form</code></th><td>Change a form's name, recipient, captcha, attachments or thank-you page. A new recipient confirms again.</td></tr>
            <tr><th scope="row"><code>remove_form</code></th><td>Delete a form.</td></tr>
            <tr><th scope="row"><code>resend_form_confirmation</code></th><td>Email the recipient the confirmation link again.</td></tr>
```

In its Authentication section, add `list_forms` to the list of tools under `sitebin:sites:read`, and add the other four under `sitebin:sites:write`.

`public/docs/api/index.html`, a section after **Containers**:

```html
      <h2 id="forms">Forms</h2>
      <p>A site's email forms (see <a href="../forms/">Forms</a>). Adding one
        answers <code>201</code> and mails the recipient a confirmation; the form
        is <code>pending</code> until they accept. Every answer is the site's form
        list: <code>enabled</code>, <code>limit</code>, <code>used</code>, and each
        form with its <code>status</code> (<code>pending</code> ·
        <code>active</code> · <code>stopped</code> · <code>paused</code>) and
        <code>snippet</code>. Past the plan's forms per site: <code>403</code>. More
        than 10 confirmation mails per site, or 3 per address, in a day:
        <code>429</code>.</p>
      <div class="copywrap">
        <pre class="code">curl -H "X-Edit-Password: $PW" https://app.sitebin.io/api/sites/$EDIT_ID/forms
curl -H "X-Edit-Password: $PW" -H "Content-Type: application/json" \
     -d '{"name":"Contact","recipient":"office@example.com","captcha":true,"files":false,"redirect":"/thanks.html"}' \
     https://app.sitebin.io/api/sites/$EDIT_ID/forms
curl -X PUT -H "X-Edit-Password: $PW" -H "Content-Type: application/json" -d '{"recipient":"team@example.com"}' \
     https://app.sitebin.io/api/sites/$EDIT_ID/forms/$KEY
curl -X DELETE -H "X-Edit-Password: $PW" https://app.sitebin.io/api/sites/$EDIT_ID/forms/$KEY
curl -X POST -H "X-Edit-Password: $PW" https://app.sitebin.io/api/sites/$EDIT_ID/forms/$KEY/confirmation</pre>
        <button class="copybtn" type="button">Copy</button>
      </div>
```

`public/docs/configuration/index.html`, a section after **MCP (AI agents)**, in the same table markup:

```html
      <h2 id="forms">Forms</h2>
      <div class="tablewrap">
        <table class="compare">
          <thead><tr><th scope="col">Variable</th><th scope="col">Default</th><th scope="col">Purpose</th></tr></thead>
          <tbody>
            <tr><th scope="row"><code>SITEBIN_FORMS_SMTP_HOST</code></th><td>—</td><td>SMTP server for <a href="../forms/">forms</a>. Unset: the instance has no forms. Separate from the account mailer's <code>SITEBIN_SMTP_*</code>.</td></tr>
            <tr><th scope="row"><code>SITEBIN_FORMS_SMTP_PORT</code></th><td><code>587</code></td><td></td></tr>
            <tr><th scope="row"><code>SITEBIN_FORMS_SMTP_USER</code> / <code>_PASS</code></th><td>—</td><td>Optional SMTP AUTH (PLAIN), sent only over TLS or to localhost.</td></tr>
            <tr><th scope="row"><code>SITEBIN_FORMS_SMTP_FROM</code></th><td>—</td><td><strong>Required</strong> with the host: a bare address such as <code>forms@example.com</code>. Each form supplies the display name. SPF/DKIM for its domain must cover the SMTP server.</td></tr>
            <tr><th scope="row"><code>SITEBIN_FORMS_SMTP_TLS</code></th><td><code>false</code></td><td>Implicit TLS (port 465). Otherwise STARTTLS whenever the server offers it.</td></tr>
            <tr><th scope="row"><code>SITEBIN_FORMS_MAX_PER_SITE</code></th><td><code>10</code> · <code>0</code> with accounts</td><td>Forms per site where no plan says otherwise. With accounts, a tier's <code>max_forms</code> decides.</td></tr>
            <tr><th scope="row"><code>SITEBIN_FORMS_MAX_FILES</code></th><td><code>5</code></td><td>Attachments per submission; <code>0</code> turns attachments off.</td></tr>
            <tr><th scope="row"><code>SITEBIN_FORMS_MAX_FILE_BYTES</code></th><td><code>2097152</code></td><td>Bytes per attachment.</td></tr>
            <tr><th scope="row"><code>SITEBIN_FORMS_PER_IP_HOUR</code></th><td><code>10</code></td><td>Submissions per visitor IP per hour, across all forms.</td></tr>
            <tr><th scope="row"><code>SITEBIN_FORMS_PER_FORM_HOUR</code></th><td><code>60</code></td><td>Submissions per form per hour.</td></tr>
          </tbody>
        </table>
      </div>
```

`public/docs/enterprise/index.html`: in the sample `tiers.json`, add `"max_forms": 1,` to `pro` and `"max_forms": 10,` to `studio`, each after `max_containers`. After the `max_zones` paragraph, add:

```html
      <p><code>max_forms</code> caps each site's <a href="../forms/">forms</a>.
        <code>0</code> or absent means <strong>none</strong>. It is stamped on a
        site like its other caps; a smaller plan pauses the newest forms rather
        than deleting them. It only matters with
        <code>SITEBIN_FORMS_SMTP_HOST</code> set.</p>
```

- [ ] **Step 5: Check the pages**

Open `public/docs/forms/index.html`, `public/pricing/index.html` and the four edited docs pages in a browser (as `file://` URLs, or serve `public/` with `python -m http.server`). Check each at desktop width and at 375 px wide. Check that every new link resolves. Compare every limit and name on the pages against Tasks 1, 9 and 12.

- [ ] **Step 6: Commit (no push)**

```bash
git -C Sitebin-Website add public
git -C Sitebin-Website commit -m "content: forms docs, pricing and API/MCP/configuration pages for site forms"
```

---

### Task 17: Website: the legal texts (draft for legal review)

**Repo:** `Sitebin-Website/`, same branch, **never pushed** by this plan.

**Files:**
- Modify: `public/privacy/index.html`, `public/dpa/index.html`, `public/terms/index.html`

**Interfaces:**
- **Operator input, needed before this task:** which service the forms mail leaves through, meaning `SITEBIN_FORMS_SMTP_HOST` on `app.sitebin.io`. Ask for its company name, postal address and processing location.
  - If it is an external mail service, it is a new **sub-processor**, and every "[provider]" below uses those details.
  - If it is a mail server IT-Trail runs itself (for example on the Hetzner machine), there is no new sub-processor: leave the Annex 3 row out, and name no provider in the privacy text.
- These are **drafts**. The operator has them reviewed legally before publication. The DPA and the terms are versioned consent documents on the SaaS Stack (`dpa`, `terms`). Publishing a new version, and whether it asks existing customers to accept again, is part of the rollout, not of this task.

- [ ] **Step 1: Privacy policy**

In `public/privacy/index.html`, after the `Content you publish` section, add:

```html
        <h3>Forms on your sites</h3>
        <p>On paid plans a site can have forms whose submissions we email to an
          address the site's owner chose and which confirmed, by clicking a link we
          sent it, that it wants those messages. When a visitor submits such a
          form, we receive what they typed and the files they attached and send it
          as one email to that address, through [provider]. <strong>We keep no copy
          of a submission</strong>: it is not stored on our servers, and our logs
          record only that a form was used — the site, the form, the size and the
          number of files, and, as for every request, the time and the sender's IP
          address, kept for 14 days. The recipient's address and the form's
          settings are stored with the site until the owner deletes the form or the
          site. The site's owner decides what the form asks and is responsible for
          informing their visitors; where the owner acts as a business, we process
          submissions on their behalf under the
          <a href="../dpa/">Data Processing Agreement</a>. The confirmation email
          and every forwarded message carry a link with which the recipient stops
          the messages at any time.</p>
```

If the mail goes out through IT-Trail's own server, the sentence ends `…and send it as one email to that address.` with no provider named.

In **Recipients**, if the provider is external, add it to the processors sentence: `…and, for forms, <strong>[provider]</strong> (delivery of form messages)`.

Update the closing status line from `8 September 2026` to the day the operator publishes. Until then, write `24 September 2026 (draft)`.

- [ ] **Step 2: DPA**

In `public/dpa/index.html`, **Annex 1**:
- Nature: `Storage, backup, transmission, rendering, export, deletion; forwarding of form submissions by email`
- Data subjects: `Persons appearing in published content; visitors of published sites, including visitors who submit forms on them; recipients of form messages designated by the Customer`
- Data categories: `Content data as uploaded by the Customer; visitor connection data in server logs; form submissions (the fields and files a visitor submits), forwarded to the recipient the Customer designated and not stored; the recipient's email address`

**Annex 2**, a new list item after **Integrity**:

```html
          <li><strong>Forms.</strong> A form sends nothing until its recipient has
            confirmed by email; every message carries a link that stops it.
            Submissions are forwarded by email and not stored; logs record neither
            their content nor the recipient. Submission rates are limited per
            visitor and per form; attachments are size-limited and executable
            types refused.</li>
```

**Annex 3**, if the provider is external, a row after Hetzner:

```html
              <tr><th scope="row">[provider, address]</th><td>Delivery of form messages by email</td><td>[location]</td></tr>
```

Update the version line from `Version 2026-09-08` to the publication date; until then, `2026-09-24 (draft)`.

- [ ] **Step 3: Terms**

In `public/terms/index.html`, **4. Paid plans → What you get**, add the forms to each plan's list, matching its phrasing: Pro `1 form per site`, Studio `10 forms per site`.

In **7. Acceptable use**, after the containers subsection and before **Abuse reports and takedowns**:

```html
        <h3 id="forms">Forms</h3>
        <p>A form sends what your visitors submit to an address that confirmed it
          wants those messages. You may not use a form to:</p>
        <ul>
          <li>collect passwords, payment-card data or other credentials, or
            imitate another organisation's sign-in or payment page;</li>
          <li>send messages to anyone who has not confirmed the form themselves, or
            to get around a recipient's decision to stop them;</li>
          <li>send advertising or other bulk messages.</li>
        </ul>
        <p>You are responsible for what your forms collect: for telling your
          visitors what happens with their data, and for handling it lawfully once
          it reaches you. We forward submissions and keep no copy. We may pause a
          form immediately when we reasonably believe it breaks these rules; where
          we reasonably can, we tell you why. Pausing a form never deletes it.</p>
```

Update the terms' version or status line the same way as the DPA's.

- [ ] **Step 4: Commit (no push)**

```bash
git -C Sitebin-Website add public/privacy public/dpa public/terms
git -C Sitebin-Website commit -m "content: draft privacy, DPA and terms sections for site forms, for legal review"
```

---

### Task 18: Whole-branch verification

- [ ] **Step 1: Every suite, both editions**

```bash
cd Sitebin
gofmt -l . | grep -v '^vendor/' ; go vet ./... && go vet -tags ee ./...
go test ./... && go test -tags ee ./... && go test -race ./internal/forms/ ./internal/httpapi/
docker build -t sitebin:dev . && docker build --build-arg EDITION=enterprise -t sitebin:dev-ee .
```

Expected: `gofmt` prints nothing, and everything else passes. The Dockerfile runs `go vet` and the suite again.

- [ ] **Step 2: The E2E scripts this feature touches, plus the core ones**

`e2e.ps1`, `spa.ps1`, `paths.ps1`, `mcp.ps1` and `forms.ps1` run against `sitebin:dev`. `accounts.ps1` and `tiers.ps1` run against `sitebin:dev-ee`. Every one must report 0 failed. `paths.ps1` matters here: it is the path-view instance on which `?_site=` must work.

- [ ] **Step 3: Review**

Request a whole-branch code review against the spec (superpowers:requesting-code-review). The review focus comes from this plan's **Review Focus** list, plus:
- no submitted content, filename or recipient in any log line;
- no `internal/` import of `ee/`;
- every `store.Quota{…}` literal passes `Forms`;
- `VerifySolution` always has `DeriveKey`;
- no GET handler changes state.

- [ ] **Step 4: Spec corrections**

Any rule that changed during implementation goes into the spec as a **Corrections (post-implementation)** block, not a silent rewrite. Commit it with `docs:`.

---

## Rollout (with the operator, after Task 18)

This follows the workspace ship order. None of it runs without the operator's go-ahead.

1. **Product repo:** merge `feat/site-forms` into `main` and push.
2. **`app.sitebin.io`** (`/opt/sitebin/`, see the hosted-instance ops memory):
   - `tiers.json`: add `"max_forms": 1` to `pro`, `"max_forms": 10` to `studio`, and `"max_forms": 100` to `unlimited` and `admin`. `drop` and `free` get nothing, which means none.
   - Env: `SITEBIN_FORMS_SMTP_HOST/PORT/USER/PASS/FROM/TLS` for the account the operator chose. The `FROM` domain's SPF and DKIM must cover that server; check it with a test mail and its headers (`dkim=pass`, `spf=pass`).
   - Deploy the image, restart, then verify live:
     - on a Pro site, add a form and confirm it from a real inbox;
     - submit with an attachment and with the captcha, **timing the captcha on a real phone** (if it is well over a second, lower `captchaCost` or the counter range);
     - receive the message, check that Gmail shows the unsubscribe button, stop the form, and see the next submission refused;
     - on a Free site, see "This site's plan includes no forms".
3. **Website repo:**
   - The forms docs and pricing (Task 16) ship after step 2.
   - The legal sections (Task 17) ship only after the operator's legal review, together with the consent-document versions on the SaaS Stack if the review decides existing customers must accept again.
   - Recommended: the forms pages wait for the legal review, so the privacy policy never lags behind a live feature. Whether they wait is the operator's call.
