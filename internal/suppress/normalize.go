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

package suppress

// normalize.go — the canonical-form definitions shared by the suppression
// matcher, the store's asset writes (M13) and the collector ingestion
// pipeline (M14 data quality).  ONE normalization definition, one place:
// the DB backstop trigger compares bytes, so a value that dodges
// normalization would dodge suppression — hence every write path
// normalizes through these functions first.

import (
	"fmt"
	"net/netip"
	"strings"

	"golang.org/x/net/idna"
)

// idnaProfile maps non-ASCII domain text to A-labels using the UTS-46
// lookup mappings (case folding, width folding, delimiter mapping).
//
// Deliberately NOT the strict idna.Lookup profile: that profile enforces
// STD3 ASCII rules and would reject DNS names that are legal in the wild
// (e.g. the underscore labels of _dmarc.example.com or
// _acme-challenge.example.com).  MapForLookup alone still folds case and
// punycodes non-ASCII labels while keeping such names ingesting.  ASCII
// labels are validated locally (see NormalizeDomain).
var idnaProfile = idna.New(idna.MapForLookup())

const (
	maxDomainLen = 253 // presentation length of a fully-qualified DNS name
	maxLabelLen  = 63  // DNS wire label limit
)

// NormalizeDomain canonicalizes a domain or hostname:
//
//   - surrounding whitespace trimmed; embedded whitespace rejected
//   - exactly one trailing root dot stripped ("example.com." →
//     "example.com"); a trailing dot is the FQDN root marker, so
//     "a..b" (which leaves an empty label) is rejected
//   - case-folded; non-ASCII labels mapped to punycode A-labels via the
//     UTS-46 lookup mappings ("München.de" → "xn--mnchen-3ya.de");
//     IDNA mapping is applied PER LABEL with an ASCII fast path so that
//     legal-in-the-wild ASCII service labels (_dmarc, _acme-challenge)
//     survive — IDNA2008 disallows U+005F even in its lookup mappings
//   - every label validated: non-empty, 1..63 chars, only [a-z0-9_-],
//     no leading/trailing hyphen
//   - total length ≤ 253
//
// Already-punycode input is returned unchanged (idempotent).  The empty
// string is an error.
func NormalizeDomain(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("normalize domain: empty value")
	}
	if strings.ContainsAny(s, " \t\r\n\v\f") {
		return "", fmt.Errorf("normalize domain: %q contains whitespace", s)
	}
	s = strings.TrimSuffix(s, ".") // single root-dot marker; "a.." fails below
	if s == "" {
		return "", fmt.Errorf("normalize domain: root-only name")
	}
	raw := strings.Split(s, ".")
	labels := make([]string, 0, len(raw))
	for _, label := range raw {
		if label == "" {
			return "", fmt.Errorf("normalize domain: %q: empty label", s)
		}
		if isASCII(label) {
			labels = append(labels, strings.ToLower(label))
			continue
		}
		// Non-ASCII label: apply the UTS-46 lookup mappings.  The
		// mapping may fold a fullwidth delimiter ("．") into an ASCII
		// dot, so the mapped text is re-split into labels.
		mapped, err := idnaProfile.ToASCII(label)
		if err != nil {
			return "", fmt.Errorf("normalize domain: %q: label %q: %v", s, label, err)
		}
		mapped = strings.ToLower(mapped)
		for _, part := range strings.Split(mapped, ".") {
			if part == "" {
				return "", fmt.Errorf("normalize domain: %q: empty label after IDNA mapping", s)
			}
			labels = append(labels, part)
		}
	}
	s = strings.Join(labels, ".")
	if len(s) > maxDomainLen {
		return "", fmt.Errorf("normalize domain: %d chars exceeds the %d-char DNS limit", len(s), maxDomainLen)
	}
	for _, label := range labels {
		if err := validateLabel(label); err != nil {
			return "", fmt.Errorf("normalize domain: %q: %v", s, err)
		}
	}
	return s, nil
}

// validateLabel enforces one canonical DNS label: 1..63 chars, only
// [a-z0-9_-], no leading or trailing hyphen.  Underscore labels are
// accepted (real-world service labels like _dmarc); everything outside
// the set is rejected so the byte-exact DB trigger can never be dodged
// by look-alike characters.
func validateLabel(label string) error {
	if label == "" {
		return fmt.Errorf("empty label")
	}
	if len(label) > maxLabelLen {
		return fmt.Errorf("label %q exceeds %d chars", label, maxLabelLen)
	}
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return fmt.Errorf("label %q has a leading or trailing hyphen", label)
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return fmt.Errorf("label %q contains disallowed character %q", label, r)
		}
	}
	return nil
}

// NormalizeIP canonicalizes an IPv4 or IPv6 address with netip: zero
// compression and lowercase for IPv6 ("2001:0DB8:0000::1" →
// "2001:db8::1"), dotted-quad for IPv4, the IPv6 scope zone stripped
// ("fe80::1%eth0" → "fe80::1"), IPv4-mapped forms printed in their
// mixed notation consistently.  Idempotent: canonical input round-trips.
func NormalizeIP(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("normalize ip: empty value")
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return "", fmt.Errorf("normalize ip: %q: %v", s, err)
	}
	addr = addr.WithZone("") // scope zones never reach storage
	return addr.String(), nil
}

// NormalizeCIDR canonicalizes an IPv4 or IPv6 CIDR prefix: host bits are
// masked off ("10.1.2.3/8" → "10.0.0.0/8", "2001:db8::1/32" →
// "2001:db8::/32") and the address half is canonicalized as in
// NormalizeIP.  Idempotent.
func NormalizeCIDR(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("normalize cidr: empty value")
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return "", fmt.Errorf("normalize cidr: %q: %v", s, err)
	}
	return p.Masked().String(), nil
}

// NormalizeValue canonicalizes by rule kind: KindIP → NormalizeIP,
// KindCIDR → NormalizeCIDR, KindDomain/KindDomainSuffix → NormalizeDomain,
// KindSource → trimmed exact name (source names are registry identifiers,
// not DNS names: case-preserved, no IDNA).
func NormalizeValue(kind Kind, value string) (string, error) {
	switch kind {
	case KindIP:
		return NormalizeIP(value)
	case KindCIDR:
		return NormalizeCIDR(value)
	case KindDomain, KindDomainSuffix:
		return NormalizeDomain(value)
	case KindSource:
		v := strings.TrimSpace(value)
		if v == "" {
			return "", fmt.Errorf("normalize source: empty value")
		}
		return v, nil
	default:
		return "", fmt.Errorf("normalize: unknown rule kind %q", kind)
	}
}

// IsSubdomain reports whether host equals suffix or is a descendant of
// suffix at a STRICT LABEL boundary.  Both arguments must already be
// canonical (NormalizeDomain): trailing dots are gone, case is folded.
//
//	IsSubdomain("example.com", "example.com")          → true
//	IsSubdomain("sub.example.com", "example.com")      → true
//	IsSubdomain("deep.a.b.example.com", "example.com") → true
//	IsSubdomain("notexample.com", "example.com")       → false
//	IsSubdomain("example.com.evil.net", "example.com") → false
//	IsSubdomain("badexample.com", "badexample.co")     → false (no partial label)
func IsSubdomain(host, suffix string) bool {
	if host == "" || suffix == "" {
		return false
	}
	if host == suffix {
		return true
	}
	return strings.HasSuffix(host, "."+suffix)
}

// isASCII reports whether s is pure ASCII (the IDNA fast-path guard).
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
