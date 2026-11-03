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

package collect

// dns.go — the operator-domain DNS connector (v0.0.2 P1).
//
// WHAT IT DOES: resolves names the OPERATOR or the index already named
// (seed_mode "config" or "index") through standard DNS recursion — the
// protocol every system on the internet speaks — and records the factual
// answers as asset-level facts (A/AAAA → ip assets; CNAME targets →
// hostname assets).
//
// WHAT IT NEVER DOES: no zone transfers, no brute-force enumeration, no
// guessing of subdomains, no NXDOMAIN-walking.  The seed list bounds the
// queries; the DNS answers bound the facts.
//
// Discovery ≠ observation (P1 contract 3): "example.com resolved to
// 203.0.113.7 at T" is a FACT about publication; it is NOT evidence that
// a service answered on any port.  No service rows are produced.
//
// Terms verdict — reviewed-enabled.  DNS resolution is the normal
// operation of a standard protocol against the operator's resolver; the
// operator chooses the resolver (config "resolver", default the system's
// — GSEER never hardcodes a third-party resolver).  Cloudflare/Google
// public resolvers document standard usage as permitted; GSEER issues
// ordinary recursive queries at conservative rates and caches answers
// (the index IS the cache).  Full review: docs/SOURCES.md §C.

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

// dnsConnector records DNS answers about operator-named names.
type dnsConnector struct{}

// Kind implements Collector.
func (dnsConnector) Kind() string { return "dns" }

// Review implements Collector (docs/SOURCES.md §C).
func (dnsConnector) Review() Review {
	return Review{
		Verdict: ReviewEnabled,
		Why:     "standard DNS recursion via the operator's resolver; answers are protocol facts, the operator names every target",
		DocRef:  "docs/SOURCES.md §C (Public DNS)",
	}
}

// Collect implements Collector: one point-lookup pass over the seeds.
func (dnsConnector) Collect(ctx Ctx) (*Batch, error) {
	names, err := seeds(ctx, "domain")
	if err != nil {
		return nil, err
	}
	r := dnsResolver(ctx.Config.Resolver)
	now := time.Now().UTC()
	raws := make([]RawFact, 0, len(names))
	lookups, answers := 0, 0
	var lookupErrs []error
	for _, name := range names {
		if err := ctx.Context.Err(); err != nil {
			return nil, err
		}
		canonical, nerr := suppress.NormalizeDomain(name)
		if nerr != nil {
			raws = append(raws, RawFact{AssetKind: "domain", Value: name, ObservedAt: now})
			// the pipeline dead-letters the invalid seed with its reason
			continue
		}
		lookups++

		addrs, aerr := lookupIPAddr(ctx.Context, r, canonical)
		if aerr == nil {
			for _, a := range addrs {
				ip := a.IP.String()
				answers++
				raws = append(raws, RawFact{
					AssetKind:  "ip",
					Value:      ip,
					ObservedAt: now,
					Confidence: "medium", // recursive answers may be cached
					Extra: map[string]any{
						"dns": map[string]any{"query": canonical, "type": addrFamily(a.IP)},
					},
				})
			}
		} else {
			lookupErrs = append(lookupErrs, aerr)
		}

		target, cerr := lookupCNAME(ctx.Context, r, canonical)
		if cerr == nil && target != "" && target != canonical && target != canonical+"." {
			if cn, nerr := suppress.NormalizeDomain(target); nerr == nil && cn != canonical {
				answers++
				raws = append(raws, RawFact{
					AssetKind:  "hostname",
					Value:      cn,
					ObservedAt: now,
					Confidence: "medium",
					Extra: map[string]any{
						"dns": map[string]any{"query": canonical, "type": "CNAME"},
					},
				})
			}
		}
	}
	// Every lookup failed → the transport is down, retryable.  Partial
	// failures are normal DNS (NXDOMAIN lives in answers==0, not errors).
	if lookups > 0 && answers == 0 && len(lookupErrs) == lookups {
		return nil, fmt.Errorf("dns: all %d lookups failed (resolver %q): %v",
			lookups, resolverLabel(ctx.Config.Resolver), lookupErrs[0])
	}
	return &Batch{
		Raws: raws,
		Done: true, // point lookups: one batch per pass, no pagination
		Notes: map[string]int{
			"lookups": lookups, "answers": answers, "seeds": len(names),
		},
	}, nil
}

// seeds resolves the seed list: config mode uses the configured names,
// index mode reads identifiers already in the operator's index.
func seeds(ctx Ctx, indexKind string) ([]string, error) {
	switch ctx.Config.SeedMode {
	case "", "config":
		return ctx.Config.Seeds, nil
	case "index":
		return ctx.IndexSeeds(indexKind, ctx.Limit)
	default:
		return nil, Fatal(fmt.Errorf("dns: unknown seed_mode %q (want \"config\" or \"index\")", ctx.Config.SeedMode))
	}
}

// dnsResolver builds the resolver: the system's by default, or the
// operator-configured "host:port" — never a hardcoded third party.
func dnsResolver(addr string) *net.Resolver {
	if addr == "" {
		return net.DefaultResolver
	}
	d := &net.Dialer{Timeout: 5 * time.Second}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			// The resolver's own address list is ignored: every query
			// goes to the operator-configured server.
			return d.DialContext(ctx, network, addr)
		},
	}
}

func resolverLabel(addr string) string {
	if addr == "" {
		return "system"
	}
	return addr
}

// addrFamily labels the record type the answer arrived as (A vs AAAA).
func addrFamily(ip net.IP) string {
	if ip.To4() != nil {
		return "A"
	}
	return "AAAA"
}

// DNS lookup seams (tests fake them; production resolves through r).
var (
	lookupIPAddr = func(ctx context.Context, r *net.Resolver, host string) ([]net.IPAddr, error) {
		return r.LookupIPAddr(ctx, host)
	}
	lookupCNAME = func(ctx context.Context, r *net.Resolver, host string) (string, error) {
		return r.LookupCNAME(ctx, host)
	}
)

func init() { register(dnsConnector{}) }
