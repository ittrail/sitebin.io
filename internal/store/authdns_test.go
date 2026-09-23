package store

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeAuth is a set of authoritative servers answering from a table. NS
// records exist for the names in zones; every server address answers with
// the reply its entry in replies builds.
type fakeAuth struct {
	zones   map[string][]string // zone -> ns hosts
	replies map[string]func(q dnsmessage.Question) dnsmessage.Message
	asked   []string // "server tcp?"
}

func (f *fakeAuth) dns() *authDNS {
	return &authDNS{
		lookupNS: func(_ context.Context, name string) ([]*net.NS, error) {
			hosts, ok := f.zones[name]
			if !ok {
				return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
			}
			var out []*net.NS
			for _, h := range hosts {
				out = append(out, &net.NS{Host: h + "."})
			}
			return out, nil
		},
		lookupHost: func(_ context.Context, host string) ([]string, error) {
			return []string{host}, nil // the host name stands in for its address
		},
		exchange: func(_ context.Context, server string, query []byte, tcp bool) ([]byte, error) {
			f.asked = append(f.asked, server+map[bool]string{true: " tcp", false: ""}[tcp])
			var q dnsmessage.Message
			if err := q.Unpack(query); err != nil {
				return nil, err
			}
			if q.Header.RecursionDesired {
				return nil, errors.New("recursion asked of an authoritative server")
			}
			host, _, _ := net.SplitHostPort(server)
			build, ok := f.replies[host]
			if !ok {
				return nil, errors.New("timeout")
			}
			m := build(q.Questions[0])
			if tcp {
				m.Header.Truncated = false
			}
			m.Header.ID = q.Header.ID
			m.Header.Response = true
			m.Questions = q.Questions
			return m.Pack()
		},
	}
}

func txtAnswer(q dnsmessage.Question, vals ...string) dnsmessage.Message {
	m := dnsmessage.Message{Header: dnsmessage.Header{Authoritative: true}}
	for _, v := range vals {
		m.Answers = append(m.Answers, dnsmessage.Resource{
			Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: 60},
			Body:   &dnsmessage.TXTResource{TXT: strings.Split(v, "|")},
		})
	}
	return m
}

func rcode(c dnsmessage.RCode, aa bool) func(dnsmessage.Question) dnsmessage.Message {
	return func(dnsmessage.Question) dnsmessage.Message {
		return dnsmessage.Message{Header: dnsmessage.Header{RCode: c, Authoritative: aa}}
	}
}

func TestAuthDNSAsksTheZonesOwnServers(t *testing.T) {
	f := &fakeAuth{
		zones: map[string][]string{"kunde.example": {"ns1", "ns2"}},
		replies: map[string]func(dnsmessage.Question) dnsmessage.Message{
			"ns1": rcode(dnsmessage.RCodeServerFailure, true), // first one is broken
			"ns2": func(q dnsmessage.Question) dnsmessage.Message { return txtAnswer(q, "sitebin-zone=|abc", "other") },
		},
	}
	got, err := f.dns().LookupTXT(context.Background(), "_sitebin-zone.deep.kunde.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "sitebin-zone=abc" {
		t.Errorf("TXT = %q (split strings must be joined)", got)
	}
	if len(f.asked) != 2 || f.asked[0] != "ns1:53" || f.asked[1] != "ns2:53" {
		t.Errorf("asked %v", f.asked)
	}
}

func TestAuthDNSSkipsUnderscoreLabelsAndFailedLevels(t *testing.T) {
	f := &fakeAuth{
		zones:   map[string][]string{"kunde.example": {"ns1"}},
		replies: map[string]func(dnsmessage.Question) dnsmessage.Message{"ns1": func(q dnsmessage.Question) dnsmessage.Message { return txtAnswer(q, "ok") }},
	}
	a := f.dns()
	var nsAsked []string
	inner := a.lookupNS
	a.lookupNS = func(ctx context.Context, name string) ([]*net.NS, error) {
		nsAsked = append(nsAsked, name)
		if name == "deep.kunde.example" {
			return nil, errors.New("i/o timeout") // a flaky level is stepped over
		}
		return inner(ctx, name)
	}
	if got, err := a.LookupTXT(context.Background(), "_sitebin-zone._x.deep.kunde.example"); err != nil || got[0] != "ok" {
		t.Fatalf("= %v %v", got, err)
	}
	if strings.Join(nsAsked, ",") != "deep.kunde.example,kunde.example" {
		t.Errorf("NS asked for %v", nsAsked)
	}
}

func TestAuthDNSDefinitiveAbsence(t *testing.T) {
	for name, reply := range map[string]func(dnsmessage.Question) dnsmessage.Message{
		"NXDOMAIN": rcode(dnsmessage.RCodeNameError, true),
		"NODATA":   rcode(dnsmessage.RCodeSuccess, true),
	} {
		f := &fakeAuth{zones: map[string][]string{"kunde.example": {"ns1"}}, replies: map[string]func(dnsmessage.Question) dnsmessage.Message{"ns1": reply}}
		_, err := f.dns().LookupTXT(context.Background(), "_sitebin-zone.kunde.example")
		if !isNotFound(err) {
			t.Errorf("%s: err = %v, want a not-found answer", name, err)
		}
	}
}

func TestAuthDNSNoAuthorityFallsBack(t *testing.T) {
	cases := map[string]*fakeAuth{
		"no NS anywhere": {zones: map[string][]string{}},
		"servers down":   {zones: map[string][]string{"kunde.example": {"ns1"}}},
		"not authoritative": {zones: map[string][]string{"kunde.example": {"ns1"}},
			replies: map[string]func(dnsmessage.Question) dnsmessage.Message{"ns1": rcode(dnsmessage.RCodeSuccess, false)}},
		"refused": {zones: map[string][]string{"kunde.example": {"ns1"}},
			replies: map[string]func(dnsmessage.Question) dnsmessage.Message{"ns1": rcode(dnsmessage.RCodeRefused, true)}},
	}
	for name, f := range cases {
		a := f.dns()
		_, err := a.LookupTXT(context.Background(), "_sitebin-zone.kunde.example")
		if !errors.Is(err, errNoAuthority) || isNotFound(err) {
			t.Errorf("%s: err = %v, want errNoAuthority and not a definitive absence", name, err)
		}
		sysAsked := false
		lookup := withFallback(a.LookupTXT, func(context.Context, string) ([]string, error) {
			sysAsked = true
			return []string{"from the system resolver"}, nil
		})
		if got, err := lookup(context.Background(), "_sitebin-zone.kunde.example"); err != nil || !sysAsked || got[0] != "from the system resolver" {
			t.Errorf("%s: fallback = %v %v", name, got, err)
		}
	}
	// A definitive answer is never second-guessed by the system resolver.
	f := &fakeAuth{zones: map[string][]string{"kunde.example": {"ns1"}},
		replies: map[string]func(dnsmessage.Question) dnsmessage.Message{"ns1": rcode(dnsmessage.RCodeNameError, true)}}
	lookup := withFallback(f.dns().LookupTXT, func(context.Context, string) ([]string, error) {
		t.Error("the system resolver was asked after an authoritative NXDOMAIN")
		return nil, nil
	})
	lookup(context.Background(), "x.kunde.example")
}

func TestAuthDNSRetriesTruncatedOverTCP(t *testing.T) {
	f := &fakeAuth{
		zones: map[string][]string{"kunde.example": {"ns1"}},
		replies: map[string]func(dnsmessage.Question) dnsmessage.Message{"ns1": func(q dnsmessage.Question) dnsmessage.Message {
			m := txtAnswer(q, "big")
			m.Header.Truncated = true // cleared by the fake over TCP
			return m
		}},
	}
	got, err := f.dns().LookupTXT(context.Background(), "kunde.example")
	if err != nil || len(got) != 1 || got[0] != "big" {
		t.Fatalf("= %v %v", got, err)
	}
	if len(f.asked) != 2 || f.asked[1] != "ns1:53 tcp" {
		t.Errorf("asked %v, want UDP then TCP", f.asked)
	}
}

func TestAuthDNSFollowsTheCNAMEChain(t *testing.T) {
	name := func(s string) dnsmessage.Name { n, _ := dnsmessage.NewName(s); return n }
	chain := map[string]string{"shop.kunde.example.": "proxy.kunde.example.", "proxy.kunde.example.": "abc.sitebin.app."}
	reply := func(q dnsmessage.Question) dnsmessage.Message {
		m := dnsmessage.Message{Header: dnsmessage.Header{Authoritative: true}}
		if to, ok := chain[q.Name.String()]; ok {
			m.Answers = []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 60},
				Body:   &dnsmessage.CNAMEResource{CNAME: name(to)},
			}}
		}
		return m
	}
	f := &fakeAuth{
		zones:   map[string][]string{"kunde.example": {"ns1"}, "sitebin.app": {"ns2"}},
		replies: map[string]func(dnsmessage.Question) dnsmessage.Message{"ns1": reply, "ns2": reply},
	}
	got, err := f.dns().LookupCNAME(context.Background(), "shop.kunde.example")
	if err != nil || got != "abc.sitebin.app." {
		t.Errorf("chain end = %q %v", got, err)
	}
}

func TestAuthDNSCNAME(t *testing.T) {
	target, _ := dnsmessage.NewName("abc.sitebin.app.")
	f := &fakeAuth{
		zones: map[string][]string{"kunde.example": {"ns1"}},
		replies: map[string]func(dnsmessage.Question) dnsmessage.Message{"ns1": func(q dnsmessage.Question) dnsmessage.Message {
			if q.Name.String() != "www.kunde.example." {
				return dnsmessage.Message{Header: dnsmessage.Header{Authoritative: true}} // NODATA: A records only
			}
			return dnsmessage.Message{Header: dnsmessage.Header{Authoritative: true}, Answers: []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 60},
				Body:   &dnsmessage.CNAMEResource{CNAME: target},
			}}}
		}},
	}
	a := f.dns()
	if got, err := a.LookupCNAME(context.Background(), "www.kunde.example"); err != nil || got != "abc.sitebin.app." {
		t.Errorf("CNAME = %q %v", got, err)
	}
	if _, err := a.LookupCNAME(context.Background(), "kunde.example"); !isNotFound(err) {
		t.Errorf("apex with A records only = %v, want not found", err)
	}

	// End to end through the verifier: the CNAME route proves the domain.
	v := &DNSVerifier{lookupTXT: a.LookupTXT, lookupCNAME: a.LookupCNAME}
	if ok, err := v.Verify(context.Background(), "www.kunde.example", "tok", "abc.sitebin.app"); !ok || err != nil {
		t.Errorf("Verify via authoritative CNAME = %v %v", ok, err)
	}
}
