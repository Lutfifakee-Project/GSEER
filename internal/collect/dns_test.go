// GSEER — internet-asset search engine.
// Copyright (C) 2026  The GSEER Authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package collect

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// dns_test.go — the dns and ip2asn connectors against fake resolvers
// (no real network is ever touched by the test suite).

func withSeams(t *testing.T, ip func() ([]net.IPAddr, error), cname func() (string, error), txt func(name string) ([]string, error)) {
	t.Helper()
	oldIP, oldCN, oldTXT, oldMX, oldNS := lookupIPAddr, lookupCNAME, lookupTXT, lookupMX, lookupNS
	lookupIPAddr = func(context.Context, *net.Resolver, string) ([]net.IPAddr, error) { return ip() }
	lookupCNAME = func(context.Context, *net.Resolver, string) (string, error) { return cname() }
	lookupTXT = func(_ context.Context, _ *net.Resolver, name string) ([]string, error) { return txt(name) }
	// MX/NS default to "no such host" so the dns connector's best-effort
	// probes never reach the real resolver from a unit test.
	lookupMX = func(context.Context, *net.Resolver, string) ([]*net.MX, error) {
		return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
	}
	lookupNS = func(context.Context, *net.Resolver, string) ([]*net.NS, error) {
		return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
	}
	t.Cleanup(func() { lookupIPAddr, lookupCNAME, lookupTXT, lookupMX, lookupNS = oldIP, oldCN, oldTXT, oldMX, oldNS })
}

// withSeamsMXNS overrides the MX/NS stubs set up by withSeams (call it
// AFTER withSeams; the cleanup order restores both).
func withSeamsMXNS(t *testing.T, mx func() ([]*net.MX, error), ns func() ([]*net.NS, error)) {
	t.Helper()
	oldMX, oldNS := lookupMX, lookupNS
	lookupMX = func(context.Context, *net.Resolver, string) ([]*net.MX, error) { return mx() }
	lookupNS = func(context.Context, *net.Resolver, string) ([]*net.NS, error) { return ns() }
	t.Cleanup(func() { lookupMX, lookupNS = oldMX, oldNS })
}

func ctxFor(cfg SourceConfig) Ctx {
	return Ctx{
		Context: context.Background(),
		Config:  cfg,
		Limit:   10,
		IndexSeeds: func(kind string, limit int) ([]string, error) {
			if kind == "ip" {
				return []string{"203.0.113.7"}, nil
			}
			return []string{"seeded.example", "nx.example"}, nil
		},
		Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
}

func TestDNSConnectorRecordsAnswers(t *testing.T) {
	withSeams(t,
		func() ([]net.IPAddr, error) {
			return []net.IPAddr{
				{IP: net.IPv4(203, 0, 113, 7).To4()},
				{IP: net.ParseIP("2001:db8::1")},
			}, nil
		},
		func() (string, error) { return "cdn.example.", nil },
		func(string) ([]string, error) { return nil, errors.New("not used") })

	b, err := dnsConnector{}.Collect(ctxFor(SourceConfig{SeedMode: "config", Seeds: []string{"Example.com."}}))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !b.Done {
		t.Error("dns pass must be Done (point lookups)")
	}
	var ips, hosts int
	for _, r := range b.Raws {
		switch r.AssetKind {
		case "ip":
			ips++
			if r.Value != "203.0.113.7" && r.Value != "2001:db8::1" {
				t.Errorf("unexpected ip answer %q", r.Value)
			}
		case "hostname":
			hosts++
			if r.Value != "cdn.example" {
				t.Errorf("CNAME target not canonicalized: %q", r.Value)
			}
		}
	}
	if ips != 2 || hosts != 1 {
		t.Fatalf("expected 2 ips + 1 hostname, got %d/%d", ips, hosts)
	}
	if b.Notes["lookups"] != 1 || b.Notes["answers"] != 3 {
		t.Errorf("notes wrong: %+v", b.Notes)
	}
}

// TestDNSConnectorRecordsMXAndNS pins the MX/NS record types: a
// mail-exchanger or nameserver answer becomes a hostname asset fact with
// the query name, the record type and (for MX) the preference in its
// evidence.  The answers stay best-effort like the CNAME probe.
func TestDNSConnectorRecordsMXAndNS(t *testing.T) {
	withSeams(t,
		func() ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.IPv4(203, 0, 113, 7).To4()}}, nil
		},
		func() (string, error) { return "", &net.DNSError{Err: "no such host", IsNotFound: true} },
		func(string) ([]string, error) { return nil, errors.New("not used") })
	withSeamsMXNS(t,
		func() ([]*net.MX, error) {
			return []*net.MX{
				{Host: "mail1.example.com.", Pref: 10},
				{Host: "mail1.example.com.", Pref: 10}, // same target again: two answers, one fact after pipeline dedup
				{Host: "Example.COM.", Pref: 20},       // apex itself: skipped
			}, nil
		},
		func() ([]*net.NS, error) {
			return []*net.NS{
				{Host: "ns1.example.com."},
				{Host: "ns2.example.com."},
			}, nil
		})

	b, err := dnsConnector{}.Collect(ctxFor(SourceConfig{SeedMode: "config", Seeds: []string{"example.com"}}))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	types := map[string]string{}
	for _, r := range b.Raws {
		if r.AssetKind != "hostname" {
			continue
		}
		dnsMap, _ := r.Extra["dns"].(map[string]any)
		rt, _ := dnsMap["type"].(string)
		types[r.Value] = rt
	}
	for want, wantType := range map[string]string{
		"mail1.example.com": "MX",
		"ns1.example.com":   "NS",
		"ns2.example.com":   "NS",
	} {
		if types[want] != wantType {
			t.Errorf("expected %s fact (type %s), got %q", want, wantType, types[want])
		}
	}
	if _, dup := types["mail1.example.com."]; dup {
		t.Error("MX target kept its trailing dot (not normalized)")
	}
	// Notes count ANSWERS as delivered (mx:2 for the duplicate target);
	// the pipeline collapses them into one fact via intra-batch + L1/L2 dedup.
	if b.Notes["mx"] != 2 || b.Notes["ns"] != 2 {
		t.Errorf("mx/ns answer counters wrong: %+v", b.Notes)
	}
}

// TestDNSConnectorMXFailureIsBestEffort: MX/NS lookup failures on a seed
// whose A lookup succeeded are ordinary DNS, not a transport outage —
// the pass must succeed (retrying it would not help).
func TestDNSConnectorMXFailureIsBestEffort(t *testing.T) {
	withSeams(t,
		func() ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.IPv4(203, 0, 113, 7).To4()}}, nil
		},
		func() (string, error) { return "", &net.DNSError{Err: "no such host", IsNotFound: true} },
		func(string) ([]string, error) { return nil, errors.New("not used") })
	withSeamsMXNS(t,
		func() ([]*net.MX, error) {
			return nil, &net.DNSError{Err: "server misbehaving", IsTemporary: true}
		},
		func() ([]*net.NS, error) {
			return nil, &net.DNSError{Err: "server misbehaving", IsTemporary: true}
		})

	b, err := dnsConnector{}.Collect(ctxFor(SourceConfig{SeedMode: "config", Seeds: []string{"example.com"}}))
	if err != nil {
		t.Fatalf("MX/NS failures on a live seed must not fail the pass, got %v", err)
	}
	if len(b.Raws) != 1 {
		t.Errorf("expected only the A-record fact, got %d records", len(b.Raws))
	}
}

func TestDNSConnectorToleratesNXDOMAINButFailsWhenAllDown(t *testing.T) {
	// One seed NXDOMAIN (no error), one resolver outage (error):
	// partial failure is normal DNS, total failure is retryable.
	calls := 0
	withSeams(t,
		func() ([]net.IPAddr, error) {
			calls++
			if calls == 1 {
				return nil, &net.DNSError{Err: "no such host", Name: "nx.example", IsNotFound: true}
			}
			return nil, &net.DNSError{Err: "i/o timeout", IsTimeout: true}
		},
		func() (string, error) { return "", &net.DNSError{Err: "no such host", IsNotFound: true} },
		func(string) ([]string, error) { return nil, nil })

	_, err := dnsConnector{}.Collect(ctxFor(SourceConfig{SeedMode: "index"}))
	if err == nil || !stringsContains(err.Error(), "all 2 lookups failed") {
		t.Fatalf("expected total-failure error, got %v", err)
	}
}

func TestDNSConnectorRejectsUnknownSeedMode(t *testing.T) {
	_, err := dnsConnector{}.Collect(ctxFor(SourceConfig{SeedMode: "wordlist"}))
	var fatal FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("unknown seed_mode must be a FatalError (no retry), got %v", err)
	}
}

func TestIP2ASNConnectorEnriches(t *testing.T) {
	txtNames := map[string][]string{}
	withSeams(t,
		func() ([]net.IPAddr, error) { return nil, errors.New("not used") },
		func() (string, error) { return "", errors.New("not used") },
		func(name string) ([]string, error) {
			if v, ok := txtNames[name]; ok {
				return v, nil
			}
			return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
		})
	txtNames["7.113.0.203."+ip2ASNDomainV4] = []string{"64500 | 203.0.113.0/24 | US | arin | 2002-04-23"}
	// Live asn.cymru.com wire format: "AS | CC | registry | allocated | AS NAME"
	// (name is field 5; captured from the production service 2026-11).
	txtNames["AS64500."+asnNameDomain] = []string{"64500 | US | arin | 2002-04-23 | EXAMPLE-AS, US"}

	b, err := ip2asnConnector{}.Collect(ctxFor(SourceConfig{SeedMode: "index"}))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(b.Raws) != 1 {
		t.Fatalf("expected 1 enriched record, got %d", len(b.Raws))
	}
	r := b.Raws[0]
	if r.Value != "203.0.113.7" || r.ASN != 64500 || r.GeoCountry != "US" {
		t.Errorf("enrichment wrong: %+v", r)
	}
	if r.ASNOrg != "EXAMPLE-AS, US" {
		t.Errorf("AS name parse wrong: %q", r.ASNOrg)
	}
	if b.Notes["hits"] != 1 {
		t.Errorf("hit counter wrong: %+v", b.Notes)
	}
}

func TestIP2ASNConnectorHandlesUnknownIPAndBadAnswer(t *testing.T) {
	raws := map[string][]string{}
	withSeams(t,
		func() ([]net.IPAddr, error) { return nil, errors.New("not used") },
		func() (string, error) { return "", errors.New("not used") },
		func(name string) ([]string, error) {
			if v, ok := raws[name]; ok {
				return v, nil
			}
			return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
		})
	// 203.0.113.8 → no answer; 203.0.113.9 → garbage answer.
	raws["9.113.0.203."+ip2ASNDomainV4] = []string{"not a cymru line"}

	b, err := ip2asnConnector{}.Collect(ctxFor(SourceConfig{
		SeedMode: "config", Seeds: []string{"203.0.113.8", "203.0.113.9"}}))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(b.Raws) != 1 {
		t.Fatalf("unknown IP must be skipped, bad answer audited; got %+v", b.Raws)
	}
	if b.Raws[0].Value != "203.0.113.9" || b.Raws[0].Confidence != "low" {
		t.Errorf("bad-answer audit record wrong: %+v", b.Raws[0])
	}
}

func TestIP2ASNConnectorIPv6ReverseName(t *testing.T) {
	if got := reverseIP("2001:db8::1"); len(got) == 0 || !stringsContains(got, "8.b.d.0.1.0.0.2") {
		t.Errorf("IPv6 reverse name wrong: %q", got)
	}
	if got := reverseIP("203.0.113.7"); got != "7.113.0.203" {
		t.Errorf("IPv4 reverse name wrong: %q", got)
	}
	if ip2ASNDomainOf("2001:db8::1") != ip2ASNDomainV6 || ip2ASNDomainOf("203.0.113.7") != ip2ASNDomainV4 {
		t.Error("family routing wrong")
	}
}

func stringsContains(hay, needle string) bool {
	return len(hay) >= len(needle) && (hay == needle || indexOf(hay, needle) >= 0)
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
