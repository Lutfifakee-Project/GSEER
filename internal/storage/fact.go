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

package storage

// fact.go — the ingestion unit types, moved from internal/storage/postgres
// to the types package in v0.0.2 (M14).
//
// Why: the collector framework (internal/collect) builds Facts before any
// store exists in scope, and the doc.go layering contract
// (collect → storage/postgres → storage, suppress) would otherwise force
// the connector code to import the pgx-heavy store package merely to name
// a struct.  The postgres package keeps TYPE ALIASES (type Fact =
// storage.Fact), so every M4-era reference — store.IngestFacts([]Fact),
// FactResult, IngestStats — compiles unchanged, and []storage.Fact IS
// []postgres.Fact (identical named types), so batches cross the boundary
// without conversion.

// Fact is one atomic ingest unit: the asset the fact is about, the
// optional service observed on that asset (nil = asset-level fact, no
// service row), and the provenance-bearing observation that ties them to
// a source (SPEC-05 §2/§5).
type Fact struct {
	Asset       Asset
	Service     *Service // nil = asset-level fact (no service row)
	Observation Observation
}

// FactResult reports what one IngestFact applied: the resolved row ids
// (ServiceID is 0 when Fact.Service was nil) and which rows were freshly
// inserted (via the (xmax = 0) RETURNING heuristic).  Re-ingesting an
// identical fact yields all inserted=false with identical ids.
// Suppressed=true means the fact matched an enabled suppression rule and
// NOTHING was written (M13); all ids are then zero.
type FactResult struct {
	AssetID, ServiceID, ObservationID                   int64 // ServiceID 0 when Service==nil
	AssetInserted, ServiceInserted, ObservationInserted bool
	Suppressed                                          bool
}

// IngestStats aggregates an IngestFacts run.  "Updated" counts rows whose
// identity already existed and was refreshed (idempotent replay);
// observations that collapsed onto an already-stored row count as deduped.
// Suppressed counts facts refused by the suppression gate before any
// write (M13) — they are neither inserted nor updated and carry no
// observation row.
type IngestStats struct {
	AssetsInserted, AssetsUpdated             int
	ServicesInserted, ServicesUpdated         int
	ObservationsInserted, ObservationsDeduped int
	Suppressed                                int
}
