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

const (
	noForm          = "There is no form at this address."
	tooManyMessages = "Too many messages were sent through this form. Please try again later."
)

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
	// The per-IP bucket is spent first, before any body is read. The per-form
	// one is spent only on a message about to be mailed (below): otherwise a
	// botnet's honeypot hits and failed captchas would lock out real people.
	if !a.forms.perIP.Allow(clientIP(r)) {
		fail(429, tooManyMessages)
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
	if !a.forms.perForm.Allow(site.ViewID + "/" + f.Key) {
		fail(429, tooManyMessages)
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
