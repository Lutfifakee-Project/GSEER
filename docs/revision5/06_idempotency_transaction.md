# Section 6 — Idempotency and Transaction Model

## 6.1 Idempotency model (two levels)

### L1 — Delivery identity
- Unique index: `ux_observations_delivery (source_id, source_cursor)`.
- Meaning: the same source record re-delivered across restarts/retries/replay is processed once.
- Behavior: `INSERT ... ON CONFLICT DO NOTHING`; on conflict the stored row wins; `inserted=false`; surviving id resolved via L1 lookup then L2 lookup.
- Guarantee: at-least-once delivery collapses to exactly-once processing per delivery identity.

### L2 — Fact identity
- Unique index: `ux_observations_fact (source_id, asset_id, COALESCE(service_id,0::bigint), observed_at, payload_hash)`.
- Meaning: distinct records with identical content within a source collapse to one fact.
- Behavior: two different sources observing the same endpoint still yield two rows (provenance kept). Asset-level observations use `COALESCE(service_id,0)` to match `NULL service_id` without a separate IS NULL branch.

### L1 vs L2
- L1 is about *delivery* (same record resent). L2 is about *content* (same fact observed again). Replay of the same import: same cursors → L1 dedupe (recognized as replay). Re-ingestion of identical content under a new cursor: L1 inserts a new delivery row, L2 collapses to the same fact (deduped).

## 6.2 Transaction model

### Per-fact transaction (existing, verified)
`IngestFact`: `BEGIN` → suppression gate → asset upsert → optional service upsert → observation insert → `COMMIT`/`ROLLBACK`.
- Atomicity: a failure mid-fact rolls back the entire fact (no service without its asset, no observation without its service).
- The observation write is last; an FK failure there rolls back asset/service upserts in the same fact.

### Across-fact independence (existing, verified)
`IngestFacts`: each fact commits separately. One failing fact aborts only its own transaction; accumulated stats returned with the error. Re-runs are idempotent (L1/L2).

### Suppression gate (fail-closed)
- Rule set compiled once per `IngestFact` call (or once per batch for `IngestFacts`).
- If the rule set cannot compile, ingestion stops rather than bypassing (fail-closed).
- `gateFact` checks source exclusion, then asset rules, BEFORE any write. Suppressed fact writes nothing (not even asset metadata), returns `Suppressed:true`, one `suppression_applications` ingest-block row.
- DB backstop trigger (`trg_assets_suppression`) on `assets` rejects any write matching an enabled rule — defense in depth.

## 6.3 Recommended batch policy for JSONL import

Per-record transactional ingestion (Section 5.14), consistent with `IngestFacts`. Not all-or-nothing.

## 6.4 Dry-run transaction boundary

A dry run opens NO write transaction. It runs validation and resolution, then reports. Zero unintended DB writes.
