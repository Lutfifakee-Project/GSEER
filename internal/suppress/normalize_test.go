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
	"strings"
	"testing"
	"time"
)

// normalize_test.go — the owner-mandated normalization test matrix:
// IPv4, IPv6, IDNA, trailing-dot and domain label boundaries (directive
// §3 "Add IPv4, IPv6, IDNA, trailing-dot, and domain-boundary tests").

func TestNormalizeDomain(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		// Plain ASCII round-trip + case folding.
		{"example.com", "example.com", false},
		{"EXAMPLE.COM", "example.com", false},
		{"WwW.ExAmPlE.CoM", "www.example.com", false},
		// Trailing-dot handling (the FQDN root marker).
		{"example.com.", "example.com", false},
		{"EXAMPLE.COM.", "example.com", false},
		{".", "", true},         // root-only name
		{"a..b", "", true},      // empty label after root-dot strip
		{"a...b.com", "", true}, // empty labels
		{"", "", true},          // empty
		{"   ", "", true},       // whitespace only
		// IDNA: unicode → punycode (A-labels), already-punycode idempotent.
		{"münchen.de", "xn--mnchen-3ya.de", false},
		{"MÜNCHEN.DE", "xn--mnchen-3ya.de", false},
		{"xn--mnchen-3ya.de", "xn--mnchen-3ya.de", false},
		{"bücher.example", "xn--bcher-kva.example", false},
		{"日本.jp", "xn--wgv71a.jp", false},
		// Fullwidth characters fold via UTS-46 lookup mappings.
		{"ＥＸＡＭＰＬＥ．ＣＯＭ", "example.com", false},
		// Underscore service labels are legal DNS names and must ingest.
		{"_dmarc.example.com", "_dmarc.example.com", false},
		{"_ACME-Challenge.Example.COM", "_acme-challenge.example.com", false},
		{"sub_domain.example.com", "sub_domain.example.com", false},
		// Whitespace anywhere is rejected.
		{"bad example.com", "", true},
		{"bad\texample.com", "", true},
		// Disallowed characters.
		{"bad!label.example.com", "", true},
		{"ex ample.com", "", true},
		// Hyphen boundaries.
		{"-leading.example.com", "", true},
		{"trailing-.example.com", "", true},
		{"mid-dle.example.com", "mid-dle.example.com", false},
		// Length limits.
		{strings.Repeat("a", 64) + ".com", "", true},                                                  // label > 63
		{strings.Repeat("a.", 126) + "com", "", true},                                                 // > 253 total
		{strings.Repeat("ab.", 80) + "example.com", strings.Repeat("ab.", 80) + "example.com", false}, // 251 chars ok
	}
	for _, tc := range cases {
		got, err := NormalizeDomain(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeDomain(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeDomain(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeDomain(%q) = %q, want %q", tc.in, got, tc.want)
		}
		// Idempotency: canonical input round-trips.
		again, err := NormalizeDomain(got)
		if err != nil || again != got {
			t.Errorf("NormalizeDomain(%q) not idempotent: %q, %v", got, again, err)
		}
	}
}

func TestNormalizeIP(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		// IPv4.
		{"192.0.2.1", "192.0.2.1", false},
		{" 192.0.2.1 ", "192.0.2.1", false},
		{"203.0.113.255", "203.0.113.255", false},
		{"256.0.2.1", "", true},
		{"192.0.2", "", true},
		{"not-an-ip", "", true},
		{"", "", true},
		// IPv6 canonicalization: zero compression + lowercase.
		{"2001:0DB8:0000:0000:0000:0000:0000:0001", "2001:db8::1", false},
		{"2001:db8::1", "2001:db8::1", false},
		{"::1", "::1", false},
		{"::", "::", false},
		{"2001:DB8::1", "2001:db8::1", false},
		// Scope zone stripped.
		{"fe80::1%eth0", "fe80::1", false},
		// IPv4-mapped canonicalizes consistently in mixed notation.
		{"::ffff:192.0.2.1", "::ffff:192.0.2.1", false},
		{"::FFFF:192.0.2.129", "::ffff:192.0.2.129", false},
	}
	for _, tc := range cases {
		got, err := NormalizeIP(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeIP(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeIP(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeIP(%q) = %q, want %q", tc.in, got, tc.want)
		}
		again, err := NormalizeIP(got)
		if err != nil || again != got {
			t.Errorf("NormalizeIP(%q) not idempotent: %q, %v", got, again, err)
		}
	}
}

func TestNormalizeCIDR(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		// Host bits masked off.
		{"10.1.2.3/8", "10.0.0.0/8", false},
		{"203.0.113.0/24", "203.0.113.0/24", false},
		{"0.0.0.0/0", "0.0.0.0/0", false},
		{"2001:db8:aaaa:bbbb::1/32", "2001:db8::/32", false},
		{"2001:DB8::/32", "2001:db8::/32", false},
		{"2001:db8::/128", "2001:db8::/128", false},
		// Trailing garbage, missing bits.
		{"10.0.0.0", "", true},
		{"10.0.0.0/33", "", true},
		{"10.0.0.0/-1", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		got, err := NormalizeCIDR(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeCIDR(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeCIDR(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeCIDR(%q) = %q, want %q", tc.in, got, tc.want)
		}
		again, err := NormalizeCIDR(got)
		if err != nil || again != got {
			t.Errorf("NormalizeCIDR(%q) not idempotent: %q, %v", got, again, err)
		}
	}
}

func TestIsSubdomainLabelBoundaries(t *testing.T) {
	cases := []struct {
		host, suffix string
		want         bool
	}{
		// Exact match is a match.
		{"example.com", "example.com", true},
		// Strict dot-boundary descendants.
		{"sub.example.com", "example.com", true},
		{"deep.a.b.example.com", "example.com", true},
		// The classic bypass attempts: partial-label and suffix-of-suffix.
		{"notexample.com", "example.com", false},
		{"example.com.evil.net", "example.com", false},
		{"badexample.com", "badexample.co", false},
		{"example.comx", "example.com", false},
		// Suffix must be the tail, never an interior label run.
		{"example.com.sub.net", "example.com", false},
		// Empty inputs never match.
		{"", "example.com", false},
		{"example.com", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		if got := IsSubdomain(tc.host, tc.suffix); got != tc.want {
			t.Errorf("IsSubdomain(%q, %q) = %v, want %v", tc.host, tc.suffix, got, tc.want)
		}
	}
}

// TestNormalizeDomainUnderscoreViaIDNA guards the profile decision: the
// UTS-46 lookup mappings must not reject underscore labels even when the
// input routes through the non-ASCII code path.
func TestNormalizeDomainUnderscoreViaIDNA(t *testing.T) {
	// A domain whose OTHER label is non-ASCII forces the IDNA path while
	// the underscore label must survive it.
	got, err := NormalizeDomain("_dmarc.münchen.de")
	if err != nil {
		t.Fatalf("NormalizeDomain(_dmarc.münchen.de) error: %v", err)
	}
	if want := "_dmarc.xn--mnchen-3ya.de"; got != want {
		t.Errorf("NormalizeDomain(_dmarc.münchen.de) = %q, want %q", got, want)
	}
}

func TestRuleExpiredAt(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	never := Rule{}
	if never.ExpiredAt(now) {
		t.Error("nil expiry must never be expired")
	}
	future := now.Add(time.Hour)
	r := Rule{ExpiresAt: &future}
	if r.ExpiredAt(now) {
		t.Error("future expiry must not be expired")
	}
	if !r.ExpiredAt(future) {
		t.Error("expiry at exactly now must be expired (After semantics)")
	}
	past := now.Add(-time.Second)
	if !(Rule{ExpiresAt: &past}.ExpiredAt(now)) {
		t.Error("past expiry must be expired")
	}
}

func TestValidateKind(t *testing.T) {
	for _, k := range []Kind{KindIP, KindCIDR, KindDomain, KindDomainSuffix, KindSource} {
		if !ValidateKind(k) {
			t.Errorf("ValidateKind(%q) = false, want true", k)
		}
	}
	if ValidateKind("wildcard") {
		t.Error(`ValidateKind("wildcard") = true, want false`)
	}
	if ValidateKind("") {
		t.Error(`ValidateKind("") = true, want false`)
	}
}
