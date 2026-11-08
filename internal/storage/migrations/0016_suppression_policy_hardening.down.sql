-- GSEER migration 0016_suppression_policy_hardening.down.sql — full,
-- dependency-safe teardown of the gseer_policy layer (Rev 6.7.2 §3).
--
-- Restores the exact v0.1.0 state of every object this migration's up file
-- touched: the four statement triggers and four functions are dropped in
-- the mandated order, the schema is dropped only after it is provably
-- empty, public.fn_check_asset_suppression is restored with the EXACT
-- v0.1.0 body (0005_suppressions.up.sql lines 130–200 at baseline
-- d84b291583bf2f0ec8d3499faa62031239e2681b), and the post-down ACL state is
-- normalized.  NO CASCADE is used anywhere: dependency violations must
-- fail loudly, never cascade silently.
--
-- Block order (Rev 6.7.2 §3, dependency-safe):
--   [1] Down refusal guard (pre-flight)
--   [2] DROP TRIGGER ×4 on public.suppressions      (§3 item 1)
--   [3] Default-privilege reset                     (§3 item 8, part 1)
--   [4] DROP FUNCTION suppression_insert            (§3 item 2)
--       DROP FUNCTION suppression_update            (§3 item 3)
--       DROP FUNCTION suppression_delete            (§3 item 4)
--       DROP FUNCTION fn_suppressions_policy_lock   (§3 item 5)
--       DROP SCHEMA gseer_policy (now empty)        (§3 item 6)
--   [5] Restore public.fn_check_asset_suppression   (§3 item 7, exact v0.1.0 body)
--   [6] Restore the intended post-down ACL state    (§3 item 8, part 2)
--   [7] Post-condition verification + backstop functional probe
--
-- Why triggers drop before functions: DROP FUNCTION on a function used by
-- an existing trigger fails with a dependency error; the order above makes
-- every drop legal without CASCADE (AC-112).
--
-- Post-down role state (Rev 6.7.2 §7 / OD-1 default A): gseer_policy_owner
-- keeps its NOLOGIN attribute, its table/sequence privileges on
-- public.suppressions, and its D-47 membership — it is OBJECT-EMPTY but
-- NOT PRIVILEGE-EMPTY.  Revoking those grants and/or the membership are
-- owner-decision variants (OD-1 B/C) and are deliberately NOT implemented
-- in this file.  Cluster-global roles are never dropped by a migration.
--
-- Transaction contract: one implicit transaction for the whole file (the
-- migrate driver); NO transaction-control statements.  SET ROLE is a
-- permitted session statement and is REQUIRED here: the functions and the
-- schema are owned by gseer_policy_owner (NOLOGIN), and only the owner —
-- or a role assuming it via D-47 SET membership — may drop them.
--
-- Ledger note: after this down, the golang-migrate driver bookkeeping is
-- `(version, dirty) = (6, false)` (the v0.1.0 head) — written by the
-- driver itself; application SQL never touches schema_migrations.

-- ---------------------------------------------------------------------------
-- [1] Down refusal guard.
-- ---------------------------------------------------------------------------

DO $gseer_0016d_guard$
DECLARE
    v_failures text[] := '{}';
    v_trig     text;
BEGIN
    IF current_setting('server_version_num')::int < 160000 THEN
        v_failures := v_failures || 'PostgreSQL 16+ required (pg_has_role SET mode)'::text;
    END IF;

    IF to_regnamespace('gseer_policy') IS NULL THEN
        v_failures := v_failures ||
            'schema gseer_policy does not exist; the up migration was not applied (nothing to down)'::text;
    END IF;

    -- Function inventory (all four must exist; a missing one means the
    -- on-disk state diverges from the up migration — fail loudly).
    IF to_regprocedure('gseer_policy.suppression_insert(text,text,text,text,timestamptz,boolean)') IS NULL THEN
        v_failures := v_failures || 'gseer_policy.suppression_insert(text,text,text,text,timestamptz,boolean) is missing'::text;
    END IF;
    IF to_regprocedure('gseer_policy.suppression_update(bigint,text,timestamptz,boolean)') IS NULL THEN
        v_failures := v_failures || 'gseer_policy.suppression_update(bigint,text,timestamptz,boolean) is missing'::text;
    END IF;
    IF to_regprocedure('gseer_policy.suppression_delete(bigint)') IS NULL THEN
        v_failures := v_failures || 'gseer_policy.suppression_delete(bigint) is missing'::text;
    END IF;
    IF to_regprocedure('gseer_policy.fn_suppressions_policy_lock()') IS NULL THEN
        v_failures := v_failures || 'gseer_policy.fn_suppressions_policy_lock() is missing'::text;
    END IF;

    -- Trigger inventory on public.suppressions.
    FOREACH v_trig IN ARRAY ARRAY[
        'trg_suppressions_policy_insert',
        'trg_suppressions_policy_update',
        'trg_suppressions_policy_delete',
        'trg_suppressions_policy_truncate'
    ] LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_trigger
            WHERE tgrelid = 'public.suppressions'::regclass
              AND tgname = v_trig
              AND NOT tgisinternal
        ) THEN
            v_failures := v_failures || format('trigger %s is missing on public.suppressions (state diverges from the up migration)', v_trig);
        END IF;
    END LOOP;

    IF array_length(v_failures, 1) > 0 THEN
        RAISE EXCEPTION 'migration 0016 down refusal guard failed: %',
            array_to_string(v_failures, ' | ')
            USING ERRCODE = 'GSUPF';
    END IF;
END
$gseer_0016d_guard$;

-- ---------------------------------------------------------------------------
-- [2] DROP TRIGGER for all four statement triggers on public.suppressions
--     (§3 item 1).  Executed as the executing role = table owner.
-- ---------------------------------------------------------------------------

DROP TRIGGER trg_suppressions_policy_insert  ON public.suppressions;
DROP TRIGGER trg_suppressions_policy_update  ON public.suppressions;
DROP TRIGGER trg_suppressions_policy_delete  ON public.suppressions;
DROP TRIGGER trg_suppressions_policy_truncate ON public.suppressions;

-- ---------------------------------------------------------------------------
-- [3] Default-privilege reset (§3 item 8, part 1): the up migration's
--     hardening entry is GLOBAL (FOR ROLE gseer_policy_owner, no schema
--     scope — see the up file, block [6], for the verified PostgreSQL 17.2
--     semantics).  Re-granting to PUBLIC merges the PUBLIC item back into
--     the entry, restoring the built-in default template for future
--     functions of that role.  Convergence is verified empirically by
--     AC-113 (up/down/up state equality).
-- ---------------------------------------------------------------------------

ALTER DEFAULT PRIVILEGES FOR ROLE gseer_policy_owner
    GRANT ALL ON FUNCTIONS TO PUBLIC;

-- ---------------------------------------------------------------------------
-- [4] Function drops in the §3 order, then the schema drop.  The objects
--     are owned by gseer_policy_owner (NOLOGIN): assume it via the D-47 SET
--     membership (guard-verified in the up migration and re-verified below
--     for the down path), drop, and return to the migration role.
-- ---------------------------------------------------------------------------

DO $gseer_0016d_rolecheck$
BEGIN
    IF NOT pg_has_role(current_user, 'gseer_policy_owner', 'SET') THEN
        RAISE EXCEPTION 'role % cannot SET ROLE gseer_policy_owner (D-47 SET membership required to drop the policy objects)', current_user
            USING ERRCODE = 'GSUPF';
    END IF;
END
$gseer_0016d_rolecheck$;

SET ROLE gseer_policy_owner;

DROP FUNCTION gseer_policy.suppression_insert(text, text, text, text, timestamptz, boolean);  -- §3 item 2
DROP FUNCTION gseer_policy.suppression_update(bigint, text, timestamptz, boolean);            -- §3 item 3
DROP FUNCTION gseer_policy.suppression_delete(bigint);                                        -- §3 item 4
DROP FUNCTION gseer_policy.fn_suppressions_policy_lock();                                     -- §3 item 5

-- §3 item 6: drop the schema now that it is empty.  Plain DROP SCHEMA
-- (RESTRICT is the default): any overlooked object makes this fail loudly
-- instead of cascading.  CASCADE is never used in this migration.
DROP SCHEMA gseer_policy;

RESET ROLE;

-- ---------------------------------------------------------------------------
-- [5] Restore public.fn_check_asset_suppression using the exact v0.1.0 body
--     (§3 item 7).  Source: 0005_suppressions.up.sql lines 130–200 at
--     baseline d84b291583bf2f0ec8d3499faa62031239e2681b.  The function BODY,
--     volatility (default VOLATILE) and absence of SECURITY/SET clauses are
--     byte-identical to v0.1.0; the statement head necessarily differs in
--     two documented ways: (1) CREATE OR REPLACE — the object exists (v2
--     replaced it in the up migration) and CREATE alone would fail;
--     (2) the public. schema qualification — the restore must not depend on
--     the session search_path.  CREATE OR REPLACE with no SET clause also
--     CLEARS the v2 search_path pin, restoring the v0.1.0 attribute state
--     exactly.  The 0005 trigger trg_assets_suppression survives untouched
--     (same name/arguments/return type).
-- ---------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION public.fn_check_asset_suppression() RETURNS trigger AS $$
DECLARE
    hit TEXT;
BEGIN
    IF TG_OP = 'INSERT' OR (NEW.kind <> OLD.kind OR NEW.value <> OLD.value
                            OR NEW.ip IS DISTINCT FROM OLD.ip
                            OR NEW.domain IS DISTINCT FROM OLD.domain
                            OR NEW.hostname IS DISTINCT FROM OLD.hostname) THEN
        IF NEW.kind = 'ip' AND NEW.ip IS NOT NULL THEN
            SELECT 'ip:' || s.value INTO hit
              FROM suppressions s
             WHERE s.enabled
               AND s.kind = 'ip'
               AND (s.expires_at IS NULL OR s.expires_at > now())
               AND NEW.ip = s.value::inet
             LIMIT 1;
            IF hit IS NULL THEN
                SELECT 'cidr:' || s.value INTO hit
                  FROM suppressions s
                 WHERE s.enabled
                   AND s.kind = 'cidr'
                   AND (s.expires_at IS NULL OR s.expires_at > now())
                   AND NEW.ip::cidr <<= s.value::cidr
                 LIMIT 1;
            END IF;
        ELSIF NEW.kind = 'domain' AND NEW.domain IS NOT NULL THEN
            SELECT 'domain:' || s.value INTO hit
              FROM suppressions s
             WHERE s.enabled
               AND s.kind = 'domain'
               AND (s.expires_at IS NULL OR s.expires_at > now())
               AND NEW.domain = s.value
             LIMIT 1;
            IF hit IS NULL THEN
                SELECT 'suffix:' || s.value INTO hit
                  FROM suppressions s
                 WHERE s.enabled
                   AND s.kind = 'domain_suffix'
                   AND (s.expires_at IS NULL OR s.expires_at > now())
                   AND (NEW.domain = s.value
                        OR right(NEW.domain, length(s.value) + 1) = '.' || s.value)
                 LIMIT 1;
            END IF;
        ELSIF NEW.kind = 'hostname' AND NEW.hostname IS NOT NULL THEN
            SELECT 'domain:' || s.value INTO hit
              FROM suppressions s
             WHERE s.enabled
               AND s.kind = 'domain'
               AND (s.expires_at IS NULL OR s.expires_at > now())
               AND NEW.hostname = s.value
             LIMIT 1;
            IF hit IS NULL THEN
                SELECT 'suffix:' || s.value INTO hit
                  FROM suppressions s
                 WHERE s.enabled
                   AND s.kind = 'domain_suffix'
                   AND (s.expires_at IS NULL OR s.expires_at > now())
                   AND (NEW.hostname = s.value
                        OR right(NEW.hostname, length(s.value) + 1) = '.' || s.value)
                 LIMIT 1;
            END IF;
        END IF;
    END IF;

    IF hit IS NOT NULL THEN
        RAISE EXCEPTION 'asset rejected: matches suppression rule %', hit
            USING ERRCODE = 'GSUP1';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ---------------------------------------------------------------------------
-- [6] Restore the intended post-down ACL and membership state (§3 item 8).
--
--     Pre-0016, gseer_app held NO privileges on public.suppressions (the
--     v0.1.0 runtime ran on the owner credential).  The up migration's
--     SELECT grant is therefore revoked here.
--
--     OD-1 default A (Rev 6.7.2 §7): the gseer_policy_owner table/sequence
--     privileges and the D-47 membership are RETAINED for upgrade
--     compatibility.  The role is object-empty but NOT privilege-empty;
--     revoking grants (option B) and/or membership (option C) require an
--     owner-approved revision of D-47 and are intentionally absent here.
--     Cluster-global roles are never dropped by this migration.
-- ---------------------------------------------------------------------------

REVOKE SELECT ON TABLE public.suppressions FROM gseer_app;

-- ---------------------------------------------------------------------------
-- [7] Post-condition verification + backstop functional probe.
-- ---------------------------------------------------------------------------

DO $gseer_0016d_verify$
DECLARE
    v_failures text[] := '{}';
    v_priv     text;
BEGIN
    -- Schema and functions are gone.
    IF to_regnamespace('gseer_policy') IS NOT NULL THEN
        v_failures := v_failures || 'gseer_policy schema still exists after down'::text;
    END IF;
    IF to_regprocedure('gseer_policy.suppression_insert(text,text,text,text,timestamptz,boolean)') IS NOT NULL
       OR to_regprocedure('gseer_policy.suppression_update(bigint,text,timestamptz,boolean)') IS NOT NULL
       OR to_regprocedure('gseer_policy.suppression_delete(bigint)') IS NOT NULL
       OR to_regprocedure('gseer_policy.fn_suppressions_policy_lock()') IS NOT NULL THEN
        v_failures := v_failures || 'a gseer_policy function still exists after down'::text;
    END IF;

    -- Zero policy triggers/functions/schema (§3 acceptance).
    IF EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'public.suppressions'::regclass
          AND tgname IN ('trg_suppressions_policy_insert','trg_suppressions_policy_update',
                         'trg_suppressions_policy_delete','trg_suppressions_policy_truncate')
          AND NOT tgisinternal
    ) THEN
        v_failures := v_failures || 'a policy statement trigger still exists on public.suppressions'::text;
    END IF;

    -- fn_check_asset_suppression restored to the v0.1.0 attribute state:
    -- no pinned search_path, no SECURITY clause, GSUP1 body marker present.
    IF position('search_path' IN pg_get_functiondef('public.fn_check_asset_suppression()'::regprocedure)) <> 0
       OR position('SECURITY' IN upper(pg_get_functiondef('public.fn_check_asset_suppression()'::regprocedure))) <> 0 THEN
        v_failures := v_failures || 'fn_check_asset_suppression does not match the v0.1.0 attribute state'::text;
    END IF;
    IF position('GSUP1' IN pg_get_functiondef('public.fn_check_asset_suppression()'::regprocedure)) = 0 THEN
        v_failures := v_failures || 'fn_check_asset_suppression body lost the GSUP1 marker'::text;
    END IF;

    -- The 0005 row trigger is intact.
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'public.assets'::regclass
          AND tgname = 'trg_assets_suppression'
          AND NOT tgisinternal
    ) THEN
        v_failures := v_failures || 'trg_assets_suppression is missing after down'::text;
    END IF;

    -- Post-down ACL: gseer_app holds nothing on public.suppressions;
    -- gseer_policy_owner retains exactly its OD-1-A grant set.
    FOREACH v_priv IN ARRAY ARRAY['SELECT','INSERT','UPDATE','DELETE','TRUNCATE'] LOOP
        IF has_table_privilege('gseer_app', 'public.suppressions', v_priv) THEN
            v_failures := v_failures || format('gseer_app still holds %s on public.suppressions after down', v_priv);
        END IF;
    END LOOP;

    IF array_length(v_failures, 1) > 0 THEN
        RAISE EXCEPTION 'migration 0016 down post-condition verification failed: %',
            array_to_string(v_failures, ' | ')
            USING ERRCODE = 'GSUPF';
    END IF;

    RAISE NOTICE '0016 down: teardown verified; gseer_policy_owner retains its OD-1-A grants and D-47 membership (object-empty, not privilege-empty)';
END
$gseer_0016d_verify$;

-- Backstop functional probe (§3 acceptance: "Confirm restored backstop
-- behavior"): a rule plus a matching asset write are created, the backstop
-- MUST refuse the asset write with GSUP1, and the whole probe is rolled
-- back.  If trg_assets_suppression were missing, the asset INSERT would
-- succeed and the GSUP9 marker would fire instead — failing the migration.
DO $gseer_0016d_probe$
BEGIN
    INSERT INTO public.suppressions (kind, value, reason)
    VALUES ('ip', '192.0.2.235', '0016 down backstop probe — rolled back');

    BEGIN
        INSERT INTO public.assets (kind, value, ip)
        VALUES ('ip', '192.0.2.235', '192.0.2.235');
        -- Reaching this point means the backstop did NOT fire.
        RAISE EXCEPTION '0016 down probe: restored backstop failed to refuse a suppressed asset write'
            USING ERRCODE = 'GSUP9';
    EXCEPTION
        WHEN SQLSTATE 'GSUP1' THEN
            NULL;  -- expected
    END;

    RAISE EXCEPTION '0016 down probe: rollback marker'
        USING ERRCODE = 'GSUP9';
EXCEPTION
    WHEN SQLSTATE 'GSUP9' THEN
        NULL;  -- expected: the probe subtransaction rolled back
END
$gseer_0016d_probe$;
