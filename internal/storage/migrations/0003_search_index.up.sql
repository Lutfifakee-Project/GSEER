-- GSEER migration 0003_search_index.up.sql — the service-granular union
-- view (SPEC-05 §3; the M9 planner's target; AC-4.6/4.7/4.8).
--
-- Design decision (conservative, reversible — continuous directive §2):
-- v0.0.1 ships search_index as a plain VIEW over assets/services and
-- the M3 latest_* helper views.  SPEC-05 §3 notes a maintained backing
-- table as a v0.0.1 performance option; with the fixture-scale corpus
-- the plain view is correct and index-sufficient, and swapping to a
-- maintained table later is a contained migration (the view definition
-- below IS the normative semantic contract either way).

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
       a.sources, a.first_seen, a.last_seen, false
FROM assets a
WHERE NOT EXISTS (SELECT 1 FROM services s WHERE s.asset_id = a.id);
