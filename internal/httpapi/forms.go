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
	if a.forms == nil {
		return formsJSON{}, errFormsOff
	}
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
