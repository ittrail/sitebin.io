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
