-- GSEER migration 0002_helper_views.up.sql — latest-per-service helper views.
--
-- SPEC-05 §2 row 4: "latest_http / latest_tls / latest_fingerprints —
-- latest-per-service helper views (maintained on write); one row per
-- service."  SPEC-05 §3 has the search_index union view LEFT JOIN these
-- (AC-4.6), so they are required in v0.0.1 (owner-approved M3 scope).
--
-- Definition basis (owner decision D-M3-2): the views are defined over
-- `observations` — the canonical provenance surface — extracting the
-- typed observation payload from the `evidence` JSONB.  This keeps the
-- provenance chain (source_id, observed_at) intact and gives the same
-- semantics for v0.0.1 fixture loads (M4) and v0.0.2 collector writes:
-- whoever writes an observation row gets the views refreshed for free.
--
-- One row per service: DISTINCT ON (service_id) with the newest
-- observed_at wins.  Services without an observation of the given kind
-- simply have no row (LEFT JOIN in search_index handles that).

CREATE VIEW latest_http AS
SELECT DISTINCT ON (o.service_id)
       o.service_id,
       (o.evidence -> 'http' ->> 'status')::integer AS status,
       o.evidence -> 'http' ->> 'title'             AS title,
       o.evidence -> 'http' ->> 'server'            AS server,
       o.observed_at
FROM observations o
WHERE o.service_id IS NOT NULL
  AND o.evidence ? 'http'
ORDER BY o.service_id, o.observed_at DESC, o.id DESC;

CREATE VIEW latest_tls AS
SELECT DISTINCT ON (o.service_id)
       o.service_id,
       o.evidence -> 'tls' ->> 'version'        AS tls_version,
       o.evidence -> 'tls' ->> 'cert_subject_cn' AS cert_subject_cn,
       o.evidence -> 'tls' ->> 'cert_issuer_cn'  AS cert_issuer_cn,
       (o.evidence -> 'tls' ->> 'cert_expiry')::timestamptz AS cert_expiry,
       o.observed_at
FROM observations o
WHERE o.service_id IS NOT NULL
  AND o.evidence ? 'tls'
ORDER BY o.service_id, o.observed_at DESC, o.id DESC;

CREATE VIEW latest_fingerprints AS
SELECT DISTINCT ON (o.service_id)
       o.service_id,
       -- technologies is a JSON array of strings in the evidence payload;
       -- normalize to a Postgres text[] for search_index GIN indexing.
       ARRAY(
           SELECT jsonb_array_elements_text(o.evidence -> 'fingerprint' -> 'technologies')
       ) AS technologies,
       o.observed_at
FROM observations o
WHERE o.service_id IS NOT NULL
  AND o.evidence ? 'fingerprint'
  AND jsonb_typeof(o.evidence -> 'fingerprint' -> 'technologies') = 'array'
ORDER BY o.service_id, o.observed_at DESC, o.id DESC;
