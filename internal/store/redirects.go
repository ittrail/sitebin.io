package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Domain redirects and the site's generated address.
//
// A verified custom domain can redirect to another verified custom domain of
// the same site, and the generated address (<id>.<view domain>, /v/<id>/) can
// serve, be off, or redirect to one. authz answers every rule before anything
// is served. What is stored is the owner's choice; what is IN EFFECT is decided
// against the domains attached right now (DomainRedirect, ViewRule), so a
// domain the sweep detaches turns a rule that pointed at it back into serving
// the site rather than into a dead end. See
// docs/superpowers/specs/2026-10-16-domain-redirects-design.md.

// ErrBadRedirect is a rule the setters refuse. Its message says why, so every
// surface can show it to the owner as it is.
var ErrBadRedirect = errors.New("invalid redirect")

// The generated address's modes. ViewServe is never stored: a nil
// Meta.ViewAddress is "serve", which is what every site written before
// redirects existed has.
const (
	ViewServe    = "serve"
	ViewOff      = "off"
	ViewRedirect = "redirect"
)

// RedirectCodes are the status codes a redirect may answer with.
var RedirectCodes = []int{301, 302, 307, 308}

// DefaultRedirectCode is what a rule set without a code answers with.
const DefaultRedirectCode = 301

// Redirect sends a custom domain's visitors to another of the site's custom
// domains, keeping the path and query.
type Redirect struct {
	To   string `json:"to"`
	Code int    `json:"code"`
}

// ViewAddress is what the generated address does instead of serving the site:
// Mode ViewOff (not served) or ViewRedirect (To, Code).
type ViewAddress struct {
	Mode string `json:"mode"`
	To   string `json:"to,omitempty"`
	Code int    `json:"code,omitempty"`
}

// DomainRedirect is the redirect host answers with now, if any: a stored rule
// whose domain is attached and whose target serves the site.
func (m Meta) DomainRedirect(host string) (Redirect, bool) {
	d := strings.ToLower(strings.TrimSuffix(host, "."))
	if !slices.Contains(m.CustomDomains, d) {
		return Redirect{}, false
	}
	r, ok := m.DomainRedirects[d]
	if !ok || !m.followable(d, r) {
		return Redirect{}, false
	}
	return Redirect{To: r.To, Code: codeOrDefault(r.Code)}, true
}

// ViewRule is what the generated address does now: ViewServe unless a stored
// rule is in effect. Off holds only while a custom domain serves the site — if
// the sweep detaches the last one, the generated address serves again rather
// than leaving the site with no address at all. On a container site it is the
// rule of the site's own address (addresses.go).
func (m Meta) ViewRule() ViewAddress {
	serve := ViewAddress{Mode: ViewServe}
	if m.Mode == ModeContainer {
		return m.LabelRule(m.ID)
	}
	va := m.ViewAddress
	if va == nil {
		return serve
	}
	switch va.Mode {
	case ViewOff:
		if len(m.ServingDomains()) > 0 {
			return ViewAddress{Mode: ViewOff}
		}
	case ViewRedirect:
		if m.serves(va.To) {
			return ViewAddress{Mode: ViewRedirect, To: va.To, Code: codeOrDefault(va.Code)}
		}
	}
	return serve
}

// ServingDomains are the attached custom domains that serve the site rather
// than redirect, in the order they were attached.
func (m Meta) ServingDomains() []string {
	var out []string
	for _, d := range m.CustomDomains {
		if m.serves(d) {
			out = append(out, d)
		}
	}
	return out
}

// serves reports whether d is an attached custom domain with no redirect in
// effect — and, on a container site, a target its compose file declares.
func (m Meta) serves(d string) bool {
	if !slices.Contains(m.CustomDomains, d) {
		return false
	}
	if r, ok := m.DomainRedirects[d]; ok && m.followable(d, r) {
		return false
	}
	return m.Mode != ModeContainer || m.HasEndpoint(m.DomainTargets[d])
}

// followable reports whether from's stored rule can be followed: its target
// is attached, has no rule of its own, and — on a container site — serves a
// port the compose file declares. The setters never write a chain; this
// keeps one that the sweep's detachments could leave from ever being
// followed.
func (m Meta) followable(from string, r Redirect) bool {
	if r.To == from || !slices.Contains(m.CustomDomains, r.To) {
		return false
	}
	if _, chained := m.DomainRedirects[r.To]; chained {
		return false
	}
	return m.Mode != ModeContainer || m.HasEndpoint(m.DomainTargets[r.To])
}

// redirectSource names what redirects to d — another domain, or the generated
// address — or "" when nothing does. Stored rules count, in effect or not.
func (m Meta) redirectSource(d string) string {
	for from, r := range m.DomainRedirects {
		if r.To == d {
			return from
		}
	}
	if va := m.ViewAddress; m.Mode != ModeContainer && va != nil && va.Mode == ViewRedirect && va.To == d {
		return "the site's generated address"
	}
	for _, a := range m.Addresses {
		if a.Rule != nil && a.Rule.Mode == ViewRedirect && a.Rule.To == d {
			return "the generated address of " + a.Endpoint
		}
	}
	return ""
}

// dropAddressRules forgets every rule a removed domain was part of: its own
// redirect, redirects to it (those domains serve the site again), a
// generated-address redirect to it, and an off that no serving domain is left
// to justify. RemoveDomain calls it; the sweep's detachment does not, because
// detaching is not the owner's decision.
func (m *Meta) dropAddressRules(d string) {
	delete(m.DomainRedirects, d)
	for from, r := range m.DomainRedirects {
		if r.To == d {
			delete(m.DomainRedirects, from)
		}
	}
	if len(m.DomainRedirects) == 0 {
		m.DomainRedirects = nil
	}
	if va := m.ViewAddress; va != nil {
		if (va.Mode == ViewRedirect && va.To == d) || (va.Mode == ViewOff && len(m.ServingDomains()) == 0) {
			m.ViewAddress = nil
		}
	}
	// A container site's: the domain's target, and the address rules it was
	// part of — a redirect to it, an off it alone justified.
	delete(m.DomainTargets, d)
	if len(m.DomainTargets) == 0 {
		m.DomainTargets = nil
	}
	for i := range m.Addresses {
		a := &m.Addresses[i]
		if r := a.Rule; r != nil && ((r.Mode == ViewRedirect && r.To == d) || (r.Mode == ViewOff && !m.servesEndpoint(a.Endpoint))) {
			a.Rule = nil
		}
	}
}

func codeOrDefault(code int) int {
	if code == 0 {
		return DefaultRedirectCode
	}
	return code
}

// redirectTarget validates a target and its code: a domain name and one of
// RedirectCodes (0 is the default).
func redirectTarget(to string, code int) (string, int, error) {
	t, err := normalizeDomain(to)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %q is not a domain name — the target is one of this site's custom domains", ErrBadRedirect, to)
	}
	code = codeOrDefault(code)
	if !slices.Contains(RedirectCodes, code) {
		return "", 0, fmt.Errorf("%w: code %d is not a redirect — use 301, 302, 307 or 308", ErrBadRedirect, code)
	}
	return t, code, nil
}

// checkTarget refuses a target that does not serve the site: not attached
// here, or redirecting itself (one hop, never a chain).
func checkTarget(m *Meta, t string) error {
	if !slices.Contains(m.CustomDomains, t) {
		return fmt.Errorf("%w: %s is not an attached custom domain of this site", ErrBadRedirect, t)
	}
	if _, ok := m.DomainRedirects[t]; ok {
		return fmt.Errorf("%w: %s redirects to %s itself — point at %[3]s instead", ErrBadRedirect, t, m.DomainRedirects[t].To)
	}
	if m.Mode == ModeContainer && !m.HasEndpoint(m.DomainTargets[t]) {
		return fmt.Errorf("%w: %s serves none of this app's HTTP ports — point it at one first", ErrBadRedirect, t)
	}
	return nil
}

// ruleTarget validates a generated address's mode and, for a redirect, its
// target and code (0: 301).
func ruleTarget(mode, to string, code int) (string, int, error) {
	switch mode {
	case ViewServe, "", ViewOff:
		return "", 0, nil
	case ViewRedirect:
		if strings.TrimSpace(to) == "" {
			return "", 0, fmt.Errorf("%w: a redirect needs a target — one of this site's custom domains", ErrBadRedirect)
		}
		return redirectTarget(to, code)
	default:
		return "", 0, fmt.Errorf("%w: mode %q — use serve, off or redirect", ErrBadRedirect, mode)
	}
}

// SetDomainRedirect makes the attached custom domain redirect to another of
// the site's attached custom domains with code (0: 301), keeping path and
// query. An empty to makes the domain serve the site again.
func (s *Store) SetDomainRedirect(site *Site, domain, to string, code int) error {
	d, err := normalizeDomain(domain)
	if err != nil {
		return fmt.Errorf("%w: %q is not an attached custom domain of this site", ErrNotFound, domain)
	}
	var target string
	if strings.TrimSpace(to) != "" {
		if target, code, err = redirectTarget(to, code); err != nil {
			return err
		}
	}
	return s.Update(site, func(m *Meta) error {
		if target == "" {
			if _, ok := m.DomainRedirects[d]; !ok && !slices.Contains(m.CustomDomains, d) {
				return fmt.Errorf("%w: %s is not an attached custom domain of this site", ErrNotFound, d)
			}
			delete(m.DomainRedirects, d)
			if len(m.DomainRedirects) == 0 {
				m.DomainRedirects = nil
			}
			return nil
		}
		if !slices.Contains(m.CustomDomains, d) {
			return fmt.Errorf("%w: %s is not an attached custom domain of this site — a domain redirects once it is verified", ErrNotFound, d)
		}
		if target == d {
			return fmt.Errorf("%w: %s cannot redirect to itself", ErrBadRedirect, d)
		}
		if err := checkTarget(m, target); err != nil {
			return err
		}
		if src := m.redirectSource(d); src != "" {
			return fmt.Errorf("%w: %s is where %s redirects to — change that first, or it would redirect twice", ErrBadRedirect, d, src)
		}
		if m.DomainRedirects == nil {
			m.DomainRedirects = map[string]Redirect{}
		}
		m.DomainRedirects[d] = Redirect{To: target, Code: code}
		return nil
	})
}

// SetViewAddress sets what the generated address does: ViewServe (or ""),
// ViewOff — refused while no custom domain serves the site — or ViewRedirect
// to an attached custom domain that serves it, with code (0: 301).
func (s *Store) SetViewAddress(site *Site, mode, to string, code int) error {
	target, code, err := ruleTarget(mode, to, code)
	if err != nil {
		return err
	}
	return s.Update(site, func(m *Meta) error {
		if m.Mode == ModeContainer {
			return fmt.Errorf("%w: a container site has one generated address per HTTP port — set it with SetAddressRule", ErrBadRedirect)
		}
		switch mode {
		case ViewOff:
			if len(m.ServingDomains()) == 0 {
				return fmt.Errorf("%w: no custom domain serves this site yet — switching its generated address off would take it offline", ErrBadRedirect)
			}
			m.ViewAddress = &ViewAddress{Mode: ViewOff}
		case ViewRedirect:
			if err := checkTarget(m, target); err != nil {
				return err
			}
			m.ViewAddress = &ViewAddress{Mode: ViewRedirect, To: target, Code: code}
		default:
			m.ViewAddress = nil
		}
		return nil
	})
}
