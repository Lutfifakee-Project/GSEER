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

package fixture

// corpus_test.go — unit tests for the M4 corpus builder (SPEC-04 §9).
// Pure in-memory: Facts() is exercised directly and no database is
// touched (the Load path's DB behavior is covered by the postgres
// package's integration tests).

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
)

// payloadHashHex is the strict lowercase-sha256-hex shape every
// PayloadHash must have (64 hex chars — a sha256 sum, nothing else).
var payloadHashHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// TestFactsCorpusShape locks the corpus cardinalities and the two
// honesty invariants stamped on every fact: provenance=fixture on the
// observation, sources=["fixture"] on the asset.
func TestFactsCorpusShape(t *testing.T) {
	facts, err := Facts()
	if err != nil {
		t.Fatalf("Facts() error = %v, want nil", err)
	}
	if got := len(facts); got != 7 {
		t.Fatalf("Facts() returned %d facts, want 7 (one per corpus observation o1..o7)", got)
	}
	// The corpus declares 4 assets (A–D) and 6 services (s1–s6).
	assets := make(map[string]bool) // (kind,value) → seen
	services := 0
	for i, f := range facts {
		assets[string(f.Asset.Kind)+"/"+f.Asset.Value] = true
		if f.Service != nil {
			services++
		}
		if f.Observation.Provenance != storage.ProvenanceFixture {
			t.Errorf("facts[%d] (%s) Provenance = %q, want %q",
				i, f.Observation.SourceCursor, f.Observation.Provenance, storage.ProvenanceFixture)
		}
		if !reflect.DeepEqual(f.Asset.Sources, []string{"fixture"}) {
			t.Errorf("facts[%d] (%s) Asset.Sources = %v, want [fixture]",
				i, f.Observation.SourceCursor, f.Asset.Sources)
		}
	}
	if len(assets) != 4 {
		t.Errorf("corpus produced %d distinct assets, want 4 (A=203.0.113.10, B=.11, C=.12, D=portal.example.net)", len(assets))
	}
	if services != 6 {
		t.Errorf("corpus produced %d service-bearing facts, want 6 (o1..o6; o7 is asset-level)", services)
	}
}

// TestFactsRefWiring checks the corpus ref wiring end to end on three
// representative observations: o1 (service fact), o3 (ssh evidence),
// o7 (asset-level CT sighting with nil Service).
func TestFactsRefWiring(t *testing.T) {
	facts, err := Facts()
	if err != nil {
		t.Fatalf("Facts() error = %v, want nil", err)
	}

	// facts[0] is o1: asset 203.0.113.10 via its https/443 service.
	o1 := facts[0]
	if o1.Asset.Kind != storage.AssetKindIP || o1.Asset.Value != "203.0.113.10" {
		t.Errorf("facts[0] asset = %s/%s, want ip/203.0.113.10 (corpus ref A)", o1.Asset.Kind, o1.Asset.Value)
	}
	if o1.Observation.SourceCursor != "fixture://203.0.113.10:443/https" {
		t.Errorf("facts[0] SourceCursor = %q, want fixture://203.0.113.10:443/https (o1)", o1.Observation.SourceCursor)
	}
	if o1.Service == nil {
		t.Fatalf("facts[0] Service = nil, want the s1 https service")
	}
	if o1.Service.Port != 443 || o1.Service.Transport != "tcp" || o1.Service.Protocol != "https" {
		t.Errorf("facts[0] Service = %d/%s/%s, want 443/tcp/https (corpus ref s1)",
			o1.Service.Port, o1.Service.Transport, o1.Service.Protocol)
	}

	// facts[2] is o3: the ssh banner evidence on the OpenSSH service.
	o3 := facts[2]
	ssh, ok := o3.Observation.Evidence["ssh"].(map[string]any)
	if !ok {
		t.Fatalf("facts[2] evidence[ssh] = %T, want map[string]any", o3.Observation.Evidence["ssh"])
	}
	if got, _ := ssh["banner"].(string); got != "SSH-2.0-OpenSSH_9.6" {
		t.Errorf("facts[2] ssh banner = %q, want %q", got, "SSH-2.0-OpenSSH_9.6")
	}

	// facts[6] is o7: the asset-level CT sighting — nil Service, domain asset.
	o7 := facts[6]
	if o7.Service != nil {
		t.Errorf("facts[6] Service = %+v, want nil (o7 has no service row)", o7.Service)
	}
	if o7.Asset.Kind != storage.AssetKindDomain || o7.Asset.Value != "portal.example.net" {
		t.Errorf("facts[6] asset = %s/%s, want domain/portal.example.net (corpus ref D)",
			o7.Asset.Kind, o7.Asset.Value)
	}
	if o7.Observation.SourceCursor != "fixture://portal.example.net/ct" {
		t.Errorf("facts[6] SourceCursor = %q, want fixture://portal.example.net/ct (o7)", o7.Observation.SourceCursor)
	}
	if o7.Asset.Domain == nil || *o7.Asset.Domain != "portal.example.net" {
		t.Errorf("facts[6] Asset.Domain = %v, want &\"portal.example.net\" (typed pointer per kind)", o7.Asset.Domain)
	}
}

// TestFactsDeterministic runs Facts() twice and requires byte-level
// determinism: identical Observation.ID slices and an identical
// PayloadHash per ref (refs are identified by their unique
// source_cursor).  A non-deterministic corpus would silently break the
// L2 fact identity and the caller-assigned observations.id.
func TestFactsDeterministic(t *testing.T) {
	first, err := Facts()
	if err != nil {
		t.Fatalf("Facts() #1 error = %v, want nil", err)
	}
	second, err := Facts()
	if err != nil {
		t.Fatalf("Facts() #2 error = %v, want nil", err)
	}

	ids1 := make([]int64, len(first))
	ids2 := make([]int64, len(second))
	for i := range first {
		ids1[i] = first[i].Observation.ID
		ids2[i] = second[i].Observation.ID
	}
	if !reflect.DeepEqual(ids1, ids2) {
		t.Errorf("Observation.ID slices differ across runs:\n first=%v\nsecond=%v", ids1, ids2)
	}

	hash1 := make(map[string]string, len(first)) // source_cursor → PayloadHash
	for _, f := range first {
		hash1[f.Observation.SourceCursor] = f.Observation.PayloadHash
	}
	for _, f := range second {
		want, ok := hash1[f.Observation.SourceCursor]
		if !ok {
			t.Fatalf("second run introduced unknown cursor %q", f.Observation.SourceCursor)
		}
		if got := f.Observation.PayloadHash; got != want {
			t.Errorf("PayloadHash for %q differs across runs: %q vs %q", f.Observation.SourceCursor, got, want)
		}
	}
}

// TestFactsPayloadHashShape asserts every PayloadHash is exactly 64 hex
// chars — the sha256-hex contract the search-index provenance and the
// L2 fact identity rely on.
func TestFactsPayloadHashShape(t *testing.T) {
	facts, err := Facts()
	if err != nil {
		t.Fatalf("Facts() error = %v, want nil", err)
	}
	for i, f := range facts {
		if !payloadHashHex.MatchString(f.Observation.PayloadHash) {
			t.Errorf("facts[%d] (%s) PayloadHash = %q, want 64 lowercase hex chars",
				i, f.Observation.SourceCursor, f.Observation.PayloadHash)
		}
	}
}

// TestBannerConstant pins the Banner bytes exactly: the em-dash
// (U+2014, not a hyphen and not an en-dash) is part of the
// SPEC-01 §45 contract every surface prints.  Hard-coding the expected
// string here — rather than comparing Banner to itself — is the point.
func TestBannerConstant(t *testing.T) {
	const want = "fixture dataset — synthetic observations for evaluation; NOT real internet data"
	if Banner != want {
		t.Errorf("Banner = %q, want %q (em-dash U+2014 must be byte-exact)", Banner, want)
	}
}
