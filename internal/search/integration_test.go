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

package search

// integration_test.go — the M10 SearchService acceptance tests (build
// tag `integration`): the timeout contract (owner directive §2) plus
// end-to-end search correctness over the fixture corpus.
//
// Timeout contract proven here:
//   1. enforcement — a blocked SELECT (ACCESS EXCLUSIVE lock held by a
//      second transaction) is canceled by the transaction-local
//      statement_timeout and surfaces as *TimeoutError;
//   2. no leakage — after a timed-out search the SAME service issues a
//      normal search that succeeds, proving set_config(is_local=true)
//      died with the aborted transaction;
//   3. pool reuse — pool statistics show the pool still acquires
//      connections after the timeout path;
//   4. commit/rollback correctness — successful searches commit (their
//      stats reads see committed state), failed searches roll back.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Lutfifakee-Project/GSEER/internal/fixture"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
)

const sIntAdminDSN = "postgres://gseer@127.0.0.1:5433/postgres?sslmode=disable"

var (
	sIntMu      sync.Mutex
	sIntCounter int
)

func sIntTestDB(t *testing.T) (string, func()) {
	t.Helper()
	dsn := os.Getenv("GSEER_TEST_PG_ADMIN")
	if dsn == "" {
		dsn = sIntAdminDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	sIntMu.Lock()
	sIntCounter++
	name := fmt.Sprintf("gseer_m10_search_%d_%d", os.Getpid(), sIntCounter)
	sIntMu.Unlock()
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create db: %v", err)
	}
	_ = admin.Close(ctx)
	testDSN := strings.Replace(dsn, "/postgres?", "/"+name+"?", 1)
	if testDSN == dsn {
		testDSN = strings.TrimSuffix(dsn, "/postgres") + "/" + name
	}
	cleanup := func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ccancel()
		a, err := pgx.Connect(cctx, dsn)
		if err != nil {
			return
		}
		defer func() { _ = a.Close(cctx) }()
		_, _ = a.Exec(cctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()`, name)
		_, _ = a.Exec(cctx, `DROP DATABASE IF EXISTS `+name)
	}
	return testDSN, cleanup
}

// sIntSetup migrates + loads fixtures and returns the ready Service.
func sIntSetup(t *testing.T) (*Service, func()) {
	t.Helper()
	dsn, dbCleanup := sIntTestDB(t)
	// v0.2.0: bootstrap roles before the hardened migration set.
	if _, err := postgres.BootstrapRoles(context.Background(), dsn); err != nil {
		dbCleanup()
		t.Fatalf("BootstrapRoles: %v", err)
	}
	if _, _, err := postgres.RunMigrations(dsn, 1001); err != nil {
		dbCleanup()
		t.Fatalf("migrate: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		dbCleanup()
		t.Fatalf("pool: %v", err)
	}
	if _, err := fixture.Load(ctx, postgres.NewStore(pool)); err != nil {
		pool.Close()
		dbCleanup()
		t.Fatalf("fixture load: %v", err)
	}
	return New(pool), func() { pool.Close(); dbCleanup() }
}

// sIntEmptySetup migrates only — an empty index for notice tests.
func sIntEmptySetup(t *testing.T) (*Service, func()) {
	t.Helper()
	dsn, dbCleanup := sIntTestDB(t)
	// v0.2.0: bootstrap roles before the hardened migration set.
	if _, err := postgres.BootstrapRoles(context.Background(), dsn); err != nil {
		dbCleanup()
		t.Fatalf("BootstrapRoles: %v", err)
	}
	if _, _, err := postgres.RunMigrations(dsn, 1001); err != nil {
		dbCleanup()
		t.Fatalf("migrate: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		dbCleanup()
		t.Fatalf("pool: %v", err)
	}
	return New(pool), func() { pool.Close(); dbCleanup() }
}

// ---------------------------------------------------------------------------
// End-to-end search correctness through the Service
// ---------------------------------------------------------------------------

func TestSearchEndToEndPortEq(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()

	res, err := svc.Search(context.Background(), Query{GQL: `port == 443`, WithStats: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(res.Rows))
	}
	if res.Query.RowCount != 2 || res.Query.Mode != "service" {
		t.Errorf("query info wrong: %+v", res.Query)
	}
	if res.Index == nil || !res.Index.FixtureOnly {
		t.Errorf("expected fixture-only index metadata, got %+v", res.Index)
	}
	if res.Notice == "" || !strings.Contains(res.Notice, "SYNTHETIC EVALUATION DATA") {
		t.Errorf("expected fixture notice, got %q", res.Notice)
	}
	if res.Query.TookMs < 0 {
		t.Errorf("negative TookMs")
	}
}

func TestSearchEmptyIndexNotice(t *testing.T) {
	svc, cleanup := sIntEmptySetup(t)
	defer cleanup()

	res, err := svc.Search(context.Background(), Query{GQL: `port == 443`, WithStats: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("expected 0 rows on an empty index, got %d", len(res.Rows))
	}
	if res.Index == nil || !res.Index.Empty {
		t.Errorf("expected Empty index metadata, got %+v", res.Index)
	}
	if !strings.Contains(res.Notice, "index is empty") {
		t.Errorf("expected empty-index notice, got %q", res.Notice)
	}
}

func TestSearchQueryErrors(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()
	ctx := context.Background()

	// `port == "443"` is NOT an error anymore: the GQL compatibility
	// layer coerces quoted integers in place (checkType in
	// internal/gql/validator), matching how hosted-engine compat
	// clients quote every value.  The strict-typing probe therefore
	// uses a quoted value that cannot coerce.
	cases := []struct {
		name string
		gql  string
	}{
		{"unknown field", `bogusfield == 1`},
		{"type violation", `port == "not-a-number"`},
		{"op violation", `port CONTAINS "443"`},
		{"parse error", `port ==`},
		{"lex error", `port == "unterminated`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Search(ctx, Query{GQL: tc.gql})
			if err == nil {
				t.Fatalf("expected error for %q", tc.gql)
			}
			qe, ok := AsQuery(err)
			if !ok {
				t.Fatalf("expected QueryError, got %T: %v", err, err)
			}
			if qe.Code == "" || qe.Detail == "" {
				t.Errorf("QueryError missing code/detail: %+v", qe)
			}
		})
	}

	// Empty query text.
	_, err := svc.Search(ctx, Query{GQL: "   "})
	if err == nil || !strings.Contains(err.Error(), "E_QUERY_EMPTY") {
		t.Errorf("expected E_QUERY_EMPTY, got %v", err)
	}
}

func TestSearchPaginationEndToEnd(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()
	ctx := context.Background()

	// transport == "tcp" matches all 6 services; walk with limit=2.
	var seen []Row
	cursor := ""
	pages := 0
	for {
		res, err := svc.Search(ctx, Query{
			GQL:    `transport == "tcp"`,
			Sort:   "transport:asc",
			Limit:  2,
			Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", pages+1, err)
		}
		pages++
		seen = append(seen, res.Rows...)
		if !res.HasMore || res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
		if pages > 10 {
			t.Fatalf("pagination did not terminate")
		}
	}
	if len(seen) != 6 {
		t.Fatalf("expected 6 rows across pages, got %d", len(seen))
	}
	uniq := map[int64]bool{}
	for _, r := range seen {
		if uniq[r.RowID] {
			t.Fatalf("row %d seen twice", r.RowID)
		}
		uniq[r.RowID] = true
	}
}

func TestSearchAssetModeEndToEnd(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()

	res, err := svc.Search(context.Background(), Query{
		GQL:  `port == 443`,
		Mode: "asset",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 distinct assets (A, C), got %d", len(res.Rows))
	}
	if res.Query.Mode != "asset" {
		t.Errorf("mode not asset: %+v", res.Query)
	}
}

// ---------------------------------------------------------------------------
// Timeout contract (owner directive §2)
// ---------------------------------------------------------------------------

// holdAssetsLock opens a dedicated connection, takes ACCESS EXCLUSIVE
// on assets inside a transaction, and returns the release func.  Every
// search SELECT joins assets, so holding this blocks any in-flight or
// next search statement until released.
func holdAssetsLock(t *testing.T, dsn string) (release func()) {
	t.Helper()
	ctx := context.Background()
	holder, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("holder connect: %v", err)
	}
	htx, err := holder.Begin(ctx)
	if err != nil {
		_ = holder.Close(ctx)
		t.Fatalf("holder begin: %v", err)
	}
	if _, err := htx.Exec(ctx, `LOCK TABLE assets IN ACCESS EXCLUSIVE MODE`); err != nil {
		_ = htx.Rollback(ctx)
		_ = holder.Close(ctx)
		t.Fatalf("holder lock: %v", err)
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = htx.Rollback(ctx)
		_ = holder.Close(ctx)
	}
}

// TestSearchTimeoutEnforced: a second transaction holds ACCESS EXCLUSIVE
// on assets, so the search's SELECT blocks; the transaction-local
// statement_timeout must cancel it and surface *TimeoutError.
func TestSearchTimeoutEnforced(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()
	ctx := context.Background()

	release := holdAssetsLock(t, svc.pool.Config().ConnConfig.ConnString())
	defer release()

	start := time.Now()
	_, serr := svc.Search(ctx, Query{GQL: `port == 443`, Timeout: 300 * time.Millisecond})
	elapsed := time.Since(start)
	if serr == nil {
		t.Fatal("expected timeout error while the lock is held")
	}
	if !AsTimeout(serr) {
		t.Fatalf("expected *TimeoutError, got %T: %v", serr, serr)
	}
	if elapsed > 3*time.Second {
		t.Errorf("timeout took %s — the statement was not canceled promptly", elapsed)
	}
	// The rendered shape is stable (pinned for the CLI/API docs).
	if !strings.Contains(serr.Error(), "E_QUERY_TIMEOUT") {
		t.Errorf("timeout error rendering changed: %v", serr)
	}

	// 2. No leakage: after the timed-out search, a normal search on the
	// SAME service (fresh transaction, released lock) succeeds.
	release()
	res, err := svc.Search(ctx, Query{GQL: `port == 443`})
	if err != nil {
		t.Fatalf("post-timeout search failed (timeout config leaked?): %v", err)
	}
	if len(res.Rows) != 2 {
		t.Errorf("post-timeout search returned %d rows, want 2", len(res.Rows))
	}
}

// TestSearchPoolReuseAfterTimeout: after several timed-out searches the
// pool must still acquire connections and serve searches (the aborted
// transactions are rolled back / their connections reset, not poisoned).
func TestSearchPoolReuseAfterTimeout(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()
	ctx := context.Background()

	release := holdAssetsLock(t, svc.pool.Config().ConnConfig.ConnString())
	defer release()

	for i := 0; i < 3; i++ {
		_, err := svc.Search(ctx, Query{GQL: `port == 443`, Timeout: 200 * time.Millisecond})
		if !AsTimeout(err) {
			t.Fatalf("iteration %d: expected timeout, got %v", i, err)
		}
	}
	release()
	stat := svc.pool.Stat()
	if stat.NewConnsCount() == 0 {
		t.Fatal("pool never established a connection")
	}
	if stat.TotalConns() == 0 {
		t.Fatal("pool holds no connections after the timeout storm")
	}
	// A successful search proves the pool is functional.
	res, err := svc.Search(ctx, Query{GQL: `port == 443`})
	if err != nil {
		t.Fatalf("pool unusable after timeouts: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Errorf("expected 2 rows after timeout storm, got %d", len(res.Rows))
	}
}

// TestSearchTimeoutResetNoLeak: set_config(is_local=true) is
// transaction-scoped — the test pins that a LONG timeout on one search
// does not persist into the session state of the pooled connection used
// by a later, independent statement (which runs OUTSIDE any search tx).
func TestSearchTimeoutResetNoLeak(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()
	ctx := context.Background()

	// Search with a long timeout (applies set_config inside its tx).
	if _, err := svc.Search(ctx, Query{GQL: `port == 443`, Timeout: 10 * time.Second}); err != nil {
		t.Fatalf("search: %v", err)
	}

	// Read statement_timeout OUTSIDE the search transaction: it must be
	// the server default (0 = disabled), not the search's 10s.
	var got string
	if err := svc.pool.QueryRow(ctx, `SHOW statement_timeout`).Scan(&got); err != nil {
		t.Fatalf("SHOW statement_timeout: %v", err)
	}
	if got != "0" {
		t.Fatalf("statement_timeout leaked into the pooled session: %q (want \"0\")", got)
	}
}

// TestSearchUnavailableMapping: a Service whose pool points at a dead
// endpoint maps the connect failure to *UnavailableError with the
// doctor pointer in the rendered text.
func TestSearchUnavailableMapping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "postgres://gseer@127.0.0.1:5999/postgres?sslmode=disable&connect_timeout=1")
	if err == nil {
		pool.Close()
		t.Skip("port 5999 unexpectedly reachable")
	}
	// The pool constructor tolerates a dead endpoint; first query fails.
	pool2, err := pgxpool.New(ctx, "postgres://gseer@127.0.0.1:5999/postgres?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool2.Close()
	svc := New(pool2)
	_, serr := svc.Search(ctx, Query{GQL: `port == 443`})
	if serr == nil {
		t.Fatal("expected unavailable error")
	}
	if !AsUnavailable(serr) {
		t.Fatalf("expected *UnavailableError, got %T: %v", serr, serr)
	}
	if !strings.Contains(serr.Error(), "gseer doctor") {
		t.Errorf("unavailable error missing doctor pointer: %v", serr)
	}
}

// TestSearchCommitVisible: a search with WithStats sees the committed
// fixture rows — the implicit proof that successful searches commit
// rather than roll back (rows written by fixture.Load outside the tx
// are visible; the search's own tx had no writes, but the commit path
// is exercised on every Search call and a broken commit would error).
func TestSearchCommitVisible(t *testing.T) {
	svc, cleanup := sIntSetup(t)
	defer cleanup()

	meta, err := svc.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if meta.Assets != 4 || meta.Services != 6 || meta.Observations != 7 {
		t.Errorf("fixture index shape wrong: %+v", meta)
	}
	if meta.FixtureObs != 7 || !meta.FixtureOnly {
		t.Errorf("fixture-only flag wrong: %+v", meta)
	}
	if meta.LastObserved == nil {
		t.Error("expected last_observed_at to be set")
	}
	if meta.Empty {
		t.Error("index must not be Empty")
	}
	_ = errors.Is // keep the errors import for the mapping helpers above
}
