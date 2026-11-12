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

// ip2asn_test.go — the IP-to-ASN parser tests.  The quoted TXT lines are
// the PRODUCTION format captured live from the provider's documented
// origin/AS-name DNS interfaces (docs/SOURCES.md §D) in 2026-11 and keep
// their real field shapes; they are fixtures of the wire format, not
// observations.

import "testing"

// TestParseCymruASNameLiveFormat pins the five-field AS-name format:
// "AS{n} | CC | registry | allocated | AS NAME" — the name is field 5.
// (Regression: an earlier parser read field 3 and stored the REGISTRY —
// "arin" — as the AS organisation.)
func TestParseCymruASNameLiveFormat(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			name: "AS15169 live answer",
			line: "15169 | US | arin | 2000-03-30 | GOOGLE - Google LLC, US",
			want: "GOOGLE - Google LLC, US",
		},
		{
			name: "AS13335 live answer",
			line: "13335 | US | arin | 2010-07-14 | CLOUDFLARENET - Cloudflare, Inc., US",
			want: "CLOUDFLARENET - Cloudflare, Inc., US",
		},
		{
			name: "name is exactly field 5 (extra fields are not part of the format)",
			line: "64500 | US | arin | 2002-04-23 | EXAMPLE-AS - Example, US | extra",
			want: "EXAMPLE-AS - Example, US",
		},
		{
			name: "empty name field",
			line: "64500 | US | arin | 2002-04-23 | ",
			want: "",
		},
		{
			name: "registry-only three-field shape yields empty (never 'arin')",
			line: "15169 | US | arin",
			want: "",
		},
		{
			name: "two-field shape yields empty",
			line: "15169 | US",
			want: "",
		},
		{
			name: "empty input yields empty",
			line: "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCymruASName(tc.line); got != tc.want {
				t.Fatalf("parseCymruASName(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

// TestParseCymruOriginFixtures covers the canonical origin answer shapes:
// five fields with the allocation date, and the documented four-field
// variant without one.
func TestParseCymruOriginFixtures(t *testing.T) {
	t.Run("five-field origin line", func(t *testing.T) {
		rec, err := parseCymruOrigin("15169 | 8.8.8.0/24 | US | arin | 2023-12-28")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rec.ASN != 15169 || rec.Prefix != "8.8.8.0/24" || rec.Country != "US" || rec.Registry != "arin" || rec.Allocated != "2023-12-28" {
			t.Fatalf("parsed record wrong: %+v", rec)
		}
	})
	t.Run("four-field origin line (no allocation date)", func(t *testing.T) {
		rec, err := parseCymruOrigin("64500 | 203.0.113.0/24 | US | arin")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rec.ASN != 64500 || rec.Prefix != "203.0.113.0/24" || rec.Country != "US" || rec.Registry != "arin" || rec.Allocated != "" {
			t.Fatalf("parsed record wrong: %+v", rec)
		}
	})
	t.Run("non-numeric ASN rejected", func(t *testing.T) {
		if _, err := parseCymruOrigin("not-an-asn | 203.0.113.0/24 | US | arin"); err == nil {
			t.Fatal("expected an error for a non-numeric ASN field")
		}
	})
	t.Run("empty prefix rejected", func(t *testing.T) {
		if _, err := parseCymruOrigin("64500 |  | US | arin"); err == nil {
			t.Fatal("expected an error for an empty prefix field")
		}
	})
}

// TestReverseIPFixtures pins the reverse-DNS label orders both IP-to-ASN
// interfaces expect (RFC 4291 §2.2 nibble order for IPv6).
func TestReverseIPFixtures(t *testing.T) {
	v4 := reverseIP("8.8.8.8")
	if v4 != "8.8.8.8" {
		t.Fatalf("reverseIP(8.8.8.8) = %q, want \"8.8.8.8\"", v4)
	}
	v4b := reverseIP("203.0.113.7")
	if v4b != "7.113.0.203" {
		t.Fatalf("reverseIP(203.0.113.7) = %q, want \"7.113.0.203\"", v4b)
	}
	v6 := reverseIP("2001:db8::1")
	// 2001:0db8:0000:0000:0000:0000:0000:0001 reversed nibble order.
	want := "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2"
	if v6 != want {
		t.Fatalf("reverseIP(2001:db8::1) = %q, want %q", v6, want)
	}
}
