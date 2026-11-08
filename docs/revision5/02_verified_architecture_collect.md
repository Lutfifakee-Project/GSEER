# Section 2 — Verified Source Architecture (Part A: `internal/collect/`)

## 2.1 `internal/collect/` — packages and symbols inspected

| File | Symbol / function | Status | Actual current behavior |
|------|------------------|--------|-------------------------|
| `internal/collect/collect.go` | `Collector` interface | Implemented | Passive data provider contract: `Kind()`, `Review()`, `Collect(ctx) (*Batch, error)`. A connector NEVER ingests; it only produces facts. The Runner owns ingestion, checkpoints, dead letters, job bookkeeping. |
| | `Batch` struct | Implemented | `Raws []RawFact`, `Cursor string`, `Done bool`, `Notes map[string]int`. Cursor resumes next run; "" = begin; Done = nothing further this pass. |
| | `RawFact` struct | Implemented | Always asset-level (P1 honesty contract 3): `AssetKind`, `Value`, `ObservedAt`, `Confidence`, `ASN`, `ASNOrg`, `GeoCountry`, `Extra map[string]any`. No service can be expressed here. |
| | `BuildFacts(connectorKind, raws) ([]storage.Fact, []BadRecord)` | Implemented | Canonicalizes via `suppress` normalizers; dedupes intra-batch on `(kind, canonical)`; builds asset-level facts only; `SourceCursor = ""` (stamped by Runner per delivery); `Observation.ID` not set here (stamped by Runner). Invalid records → BadRecords (dead-lettered, never silently dropped). |
| | `payloadHash(...)` | Implemented | L2 fact-identity hash: `sha256` over `{connectorKind, kind, value, observed_at(seconds), evidence}`, prefixed `sha256:`. |
| `internal/collect/runner.go` | `Runner` struct | Implemented | Execution engine: source registry lookup, terms gate, checkpoints, transport retries, fact pipeline, dead-lettering, job bookkeeping. `Store *postgres.Store`. |
| | `Run(ctx, sourceName) (*RunStats, error)` | Implemented | Gate order: (1) unknown connector → `E_SOURCE_CONNECTOR`; (2) review verdict disabled → `E_SOURCE_TERMS`; (3) source `enabled=false` → clean skip. Runs `runPass`. |
| | `runPass` | Implemented | Batch loop: collect → pipeline → ingest → checkpoint. Checkpoint advances only after batch ingests (at-least-once; L1 identity makes replays no-ops). Aborts after `MaxBatches` without a `Done` batch. |
| | `pipeline` | Implemented | `BuildFacts` → stamp delivery identity (`SourceID`, `SourceCursor = deliveryCursor(batch.Cursor, i)`, `ID = observationID(src.ID, cursor)`) → `ingestWithPoisonIsolation` → dead-letter bad records. |
| | `ingestWithPoisonIsolation` | Implemented | `IngestFacts` batch; on error re-drives fact-by-fact via `IngestFact`; poison records dead-lettered while healthy ones land. |
| | `deliveryCursor(batchCursor, index)` | Implemented | `hexSHA256("v1|batch:<batchCursor>|rec:<index>")`. Deterministic across re-runs from same checkpoint. **Contradiction to resolve (Section 5): Revision 4 says `source_cursor` is in JSONL but another section says the runner generates it.** For the collector path the runner generates it; for JSONL import this must be explicitly resolved. |
| | `observationID(sourceID, cursor)` | Implemented | `fnv1a-64` over `"collect:<sourceID>:<cursor>"`, masked to positive BIGINT. Bookkeeping id only; L1 `(source_id, source_cursor)` is the idempotency key. |
| `internal/collect/registry.go` | `Registry() map[string]Collector` | Implemented | Built-in connector registry keyed by `data_sources.connector`. Duplicate kind panics at startup. |
| `internal/collect/doc.go` | connector review contract | Implemented | Review verdicts live with connectors (docs/SOURCES.md): `dns` reviewed-enabled, `ip2asn` reviewed-enabled, `rdap` disabled, `ct` disabled. |

## 2.2 Fingerprint engine invocation verification

**Claim to verify: "Whether the fingerprint engine is actually invoked by ingestion or search."**

| Invocation point | File | Status | Behavior |
|------------------|------|--------|----------|
| Ingestion enrichment | `internal/storage/postgres/store.go` `insertObservationCore` + `enrichEvidence` (fingerprint.go) | Implemented, optional | When `Store.fingerprints` is set (non-nil, healthy), observation evidence is enriched with `fingerprint.technologies` + `fingerprint.derived` at ingestion. Nil = default; fixture loader never sets it (stores evidence verbatim). The engine only fires when evidence has http/tls/ssh payload AND no source-provided `fingerprint` key (source always wins). |
| CLI attach | `internal/cli/collect.go` `runCollectRun` → `fingerprintEngineFromConfig` | Implemented | `gseer collect run` attaches the rule engine (from config `fingerprints.rules_dir` or embedded seed set) to the store for that run. Any problem disables enrichment (fail-closed) and warns. |
| Search | `internal/search/service.go`, `planner`, `validator` | Implemented for read | `technologies` is a searchable/faceted list field (read from `search_index.technologies`). The engine is NOT invoked at search time; it only enriches at ingestion. |

**Finding:** The fingerprint engine IS invoked by ingestion (when attached via config), but is NOT part of the default/fixture path and is NOT invoked at search time. It never creates observations, services, ports, or protocols.

## 2.3 Service-level ingestion in `internal/collect/`

**Finding: ABSENT in v0.1.0.** `collect.BuildFacts` builds asset-level facts only (`Fact.Service` is always nil; `RawFact` cannot express a service). The runner stamps `SourceCursor`/`SourceID`/`ID` on observations but never resolves a service endpoint. Service-level observations (ServiceFact) do not exist in the collect path. Service rows are created only by the fixture loader (via `store.IngestFact` with a `Service` set) or by direct `UpsertService` calls.

## 2.4 Observation ID generation

- `observations.id` has **no sequence default** (caller-provided).
- Fixture loader: assigns deterministic IDs (D-M3-2).
- Collector runner: `observationID(sourceID, cursor) = fnv1a-64("collect:<sourceID>:<cursor>") & 0x7fff_ffff_ffff_ffff`.
- JSONL import: not yet implemented (no JSONL command exists). Section 4 defines how IDs are assigned for JSONL.

## 2.5 Key architectural caveat for v0.2.0

The collector framework is **asset-level only** (P1 honesty contract 3: "discovery ≠ observation"). Extending it to service-level observations requires a new fact shape (`Fact.Service != nil`) and new runner logic to resolve `ServiceID` per record — this is part of the v0.2.0 ServiceFact contract (Section 4), not something already present.
