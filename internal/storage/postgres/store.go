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

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Lutfifakee-Project/GSEER/internal/fprule"
	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

// store.go — the M4 write-side repositories (SPEC-05 §2/§5): asset/service
// upserts, the L1/L2 observation insert paths, and the per-fact ingestion
// transaction.
//
// Every fact (asset + optional service + observation) is applied in ONE
// transaction so a failure mid-fact can never leave a service without its
// asset or an observation without its service; facts are independent, so
// each fact commits separately and a re-run is idempotent (L1 delivery
// identity, L2 fact identity).

// querier is the minimal query surface shared by *pgxpool.Pool and
// pgx.Tx, so the upsert/insert cores run unchanged at pool scope
// (single-statement auto-commit) and inside the per-fact transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	// Query supports multi-row reads (the suppression rule loader,
	// M13); *pgxpool.Pool and pgx.Tx both provide it.
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Store is the M4 write-side repository over the application pool (the
// pool is created after migrations succeed — D-M3-8).
type Store struct {
	pool *pgxpool.Pool

	// fingerprints is the optional v0.1.0 rule engine (internal/fprule).
	// When set, observation evidence is enriched with derived
	// `fingerprint.technologies` at ingestion — the rule set classifies
	// evidence the record already carries; it never invents an
	// observation.  Nil (the default, and always for the fixture
	// loader) stores evidence verbatim (v0.0.2 behavior).
	fingerprints *fprule.Registry
}

// NewStore returns a Store over pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// SetFingerprintEngine attaches the rule engine used to enrich
// ingested observation evidence.  A nil engine disables enrichment.
// The engine only fires when the evidence has http/tls/ssh payload and
// no source-provided `fingerprint` key (the source always wins).
func (s *Store) SetFingerprintEngine(reg *fprule.Registry) {
	s.fingerprints = reg
}

// Fact, FactResult and IngestStats moved to internal/storage in v0.0.2
// (M14: the collector framework names them without importing the
// pgx-heavy store package).  These aliases keep every reference in this
// package — and every caller — compiling unchanged; []storage.Fact IS
// []Fact because the types are identical aliases.
type Fact = storage.Fact

// FactResult reports what one IngestFact applied (see storage.FactResult).
type FactResult = storage.FactResult

// IngestStats aggregates an IngestFacts run (see storage.IngestStats).
type IngestStats = storage.IngestStats

// ---------------------------------------------------------------------------
// Asset upsert (AC-4.2 — identity (kind, value))
// ---------------------------------------------------------------------------

// upsertAssetSQL merges a sighting into the (kind, value)-unique assets
// row.  Conflict semantics: a non-zero / non-empty / non-NULL EXCLUDED
// field overwrites; zero values PRESERVE the stored metadata (a partial
// fact never blanks what is known); sources are unioned via array_agg
// DISTINCT; last_seen never regresses (GREATEST); any fresh sighting
// clears stale.  first_seen is NOT in the update set, so RETURNING
// first_seen keeps the original on conflict.  The COALESCE around
// array_agg keeps an empty∪empty union at '{}' instead of the NULL
// array_agg returns over zero rows (sources is NOT NULL).
const upsertAssetSQL = `INSERT INTO assets
        (kind, value, ip, domain, hostname, asn, asn_org, geo_country, geo_region, geo_city, sources, last_seen)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (kind, value) DO UPDATE SET
        ip           = COALESCE(EXCLUDED.ip, assets.ip),
        domain       = COALESCE(EXCLUDED.domain, assets.domain),
        hostname     = COALESCE(EXCLUDED.hostname, assets.hostname),
        asn          = CASE WHEN EXCLUDED.asn <> 0 THEN EXCLUDED.asn ELSE assets.asn END,
        asn_org      = CASE WHEN EXCLUDED.asn_org <> '' THEN EXCLUDED.asn_org ELSE assets.asn_org END,
        geo_country  = CASE WHEN EXCLUDED.geo_country <> '' THEN EXCLUDED.geo_country ELSE assets.geo_country END,
        geo_region   = CASE WHEN EXCLUDED.geo_region <> '' THEN EXCLUDED.geo_region ELSE assets.geo_region END,
        geo_city     = CASE WHEN EXCLUDED.geo_city <> '' THEN EXCLUDED.geo_city ELSE assets.geo_city END,
        sources      = COALESCE((SELECT array_agg(DISTINCT s) FROM unnest(assets.sources || EXCLUDED.sources) AS s), assets.sources),
        last_seen    = GREATEST(assets.last_seen, EXCLUDED.last_seen),
        stale        = false
RETURNING id, first_seen, last_seen, (xmax = 0) AS inserted`

// UpsertAsset upserts one asset at pool scope (single-statement
// auto-commit; callers wanting per-fact atomicity use IngestFact).
//
// Validation: Kind must be one of ip|domain|hostname, the kind's typed
// pointer (IP/Domain/Hostname) must be non-nil and Value non-empty —
// usage errors are E_DB_USAGE, not raw SQLSTATEs from the schema's
// assets_kind_fields_check.
//
// On return a.ID, a.FirstSeen and a.LastSeen carry the stored row's
// values (on conflict FirstSeen stays at the original, since RETURNING
// exposes the new row and first_seen is not in the update set);
// a.Sources is NOT re-read — the DB-side union may differ in order from
// the caller's slice.  A zero LastSeen means "seen now" (the column is
// written explicitly, so the year-1 zero time must never reach SQL).
// ASN 0 = unset (the CASE preserves the stored value on conflict).
func (s *Store) UpsertAsset(ctx context.Context, a *storage.Asset) (id int64, inserted bool, err error) {
	if err := s.gateAsset(ctx, s.pool, a.Kind, a.Value); err != nil {
		return 0, false, err
	}
	return upsertAssetCore(ctx, s.pool, a)
}

// validateAssetForUpsert enforces the (kind ↔ typed column, Value)
// invariant before SQL runs.
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
		return newDBError(CodeDBUsage, "invalid asset for upsert").
			withWhy(fmt.Sprintf("kind=%q value=%q: Value is empty or the kind's typed field (IP/Domain/Hostname) is nil", a.Kind, a.Value)).
			withNext("set Kind to ip|domain|hostname, fill the matching typed pointer (storage.Asset.IP/Domain/Hostname) and give a non-empty Value")
	}
	return nil
}

// upsertAssetCore is the tx-scoped asset upsert (querier = pool or tx).
//
// Normalization (M13/M14 data quality): the identifier values are
// canonicalized through the suppress package normalizers BEFORE the SQL
// runs — lowercase punycode domains without the root dot, netip-canonical
// IPs.  Every writer (fixture loader, collectors, direct upserts) shares
// this single normalization definition, and the suppression backstop
// trigger compares canonical bytes, so an un-normalized value can never
// dodge a rule.  A value that fails normalization is E_DB_USAGE.
func upsertAssetCore(ctx context.Context, q querier, a *storage.Asset) (int64, bool, error) {
	if err := validateAssetForUpsert(a); err != nil {
		return 0, false, err
	}
	if err := normalizeAssetForUpsert(a); err != nil {
		return 0, false, err
	}
	lastSeen := a.LastSeen
	if lastSeen.IsZero() {
		lastSeen = time.Now().UTC() // a fresh sighting is seen now
	}
	sources := a.Sources
	if sources == nil {
		sources = []string{} // NOT NULL column; a nil slice encodes as NULL
	}
	var (
		id         int64
		firstSeen  time.Time
		storedLast time.Time
		inserted   bool
	)
	err := q.QueryRow(ctx, upsertAssetSQL,
		string(a.Kind), a.Value, a.IP, a.Domain, a.Hostname,
		int64(a.ASN), a.ASNOrg, a.GeoCountry, a.GeoRegion, a.GeoCity,
		sources, lastSeen,
	).Scan(&id, &firstSeen, &storedLast, &inserted)
	if err != nil {
		// The DB backstop trigger (migration 0005) rejects assets matching
		// an enabled suppression rule — map its custom ERRCODE so bypass
		// attempts read as the policy violation they are.
		if serr := mapSuppressionTrigger(err); serr != err {
			return 0, false, serr
		}
		// Real write failures (e.g. 23514 from assets_kind_fields_check)
		// surface raw so callers can match on SQLSTATE.
		return 0, false, err
	}
	a.ID, a.FirstSeen, a.LastSeen = id, firstSeen, storedLast
	return id, inserted, nil
}

// normalizeAssetForUpsert canonicalizes the asset's identifier in place
// (Value + the kind's typed pointer).  See upsertAssetCore.
func normalizeAssetForUpsert(a *storage.Asset) error {
	var (
		canonical string
		err       error
	)
	switch a.Kind {
	case storage.AssetKindIP:
		canonical, err = suppress.NormalizeIP(*a.IP)
	case storage.AssetKindDomain:
		canonical, err = suppress.NormalizeDomain(*a.Domain)
	case storage.AssetKindHostname:
		canonical, err = suppress.NormalizeDomain(*a.Hostname)
	default:
		return newDBError(CodeDBUsage, "invalid asset kind").
			withWhy(fmt.Sprintf("kind %q not in {ip, domain, hostname}", a.Kind)).
			withNext("set Kind to ip|domain|hostname")
	}
	if err != nil {
		return newDBError(CodeDBUsage, "asset identifier failed normalization").
			withWhy(err.Error()).
			withNext("the writer must deliver a canonical value; see internal/suppress.NormalizeValue")
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
// Service upsert (AC-4.3 — identity (asset_id, port, transport))
// ---------------------------------------------------------------------------

// upsertServiceSQL inserts or refreshes a service.  Unlike assets, the
// identity fields are REPLACED wholesale on conflict (protocol/name/
// product/version): the latest sighting is authoritative for what a
// service is, while last_seen still never regresses and stale clears.
const upsertServiceSQL = `INSERT INTO services
        (asset_id, port, transport, protocol, name, product, version, last_seen)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (asset_id, port, transport) DO UPDATE SET
        protocol  = EXCLUDED.protocol,
        name      = EXCLUDED.name,
        product   = EXCLUDED.product,
        version   = EXCLUDED.version,
        last_seen = GREATEST(services.last_seen, EXCLUDED.last_seen),
        stale     = false
RETURNING id, first_seen, last_seen, (xmax = 0) AS inserted`

// UpsertService upserts one service at pool scope.  Validation: AssetID
// must be a positive asset id, Port 1..65535, Transport tcp|udp and
// Protocol non-empty (E_DB_USAGE otherwise).  On return sv.ID, sv.FirstSeen
// and sv.LastSeen carry the stored row's values; a zero LastSeen means
// "seen now".
//
// No suppression gate here BY DESIGN: a service hangs off an existing
// asset, and the asset's identifier was gated when the asset was written
// (and is re-gated by the backstop trigger on every asset write).  A
// suppression retraction removes the asset row, cascading its services —
// so no service can outlive a suppression that covers its asset.
func (s *Store) UpsertService(ctx context.Context, sv *storage.Service) (id int64, inserted bool, err error) {
	return upsertServiceCore(ctx, s.pool, sv)
}

// validateServiceForUpsert mirrors the schema checks for services so bad
// callers get E_DB_USAGE with guidance instead of SQLSTATE 23514/23503.
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
	return newDBError(CodeDBUsage, "invalid service for upsert").
		withWhy(why).
		withNext("fill AssetID, Port 1..65535, Transport tcp|udp and a non-empty Protocol")
}

// upsertServiceCore is the tx-scoped service upsert (querier = pool or tx).
func upsertServiceCore(ctx context.Context, q querier, sv *storage.Service) (int64, bool, error) {
	if err := validateServiceForUpsert(sv); err != nil {
		return 0, false, err
	}
	lastSeen := sv.LastSeen
	if lastSeen.IsZero() {
		lastSeen = time.Now().UTC() // a fresh sighting is seen now
	}
	var (
		id         int64
		firstSeen  time.Time
		storedLast time.Time
		inserted   bool
	)
	err := q.QueryRow(ctx, upsertServiceSQL,
		sv.AssetID, sv.Port, sv.Transport, sv.Protocol,
		sv.Name, sv.Product, sv.Version, lastSeen,
	).Scan(&id, &firstSeen, &storedLast, &inserted)
	if err != nil {
		// e.g. 23503 FK to assets (asset not upserted yet) surfaces raw.
		return 0, false, err
	}
	sv.ID, sv.FirstSeen, sv.LastSeen = id, firstSeen, storedLast
	return id, inserted, nil
}

// ---------------------------------------------------------------------------
// Observation insert (L1/L2 — SPEC-05 §5)
// ---------------------------------------------------------------------------

// insertObservationSQL inserts one observation.  ON CONFLICT DO NOTHING
// covers ALL uniqueness surfaces at once (primary key id, L1
// ux_observations_delivery, L2 ux_observations_fact): a conflicting
// insert is a dedup, and the surviving row is resolved by lookup below.
const insertObservationSQL = `INSERT INTO observations
        (id, source_id, asset_id, service_id, observed_at, payload_hash, source_cursor, provenance, evidence)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT DO NOTHING
RETURNING id`

// lookupObservationL1SQL resolves a dedup by delivery identity: the same
// source record re-delivered across restarts/retries/replay (AC-4.2).
const lookupObservationL1SQL = `SELECT id FROM observations
WHERE source_id = $1 AND source_cursor = $2`

// lookupObservationL2SQL resolves a dedup by fact identity: a distinct
// record with identical content within a source.  COALESCE(service_id, 0)
// on BOTH sides matches asset-level facts (NULL service_id) without a
// separate IS NULL branch.
const lookupObservationL2SQL = `SELECT id FROM observations
WHERE source_id = $1
  AND asset_id = $2
  AND COALESCE(service_id, 0::bigint) = COALESCE($3, 0::bigint)
  AND observed_at = $4
  AND payload_hash = $5`

// InsertObservation inserts one observation at pool scope.  The id is
// CALLER-PROVIDED — observations.id has no sequence default because the
// fixture corpus loader assigns deterministic ids (D-M3-2) — and is
// passed through as-is.
//
// Dedup semantics: on any unique-index conflict the already-stored row
// WINS (its content and provenance are kept) and inserted=false is
// returned with the surviving row's id, resolved via the L1 delivery
// lookup first, then the L2 fact lookup.  An empty Provenance defaults
// to the fixture tag (QS-6: unlabeled rows must never look real).
func (s *Store) InsertObservation(ctx context.Context, o *storage.Observation) (id int64, inserted bool, err error) {
	return insertObservationCore(ctx, s.pool, o, s.fingerprints)
}

// insertObservationCore is the tx-scoped observation insert.  The
// optional engine enriches the stored evidence copy (never the
// caller's map, never PayloadHash) before persist.
func insertObservationCore(ctx context.Context, q querier, o *storage.Observation, engine *fprule.Registry) (int64, bool, error) {
	if o.ID == 0 || o.SourceID <= 0 || o.AssetID <= 0 || o.SourceCursor == "" || o.PayloadHash == "" || o.ObservedAt.IsZero() {
		return 0, false, newDBError(CodeDBUsage, "invalid observation for insert").
			withWhy(fmt.Sprintf("id=%d source_id=%d asset_id=%d source_cursor=%q payload_hash=%q observed_at_zero=%v — one of the identity fields is missing",
				o.ID, o.SourceID, o.AssetID, o.SourceCursor, o.PayloadHash, o.ObservedAt.IsZero())).
			withNext("fill ID (caller-assigned: the column has no default and a zero id collides on the PRIMARY KEY, which ON CONFLICT DO NOTHING swallows without an L1/L2 match), SourceID, AssetID, SourceCursor, PayloadHash and a non-zero ObservedAt")
	}
	provenance := o.Provenance
	if provenance == "" {
		provenance = storage.ProvenanceFixture
	}
	evidence := []byte("{}")
	if o.Evidence != nil {
		enriched := enrichEvidence(engine, o.Evidence)
		b, err := json.Marshal(enriched)
		if err != nil {
			return 0, false, newDBError(CodeDBUsage, "cannot marshal observation evidence to JSON").
				withWhy(err.Error()).
				withNext("use JSON-serializable evidence values (string keys, JSON types)")
		}
		evidence = b
	}

	var id int64
	err := q.QueryRow(ctx, insertObservationSQL,
		o.ID, o.SourceID, o.AssetID, o.ServiceID, o.ObservedAt,
		o.PayloadHash, o.SourceCursor, provenance, evidence,
	).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		// Real write failure (e.g. 23503 FK to data_sources) — surfaced
		// raw; in IngestFact the tx rollback discards the asset/service
		// upserts applied earlier in the same fact.
		return 0, false, err
	}

	// DO NOTHING fired: resolve the surviving row — L1 first, then L2.
	if l1err := q.QueryRow(ctx, lookupObservationL1SQL, o.SourceID, o.SourceCursor).Scan(&id); l1err == nil {
		return id, false, nil
	} else if !errors.Is(l1err, pgx.ErrNoRows) {
		return 0, false, l1err
	}
	err = q.QueryRow(ctx, lookupObservationL2SQL,
		o.SourceID, o.AssetID, o.ServiceID, o.ObservedAt, o.PayloadHash,
	).Scan(&id)
	switch {
	case err == nil:
		return id, false, nil
	case errors.Is(err, pgx.ErrNoRows):
		return 0, false, newDBError(CodeDBUsage, "observation conflict but no existing row found (L1/L2 lookup missed)").
			withWhy(fmt.Sprintf("insert deduped but neither delivery identity (source_id=%d, cursor=%q) nor fact identity (hash=%q) matched a stored row",
				o.SourceID, o.SourceCursor, o.PayloadHash)).
			withNext("report as a bug: the unique index that fired must be covered by the L1 or L2 lookup")
	default:
		return 0, false, err
	}
}

// ---------------------------------------------------------------------------
// Per-fact ingestion transaction
// ---------------------------------------------------------------------------

// IngestFact applies one Fact atomically: asset upsert → optional service
// upsert → observation insert, all in ONE transaction.  The observation
// write is last, so a failure there (e.g. an FK violation for an unknown
// source) rolls the whole fact back — a service can never be left without
// its asset, nor an observation without its service.
//
// Suppression gate (M13): the fact is checked against the compiled rule
// set BEFORE any write — source exclusion first, then asset rules.  A
// suppressed fact writes nothing (not even asset metadata): it returns
// FactResult{Suppressed: true} with a nil error, and one ingest-block row
// lands in suppression_applications.
//
// Deliberate side effect: the caller's f.Observation.AssetID,
// f.Service.AssetID and — when a service is present —
// f.Observation.ServiceID are updated to the resolved row ids, so
// follow-up code can reference them without a re-read.
func (s *Store) IngestFact(ctx context.Context, f *Fact) (FactResult, error) {
	// Suppression gate (fail-closed: a rule set that cannot compile stops
	// ingestion rather than bypassing it).
	g, err := s.loadGate(ctx)
	if err != nil {
		return FactResult{}, err
	}
	return s.ingestFactGated(ctx, g, f)
}

// ingestFactGated applies one already-gated fact (the per-fact
// transaction).  IngestFact builds the single-fact gate; IngestFacts
// passes its batch-wide gate so the rule set loads once per batch.
func (s *Store) ingestFactGated(ctx context.Context, g *gate, f *Fact) (FactResult, error) {
	out, err := g.gateFact(ctx, s.pool, f)
	if err != nil {
		return FactResult{}, err
	}
	if out.suppressed {
		return FactResult{Suppressed: true}, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return FactResult{}, newDBError(CodeDBUnreachable, "cannot begin the per-fact ingestion transaction").
			withWhy(err.Error()).
			withNext("check the database connection and retry")
	}
	res, err := ingestFactCore(ctx, tx, f, s.fingerprints)
	if err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			err = errors.Join(err, rbErr)
		}
		return FactResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			err = errors.Join(err, rbErr)
		}
		return FactResult{}, err
	}
	return res, nil
}

// ingestFactCore applies the fact's three writes on the given querier
// (pool or tx) in dependency order.  The optional engine enriches the
// observation evidence (fingerprint.go).
func ingestFactCore(ctx context.Context, q querier, f *Fact, engine *fprule.Registry) (FactResult, error) {
	assetID, assetInserted, err := upsertAssetCore(ctx, q, &f.Asset)
	if err != nil {
		return FactResult{}, err
	}
	f.Observation.AssetID = assetID

	var (
		svcID       int64
		svcInserted bool
	)
	if f.Service != nil {
		f.Service.AssetID = assetID
		svcID, svcInserted, err = upsertServiceCore(ctx, q, f.Service)
		if err != nil {
			return FactResult{}, err
		}
		f.Observation.ServiceID = &svcID
	}
	obsID, obsInserted, err := insertObservationCore(ctx, q, &f.Observation, engine)
	if err != nil {
		return FactResult{}, err
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

// IngestFacts applies facts one at a time — each fact commits separately
// because facts are independent: a failure aborts only its own
// transaction, and a re-run is idempotent (L1/L2 identities) — and
// aggregates the results.  On the first error it stops and returns the
// stats accumulated so far with the error.  Service counters advance only
// for facts that carry a service (an asset-level fact is neither a
// service insert nor a service update).
//
// The suppression rule set is compiled ONCE for the whole batch (the
// gate then applies per fact, writing the ingest-block audit rows);
// suppressed facts count into IngestStats.Suppressed and are skipped.
func (s *Store) IngestFacts(ctx context.Context, facts []Fact) (IngestStats, error) {
	var stats IngestStats
	// One compiled rule set for the whole batch.
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
// Fixture source registry + purge
// ---------------------------------------------------------------------------

// fixtureSourceTermsRef is the registry-visible honesty note (D-M3-1,
// QS-6): the fixture source documents its labeled-synthetic nature in
// data_sources.terms_ref so no consumer mistakes it for real telemetry.
const fixtureSourceTermsRef = "labeled synthetic evaluation dataset (SPEC-04 §9); " +
	"every observation carries provenance=fixture; NOT real internet data (QS-6)"

// EnsureFixtureSource upserts the fixture source registry row and returns
// its id (D-M3-1: the fixture source is itself a data_sources row).
// Re-running refreshes terms_ref but deliberately leaves enabled
// untouched: v0.0.1 sources are never enabled (SPEC-09 §1).
func (s *Store) EnsureFixtureSource(ctx context.Context) (sourceID int64, err error) {
	err = s.pool.QueryRow(ctx, `
INSERT INTO data_sources (name, connector, terms_ref, enabled)
VALUES ($1, 'fixture', $2, false)
ON CONFLICT (name) DO UPDATE SET terms_ref = EXCLUDED.terms_ref
RETURNING id`,
		storage.FixtureSourceName, fixtureSourceTermsRef).Scan(&sourceID)
	if err != nil {
		return 0, err
	}
	return sourceID, nil
}

// PurgeStats reports what PurgeFixtureData removed.
type PurgeStats struct {
	Observations, Services, Assets int64
	FixtureSourcePresent           bool
}

// PurgeFixtureData removes every row the fixture loader wrote, in ONE
// transaction, scoped so real observations are NEVER deleted:
//
//  1. the observation DELETE binds to the fixture source's registry id
//     AND the provenance='fixture' tag — a real source's observation is
//     unreachable by both predicates;
//  2. services are deleted only if a fixture observation referenced them
//     AND no observation references them anymore AFTER the observation
//     deletion (separate statement so it sees the post-deletion state) —
//     a real source's service always has a live reference and survives;
//  3. assets are deleted only if they are fixture-sourced AND have no
//     observation AND no service left — a real source's asset keeps its
//     merged sources[] and/or live references and survives;
//  4. surviving assets have 'fixture' stripped from sources[] (registry
//     honesty: the fixture source no longer claims them).
//
// The fixture source registry row itself is left in place (it is
// configuration, not data); FixtureSourcePresent reports whether it
// existed.  A database with no fixture source row is a no-op success
// (nothing was ever loaded), not an error.
func PurgeFixtureData(ctx context.Context, pool *pgxpool.Pool) (PurgeStats, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return PurgeStats{}, newDBError(CodeDBUnreachable, "cannot begin the fixture purge transaction").
			withWhy(err.Error()).
			withNext("check the database connection and retry")
	}
	stats, err := purgeFixtureDataCore(ctx, tx)
	if err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			err = errors.Join(err, rbErr)
		}
		return PurgeStats{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			err = errors.Join(err, rbErr)
		}
		return PurgeStats{}, err
	}
	return stats, nil
}

// purgeFixtureDataCore runs the six scoped purge steps on one querier.
func purgeFixtureDataCore(ctx context.Context, q querier) (PurgeStats, error) {
	var stats PurgeStats

	// 1. Fixture data can only exist if the fixture source row does.
	var sourceID int64
	err := q.QueryRow(ctx, `SELECT id FROM data_sources WHERE name = $1`, storage.FixtureSourceName).Scan(&sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return PurgeStats{FixtureSourcePresent: false}, nil
	}
	if err != nil {
		return PurgeStats{}, err
	}

	// 2. Stage the service ids the fixture observations referenced.  ON
	// COMMIT DROP keeps the staging ephemeral: it never outlives the purge
	// and a re-run can never collide with a stale copy.
	if _, err := q.Exec(ctx, `CREATE TEMP TABLE tmp_fixture_services ON COMMIT DROP AS
SELECT DISTINCT service_id FROM observations
WHERE source_id = $1 AND provenance = $2 AND service_id IS NOT NULL`,
		sourceID, storage.ProvenanceFixture); err != nil {
		return PurgeStats{}, err
	}

	// 3. Delete the fixture observations (source id AND provenance bound).
	if err := q.QueryRow(ctx, `WITH del AS (
        DELETE FROM observations WHERE source_id = $1 AND provenance = $2 RETURNING 1
) SELECT count(*) FROM del`, sourceID, storage.ProvenanceFixture).Scan(&stats.Observations); err != nil {
		return PurgeStats{}, err
	}

	// 4. Delete the referenced services ONLY when orphaned post-deletion.
	if err := q.QueryRow(ctx, `WITH del AS (
        DELETE FROM services s
        WHERE s.id IN (SELECT service_id FROM tmp_fixture_services)
          AND NOT EXISTS (SELECT 1 FROM observations o WHERE o.service_id = s.id)
        RETURNING 1
) SELECT count(*) FROM del`).Scan(&stats.Services); err != nil {
		return PurgeStats{}, err
	}

	// 5. Delete fixture-sourced assets ONLY when fully unreferenced.
	if err := q.QueryRow(ctx, `WITH del AS (
        DELETE FROM assets a
        WHERE 'fixture' = ANY(a.sources)
          AND NOT EXISTS (SELECT 1 FROM observations o WHERE o.asset_id = a.id)
          AND NOT EXISTS (SELECT 1 FROM services s WHERE s.asset_id = a.id)
        RETURNING 1
) SELECT count(*) FROM del`).Scan(&stats.Assets); err != nil {
		return PurgeStats{}, err
	}

	// 6. Registry honesty for surviving assets (count not reported).
	if _, err := q.Exec(ctx, `UPDATE assets SET sources = array_remove(sources, 'fixture') WHERE 'fixture' = ANY(sources)`); err != nil {
		return PurgeStats{}, err
	}

	stats.FixtureSourcePresent = true
	return stats, nil
}
