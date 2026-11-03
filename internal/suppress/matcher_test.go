// GSEER — Internet Asset Intelligence & Search Engine.
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

package suppress

import (
	"testing"
	"time"
)

// matcher_test.go — compiled-matcher behavior: every rule kind, IPv4/IPv6,
// IDNA and trailing-dot inputs, strict label boundaries, expiration,
// determinism of multi-rule matches and the fail-closed error paths.

func mustMatcher(t *testing.T, rules []Rule) *Matcher {
	t.Helper()
	m, err := New(rules, time.Now())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func ipRule(id int64, v string) Rule {
	return Rule{ID: id, Kind: KindIP, Value: v, Enabled: true}
}
func cidrRule(id int64, v string) Rule {
	return Rule{ID: id, Kind: KindCIDR, Value: v, Enabled: true}
}
func domainRule(id int64, v string) Rule {
	return Rule{ID: id, Kind: KindDomain, Value: v, Enabled: true}
}
func suffixRule(id int64, v string) Rule {
	return Rule{ID: id, Kind: KindDomainSuffix, Value: v, Enabled: true}
}
func sourceRule(id int64, v string) Rule {
	return Rule{ID: id, Kind: KindSource, Value: v, Enabled: true}
}

func TestMatcherExactIPv4(t *testing.T) {
	m := mustMatcher(t, []Rule{ipRule(1, "192.0.2.1")})
	// Exact + non-canonical forms of the same address all match.
	for _, in := range []string{"192.0.2.1", " 192.0.2.1 "} {
		r, err := m.MatchesAsset("ip", in)
		if err != nil || r == nil {
			t.Fatalf("MatchesAsset(ip, %q) = %v, %v; want rule", in, r, err)
		}
		if r.Value != "192.0.2.1" {
			t.Errorf("matched rule value = %q", r.Value)
		}
	}
	// Different address: no match.
	if r, err := m.MatchesAsset("ip", "192.0.2.2"); err != nil || r != nil {
		t.Errorf("MatchesAsset(ip, 192.0.2.2) = %v, %v; want nil", r, err)
	}
}

func TestMatcherExactIPv6(t *testing.T) {
	m := mustMatcher(t, []Rule{ipRule(1, "2001:0DB8:0000::1")}) // stored non-canonical → normalized at compile
	for _, in := range []string{"2001:db8::1", "2001:DB8:0:0:0:0:0:1", "fe80::1%eth0-sniff"} {
		wantMatch := in != "fe80::1%eth0-sniff"
		r, err := m.MatchesAsset("ip", in)
		if err != nil {
			t.Fatalf("MatchesAsset(ip, %q): %v", in, err)
		}
		if wantMatch && r == nil {
			t.Errorf("MatchesAsset(ip, %q) = nil, want rule", in)
		}
		if !wantMatch && r != nil {
			t.Errorf("MatchesAsset(ip, %q) = %v, want nil", in, r)
		}
	}
}

func TestMatcherCIDRContainment(t *testing.T) {
	m := mustMatcher(t, []Rule{cidrRule(1, "203.0.113.0/24"), cidrRule(2, "2001:db8::/32")})
	for _, in := range []string{"203.0.113.1", "203.0.113.254", "203.0.113.0"} {
		r, err := m.MatchesAsset("ip", in)
		if err != nil || r == nil || r.ID != 1 {
			t.Errorf("MatchesAsset(ip, %q) = %v, %v; want rule 1", in, r, err)
		}
	}
	for _, in := range []string{"203.0.114.1", "192.0.2.1"} {
		r, err := m.MatchesAsset("ip", in)
		if err != nil || r != nil {
			t.Errorf("MatchesAsset(ip, %q) = %v, %v; want nil", in, r, err)
		}
	}
	r, err := m.MatchesAsset("ip", "2001:db8:ffff::1")
	if err != nil || r == nil || r.ID != 2 {
		t.Errorf("IPv6 CIDR match = %v, %v; want rule 2", r, err)
	}
}

func TestMatcherCIDRMostSpecificWins(t *testing.T) {
	m := mustMatcher(t, []Rule{cidrRule(1, "10.0.0.0/8"), cidrRule(2, "10.1.0.0/16")})
	r, err := m.MatchesAsset("ip", "10.1.2.3")
	if err != nil || r == nil || r.ID != 2 {
		t.Errorf("most-specific CIDR = %v, %v; want rule 2", r, err)
	}
	r, _ = m.MatchesAsset("ip", "10.2.0.1")
	if r == nil || r.ID != 1 {
		t.Errorf("outer CIDR = %v; want rule 1", r)
	}
}

func TestMatcherDomainExactAndCase(t *testing.T) {
	m := mustMatcher(t, []Rule{domainRule(1, "EXAMPLE.com")})
	for _, in := range []string{"example.com", "EXAMPLE.COM", "Example.com.", " example.com "} {
		r, err := m.MatchesAsset("domain", in)
		if err != nil || r == nil {
			t.Fatalf("MatchesAsset(domain, %q) = %v, %v; want rule", in, r, err)
		}
	}
	// Hostname assets match exact domain rules too.
	r, err := m.MatchesAsset("hostname", "example.com")
	if err != nil || r == nil {
		t.Errorf("MatchesAsset(hostname, example.com) = %v, %v; want rule", r, err)
	}
	if r, _ := m.MatchesAsset("domain", "sub.example.com"); r != nil {
		t.Errorf("exact rule must not match descendant: %v", r)
	}
}

func TestMatcherIDNAAndTrailingDot(t *testing.T) {
	m := mustMatcher(t, []Rule{domainRule(1, "münchen.de")})
	// All surface forms of the same name must hit the ONE canonical rule.
	for _, in := range []string{"münchen.de", "MÜNCHEN.DE", "xn--mnchen-3ya.de", "MÜNCHEN.de."} {
		r, err := m.MatchesAsset("domain", in)
		if err != nil || r == nil {
			t.Errorf("MatchesAsset(domain, %q) = %v, %v; want rule", in, r, err)
		}
	}
}

func TestMatcherSuffixLabelBoundary(t *testing.T) {
	m := mustMatcher(t, []Rule{suffixRule(1, "example.com")})
	match := []string{"example.com", "sub.example.com", "deep.a.b.example.com", "SUB.EXAMPLE.COM."}
	noMatch := []string{"notexample.com", "example.com.evil.net", "example.comx", "badexample.co"}
	for _, in := range match {
		r, err := m.MatchesAsset("domain", in)
		if err != nil || r == nil {
			t.Errorf("MatchesAsset(domain, %q) = %v, %v; want suffix rule", in, r, err)
		}
	}
	for _, in := range noMatch {
		if r, err := m.MatchesAsset("domain", in); err != nil || r != nil {
			t.Errorf("MatchesAsset(domain, %q) = %v, %v; want nil", in, r, err)
		}
	}
	// Hostname assets follow the same semantics.
	if r, _ := m.MatchesAsset("hostname", "www.sub.example.com"); r == nil {
		t.Error("hostname descendant must match suffix rule")
	}
}

func TestMatcherSuffixMostSpecificWins(t *testing.T) {
	m := mustMatcher(t, []Rule{suffixRule(1, "example.com"), suffixRule(2, "cdn.example.com")})
	r, err := m.MatchesAsset("domain", "a.cdn.example.com")
	if err != nil || r == nil || r.ID != 2 {
		t.Errorf("longest suffix = %v, %v; want rule 2", r, err)
	}
	r, _ = m.MatchesAsset("domain", "www.example.com")
	if r == nil || r.ID != 1 {
		t.Errorf("parent suffix = %v; want rule 1", r)
	}
}

func TestMatcherDisabledAndExpiredSkipped(t *testing.T) {
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	m := mustMatcher(t, []Rule{
		{ID: 1, Kind: KindIP, Value: "192.0.2.1", Enabled: false},
		{ID: 2, Kind: KindDomainSuffix, Value: "expired.example", Enabled: true, ExpiresAt: &past},
		{ID: 3, Kind: KindDomain, Value: "live.example", Enabled: true, ExpiresAt: &future},
	})
	if m.Count() != 1 {
		t.Errorf("Count() = %d, want 1 (only the enabled, unexpired rule)", m.Count())
	}
	if r, _ := m.MatchesAsset("ip", "192.0.2.1"); r != nil {
		t.Errorf("disabled rule matched: %v", r)
	}
	if r, _ := m.MatchesAsset("domain", "x.expired.example"); r != nil {
		t.Errorf("expired rule matched: %v", r)
	}
	if r, _ := m.MatchesAsset("domain", "live.example"); r == nil {
		t.Error("enabled unexpired rule did not match")
	}
}

func TestMatcherSourceExclusion(t *testing.T) {
	m := mustMatcher(t, []Rule{sourceRule(1, "shady-feed")})
	if r := m.MatchesSource("shady-feed"); r == nil {
		t.Error("MatchesSource(shady-feed) = nil, want rule")
	}
	if r := m.MatchesSource("other-feed"); r != nil {
		t.Errorf("MatchesSource(other-feed) = %v, want nil", r)
	}
	if r := m.MatchesSource(""); r != nil {
		t.Errorf("MatchesSource(\"\") = %v, want nil", r)
	}
}

func TestMatcherFailClosedOnBadValues(t *testing.T) {
	m := mustMatcher(t, []Rule{ipRule(1, "192.0.2.1")})
	// An invalid value must be an ERROR, never a silent nil (the caller
	// then refuses the record on validation grounds).
	if _, err := m.MatchesAsset("ip", "not-an-ip"); err == nil {
		t.Error("invalid IP: want error")
	}
	if _, err := m.MatchesAsset("domain", "bad..name"); err == nil {
		t.Error("invalid domain: want error")
	}
	if _, err := m.MatchesAsset("ipX", "192.0.2.1"); err == nil {
		t.Error("unknown asset kind: want error")
	}
}

func TestNewRejectsBrokenRules(t *testing.T) {
	if _, err := New([]Rule{{ID: 1, Kind: KindIP, Value: "not-an-ip", Enabled: true}}, time.Now()); err == nil {
		t.Error("invalid IP rule value: want error")
	}
	if _, err := New([]Rule{{ID: 1, Kind: KindDomain, Value: "bad..domain", Enabled: true}}, time.Now()); err == nil {
		t.Error("invalid domain rule value: want error")
	}
	if _, err := New([]Rule{{ID: 1, Kind: "alien", Value: "x", Enabled: true}}, time.Now()); err == nil {
		t.Error("unknown kind: want error")
	}
	// Disabled rules with broken values are skipped, not errors: they
	// never match, and an operator must be able to park a rule.
	if _, err := New([]Rule{{ID: 1, Kind: KindIP, Value: "nope", Enabled: false}}, time.Now()); err != nil {
		t.Errorf("disabled broken rule: %v", err)
	}
}

func TestMatcherZeroValueMatchesNothing(t *testing.T) {
	var m Matcher
	if r := m.MatchesSource("anything"); r != nil {
		t.Errorf("zero Matcher matched source: %v", r)
	}
	if r, err := m.MatchesAsset("ip", "192.0.2.1"); err != nil || r != nil {
		t.Errorf("zero Matcher matched asset: %v, %v", r, err)
	}
}
