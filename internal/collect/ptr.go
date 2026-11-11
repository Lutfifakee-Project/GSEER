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

// ptr.go — the reverse-DNS (PTR) connector (v0.3.0).
//
// WHAT IT DOES: resolves pointer records for IP addresses the OPERATOR
// or the index already named (seed_mode "config" or "index") through
// standard DNS recursion — the same protocol and the same resolver the
// `dns` connector speaks — and records the factual answers as
// asset-level hostname facts.  A PTR answer ("203.0.113.10 pointed at
// portal.example.test at T") is a publication fact about the address's
// operator; it is recorded exactly as the resolver answered it.
//
// WHAT IT NEVER DOES: no reverse sweeps of CIDR blocks, no enumeration
// of neighbors of a seed address, no guessing.  The seed list bounds
// the queries; the DNS answers bound the facts.
//
// Discovery ≠ observation (P1 contract 3): a PTR answer proves a NAME
// was published for the address — it is NOT evidence that any service
// answered there.  No service rows are produced.
//
// Terms verdict — reviewed-enabled.  Same basis as the `dns` connector
// (docs/SOURCES.md §C): reverse lookup is the normal operation of a
// standard protocol against the operator's resolver (config "resolver",
// default the system's — GSEER never hardcodes a third-party resolver);
// the operator names every target; answers are protocol facts recorded
// with their lookup time and cached in the index (the index IS the
// cache).  Full review: docs/SOURCES.md §E (Reverse DNS).

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/Lutfifakee-Project/gseer/internal/suppress"
)

// ptrConnector records reverse-DNS answers about operator-named addresses.
type ptrConnector struct{}

// Kind implements Collector.
func (ptrConnector) Kind() string { return "ptr" }

// Review implements Collector (docs/SOURCES.md §E).
func (ptrConnector) Review() Review {
	return Review{
		Verdict: ReviewEnabled,
		Why:     "standard reverse-DNS (PTR) recursion via the operator's resolver; answers are protocol facts, the operator names every target",
		DocRef:  "docs/SOURCES.md §E (Reverse DNS)",
	}
}

// Collect implements Collector: one pointer-lookup pass over the seeds.
func (ptrConnector) Collect(ctx Ctx) (*Batch, error) {
	addrs, err := seeds(ctx, "ip")
	if err != nil {
		return nil, err
	}
	r := dnsResolver(ctx.Config.Resolver)
	now := time.Now().UTC()
	raws := make([]RawFact, 0, len(addrs))
	lookups, answers := 0, 0
	var lookupErrs []error
	for _, addr := range addrs {
		if err := ctx.Context.Err(); err != nil {
			return nil, err
		}
		canonical, nerr := suppress.NormalizeIP(addr)
		if nerr != nil {
			raws = append(raws, RawFact{AssetKind: "ip", Value: addr, ObservedAt: now})
			// the pipeline dead-letters the invalid seed with its reason
			continue
		}
		ip := net.ParseIP(canonical)
		if ip == nil {
			raws = append(raws, RawFact{AssetKind: "ip", Value: addr, ObservedAt: now})
			continue
		}
		lookups++

		names, perr := lookupAddr(ctx.Context, r, canonical)
		if perr != nil {
			lookupErrs = append(lookupErrs, perr)
			continue
		}
		for _, name := range names {
			// LookupAddr returns names with a trailing dot; the
			// normalization drops it and the hostname kind keeps
			// the distinction from registrable domains.
			if hn, herr := suppress.NormalizeDomain(name); herr == nil && hn != "" {
				answers++
				raws = append(raws, RawFact{
					AssetKind:  "hostname",
					Value:      hn,
					ObservedAt: now,
					Confidence: "medium", // recursive answers may be cached
					Extra: map[string]any{
						"ptr": map[string]any{"ip": canonical, "name": name},
					},
				})
			}
		}
	}
	// Every lookup failed → the transport is down, retryable.  Partial
	// failures are normal DNS (no PTR record lives in answers==0, not
	// errors).
	if lookups > 0 && answers == 0 && len(lookupErrs) == lookups {
		return nil, fmt.Errorf("ptr: all %d reverse lookups failed (resolver %q): %v",
			lookups, resolverLabel(ctx.Config.Resolver), lookupErrs[0])
	}
	return &Batch{
		Raws: raws,
		Done: true, // point lookups: one batch per pass, no pagination
		Notes: map[string]int{
			"lookups": lookups, "answers": answers, "seeds": len(addrs),
		},
	}, nil
}

// DNS lookup seam (tests fake it; production resolves through r).
var lookupAddr = func(ctx context.Context, r *net.Resolver, addr string) ([]string, error) {
	return r.LookupAddr(ctx, addr)
}

func init() { register(ptrConnector{}) }
