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
