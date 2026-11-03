-- GSEER migration 0005_suppressions.up.sql — the centralized suppression
-- policy layer (v0.0.2 Passive Intelligence, milestone M13).
--
-- Scope (owner directive §3):
--   suppressions               — the policy rules (exact IP, CIDR, exact
--                                domain, domain suffix with strict label
--                                boundaries, source exclusion, expiration,
--                                reason + audit metadata)
--   suppression_applications   — the audit surface (every ingest-block and
--                                retroactive retraction is recorded here)
--   fn_check_asset_suppression + trg_* — the DATABASE-level backstop that
--                                makes suppression bypass-proof: ANY write
--                                to assets (today's store, tomorrow's
--                                collectors, a psql session) is rejected
--                                while a matching enabled rule exists.
--
-- Enforcement model (two layers, fail-closed):
--   1) Go gate  — internal/suppress Matcher checked in the store's
--      ingestion paths (IngestFact / IngestFacts / UpsertAsset /
--      UpsertService) BEFORE any write: suppressed facts are skipped
--      cleanly, counted in IngestStats.Suppressed, and audited.
--   2) DB backstop — this trigger.  If any future ingestion path forgets
--      the Go gate, PostgreSQL itself refuses the row (custom ERRCODE
--      GSUP1; the store maps it to DB_SUPPRESSED).  Fail-closed by
--      construction, not by convention.
--
-- Semantics (documented in internal/suppress/doc.go and docs/SOURCES.md):
--   * values are stored CANONICAL: lowercase, no trailing dot, IDNA
--     punycode (A-labels), IPs via netip canonical form, CIDRs masked;
--     writers normalize before INSERT (the Go layer owns normalization).
--   * domain_suffix matches the exact value OR any descendant where the
--     remaining prefix ends at a LABEL boundary ('.value' suffix test),
--     so 'notexample.com' is NOT matched by suffix 'example.com'.
--   * IP rules match kind='ip' assets only; domain/suffix rules match
--     kind='domain' and kind='hostname' assets.  Cross-type propagation
--     (a domain rule also blocking its resolved IPs) is deliberately NOT
--     automatic — it is a policy decision, not a syntactic one.
--   * expired rules (expires_at <= now()) stop matching immediately but
--     stay listed for audit; they are never auto-deleted.
--
-- Fixture-purge isolation (owner directive §3): this migration does not
-- touch observations or the purge path; suppression retraction deletes
-- assets/services/observations by SUPPRESSION MATCH, never by provenance,
-- so the QS-6 fixture purge (provenance='fixture' scoped) and suppression
-- retraction remain orthogonal operations.
--
-- Transaction-safe like every GSEER migration (no CONCURRENTLY, no
-- VACUUM, no CREATE DATABASE): the migrate driver wraps this file in a
-- single atomic transaction (MultiStatementMode=false).

-- ---------------------------------------------------------------------------
-- suppressions — the policy rules.
-- ---------------------------------------------------------------------------
CREATE TABLE suppressions (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Rule kind: 'ip' (exact address), 'cidr' (network range),
    -- 'domain' (exact name), 'domain_suffix' (name + strict-label
    -- descendants), 'source' (data_sources.name exclusion).
    kind        TEXT        NOT NULL CHECK (kind IN
                            ('ip', 'cidr', 'domain', 'domain_suffix', 'source')),
    -- Canonical rule value: netip-canonical IP; masked CIDR; lowercase,
    -- trailing-dot-free, punycode domain; data source name.
    value       TEXT        NOT NULL,
    -- Why the rule exists (operator-supplied, human-readable, mandatory
    -- in the CLI; audit metadata per owner directive §3).
    reason      TEXT        NOT NULL DEFAULT '',
    -- Who added the rule (identity string, e.g. "cli:alice").
    created_by  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- NULL = never expires.  Expired rules stop matching (both gate
    -- layers re-evaluate per statement) but remain listed for audit.
    expires_at  TIMESTAMPTZ,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    CONSTRAINT suppressions_kind_value_unique UNIQUE (kind, value)
);

-- The trigger evaluates rules per asset write; the table is
-- operator-authored (tens of rows) but keep point lookups indexed so the
-- backstop stays cheap even with larger operator rule sets.
CREATE INDEX ix_suppressions_ip     ON suppressions (value) WHERE enabled AND kind = 'ip';
CREATE INDEX ix_suppressions_cidr   ON suppressions (value) WHERE enabled AND kind = 'cidr';
CREATE INDEX ix_suppressions_domain ON suppressions (value) WHERE enabled AND kind IN ('domain', 'domain_suffix');

-- ---------------------------------------------------------------------------
-- suppression_applications — the audit surface.  One row per enforced
-- application: an ingest-block (a fact refused before any write) or a
-- retroactive retraction (existing indexed records removed when the rule
-- was introduced).
--
-- asset_id is deliberately a PLAIN BIGINT, not a foreign key: the audit
-- log must survive the very deletion it records (a retraction deletes the
-- asset row; ON DELETE CASCADE/SET NULL would either erase or dangle the
-- evidence).  The detail JSONB carries the full identity of what was
-- blocked or retracted (kind, value, source, counts) at application time.
--
-- suppression_id is a soft reference (nullable, ON DELETE SET NULL): the
-- audit history of a rule must survive the rule's REMOVAL too — deleting
-- a policy never rewrites what that policy did.  Rule identity is also
-- denormalized into detail at write time, so every row is self-describing
-- even after the rule row is gone.
-- ---------------------------------------------------------------------------
CREATE TABLE suppression_applications (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- 'ingest-blocked'      — a fact refused by the Go gate before any write
    -- 'retroactive-retract' — existing indexed records removed for this rule
    -- 'retract-source'      — observations of an excluded source removed
    action          TEXT        NOT NULL CHECK (action IN
                                ('ingest-blocked', 'retroactive-retract', 'retract-source')),
    suppression_id  BIGINT,
    asset_id        BIGINT,
    detail          JSONB       NOT NULL DEFAULT '{}'::jsonb,
    applied_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT suppression_applications_rule_fk
        FOREIGN KEY (suppression_id) REFERENCES suppressions(id) ON DELETE SET NULL
);

CREATE INDEX ix_suppression_applications_rule ON suppression_applications (suppression_id, applied_at DESC);

-- ---------------------------------------------------------------------------
-- fn_check_asset_suppression — the database-level backstop.
--
-- Fires BEFORE INSERT OR UPDATE on assets.  Matching semantics mirror the
-- Go matcher EXACTLY (canonical values in, dot-boundary suffix test,
-- inet containment) so the two layers can never disagree about what is
-- suppressed.
--
-- Custom ERRCODE 'GSUP1' is mapped by the storage layer to DB_SUPPRESSED
-- so a bypass attempt surfaces as an actionable error, not a raw SQLSTATE.
-- ---------------------------------------------------------------------------
CREATE FUNCTION fn_check_asset_suppression() RETURNS trigger AS $$
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

CREATE TRIGGER trg_assets_suppression
    BEFORE INSERT OR UPDATE OF kind, value, ip, domain, hostname
    ON assets
    FOR EACH ROW EXECUTE FUNCTION fn_check_asset_suppression();
