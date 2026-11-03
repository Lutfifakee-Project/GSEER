-- GSEER migration 0005_suppressions.down.sql — remove the suppression
-- policy layer (M13).  Reverting deletes the audit surface with it: this
-- is acceptable because a v0.0.2 deployment that removes suppressions has
-- no ingestion to audit (the Go gate refuses to run without the table —
-- LoadSuppressions errors and every ingestion path fails closed).

DROP TRIGGER IF EXISTS trg_assets_suppression ON assets;
DROP FUNCTION IF EXISTS fn_check_asset_suppression();
DROP TABLE IF EXISTS suppression_applications;
DROP TABLE IF EXISTS suppressions;
