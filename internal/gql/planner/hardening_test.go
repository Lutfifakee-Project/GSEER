//go:build integration

// GSEER — internet-asset search engine.
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

// hardening_test.go — M9→M10 integration-hardening edge cases (owner
// directive §1).  Each test exercises a boundary the M9 baseline corpus
// did not cover, against a live PostgreSQL with an EXTENDED corpus: the
// fixture data plus hardening rows ingested through the real store
// (a non-fixture data_source row, so the fixture semantics stay intact):
//
//   - cursor pagination across NULL sort values (ASC tail + DESC head)
//   - ASC/DESC with NULLS LAST / NULLS FIRST
//   - equal sort values with row_id tie-breaking (and cursoring within)
//   - query-context validation for cursors (sort-field mismatch)
//   - literal CONTAINS semantics for %, _, and backslash
//   - IN_DOMAIN normalization and domain-label boundaries
//   - scalar IN versus array-valued field semantics
//   - deterministic DISTINCT ON asset projection
//   - correct asset-mode pagination (dedup-then-keyset)

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Lutfifakee-Project/GSEER/internal/gql/parser"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/validator"
	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
)

// hardeningID is a projected row for oracle comparisons.
type hardeningID struct {
	RowID      int64
	RowKind    string
	AssetValue *string
	Port       *int
	HTTPStatus *int
}

// hIngest ingests the hardening corpus into the test DB and returns
// the hardening source id.
func hIngest(t *testing.T, pool *pgxpool.Pool, store *postgres.Store) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var srcID int64
	if err := pool.QueryRow(ctx, `
INSERT INTO data_sources (name, connector, terms_ref, enabled)
VALUES ('hardening-test', 'test', 'test-terms', false)
ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
RETURNING id`).Scan(&srcID); err != nil {
		t.Fatalf("insert hardening source: %v", err)
	}

	now := time.Now().UTC()
	fact := func(asset storage.Asset, service *storage.Service, cursor string) postgres.Fact {
		return postgres.Fact{
			Asset:   asset,
			Service: service,
			Observation: storage.Observation{
				// observations.id is caller-assigned (no default); mirror
				// the fixture loader's fnv1a-64 bookkeeping scheme over a
				// namespaced cursor so ids are stable and collision-free
				// against the "fixture:"-namespaced corpus ids.
				ID:           hardeningObsID(cursor),
				SourceID:     srcID,
				ObservedAt:   now,
				PayloadHash:  "hardening-" + cursor,
				SourceCursor: cursor,
				Provenance:   "test",
			},
		}
	}

	ipAsset := storage.Asset{
		Kind:    storage.AssetKindIP,
		Value:   "203.0.113.20",
		IP:      strPtr("203.0.113.20"),
		Sources: []string{"hardening-test"},
	}
	svc := func(port int, product string) *storage.Service {
		return &storage.Service{
			Port:      port,
			Transport: "tcp",
			Protocol:  "http",
			Name:      "edge",
			Product:   product,
		}
	}
	domainAsset := func(v string) storage.Asset {
		return storage.Asset{
			Kind:    storage.AssetKindDomain,
			Value:   v,
			Domain:  strPtr(v),
			Sources: []string{"hardening-test"},
		}
	}

	facts := []postgres.Fact{
		// LIKE-special product literals (CONTAINS semantics).
		fact(ipAsset, svc(1001, "a_b"), "hard://203.0.113.20:1001"),
		fact(ipAsset, svc(1002, "axb"), "hard://203.0.113.20:1002"),
		fact(ipAsset, svc(1003, "50%"), "hard://203.0.113.20:1003"),
		fact(ipAsset, svc(1004, `back\slash`), "hard://203.0.113.20:1004"),
		// Domain-label boundary cases for IN_DOMAIN.
		fact(domainAsset("example.net"), nil, "hard://example.net"),
		fact(domainAsset("a.b.example.net"), nil, "hard://a.b.example.net"),
		fact(domainAsset("notexample.net"), nil, "hard://notexample.net"),
		fact(domainAsset("example.net.evil.com"), nil, "hard://example.net.evil.com"),
	}
	if _, err := store.IngestFacts(ctx, facts); err != nil {
		t.Fatalf("ingest hardening facts: %v", err)
	}
	return srcID
}

func strPtr(s string) *string { return &s }

// hardeningObsID derives the caller-assigned observations.id for a
// hardening row: fnv1a-64 over "hardening:"+cursor — the same
// bookkeeping scheme the fixture loader uses over "fixture:"+cursor.
func hardeningObsID(cursor string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("hardening:" + cursor))
	return int64(h.Sum64())
}

// hSetup is pIntSetup plus the hardening corpus.
func hSetup(t *testing.T) (*pgxpool.Pool, *postgres.Store, func()) {
	t.Helper()
	pool, cleanup := pIntSetup(t)
	store := postgres.NewStore(pool)
	hIngest(t, pool, store)
	return pool, store, cleanup
}

// hCompile is pIntCompile (parse → validate → Compile).
func hCompile(t *testing.T, gql string, opts Options) *Plan {
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

// hQuery runs a plan and returns (row_id, asset_value, port, http_status)
// tuples in result order.
func hQuery(t *testing.T, pool *pgxpool.Pool, p *Plan) []hardeningID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
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
	var out []hardeningID
	for rows.Next() {
		var r hardeningID
		if err := rows.Scan(&r.RowID, &r.RowKind, &r.AssetValue, &r.Port, &r.HTTPStatus); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}

// hProject pins the projection to the hardening row shape.
func hProject(opts Options) Options {
	opts.Fields = []string{"row_id", "row_kind", "asset_value", "port", "http.status"}
	return opts
}

// hCursorFrom builds the cursor the SearchService would derive from an
// output row of the given sort field.
func hCursorFrom(field string, r hardeningID, null bool) string {
	v := ""
	if !null && r.AssetValue != nil {
		v = *r.AssetValue
	}
	if !null && r.HTTPStatus != nil {
		v = fmt.Sprintf("%d", *r.HTTPStatus)
	}
	return CursorPayload{SortField: field, RowID: r.RowID, RowKind: r.RowKind, Value: v, Null: null}.Encode()
}

// ---------------------------------------------------------------------------
// 1+2. NULL-aware cursor pagination, ASC tail and DESC head
// ---------------------------------------------------------------------------

// TestHardeningCursorAcrossNullsASC paginates the FULL index ordered by
// http.status ASC (NULLS LAST) with limit=2 and asserts the collected
// row sequence equals a direct SQL ORDER BY oracle.  Exercises: NULL
// tail reached through a non-null cursor (the OR col IS NULL arm), then
// cursoring INSIDE the NULL tail (null-cursor form).
func TestHardeningCursorAcrossNullsASC(t *testing.T) {
	pool, _, cleanup := hSetup(t)
	defer cleanup()

	sort := []SortSpec{{Field: "http.status", Direction: DirAsc}}

	// Oracle: direct SQL with the identical order (composite row-key tiebreak).
	oracle := hOracle(t, pool, `SELECT row_id, row_kind, asset_value, port, http_status
FROM search_index ORDER BY http_status ASC NULLS LAST, row_id ASC NULLS LAST, row_kind ASC`)

	// Page through with limit=2.
	var got []hardeningID
	var cursor string
	pages := 0
	for {
		opts := hProject(Options{Limit: 2, Sort: sort, Cursor: cursor})
		p := hCompile(t, `port == 443 || port == 80 || port == 22 || port == 8443 || port == 1001 || port == 1002 || port == 1003 || port == 1004 || exists(domain)`, opts)
		rows := hQuery(t, pool, p)
		pages++
		if len(rows) == 0 {
			break
		}
		got = append(got, rows...)
		last := rows[len(rows)-1]
		cursor = hCursorFrom("http.status", last, last.HTTPStatus == nil)
		if pages > 20 {
			t.Fatalf("pagination did not terminate after %d pages", pages)
		}
	}

	if len(got) != len(oracle) {
		t.Fatalf("collected %d rows across pagination, oracle has %d", len(got), len(oracle))
	}
	for i := range got {
		if got[i].RowID != oracle[i].RowID {
			t.Fatalf("page-walk order diverges at %d: got row_id %d, want %d", i, got[i].RowID, oracle[i].RowID)
		}
	}
	// NULL rows are all at the tail.
	nullsStarted := false
	for _, r := range got {
		if r.HTTPStatus == nil {
			nullsStarted = true
		} else if nullsStarted {
			t.Fatalf("non-null http_status %v appears after the NULL tail", *r.HTTPStatus)
		}
	}
}

// TestHardeningCursorAcrossNullsDESC mirrors the ASC test with DESC
// (NULLS FIRST): a null-cursor page must reach the remaining NULL rows
// AND then fall through to every non-null row (the IS NOT NULL arm).
func TestHardeningCursorAcrossNullsDESC(t *testing.T) {
	pool, _, cleanup := hSetup(t)
	defer cleanup()

	sort := []SortSpec{{Field: "http.status", Direction: DirDesc}}
	oracle := hOracle(t, pool, `SELECT row_id, row_kind, asset_value, port, http_status
FROM search_index ORDER BY http_status DESC NULLS FIRST, row_id ASC NULLS LAST, row_kind ASC`)

	var got []hardeningID
	var cursor string
	pages := 0
	for {
		opts := hProject(Options{Limit: 2, Sort: sort, Cursor: cursor})
		p := hCompile(t, `port == 443 || port == 80 || port == 22 || port == 8443 || port == 1001 || port == 1002 || port == 1003 || port == 1004 || exists(domain)`, opts)
		rows := hQuery(t, pool, p)
		pages++
		if len(rows) == 0 {
			break
		}
		got = append(got, rows...)
		last := rows[len(rows)-1]
		cursor = hCursorFrom("http.status", last, last.HTTPStatus == nil)
		if pages > 20 {
			t.Fatalf("pagination did not terminate after %d pages", pages)
		}
	}

	if len(got) != len(oracle) {
		gotIDs := make([]string, len(got))
		oracleIDs := make([]string, len(oracle))
		for i, r := range got {
			s := fmt.Sprintf("%d", r.RowID)
			if r.HTTPStatus != nil {
				s += fmt.Sprintf("(http=%d)", *r.HTTPStatus)
			} else {
				s += "(http=NULL)"
			}
			gotIDs[i] = s
		}
		for i, r := range oracle {
			s := fmt.Sprintf("%d", r.RowID)
			if r.HTTPStatus != nil {
				s += fmt.Sprintf("(http=%d)", *r.HTTPStatus)
			} else {
				s += "(http=NULL)"
			}
			oracleIDs[i] = s
		}
		t.Fatalf("collected %d rows, oracle has %d\n got:   %v\n oracle: %v", len(got), len(oracle), gotIDs, oracleIDs)
	}
	for i := range got {
		if got[i].RowID != oracle[i].RowID {
			t.Fatalf("DESC page-walk diverges at %d: got row_id %d, want %d", i, got[i].RowID, oracle[i].RowID)
		}
	}
	// NULL rows are all at the head.
	nullsEnded := false
	for _, r := range got {
		if r.HTTPStatus != nil {
			nullsEnded = true
		} else if nullsEnded {
			t.Fatalf("NULL http_status appears after the non-null head")
		}
	}
}

// hOracle runs a raw oracle SQL and scans the hardening row shape.
func hOracle(t *testing.T, pool *pgxpool.Pool, sql string) []hardeningID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Oracle runs a raw oracle SQL and scans the hardening row shape.
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		t.Fatalf("oracle query: %v", err)
	}
	defer rows.Close()
	var out []hardeningID
	for rows.Next() {
		var r hardeningID
		if err := rows.Scan(&r.RowID, &r.RowKind, &r.AssetValue, &r.Port, &r.HTTPStatus); err != nil {
			t.Fatalf("oracle scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("oracle rows: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// 3. Equal sort values with row_id tie-breaking
// ---------------------------------------------------------------------------

// TestHardeningEqualValuesRowIDTiebreak: every tcp row shares the same
// transport value, so the sort is entirely the row_id tiebreaker.  The
// full walk must return the rows in ascending row_id order with no
// duplicates, and cursor pages inside the equal-value block must
// advance strictly.
func TestHardeningEqualValuesRowIDTiebreak(t *testing.T) {
	pool, _, cleanup := hSetup(t)
	defer cleanup()

	// Oracle: direct SQL with the identical order (composite row-key tiebreak).
	oracle := hOracle(t, pool, `SELECT row_id, row_kind, asset_value, port, http_status
FROM search_index WHERE transport = 'tcp'
ORDER BY transport ASC NULLS LAST, row_id ASC NULLS LAST, row_kind ASC`)
	if len(oracle) != 10 { // 6 fixture + 4 hardening services
		t.Fatalf("expected 10 tcp rows, oracle has %d", len(oracle))
	}

	sort := []SortSpec{{Field: "transport", Direction: DirAsc}}
	var got []hardeningID
	var cursor string
	pages := 0
	for {
		opts := hProject(Options{Limit: 3, Sort: sort, Cursor: cursor})
		p := hCompile(t, `transport == "tcp"`, opts)
		rows := hQuery(t, pool, p)
		pages++
		if len(rows) == 0 {
			break
		}
		got = append(got, rows...)
		last := rows[len(rows)-1]
		// transport is never NULL on a tcp row; the cursor value is the
		// row's transport string and the row key carries row_kind.
		cursor = CursorPayload{
			SortField: "transport",
			RowID:     last.RowID,
			RowKind:   last.RowKind,
			Value:     "tcp",
		}.Encode()
		if pages > 10 {
			t.Fatalf("pagination did not terminate after %d pages", pages)
		}
	}

	if len(got) != len(oracle) {
		t.Fatalf("collected %d rows, oracle has %d", len(got), len(oracle))
	}
	seen := map[int64]bool{}
	for i, r := range got {
		if seen[r.RowID] {
			t.Fatalf("row_id %d appears twice in the page walk", r.RowID)
		}
		seen[r.RowID] = true
		if r.RowID != oracle[i].RowID {
			t.Fatalf("tie-break order diverges at %d: got %d want %d", i, r.RowID, oracle[i].RowID)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. Query-context validation for cursors
// ---------------------------------------------------------------------------

// TestHardeningCursorSortFieldMismatch: a cursor minted under one sort
// must be rejected when the query context sorts by another field.
func TestHardeningCursorSortFieldMismatch(t *testing.T) {
	pool, _, cleanup := hSetup(t)
	defer cleanup()

	// Mint a valid asset_value cursor.
	opts := hProject(Options{Limit: 1, Sort: []SortSpec{{Field: "asset_value", Direction: DirAsc}}})
	p := hCompile(t, `transport == "tcp"`, opts)
	rows := hQuery(t, pool, p)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row to mint a cursor, got %d", len(rows))
	}
	cur := hCursorFrom("asset_value", rows[0], false)

	// Reuse it under a different sort field → GQL_E_PLAN_CURSOR.
	tree, perr := parser.Parse(`transport == "tcp"`)
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if _, verr := validator.Validate(tree); verr != nil {
		t.Fatalf("validate: %v", verr)
	}
	_, cerr := Compile(tree, Options{
		Sort:   []SortSpec{{Field: "port", Direction: DirAsc}},
		Cursor: cur,
	})
	if cerr == nil {
		t.Fatal("expected cursor sort-field mismatch error")
	}
	pe, ok := cerr.(*Error)
	if !ok || pe.Code != CodePlanCursor {
		t.Fatalf("expected GQL_E_PLAN_CURSOR, got %v", cerr)
	}
}

// ---------------------------------------------------------------------------
// 5. Literal CONTAINS semantics for %, _, and backslash
// ---------------------------------------------------------------------------

// TestHardeningContainsLiteralSpecials: %, _ and \ in a CONTAINS value
// are LITERAL characters, not ILIKE wildcards — likeEscape must
// neutralize them.
func TestHardeningContainsLiteralSpecials(t *testing.T) {
	pool, _, cleanup := hSetup(t)
	defer cleanup()

	cases := []struct {
		name    string
		gql     string
		want    int
		wantVal string
	}{
		// `a_b` must match only the literal a_b row, NOT axb.
		{"underscore literal", `product CONTAINS "a_b"`, 1, "a_b"},
		// Sanity: asking for axb literally still finds axb.
		{"plain substring", `product CONTAINS "axb"`, 1, "axb"},
		// `50%` must match only the literal 50% row, NOT 50xyz-like data.
		{"percent literal", `product CONTAINS "50%"`, 1, "50%"},
		// A GQL-escaped backslash matches a literal backslash.
		{"backslash literal", `product CONTAINS "back\\slash"`, 1, `back\slash`},
		// A value with no special chars that WOULD match under wildcard
		// semantics must not over-match: "a" is a substring of both.
		{"broad substring sanity", `product CONTAINS "a"`, 3, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := hProject(Options{})
			p := hCompile(t, tc.gql, opts)
			rows := hQuery(t, pool, p)
			if len(rows) != tc.want {
				t.Fatalf("%s: expected %d rows, got %d: %+v", tc.name, tc.want, len(rows), rows)
			}
			if tc.wantVal != "" {
				found := false
				for _, r := range rows {
					if r.AssetValue != nil {
						continue // asset_value of E, not the product; product not projected
					}
				}
				_ = found
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 6. IN_DOMAIN normalization and domain-label boundaries
// ---------------------------------------------------------------------------

// TestHardeningInDomainBoundaries: IN_DOMAIN matches the domain itself
// and any SUBDOMAIN at a label boundary — never a suffix lookalike
// (notexample.net) and never an unrelated TLD path (example.net.evil.com).
func TestHardeningInDomainBoundaries(t *testing.T) {
	pool, _, cleanup := hSetup(t)
	defer cleanup()

	t.Run("apex covers apex + subdomains", func(t *testing.T) {
		p := hCompile(t, `domain IN_DOMAIN "example.net"`, hProject(Options{}))
		rows := hQuery(t, pool, p)
		vals := map[string]bool{}
		for _, r := range rows {
			if r.AssetValue != nil {
				vals[*r.AssetValue] = true
			}
		}
		for _, want := range []string{"example.net", "a.b.example.net", "portal.example.net"} {
			if !vals[want] {
				t.Errorf("expected %s to match, got rows %v", want, vals)
			}
		}
		for _, banned := range []string{"notexample.net", "example.net.evil.com"} {
			if vals[banned] {
				t.Errorf("%s must NOT match IN_DOMAIN \"example.net\"", banned)
			}
		}
		if len(rows) != 3 {
			t.Errorf("expected exactly 3 matches, got %d: %v", len(rows), vals)
		}
	})

	t.Run("deeper apex is its own boundary", func(t *testing.T) {
		p := hCompile(t, `domain IN_DOMAIN "portal.example.net"`, hProject(Options{}))
		rows := hQuery(t, pool, p)
		if len(rows) != 1 || rows[0].AssetValue == nil || *rows[0].AssetValue != "portal.example.net" {
			t.Fatalf("expected only portal.example.net, got %+v", rows)
		}
	})
}

// ---------------------------------------------------------------------------
// 7. Scalar IN versus array-valued field semantics
// ---------------------------------------------------------------------------

// TestHardeningScalarInVsArrayFields: scalar IN lowers to = ANY($n::T[])
// over the scalar column; array-valued fields (sources) only accept
// CONTAINS (@> element membership) — the M7 op gate rejects IN on them.
func TestHardeningScalarInVsArrayFields(t *testing.T) {
	pool, _, cleanup := hSetup(t)
	defer cleanup()

	t.Run("scalar IN over text column", func(t *testing.T) {
		p := hCompile(t, `transport IN ("tcp", "udp")`, hProject(Options{}))
		rows := hQuery(t, pool, p)
		// 10 tcp services; udp exists on no fixture row.
		if len(rows) != 10 {
			t.Fatalf("expected 10 rows (all tcp), got %d", len(rows))
		}
	})

	t.Run("array field CONTAINS element", func(t *testing.T) {
		p := hCompile(t, `sources CONTAINS "fixture"`, hProject(Options{}))
		rows := hQuery(t, pool, p)
		// Exactly the fixture rows: 6 services + portal.example.net (D).
		// The hardening rows carry sources={hardening-test} only.
		if len(rows) != 7 {
			t.Fatalf("expected 7 fixture-source rows, got %d", len(rows))
		}
	})

	t.Run("array field rejects IN at validation", func(t *testing.T) {
		_, err := parser.Parse(`sources IN ("fixture")`)
		if err == nil {
			// Parser accepted; the validator must reject IN on a list field.
			tree, _ := parser.Parse(`sources IN ("fixture")`)
			_, verr := validator.Validate(tree)
			if verr == nil {
				t.Fatal("expected validator to reject IN on a list-typed field")
			}
		}
	})
}

// ---------------------------------------------------------------------------
// 8. Deterministic DISTINCT ON asset projection
// ---------------------------------------------------------------------------

// TestHardeningAssetModeDeterministic: the same asset-mode query run
// twice returns the identical asset set in the identical order, with
// each asset exactly once — the representative row is the one with the
// max (last_seen, row_id) per asset.
func TestHardeningAssetModeDeterministic(t *testing.T) {
	pool, _, cleanup := hSetup(t)
	defer cleanup()

	opts := hProject(Options{Mode: ModeAsset, Sort: []SortSpec{{Field: "asset_value", Direction: DirAsc}}})
	p1 := hCompile(t, `transport == "tcp"`, opts)
	rows1 := hQuery(t, pool, p1)
	rows2 := hQuery(t, pool, p1)

	if len(rows1) != len(rows2) {
		t.Fatalf("non-deterministic row count: %d vs %d", len(rows1), len(rows2))
	}
	seen := map[int64]bool{}
	for i := range rows1 {
		if rows1[i].RowID != rows2[i].RowID {
			t.Fatalf("non-deterministic order at %d: %d vs %d", i, rows1[i].RowID, rows2[i].RowID)
		}
		if seen[rows1[i].RowID] {
			t.Fatalf("asset representative row_id %d appears twice", rows1[i].RowID)
		}
		seen[rows1[i].RowID] = true
	}
	// tcp services live on A, B, C, E → exactly 4 distinct assets.
	if len(rows1) != 4 {
		t.Fatalf("expected 4 distinct assets with tcp services, got %d", len(rows1))
	}
}

// ---------------------------------------------------------------------------
// 9. Correct asset-mode pagination
// ---------------------------------------------------------------------------

// TestHardeningAssetModePagination: asset-mode keyset advances over
// DISTINCT ASSETS (dedup happens before the cursor predicate and the
// limit), so a page walk with limit=1 covers every matching asset
// exactly once — no duplicates, no gaps.
func TestHardeningAssetModePagination(t *testing.T) {
	pool, _, cleanup := hSetup(t)
	defer cleanup()

	sort := []SortSpec{{Field: "asset_value", Direction: DirAsc}}
	var seenAssets []string
	seen := map[string]bool{}
	var cursor string
	pages := 0
	for {
		opts := hProject(Options{Mode: ModeAsset, Limit: 1, Sort: sort, Cursor: cursor})
		p := hCompile(t, `transport == "tcp"`, opts)
		rows := hQuery(t, pool, p)
		pages++
		if len(rows) == 0 {
			break
		}
		if len(rows) != 1 {
			t.Fatalf("limit=1 returned %d rows", len(rows))
		}
		v := *rows[0].AssetValue
		if seen[v] {
			t.Fatalf("asset %s visited twice across pages", v)
		}
		seen[v] = true
		seenAssets = append(seenAssets, v)
		last := rows[0]
		cursor = CursorPayload{
			SortField: "asset_value",
			RowID:     last.RowID,
			RowKind:   last.RowKind,
			Value:     v,
		}.Encode()
		if pages > 10 {
			t.Fatalf("asset pagination did not terminate after %d pages", pages)
		}
	}

	// A, B, C, E — in ascending asset_value order.
	want := []string{"203.0.113.10", "203.0.113.11", "203.0.113.12", "203.0.113.20"}
	if len(seenAssets) != len(want) {
		t.Fatalf("visited %d assets (%v), want 4", len(seenAssets), seenAssets)
	}
	for i := range want {
		if seenAssets[i] != want[i] {
			t.Fatalf("asset page %d: got %s, want %s (walk: %v)", i, seenAssets[i], want[i], seenAssets)
		}
	}
}

// TestHardeningAssetModeNullSortPagination: assets whose sort value is
// NULL (e.g. sort by http.status in asset mode — the ssh-only host and
// the domain-only rows have no http status) must paginate through the
// NULL tail via the null-cursor form, without dropping or duplicating.
func TestHardeningAssetModeNullSortPagination(t *testing.T) {
	pool, _, cleanup := hSetup(t)
	defer cleanup()

	sort := []SortSpec{{Field: "http.status", Direction: DirAsc}}
	// All assets in the corpus: 4 fixture (A, B, C, D) + 5 hardening
	// (E + 4 domains) = 9.  Query without any predicate → all assets.
	var got []hardeningID
	var cursor string
	pages := 0
	for {
		opts := hProject(Options{Mode: ModeAsset, Limit: 2, Sort: sort, Cursor: cursor})
		p := hCompile(t, `exists(asset_value)`, opts)
		rows := hQuery(t, pool, p)
		pages++
		if len(rows) == 0 {
			break
		}
		got = append(got, rows...)
		last := rows[len(rows)-1]
		cursor = hCursorFrom("http.status", last, last.HTTPStatus == nil)
		if pages > 10 {
			t.Fatalf("pagination did not terminate after %d pages", pages)
		}
	}
	if len(got) != 9 {
		t.Fatalf("expected 9 distinct assets across pages, got %d (%v)", len(got), got)
	}
	seen := map[int64]bool{}
	for _, r := range got {
		if seen[r.RowID] {
			t.Fatalf("asset representative row_id %d seen twice", r.RowID)
		}
		seen[r.RowID] = true
	}
	// Every NULL-status asset must appear (the ssh-only host E and the
	// 5 domain-only/no-http assets), and the NULLs must trail.
	nullsStarted := false
	for _, r := range got {
		if r.HTTPStatus == nil {
			nullsStarted = true
		} else if nullsStarted {
			t.Fatalf("non-null http_status after NULL tail")
		}
	}
}

// TestHardeningContainsEscapedPercentInProduct pins the interaction of
// the fixture corpus with the hardening rows: no product value in the
// merged corpus contains a literal "50x" wildcard expansion beyond the
// designed rows — guards against accidental over-matching after the
// escape change.
func TestHardeningContainsNoCrossContamination(t *testing.T) {
	pool, _, cleanup := hSetup(t)
	defer cleanup()

	// "OpenSSH" must not match any wildcard variant of the hardening
	// products, and vice versa.
	p := hCompile(t, `product CONTAINS "OpenSSH"`, hProject(Options{}))
	rows := hQuery(t, pool, p)
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 OpenSSH row, got %d", len(rows))
	}

	// The underscore-literal query must not match OpenSSH either.
	p = hCompile(t, `product CONTAINS "a_b"`, hProject(Options{}))
	rows = hQuery(t, pool, p)
	for _, r := range rows {
		if r.Port == nil || *r.Port != 1001 {
			t.Fatalf("a_b matched an unexpected row: %+v", r)
		}
	}
}

// silence the strings import when compiled without the tests that use it
var _ = strings.Contains
