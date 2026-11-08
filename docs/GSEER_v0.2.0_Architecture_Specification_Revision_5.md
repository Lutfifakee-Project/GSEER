# GSEER v0.2.0 — Architecture Specification Revision 5

**Status:** Inspection, architecture correction, and planning only — design document.
**Repository:** `Lutfifakee-Project/GSEER`
**Baseline:** `d84b291583bf2f0ec8d3499faa62031239e2681b` (which is tag `v0.1.0`)
**Input:** Architecture Specification Revision 4
**Restriction:** Read-only inspection and reporting. No implementation, repository modification, migration, commit, push, tag, release, deployment, collection, or scanning is authorized. Wait for explicit owner approval before implementation begins.

---

## Document control

| Section | Title |
|---------|-------|
| 1 | Correct Release Integrity Report |
| 2 | Verified Source Architecture |
| 3 | Current Schema and Entity Relationships |
| 4 | Final ServiceFact Contract |
| 5 | Source Registration and JSONL Import Contract |
| 6 | Idempotency and Transaction Model |
| 7 | Suppression and Retention Policy |
| 8 | Virtual Hosting and Endpoint Identity |
| 9 | Fingerprinting Evidence Model |
| 10 | Canonical GQL Compatibility Specification |
| 11 | Freshness Lifecycle |
| 12 | Security and Authorization Boundaries |
| 13 | Beginner Workflow |
| 14 | Complete ADR Index with Unique IDs |
| 15 | Dependency-Driven Roadmap |
| 16 | Milestone Acceptance Criteria |
| 17 | Explicit v0.2.0 Non-Goals |
| 18 | Outstanding Owner Decisions |
| 19 | Remaining Technical Risks |
| 20 | Final Architecture Recommendation |
| 21 | Final Verdict |

---

**Conventions used throughout:**

- *Implemented* — present in v0.1.0 source at baseline `d84b291`, exercised by tests.
- *Partial* — a type/function exists but the claimed behavior is not wired into a real call path.
- *Proposed* — specified here for v0.2.0; not present in v0.1.0 source.
- *Absent* — no code path exists.

---

**Long-term identity preserved:** GSEER — Independent Internet Asset Intelligence & Search Engine. JSONL import is an initial data-ingestion path, not the final definition of GSEER's collection architecture.
# Section 1 — Correct Release Integrity Report

## 1.1 Git diff evidence for `3432d32..d84b291583bf2f0ec8d3499faa62031239e2681b`

The exact `git diff --name-status` output was executed at the baseline `d84b291` and produced:

```
M	CHANGELOG.md
M	README.md
A	RELEASE_NOTES_v0.1.0.md
A	banner.png
M	docs/INSTALL.md
M	docs/QUICKSTART.md
M	docs/ROADMAP.md
M	docs/UPDATE.md
A	internal/cli/banner.go
A	internal/cli/banner_test.go
M	internal/cli/doctor.go
M	internal/cli/no_phantom_commands_test.go
M	internal/cli/root.go
M	internal/cli/root_test.go
M	internal/cli/source_collect_test.go
```

### Actual counts (Git name-status)

| Status | Count | Files |
|--------|-------|-------|
| Modified (`M`) | 11 | CHANGELOG.md, README.md, docs/INSTALL.md, docs/QUICKSTART.md, docs/ROADMAP.md, docs/UPDATE.md, internal/cli/doctor.go, internal/cli/no_phantom_commands_test.go, internal/cli/root.go, internal/cli/root_test.go, internal/cli/source_collect_test.go |
| Added (`A`) | 4 | RELEASE_NOTES_v0.1.0.md, banner.png, internal/cli/banner.go, internal/cli/banner_test.go |
| **Total changed** | **15** | — |

### Verification of Revision 4's count

Revision 4 stated "11 modified files, 4 added files, 15 total changed files." **This count is correct.** Git name-status confirms exactly 11 `M` and 4 `A` for the diff `3432d32..d84b291`.

## 1.2 Tag relationship

- `git rev-list -n 1 v0.1.0` returns `d84b291583bf2f0ec8d3499faa62031239e2681b`.
- The current HEAD (`d84b291`) **is** the `v0.1.0` release tag. Nothing in the working tree is "after" the release except one uncommitted formatting change.
- The uncommitted change (`internal/cli/banner_test.go`, formatting-only per restriction) must remain untouched and is NOT part of the `3432d32..d84b291` diff.

## 1.3 Working-tree difference

| Item | State |
|------|-------|
| Uncommitted change | `internal/cli/banner_test.go` — modified, not staged |
| Restriction | Formatting-only; must remain untouched; not part of any release diff |
| Is it "untracked"? | No — it is a tracked file with a working-tree modification |

The uncommitted change is tracked (`banner_test.go` was added in commit `3432d32` as part of the diff above), so it must NOT be described as untracked.

## 1.4 Distinctions per restriction

| Category | Determination | Evidence |
|----------|---------------|----------|
| Files included in release tag `v0.1.0` | The full tree at `d84b291` | `git rev-list -n 1 v0.1.0 == HEAD` |
| Files modified after the release | None committed; only the uncommitted `banner_test.go` formatting change | `git status` shows only `banner_test.go` modified |
| Current working-tree difference | `internal/cli/banner_test.go` only (formatting-only, must remain untouched) | `git status` |
| Release verification | 15 changed files in `3432d32..d84b291`: 11 modified + 4 added | `git diff --name-status 3432d32..d84b291` |

## 1.5 Test-result reporting discipline

No test results are claimed in this section without identifying environment and execution. This section reports only git metadata (name-status output, tag resolution, working-tree status), which was observed directly against the repository at baseline `d84b291` on the local workstation.
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
# Section 2 — Verified Source Architecture (Part C: `internal/search/`, `internal/gql/`, `internal/fprule/`, `internal/suppress/`, `internal/dashboard/`, `internal/api/`, `internal/cli/`)

## 2.13 `internal/search/` — SearchService

| File | Symbol | Status | Actual current behavior |
|------|--------|--------|-------------------------|
| `service.go` | `Service` | Implemented | Single search implementation shared by CLI (`gseer search`), REST API, and dashboard. `Search(ctx, Query) (*Result, error)`: validate → plan → execute → shape. `DefaultMode = ModeService` (PD-1). Read-only against `search_index`; no collector/egress dependency (AC-3.1 import-graph test). |
| | `Query` | Implemented | `GQL`, `Mode` ("service"/"asset"), `Sort`, `Limit`, `Timeout`, `Cursor`, `Fields`, `WithStats`. |
| | `Result` / `Row` | Implemented | `Rows []Row`, `NextCursor`, `HasMore`, `QueryInfo`, `Index`, `Explain`, `Notice`. `Row` has `row_kind` (`service` \| `asset-only`), asset fields, service fields (`port`, `transport`, `protocol`, `name`, `product`, `version`, etc.), http/tls fields, `technologies []string`, `sources []string`, `first_seen`, `last_seen`, `stale`. |
| | Mode semantics | Implemented | `ModeService` = one row per (asset, service) pair plus orphan assets (raw `search_index` projection). `ModeAsset` = `DISTINCT ON (asset_id)` collapsing to one row per asset (most recent `last_seen`). |
| | Pagination | Implemented | Keyset pagination over composite row key `(row_id, row_kind)` + sort value. `row_id` alone is NOT unique across the union view (services.id on service rows, assets.id on asset-only rows are independent sequences that can collide numerically). Cursor is base64 `CursorPayload{SortField, RowID, RowKind, Value, Null}`. |
| `facets.go` | `Facets` | Implemented | Same GQL chain as search (parse → validate → `CompileWhere`). Count semantics by mode: service mode counts each (asset,service) row once; asset mode counts `DISTINCT (row_kind, asset_id)`. Dimensions: port, protocol, technology, service, country, source, freshness. |
| `row.go` | `Row` + scanner | Implemented | Projection-aware scanner binds by column name. `sortValue` supports string/int/time fields including `tls.cert_expiry`. |
| `stats.go` | `Stats` | Implemented | Index freshness metadata (`Empty`, `FixtureOnly`, `Assets`, `Services`, `Observations`, `LastObserved`, `Notice()`). Used by doctor and `WithStats`. |

## 2.14 `internal/gql/` — field registry, validator, planner

| File | Symbol | Status | Actual current behavior |
|------|--------|--------|-------------------------|
| `validator/validator.go` | `fields` registry | Implemented | 24 canonical fields. Key entries: `port` (TypeInt, canonical `port`), `transport`, `protocol`, `name`, `product`, `version` (all TypeString), `http.status` (TypeInt, alias `status`), `http.title` (alias `title`), `http.server` (alias `server`), `tls.cert_expiry` (TypeTime, alias `cert_expiry`), `technologies` (TypeList, aliases `tech`, `fingerprint`), `sources` (TypeList), `first_seen`/`last_seen` (TypeTime), `stale` (TypeBool), `geo_country` (alias `country`), `geo_region` (alias `region`), `geo_city` (alias `city`), `ip` (TypeString + `IN_CIDR`), `domain`/`hostname` (TypeString + `IN_DOMAIN`). |
| | Alias rewrite | Implemented | `lookupField` resolves user-written name → canonical key; alias rewritten in place. `port` and `port` are the same canonical field `port` (no `service.port` prefix exists in v0.1.0). |
| | Typing | Implemented | Strict: INT fields reject quoted values; TIME fields require RFC 3339; BOOL accepts only `"true"`/`"false"`; LIST requires CONTAINS. |
| | `ALL(...)` | Rejected | `QuantAll` → `GQL_E_NOT_SUPPORTED_IN_VERSION` with `!ANY` suggestion. Kept rejected in v0.2.0 unless separately approved. |
| `planner/planner.go` | `searchColumns` map | Implemented | Canonical path → `search_index` column. `port` → `port`, `transport` → `transport`, etc. Dotted paths (`http.title` → `http_title`, `tls.cert_subject_cn` → `cert_subject_cn`). **No `service.port` / `service.transport` prefixed canonical paths exist.** |
| | `Compile` | Implemented | Lowers validated AST to parameterized SQL Plan against `search_index`. Values are `$N` placeholders (never interpolated). Keyset cursor, `DISTINCT ON (asset_id)` for asset mode, deterministic tiebreaker `row_id ASC, row_kind ASC`. |
| | Sortable fields | Implemented | `port`, `transport`, `protocol`, `name`, `product`, `version`, `first_seen`, `last_seen`, etc. List (`technologies`, `sources`) and bool (`stale`) excluded from ORDER BY. |

### GQL alias findings (relevant to Section 10)

- There is NO `service.port` / `service.transport` / `service.protocol` / `service.name` / `service.product` / `service.version` field in v0.1.0. The canonical fields are bare: `port`, `transport`, `protocol`, `name`, `product`, `version`.
- `port` is TypeInt (canonical `port`). It is service-level data (from `search_index.port`), NOT asset-level. Revision 4 must not describe `port` as an asset-level port filter.
- Sorting/faceting operate on these bare service fields; no duplicate semantic dimensions exist in v0.1.0.

## 2.15 `internal/fprule/` — fingerprint engine

| File | Symbol | Status | Actual current behavior |
|------|--------|--------|-------------------------|
| `rule.go` | `Rule`, `MatchSpec`, `Test`, `Family`, `Field` | Implemented | Closed family set (web-server, cms, language, framework, web-tech, service). Closed field set (http.server, http.title, tls.version, tls.cert_subject_cn, tls.cert_issuer_cn, ssh.banner). `Lint()` validates ids, family, product, confidence, provenance, matches, tests (≥2, ≥1 positive + ≥1 negative), exploitation-pattern rejection. `LoadDir(dir)` loads `*.yaml` rules, one per file named `<id>.yaml`. |
| `engine.go` | `Registry.Apply(evidence)` | Implemented | Rule fires when ANY match spec hits (OR); best hit per rule; one product survives (highest confidence); returns `[]Match{RuleID, Family, Product, Version, Confidence}`. Fail-closed on compile problems. `Products(matches)` → `[]string` product names. |
| `seed/` | 22 rule files | Implemented | Enumerated in Section 9. |

## 2.16 `internal/suppress/` — suppression

| File | Symbol | Status | Actual current behavior |
|------|--------|--------|-------------------------|
| `matcher.go` | `Matcher` | Implemented | Compiled rule set: `ips`, `cidrs`, `domains`, `suffixes`, `sources` maps. `MatchesAsset(assetKind, v)` returns the suppressing rule or nil. Most-specific-first ordering (longest CIDR prefix / longest suffix; tie → smallest rule id). Disabled and expired rules skipped. Fail-closed on un-normalizable value. |
| `normalize.go` | `NormalizeIP`, `NormalizeDomain`, `NormalizeValue` | Implemented | Canonical: lowercase, no trailing dot, IDNA punycode (A-labels), netip-canonical IPs, masked CIDRs. |
| `doc.go` | semantics | Implemented | Two-layer fail-closed enforcement: Go gate (store) + DB backstop trigger (migration 0005). Cross-type propagation (domain rule blocking resolved IPs) deliberately NOT automatic. |

**Suppression relationship finding:** Suppression matches **assets** (asset-level only). There is no `suppressions` → `assets` foreign key (`suppression_applications.asset_id` is a PLAIN BIGINT, not a FK). One suppression rule can match multiple assets; one asset can match multiple rules (rule table has `UNIQUE (kind, value)`; matching is by value, not by asset row). `suppression_applications` is the audit/application record. There is NO Asset → Suppression N:1 relational constraint. Service ingestion uses the same suppression boundary only indirectly (asset gated when written; retraction cascades asset→services→observations via ON DELETE CASCADE).

## 2.17 `internal/dashboard/`, `internal/api/`

| File | Symbol | Status | Actual current behavior |
|------|--------|--------|-------------------------|
| `dashboard/dashboard.go` | dashboard | Implemented (thin) | Shares the `search.SearchService`; dashboard test exists. |
| `api/server.go`, `api/middleware.go`, `api/envelope.go` | REST API | Implemented | Token-authed (SHA-256 hash of plaintext token), trusted-proxy opt-in, `/api/v1/search`, `/api/v1/facets`, `/api/v1/export`, `/api/v1/health`. Shares `search.SearchService` so GQL semantics identical across CLI/API/dashboard. |
| **No `/api/v1/scan`** | — | Absent | Confirmed absent. Active observation is outside v0.1.0/v0.2.0 implementation scope. |

## 2.18 `internal/cli/` — commands

| Command | File | Status | Behavior / exit code |
|---------|------|--------|----------------------|
| `gseer` root | `root.go` | Implemented | Help tree truthful; `version` working; coming-later stubs. Exit codes: 0 success, 2 invalid input, 3 config, 4 unavailable, 5 auth, 64 usage/planned, 70 internal. |
| `gseer search` | `search.go` | Implemented | Calls `search.Service`. `--validate`, `--explain`, `--facets`, `--mode`, `--sort`, `--limit`, `--cursor`. |
| `gseer setup` | `setup.go` | Implemented | `--self-hosted` (reachability → PG version → migrations to head), `--with-compose` (write compose + `.env`), `--load-fixtures`/`--purge-fixtures` (QS-6 gate), `--db-url` override. |
| `gseer doctor` | `doctor.go` | Implemented | Config validity, binary identity, DB checks, index contents (EMPTY/fixture-labeled/live counts), server reachability (client profile). Read-only; never migrates. |
| `gseer source` | `source.go` | Implemented | `list`/`add`/`enable`/`disable`. Registration records terms review; enablement explicit; terms gate outranks hand-flipped enabled row. |
| `gseer collect` | `collect.go` | Implemented | `run` (`--source`/`--all`), `status`, `redrive`. Passive only; nothing scans; nothing auto-runs. |
| `gseer suppress` | `suppress.go` | Implemented | `add`/`list`/etc. Suppression policy management. |
| `gseer rules` | `rules.go` | Implemented | `list`/`validate` (lint + false-positive regression tests). |
| `gseer db` | `db.go` | Implemented | Migration/status/diagnose commands. |
| `gseer server` | `server.go` | Implemented | `start`/`status`/`stop`. API + dashboard. Migrations run automatically on start (QS-7). |
| `gseer export` | `export.go` | Implemented | Streamed JSON/CSV/NDJSON export. |
| `gseer update` | `update.go` | Implemented | Signed manifest → verify → checksum → atomic replace. |
| `gseer asset` | `coming_later.go` | **Hidden stub** | Registered as hidden command; any invocation prints pointer line + exits 64. NOT implemented. |
| `gseer scan` | `coming_later.go` | **Hidden stub** | Registered as hidden command; any invocation prints pointer line + exits 64. NOT implemented. Active observation waits on P2/PAC regime. |

### `gseer asset` registration verification

- `gseer asset` IS registered as a planned command (hidden, `DisableFlagParsing: true`, `Args: cobra.ArbitraryArgs`).
- It prints: `E_NOT_IMPLEMENTED: "gseer asset" is planned, not in this release (v0.1.0).` + `Asset detail view is planned for a later release — see docs/ROADMAP.md.` + `Exit code 64.`
- It calls `exitFunc(ExitUsagePlanned)` → exit code **64**.
- `TestNoPhantomCommands` (AC-5.7–5.8) asserts `scan` and `asset` are registered as `Hidden=true` so invocation does not fall through to cobra's "unknown command".

## 2.19 Summary of gaps relevant to v0.2.0

1. **No JSONL import command exists** anywhere in v0.1.0. Section 5 must define it from scratch.
2. **No service-level observation ingestion** in the collect path (asset-level only). Section 4/5 define the ServiceFact contract.
3. **No `service.port` prefixed GQL fields**; bare `port` etc. are service-level. Section 10 defines canonical/legacy aliases.
4. **Suppression is asset-level only**, no FK, no service-level suppression gate. Section 7 corrects the N:1 claim.
5. **`gseer asset` and `gseer scan` are stubs (exit 64).** Section 10 resolves the release boundary.
# Section 3 — Current Schema and Entity Relationships

## 3.1 Tables (migrations 0001_core, 0005_suppressions, 0006_collection)

### `data_sources`
```sql
id BIGINT IDENTITY PK,
name TEXT NOT NULL UNIQUE,
connector TEXT NOT NULL,            -- "fixture", "ct", "rdap", "dns", ...
terms_ref TEXT NOT NULL DEFAULT '',
config JSONB NOT NULL DEFAULT '{}'::jsonb,
enabled BOOLEAN NOT NULL DEFAULT FALSE,
created_at TIMESTAMPTZ NOT NULL DEFAULT now()
```
- Source registry. `observations.source_id` FKs here. Fixture source is itself a row (`name='fixture'`, `enabled=false`).

### `assets`
```sql
id BIGINT IDENTITY PK,
kind TEXT NOT NULL CHECK (kind IN ('ip','domain','hostname')),
value TEXT NOT NULL,
ip INET, domain TEXT, hostname TEXT,
asn INTEGER, asn_org TEXT,
geo_country TEXT, geo_region TEXT, geo_city TEXT,
sources TEXT[] NOT NULL DEFAULT '{}',
first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
last_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
stale BOOLEAN NOT NULL DEFAULT FALSE,
CONSTRAINT assets_kind_value_unique UNIQUE (kind, value),
CONSTRAINT assets_kind_fields_check CHECK (...)
```
- Uniqueness: `(kind, value)`. Only the kind's typed column is set.

### `services`
```sql
id BIGINT IDENTITY PK,
asset_id BIGINT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
transport TEXT NOT NULL CHECK (transport IN ('tcp','udp')),
protocol TEXT NOT NULL,
name TEXT NOT NULL DEFAULT '',
product TEXT NOT NULL DEFAULT '',
version TEXT NOT NULL DEFAULT '',
first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
last_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
stale BOOLEAN NOT NULL DEFAULT FALSE,
CONSTRAINT services_asset_port_transport_unique UNIQUE (asset_id, port, transport)
```
- Uniqueness: `(asset_id, port, transport)`. **No hostname / SNI / HTTP Host column.** Network endpoint identity = `(asset_id, port, transport)`.

### `observations`
```sql
id BIGINT NOT NULL PK,              -- caller-provided; no sequence default
source_id BIGINT NOT NULL REFERENCES data_sources(id),
asset_id BIGINT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
service_id BIGINT REFERENCES services(id) ON DELETE CASCADE,
observed_at TIMESTAMPTZ NOT NULL,    -- time the source made the observation
payload_hash TEXT NOT NULL,
source_cursor TEXT NOT NULL,         -- L1 delivery identity
provenance TEXT NOT NULL DEFAULT 'fixture',
evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
created_at TIMESTAMPTZ NOT NULL DEFAULT now()  -- time the row was inserted
```
- `observed_at` = observation time (source-made). `created_at` = insertion time. These are distinct and must not be conflated.
- L1 unique index: `ux_observations_delivery (source_id, source_cursor)`.
- L2 unique index: `ux_observations_fact (source_id, asset_id, COALESCE(service_id,0), observed_at, payload_hash)`.

### `suppressions` (0005)
```sql
id BIGINT IDENTITY PK,
kind TEXT NOT NULL CHECK (kind IN ('ip','cidr','domain','domain_suffix','source')),
value TEXT NOT NULL,                 -- canonical
reason TEXT NOT NULL DEFAULT '',
created_by TEXT NOT NULL DEFAULT '',
created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
expires_at TIMESTAMPTZ,
enabled BOOLEAN NOT NULL DEFAULT TRUE,
CONSTRAINT suppressions_kind_value_unique UNIQUE (kind, value)
```

### `suppression_applications` (0005) — audit, NO FK on asset_id
```sql
id BIGINT IDENTITY PK,
action TEXT NOT NULL CHECK (action IN ('ingest-blocked','retroactive-retract','retract-source')),
suppression_id BIGINT REFERENCES suppressions(id) ON DELETE SET NULL,
asset_id BIGINT,                     -- PLAIN BIGINT, NOT a foreign key
detail JSONB NOT NULL DEFAULT '{}'::jsonb,
applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
```

### `collection_jobs`, `source_checkpoints`, `ingestion_dead_letter`, `scopes` (0006)
- `collection_jobs(source_id, kind, status, stats, ...)`.
- `source_checkpoints(source_id, key, cursor, updated_at)`, PK `(source_id, key)`.
- `ingestion_dead_letter(source_id, payload JSONB, reason, attempts, first_seen, last_seen, status pending/resolved/discarded)`.
- `scopes(kind, value, mode, note)` — P2 active-observation scope registry, created but DELIBERATELY UNUSED by v0.0.2/v0.1.0.

## 3.2 Foreign keys and cascades

| FK | On delete |
|----|-----------|
| `services.asset_id → assets(id)` | ON DELETE CASCADE |
| `observations.asset_id → assets(id)` | ON DELETE CASCADE |
| `observations.service_id → services(id)` | ON DELETE CASCADE |
| `observations.source_id → data_sources(id)` | (no cascade clause listed; default NO ACTION) |
| `collection_jobs.source_id → data_sources(id)` | ON DELETE CASCADE |
| `source_checkpoints.source_id → data_sources(id)` | ON DELETE CASCADE |
| `ingestion_dead_letter.source_id → data_sources(id)` | ON DELETE CASCADE |
| `suppression_applications.suppression_id → suppressions(id)` | ON DELETE SET NULL |
| `suppression_applications.asset_id` | **NOT a foreign key** (plain BIGINT) |

## 3.3 Database triggers

- `trg_assets_suppression` BEFORE INSERT OR UPDATE ON assets → `fn_check_asset_suppression()`. Raises `GSUP1` when the asset matches an enabled, unexpired suppression rule. This is the DB-level backstop for asset writes only. There is **no** trigger on `services` or `observations` for suppression.

## 3.4 Entity relationships (verified, not invented)

- **Asset → Service:** 1:N. A service belongs to exactly one asset (`asset_id` FK, cascade). Uniqueness `(asset_id, port, transport)`.
- **Asset → Observation:** 1:N (an observation references one asset; cascade on asset delete).
- **Service → Observation:** 1:N (an observation references at most one service; `service_id` nullable = asset-level observation; cascade on service delete).
- **DataSource → Observation:** 1:N (`source_id` FK). One source produces many observations.
- **Suppression → Asset:** NOT a relational constraint. It is a **matching/enforcement policy**: a suppression rule matches assets by value (ip/cidr/domain/domain_suffix) or excludes a source by name. One rule can match many assets; one asset can match many rules. `suppression_applications` records each enforcement (ingest-block or retroactive-retract) as an audit row; `asset_id` is a plain BIGINT so the audit survives the deletion it records.
- **No relationship/edge table between assets exists** in v0.1.0. GSEER's long-term asset-correlation vision (evidence-backed relationships) is NOT present as explicit relationship records.

## 3.5 The "real observations are never deleted" vs ON DELETE CASCADE conflict

The schema uses `ON DELETE CASCADE` on `observations.asset_id` and `observations.service_id`. This means: **if an asset or service row is explicitly deleted, its observations are cascade-deleted by PostgreSQL.** This is a schema-level behavior, not an automatic retention mechanism.

Resolution for Revision 5:
- No automatic retention deletion and no automatic observation expiration exist in v0.1.0 (no background job, no TTL, no `expires_at` on observations).
- Explicit administrative deletion of an asset/service IS possible (the row can be deleted), and cascade deletes its observations. This is a deliberate schema choice.
- The statement "real observations are never deleted" means: GSEER does not automatically expire or garbage-collect observations based on age. It does NOT mean observations are immune to explicit administrative deletion or to suppression-retraction (which deletes fixture/suppressed records by match).
- v0.2.0 preserves the existing schema unless a justified migration is separately approved. No retention/migration change is proposed in Revision 5.

## 3.6 Suppression relationship correction (replaces Revision 4's incorrect N:1)

Revision 4 incorrectly described an "Asset → Suppression N:1" relationship. The truth:
- There is no FK from `suppressions` to `assets` or from `assets` to `suppressions`.
- Suppression is a policy evaluated at write time (Go gate `Matcher`) and at the DB backstop (trigger on `assets`).
- One suppression rule (e.g. `domain_suffix example.com`) can match many assets. One asset can be matched by multiple rules (different kinds/values).
- `suppression_applications` is the audit/application record (one row per enforced application), with `asset_id` as a plain BIGINT (not FK) so it survives the asset's deletion.
- Service ingestion shares the suppression boundary only indirectly: the asset is gated when written; a suppression retraction deletes the asset row, cascading its services and observations.

## 3.7 Asset relationships (long-term vision)

- No explicit relationship/edge table exists in v0.1.0. Deferred from v0.2.0 scope.
- Future architecture requirement (if approved): a relationship table with `type`, `source_asset_id`, `dest_asset_id`, `provenance`, `observed_at`, `evidence`, and optional `confidence`. A shared IP / FK / common provider / matching attribute must NOT automatically establish a semantic relationship.
- No graph database is introduced.
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
# Section 7 — Suppression and Retention Policy

## 7.1 Suppression is a matching/enforcement policy (not an Asset → Suppression N:1 relation)

Revision 4 incorrectly described an "Asset → Suppression N:1" relationship. The verified truth (Section 3):
- There is **no foreign key** from `suppressions` to `assets` or from `assets` to `suppressions`. `suppression_applications.asset_id` is a PLAIN BIGINT, not a FK.
- Suppression is a policy evaluated at write time:
  - **Go gate:** `internal/suppress.Matcher` consulted by the store's ingestion paths (`IngestFact`/`IngestFacts`/`UpsertAsset`) BEFORE any write. Suppressed facts are skipped cleanly, counted in `IngestStats.Suppressed`, and audited as an ingest-block row in `suppression_applications`.
  - **DB backstop:** trigger `trg_assets_suppression` → `fn_check_asset_suppression()` BEFORE INSERT OR UPDATE ON `assets`. Mirrors the Go matcher byte-for-byte (canonical values, dot-boundary suffix test, inet containment). Raises `GSUP1` (mapped to `DB_SUPPRESSED`) on violation. Fail-closed by construction.
- One suppression rule can match many assets (e.g. `domain_suffix example.com` matches all `*.example.com` assets). One asset can match multiple rules (different kinds/values). Winner: exact match beats suffix; among CIDRs/suffixes, most specific wins; tie → smallest rule id (deterministic audit).
- `suppression_applications` is the audit/application record: one row per enforced application (ingest-block, retroactive-retract, retract-source), with `detail` JSONB self-describing what was blocked/retracted. Rule identity denormalized into `detail` so rows survive rule removal.
- Suppression applies to **assets** (asset-level). Service ingestion shares the suppression boundary only **indirectly**: the asset is gated when written; a suppression retraction deletes the asset row, cascading services and observations via ON DELETE CASCADE. There is no separate service-level suppression gate in v0.1.0; v0.2.0 must not invent one without a migration decision.

### Suppression kinds
| Kind | Matches | Canonical form |
|------|---------|----------------|
| `ip` | `kind='ip'` assets | netip-canonical IP |
| `cidr` | `kind='ip'` assets in range | masked CIDR |
| `domain` | `kind='domain'` exact | lowercase, no trailing dot, punycode |
| `domain_suffix` | `kind='domain'`/`'hostname'` exact + label-boundary descendants | lowercase, no trailing dot, punycode |
| `source` | `data_sources.name` exclusion | exact source name |

- Cross-type propagation (a domain rule also blocking its resolved IPs) is deliberately NOT automatic — a policy decision, not syntactic.
- Expired rules (`expires_at <= now()`) stop matching immediately but remain listed for audit; never auto-deleted.

## 7.2 Retention policy

- **No automatic retention deletion.** No background job, no TTL, no `expires_at` on observations. v0.1.0 has none; v0.2.0 adds none.
- **No automatic observation expiration.** Observations are never expired based on age.
- **Explicit administrative deletion is allowed.** An asset or service row can be explicitly deleted; `ON DELETE CASCADE` deletes its observations (services→observations, assets→services→observations). This is schema behavior, not automatic retention.
- **Suppression retraction** deletes records by suppression match (not by provenance), and is orthogonal to the fixture purge (which is scoped by `provenance='fixture'` + fixture source id).

## 7.3 v0.2.0 schema preservation

Preserve the existing schema in v0.2.0 unless a justified change is separately approved. No retention migration is proposed in Revision 5.

## 7.4 What happens to observations when an asset/service is explicitly deleted

- Asset deleted → `ON DELETE CASCADE` deletes its services, and services' observations; also the asset's direct observations. All observations referencing the asset (directly or via its services) are cascade-deleted by PostgreSQL.
- Service deleted → its observations cascade-deleted.
- This is explicit administrative deletion, not automatic expiration.

## 7.5 Asset relationships (long-term vision, deferred)

- No explicit relationship/edge table in v0.1.0. Deferred from v0.2.0 scope.
- Future requirement (if approved): relationship table with `type`, `source_asset_id`, `dest_asset_id`, `provenance`, `observed_at`, `evidence`, optional `confidence`. A shared IP / FK / common provider / matching attribute does NOT automatically establish a semantic relationship.
- No graph database.
# Section 8 — Virtual Hosting and Endpoint Identity

## 8.1 Existing endpoint uniqueness constraint (verified)

`services` table:
```sql
CONSTRAINT services_asset_port_transport_unique UNIQUE (asset_id, port, transport)
```
- Network endpoint identity = `(asset_id, port, transport)`.
- There is **no** hostname / SNI / HTTP Host column on `services` or `observations`.
- `search_index`/`Row` has no `sni`, `http_host`, or `hostname`-of-service column.

## 8.2 The virtual-hosting problem

Several hostnames can share the same IP and port (e.g. `a.example.com:443` and `b.example.com:443` on the same IP). The current schema represents this as ONE service row `(asset=IP, port=443, transport=tcp)`. Different hostnames/SNI/Host cannot be distinguished as separate services.

## 8.3 Distinctions (Revision 5 must not collapse these)

| Concept | Where it lives (v0.1.0) | Notes |
|---------|------------------------|-------|
| Network endpoint identity | `services.(asset_id, port, transport)` | The unique service identity. |
| DNS name | `assets.value` (kind=hostname/domain) or observation evidence | A separate asset, not part of service identity. |
| TLS SNI | Not stored | Absent from schema. |
| HTTP Host authority | Not stored | Absent from schema. |
| Observation context | `observations.evidence` JSONB | Authority-specific details (SNI, Host, server cert) can live here. |

## 8.4 Policy decision (Revision 5)

**Preserve endpoint identity and record authority-specific details in observation evidence.** Do NOT create duplicate services merely to represent different hostnames. Do NOT silently change the `(asset_id, port, transport)` uniqueness constraint.

- Different hostnames resolving to the same IP:port map to ONE service row. The hostname is a separate `kind='hostname'` asset (if observed) and/or recorded in `observations.evidence`.
- SNI / HTTP Host observed during a scan/collection are stored in `observations.evidence` (raw source evidence), not as new service columns.
- Search can filter by `domain`/`hostname` asset and by `port`/`transport` service independently; it cannot (yet) filter "service on IP:443 with SNI x" because SNI is not a service column.

## 8.5 Limitation and migration proposal

**Limitation:** The current schema cannot represent SNI-specific or Host-specific service distinctions as separate service rows. This is a documented limitation, not a bug.

**Migration proposal (separate from v0.2.0 no-migration baseline):** If SNI/Host distinction is required, add a nullable `sni` / `http_host` column to `services` and extend the uniqueness to `(asset_id, port, transport, COALESCE(sni,''), COALESCE(http_host,''))`. This is a **post-v0.2.0 migration proposal**, requires separate owner approval, and is NOT part of Revision 5's implementation baseline.

## 8.6 Do not
- Do not create duplicate services for different hostnames sharing an endpoint.
- Do not silently change the existing uniqueness constraint.
- Do not claim the schema represents virtual-host-specific services when it does not.
# Section 9 — Fingerprinting Evidence Model

## 9.1 Seed rules enumeration (verified from `internal/fprule/seed/`)

The seed directory contains **22 rule files** (Revision 4's "22 rules" claim is correct):

| # | File | Rule ID | Family | Product |
|---|------|---------|--------|---------|
| 1 | web-server-apache.yaml | web-server-apache | web-server | Apache |
| 2 | web-server-caddy.yaml | web-server-caddy | web-server | Caddy |
| 3 | web-server-envoy.yaml | web-server-envoy | web-server | Envoy |
| 4 | web-server-gunicorn.yaml | web-server-gunicorn | web-server | Gunicorn |
| 5 | web-server-iis.yaml | web-server-iis | web-server | IIS |
| 6 | web-server-jetty.yaml | web-server-jetty | web-server | Jetty |
| 7 | web-server-kestrel.yaml | web-server-kestrel | web-server | Kestrel |
| 8 | web-server-lighttpd.yaml | web-server-lighttpd | web-server | Lighttpd |
| 9 | web-server-litespeed.yaml | web-server-litespeed | web-server | LiteSpeed |
| 10 | web-server-nginx.yaml | web-server-nginx | web-server | nginx |
| 11 | web-server-openresty.yaml | web-server-openresty | web-server | OpenResty |
| 12 | web-server-tomcat.yaml | web-server-tomcat | web-server | Tomcat |
| 13 | web-server-tornado.yaml | web-server-tornado | web-server | Tornado |
| 14 | web-server-werkzeug.yaml | web-server-werkzeug | web-server | Werkzeug |
| 15 | web-tech-cloudflare.yaml | web-tech-cloudflare | web-tech | Cloudflare |
| 16 | web-tech-varnish.yaml | web-tech-varnish | web-tech | Varnish |
| 17 | web-tech-vercel.yaml | web-tech-vercel | web-tech | Vercel |
| 18 | cms-wordpress.yaml | cms-wordpress | cms | WordPress |
| 19 | framework-django-admin.yaml | framework-django-admin | framework | Django Admin |
| 20 | language-php.yaml | language-php | language | PHP |
| 21 | language-python.yaml | language-python | language | Python |
| 22 | service-openssh.yaml | service-openssh | service | OpenSSH |

Per-rule detail (required evidence fields, fixtures, confidence) is asserted by each rule's own `Tests` block (≥1 positive, ≥1 negative) and linted by `fprule.Lint`. Positive/negative fixture coverage is encoded in the YAML `tests` blocks, not in a separate fixture manifest. **Revision 5 does not claim real-world precision or recall from synthetic fixtures** — coverage is regression-level only.

## 9.2 Where fingerprint results are stored / exposed

- **Ingestion:** `enrichEvidence` (fingerprint.go) merges `fingerprint` into the **stored evidence copy** when an engine is attached and healthy:
  ```json
  "fingerprint": {
    "technologies": ["nginx", ...],   // plain product names, confidence order
    "derived": [ {"rule_id","family","product","version","confidence"} ]
  }
  ```
  Stored in `observations.evidence` (JSONB). The caller's evidence map is never mutated; `PayloadHash` (L2 identity) is computed from the RAW record and never affected. A source-provided `fingerprint` key always wins (engine fills an absence only).
- **Search:** `search_index.technologies` is a `text[]` column; `technologies` is a queryable/list GQL field (CONTAINS element match) and a facet dimension. `Row.Technologies []string`.
- **The engine is NOT invoked at search time.** It only enriches at ingestion (when attached via config/fingerprint engine). `gseer collect run` attaches it.

## 9.3 Data boundaries (Revision 5 resolution)

| Concept | Definition | Where stored |
|---------|------------|--------------|
| Raw source evidence | What the source actually delivered | `observations.evidence` (verbatim, never overwritten) |
| Source-provided service name/product/version | Source's own identification | `services.name/product/version` (source-authoritative) |
| Source-provided fingerprint info | Source's own `fingerprint` key if present | `observations.evidence["fingerprint"]` (source wins) |
| GSEER-derived classification | `fprule` engine output over evidence | `observations.evidence["fingerprint.technologies"]` + `.derived` (derived, never overwrites source) |
| Rule version + provenance | Rule `ID`, `Family`, `Product`, `Version`, `Provenance` | In `fingerprint.derived` entries; rule registry itself |

### Integrity rules (do not violate)
1. **Do not overwrite source evidence.** Enrichment is copy-on-write; the raw evidence map is preserved.
2. **Do not overwrite authoritative source-provided identification with a weaker inferred result.** A source-provided `fingerprint` key wins; source-provided `product`/`version` on the service row are authoritative.
3. **Do not claim real-world precision/recall from synthetic fixtures.** Fixture tests are regression coverage only.

## 9.4 `fingerprint.technologies` storage resolution

Revision 4 did not sufficiently define how `fingerprint.technologies` is stored. Resolution:
- Stored as a nested JSON object under `observations.evidence["fingerprint"]["technologies"]` (a `[]string` of plain product names, confidence order) and `["fingerprint"]["derived"]` (detail array).
- Projected into `search_index.technologies` (text[]) so it is queryable/facetable. The projection populates `technologies` from the stored evidence's `fingerprint.technologies`.
- Derived results are attached to the observation's own row; they never create a service, port, or protocol.

## 9.5 Architectical options if derived results cannot be persisted without ambiguity

The existing schema supports derived fingerprint storage via `observations.evidence` JSONB + `search_index.technologies` text[] with no ambiguity, because evidence is extensible JSONB. **Recommended (least disruptive):** keep the current `evidence["fingerprint"]` structure; no new column or table needed. A separate `service_fingerprints` table is NOT proposed for v0.2.0 (would require a migration and is not justified by current usage).
# Section 10 — Canonical GQL Compatibility Specification

## 10.1 Canonical aliases (field registry)

Revision 4's recommendation adopted for v0.2.0:

| Canonical field (planner target) | Type | Legacy aliases | search_index column |
|----------------------------------|------|----------------|---------------------|
| `port` | Integer | `port` | `port` |
| `transport` | String | `transport` | `transport` |
| `protocol` | String | `protocol` | `protocol` |
| `name` | String | `name` | `name` |
| `product` | String | `product` | `product` |
| `version` | String | `version` | `version` |

**There is NO `service.port` / `service.transport` / ... prefixed field in v0.1.0.** The canonical fields are bare (`port`, `transport`, ...). Revision 4's "service-prefixed" canonical fields (`service.port`) do not exist in the source.

**Adopted compatibility direction for v0.2.0 (reconciled with source):**
- `service.port` → canonical `port` (the service-prefixed form normalizes to the same planner field as bare `port`).
- `port` → legacy alias, normalizes to canonical `port`.
- Equivalent for `transport`, `protocol`, `name`, `product`, `version`.

Rationale: The validator registry already maps aliases to canonical paths. Adding `service.port` as an alias that rewrites to canonical `port` preserves old queries (`port==443`) while allowing the service-prefixed spelling. Aliases normalize to the SAME planner field and SQL expression (`search_index.port`).

**Do not describe `port` as an asset-level port filter.** Both `port` and `service.port` refer to the same service-level data (`search_index.port`). `port` is TypeInt.

## 10.2 Correct types (verified + corrected)

| Field | Type | GQL value form | Notes |
|-------|------|----------------|-------|
| IP address (`ip`) | IP (string) | `"203.0.113.10"` | TypeString + `IN_CIDR`. Column `ip` is PG inet. |
| Port (`port`, `http.status`) | Integer | bare number `443` | TypeInt. Range ops allowed. |
| Status code (`http.status`) | Integer | bare number `200` | TypeInt, alias `status`. |
| First/last observation (`first_seen`, `last_seen`) | Timestamp | RFC 3339 `"2026-01-01T00:00:00Z"` | TypeTime. Range ops allowed. |
| Certificate expiration (`tls.cert_expiry`) | Timestamp | RFC 3339 | TypeTime, alias `cert_expiry`. Column `cert_expiry` (no `tls_` prefix in view). |
| Stale (`stale`) | Boolean | `"true"`/`"false"` | TypeBool. Only eq/neq. |
| Text attributes (`product`, `version`, `transport`, `protocol`, `name`, `http.server`, ...) | String | quoted `"nginx"` | TypeString / TypeList. |

**Parser literal support (verified):** The GQL lexer/parser supports StrValue (quoted), IntValue (bare number), ListValue (`[a, b]`). Timestamps are quoted strings validated as RFC 3339 by the validator. Bool is `"true"`/`"false"` quoted. The existing parser supports all required literal types.

**Preserve old query syntax:** `port=443` (single `=` sugar for `==`), `port==443`, `title="phpMyAdmin"`, `ip IN_CIDR "203.0.113.0/24"`, `domain IN_DOMAIN "example.net"`, `ANY(host){...}`, `ANY(service){...}`. All remain valid. Document compatibility behavior; do not silently break old queries.

## 10.3 `ALL(...)` rejection

`ALL(...)` is rejected by the validator (`GQL_E_NOT_SUPPORTED_IN_VERSION` with `!ANY` suggestion). **Keep `ALL(...)` rejected in v0.2.0 unless a separate, justified decision is approved.** Do not introduce new query-language semantics unnecessarily.

## 10.4 Result semantics

### One row per service (default service mode)
`ModeService` (PD-1 default): one row per (asset, service) pair plus orphan assets (asset-only rows where no service matches). A multi-service asset appears as multiple rows (one per service), each repeating the asset fields.

### One row per asset (asset mode)
`ModeAsset`: `DISTINCT ON (asset_id)`, preferring the row with the most recent `last_seen`, tiebroken by `row_id ASC`. One row per asset.

### Observations and current service attributes
Observations contribute to service attributes (`port`, `transport`, `protocol`, `name`, `product`, `version`, `last_seen`) via the service upsert (`latest sighting authoritative`; `last_seen = GREATEST`; `stale=false`). Each observation row is tied to one service (or asset-level if `service_id` NULL).

### Stable pagination with multi-service assets
Keyset pagination over composite row key `(row_id, row_kind)` + sort value. `row_id` alone is NOT unique across the union view (services.id on service rows, assets.id on asset-only rows are independent sequences that can collide numerically), so pagination advances over `(row_id, row_kind)`. In asset mode, pagination advances over deduplicated assets (`DISTINCT ON (asset_id)` applied before cursor/ORDER/LIMIT).

### Facets
Facets run the same GQL chain (`CompileWhere`) as search. Service mode counts each (asset,service) row once; asset mode counts `DISTINCT (row_kind, asset_id)`. Dimensions: port, protocol, technology, service (`name`), country, source, freshness. No duplicate semantic dimensions (aliases normalize to the same field).

### CLI / API / dashboard share one SearchService
`internal/search.Service` is the single implementation. `gseer search` (CLI), `GET /api/v1/search`, and the dashboard all call it. GQL semantics and pagination are identical everywhere.

## 10.5 Concrete queries and expected result shapes

```
gseer search 'port==443'
  → service mode: one row per (asset, service on port 443). Row: {asset_id, asset_value, port:443, transport, protocol, product, version, last_seen, technologies, sources, ...}

gseer search 'port==443' --mode asset
  → one row per asset that has a service on port 443 (DISTINCT ON asset_id).

gseer search 'product="nginx" && port==443'
  → services with product nginx on port 443.

gseer search 'ANY(host){ port==443 && product="nginx" }'
  → hosts (assets) that have ANY service on port 443 with product nginx. EXISTS subquery over search_index rows of same asset_id with row_kind='service'.

gseer search 'port==443' --facets
  → facets over port/protocol/technology/service/country/source/freshness for the filtered row set.

gseer search 'port==443' --validate
  → validate only, no DB contact.

gseer search 'port==443' --explain
  → AST → branch → SQL plan (parameterized, no literal interpolation).
```

Old queries remain valid:
```
gseer search 'port=443'            # `=` sugar for `==`, still valid
gseer search 'server="nginx"'      # alias `server` → canonical `http.server`
gseer search 'status==200'         # alias `status` → canonical `http.status`
```

## 10.6 Sorting and facets

- Sort fields: the sortable set (`port`, `transport`, `protocol`, `name`, `product`, `version`, `first_seen`, `last_seen`, `ip`, `domain`, `hostname`, etc.). List (`technologies`, `sources`) and bool (`stale`) excluded from ORDER BY. Default sort: `last_seen DESC`.
- Aliases (`service.port` → `port`) normalize to the same sortable field and SQL expression; sorting does not create duplicate dimensions.
- Facet dimension names are fixed; unknown names rejected with `E_QUERY_FACET`.

## 10.7 What v0.2.0 must NOT do
- Do not introduce `service.port` as a *different* field from `port`; they are the same.
- Do not describe `port` as asset-level.
- Do not add `ALL(...)` support without separate approval.
- Do not introduce new query-language semantics unnecessarily.
# Section 11 — Freshness Lifecycle

## 11.1 Timestamps (use the ACTUAL schema)

- `observations.observed_at`: time the source made the observation. Not ingestion time.
- `observations.created_at`: time the observation row was inserted into GSEER (`now()` default). Ingestion time.
- `assets.first_seen` / `assets.last_seen`: set on insert; `last_seen = GREATEST(...)` advances, never regresses; `first_seen` preserved on conflict.
- `services.first_seen` / `services.last_seen`: same pattern.
- `stale` (boolean): cleared on any fresh sighting; set by staleness logic (see below).

## 11.2 Staleness

`stale = false` on any fresh sighting (upsert sets `stale = false`). The reverse (marking stale) is a freshness-lifecycle concern: an asset/service is considered stale when its `last_seen` is older than the operator's staleness threshold (configured, not hardcoded in v0.1.0 schema). v0.1.0 does not auto-mark stale; it only clears stale on sighting. v0.2.0 preserves this unless a staleness job is separately approved.

## 11.3 Observation immutability

Once created, an observation's `observed_at`, `payload_hash`, `source_cursor`, `provenance`, `evidence` are not updated (`ON CONFLICT DO NOTHING` keeps the stored row). Observations are immutable after creation.

## 11.4 No automatic expiration

No automatic retention deletion and no automatic observation expiration in v0.1.0; v0.2.0 adds none. Observations persist until explicit administrative deletion or suppression retraction.

---

# Section 12 — Security and Authorization Boundaries

## 12.1 Active observation boundary (gseer scan)

- `gseer scan` is a **hidden stub** (exit 64) in v0.1.0; NOT implemented in v0.2.0.
- v0.2.0 must NOT implement scanners, create scan migrations, add `/api/v1/scan`, enable active collection, or treat index membership as authorization.
- Active observation waits on the P2/PAC regime (separate authorization + security review). GPIM activation blocked until G-1 through G-8 satisfied and owner go/no-go.

## 12.2 Search ≠ Scan seam (ADR-0010)

Enforced from M1: no command in `internal/cli` imports any collector/egress package. `internal/search.Service` is read-only against `search_index`; holds no collector/egress dependency (AC-3.1 import-graph test pins this). Index membership is NOT authorization to scan.

## 12.3 Terms review (passive connectors)

- Each connector carries a review verdict (`collect.Review`): `dns`/`ip2asn` reviewed-enabled; `rdap`/`ct` disabled pending provider authorization.
- `gseer source add --enable` refuses to enable a disabled-by-terms connector (`E_SOURCE_TERMS`).
- A hand-flipped `enabled=true` row cannot outrun the connector's code-level review gate (`collect.Runner.Run` checks `Review().Verdict != ReviewEnabled` → hard error).

## 12.4 Source registration boundary

- `source_id` assigned by GSEER's registration machinery; never trusted from JSONL.
- `operator-import` source: `connector="operator-import"`, `terms_ref` documents operator review, `enabled=false` default, explicit enablement, provenance `"operator-import"`.
- Imported data distinguishable from network-collected data via `provenance` + `data_sources.connector`. A JSONL field cannot masquerade as a trusted collector.

## 12.5 API / dashboard auth

- Token-authenticated (SHA-256 hash of plaintext token; plaintext cleared after Load).
- Trusted-proxy opt-in: `X-Forwarded-For` ignored unless the proxy is explicitly listed; client can never spoof identity via header.
- `gseer server start` binds loopback by default; non-loopback requires explicit operator action + auth.

## 12.6 Fixture gate (QS-6)

`setup --load-fixtures`/`--purge-fixtures`: when `policy.evaluation_fixtures` is false, requires interactive y/yes confirmation before any DB contact; piped scripts without the opt-in are refused. Fixture data is labeled `provenance='fixture'` and marked synthetic everywhere; never promoted to real data.
# Section 13 — Beginner Workflow

## 13.1 Verified actual setup implementation (v0.1.0)

| Step | Command | What it does |
|------|---------|--------------|
| Build | `make build VERSION=v0.1.0` | Compiles `./bin/gseer` with version ldflags (source-only release; no prebuilt binaries, no published Docker image). |
| Compose | `gseer setup --self-hosted --with-compose` | Writes `docker-compose.yml` (db-only) + generated `.env` (random password, chmod 600). Prints next commands verbatim. Files written FIRST, then stops. |
| Start DB | `docker compose up -d db` | Operator starts PostgreSQL. |
| Schema | `gseer setup --self-hosted --db-url "$(grep '^GSEER_DSN=' .env | cut -d= -f2-)"` | Reachability → PG version (15+) → migrations to head. Zero manual SQL. |
| Verify | `gseer doctor` | Config validity, binary identity, DB checks, index contents (EMPTY/fixture/live), server reachability. Read-only. |
| Search | `gseer search 'port==443'` | Honest empty state (exit 0) when index empty. |
| Server | `gseer server start` | API + dashboard on `http://127.0.0.1:8443` (loopback by default); migrations run automatically on start (QS-7). |
| Fixtures (opt-in) | `gseer setup --load-fixtures` | QS-6 gate; labeled synthetic evaluation data. |

## 13.2 What the default workflow must NOT require (v0.2.0 goal)

- Read or edit SQL. (Verified: setup runs migrations; zero manual SQL.)
- Understand migrations. (Verified: `gseer setup --self-hosted` runs them to head.)
- Extract database credentials from `.env`. (The quickstart shows `grep GSEER_DSN .env`, but the goal is to minimize manual DSN handling; `--with-compose` writes `.env` and the operator passes it via `--db-url`.)
- Repeat setup just to initialize the database. (Setup is idempotent: migrations to head.)
- Understand AST or query planner internals. (Not required for setup.)
- Manually assemble PostgreSQL connection strings. (`Database.EffectiveDSN()` assembles from components with proper percent-encoding; operator can set component fields or a single DSN.)

## 13.3 Executable path after `make build`

The binary is `./bin/gseer`. v0.1.0 distributes source, not official prebuilt binaries. Revision 5 must reference `./bin/gseer` (or `make run`), not bare `gseer`, unless installation/PATH behavior is actually provided. `make build VERSION=v0.1.0` → `./bin/gseer version` reports `gseer v0.1.0`.

## 13.4 Docker availability and PostgreSQL provisioning

- Docker: available for local builds (`docker build --build-arg VERSION=v0.1.0 -t gseer:local .`) or via `gseer setup --with-compose --full` → `docker compose -f docker-compose.full.yml up -d --build`.
- **No official GSEER image in any container registry for v0.1.0.** Image built locally from source.
- PostgreSQL: operator-provisioned (own PG 15+ or via compose `postgres:17-alpine`). **No automatic provisioning** that the current implementation cannot perform. `gseer setup --with-compose` writes files; `docker compose up -d db` is the operator's next command.

## 13.5 Recommended v0.2.0 beginner journey (no new commands beyond existing)

1. Install/build: `git clone ... ; cd GSEER ; make build VERSION=v0.2.0 ; ./bin/gseer version`
2. One guided self-hosted setup: `./bin/gseer setup --self-hosted --with-compose` → `docker compose up -d db` → `./bin/gseer setup --self-hosted --db-url <DSN>`
3. Clear health diagnostics: `./bin/gseer doctor` (all checks PASS; index reported EMPTY honestly).
4. Honest empty-index explanation: `./bin/gseer search 'port==443'` → empty, exit 0, with empty-index pointer.
5. First operator-provided service-data import: via the new `operator-import`/JSONL import path (Section 5) — register source, enable, import JSONL.
6. First successful service search: `./bin/gseer search 'port==443'`

Avoid hardcoding a fixed number of `PASS` checks unless guaranteed by the actual `doctor` implementation (doctor reports PASS/FAIL/HINT per check; the count varies by profile).

## 13.6 Recovery

- Dirty migration state → `gseer db migrate diagnose` (doctor reports FAIL with fix pointer).
- Index empty → `gseer setup --load-fixtures` (QS-6) or first real import.
- Fixture labeled → doctor reports HINT with counts; purge with `gseer setup --purge-fixtures`.
# Section 14 — Complete ADR Index with Unique IDs

## 14.1 Existing ADRs

**None exist.** A `find` for `*adr*` across the repository (excluding `.git`) returns no files. Therefore no existing ADR identifiers are in use, and Revision 5 allocates fresh unique IDs starting at `ADR-0001`. No placeholder `ADR-001X` values appear; every ID below is unique and used exactly once.

## 14.2 ADR index

| ID | Title | Status | Decision | Owner approval required? |
|----|-------|--------|----------|--------------------------|
| ADR-0001 | Search ≠ Scan seam | **Accepted (v0.1.0)** | No CLI command imports collector/egress; `gseer search` read-only; index membership is not authorization to scan. | No — already enforced in source (AC-3.1). |
| ADR-0002 | Suppression: asset-level matching policy, no Asset→Suppression FK | **Accepted (v0.1.0)** | Suppression is a matching/enforcement policy (Go gate + DB backstop trigger on `assets`); `suppression_applications.asset_id` is a plain BIGINT; one rule matches many assets; one asset matches many rules. | No — already in source (migration 0005). |
| ADR-0003 | Retention: no automatic expiration/deletion; explicit admin deletion cascades | **Accepted (v0.1.0)** | No background TTL/retention job; `ON DELETE CASCADE` on explicit asset/service deletion; no `expires_at` on observations. | No — existing schema. |
| ADR-0004 | ServiceFact contract (service-level observations) | **Proposed** | Define the 10-field ServiceFact contract (Section 4); asset identity, service endpoint identity, observation context, source identity, delivery cursor, timestamps, provenance, evidence, GSEER-derived ID. | **Yes** — new capability. |
| ADR-0005 | JSONL import cursor strategy | **Proposed** | `source_cursor` generated by GSEER (never trusted from JSONL); deterministic batch-id + record position: `sha256("v1|operator-import|<batchId>|rec:<i>")`. Same cursor+changed payload = conflict, not silent duplicate. | **Yes** — new import capability. |
| ADR-0006 | Ingestion batch failure policy | **Proposed** | Per-record transactional ingestion (matches `IngestFacts`); not all-or-nothing. Dry-run persists nothing. | **Yes** — new import capability. |
| ADR-0007 | GQL canonical/legacy aliases | **Proposed** | `service.port` and `port` both normalize to canonical `port` (service-level). Equivalent for transport/protocol/name/product/version. `port` is NOT asset-level. `ALL(...)` stays rejected. | **Yes** — query-language compatibility. |
| ADR-0008 | Virtual hosting & endpoint identity | **Proposed** | Preserve `UNIQUE (asset_id, port, transport)`; no duplicate services for different hostnames; SNI/HTTP Host recorded in `observations.evidence`; SNI/Host service-columns deferred to post-v0.2.0 migration. | **Yes** — schema decision. |
| ADR-0009 | Fingerprint evidence storage | **Proposed** | Store `fingerprint.technologies` + `fingerprint.derived` under `observations.evidence["fingerprint"]`; project to `search_index.technologies`; source-provided `fingerprint` key wins; never overwrite source evidence; no new column/table. | **Yes** — data model. |
| ADR-0010 | v0.2.0 release boundary | **Proposed** | `gseer asset` deferred (not in v0.2.0); `gseer scan` deferred to future P2/PAC-gated milestone; Public Search Service separately governed, not a v0.2.0 gate; only self-hosted in v0.2.0. | **Yes** — release scope. |
| ADR-0011 | Beginner workflow | **Proposed** | `./bin/gseer` after `make build`; `setup --self-hosted --with-compose` → compose up → `setup --self-hosted --db-url` → `doctor` → empty search → JSONL import → service search. No SQL, no DSN assembly, no fixed PASS count. | **Yes** — UX. |

## 14.3 Notes on status

- ADR-0001, ADR-0002, ADR-0003 reflect decisions already accepted and implemented in v0.1.0 source; Revision 5 documents them but does not create them.
- ADR-0004 through ADR-0011 are proposals awaiting owner approval. None is claimed accepted merely by appearing in this specification.
- Each proposal's full Context/Problem/Alternatives/Decision/Trade-offs/Rationale/Dependencies is in the corresponding numbered section of Revision 5 (Sections 4–13).
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
# Section 17 — Explicit v0.2.0 Non-Goals

| Non-goal | Rationale / disposition |
|----------|-------------------------|
| `gseer asset` detail view in v0.2.0 | Deferred to a later release (ADR-0010 / release boundary). It appears in neither v0.2.0 goals nor CLI commands; it remains a hidden stub (exit 64). |
| `gseer scan` / active observation | Outside v0.2.0 implementation scope. No scanners, no scan migrations, no `/api/v1/scan`, no active collection, index membership is not authorization. Moved to a separately gated future P2/PAC milestone. |
| `/api/v1/scan` endpoint | Not added. |
| Public Search Service as v0.2.0 release gate | Separately governed; readiness work (pilot, GPIM G-1..G-8, owner go/no-go) is not a mandatory v0.2.0 implementation. Self-hosted only in v0.2.0. |
| Public service implying unrestricted internet-wide active scanning | Explicitly not implied. |
| Community Collector Network in v0.2.0 | Opt-in, separately governed future capability; not implemented. |
| All-or-nothing batch ingestion | Deferred; per-record transactional ingestion recommended (ADR-0006). |
| SNI/HTTP Host as service columns | Deferred to post-v0.2.0 migration (ADR-0008). |
| Automatic retention deletion / observation expiration | Not added (ADR-0003). |
| `ingested_at` column | Not added; `created_at` already serves as ingestion timestamp. |
| Graph database | Not introduced. |
| `ALL(...)` GQL support | Remains rejected unless separately approved. |
| Asset relationship/edge table | Deferred (future architecture requirement only). |

# Section 18 — Outstanding Owner Decisions

| # | Exact question | Alternatives | Recommended choice | Consequence of each | Owner approval required? |
|---|----------------|--------------|--------------------|---------------------|--------------------------|
| D-1 | Include `gseer asset` in v0.2.0 or defer? | Include / Defer | **Defer** (ADR-0010). Keeps v0.2.0 coherent (service intelligence + JSONL import); asset detail is a larger UX piece. | Include: delays v0.2.0, expands scope. Defer: ships v0.2.0 on time; asset view later. | **Yes** — release scope. |
| D-2 | JSONL `source_cursor`: client-supplied or runner-generated? | Client / Runner-generated | **Runner-generated** (never trusted from JSONL); deterministic batch-id + record position (ADR-0005). | Client-supplied: collision/trust risks. Runner-generated: idempotent replay, unique within source. | **Yes** — import contract. |
| D-3 | Ingestion batch failure policy: per-record or all-or-nothing? | Per-record / All-or-nothing | **Per-record transactional** (ADR-0006), matching existing `IngestFacts`. | All-or-nothing: one poison record fails whole import. Per-record: partial progress preserved, poison isolated. | **Yes** — ingestion semantics. |
| D-4 | GQL `service.port` vs `port` relationship? | Same field / Separate fields | **Same field**: both normalize to canonical `port` (service-level) (ADR-0007). | Separate fields: duplicate dimensions, broken old queries. Same: compatible, preserves old syntax. | **Yes** — query language. |
| D-5 | Virtual hosting: change `(asset_id,port,transport)` uniqueness or preserve? | Preserve / Change | **Preserve** (ADR-0008); SNI/Host in evidence only; separate migration later if needed. | Change: migration required, schema drift. Preserve: no migration; documented limitation. | **Yes** — schema. |
| D-6 | Fingerprint derived results storage? | `evidence["fingerprint"]` / new table | **`evidence["fingerprint"]`** + project to `search_index.technologies` (ADR-0009); no new column/table. | New table: migration, complexity. Evidence JSONB: least disruptive, already supported. | **Yes** — data model. |
| D-7 | `gseer scan` acceptance criteria moved to which future milestone? | P2/PAC-gated milestone | **Future P2/PAC milestone** with GPIM G-1..G-8 + owner go/no-go + security review + pilot. | Not in v0.2.0. | **Yes** — release boundary. |
| D-8 | Public Search Service readiness in v0.2.0? | Gate / Not gate | **Not a v0.2.0 gate** (ADR-0010); separately governed. | Gate: blocks v0.2.0. Not gate: v0.2.0 ships self-hosted; public service later. | **Yes** — release boundary. |

# Section 19 — Remaining Technical Risks

| Risk | Description | Mitigation |
|------|-------------|------------|
| R-1 | JSONL import command not yet implemented; no service-level ingestion in collect path. | New implementation in v0.2.0; covered by AC-2..AC-22. |
| R-2 | `observations.id` caller-provided (no sequence); possible id collisions across independent imports. | Collisions land in L1/L2 dedup path (`ON CONFLICT DO NOTHING` + lookup); never a correctness failure. Derive IDs deterministically per source+cursor. |
| R-3 | One batch cursor for every row would break L1 uniqueness. | Rejected; per-record cursor = batch-id + record position (ADR-0005). |
| R-4 | `service.port` not present in v0.1.0; adding as alias to `port` is a query-language change. | Must be implemented carefully in validator/planner so old `port==443` still works identically (ADR-0007, AC-17/18). |
| R-5 | Suppression is asset-level only; no service-level suppression gate. | Service shares boundary indirectly (asset gated; retraction cascades). Do not invent a service-level gate without migration decision. |
| R-6 | Virtual-hosting limitation: SNI/Host not distinguishable as separate services. | Documented limitation; preserve uniqueness; post-v0.2.0 migration if required (ADR-0008). |
| R-7 | `doctor` PASS count varies by profile; avoid hardcoding `5 PASS`. | Report per-check PASS/FAIL/HINT; no fixed count (AC-22). |
| R-8 | Source-only release: no prebuilt binaries / Docker image. | Beginner workflow uses `make build` → `./bin/gseer`; Docker built locally (ADR-0011, Section 13). |

# Section 20 — Final Architecture Recommendation

**Adopt Revision 5 as the authoritative v0.2.0 architecture.** It resolves every contradiction from Revision 4:

1. Release integrity: 11 modified + 4 added = 15 (verified correct); `d84b291` is tag `v0.1.0`; uncommitted `banner_test.go` untouched.
2. Source architecture verified: service-level ingestion is NOT in v0.1.0 collect path (asset-level only); fingerprint engine invoked at ingestion only (optional, attached); suppression is asset-level matching policy (no Asset→Suppression FK); `gseer asset`/`gseer scan` are hidden stubs (exit 64); no JSONL command exists.
3. ServiceFact contract: 10 fields defined; timestamps use `observed_at`/`created_at` correctly; `source_id` never from JSONL; `operator-import` source defined; cursor contradiction resolved (runner-generated, batch-id + record position).
4. Ingestion semantics: per-record transactional, L1/L2 idempotency, cursor-collision = conflict, dry-run persists nothing, dead-letter poison records.
5. Suppression & retention: corrected N:1 → matching policy; no automatic expiration; explicit admin deletion cascades; schema preserved.
6. Virtual hosting: `(asset_id,port,transport)` preserved; SNI/Host in evidence; no duplicate services.
7. Fingerprint: `evidence["fingerprint"]` storage; source wins; no new column; 22 seed rules enumerated; no false precision claims.
8. GQL: `port` canonical (service-level); `service.port` alias → same; `ALL(...)` stays rejected; types corrected; old syntax preserved.
9. Release boundary: `gseer asset` deferred; `gseer scan` deferred to P2/PAC; Public Search Service separately governed (not a v0.2.0 gate); self-hosted only in v0.2.0.
10. Beginner workflow: `./bin/gseer` after `make build`; guided setup; honest empty index; no SQL/DSN assembly; no fixed PASS count.
11. ADRs: fresh unique IDs ADR-0001..ADR-0011; accepted vs proposed distinguished; none claimed accepted merely by appearing.

# Section 21 — Final Verdict

## CONDITIONALLY READY — OWNER DECISIONS REQUIRED

**Evidence:**
- The architecture is technically coherent and every contradiction from Revision 4 is resolved (Sections 1–16).
- All verified claims are grounded in the actual v0.1.0 source at baseline `d84b291` (tag `v0.1.0`).
- No mandatory schema, ingestion, GQL, security, or release-boundary contradictions remain *in the specification*.

**However,** the following owner decisions (D-1 through D-8, Section 18) are required before implementation can begin, and the implementation work itself (JSONL import command, service-level ingestion, GQL alias wiring, ADR-0004..0011) is not yet done:

- D-1: Defer `gseer asset` (recommended).
- D-2: Runner-generated `source_cursor` (recommended).
- D-3: Per-record transactional ingestion (recommended).
- D-4: `service.port` and `port` same canonical field (recommended).
- D-5: Preserve `(asset_id,port,transport)` uniqueness (recommended).
- D-6: `evidence["fingerprint"]` storage (recommended).
- D-7: `gseer scan` to future P2/PAC milestone (recommended).
- D-8: Public Search Service not a v0.2.0 gate (recommended).

**Final restriction honored:** Revision 5 is a design document only. No implementation, repository modification, migration, commit, push, tag, release, deployment, collection, or scanning has been performed or is authorized. Wait for explicit owner approval before implementation begins.
