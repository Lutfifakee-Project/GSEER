-- GSEER personal-mode migration 0001_personal_mode.up.sql — the whole
-- SQLite personal schema in one transactional file.
--
-- Scope: the personal-mode (checkpoint B) schema — a 1:1 port of the
-- PostgreSQL v0.2.0 contract subset the personal engine supports:
--   data_sources, assets, services, observations (+ L1/L2 identity),
--   latest_http/latest_tls/latest_fingerprints helper views,
--   search_index union view, suppressions + suppression_applications
--   (+ the database-level backstop trigger), collection_jobs,
--   source_checkpoints, ingestion_dead_letter.
--
-- Dialect notes (documented in docs/PERSONAL.md):
--   - TIMESTAMP columns are TEXT holding fixed-width UTC microsecond
--     ISO strings ("2006-01-02T15:04:05.000000Z", SQ-02) — the storage
--     layer is the only writer, so lexicographic order equals
--     chronological order and L2 dedup/pagination are exact;
--   - JSON columns are TEXT (json1 functions validate/extract);
--   - assets.ip is the CANONICAL address text (no /32 suffix — the
--     storage layer normalizes before write), so text equality matches;
--   - sources is a JSON array of strings ([]when empty);
--   - the suppression backstop is a TRIGGER pair with the same
--     matching semantics as the Go matcher and the PG trigger; CIDR
--     containment goes through the registered gseer_ip_in_cidr
--     function and the expiry clock through gseer_now_micro();
--   - DISTINCT ON is expressed with ROW_NUMBER() window functions
--     (modernc SQLite bundles a window-function-capable engine).
--
-- schema_migrations is owned by the storage layer's migrate runner
-- (mirrors golang-migrate's contract: version + dirty flag).

CREATE TABLE data_sources (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE,
    connector  TEXT    NOT NULL,
    terms_ref  TEXT    NOT NULL DEFAULT '',
    config     TEXT    NOT NULL DEFAULT '{}',
    enabled    INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL
);

CREATE TABLE assets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT    NOT NULL CHECK (kind IN ('ip', 'domain', 'hostname')),
    value       TEXT    NOT NULL,
    ip          TEXT,
    domain      TEXT,
    hostname    TEXT,
    asn         INTEGER,
    asn_org     TEXT,
    geo_country TEXT,
    geo_region  TEXT,
    geo_city    TEXT,
    sources     TEXT    NOT NULL DEFAULT '[]',
    first_seen  TEXT    NOT NULL,
    last_seen   TEXT    NOT NULL,
    stale       INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT assets_kind_value_unique UNIQUE (kind, value),
    CONSTRAINT assets_kind_fields_check CHECK (
        (kind = 'ip'       AND ip      IS NOT NULL) OR
        (kind = 'domain'   AND domain  IS NOT NULL) OR
        (kind = 'hostname' AND hostname IS NOT NULL)
    )
);

CREATE TABLE services (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id   INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    port       INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    transport  TEXT    NOT NULL CHECK (transport IN ('tcp', 'udp')),
    protocol   TEXT    NOT NULL,
    name       TEXT    NOT NULL DEFAULT '',
    product    TEXT    NOT NULL DEFAULT '',
    version    TEXT    NOT NULL DEFAULT '',
    first_seen TEXT    NOT NULL,
    last_seen  TEXT    NOT NULL,
    stale      INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT services_asset_port_transport_unique UNIQUE (asset_id, port, transport)
);

CREATE TABLE observations (
    id            INTEGER NOT NULL PRIMARY KEY,
    source_id     INTEGER NOT NULL REFERENCES data_sources(id),
    asset_id      INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    service_id    INTEGER REFERENCES services(id) ON DELETE CASCADE,
    observed_at   TEXT    NOT NULL,
    payload_hash  TEXT    NOT NULL,
    source_cursor TEXT    NOT NULL,
    provenance    TEXT    NOT NULL DEFAULT 'fixture',
    evidence      TEXT    NOT NULL DEFAULT '{}',
    created_at    TEXT    NOT NULL
);

-- L1 — delivery identity (SPEC-05 §5): the same source record
-- re-delivered across restarts/retries/replay is processed once.
CREATE UNIQUE INDEX ux_observations_delivery
    ON observations (source_id, source_cursor);

-- L2 — fact identity: distinct records with identical content within a
-- source collapse to one fact.  COALESCE(service_id, 0) matches
-- asset-level facts (NULL service_id) without a separate branch.
CREATE UNIQUE INDEX ux_observations_fact
    ON observations (source_id, asset_id, COALESCE(service_id, 0),
                     observed_at, payload_hash);

CREATE INDEX ix_observations_asset     ON observations (asset_id);
CREATE INDEX ix_observations_service   ON observations (service_id);
CREATE INDEX ix_observations_source    ON observations (source_id, observed_at);
CREATE INDEX ix_services_asset         ON services (asset_id);
CREATE INDEX ix_assets_ip              ON assets (ip);
CREATE INDEX ix_assets_domain_hostname ON assets (domain, hostname);

-- ---------------------------------------------------------------------------
-- latest-per-service helper views (ROW_NUMBER replaces PG's DISTINCT ON;
-- newest observed_at wins, ties break on the higher observation id).
-- ---------------------------------------------------------------------------
CREATE VIEW latest_http AS
SELECT service_id, status, title, server, observed_at FROM (
    SELECT o.service_id AS service_id,
           CAST(json_extract(o.evidence, '$.http.status') AS INTEGER) AS status,
           json_extract(o.evidence, '$.http.title')  AS title,
           json_extract(o.evidence, '$.http.server') AS server,
           o.observed_at AS observed_at,
           ROW_NUMBER() OVER (PARTITION BY o.service_id
                              ORDER BY o.observed_at DESC, o.id DESC) AS rn
    FROM observations o
    WHERE o.service_id IS NOT NULL
      AND json_valid(o.evidence)
      AND json_extract(o.evidence, '$.http') IS NOT NULL
) WHERE rn = 1;

CREATE VIEW latest_tls AS
SELECT service_id, tls_version, cert_subject_cn, cert_issuer_cn, cert_expiry, observed_at FROM (
    SELECT o.service_id AS service_id,
           json_extract(o.evidence, '$.tls.version')           AS tls_version,
           json_extract(o.evidence, '$.tls.cert_subject_cn')   AS cert_subject_cn,
           json_extract(o.evidence, '$.tls.cert_issuer_cn')    AS cert_issuer_cn,
           json_extract(o.evidence, '$.tls.cert_expiry')       AS cert_expiry,
           o.observed_at AS observed_at,
           ROW_NUMBER() OVER (PARTITION BY o.service_id
                              ORDER BY o.observed_at DESC, o.id DESC) AS rn
    FROM observations o
    WHERE o.service_id IS NOT NULL
      AND json_valid(o.evidence)
      AND json_extract(o.evidence, '$.tls') IS NOT NULL
) WHERE rn = 1;

CREATE VIEW latest_fingerprints AS
SELECT service_id, technologies, observed_at FROM (
    SELECT o.service_id AS service_id,
           CASE
             WHEN json_type(o.evidence, '$.fingerprint.technologies') = 'array'
             THEN json_extract(o.evidence, '$.fingerprint.technologies')
             ELSE '[]'
           END AS technologies,
           o.observed_at AS observed_at,
           ROW_NUMBER() OVER (PARTITION BY o.service_id
                              ORDER BY o.observed_at DESC, o.id DESC) AS rn
    FROM observations o
    WHERE o.service_id IS NOT NULL
      AND json_valid(o.evidence)
      AND json_extract(o.evidence, '$.fingerprint') IS NOT NULL
) WHERE rn = 1;

-- ---------------------------------------------------------------------------
-- search_index — the service-granular union view (0003 contract, 1:1).
-- ---------------------------------------------------------------------------
CREATE VIEW search_index AS
SELECT s.id AS row_id, 'service' AS row_kind,
       a.id AS asset_id, a.kind AS asset_kind, a.value AS asset_value,
       a.ip, a.domain, a.hostname, a.asn, a.asn_org,
       a.geo_country, a.geo_region, a.geo_city,
       s.port, s.transport, s.protocol, s.name, s.product, s.version,
       h.status AS http_status, h.title AS http_title, h.server AS http_server,
       tls.tls_version, tls.cert_subject_cn, tls.cert_issuer_cn, tls.cert_expiry,
       fp.technologies,
       a.sources, a.first_seen, s.last_seen, s.stale
FROM services s
JOIN assets a ON a.id = s.asset_id
LEFT JOIN latest_http h ON h.service_id = s.id
LEFT JOIN latest_tls tls ON tls.service_id = s.id
LEFT JOIN latest_fingerprints fp ON fp.service_id = s.id
UNION ALL
SELECT a.id, 'asset-only', a.id, a.kind, a.value,
       a.ip, a.domain, a.hostname, a.asn, a.asn_org,
       a.geo_country, a.geo_region, a.geo_city,
       NULL, NULL, NULL, NULL, NULL, NULL,
       NULL, NULL, NULL,
       NULL, NULL, NULL, NULL, NULL,
       a.sources, a.first_seen, a.last_seen, 0
FROM assets a
WHERE NOT EXISTS (SELECT 1 FROM services s WHERE s.asset_id = a.id);

-- ---------------------------------------------------------------------------
-- suppressions + suppression_applications (0005 contract).
-- ---------------------------------------------------------------------------
CREATE TABLE suppressions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT    NOT NULL CHECK (kind IN
                       ('ip', 'cidr', 'domain', 'domain_suffix', 'source')),
    value      TEXT    NOT NULL,
    reason     TEXT    NOT NULL DEFAULT '',
    created_by TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    expires_at TEXT,
    enabled    INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT suppressions_kind_value_unique UNIQUE (kind, value)
);

CREATE INDEX ix_suppressions_enabled ON suppressions (kind, value, enabled);

CREATE TABLE suppression_applications (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    action         TEXT    NOT NULL CHECK (action IN
                           ('ingest-blocked', 'retroactive-retract', 'retract-source')),
    suppression_id INTEGER,
    asset_id       INTEGER,
    detail         TEXT    NOT NULL DEFAULT '{}',
    applied_at     TEXT    NOT NULL,
    CONSTRAINT suppression_applications_rule_fk
        FOREIGN KEY (suppression_id) REFERENCES suppressions(id) ON DELETE SET NULL
);

CREATE INDEX ix_suppression_applications_rule ON suppression_applications (suppression_id, applied_at DESC);

-- The database-level backstop (the personal mode's GSUP1 layer): ANY
-- assets write that matches an enabled, unexpired rule is refused —
-- the Go gate must never be the only enforcement layer.  Matching
-- semantics mirror the Go matcher and the PG trigger byte-for-byte:
-- canonical values in, dot-boundary suffix test, netip containment
-- (via the registered gseer_ip_in_cidr function).
CREATE TRIGGER trg_assets_suppression_insert
BEFORE INSERT ON assets
WHEN EXISTS (
    SELECT 1 FROM suppressions s
    WHERE s.enabled = 1
      AND (s.expires_at IS NULL OR s.expires_at > gseer_now_micro())
      AND (
        (NEW.kind = 'ip' AND NEW.ip IS NOT NULL AND s.kind = 'ip'
         AND NEW.ip = s.value)
        OR
        (NEW.kind = 'ip' AND NEW.ip IS NOT NULL AND s.kind = 'cidr'
         AND gseer_ip_in_cidr(NEW.ip, s.value) = 1)
        OR
        (NEW.kind = 'domain' AND NEW.domain IS NOT NULL AND s.kind = 'domain'
         AND NEW.domain = s.value)
        OR
        (NEW.kind = 'domain' AND NEW.domain IS NOT NULL AND s.kind = 'domain_suffix'
         AND (NEW.domain = s.value
              OR substr(NEW.domain, -(length(s.value) + 1)) = '.' || s.value))
        OR
        (NEW.kind = 'hostname' AND NEW.hostname IS NOT NULL AND s.kind = 'domain'
         AND NEW.hostname = s.value)
        OR
        (NEW.kind = 'hostname' AND NEW.hostname IS NOT NULL AND s.kind = 'domain_suffix'
         AND (NEW.hostname = s.value
              OR substr(NEW.hostname, -(length(s.value) + 1)) = '.' || s.value))
      )
)
BEGIN
    SELECT RAISE(ABORT, 'GSUP1: asset rejected: matches an enabled suppression rule');
END;

CREATE TRIGGER trg_assets_suppression_update
BEFORE UPDATE OF kind, value, ip, domain, hostname ON assets
WHEN (OLD.kind <> NEW.kind OR OLD.value <> NEW.value
      OR OLD.ip IS NOT NEW.ip OR OLD.domain IS NOT NEW.domain
      OR OLD.hostname IS NOT NEW.hostname)
 AND EXISTS (
    SELECT 1 FROM suppressions s
    WHERE s.enabled = 1
      AND (s.expires_at IS NULL OR s.expires_at > gseer_now_micro())
      AND (
        (NEW.kind = 'ip' AND NEW.ip IS NOT NULL AND s.kind = 'ip'
         AND NEW.ip = s.value)
        OR
        (NEW.kind = 'ip' AND NEW.ip IS NOT NULL AND s.kind = 'cidr'
         AND gseer_ip_in_cidr(NEW.ip, s.value) = 1)
        OR
        (NEW.kind = 'domain' AND NEW.domain IS NOT NULL AND s.kind = 'domain'
         AND NEW.domain = s.value)
        OR
        (NEW.kind = 'domain' AND NEW.domain IS NOT NULL AND s.kind = 'domain_suffix'
         AND (NEW.domain = s.value
              OR substr(NEW.domain, -(length(s.value) + 1)) = '.' || s.value))
        OR
        (NEW.kind = 'hostname' AND NEW.hostname IS NOT NULL AND s.kind = 'domain'
         AND NEW.hostname = s.value)
        OR
        (NEW.kind = 'hostname' AND NEW.hostname IS NOT NULL AND s.kind = 'domain_suffix'
         AND (NEW.hostname = s.value
              OR substr(NEW.hostname, -(length(s.value) + 1)) = '.' || s.value))
      )
)
BEGIN
    SELECT RAISE(ABORT, 'GSUP1: asset rejected: matches an enabled suppression rule');
END;

-- ---------------------------------------------------------------------------
-- Collection bookkeeping (0006 contract subset the runner interface uses).
-- ---------------------------------------------------------------------------
CREATE TABLE collection_jobs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id    INTEGER NOT NULL REFERENCES data_sources(id),
    kind         TEXT    NOT NULL,
    status       TEXT    NOT NULL,
    stats        TEXT    NOT NULL DEFAULT '{}',
    error        TEXT    NOT NULL DEFAULT '',
    started_at   TEXT    NOT NULL,
    finished_at  TEXT
);

CREATE TABLE source_checkpoints (
    source_id INTEGER NOT NULL REFERENCES data_sources(id),
    key       TEXT    NOT NULL,
    cursor    TEXT    NOT NULL DEFAULT '',
    updated_at TEXT   NOT NULL,
    PRIMARY KEY (source_id, key)
);

CREATE TABLE ingestion_dead_letter (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id   INTEGER NOT NULL REFERENCES data_sources(id),
    payload     TEXT    NOT NULL DEFAULT '{}',
    reason      TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL DEFAULT 'pending',
    failure     TEXT    NOT NULL DEFAULT '',
    resolve_note TEXT   NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);
