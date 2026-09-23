package store

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
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
// makes the walk step up one more label). If no authoritative server answers
// at all, the lookup falls back to the system resolver: a slower proof is
// better than none.

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

// errNoAuthority reports that no authoritative server gave a usable answer;
// the caller falls back to the system resolver.
var errNoAuthority = errors.New("no authoritative nameserver answered")

// servers finds the nameservers of the zone that holds name: the NS set of
// name itself or its nearest ancestor that has one. Leading underscore labels
// (_sitebin-zone., _sitebin-challenge.) are never a zone apex and are skipped.
// A lookup that fails outright steps up a label too: if the label was in fact
// a delegated zone, its parent's servers answer with a referral, which is not
// authoritative, and the caller falls back to the system resolver.
func (a *authDNS) servers(ctx context.Context, name string) ([]string, error) {
	cand := strings.TrimSuffix(name, ".")
	for strings.HasPrefix(cand, "_") {
		_, cand, _ = strings.Cut(cand, ".")
	}
	var lastErr error
	for strings.Contains(cand, ".") {
		ns, err := a.lookupNS(ctx, cand)
		if err == nil && len(ns) > 0 {
			var out []string
			for _, n := range ns {
				addrs, err := a.lookupHost(ctx, strings.TrimSuffix(n.Host, "."))
				if err != nil {
					continue
				}
				for _, ip := range addrs {
					out = append(out, net.JoinHostPort(ip, "53"))
					if len(out) >= maxAuthServers {
						return out, nil
					}
				}
			}
			if len(out) == 0 {
				return nil, fmt.Errorf("%w: the nameservers of %s do not resolve", errNoAuthority, cand)
			}
			return out, nil
		}
		if err != nil && !isNotFound(err) {
			lastErr = err
		}
		_, cand, _ = strings.Cut(cand, ".")
	}
	return nil, fmt.Errorf("%w: no NS found for %s (%v)", errNoAuthority, name, lastErr)
}

// query asks the authoritative servers for name/qtype. It returns the answer
// records of that type, or a *net.DNSError with IsNotFound for a definitive
// NXDOMAIN or NODATA, or errNoAuthority when no server answered with
// authority.
func (a *authDNS) query(ctx context.Context, name string, qtype dnsmessage.Type) ([]dnsmessage.Resource, error) {
	servers, err := a.servers(ctx, name)
	if err != nil {
		return nil, err
	}
	qname, err := dnsmessage.NewName(strings.TrimSuffix(name, ".") + ".")
	if err != nil {
		return nil, err
	}
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
	for _, srv := range servers {
		resp, err := a.ask(ctx, srv, packed, msg.Header.ID, qname, qtype)
		if err != nil {
			last = fmt.Errorf("%w: %s: %v", errNoAuthority, srv, err)
			continue
		}
		switch resp.Header.RCode {
		case dnsmessage.RCodeNameError:
			return nil, &net.DNSError{Err: "no such host", Name: name, Server: srv, IsNotFound: true}
		case dnsmessage.RCodeSuccess:
		default:
			last = fmt.Errorf("%w: %s answered %v", errNoAuthority, srv, resp.Header.RCode)
			continue
		}
		if !resp.Header.Authoritative {
			// A lame or referring server knows nothing definitive.
			last = fmt.Errorf("%w: %s is not authoritative for %s", errNoAuthority, srv, name)
			continue
		}
		var out []dnsmessage.Resource
		for _, rr := range resp.Answers {
			if rr.Header.Type == qtype && strings.EqualFold(rr.Header.Name.String(), qname.String()) {
				out = append(out, rr)
			}
		}
		if len(out) == 0 {
			return nil, &net.DNSError{Err: "no such record", Name: name, Server: srv, IsNotFound: true}
		}
		return out, nil
	}
	return nil, last
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
// only when no authoritative server could be asked.
func withFallback[T any](auth, sys func(context.Context, string) (T, error)) func(context.Context, string) (T, error) {
	return func(ctx context.Context, name string) (T, error) {
		v, err := auth(ctx, name)
		if err != nil && errors.Is(err, errNoAuthority) {
			return sys(ctx, name)
		}
		return v, err
	}
}

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
