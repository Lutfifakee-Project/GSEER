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
