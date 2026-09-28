package store

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

// Ownership proofs are asked of the domain's AUTHORITATIVE nameservers, not
// of the system resolver.
//
// A recursive resolver caches "no such record" for the zone's negative TTL —
// an hour on Hetzner — and the proof is always looked up before it exists:
// when the owner adds the domain or zone (the token is minted right then), and
// again when they press "check now" seconds after creating the record, before
// the provider has published it. Each of those planted an NXDOMAIN that the
// instance's resolver then served back for an hour, so a correctly created
// record looked missing. The instance's resolver is the hosting provider's,
// and no setting on our side turns its cache off; asking the servers that
// hold the zone skips every cache in between.
//
// Finding those servers still goes through the system resolver (NS records
// are stable, and a cached negative answer for a label that has no NS only
// makes the walk step up one more label). The walk goes up as far as the TLD,
// and a server that answers with a delegation towards the name is followed to
// the servers it names: a domain registered minutes ago is looked up before
// its registry publishes it, the resolver then denies the WHOLE domain exists
// for the TLD's negative TTL, and only the registry's own delegation still
// leads to the zone's servers. If no authoritative server answers at all, the
// lookup falls back to the system resolver: a slower proof is better than
// none.

// authDNS asks a name's authoritative nameservers directly.
type authDNS struct {
	lookupNS   func(ctx context.Context, name string) ([]*net.NS, error)
	lookupHost func(ctx context.Context, host string) ([]string, error)
	exchange   func(ctx context.Context, server string, query []byte, tcp bool) ([]byte, error)
}

func newAuthDNS() *authDNS {
	r := net.DefaultResolver
	return &authDNS{lookupNS: r.LookupNS, lookupHost: r.LookupHost, exchange: dnsExchange}
}

// maxAuthServers bounds how many nameserver addresses one lookup tries.
const maxAuthServers = 4

// maxReferrals bounds how many delegations one lookup follows.
const maxReferrals = 6

// errNoAuthority reports that no authoritative server gave a usable answer;
// the caller falls back to the system resolver.
var errNoAuthority = errors.New("no authoritative nameserver answered")

// nameserver is one address to ask, with the name it was found under, which
// is what a person reading "no such record" wants to know.
type nameserver struct {
	addr string // host:port
	host string
}

// servers finds the nameservers of the zone that holds name: the NS set of
// name itself or its nearest ancestor that has one, up to and including the
// TLD, whose servers then delegate (see query). Leading underscore labels
// (_sitebin-zone., _sitebin-challenge.) are never a zone apex and are skipped.
// A lookup that fails outright steps up a label too. It returns the zone whose
// NS set it found alongside its servers.
func (a *authDNS) servers(ctx context.Context, name string) (string, []nameserver, error) {
	cand := strings.ToLower(strings.TrimSuffix(name, "."))
	for strings.HasPrefix(cand, "_") {
		_, cand, _ = strings.Cut(cand, ".")
	}
	var lastErr error
	for cand != "" {
		// Fully qualified: a bare TLD would be tried with the search domains
		// first, and some platforms refuse a single label outright.
		ns, err := a.lookupNS(ctx, cand+".")
		if err == nil && len(ns) > 0 {
			hosts := make([]string, 0, len(ns))
			for _, n := range ns {
				hosts = append(hosts, n.Host)
			}
			out := a.addresses(ctx, hosts, nil)
			if len(out) == 0 {
				return "", nil, fmt.Errorf("%w: the nameservers of %s do not resolve", errNoAuthority, cand)
			}
			return cand, out, nil
		}
		if err != nil && !isNotFound(err) {
			lastErr = err
		}
		_, cand, _ = strings.Cut(cand, ".")
	}
	return "", nil, fmt.Errorf("%w: no NS found for %s (%v)", errNoAuthority, name, lastErr)
}

// addresses resolves nameserver host names to at most maxAuthServers
// addresses, preferring the glue a referral carried (which the caller has
// already restricted to names it may vouch for) over the system resolver.
func (a *authDNS) addresses(ctx context.Context, hosts []string, glue map[string][]string) []nameserver {
	var out []nameserver
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSuffix(h, "."))
		addrs := glue[h]
		if len(addrs) == 0 {
			var err error
			if addrs, err = a.lookupHost(ctx, h); err != nil {
				continue
			}
		}
		for _, ip := range addrs {
			out = append(out, nameserver{addr: net.JoinHostPort(ip, "53"), host: h})
			if len(out) >= maxAuthServers {
				return out
			}
		}
	}
	return out
}

// inZone reports whether name is zone or lies below it. Both are lower-case
// and carry no trailing dot; "" is the root, which holds every name.
func inZone(name, zone string) bool {
	return zone == "" || name == zone || strings.HasSuffix(name, "."+zone)
}

// canonical is a dnsmessage name as inZone compares it.
func canonical(n dnsmessage.Name) string {
	return strings.ToLower(strings.TrimSuffix(n.String(), "."))
}

// delegation reads a non-authoritative answer from a server of zone as a
// referral towards qname: the child zone it names, that zone's nameservers
// and the glue it may vouch for. ok is false for anything else — a referral
// sideways, upwards or to the zone itself is a server that knows nothing.
func delegation(resp *dnsmessage.Message, zone, qname string) (child string, hosts []string, glue map[string][]string, ok bool) {
	for _, rr := range resp.Authorities {
		ns, isNS := rr.Body.(*dnsmessage.NSResource)
		if !isNS {
			continue
		}
		owner := canonical(rr.Header.Name)
		if owner == zone || !inZone(owner, zone) || !inZone(qname, owner) {
			continue
		}
		if child == "" || len(owner) > len(child) {
			child, hosts = owner, nil
		}
		if owner == child {
			hosts = append(hosts, canonical(ns.NS))
		}
	}
	if child == "" || len(hosts) == 0 {
		return "", nil, nil, false
	}
	// Glue is trusted only for a nameserver inside the zone of the server
	// that sent it: that server is the authority for that name, and for
	// nothing else.
	glue = map[string][]string{}
	for _, rr := range resp.Additionals {
		h := canonical(rr.Header.Name)
		if !inZone(h, zone) || !slices.Contains(hosts, h) {
			continue
		}
		switch b := rr.Body.(type) {
		case *dnsmessage.AResource:
			glue[h] = append(glue[h], net.IP(b.A[:]).String())
		case *dnsmessage.AAAAResource:
			glue[h] = append(glue[h], net.IP(b.AAAA[:]).String())
		}
	}
	return child, hosts, glue, true
}

// query asks the authoritative servers for name/qtype. It returns the answer
// records of that type, or a *net.DNSError with IsNotFound for a definitive
// NXDOMAIN or NODATA, or errNoAuthority when no server answered with
// authority. A delegation towards name is followed, at most maxReferrals
// times.
func (a *authDNS) query(ctx context.Context, name string, qtype dnsmessage.Type) ([]dnsmessage.Resource, error) {
	zone, servers, err := a.servers(ctx, name)
	if err != nil {
		return nil, err
	}
	qname, err := dnsmessage.NewName(strings.TrimSuffix(name, ".") + ".")
	if err != nil {
		return nil, err
	}
	want := canonical(qname)
	var id [2]byte
	rand.Read(id[:])
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: binary.BigEndian.Uint16(id[:])},
		Questions: []dnsmessage.Question{{Name: qname, Type: qtype, Class: dnsmessage.ClassINET}},
	}
	packed, err := msg.Pack()
	if err != nil {
		return nil, err
	}
	var last error = errNoAuthority
	for referrals := 0; ; referrals++ {
		var next []nameserver
		nextZone := ""
	tryServers:
		for _, srv := range servers {
			resp, err := a.ask(ctx, srv.addr, packed, msg.Header.ID, qname, qtype)
			if err != nil {
				last = fmt.Errorf("%w: %s: %v", errNoAuthority, srv.host, err)
				continue
			}
			switch resp.Header.RCode {
			case dnsmessage.RCodeNameError:
				if resp.Header.Authoritative {
					return nil, &net.DNSError{Err: "no such host", Name: name, Server: srv.host, IsNotFound: true}
				}
				last = fmt.Errorf("%w: %s is not authoritative for %s", errNoAuthority, srv.host, name)
				continue
			case dnsmessage.RCodeSuccess:
			default:
				last = fmt.Errorf("%w: %s answered %v", errNoAuthority, srv.host, resp.Header.RCode)
				continue
			}
			if !resp.Header.Authoritative {
				child, hosts, glue, ok := delegation(resp, zone, want)
				if !ok {
					// A lame server knows nothing definitive.
					last = fmt.Errorf("%w: %s is not authoritative for %s", errNoAuthority, srv.host, name)
					continue
				}
				if referrals >= maxReferrals {
					return nil, fmt.Errorf("%w: more than %d delegations on the way to %s", errNoAuthority, maxReferrals, name)
				}
				if next = a.addresses(ctx, hosts, glue); len(next) == 0 {
					last = fmt.Errorf("%w: the nameservers %s delegates %s to do not resolve", errNoAuthority, srv.host, child)
					continue
				}
				nextZone = child
				break tryServers
			}
			var out []dnsmessage.Resource
			for _, rr := range resp.Answers {
				if rr.Header.Type == qtype && strings.EqualFold(rr.Header.Name.String(), qname.String()) {
					out = append(out, rr)
				}
			}
			if len(out) == 0 {
				return nil, &net.DNSError{Err: "no such record", Name: name, Server: srv.host, IsNotFound: true}
			}
			return out, nil
		}
		if len(next) == 0 {
			return nil, last
		}
		servers, zone = next, nextZone
	}
}

// ask sends one query over UDP, and again over TCP if the answer was cut.
func (a *authDNS) ask(ctx context.Context, srv string, packed []byte, id uint16, qname dnsmessage.Name, qtype dnsmessage.Type) (*dnsmessage.Message, error) {
	for _, tcp := range []bool{false, true} {
		raw, err := a.exchange(ctx, srv, packed, tcp)
		if err != nil {
			return nil, err
		}
		var resp dnsmessage.Message
		if err := resp.Unpack(raw); err != nil {
			return nil, err
		}
		if resp.Header.ID != id || len(resp.Questions) != 1 ||
			!strings.EqualFold(resp.Questions[0].Name.String(), qname.String()) || resp.Questions[0].Type != qtype {
			return nil, errors.New("answer does not match the question")
		}
		if resp.Header.Truncated && !tcp {
			continue
		}
		return &resp, nil
	}
	return nil, errors.New("truncated over TCP")
}

// LookupTXT is net.Resolver.LookupTXT against the authoritative servers:
// each record's strings joined, as the resolver does.
func (a *authDNS) LookupTXT(ctx context.Context, name string) ([]string, error) {
	rrs, err := a.query(ctx, name, dnsmessage.TypeTXT)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, rr := range rrs {
		if t, ok := rr.Body.(*dnsmessage.TXTResource); ok {
			out = append(out, strings.Join(t.TXT, ""))
		}
	}
	return out, nil
}

// maxCNAMEHops bounds how far a CNAME chain is followed.
const maxCNAMEHops = 8

// LookupCNAME returns the end of the CNAME chain that starts at name, as
// net.Resolver.LookupCNAME does — each hop asked of the servers of the zone
// it lives in — or a not-found error when name itself is no CNAME (the
// resolver would report the name itself then; both mean "not a CNAME at the
// view host" to the verifier). A hop that cannot be asked ends the chain
// there, which at worst fails to prove a domain; it never proves a wrong one.
func (a *authDNS) LookupCNAME(ctx context.Context, name string) (string, error) {
	target := ""
	cur := name
	for hop := 0; hop < maxCNAMEHops; hop++ {
		rrs, err := a.query(ctx, cur, dnsmessage.TypeCNAME)
		if err != nil {
			if target == "" {
				return "", err
			}
			return target, nil // cur is not a CNAME: the chain ends at it
		}
		c, ok := rrs[0].Body.(*dnsmessage.CNAMEResource)
		if !ok {
			break
		}
		target = c.CNAME.String()
		cur = target
	}
	if target == "" {
		return "", &net.DNSError{Err: "no such record", Name: name, IsNotFound: true}
	}
	return target, nil
}

// withFallback prefers the authoritative answer and uses the system resolver
// only when no authoritative server could be asked. The resolver's errors
// are marked (fromResolver), so a message can say the answer may be old.
func withFallback[T any](auth, sys func(context.Context, string) (T, error)) func(context.Context, string) (T, error) {
	return func(ctx context.Context, name string) (T, error) {
		v, err := auth(ctx, name)
		if err != nil && errors.Is(err, errNoAuthority) {
			v, err = sys(ctx, name)
			if err != nil {
				err = fromResolver{err}
			}
		}
		return v, err
	}
}

// fromResolver marks an error from the system resolver, reached because no
// authoritative server could be asked. It unwraps to the resolver's own
// error, so a not-found answer still reads as one.
type fromResolver struct{ err error }

func (e fromResolver) Error() string { return e.err.Error() }
func (e fromResolver) Unwrap() error { return e.err }

// dnsExchange sends one DNS message to server and returns the reply.
func dnsExchange(ctx context.Context, server string, query []byte, tcp bool) ([]byte, error) {
	network := "udp"
	if tcp {
		network = "tcp"
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if !tcp {
		if _, err := conn.Write(query); err != nil {
			return nil, err
		}
		buf := make([]byte, 65535)
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	}
	frame := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(frame, uint16(len(query)))
	copy(frame[2:], query)
	if _, err := conn.Write(frame); err != nil {
		return nil, err
	}
	var l [2]byte
	if _, err := io.ReadFull(conn, l[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
