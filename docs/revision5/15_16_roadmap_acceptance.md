# Section 15 — Dependency-Driven Roadmap

## 15.1 Existing foundation (v0.1.0, verified in source at `d84b291`)

- Asset/service/observation/domain model with `(kind,value)` asset uniqueness and `(asset_id,port,transport)` service uniqueness.
- `observations` with `observed_at`/`created_at`, L1 `(source_id,source_cursor)` and L2 fact uniqueness, `provenance`, `evidence JSONB`.
- `data_sources` registry; `EnsureSource`/`GetSource`/`SetSourceEnabled`; source checkpoints, collection jobs, dead-letter (0006).
- Suppression: Go gate + DB backstop trigger (0005); `suppression_applications` audit; no Asset→Suppression FK.
- Collector framework (`internal/collect`): asset-level only, terms review, checkpoints, dead-letter, re-drive.
- Fingerprint engine (`internal/fprule`): 22 lint-gated seed rules; ingestion-time evidence enrichment (`evidence["fingerprint"]`).
- SearchService shared by CLI/API/dashboard; GQL field registry + validator + planner; service mode (default) + asset mode; keyset pagination; facets; technologies list field.
- CLI: `search`, `setup` (self-hosted, with-compose, load/purge fixtures, QS-6 gate), `doctor`, `source`, `collect` (run/status/redrive), `suppress`, `rules`, `db`, `server`, `export`, `update`. `gseer asset` and `gseer scan` are hidden stubs (exit 64).
- No JSONL import command. No service-level observation ingestion in collect path.

## 15.2 v0.2.0 mandatory service intelligence

- ServiceFact contract (10 fields) and service-level observation ingestion.
- `operator-import` / JSONL import path (register source → import JSONL → persist asset/service/observation) with per-record transactional ingestion, L1/L2 idempotency, suppression gate, dead-letter, dry-run.
- Deterministic per-record `source_cursor` strategy (batch-id + record position); `source_cursor` never trusted from JSONL.
- GQL canonical/legacy aliases: `service.port` ↔ `port` (both → canonical `port` service-level); `ALL(...)` stays rejected.
- Preserve `(asset_id,port,transport)` uniqueness; no duplicate services for hostnames; SNI/Host in evidence only.
- Fingerprint evidence storage via `evidence["fingerprint"]`; no new column/table.
- v0.2.0 = self-hosted only. `gseer asset` deferred. `gseer scan` deferred to future P2/PAC milestone. Public Search Service separately governed, not a v0.2.0 gate.
- Beginner workflow: `./bin/gseer` → `setup --self-hosted --with-compose` → compose up → `setup --self-hosted --db-url` → `doctor` → empty search → JSONL import → service search.

## 15.3 Deferred service intelligence (not mandatory for v0.2.0)

- `gseer asset` detail view + history.
- Service-level ingestion in the collector framework (`collect.Runner` building `Fact.Service != nil`).
- SNI/HTTP Host as service columns with extended uniqueness (post-v0.2.0 migration).
- Automatic staleness marking / retention expiration.
- Asset relationship/edge table (evidence-backed, no graph DB).
- Batch-level all-or-nothing ingestion option.

## 15.4 Future active observation (separate authorization + security-gated)

- `gseer scan`; P2/PAC regime; GPIM G-1 through G-8; owner go/no-go; pilot program; security review. Not in v0.2.0.

## 15.5 Future Public Search Service (separately governed deployment)

- Separately governed; opt-in Distributed Community Collector Network. Not a v0.2.0 implementation or release gate. Self-hosted only in v0.2.0.

## 15.6 Future community collectors

- Opt-in community deployment model, explicitly governed, separate capability. Not in v0.2.0.

**Long-term identity preserved:** GSEER — Independent Internet Asset Intelligence & Search Engine. JSONL import is an initial data-ingestion path, not the final definition of GSEER's collection architecture.

---

# Section 16 — Milestone Acceptance Criteria

Each criterion lists the verification type:
- **U** = unit verification
- **DB** = database integration verification
- **CLI** = CLI end-to-end verification
- **API/D** = API/dashboard verification
- **X** = real external collection (separately authorized; NOT required for v0.2.0)

| # | Criterion | Type |
|---|-----------|------|
| AC-1 | Fresh database reports honest EMPTY index (doctor HINT, empty search exit 0). | DB + CLI |
| AC-2 | Valid JSONL import: register `operator-import` source, import well-formed JSONL, assets/services/observations persisted with correct counts. | CLI + DB |
| AC-3 | Invalid JSONL handling: malformed line / invalid record dead-lettered with reason, never silently dropped; valid records still imported. | CLI + DB |
| AC-4 | Dry-run with zero unintended writes: `--dry-run` validates and reports; no asset/service/observation/checkpoint/dead-letter writes. | CLI + DB |
| AC-5 | Stable source registration: `EnsureSource` upserts on `name`; repeated registration reuses the same `source_id`; `enabled` not flipped as side effect. | DB |
| AC-6 | Per-record cursor uniqueness: distinct records in same import get distinct `source_cursor`; no two rows share `(source_id, source_cursor)`. | DB |
| AC-7 | Exact replay idempotency: re-importing the same JSONL yields L1 dedup (same cursors), `inserted=false`, no duplicate observations. | CLI + DB |
| AC-8 | Same cursor with changed payload: treated as conflict (stored row wins, reported), not silently accepted as duplicate. | DB |
| AC-9 | L2 duplicate detection: identical content within a source collapses to one fact (deduped), even under a new cursor. | DB |
| AC-10 | Out-of-order observations: `last_seen` never regresses (`GREATEST`); `first_seen` preserved on conflict. | DB |
| AC-11 | Asset/service timestamp consistency: `observed_at` = source time; `created_at` = insert time; distinct; no `ingested_at` column. | DB |
| AC-12 | Suppression enforcement: suppressed asset/service/ingestion blocked at Go gate and DB backstop; counted; audit row in `suppression_applications`. | CLI + DB |
| AC-13 | Service endpoint uniqueness: `UNIQUE (asset_id, port, transport)`; duplicate insert upserts, does not create duplicate service. | DB |
| AC-14 | Multiple hostnames sharing an endpoint: one service row `(ip,443,tcp)`; hostnames recorded in evidence / separate hostname asset; no duplicate service per hostname. | CLI + DB |
| AC-15 | Source evidence preservation: `observations.evidence` stored verbatim; fingerprint enrichment is copy-on-write; caller's evidence not mutated; `PayloadHash` unaffected. | DB |
| AC-16 | Fingerprint evidence provenance: `evidence["fingerprint"]` holds `technologies` + `derived`; source-provided `fingerprint` key wins; derived never overwrites source. | DB |
| AC-17 | Legacy GQL compatibility: `port==443`, `port=443`, `server="nginx"`, `status==200`, `domain IN_DOMAIN`, `ip IN_CIDR`, `ANY(host){...}` all valid. | CLI |
| AC-18 | Canonical service-prefixed GQL fields: `service.port==443` normalizes to same planner field/SQL as `port==443`; identical results. | CLI |
| AC-19 | Sorting and facets: `--sort`, `--facets` work on service fields; aliases don't create duplicate dimensions; facet counts match filtered row set. | CLI |
| AC-20 | Multi-service pagination: asset with N services returns N rows in service mode; stable cursor over `(row_id,row_kind)`; asset mode returns one row per asset. | CLI + DB |
| AC-21 | Partial import failure behavior: one bad fact dead-lettered, healthy facts still imported; run reports stats + error. | CLI + DB |
| AC-22 | Beginner setup and recovery: `make build` → `./bin/gseer` → setup → doctor → empty search → import → search; recovery pointers for dirty migrations / empty index. | CLI |
| AC-23 | Regression compatibility with v0.1.0: existing `gseer search`/`doctor`/`source`/`collect`/`rules` behavior unchanged; `gseer asset`/`gseer scan` still exit 64. | CLI + DB |

**Verification separation:** Unit tests alone do not claim end-to-end operational readiness. Database integration, CLI end-to-end, and API/dashboard checks are required for AC-2 through AC-22. No real external collection (AC-X) is required for v0.2.0 JSONL import acceptance.
