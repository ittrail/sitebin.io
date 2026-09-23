package store

import (
	"errors"
	"fmt"
	"strings"
)

// Operator zones are domains the instance's operator owns and points at the
// instance, usually with a wildcard (*.app.example.com). A subdomain of one
// is attached to an OPERATOR's site at once, with no DNS proof, and refused
// to everybody else.
//
// The DNS proof exists so that a stranger cannot claim a domain whose DNS
// already points here. A wildcard is exactly that situation for every name
// under it, so without this a customer could claim anything.app.example.com;
// with it the zone is reserved as a whole, and the operator, whose zone it
// is, needs no record per app.

// errOperatorUnknown is returned by verify when the store cannot tell who the
// operator is (the extension is not wired, as in the one-shot cleanup
// command). It is a lookup error, and lookup errors change nothing.
var errOperatorUnknown = errors.New("the operator check is not available")

// SetOperatorZones installs the operator's zones. Each is a bare domain; a
// leading "*." is tolerated.
func (s *Store) SetOperatorZones(zones []string) {
	s.operatorZones = nil
	for _, z := range zones {
		z = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(z)), "*.")
		if z != "" {
			s.operatorZones = append(s.operatorZones, z)
		}
	}
}

// SetOperatorCheck installs the answer to "is this account the operator?".
// The core does not know; the extension does.
func (s *Store) SetOperatorCheck(isOperator func(ownerAccountID string) bool) {
	s.isOperator = isOperator
}

// InOperatorZone reports whether d is one of the operator's zones or a name
// under one.
func (s *Store) InOperatorZone(d string) bool {
	d = strings.ToLower(d)
	for _, z := range s.operatorZones {
		if d == z || strings.HasSuffix(d, "."+z) {
			return true
		}
	}
	return false
}

// operatorOwns answers whether the site belongs to the operator. The error is
// errOperatorUnknown when nothing can answer.
func (s *Store) operatorOwns(site *Site) (bool, error) {
	if s.isOperator == nil {
		return false, errOperatorUnknown
	}
	owner := site.Meta.OwnerAccountID
	return owner != "" && s.isOperator(owner), nil
}

// refuseOperatorZone is AddDomain's gate: a name in an operator zone is
// refused outright unless the site is the operator's, rather than recorded as
// a claim that could never verify.
func (s *Store) refuseOperatorZone(site *Site, d string) error {
	if !s.InOperatorZone(d) {
		return nil
	}
	ok, err := s.operatorOwns(site)
	if err != nil || !ok {
		return fmt.Errorf("%w: %s is reserved for this instance's operator", ErrBadDomain, d)
	}
	return nil
}
