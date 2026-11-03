-- GSEER migration 0004_search_indexes.down.sql — drop the evidence-driven
-- supporting indexes added by 0004_search_indexes.up.sql (reverse order).

DROP INDEX IF EXISTS ix_assets_value;
DROP INDEX IF EXISTS ix_observations_fp_scan;
DROP INDEX IF EXISTS ix_observations_tls_scan;
DROP INDEX IF EXISTS ix_observations_http_scan;
