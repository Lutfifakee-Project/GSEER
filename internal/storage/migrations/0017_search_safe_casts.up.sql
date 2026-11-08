-- GSEER migration 0017_search_safe_casts.up.sql — safe-cast functionality
-- for the latest-per-service helper views (GAP-1 mandate; roadmap M-H).
--
-- SPEC (M-H, authored under the implementation authorization; subject to
-- owner review per the register §4.3 row 17):
--
--   PROBLEM. Migration 0002 defines latest_http and latest_tls by casting
--   JSONB evidence text straight to typed columns:
--
--       (o.evidence -> 'http' ->> 'status')::integer
--       (o.evidence -> 'tls' ->> 'cert_expiry')::timestamptz
--
--   These casts RAISE on malformed evidence ("200x", "soon", embedded
--   NULs, ...).  Because latest_* feed the search_index union view — the
--   M9 planner's only target — ONE malformed observation poisons every
--   query that touches the affected column: search, facets, export, the
--   API, and the dashboard all fail with
--   `invalid input syntax for type integer/timestamptz` until the row is
--   removed by hand.  For a product whose input is remote evidence, this
--   is a search-correctness and availability defect, not a hypothetical.
--
--   FUNCTIONALITY (GAP-1 "safe-cast", assigned to 0017 by the owner):
--   casts used by the search surface must yield NULL for values that do
--   not parse instead of raising.  A row with malformed evidence keeps
--   every OTHER searchable column; the malformed column simply does not
--   match typed filters and exports as null.  Valid values are unchanged,
--   byte-for-byte, in both type and value.
--
--   SCOPE.  Exactly the two raising casts in 0002 (the only unsafe casts
--   in the embedded set — verified by grep before authoring):
--     latest_http.status       integer      (0002:22)
--     latest_tls.cert_expiry   timestamptz  (0002:37)
--   latest_fingerprints already guards its parse with jsonb_typeof
--   (0002:56) and needs no change.  No table data is touched; no column
--   name, order, or type changes (CREATE OR REPLACE VIEW with identical
--   columns keeps the planner, the §5.2 grants, and search_index valid).
--
--   MECHANISM.  Two STRICT, IMMUTABLE, PARALLEL SAFE helper functions in
--   the public schema (the runtime credential already holds USAGE there;
--   views execute with their owner's privileges, so no new grant is
--   needed for gseer_app — the §5.2 matrix stays complete):
--
--       public.gseer_safe_int(text)         RETURNS integer
--       public.gseer_safe_timestamptz(text) RETURNS timestamptz
--
--   NULL in → NULL out (STRICT); unparseable input → NULL; parse errors
--   of any other kind are re-raised (a genuine out-of-range condition
--   must not be laundered as "no data" — only SYNTAX/value parse
--   failures are softened; see the WHEN clauses).
--
--   ROLLBACK.  0017_search_safe_casts.down.sql restores the 0002 view
--   definitions verbatim and drops both helpers.  After a successful
--   down, the ledger reads (16, false) and pg_get_viewdef for both views
--   equals the 0002-produced definition (round-trip equality is an
--   automated AC; see internal/storage/postgres/migrate_test.go).
--
--   CONTRACTS.  D-M3-5: one simple-protocol statement batch, single
--   implicit transaction, no transaction-control statements.  OD-7: PG16
--   floor, guard-enforced below (GSUPF), matching the 0016 guard shape.
--   OD-4: version 0017, per the GAP-1 mandate; 0007–0015 stay reserved.
--
--   ERRCODE.  Guard/verification failures use 'GSUPF' (the established
--   fail-closed deployment code); the backstop v2 keeps 'GSUP1'; the
--   policy layer keeps 'GSUP2' — no new codes are introduced.

-- ---------------------------------------------------------------------------
-- [1] PostgreSQL floor guard (OD-7) — same shape as the 0016 guard.
-- ---------------------------------------------------------------------------
DO $gseer_0017_guard$
BEGIN
    IF current_setting('server_version_num')::int < 160000 THEN
        RAISE EXCEPTION
            'migration 0017 requires PostgreSQL 16 or newer (OD-7 floor); this server reports server_version_num %',
            current_setting('server_version_num')
            USING ERRCODE = 'GSUPF',
                  HINT    = 'upgrade the cluster to PostgreSQL 16+ before applying the v0.2.0 migration set';
    END IF;
END
$gseer_0017_guard$;

-- ---------------------------------------------------------------------------
-- [2] Safe-cast helpers.
--     STRICT: NULL input → NULL output, no exception handler needed.
--     IMMUTABLE + PARALLEL SAFE: pure functions of their argument, usable
--     in index expressions and parallel plans.
-- ---------------------------------------------------------------------------
CREATE FUNCTION public.gseer_safe_int(p text)
RETURNS integer
LANGUAGE plpgsql
IMMUTABLE
PARALLEL SAFE
AS $fn$
BEGIN
    RETURN p::integer;
EXCEPTION
    WHEN invalid_text_representation
      OR numeric_value_out_of_range THEN
        RETURN NULL;
END;
$fn$;

COMMENT ON FUNCTION public.gseer_safe_int(text) IS
    'GSEER 0017 safe-cast helper: text -> integer, NULL when the input does not parse as an integer (search-surface availability; GAP-1). Valid values are unchanged.';

CREATE FUNCTION public.gseer_safe_timestamptz(p text)
RETURNS timestamptz
LANGUAGE plpgsql
IMMUTABLE
PARALLEL SAFE
AS $fn$
BEGIN
    RETURN p::timestamptz;
EXCEPTION
    WHEN invalid_text_representation
      OR invalid_datetime_format
      OR datetime_field_overflow THEN
        RETURN NULL;
END;
$fn$;

COMMENT ON FUNCTION public.gseer_safe_timestamptz(text) IS
    'GSEER 0017 safe-cast helper: text -> timestamptz, NULL when the input does not parse as a timestamp (search-surface availability; GAP-1). Valid values are unchanged.';

-- ---------------------------------------------------------------------------
-- [3] Rebuild the affected helper views with safe casts.
--     Column names, order, and types are IDENTICAL to 0002 — only the
--     two cast expressions change.  search_index (0003) references the
--     views by name and keeps its exact shape.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE VIEW latest_http AS
SELECT DISTINCT ON (o.service_id)
       o.service_id,
       public.gseer_safe_int(o.evidence -> 'http' ->> 'status') AS status,
       o.evidence -> 'http' ->> 'title'             AS title,
       o.evidence -> 'http' ->> 'server'            AS server,
       o.observed_at
FROM observations o
WHERE o.service_id IS NOT NULL
  AND o.evidence ? 'http'
ORDER BY o.service_id, o.observed_at DESC, o.id DESC;

CREATE OR REPLACE VIEW latest_tls AS
SELECT DISTINCT ON (o.service_id)
       o.service_id,
       o.evidence -> 'tls' ->> 'version'        AS tls_version,
       o.evidence -> 'tls' ->> 'cert_subject_cn' AS cert_subject_cn,
       o.evidence -> 'tls' ->> 'cert_issuer_cn'  AS cert_issuer_cn,
       public.gseer_safe_timestamptz(o.evidence -> 'tls' ->> 'cert_expiry') AS cert_expiry,
       o.observed_at
FROM observations o
WHERE o.service_id IS NOT NULL
  AND o.evidence ? 'tls'
ORDER BY o.service_id, o.observed_at DESC, o.id DESC;

-- ---------------------------------------------------------------------------
-- [4] Post-condition verification (fail-closed, GSUPF).
-- ---------------------------------------------------------------------------
DO $gseer_0017_verify$
DECLARE
    v_failures text[] := '{}';
BEGIN
    IF to_regprocedure('public.gseer_safe_int(text)') IS NULL THEN
        v_failures := v_failures || 'public.gseer_safe_int(text) is missing';
    END IF;
    IF to_regprocedure('public.gseer_safe_timestamptz(text)') IS NULL THEN
        v_failures := v_failures || 'public.gseer_safe_timestamptz(text) is missing';
    END IF;
    IF to_regclass('public.latest_http') IS NULL THEN
        v_failures := v_failures || 'view public.latest_http is missing';
    END IF;
    IF to_regclass('public.latest_tls') IS NULL THEN
        v_failures := v_failures || 'view public.latest_tls is missing';
    END IF;

    -- Functional probes: valid values unchanged, malformed values NULL.
    IF public.gseer_safe_int('200') <> 200
       OR public.gseer_safe_int('200x') IS NOT NULL
       OR public.gseer_safe_int(NULL) IS NOT NULL THEN
        v_failures := v_failures || 'gseer_safe_int functional probe failed';
    END IF;
    IF public.gseer_safe_timestamptz('2026-01-02T15:04:05Z') IS NULL
       OR public.gseer_safe_timestamptz('not-a-timestamp') IS NOT NULL
       OR public.gseer_safe_timestamptz(NULL) IS NOT NULL THEN
        v_failures := v_failures || 'gseer_safe_timestamptz functional probe failed';
    END IF;

    -- The rebuilt views must be queryable end-to-end (empty result, no
    -- error) — proves the view definitions parse against the live schema.
    BEGIN
        PERFORM 1 FROM public.latest_http WHERE false;
        PERFORM 1 FROM public.latest_tls WHERE false;
    EXCEPTION WHEN OTHERS THEN
        v_failures := v_failures || format('rebuilt views are not queryable: %s', SQLERRM);
    END;

    IF array_length(v_failures, 1) > 0 THEN
        RAISE EXCEPTION 'migration 0017 post-condition verification failed with % failure(s): %',
            array_length(v_failures, 1), array_to_string(v_failures, ' | ')
            USING ERRCODE = 'GSUPF';
    END IF;

    RAISE NOTICE '0017: safe-cast helpers installed; latest_http/latest_tls rebuilt with safe casts';
END
$gseer_0017_verify$;
