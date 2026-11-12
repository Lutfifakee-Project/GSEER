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

// ip2asn.go — the ASN/BGP enrichment connector (v0.0.2 P1), backed by
// Team Cymru's IP-to-ASN mapping service over its DNS interfaces.
//
// WHAT IT DOES: for IP assets the operator's index already holds (seed_mode
// "index") or the operator names in config, it asks Team Cymru's
// IP-to-ASN DNS service for the origin ASN, prefix, country and registry,
// plus the AS name from the companion ASN interface — and writes the
// answer as an ENRICHMENT fact on that same IP asset (asn / asn_org /
// geo_country columns; raw answer lines preserved in evidence).
//
// Terms verdict — reviewed-enabled.  Team Cymru publishes the service as
// "Free, forever" community infrastructure and asks volume users to use
// the bulk or DNS interfaces ("The only ask is that you use bulk mode or
// DNS at volume").  GSEER uses the DNS interface (the lighter of the two),
// issues one lookup per identifier (answered lookups are cached by the
// index — re-runs dedupe at L2 and re-ask only fresh seeds), and caps
// runs through limit_per_run.  Full review: docs/SOURCES.md §D.
//
// Discovery ≠ observation: an origin-AS answer is BGP/routing publication
// data.  It says nothing about which ports answer on the address.

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Lutfifakee-Project/gseer/internal/suppress"
)

// Team Cymru DNS interface bases (docs/SOURCES.md §D; overridable in
// config only for tests — production defaults are the documented ones).
const (
	ip2ASNDomainV4 = "origin.asn.cymru.com"
	ip2ASNDomainV6 = "origin6.asn.cymru.com"
	asnNameDomain  = "asn.cymru.com"
)

// ip2asnConnector enriches IP assets with origin-AS data.
type ip2asnConnector struct{}

// Kind implements Collector.
func (ip2asnConnector) Kind() string { return "ip2asn" }

// Review implements Collector (docs/SOURCES.md §D).
func (ip2asnConnector) Review() Review {
	return Review{
		Verdict: ReviewEnabled,
		Why:     "free community IP-to-ASN service over its DNS interface, conservative rates, answers cached in the index",
		DocRef:  "docs/SOURCES.md §D (ASN/BGP IP-to-ASN)",
	}
}

// Collect implements Collector: one enrichment pass over the seeds.
func (ip2asnConnector) Collect(ctx Ctx) (*Batch, error) {
	if ctx.Config.SeedMode == "index" && ctx.Config.SeedKind != "" && ctx.Config.SeedKind != "ip" {
		return nil, Fatal(fmt.Errorf("ip2asn: seed_kind %q not enrichable (want \"ip\")", ctx.Config.SeedKind))
	}
	var names []string
	var err error
	if ctx.Config.SeedMode == "config" {
		names = ctx.Config.Seeds
	} else {
		names, err = ctx.IndexSeeds("ip", ctx.Limit)
		if err != nil {
			return nil, err
		}
	}

	r := dnsResolver(ctx.Config.Resolver)
	now := time.Now().UTC()
	raws := make([]RawFact, 0, len(names))
	asks, hits := 0, 0
	var lookupErrs []error

	for _, seed := range names {
		if err := ctx.Context.Err(); err != nil {
			return nil, err
		}
		addr, nerr := suppress.NormalizeIP(seed)
		if nerr != nil {
			raws = append(raws, RawFact{AssetKind: "ip", Value: seed, ObservedAt: now})
			continue // pipeline dead-letters the invalid seed
		}
		asks++

		txt, qerr := lookupTXT(ctx.Context, r, reverseIP(addr)+"."+ip2ASNDomainOf(addr))
		if qerr != nil {
			lookupErrs = append(lookupErrs, qerr)
			continue
		}
		if len(txt) == 0 {
			continue // no route/answer published for this address
		}
		rec, perr := parseCymruOrigin(txt[0])
		if perr != nil {
			// Malformed answer: keep it for audit, do not enrich.
			raws = append(raws, RawFact{
				AssetKind: "ip", Value: addr, ObservedAt: now, Confidence: "low",
				Extra: map[string]any{"ip2asn": map[string]any{"raw": strings.Join(txt, " | "), "parse_error": perr.Error()}},
			})
			hits++
			continue
		}

		// AS name from the companion interface (best effort — a miss
		// never blocks the ASN enrichment itself).
		asName := ""
		if rec.ASN > 0 {
			nameTXT, nerr := lookupTXT(ctx.Context, r, fmt.Sprintf("AS%d.%s", rec.ASN, asnNameDomain))
			if nerr == nil && len(nameTXT) > 0 {
				asName = parseCymruASName(nameTXT[0])
			}
		}

		hits++
		raws = append(raws, RawFact{
			AssetKind:  "ip",
			Value:      addr,
			ObservedAt: now,
			Confidence: "medium", // routing data is point-in-time
			ASN:        rec.ASN,
			ASNOrg:     asName,
			GeoCountry: rec.Country,
			Extra: map[string]any{
				"ip2asn": map[string]any{
					"asn": rec.ASN, "prefix": rec.Prefix, "country": rec.Country,
					"registry": rec.Registry, "allocated": rec.Allocated, "raw": rec.Raw,
				},
			},
		})
	}

	// Every lookup failed → transport down, retryable.
	if asks > 0 && hits == 0 && len(lookupErrs) == asks {
		return nil, fmt.Errorf("ip2asn: all %d lookups failed (resolver %q): %v",
			asks, resolverLabel(ctx.Config.Resolver), lookupErrs[0])
	}
	return &Batch{
		Raws:  raws,
		Done:  true,
		Notes: map[string]int{"asks": asks, "hits": hits, "seeds": len(names)},
	}, nil
}

// cymruOrigin is one parsed origin-AS answer.
type cymruOrigin struct {
	ASN       int
	Prefix    string
	Country   string
	Registry  string
	Allocated string
	Raw       string
}

// parseCymruOrigin parses the canonical IP-to-ASN TXT format
// "64500 | 203.0.113.0/24 | US | arin | 2002-04-23" (fields may vary in
// count across responses; the first four are the contract).
func parseCymruOrigin(line string) (cymruOrigin, error) {
	fields := strings.Split(line, "|")
	trim := func(i int) string {
		if i < len(fields) {
			return strings.TrimSpace(fields[i])
		}
		return ""
	}
	out := cymruOrigin{Raw: line, Prefix: trim(1), Country: trim(2), Registry: trim(3), Allocated: trim(4)}
	asn, err := strconv.Atoi(trim(0))
	if err != nil {
		return out, fmt.Errorf("origin answer %q: ASN field: %v", line, err)
	}
	if asn <= 0 || asn > 4294967295 {
		return out, fmt.Errorf("origin answer %q: ASN %d out of range", line, asn)
	}
	out.ASN = asn
	if out.Prefix == "" {
		return out, fmt.Errorf("origin answer %q: empty prefix field", line)
	}
	return out, nil
}

// parseCymruASName extracts the AS name from an asn.cymru.com TXT line
// defensively.  The documented live format is FIVE pipe-separated fields
// (verified against the production service 2026-11):
//
//	"AS15169 | US | arin | 2000-03-30 | GOOGLE - Google LLC, US"
//	  ASN    | CC | registry | allocated | AS NAME
//
// so the name is the FIFTH field.  Anything else — fewer fields, an
// empty name — returns "" (the caller treats the name as best-effort
// enrichment and never blocks the ASN itself on it).
func parseCymruASName(line string) string {
	fields := strings.Split(line, "|")
	if len(fields) < 5 {
		return ""
	}
	return strings.TrimSpace(fields[4])
}

// ip2ASNDomainOf picks the v4/v6 interface by address family.
func ip2ASNDomainOf(addr string) string {
	if isIPv6(addr) {
		return ip2ASNDomainV6
	}
	return ip2ASNDomainV4
}

// reverseIP renders the address in the DNS-name order Team Cymru's
// interfaces expect: IPv4 octets reversed ("203.0.113.7" →
// "7.113.0.203"); IPv6 nibbles reversed (RFC 4291 §2.2 ip6.arpa order).
func reverseIP(addr string) string {
	if isIPv6(addr) {
		return reverseIPv6(addr)
	}
	parts := strings.Split(addr, ".")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, ".")
}

func isIPv6(addr string) bool { return strings.Contains(addr, ":") }

// reverseIPv6 expands the address and reverses its 32 nibbles
// ("2001:db8::1" → "1.0.0.….2.0.0.2"), the order ip6.arpa-style lookups
// expect WITHOUT the .ip6.arpa suffix (the interface domain supplies it).
func reverseIPv6(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil {
		return ""
	}
	b := ip.To16()
	if b == nil {
		return ""
	}
	// Nibbles, reversed: low nibble of last byte first; rendered as hex
	// digits (nibbles 10-15 are letters — '0'+n would emit control-range
	// garbage like ':' and ';').
	const hexDigits = "0123456789abcdef"
	nibbles := make([]byte, 0, 32)
	for i := len(b) - 1; i >= 0; i-- {
		nibbles = append(nibbles, b[i]&0x0f, b[i]>>4)
	}
	out := make([]byte, 0, 63)
	for i, n := range nibbles {
		if i > 0 {
			out = append(out, '.')
		}
		out = append(out, hexDigits[n])
	}
	return string(out)
}

// lookupTXT is the seam tests fake: one TXT query through r.
var lookupTXT = func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
	return r.LookupTXT(ctx, name)
}

func init() { register(ip2asnConnector{}) }
