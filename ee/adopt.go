//go:build ee

package ee

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// Adopting an anonymous drop: a signed-in account on a plan that shares takes
// a drop into the account with the drop's public address and its edit
// password. A drop's ticket links here with ?site=<view url>.
//
// What the page must not become is an oracle for other people's drops. Every
// failure that depends on the drop (unknown, wrong password, throttled
// password check, owned, locked, expired, a refusal from the core) answers
// the same status and the same text, and the plan's limits are shown only
// AFTER the password was proven, to someone who already knows the drop.

const (
	// returnCookie carries the ?site= value of a signed-out visitor across
	// the sign-in. It is signed, short-lived, holds no password, and the way
	// back is always /account/adopt: never a destination the visitor chose.
	returnCookie = "sitebin_return"
	returnTTL    = time.Hour
	// noticeCookie carries a one-time dashboard notice (the adopted site's
	// id, signed). The dashboard shows it once and clears it.
	noticeCookie = "sitebin_notice"
	noticeTTL    = 5 * time.Minute

	adoptSitePath  = "/account/adopt"
	maxAdoptSite   = 2048 // as core's ResolveSite
	maxAdoptReturn = 512  // what a cookie carries across a sign-in
	maxAdoptPass   = 256
	adoptBodyLimit = 16 << 10

	adoptWrong = "URL or password wrong."
)

// The part of the core's ext.OwnershipSites that adoption calls.
type siteAdopter interface {
	ResolveSite(urlOrID string) (viewID string, ok bool)
	CheckEditPassword(viewID, password, ip string) (ok bool, retryAfter time.Duration)
	AdoptSite(viewID, to string, g ext.CreateGrant, by ext.ChangeBy) error
}

type adoptView struct {
	CSRF      string
	Site      string
	Error     string
	Lines     []checkLine
	NeedsPlan bool
}

func (p *provider) renderAdopt(w http.ResponseWriter, status int, v adoptView) {
	p.securityHeaders(w)
	w.WriteHeader(status)
	adoptTmpl.Execute(w, v)
}

// renderRetry is the answer to a lookup that failed: nothing is known, so
// nothing is done.
func (p *provider) renderRetry(w http.ResponseWriter) {
	p.securityHeaders(w)
	w.WriteHeader(http.StatusServiceUnavailable)
	msgTmpl.Execute(w, msgView{Title: "Could not check that right now", Body: "Nothing was changed. Please try again in a minute.", Back: adoptSitePath})
}

// handleAdoptGet shows the form, prefilled from ?site=. A signed-out visitor
// is sent to sign in and brought back here with the site value kept.
func (p *provider) handleAdoptGet(w http.ResponseWriter, r *http.Request) {
	site := r.URL.Query().Get("site")
	acc, ok := p.currentAccount(r)
	if !ok {
		p.setReturn(w, site)
		p.redirect(w, r, "/account/login")
		return
	}
	if len(site) > maxAdoptSite {
		site = ""
	}
	p.renderAdopt(w, http.StatusOK, adoptView{CSRF: p.csrf(acc), Site: site})
}

// handleAdoptPost runs the checks in a fixed order; see the numbered steps.
func (p *provider) handleAdoptPost(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, adoptBodyLimit)
	// 1. CSRF, then the per-address and per-account limits: each attempt is
	// a guess at an edit password, so they come before any lookup.
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !p.limits.allowAdopt(r, acc.ID) {
		p.renderThrottledMessage(w, msgView{Title: "Too many attempts", Body: "Please wait a while before you try again.", Back: "/account"})
		return
	}
	site := strings.TrimSpace(r.PostFormValue("site"))
	password := r.PostFormValue("password")
	shown := site
	if len(shown) > maxAdoptSite {
		shown = ""
	}
	// The one answer for everything about the drop. The password is never
	// rendered back.
	refuse := func() {
		p.renderAdopt(w, http.StatusBadRequest, adoptView{CSRF: p.csrf(acc), Site: shown, Error: adoptWrong})
	}

	// 2. The plan shares at all. This says nothing about any drop.
	allowed, err := p.SharingAllowed(acc.ID)
	if err != nil {
		slog.Error("adopt: could not read the plan", "account", acc.ID, "err", err)
		p.renderRetry(w)
		return
	}
	if !allowed {
		p.renderAdopt(w, http.StatusForbidden, adoptView{NeedsPlan: true})
		return
	}
	ad, ok := p.host.Sites().(siteAdopter)
	if !ok {
		slog.Error("adopt: the site service cannot adopt")
		p.renderRetry(w)
		return
	}

	// 3. The drop, from its address. Junk is "not found".
	if site == "" || password == "" || len(site) > maxAdoptSite || len(password) > maxAdoptPass {
		refuse()
		return
	}
	viewID, found := ad.ResolveSite(site)
	if !found {
		refuse()
		return
	}
	// 4. The edit password, through the core's own limits (same address, same
	// site). A throttled check is a wrong one to the visitor.
	if ok, _ := ad.CheckEditPassword(viewID, password, auth.ClientIP(r)); !ok {
		refuse()
		return
	}
	// 5. Adoptable, read only: anonymous, not locked, not expired. AdoptSite
	// decides again under the site lock.
	info, found := p.host.Sites().Info(viewID)
	if !found || info.Owner != "" || info.Locked != nil || (info.ExpiresAt != nil && !info.ExpiresAt.After(time.Now())) {
		refuse()
		return
	}

	// 6. Does it fit the account's plan? Only now: the password is proven, so
	// the person already knows the drop and may be told what stands in the way.
	lines, err := p.limitsCheck(acc, []string{viewID})
	if err != nil {
		slog.Error("adopt: the limits check failed", "account", acc.ID, "site", viewID, "err", err)
		p.renderRetry(w)
		return
	}
	if failed := failedLines(lines); len(failed) > 0 {
		p.renderAdopt(w, http.StatusForbidden, adoptView{CSRF: p.csrf(acc), Site: shown, Lines: failed})
		return
	}
	grant, err := p.grantForAccount(acc)
	if err != nil {
		var ce *ext.CreateError
		if errors.As(err, &ce) {
			p.renderAdopt(w, http.StatusForbidden, adoptView{CSRF: p.csrf(acc), Site: shown, Lines: []checkLine{{Label: "Plan", Detail: ce.Msg}}})
			return
		}
		slog.Error("adopt: could not read the plan's caps", "account", acc.ID, "err", err)
		p.renderRetry(w)
		return
	}

	// 7. The core stamps the drop exactly as a creation would, under the site
	// lock, and refuses an owned, locked or expired one. Whatever it says is
	// "wrong" to the visitor; the operator reads the reason in the log.
	by := ext.ChangeBy{Account: acc.ID, IP: auth.ClientIP(r), UserAgent: r.UserAgent()}
	if err := ad.AdoptSite(viewID, acc.ID, grant, by); err != nil {
		if errors.Is(err, ext.ErrNotAdoptable) || errors.Is(err, ext.ErrSiteLocked) || errors.Is(err, ext.ErrSiteGone) {
			slog.Info("adopt: refused by the core", "account", acc.ID, "site", viewID, "err", err)
		} else {
			slog.Error("adopt: the core failed", "account", acc.ID, "site", viewID, "err", err)
		}
		refuse()
		return
	}

	// 8. The marker. meta.json is the truth: the owner filter of every
	// account page reads it, so the site is already the account's and a
	// failed marker costs nothing visible. It is logged loudly, the visitor
	// is told it worked (it did), and the background loop that reconciles
	// markers with meta.json (Task 10) links it later.
	if err := p.accounts.LinkSite(acc, viewID); err != nil {
		slog.Error("adopt: the site is adopted but its marker could not be written; meta.json owns it and the reconciliation loop will link it",
			"account", acc.ID, "site", viewID, "err", err)
	}

	// 9. The account's own log.
	e := accountEntry(r, provenance.ActionAdopt, provenance.SurfaceDashboard, provenance.AuthSession)
	e.Account = acc.ID
	e.Site = viewID
	e.Detail = "adopted an anonymous drop"
	p.RecordAccountProvenance(acc.ID, e)

	http.SetCookie(w, &http.Cookie{
		Name: noticeCookie, Value: p.signer("notice").Sign(viewID, time.Now(), noticeTTL),
		Path: "/account", MaxAge: int(noticeTTL / time.Second),
		HttpOnly: true, Secure: !p.host.HTTPOnly(), SameSite: http.SameSiteLaxMode,
	})
	p.redirect(w, r, "/account")
}

// ---- the way back from sign-in ----

// setReturn remembers a signed-out visitor's ?site= across the sign-in. A
// value too long for a cookie is dropped: the visitor lands on the dashboard,
// whose adopt card takes the address by hand.
func (p *provider) setReturn(w http.ResponseWriter, site string) {
	if site == "" || len(site) > maxAdoptReturn {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: returnCookie, Value: p.signer("return").Sign(site, time.Now(), returnTTL),
		Path: "/account", MaxAge: int(returnTTL / time.Second),
		HttpOnly: true, Secure: !p.host.HTTPOnly(), SameSite: http.SameSiteLaxMode,
	})
}

// landing is where a completed sign-in goes: back to the adopt page with the
// site value the visitor came with, else the dashboard. The path is fixed;
// only the (signed, escaped) site value travels, so there is nothing to
// redirect to that the visitor chose.
func (p *provider) landing(w http.ResponseWriter, r *http.Request) {
	dest := "/account"
	if c, err := r.Cookie(returnCookie); err == nil {
		http.SetCookie(w, &http.Cookie{Name: returnCookie, Value: "", Path: "/account", MaxAge: -1})
		if site, ok := p.signer("return").Parse(c.Value, time.Now()); ok && site != "" {
			dest = adoptSitePath + "?site=" + url.QueryEscape(site)
		}
	}
	p.redirect(w, r, dest)
}

// takeNotice returns the dashboard's one-time notice, if the visitor carries
// a signed one for a site this account owns, and clears the cookie either way.
func (p *provider) takeNotice(w http.ResponseWriter, r *http.Request, acc *account.Account) string {
	c, err := r.Cookie(noticeCookie)
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{Name: noticeCookie, Value: "", Path: "/account", MaxAge: -1})
	viewID, ok := p.signer("notice").Parse(c.Value, time.Now())
	if !ok {
		return ""
	}
	info, found := p.host.Sites().Info(viewID)
	if !found || info.Owner != acc.ID {
		return ""
	}
	return adoptedNotice(siteLabel(info))
}

// adoptedNotice is the text of the notice for a freshly adopted site.
func adoptedNotice(label string) string {
	return fmt.Sprintf("Adopted %s. It is a site of this account now; its address and its edit password work as before.", label)
}
