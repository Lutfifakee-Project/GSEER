-- GSEER v0.2.0 runtime grant script for gseer_app (Rev 6.7.2 §4/§5).
--
-- Reconciles the Rev 6.7.1 §5.2/§5.3/§5.5 matrix with the actual grant
-- statements.  Every grant below is backed by an evidenced runtime SQL
-- path in the v0.1.0 codebase (baseline d84b291583bf2f0ec8d3499faa62031239e2681b);
-- privileges WITHOUT evidence are deliberately absent and named in the
-- matrix.  Applied by the deployment procedure BEFORE `gseer server start`
-- and AFTER migration 0016 (which owns the public.suppressions and
-- gseer_policy ACLs — those are not repeated here, keeping single-owner
-- ACL provenance).  Idempotent.
--
-- Contracts implemented:
--   * §4 CHOICE A: CONNECT **and** TEMPORARY are EXPLICIT gseer_app grants.
--     Evidence for TEMPORARY: the suppression retraction path executes
--     `CREATE TEMP TABLE tmp_retract_assets ON COMMIT DROP AS SELECT ...`
--     (internal/storage/postgres/suppression.go:367).  An explicit grant
--     survives any cluster hardening that revokes PUBLIC's database
--     privileges; least privilege is preserved because the privilege IS
--     required at runtime.
--   * Sequences: NONE.  INSERT through an IDENTITY column uses
--     nextval_internal(), which performs no sequence ACL check (verified
--     on PostgreSQL 17.2).  No application SQL calls nextval/currval.
--   * public.suppressions: granted by migration 0016 (SELECT only).
--   * public.schema_migrations: NOT granted (driver-owned ledger;
--     diagnostics run under the operator credential).
--   * public.scopes: NOT granted (no v0.1.0 runtime path; P2 registry).
--
-- PostgreSQL floor: 16.  No transaction-control statements.

-- Database-level (§4 Choice A: explicit CONNECT + explicit TEMPORARY).
GRANT CONNECT,   TEMPORARY ON DATABASE gseer TO gseer_app;

-- Schema-level.
GRANT USAGE ON SCHEMA public TO gseer_app;

-- Core search/ingest tables.
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.assets      TO gseer_app;  -- upserts; retraction/purge deletes
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.services    TO gseer_app;  -- upserts; orphan/retention deletes
GRANT SELECT, INSERT, DELETE         ON TABLE public.observations TO gseer_app; -- insert-dedup; purge/retract deletes (no UPDATE path)
GRANT SELECT, INSERT, UPDATE         ON TABLE public.data_sources TO gseer_app; -- registry upsert + enable/disable (no DELETE path)

-- Suppression audit surface (append-only; no UPDATE/DELETE path exists).
GRANT SELECT, INSERT                 ON TABLE public.suppression_applications TO gseer_app;

-- Collector bookkeeping (M14; collection_jobs count read: cli/collect.go).
GRANT SELECT, INSERT, UPDATE         ON TABLE public.collection_jobs       TO gseer_app;
GRANT SELECT, INSERT, UPDATE         ON TABLE public.source_checkpoints    TO gseer_app;
GRANT SELECT, INSERT, UPDATE         ON TABLE public.ingestion_dead_letter TO gseer_app;

-- Search surfaces (M9 planner + dashboard; plain views execute with the
-- view owner's privileges, but the app also reads the base tables above).
GRANT SELECT ON TABLE public.latest_http        TO gseer_app;
GRANT SELECT ON TABLE public.latest_tls         TO gseer_app;
GRANT SELECT ON TABLE public.latest_fingerprints TO gseer_app;
GRANT SELECT ON TABLE public.search_index       TO gseer_app;

-- Sequences: none (see header).  SCOPES: none (see header).
-- schema_migrations: none (see header).
