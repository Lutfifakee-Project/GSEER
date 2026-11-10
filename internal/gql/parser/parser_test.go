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

package parser

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Lutfifakee-Project/GSEER/internal/gql/ast"
)

// parser_test.go — the M6 acceptance tests: precedence, sugar, `!=` as
// its own operator, IN lists and their cap, quantified blocks, exists,
// positions, canonical-String fixed points, and every GQL_E_PARSE
// branch (including did-you-mean and the K-13 limits).

// mustParse parses or fails the test.
func mustParse(t *testing.T, src string) ast.Node {
	t.Helper()
	n, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return n
}

// parseErr parses expecting a *Error (GQL_E_PARSE).
func parseErr(t *testing.T, src string) *Error {
	t.Helper()
	n, err := Parse(src)
	if err == nil {
		t.Fatalf("Parse(%q) = %s, want GQL_E_PARSE error", src, n)
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("Parse(%q) error is %T (%v), want *parser.Error", src, err, err)
	}
	if pe.Code() != CodeParse {
		t.Errorf("code = %q, want %q", pe.Code(), CodeParse)
	}
	return pe
}

// eq builds a == comparison for test trees.
func eq(field string, n int64) *ast.Comparison {
	return &ast.Comparison{Field: field, Op: ast.OpEq, Value: &ast.IntValue{N: n}}
}

// TestParsePrecedence — `!` > `&&` > `||`, with parens overriding.
func TestParsePrecedence(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		// || binds loosest: a && b || c → (a && b) || c
		{"a==1 && b==2 || c==3", "((a == 1 && b == 2) || c == 3)"},
		// && binds tighter than ||
		{"a==1 || b==2 && c==3", "(a == 1 || (b == 2 && c == 3))"},
		// ! binds tightest (applies to the whole comparison)
		{"!a==1 && b==2", "(!(a == 1) && b == 2)"},
		{"!a==1 || !b==2", "(!(a == 1) || !(b == 2))"},
		// parens override (parens are syntax, not nodes: extra groups
		// collapse in the canonical form)
		{"(a==1 || b==2) && c==3", "((a == 1 || b == 2) && c == 3)"},
		{"((a==1))", "a == 1"},
		// long chain, left-associative
		{"a==1 && b==2 && c==3", "((a == 1 && b == 2) && c == 3)"},
		{"a==1 || b==2 || c==3", "((a == 1 || b == 2) || c == 3)"},
	}
	for _, c := range cases {
		got := mustParse(t, c.src).String()
		if got != c.want {
			t.Errorf("Parse(%q).String() =\n  %s\nwant:\n  %s", c.src, got, c.want)
		}
	}
}

// TestParseSugar — `=` is `==`, keyword booleans are the symbolic
// operators (any casing), and the tree cannot tell them apart.
func TestParseSugar(t *testing.T) {
	cases := []struct{ src, want string }{
		{`port = 443`, `port == 443`},
		{`port == 443`, `port == 443`},
		{`a==1 and b==2`, `(a == 1 && b == 2)`},
		{`a==1 AND b==2`, `(a == 1 && b == 2)`},
		{`a==1 && b==2`, `(a == 1 && b == 2)`},
		{`a==1 or b==2`, `(a == 1 || b == 2)`},
		{`a==1 || b==2`, `(a == 1 || b == 2)`},
		{`not a==1`, `!(a == 1)`},
		{`NOT a==1`, `!(a == 1)`},
		{`!a==1`, `!(a == 1)`},
		{`not not a==1`, `!(!(a == 1))`},
	}
	for _, c := range cases {
		if got := mustParse(t, c.src).String(); got != c.want {
			t.Errorf("Parse(%q).String() = %s, want %s", c.src, got, c.want)
		}
	}
}

// TestParseNeqOwnOperator — `field != "value"` is one comparison node,
// not a negated equality.
func TestParseNeqOwnOperator(t *testing.T) {
	n := mustParse(t, `http.title != "login"`)
	cmp, ok := n.(*ast.Comparison)
	if !ok {
		t.Fatalf("want *ast.Comparison, got %T", n)
	}
	if cmp.Op != ast.OpNeq {
		t.Errorf("op = %v, want OpNeq", cmp.Op)
	}
	if _, wrapped := cmp.Value.(*ast.StrValue); !wrapped {
		t.Errorf("value = %T, want *ast.StrValue", cmp.Value)
	}
	if got := n.String(); got != `http.title != "login"` {
		t.Errorf("String() = %s", got)
	}
}

// TestParseComparisonOperators — every operator reaches its AST Op.
func TestParseComparisonOperators(t *testing.T) {
	cases := map[string]ast.Op{
		`a == 1`:                       ast.OpEq,
		`a = 1`:                        ast.OpEq,
		`a != 1`:                       ast.OpNeq,
		`a < 1`:                        ast.OpLt,
		`a <= 1`:                       ast.OpLe,
		`a > 1`:                        ast.OpGt,
		`a >= 1`:                       ast.OpGe,
		`a CONTAINS "x"`:               ast.OpContains,
		`a contains "x"`:               ast.OpContains,
		`ip IN_CIDR "198.51.100.0/24"`: ast.OpInCidr,
		`ip in_cidr "198.51.100.0/24"`: ast.OpInCidr,
		`h IN_DOMAIN "example.net"`:    ast.OpInDomain,
	}
	for src, want := range cases {
		n := mustParse(t, src)
		cmp, ok := n.(*ast.Comparison)
		if !ok {
			t.Errorf("Parse(%q) = %T, want *ast.Comparison", src, n)
			continue
		}
		if cmp.Op != want {
			t.Errorf("Parse(%q).Op = %v, want %v", src, cmp.Op, want)
		}
	}
}

// TestParseInList — lists parse to ListValue; the ≤256 cap holds.
func TestParseInList(t *testing.T) {
	n := mustParse(t, `port IN (80, 443, 8443)`)
	cmp := n.(*ast.Comparison)
	lst := cmp.Value.(*ast.ListValue)
	if len(lst.Items) != 3 {
		t.Fatalf("list length = %d, want 3", len(lst.Items))
	}
	if got := n.String(); got != "port IN (80, 443, 8443)" {
		t.Errorf("String() = %s", got)
	}

	// 256 elements parse; 257 hit the cap.
	build := func(n int) string {
		items := make([]string, n)
		for i := range items {
			items[i] = fmt.Sprintf("%d", i)
		}
		return "port IN (" + strings.Join(items, ",") + ")"
	}
	if got := mustParse(t, build(256)).String(); !strings.HasPrefix(got, "port IN (0,") {
		t.Error("256-element IN list must parse")
	}
	pe := parseErr(t, build(257))
	if !strings.Contains(pe.Detail, "exceeds 256") {
		t.Errorf("257-element list error = %s", pe.Detail)
	}

	// Empty list is a grammar error (at least one element).
	parseErr(t, "port IN ()")
	// Trailing comma too.
	parseErr(t, "port IN (80,)")
}

// TestParseQuantified — ANY blocks build Quantified nodes; ALL parses
// (validator rejects in M7); dotted scope names are a parse error.
func TestParseQuantified(t *testing.T) {
	n := mustParse(t, `ANY(host){ port == 443 }`)
	q, ok := n.(*ast.Quantified)
	if !ok {
		t.Fatalf("want *ast.Quantified, got %T", n)
	}
	if q.Q != ast.QuantAny || q.Field != "host" {
		t.Errorf("quantifier = %v(%s), want ANY(host)", q.Q, q.Field)
	}
	if got := n.String(); got != "ANY(host){ port == 443 }" {
		t.Errorf("String() = %s", got)
	}

	n = mustParse(t, `ALL(service){ exists(http.title) && port == 80 }`)
	q = n.(*ast.Quantified)
	if q.Q != ast.QuantAll {
		t.Errorf("ALL parsed as %v", q.Q)
	}

	// Dotted scope → parse error.
	pe := parseErr(t, `ANY(http.title){ port == 80 }`)
	if !strings.Contains(pe.Detail, "simple name") {
		t.Errorf("dotted-scope error = %s", pe.Detail)
	}
	// Missing pieces.
	parseErr(t, `ANY port == 443 }`)
	parseErr(t, `ANY(host) port == 443`)
	parseErr(t, `ANY(host){ port == 443`)
}

// TestParseExists — a bare field is a presence test.
func TestParseExists(t *testing.T) {
	n := mustParse(t, `http.title`)
	ex, ok := n.(*ast.Exists)
	if !ok {
		t.Fatalf("want *ast.Exists, got %T", n)
	}
	if ex.Field != "http.title" {
		t.Errorf("field = %q", ex.Field)
	}
	if got := n.String(); got != "exists(http.title)" {
		t.Errorf("String() = %s", got)
	}

	// Mixed with predicates in a larger query.
	if got := mustParse(t, `port == 443 && http.title`).String(); got != `(port == 443 && exists(http.title))` {
		t.Errorf("combined String() = %s", got)
	}
}

// TestParseValues — strings decode through the lexer; integers parse.
func TestParseValues(t *testing.T) {
	n := mustParse(t, `http.title == "Admin \"Login\""`)
	cmp := n.(*ast.Comparison)
	if got := cmp.Value.(*ast.StrValue).S; got != `Admin "Login"` {
		t.Errorf("decoded string = %q", got)
	}

	n = mustParse(t, `port == 65535`)
	if got := n.(*ast.Comparison).Value.(*ast.IntValue).N; got != 65535 {
		t.Errorf("int = %d", got)
	}
}

// TestParsePositions — nodes carry their first-token positions.
func TestParsePositions(t *testing.T) {
	n := mustParse(t, "port == 443 &&\n  !(ip IN_CIDR \"198.51.100.0/24\")")
	or, _ := n.String(), ""
	_ = or
	root := n.(*ast.And)
	if root.Line != 1 || root.Col != 13 {
		t.Errorf("And pos = %d:%d, want 1:13", root.Line, root.Col)
	}
	not := root.R.(*ast.Not)
	if not.Line != 2 || not.Col != 3 {
		t.Errorf("Not pos = %d:%d, want 2:3", not.Line, not.Col)
	}
	cmp := root.L.(*ast.Comparison)
	if cmp.Line != 1 || cmp.Col != 1 {
		t.Errorf("Comparison pos = %d:%d, want 1:1", cmp.Line, cmp.Col)
	}
}

// TestParseCanonicalFixedPoint — parse(String(tree)) renders to the
// identical canonical form (the --explain contract).
func TestParseCanonicalFixedPoint(t *testing.T) {
	srcs := []string{
		`port == 443 && title CONTAINS "login"`,
		`!(a == 1) || ANY(host){ b == 2 } && ip IN_CIDR "198.51.100.0/24"`,
		`port in (80, 443) || http.title`,
		`not (a = 1 and (b == 2 or not c == 3))`,
	}
	for _, src := range srcs {
		once := mustParse(t, src).String()
		twice := mustParse(t, once).String()
		if once != twice {
			t.Errorf("canonical form is not a fixed point:\n  %s\n  %s", once, twice)
		}
	}
}

// TestParseErrors — the GQL_E_PARSE branches with positions and hints.
func TestParseErrors(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		line      int
		col       int
		detailHas string
		nextHas   string
	}{
		{"empty", "", 1, 1, "empty query", ""},
		{"whitespace only", "   ", 1, 4, "empty query", ""},
		{"trailing operator", "port == 443 &&", 1, 15, "unexpected end of query", ""},
		{"trailing token", "port == 443 port == 80", 1, 13, "unexpected field path", ""},
		{"missing value", "port ==", 1, 8, "missing value", ""},
		{"bad value", "port == port", 1, 9, "unexpected field path", ""},
		{"unclosed group", "(port == 443", 1, 13, "unexpected end of query", "close the group"},
		{"unopened group", "port == 443)", 1, 12, `unexpected ")"`, ""},
		{"missing op", "port 443", 1, 6, "unexpected integer literal", "an operator after field port"},
		{"bare && start", "&& port == 1", 1, 1, "unexpected", ""},
		{"contains typo hint", `title containz "x"`, 1, 7, "unexpected field path containz", `did you mean the keyword "contains"?`},
		{"quantifier typo hint", `ANYY(host){ port == 1 }`, 1, 1, "unexpected field path ANYY", `did you mean the keyword "any"?`},
		{"no hint for distant words", "port == 443 && 443", 1, 16, "unexpected integer literal", ""},
	}
	for _, c := range cases {
		pe := parseErr(t, c.src)
		if c.detailHas != "" && !strings.Contains(pe.Detail, c.detailHas) {
			t.Errorf("%s: detail = %q, want it to contain %q", c.name, pe.Detail, c.detailHas)
		}
		if c.nextHas != "" && !strings.Contains(pe.Next, c.nextHas) {
			t.Errorf("%s: next = %q, want it to contain %q", c.name, pe.Next, c.nextHas)
		}
		if c.line != 0 && (pe.Line != c.line || pe.Col != c.col) {
			t.Errorf("%s: error position = %d:%d, want %d:%d", c.name, pe.Line, pe.Col, c.line, c.col)
		}
		if strings.Contains(pe.Next, "did you mean") && c.nextHas == "" {
			t.Errorf("%s: unexpected did-you-mean hint: %q", c.name, pe.Next)
		}
	}
}

// TestParseDidYouMean — misspelled keywords get a concrete suggestion;
// distant words get none (a wrong guess is worse than none).
func TestParseDidYouMean(t *testing.T) {
	pe := parseErr(t, `title containz "x"`)
	if !strings.Contains(pe.Next, `did you mean the keyword "contains"?`) {
		t.Errorf("hint = %q, want contains suggestion", pe.Next)
	}
	pe = parseErr(t, `ANYY(host){ port == 1 }`)
	if !strings.Contains(pe.Next, `did you mean the keyword "any"?`) {
		t.Errorf("hint = %q, want any suggestion", pe.Next)
	}
	if pe.Line != 1 || pe.Col != 1 {
		t.Errorf("hint error position = %d:%d, want 1:1 (at the misspelled field)", pe.Line, pe.Col)
	}
}

// TestParseK13Limits — the parse-time resource bounds reject
// pathological trees with the K-13 wording.
func TestParseK13Limits(t *testing.T) {
	// Depth: 33 nested NOTs exceed maxDepth (32); 31 parse fine.
	deep := strings.Repeat("!", maxDepth+1) + "a == 1"
	pe := parseErr(t, deep)
	if !strings.Contains(pe.Detail, "nesting limit") {
		t.Errorf("depth-limit detail = %q", pe.Detail)
	}

	// Node count: a BALANCED tree over 1024 leaves holds 2047 nodes
	// > maxNodes (1024) while staying at depth 11 < maxDepth (32) —
	// so the node budget is what fires.
	leaves := make([]string, maxNodes)
	for i := range leaves {
		leaves[i] = fmt.Sprintf("f%d == 1", i)
	}
	for len(leaves) > 1 {
		next := make([]string, 0, len(leaves)/2+1)
		for j := 0; j+1 < len(leaves); j += 2 {
			next = append(next, "("+leaves[j]+" && "+leaves[j+1]+")")
		}
		if len(leaves)%2 == 1 {
			next = append(next, leaves[len(leaves)-1])
		}
		leaves = next
	}
	pe = parseErr(t, leaves[0])
	if !strings.Contains(pe.Detail, "node limit") {
		t.Errorf("node-limit detail = %q", pe.Detail)
	}

	// Just under the depth bound parses fine.
	ok := strings.Repeat("!", maxDepth-1) + "a == 1"
	if _, err := Parse(ok); err != nil {
		t.Errorf("depth-%d tree must parse, got: %v", maxDepth-1, err)
	}
}

// TestParseErrorRendering — GQL_E_PARSE renders what → why → Try.
func TestParseErrorRendering(t *testing.T) {
	pe := parseErr(t, "port == 443 &&")
	rendered := pe.Error()
	for _, want := range []string{"GQL_E_PARSE:", "\nWhy: ", "\nTry: "} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered error missing %q:\n%s", want, rendered)
		}
	}
}
