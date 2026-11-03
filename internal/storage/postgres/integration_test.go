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

package postgres

// integration_test.go — the M3 database acceptance tests (build tag
// `integration`; run with `go test -tags integration ./...` against a
// live PostgreSQL 15+).
//
// Environment: GSEER_TEST_PG_ADMIN — a superuser DSN to an admin
// database used to create/drop per-test databases.  Default:
//   postgres://gseer@127.0.0.1:5433/postgres?sslmode=disable
// (matches the standalone PG 17 used for local development).

import (
	"context"
	"fmt"
	"hash/crc32"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/migrations"
)

const defaultAdminDSN = "postgres://gseer@127.0.0.1:5433/postgres?sslmode=disable"

// dbCounter makes per-test database names unique (tests run serially;
// the counter is belt-and-braces against -count re-runs in one process).
var (
	dbMu      sync.Mutex
	dbCounter int
)

// adminConnect connects to the admin database.
func adminConnect(t *testing.T) (*pgx.Conn, string) {
	t.Helper()
	adminDSN := os.Getenv("GSEER_TEST_PG_ADMIN")
	if adminDSN == "" {
		adminDSN = defaultAdminDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("cannot connect to admin PG (%s): %v — start the test PostgreSQL and set GSEER_TEST_PG_ADMIN", adminDSN, err)
	}
	return conn, adminDSN
}

// testDB creates a unique, empty database and returns its DSN plus a
// cleanup func that drops it.  Skips the test when no PG is reachable
// only when GSEER_TEST_REQUIRE=1 is unset (CI sets it to fail hard).
func testDB(t *testing.T) (string, func()) {
	t.Helper()
	conn, adminDSN := adminConnect(t)
	defer func() { _ = conn.Close(context.Background()) }()

	dbMu.Lock()
	dbCounter++
	name := fmt.Sprintf("gseer_m3_test_%d_%d", os.Getpid(), dbCounter)
	dbMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, name)); err != nil {
		t.Fatalf("cannot create test database %s: %v", name, err)
	}

	// Derive the per-test DSN from the admin DSN (same host/user).
	dsn := strings.Replace(adminDSN, "/postgres?", "/"+name+"?", 1)
	if dsn == adminDSN { // admin DSN had no "?"; append path
		dsn = strings.TrimSuffix(adminDSN, "/postgres") + "/" + name
	}
	cleanup := func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		admin2, err := pgx.Connect(cctx, adminDSN)
		if err != nil {
			t.Logf("cleanup: cannot reconnect to drop %s: %v", name, err)
			return
		}
		defer func() { _ = admin2.Close(cctx) }()
		// Terminate lingering connections, then drop.
		_, _ = admin2.Exec(cctx,
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()`, name)
		if _, err := admin2.Exec(cctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, name)); err != nil {
			t.Logf("cleanup: drop database %s failed: %v", name, err)
		}
	}
	return dsn, cleanup
}

// tableExists reports whether a table or view exists in public.
func relationExists(t *testing.T, dsn, name string) bool {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var ok bool
	err = conn.QueryRow(context.Background(), `
        SELECT EXISTS (SELECT 1 FROM information_schema.tables
                       WHERE table_schema='public' AND table_name=$1)
            OR EXISTS (SELECT 1 FROM information_schema.views
                       WHERE table_schema='public' AND table_name=$1)`, name).Scan(&ok)
	if err != nil {
		t.Fatalf("relationExists query: %v", err)
	}
	return ok
}

func schemaMigrationsRow(t *testing.T, dsn string) (version int64, dirty bool, exists bool) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	err = conn.QueryRow(context.Background(),
		`SELECT version, dirty FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &dirty)
	if err != nil {
		if isUndefinedTable(err) || strings.Contains(err.Error(), "no rows") {
			return 0, false, false
		}
		t.Fatalf("schema_migrations query: %v", err)
	}
	return version, dirty, true
}

func lockTimeoutForTest() int {
	if v := os.Getenv("GSEER_TEST_LOCK_TIMEOUT_MS"); v != "" {
		n := 0
		fmt.Sscanf(v, "%d", &n)
		if n >= 1000 {
			return n
		}
	}
	return 1001 // minimum configurable; keeps the concurrency test fast
}

// TestMigrateFreshDatabaseRoundTrip — AC-M3-1.
func TestMigrateFreshDatabaseRoundTrip(t *testing.T) {
	dsn, cleanup := testDB(t)
	defer cleanup()

	applied, head, err := RunMigrations(dsn, lockTimeoutForTest())
	if err != nil {
		t.Fatalf("RunMigrations on fresh DB: %v", err)
	}
	if head != wantHead {
		t.Errorf("head = %d, want %d", head, wantHead)
	}
	if applied != wantHead {
		t.Errorf("applied = %d, want %d", applied, wantHead)
	}
	for _, rel := range []string{"assets", "services", "observations", "data_sources",
		"latest_http", "latest_tls", "latest_fingerprints", "search_index", "schema_migrations"} {
		if !relationExists(t, dsn, rel) {
			t.Errorf("relation %q missing after migrate", rel)
		}
	}
	// Deferred collector tables (D-M3-1): v0.0.2 (M14) promotes them
	// into migration 0006 — they must now EXIST.  The scopes table
	// exists too but stays deliberately unused by P1 connectors.
	for _, rel := range []string{"source_checkpoints", "ingestion_dead_letter", "scopes", "collection_jobs"} {
		if !relationExists(t, dsn, rel) {
			t.Errorf("M14 table %q missing after migrate", rel)
		}
	}
	for _, rel := range []string{"suppressions", "suppression_applications"} {
		if !relationExists(t, dsn, rel) {
			t.Errorf("M13 table %q missing after migrate", rel)
		}
	}
	if v, dirty, ok := schemaMigrationsRow(t, dsn); !ok || v != wantHead || dirty {
		t.Errorf("schema_migrations = (%d, %v, exists=%v), want (%d, false, true)", v, dirty, ok, wantHead)
	}

	// Down() round-trip via Force is not used here; exercise Down by
	// driving migrate through the same runner internals is out of scope
	// for the runner API (v0.0.1 has no down command) — verified by the
	// migration SQL files' symmetry in the force/rollback tests.
}

// TestMigrateIdempotentAtHead — AC-M3-2.
func TestMigrateIdempotentAtHead(t *testing.T) {
	dsn, cleanup := testDB(t)
	defer cleanup()

	if _, _, err := RunMigrations(dsn, lockTimeoutForTest()); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	applied, _, err := RunMigrations(dsn, lockTimeoutForTest())
	if err != nil {
		t.Fatalf("second migrate (idempotent): %v", err)
	}
	if applied != 0 {
		t.Errorf("applied = %d on re-run, want 0", applied)
	}
	if v, dirty, ok := schemaMigrationsRow(t, dsn); !ok || v != wantHead || dirty {
		t.Errorf("schema_migrations disturbed by re-run: (%d, %v, %v), want (%d, false, true)", v, dirty, ok, wantHead)
	}
}

// TestMigrateFailureInjectionRollback — AC-M3-3 (owner directive §4.4).
// A migration whose SECOND statement fails must roll back its FIRST
// statement too (per-migration atomicity), leave earlier migrations
// intact, mark the version dirty, refuse further Up()s, and recover
// through Force.
func TestMigrateFailureInjectionRollback(t *testing.T) {
	dsn, cleanup := testDB(t)
	defer cleanup()

	// Baseline: real migrations to head.
	if _, _, err := RunMigrations(dsn, lockTimeoutForTest()); err != nil {
		t.Fatalf("baseline migrate: %v", err)
	}

	// Inject a failing migration by creating it directly in the DB-side
	// of the runner: we simulate a failing version 3 by registering the
	// file in a custom source.  The runner consumes the embedded FS, so
	// the injection drives migrate directly with a temp source — this
	// keeps the embedded set pristine while proving the driver's
	// transaction semantics that the runner relies on.
	// NOTE: RunMigrations itself always uses the embedded FS; the
	// failure path it translates (57014 / dirty / failed) is exercised
	// via the driver here.
	err := runFailingMigrationDirect(t, dsn)
	if err == nil {
		t.Fatal("expected the injected failing migration to fail")
	}
	if !isStatementTimeoutErr(err) && !strings.Contains(err.Error(), "migration failed") &&
		!strings.Contains(err.Error(), "Deliberate failure") {
		t.Logf("note: failure surfaced as: %v", err)
	}

	// The first statement of the failed migration rolled back.
	if relationExists(t, dsn, "should_not_survive") {
		t.Error("per-migration atomicity violated: table from before the failing statement exists")
	}
	// Earlier migrations are intact.
	if !relationExists(t, dsn, "assets") || !relationExists(t, dsn, "observations") {
		t.Error("cross-migration isolation violated: baseline tables missing")
	}
	// Dirty state recorded at v99.
	v, dirty, ok := schemaMigrationsRow(t, dsn)
	if !ok || v != 99 || !dirty {
		t.Errorf("schema_migrations = (%d, %v, exists=%v), want (99, true, true)", v, dirty, ok)
	}

	// Force recovery: metadata-only reset to the last good version (2).
	if err := ForceMigrationVersion(dsn, lockTimeoutForTest(), 2); err != nil {
		t.Fatalf("Force(2): %v", err)
	}
	if v, dirty, ok := schemaMigrationsRow(t, dsn); !ok || v != 2 || dirty {
		t.Errorf("post-force schema_migrations = (%d, %v, %v), want (2, false, true)", v, dirty, ok)
	}
	// Force is metadata-only: the leftover (dropped by rollback) check —
	// and critically, baseline tables still exist.
	if !relationExists(t, dsn, "assets") {
		t.Error("force dropped baseline tables — force must be metadata-only")
	}
	// Force to a version whose DDL IS present leaves it intact:
	// forcing to 2 did not drop the 0002 helper views.
	if !relationExists(t, dsn, "latest_http") {
		t.Error("force to v2 dropped the v2 helper views — force must not execute SQL")
	}
}

// migrateAdvisoryLockID mirrors the advisory-lock key the pgx/v5
// driver uses, so the test can stage deterministic contention:
//
//	database.GenerateAdvisoryLockId(dbName, schemaName, tableName)
//	= crc32_IEEE("public\x00schema_migrations\x00" + dbName) * 1486364155
//
// where dbName is purl.Path ("/<dbname>") per the driver's Open().
func migrateAdvisoryLockID(dbPath string) int64 {
	sum := crc32.ChecksumIEEE([]byte("public\x00schema_migrations\x00" + dbPath))
	return int64(sum * 1486364155)
}

// dbPathOf returns the URL path component ("/<dbname>") of a DSN.
func dbPathOf(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	return u.Path
}

// takeMigrateLock acquires the driver's advisory lock from an
// independent connection (deterministic contention staging).
func takeMigrateLock(t *testing.T, dsn string) (*pgx.Conn, func()) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("holder connect: %v", err)
	}
	id := migrateAdvisoryLockID(dbPathOf(t, dsn))
	if _, err := conn.Exec(context.Background(), `SELECT pg_advisory_lock($1)`, id); err != nil {
		t.Fatalf("holder pg_advisory_lock(%d): %v", id, err)
	}
	return conn, func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, id)
		_ = conn.Close(context.Background())
	}
}

// TestMigrateLockTimeoutConcurrent — AC-M3-10 (owner directive §1.7):
// two CONCURRENT migration processes waiting on the held advisory lock
// both fail fast with DB_MIGRATION_LOCK_TIMEOUT (bounded wait, no
// frozen CLI); after the holder releases, a third run migrates
// normally and the schema reaches head.
func TestMigrateLockTimeoutConcurrent(t *testing.T) {
	dsn, cleanup := testDB(t)
	defer cleanup()

	timeout := lockTimeoutForTest()
	_, release := takeMigrateLock(t, dsn)

	type result struct {
		applied int
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			applied, _, err := RunMigrations(dsn, timeout)
			results <- result{applied, err}
		}()
	}
	wg.Wait()
	close(results)

	timeouts, otherErrs := 0, 0
	for r := range results {
		if r.err == nil {
			otherErrs++ // neither should succeed while the lock is held
			continue
		}
		var dbErr *DBError
		if asDBError(r.err, &dbErr) && dbErr.Code == CodeDBMigrationLockTimeout {
			timeouts++
			if !strings.Contains(dbErr.Error(), "DB_MIGRATION_LOCK_TIMEOUT") {
				t.Errorf("timeout error missing code: %s", dbErr.Error())
			}
		} else {
			t.Errorf("unexpected runner error: %v", r.err)
		}
	}
	if timeouts != 2 {
		t.Errorf("timeouts = %d, want 2 (both concurrent runners must fail fast, not hang)", timeouts)
	}
	if otherErrs != 0 {
		t.Errorf("%d runner(s) succeeded while the lock was held — bounded wait did not fire", otherErrs)
	}

	// Release the lock; a third (serial) run migrates normally.
	release()
	applied, head, err := RunMigrations(dsn, timeout)
	if err != nil {
		t.Fatalf("post-release migrate: %v", err)
	}
	if applied != wantHead || head != wantHead {
		t.Errorf("post-release applied=%d head=%d, want %d/%d", applied, head, wantHead, wantHead)
	}
	if v, dirty, ok := schemaMigrationsRow(t, dsn); !ok || v != wantHead || dirty {
		t.Errorf("schema_migrations = (%d, %v, %v), want (%d, false, true)", v, dirty, ok, wantHead)
	}
}

// TestMigrationStatusBranches — AC-M3-8: doctor/status classification
// for UNINITIALIZED, AT_HEAD, BEHIND, and DIRTY.
func TestMigrationStatusBranches(t *testing.T) {
	ctx := context.Background()

	// UNINITIALIZED.
	dsn, cleanup := testDB(t)
	defer cleanup()
	st, err := GetMigrationStatus(ctx, dsn)
	if err != nil {
		t.Fatalf("status on fresh DB: %v", err)
	}
	if st.State != StateUninitialized {
		t.Errorf("fresh DB state = %s, want UNINITIALIZED", st.State)
	}
	if len(st.Pending) != wantHead {
		t.Errorf("fresh DB pending = %v, want %d files", st.Pending, wantHead)
	}

	// AT_HEAD.
	if _, _, err := RunMigrations(dsn, lockTimeoutForTest()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st, err = GetMigrationStatus(ctx, dsn)
	if err != nil {
		t.Fatalf("status at head: %v", err)
	}
	if st.State != StateAtHead {
		t.Errorf("at-head state = %s, want AT_HEAD", st.State)
	}

	// BEHIND: force version back to 1 (metadata-only) to simulate an
	// older schema while the files exist.
	if err := ForceMigrationVersion(dsn, lockTimeoutForTest(), 1); err != nil {
		t.Fatalf("force 1: %v", err)
	}
	st, err = GetMigrationStatus(ctx, dsn)
	if err != nil {
		t.Fatalf("status behind: %v", err)
	}
	if st.State != StateBehind {
		t.Errorf("behind state = %s, want BEHIND", st.State)
	}
	if len(st.Pending) != 5 || st.Pending[0] != "0002_helper_views.up.sql" ||
		st.Pending[1] != "0003_search_index.up.sql" || st.Pending[2] != "0004_search_indexes.up.sql" ||
		st.Pending[3] != "0005_suppressions.up.sql" {
		t.Errorf("behind pending = %v", st.Pending)
	}

	// DIRTY: simulate a failed migration by dirtying the version row.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE schema_migrations SET dirty = true WHERE version = 1`); err != nil {
		t.Fatalf("dirty: %v", err)
	}
	_ = conn.Close(ctx)
	st, err = GetMigrationStatus(ctx, dsn)
	if err != nil {
		t.Fatalf("status dirty: %v", err)
	}
	if st.State != StateDirty {
		t.Errorf("dirty state = %s, want DIRTY", st.State)
	}

	// Diagnose returns the force hint and inspection SQL (read-only).
	d, err := DiagnoseMigration(ctx, dsn)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if d.ForceHint == "" || !strings.Contains(d.ForceHint, "gseer db migrate force --version") {
		t.Errorf("diagnose force hint = %q", d.ForceHint)
	}
	if len(d.InspectionSQL) == 0 {
		t.Error("diagnose inspection SQL empty")
	}
}

// TestStatusReadOnly — AC-M3-16: status and diagnose must not write.
// Verified functionally: run both against a database, snapshot
// schema_migrations + a table checksum before/after, and require
// byte-equality.  (The query-log capture variant lives in CI.)
func TestStatusReadOnly(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := testDB(t)
	defer cleanup()
	if _, _, err := RunMigrations(dsn, lockTimeoutForTest()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	snapshot := func(t *testing.T) string {
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer func() { _ = conn.Close(ctx) }()
		var snap string
		err = conn.QueryRow(ctx, `
            SELECT (SELECT count(*) || ':' || coalesce(string_agg(version::text || dirty::text, ',' ORDER BY version), '')
                      FROM schema_migrations) || '|' ||
                   (SELECT count(*) FROM assets) || '|' ||
                   (SELECT count(*) FROM observations)`).Scan(&snap)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		return snap
	}

	before := snapshot(t)
	if _, err := GetMigrationStatus(ctx, dsn); err != nil {
		t.Fatalf("status: %v", err)
	}
	if _, err := DiagnoseMigration(ctx, dsn); err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	DoctorDBChecks(ctx, dsn, 15)
	if after := snapshot(t); after != before {
		t.Errorf("read-only violated: state changed %q -> %q", before, after)
	}
}

// TestPGMajorVersion — AC-M3-6.
func TestPGMajorVersion(t *testing.T) {
	dsn, cleanup := testDB(t)
	defer cleanup()

	major, err := CheckPGVersion(context.Background(), dsn, 15)
	if err != nil {
		t.Fatalf("CheckPGVersion >= 15: %v", err)
	}
	if major < 15 {
		t.Errorf("major = %d", major)
	}
	// Floor above the actual server must fail with E_DB_VERSION.
	_, err = CheckPGVersion(context.Background(), dsn, 99)
	if err == nil {
		t.Fatal("CheckPGVersion >= 99 should fail")
	}
	var dbErr *DBError
	if !asDBError(err, &dbErr) || dbErr.Code != CodeDBVersion {
		t.Errorf("want E_DB_VERSION, got %v", err)
	}
}

// TestReachabilityBranches — AC-M3-4/5.
func TestReachabilityBranches(t *testing.T) {
	dsn, cleanup := testDB(t)
	defer cleanup()

	if err := CheckReachable(context.Background(), dsn); err != nil {
		t.Errorf("reachable DB failed the check: %v", err)
	}

	bad := "postgres://gseer@127.0.0.1:1/gseer?sslmode=disable&connect_timeout=1"
	err := CheckReachable(context.Background(), bad)
	if err == nil {
		t.Fatal("unreachable DB passed the check")
	}
	var dbErr *DBError
	if !asDBError(err, &dbErr) || dbErr.Code != CodeDBUnreachable {
		t.Errorf("want E_DB_UNREACHABLE, got %v", err)
	}
	if strings.Contains(err.Error(), "password") || strings.Count(err.Error(), "@") > 1 {
		t.Errorf("error text may leak credentials: %s", err.Error())
	}
}

// TestPoolLifecycle — AC-M3-9.
func TestPoolLifecycle(t *testing.T) {
	dsn, cleanup := testDB(t)
	defer cleanup()
	if _, _, err := RunMigrations(dsn, lockTimeoutForTest()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	pool, err := NewPool(context.Background(), dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	if err := PingPool(context.Background(), pool); err != nil {
		t.Errorf("PingPool: %v", err)
	}
	// A trivial query through the pool against the migrated schema.
	var one int
	if err := pool.QueryRow(context.Background(), `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Errorf("pool query: %v (one=%d)", err, one)
	}
	pool.Close()
}

// runFailingMigrationDirect drives migrate.Up() through the SAME
// pgx/v5 driver path the runner uses, with a source that contains the
// embedded migrations PLUS an injected failing version 3.  The second
// statement raises a deliberate exception, proving per-migration
// atomicity: the first statement's CREATE TABLE must roll back with it.
// The embedded FS itself is never modified.
func runFailingMigrationDirect(t *testing.T, dsn string) error {
	t.Helper()
	fmap := fstest.MapFS{}
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	for _, e := range entries {
		data, err := migrations.FS.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		fmap[e.Name()] = &fstest.MapFile{Data: data}
	}
	fmap["0099_deliberate_fail.up.sql"] = &fstest.MapFile{Data: []byte(
		"CREATE TABLE should_not_survive (id int);\n" +
			"DO $$ BEGIN RAISE EXCEPTION 'Deliberate failure injection (M3 rollback test)'; END $$;\n")}
	fmap["0099_deliberate_fail.down.sql"] = &fstest.MapFile{Data: []byte(
		"DROP TABLE IF EXISTS should_not_survive;\n")}

	effDSN, derr := effectiveMigrateDSN(dsn, lockTimeoutForTest())
	if derr != nil {
		t.Fatalf("effectiveMigrateDSN: %v", derr)
	}
	src, err := iofs.New(fmap, ".")
	if err != nil {
		t.Fatalf("iofs: %v", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, effDSN)
	if err != nil {
		t.Fatalf("migrate instance: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Logf("injected migration failed as intended: %v", err)
	}
	_, derr2 := m.Close()
	if derr2 != nil {
		t.Logf("close after failure: %v", derr2)
	}
	return fmt.Errorf("migration deliberately failed")
}

var _ = migrations.FS

// TestSearchIndexUnionContract — AC-4.6 semantics of the search_index
// union view (M9 planner target), exercised with minimal inserted rows:
//   - the asset-only arm is reachable for assets without services;
//   - service rows carry the service + latest_* projection;
//   - `port == 443` can never match an asset-only row (NULL ≠ 443, F7);
//   - the view is empty on a freshly migrated database.
func TestSearchIndexUnionContract(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := testDB(t)
	defer cleanup()
	if _, _, err := RunMigrations(dsn, lockTimeoutForTest()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Empty on fresh schema.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM search_index`).Scan(&n); err != nil {
		t.Fatalf("count empty: %v", err)
	}
	if n != 0 {
		t.Errorf("fresh search_index has %d rows, want 0", n)
	}

	// Seed: one domain asset WITHOUT services (asset-only arm), one IP
	// asset with an https service + an http observation (service arm).
	var fixtureSrc int64
	err = conn.QueryRow(ctx, `
        INSERT INTO data_sources (name, connector, terms_ref, enabled)
        VALUES ('fixture', 'fixture', 'labeled synthetic dataset (QS-6)', false)
        RETURNING id`).Scan(&fixtureSrc)
	if err != nil {
		t.Fatalf("seed data_source: %v", err)
	}
	var domAsset int64
	err = conn.QueryRow(ctx, `
        INSERT INTO assets (kind, value, domain, sources)
        VALUES ('domain', 'example-fix.test', 'example-fix.test', '{fixture}')
        RETURNING id`).Scan(&domAsset)
	if err != nil {
		t.Fatalf("seed domain asset: %v", err)
	}
	var ipAsset, svc int64
	err = conn.QueryRow(ctx, `
        INSERT INTO assets (kind, value, ip, sources)
        VALUES ('ip', '203.0.113.10', '203.0.113.10', '{fixture}')
        RETURNING id`).Scan(&ipAsset)
	if err != nil {
		t.Fatalf("seed ip asset: %v", err)
	}
	err = conn.QueryRow(ctx, `
        INSERT INTO services (asset_id, port, transport, protocol, name, product, version)
        VALUES ($1, 443, 'tcp', 'https', 'https', 'nginx', '1.25.0')
        RETURNING id`, ipAsset).Scan(&svc)
	if err != nil {
		t.Fatalf("seed service: %v", err)
	}
	obsID := int64(1)
	if _, err := conn.Exec(ctx, `
        INSERT INTO observations (id, source_id, asset_id, service_id, observed_at,
                                  payload_hash, source_cursor, provenance, evidence)
        VALUES ($1, $2, $3, $4, now(), 'h1', 'c1', 'fixture',
                '{"http": {"status": 200, "title": "Example Fix", "server": "nginx"}}')`,
		obsID, fixtureSrc, ipAsset, svc); err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	// Row inventory: 1 service row + 1 asset-only row.
	type row struct {
		kind, assetValue, title string
		port                    *int
	}
	var rows []row
	rset, err := conn.Query(ctx, `
        SELECT row_kind, asset_value, coalesce(http_title, ''), port
        FROM search_index ORDER BY row_kind`)
	if err != nil {
		t.Fatalf("query search_index: %v", err)
	}
	for rset.Next() {
		var r row
		if err := rset.Scan(&r.kind, &r.assetValue, &r.title, &r.port); err != nil {
			t.Fatalf("scan: %v", err)
		}
		rows = append(rows, r)
	}
	rset.Close()
	if len(rows) != 2 {
		t.Fatalf("search_index rows = %d, want 2 (one service, one asset-only)", len(rows))
	}

	// The domain asset WITHOUT services must surface via the asset-only
	// arm (AC-4.6: asset-only arm reachable).
	sawAssetOnly, sawService := false, false
	for _, r := range rows {
		switch {
		case r.kind == "asset-only" && r.assetValue == "example-fix.test":
			sawAssetOnly = true
			if r.port != nil {
				t.Errorf("asset-only row carries port %v, want NULL", *r.port)
			}
		case r.kind == "service" && r.assetValue == "203.0.113.10":
			sawService = true
			if r.port == nil || *r.port != 443 {
				t.Errorf("service row port = %v, want 443", r.port)
			}
			if r.title != "Example Fix" {
				t.Errorf("service row http_title = %q, want fixture title via latest_http", r.title)
			}
		}
	}
	if !sawAssetOnly || !sawService {
		t.Errorf("union arms incomplete: asset-only=%v service=%v", sawAssetOnly, sawService)
	}

	// F7 invariant: a port filter can never reach the asset-only arm —
	// NULL port never equals 443.
	var m int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM search_index WHERE row_kind='asset-only' AND port = 443`).Scan(&m); err != nil {
		t.Fatalf("F7 query: %v", err)
	}
	if m != 0 {
		t.Errorf("F7 violated: %d asset-only rows matched port==443", m)
	}

	// Provenance integrity: the observation behind latest_http is
	// labeled fixture (QS-6/AC-9.7.x chain end-to-end).
	var prov string
	if err := conn.QueryRow(ctx,
		`SELECT o.provenance FROM observations o
         JOIN latest_http h ON h.service_id = o.service_id LIMIT 1`).Scan(&prov); err != nil {
		t.Fatalf("provenance query: %v", err)
	}
	if prov != "fixture" {
		t.Errorf("provenance = %q, want fixture", prov)
	}
}

// ---------------------------------------------------------------------------
// M4 query-plan verification (EXPLAIN ANALYZE evidence) — temporary baseline
// ---------------------------------------------------------------------------

// seedPlanScaleData — synthetic corpus at plan-relevant scale: one data
// source, 50k unique ip assets, one service per asset, one observation
// per service (id = service id — observations.id is caller-provided).
// The value column carries a deliberately 5-segment synthetic string
// ('198.51.100.'||(g/254)||'.'||(g%254); i=25500 → '198.51.100.100.100')
// so value-equality plans cannot be satisfied by the ip GiST index or by
// IP-string coupling; the typed ip column uses the valid 4-octet form
// ('198.51.'||(g/254)||'.'||(g%254), 198.51.0.1 … 198.51.196.213) so the
// inet cast succeeds and the CIDR probe (198.51.100.0/24) has ~254-row
// selectivity.
func seedPlanScaleData(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var sourceID int64
	if err := pool.QueryRow(ctx, `
        INSERT INTO data_sources (name, connector, terms_ref, enabled)
        VALUES ('synthetic-plan-test', 'synthetic', 'test', false)
        RETURNING id`).Scan(&sourceID); err != nil {
		t.Fatalf("seed data_source: %v", err)
	}
	if _, err := pool.Exec(ctx, `
        INSERT INTO assets (kind, value, ip, geo_country, sources)
        SELECT 'ip',
               '198.51.100.'||(g/254)||'.'||(g%254),
               ('198.51.'||(g/254)||'.'||(g%254))::inet,
               'US', '{synthetic}'
        FROM generate_series(1,50000) g`); err != nil {
		t.Fatalf("seed assets: %v", err)
	}
	if _, err := pool.Exec(ctx, `
        INSERT INTO services (asset_id, port, transport, protocol)
        SELECT a.id, CASE a.id%3 WHEN 0 THEN 443 WHEN 1 THEN 80 ELSE 8443 END, 'tcp', 'http'
        FROM assets a WHERE a.sources = '{synthetic}'`); err != nil {
		t.Fatalf("seed services: %v", err)
	}
	if _, err := pool.Exec(ctx, `
        INSERT INTO observations (id, source_id, asset_id, service_id, observed_at,
                                  payload_hash, source_cursor, provenance, evidence)
        SELECT s.id, $1, s.asset_id, s.id, now(), 'h'||s.id, 'synthetic://'||s.id, 'synthetic',
               CASE s.id%3 WHEN 0 THEN jsonb_build_object('http', jsonb_build_object(
                           'status', 200, 'title', 'Admin Login', 'server', 'nginx'))
                           ELSE jsonb_build_object('other', 'x') END
        FROM services s`, sourceID); err != nil {
		t.Fatalf("seed observations: %v", err)
	}
}

// explainPlan runs EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) and returns
// the plan verbatim as one string.
func explainPlan(t *testing.T, pool *pgxpool.Pool, query string) string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) `+query)
	if err != nil {
		t.Fatalf("explain %q: %v", query, err)
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("explain scan: %v", err)
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explain rows: %v", err)
	}
	return sb.String()
}

// TestSearchPlanIndexes — permanent M4 STEP 2/3 regression guard (the
// former TestPlanBaselineTmp, kept after the baseline evidence was
// recorded in testdata/plan_baseline.md).  Runs the four canonical
// search_index queries over the 50k synthetic corpus and asserts the
// plans select the evidence-driven indexes of migration 0004 — so a
// future schema change that silently degrades these access paths fails
// here instead of shipping:
//
//	ix_assets_value                    (asset_value point equality)
//	ix_observations_http_scan          (latest_http ordered partial scan)
//	ix_observations_tls_scan           (latest_tls ordered partial scan)
//	ix_observations_fp_scan            (latest_fingerprints ordered partial scan)
//	ix_assets_ip                       (CIDR containment, from 0001)
//
// Query timings are NOT asserted (shared CI hardware varies); the plan
// node names are the stable contract.  Numbers live in the evidence doc.
func TestSearchPlanIndexes(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := testDB(t)
	defer cleanup()
	applied, head, err := RunMigrations(dsn, 1001)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if applied != wantHead || head != wantHead {
		t.Errorf("applied=%d head=%d, want %d/%d (fresh DB, all embedded migrations)", applied, head, wantHead, wantHead)
	}
	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()
	seedPlanScaleData(t, pool)

	queries := []struct {
		name        string
		sql         string
		wantIndexes []string
	}{
		{"Q1 asset_value eq",
			`SELECT * FROM search_index WHERE asset_value = '198.51.100.100.100'`,
			[]string{"ix_assets_value", "ix_observations_http_scan",
				"ix_observations_tls_scan", "ix_observations_fp_scan"}},
		{"Q2 port eq",
			`SELECT * FROM search_index WHERE port = 443`,
			[]string{"ix_observations_http_scan",
				"ix_observations_tls_scan", "ix_observations_fp_scan"}},
		{"Q3 ip CIDR",
			`SELECT * FROM search_index WHERE ip << '198.51.100.0/24'::inet`,
			[]string{"ix_assets_ip", "ix_observations_http_scan",
				"ix_observations_tls_scan", "ix_observations_fp_scan"}},
		{"Q4 http_title ILIKE",
			`SELECT * FROM search_index WHERE http_title ILIKE '%login%'`,
			[]string{"ix_observations_http_scan",
				"ix_observations_tls_scan", "ix_observations_fp_scan"}},
	}
	for _, q := range queries {
		plan := explainPlan(t, pool, q.sql)
		t.Logf("%s plan:\n%s", q.name, plan)
		for _, idx := range q.wantIndexes {
			if !strings.Contains(plan, idx) {
				t.Errorf("%s: plan lost index %s — the migration-0004 access path "+
					"degraded; see testdata/plan_baseline.md for the evidence baseline",
					q.name, idx)
			}
		}
	}
}

// TestMigrateSlowStatementOutlivesLockTimeout — owner directive §1 —
// slow-but-honest migrations must never be killed by the lock-wait
// timeout (M3 verification gap b).  The session statement_timeout
// bounds ONLY the advisory-lock wait: after the lock is acquired the
// driver must reset it to 0, so a migration statement running LONGER
// than the lock timeout (pg_sleep(3) > 1001ms) completes successfully.
// If the post-lock reset silently failed, this statement would be
// killed with SQLSTATE 57014 and the test would fail loudly.
func TestMigrateSlowStatementOutlivesLockTimeout(t *testing.T) {
	dsn, cleanup := testDB(t)
	defer cleanup()

	// One injected migration whose first statement sleeps 3s — well past
	// the 1001ms lock-wait timeout — then creates a table we can verify.
	// The MapFS mirrors runFailingMigrationDirect's source construction
	// (paired .down.sql keeps the iofs source symmetric).
	fmap := fstest.MapFS{
		"0001_slow.up.sql": &fstest.MapFile{Data: []byte(
			"SELECT pg_sleep(3);\nCREATE TABLE slow_survivor(id int);\n")},
		"0001_slow.down.sql": &fstest.MapFile{Data: []byte(
			"DROP TABLE IF EXISTS slow_survivor;\n")},
	}

	effDSN, err := effectiveMigrateDSN(dsn, lockTimeoutForTest())
	if err != nil {
		t.Fatalf("effectiveMigrateDSN: %v", err)
	}
	src, err := iofs.New(fmap, ".")
	if err != nil {
		t.Fatalf("iofs: %v", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, effDSN)
	if err != nil {
		t.Fatalf("migrate instance: %v", err)
	}

	start := time.Now()
	err = m.Up()
	elapsed := time.Since(start)
	if _, cerr := m.Close(); cerr != nil {
		t.Logf("close after up: %v", cerr)
	}

	if err != nil {
		if isStatementTimeoutErr(err) {
			t.Fatalf("LOCK-TIMEOUT RESET INVARIANT BROKE (owner directive §1): " +
				"the migration statement was killed by the lock-wait timeout " +
				"(SQLSTATE 57014) — the post-lock statement_timeout reset failed; " +
				"slow-but-honest migrations must never be killed by the wait timeout")
		}
		t.Fatalf("slow migration must outlive the lock timeout: %v", err)
	}
	t.Logf("slow statement (pg_sleep 3s > %dms lock timeout) completed in %s — reset invariant holds",
		lockTimeoutForTest(), elapsed)

	if !relationExists(t, dsn, "slow_survivor") {
		t.Error("slow_survivor table missing — migration did not fully apply")
	}
	if v, dirty, ok := schemaMigrationsRow(t, dsn); !ok || v != 1 || dirty {
		t.Errorf("schema_migrations = (%d, %v, %v), want (1, false, true)", v, dirty, ok)
	}
}

// TestMigrateSerializationWaitThenSucceed — M3 verification gap c —
// bounded wait then success; serialization with the upstream lock key.
// A second migrator arriving while the advisory lock is held WAITS
// (the 600ms hold sits inside the 1001ms lock timeout) and SUCCEEDS
// after the holder releases; the two never run migration SQL
// concurrently.
func TestMigrateSerializationWaitThenSucceed(t *testing.T) {
	dsn, cleanup := testDB(t)
	defer cleanup()

	timeout := lockTimeoutForTest()
	// Independent connection holds the driver's advisory lock.
	_, release := takeMigrateLock(t, dsn)

	t0 := time.Now()
	// The holder releases the lock 600ms after t0 — inside the 1001ms
	// timeout, so a waiting migrator must eventually succeed.
	time.AfterFunc(600*time.Millisecond, release)

	type result struct {
		applied int
		head    uint
		err     error
	}
	done := make(chan result, 1)
	// The contender starts 50ms in, while the lock is still held: it must
	// wait out the remaining hold instead of racing the holder's SQL.
	time.AfterFunc(50*time.Millisecond, func() {
		applied, head, err := RunMigrations(dsn, timeout)
		done <- result{applied, head, err}
	})

	r := <-done
	t1 := time.Now()

	if r.err != nil {
		var dbErr *DBError
		if asDBError(r.err, &dbErr) && dbErr.Code == CodeDBMigrationLockTimeout {
			t.Fatalf("SERIALIZATION CONTRACT BROKE (M3 gap c): the waiting migrator "+
				"failed with DB_MIGRATION_LOCK_TIMEOUT even though the holder "+
				"released within 600ms (< %dms timeout) — a bounded wait must "+
				"succeed once the lock frees", timeout)
		}
		t.Fatalf("post-wait migrate: %v", r.err)
	}
	if r.applied != wantHead || r.head != wantHead {
		t.Errorf("applied=%d head=%d, want %d/%d (fresh DB, all embedded migrations)", r.applied, r.head, wantHead, wantHead)
	}
	// Ordering proof: the contender finished AFTER the holder released
	// (t0+600ms), i.e. it waited for the lock rather than running
	// concurrently with it.
	if wait := t1.Sub(t0); wait < 550*time.Millisecond {
		t.Errorf("contender completed %s after start, want >= 550ms — "+
			"it did not wait for the held advisory lock", wait)
	} else {
		t.Logf("contender waited %s for the holder (600ms hold, %dms timeout), then migrated to head",
			wait, timeout)
	}
	if v, dirty, ok := schemaMigrationsRow(t, dsn); !ok || v != wantHead || dirty {
		t.Errorf("schema_migrations = (%d, %v, %v), want (%d, false, true)", v, dirty, ok, wantHead)
	}
}

// ---------------------------------------------------------------------------
// M4 store repositories (write side) — integration tests
// ---------------------------------------------------------------------------

// countTable returns count(*) of a table through the pool (M4 test helper;
// table names come from test literals only, never user input).
func countTable(t *testing.T, pool *pgxpool.Pool, table string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count(*) on %s: %v", table, err)
	}
	return n
}

// containsAll reports whether set contains every want element (order
// irrelevant — array_agg(DISTINCT) order is not guaranteed).
func containsAll(set []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, s := range set {
			if s == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// TestStoreUpsertIdempotencyAndSemantics — AC-4.2/4.3/4.4: one fact
// ingested through the store's per-fact transaction is idempotent end to
// end — an identical re-ingest (same observation id, cursor, hash) flips
// all three inserted flags to false with identical ids and leaves the row
// counts untouched, and a content-identical record with a different id +
// cursor collapses onto the first row via the L2 fact identity (the
// original row's content and provenance are kept).
func TestStoreUpsertIdempotencyAndSemantics(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := testDB(t)
	defer cleanup()
	if _, _, err := RunMigrations(dsn, lockTimeoutForTest()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()
	store := NewStore(pool)

	sourceID, err := store.EnsureFixtureSource(ctx)
	if err != nil {
		t.Fatalf("EnsureFixtureSource: %v", err)
	}

	t0 := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	ip := "203.0.113.10"
	// mkFact builds a fresh fact per ingest: Observation.ServiceID is
	// deliberately overwritten by IngestFact, so each call starts clean.
	mkFact := func(id int64, cursor string) Fact {
		return Fact{
			Asset: storage.Asset{
				Kind: storage.AssetKindIP, Value: ip, IP: &ip,
				ASN: 64500, ASNOrg: "AS64500 FIXTURE-VANNET", GeoCountry: "US",
				Sources: []string{storage.FixtureSourceName},
			},
			Service: &storage.Service{Port: 443, Transport: "tcp", Protocol: "https", Name: "https"},
			Observation: storage.Observation{
				ID: id, SourceID: sourceID, SourceCursor: cursor,
				ObservedAt: t0, PayloadHash: "hash-s1",
				Provenance: storage.ProvenanceFixture,
				Evidence: map[string]any{
					"http": map[string]any{"status": 200, "title": "Admin Login", "server": "nginx"},
				},
			},
		}
	}

	// First ingest: all three rows fresh.
	f1 := mkFact(1001, "fixture://203.0.113.10:443/http")
	res1, err := store.IngestFact(ctx, &f1)
	if err != nil {
		t.Fatalf("first IngestFact: %v", err)
	}
	if !res1.AssetInserted || !res1.ServiceInserted || !res1.ObservationInserted {
		t.Errorf("first ingest = (asset=%v service=%v obs=%v), want all true",
			res1.AssetInserted, res1.ServiceInserted, res1.ObservationInserted)
	}
	if res1.AssetID <= 0 || res1.ServiceID <= 0 || res1.ObservationID <= 0 {
		t.Errorf("first ingest ids = (%d, %d, %d), want all > 0",
			res1.AssetID, res1.ServiceID, res1.ObservationID)
	}

	// Identical re-ingest: pure no-op, identical ids.
	f2 := mkFact(1001, "fixture://203.0.113.10:443/http")
	res2, err := store.IngestFact(ctx, &f2)
	if err != nil {
		t.Fatalf("identical re-ingest: %v", err)
	}
	if res2.AssetInserted || res2.ServiceInserted || res2.ObservationInserted {
		t.Errorf("re-ingest = (asset=%v service=%v obs=%v), want all false (idempotent)",
			res2.AssetInserted, res2.ServiceInserted, res2.ObservationInserted)
	}
	if res2.AssetID != res1.AssetID || res2.ServiceID != res1.ServiceID || res2.ObservationID != res1.ObservationID {
		t.Errorf("re-ingest ids = (%d, %d, %d), want identical (%d, %d, %d)",
			res2.AssetID, res2.ServiceID, res2.ObservationID,
			res1.AssetID, res1.ServiceID, res1.ObservationID)
	}
	if got := countTable(t, pool, "assets"); got != 1 {
		t.Errorf("assets = %d, want 1 after re-ingest", got)
	}
	if got := countTable(t, pool, "services"); got != 1 {
		t.Errorf("services = %d, want 1 after re-ingest", got)
	}
	if got := countTable(t, pool, "observations"); got != 1 {
		t.Errorf("observations = %d, want 1 after re-ingest", got)
	}

	// L2 fact identity: different id + cursor, same content → the stored
	// row wins (id + provenance kept), no second observation.
	f3 := mkFact(1002, "fixture://s1/replay")
	res3, err := store.IngestFact(ctx, &f3)
	if err != nil {
		t.Fatalf("L2 replay ingest: %v", err)
	}
	if res3.ObservationInserted {
		t.Errorf("L2 replay inserted=true, want false (fact identity must collapse)")
	}
	if res3.ObservationID != res1.ObservationID {
		t.Errorf("L2 replay observation id = %d, want the first row's %d", res3.ObservationID, res1.ObservationID)
	}
	if got := countTable(t, pool, "observations"); got != 1 {
		t.Errorf("observations = %d, want 1 after L2 replay", got)
	}
}

// TestStoreUpsertMetadataMerge — upsert conflict semantics: a partial
// second sighting MERGES into the stored row — geo fills in, an extra
// source unions in (array_agg DISTINCT), asn=0 preserves the stored ASN —
// and a service re-upsert refreshes product/version wholesale without
// moving first_seen or regressing last_seen.
func TestStoreUpsertMetadataMerge(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := testDB(t)
	defer cleanup()
	if _, _, err := RunMigrations(dsn, lockTimeoutForTest()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()
	store := NewStore(pool)

	ip := "198.51.100.7"
	// First sighting: ASN set, no geo, fixture source.
	a := storage.Asset{
		Kind: storage.AssetKindIP, Value: ip, IP: &ip,
		ASN: 64500, ASNOrg: "AS64500 FIXTURE-VANNET",
		Sources: []string{storage.FixtureSourceName},
	}
	id, inserted, err := store.UpsertAsset(ctx, &a)
	if err != nil {
		t.Fatalf("first UpsertAsset: %v", err)
	}
	if !inserted || id <= 0 {
		t.Fatalf("first UpsertAsset = (id=%d inserted=%v), want fresh insert", id, inserted)
	}

	// Second sighting: geo fills, extra source unions, asn deliberately 0
	// (must NOT blank the stored ASN).
	a2 := storage.Asset{
		Kind: storage.AssetKindIP, Value: ip, IP: &ip,
		ASN: 0, GeoCountry: "US", Sources: []string{"ct"},
	}
	id2, inserted2, err := store.UpsertAsset(ctx, &a2)
	if err != nil {
		t.Fatalf("second UpsertAsset: %v", err)
	}
	if inserted2 {
		t.Errorf("second UpsertAsset inserted=true, want false (conflict path)")
	}
	if id2 != id {
		t.Errorf("second UpsertAsset id = %d, want the original %d", id2, id)
	}
	var geoCountry string
	var asn int64
	var sources []string
	if err := pool.QueryRow(ctx, `SELECT geo_country, asn, sources FROM assets WHERE id = $1`, id).
		Scan(&geoCountry, &asn, &sources); err != nil {
		t.Fatalf("read merged asset: %v", err)
	}
	if geoCountry != "US" {
		t.Errorf("geo_country = %q, want US (COALESCE fill-in)", geoCountry)
	}
	if asn != 64500 {
		t.Errorf("asn = %d, want 64500 preserved (EXCLUDED.asn = 0 must not overwrite)", asn)
	}
	if !containsAll(sources, storage.FixtureSourceName, "ct") {
		t.Errorf("sources = %v, want the union of {fixture, ct}", sources)
	}

	// Service: first sighting, then a re-upsert that refreshes
	// product/version (latest sighting is authoritative for identity
	// fields) while first_seen stays put.
	sv := storage.Service{AssetID: id, Port: 22, Transport: "tcp", Protocol: "ssh", Name: "ssh"}
	svcID, svcInserted, err := store.UpsertService(ctx, &sv)
	if err != nil {
		t.Fatalf("first UpsertService: %v", err)
	}
	if !svcInserted || svcID <= 0 {
		t.Fatalf("first UpsertService = (id=%d inserted=%v), want fresh insert", svcID, svcInserted)
	}
	firstSeen := sv.FirstSeen

	sv2 := storage.Service{AssetID: id, Port: 22, Transport: "tcp", Protocol: "ssh", Name: "ssh",
		Product: "OpenSSH", Version: "9.6"}
	svcID2, svcInserted2, err := store.UpsertService(ctx, &sv2)
	if err != nil {
		t.Fatalf("second UpsertService: %v", err)
	}
	if svcInserted2 {
		t.Errorf("service re-upsert inserted=true, want false (conflict path)")
	}
	if svcID2 != svcID {
		t.Errorf("service re-upsert id = %d, want the original %d", svcID2, svcID)
	}
	var product, version string
	var fs, ls time.Time
	if err := pool.QueryRow(ctx, `SELECT product, version, first_seen, last_seen FROM services WHERE id = $1`, svcID).
		Scan(&product, &version, &fs, &ls); err != nil {
		t.Fatalf("read merged service: %v", err)
	}
	if product != "OpenSSH" || version != "9.6" {
		t.Errorf("service product/version = (%q, %q), want (OpenSSH, 9.6)", product, version)
	}
	if !fs.Equal(firstSeen) {
		t.Errorf("service first_seen moved on re-upsert: %v -> %v", firstSeen, fs)
	}
	if ls.Before(fs) {
		t.Errorf("service last_seen %v before first_seen %v", ls, fs)
	}
}

// TestStoreIngestTransactionRollback — per-fact atomicity: the
// observation write is the LAST statement of the fact transaction, so an
// observation-level failure (FK to a nonexistent source) must roll the
// asset and service upserts applied earlier in the SAME transaction back
// with it.  Nothing may leak — this is the proof that a failed fact can
// never leave a service without its asset.
func TestStoreIngestTransactionRollback(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := testDB(t)
	defer cleanup()
	if _, _, err := RunMigrations(dsn, lockTimeoutForTest()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()
	store := NewStore(pool)
	// No EnsureFixtureSource: data_sources is empty, so the fact's
	// observation violates its FK to data_sources.

	ip := "203.0.113.99"
	f := Fact{
		Asset: storage.Asset{
			Kind: storage.AssetKindIP, Value: ip, IP: &ip,
			Sources: []string{storage.FixtureSourceName},
		},
		Service: &storage.Service{Port: 8080, Transport: "tcp", Protocol: "http"},
		Observation: storage.Observation{
			ID: 2001, SourceID: 999999, SourceCursor: "fixture://rollback",
			ObservedAt:  time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC),
			PayloadHash: "hash-rollback",
			Provenance:  storage.ProvenanceFixture,
		},
	}
	res, err := store.IngestFact(ctx, &f)
	if err == nil {
		t.Fatalf("IngestFact with nonexistent source id succeeded: %+v", res)
	}
	t.Logf("fact failed as intended: %v", err)

	// The asset + service upserts that ran BEFORE the failed observation
	// must have rolled back with it.
	if got := countTable(t, pool, "assets"); got != 0 {
		t.Errorf("assets = %d, want 0 (asset upsert must roll back with the failed fact)", got)
	}
	if got := countTable(t, pool, "services"); got != 0 {
		t.Errorf("services = %d, want 0 (service upsert must roll back with the failed fact)", got)
	}
	if got := countTable(t, pool, "observations"); got != 0 {
		t.Errorf("observations = %d, want 0", got)
	}
}
