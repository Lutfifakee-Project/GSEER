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

package sqlite

// suppression.go — SQ-06: the store-side suppression enforcement, a 1:1
// port of the postgres.Store's three responsibilities:
//
//  1. The GATE — every ingestion path (IngestFact, IngestFacts,
//     UpsertAsset) compiles the rule set into a suppress.Matcher and
//     refuses matching records BEFORE any write.  Suppressed facts are
//     skipped cleanly (IngestStats.Suppressed) and audited; suppressed
//     single-record upserts return an E_DB_SUPPRESSED refusal.  The
//     backstop TRIGGER (migration 0001) covers every future path —
//     fail-closed by construction, not convention.
//  2. The REGISTRY — AddSuppression / ListSuppressions /
//     RemoveSuppression maintain the suppressions table (canonical
//     values via the suppress normalizers).  Concurrency: rule
//     mutations run inside a transaction and the pool serializes on
//     ONE connection (SQ-05), so the existed-determination cannot race.
//  3. The RETRACTION — RetractSuppressed removes already-indexed
//     records when a rule is introduced, writing an audit row per
//     retracted asset BEFORE the delete.  The whole retraction is ONE
//     transaction; a rule matching more than RetractLimit assets is
//     refused (the operator narrows the rule instead of a silent bulk
//     delete).
//
// Fixture-purge isolation (unchanged): retraction deletes by
// SUPPRESSION MATCH only — it never reads provenance; the fixture
// purge deletes by (source, provenance='fixture') only.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

// RetractLimit bounds one retraction run (same value as the PG store).
const RetractLimit = 100_000

// LoadSuppressions reads every rule row (enabled, disabled and expired
// — the Matcher applies the filters so a rule flip is visible in one
// place).
func (s *Store) LoadSuppressions(ctx context.Context) ([]suppress.Rule, error) {
	return loadSuppressions(ctx, s.DB)
}

func loadSuppressions(ctx context.Context, q Querier) ([]suppress.Rule, error) {
	rows, err := q.QueryContext(ctx, `
SELECT id, kind, value, reason, created_by, created_at, expires_at, enabled
FROM suppressions ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: cannot load suppressions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var rules []suppress.Rule
	for rows.Next() {
		var (
			r        suppress.Rule
			enabled  int
			expires  sql.NullString
			createdS string
		)
		if err := rows.Scan(&r.ID, &r.Kind, &r.Value, &r.Reason, &r.CreatedBy, &createdS, &expires, &enabled); err != nil {
			return nil, err
		}
		if r.CreatedAt, err = ParseTS(createdS); err != nil {
			return nil, fmt.Errorf("sqlite: suppression %d created_at: %w", r.ID, err)
		}
		if expires.Valid && expires.String != "" {
			if e, perr := ParseTS(expires.String); perr == nil {
				r.ExpiresAt = &e
			}
		}
		r.Enabled = enabled != 0
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

// SuppressInput is one new suppression rule (same shape as PG).
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
func (s *Store) AddSuppression(ctx context.Context, in SuppressInput) (suppress.Rule, bool, error) {
	if !suppress.ValidateKind(in.Kind) {
		return suppress.Rule{}, false, fmt.Errorf("E_DB_USAGE: invalid suppression kind %q — pick one of: ip, cidr, domain, domain_suffix, source", in.Kind)
	}
	value, err := suppress.NormalizeValue(in.Kind, in.Value)
	if err != nil {
		return suppress.Rule{}, false, fmt.Errorf("E_DB_USAGE: invalid suppression value: %w", err)
	}
	var (
		r       suppress.Rule
		existed bool
	)
	err = withTx(ctx, s.DB, func(tx Querier) error {
		row, existing, serr := selectSuppressionByKey(ctx, tx, string(in.Kind), value)
		if serr != nil {
			return serr
		}
		if existing {
			r = row
			existed = true
			return nil
		}
		var expires any
		if in.ExpiresAt != nil {
			expires = FormatTS(*in.ExpiresAt)
		}
		_, ierr := tx.ExecContext(ctx, `INSERT INTO suppressions
                        (kind, value, reason, created_by, created_at, expires_at, enabled)
                        VALUES (?, ?, ?, ?, ?, ?, 1)`,
			string(in.Kind), value, in.Reason, in.CreatedBy, FormatTS(time.Now().UTC()), expires)
		if ierr != nil {
			return ierr
		}
		rule, _, gerr := selectSuppressionByKey(ctx, tx, string(in.Kind), value)
		if gerr != nil {
			return gerr
		}
		r = rule
		return nil
	})
	if err != nil {
		return suppress.Rule{}, false, err
	}
	return r, existed, nil
}

func selectSuppressionByKey(ctx context.Context, q Querier, kind, value string) (suppress.Rule, bool, error) {
	var (
		r        suppress.Rule
		enabled  int
		expires  sql.NullString
		createdS string
	)
	err := q.QueryRowContext(ctx, `
SELECT id, kind, value, reason, created_by, created_at, expires_at, enabled
FROM suppressions WHERE kind = ? AND value = ?`, kind, value).
		Scan(&r.ID, &r.Kind, &r.Value, &r.Reason, &r.CreatedBy, &createdS, &expires, &enabled)
	if isNoRows(err) {
		return suppress.Rule{}, false, nil
	}
	if err != nil {
		return suppress.Rule{}, false, err
	}
	if r.CreatedAt, err = ParseTS(createdS); err != nil {
		return suppress.Rule{}, false, err
	}
	if expires.Valid && expires.String != "" {
		if e, perr := ParseTS(expires.String); perr == nil {
			r.ExpiresAt = &e
		}
	}
	r.Enabled = enabled != 0
	return r, true, nil
}

// ListSuppressions returns every rule (same contract as LoadSuppressions;
// the CLI reads through this name).
func (s *Store) ListSuppressions(ctx context.Context) ([]suppress.Rule, error) {
	return s.LoadSuppressions(ctx)
}

// RemoveSuppression deletes a rule.  The audit history survives the
// rule's removal (suppression_id goes NULL via ON DELETE SET NULL).
func (s *Store) RemoveSuppression(ctx context.Context, id int64) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM suppressions WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("sqlite: cannot remove suppression %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ---------------------------------------------------------------------------
// The gate
// ---------------------------------------------------------------------------

// gate is a compiled matcher plus the per-call source-name cache.
type gate struct {
	m      *suppress.Matcher
	source string
}

// loadGate compiles the current rule set.  A rule that fails to compile
// is a hard error (fail-closed): ingestion pauses rather than bypassing.
func (s *Store) loadGate(ctx context.Context) (*gate, error) {
	rules, err := s.LoadSuppressions(ctx)
	if err != nil {
		return nil, fmt.Errorf("E_DB_UNREACHABLE: cannot load suppression rules: %w", err)
	}
	m, err := suppress.New(rules, time.Now())
	if err != nil {
		return nil, fmt.Errorf("E_DB_USAGE: suppression rule set failed to compile: %w — fix or remove the offending rule: gseer suppress list", err)
	}
	return &gate{m: m}, nil
}

// ingestOutcome distinguishes "skipped by suppression" from "applied".
type ingestOutcome struct {
	suppressed bool
	rule       suppress.Rule
}

// gateFact resolves the fact's source name, applies source-then-asset
// rules and — when suppressed — writes the ingest-block audit row on
// the same querier, so a rolled-back run also rolls back its audit rows.
func (g *gate) gateFact(ctx context.Context, q Querier, f *Fact) (ingestOutcome, error) {
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
	rule, err := g.m.MatchesAsset(string(f.Asset.Kind), f.Asset.Value)
	if err != nil {
		return ingestOutcome{}, fmt.Errorf("E_DB_USAGE: fact value failed suppression normalization: %w", err)
	}
	if rule != nil {
		if err := auditBlocked(ctx, q, *rule, string(f.Asset.Kind), f.Asset.Value, g.source); err != nil {
			return ingestOutcome{}, err
		}
		return ingestOutcome{suppressed: true, rule: *rule}, nil
	}
	return ingestOutcome{}, nil
}

// gateAssetWrite applies the asset rules to a single-record upsert —
// a suppressed single record is an ERROR (the caller asked about one
// specific row).
func (s *Store) gateAssetWrite(ctx context.Context, kind storage.AssetKind, value string) error {
	rules, err := loadSuppressions(ctx, s.DB)
	if err != nil {
		return fmt.Errorf("E_DB_UNREACHABLE: cannot load suppression rules: %w", err)
	}
	m, err := suppress.New(rules, time.Now())
	if err != nil {
		return fmt.Errorf("E_DB_USAGE: suppression rule set failed to compile: %w", err)
	}
	rule, err := m.MatchesAsset(string(kind), value)
	if err != nil {
		return fmt.Errorf("E_DB_USAGE: asset value failed suppression normalization: %w", err)
	}
	if rule != nil {
		return &suppressedError{msg: fmt.Sprintf(
			"E_DB_SUPPRESSED: asset matches suppression rule #%d (%s:%s) — remove or narrow the rule first: gseer suppress remove %d",
			rule.ID, rule.Kind, rule.Value, rule.ID)}
	}
	return nil
}

// sourceName resolves a data_sources.id to its name.
func sourceName(ctx context.Context, q Querier, sourceID int64) (string, error) {
	var name string
	err := q.QueryRowContext(ctx, `SELECT name FROM data_sources WHERE id = ?`, sourceID).Scan(&name)
	if isNoRows(err) {
		return "", fmt.Errorf("E_DB_USAGE: fact references an unregistered data source (id=%d) — register the source first", sourceID)
	}
	return name, err
}

// auditBlocked writes one ingest-block audit row.  Rule identity is
// denormalized into detail so the row stays self-describing even after
// the rule is removed.
func auditBlocked(ctx context.Context, q Querier, rule suppress.Rule, assetKind, assetValue, sourceName string) error {
	detail, err := json.Marshal(map[string]string{
		"rule_kind":   string(rule.Kind),
		"rule_value":  rule.Value,
		"asset_kind":  assetKind,
		"asset_value": assetValue,
		"source":      sourceName,
	})
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `
INSERT INTO suppression_applications (suppression_id, action, detail, applied_at)
VALUES (?, 'ingest-blocked', ?, ?)`,
		rule.ID, string(detail), FormatTS(time.Now().UTC()))
	return err
}

// ---------------------------------------------------------------------------
// Retroactive retraction (owner directive §3)
// ---------------------------------------------------------------------------

// RetractSuppressed removes the already-indexed records matched by rule
// and writes one retroactive-retract audit row per retracted asset (plus
// one summary row for source retractions).  The whole retraction is ONE
// transaction.
//
// Scope by rule kind:
//   - ip/cidr: kind='ip' assets by exact / containment match (netip in
//     Go — the personal ip column stores canonical text, so containment
//     is decided on parsed values, never on text renderings);
//   - domain: kind='domain'|'hostname' assets by exact name;
//   - domain_suffix: those kinds by strict-label suffix;
//   - source: every observation of that source, then orphaned services,
//     then the source name stripped from assets.sources[], then assets
//     left with no observation, no service and no source name.
func (s *Store) RetractSuppressed(ctx context.Context, rule suppress.Rule) (RetractStats, error) {
	canonical, err := suppress.NormalizeValue(rule.Kind, rule.Value)
	if err != nil {
		return RetractStats{}, fmt.Errorf("E_DB_USAGE: retraction rule value invalid: %w", err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RetractStats{}, fmt.Errorf("E_DB_UNREACHABLE: cannot begin the retraction transaction: %w", err)
	}
	stats, err := s.retractSuppressedCore(ctx, tx, rule, canonical)
	if err != nil {
		_ = tx.Rollback()
		return RetractStats{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return RetractStats{}, fmt.Errorf("sqlite: cannot commit the retraction: %w", err)
	}
	return stats, nil
}

func (s *Store) retractSuppressedCore(ctx context.Context, tx Querier, rule suppress.Rule, canonical string) (RetractStats, error) {
	var stats RetractStats

	if rule.Kind == suppress.KindSource {
		return s.retractBySource(ctx, tx, rule, canonical)
	}

	// Candidate assets are resolved IN GO (netip containment for cidr),
	// then audited and deleted inside the same transaction.
	ids, kinds, values, sourcesRaw, err := retractCandidates(ctx, tx, rule.Kind, canonical)
	if err != nil {
		return stats, err
	}
	if len(ids) > RetractLimit {
		return stats, fmt.Errorf("E_DB_USAGE: suppression rule matches more assets than one retraction may delete (rule (%s:%s) > %d assets) — narrow the rule (smaller CIDR, longer suffix) before retracting",
			rule.Kind, canonical, RetractLimit)
	}
	if len(ids) == 0 {
		return stats, nil
	}

	// Count what the cascade will remove.
	for _, id := range ids {
		var svcN, obsN int
		if err := tx.QueryRowContext(ctx, `SELECT
                        (SELECT count(*) FROM services WHERE asset_id = ?),
                        (SELECT count(*) FROM observations WHERE asset_id = ?)`, id, id).Scan(&svcN, &obsN); err != nil {
			return stats, err
		}
		stats.ServicesRemoved += svcN
		stats.ObservationsRemoved += obsN
	}

	// Audit BEFORE the delete: the applications row survives the asset.
	for i, id := range ids {
		detail, derr := json.Marshal(map[string]any{
			"rule_kind":   string(rule.Kind),
			"rule_value":  canonical,
			"asset_kind":  kinds[i],
			"asset_value": values[i],
			"sources":     jsonSources(sourcesRaw[i]),
		})
		if derr != nil {
			return stats, derr
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO suppression_applications (suppression_id, action, asset_id, detail, applied_at)
VALUES (?, 'retroactive-retract', ?, ?, ?)`, rule.ID, id, string(detail), FormatTS(time.Now().UTC())); err != nil {
			return stats, err
		}
	}

	// The delete: services + observations go via ON DELETE CASCADE
	// (foreign_keys pragma ON on every connection).
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, id); err != nil {
			return stats, mapSuppressionTrigger(err)
		}
	}
	stats.AssetsRetracted = len(ids)
	stats.AuditRows = len(ids)
	return stats, nil
}

// retractCandidates resolves the asset ids matching the rule (the same
// match semantics as assetMatchSQL on the PG side, with netip for cidr).
func retractCandidates(ctx context.Context, q Querier, kind suppress.Kind, canonical string) (ids []int64, kinds []string, values []string, sourcesRaw []string, err error) {
	var rows *sql.Rows
	switch kind {
	case suppress.KindIP:
		rows, err = q.QueryContext(ctx, `SELECT id, kind, value, ip, sources FROM assets WHERE kind = 'ip' AND ip = ?`, canonical)
	case suppress.KindCIDR:
		prefix, perr := netip.ParsePrefix(canonical)
		if perr != nil {
			return nil, nil, nil, nil, fmt.Errorf("E_DB_USAGE: cidr rule value invalid: %w", perr)
		}
		prefix = prefix.Masked()
		rows, err = q.QueryContext(ctx, `SELECT id, kind, value, ip, sources FROM assets WHERE kind = 'ip' AND ip IS NOT NULL`)
		if err == nil {
			var (
				allIDs   []int64
				allKinds []string
				allVals  []string
				allSrc   []string
			)
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var id int64
				var k, v, ip, src string
				if err = rows.Scan(&id, &k, &v, &ip, &src); err != nil {
					return nil, nil, nil, nil, err
				}
				a, aerr := netip.ParseAddr(ip)
				if aerr == nil && prefix.Contains(a.Unmap()) {
					allIDs = append(allIDs, id)
					allKinds = append(allKinds, k)
					allVals = append(allVals, v)
					allSrc = append(allSrc, src)
				}
			}
			if err = rows.Err(); err != nil {
				return nil, nil, nil, nil, err
			}
			return allIDs, allKinds, allVals, allSrc, nil
		}
	case suppress.KindDomain:
		rows, err = q.QueryContext(ctx, `SELECT id, kind, value, domain, sources FROM assets
                        WHERE (kind = 'domain' AND domain = ?) OR (kind = 'hostname' AND hostname = ?)`, canonical, canonical)
	case suppress.KindDomainSuffix:
		rows, err = q.QueryContext(ctx, `SELECT id, kind, value, domain, sources FROM assets
                        WHERE (kind = 'domain' AND domain IS NOT NULL AND (domain = ? OR substr(domain, -(length(?) + 1)) = '.' || ?))
                           OR (kind = 'hostname' AND hostname IS NOT NULL AND (hostname = ? OR substr(hostname, -(length(?) + 1)) = '.' || ?))`,
			canonical, canonical, canonical, canonical, canonical, canonical)
	default:
		return nil, nil, nil, nil, fmt.Errorf("E_DB_USAGE: unsupported retraction kind %q", kind)
	}
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var k, v, ident, src string
		if err = rows.Scan(&id, &k, &v, &ident, &src); err != nil {
			return nil, nil, nil, nil, err
		}
		ids = append(ids, id)
		kinds = append(kinds, k)
		values = append(values, v)
		sourcesRaw = append(sourcesRaw, src)
	}
	err = rows.Err()
	return ids, kinds, values, sourcesRaw, err
}

// retractBySource implements the source-exclusion retraction (the same
// four steps + summary audit row as the PG path).
func (s *Store) retractBySource(ctx context.Context, tx Querier, rule suppress.Rule, name string) (RetractStats, error) {
	var stats RetractStats
	var sourceID int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM data_sources WHERE name = ?`, name).Scan(&sourceID)
	if isNoRows(err) {
		// Nothing delivered under that name (yet): the rule still guards
		// future ingestion.  Record the (empty) application for audit.
		detail, _ := json.Marshal(map[string]string{
			"rule_kind": "source", "rule_value": name, "source": name,
			"note": "source not registered; nothing to retract",
		})
		if _, err := tx.ExecContext(ctx, `
INSERT INTO suppression_applications (suppression_id, action, detail, applied_at)
VALUES (?, 'retract-source', ?, ?)`, rule.ID, string(detail), FormatTS(time.Now().UTC())); err != nil {
			return stats, err
		}
		return RetractStats{AuditRows: 1}, nil
	}
	if err != nil {
		return stats, err
	}

	// 1. Delete the source's observations (count first — no
	// data-modifying CTEs in SQLite).
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM observations WHERE source_id = ?`, sourceID).Scan(&stats.ObservationsRemoved); err != nil {
		return stats, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM observations WHERE source_id = ?`, sourceID); err != nil {
		return stats, err
	}

	// 2. Services orphaned by (1) — the same double-delete guard.
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM services WHERE NOT EXISTS (SELECT 1 FROM observations o WHERE o.service_id = services.id)`).Scan(&stats.ServicesRemoved); err != nil {
		return stats, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM services WHERE NOT EXISTS (SELECT 1 FROM observations o WHERE o.service_id = services.id)`); err != nil {
		return stats, err
	}

	// 3. Strip the source name from the surviving assets' sources[].
	// The scan runs in a scope that ALWAYS closes the rows (defer)
	// BEFORE the UPDATE loop below — the early release keeps the
	// long UPDATE pass from holding the read cursor open.
	rows, err := tx.QueryContext(ctx, `SELECT id, sources FROM assets WHERE sources LIKE ?`, "%\""+name+"\"%")
	if err != nil {
		return stats, err
	}
	type idSrc struct {
		id  int64
		src string
	}
	var hits []idSrc
	if scanErr := func() error {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var h idSrc
			if err := rows.Scan(&h.id, &h.src); err != nil {
				return err
			}
			hits = append(hits, h)
		}
		return rows.Err()
	}(); scanErr != nil {
		return stats, scanErr
	}
	for _, h := range hits {
		merged, _ := json.Marshal(removeString(jsonSources(h.src), name))
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET sources = ? WHERE id = ?`, string(merged), h.id); err != nil {
			return stats, err
		}
	}

	// 4. Assets left with no observation, no service and no source name
	// are index-invisible rows: delete them.
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM assets a WHERE
                  NOT EXISTS (SELECT 1 FROM observations o WHERE o.asset_id = a.id)
                  AND NOT EXISTS (SELECT 1 FROM services s WHERE s.asset_id = a.id)
                  AND (a.sources = '[]' OR json_array_length(a.sources) = 0)`).Scan(&stats.AssetsRetracted); err != nil {
		return stats, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM assets WHERE
                  NOT EXISTS (SELECT 1 FROM observations o WHERE o.asset_id = assets.id)
                  AND NOT EXISTS (SELECT 1 FROM services s WHERE s.asset_id = assets.id)
                  AND (assets.sources = '[]' OR json_array_length(assets.sources) = 0)`); err != nil {
		return stats, err
	}

	// 5. One summary audit row.
	detail, _ := json.Marshal(map[string]any{
		"rule_kind": "source", "rule_value": name, "source": name,
		"observations": stats.ObservationsRemoved,
		"services":     stats.ServicesRemoved,
		"assets":       stats.AssetsRetracted,
	})
	if _, err := tx.ExecContext(ctx, `
INSERT INTO suppression_applications (suppression_id, action, detail, applied_at)
VALUES (?, 'retract-source', ?, ?)`, rule.ID, string(detail), FormatTS(time.Now().UTC())); err != nil {
		return stats, err
	}
	stats.AuditRows = 1
	return stats, nil
}

// removeString removes the first occurrence of v from in (the
// array_remove parity).
func removeString(in []string, v string) []string {
	out := make([]string, 0, len(in))
	removed := false
	for _, s := range in {
		if !removed && s == v {
			removed = true
			continue
		}
		out = append(out, s)
	}
	return out
}

// suppressionApplicationsList reads the audit surface (the CLI's
// suppress audit/list path).
func (s *Store) SuppressionApplications(ctx context.Context, limit int) ([]SuppressionApplication, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, COALESCE(suppression_id, 0), action, COALESCE(asset_id, 0), detail, applied_at
FROM suppression_applications ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: cannot list suppression applications: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []SuppressionApplication
	for rows.Next() {
		var a SuppressionApplication
		if err := rows.Scan(&a.ID, &a.SuppressionID, &a.Action, &a.AssetID, &a.Detail, &a.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SuppressionApplication is one audit row (denormalized detail).
type SuppressionApplication struct {
	ID            int64
	SuppressionID int64
	Action        string
	AssetID       int64
	Detail        string
	AppliedAt     string
}

// compile-time: the suppress package re-export keeps imports honest.
var _ = storage.ProvenanceFixture
