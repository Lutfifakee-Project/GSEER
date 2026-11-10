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

// store.go — the personal-mode Store: the same method set (and type
// aliases) the self-hosted postgres.Store exposes, lowered to
// database/sql + SQLite.  The postgres package is untouched; both
// stores satisfy the CLI/collect backend interfaces.
//
// SQ-01 (first_seen consistency): the upserts run as a two-step
// transaction — SELECT the stored row, INSERT or UPDATE accordingly —
// and RETURN the STORED first_seen/last_seen on the caller's structs,
// exactly the contract the PG store's RETURNING provides.  A caller
// that hands in a bogus FirstSeen never sees it echo back: the stored
// value wins.
//
// SQ-02 (sub-millisecond precision): every timestamp crosses the
// boundary through FormatTS/ParseTS (fixed-width UTC microseconds), so
// L1/L2 dedup, the last_seen regression guard (MAX under the
// fixed-width format is chronological) and keyset pagination are exact.
//
// SQ-06: the suppression gate wraps every write path exactly like the
// PG store (IngestFact/IngestFacts skip + audit; single-record upserts
// error); the backstop TRIGGER in migration 0001 covers raw writes —
// bypass attempts surface as a suppression refusal (GSUP1).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/fprule"
	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

// Type aliases — identical to the postgres package's, so callers' code
// compiles against either backend.
type (
	Fact        = storage.Fact
	FactResult  = storage.FactResult
	IngestStats = storage.IngestStats
)

// RetractStats reports one RetractSuppressed run (same field set as the
// postgres package's type).
type RetractStats struct {
	AssetsRetracted     int
	ServicesRemoved     int
	ObservationsRemoved int
	AuditRows           int
}

// PurgeStats reports one PurgeFixtures run (same field set as PG).
type PurgeStats struct {
	ObservationsRemoved int64
	ServicesRemoved     int64
	AssetsRemoved       int64
}

// IndexStats is the index snapshot (searchlite.Stats parity source).
type IndexStats struct {
	Assets       int64
	Services     int64
	Observations int64
	FixtureObs   int64
	LastObserved *time.Time
}

// Querier is the tx-or-pool surface the cores run against.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store is the personal-mode storage backend.
type Store struct {
	DB           *sql.DB
	fingerprints *fprule.Registry
}

// NewStore wraps an opened DB (Open → Migrate → NewStore).
func NewStore(db *sql.DB) *Store {
	return &Store{DB: db}
}

// SetFingerprintEngine attaches the fingerprint enrichment registry
// (same hook as the PG store; optional).
func (s *Store) SetFingerprintEngine(reg *fprule.Registry) {
	s.fingerprints = reg
}

// Close closes the underlying database handle.
func (s *Store) Close() error { return s.DB.Close() }

// suppressedError is the personal engine's suppression refusal marker
// (the PG store maps the GSUP1 SQLSTATE; here the Go gate returns this
// type directly and the backstop trigger's RAISE text is mapped).
type suppressedError struct{ msg string }

func (e *suppressedError) Error() string { return e.msg }

// IsSuppressedError reports whether err is a suppression refusal
// (Go gate or GSUP1 backstop trigger).
func IsSuppressedError(err error) bool {
	if err == nil {
		return false
	}
	var se *suppressedError
	if errors.As(err, &se) {
		return true
	}
	return containsStr(err.Error(), "GSUP1")
}

func containsStr(s, sub string) bool {
	for i := 0; len(sub) > 0 && i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Asset upsert (SQ-01) — identity (kind, value); merge semantics mirror
// the PG upsertAssetSQL: non-empty incoming metadata overwrites stored,
// zero values preserve, sources union DISTINCT, last_seen never
// regresses, any fresh sighting clears stale, first_seen NEVER changes
// after insert.
// ---------------------------------------------------------------------------

func (s *Store) UpsertAsset(ctx context.Context, a *storage.Asset) (int64, bool, error) {
	if err := s.gateAssetWrite(ctx, a.Kind, a.Value); err != nil {
		return 0, false, err
	}
	return s.upsertAssetCore(ctx, s.DB, a)
}

func (s *Store) upsertAssetCore(ctx context.Context, q Querier, a *storage.Asset) (int64, bool, error) {
	if err := validateAssetForUpsert(a); err != nil {
		return 0, false, err
	}
	if err := normalizeAssetForUpsert(a); err != nil {
		return 0, false, err
	}
	lastSeen := a.LastSeen
	if lastSeen.IsZero() {
		lastSeen = time.Now().UTC()
	}
	inSources := a.Sources
	if inSources == nil {
		inSources = []string{}
	}

	var (
		id              int64
		firstSeenStored time.Time
		lastSeenStored  time.Time
		inserted        bool
	)
	err := withTx(ctx, q, func(tx Querier) error {
		var sourcesRaw string
		var fsRaw, lsRaw string
		err := tx.QueryRowContext(ctx,
			`SELECT id, first_seen, last_seen, sources FROM assets WHERE kind = ? AND value = ?`,
			string(a.Kind), a.Value,
		).Scan(&id, &fsRaw, &lsRaw, &sourcesRaw)
		if err == nil {
			firstSeenStored, err = ParseTS(fsRaw)
			if err != nil {
				return fmt.Errorf("sqlite: asset %d first_seen: %w", id, err)
			}
			lastSeenStored, err = ParseTS(lsRaw)
			if err != nil {
				return fmt.Errorf("sqlite: asset %d last_seen: %w", id, err)
			}
		}
		switch {
		case err == nil:
			// UPDATE — merged metadata mirrors the PG ON CONFLICT clause:
			// COALESCE semantics for pointers, CASE for scalars, the
			// sources union is computed here from the stored + incoming
			// lists, and last_seen regresses never.
			mergedSources, merr := json.Marshal(uniqueStrings(append(jsonSources(sourcesRaw), inSources...)))
			if merr != nil {
				return merr
			}
			_, err = tx.ExecContext(ctx, `UPDATE assets SET
				ip          = COALESCE(?, ip),
				domain      = COALESCE(?, domain),
				hostname    = COALESCE(?, hostname),
				asn         = CASE WHEN ? <> 0 THEN ? ELSE asn END,
				asn_org     = CASE WHEN ? <> '' THEN ? ELSE asn_org END,
				geo_country = CASE WHEN ? <> '' THEN ? ELSE geo_country END,
				geo_region  = CASE WHEN ? <> '' THEN ? ELSE geo_region END,
				geo_city    = CASE WHEN ? <> '' THEN ? ELSE geo_city END,
				sources     = ?,
				last_seen   = CASE WHEN ? > last_seen THEN ? ELSE last_seen END,
				stale       = 0
			WHERE id = ?`,
				a.IP, a.Domain, a.Hostname,
				int64(a.ASN), int64(a.ASN),
				a.ASNOrg, a.ASNOrg,
				a.GeoCountry, a.GeoCountry,
				a.GeoRegion, a.GeoRegion,
				a.GeoCity, a.GeoCity,
				string(mergedSources),
				FormatTS(lastSeen), FormatTS(lastSeen),
				id)
			if err != nil {
				return mapSuppressionTrigger(err)
			}
			// Re-read the merged last_seen (MAX semantics applied in SQL).
			var mergedLast string
			if err := tx.QueryRowContext(ctx, `SELECT last_seen FROM assets WHERE id = ?`, id).Scan(&mergedLast); err != nil {
				return err
			}
			if t, perr := ParseTS(mergedLast); perr == nil {
				lastSeenStored = t
			}
			inserted = false
			return nil
		case isNoRows(err):
			sourcesJSON, merr := json.Marshal(uniqueStrings(inSources))
			if merr != nil {
				return merr
			}
			res, ierr := tx.ExecContext(ctx, `INSERT INTO assets
				(kind, value, ip, domain, hostname, asn, asn_org, geo_country, geo_region, geo_city, sources, first_seen, last_seen, stale)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
				string(a.Kind), a.Value, a.IP, a.Domain, a.Hostname,
				int64(a.ASN), a.ASNOrg, a.GeoCountry, a.GeoRegion, a.GeoCity,
				string(sourcesJSON), FormatTS(lastSeen), FormatTS(lastSeen))
			if ierr != nil {
				return mapSuppressionTrigger(ierr)
			}
			id, ierr = res.LastInsertId()
			if ierr != nil {
				return ierr
			}
			firstSeenStored, lastSeenStored = lastSeen, lastSeen
			inserted = true
			return nil
		default:
			return err
		}
	})
	if err != nil {
		return 0, false, err
	}
	a.ID, a.FirstSeen, a.LastSeen = id, firstSeenStored, lastSeenStored
	return id, inserted, nil
}

// validateAssetForUpsert mirrors the PG validation (kind ↔ typed field).
func validateAssetForUpsert(a *storage.Asset) error {
	matched := false
	switch a.Kind {
	case storage.AssetKindIP:
		matched = a.IP != nil
	case storage.AssetKindDomain:
		matched = a.Domain != nil
	case storage.AssetKindHostname:
		matched = a.Hostname != nil
	}
	if a.Value == "" || !matched {
		return fmt.Errorf("E_DB_USAGE: invalid asset for upsert (kind=%q value=%q): Value is empty or the kind's typed field (IP/Domain/Hostname) is nil", a.Kind, a.Value)
	}
	return nil
}

// normalizeAssetForUpsert canonicalizes through the suppress package —
// the same normalization definition as every PG writer.
func normalizeAssetForUpsert(a *storage.Asset) error {
	var canonical string
	var err error
	switch a.Kind {
	case storage.AssetKindIP:
		canonical, err = suppress.NormalizeIP(*a.IP)
	case storage.AssetKindDomain:
		canonical, err = suppress.NormalizeDomain(*a.Domain)
	case storage.AssetKindHostname:
		canonical, err = suppress.NormalizeDomain(*a.Hostname)
	default:
		return fmt.Errorf("E_DB_USAGE: invalid asset kind %q", a.Kind)
	}
	if err != nil {
		return fmt.Errorf("E_DB_USAGE: asset identifier failed normalization: %w", err)
	}
	a.Value = canonical
	switch a.Kind {
	case storage.AssetKindIP:
		a.IP = &canonical
	case storage.AssetKindDomain:
		a.Domain = &canonical
	case storage.AssetKindHostname:
		a.Hostname = &canonical
	}
	return nil
}

// ---------------------------------------------------------------------------
// Service upsert (SQ-01) — identity (asset_id, port, transport); the
// identity fields are replaced wholesale on conflict (the latest
// sighting is authoritative), last_seen never regresses, stale clears.
// ---------------------------------------------------------------------------

func (s *Store) UpsertService(ctx context.Context, sv *storage.Service) (int64, bool, error) {
	return s.upsertServiceCore(ctx, s.DB, sv)
}

func (s *Store) upsertServiceCore(ctx context.Context, q Querier, sv *storage.Service) (int64, bool, error) {
	if err := validateServiceForUpsert(sv); err != nil {
		return 0, false, err
	}
	lastSeen := sv.LastSeen
	if lastSeen.IsZero() {
		lastSeen = time.Now().UTC()
	}
	var (
		id              int64
		firstSeenStored time.Time
		lastSeenStored  time.Time
		inserted        bool
	)
	err := withTx(ctx, q, func(tx Querier) error {
		var fsRaw, lsRaw string
		err := tx.QueryRowContext(ctx,
			`SELECT id, first_seen, last_seen FROM services WHERE asset_id = ? AND port = ? AND transport = ?`,
			sv.AssetID, sv.Port, sv.Transport,
		).Scan(&id, &fsRaw, &lsRaw)
		if err == nil {
			firstSeenStored, err = ParseTS(fsRaw)
			if err != nil {
				return fmt.Errorf("sqlite: service %d first_seen: %w", id, err)
			}
			lastSeenStored, err = ParseTS(lsRaw)
			if err != nil {
				return fmt.Errorf("sqlite: service %d last_seen: %w", id, err)
			}
		}
		switch {
		case err == nil:
			_, err = tx.ExecContext(ctx, `UPDATE services SET
				protocol  = ?,
				name      = ?,
				product   = ?,
				version   = ?,
				last_seen = CASE WHEN ? > last_seen THEN ? ELSE last_seen END,
				stale     = 0
			WHERE id = ?`,
				sv.Protocol, sv.Name, sv.Product, sv.Version,
				FormatTS(lastSeen), FormatTS(lastSeen), id)
			if err != nil {
				return err
			}
			var mergedLast string
			if err := tx.QueryRowContext(ctx, `SELECT last_seen FROM services WHERE id = ?`, id).Scan(&mergedLast); err != nil {
				return err
			}
			if t, perr := ParseTS(mergedLast); perr == nil {
				lastSeenStored = t
			}
			inserted = false
			return nil
		case isNoRows(err):
			res, ierr := tx.ExecContext(ctx, `INSERT INTO services
				(asset_id, port, transport, protocol, name, product, version, first_seen, last_seen, stale)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
				sv.AssetID, sv.Port, sv.Transport, sv.Protocol,
				sv.Name, sv.Product, sv.Version,
				FormatTS(lastSeen), FormatTS(lastSeen))
			if ierr != nil {
				return ierr
			}
			id, ierr = res.LastInsertId()
			if ierr != nil {
				return ierr
			}
			firstSeenStored, lastSeenStored = lastSeen, lastSeen
			inserted = true
			return nil
		default:
			return err
		}
	})
	if err != nil {
		return 0, false, err
	}
	sv.ID, sv.FirstSeen, sv.LastSeen = id, firstSeenStored, lastSeenStored
	return id, inserted, nil
}

func validateServiceForUpsert(sv *storage.Service) error {
	var why string
	switch {
	case sv.AssetID <= 0:
		why = fmt.Sprintf("asset_id=%d is not a positive asset id (upsert the asset first)", sv.AssetID)
	case sv.Port < 1 || sv.Port > 65535:
		why = fmt.Sprintf("port %d outside 1..65535", sv.Port)
	case sv.Transport != "tcp" && sv.Transport != "udp":
		why = fmt.Sprintf("transport %q not in {tcp, udp}", sv.Transport)
	case sv.Protocol == "":
		why = "Protocol is empty"
	default:
		return nil
	}
	return fmt.Errorf("E_DB_USAGE: invalid service for upsert: %s — fill AssetID, Port 1..65535, Transport tcp|udp and a non-empty Protocol", why)
}

// ---------------------------------------------------------------------------
// Observation insert (L1/L2, same contract as the PG store).
// ---------------------------------------------------------------------------

func (s *Store) InsertObservation(ctx context.Context, o *storage.Observation) (int64, bool, error) {
	return s.insertObservationCore(ctx, s.DB, o)
}

func (s *Store) insertObservationCore(ctx context.Context, q Querier, o *storage.Observation) (int64, bool, error) {
	if o.ID == 0 || o.SourceID <= 0 || o.AssetID <= 0 || o.SourceCursor == "" || o.PayloadHash == "" || o.ObservedAt.IsZero() {
		return 0, false, fmt.Errorf("E_DB_USAGE: invalid observation for insert: id=%d source_id=%d asset_id=%d source_cursor=%q payload_hash=%q observed_at_zero=%v — one of the identity fields is missing",
			o.ID, o.SourceID, o.AssetID, o.SourceCursor, o.PayloadHash, o.ObservedAt.IsZero())
	}
	provenance := o.Provenance
	if provenance == "" {
		provenance = storage.ProvenanceFixture
	}
	evidence := "{}"
	if o.Evidence != nil {
		enriched := enrichEvidence(s.fingerprints, o.Evidence)
		b, err := json.Marshal(enriched)
		if err != nil {
			return 0, false, fmt.Errorf("E_DB_USAGE: cannot marshal observation evidence to JSON: %w", err)
		}
		evidence = string(b)
	}
	observedAt, err := ParseTS(FormatTS(o.ObservedAt))
	if err != nil {
		return 0, false, fmt.Errorf("E_DB_USAGE: observed_at not parseable: %w", err)
	}

	var id int64
	err = q.QueryRowContext(ctx, `INSERT INTO observations
		(id, source_id, asset_id, service_id, observed_at, payload_hash, source_cursor, provenance, evidence, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING
		RETURNING id`,
		o.ID, o.SourceID, o.AssetID, o.ServiceID,
		FormatTS(observedAt), o.PayloadHash, o.SourceCursor, provenance, evidence,
		FormatTS(time.Now().UTC()),
	).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !isNoRows(err) {
		return 0, false, err
	}
	// DO NOTHING fired — resolve the surviving row: L1 first, then L2.
	if l1err := q.QueryRowContext(ctx,
		`SELECT id FROM observations WHERE source_id = ? AND source_cursor = ?`,
		o.SourceID, o.SourceCursor).Scan(&id); l1err == nil {
		return id, false, nil
	} else if !isNoRows(l1err) {
		return 0, false, l1err
	}
	err = q.QueryRowContext(ctx, `SELECT id FROM observations
		WHERE source_id = ? AND asset_id = ?
		  AND COALESCE(service_id, 0) = COALESCE(?, 0)
		  AND observed_at = ? AND payload_hash = ?`,
		o.SourceID, o.AssetID, o.ServiceID, FormatTS(observedAt), o.PayloadHash,
	).Scan(&id)
	switch {
	case err == nil:
		return id, false, nil
	case isNoRows(err):
		return 0, false, fmt.Errorf("E_DB_USAGE: observation conflict but no existing row found (L1/L2 lookup missed) — report as a bug")
	default:
		return 0, false, err
	}
}

// ---------------------------------------------------------------------------
// Per-fact ingestion (SQ-06 gate + atomic tx).
// ---------------------------------------------------------------------------

// IngestFact applies one Fact atomically with the full suppression
// gate (source exclusion first, then asset rules; a suppressed fact
// writes nothing and lands one ingest-block audit row).
func (s *Store) IngestFact(ctx context.Context, f *Fact) (FactResult, error) {
	g, err := s.loadGate(ctx)
	if err != nil {
		return FactResult{}, err
	}
	return s.ingestFactGated(ctx, g, f)
}

func (s *Store) ingestFactGated(ctx context.Context, g *gate, f *Fact) (FactResult, error) {
	out, err := g.gateFact(ctx, s.DB, f)
	if err != nil {
		return FactResult{}, err
	}
	if out.suppressed {
		return FactResult{Suppressed: true}, nil
	}
	return s.ingestFactCore(ctx, f)
}

func (s *Store) ingestFactCore(ctx context.Context, f *Fact) (FactResult, error) {
	// With SetMaxOpenConns(1) every transaction serializes; Begin/Commit
	// gives the same atomicity contract as the PG per-fact transaction
	// (observation last — a failure rolls back the whole fact).
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return FactResult{}, fmt.Errorf("E_DB_UNREACHABLE: cannot begin the per-fact ingestion transaction: %w", err)
	}
	assetID, assetInserted, err := s.upsertAssetCore(ctx, tx, &f.Asset)
	if err != nil {
		_ = tx.Rollback()
		return FactResult{}, err
	}
	f.Observation.AssetID = assetID

	var svcID int64
	var svcInserted bool
	if f.Service != nil {
		f.Service.AssetID = assetID
		svcID, svcInserted, err = s.upsertServiceCore(ctx, tx, f.Service)
		if err != nil {
			_ = tx.Rollback()
			return FactResult{}, err
		}
		f.Observation.ServiceID = &svcID
	}
	obsID, obsInserted, err := s.insertObservationCore(ctx, tx, &f.Observation)
	if err != nil {
		_ = tx.Rollback()
		return FactResult{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return FactResult{}, fmt.Errorf("sqlite: cannot commit the per-fact ingestion transaction: %w", err)
	}
	return FactResult{
		AssetID:             assetID,
		ServiceID:           svcID,
		ObservationID:       obsID,
		AssetInserted:       assetInserted,
		ServiceInserted:     svcInserted,
		ObservationInserted: obsInserted,
	}, nil
}

// IngestFacts applies facts one at a time (each fact commits separately;
// idempotent re-runs) with ONE compiled rule set for the whole batch.
func (s *Store) IngestFacts(ctx context.Context, facts []Fact) (IngestStats, error) {
	var stats IngestStats
	g, err := s.loadGate(ctx)
	if err != nil {
		return stats, err
	}
	for i := range facts {
		f := &facts[i]
		res, err := s.ingestFactGated(ctx, g, f)
		if err != nil {
			return stats, err
		}
		if res.Suppressed {
			stats.Suppressed++
			continue
		}
		if res.AssetInserted {
			stats.AssetsInserted++
		} else {
			stats.AssetsUpdated++
		}
		if f.Service != nil {
			if res.ServiceInserted {
				stats.ServicesInserted++
			} else {
				stats.ServicesUpdated++
			}
		}
		if res.ObservationInserted {
			stats.ObservationsInserted++
		} else {
			stats.ObservationsDeduped++
		}
	}
	return stats, nil
}

// ---------------------------------------------------------------------------
// Fixture source + purge (QS-6 parity).
// ---------------------------------------------------------------------------

const fixtureSourceTermsRef = "labeled synthetic evaluation dataset (SPEC-04 §9); " +
	"every observation carries provenance=fixture; NOT real internet data (QS-6)"

// EnsureFixtureSource upserts the fixture source registry row.
func (s *Store) EnsureFixtureSource(ctx context.Context) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO data_sources (name, connector, terms_ref, config, enabled, created_at)
VALUES ('fixture', 'fixture', ?, '{}', 0, ?)
ON CONFLICT(name) DO UPDATE SET terms_ref = excluded.terms_ref
RETURNING id`, fixtureSourceTermsRef, FormatTS(time.Now().UTC())).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !isNoRows(err) && !containsStr(err.Error(), "ON CONFLICT") {
		return 0, fmt.Errorf("sqlite: cannot upsert the fixture source: %w", err)
	}
	// Conflict path (no row returned) — resolve the surviving row.
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM data_sources WHERE name = 'fixture'`).Scan(&id); err != nil {
		return 0, fmt.Errorf("sqlite: fixture source lookup after conflict: %w", err)
	}
	return id, nil
}

// PurgeFixtures deletes every fixture-tagged observation/service/asset
// in ONE scoped transaction (rows not tagged provenance='fixture' are
// never touched) — the QS-6 purge parity path.
func (s *Store) PurgeFixtures(ctx context.Context) (PurgeStats, error) {
	var stats PurgeStats
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("E_DB_UNREACHABLE: cannot begin the fixture purge transaction: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	// SQLite has no data-modifying CTEs: count first, delete after —
	// both inside the same transaction (one serialized connection, SQ-05).
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM observations WHERE provenance = 'fixture'`).Scan(&stats.ObservationsRemoved); err != nil {
		rollback()
		return stats, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM observations WHERE provenance = 'fixture'`); err != nil {
		rollback()
		return stats, err
	}
	// 2. orphaned services (no observations left).
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM services WHERE NOT EXISTS (SELECT 1 FROM observations o WHERE o.service_id = services.id)`).Scan(&stats.ServicesRemoved); err != nil {
		rollback()
		return stats, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM services WHERE NOT EXISTS (SELECT 1 FROM observations o WHERE o.service_id = services.id)`); err != nil {
		rollback()
		return stats, err
	}
	// 3. assets with no observation, no service and no sources left.
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM assets a WHERE
		  NOT EXISTS (SELECT 1 FROM observations o WHERE o.asset_id = a.id)
		  AND NOT EXISTS (SELECT 1 FROM services s WHERE s.asset_id = a.id)
		  AND (a.sources = '[]' OR json_array_length(a.sources) = 0)`).Scan(&stats.AssetsRemoved); err != nil {
		rollback()
		return stats, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM assets WHERE
		  NOT EXISTS (SELECT 1 FROM observations o WHERE o.asset_id = assets.id)
		  AND NOT EXISTS (SELECT 1 FROM services s WHERE s.asset_id = assets.id)
		  AND (assets.sources = '[]' OR json_array_length(assets.sources) = 0)`); err != nil {
		rollback()
		return stats, err
	}
	if err := tx.Commit(); err != nil {
		rollback()
		return stats, fmt.Errorf("sqlite: cannot commit the fixture purge: %w", err)
	}
	return stats, nil
}

// Stats snapshots the index counts (one round-trip, read-only).
func (s *Store) Stats(ctx context.Context) (IndexStats, error) {
	var st IndexStats
	var lastObs string
	err := s.DB.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM assets),
  (SELECT count(*) FROM services),
  (SELECT count(*) FROM observations),
  (SELECT count(*) FROM observations WHERE provenance = 'fixture'),
  (SELECT COALESCE(max(observed_at), '') FROM observations)
`).Scan(&st.Assets, &st.Services, &st.Observations, &st.FixtureObs, &lastObs)
	if err != nil {
		return st, fmt.Errorf("sqlite: cannot read index stats: %w", err)
	}
	if lastObs != "" {
		if t, perr := ParseTS(lastObs); perr == nil {
			st.LastObserved = &t
		}
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// withTx runs fn inside a transaction when the Querier is a *sql.DB;
// an existing transaction runs fn directly (the per-fact path relies
// on this nesting rule).
func withTx(ctx context.Context, q Querier, fn func(tx Querier) error) error {
	if db, ok := q.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: cannot begin transaction: %w", err)
		}
		if err := fn(tx); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("sqlite: cannot commit transaction: %w", err)
		}
		return nil
	}
	return fn(q)
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// mapSuppressionTrigger converts the backstop trigger's RAISE(ABORT,
// 'GSUP1: …') into the suppressedError the callers map to E_DB_SUPPRESSED.
func mapSuppressionTrigger(err error) error {
	if err != nil && containsStr(err.Error(), "GSUP1") {
		return &suppressedError{msg: err.Error()}
	}
	return err
}

// uniqueStrings dedupes preserving order (the sources union contract).
func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// jsonSources decodes the assets.sources JSON column.
func jsonSources(raw string) []string {
	if raw == "" || raw == "[]" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	return out
}
