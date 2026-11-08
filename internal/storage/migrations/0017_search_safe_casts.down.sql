-- GSEER migration 0017_search_safe_casts.down.sql — ordered rollback.
--
-- Restores the 0002 view definitions VERBATIM (the exact statements the
-- 0002 up file produced, so pg_get_viewdef round-trips byte-for-byte)
-- and drops the two 0017 safe-cast helpers.  After a successful down the
-- ledger reads (16, false) and the schema matches the post-0016 state.
--
-- NOTE: rolling back re-EXPOSES the pre-0017 behavior — a malformed
-- evidence value can again raise inside latest_http/latest_tls.  That is
-- what "down" means here; the up file's header documents the defect.

-- ---------------------------------------------------------------------------
-- [1] Restore the 0002 view definitions verbatim (0002_helper_views.up.sql
--     lines 19-42, unchanged).
-- ---------------------------------------------------------------------------
CREATE OR REPLACE VIEW latest_http AS
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

CREATE OR REPLACE VIEW latest_tls AS
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

-- ---------------------------------------------------------------------------
-- [2] Drop the 0017 helpers (no dependencies remain after [1]).
-- ---------------------------------------------------------------------------
DROP FUNCTION IF EXISTS public.gseer_safe_int(text);
DROP FUNCTION IF EXISTS public.gseer_safe_timestamptz(text);

DO $gseer_0017_down_verify$
BEGIN
    IF to_regprocedure('public.gseer_safe_int(text)') IS NOT NULL
       OR to_regprocedure('public.gseer_safe_timestamptz(text)') IS NOT NULL THEN
        RAISE EXCEPTION 'migration 0017 down: safe-cast helpers still present'
            USING ERRCODE = 'GSUPF';
    END IF;
    RAISE NOTICE '0017 down: 0002 view definitions restored; safe-cast helpers dropped';
END
$gseer_0017_down_verify$;
