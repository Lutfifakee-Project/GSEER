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

import (
	"strings"
	"testing"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/gql/ast"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/parser"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/validator"
)

// planner_test.go — unit tests for the M9 planner.
//
// These tests compile GQL → SQL and assert:
//   - the SQL shape (regex/substring match against expected fragments);
//   - the parameter vector (positional values);
//   - branch classification;
//   - error branches (empty AST, bad sort, bad cursor, limit/timeout
//     out of range, projection field not a column);
//   - the AC-2.8 invariant: literal values NEVER appear in the SQL
//     string (only $N placeholders).
//
// Integration tests (running the SQL against PG with the fixture
// corpus) live in integration_test.go behind the integration build tag.

// compile is the test helper: lex → parse → validate → Compile.
func compile(t *testing.T, gql string, opts Options) *Plan {
	t.Helper()
	tree, err := parser.Parse(gql)
	if err != nil {
		t.Fatalf("parser: %v", err)
	}
	if _, err := validator.Validate(tree); err != nil {
		t.Fatalf("validator: %v", err)
	}
	p, err := Compile(tree, opts)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return p
}

// mustParseValidate runs parser+validator without Compile, for tests
// that need to assert a Compile error against a valid AST.
func mustParseValidate(t *testing.T, gql string) ast.Node {
	t.Helper()
	tree, err := parser.Parse(gql)
	if err != nil {
		t.Fatalf("parser: %v", err)
	}
	if _, err := validator.Validate(tree); err != nil {
		t.Fatalf("validator: %v", err)
	}
	return tree
}

// assertNoLiteral scans the SQL for any of the literal values that
// must be parameterized (AC-2.8).  Numbers, IP/CIDR strings, domain
// strings, and ILIKE pattern values all count.
func assertNoLiteral(t *testing.T, sql string, literals ...string) {
	t.Helper()
	for _, lit := range literals {
		if strings.Contains(sql, lit) {
			t.Errorf("AC-2.8 violation: literal %q appears in SQL:\n%s", lit, sql)
		}
	}
}

func TestCompileWitnessPortEq(t *testing.T) {
	p := compile(t, `port == 443`, Options{})
	if !p.Branch.Witness {
		t.Errorf("expected witness branch, got %s", p.Branch)
	}
	if !strings.Contains(p.SQL, `"port" = $1`) {
		t.Errorf("expected port = $1 fragment, got:\n%s", p.SQL)
	}
	// 2 args: the port value + the limit.  The timeout is no longer
	// embedded as a placeholder (it lives on Plan.Timeout and is
	// applied via ApplyStatementTimeoutSQL).
	if len(p.Args) != 2 {
		t.Errorf("expected 2 args (value + limit), got %d (%v)", len(p.Args), p.Args)
	}
	if p.Args[0] != int64(443) {
		t.Errorf("expected first arg 443, got %v", p.Args[0])
	}
	assertNoLiteral(t, p.SQL, "443")
}

func TestCompileAndOrPrecedence(t *testing.T) {
	p := compile(t, `port == 443 && http.status == 200 || ip == "10.0.0.1"`, Options{})
	// Expect ((port=$1 AND http.status=$2) OR ip=$3).
	if !strings.Contains(p.SQL, `search_index."port" = $1 AND search_index."http_status" = $2`) {
		t.Errorf("missing AND arm:\n%s", p.SQL)
	}
	if !strings.Contains(p.SQL, `OR search_index."ip" = $3`) {
		t.Errorf("missing OR arm:\n%s", p.SQL)
	}
	assertNoLiteral(t, p.SQL, "443", "200", "10.0.0.1")
}

func TestCompileNot(t *testing.T) {
	p := compile(t, `!(port == 443)`, Options{})
	if !strings.Contains(p.SQL, `(NOT (search_index."port" = $1))`) {
		t.Errorf("expected NOT wrapper, got:\n%s", p.SQL)
	}
}

func TestCompileExists(t *testing.T) {
	p := compile(t, `exists(http.title)`, Options{})
	if !strings.Contains(p.SQL, `"http_title" IS NOT NULL`) {
		t.Errorf("expected IS NOT NULL fragment, got:\n%s", p.SQL)
	}
	if !p.Branch.Witness {
		t.Errorf("exists() should classify as witness, got %s", p.Branch)
	}
}

func TestCompileInListStrings(t *testing.T) {
	p := compile(t, `transport IN ("tcp", "udp", "sctp")`, Options{})
	if !strings.Contains(p.SQL, `= ANY($1::text[])`) {
		t.Errorf("expected ANY($1::text[]), got:\n%s", p.SQL)
	}
	arr, ok := p.Args[0].([]string)
	if !ok || len(arr) != 3 || arr[0] != "tcp" {
		t.Errorf("expected [tcp udp sctp], got %v", p.Args[0])
	}
	assertNoLiteral(t, p.SQL, "tcp", "udp", "sctp")
}

func TestCompileInListInts(t *testing.T) {
	p := compile(t, `port IN (80, 443, 8080)`, Options{})
	if !strings.Contains(p.SQL, `= ANY($1::bigint[])`) {
		t.Errorf("expected ANY($1::bigint[]), got:\n%s", p.SQL)
	}
	arr, ok := p.Args[0].([]int64)
	if !ok || len(arr) != 3 || arr[2] != 8080 {
		t.Errorf("expected [80 443 8080], got %v", p.Args[0])
	}
	assertNoLiteral(t, p.SQL, "80", "443", "8080")
}

func TestCompileContainsString(t *testing.T) {
	p := compile(t, `http.title CONTAINS "login"`, Options{})
	if !strings.Contains(p.SQL, `"http_title" ILIKE $1 ESCAPE '\'`) {
		t.Errorf("expected ILIKE with ESCAPE, got:\n%s", p.SQL)
	}
	if p.Args[0] != "%login%" {
		t.Errorf("expected %%login%%, got %v", p.Args[0])
	}
	assertNoLiteral(t, p.SQL, "login")
}

func TestCompileContainsList(t *testing.T) {
	p := compile(t, `technologies CONTAINS "nginx"`, Options{})
	if !strings.Contains(p.SQL, `"technologies" @> ARRAY[$1]::text[]`) {
		t.Errorf("expected @> ARRAY fragment, got:\n%s", p.SQL)
	}
	if p.Args[0] != "nginx" {
		t.Errorf("expected nginx, got %v", p.Args[0])
	}
	assertNoLiteral(t, p.SQL, "nginx")
}

func TestCompileInCidr(t *testing.T) {
	p := compile(t, `ip IN_CIDR "10.0.0.0/8"`, Options{})
	if !strings.Contains(p.SQL, `"ip"::inet <<= $1::cidr`) {
		t.Errorf("expected inet <<= cidr, got:\n%s", p.SQL)
	}
	if p.Args[0] != "10.0.0.0/8" {
		t.Errorf("expected CIDR string, got %v", p.Args[0])
	}
	assertNoLiteral(t, p.SQL, "10.0.0.0/8")
}

func TestCompileInDomain(t *testing.T) {
	p := compile(t, `domain IN_DOMAIN "example.com"`, Options{})
	// Expect (domain = $1 OR domain ILIKE $2 ESCAPE '\').
	if !strings.Contains(p.SQL, `search_index."domain" = $1 OR search_index."domain" ILIKE $2 ESCAPE '\'`) {
		t.Errorf("expected OR-ILIKE fragment, got:\n%s", p.SQL)
	}
	if p.Args[0] != "example.com" || p.Args[1] != "%.example.com" {
		t.Errorf("expected [example.com pct.example.com], got %v", p.Args[:2])
	}
	assertNoLiteral(t, p.SQL, "example.com")
}

func TestCompileAnyHostBranch(t *testing.T) {
	p := compile(t, `ANY(host){ http.status == 200 }`, Options{})
	if !p.Branch.AnyUnion {
		t.Errorf("expected any-union branch, got %s", p.Branch)
	}
	if !strings.Contains(p.SQL, "EXISTS (SELECT 1 FROM search_index host_row") {
		t.Errorf("expected EXISTS subquery, got:\n%s", p.SQL)
	}
	if !strings.Contains(p.SQL, `host_row.asset_id = search_index.asset_id`) {
		t.Errorf("expected correlated asset_id link, got:\n%s", p.SQL)
	}
	if !strings.Contains(p.SQL, `host_row.row_kind = 'service'`) {
		t.Errorf("expected row_kind = 'service' filter, got:\n%s", p.SQL)
	}
	// The 'service' literal in the EXISTS is structural (constant),
	// not a user value — it is allowed.
}

func TestCompileAnyServiceFoldsInline(t *testing.T) {
	p := compile(t, `ANY(service){ port == 443 }`, Options{})
	if !p.Branch.AnyService {
		t.Errorf("expected any-service-inline, got %s", p.Branch)
	}
	if !p.Branch.Witness {
		t.Errorf("any-service should also mark witness, got %s", p.Branch)
	}
	// No EXISTS subquery: folded into the outer alias.
	if strings.Contains(p.SQL, "EXISTS") {
		t.Errorf("any(service) should not emit EXISTS, got:\n%s", p.SQL)
	}
	if !strings.Contains(p.SQL, `"port" = $1`) {
		t.Errorf("expected port = $1 inline, got:\n%s", p.SQL)
	}
}

func TestCompileModeAssetWrapsDistinctOn(t *testing.T) {
	p := compile(t, `port == 443`, Options{Mode: ModeAsset})
	if !p.Branch.AssetAll {
		t.Errorf("expected asset-all branch, got %s", p.Branch)
	}
	if !strings.Contains(p.SQL, "WITH page AS (") {
		t.Errorf("expected WITH page AS wrapper, got:\n%s", p.SQL)
	}
	if !strings.Contains(p.SQL, "DISTINCT ON (asset_id)") {
		t.Errorf("expected DISTINCT ON (asset_id), got:\n%s", p.SQL)
	}
	if !strings.Contains(p.SQL, "ORDER BY asset_id, last_seen DESC NULLS LAST, row_id ASC") {
		t.Errorf("expected final ORDER BY asset_id, last_seen DESC, row_id ASC, got:\n%s", p.SQL)
	}
}

func TestCompileSortDefault(t *testing.T) {
	p := compile(t, `port == 443`, Options{})
	// Default sort: last_seen DESC NULLS FIRST, then the composite row
	// key (row_id ASC NULLS LAST, row_kind ASC).
	if !strings.Contains(p.SQL, `ORDER BY "last_seen" DESC NULLS FIRST, "row_id" ASC NULLS LAST, "row_kind" ASC`) {
		t.Errorf("expected default sort with composite tiebreak, got:\n%s", p.SQL)
	}
}

func TestCompileSortExplicit(t *testing.T) {
	p := compile(t, `port == 443`, Options{Sort: []SortSpec{{Field: "port", Direction: DirAsc}}})
	if !strings.Contains(p.SQL, `ORDER BY "port" ASC NULLS LAST`) {
		t.Errorf("expected port ASC, got:\n%s", p.SQL)
	}
}

func TestCompileSortUnknown(t *testing.T) {
	tree := mustParseValidate(t, `port == 443`)
	if _, err := Compile(tree, Options{Sort: []SortSpec{{Field: "bogus"}}}); err == nil {
		t.Fatal("expected error for unknown sort field")
	} else {
		pe, ok := err.(*Error)
		if !ok || pe.Code != CodePlanSortField {
			t.Errorf("expected GQL_E_PLAN_SORT_FIELD, got %v", err)
		}
	}
}

func TestCompileSortListFieldRejected(t *testing.T) {
	tree := mustParseValidate(t, `port == 443`)
	// technologies is a list field — not sortable.
	if _, err := Compile(tree, Options{Sort: []SortSpec{{Field: "technologies"}}}); err == nil {
		t.Fatal("expected error for non-sortable list field")
	} else {
		pe, ok := err.(*Error)
		if !ok || pe.Code != CodePlanSortField {
			t.Errorf("expected GQL_E_PLAN_SORT_FIELD, got %v", err)
		}
	}
}

func TestCompileCursorAdvancement(t *testing.T) {
	cp := CursorPayload{SortField: "port", RowID: 99, Value: "443"}
	cur := cp.Encode()
	tree := mustParseValidate(t, `port == 443`)
	p, err := Compile(tree, Options{Sort: []SortSpec{{Field: "port", Direction: DirAsc}}, Cursor: cur})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// ASC + non-null cursor: >, =-and-rowkey, OR col IS NULL (NULLs sort
	// after every non-null value under NULLS LAST), typed ::bigint cast.
	if !strings.Contains(p.SQL, `"port" > ($`) {
		t.Errorf("expected cursor > predicate with cast, got:\n%s", p.SQL)
	}
	if !strings.Contains(p.SQL, `(row_id > $`) || !strings.Contains(p.SQL, `AND row_kind > $`) {
		t.Errorf("expected composite row-key advancement, got:\n%s", p.SQL)
	}
	if !strings.Contains(p.SQL, `OR "port" IS NULL)`) {
		t.Errorf("expected NULL-tail arm in ASC cursor predicate, got:\n%s", p.SQL)
	}
	if !strings.Contains(p.SQL, `::bigint`) {
		t.Errorf("expected typed cast on cursor value, got:\n%s", p.SQL)
	}
	if p.CursorDecoded == nil || p.CursorDecoded.RowID != 99 {
		t.Errorf("cursor not decoded: %v", p.CursorDecoded)
	}
}

func TestCompileCursorDirectionFlipped(t *testing.T) {
	cp := CursorPayload{SortField: "port", RowID: 99, Value: "443"}
	cur := cp.Encode()
	tree := mustParseValidate(t, `port == 443`)
	p, err := Compile(tree, Options{Sort: []SortSpec{{Field: "port", Direction: DirDesc}}, Cursor: cur})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// DESC + non-null cursor: < with cast; NO "IS NULL" arm (NULLs sit
	// at the DESC head under NULLS FIRST and are already behind us).
	if !strings.Contains(p.SQL, `"port" < ($`) {
		t.Errorf("expected cursor < predicate for DESC, got:\n%s", p.SQL)
	}
	if strings.Contains(p.SQL, `"port" IS NULL`) {
		t.Errorf("DESC cursor predicate must not include the NULL arm:\n%s", p.SQL)
	}
}

func TestCompileCursorNullForms(t *testing.T) {
	tree := mustParseValidate(t, `port == 443`)

	// ASC + Null cursor: only the NULL-tail form.
	cur := CursorPayload{SortField: "port", RowID: 7, RowKind: "service", Null: true}.Encode()
	p, err := Compile(tree, Options{Sort: []SortSpec{{Field: "port", Direction: DirAsc}}, Cursor: cur})
	if err != nil {
		t.Fatalf("Compile ASC null: %v", err)
	}
	if !strings.Contains(p.SQL, `("port" IS NULL AND (row_id > $`) {
		t.Errorf("expected ASC null-cursor predicate, got:\n%s", p.SQL)
	}
	if strings.Contains(p.SQL, `"port" > ($`) {
		t.Errorf("ASC null cursor must not emit the value comparison:\n%s", p.SQL)
	}

	// DESC + Null cursor: NULL head form with the IS NOT NULL fallthrough.
	p, err = Compile(tree, Options{Sort: []SortSpec{{Field: "port", Direction: DirDesc}}, Cursor: cur})
	if err != nil {
		t.Fatalf("Compile DESC null: %v", err)
	}
	if !strings.Contains(p.SQL, `(("port" IS NULL AND (row_id > $`) ||
		!strings.Contains(p.SQL, `OR "port" IS NOT NULL)`) {
		t.Errorf("expected DESC null-cursor predicate, got:\n%s", p.SQL)
	}
}

func TestCompileCursorFieldMismatch(t *testing.T) {
	cp := CursorPayload{SortField: "port", RowID: 99, Value: "443"}
	cur := cp.Encode()
	tree := mustParseValidate(t, `port == 443`)
	_, err := Compile(tree, Options{Sort: []SortSpec{{Field: "asn"}}, Cursor: cur})
	if err == nil {
		t.Fatal("expected cursor field mismatch error")
	}
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodePlanCursor {
		t.Errorf("expected GQL_E_PLAN_CURSOR, got %v", err)
	}
}

func TestCompileCursorMalformed(t *testing.T) {
	tree := mustParseValidate(t, `port == 443`)
	_, err := Compile(tree, Options{Cursor: "!!!"})
	if err == nil {
		t.Fatal("expected malformed cursor error")
	}
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodePlanCursor {
		t.Errorf("expected GQL_E_PLAN_CURSOR, got %v", err)
	}
}

func TestCompileLimitDefault(t *testing.T) {
	p := compile(t, `port == 443`, Options{})
	if p.Limit != DefaultLimit {
		t.Errorf("expected default limit %d, got %d", DefaultLimit, p.Limit)
	}
	if !strings.Contains(p.SQL, "LIMIT $") {
		t.Errorf("expected LIMIT placeholder, got:\n%s", p.SQL)
	}
}

func TestCompileLimitOutOfRange(t *testing.T) {
	tree := mustParseValidate(t, `port == 443`)
	_, err := Compile(tree, Options{Limit: -1})
	if err == nil {
		t.Fatal("expected negative limit error")
	}
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodePlanLimit {
		t.Errorf("expected GQL_E_PLAN_LIMIT, got %v", err)
	}
	_, err = Compile(tree, Options{Limit: MaxLimit + 1})
	if err == nil {
		t.Fatal("expected over-limit error")
	}
	pe, ok = err.(*Error)
	if !ok || pe.Code != CodePlanLimit {
		t.Errorf("expected GQL_E_PLAN_LIMIT, got %v", err)
	}
}

func TestCompileTimeoutDefault(t *testing.T) {
	p := compile(t, `port == 443`, Options{})
	if p.Timeout != 5*time.Second {
		t.Errorf("expected 5s default timeout, got %s", p.Timeout)
	}
}

func TestCompileTimeoutOutOfRange(t *testing.T) {
	tree := mustParseValidate(t, `port == 443`)
	_, err := Compile(tree, Options{Timeout: time.Microsecond})
	if err == nil {
		t.Fatal("expected sub-min timeout error")
	}
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodePlanTimeout {
		t.Errorf("expected GQL_E_PLAN_TIMEOUT, got %v", err)
	}
	_, err = Compile(tree, Options{Timeout: 31 * time.Second})
	if err == nil {
		t.Fatal("expected over-cap timeout error")
	}
	pe, ok = err.(*Error)
	if !ok || pe.Code != CodePlanTimeout {
		t.Errorf("expected GQL_E_PLAN_TIMEOUT, got %v", err)
	}
}

func TestCompileEmptyAST(t *testing.T) {
	_, err := Compile(nil, Options{})
	if err == nil {
		t.Fatal("expected empty AST error")
	}
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodePlanEmpty {
		t.Errorf("expected GQL_E_PLAN_EMPTY, got %v", err)
	}
}

func TestCompileProjectionFields(t *testing.T) {
	p := compile(t, `port == 443`, Options{Fields: []string{"ip", "port", "http.title"}})
	if !strings.Contains(p.SQL, `"ip", "port", "http_title"`) {
		t.Errorf("expected quoted projection cols, got:\n%s", p.SQL)
	}
}

func TestCompileProjectionFieldUnknown(t *testing.T) {
	tree := mustParseValidate(t, `port == 443`)
	_, err := Compile(tree, Options{Fields: []string{"bogus"}})
	if err == nil {
		t.Fatal("expected unknown projection field error")
	}
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodePlanProjection {
		t.Errorf("expected GQL_E_PLAN_PROJECTION, got %v", err)
	}
}

func TestCompileStatementTimeoutOnPlan(t *testing.T) {
	p := compile(t, `port == 443`, Options{})
	if p.Timeout != 5*time.Second {
		t.Errorf("expected default 5s timeout on Plan, got %s", p.Timeout)
	}
	// The Plan.SQL must NOT contain a SET LOCAL prefix — the timeout is
	// applied separately via ApplyStatementTimeoutSQL so it stays
	// parameterized (AC-2.8).
	if strings.Contains(p.SQL, "statement_timeout") {
		t.Errorf("Plan.SQL should not embed statement_timeout:\n%s", p.SQL)
	}
	sql, args := p.ApplyStatementTimeoutSQL()
	if sql == "" {
		t.Fatal("expected non-empty timeout SQL")
	}
	if !strings.Contains(sql, "set_config('statement_timeout'") {
		t.Errorf("expected set_config form, got %s", sql)
	}
	if len(args) != 1 || args[0] != "5000" {
		t.Errorf("expected [5000], got %v", args)
	}
}

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"":        ModeService,
		"service": ModeService,
		"SERVICE": ModeService,
		"asset":   ModeAsset,
		"Asset":   ModeAsset,
	}
	for in, want := range cases {
		got, err := ParseMode(in)
		if err != nil {
			t.Errorf("ParseMode(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMode(%q): got %s want %s", in, got, want)
		}
	}
	if _, err := ParseMode("bogus"); err == nil {
		t.Error("expected error for bogus mode")
	}
}

func TestParseDirection(t *testing.T) {
	cases := map[string]Direction{
		"":           DirAsc,
		"asc":        DirAsc,
		"ASC":        DirAsc,
		"desc":       DirDesc,
		"descending": DirDesc,
	}
	for in, want := range cases {
		got, err := ParseDirection(in)
		if err != nil {
			t.Errorf("ParseDirection(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseDirection(%q): got %s want %s", in, got, want)
		}
	}
	if _, err := ParseDirection("sideways"); err == nil {
		t.Error("expected error for bogus direction")
	}
}

func TestCursorEncodeDecodeRoundTrip(t *testing.T) {
	cp := CursorPayload{SortField: "last_seen", RowID: 12345, Value: "2026-01-01T00:00:00Z"}
	s := cp.Encode()
	out, err := DecodeCursor(s)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if out.SortField != cp.SortField || out.RowID != cp.RowID || out.Value != cp.Value {
		t.Errorf("roundtrip mismatch: in=%+v out=%+v", cp, out)
	}
}

func TestDecodeCursorEmpty(t *testing.T) {
	out, err := DecodeCursor("")
	if err != nil {
		t.Fatalf("expected nil,nil for empty cursor, got %v", err)
	}
	if out != nil {
		t.Errorf("expected nil payload, got %+v", out)
	}
}

func TestExplainRendering(t *testing.T) {
	p := compile(t, `port == 443`, Options{Mode: ModeAsset})
	expl := p.Explain()
	for _, want := range []string{"GQL Plan", "mode:     asset", "branch:", "asset-all", "limit:", "timeout:", "SQL", "args:"} {
		if !strings.Contains(expl, want) {
			t.Errorf("explain missing %q:\n%s", want, expl)
		}
	}
}

func TestSortedFieldNamesExcludesListAndBool(t *testing.T) {
	names := SortedFieldNames()
	contains := func(s string) bool {
		for _, n := range names {
			if n == s {
				return true
			}
		}
		return false
	}
	if contains("technologies") {
		t.Errorf("technologies should not be sortable, found in %v", names)
	}
	if contains("stale") {
		t.Errorf("stale should not be sortable, found in %v", names)
	}
	if contains("sources") {
		t.Errorf("sources should not be sortable, found in %v", names)
	}
	if !contains("port") {
		t.Errorf("port should be sortable, missing from %v", names)
	}
	if !contains("last_seen") {
		t.Errorf("last_seen should be sortable, missing from %v", names)
	}
}

func TestCompileCompoundAnyHostAndWitness(t *testing.T) {
	// Top-level AND: witness (port==443) AND any-union (ANY(host){http.status==200}).
	p := compile(t, `port == 443 && ANY(host){ http.status == 200 }`, Options{})
	if !p.Branch.Witness || !p.Branch.AnyUnion {
		t.Errorf("expected witness+any-union, got %s", p.Branch)
	}
	if !strings.Contains(p.SQL, `"port" = $1`) {
		t.Errorf("missing witness fragment")
	}
	if !strings.Contains(p.SQL, "EXISTS (SELECT 1 FROM search_index host_row") {
		t.Errorf("missing EXISTS subquery:\n%s", p.SQL)
	}
}
