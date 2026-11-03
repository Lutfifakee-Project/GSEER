-- GSEER migration 0006_collection.down.sql — remove the collector-side
-- bookkeeping tables (M14).  Dropping loses job history, checkpoints and
-- dead-letter payloads: only meaningful for a full teardown.

DROP TABLE IF EXISTS scopes;
DROP TABLE IF EXISTS ingestion_dead_letter;
DROP TABLE IF EXISTS source_checkpoints;
DROP TABLE IF EXISTS collection_jobs;
