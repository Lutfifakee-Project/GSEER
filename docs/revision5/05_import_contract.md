# Section 5 — Source Registration and JSONL Import Contract

## 5.1 Source registration (prerequisite to import)

Before any JSONL import, the operator must register a data source:

```
gseer source add <name> --type operator-import --terms-ref <ref>
```

- `EnsureSource(name, connector="operator-import", termsRef, config)` → upserts `data_sources` on `name` UNIQUE. Repeated calls reuse the existing row (idempotent registration).
- `enabled` is `false` by default; enablement is an explicit operator action (`gseer source enable <name>`). Registration does NOT enable collection/import.
- The import pipeline resolves `source_id` by looking up the registered source by name. If the source is not registered, the record is invalid.
- `source_id` is never accepted from JSONL input.

## 5.2 Complete import pipeline (operator-import / JSONL)

1. **Parse input.** Read JSONL; one record per line. Parse each line as a JSON object. Malformed JSON → that line is a bad record (dead-lettered/reported), never silently dropped.
2. **Validate each record.** Validate required fields (asset kind + value, service port/transport/protocol, observed_at non-zero). Invalid → bad record (dead-letter).
3. **Resolve asset identity.** Normalize asset `(kind, value)` via `suppress` normalizers; upsert asset → resolved `AssetID`.
4. **Resolve the registered data source.** Look up source by name → resolved `SourceID`. Unknown source → invalid record.
5. **Resolve service endpoint identity.** For service-level records: upsert service `(asset_id, port, transport)` → resolved `ServiceID`. Asset-level records have `ServiceID = nil`.
6. **Normalize only fields whose normalization is explicitly defined.** Asset identifiers via `suppress.NormalizeIP`/`NormalizeDomain`. No other implicit normalization.
7. **Compute deterministic payload hash.** `sha256` over the canonicalized record content (connector kind, asset kind/value, service port/transport/protocol/product/version, observed_at, evidence) → `payload_hash` (L2 fact identity). Computed from the RAW delivered record.
8. **Apply suppression policy.** Check the compiled suppression rule set BEFORE any write: source exclusion first, then asset rules. Suppressed fact → write nothing, count as suppressed, record ingest-block in `suppression_applications`.
9. **Persist asset, service, observation.** Per-fact transaction: asset upsert → optional service upsert → observation insert (`ON CONFLICT DO NOTHING` with L1/L2 dedup).
10. **Record failures and provide an actionable report.** Per-record results aggregated into an import report: inserted/updated/deduped/suppressed/invalid counts, plus a list of bad records with reasons.

## 5.3 Canonical JSON / hash behavior

- `payload_hash` is computed over a stable, canonical serialization of the record fields that participate in fact identity, so identical records yield identical hashes regardless of key ordering in the input JSON.
- Evidence is stored verbatim (JSONB) in `observations.evidence`; the hash is computed pre-enrichment and never affected by fingerprint derivation.
- The canonical hash input excludes `source_cursor` and `id` (delivery/bookkeeping) so that re-delivery of the same content under a new cursor still dedups at L2.

## 5.4 L1 idempotency (delivery identity)

- Key: `(source_id, source_cursor)` (unique index `ux_observations_delivery`).
- Same source record re-delivered (replay, retry) → L1 conflict → stored row wins → `inserted=false`, deduped. No duplicate observation.

## 5.5 L2 idempotency (fact identity)

- Key: `(source_id, asset_id, COALESCE(service_id,0), observed_at, payload_hash)` (unique index `ux_observations_fact`).
- Distinct record with identical content within a source → collapses to one fact. Two different sources observing the same endpoint yield two rows (provenance kept).

## 5.6 Cursor collision behavior

- A repeated cursor with a **different payload** (`payload_hash` differs) hits the L1 index (`source_id, source_cursor`). `ON CONFLICT DO NOTHING` keeps the stored row; the pipeline detects L1 match + differing payload_hash and reports it as a **cursor collision / payload changed** conflict in the import record — treated as a conflict, NOT silently accepted as a successful duplicate. The existing observation is never overwritten.
- A repeated cursor with the **same payload** → L1 dedupe → recognized as replay.

## 5.7 Out-of-order observations

- Observations are stored with their `observed_at`. `first_seen`/`last_seen` on assets/services are maintained as `GREATEST` (never regress) on upsert; `first_seen` preserved on conflict. Out-of-order arrival does not corrupt `last_seen` (GREATEST) and does not move `first_seen` backward.

## 5.8 `first_seen` and `last_seen`

- `assets.first_seen`: set on insert, never changed on conflict (preserved).
- `assets.last_seen` / `services.last_seen`: `GREATEST(stored, excluded)` — advances forward, never regresses.
- `stale = false` on any fresh sighting.
- Observations carry their own `observed_at`; they are not updated on dedupe (the stored row wins).

## 5.9 Observation immutability

- Once an observation row exists, its `observed_at`, `payload_hash`, `source_cursor`, `provenance`, and `evidence` are not updated by re-ingestion (`ON CONFLICT DO NOTHING` keeps the stored row). Observations are immutable after creation.

## 5.10 Partial failure behavior

- `IngestFacts` commits each fact separately. A failing fact aborts only its own transaction; previously committed facts are retained. The run returns accumulated stats + the first error.
- Poison isolation: on batch error, re-drive fact-by-fact; healthy facts land, poison records dead-lettered.
- Invalid records (bad JSON, validation failure) are dead-lettered (`ingestion_dead_letter`) with reason, never silently dropped.

## 5.11 Transaction boundaries

- Per fact: ONE transaction (asset → service → observation). Failure rolls back the whole fact.
- Across facts: independent transactions (per-record), not one giant batch transaction.

## 5.12 Dead-letter behavior

- Records that fail validation or ingestion are written to `ingestion_dead_letter` (`source_id`, `payload` JSONB, `reason`, attempts, status `pending`). Preserved for re-drive (`gseer collect redrive`), never silently dropped.
- Identical pending payload bumps `attempts`/`last_seen` instead of inserting a duplicate (bounded growth under retry storms).

## 5.13 Dry-run guarantees

- A dry run must persist **nothing**: no assets, no services, no observations, no source checkpoints, no dead-letter rows, no `schema_migrations` changes. It validates and reports only.
- Dry-run is a distinct execution mode (`--dry-run`) that runs the pipeline up to (but not including) the write transaction, then reports what *would* happen.

## 5.14 Batch failure policy: recommendation

**Compare:**
- **All-or-nothing batch ingestion:** one transaction for the whole batch. Pro: atomic. Con: one poison record fails the entire import; large batches risk long transactions/locks; partial progress lost.
- **Per-record transactional ingestion (recommended):** each fact its own transaction (matches existing `IngestFacts`). Pro: one bad fact doesn't kill the batch; idempotent re-runs; partial progress preserved. Con: non-atomic across the batch (some facts commit, others fail) — mitigated by the import report.

**Recommendation for v0.2.0:** Per-record transactional ingestion, consistent with the existing `IngestFacts`/`Store` design. This matches GSEER's at-least-once, idempotent-replay philosophy. A batch-level "atomic" option is deferred (not in v0.2.0 scope).

## 5.15 No JSONL command exists in v0.1.0

The `operator-import` / JSONL import command must be created in v0.2.0. It is not present in the baseline. The pipeline above (Sections 5.1–5.14) is the contract to implement.
