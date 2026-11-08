# Section 4 — Final ServiceFact Contract

## 4.1 Purpose

A complete Go contract for service-level observations (service-level facts), to be implemented in v0.2.0. The collector framework in v0.1.0 is asset-level only (`Fact.Service` is always nil); this contract defines the service-level shape that JSONL import and future collectors will use.

## 4.2 The ten required fields (the ServiceFact contract)

| # | Concern | Field (Go type) | Source / semantics |
|---|---------|-----------------|--------------------|
| 1 | Asset identity | `AssetID int64` (or resolved `Asset` value) | Resolved asset row id; asset identity is `(kind, value)`. |
| 2 | Service endpoint identity | `Port int`, `Transport string` (+ resolved `ServiceID int64`) | Endpoint = `(asset_id, port, transport)`. Service identity resolved after asset. |
| 3 | Observation context | `ObservedAt time.Time` | Time the source made the observation. Maps to `observations.observed_at`. |
| 4 | Source identity | `SourceID int64` | Assigned by GSEER's source registration (`data_sources.id`). Never trusted from JSONL. |
| 5 | Delivery cursor | `SourceCursor string` | L1 delivery identity; unique within its source. See Section 4.8 for cursor strategy. |
| 6 | Observation timestamp | `ObservedAt time.Time` | (same as #3) source-made observation time. |
| 7 | Ingestion timestamp | `CreatedAt time.Time` (read-only, DB default `now()`) | Time the observation row was inserted. Maps to `observations.created_at`. Not an input. |
| 8 | Provenance | `Provenance string` | Where the observation came from (e.g. `operator-import`, `dns`, `fixture`). |
| 9 | Raw source evidence | `Evidence map[string]any` | Source-provided evidence, stored verbatim in `observations.evidence`. Never overwritten. |
| 10 | GSEER-derived identification | `Fingerprint map[string]any` (derived) | GSEER-derived `fingerprint.technologies` + `fingerprint.derived`, computed at ingestion by the rule engine when attached. Source-provided `fingerprint` key always wins. Never overwrites source evidence. |

### Go contract (proposed, for v0.2.0 implementation)

```go
// ServiceFact is one service-level observation ready for ingestion.
type ServiceFact struct {
    // Asset identity (resolved to a row id by the pipeline).
    Asset   storage.Asset        // kind + value (+ optional metadata)
    AssetID int64                // resolved after asset upsert (0 until resolved)

    // Service endpoint identity (resolved to a row id by the pipeline).
    Port     int    // 1..65535
    Transport string // "tcp" | "udp"
    Protocol string // e.g. "http", "tls", "ssh"
    Name     string // optional service name
    Product  string // optional source-provided product
    Version  string // optional source-provided version
    ServiceID int64   // resolved after service upsert (0 until resolved)

    // Observation context.
    ObservedAt time.Time   // source-made observation time (observations.observed_at)
    SourceID   int64       // registered data source id (never from JSONL)
    SourceCursor string   // L1 delivery cursor, unique within source

    // Provenance + raw evidence + derived identification.
    Provenance  string              // e.g. "operator-import"
    Evidence    map[string]any      // raw source evidence, stored verbatim
    Fingerprint map[string]any      // GSEER-derived (fingerprint.technologies, fingerprint.derived); computed, never source-trusted
}
```

## 4.3 Timestamp rules (use the ACTUAL existing schema)

- `observations.observed_at` = time the source made the observation. **Not ingestion time.**
- `observations.created_at` = time the observation row was inserted into GSEER (DB `now()` default). **Not observation time.**

Do not claim `observed_at` represents ingestion time. Do not introduce `ingested_at` as an additional column in v0.2.0 (no specific, demonstrable requirement justifies it; `created_at` already serves as the ingestion timestamp).

### Handling of observation timestamps

| Case | Handling |
|------|----------|
| Missing / zero `observed_at` | Reject the record (E_DB_USAGE); `insertObservationCore` requires a non-zero `ObservedAt`. For JSONL, treat as invalid → dead-letter / report. |
| Invalid / unparseable | Reject at validation; do not persist. |
| Future-dated (observed_at > now() + tolerance) | Accept but mark `stale` logic per freshness (Section 11) or reject per operator policy; do not silently normalize. Recommendation: accept and let freshness/lifecycle flag it. |
| Excessively old | Accept; no expiration. Observations are never automatically expired (Section 7). |

## 4.4 Source identity (`source_id`) — never trusted from JSONL

- `source_id` must be assigned by GSEER's source registration and ingestion machinery (`EnsureSource` / `GetSource` → `data_sources.id`).
- JSONL input MUST NOT carry a `source_id` that GSEER trusts. If JSONL names a source, the pipeline looks it up by name; if unknown, the record is invalid (dead-lettered/reported).
- The caller must not be able to claim operator-import data originated from a trusted passive/active collector merely by changing a JSON field. Provenance is set by the pipeline based on the registered source's connector/category, not by a JSON field.

## 4.5 The `operator-import` source

| Attribute | Definition |
|-----------|------------|
| Connector identity | `connector = "operator-import"` |
| Registration behavior | Registered via `EnsureSource(name, "operator-import", termsRef, config)` → `data_sources` row, `enabled=false` by default; enablement is an explicit operator action. Re-running `EnsureSource` with the same name reuses the existing row (upsert on `name` UNIQUE). Repeated imports reuse the same data source. |
| Terms-review semantics | `terms_ref` documents the operator's review of the imported data's provenance/license. The operator attests to the right to ingest. |
| Provenance semantics | Every observation written by this source carries `provenance = "operator-import"`. This is distinct from `dns`, `ip2asn`, `fixture`, etc. |
| Distinguishing imported vs network-collected | `provenance` + `data_sources.connector` make it queryable: `sources CONTAINS "operator-import"` vs `connector = "dns"`. A JSONL `source` field cannot masquerade as a trusted collector because provenance is server-assigned from the registry row, not echoed from JSONL. |

## 4.6 Source cursor and JSONL ownership — resolving the Revision 4 contradiction

**Revision 4 contradiction:** One section says `source_cursor` is present in JSONL (client-supplied); another says the runner generates it.

**Resolution:** `source_cursor` is **generated by GSEER's ingestion machinery, never trusted from JSONL**. For the `operator-import` (JSONL) path, the import runner derives a stable per-record cursor deterministically from the import batch and record position. JSONL must not supply `source_cursor`; if present it is ignored/overridden.

### Selected cursor strategy: deterministic batch-id + record position

For each import run:
1. Compute a **batch identifier** = `sha256` over the canonicalized import payload (file content hash, or an operator-supplied import id) → stable per import execution.
2. For record `i` (0-based) in the batch: `source_cursor = hexSHA256("v1|operator-import|<batchId>|rec:<i>")`.

This guarantees:
- **No collisions within the same import:** different `i` → different cursor (hash input includes `i`).
- **Replay recognition:** re-importing the same file yields the same batchId and same cursors → L1 dedup (`ux_observations_delivery`) → replays recognized as dedupes.
- **Unique within its source:** cursor includes the source context (batchId derived per source+file); combined with `source_id` in the L1 index it is unique per source.
- **Same cursor + changed payload = conflict, not silent duplicate:** Because `observations.id` is caller-provided and the L2 fact index includes `payload_hash`, a repeated cursor with a different payload would hit L1 (`source_id, source_cursor`) → the stored row wins → `inserted=false`. To treat this as an explicit conflict rather than a silent duplicate, the pipeline should detect L1 match with a different `payload_hash` and surface it as a `cursor collision / payload changed` warning in the import report (not silently overwrite). The stored row is never overwritten by `ON CONFLICT DO NOTHING`.
- **L1 and L2 idempotency compatible:** L1 `(source_id, source_cursor)` and L2 `(source_id, asset_id, service_id, observed_at, payload_hash)` both hold.

**Limitation:** If the same logical record appears at two different positions in the same file (two indices), it gets two different cursors → two L1 rows; L2 dedup only collapses if `payload_hash` AND `observed_at` AND asset/service also match. This is acceptable: position is part of delivery identity, not fact identity. A truly identical record re-imported in a *different* batch (different batchId) gets a different cursor → L1 treats it as a new delivery; L2 collapses it to the same fact if content matches (deduped). This is correct at-least-once behavior.

### Why not one identical batch cursor for every row

Rejected: using one batch-level cursor for every row would violate L1 uniqueness `(source_id, source_cursor)` — only the first row inserts; all subsequent rows dedup to the first row's id, losing distinct observations. Must be per-record.

## 4.7 Observation ID (`observations.id`)

- Column has no sequence default; id is caller-provided.
- For JSONL import: derive deterministically, e.g. `fnv1a-64("import:<sourceID>:<sourceCursor>")` masked to positive BIGINT, mirroring the collector runner's `observationID`. Collisions land in the L1/L2 dedup path (never a correctness failure because `ON CONFLICT DO NOTHING` + lookup).
- The id is bookkeeping only; L1 `(source_id, source_cursor)` is the true idempotency key.

## 4.8 GSEER-derived identification (fingerprint)

- Computed at ingestion by the attached `fprule.Registry` over the raw evidence (`http.server`, `tls.version`, `ssh.banner`, etc.).
- Stored under `evidence["fingerprint"] = {technologies: [...], derived: [...]}` in the **stored copy only**; the caller's evidence map is never mutated.
- `PayloadHash` (L2 identity) is computed from the RAW delivered record and is never affected by fingerprint enrichment.
- A source-provided `fingerprint` key always wins (engine fills an absence only).
- Derived identification never creates a service, port, or protocol; it attaches to the observation's own row.
