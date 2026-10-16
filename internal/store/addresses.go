package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ittrail/sitebin.io/internal/ids"
)

// Container addresses.
//
// A container site's compose file lists the ports its services speak HTTP on
// (http:); each one is an endpoint, "service:port". Every endpoint gets a
// generated address here: the first gets the site's own address (its view
// id), every further one a random label of the same shape — as unguessable as
// a view id, so an admin port is not found by reading the app's address —
// indexed in address-index/ so a request finds its site. Which port a custom
// domain serves is DomainTargets. Each address serves, is off, or redirects,
// with the file site's rules (redirects.go). The compose file never names an
// address, so changing one never restarts the project. See
// docs/superpowers/specs/2026-10-16-container-addresses-design.md.

// ErrBadTarget is a domain target the setters refuse. Its message says why.
var ErrBadTarget = errors.New("invalid target")

// Address is one HTTP port of a container site and its generated address.
type Address struct {
	// Endpoint is the port, as "service:port".
	Endpoint string `json:"endpoint"`
	// Label is the address's host label under the view domain: the site's
	// view id for its own address, else a random label of the same shape.
	Label string `json:"label"`
	// Rule is what the address does instead of serving its endpoint: off or
	// a redirect. Nil serves. What is in effect is Meta.AddressRule.
	Rule *ViewAddress `json:"rule,omitempty"`
}

// ByAddress loads the site a random generated label belongs to. The index is
// only the way there: a label its site no longer holds — a link a crash left
// behind — answers nothing.
func (s *Store) ByAddress(label string) (*Site, error) {
	if !ids.ValidID(label) {
		return nil, ErrNotFound
	}
	site, err := s.load(filepath.Join(s.addressIndexDir(), label))
	if err != nil {
		return nil, err
	}
	if _, ok := site.Meta.AddressByLabel(label); !ok {
		return nil, ErrNotFound
	}
	return site, nil
}

// SyncAddresses makes a container site's generated addresses match the HTTP
// ports its compose file declares (endpoints, in file order). An endpoint
// keeps its address and rule as long as it is declared; a new one gets the
// site's own address when no endpoint holds it — so a renamed service keeps
// it — else a fresh random label; a port no longer declared loses its
// address. Custom domains pointed at a vanished port keep their target (and
// answer "nothing here"): moving one to another service could put the wrong
// app under it. A domain with no target yet gets the main endpoint. The
// runtime calls this after every compose file that parses.
func (s *Store) SyncAddresses(site *Site, endpoints []string) error {
	var dropped []string
	err := s.Update(site, func(m *Meta) error {
		dropped = nil
		if m.Mode != ModeContainer {
			return nil // left the mode while the file was being read
		}
		kept := map[string]Address{}
		for _, a := range m.Addresses {
			kept[a.Endpoint] = a
		}
		next := make([]Address, 0, len(endpoints))
		for _, e := range endpoints {
			if _, _, ok := SplitEndpoint(e); !ok || slices.ContainsFunc(next, func(a Address) bool { return a.Endpoint == e }) {
				continue
			}
			if a, ok := kept[e]; ok {
				next = append(next, a)
				delete(kept, e)
				continue
			}
			next = append(next, Address{Endpoint: e})
		}
		for _, a := range kept {
			if a.Label != m.ID {
				dropped = append(dropped, a.Label)
			}
		}
		own := slices.ContainsFunc(next, func(a Address) bool { return a.Label == m.ID })
		for i := range next {
			switch {
			case next[i].Label != "":
			case !own:
				next[i].Label, own = m.ID, true
			default:
				l, err := s.freeLabel(next)
				if err != nil {
					return err
				}
				next[i].Label = l
			}
		}
		m.Addresses = nil
		if len(next) > 0 {
			m.Addresses = next
		}
		if main := m.mainEndpoint(); main != "" {
			for _, d := range m.claimedDomains() {
				if m.DomainTargets[d] == "" {
					if m.DomainTargets == nil {
						m.DomainTargets = map[string]string{}
					}
					m.DomainTargets[d] = main
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, l := range dropped {
		os.Remove(filepath.Join(s.addressIndexDir(), l))
	}
	// Every random label is indexed: the new ones, and any a crash lost.
	for _, a := range site.Meta.Addresses {
		if a.Label == site.ViewID {
			continue
		}
		link := filepath.Join(s.addressIndexDir(), a.Label)
		if linkExists(link) {
			continue
		}
		if err := makeLink(link, filepath.Join("..", sitesDirName, site.ViewID), site.dir); err != nil {
			return fmt.Errorf("index address: %w", err)
		}
	}
	return nil
}

// freeLabel draws a random label no site and no other address holds.
func (s *Store) freeLabel(taken []Address) (string, error) {
	for range 8 {
		l := ids.New()
		if slices.ContainsFunc(taken, func(a Address) bool { return a.Label == l }) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(s.sitesDir(), l)); err == nil {
			continue
		}
		if linkExists(filepath.Join(s.addressIndexDir(), l)) {
			continue
		}
		return l, nil
	}
	return "", errors.New("no free address label")
}

// ClearAddresses forgets a site's generated addresses and domain targets, for
// a site leaving container mode: its domains stay, and serve its files.
func (s *Store) ClearAddresses(site *Site) error {
	var old Meta
	err := s.Update(site, func(m *Meta) error {
		old = *m
		m.Addresses = nil
		m.DomainTargets = nil
		return nil
	})
	if err != nil {
		return err
	}
	s.unlinkAddresses(old)
	return nil
}

// unlinkAddresses removes the index entries of m's random labels.
func (s *Store) unlinkAddresses(m Meta) {
	for _, a := range m.Addresses {
		if a.Label != m.ID && a.Label != "" {
			os.Remove(filepath.Join(s.addressIndexDir(), a.Label))
		}
	}
}

// SetDomainTarget points a custom domain of a container site — attached or
// still pending — at one of the HTTP ports its compose file declares.
func (s *Store) SetDomainTarget(site *Site, domain, endpoint string) error {
	d, err := normalizeDomain(domain)
	if err != nil {
		return fmt.Errorf("%w: %q is not a custom domain of this site", ErrNotFound, domain)
	}
	return s.Update(site, func(m *Meta) error {
		if m.Mode != ModeContainer {
			return fmt.Errorf("%w: only a container site's domains are pointed at a port", ErrBadTarget)
		}
		if !slices.Contains(m.claimedDomains(), d) {
			return fmt.Errorf("%w: %s is not a custom domain of this site", ErrNotFound, d)
		}
		if !m.HasEndpoint(endpoint) {
			return m.UnknownEndpoint(endpoint)
		}
		if m.DomainTargets == nil {
			m.DomainTargets = map[string]string{}
		}
		m.DomainTargets[d] = endpoint
		return nil
	})
}

// DefaultDomainTarget gives a container site's custom domain the main
// endpoint when it has no target yet; a domain that has one keeps it.
func (s *Store) DefaultDomainTarget(site *Site, domain string) error {
	d, err := normalizeDomain(domain)
	if err != nil {
		return nil
	}
	if site.Meta.Mode != ModeContainer || site.Meta.DomainTargets[d] != "" || site.Meta.mainEndpoint() == "" {
		return nil
	}
	return s.Update(site, func(m *Meta) error {
		main := m.mainEndpoint()
		if m.Mode != ModeContainer || m.DomainTargets[d] != "" || main == "" || !slices.Contains(m.claimedDomains(), d) {
			return nil
		}
		if m.DomainTargets == nil {
			m.DomainTargets = map[string]string{}
		}
		m.DomainTargets[d] = main
		return nil
	})
}

// SetAddressRule sets what the generated address of a container site's
// endpoint does (endpoint "" is the site's own address): ViewServe (or ""),
// ViewOff — refused while no custom domain serves that endpoint — or
// ViewRedirect to an attached custom domain that serves, with code (0: 301).
func (s *Store) SetAddressRule(site *Site, endpoint, mode, to string, code int) error {
	target, code, err := ruleTarget(mode, to, code)
	if err != nil {
		return err
	}
	return s.Update(site, func(m *Meta) error {
		i := slices.IndexFunc(m.Addresses, func(a Address) bool {
			if endpoint == "" {
				return a.Label == m.ID
			}
			return a.Endpoint == endpoint
		})
		if i < 0 {
			if endpoint == "" {
				return fmt.Errorf("%w: this app has no address yet — list its HTTP ports under http: in %s", ErrNotFound, ComposeFile)
			}
			return m.UnknownEndpoint(endpoint)
		}
		a := &m.Addresses[i]
		switch mode {
		case ViewOff:
			if !m.servesEndpoint(a.Endpoint) {
				return fmt.Errorf("%w: no custom domain serves %s yet — switching its generated address off would take it offline", ErrBadRedirect, a.Endpoint)
			}
			a.Rule = &ViewAddress{Mode: ViewOff}
		case ViewRedirect:
			if err := checkTarget(m, target); err != nil {
				return err
			}
			a.Rule = &ViewAddress{Mode: ViewRedirect, To: target, Code: code}
		default:
			a.Rule = nil
		}
		return nil
	})
}

// UnknownEndpoint is the refusal of a port the compose file does not declare.
func (m Meta) UnknownEndpoint(endpoint string) error {
	if len(m.Addresses) == 0 {
		return fmt.Errorf("%w: %q is not one of this app's HTTP ports — it declares none yet (http: in %s)", ErrBadTarget, endpoint, ComposeFile)
	}
	return fmt.Errorf("%w: %q is not one of this app's HTTP ports (%s)", ErrBadTarget, endpoint, strings.Join(m.Endpoints(), ", "))
}

// Endpoints are the container site's HTTP ports, in its compose file's order.
func (m Meta) Endpoints() []string {
	out := make([]string, 0, len(m.Addresses))
	for _, a := range m.Addresses {
		out = append(out, a.Endpoint)
	}
	return out
}

// HasEndpoint reports whether the compose file declares the endpoint.
func (m Meta) HasEndpoint(endpoint string) bool {
	return endpoint != "" && slices.ContainsFunc(m.Addresses, func(a Address) bool { return a.Endpoint == endpoint })
}

// AddressByLabel finds the generated address with that host label.
func (m Meta) AddressByLabel(label string) (Address, bool) {
	label = strings.ToLower(label)
	for _, a := range m.Addresses {
		if a.Label == label {
			return a, true
		}
	}
	return Address{}, false
}

// DomainEndpoint is the port an attached custom domain of a container site
// serves now: its target, while the compose file declares it; "" otherwise.
func (m Meta) DomainEndpoint(domain string) string {
	d := strings.ToLower(strings.TrimSuffix(domain, "."))
	if !slices.Contains(m.CustomDomains, d) {
		return ""
	}
	if e := m.DomainTargets[d]; m.HasEndpoint(e) {
		return e
	}
	return ""
}

// mainEndpoint is the port a domain serves when nobody chose one: the one the
// site's own address serves, else the first.
func (m Meta) mainEndpoint() string {
	if a, ok := m.AddressByLabel(m.ID); ok {
		return a.Endpoint
	}
	if len(m.Addresses) > 0 {
		return m.Addresses[0].Endpoint
	}
	return ""
}

// claimedDomains are the site's custom domains, attached and pending.
func (m Meta) claimedDomains() []string {
	out := slices.Clone(m.CustomDomains)
	for _, c := range m.DomainClaims {
		if !slices.Contains(out, c.Domain) {
			out = append(out, c.Domain)
		}
	}
	return out
}

// servesEndpoint reports whether a custom domain serves the endpoint now.
func (m Meta) servesEndpoint(endpoint string) bool {
	for _, d := range m.CustomDomains {
		if m.DomainTargets[d] == endpoint && m.serves(d) {
			return true
		}
	}
	return false
}

// AddressRule is what a generated address of a container site does now: its
// stored rule while that is in effect — off while a custom domain serves the
// same endpoint, a redirect while its target serves — else ViewServe.
func (m Meta) AddressRule(a Address) ViewAddress {
	serve := ViewAddress{Mode: ViewServe}
	if a.Rule == nil {
		return serve
	}
	switch a.Rule.Mode {
	case ViewOff:
		if m.servesEndpoint(a.Endpoint) {
			return ViewAddress{Mode: ViewOff}
		}
	case ViewRedirect:
		if m.serves(a.Rule.To) {
			return ViewAddress{Mode: ViewRedirect, To: a.Rule.To, Code: codeOrDefault(a.Rule.Code)}
		}
	}
	return serve
}

// LabelRule is AddressRule for the address with that label; a label no
// address holds serves, which for a container site means "nothing here".
func (m Meta) LabelRule(label string) ViewAddress {
	if a, ok := m.AddressByLabel(label); ok {
		return m.AddressRule(a)
	}
	return ViewAddress{Mode: ViewServe}
}
