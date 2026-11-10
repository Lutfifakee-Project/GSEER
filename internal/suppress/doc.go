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

// Package suppress is the centralized suppression policy layer (v0.0.2
// Passive Intelligence, milestone M13, owner directive §3).
//
// A suppression rule removes classes of records from GSEER's data plane:
//
//	KindIP           exact IP address (canonical netip form; IPv4 + IPv6)
//	KindCIDR         network range (masked prefix; containment match)
//	KindDomain       exact domain or hostname (canonical IDNA/lowercase)
//	KindDomainSuffix the exact name plus every descendant that matches at
//	                 a STRICT LABEL boundary (sub.example.com matches
//	                 suffix example.com; notexample.com does not)
//	KindSource       a registered data_sources.name: everything that
//	                 source delivers is refused before ingestion
//
// Rules carry reason + created_by + created_at audit metadata, an optional
// expiration (expired rules stop matching but remain listed for audit) and
// an enabled flag.
//
// # Enforcement model — two layers, fail-closed
//
//  1. Go gate (this package + internal/storage/postgres): every ingestion
//     path (IngestFact, IngestFacts, UpsertAsset, UpsertService) compiles
//     the rule set into a Matcher and refuses matching facts BEFORE any
//     write.  Suppressed facts are skipped cleanly (IngestStats.Suppressed)
//     and audited in suppression_applications.
//
//  2. DB backstop (migration 0005): a BEFORE INSERT/UPDATE trigger on
//     assets re-evaluates the rules inside PostgreSQL with the exact same
//     matching semantics.  Any future ingestion path that forgets the Go
//     gate — a new collector, a bulk loader, a psql session — is rejected
//     by the database itself (custom ERRCODE GSUP1, mapped to
//     E_DB_SUPPRESSED).
//
// # Canonical values
//
// Matching is exact-string and therefore only correct if both the rules
// and the stored asset values are CANONICAL:
//
//   - domains/hostnames: lowercase, no trailing root dot, IDNA punycode
//     (A-labels) for non-ASCII labels, non-empty labels
//   - IPs: netip canonical form, zone stripped (IPv4 + IPv6)
//   - CIDRs: masked (10.1.2.3/8 → 10.0.0.0/8)
//
// The store normalizes every asset write through NormalizeDomain /
// NormalizeIP / NormalizeCIDR, so the search index, the DB trigger and the
// Go matcher all see the same byte sequences.  NormalizeXxx are exported
// here because v0.0.2 data-quality (M14) applies the same functions in the
// collector ingestion pipeline — one normalization definition, one place.
//
// # Deliberate non-features
//
//   - Cross-type propagation is NOT automatic: a domain rule does not
//     block the IPs the domain resolves to, and an IP rule does not block
//     reverse-DNS names.  Identifier types are suppressed by identifier
//     type; propagation is a policy decision for the operator.
//   - Matching is name-based, never content-based; there is no regex rule
//     kind (regex rules are un-auditable at enforcement time).
//   - Expired rules are never auto-deleted; they stay for audit.
//
// Layering (AC-3.1): this package imports only the standard library and
// golang.org/x/net/idna.  It never imports storage, pgx, collectors or
// egress; the storage layer imports IT.
package suppress

import "time"

// Kind enumerates the suppression rule kinds (migration 0005
// suppressions.kind CHECK constraint).
type Kind string

const (
	// KindIP matches kind='ip' assets by exact canonical address.
	KindIP Kind = "ip"
	// KindCIDR matches kind='ip' assets whose address is inside the
	// (masked) prefix.
	KindCIDR Kind = "cidr"
	// KindDomain matches kind='domain' and kind='hostname' assets by
	// exact canonical name.
	KindDomain Kind = "domain"
	// KindDomainSuffix matches kind='domain' and kind='hostname' assets
	// whose canonical name equals the value or ends with "." + value
	// (strict label boundary).
	KindDomainSuffix Kind = "domain_suffix"
	// KindSource excludes everything a registered data source delivers.
	// The value is the data_sources.name.
	KindSource Kind = "source"
)

// Rule is one suppression policy row (suppressions table).
type Rule struct {
	ID        int64      `json:"id"`
	Kind      Kind       `json:"kind"`
	Value     string     `json:"value"` // canonical form
	Reason    string     `json:"reason"`
	CreatedBy string     `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // nil = never expires
	Enabled   bool       `json:"enabled"`
}

// ExpiredAt reports whether the rule is expired at the given instant.
func (r Rule) ExpiredAt(now time.Time) bool {
	return r.ExpiresAt != nil && !r.ExpiresAt.After(now)
}

// ValidateKind reports whether k is one of the five rule kinds.
func ValidateKind(k Kind) bool {
	switch k {
	case KindIP, KindCIDR, KindDomain, KindDomainSuffix, KindSource:
		return true
	}
	return false
}
