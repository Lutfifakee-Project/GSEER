-- GSEER migration 0006_collection.up.sql — the collector-side
-- bookkeeping tables (v0.0.2 Passive Intelligence, milestone M14).
--
-- Scope (owner decisions D-M3-1/D-M3-3, deferred from M3 into v0.0.2):
--   collection_jobs       — one row per connector run (observability)
--   source_checkpoints    — resumable per-source positions (retry/checkpoint
--                           behavior, owner directive §4)
--   ingestion_dead_letter — records that could not be ingested, preserved
--                           for re-drive instead of being dropped silently
--   scopes                — the P2 active-observation scope registry.
--                           Created here per the deferred-tables register
--                           but DELIBERATELY UNUSED by v0.0.2: the P1
--                           passive connectors do not target anything —
--                           they receive or look up data about identifiers
--                           the operator or the index already names.  The
--                           scopes table belongs to the PAC regime
--                           (SPEC-06 §3) that gates `gseer scan` (P2).
--
-- Suppression interplay (M13, by construction): every ingestion path —
-- including everything a collector produces — passes the suppression gate
-- and the assets backstop trigger.  A suppressed record a collector
-- delivers is skipped (or, on a bypass, refused by the database), never
-- silently re-ingested.  The dead letter preserves the payload for audit
-- without re-ingesting it.
--
-- Transaction-safe like every GSEER migration.

-- ---------------------------------------------------------------------------
-- collection_jobs — one row per connector run.
-- ---------------------------------------------------------------------------
CREATE TABLE collection_jobs (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id   BIGINT      NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
    -- 'fetch' is the P1 passive run kind; later phases add more kinds.
    kind        TEXT        NOT NULL DEFAULT 'fetch'
                            CHECK (kind IN ('fetch', 'backfill', 'retract')),
    status      TEXT        NOT NULL DEFAULT 'running'
                            CHECK (status IN ('running', 'success', 'failed', 'aborted')),
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    -- Connector-reported counters (batches, facts, suppressed, deduped,
    -- dead-lettered, ...) as JSON.
    stats       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    error       TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX ix_collection_jobs_source ON collection_jobs (source_id, started_at DESC);

-- ---------------------------------------------------------------------------
-- source_checkpoints — resumable positions.  cursor is connector-defined
-- (an HTTP cursor, a timestamp, a name); '' = from the beginning.
-- ---------------------------------------------------------------------------
CREATE TABLE source_checkpoints (
    source_id  BIGINT      NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
    key        TEXT        NOT NULL DEFAULT 'default',
    cursor     TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_id, key)
);

-- ---------------------------------------------------------------------------
-- ingestion_dead_letter — poison records preserved, never dropped
-- silently (owner directive §4: retry and checkpoint behavior).  attempts
-- counts delivery attempts; re-drive bumps attempts and moves last_seen.
-- ---------------------------------------------------------------------------
CREATE TABLE ingestion_dead_letter (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id  BIGINT      NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
    -- The normalized fact (or raw record) that failed, for re-drive.
    payload    JSONB       NOT NULL,
    reason     TEXT        NOT NULL,
    attempts   INTEGER     NOT NULL DEFAULT 1,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    status     TEXT        NOT NULL DEFAULT 'pending'
                           CHECK (status IN ('pending', 'resolved', 'discarded'))
);

CREATE INDEX ix_ingestion_dead_letter_pending
    ON ingestion_dead_letter (status, last_seen DESC) WHERE status = 'pending';

-- ---------------------------------------------------------------------------
-- scopes — the P2 active-observation scope registry (created per the
-- deferred-tables register; unused by v0.0.2 P1 connectors).
-- ---------------------------------------------------------------------------
CREATE TABLE scopes (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind       TEXT        NOT NULL CHECK (kind IN ('domain', 'domain_suffix', 'cidr', 'ip')),
    value      TEXT        NOT NULL,
    mode       TEXT        NOT NULL DEFAULT 'allow' CHECK (mode IN ('allow', 'deny')),
    note       TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT scopes_kind_value_unique UNIQUE (kind, value)
);
