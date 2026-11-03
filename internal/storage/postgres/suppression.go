// GSEER — Internet Asset Intelligence & Search Engine.
// Copyright (C) 2026  The GSEER Authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package postgres

// suppression.go — the store-side suppression enforcement (M13).
//
// Three responsibilities:
//
//  1. The GATE — every ingestion path (IngestFact, IngestFacts,
//     UpsertAsset, UpsertService) compiles the rule set into a
//     suppress.Matcher and refuses matching records BEFORE any write.
//     Suppressed facts are skipped cleanly (IngestStats.Suppressed) and
//     audited; suppressed single-record upserts return E_DB_SUPPRESSED
//     (the caller asked about one specific record — silence would lie).
//     The DB backstop trigger (migration 0005) covers any FUTURE path
//     that forgets this gate: fail-closed by construction, not convention.
//
//  2. The REGISTRY — AddSuppression / ListSuppressions /
//     RemoveSuppression maintain the suppressions table (canonical values
//     written via the suppress normalizers).
//
//  3. The RETRACTION — RetractSuppressed removes already-indexed records
//     when a rule is introduced (owner directive §3: "Handle existing
//     indexed records when suppression is introduced"), writing a
//     suppression_applications audit row per retracted asset BEFORE the
//     delete (the audit must survive the deletion it records).
//
// Fixture-purge isolation: retraction deletes by SUPPRESSION MATCH only —
// it never reads or writes provenance — while the QS-6 fixture purge
// deletes by (source, provenance='fixture') only.  The two operations are
// orthogonal by construction; neither can widen the other's scope.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

// RetractLimit bounds one retraction run.  A rule that matches more
// assets than this is a scope mistake (a /8, a TLD-wide suffix); the
// operator narrows the rule instead of the tool silently deleting
// millions of rows.
const RetractLimit = 100_000

// suppressionTriggerErrCode is the custom ERRCODE raised by the
// fn_check_asset_suppression backstop trigger (migration 0005).
const suppressionTriggerErrCode = "GSUP1"

// LoadSuppressions reads every rule row (enabled, disabled and expired —
// the Matcher applies the filters so a rule flip is visible in one place).
func (s *Store) LoadSuppressions(ctx context.Context) ([]suppress.Rule, error) {
	return loadSuppressions(ctx, s.pool)
}

func loadSuppressions(ctx context.Context, q querier) ([]suppress.Rule, error) {
	rows, err := q.Query(ctx, `
SELECT id, kind, value, reason, created_by, created_at, expires_at, enabled
FROM suppressions
ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rules []suppress.Rule
	for rows.Next() {
		var r suppress.Rule
		var expires *time.Time
		if err := rows.Scan(&r.ID, &r.Kind, &r.Value, &r.Reason, &r.CreatedBy, &r.CreatedAt, &expires, &r.Enabled); err != nil {
			return nil, err
		}
		r.ExpiresAt = expires
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

// SuppressInput is one new suppression rule.
type SuppressInput struct {
	Kind      suppress.Kind
	Value     string // raw (any case, trailing dot, non-punycode); normalized here
	Reason    string
	CreatedBy string
	ExpiresAt *time.Time
}

// AddSuppression inserts a rule (value canonicalized first) and returns
// it with its database id.  existed=true when an identical (kind, value)
// rule was already present — the existing row is returned unchanged
// (idempotent re-adds are not audit events).
func (s *Store) AddSuppression(ctx context.Context, in SuppressInput) (rule suppress.Rule, existed bool, err error) {
	if !suppress.ValidateKind(in.Kind) {
		return suppress.Rule{}, false, newDBError(CodeDBUsage, "invalid suppression kind").
			withWhy(fmt.Sprintf("kind %q is not one of ip|cidr|domain|domain_suffix|source", in.Kind)).
			withNext("pick one of: ip, cidr, domain, domain_suffix, source")
	}
	value, err := suppress.NormalizeValue(in.Kind, in.Value)
	if err != nil {
		return suppress.Rule{}, false, newDBError(CodeDBUsage, "invalid suppression value").
			withWhy(err.Error()).
			withNext("fix the value; the stored rule must be canonical")
	}
	row := s.pool.QueryRow(ctx, `
INSERT INTO suppressions (kind, value, reason, created_by, expires_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (kind, value) DO UPDATE SET kind = EXCLUDED.kind
RETURNING id, kind, value, reason, created_by, created_at, expires_at, enabled,
          (xmax <> 0) AS existed`,
		string(in.Kind), value, in.Reason, in.CreatedBy, in.ExpiresAt)
	var r suppress.Rule
	var expires *time.Time
	if err := row.Scan(&r.ID, &r.Kind, &r.Value, &r.Reason, &r.CreatedBy, &r.CreatedAt, &expires, &r.Enabled, &existed); err != nil {
		return suppress.Rule{}, false, err
	}
	r.ExpiresAt = expires
	return r, existed, nil
}

// ListSuppressions returns every rule (including disabled and expired —
// audit sees everything; the Matcher applies the filters).
func (s *Store) ListSuppressions(ctx context.Context) ([]suppress.Rule, error) {
	return s.LoadSuppressions(ctx)
}

// RemoveSuppression deletes rule id.  Returns false when the id does not
// exist.  NOTE: removing a rule does NOT resurrect previously retracted
// records — they were deleted with audit rows in suppression_applications.
func (s *Store) RemoveSuppression(ctx context.Context, id int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM suppressions WHERE id = $1`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ---------------------------------------------------------------------------
// The gate
// ---------------------------------------------------------------------------

// gate is a compiled matcher plus the per-call source-name cache.
type gate struct {
	m      *suppress.Matcher
	source string // data_sources.name of the fact being gated (resolved per fact)
}

// loadGate compiles the current rule set.  A rule that fails to compile
// is a hard error (fail-closed): ingestion pauses rather than bypassing.
func (s *Store) loadGate(ctx context.Context) (*gate, error) {
	rules, err := s.LoadSuppressions(ctx)
	if err != nil {
		return nil, newDBError(CodeDBUnreachable, "cannot load suppression rules").
			withWhy(err.Error()).
			withNext("check the database connection; ingestion refuses to run ungated")
	}
	m, err := suppress.New(rules, time.Now())
	if err != nil {
		return nil, newDBError(CodeDBUsage, "suppression rule set failed to compile").
			withWhy(err.Error()).
			withNext("fix or remove the offending rule: gseer suppress list")
	}
	return &gate{m: m}, nil
}

// ingestFactResult distinguishes "skipped by suppression" from "applied".
type ingestOutcome struct {
	res        FactResult
	suppressed bool
	rule       suppress.Rule
}

// gateFact resolves the fact's source name, applies source-then-asset
// rules and — when suppressed — writes the ingest-block audit row.
// The audit insert runs on the same querier the caller would have used
// (pool for single facts, tx for bulk), so a rolled-back bulk run also
// rolls back its audit rows: audit state stays consistent with data.
func (g *gate) gateFact(ctx context.Context, q querier, f *Fact) (ingestOutcome, error) {
	// Source exclusion first: one lookup covers the whole fact (the
	// matcher needs the source NAME, so resolve the id once).
	if f.Observation.SourceID != 0 {
		name, err := sourceName(ctx, q, f.Observation.SourceID)
		if err != nil {
			return ingestOutcome{}, err
		}
		g.source = name
		if rule := g.m.MatchesSource(name); rule != nil {
			if err := auditBlocked(ctx, q, *rule, string(f.Asset.Kind), f.Asset.Value, name); err != nil {
				return ingestOutcome{}, err
			}
			return ingestOutcome{suppressed: true, rule: *rule}, nil
		}
	}
	// Asset rules.
	rule, err := g.m.MatchesAsset(string(f.Asset.Kind), f.Asset.Value)
	if err != nil {
		// Un-normalizable values can never match a rule but also can
		// never pass asset validation; fail the fact with a usage error
		// instead of ingesting an unmatchable value.
		return ingestOutcome{}, newDBError(CodeDBUsage, "fact value failed suppression normalization").
			withWhy(err.Error()).
			withNext("the collector must deliver canonical values; see internal/suppress.NormalizeValue")
	}
	if rule != nil {
		if err := auditBlocked(ctx, q, *rule, string(f.Asset.Kind), f.Asset.Value, g.source); err != nil {
			return ingestOutcome{}, err
		}
		return ingestOutcome{suppressed: true, rule: *rule}, nil
	}
	return ingestOutcome{}, nil
}

// gateAsset applies the asset rules to a single-record upsert (the
// UpsertAsset/UpsertService path).  Unlike facts, a suppressed single
// record is an ERROR: the caller asked about one specific row.
func (s *Store) gateAsset(ctx context.Context, q querier, kind storage.AssetKind, value string) error {
	rules, err := loadSuppressions(ctx, q)
	if err != nil {
		return newDBError(CodeDBUnreachable, "cannot load suppression rules").
			withWhy(err.Error()).
			withNext("check the database connection; writes refuse to run ungated")
	}
	m, err := suppress.New(rules, time.Now())
	if err != nil {
		return newDBError(CodeDBUsage, "suppression rule set failed to compile").
			withWhy(err.Error()).
			withNext("fix or remove the offending rule: gseer suppress list")
	}
	rule, err := m.MatchesAsset(string(kind), value)
	if err != nil {
		return newDBError(CodeDBUsage, "asset value failed suppression normalization").
			withWhy(err.Error()).
			withNext("canonicalize the value (internal/suppress.NormalizeValue) before writing")
	}
	if rule != nil {
		return newDBError(CodeDBSuppressed, fmt.Sprintf("asset matches suppression rule #%d (%s:%s)", rule.ID, rule.Kind, rule.Value)).
			withWhy(rule.Reason).
			withNext("remove or narrow the rule first: gseer suppress remove " + fmt.Sprint(rule.ID))
	}
	return nil
}

// sourceName resolves a data_sources.id to its name.
func sourceName(ctx context.Context, q querier, sourceID int64) (string, error) {
	var name string
	err := q.QueryRow(ctx, `SELECT name FROM data_sources WHERE id = $1`, sourceID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", newDBError(CodeDBUsage, "fact references an unregistered data source").
			withWhy(fmt.Sprintf("data_sources.id=%d does not exist", sourceID)).
			withNext("register the source first (the fixture loader uses EnsureFixtureSource)")
	}
	return name, err
}

// auditBlocked writes one ingest-block audit row.  Rule identity is
// denormalized into detail so the row stays self-describing even after
// the rule is removed (suppression_id goes NULL on rule deletion).
func auditBlocked(ctx context.Context, q querier, rule suppress.Rule, assetKind, assetValue, sourceName string) error {
	_, err := q.Exec(ctx, `
INSERT INTO suppression_applications (suppression_id, action, detail)
VALUES ($1, 'ingest-blocked', $2::jsonb)`,
		rule.ID, fmt.Sprintf(`{"rule_kind":%q,"rule_value":%q,"asset_kind":%q,"asset_value":%q,"source":%q}`,
			rule.Kind, rule.Value, assetKind, assetValue, sourceName))
	return err
}

// mapSuppressionTrigger maps the DB backstop's GSUP1 SQLSTATE to
// E_DB_SUPPRESSED so a bypass attempt reads as the policy violation it is.
func mapSuppressionTrigger(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == suppressionTriggerErrCode {
		return newDBError(CodeDBSuppressed, "write refused by the database-level suppression backstop").
			withWhy(pgErr.Message).
			withNext("the Go gate missed this record — remove the value or widen the gate; this error also fires for raw-SQL writes, by design")
	}
	return err
}

// ---------------------------------------------------------------------------
// Retroactive retraction (owner directive §3)
// ---------------------------------------------------------------------------

// RetractStats reports one RetractSuppressed run.
type RetractStats struct {
	AssetsRetracted     int
	ServicesRemoved     int
	ObservationsRemoved int
	AuditRows           int
}

// RetractSuppressed removes the already-indexed records matched by rule
// and writes one retroactive-retract audit row per retracted asset (plus
// one summary row for source retractions).  The whole retraction is ONE
// transaction: either the matching records leave the index together with
// their audit rows, or nothing changes.
//
// Scope by rule kind:
//   - ip/cidr: kind='ip' assets by exact / containment match
//   - domain:  kind='domain'|'hostname' assets by exact name
//   - domain_suffix: those kinds by strict-label suffix
//   - source:  every observation of that source, then orphaned services,
//     then the source name stripped from assets.sources[], then assets
//     left with no observation, no service and no source name
//
// A rule matching more than RetractLimit assets is refused (E_DB_USAGE):
// narrowing a runaway rule is an operator decision, never a silent bulk
// delete.  Retraction deletes by MATCH only — provenance is never read —
// so the fixture purge (provenance='fixture'-scoped) and this path stay
// orthogonal (fixture-purge isolation directive).
func (s *Store) RetractSuppressed(ctx context.Context, rule suppress.Rule) (RetractStats, error) {
	canonical, err := suppress.NormalizeValue(rule.Kind, rule.Value)
	if err != nil {
		return RetractStats{}, newDBError(CodeDBUsage, "retraction rule value invalid").
			withWhy(err.Error()).
			withNext("re-check the rule: gseer suppress list")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RetractStats{}, newDBError(CodeDBUnreachable, "cannot begin the retraction transaction").
			withWhy(err.Error()).
			withNext("check the database connection and retry")
	}
	stats, err := retractSuppressedCore(ctx, tx, rule, canonical)
	if err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			err = errors.Join(err, rbErr)
		}
		return RetractStats{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			err = errors.Join(err, rbErr)
		}
		return RetractStats{}, err
	}
	return stats, nil
}

func retractSuppressedCore(ctx context.Context, tx pgx.Tx, rule suppress.Rule, canonical string) (RetractStats, error) {
	var stats RetractStats

	if rule.Kind == suppress.KindSource {
		return retractBySource(ctx, tx, rule, canonical)
	}

	// Stage the candidate assets first so the audit rows are written from
	// the same snapshot that gets deleted.  $1 = the rule value, $2 = the
	// retraction bound (DISTINCT parameters: one holds a cidr/domain, the
	// other a bigint — a shared placeholder would mis-type the LIMIT).
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE tmp_retract_assets ON COMMIT DROP AS
SELECT id, kind, value, sources FROM assets
WHERE `+assetMatchSQL(rule.Kind)+` LIMIT $2`, canonical, int64(RetractLimit+1)); err != nil {
		return RetractStats{}, err
	}
	var candidates int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tmp_retract_assets`).Scan(&candidates); err != nil {
		return RetractStats{}, err
	}
	if candidates > RetractLimit {
		return RetractStats{}, newDBError(CodeDBUsage, "suppression rule matches more assets than one retraction may delete").
			withWhy(fmt.Sprintf("rule (%s:%s) matches > %d assets", rule.Kind, canonical, RetractLimit)).
			withNext("narrow the rule (smaller CIDR, longer suffix) before retracting")
	}

	// Count what the cascade will remove, for the audit rows + report.
	if err := tx.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM services s   JOIN tmp_retract_assets t ON s.asset_id = t.id),
  (SELECT count(*) FROM observations o JOIN tmp_retract_assets t ON o.asset_id = t.id)`).
		Scan(&stats.ServicesRemoved, &stats.ObservationsRemoved); err != nil {
		return RetractStats{}, err
	}

	// Audit BEFORE the delete: the applications row survives the asset
	// (its asset_id column is a plain BIGINT by design) and carries the
	// full rule identity.
	if _, err := tx.Exec(ctx, `
INSERT INTO suppression_applications (suppression_id, action, asset_id, detail)
SELECT $1, 'retroactive-retract', t.id,
       jsonb_build_object('rule_kind', $2::text, 'rule_value', $3::text,
                          'asset_kind', t.kind, 'asset_value', t.value, 'sources', to_jsonb(t.sources))
FROM tmp_retract_assets t`, rule.ID, string(rule.Kind), canonical); err != nil {
		return RetractStats{}, err
	}

	// The delete: services + observations go via ON DELETE CASCADE.
	if _, err := tx.Exec(ctx, `
WITH del AS (
  DELETE FROM assets WHERE id IN (SELECT id FROM tmp_retract_assets) RETURNING 1
) SELECT count(*) FROM del`); err != nil {
		return RetractStats{}, mapSuppressionTrigger(err)
	}

	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tmp_retract_assets`).Scan(&stats.AssetsRetracted); err != nil {
		return RetractStats{}, err
	}
	stats.AuditRows = stats.AssetsRetracted
	return stats, nil
}

// assetMatchSQL returns the WHERE fragment matching assets for a rule
// kind ($1 = canonical rule value).  Semantics mirror the Go matcher and
// the DB backstop trigger byte-for-byte.
func assetMatchSQL(kind suppress.Kind) string {
	switch kind {
	case suppress.KindIP:
		// Binary comparison (ip = $1::inet), never a text rendering: the
		// inet textual form of exotic families must not decide matching.
		return `kind = 'ip' AND ip = $1::inet`
	case suppress.KindCIDR:
		return `kind = 'ip' AND ip::cidr <<= $1::cidr`
	case suppress.KindDomain:
		return `(kind = 'domain' AND domain = $1) OR (kind = 'hostname' AND hostname = $1)`
	case suppress.KindDomainSuffix:
		return `(kind = 'domain' AND (domain = $1 OR right(domain, length($1) + 1) = '.' || $1))
             OR (kind = 'hostname' AND (hostname = $1 OR right(hostname, length($1) + 1) = '.' || $1))`
	default:
		// Callers route source rules to retractBySource; unreachable.
		return `false`
	}
}

// retractBySource implements the source-exclusion retraction.
func retractBySource(ctx context.Context, tx pgx.Tx, rule suppress.Rule, name string) (RetractStats, error) {
	var stats RetractStats
	var sourceID int64
	err := tx.QueryRow(ctx, `SELECT id FROM data_sources WHERE name = $1`, name).Scan(&sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing delivered under that name (yet): the rule still guards
		// future ingestion.  Record the (empty) application for audit.
		_, err = tx.Exec(ctx, `
INSERT INTO suppression_applications (suppression_id, action, detail)
VALUES ($1, 'retract-source', $2::jsonb)`, rule.ID, fmt.Sprintf(`{"rule_kind":"source","rule_value":%q,"source":%q,"note":"source not registered; nothing to retract"}`, name, name))
		if err != nil {
			return RetractStats{}, err
		}
		return RetractStats{AuditRows: 1}, nil
	}
	if err != nil {
		return RetractStats{}, err
	}

	// 1. Delete the source's observations.
	if err := tx.QueryRow(ctx, `WITH del AS (
  DELETE FROM observations WHERE source_id = $1 RETURNING 1
) SELECT count(*) FROM del`, sourceID).Scan(&stats.ObservationsRemoved); err != nil {
		return RetractStats{}, err
	}

	// 2. Services orphaned by (1).  NOTE the DOUBLE DELETE GUARD: this
	// deletes ANY observation-less service, which is exactly the M4
	// purge's orphan rule — services cannot exist unobserved.
	if err := tx.QueryRow(ctx, `WITH del AS (
  DELETE FROM services s
  WHERE NOT EXISTS (SELECT 1 FROM observations o WHERE o.service_id = s.id)
  RETURNING 1
) SELECT count(*) FROM del`).Scan(&stats.ServicesRemoved); err != nil {
		return RetractStats{}, err
	}

	// 3. Strip the source name from the surviving assets' sources[].
	if _, err := tx.Exec(ctx, `UPDATE assets SET sources = array_remove(sources, $1) WHERE $1 = ANY(sources)`, name); err != nil {
		return RetractStats{}, err
	}

	// 4. Assets left with no observation, no service and no source name
	// are index-invisible rows: delete them (cascade removes any
	// remaining children).
	if err := tx.QueryRow(ctx, `WITH del AS (
  DELETE FROM assets a
  WHERE NOT EXISTS (SELECT 1 FROM observations o WHERE o.asset_id = a.id)
    AND NOT EXISTS (SELECT 1 FROM services s   WHERE s.asset_id = a.id)
    AND cardinality(a.sources) = 0
  RETURNING 1
) SELECT count(*) FROM del`).Scan(&stats.AssetsRetracted); err != nil {
		return RetractStats{}, err
	}

	// 5. One summary audit row (per-asset rows would duplicate the
	// information already in the purge steps above).
	detail := fmt.Sprintf(`{"rule_kind":"source","rule_value":%q,"source":%q,"observations":%d,"services":%d,"assets":%d}`,
		name, name, stats.ObservationsRemoved, stats.ServicesRemoved, stats.AssetsRetracted)
	if _, err := tx.Exec(ctx, `
INSERT INTO suppression_applications (suppression_id, action, detail)
VALUES ($1, 'retract-source', $2::jsonb)`, rule.ID, detail); err != nil {
		return RetractStats{}, err
	}
	stats.AuditRows = 1
	return stats, nil
}

// NOTE: suppression never special-cases the fixture source — an operator
// rule CAN suppress the labeled synthetic corpus, and that is the
// operator's call (the fixture source is just data_sources.name="fixture"
// to the gate).
