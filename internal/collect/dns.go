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

// dns.go — the operator-domain DNS connector (v0.0.2 P1).
//
// WHAT IT DOES: resolves names the OPERATOR or the index already named
// (seed_mode "config" or "index") through standard DNS recursion — the
// protocol every system on the internet speaks — and records the factual
// answers as asset-level facts (A/AAAA → ip assets; CNAME, MX and NS
// targets → hostname assets).  MX and NS answers name the operator's
// published mail exchangers and nameservers — the same kind of
// publication fact a CNAME target is — and stay within the reviewed
// terms basis (§C: ordinary recursion of operator-named names).
//
// WHAT IT NEVER DOES: no zone transfers, no brute-force enumeration, no
// guessing of subdomains, no NXDOMAIN-walking.  The seed list bounds the
// queries; the DNS answers bound the facts.  TXT records are NOT
// collected: they are free-form metadata strings with no typed,
// searchable column in the index (collected they would be evidence-only
// noise, not searchable observations).
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
// Each valid seed fans out into four focused record-type collectors
// (A/AAAA, CNAME, MX, NS) sharing one pass accumulator; the helpers live
// below Collect and keep this loop the whole story of a pass.
func (dnsConnector) Collect(ctx Ctx) (*Batch, error) {
	names, err := seeds(ctx, "domain")
	if err != nil {
		return nil, err
	}
	r := dnsResolver(ctx.Config.Resolver)
	now := time.Now().UTC()
	pass := newDNSPass(len(names))
	for _, name := range names {
		if err := ctx.Context.Err(); err != nil {
			return nil, err
		}
		canonical, nerr := suppress.NormalizeDomain(name)
		if nerr != nil {
			pass.raws = append(pass.raws, RawFact{AssetKind: "domain", Value: name, ObservedAt: now})
			// the pipeline dead-letters the invalid seed with its reason
			continue
		}
		pass.lookups++
		pass.collectAddrs(ctx.Context, r, canonical, now)
		pass.collectCNAME(ctx.Context, r, canonical, now)
		pass.collectMX(ctx.Context, r, canonical, now)
		pass.collectNS(ctx.Context, r, canonical, now)
	}
	return pass.batch(ctx.Config.Resolver, len(names))
}

// dnsPass accumulates one connector pass: the collected facts, the
// honest answer counters that become the job Notes, and the
// address-lookup errors that drive the total-failure sentinel.
type dnsPass struct {
	raws    []RawFact
	lookups int // valid seeds that reached the resolver
	answers int // record answers kept as facts (all types)
	mx, ns  int // per-type answer counters (Notes truthfulness)
	errs    []error
}

func newDNSPass(n int) *dnsPass {
	return &dnsPass{raws: make([]RawFact, 0, n)}
}

// collectAddrs records the A/AAAA answers as ip asset facts (recursive
// answers may be cached, hence medium confidence).  A failed address
// lookup is remembered: when EVERY seed's address lookup fails, the
// transport is down and the whole pass is retryable.
func (p *dnsPass) collectAddrs(ctx context.Context, r *net.Resolver, canonical string, now time.Time) {
	addrs, err := lookupIPAddr(ctx, r, canonical)
	if err != nil {
		p.errs = append(p.errs, err)
		return
	}
	for _, a := range addrs {
		p.answers++
		p.raws = append(p.raws, RawFact{
			AssetKind:  "ip",
			Value:      a.IP.String(),
			ObservedAt: now,
			Confidence: "medium", // recursive answers may be cached
			Extra: map[string]any{
				"dns": map[string]any{"query": canonical, "type": addrFamily(a.IP)},
			},
		})
	}
}

// collectCNAME records the CNAME target as a hostname asset fact (the
// same publication-fact character as the address answers).
// Self-referential answers — the queried name itself, with or without
// the trailing root dot — name no other asset and are skipped.
func (p *dnsPass) collectCNAME(ctx context.Context, r *net.Resolver, canonical string, now time.Time) {
	target, err := lookupCNAME(ctx, r, canonical)
	if err != nil || target == "" || target == canonical || target == canonical+"." {
		return
	}
	cn, nerr := suppress.NormalizeDomain(target)
	if nerr != nil || cn == canonical {
		return
	}
	p.answers++
	p.raws = append(p.raws, hostnameFact(canonical, "CNAME", cn, now, nil))
}

// collectMX records the domain's published mail exchangers — the same
// publication-fact character as a CNAME target.  Best-effort like the
// CNAME probe: an MX lookup failure on a seed whose A lookup succeeded
// is ordinary DNS (an apex without MX is normal), not a transport
// outage; the answer counters keep the all-failed retry semantics
// truthful.
func (p *dnsPass) collectMX(ctx context.Context, r *net.Resolver, canonical string, now time.Time) {
	mxs, err := lookupMX(ctx, r, canonical)
	if err != nil {
		return
	}
	for _, mx := range mxs {
		if mx == nil || mx.Host == "" {
			continue
		}
		if hn, herr := suppress.NormalizeDomain(mx.Host); herr == nil && hn != "" && hn != canonical {
			p.answers++
			p.mx++
			p.raws = append(p.raws, hostnameFact(canonical, "MX", hn, now,
				map[string]any{"pref": mx.Pref}))
		}
	}
}

// collectNS records the domain's published nameservers (same basis and
// same best-effort treatment as MX).
func (p *dnsPass) collectNS(ctx context.Context, r *net.Resolver, canonical string, now time.Time) {
	nss, err := lookupNS(ctx, r, canonical)
	if err != nil {
		return
	}
	for _, ns := range nss {
		if ns == nil || ns.Host == "" {
			continue
		}
		if hn, herr := suppress.NormalizeDomain(ns.Host); herr == nil && hn != "" && hn != canonical {
			p.answers++
			p.ns++
			p.raws = append(p.raws, hostnameFact(canonical, "NS", hn, now, nil))
		}
	}
}

// hostnameFact builds one hostname answer fact — the shape shared by
// the CNAME, MX and NS record types (query name, record type, and the
// type's own evidence fields; medium confidence like every recursive
// answer).
func hostnameFact(query, rtype, host string, now time.Time, extra map[string]any) RawFact {
	dns := make(map[string]any, 2+len(extra))
	dns["query"] = query
	dns["type"] = rtype
	for k, v := range extra {
		dns[k] = v
	}
	return RawFact{
		AssetKind:  "hostname",
		Value:      host,
		ObservedAt: now,
		Confidence: "medium",
		Extra:      map[string]any{"dns": dns},
	}
}

// batch closes the pass: the total-failure sentinel (every seed's
// address lookup errored — the transport is down, the pass is
// retryable) or the collected facts with honest job statistics.
// Partial failures are normal DNS: NXDOMAIN lives in answers==0, not
// in errors.
func (p *dnsPass) batch(resolver string, seedCount int) (*Batch, error) {
	if p.lookups > 0 && p.answers == 0 && len(p.errs) == p.lookups {
		return nil, fmt.Errorf("dns: all %d lookups failed (resolver %q): %v",
			p.lookups, resolverLabel(resolver), p.errs[0])
	}
	return &Batch{
		Raws: p.raws,
		Done: true, // point lookups: one batch per pass, no pagination
		Notes: map[string]int{
			"lookups": p.lookups, "answers": p.answers, "seeds": seedCount,
			"mx": p.mx, "ns": p.ns,
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
	lookupMX = func(ctx context.Context, r *net.Resolver, host string) ([]*net.MX, error) {
		return r.LookupMX(ctx, host)
	}
	lookupNS = func(ctx context.Context, r *net.Resolver, host string) ([]*net.NS, error) {
		return r.LookupNS(ctx, host)
	}
)

func init() { register(dnsConnector{}) }
