//go:build integration

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

package fixture

// roundtrip_test.go — the M4 Load → search → purge round-trip integration
// test (build tag `integration`; run with `go test -tags integration ...`
// against a live PostgreSQL).
//
// Go test files are never importable across packages, so this file cannot
// reuse the postgres package's harness; it carries its own private one
// with the same shape as internal/storage/postgres/integration_test.go
// (testDB): an admin connection creates a throwaway database per test,
// postgres.RunMigrations brings it to head, and the cleanup terminates
// lingering backends before DROP DATABASE (drop-with-terminate pattern).

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
)

// defaultAdminDSN matches the standalone PG 17 used for local development
// (scripts/ensure_pg.sh); override with GSEER_TEST_PG_ADMIN.
const defaultAdminDSN = "postgres://gseer@127.0.0.1:5433/postgres?sslmode=disable"

// testCounter makes per-test database names unique (tests run serially;
// the mutex-guarded counter is belt-and-braces against -count re-runs in
// one process).
var (
	testMu      sync.Mutex
	testCounter int
)

// adminDSN returns the superuser DSN used to create/drop per-test
// databases: GSEER_TEST_PG_ADMIN if set, the local default otherwise.
func adminDSN() string {
	if v := os.Getenv("GSEER_TEST_PG_ADMIN"); v != "" {
		return v
	}
	return defaultAdminDSN
}

// testPool creates a unique empty database, migrates it to head and
// returns a connected pool plus a cleanup func that terminates lingering
// backends and drops the database.  CI sets GSEER_TEST_REQUIRE=1 to fail
// hard when no PG is reachable; without it the harness still fails (the
// round-trip under test is the point of this file).
func testPool(t *testing.T) (*pgxpool.Pool, string, func()) {
	t.Helper()
	dsn := adminDSN()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("cannot connect to admin PG (%s): %v — start the test PostgreSQL and set GSEER_TEST_PG_ADMIN", dsn, err)
	}

	testMu.Lock()
	testCounter++
	name := fmt.Sprintf("gseer_m4_fixture_%d_%d", os.Getpid(), testCounter)
	testMu.Unlock()

	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("cannot create test database %s: %v", name, err)
	}
	if err := admin.Close(ctx); err != nil {
		t.Logf("cleanup note: admin connection close: %v", err)
	}

	// Derive the per-test DSN from the admin DSN (same host/user).
	testDSN := strings.Replace(dsn, "/postgres?", "/"+name+"?", 1)
	if testDSN == dsn { // admin DSN had no "?"; append the path
		testDSN = strings.TrimSuffix(dsn, "/postgres") + "/" + name
	}

	pool, err := postgres.NewPool(ctx, testDSN)
	if err != nil {
		dropDatabase(t, dsn, name)
		t.Fatalf("cannot build pool for %s: %v", name, err)
	}
	// v0.2.0: bootstrap roles before the hardened migration set.
	if _, err := postgres.BootstrapRoles(context.Background(), testDSN); err != nil {
		pool.Close()
		dropDatabase(t, dsn, name)
		t.Fatalf("BootstrapRoles on %s: %v", name, err)
	}
	if _, _, err := postgres.RunMigrations(testDSN, 1001); err != nil {
		pool.Close()
		dropDatabase(t, dsn, name)
		t.Fatalf("RunMigrations on %s: %v", name, err)
	}

	cleanup := func() {
		pool.Close()
		dropDatabase(t, dsn, name)
	}
	return pool, testDSN, cleanup
}

// dropDatabase terminates the database's lingering backends and drops it
// (the pool must already be closed) — the drop-with-terminate cleanup
// pattern from internal/storage/postgres/integration_test.go (testDB).
func dropDatabase(t *testing.T, adminDSN, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Logf("cleanup: cannot reconnect to drop %s: %v", name, err)
		return
	}
	defer func() { _ = admin.Close(ctx) }()
	_, _ = admin.Exec(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()`, name)
	if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name); err != nil {
		t.Logf("cleanup: drop database %s failed: %v", name, err)
	}
}

// queryCount runs a scalar count query and fails the test on error.
func queryCount(t *testing.T, pool *pgxpool.Pool, label, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	return n
}

// countChecks asserts a table of scalar count expectations.
func countChecks(t *testing.T, pool *pgxpool.Pool, checks []struct {
	label, sql string
	want       int64
}) {
	t.Helper()
	for _, check := range checks {
		if got := queryCount(t, pool, check.label, check.sql); got != check.want {
			t.Errorf("%s = %d, want %d", check.label, got, check.want)
		}
	}
}

// TestFixtureLoadSearchPurgeRoundTrip drives the full M4 story in one
// throwaway database: Load ingests the corpus (4 assets, 6 services,
// 7 observations) with provenance=fixture everywhere and the search_index
// projections the corpus implies; a replay inserts nothing; a REAL
// (non-fixture) observation lands next to the corpus and merges into the
// already-fixture-tagged asset; PurgeFixtureData then removes exactly the
// fixture rows (7 observations, 6 services, 3 assets) and strips 'fixture'
// from the surviving asset's sources without touching the real row; a
// second purge is a no-op with the registry row still present.
func TestFixtureLoadSearchPurgeRoundTrip(t *testing.T) {
	pool, _, cleanup := testPool(t)
	defer cleanup()

	ctx := context.Background()
	store := postgres.NewStore(pool)

	// --- First Load: the corpus ingests fully. -----------------------------
	stats1, err := Load(ctx, store)
	if err != nil {
		t.Fatalf("Load #1: %v", err)
	}
	if stats1.SourceID <= 0 {
		t.Errorf("Load #1 SourceID = %d, want > 0 (EnsureFixtureSource registry id)", stats1.SourceID)
	}
	if stats1.AssetsInserted != 4 {
		t.Errorf("Load #1 AssetsInserted = %d, want 4 (corpus assets A–D)", stats1.AssetsInserted)
	}
	if stats1.ServicesInserted != 6 {
		t.Errorf("Load #1 ServicesInserted = %d, want 6 (corpus services s1–s6)", stats1.ServicesInserted)
	}
	if stats1.ObservationsInserted != 7 {
		t.Errorf("Load #1 ObservationsInserted = %d, want 7 (corpus observations o1–o7)", stats1.ObservationsInserted)
	}

	// --- Row-level assertions after the first load. ------------------------
	countChecks(t, pool, []struct {
		label, sql string
		want       int64
	}{
		{"assets after load", `SELECT count(*) FROM assets`, 4},
		{"services after load", `SELECT count(*) FROM services`, 6},
		{"observations after load", `SELECT count(*) FROM observations`, 7},
		{"non-fixture provenance after load", `SELECT count(*) FROM observations WHERE provenance <> 'fixture'`, 0},
		{"fixture registry row", `SELECT count(*) FROM data_sources WHERE name = 'fixture' AND connector = 'fixture' AND enabled = false AND terms_ref <> ''`, 1},
		{"search_index rows after load", `SELECT count(*) FROM search_index`, 7},
		{"search_index asset-only portal row", `SELECT count(*) FROM search_index WHERE row_kind = 'asset-only' AND asset_value = 'portal.example.net' AND port IS NULL`, 1},
		{"search_index port-443 rows", `SELECT count(*) FROM search_index WHERE port = 443`, 2},
	})

	// The http_title='Portal' row is asset C's http/443 service (corpus o5).
	var proto string
	var port int64
	if err := pool.QueryRow(ctx,
		`SELECT protocol, port FROM search_index WHERE http_title = 'Portal'`).Scan(&proto, &port); err != nil {
		t.Fatalf("search_index row with http_title='Portal': %v", err)
	}
	if proto != "http" || port != 443 {
		t.Errorf("Portal row = (%q, %d), want (http, 443)", proto, port)
	}

	// The 203.0.113.10/443 row carries o1's TLS evidence into the view.
	var certCN string
	if err := pool.QueryRow(ctx,
		`SELECT coalesce(cert_subject_cn, '') FROM search_index WHERE asset_value = '203.0.113.10' AND port = 443`).Scan(&certCN); err != nil {
		t.Fatalf("search_index row 203.0.113.10/443: %v", err)
	}
	if certCN != "a.example.com" {
		t.Errorf("203.0.113.10/443 cert_subject_cn = %q, want %q (corpus o1 tls evidence)", certCN, "a.example.com")
	}

	// --- Second Load: idempotent replay inserts nothing. -------------------
	stats2, err := Load(ctx, store)
	if err != nil {
		t.Fatalf("Load #2 (replay): %v", err)
	}
	if stats2.AssetsInserted != 0 || stats2.ServicesInserted != 0 || stats2.ObservationsInserted != 0 {
		t.Errorf("Load #2 inserted counters = (assets %d, services %d, observations %d), want all 0 (L1/L2 idempotency)",
			stats2.AssetsInserted, stats2.ServicesInserted, stats2.ObservationsInserted)
	}
	countChecks(t, pool, []struct {
		label, sql string
		want       int64
	}{
		{"assets after replay", `SELECT count(*) FROM assets`, 4},
		{"services after replay", `SELECT count(*) FROM services`, 6},
		{"observations after replay", `SELECT count(*) FROM observations`, 7},
		{"search_index after replay", `SELECT count(*) FROM search_index`, 7},
	})

	// --- Real-observation protection: a non-fixture source lands a row. ---
	var ctEvalID int64
	err = pool.QueryRow(ctx,
		`INSERT INTO data_sources (name, connector, terms_ref, enabled)
VALUES ('ct-eval', 'ct', 'test-eval-source', false) RETURNING id`).Scan(&ctEvalID)
	if err != nil {
		t.Fatalf("register ct-eval data source: %v", err)
	}

	ip := "203.0.113.10" // the asset the fixture corpus observed — must merge, not duplicate
	real := postgres.Fact{
		Asset: storage.Asset{
			Kind:    storage.AssetKindIP,
			Value:   ip,
			IP:      &ip,
			Sources: []string{"ct-eval"},
		},
		Service: &storage.Service{Port: 444, Transport: "tcp", Protocol: "banner"},
		Observation: storage.Observation{
			// AssetID stays 0: IngestFact resolves it from the upserted asset.
			SourceID:     ctEvalID,
			SourceCursor: "ct://real-1",
			ObservedAt:   time.Now().UTC(),
			PayloadHash:  "real-hash-1",
			Provenance:   "ct",
			ID:           987654321,
			Evidence:     map[string]any{"banner": map[string]any{"raw": "REAL"}},
		},
	}
	res, err := store.IngestFact(ctx, &real)
	if err != nil {
		t.Fatalf("IngestFact (real ct observation): %v", err)
	}
	if !res.ObservationInserted {
		t.Errorf("real observation inserted = false, want true (a fresh L1 delivery identity must insert)")
	}

	// --- Purge: exactly the fixture rows go, the real row survives. --------
	purge, err := postgres.PurgeFixtureData(ctx, pool)
	if err != nil {
		t.Fatalf("PurgeFixtureData: %v", err)
	}
	if !purge.FixtureSourcePresent {
		t.Errorf("purge.FixtureSourcePresent = false, want true (the registry row is configuration, not data)")
	}
	if purge.Observations != 7 {
		t.Errorf("purge.Observations = %d, want 7 (every fixture observation)", purge.Observations)
	}
	if purge.Services != 6 {
		t.Errorf("purge.Services = %d, want 6 (the ct-eval service on :444 survives)", purge.Services)
	}
	if purge.Assets != 3 {
		t.Errorf("purge.Assets = %d, want 3 (203.0.113.10 survives: live ct observation + :444 service)", purge.Assets)
	}

	// --- Post-purge SQL: only the real row remains, 'fixture' is stripped --
	countChecks(t, pool, []struct {
		label, sql string
		want       int64
	}{
		{"observations after purge", `SELECT count(*) FROM observations`, 1},
		{"fixture provenance after purge", `SELECT count(*) FROM observations WHERE provenance = 'fixture'`, 0},
		{"services after purge", `SELECT count(*) FROM services`, 1},
		{"services on :444 after purge", `SELECT count(*) FROM services WHERE port = 444`, 1},
		{"assets after purge", `SELECT count(*) FROM assets`, 1},
		{"surviving asset value", `SELECT count(*) FROM assets WHERE value = '203.0.113.10'`, 1},
		{"assets still tagged 'fixture'", `SELECT count(*) FROM assets WHERE 'fixture' = ANY(sources)`, 0},
		{"assets tagged 'ct-eval'", `SELECT count(*) FROM assets WHERE 'ct-eval' = ANY(sources)`, 1},
		{"search_index after purge", `SELECT count(*) FROM search_index`, 1},
	})

	var prov, cursor string
	if err := pool.QueryRow(ctx,
		`SELECT provenance, source_cursor FROM observations`).Scan(&prov, &cursor); err != nil {
		t.Fatalf("surviving observation: %v", err)
	}
	if prov != "ct" || cursor != "ct://real-1" {
		t.Errorf("surviving observation = (%q, %q), want (ct, ct://real-1)", prov, cursor)
	}

	// --- Second purge: nothing left to remove, registry row still there. ---
	purge2, err := postgres.PurgeFixtureData(ctx, pool)
	if err != nil {
		t.Fatalf("PurgeFixtureData #2: %v", err)
	}
	if purge2.Observations != 0 || purge2.Services != 0 || purge2.Assets != 0 {
		t.Errorf("purge #2 counters = (observations %d, services %d, assets %d), want all 0",
			purge2.Observations, purge2.Services, purge2.Assets)
	}
	if !purge2.FixtureSourcePresent {
		t.Errorf("purge #2 FixtureSourcePresent = false, want true (registry row is never deleted)")
	}
}

// TestPurgeWithoutFixtureSource — a database that never saw the fixture
// loader purges as a no-op success: no error, nothing removed, and
// FixtureSourcePresent=false because the registry row never existed.
func TestPurgeWithoutFixtureSource(t *testing.T) {
	pool, _, cleanup := testPool(t)
	defer cleanup()

	purge, err := postgres.PurgeFixtureData(context.Background(), pool)
	if err != nil {
		t.Fatalf("PurgeFixtureData on fixture-less DB: %v — want a no-op success, not an error", err)
	}
	if purge.FixtureSourcePresent {
		t.Errorf("FixtureSourcePresent = true, want false (no fixture registry row was ever created)")
	}
	if purge.Observations != 0 || purge.Services != 0 || purge.Assets != 0 {
		t.Errorf("purge counters = (observations %d, services %d, assets %d), want all 0",
			purge.Observations, purge.Services, purge.Assets)
	}
}
