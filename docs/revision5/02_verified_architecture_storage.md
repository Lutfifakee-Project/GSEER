# Section 2 — Verified Source Architecture (Part B: `internal/storage/postgres/`)

## 2.6 `Store` and ingestion path

| File | Symbol / function | Status | Actual current behavior |
|------|------------------|--------|-------------------------|
| `store.go` | `Store` struct | Implemented | `pool *pgxpool.Pool` + optional `fingerprints *fprule.Registry`. `NewStore(pool)`, `SetFingerprintEngine(reg)`. |
| | `IngestFact(ctx, f *Fact) (FactResult, error)` | Implemented | Per-fact transaction: load suppression gate → `gateFact` → if suppressed return `{Suppressed:true}` (nothing written) → `BEGIN` → `ingestFactCore` (asset upsert → optional service upsert → observation insert) → `COMMIT`/`ROLLBACK`. Observation write is last so FK failure rolls back the whole fact. |
| | `IngestFacts(ctx, facts []Fact) (IngestStats, error)` | Implemented | One compiled rule set for the batch; facts committed separately (each fact its own transaction). On first error stops and returns accumulated stats + error. Suppressed facts count into `Stats.Suppressed`, skipped. **Per-record transactional ingestion** (not all-or-nothing batch). |
| | `ingestFactCore(ctx, q, f, engine)` | Implemented | `upsertAssetCore` → sets `f.Observation.AssetID`; if `f.Service != nil`: `upsertServiceCore` (sets `f.Service.AssetID`, `f.Observation.ServiceID`) → `insertObservationCore`. Returns `FactResult` with resolved ids and inserted flags. |
| | `UpsertAsset` / `upsertAssetCore` | Implemented | `INSERT ... ON CONFLICT (kind, value) DO UPDATE`. Non-zero/non-empty/non-NULL EXCLUDED overwrites; zero values preserve stored metadata. `sources` unioned via `array_agg DISTINCT`. `last_seen = GREATEST(...)`. `first_seen` NOT in update set (preserved on conflict). `stale = false`. Normalization via `suppress` before SQL; suppression gate `gateAsset` before write. |
| | `UpsertService` / `upsertServiceCore` | Implemented | `INSERT ... ON CONFLICT (asset_id, port, transport) DO UPDATE` replacing protocol/name/product/version wholesale; `last_seen = GREATEST(...)`, `stale = false`. **No suppression gate here by design** (asset gated when written; retraction cascades). |
| | `InsertObservation` / `insertObservationCore` | Implemented | `INSERT ... ON CONFLICT DO NOTHING` (covers PK id, L1 `ux_observations_delivery`, L2 `ux_observations_fact`). On conflict the stored row WINS; `inserted=false`; surviving row resolved via L1 lookup `(source_id, source_cursor)` then L2 lookup `(source_id, asset_id, COALESCE(service_id,0), observed_at, payload_hash)`. `id` caller-provided (no sequence default). `observed_at` must be non-zero; `payload_hash` and `source_cursor` must be non-empty. `provenance` defaults to `ProvenanceFixture` if empty. Fingerprint enrichment applied to the stored evidence copy only (caller's map never mutated; PayloadHash never affected). |

## 2.7 `IngestFact` / `IngestFacts` transactional behavior — verified

- **Atomicity per fact:** `IngestFact` wraps asset+service+observation in ONE `pgx` transaction. A failure at any step rolls back the entire fact.
- **Independence across facts:** `IngestFacts` commits each fact separately; one failing fact does not roll back previously committed facts.
- **L1 idempotency:** `(source_id, source_cursor)` unique index. Re-delivery of the same source record → `ON CONFLICT DO NOTHING` → L1 lookup returns existing id → `inserted=false`, deduped.
- **L2 idempotency:** `(source_id, asset_id, COALESCE(service_id,0), observed_at, payload_hash)` unique index. Distinct record with identical content within a source collapses to one fact.
- **Suppression:** `loadGate` compiles rules once; `gateFact` checks source exclusion then asset rules BEFORE any write. Suppressed fact writes nothing (not even asset metadata), returns `Suppressed:true`, one `suppression_applications` ingest-block row.

## 2.8 Source registration

| File | Symbol | Status | Behavior |
|------|--------|--------|----------|
| `collection.go` | `EnsureSource(ctx, name, connector, termsRef, cfg) (id, error)` | Implemented | `INSERT INTO data_sources ... ON CONFLICT (name) DO UPDATE SET connector, terms_ref, config`. **Enabled is always `false` here** — never flipped as a side effect of registration. Returns id. |
| | `GetSource(ctx, name) (DataSourceRow, error)` | Implemented | Loads by name; `ErrSourceNotFound` if absent. |
| | `ListSources(ctx)` | Implemented | All registered sources ordered by id. |
| | `SetSourceEnabled(ctx, name, enabled)` | Implemented | Explicit operator action. Returns `ErrSourceNotFound` if no rows matched. |
| | `StartCollectionJob` / `FinishCollectionJob` | Implemented | Job bookkeeping (M14): one row per connector run, kind `fetch`/`backfill`/`retract`, status running/success/failed/aborted, stats JSONB. |
| | `GetCheckpoint` / `SetCheckpoint` | Implemented | Resumable per-source `(source_id, key)` cursor. `''` = from beginning. |
| | `AddDeadLetter` / `PendingDeadLetters` / `ResolveDeadLetter` / `FailDeadLetter` / `DiscardDeadLetters` | Implemented | Poison-record preservation with bounded re-attempt semantics. |

## 2.9 Fixture source + purge

| File | Symbol | Status | Behavior |
|------|--------|--------|----------|
| `store.go` | `EnsureFixtureSource(ctx) (sourceID, error)` | Implemented | Upserts `data_sources` row `(name='fixture', connector='fixture', enabled=false, terms_ref=...)`. Re-running refreshes `terms_ref` but leaves `enabled` untouched (v0.0.1 sources never enabled). |
| | `PurgeFixtureData(ctx, pool) (PurgeStats, error)` | Implemented | One transaction; deletes observations bound to fixture source id AND `provenance='fixture'`; deletes services only if orphaned post-deletion AND fixture-referenced; deletes assets only if fixture-sourced AND no observation AND no service left; strips `'fixture'` from surviving `assets.sources[]`. Real observations unreachable by both predicates → never deleted. Fixture source registry row left in place. |

## 2.10 Migration driver state

| File | Symbol | Status | Behavior |
|------|--------|--------|----------|
| `migrate.go` / `migratedriver.go` / `lock.go` | `RunMigrations(dsn, lockTimeoutMs)` | Implemented | golang-migrate driver; one atomic transaction per migration; bounded advisory-lock wait; dirty state fails with recovery guidance. `schema_migrations` table owned by the driver (not created in `0001_core`). |

## 2.11 Reachability / doctor

| File | Symbol | Status | Behavior |
|------|--------|--------|----------|
| `reachability.go` | `CheckReachable(ctx, dsn)` | Implemented | DB reachability check before migrations (SPEC-05 §1). |
| | `CheckPGVersion(ctx, dsn, major)` | Implemented | PG version floor (PD-8: PostgreSQL 15+; verified against PG 17). |
| `doctor.go` | `DoctorDBChecks(ctx, dsn, major)` | Implemented | Returns per-check results (reachable, version, migrations head, etc.) used by `gseer doctor`. |

## 2.12 Redaction

| File | Symbol | Status | Behavior |
|------|--------|--------|----------|
| `redact.go` | `RedactDSN` / `MustRedactDSN` | Implemented | Redacts password from DSN for display paths only; DSN passed to connection APIs never redacted. |
