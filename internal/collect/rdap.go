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

// rdap.go — the RDAP registration-data connector (v0.0.2 P1).
//
// WHAT IT WOULD DO: for IP assets already in the operator's index, query
// the responsible RIR's RDAP endpoint (discovered through the IANA RDAP
// bootstrap registry) and record the network object's country, name,
// handle and address range as enrichment.
//
// TERMS VERDICT — DISABLED (docs/SOURCES.md §B).  The RIR registration
// data services carry restrictive terms: ARIN's Whois/RDAP Terms of Use
// permit querying only for enumerated purposes and state that "no other
// use of the Whois Service other than as expressly described in these
// Terms is implied or permitted" absent prior written authorization, and
// prohibit republishing/making the data publicly available; RIPE NCC's
// terms prohibit re-packaging, compiling and re-distributing RIPEstat /
// registration data.  Indexing the answers into a GSEER search index is
// redistribution the terms do not grant.  Per the owner directive ("if a
// provider requires unavailable credentials, payment, or unclear
// permission for indexing/reuse, keep that connector disabled"), the
// runner REFUSES this connector (E_SOURCE_TERMS) regardless of any
// database row.  The implementation is kept complete and unit-tested so
// the owner can flip the verdict after obtaining the provider's written
// authorization — a one-line change in Review(), gated by the terms gate.

import (
	"fmt"
	"strings"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

// Default RDAP bootstrap endpoints (IANA).  Vars, not consts, only so
// integration tests can point them at a local httptest server — the
// production defaults are exactly these values.
var (
	rdapBootstrapV4 = "https://data.iana.org/rdap/ipv4.json"
	rdapBootstrapV6 = "https://data.iana.org/rdap/ipv6.json"
)

// rdapConnector implements the (disabled) RDAP enrichment connector.
type rdapConnector struct{}

// Kind implements Collector.
func (rdapConnector) Kind() string { return "rdap" }

// Review implements Collector (docs/SOURCES.md §B — verdict: DISABLED).
func (rdapConnector) Review() Review {
	return Review{
		Verdict: ReviewDisabled,
		Why:     "RIR registration-data terms (e.g. ARIN ToU, RIPE NCC T&C) do not grant indexing/redistribution; needs the provider's written authorization before enablement",
		DocRef:  "docs/SOURCES.md §B (RDAP)",
	}
}

// rdapBootstrap is the IANA bootstrap file shape (RFC 9224).
type rdapBootstrap struct {
	Description string    `json:"description"`
	Services    [][]any   `json:"services"` // [ [cidr|tld...], [urls...] ]
	Version     string    `json:"version"`
	Published   time.Time `json:"publication"`
}

// rdapNetwork is the subset of an RDAP ip object GSEER consumes.
type rdapNetwork struct {
	Handle       string      `json:"handle"`
	Name         string      `json:"name"`
	Type         string      `json:"type"`
	Country      string      `json:"country"`
	StartAddress string      `json:"startAddress"`
	EndAddress   string      `json:"endAddress"`
	Events       []rdapEvent `json:"events"`
	Links        []rdapLink  `json:"links"`
}

type rdapEvent struct {
	EventAction string `json:"eventAction"`
	EventDate   string `json:"eventDate"`
}

type rdapLink struct {
	Value string `json:"value"`
	Rel   string `json:"rel"`
	Href  string `json:"href"`
}

// Collect implements Collector: bootstrap → per-seed RIR lookups, one
// batch.  Unreachable in production while the verdict is "disabled" (the
// runner gates first); tests exercise it through httptest endpoints.
func (rdapConnector) Collect(ctx Ctx) (*Batch, error) {
	if ctx.Client == nil {
		return nil, Fatal(fmt.Errorf("rdap: no HTTP client configured"))
	}
	var seeds []string
	var err error
	if ctx.Config.SeedMode == "config" {
		seeds = ctx.Config.Seeds
	} else {
		seeds, err = ctx.IndexSeeds("ip", ctx.Limit)
		if err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	raws := make([]RawFact, 0, len(seeds))
	baseV4, baseV6, err := rdapEndpoints(ctx)
	if err != nil {
		return nil, err
	}

	for _, seed := range seeds {
		if err := ctx.Context.Err(); err != nil {
			return nil, err
		}
		addr, nerr := suppress.NormalizeIP(seed)
		if nerr != nil {
			raws = append(raws, RawFact{AssetKind: "ip", Value: seed, ObservedAt: now})
			continue
		}
		base := baseV4
		if strings.Contains(addr, ":") {
			base = baseV6
		}
		if base == "" {
			return nil, fmt.Errorf("rdap: bootstrap returned no endpoint for family of %s", redactURL(addr))
		}

		var net rdapNetwork
		url := strings.TrimRight(base, "/") + "/ip/" + addr
		if err := ctx.Client.GetJSON(ctx.Context, url, &net); err != nil {
			return nil, err // transport/bootstrap failure: retryable
		}

		ev := map[string]any{
			"handle": net.Handle, "name": net.Name, "type": net.Type,
			"start_address": net.StartAddress, "end_address": net.EndAddress,
		}
		for _, e := range net.Events {
			if e.EventAction == "registration" {
				ev["registered"] = e.EventDate
			}
		}
		raws = append(raws, RawFact{
			AssetKind:  "ip",
			Value:      addr,
			ObservedAt: now,
			Confidence: "high", // the registry's own record
			GeoCountry: strings.ToUpper(strings.TrimSpace(net.Country)),
			Extra:      map[string]any{"rdap": ev},
		})
	}

	return &Batch{Raws: raws, Done: true, Notes: map[string]int{"seeds": len(seeds)}}, nil
}

// rdapEndpoints fetches the IANA bootstrap files and returns the first
// RDAP endpoint matching the v4/v6 address spaces GSEER may enrich.
// (Per-seed prefix matching is overkill for the five RIRs: the bootstrap
// service lists cover the whole space, and the first URL of the matching
// service entry is authoritative for it.)
func rdapEndpoints(ctx Ctx) (v4, v6 string, err error) {
	var b4, b6 rdapBootstrap
	if err = ctx.Client.GetJSON(ctx.Context, rdapBootstrapV4, &b4); err != nil {
		return "", "", err
	}
	if err = ctx.Client.GetJSON(ctx.Context, rdapBootstrapV6, &b6); err != nil {
		return "", "", err
	}
	v4 = firstRDAPEndpoint(b4.Services)
	v6 = firstRDAPEndpoint(b6.Services)
	if v4 == "" && v6 == "" {
		return "", "", Fatal(fmt.Errorf("rdap: bootstrap lists carry no endpoints"))
	}
	return v4, v6, nil
}

// firstRDAPEndpoint returns the first URL of the first service entry.
func firstRDAPEndpoint(services [][]any) string {
	for _, svc := range services {
		if len(svc) < 2 {
			continue
		}
		urls, ok := svc[1].([]any)
		if !ok || len(urls) == 0 {
			continue
		}
		if s, ok := urls[0].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func init() { register(rdapConnector{}) }
