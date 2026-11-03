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

package planner

// integration_test.go — the M9 planner acceptance tests (build tag
// `integration`).  Each test compiles a GQL expression to a Plan,
// executes the Plan's SQL against a fresh PostgreSQL with the fixture
// corpus loaded, and asserts the result rows match the expected set.
//
// These tests are the strongest evidence the planner is correct: they
// exercise the actual parameterized SQL against the actual search_index
// view with the actual data the v0.0.1 SearchService will serve.
//
// Environment: GSEER_TEST_PG_ADMIN — superuser DSN to an admin
// database used to create/drop per-test databases.  Default matches
// scripts/ensure_pg.sh.

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

	"github.com/Lutfifakee-Project/GSEER/internal/fixture"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/parser"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/validator"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
)

const pIntAdminDSN = "postgres://gseer@127.0.0.1:5433/postgres?sslmode=disable"

var (
	pIntMu      sync.Mutex
	pIntCounter int
)

// pIntTestDB creates a unique empty database and returns its DSN plus
// a drop-with-terminate cleanup func.
func pIntTestDB(t *testing.T) (string, func()) {
	t.Helper()
	dsn := os.Getenv("GSEER_TEST_PG_ADMIN")
	if dsn == "" {
		dsn = pIntAdminDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("admin connect (%s): %v — set GSEER_TEST_PG_ADMIN or run scripts/ensure_pg.sh", dsn, err)
	}

	pIntMu.Lock()
	pIntCounter++
	name := fmt.Sprintf("gseer_m9_plan_%d_%d", os.Getpid(), pIntCounter)
	pIntMu.Unlock()

	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create db %s: %v", name, err)
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
			t.Logf("cleanup: reconnect failed: %v", err)
			return
		}
		defer func() { _ = a.Close(cctx) }()
		_, _ = a.Exec(cctx,
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()`, name)
		if _, err := a.Exec(cctx, `DROP DATABASE IF EXISTS `+name); err != nil {
			t.Logf("cleanup: drop %s failed: %v", name, err)
		}
	}
	return testDSN, cleanup
}

// pIntSetup migrates a fresh DB and loads the fixture corpus.  Returns
// a pool ready for queries.
func pIntSetup(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	dsn, dbCleanup := pIntTestDB(t)
	if _, _, err := postgres.RunMigrations(dsn, 1001); err != nil {
		dbCleanup()
		t.Fatalf("RunMigrations: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		dbCleanup()
		t.Fatalf("pgxpool.New: %v", err)
	}
	store := postgres.NewStore(pool)
	if _, err := fixture.Load(ctx, store); err != nil {
		pool.Close()
		dbCleanup()
		t.Fatalf("fixture.Load: %v", err)
	}
	return pool, func() {
		pool.Close()
		dbCleanup()
	}
}

// pIntCompile parses+validates+compiles a GQL expression.
func pIntCompile(t *testing.T, gql string, opts Options) *Plan {
	t.Helper()
	tree, err := parser.Parse(gql)
	if err != nil {
		t.Fatalf("parser.Parse(%q): %v", gql, err)
	}
	if _, err := validator.Validate(tree); err != nil {
		t.Fatalf("validator.Validate(%q): %v", gql, err)
	}
	p, err := Compile(tree, opts)
	if err != nil {
		t.Fatalf("Compile(%q): %v", gql, err)
	}
	return p
}

// pIntRun executes a Plan's SQL against the pool, returning the
// collected (port, asset_value, http_title, row_kind) tuples for
// assertion.  The SET-config + SELECT are run in one transaction so
// the per-query statement_timeout applies to the SELECT.
//
// asset_value (text) is projected instead of ip because the ip column
// is PG type inet, which pgx cannot scan into *string without a
// pgtype.Text / net.IPNet intermediary.  The integration tests don't
// need the ip column directly — asset_value carries the same identity
// information in text form.
type resultRow struct {
	Port       *int
	AssetValue *string
	HTTPTitle  *string
	RowKind    string
}

func pIntRun(t *testing.T, pool *pgxpool.Pool, p *Plan) []resultRow {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Apply the plan's per-query statement_timeout via the parameterized
	// set_config() form (PG's `SET LOCAL` does not accept a $N
	// placeholder; the planner exposes the timeout as a separate SQL
	// pair rather than baking it into Plan.SQL).
	if toSQL, toArgs := p.ApplyStatementTimeoutSQL(); toSQL != "" {
		if _, err := tx.Exec(ctx, toSQL, toArgs...); err != nil {
			t.Fatalf("apply statement_timeout: %v", err)
		}
	}

	rows, err := tx.Query(ctx, p.SQL, p.Args...)
	if err != nil {
		t.Fatalf("Query: %v\nSQL:\n%s\nargs: %v", err, p.SQL, p.Args)
	}
	defer rows.Close()
	var out []resultRow
	for rows.Next() {
		var r resultRow
		if err := rows.Scan(&r.Port, &r.AssetValue, &r.HTTPTitle, &r.RowKind); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}

// pIntCompileRun is the convenience helper for the common path.
func pIntCompileRun(t *testing.T, pool *pgxpool.Pool, gql string, opts Options) []resultRow {
	t.Helper()
	// Project only the columns we need to keep the scan tight.  ip is
	// PG inet; we use asset_value (text) instead for identity.
	opts.Fields = []string{"port", "asset_value", "http_title", "row_kind"}
	p := pIntCompile(t, gql, opts)
	return pIntRun(t, pool, p)
}

// ---------------------------------------------------------------------------
// Acceptance tests
// ---------------------------------------------------------------------------

func TestPlanWitnessPortEq(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	rows := pIntCompileRun(t, pool, `port == 443`, Options{})
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (s1 on A, s5 on C), got %d: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.Port == nil || *r.Port != 443 {
			t.Errorf("row port != 443: %+v", r)
		}
	}
}

func TestPlanWitnessPort22(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	rows := pIntCompileRun(t, pool, `port == 22`, Options{})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (s3 on A), got %d: %+v", len(rows), rows)
	}
}

func TestPlanWitnessTransportTCP(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	rows := pIntCompileRun(t, pool, `transport == "tcp"`, Options{})
	// All 6 services are tcp; D (orphan asset) has transport=NULL → excluded.
	if len(rows) != 6 {
		t.Fatalf("expected 6 rows (all services are tcp), got %d: %+v", len(rows), rows)
	}
}

func TestPlanWitnessASN(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// asn==64500 → A and B; A has 3 services, B has 1 → 4 service rows.
	rows := pIntCompileRun(t, pool, `asn == 64500`, Options{})
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows (A's 3 services + B's 1 service), got %d: %+v", len(rows), rows)
	}
}

func TestPlanInCidr(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// 203.0.113.0/24 covers A, B, C IPs → 6 service rows.
	// D is a domain asset (ip=NULL) → NULL::inet <<= cidr is NULL → excluded.
	rows := pIntCompileRun(t, pool, `ip IN_CIDR "203.0.113.0/24"`, Options{})
	if len(rows) != 6 {
		t.Fatalf("expected 6 rows (all service rows of A,B,C), got %d: %+v", len(rows), rows)
	}
}

func TestPlanInDomain(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// domain IN_DOMAIN "example.net" matches "portal.example.net" (D).
	// D is an asset-only row; A,B,C have domain=NULL → excluded.
	rows := pIntCompileRun(t, pool, `domain IN_DOMAIN "example.net"`, Options{})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (D = portal.example.net), got %d: %+v", len(rows), rows)
	}
	if rows[0].RowKind != "asset-only" {
		t.Errorf("expected asset-only row_kind, got %s", rows[0].RowKind)
	}
}

func TestPlanContainsString(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// http.title CONTAINS "Admin" → o1 (A:s1), o4 (B:s4), o6 (C:s6) = 3 rows.
	rows := pIntCompileRun(t, pool, `http.title CONTAINS "Admin"`, Options{})
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows with 'Admin' title, got %d: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.HTTPTitle == nil || !strings.Contains(*r.HTTPTitle, "Admin") {
			t.Errorf("row title not containing 'Admin': %+v", r)
		}
	}
}

func TestPlanContainsStringCaseInsensitive(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// ILIKE is case-insensitive: "admin" matches "Admin Login".
	rows := pIntCompileRun(t, pool, `http.title CONTAINS "admin"`, Options{})
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows (case-insensitive Admin), got %d: %+v", len(rows), rows)
	}
}

func TestPlanHttpStatusEq(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// http.status == 200 → o1, o4, o5, o6 = 4 rows.
	rows := pIntCompileRun(t, pool, `http.status == 200`, Options{})
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows with http.status=200, got %d: %+v", len(rows), rows)
	}
}

func TestPlanExistsHTTPTitle(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// exists(http.title) → services with an http observation carrying a title:
	// s1, s2, s4, s5, s6 = 5 rows.  s3 (ssh only) excluded; D excluded.
	rows := pIntCompileRun(t, pool, `exists(http.title)`, Options{})
	if len(rows) != 5 {
		t.Fatalf("expected 5 rows with http.title, got %d: %+v", len(rows), rows)
	}
}

func TestPlanInList(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// port IN (80, 443) → s1 (443), s2 (80), s5 (443) = 3 rows.
	rows := pIntCompileRun(t, pool, `port IN (80, 443)`, Options{})
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows (ports 80 and 443), got %d: %+v", len(rows), rows)
	}
}

func TestPlanContainsListEmpty(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// technologies CONTAINS "nginx" — no observation sets technologies,
	// so latest_fingerprints has no rows; every technologies cell is NULL.
	// The @> ARRAY[$1]::text[] predicate on NULL → NULL → filtered out.
	rows := pIntCompileRun(t, pool, `technologies CONTAINS "nginx"`, Options{})
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows (no technologies in fixture), got %d: %+v", len(rows), rows)
	}
}

func TestPlanAnyHostBranch(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// ANY(host){ http.status == 200 } — hosts with at least one service
	// returning 200: A (s1=200), B (s4=200), C (s5,s6=200).  D has no
	// services → excluded.  The predicate matches every search_index
	// row of a matching host, so all 6 service rows of A,B,C return.
	rows := pIntCompileRun(t, pool, `ANY(host){ http.status == 200 }`, Options{})
	if len(rows) != 6 {
		t.Fatalf("expected 6 rows (all service rows of A,B,C), got %d: %+v", len(rows), rows)
	}
}

func TestPlanAnyHostNegative(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// No host has a service with http.status == 500.
	rows := pIntCompileRun(t, pool, `ANY(host){ http.status == 500 }`, Options{})
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows (no service returns 500), got %d: %+v", len(rows), rows)
	}
}

func TestPlanAnyServiceInline(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// ANY(service){ port == 443 } — folds inline to `port == 443` → 2 rows.
	rows := pIntCompileRun(t, pool, `ANY(service){ port == 443 }`, Options{})
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (port==443 folded inline), got %d: %+v", len(rows), rows)
	}
}

func TestPlanCompoundAndOr(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// port == 443 && asn == 64500 → s1 on A (asn=64500, port=443).
	// s5 on C (port=443, asn=64512) excluded by asn clause.
	rows := pIntCompileRun(t, pool, `port == 443 && asn == 64500`, Options{})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (s1 on A), got %d: %+v", len(rows), rows)
	}
	// (port == 22 || port == 8443) → s3 (A:22), s4 (B:8443), s6 (C:8443) = 3 rows.
	rows = pIntCompileRun(t, pool, `port == 22 || port == 8443`, Options{})
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d: %+v", len(rows), rows)
	}
}

func TestPlanNot(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// !(transport == "tcp") — all 6 services are tcp; D (orphan) has
	// transport=NULL.  In SQL, NOT (NULL = 'tcp') = NOT NULL = NULL,
	// which is not TRUE → D is excluded too.  This is the correct SQL
	// three-valued logic: a NULL transport means "we don't know", and
	// negation of "we don't know" stays "we don't know".  GQL users
	// who want to match NULL transports use `exists(transport)` or
	// `transport != "tcp"` (the latter is also NULL → excluded).  The
	// v0.0.1 planner deliberately mirrors SQL semantics rather than
	// inventing a NULL-equality rule (this is the conservative choice
	// — change only with corpus evidence).
	rows := pIntCompileRun(t, pool, `!(transport == "tcp")`, Options{})
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows (NULL transport excluded by NOT semantics), got %d: %+v", len(rows), rows)
	}
}

func TestPlanModeAsset(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// mode=asset, port == 443 → distinct assets with at least one :443
	// service: A (s1=443) and C (s5=443).  2 rows.
	rows := pIntCompileRun(t, pool, `port == 443`, Options{Mode: ModeAsset})
	if len(rows) != 2 {
		t.Fatalf("expected 2 distinct assets (A, C), got %d: %+v", len(rows), rows)
	}
}

func TestPlanModeAssetAllAssets(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// mode=asset, asn == 64500 → A and B (both have asn=64500).  2 rows.
	rows := pIntCompileRun(t, pool, `asn == 64500`, Options{Mode: ModeAsset})
	if len(rows) != 2 {
		t.Fatalf("expected 2 distinct assets (A, B), got %d: %+v", len(rows), rows)
	}
}

func TestPlanCursorPagination(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()

	// Page 1: port == 443 with limit=1, sort port ASC.
	page1 := pIntCompileRun(t, pool, `port == 443`, Options{
		Limit: 1,
		Sort:  []SortSpec{{Field: "port", Direction: DirAsc}},
	})
	if len(page1) != 1 {
		t.Fatalf("page 1: expected 1 row, got %d: %+v", len(page1), page1)
	}
	// Cursor: encode the (port, row_id, row_kind) of the returned
	// page-1 row — the composite row key is part of the cursor.
	opts1 := Options{
		Limit:  1,
		Sort:   []SortSpec{{Field: "port", Direction: DirAsc}},
		Fields: []string{"row_id", "row_kind", "port"},
	}
	p1 := pIntCompile(t, `port == 443`, opts1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	r1, err := tx.Query(ctx, p1.SQL, p1.Args...)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	var rowID int64
	var rowKind string
	var portVal int
	if !r1.Next() {
		t.Fatalf("page1: no rows")
	}
	if err := r1.Scan(&rowID, &rowKind, &portVal); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	r1.Close()

	cursor := CursorPayload{SortField: "port", RowID: rowID, RowKind: rowKind, Value: fmt.Sprintf("%d", portVal)}.Encode()

	// Page 2 with the cursor: should return the remaining :443 row.
	page2 := pIntCompileRun(t, pool, `port == 443`, Options{
		Limit:  10,
		Sort:   []SortSpec{{Field: "port", Direction: DirAsc}},
		Cursor: cursor,
	})
	if len(page2) != 1 {
		t.Fatalf("page 2: expected 1 row, got %d: %+v", len(page2), page2)
	}
}

func TestPlanLimitEnforced(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// 6 services are tcp; limit=2 returns 2.
	rows := pIntCompileRun(t, pool, `transport == "tcp"`, Options{Limit: 2})
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (limit), got %d: %+v", len(rows), rows)
	}
}

func TestPlanExplainContainsPlanShape(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	_ = pool // no execution needed; just exercise Explain on a real plan.
	p := pIntCompile(t, `port == 443 && ANY(host){ http.status == 200 }`, Options{Mode: ModeAsset})
	expl := p.Explain()
	for _, want := range []string{
		"GQL Plan", "mode:     asset", "witness", "any-union", "asset-all",
		"limit:", "timeout:", "SQL", "args:",
	} {
		if !strings.Contains(expl, want) {
			t.Errorf("Explain missing %q:\n%s", want, expl)
		}
	}
}

func TestPlanStaleBool(t *testing.T) {
	pool, cleanup := pIntSetup(t)
	defer cleanup()
	// stale == "true" — no service is marked stale in the fixture corpus.
	rows := pIntCompileRun(t, pool, `stale == "true"`, Options{})
	if len(rows) != 0 {
		t.Fatalf("expected 0 stale rows, got %d: %+v", len(rows), rows)
	}
	// stale == "false" — all 6 service rows have stale=false; D too.
	rows = pIntCompileRun(t, pool, `stale == "false"`, Options{})
	if len(rows) != 7 {
		t.Fatalf("expected 7 non-stale rows (6 services + D orphan), got %d: %+v", len(rows), rows)
	}
}
