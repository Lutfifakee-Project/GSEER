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
