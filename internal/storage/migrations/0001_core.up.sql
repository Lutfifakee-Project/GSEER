-- GSEER migration 0001_core.up.sql — v0.0.1 core tables + uniqueness indexes.
--
-- Scope (owner-approved M3 schema, SPEC-05 §2/§3/§5):
--   assets, services, observations, data_sources
--   L1 delivery-identity + L2 fact-identity uniqueness (SPEC-05 §5)
--   search-performance indexes (SPEC-05 §3)
--
-- NOT in this migration (deferred, owner decision D-M3-1):
--   source_checkpoints, ingestion_dead_letter, scopes,
--   collection_jobs, suppressions  → v0.0.2
--   latest_http / latest_tls / latest_fingerprints views → 0002
--   search_index union view → M4 (planner target, lands with repositories)
--
-- The schema_migrations table is NOT created here: it is owned and
-- managed by the golang-migrate driver itself (owner directive §2).
--
-- Every statement here is transaction-safe (no CONCURRENTLY, no VACUUM,
-- no CREATE DATABASE): the migrate driver wraps this file in a single
-- atomic transaction (MultiStatementMode=false).

-- ---------------------------------------------------------------------------
-- data_sources — source registry.  observations.source_id FKs here; the
-- fixture source (M4) is itself a row in this table with terms_ref
-- documenting its labeled-synthetic nature (owner decision D-M3-1/D-M3-2).
-- ---------------------------------------------------------------------------
CREATE TABLE data_sources (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name         TEXT        NOT NULL UNIQUE,
    -- connector "type" (e.g. "fixture", "ct", "rdap", "dns").  v0.0.1
    -- registers only "fixture"; real connectors are v0.0.2 (SPEC-09 §1).
    connector    TEXT        NOT NULL,
    -- Per-source terms/license review reference (SPEC-05 §2).  Free-form
    -- in v0.0.1 (the fixture source records its synthetic labeling);
    -- reviewed sources carry a real reference from v0.0.2.
    terms_ref    TEXT        NOT NULL DEFAULT '',
    config       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    enabled      BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- assets — the searched entities (ip / domain / hostname; SPEC-05 §2 row 1).
-- Unique per (kind, value) so the fixture loader and v0.0.2 collectors
-- upsert idempotently (AC-4.2).
-- ---------------------------------------------------------------------------
CREATE TABLE assets (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind        TEXT        NOT NULL CHECK (kind IN ('ip', 'domain', 'hostname')),
    value       TEXT        NOT NULL,
    -- Typed columns mirroring the kind (SPEC-05 §2); inet gives CIDR
    -- indexing for the planner's IN_CIDR operator (M9).
    ip          INET,
    domain      TEXT,
    hostname    TEXT,
    asn         INTEGER,
    asn_org     TEXT,
    geo_country TEXT,
    geo_region  TEXT,
    geo_city    TEXT,
    -- sources that have observed this asset (array for search_index).
    sources     TEXT[]      NOT NULL DEFAULT '{}',
    first_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    stale       BOOLEAN     NOT NULL DEFAULT FALSE,
    CONSTRAINT assets_kind_value_unique UNIQUE (kind, value),
    CONSTRAINT assets_kind_fields_check CHECK (
        (kind = 'ip'       AND ip      IS NOT NULL) OR
        (kind = 'domain'   AND domain  IS NOT NULL) OR
        (kind = 'hostname' AND hostname IS NOT NULL)
    )
);

-- ---------------------------------------------------------------------------
-- services — a service observed on an asset (SPEC-05 §2 row 2).  Unique
-- per (asset_id, port, transport) for idempotent upserts (AC-4.3).
-- ---------------------------------------------------------------------------
CREATE TABLE services (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    asset_id   BIGINT      NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    port       INTEGER     NOT NULL CHECK (port BETWEEN 1 AND 65535),
    transport  TEXT        NOT NULL CHECK (transport IN ('tcp', 'udp')),
    protocol   TEXT        NOT NULL,
    name       TEXT        NOT NULL DEFAULT '',
    product    TEXT        NOT NULL DEFAULT '',
    version    TEXT        NOT NULL DEFAULT '',
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    stale      BOOLEAN     NOT NULL DEFAULT FALSE,
    CONSTRAINT services_asset_port_transport_unique UNIQUE (asset_id, port, transport)
);

-- ---------------------------------------------------------------------------
-- observations — the provenance surface (SPEC-05 §2 row 3, §5, §6).
-- Owner decision D-M3-2: shipped in v0.0.1 because fixture rows carry
-- provenance=fixture (QS-6/AC-9.7.x) and the L1 delivery-identity
-- uniqueness that makes --load-fixtures idempotent lives here.
-- ---------------------------------------------------------------------------
CREATE TABLE observations (
    id            BIGINT      NOT NULL PRIMARY KEY,
    source_id     BIGINT      NOT NULL REFERENCES data_sources(id),
    asset_id      BIGINT      NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    service_id    BIGINT      REFERENCES services(id) ON DELETE CASCADE,
    observed_at   TIMESTAMPTZ NOT NULL,
    payload_hash  TEXT        NOT NULL,
    -- FR-4 delta: source delivery cursor (L1 identity; SPEC-05 §5).
    source_cursor TEXT        NOT NULL,
    -- Labeled provenance: "fixture" for the synthetic dataset (QS-6);
    -- real collectors use their source category from v0.0.2.
    provenance    TEXT        NOT NULL DEFAULT 'fixture',
    evidence      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- L1 — delivery identity: the same source record re-delivered across
-- restarts/retries/replay is processed once (SPEC-05 §5; AC-4.2).
CREATE UNIQUE INDEX ux_observations_delivery
    ON observations (source_id, source_cursor);

-- L2 — fact identity: distinct records with identical content within a
-- source collapse to one fact (SPEC-05 §5).  Two different sources
-- observing the same endpoint still yield two rows (provenance kept).
CREATE UNIQUE INDEX ux_observations_fact
    ON observations (source_id, asset_id,
                     COALESCE(service_id, 0::bigint),
                     observed_at, payload_hash);

-- FK-supporting and query indexes.
CREATE INDEX ix_observations_asset     ON observations (asset_id);
CREATE INDEX ix_observations_service   ON observations (service_id);
CREATE INDEX ix_observations_source    ON observations (source_id, observed_at);
CREATE INDEX ix_services_asset         ON services (asset_id);
CREATE INDEX ix_assets_ip              ON assets USING gist (ip inet_ops);
CREATE INDEX ix_assets_domain_hostname ON assets (domain, hostname);
