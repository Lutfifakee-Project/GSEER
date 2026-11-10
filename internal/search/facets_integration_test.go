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

//go:build integration

package search

import (
	"context"
	"hash/fnv"
	"strings"
	"testing"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
)

// facets_integration_test.go — the v0.1.0 facet engine on the fixture
// corpus (PG 17).  Fixture expectations (SPEC-04 §9): six service rows
// (s1 443/https, s2 80/http, s3 22/ssh, s4 8443/https, s5 443/http,
// s6 8443/https) + one asset-only row (D, US); sources all "fixture";
// last_seen fixed at the corpus observed_at (older than the freshness
// buckets).

// facetFPObsID derives the caller-assigned observation id (the column
// has no default — same fnv bookkeeping as the other suites).
func facetFPObsID(cursor string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("facet-fp:" + cursor))
	return int64(h.Sum64())
}

func facetValue(t *testing.T, f Facet, value string) (int64, bool) {
	t.Helper()
	for _, v := range f.Values {
		if v.Value == value {
			return v.Count, true
		}
	}
	return 0, false
}

func TestFacetsAllDimensionsOnFixtures(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()

	res, err := svc.Facets(context.Background(), FacetsQuery{GQL: `stale == "false"`, MaxValues: 10})
	if err != nil {
		t.Fatalf("facets: %v", err)
	}
	byName := map[string]Facet{}
	for _, f := range res.Facets {
		byName[f.Name] = f
	}
	if len(res.Facets) != 7 {
		t.Fatalf("want 7 facet dimensions, got %d: %v", len(res.Facets), res.Facets)
	}

	// port: 443→2, 80→1, 22→1, 8443→2 (asset-only row has no port).
	port := byName[FacetPort]
	for value, want := range map[string]int64{"443": 2, "80": 1, "22": 1, "8443": 2} {
		if got, ok := facetValue(t, port, value); !ok || got != want {
			t.Errorf("port facet[%s] = %d (ok=%v), want %d (values: %v)", value, got, ok, want, port.Values)
		}
	}
	// protocol: https→3, http→2, ssh→1.
	proto := byName[FacetProtocol]
	for value, want := range map[string]int64{"https": 3, "http": 2, "ssh": 1} {
		if got, ok := facetValue(t, proto, value); !ok || got != want {
			t.Errorf("protocol facet[%s] = %d (ok=%v), want %d", value, got, ok, want)
		}
	}
	// country (service mode): US→5 (s1,s2,s3,s4 + asset-only D), DE→2.
	country := byName[FacetCountry]
	for value, want := range map[string]int64{"US": 5, "DE": 2} {
		if got, ok := facetValue(t, country, value); !ok || got != want {
			t.Errorf("country facet[%s] = %d (ok=%v), want %d", value, got, ok, want)
		}
	}
	// source: everything is fixture-tagged.
	src := byName[FacetSource]
	if got, ok := facetValue(t, src, "fixture"); !ok || got != 7 {
		t.Errorf("source facet[fixture] = %d (ok=%v), want 7", got, ok)
	}
	// freshness: the fixture loader stamps last_seen freshly
	// (relative to load time), so every row lands in "24h" — the
	// bucket value is relative, the completeness is pinned.
	fresh := byName[FacetFreshness]
	if got, ok := facetValue(t, fresh, "24h"); !ok || got != 7 {
		t.Errorf("freshness facet[24h] = %d (ok=%v), want 7 (values: %v)", got, ok, fresh.Values)
	}
	for _, bucket := range []string{"stale", "week", "month", "older"} {
		if got, ok := facetValue(t, fresh, bucket); ok && got != 0 {
			t.Errorf("freshness facet[%s] = %d, want 0 (freshly loaded corpus)", bucket, got)
		}
	}
	// technology: the fixture corpus carries no fingerprint evidence.
	tech := byName[FacetTechnology]
	if len(tech.Values) != 0 {
		t.Errorf("technology facet = %v, want empty (fixtures carry no fingerprint evidence)", tech.Values)
	}
	// service: the service label follows the protocol labels here.
	svcFacet := byName[FacetService]
	if got, ok := facetValue(t, svcFacet, "https"); !ok || got != 3 {
		t.Errorf("service facet[https] = %d (ok=%v), want 3", got, ok)
	}
}

func TestFacetsFilterNarrowsCounts(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()

	res, err := svc.Facets(context.Background(), FacetsQuery{GQL: `port == 443`})
	if err != nil {
		t.Fatalf("facets: %v", err)
	}
	byName := map[string]Facet{}
	for _, f := range res.Facets {
		byName[f.Name] = f
	}
	if got, ok := facetValue(t, byName[FacetPort], "443"); !ok || got != 2 {
		t.Errorf("port facet[443] = %d (ok=%v), want 2", got, ok)
	}
	if got, ok := facetValue(t, byName[FacetProtocol], "http"); !ok || got != 1 {
		t.Errorf("protocol facet[http] under port==443 = %d (ok=%v), want 1 (s5 only)", got, ok)
	}
	// country: 443 rows are A (US, s1) and C (DE, s5).
	if got, ok := facetValue(t, byName[FacetCountry], "US"); !ok || got != 1 {
		t.Errorf("country facet[US] under port==443 = %d (ok=%v), want 1", got, ok)
	}
	if got, ok := facetValue(t, byName[FacetCountry], "DE"); !ok || got != 1 {
		t.Errorf("country facet[DE] under port==443 = %d (ok=%v), want 1", got, ok)
	}
}

func TestFacetsTechnologyFromIngestedFingerprint(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()

	// Ingest one service observation with explicit fingerprint evidence
	// (the shape a source-provided payload carries).  The fixture
	// loader stays verbatim, so this fact is added through the store.
	ctx := context.Background()
	store := postgres.NewStore(svc.pool)
	sourceID, err := store.EnsureSource(ctx, "fp-eval", "test", "facet test source", map[string]any{})
	if err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	ip := "203.0.113.77"
	f := postgres.Fact{
		Asset:   storage.Asset{Kind: storage.AssetKindIP, Value: ip, IP: &ip, Sources: []string{"fp-eval"}},
		Service: &storage.Service{Port: 8443, Transport: "tcp", Protocol: "https", Name: "https"},
		Observation: storage.Observation{
			ID:           facetFPObsID("facet-fp-1"),
			SourceID:     sourceID,
			SourceCursor: "facet-fp-1",
			PayloadHash:  "facet-fp-hash-1",
			ObservedAt:   time.Now().UTC().Truncate(time.Microsecond),
			Provenance:   "observation",
			Evidence: map[string]any{
				"fingerprint": map[string]any{
					"technologies": []any{"nginx", "php"},
				},
			},
		},
	}
	if _, err := store.IngestFact(ctx, &f); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	res, err := svc.Facets(ctx, FacetsQuery{GQL: `technologies CONTAINS "nginx"`, Facets: []string{FacetTechnology, FacetPort}})
	if err != nil {
		t.Fatalf("facets: %v", err)
	}
	byName := map[string]Facet{}
	for _, fc := range res.Facets {
		byName[fc.Name] = fc
	}
	// The filter narrows to the ingested row; the technology facet
	// enumerates BOTH of its array elements (nginx and php).
	tech := byName[FacetTechnology]
	if got, ok := facetValue(t, tech, "nginx"); !ok || got != 1 {
		t.Errorf("technology facet[nginx] = %d (ok=%v), want 1 (values: %v)", got, ok, tech.Values)
	}
	if got, ok := facetValue(t, tech, "php"); !ok || got != 1 {
		t.Errorf("technology facet[php] = %d (ok=%v), want 1", got, ok)
	}
	if got, ok := facetValue(t, byName[FacetPort], "8443"); !ok || got != 1 {
		t.Errorf("port facet[8443] = %d (ok=%v), want 1 (only the ingested row carries the fingerprint)", got, ok)
	}
}

func TestFacetsAssetModeCountsDistinctAssets(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()

	res, err := svc.Facets(context.Background(), FacetsQuery{GQL: `stale == "false"`, Mode: "asset"})
	if err != nil {
		t.Fatalf("facets: %v", err)
	}
	var country Facet
	for _, f := range res.Facets {
		if f.Name == FacetCountry {
			country = f
		}
	}
	// asset mode: each asset once — A/B/D are US (3), C is DE (1).
	if got, ok := facetValue(t, country, "US"); !ok || got != 3 {
		t.Errorf("asset-mode country[US] = %d (ok=%v), want 3", got, ok)
	}
	if got, ok := facetValue(t, country, "DE"); !ok || got != 1 {
		t.Errorf("asset-mode country[DE] = %d (ok=%v), want 1", got, ok)
	}
}

func TestFacetsTruncationAndErrors(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()
	ctx := context.Background()

	// Truncation: 4 distinct ports, cap 2 → truncated + 2 values.
	res, err := svc.Facets(ctx, FacetsQuery{GQL: `stale == "false"`, Facets: []string{FacetPort}, MaxValues: 2})
	if err != nil {
		t.Fatalf("facets: %v", err)
	}
	if len(res.Facets) != 1 || !res.Facets[0].Truncated || len(res.Facets[0].Values) != 2 {
		t.Fatalf("truncation contract broken: %+v", res.Facets)
	}

	// Unknown dimension.
	if _, err := svc.Facets(ctx, FacetsQuery{GQL: `port == 443`, Facets: []string{"asn"}}); err == nil {
		t.Error("unknown facet accepted")
	}
	// Empty filter.
	if _, err := svc.Facets(ctx, FacetsQuery{GQL: "   "}); err == nil {
		t.Error("empty filter accepted")
	}
	// Invalid GQL surfaces the GQL error, not a facet error.
	if _, err := svc.Facets(ctx, FacetsQuery{GQL: `nosuchfield == 1`}); err == nil {
		t.Error("invalid GQL accepted")
	} else if !strings.Contains(err.Error(), "GQL") {
		t.Errorf("want GQL error preserved, got %v", err)
	}
	// Out-of-range cardinality.
	if _, err := svc.Facets(ctx, FacetsQuery{GQL: `port == 443`, MaxValues: 51}); err == nil {
		t.Error("max-values over cap accepted")
	}
}
