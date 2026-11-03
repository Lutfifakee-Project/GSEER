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
	"fmt"
	"net/netip"
	"sort"
	"time"
)

// matcher.go — the compiled, in-memory form of the suppression rule set.
//
// The store builds a Matcher once per ingestion call (LoadSuppressions →
// New) and consults it BEFORE any write.  Matching semantics are mirrored
// byte-for-byte by the DB backstop trigger (migration 0005), so the two
// enforcement layers can never disagree.
//
// Determinism: when several rules could match one asset, the winner is
// defined — exact match beats suffix match, and among CIDRs / suffixes
// the MOST SPECIFIC rule wins (longest prefix / longest suffix).  Ties
// between two equal-specificity rules fall back to the smallest rule id
// so audit output is stable.

// Matcher is a compiled rule set.  Create with New; zero value matches
// nothing.
type Matcher struct {
	ips      map[string]*Rule // canonical IP → rule
	cidrs    []cidrEntry      // sorted most-specific-first
	domains  map[string]*Rule // canonical domain → rule
	suffixes []suffixEntry    // sorted longest-suffix-first
	sources  map[string]*Rule // data_sources.name → rule
	count    int              // effective (enabled, unexpired) rules
}

type cidrEntry struct {
	prefix netip.Prefix
	rule   *Rule
}

type suffixEntry struct {
	suffix string
	rule   *Rule
}

// New compiles rules into a Matcher at instant now.  Disabled and expired
// rules are skipped (they remain in the database for audit but never
// match).  A rule whose value does not canonicalize is a hard error: a
// typo must not silently disable a suppression (fail-closed).
func New(rules []Rule, now time.Time) (*Matcher, error) {
	m := &Matcher{
		ips:     map[string]*Rule{},
		domains: map[string]*Rule{},
		sources: map[string]*Rule{},
	}
	for i := range rules {
		r := rules[i] // copy: the Matcher owns its rules
		if !r.Enabled || r.ExpiredAt(now) {
			continue
		}
		if !ValidateKind(r.Kind) {
			return nil, fmt.Errorf("suppression rule %d: unknown kind %q", r.ID, r.Kind)
		}
		canonical, err := NormalizeValue(r.Kind, r.Value)
		if err != nil {
			return nil, fmt.Errorf("suppression rule %d (%s): %v", r.ID, r.Kind, err)
		}
		r.Value = canonical
		own := r
		switch r.Kind {
		case KindIP:
			m.ips[canonical] = &own
		case KindCIDR:
			p, err := netip.ParsePrefix(canonical)
			if err != nil {
				return nil, fmt.Errorf("suppression rule %d: %v", r.ID, err)
			}
			m.cidrs = append(m.cidrs, cidrEntry{prefix: p, rule: &own})
		case KindDomain:
			m.domains[canonical] = &own
		case KindDomainSuffix:
			m.suffixes = append(m.suffixes, suffixEntry{suffix: canonical, rule: &own})
		case KindSource:
			m.sources[canonical] = &own
		}
		m.count++
	}
	// Most-specific-first ordering for deterministic winners.
	sort.SliceStable(m.cidrs, func(i, j int) bool {
		if m.cidrs[i].prefix.Bits() != m.cidrs[j].prefix.Bits() {
			return m.cidrs[i].prefix.Bits() > m.cidrs[j].prefix.Bits()
		}
		return m.cidrs[i].rule.ID < m.cidrs[j].rule.ID
	})
	sort.SliceStable(m.suffixes, func(i, j int) bool {
		if len(m.suffixes[i].suffix) != len(m.suffixes[j].suffix) {
			return len(m.suffixes[i].suffix) > len(m.suffixes[j].suffix)
		}
		return m.suffixes[i].rule.ID < m.suffixes[j].rule.ID
	})
	return m, nil
}

// Count reports the number of effective (enabled, unexpired) rules.
func (m *Matcher) Count() int { return m.count }

// MatchesAsset returns the rule suppressing an asset of kind assetKind
// ("ip" | "domain" | "hostname") with value v (any case, trailing dots
// and non-punycode accepted — the value is normalized here exactly as it
// will be normalized for storage).  Returns (nil, nil) when not
// suppressed.  An un-normalizable value is an error: the caller (the
// store) rejects such records on validation grounds anyway, and the gate
// must never silently pass a value it could not evaluate (fail-closed).
func (m *Matcher) MatchesAsset(assetKind, v string) (*Rule, error) {
	switch assetKind {
	case "ip":
		canonical, err := NormalizeIP(v)
		if err != nil {
			return nil, err
		}
		if r, ok := m.ips[canonical]; ok {
			return r, nil
		}
		addr, err := netip.ParseAddr(canonical)
		if err != nil {
			return nil, fmt.Errorf("suppression match: %q: %v", v, err)
		}
		for _, e := range m.cidrs {
			if e.prefix.Contains(addr) {
				return e.rule, nil
			}
		}
		return nil, nil
	case "domain", "hostname":
		canonical, err := NormalizeDomain(v)
		if err != nil {
			return nil, err
		}
		if r, ok := m.domains[canonical]; ok {
			return r, nil
		}
		for _, e := range m.suffixes {
			if IsSubdomain(canonical, e.suffix) {
				return e.rule, nil
			}
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("suppression match: unknown asset kind %q", assetKind)
	}
}

// MatchesSource returns the rule excluding the named data source
// (data_sources.name), or nil.  Source names are registry identifiers:
// exact match after trimming, case-preserved.
func (m *Matcher) MatchesSource(name string) *Rule {
	if m == nil {
		return nil
	}
	return m.sources[name]
}
