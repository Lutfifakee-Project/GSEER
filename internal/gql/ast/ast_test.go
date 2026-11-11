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

package ast

import (
	"strings"
	"testing"

	"github.com/Lutfifakee-Project/gseer/internal/gql/lexer"
)

// ast_test.go — the M8 acceptance tests: canonical String() rendering,
// the roundtrip property (String → re-lex → re-parse must reproduce the
// same tree — here verified against the token stream), Count/Depth
// accounting, and the Visitor contract.

// sampleTree builds the canonical test tree:
//
//	!(http.title != "Admin \"Login\"") && port IN (80, 443) || ANY(host){ exists(http.title) }
func sampleTree() Node {
	q := &Quantified{
		Q:     QuantAny,
		Field: "host",
		Body:  &Exists{Field: "http.title", Line: 1, Col: 58},
		Line:  1, Col: 54,
	}
	in := &Comparison{
		Field: "port",
		Op:    OpIn,
		Value: &ListValue{Items: []Value{&IntValue{N: 80}, &IntValue{N: 443}}},
		Line:  1, Col: 31,
	}
	neq := &Comparison{
		Field: "http.title",
		Op:    OpNeq,
		Value: &StrValue{S: "Admin \"Login\""},
		Line:  1, Col: 3,
	}
	and := &And{L: &Not{X: neq, Line: 1, Col: 2}, R: in, Line: 1, Col: 28}
	return &Or{L: and, R: q, Line: 1, Col: 36}
}

// TestCanonicalString — every node renders its stable canonical form.
func TestCanonicalString(t *testing.T) {
	cases := []struct {
		name string
		node Node
		want string
	}{
		{"eq", &Comparison{Field: "port", Op: OpEq, Value: &IntValue{N: 443}}, `port == 443`},
		{"neq", &Comparison{Field: "http.title", Op: OpNeq, Value: &StrValue{S: "x"}}, `http.title != "x"`},
		{"lt", &Comparison{Field: "port", Op: OpLt, Value: &IntValue{N: 1024}}, `port < 1024`},
		{"le", &Comparison{Field: "port", Op: OpLe, Value: &IntValue{N: 1024}}, `port <= 1024`},
		{"gt", &Comparison{Field: "port", Op: OpGt, Value: &IntValue{N: 1024}}, `port > 1024`},
		{"ge", &Comparison{Field: "port", Op: OpGe, Value: &IntValue{N: 1024}}, `port >= 1024`},
		{"contains", &Comparison{Field: "http.title", Op: OpContains, Value: &StrValue{S: "login"}}, `http.title CONTAINS "login"`},
		{"in_cidr", &Comparison{Field: "ip", Op: OpInCidr, Value: &StrValue{S: "198.51.100.0/24"}}, `ip IN_CIDR "198.51.100.0/24"`},
		{"in_domain", &Comparison{Field: "hostname", Op: OpInDomain, Value: &StrValue{S: "example.net"}}, `hostname IN_DOMAIN "example.net"`},
		{"in list", &Comparison{Field: "port", Op: OpIn, Value: &ListValue{Items: []Value{&IntValue{N: 80}, &IntValue{N: 443}}}}, `port IN (80, 443)`},
		{"exists", &Exists{Field: "http.title"}, `exists(http.title)`},
		{"any", &Quantified{Q: QuantAny, Field: "host", Body: &Exists{Field: "http.title"}}, `ANY(host){ exists(http.title) }`},
		{"all", &Quantified{Q: QuantAll, Field: "host", Body: &Exists{Field: "http.title"}}, `ALL(host){ exists(http.title) }`},
		{"not", &Not{X: &Comparison{Field: "port", Op: OpEq, Value: &IntValue{N: 1}}}, `!(port == 1)`},
		{"and", &And{
			L: &Comparison{Field: "a", Op: OpEq, Value: &IntValue{N: 1}},
			R: &Comparison{Field: "b", Op: OpEq, Value: &IntValue{N: 2}},
		}, `(a == 1 && b == 2)`},
		{"or", &Or{
			L: &Comparison{Field: "a", Op: OpEq, Value: &IntValue{N: 1}},
			R: &Comparison{Field: "b", Op: OpEq, Value: &IntValue{N: 2}},
		}, `(a == 1 || b == 2)`},
		{"nested", &Or{
			L: &And{
				L: &Comparison{Field: "a", Op: OpEq, Value: &IntValue{N: 1}},
				R: &Comparison{Field: "b", Op: OpEq, Value: &IntValue{N: 2}},
			},
			R: &Comparison{Field: "c", Op: OpEq, Value: &IntValue{N: 3}},
		}, `((a == 1 && b == 2) || c == 3)`},
	}
	for _, c := range cases {
		if got := c.node.String(); got != c.want {
			t.Errorf("%s: String() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestQuoteGQL — the canonical quoter is the exact inverse of the M5
// lexer's string decoding.
func TestQuoteGQL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"login", `"login"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
		{"nl\n", `"nl\n"`},
		{"tab\t", `"tab\t"`},
		{"cr\r", `"cr\r"`},
		{"\b\f", `"\b\f"`},
		{"café", `"café"`},
		{string(rune(0x01)), `"\u0001"`}, // control chars render as \uXXXX
		{"", `""`},
	}
	for _, c := range cases {
		if got := quoteGQL(c.in); got != c.want {
			t.Errorf("quoteGQL(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// TestStringRoundtripsThroughLexer — the canonical String() of a tree
// re-lexes to a token stream with the same literal values: the canonical
// form never invents syntax the lexer cannot read (the property --explain
// output relies on).
func TestStringRoundtripsThroughLexer(t *testing.T) {
	tree := sampleTree()
	rendered := tree.String()

	toks, err := lexer.Lex(rendered)
	if err != nil {
		t.Fatalf("canonical form does not re-lex: %v\nrendered: %s", err, rendered)
	}
	// Spot-check the load-bearing tokens survive the roundtrip.
	var texts []string
	var kinds []lexer.Kind
	for _, tok := range toks {
		if tok.Kind == lexer.EOF {
			break
		}
		texts = append(texts, tok.Text)
		kinds = append(kinds, tok.Kind)
	}
	for _, want := range []string{`"Admin \"Login\""`, "80", "443", "ANY", "exists"} {
		found := false
		for _, txt := range texts {
			if txt == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("re-lexed stream missing token %q:\n%s", want, strings.Join(texts, " "))
		}
	}
	// The quantifier block braces must lex as punctuation now.
	for _, want := range []lexer.Kind{lexer.LBRACE, lexer.RBRACE} {
		found := false
		for _, k := range kinds {
			if k == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("re-lexed stream missing %v", want)
		}
	}
}

// TestCountAndDepth — the K-13 measurement helpers.
func TestCountAndDepth(t *testing.T) {
	leaf := &Comparison{Field: "a", Op: OpEq, Value: &IntValue{N: 1}}
	if Count(leaf) != 1 || Depth(leaf) != 1 {
		t.Errorf("leaf Count/Depth = %d/%d, want 1/1", Count(leaf), Depth(leaf))
	}
	if Count(nil) != 0 || Depth(nil) != 0 {
		t.Errorf("nil Count/Depth = %d/%d, want 0/0", Count(nil), Depth(nil))
	}

	tree := sampleTree()
	// Nodes: Or, And, Not, 2×Comparison, ListValue(not a Node), Quantified, Exists
	// = Or(1) + And(1) + Not(1) + neq(1) + in(1) + q(1) + exists(1) = 7.
	if got := Count(tree); got != 7 {
		t.Errorf("Count(sampleTree) = %d, want 7", got)
	}
	// Depth: Or > And > Not > Comparison = 4.
	if got := Depth(tree); got != 4 {
		t.Errorf("Depth(sampleTree) = %d, want 4", got)
	}
}

// collectingVisitor records every visited node type in order.
type collectingVisitor struct{ kinds []string }

func (v *collectingVisitor) Visit(n Node) Visitor {
	switch n.(type) {
	case *Or:
		v.kinds = append(v.kinds, "Or")
	case *And:
		v.kinds = append(v.kinds, "And")
	case *Not:
		v.kinds = append(v.kinds, "Not")
	case *Comparison:
		v.kinds = append(v.kinds, "Comparison")
	case *Exists:
		v.kinds = append(v.kinds, "Exists")
	case *Quantified:
		v.kinds = append(v.kinds, "Quantified")
	}
	return v
}

// TestWalkOrder — depth-first, left-to-right, every node visited once.
func TestWalkOrder(t *testing.T) {
	v := &collectingVisitor{}
	Walk(v, sampleTree())
	want := []string{"Or", "And", "Not", "Comparison", "Comparison", "Quantified", "Exists"}
	if strings.Join(v.kinds, ",") != strings.Join(want, ",") {
		t.Errorf("Walk order = %v, want %v", v.kinds, want)
	}

	// nil node and nil visitor are no-ops (must not panic).
	Walk(v, nil)
	Walk(nil, sampleTree())
}

// skippingVisitor cuts the walk below And nodes.
type skippingVisitor struct{ ors, others int }

func (v *skippingVisitor) Visit(n Node) Visitor {
	switch t := n.(type) {
	case *Or:
		v.ors++
		return v
	case *And:
		_ = t
		return nil // skip the whole And subtree
	default:
		v.others++
		return v
	}
}

// TestWalkSkipSubtree — returning nil from Visit skips the subtree.
func TestWalkSkipSubtree(t *testing.T) {
	v := &skippingVisitor{}
	Walk(v, sampleTree())
	if v.ors != 1 {
		t.Errorf("visited %d Or nodes, want 1", v.ors)
	}
	// The And subtree (And, Not, 2 Comparisons) is skipped; only the
	// Quantified arm continues (Quantified + Exists).
	if v.others != 2 {
		t.Errorf("visited %d non-Or nodes, want 2 (Quantified + Exists)", v.others)
	}
}

// TestPos — positions surface from every node kind.
func TestPos(t *testing.T) {
	tree := sampleTree()
	line, col := tree.Pos()
	if line != 1 || col != 36 {
		t.Errorf("Or pos = %d:%d, want 1:36", line, col)
	}
	line, col = tree.(*Or).L.(*And).L.(*Not).Pos()
	if line != 1 || col != 2 {
		t.Errorf("Not pos = %d:%d, want 1:2", line, col)
	}
}

// TestOpString — canonical operator spellings (used by String()).
func TestOpString(t *testing.T) {
	want := map[Op]string{
		OpEq: "==", OpNeq: "!=", OpLt: "<", OpLe: "<=", OpGt: ">", OpGe: ">=",
		OpContains: "CONTAINS", OpInCidr: "IN_CIDR", OpInDomain: "IN_DOMAIN", OpIn: "IN",
	}
	for op, s := range want {
		if got := op.String(); got != s {
			t.Errorf("Op(%d).String() = %q, want %q", int(op), got, s)
		}
	}
}
