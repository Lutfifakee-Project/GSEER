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

package fixture

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
)

// corpus.go — the M4 fixture corpus loader (SPEC-04 §9): the embedded
// corpus.json, the Banner honesty constant, the Facts() builder and the
// Load() ingestion path.
//
// Why embedded: the corpus is part of the shipped binary so the
// --load-fixtures CLI path and the tests always see the exact same
// dataset — a drifting side-by-side file could silently change what
// "evaluation" means.  Why per-call parsing instead of a sync.Once: the
// file is ~2 KB, Facts() must hand out fresh (mutable) Fact values to
// each caller anyway, and a per-call parse keeps Load() re-entrant
// without copy semantics subtleties.
//
// Honesty invariants (QS-6): every row carries provenance=fixture and
// the "fixture" source tag; nothing here can ever be promoted to real
// internet data.  All IPs/domains are RFC 5737 (203.0.113.0/24) and
// RFC 2606 (example.net) documentation space.

// Banner is the shared fixture-surface constant (SPEC-01 §45).  CLI,
// REST and Dashboard print it wherever fixture rows appear so no
// consumer mistakes the synthetic dataset for real internet data.
// The em-dash is a deliberate, contract-pinned character (U+2014):
// tests assert the exact byte sequence.
const Banner = "fixture dataset — synthetic observations for evaluation; NOT real internet data"

// corpusPath is the go:embed location of the normative SPEC-04 §9
// corpus relative to this package's directory.
const corpusPath = "testdata/fixture/corpus.json"

// corpusFS embeds the corpus at build time; see the package comment for
// why the dataset ships inside the binary rather than beside it.
//
//go:embed testdata/fixture/corpus.json
var corpusFS embed.FS

// fixtureSources is the single source tag stamped onto every asset the
// corpus produces (the store unions it into assets.sources on conflict).
var fixtureSources = []string{storage.FixtureSourceName}

// corpusDoc mirrors testdata/fixture/corpus.json top to bottom.  The
// ref fields are corpus-internal keys ("A", "s1", "o1") used to wire
// services to assets and observations to both; they never reach storage.
type corpusDoc struct {
	ObservedAt   string          `json:"observed_at"`
	Assets       []corpusAsset   `json:"assets"`
	Services     []corpusService `json:"services"`
	Observations []corpusObs     `json:"observations"`
}

// corpusAsset is one asset entry.  The typed ip/domain/hostname fields
// mirror storage.Asset's typed pointers; exactly one is set per entry
// (the one matching kind).
type corpusAsset struct {
	Ref        string `json:"ref"`
	Kind       string `json:"kind"`
	Value      string `json:"value"`
	IP         string `json:"ip,omitempty"`
	Domain     string `json:"domain,omitempty"`
	Hostname   string `json:"hostname,omitempty"`
	ASN        int    `json:"asn,omitempty"`
	ASNOrg     string `json:"asn_org,omitempty"`
	GeoCountry string `json:"geo_country,omitempty"`
}

// corpusService is one service entry; Asset names the owning asset ref.
type corpusService struct {
	Ref       string `json:"ref"`
	Asset     string `json:"asset"`
	Port      int    `json:"port"`
	Transport string `json:"transport"`
	Protocol  string `json:"protocol"`
	Name      string `json:"name,omitempty"`
	Product   string `json:"product,omitempty"`
	Version   string `json:"version,omitempty"`
}

// corpusObs is one observation entry.  Service is a *string because
// asset-level observations (no service row, e.g. the CT log sighting)
// use JSON null.  Evidence is decoded generically: the payload schema is
// connector-specific, and storage stores it as JSONB verbatim.
type corpusObs struct {
	Ref          string         `json:"ref"`
	Asset        string         `json:"asset"`
	Service      *string        `json:"service"`
	SourceCursor string         `json:"source_cursor"`
	Evidence     map[string]any `json:"evidence"`
}

// Facts returns the SPEC-04 §9 corpus as ingestion-ready facts.
//
// Determinism guarantees (asserted by corpus_test.go): the returned
// slice is in corpus observation order; each Observation.PayloadHash is
// hex(sha256(canonical JSON of evidence)) — encoding/json sorts map
// keys, so the marshal of the decoded map is byte-stable; each
// Observation.ID is fnv1a-64 over "fixture:"+cursor, a stable
// bookkeeping id only (the L1 delivery identity (source_id,
// source_cursor) — not this id — is the ingestion idempotency key).
// AssetID/ServiceID/SourceID are left zero: the store resolves them
// during IngestFact, and Load stamps SourceID after EnsureFixtureSource.
func Facts() ([]postgres.Fact, error) {
	raw, err := corpusFS.ReadFile(corpusPath)
	if err != nil {
		return nil, fmt.Errorf("fixture: read embedded corpus %s: %w", corpusPath, err)
	}
	var doc corpusDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("fixture: parse embedded corpus %s: %w", corpusPath, err)
	}
	observedAt, err := time.Parse(time.RFC3339, doc.ObservedAt)
	if err != nil {
		return nil, fmt.Errorf("fixture: corpus observed_at %q: %w", doc.ObservedAt, err)
	}
	observedAt = observedAt.UTC() // the DB column is timestamptz; store UTC canonically

	// Assets by ref: services and observations wire through these.
	assets := make(map[string]storage.Asset, len(doc.Assets))
	for _, a := range doc.Assets {
		asset, err := corpusAssetToStorage(a)
		if err != nil {
			return nil, fmt.Errorf("fixture: corpus asset %q: %w", a.Ref, err)
		}
		assets[a.Ref] = asset
	}

	// Services by ref, validated against the asset table above.
	// AssetID stays zero here: IngestFact resolves the real row id after
	// upserting the fact's asset (the corpus has no DB row ids).
	services := make(map[string]storage.Service, len(doc.Services))
	for _, s := range doc.Services {
		if _, ok := assets[s.Asset]; !ok {
			return nil, fmt.Errorf("fixture: corpus service %q references unknown asset %q", s.Ref, s.Asset)
		}
		services[s.Ref] = storage.Service{
			Port:      s.Port,
			Transport: s.Transport,
			Protocol:  s.Protocol,
			Name:      s.Name,
			Product:   s.Product,
			Version:   s.Version,
		}
	}

	facts := make([]postgres.Fact, 0, len(doc.Observations))
	for _, o := range doc.Observations {
		asset, ok := assets[o.Asset]
		if !ok {
			return nil, fmt.Errorf("fixture: corpus observation %q references unknown asset %q", o.Ref, o.Asset)
		}
		fact := postgres.Fact{Asset: asset}
		if o.Service != nil {
			svc, ok := services[*o.Service]
			if !ok {
				return nil, fmt.Errorf("fixture: corpus observation %q references unknown service %q", o.Ref, *o.Service)
			}
			fact.Service = &svc
		}
		h := sha256.Sum256([]byte(mustCanonicalJSON(o.Ref, o.Evidence)))
		fact.Observation = storage.Observation{
			// ID: stable fnv1a-64 bookkeeping id (no DB default for the
			// column); identity/idempotency is L1 (SourceID, cursor).
			ID:           observationBookkeepingID(o.SourceCursor),
			ObservedAt:   observedAt,
			PayloadHash:  hex.EncodeToString(h[:]),
			SourceCursor: o.SourceCursor,
			Provenance:   storage.ProvenanceFixture,
			Evidence:     o.Evidence,
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// corpusAssetToStorage converts one corpus asset entry, enforcing the
// kind ↔ typed-pointer pairing the schema's assets_kind_fields_check
// expects (exactly one typed column per kind).  Sources is always the
// single "fixture" tag: provenance honesty starts at the asset level.
func corpusAssetToStorage(a corpusAsset) (storage.Asset, error) {
	asset := storage.Asset{
		Kind:       storage.AssetKind(a.Kind),
		Value:      a.Value,
		ASN:        a.ASN,
		ASNOrg:     a.ASNOrg,
		GeoCountry: a.GeoCountry,
		Sources:    fixtureSources,
	}
	switch asset.Kind {
	case storage.AssetKindIP:
		v := a.IP
		if v == "" {
			v = a.Value
		}
		asset.IP = &v
	case storage.AssetKindDomain:
		v := a.Domain
		if v == "" {
			v = a.Value
		}
		asset.Domain = &v
	case storage.AssetKindHostname:
		v := a.Hostname
		if v == "" {
			v = a.Value
		}
		asset.Hostname = &v
	default:
		return storage.Asset{}, fmt.Errorf("unsupported kind %q (want ip|domain|hostname)", a.Kind)
	}
	return asset, nil
}

// mustCanonicalJSON marshals evidence with encoding/json's sorted-map-key
// canonical form.  Marshal of a decoded map[string]any can only fail on
// values JSON cannot represent — impossible for a json.Unmarshal output,
// so an error here is a corpus bug and is reported loudly.
func mustCanonicalJSON(obsRef string, evidence map[string]any) string {
	b, err := json.Marshal(evidence)
	if err != nil {
		// Panic is deliberate: Facts() must never return corpus rows with
		// a fabricated hash, and a decoded map[string]any is by
		// construction JSON-serializable — this path is unreachable.
		panic(fmt.Sprintf("fixture: observation %s evidence cannot be canonicalized: %v", obsRef, err))
	}
	return string(b)
}

// observationBookkeepingID derives the caller-assigned observations.id
// (the column has no default, so every writer must supply one).  fnv1a-64
// over "fixture:"+cursor is deterministic across runs and processes —
// required because re-loading the corpus must re-derive the same ids —
// but it is bookkeeping only: dedup/idempotency ride on L1
// (source_id, source_cursor) and L2 (fact identity), never on this hash.
func observationBookkeepingID(sourceCursor string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("fixture:" + sourceCursor))
	// Mask to 63 bits: the bookkeeping id is a positive BIGINT; the mask
	// keeps the derivation deterministic and overflow-free.
	return int64(h.Sum64() & 0x7fff_ffff_ffff_ffff)
}

// LoadStats reports what Load did: the fixture source registry id the
// facts were stamped with, plus the store's ingest counters.
type LoadStats struct {
	SourceID int64
	postgres.IngestStats
}

// Load ensures the fixture data_source row exists, then ingests the
// corpus through store — idempotent via L1/L2 (safe to re-run).
//
// The SourceID stamp happens per fact right before ingest because the
// observation insert requires a caller-supplied source_id (FK to
// data_sources) and EnsureFixtureSource is what makes that id real.
// Re-running Load is safe by construction: EnsureFixtureSource is an
// upsert, and IngestFacts dedupes observations on the L1 delivery
// identity (source_id, source_cursor) — a second run reports the same
// rows as updated/deduped instead of duplicating anything.
func Load(ctx context.Context, store *postgres.Store) (LoadStats, error) {
	sourceID, err := store.EnsureFixtureSource(ctx)
	if err != nil {
		return LoadStats{}, fmt.Errorf("fixture: ensure fixture data source row: %w", err)
	}
	facts, err := Facts()
	if err != nil {
		return LoadStats{}, err
	}
	for i := range facts {
		facts[i].Observation.SourceID = sourceID
	}
	stats, err := store.IngestFacts(ctx, facts)
	if err != nil {
		return LoadStats{}, fmt.Errorf("fixture: ingest corpus: %w", err)
	}
	return LoadStats{SourceID: sourceID, IngestStats: stats}, nil
}
