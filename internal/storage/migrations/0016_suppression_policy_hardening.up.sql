-- GSEER migration 0016_suppression_policy_hardening.up.sql — the gseer_policy
-- enforcement layer (v0.2.0, Rev 6.7.2 normative text; successor of the
-- Rev 6.7 / Rev 6.7.1 designs).
--
-- Source of authority: GSEER v0.2.0 — Migration Artifact & Privilege
-- Consistency Correction Revision 6.7.2, §2 (complete up migration) and
-- §4/§5/§8 (grant, audit and adoption contracts).  Baseline:
-- Lutfifakee-Project/gseer @ d84b291583bf2f0ec8d3499faa62031239e2681b
-- (v0.1.0, migrations 0001–0006).
--
-- Objects created (each numbered requirement of Rev 6.7.2 §2):
--   [1] Bootstrap refusal guard        (pre-flight, fail-closed)
--   [2] gseer_policy schema + ACL      (requirement 2)
--   [3] fn_suppressions_policy_lock()  (requirement 3, trigger-lock function)
--   [4] suppression_insert/_update/_delete()  (requirement 4, SECURITY DEFINER)
--   [5] 4 BEFORE STATEMENT triggers    (requirement 6)
--   [6] Ownership transfers + default-privilege hardening  (requirements 5, 9)
--   [7] PUBLIC EXECUTE revocation + explicit EXECUTE grants + transient-grant
--       cleanup                        (requirements 7, 8)
--   [8] Function-owner table/sequence privileges + direct-DML restrictions
--                                      (requirements 10, 11)
--   [9] fn_check_asset_suppression v2 + post-condition verification and
--       functional probes              (requirement 12)
--
-- Enforcement model (two independent gates, both fail-closed):
--   ACL gate   — only gseer_policy_owner holds INSERT/UPDATE/DELETE on
--                public.suppressions; gseer_app holds SELECT only.  Direct
--                DML by the runtime role is refused by the privilege system
--                (SQLSTATE 42501) before any trigger runs.
--   Gate layer — every BEFORE STATEMENT trigger on public.suppressions calls
--                fn_suppressions_policy_lock(), which (a) serializes policy
--                mutations on the exclusive double-integer advisory xact lock
--                (classid=1196639570 'GSER', objid=1347374129 'POL1') and
--                (b) refuses any DML executed outside a policy-function
--                context (custom ERRCODE 'GSUP2').  The policy context is
--                the transaction-local GUC 'gseer.policy_dml' set by the
--                three SECURITY DEFINER policy functions.
--                HONEST LIMIT (documented, not overstated): custom GUCs are
--                settable by any session, so the GSUP2 gate is an
--                anti-accident layer for roles that already hold DML
--                privileges (table owner, future maintenance roles) — it is
--                NOT a security boundary against a hostile DML-granted role.
--                The security boundary is the ACL gate plus the pre-runtime
--                effective-privilege audit (Rev 6.7.2 §5).
--
-- Transaction contract (D-M3-5, unchanged): the migrate driver sends this
-- file as ONE statement batch — a single implicit transaction.  This file
-- contains NO transaction-control statements (no BEGIN/COMMIT/SAVEPOINT/
-- SET CONSTRAINTS).  SET ROLE is a session statement and is permitted.
--
-- PostgreSQL floor: 16 (pg_has_role(...,'SET') and pg_auth_members.
-- set_option/inherit_option are used by this migration's guard and by the
-- Rev 6.7.2 §5 audit; v0.1.0 ships PostgreSQL 17-alpine, tested on 17.2).

-- ---------------------------------------------------------------------------
-- [1] Bootstrap refusal guard — every precondition verified BEFORE any
--     object is created.  Any failure aborts the whole migration (the
--     driver rolls the implicit transaction back and marks the ledger
--     dirty; recovery is `gseer db migrate force` after operator
--     inspection, per migrations/README.md rule 4).
-- ---------------------------------------------------------------------------

DO $gseer_0016_guard$
DECLARE
    v_failures text[] := '{}';
    v_msg      text;
BEGIN
    -- (1.1) PostgreSQL 16+ floor.
    IF current_setting('server_version_num')::int < 160000 THEN
        v_failures := v_failures || format(
            'PostgreSQL 16+ required (found server_version_num=%s); the guard and the Rev 6.7.2 §5 audit need pg_auth_members SET/INHERIT options',
            current_setting('server_version_num'));
    END IF;

    -- (1.2) The executing role must own public.suppressions (it will be
    -- granted on, revoked from, and receive triggers in this migration).
    IF NOT EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relname = 'suppressions'
          AND c.relowner = current_user::regrole::oid
    ) THEN
        v_failures := v_failures || format(
            'role %L does not own public.suppressions; run migration 0016 with the credential that owns the v0.1.0 schema (GSEER_DB_USER), or transfer ownership first',
            current_user);
    END IF;

    -- (1.3) v0.1.0 baseline integrity: the backstop function and its row
    -- trigger must exist (migration 0005 applied).
    IF to_regprocedure('public.fn_check_asset_suppression()') IS NULL THEN
        v_failures := v_failures ||
            'public.fn_check_asset_suppression() is missing; the v0.1.0 baseline is incomplete (migration 0005 not applied?)'::text;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'public.assets'::regclass
          AND tgname = 'trg_assets_suppression'
          AND NOT tgisinternal
    ) THEN
        v_failures := v_failures ||
            'trigger trg_assets_suppression is missing on public.assets; the v0.1.0 baseline is incomplete (migration 0005 not applied?)'::text;
    END IF;

    -- (1.4) v0.1.0 suppressions shape drift detection (the 0005 columns
    -- must all be present; extra columns are tolerated).
    IF to_regclass('public.suppressions') IS NULL THEN
        v_failures := v_failures || 'public.suppressions is missing'::text;
    ELSIF to_regclass('public.suppressions_id_seq') IS NULL THEN
        v_failures := v_failures || 'public.suppressions_id_seq is missing'::text;
    ELSE
        FOREACH v_msg IN ARRAY ARRAY[
            'id', 'kind', 'value', 'reason', 'created_by', 'created_at', 'expires_at', 'enabled'
        ] LOOP
            IF NOT EXISTS (
                SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public'
                  AND table_name  = 'suppressions'
                  AND column_name = v_msg
            ) THEN
                v_failures := v_failures || format('public.suppressions is missing column %s (v0.1.0 shape drift)', v_msg);
            END IF;
        END LOOP;
    END IF;

    -- (1.5) Roles must pre-exist (created by the deployment bootstrap) and
    -- carry exactly the safe attribute set.  Pre-existing roles are kept
    -- AS-IS; unsafe attributes fail the migration (never silently kept).
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gseer_policy_owner') THEN
        v_failures := v_failures ||
            'role gseer_policy_owner does not exist; run the deployment bootstrap (Rev 6.7.2 §6 reference script) first'::text;
    ELSE
        IF (SELECT rolsuper      FROM pg_roles WHERE rolname = 'gseer_policy_owner') THEN
            v_failures := v_failures || 'gseer_policy_owner must not be SUPERUSER'::text;
        END IF;
        IF (SELECT rolcreaterole FROM pg_roles WHERE rolname = 'gseer_policy_owner') THEN
            v_failures := v_failures || 'gseer_policy_owner must not be CREATEROLE'::text;
        END IF;
        IF (SELECT rolcreatedb   FROM pg_roles WHERE rolname = 'gseer_policy_owner') THEN
            v_failures := v_failures || 'gseer_policy_owner must not be CREATEDB'::text;
        END IF;
        IF (SELECT rolbypassrls  FROM pg_roles WHERE rolname = 'gseer_policy_owner') THEN
            v_failures := v_failures || 'gseer_policy_owner must not be BYPASSRLS'::text;
        END IF;
        IF (SELECT rolcanlogin   FROM pg_roles WHERE rolname = 'gseer_policy_owner') THEN
            v_failures := v_failures || 'gseer_policy_owner must be NOLOGIN'::text;
        END IF;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gseer_app') THEN
        v_failures := v_failures ||
            'role gseer_app does not exist; run the deployment bootstrap (Rev 6.7.2 §6 reference script) first'::text;
    ELSE
        IF (SELECT rolsuper      FROM pg_roles WHERE rolname = 'gseer_app') THEN
            v_failures := v_failures || 'gseer_app must not be SUPERUSER'::text;
        END IF;
        IF (SELECT rolcreaterole FROM pg_roles WHERE rolname = 'gseer_app') THEN
            v_failures := v_failures || 'gseer_app must not be CREATEROLE'::text;
        END IF;
        IF (SELECT rolcreatedb   FROM pg_roles WHERE rolname = 'gseer_app') THEN
            v_failures := v_failures || 'gseer_app must not be CREATEDB'::text;
        END IF;
        IF (SELECT rolbypassrls  FROM pg_roles WHERE rolname = 'gseer_app') THEN
            v_failures := v_failures || 'gseer_app must not be BYPASSRLS'::text;
        END IF;
        IF NOT (SELECT rolcanlogin FROM pg_roles WHERE rolname = 'gseer_app') THEN
            v_failures := v_failures || 'gseer_app must be LOGIN (it is the runtime credential)'::text;
        END IF;
    END IF;

    -- (1.6) D-47 membership: the executing (migration) role must be a
    -- member of gseer_policy_owner, both MEMBER (ALTER FUNCTION OWNER /
    -- ALTER DEFAULT PRIVILEGES FOR ROLE) and SET (the down migration uses
    -- SET ROLE to drop the owner's objects).
    IF NOT pg_has_role(current_user, 'gseer_policy_owner', 'MEMBER') THEN
        v_failures := v_failures || format(
            'role %L is not a member of gseer_policy_owner (D-47 required for ALTER ... OWNER TO and ALTER DEFAULT PRIVILEGES FOR ROLE)',
            current_user);
    END IF;
    IF NOT pg_has_role(current_user, 'gseer_policy_owner', 'SET') THEN
        v_failures := v_failures || format(
            'role %L cannot SET ROLE gseer_policy_owner (D-47 SET option required; the down migration must assume the owner to drop its objects)',
            current_user);
    END IF;

    -- (1.7) §8 adoption policy: a pre-existing gseer_policy schema is never
    -- silently adopted.  CREATE SCHEMA IF NOT EXISTS is deliberately NOT
    -- used; the guard refuses instead and names the remediation paths.
    IF to_regnamespace('gseer_policy') IS NOT NULL THEN
        v_failures := v_failures ||
            'schema gseer_policy already exists; refuse-and-remediate (Rev 6.7.2 §8): either (a) verify it is an orphan of an earlier attempt and DROP SCHEMA gseer_policy CASCADE after recording its object inventory, or (b) run the §8 verified-adoption checklist and re-run this migration'::text;
    END IF;

    -- (1.8) Trigger-name collisions on public.suppressions would break
    -- block [5] mid-migration; detect them before anything is created.
    FOREACH v_msg IN ARRAY ARRAY[
        'trg_suppressions_policy_insert',
        'trg_suppressions_policy_update',
        'trg_suppressions_policy_delete',
        'trg_suppressions_policy_truncate'
    ] LOOP
        IF EXISTS (
            SELECT 1 FROM pg_trigger
            WHERE tgrelid = 'public.suppressions'::regclass
              AND tgname = v_msg
              AND NOT tgisinternal
        ) THEN
            v_failures := v_failures || format('trigger %s already exists on public.suppressions', v_msg);
        END IF;
    END LOOP;

    IF array_length(v_failures, 1) > 0 THEN
        RAISE EXCEPTION 'migration 0016 refusal guard failed with % precondition failure(s): %',
            array_length(v_failures, 1), array_to_string(v_failures, ' | ')
            USING ERRCODE = 'GSUPF';
    END IF;

    RAISE NOTICE '0016 guard: all preconditions verified (PG16+, v0.1.0 baseline, roles, D-47, no pre-existing gseer_policy)';
END
$gseer_0016_guard$;

-- ---------------------------------------------------------------------------
-- [2] gseer_policy schema + ACL (requirement 2).
--     Owner: gseer_policy_owner from creation (no later schema ownership
--     transfer is needed).  PUBLIC gets nothing.  gseer_app gets USAGE only
--     (needed to resolve/execute the policy functions).  The executing role
--     gets transient CREATE+USAGE for blocks [3]–[5]; revoked in block [7].
-- ---------------------------------------------------------------------------

CREATE SCHEMA gseer_policy AUTHORIZATION gseer_policy_owner;

COMMENT ON SCHEMA gseer_policy IS
    'GSEER suppression policy layer (0016): SECURITY DEFINER policy functions and the statement-lock trigger function. Owner: gseer_policy_owner.';

REVOKE ALL ON SCHEMA gseer_policy FROM PUBLIC;

GRANT CREATE, USAGE ON SCHEMA gseer_policy TO CURRENT_USER;  -- transient; revoked in [7]

GRANT USAGE ON SCHEMA gseer_policy TO gseer_app;

-- ---------------------------------------------------------------------------
-- [3] Trigger-lock function (requirement 3).
--     Created by the executing role (ownership transferred in [6]); the
--     four triggers in [5] are created by the executing role, which must
--     hold EXECUTE on the trigger function at CREATE TRIGGER time — true
--     here because the creator/owner retains EXECUTE until the transfer.
--
--     Advisory lock keys (exclusive, two-int4 form of pg_advisory_xact_lock):
--       classid = 1196639570 = 0x47534552 = big-endian ASCII "GSER"
--       objid   = 1347374129 = 0x504F4C31 = big-endian ASCII "POL1"
--     pg_locks representation: locktype='advisory', classid=1196639570,
--     objid=1347374129, objsubid=2, mode='ExclusiveLock' (AC-90).
-- ---------------------------------------------------------------------------

CREATE FUNCTION gseer_policy.fn_suppressions_policy_lock()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
BEGIN
    -- (a) Serialize all suppression policy mutations across transactions.
    PERFORM pg_advisory_xact_lock(1196639570, 1347374129);

    -- (b) Anti-accident gate: refuse DML that did not originate inside a
    --     gseer_policy policy function.  See the file header for the honest
    --     security scope of this check (GUCs are user-settable; the ACL is
    --     the boundary).
    IF COALESCE(current_setting('gseer.policy_dml', TRUE), '') <> 'on' THEN
        RAISE EXCEPTION
            'direct DML on public.suppressions is not permitted; use gseer_policy.suppression_insert / suppression_update / suppression_delete'
            USING ERRCODE = 'GSUP2',
                  HINT    = 'the policy context GUC gseer.policy_dml is set only by the gseer_policy policy functions';
    END IF;

    RETURN NULL;
END;
$fn$;

COMMENT ON FUNCTION gseer_policy.fn_suppressions_policy_lock() IS
    'GSEER 0016 statement-level lock + policy-context gate for public.suppressions (advisory keys GSER/POL1; ERRCODE GSUP2). Trigger-only: not granted for direct execution.';

-- ---------------------------------------------------------------------------
-- [4] Three SECURITY DEFINER policy functions (requirement 4).
--     Signatures are the v0.2.0 normative replacement for the store's direct
--     DML on public.suppressions (AddSuppression / RemoveSuppression and the
--     future update path).  Semantics mirror the v0.1.0 store exactly:
--       * insert: ON CONFLICT (kind, value) no-op update → idempotent
--         re-add, returning the existing id (parity with suppression.go
--         AddSuppression at baseline d84b2915);
--       * update: reason/enabled are COALESCEd (leave unchanged when NULL);
--         expires_at is assigned directly (NULL clears expiry);
--       * update/delete by unknown id: GSUP2 exception (fail-closed, never
--         a silent zero-row success).
--     Value normalization stays in the Go layer (suppress.NormalizeValue);
--     kind domain is enforced by the table CHECK (0005).
-- ---------------------------------------------------------------------------

CREATE FUNCTION gseer_policy.suppression_insert(
    p_kind        text,
    p_value       text,
    p_reason      text,
    p_created_by  text,
    p_expires_at  timestamptz,
    p_enabled     boolean
)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $fn$
DECLARE
    v_id bigint;
BEGIN
    PERFORM set_config('gseer.policy_dml', 'on', TRUE);

    INSERT INTO public.suppressions (kind, value, reason, created_by, expires_at, enabled)
    VALUES (p_kind, p_value, p_reason, p_created_by, p_expires_at, COALESCE(p_enabled, TRUE))
    ON CONFLICT (kind, value) DO UPDATE
        SET kind = EXCLUDED.kind   -- no-op update: parity with the v0.1.0 store upsert
    RETURNING id INTO v_id;

    RETURN v_id;
END;
$fn$;

COMMENT ON FUNCTION gseer_policy.suppression_insert(text,text,text,text,timestamptz,boolean) IS
    'GSEER 0016 policy function: idempotent rule insert; returns the rule id. SECURITY DEFINER (gseer_policy_owner). Runtime-executable by gseer_app.';

CREATE FUNCTION gseer_policy.suppression_update(
    p_id          bigint,
    p_reason      text,
    p_expires_at  timestamptz,
    p_enabled     boolean
)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $fn$
DECLARE
    v_rows bigint;
BEGIN
    PERFORM set_config('gseer.policy_dml', 'on', TRUE);

    UPDATE public.suppressions
       SET reason     = COALESCE(p_reason, reason),
           expires_at = p_expires_at,
           enabled    = COALESCE(p_enabled, enabled)
     WHERE id = p_id;

    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows = 0 THEN
        RAISE EXCEPTION 'suppression rule % not found', p_id USING ERRCODE = 'GSUP2';
    END IF;
    RETURN v_rows::integer;
END;
$fn$;

COMMENT ON FUNCTION gseer_policy.suppression_update(bigint,text,timestamptz,boolean) IS
    'GSEER 0016 policy function: partial rule update (reason/enabled COALESCEd, expires_at assigned; NULL clears expiry); 0 rows → GSUP2. SECURITY DEFINER (gseer_policy_owner).';

CREATE FUNCTION gseer_policy.suppression_delete(p_id bigint)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $fn$
DECLARE
    v_rows bigint;
BEGIN
    PERFORM set_config('gseer.policy_dml', 'on', TRUE);

    DELETE FROM public.suppressions WHERE id = p_id;

    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows = 0 THEN
        RAISE EXCEPTION 'suppression rule % not found', p_id USING ERRCODE = 'GSUP2';
    END IF;
    RETURN v_rows::integer;
END;
$fn$;

COMMENT ON FUNCTION gseer_policy.suppression_delete(bigint) IS
    'GSEER 0016 policy function: rule delete (suppression_applications FK survives via ON DELETE SET NULL); 0 rows → GSUP2. SECURITY DEFINER (gseer_policy_owner).';

-- ---------------------------------------------------------------------------
-- [5] Four BEFORE STATEMENT triggers (requirement 6).
--     Created by the executing role, which owns public.suppressions and
--     (still) owns the trigger function — both CREATE TRIGGER preconditions
--     hold at this point in the file.
-- ---------------------------------------------------------------------------

CREATE TRIGGER trg_suppressions_policy_insert
    BEFORE INSERT ON public.suppressions
    FOR EACH STATEMENT EXECUTE FUNCTION gseer_policy.fn_suppressions_policy_lock();

CREATE TRIGGER trg_suppressions_policy_update
    BEFORE UPDATE ON public.suppressions
    FOR EACH STATEMENT EXECUTE FUNCTION gseer_policy.fn_suppressions_policy_lock();

CREATE TRIGGER trg_suppressions_policy_delete
    BEFORE DELETE ON public.suppressions
    FOR EACH STATEMENT EXECUTE FUNCTION gseer_policy.fn_suppressions_policy_lock();

CREATE TRIGGER trg_suppressions_policy_truncate
    BEFORE TRUNCATE ON public.suppressions
    FOR EACH STATEMENT EXECUTE FUNCTION gseer_policy.fn_suppressions_policy_lock();

-- ---------------------------------------------------------------------------
-- [6] Ownership transfers (requirement 5) + default-privilege hardening
--     (requirement 9).  The executing role owns the four functions just
--     created and is a (guard-verified) member of gseer_policy_owner, so
--     ALTER ... OWNER TO is legal here.  The schema was born owned by
--     gseer_policy_owner (block [2], AUTHORIZATION clause) — no schema
--     ownership transfer exists or is needed.
-- ---------------------------------------------------------------------------

ALTER FUNCTION gseer_policy.fn_suppressions_policy_lock()
    OWNER TO gseer_policy_owner;
ALTER FUNCTION gseer_policy.suppression_insert(text, text, text, text, timestamptz, boolean)
    OWNER TO gseer_policy_owner;
ALTER FUNCTION gseer_policy.suppression_update(bigint, text, timestamptz, boolean)
    OWNER TO gseer_policy_owner;
ALTER FUNCTION gseer_policy.suppression_delete(bigint)
    OWNER TO gseer_policy_owner;

-- Future functions created by gseer_policy_owner must NOT be automatically
-- executable by PUBLIC.  VERIFIED POSTGRESQL 17.2 BEHAVIOR (Rev 6.7.2 §9,
-- hardening-marker note): a schema-scoped default-privilege entry is stored
-- only when it carries a GRANT — a scoped REVOKE-only template that would
-- leave nothing but implicit owner privileges is NOT stored and is a
-- silent no-op.  The GLOBAL (no IN SCHEMA) entry replaces the built-in
-- default for the role and IS honored: entry {gseer_policy_owner=X} makes
-- every future function of gseer_policy_owner non-PUBLIC-executable.
-- gseer_policy_owner is a dedicated NOLOGIN object owner whose only
-- contracted objects are the gseer_policy functions, so the global scope
-- is equivalent in effect and is the only form PostgreSQL honors.  The
-- per-function REVOKE ALL FROM PUBLIC in block [7] remains as
-- belt-and-braces.
ALTER DEFAULT PRIVILEGES FOR ROLE gseer_policy_owner
    REVOKE ALL ON FUNCTIONS FROM PUBLIC;

-- ---------------------------------------------------------------------------
-- [7] Function ACL hardening (requirements 7, 8) + transient-grant cleanup.
--     After the ownership transfer the executing role no longer holds
--     implicit EXECUTE — the grants below are the ONLY execution paths.
-- ---------------------------------------------------------------------------

REVOKE ALL ON FUNCTION gseer_policy.fn_suppressions_policy_lock() FROM PUBLIC;
REVOKE ALL ON FUNCTION gseer_policy.suppression_insert(text, text, text, text, timestamptz, boolean) FROM PUBLIC;
REVOKE ALL ON FUNCTION gseer_policy.suppression_update(bigint, text, timestamptz, boolean) FROM PUBLIC;
REVOKE ALL ON FUNCTION gseer_policy.suppression_delete(bigint) FROM PUBLIC;

-- Runtime execution surface: exactly the three policy functions.  The lock
-- function is trigger-invoked (no EXECUTE check at trigger fire time) and
-- is deliberately NOT granted to anyone but its owner.
GRANT EXECUTE ON FUNCTION gseer_policy.suppression_insert(text, text, text, text, timestamptz, boolean) TO gseer_app;
GRANT EXECUTE ON FUNCTION gseer_policy.suppression_update(bigint, text, timestamptz, boolean) TO gseer_app;
GRANT EXECUTE ON FUNCTION gseer_policy.suppression_delete(bigint) TO gseer_app;

-- Transient setup grants from block [2] end here.  Ongoing maintenance of
-- gseer_policy objects goes through SET ROLE gseer_policy_owner (D-47),
-- not through standing schema privileges on the migration role.
REVOKE ALL ON SCHEMA gseer_policy FROM CURRENT_USER;

-- ---------------------------------------------------------------------------
-- [8] Function-owner table and sequence privileges (requirement 11) +
--     direct-DML restrictions (requirement 10) + the runtime SELECT grant.
--
--     gseer_policy_owner needs SELECT/INSERT/UPDATE/DELETE on
--     public.suppressions: the SECURITY DEFINER functions execute as this
--     role, and UPDATE/DELETE ... WHERE id = $1 additionally require SELECT
--     on the referenced column.  TRUNCATE is deliberately not granted —
--     policy mutations go through the functions only.
--
--     Sequence note (verified against PostgreSQL 17.2): INSERT through an
--     IDENTITY column uses nextval_internal(), which does NOT check
--     sequence ACLs.  The USAGE grant on suppressions_id_seq is therefore
--     belt-and-braces (it keeps the owner's write path ACL-robust if a
--     policy function ever addresses the sequence directly); it is not
--     load-bearing for the identity path.  gseer_app receives NO sequence
--     privileges anywhere (identity path needs none) — Rev 6.7.2 §5.
-- ---------------------------------------------------------------------------

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.suppressions TO gseer_policy_owner;
GRANT USAGE ON SEQUENCE public.suppressions_id_seq TO gseer_policy_owner;

-- Direct-DML restrictions: normalize the ACL state so that no role except
-- the function owner holds suppression DML, and PUBLIC holds nothing.
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON TABLE public.suppressions FROM PUBLIC;
REVOKE ALL ON SEQUENCE public.suppressions_id_seq FROM PUBLIC;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON TABLE public.suppressions FROM gseer_app;

-- The runtime role reads the policy table for two evidenced paths: the Go
-- gate (LoadSuppressions) and the SECURITY INVOKER backstop trigger
-- fn_check_asset_suppression, which executes with the writing role's
-- privileges and therefore needs SELECT for every role that writes assets.
GRANT SELECT ON TABLE public.suppressions TO gseer_app;

-- ---------------------------------------------------------------------------
-- [9] fn_check_asset_suppression v2 (requirement 12) + post-condition
--     verification + functional probes.
--
--     v2 hardening relative to the v0.1.0 body (0005_suppressions.up.sql
--     lines 130–200, baseline d84b2915):
--       * every table reference is schema-qualified (public.suppressions) —
--         the v0.1.0 body resolved `suppressions` through search_path, and
--         an unlisted pg_temp schema is searched FIRST, so a session with a
--         temp table named `suppressions` could hijack the backstop read;
--       * SET search_path = pg_catalog, public, pg_temp pins resolution
--         order for any unqualified function call (pg_catalog is always
--         implicitly first; listing pg_temp LAST removes the
--         first-searched default for unlisted temp schemas);
--       * SECURITY INVOKER is stated explicitly (unchanged semantics).
--     Signature, body logic and ERRCODE 'GSUP1' are unchanged — the storage
--     layer maps GSUP1 → DB_SUPPRESSED and both enforcement layers must
--     keep agreeing.  CREATE OR REPLACE preserves the 0005 trigger
--     trg_assets_suppression and the function's owner.
-- ---------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION public.fn_check_asset_suppression()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public, pg_temp
AS $fn$
DECLARE
    hit TEXT;
BEGIN
    IF TG_OP = 'INSERT' OR (NEW.kind <> OLD.kind OR NEW.value <> OLD.value
                            OR NEW.ip IS DISTINCT FROM OLD.ip
                            OR NEW.domain IS DISTINCT FROM OLD.domain
                            OR NEW.hostname IS DISTINCT FROM OLD.hostname) THEN
        IF NEW.kind = 'ip' AND NEW.ip IS NOT NULL THEN
            SELECT 'ip:' || s.value INTO hit
              FROM public.suppressions s
             WHERE s.enabled
               AND s.kind = 'ip'
               AND (s.expires_at IS NULL OR s.expires_at > now())
               AND NEW.ip = s.value::inet
             LIMIT 1;
            IF hit IS NULL THEN
                SELECT 'cidr:' || s.value INTO hit
                  FROM public.suppressions s
                 WHERE s.enabled
                   AND s.kind = 'cidr'
                   AND (s.expires_at IS NULL OR s.expires_at > now())
                   AND NEW.ip::cidr <<= s.value::cidr
                 LIMIT 1;
            END IF;
        ELSIF NEW.kind = 'domain' AND NEW.domain IS NOT NULL THEN
            SELECT 'domain:' || s.value INTO hit
              FROM public.suppressions s
             WHERE s.enabled
               AND s.kind = 'domain'
               AND (s.expires_at IS NULL OR s.expires_at > now())
               AND NEW.domain = s.value
             LIMIT 1;
            IF hit IS NULL THEN
                SELECT 'suffix:' || s.value INTO hit
                  FROM public.suppressions s
                 WHERE s.enabled
                   AND s.kind = 'domain_suffix'
                   AND (s.expires_at IS NULL OR s.expires_at > now())
                   AND (NEW.domain = s.value
                        OR right(NEW.domain, length(s.value) + 1) = '.' || s.value)
                 LIMIT 1;
            END IF;
        ELSIF NEW.kind = 'hostname' AND NEW.hostname IS NOT NULL THEN
            SELECT 'domain:' || s.value INTO hit
              FROM public.suppressions s
             WHERE s.enabled
               AND s.kind = 'domain'
               AND (s.expires_at IS NULL OR s.expires_at > now())
               AND NEW.hostname = s.value
             LIMIT 1;
            IF hit IS NULL THEN
                SELECT 'suffix:' || s.value INTO hit
                  FROM public.suppressions s
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
$fn$;

COMMENT ON FUNCTION public.fn_check_asset_suppression() IS
    'GSEER v2 (0016): v0.1.0 backstop logic with schema-qualified references and pinned search_path (anti pg_temp hijack). ERRCODE GSUP1 unchanged.';

-- ---------------------------------------------------------------------------
-- [9a] Post-condition verification: every required object, owner, ACL and
--      trigger shape is asserted inside the migration.  Any failure rolls
--      the whole migration back (single implicit transaction).
-- ---------------------------------------------------------------------------

DO $gseer_0016_verify$
DECLARE
    v_failures text[] := '{}';
    v_tmp      text;
BEGIN
    -- Schema state.
    IF (SELECT nspowner::regrole::text FROM pg_namespace WHERE nspname = 'gseer_policy')
           IS DISTINCT FROM 'gseer_policy_owner' THEN
        v_failures := v_failures || 'gseer_policy schema owner is not gseer_policy_owner'::text;
    END IF;

    -- Function ownership and security attributes.
    IF (SELECT proowner::regrole::text FROM pg_proc WHERE oid = 'gseer_policy.fn_suppressions_policy_lock()'::regprocedure)
           IS DISTINCT FROM 'gseer_policy_owner' THEN
        v_failures := v_failures || 'fn_suppressions_policy_lock owner is not gseer_policy_owner'::text;
    END IF;
    IF (SELECT prosecdef FROM pg_proc WHERE oid = 'gseer_policy.fn_suppressions_policy_lock()'::regprocedure) THEN
        v_failures := v_failures || 'fn_suppressions_policy_lock must not be SECURITY DEFINER'::text;
    END IF;
    FOREACH v_tmp IN ARRAY ARRAY[
        'gseer_policy.suppression_insert(text,text,text,text,timestamptz,boolean)',
        'gseer_policy.suppression_update(bigint,text,timestamptz,boolean)',
        'gseer_policy.suppression_delete(bigint)'
    ] LOOP
        IF NOT (SELECT prosecdef FROM pg_proc WHERE oid = v_tmp::regprocedure) THEN
            v_failures := v_failures || format('%s is not SECURITY DEFINER', v_tmp);
        END IF;
        IF (SELECT proowner::regrole::text FROM pg_proc WHERE oid = v_tmp::regprocedure)
               IS DISTINCT FROM 'gseer_policy_owner' THEN
            v_failures := v_failures || format('%s owner is not gseer_policy_owner', v_tmp);
        END IF;
        IF position('search_path' IN coalesce((SELECT proconfig::text FROM pg_proc WHERE oid = v_tmp::regprocedure), '')) = 0 THEN
            v_failures := v_failures || format('%s has no pinned search_path', v_tmp);
        END IF;
    END LOOP;

    -- PUBLIC EXECUTE revoked everywhere (requirement 7).
    IF EXISTS (
        SELECT 1 FROM pg_proc p, aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a
        WHERE p.oid IN (
            'gseer_policy.fn_suppressions_policy_lock()'::regprocedure,
            'gseer_policy.suppression_insert(text,text,text,text,timestamptz,boolean)'::regprocedure,
            'gseer_policy.suppression_update(bigint,text,timestamptz,boolean)'::regprocedure,
            'gseer_policy.suppression_delete(bigint)'::regprocedure)
          AND a.grantee = 0  -- PUBLIC
    ) THEN
        v_failures := v_failures || 'PUBLIC still holds a privilege on a gseer_policy function'::text;
    END IF;

    -- Explicit EXECUTE surface (requirement 8).
    FOREACH v_tmp IN ARRAY ARRAY[
        'gseer_policy.suppression_insert(text,text,text,text,timestamptz,boolean)',
        'gseer_policy.suppression_update(bigint,text,timestamptz,boolean)',
        'gseer_policy.suppression_delete(bigint)'
    ] LOOP
        IF NOT has_function_privilege('gseer_app', v_tmp::regprocedure, 'EXECUTE') THEN
            v_failures := v_failures || format('gseer_app lacks EXECUTE on %s', v_tmp);
        END IF;
    END LOOP;
    IF has_function_privilege('gseer_app', 'gseer_policy.fn_suppressions_policy_lock()'::regprocedure, 'EXECUTE') THEN
        v_failures := v_failures || 'gseer_app must not hold EXECUTE on fn_suppressions_policy_lock'::text;
    END IF;

    -- Schema ACL.
    IF NOT has_schema_privilege('gseer_app', 'gseer_policy', 'USAGE') THEN
        v_failures := v_failures || 'gseer_app lacks USAGE on gseer_policy'::text;
    END IF;
    IF has_schema_privilege('gseer_app', 'gseer_policy', 'CREATE') THEN
        v_failures := v_failures || 'gseer_app must not hold CREATE on gseer_policy'::text;
    END IF;

    -- Direct-DML restrictions (requirement 10): has_table_privilege follows
    -- the full membership closure (conservative — SET-ROLE-reachable
    -- privileges included), so these assertions double as the in-migration
    -- form of the §5 effective-privilege audit for this table.
    IF NOT has_table_privilege('gseer_app', 'public.suppressions', 'SELECT') THEN
        v_failures := v_failures || 'gseer_app lacks SELECT on public.suppressions'::text;
    END IF;
    FOREACH v_tmp IN ARRAY ARRAY['INSERT', 'UPDATE', 'DELETE', 'TRUNCATE'] LOOP
        IF has_table_privilege('gseer_app', 'public.suppressions', v_tmp) THEN
            v_failures := v_failures || format('gseer_app must not hold %s on public.suppressions', v_tmp);
        END IF;
    END LOOP;

    -- Function-owner privileges (requirement 11).
    FOREACH v_tmp IN ARRAY ARRAY['SELECT', 'INSERT', 'UPDATE', 'DELETE'] LOOP
        IF NOT has_table_privilege('gseer_policy_owner', 'public.suppressions', v_tmp) THEN
            v_failures := v_failures || format('gseer_policy_owner lacks %s on public.suppressions', v_tmp);
        END IF;
    END LOOP;
    IF has_table_privilege('gseer_policy_owner', 'public.suppressions', 'TRUNCATE') THEN
        v_failures := v_failures || 'gseer_policy_owner must not hold TRUNCATE on public.suppressions'::text;
    END IF;
    IF NOT has_sequence_privilege('gseer_policy_owner', 'public.suppressions_id_seq', 'USAGE') THEN
        v_failures := v_failures || 'gseer_policy_owner lacks USAGE on public.suppressions_id_seq'::text;
    END IF;

    -- The four triggers: BEFORE + STATEMENT + correct event, pointing at
    -- the lock function (pg_trigger.tgtype bits: 2=BEFORE, 4=INSERT,
    -- 8=DELETE, 16=UPDATE, 32=TRUNCATE; STATEMENT = no ROW bit).
    FOREACH v_tmp IN ARRAY ARRAY[
        'trg_suppressions_policy_insert|6',
        'trg_suppressions_policy_update|18',
        'trg_suppressions_policy_delete|10',
        'trg_suppressions_policy_truncate|34'
    ] LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_trigger
            WHERE tgrelid = 'public.suppressions'::regclass
              AND tgname  = split_part(v_tmp, '|', 1)
              AND NOT tgisinternal
              AND tgtype  = split_part(v_tmp, '|', 2)::smallint
              AND tgfoid  = 'gseer_policy.fn_suppressions_policy_lock()'::regprocedure
        ) THEN
            v_failures := v_failures || format('trigger %s missing/wrong shape/wrong function', split_part(v_tmp, '|', 1));
        END IF;
    END LOOP;

    -- The 0005 backstop must still be in place over the replaced function.
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'public.assets'::regclass
          AND tgname = 'trg_assets_suppression'
          AND NOT tgisinternal
    ) THEN
        v_failures := v_failures || 'trg_assets_suppression disappeared during 0016'::text;
    END IF;
    IF position('public.suppressions' IN pg_get_functiondef('public.fn_check_asset_suppression()'::regprocedure)) = 0
       OR position('search_path' IN pg_get_functiondef('public.fn_check_asset_suppression()'::regprocedure)) = 0 THEN
        v_failures := v_failures || 'fn_check_asset_suppression v2 markers missing (qualified refs / pinned search_path)'::text;
    END IF;

    -- Default-privilege hardening marker (requirement 9): the GLOBAL
    -- entry for gseer_policy_owner must exist and carry no PUBLIC
    -- privilege (verified PostgreSQL 17.2 semantics: the global entry
    -- replaces the built-in default and blocks automatic PUBLIC EXECUTE
    -- on future functions of that role).
    IF NOT EXISTS (
        SELECT 1 FROM pg_default_acl d
        WHERE d.defaclrole = 'gseer_policy_owner'::regrole
          AND d.defaclnamespace = 0  -- 0 = global (unscoped) entry
          AND d.defaclobjtype = 'f'
          AND NOT EXISTS (
              SELECT 1 FROM aclexplode(d.defaclacl) a WHERE a.grantee = 0)
    ) THEN
        v_failures := v_failures || 'default-privilege hardening entry for gseer_policy_owner functions is missing'::text;
    END IF;

    IF array_length(v_failures, 1) > 0 THEN
        RAISE EXCEPTION 'migration 0016 post-condition verification failed with % failure(s): %',
            array_length(v_failures, 1), array_to_string(v_failures, ' | ')
            USING ERRCODE = 'GSUPF';
    END IF;

    RAISE NOTICE '0016 verify: structural postconditions hold';
END
$gseer_0016_verify$;

-- ---------------------------------------------------------------------------
-- [9b] Functional probes — the gate and the lock are exercised for real,
--      then rolled back inside this block (nothing the probe writes can
--      outlive the migration).  Probe values use RFC 5737 TEST-NET-3
--      (192.0.2.0/24), reserved documentation space.
--
--      Probe (a): direct INSERT without policy context MUST be refused with
--      GSUP2 by the BEFORE INSERT statement trigger.
--      Probe (b): with the policy context set, the INSERT MUST proceed,
--      MUST acquire the exclusive two-int4 advisory xact lock, and that
--      lock MUST be visible in pg_locks exactly as AC-90 specifies:
--      locktype='advisory', classid=1196639570, objid=1347374129,
--      objsubid=2, mode='ExclusiveLock', granted=true.
--      Marker 'GSUP9' rolls the probe back and is caught below; any other
--      error propagates and fails the migration.
-- ---------------------------------------------------------------------------

DO $gseer_0016_probe$
DECLARE
    v_locks integer;
BEGIN
    -- (a) gate refuses direct DML
    BEGIN
        INSERT INTO public.suppressions (kind, value, reason)
        VALUES ('ip', '192.0.2.234', '0016 gate probe — must be refused');
        -- Reaching this point means the gate did NOT fire.
        RAISE EXCEPTION '0016 probe: policy gate failed to refuse direct DML'
            USING ERRCODE = 'GSUP9';
    EXCEPTION
        WHEN SQLSTATE 'GSUP2' THEN
            NULL;  -- expected
    END;

    -- (b) policy-context DML proceeds and takes the advisory lock
    PERFORM set_config('gseer.policy_dml', 'on', TRUE);
    INSERT INTO public.suppressions (kind, value, reason)
    VALUES ('ip', '192.0.2.233', '0016 lock probe — rolled back');

    SELECT count(*) INTO v_locks
      FROM pg_locks
     WHERE locktype = 'advisory'
       AND classid  = 1196639570
       AND objid    = 1347374129
       AND objsubid = 2
       AND mode     = 'ExclusiveLock'
       AND granted;

    IF v_locks <> 1 THEN
        RAISE EXCEPTION '0016 probe: advisory lock not visible in pg_locks (found % rows)', v_locks
            USING ERRCODE = 'GSUP9';
    END IF;

    RAISE EXCEPTION '0016 probe: rollback marker'
        USING ERRCODE = 'GSUP9';
EXCEPTION
    WHEN SQLSTATE 'GSUP9' THEN
        NULL;  -- expected: the probe subtransaction rolled back
END
$gseer_0016_probe$;
