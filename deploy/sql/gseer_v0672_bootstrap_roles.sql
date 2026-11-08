-- GSEER v0.2.0 deployment bootstrap — reference role setup (Rev 6.7.2 §6).
--
-- Purpose: create the two v0.2.0 database roles and the D-47 membership
-- BEFORE migration 0016 runs (the 0016 refusal guard verifies them).
-- Applied by the deployment procedure with the database-owner credential
-- (the v0.1.0 GSEER_DB_USER).  Idempotent; NEVER alters an existing role
-- (pre-existing roles are kept as-is and only VALIDATED — unsafe
-- attributes fail loudly instead of being silently kept or "fixed").
--
-- Roles (minimum attribute set; PostgreSQL defaults supply NOSUPERUSER,
-- NOCREATEDB, NOCREATEROLE, INHERIT, NOBYPASSRLS — stated explicitly so
-- the created state is self-describing and matches the 0016 guard):
--   gseer_policy_owner  NOLOGIN — owner of the gseer_policy schema and its
--                       functions; holds the suppression DML grants that
--                       the SECURITY DEFINER policy functions rely on.
--   gseer_app           LOGIN   — the v0.2.0 runtime credential.  The
--                       deployment injects its password via the secret
--                       channel of the deployment procedure (this file
--                       never contains or generates credentials).
--   D-47: the executing (migration/operator) role is made a member of
--   gseer_policy_owner with SET enabled, so migrations can create/alter
--   owner-side objects and the down migration can SET ROLE to drop them.
--
-- PostgreSQL floor: 16 (WITH SET/INHERIT membership options).
-- Transaction contract: safe to run standalone (psql) or as one batch;
-- contains no transaction-control statements.

DO $gseer_bootstrap$
DECLARE
    v_failures text[] := '{}';
BEGIN
    -- ------------------------------------------------------------------
    -- gseer_policy_owner — create or validate.
    -- ------------------------------------------------------------------
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gseer_policy_owner') THEN
        CREATE ROLE gseer_policy_owner WITH
            NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOBYPASSRLS;
        RAISE NOTICE 'bootstrap: created role gseer_policy_owner (NOLOGIN)';
    ELSE
        IF (SELECT rolsuper      FROM pg_roles WHERE rolname = 'gseer_policy_owner')
           OR (SELECT rolcreatedb   FROM pg_roles WHERE rolname = 'gseer_policy_owner')
           OR (SELECT rolcreaterole FROM pg_roles WHERE rolname = 'gseer_policy_owner')
           OR (SELECT rolbypassrls  FROM pg_roles WHERE rolname = 'gseer_policy_owner')
           OR (SELECT rolcanlogin   FROM pg_roles WHERE rolname = 'gseer_policy_owner') THEN
            v_failures := v_failures ||
                'existing role gseer_policy_owner has an unsafe attribute set (must be NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS); remediate manually before bootstrap'::text;
        END IF;
    END IF;

    -- ------------------------------------------------------------------
    -- gseer_app — create or validate.
    -- ------------------------------------------------------------------
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gseer_app') THEN
        CREATE ROLE gseer_app WITH
            LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOBYPASSRLS;
        RAISE NOTICE 'bootstrap: created role gseer_app (LOGIN; password injected by the deployment procedure)';
    ELSE
        IF (SELECT rolsuper      FROM pg_roles WHERE rolname = 'gseer_app')
           OR (SELECT rolcreatedb   FROM pg_roles WHERE rolname = 'gseer_app')
           OR (SELECT rolcreaterole FROM pg_roles WHERE rolname = 'gseer_app')
           OR (SELECT rolbypassrls  FROM pg_roles WHERE rolname = 'gseer_app')
           OR NOT (SELECT rolcanlogin FROM pg_roles WHERE rolname = 'gseer_app') THEN
            v_failures := v_failures ||
                'existing role gseer_app has an unsafe attribute set (must be LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS); remediate manually before bootstrap'::text;
        END IF;
    END IF;

    -- ------------------------------------------------------------------
    -- D-47 membership: executing role ← member of gseer_policy_owner,
    -- SET enabled (PG16+ options), INHERIT enabled.  Membership existence
    -- is checked in pg_auth_members directly (pg_has_role would pass
    -- trivially for a superuser credential without the membership really
    -- existing — the membership must exist so non-superuser migrations
    -- and the down path work).
    -- ------------------------------------------------------------------
    IF NOT EXISTS (
        SELECT 1 FROM pg_auth_members am
        JOIN pg_roles granted ON granted.oid = am.roleid
        JOIN pg_roles member  ON member.oid = am.member
        WHERE granted.rolname = 'gseer_policy_owner'
          AND member.rolname  = current_user
    ) THEN
        GRANT gseer_policy_owner TO CURRENT_USER WITH SET TRUE, INHERIT TRUE;
        RAISE NOTICE 'bootstrap: granted gseer_policy_owner membership to % (D-47, SET TRUE INHERIT TRUE)', current_user;
    ELSE
        IF NOT pg_has_role(current_user, 'gseer_policy_owner', 'SET') THEN
            v_failures := v_failures || format(
                '% is a member of gseer_policy_owner but cannot SET ROLE to it; grant the membership WITH SET TRUE (D-47)', current_user);
        END IF;
    END IF;

    IF array_length(v_failures, 1) > 0 THEN
        RAISE EXCEPTION 'bootstrap validation failed: %', array_to_string(v_failures, ' | ')
            USING ERRCODE = 'GSUPF';
    END IF;
END
$gseer_bootstrap$;
