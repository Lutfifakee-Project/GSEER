-- GSEER migration 0001_core.down.sql — reverse of 0001_core.
-- Drop order is FK-safe: observations → services → assets → data_sources.
-- Indexes drop with their tables; the explicit index drops are no-ops
-- kept for symmetry with the up file's named indexes.

DROP INDEX IF EXISTS ix_assets_domain_hostname;
DROP INDEX IF EXISTS ix_assets_ip;
DROP INDEX IF EXISTS ix_services_asset;
DROP INDEX IF EXISTS ix_observations_source;
DROP INDEX IF EXISTS ix_observations_service;
DROP INDEX IF EXISTS ix_observations_asset;
DROP INDEX IF EXISTS ux_observations_fact;
DROP INDEX IF EXISTS ux_observations_delivery;

DROP TABLE IF EXISTS observations;
DROP TABLE IF EXISTS services;
DROP TABLE IF EXISTS assets;
DROP TABLE IF EXISTS data_sources;
