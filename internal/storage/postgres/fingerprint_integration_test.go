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

//go:build integration

package postgres

import (
	"context"
	"hash/fnv"
	"strings"
	"testing"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/fprule"
	"github.com/Lutfifakee-Project/GSEER/internal/storage"
)

// fingerprint_integration_test.go — the M18 ingestion-enrichment
// contract on a live database:
//
//   - an engine-attached store enriches stored evidence with derived
//     `fingerprint.technologies` (plain product names) + `derived`
//     metadata, and the search_index technologies array follows;
//   - PayloadHash and the caller's evidence map are untouched (the L1/L2
//     dedup identity stays the RAW record's);
//   - the default store (fixture loader path) stores evidence verbatim;
//   - a source-provided `fingerprint` key always wins;
//   - an unhealthy registry enriches nothing (fail-closed).

func fpTestStore(t *testing.T, reg *fprule.Registry) (*Store, int64) {
	t.Helper()
	store, _, _ := supTestDB(t)
	if reg != nil {
		store.SetFingerprintEngine(reg)
	}
	sourceID := supSource(t, store, "fp-test", "test")
	return store, sourceID
}

func fpObservationID(cursor string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("fp:" + cursor))
	return int64(h.Sum64())
}

func fpFact(sourceID int64, cursor string, evidence map[string]any) Fact {
	ip := "203.0.113.50"
	return Fact{
		Asset: storage.Asset{Kind: storage.AssetKindIP, Value: ip, IP: &ip, Sources: []string{"fp-test"}},
		Service: &storage.Service{
			Port: 443, Transport: "tcp", Protocol: "https",
		},
		Observation: storage.Observation{
			ID:           fpObservationID(cursor),
			SourceID:     sourceID,
			SourceCursor: cursor,
			PayloadHash:  "fp-hash-" + cursor,
			ObservedAt:   time.Now().UTC().Truncate(time.Microsecond),
			Provenance:   "observation",
			Evidence:     evidence,
		},
	}
}

func mustRule(t *testing.T, yml string) *fprule.Rule {
	t.Helper()
	r, err := fprule.ParseRule([]byte(yml))
	if err != nil {
		t.Fatalf("ParseRule: %v", err)
	}
	return r
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func TestFingerprintEnrichmentOnEngineStore(t *testing.T) {
	store, sourceID := fpTestStore(t, fprule.Seed())
	ctx := context.Background()

	evidence := map[string]any{
		"http": map[string]any{"server": "nginx/1.24.0", "status": 200},
	}
	f := fpFact(sourceID, "fp-enrich-1", evidence)
	res, err := store.IngestFact(ctx, &f)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// The caller's map is untouched.
	if _, has := evidence["fingerprint"]; has {
		t.Fatal("caller evidence map was mutated by enrichment")
	}

	// Stored evidence carries the derived payload; the raw fields are
	// intact.
	var stored string
	if err := store.pool.QueryRow(ctx,
		`SELECT evidence FROM observations WHERE id = $1`, res.ObservationID).Scan(&stored); err != nil {
		t.Fatalf("read stored evidence: %v", err)
	}
	for _, want := range []string{`"technologies"`, `"nginx"`, `"rule_id"`, `"web-server-nginx"`, `"1.24.0"`, `"server": "nginx/1.24.0"`} {
		if !contains(stored, want) {
			t.Errorf("stored evidence missing %s: %s", want, stored)
		}
	}

	// search_index technologies follow the view.
	var tech []string
	if err := store.pool.QueryRow(ctx,
		`SELECT technologies FROM search_index WHERE row_kind='service' AND port=443 LIMIT 1`).Scan(&tech); err != nil {
		t.Fatalf("search_index read: %v", err)
	}
	if len(tech) != 1 || tech[0] != "nginx" {
		t.Fatalf("search_index technologies = %v, want [nginx]", tech)
	}

	// Re-ingesting the same raw record still dedups (hash untouched by
	// enrichment): a second delivery is an L2 hit, not a new row.
	f2 := fpFact(sourceID, "fp-enrich-1", map[string]any{
		"http": map[string]any{"server": "nginx/1.24.0", "status": 200},
	})
	res2, err := store.IngestFact(ctx, &f2)
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if res2.ObservationInserted {
		t.Fatal("enriched re-delivery created a second observation row (dedup broke)")
	}
}

func TestFingerprintVerbatimWithoutEngine(t *testing.T) {
	store, sourceID := fpTestStore(t, nil)
	ctx := context.Background()

	fv := fpFact(sourceID, "fp-verbatim", map[string]any{
		"http": map[string]any{"server": "nginx/1.24.0"},
	})
	res, err := store.IngestFact(ctx, &fv)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	var stored string
	if err := store.pool.QueryRow(ctx,
		`SELECT evidence FROM observations WHERE id = $1`, res.ObservationID).Scan(&stored); err != nil {
		t.Fatalf("read stored evidence: %v", err)
	}
	if contains(stored, "fingerprint") {
		t.Fatalf("verbatim store enriched evidence anyway: %s", stored)
	}
	// And the view carries no technologies.
	var tech []string
	if err := store.pool.QueryRow(ctx,
		`SELECT technologies FROM search_index WHERE row_kind='service' AND port=443 LIMIT 1`).Scan(&tech); err != nil {
		t.Fatalf("search_index read: %v", err)
	}
	if len(tech) != 0 {
		t.Fatalf("technologies = %v, want empty", tech)
	}
}

func TestFingerprintSourceProvidedWins(t *testing.T) {
	store, sourceID := fpTestStore(t, fprule.Seed())
	ctx := context.Background()

	fs := fpFact(sourceID, "fp-source-provided", map[string]any{
		"http": map[string]any{"server": "nginx/1.24.0"},
		"fingerprint": map[string]any{
			"technologies": []any{"vendor-reported-tech"},
		},
	})
	res, err := store.IngestFact(ctx, &fs)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	var tech []string
	if err := store.pool.QueryRow(ctx,
		`SELECT technologies FROM search_index WHERE row_kind='service' AND port=443 LIMIT 1`).Scan(&tech); err != nil {
		t.Fatalf("search_index read: %v", err)
	}
	if len(tech) != 1 || tech[0] != "vendor-reported-tech" {
		t.Fatalf("technologies = %v, want the source-provided value", tech)
	}
	_ = res
}

func TestFingerprintFailClosedOnBrokenRegistry(t *testing.T) {
	// Forge a compile problem: an uncompilable pattern must disable
	// enrichment entirely (fail-closed), never silently no-op one rule.
	bad := mustRule(t, `
id: broken-fp
family: web-server
product: broken
confidence: 0.9
provenance: test-only broken rule for the fail-closed contract
matches:
  - field: http.server
    pattern: '(unclosed'
tests:
  - evidence: {http: {server: "x"}}
    match: true
`)
	broken := fprule.NewRegistry([]*fprule.Rule{bad})
	if broken.Healthy() {
		t.Fatal("broken registry reported healthy")
	}
	store, sourceID := fpTestStore(t, broken)
	ctx := context.Background()

	fb := fpFact(sourceID, "fp-broken", map[string]any{
		"http": map[string]any{"server": "nginx/1.24.0"},
	})
	res, err := store.IngestFact(ctx, &fb)
	if err != nil {
		t.Fatalf("ingest with broken engine must succeed (enrichment just disabled): %v", err)
	}
	var stored string
	if err := store.pool.QueryRow(ctx,
		`SELECT evidence FROM observations WHERE id = $1`, res.ObservationID).Scan(&stored); err != nil {
		t.Fatalf("read stored evidence: %v", err)
	}
	if contains(stored, "fingerprint") {
		t.Fatalf("unhealthy registry enriched anyway: %s", stored)
	}
}
