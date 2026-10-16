package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ittrail/sitebin.io/internal/store"
)

// Domain redirects and the generated address: authz's answer, the owner's
// routes, and the payload. The rules themselves are the store's
// (store/redirects.go); everything here only asks them. See
// docs/superpowers/specs/2026-10-16-domain-redirects-design.md.

// errContainerAddresses answers an address rule on a container site.
const errContainerAddresses = "a container site's addresses are declared in " + store.ComposeFile + ", which maps each of them to a service; they cannot redirect"

// addressRule is what the address a content request came in on does: the
// generated address's rule (its subdomain or its /v/<id>/ path view), or a
// custom domain's redirect. Mode ViewServe: serve the site.
func (a *API) addressRule(site *store.Site, host string, pathMode bool) store.ViewAddress {
	if pathMode || a.isViewHost(host) {
		return site.Meta.ViewRule()
	}
	if r, ok := site.Meta.DomainRedirect(strings.ToLower(hostWithoutPort(host))); ok {
		return store.ViewAddress{Mode: store.ViewRedirect, To: r.To, Code: r.Code}
	}
	return store.ViewAddress{Mode: store.ViewServe}
}

// isViewHost reports whether host is a site's generated address — a label of
// the view domain, which is how siteByHost resolves it.
func (a *API) isViewHost(host string) bool {
	return strings.HasSuffix(strings.ToLower(hostWithoutPort(host)), "."+a.cfg.ViewDomain)
}

// redirectAddress answers a redirecting address: the rule's code, and the
// target with the request's own path and query. A permanent redirect is
// cached for an hour, not for ever, so an owner who changes it is not
// overruled by every visitor's cache; a temporary one is not cached.
func (a *API) redirectAddress(w http.ResponseWriter, r *http.Request, site *store.Site, rule store.ViewAddress, pathMode bool) {
	uri := r.Header.Get("X-Forwarded-Uri")
	if pathMode {
		uri = strings.TrimPrefix(uri, "/v/"+site.ViewID)
	}
	// The target is fixed; the path only follows it. Anything that is not a
	// plain origin-form path — "@evil", "//evil", an absolute URL, a line
	// break — would let the client move the redirect elsewhere, and becomes
	// the target's root.
	if !strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, "//") || strings.ContainsAny(uri, "\\\r\n") {
		if strings.HasPrefix(uri, "?") {
			uri = "/" + uri
		} else {
			uri = "/"
		}
	}
	if rule.Code == http.StatusMovedPermanently || rule.Code == http.StatusPermanentRedirect {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.Header().Set("Location", a.cfg.SiteURL(rule.To)+uri)
	w.WriteHeader(rule.Code)
}

// servingSiteByHost is siteByHost for the backend's own routes on a site's
// origin (forms, the unlock form): an address that does not serve the site —
// a redirecting domain, a generated address that is off or redirects —
// answers nothing of it, the same as an address that belongs to no site.
func (a *API) servingSiteByHost(host string) (*store.Site, error) {
	site, err := a.siteByHost(host)
	if err != nil {
		return nil, err
	}
	if a.addressRule(site, host, false).Mode != store.ViewServe {
		return nil, store.ErrNotFound
	}
	return site, nil
}

// servesPathView reports whether the site's generated address serves it, for
// the routes a path view addresses by id rather than by host.
func servesPathView(site *store.Site) bool {
	return site.Meta.ViewRule().Mode == store.ViewServe
}

// changeDomainRedirect is the one path an owner's request takes to set or
// clear a custom domain's redirect, from the API and MCP alike. An empty to
// makes the domain serve the site again.
func (a *API) changeDomainRedirect(site *store.Site, domain, to string, code int) error {
	if site.Meta.Mode == store.ModeContainer {
		return &apiError{409, errContainerAddresses}
	}
	return addressError(a.st.SetDomainRedirect(site, domain, to, code))
}

// changeViewAddress is the one path an owner's request takes to set what the
// generated address does, from the API and MCP alike.
func (a *API) changeViewAddress(site *store.Site, mode, to string, code int) error {
	if site.Meta.Mode == store.ModeContainer {
		return &apiError{409, errContainerAddresses}
	}
	return addressError(a.st.SetViewAddress(site, mode, to, code))
}

// addressError turns the store's refusals into answers that say why.
func addressError(err error) error {
	reason := func(sentinel error) string {
		msg := strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
		return msg
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrBadRedirect):
		return &apiError{400, reason(store.ErrBadRedirect)}
	case errors.Is(err, store.ErrNotFound) && err != store.ErrNotFound:
		return &apiError{404, reason(store.ErrNotFound)}
	}
	return err
}

// addressDetail is the provenance detail of an address change.
func addressDetail(from, mode, to string, code int) string {
	switch {
	case mode == store.ViewOff:
		return from + " off"
	case to == "":
		return from + " serves"
	default:
		return fmt.Sprintf("%s → %s %d", from, strings.ToLower(strings.TrimSpace(to)), code)
	}
}

type redirectBody struct {
	Mode string `json:"mode"`
	To   string `json:"to"`
	Code int    `json:"code"`
}

func decodeRedirectBody(w http.ResponseWriter, r *http.Request) (redirectBody, bool) {
	var body redirectBody
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return body, false
	}
	return body, true
}

// putDomainRedirect is PUT /api/sites/{editID}/domains/{domain}/redirect,
// {"to": "example.com", "code": 301}.
func (a *API) putDomainRedirect(w http.ResponseWriter, r *http.Request, site *store.Site) {
	body, ok := decodeRedirectBody(w, r)
	if !ok || strings.TrimSpace(body.To) == "" {
		writeError(w, 400, `body must be {"to": "example.com", "code": 301}; DELETE the redirect to serve the site again`)
		return
	}
	domain := r.PathValue("domain")
	if err := a.changeDomainRedirect(site, domain, body.To, body.Code); err != nil {
		respondErr(w, err)
		return
	}
	code := body.Code
	if code == 0 {
		code = store.DefaultRedirectCode
	}
	noteOf(r).detail = addressDetail(strings.ToLower(domain), "", body.To, code)
	writeJSON(w, 200, a.sitePayload(site))
}

// deleteDomainRedirect is DELETE /api/sites/{editID}/domains/{domain}/redirect:
// the domain serves the site again.
func (a *API) deleteDomainRedirect(w http.ResponseWriter, r *http.Request, site *store.Site) {
	domain := r.PathValue("domain")
	if err := a.changeDomainRedirect(site, domain, "", 0); err != nil {
		respondErr(w, err)
		return
	}
	noteOf(r).detail = addressDetail(domain, "", "", 0)
	writeJSON(w, 200, a.sitePayload(site))
}

// putViewAddress is PUT /api/sites/{editID}/view-address,
// {"mode": "serve" | "off" | "redirect", "to": "example.com", "code": 301}.
func (a *API) putViewAddress(w http.ResponseWriter, r *http.Request, site *store.Site) {
	body, ok := decodeRedirectBody(w, r)
	if !ok || body.Mode == "" {
		writeError(w, 400, `body must be {"mode": "serve"}, {"mode": "off"} or {"mode": "redirect", "to": "example.com", "code": 301}`)
		return
	}
	if err := a.changeViewAddress(site, body.Mode, body.To, body.Code); err != nil {
		respondErr(w, err)
		return
	}
	to, code := "", 0
	if va := site.Meta.ViewAddress; va != nil {
		to, code = va.To, va.Code
	}
	noteOf(r).detail = addressDetail("generated address", body.Mode, to, code)
	writeJSON(w, 200, a.sitePayload(site))
}

// redirectsPayload is the site's stored domain redirects, each with whether it
// is in effect right now.
func redirectsPayload(m store.Meta) map[string]any {
	out := map[string]any{}
	for d, r := range m.DomainRedirects {
		_, inEffect := m.DomainRedirect(d)
		out[d] = map[string]any{"to": r.To, "code": r.Code, "in_effect": inEffect}
	}
	return out
}

// viewAddressPayload is the generated address's stored mode, and whether it
// is in effect right now.
func viewAddressPayload(m store.Meta) map[string]any {
	stored := store.ViewAddress{Mode: store.ViewServe}
	if m.ViewAddress != nil {
		stored = *m.ViewAddress
	}
	return map[string]any{"mode": stored.Mode, "to": stored.To, "code": stored.Code, "in_effect": m.ViewRule().Mode == stored.Mode}
}

// primaryURL is where the site is served first: its generated address while
// that serves, else the domain it redirects to, else its first custom domain
// that serves.
func (a *API) primaryURL(site *store.Site) string {
	switch rule := site.Meta.ViewRule(); rule.Mode {
	case store.ViewRedirect:
		return a.cfg.SiteURL(rule.To)
	case store.ViewOff:
		if serving := site.Meta.ServingDomains(); len(serving) > 0 {
			return a.cfg.SiteURL(serving[0])
		}
	}
	return a.cfg.ViewURL(site.ViewID)
}
