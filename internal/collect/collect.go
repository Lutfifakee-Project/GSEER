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

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

// NOTE: storage.Fact is defined in internal/storage (v0.0.2 M14 moved it
// out of the postgres package); the postgres store keeps type aliases, so
// []storage.Fact is directly ingestible via postgres.Store.IngestFacts.

// Collector is one data provider's passive connector.  Implementations
// are stateless: everything they need arrives in CollectInput.
type Collector interface {
	// Kind is the connector type ("dns", "rdap", "asn", "ct").
	Kind() string
	// Review is the terms-compliance verdict (see doc.go contract 2).
	Review() Review
	// Collect runs one passive pass and returns the batch.  A connector
	// NEVER ingests: it only produces facts (the Runner owns ingestion,
	// checkpoints, dead letters and job bookkeeping).
	Collect(ctx Ctx) (*Batch, error)
}

// Ctx is the connector's view of one run: its configuration, the resume
// cursor, the shared HTTP client (nil for DNS-only connectors), the run
// context (cancellation flows to provider calls) and the seed loader for
// index-driven enrichment.
type Ctx struct {
	Context context.Context // the run's context; never nil from the Runner
	Source  SourceRef
	Config  SourceConfig
	Cursor  string
	Limit   int     // per-run record budget (0 = DefaultRunLimit)
	Client  *Client // shared rate-limited HTTP client
	// IndexSeeds loads seed identifiers already in the index (seed_mode
	// "index"): no third-party target lists are ever used.
	IndexSeeds func(kind string, limit int) ([]string, error)
	// Now is injectable for tests.
	Now func() time.Time
}

// SourceRef identifies the data_sources row driving the run.
type SourceRef struct {
	ID   int64
	Name string
}

// SourceConfig is the decoded data_sources.config / YAML source block.
type SourceConfig struct {
	// Endpoint is the provider's API base URL (HTTP connectors).
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	// Seeds are operator-configured identifiers (seed_mode "config").
	Seeds []string `json:"seeds,omitempty" yaml:"seeds,omitempty"`
	// SeedMode: "config" (use Seeds) or "index" (use identifiers already
	// in the GSEER index — the connector enriches the operator's own
	// corpus; it never consults a third-party target list).
	SeedMode string `json:"seed_mode,omitempty" yaml:"seed_mode,omitempty"`
	// SeedKind is the asset kind index seeds are drawn from
	// ("ip" for rdap/asn enrichment; unused for "config" mode).
	SeedKind string `json:"seed_kind,omitempty" yaml:"seed_kind,omitempty"`
	// Resolver is an optional DNS server address ("host:port") for the
	// dns/asn connectors; empty = the system resolver.
	Resolver string `json:"resolver,omitempty" yaml:"resolver,omitempty"`
	// RequestsPerSecond caps the HTTP client (0 = connector default).
	RequestsPerSecond float64 `json:"requests_per_second,omitempty" yaml:"requests_per_second,omitempty"`
	// TimeoutMs bounds one provider request (0 = connector default).
	TimeoutMs int `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`
	// LimitPerRun bounds one run's record budget (0 = DefaultRunLimit).
	LimitPerRun int `json:"limit_per_run,omitempty" yaml:"limit_per_run,omitempty"`
}

// DecodeSourceConfig parses the raw JSONB config; unknown keys are kept
// out — configs are small and explicit.
func DecodeSourceConfig(raw []byte) (SourceConfig, error) {
	var cfg SourceConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("decode source config: %v", err)
	}
	return cfg, nil
}

// DefaultRunLimit bounds one run when the config says nothing: passive
// politeness by default.
const DefaultRunLimit = 500

// Batch is one connector's output: RAW provider records only.  The
// Runner owns the fact pipeline (normalize → dedupe → ingest) so
// normalization can never diverge between connectors, and every record
// passes the same suppression gate at the store.  Records MUST be
// asset-level (P1 honesty contract 3): a RawFact can never express a
// service, and the runner would refuse one that tried.  Cursor resumes
// the next run (” = begin; Done means the connector has nothing
// further this pass).
type Batch struct {
	Raws   []RawFact
	Cursor string
	Done   bool
	// Notes are connector-reported counters surfaced in the job stats.
	Notes map[string]int
}

// RawFact is one provider record a connector derived from a response,
// pre-ingestion (the fact pipeline turns these into storage.Fact values,
// dead-lettering the invalid ones).  A raw record is ALWAYS asset-level:
// there is no way to express a service here (P1 honesty contract 3).
type RawFact struct {
	AssetKind  string // "ip" | "domain" | "hostname"
	Value      string // raw surface form; canonicalized by the pipeline
	ObservedAt time.Time
	Confidence string // "high" | "medium" (provider-dependent)

	// Enrichment fields (asset metadata the upsert merges without
	// blanking stored values): the ASN/BGP connector fills ASN/ASNOrg/
	// GeoCountry; RDAP fills GeoCountry.  They ride on the asset row —
	// never invented beyond what the provider stated.
	ASN        int
	ASNOrg     string
	GeoCountry string

	// Extra lands inside observation.evidence under the connector's key.
	Extra map[string]any
}

// BadRecord is a raw record the pipeline refused (dead-lettered, never
// silently dropped).
type BadRecord struct {
	Raw    RawFact
	Reason string
}

// BuildFacts canonicalizes and dedupes raw records into ingestible
// asset-level facts (data-quality pipeline; owner directive §4):
//
//   - identifiers normalize through the suppression package's
//     canonicalizers — ONE normalization definition shared with the gate;
//   - invalid records come back as BadRecords (dead-letter fodder), they
//     are never silently dropped;
//   - the batch is deduped on (asset kind, canonical value) so one
//     provider response yields one fact per identifier;
//   - every fact is asset-level: no service rows are ever built here
//     (discovery ≠ observation);
//   - provenance is the connector kind; confidence lands in evidence.
func BuildFacts(connectorKind string, raws []RawFact) ([]storage.Fact, []BadRecord) {
	var (
		facts []storage.Fact
		bad   []BadRecord
		seen  = map[string]bool{}
	)
	now := time.Now().UTC()
	for _, r := range raws {
		var (
			canonical string
			err       error
		)
		switch r.AssetKind {
		case "ip":
			canonical, err = suppress.NormalizeIP(r.Value)
		case "domain":
			canonical, err = suppress.NormalizeDomain(r.Value)
		case "hostname":
			// Hostnames KEEP their kind: "www.example.com" is not
			// a registrable domain, and the (kind, value) asset
			// identity must reflect that (data quality — a CT
			// or CNAME discovery is a hostname sighting).
			canonical, err = suppress.NormalizeDomain(r.Value)
		default:
			err = fmt.Errorf("unknown asset kind %q", r.AssetKind)
		}
		if err != nil {
			bad = append(bad, BadRecord{Raw: r, Reason: err.Error()})
			continue
		}
		key := r.AssetKind + "|" + canonical
		if seen[key] {
			continue // intra-batch dedup (DB L1/L2 handles cross-run)
		}
		seen[key] = true

		observed := r.ObservedAt
		if observed.IsZero() {
			observed = now
		}
		a := storage.Asset{Kind: storage.AssetKind(r.AssetKind), Value: canonical}
		switch r.AssetKind {
		case "ip":
			v := canonical
			a.IP = &v
		case "domain":
			v := canonical
			a.Domain = &v
		case "hostname":
			v := canonical
			a.Hostname = &v
		}
		// Enrichment metadata (never blanked on conflict by the
		// upsert's COALESCE/CASE semantics — a partial enrichment
		// cannot erase what a previous source established).
		a.ASN = r.ASN
		a.ASNOrg = r.ASNOrg
		a.GeoCountry = r.GeoCountry
		evidence := r.Extra
		if evidence == nil {
			evidence = map[string]any{}
		}
		if r.Confidence != "" {
			evidence["confidence"] = r.Confidence
		}
		// Honesty (P1 contract 3 / owner directive §4): a passive
		// record asserts an identifier was DISCOVERED — it never
		// asserts a service answered.  The evidence tag makes the
		// distinction queryable from search (evidence is stored
		// on every observation).
		facts = append(facts, storage.Fact{
			Asset: a,
			Observation: storage.Observation{
				ObservedAt:   observed.UTC().Truncate(time.Microsecond),
				PayloadHash:  payloadHash(connectorKind, r.AssetKind, canonical, observed, evidence),
				Provenance:   connectorKind,
				SourceCursor: "", // stamped by the Runner per delivery
				Evidence:     evidence,
			},
		})
	}
	return facts, bad
}

// payloadHash is the L2 fact-identity hash: identical content from the
// same source at the same timestamp collapses at the store level.
func payloadHash(connectorKind, kind, value string, at time.Time, evidence map[string]any) string {
	raw, _ := json.Marshal(map[string]any{
		"c": connectorKind, "k": kind, "v": value,
		"at": at.UTC().Truncate(time.Second).Format(time.RFC3339),
		"e":  evidence,
	})
	return fmt.Sprintf("sha256:%x", sha256Sum(raw))
}
