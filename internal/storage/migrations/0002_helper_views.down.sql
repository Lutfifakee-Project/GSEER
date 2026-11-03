-- GSEER migration 0002_helper_views.down.sql — reverse of 0002.

DROP VIEW IF EXISTS latest_fingerprints;
DROP VIEW IF EXISTS latest_tls;
DROP VIEW IF EXISTS latest_http;
