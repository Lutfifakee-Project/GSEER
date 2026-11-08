-- GSEER v0.2.0 pre-runtime effective-privilege audit (Rev 6.7.2 §5).
--
-- PURPOSE — the fail-closed deployment gate: NO gseer_app runtime traffic
-- may be enabled unless this audit passes.  The deployment procedure runs
-- it AFTER migrations (including 0016) and BEFORE `gseer server start`;
-- a non-zero exit MUST stop the deployment (no application startup).
-- Read-only: the script mutates nothing.
--
-- What "effective" means here (§5): privileges are evaluated for
-- gseer_app AND for every role reachable from it through pg_auth_members
-- at any depth (the full membership closure).  On PostgreSQL 16+ a
-- membership edge with SET TRUE confers the granted role's privileges via
-- SET ROLE even when INHERIT is FALSE, and INHERIT TRUE confers them
-- implicitly — so the closure deliberately includes ALL edges regardless
-- of their options (conservative; false-positive-lean, never
-- false-negative).  has_table_privilege()/has_function_privilege()
-- additionally follow the "privileges of role" closure internally, so the
-- point checks and the closure walk double-cover each other.
--
-- Run as: the operator credential OR gseer_app itself (all catalogs
-- consulted are readable by non-superusers).  Exit: 0 = pass,
-- non-zero = at least one failure (psql: ON_ERROR_STOP=1).
--
-- PostgreSQL floor: 16.

DO $gseer_audit$
DECLARE
    v_failures  text[] := '{}';
    v_warnings  text[] := '{}';
    v_oid       oid;
    s           record;
    v_reachable text;
    v_setany    boolean;
    v_temp_direct boolean;
    v_temp_public boolean;
BEGIN
    -- ================================================================
    -- A. Role existence and attributes.
    -- ================================================================
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gseer_app') THEN
        v_failures := v_failures || 'role gseer_app does not exist'::text;
    ELSE
        IF (SELECT rolsuper      FROM pg_roles WHERE rolname = 'gseer_app') THEN
            v_failures := v_failures || 'gseer_app is SUPERUSER (forbidden)'::text;
        END IF;
        IF (SELECT rolcreaterole FROM pg_roles WHERE rolname = 'gseer_app') THEN
            v_failures := v_failures || 'gseer_app is CREATEROLE (forbidden: self-grant escalation on PG16)'::text;
        END IF;
        IF (SELECT rolcreatedb   FROM pg_roles WHERE rolname = 'gseer_app') THEN
            v_failures := v_failures || 'gseer_app is CREATEDB (forbidden)'::text;
        END IF;
        IF (SELECT rolbypassrls  FROM pg_roles WHERE rolname = 'gseer_app') THEN
            v_failures := v_failures || 'gseer_app is BYPASSRLS (forbidden)'::text;
        END IF;
        IF NOT (SELECT rolcanlogin FROM pg_roles WHERE rolname = 'gseer_app') THEN
            v_failures := v_failures || 'gseer_app is not LOGIN (runtime credential required)'::text;
        END IF;
        IF (SELECT rolinherit FROM pg_roles WHERE rolname = 'gseer_app') THEN
            RAISE NOTICE 'audit: gseer_app INHERIT=on (direct grants always apply; membership grants apply via inheritance)';
        ELSE
            v_warnings := v_warnings || 'gseer_app INHERIT=off (membership privileges apply only via SET ROLE)'::text;
        END IF;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gseer_policy_owner') THEN
        v_failures := v_failures || 'role gseer_policy_owner does not exist'::text;
    ELSE
        IF (SELECT rolsuper      FROM pg_roles WHERE rolname = 'gseer_policy_owner')
           OR (SELECT rolcreaterole FROM pg_roles WHERE rolname = 'gseer_policy_owner')
           OR (SELECT rolcreatedb   FROM pg_roles WHERE rolname = 'gseer_policy_owner')
           OR (SELECT rolbypassrls  FROM pg_roles WHERE rolname = 'gseer_policy_owner')
           OR (SELECT rolcanlogin   FROM pg_roles WHERE rolname = 'gseer_policy_owner') THEN
            v_failures := v_failures || 'gseer_policy_owner has an unsafe attribute (must be NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS)'::text;
        END IF;
    END IF;

    -- ================================================================
    -- B. Membership closure from gseer_app (all edges, any depth).
    --    PG16+ options are reported; superuser / bypassrls / createrole
    --    anywhere in the closure is a hard failure.
    -- ================================================================
    CREATE TEMP TABLE audit_reachable ON COMMIT DROP AS
    WITH RECURSIVE closure AS (
        SELECT proles.oid AS role_oid, 0 AS depth, NULL::oid AS via_oid,
               NULL::text AS via_set, NULL::text AS via_inherit
          FROM pg_roles proles WHERE proles.rolname = 'gseer_app'
        UNION
        SELECT am.roleid, c.depth + 1, c.role_oid,
               am.set_option::text, am.inherit_option::text
          FROM closure c
          JOIN pg_auth_members am ON am.member = c.role_oid
    )
    SELECT cl.role_oid,
           pg_get_userbyid(cl.role_oid) AS role_name,
           cl.depth, cl.via_oid, cl.via_set, cl.via_inherit
      FROM closure cl;

    FOR s IN
        SELECT ar.role_name, ar.depth, ar.via_set, ar.via_inherit,
               p.rolsuper, p.rolbypassrls, p.rolcreaterole, p.rolcreatedb
          FROM audit_reachable ar JOIN pg_roles p ON p.oid = ar.role_oid
    LOOP
        RAISE NOTICE 'audit: reachable role % (depth %, via SET=% INHERIT=%)',
            s.role_name, s.depth, coalesce(s.via_set,'(self)'), coalesce(s.via_inherit,'(self)');
        IF s.rolsuper THEN
            v_failures := v_failures || format('membership closure reaches SUPERUSER role %s', s.role_name);
        END IF;
        IF s.rolbypassrls THEN
            v_failures := v_failures || format('membership closure reaches BYPASSRLS role %s', s.role_name);
        END IF;
        IF s.rolcreaterole AND s.depth > 0 THEN
            v_failures := v_failures || format('membership closure reaches CREATEROLE role %s', s.role_name);
        END IF;
    END LOOP;

    -- ================================================================
    -- C. Effective privileges on public.suppressions.
    -- ================================================================
    IF to_regclass('public.suppressions') IS NULL THEN
        v_failures := v_failures || 'public.suppressions is missing'::text;
    ELSE
        SELECT relowner INTO v_oid FROM pg_class
         WHERE oid = 'public.suppressions'::regclass;

        -- Owner path: no reachable role may own the table (ownership plus
        -- SET ROLE would confer full DML).
        FOR s IN
            SELECT ar.role_name FROM audit_reachable ar
             WHERE ar.role_oid = (SELECT relowner FROM pg_class WHERE oid = 'public.suppressions'::regclass)
        LOOP
            v_failures := v_failures || format('membership closure reaches the suppressions table owner (%s)', s.role_name);
        END LOOP;

        -- Effective ACL walk: every reachable role's direct ACL entries.
        FOR s IN
            SELECT ar.role_name AS role_name, a.privilege_type
              FROM audit_reachable ar
              JOIN pg_class c ON c.oid = 'public.suppressions'::regclass
              JOIN aclexplode(coalesce(c.relacl, acldefault('r', c.relowner))) a
                ON a.grantee = ar.role_oid
             WHERE a.privilege_type IN ('INSERT','UPDATE','DELETE','TRUNCATE')
        LOOP
            v_failures := v_failures || format(
                'role %s holds direct %s on public.suppressions (indirect DML path)', s.role_name, s.privilege_type);
        END LOOP;

        -- Point checks for the runtime role (closure-following).
        IF NOT has_table_privilege('gseer_app', 'public.suppressions', 'SELECT') THEN
            v_failures := v_failures || 'gseer_app lacks SELECT on public.suppressions (Go gate + backstop trigger read)'::text;
        END IF;
        FOR s IN SELECT unnest(ARRAY['INSERT','UPDATE','DELETE','TRUNCATE']) AS p LOOP
            IF has_table_privilege('gseer_app', 'public.suppressions', s.p) THEN
                v_failures := v_failures || format('gseer_app effectively holds %s on public.suppressions', s.p);
            END IF;
        END LOOP;
    END IF;

    -- gseer_app must own nothing in public (least-privilege invariant).
    FOR s IN
        SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'public' AND c.relowner = 'gseer_app'::regrole
    LOOP
        v_failures := v_failures || format('gseer_app owns object public.%s (runtime role must own nothing)', s.relname);
    END LOOP;

    -- ================================================================
    -- D. Policy-function execution surface.
    -- ================================================================
    IF to_regnamespace('gseer_policy') IS NULL THEN
        v_failures := v_failures || 'schema gseer_policy is missing (migration 0016 not applied?)'::text;
    ELSE
        FOR s IN SELECT unnest(ARRAY[
            'gseer_policy.suppression_insert(text,text,text,text,timestamptz,boolean)',
            'gseer_policy.suppression_update(bigint,text,timestamptz,boolean)',
            'gseer_policy.suppression_delete(bigint)']) AS f
        LOOP
            IF to_regprocedure(s.f) IS NULL THEN
                v_failures := v_failures || format('policy function %s is missing', s.f);
            ELSIF NOT has_function_privilege('gseer_app', s.f::regprocedure, 'EXECUTE') THEN
                v_failures := v_failures || format('gseer_app lacks EXECUTE on %s', s.f);
            END IF;
        END LOOP;

        IF to_regprocedure('gseer_policy.fn_suppressions_policy_lock()') IS NOT NULL
           AND has_function_privilege('gseer_app', 'gseer_policy.fn_suppressions_policy_lock()'::regprocedure, 'EXECUTE') THEN
            v_failures := v_failures || 'gseer_app holds EXECUTE on fn_suppressions_policy_lock (trigger-only surface)'::text;
        END IF;

        -- PUBLIC must hold nothing on any gseer_policy function.
        FOR s IN
            SELECT p.proname, a.privilege_type
              FROM pg_proc p
              JOIN aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a ON TRUE
             WHERE p.pronamespace = 'gseer_policy'::regnamespace
               AND a.grantee = 0
        LOOP
            v_failures := v_failures || format('PUBLIC holds %s on gseer_policy.%s', s.privilege_type, s.proname);
        END LOOP;
    END IF;

    -- ================================================================
    -- E. Schema and database privileges.
    -- ================================================================
    IF to_regnamespace('gseer_policy') IS NOT NULL THEN
        IF NOT has_schema_privilege('gseer_app', 'gseer_policy', 'USAGE') THEN
            v_failures := v_failures || 'gseer_app lacks USAGE on gseer_policy'::text;
        END IF;
        IF has_schema_privilege('gseer_app', 'gseer_policy', 'CREATE') THEN
            v_failures := v_failures || 'gseer_app holds CREATE on gseer_policy (forbidden)'::text;
        END IF;
    END IF;
    IF NOT has_schema_privilege('gseer_app', 'public', 'USAGE') THEN
        v_failures := v_failures || 'gseer_app lacks USAGE on public'::text;
    END IF;
    IF NOT has_database_privilege('gseer_app', current_database(), 'CONNECT') THEN
        v_failures := v_failures || 'gseer_app lacks CONNECT on ' || current_database();
    END IF;

    -- §4 Choice A contract: TEMPORARY must be effectively available
    -- (retraction path: CREATE TEMP TABLE ... ON COMMIT DROP) and the
    -- source (direct grant vs PUBLIC inheritance) is reported.
    v_temp_direct := EXISTS (
        SELECT 1 FROM pg_database d, aclexplode(coalesce(d.datacl, acldefault('d', d.datdba))) a
         WHERE d.datname = current_database() AND a.grantee = 'gseer_app'::regrole
           AND a.privilege_type = 'TEMPORARY');
    v_temp_public := EXISTS (
        SELECT 1 FROM pg_database d, aclexplode(coalesce(d.datacl, acldefault('d', d.datdba))) a
         WHERE d.datname = current_database() AND a.grantee = 0
           AND a.privilege_type = 'TEMPORARY');
    IF NOT has_database_privilege('gseer_app', current_database(), 'TEMPORARY') THEN
        v_failures := v_failures || format(
            'gseer_app lacks effective TEMPORARY on %s (§4 Choice A: explicit grant required; retraction path creates temp tables)', current_database());
    ELSIF v_temp_direct THEN
        RAISE NOTICE 'audit: TEMPORARY on % is an explicit gseer_app grant (§4 Choice A satisfied)', current_database();
    ELSIF v_temp_public THEN
        v_warnings := v_warnings || format(
            'TEMPORARY on %s is only PUBLIC-inherited; §4 Choice A expects an explicit gseer_app grant', current_database());
    END IF;

    -- ================================================================
    -- F. Runtime table matrix (evidence-backed positive grants).
    -- ================================================================
    FOR s IN
        SELECT * FROM (VALUES
            ('assets',                   ARRAY['SELECT','INSERT','UPDATE','DELETE']),
            ('services',                 ARRAY['SELECT','INSERT','UPDATE','DELETE']),
            ('observations',             ARRAY['SELECT','INSERT','DELETE']),
            ('data_sources',             ARRAY['SELECT','INSERT','UPDATE']),
            ('suppression_applications', ARRAY['SELECT','INSERT']),
            ('collection_jobs',          ARRAY['SELECT','INSERT','UPDATE']),
            ('source_checkpoints',       ARRAY['SELECT','INSERT','UPDATE']),
            ('ingestion_dead_letter',    ARRAY['SELECT','INSERT','UPDATE'])
        ) AS t(tbl, privs)
    LOOP
        IF to_regclass(format('public.%s', s.tbl)) IS NULL THEN
            v_failures := v_failures || format('public.%s is missing', s.tbl);
        ELSE
            FOREACH v_reachable IN ARRAY s.privs LOOP
                IF NOT has_table_privilege('gseer_app', format('public.%s', s.tbl), v_reachable) THEN
                    v_failures := v_failures || format('gseer_app lacks %s on public.%s', v_reachable, s.tbl);
                END IF;
            END LOOP;
        END IF;
    END LOOP;

    FOR s IN SELECT unnest(ARRAY['latest_http','latest_tls','latest_fingerprints','search_index']) AS v LOOP
        IF to_regclass(format('public.%s', s.v)) IS NULL THEN
            v_failures := v_failures || format('view public.%s is missing', s.v);
        ELSIF NOT has_table_privilege('gseer_app', format('public.%s', s.v), 'SELECT') THEN
            v_failures := v_failures || format('gseer_app lacks SELECT on public.%s', s.v);
        END IF;
    END LOOP;

    -- ================================================================
    -- G. Sequence and ledger hygiene.
    -- ================================================================
    FOR s IN
        SELECT c.relname
          FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'public' AND c.relkind = 'S'
    LOOP
        IF has_sequence_privilege('gseer_app', format('public.%s', s.relname), 'USAGE')
           OR has_sequence_privilege('gseer_app', format('public.%s', s.relname), 'SELECT')
           OR has_sequence_privilege('gseer_app', format('public.%s', s.relname), 'UPDATE') THEN
            v_failures := v_failures || format('gseer_app holds a privilege on sequence public.%s (identity path needs none)', s.relname);
        END IF;
    END LOOP;

    IF to_regclass('public.schema_migrations') IS NOT NULL THEN
        FOR s IN SELECT unnest(ARRAY['SELECT','INSERT','UPDATE','DELETE']) AS p LOOP
            IF has_table_privilege('gseer_app', 'public.schema_migrations', s.p) THEN
                v_failures := v_failures || format('gseer_app holds %s on public.schema_migrations (driver-owned ledger)', s.p);
            END IF;
        END LOOP;
    END IF;

    -- ================================================================
    -- H. 0016 hardening markers (GLOBAL default-privilege entry with no
    --    PUBLIC privilege — see the up file, block [6], for the verified
    --    PostgreSQL 17.2 semantics).
    -- ================================================================
    IF to_regnamespace('gseer_policy') IS NOT NULL THEN
        IF NOT EXISTS (
            SELECT 1 FROM pg_default_acl d
             WHERE d.defaclrole = 'gseer_policy_owner'::regrole
               AND d.defaclnamespace = 0  -- 0 = global (unscoped) entry
               AND d.defaclobjtype = 'f'
               AND NOT EXISTS (
                   SELECT 1 FROM aclexplode(d.defaclacl) a WHERE a.grantee = 0)
        ) THEN
            v_failures := v_failures || 'default-privilege hardening for gseer_policy_owner functions is missing'::text;
        END IF;
    END IF;

    -- ================================================================
    -- Verdict.
    -- ================================================================
    FOREACH v_reachable IN ARRAY v_warnings LOOP
        RAISE NOTICE 'audit WARNING: %', v_reachable;
    END LOOP;

    IF array_length(v_failures, 1) > 0 THEN
        RAISE EXCEPTION 'EFFECTIVE-PRIVILEGE AUDIT FAILED with % failure(s): %',
            array_length(v_failures, 1), array_to_string(v_failures, ' | ')
            USING ERRCODE = 'GSUPF',
                  HINT    = 'Do NOT start the application; remediate the grants/roles and re-run this audit (fail-closed deployment gate, Rev 6.7.2 §5)';
    END IF;

    RAISE NOTICE 'EFFECTIVE-PRIVILEGE AUDIT PASSED: SELECT allowed, policy EXECUTE allowed, direct suppression DML denied, no indirect DML path, no owner/superuser path';
END
$gseer_audit$;
