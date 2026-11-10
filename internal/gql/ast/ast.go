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

// Package ast holds the typed GQL tree + visitor + explain rendering
// (SPEC-04 §4).
//
// M8 (landed):
//   - the node types Or/And/Not/Comparison/Exists/Quantified{ANY,ALL}
//     with 1-based source positions; Comparison/Exists/Quantified carry
//     the field path as a mutable string so the M7 validator can rewrite
//     aliases to canonical paths in place;
//   - value nodes (string, integer, IN-list) — ASSIGN never appears:
//     the parser normalizes `=` sugar to EQ at construction time;
//   - the visitor (Walk/Visitor, go/ast semantics: returning nil from
//     Visit skips the subtree);
//   - node-count/depth accounting (Count/Depth) — the measurement the
//     parser (M6) uses to enforce the K-13 limits at parse time; the
//     limit VALUES live with the parser, not here;
//   - a stable canonical String() per node, fully parenthesized for the
//     boolean spine, used by --explain: printing an AST and re-lexing
//     the output must yield the identical tree (pinned by test).
//
// The AST never imports any collector, egress, or P2 package
// (ADR-0010; AC-3.1).
package ast

import (
	"fmt"
	"strings"
)

// Node is one typed GQL tree node.  Every node knows its 1-based source
// position and renders its canonical form via String.
type Node interface {
	// Pos returns the 1-based line:col of the node's first token.
	Pos() (line, col int)
	// String renders the canonical form (see package doc).
	String() string
}

// Op enumerates the comparison operators that can appear in a
// Comparison node.  The parser maps keyword and sugar forms onto these
// (ASSIGN `=` → OpEq; `and`/`or`/`not` are boolean structure, not Ops).
type Op int

// Comparison operators (SPEC-04 §3).  String() renders the canonical
// spelling of each (uppercase operator keywords, symbolic comparators).
const (
	OpEq  Op = iota // ==
	OpNeq           // !=
	OpLt            // <
	OpLe            // <=
	OpGt            // >
	OpGe            // >=

	OpContains // CONTAINS
	OpInCidr   // IN_CIDR
	OpInDomain // IN_DOMAIN
	OpIn       // IN (list membership)
	OpMatch    // *= (wildcard fuzzy match: * = any run, ? = one char)
)

var opNames = map[Op]string{
	OpEq: "==", OpNeq: "!=", OpLt: "<", OpLe: "<=", OpGt: ">", OpGe: ">=",
	OpContains: "CONTAINS", OpInCidr: "IN_CIDR", OpInDomain: "IN_DOMAIN",
	OpIn: "IN", OpMatch: "*=",
}

// String renders the canonical operator spelling.
func (o Op) String() string {
	if n, ok := opNames[o]; ok {
		return n
	}
	return fmt.Sprintf("Op(%d)", int(o))
}

// OpSet is a bitmask of operators (the M7 field registry's compatibility
// contract).
type OpSet uint32

// The single-op set constructors used by the registry.
const (
	OpSetEq       OpSet = 1 << OpEq
	OpSetNeq      OpSet = 1 << OpNeq
	OpSetLt       OpSet = 1 << OpLt
	OpSetLe       OpSet = 1 << OpLe
	OpSetGt       OpSet = 1 << OpGt
	OpSetGe       OpSet = 1 << OpGe
	OpSetContains OpSet = 1 << OpContains
	OpSetInCidr   OpSet = 1 << OpInCidr
	OpSetInDomain OpSet = 1 << OpInDomain
	OpSetIn       OpSet = 1 << OpIn
	OpSetMatch    OpSet = 1 << OpMatch
)

// Has reports whether the set contains op.
func (s OpSet) Has(op Op) bool { return s&(1<<op) != 0 }

// String renders the set's operators in canonical order, comma-
// separated — used in "field accepts: …" error messages.
func (s OpSet) String() string {
	var parts []string
	for op := OpEq; op <= OpMatch; op++ {
		if s.Has(op) {
			parts = append(parts, op.String())
		}
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

// Quant enumerates the quantified-block kinds.  ALL parses but the M7
// validator rejects it in v0.0.1 (GQL_E_NOT_SUPPORTED_IN_VERSION).
type Quant int

// Quantifier kinds.
const (
	QuantAny Quant = iota // ANY — v0.0.1 accepted
	QuantAll              // ALL — validator rejects in v0.0.1
)

// String renders the canonical quantifier keyword.
func (q Quant) String() string {
	if q == QuantAll {
		return "ALL"
	}
	return "ANY"
}

// ---------------------------------------------------------------------------
// Values
// ---------------------------------------------------------------------------

// Value is one literal value in a Comparison (or one element of an
// IN list).  The M7 validator owns typing: the AST carries the literal
// as lexed.
type Value interface {
	String() string // canonical rendering (strings requoted, ints plain)
	isValue()
}

// StrValue is a double-quoted string literal.
type StrValue struct{ S string }

// IntValue is an integer literal.
type IntValue struct{ N int64 }

// ListValue is the parenthesized list of an IN comparison (the parser
// bounds its length; the AST does not).
type ListValue struct{ Items []Value }

func (*StrValue) isValue()  {}
func (*IntValue) isValue()  {}
func (*ListValue) isValue() {}

// String renders `"…"`, re-escaped per the canonical quoting rules
// (quoteGQL).
func (v *StrValue) String() string { return quoteGQL(v.S) }

// String renders the integer in base 10.
func (v *IntValue) String() string { return fmt.Sprintf("%d", v.N) }

// String renders `(a, b, …)` with canonical element forms.
func (v *ListValue) String() string {
	parts := make([]string, len(v.Items))
	for i, it := range v.Items {
		parts[i] = it.String()
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// quoteGQL renders s as a canonical GQL string literal: wrapped in
// double quotes with `"` and `\` backslash-escaped and control
// characters rendered via the short escapes or \uXXXX — the exact
// inverse of the M5 lexer's string decoding.
func quoteGQL(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ---------------------------------------------------------------------------
// Boolean spine
// ---------------------------------------------------------------------------

// Or is the `||` / OR disjunction (lowest precedence).
type Or struct {
	L, R Node
	Line int
	Col  int
}

// And is the `&&` / AND conjunction.
type And struct {
	L, R Node
	Line int
	Col  int
}

// Not is the `!` / NOT negation (binds tightest of the booleans).
type Not struct {
	X    Node
	Line int
	Col  int
}

// Pos returns the operator token's position.
func (n *Or) Pos() (int, int)  { return n.Line, n.Col }
func (n *And) Pos() (int, int) { return n.Line, n.Col }
func (n *Not) Pos() (int, int) { return n.Line, n.Col }

// String renders fully parenthesized canonical forms — `((a && b) || c)`
// — so the rendering is unambiguous and re-parses to the same tree.
func (n *Or) String() string  { return "(" + n.L.String() + " || " + n.R.String() + ")" }
func (n *And) String() string { return "(" + n.L.String() + " && " + n.R.String() + ")" }
func (n *Not) String() string { return "!(" + n.X.String() + ")" }

// ---------------------------------------------------------------------------
// Leaves
// ---------------------------------------------------------------------------

// Comparison is one field/operator/value triple.  Field is the user-
// written path until the M7 validator rewrites aliases to canonical
// paths in place (the doc contract: "carrying canonical field paths
// after alias rewrite").
type Comparison struct {
	Field string
	Op    Op
	Value Value // *StrValue, *IntValue or *ListValue
	Line  int
	Col   int
}

// Exists is a bare-field presence test (`exists(http.title)` in
// canonical form).
type Exists struct {
	Field string
	Line  int
	Col   int
}

// FreeText is the bare-keyword search form: one or more quoted string
// literals with no field path (`"wordpress"`, `"apache" "login"`).
// The parser accepts it anywhere a comparison is legal; the validator
// expands it into CONTAINS comparisons over the fixed free-text field
// list (validator.FreeTextFields) — one OR-chain per term, AND across
// terms.  It is a leaf: it holds no child node.
type FreeText struct {
	Terms []string
	Line  int
	Col   int
}

// Pos returns the node's first token position.
func (n *FreeText) Pos() (int, int) { return n.Line, n.Col }

// String renders the canonical multi-literal form (each term re-quoted
// and space-separated, which re-parses to the same node).
func (n *FreeText) String() string {
	parts := make([]string, 0, len(n.Terms))
	for _, t := range n.Terms {
		parts = append(parts, (&StrValue{S: t}).String())
	}
	return strings.Join(parts, " ")
}

// Quantified is a quantified block: EVERY/ANY over the Body for the
// scope Field names.  QuantAll parses (M6) but is validator-rejected in
// v0.0.1.
type Quantified struct {
	Q     Quant
	Field string
	Body  Node
	Line  int
	Col   int
}

// Pos returns the node's first token position.
func (n *Comparison) Pos() (int, int) { return n.Line, n.Col }
func (n *Exists) Pos() (int, int)     { return n.Line, n.Col }
func (n *Quantified) Pos() (int, int) { return n.Line, n.Col }

// String renders `field op value` with canonical spellings.
func (n *Comparison) String() string {
	return n.Field + " " + n.Op.String() + " " + n.Value.String()
}

// String renders `exists(field)`.
func (n *Exists) String() string { return "exists(" + n.Field + ")" }

// String renders `ANY(field){ body }` / `ALL(field){ body }`.
func (n *Quantified) String() string {
	return n.Q.String() + "(" + n.Field + "){ " + n.Body.String() + " }"
}

// ---------------------------------------------------------------------------
// Visitor
// ---------------------------------------------------------------------------

// Visitor inspects nodes during Walk.  Returning nil from Visit skips
// that node's children (go/ast semantics); returning a Visitor continues
// into the children with the returned visitor.
type Visitor interface {
	Visit(node Node) (w Visitor)
}

// Walk traverses n depth-first, left-to-right, calling v.Visit before
// descending.  A nil node is a no-op.
func Walk(v Visitor, n Node) {
	if v == nil || n == nil {
		return
	}
	switch t := n.(type) {
	case *Or:
		if w := v.Visit(n); w != nil {
			Walk(w, t.L)
			Walk(w, t.R)
		}
	case *And:
		if w := v.Visit(n); w != nil {
			Walk(w, t.L)
			Walk(w, t.R)
		}
	case *Not:
		if w := v.Visit(n); w != nil {
			Walk(w, t.X)
		}
	case *Comparison:
		v.Visit(n)
	case *Exists:
		v.Visit(n)
	case *FreeText:
		v.Visit(n)
	case *Quantified:
		if w := v.Visit(n); w != nil {
			Walk(w, t.Body)
		}
	default:
		// Unknown node implementations cannot occur inside this
		// package's trees; a panic would hide that from no one.  Keep
		// Walk total instead: visiting a foreign node is a no-op.
		v.Visit(n)
	}
}

// ---------------------------------------------------------------------------
// K-13 accounting (the parser enforces the limit VALUES)
// ---------------------------------------------------------------------------

// Count returns the number of nodes in the subtree rooted at n
// (including n).  The M6 parser uses this to enforce the K-13
// node-count limit at parse time.
func Count(n Node) int {
	if n == nil {
		return 0
	}
	switch t := n.(type) {
	case *Or:
		return 1 + Count(t.L) + Count(t.R)
	case *And:
		return 1 + Count(t.L) + Count(t.R)
	case *Not:
		return 1 + Count(t.X)
	case *Comparison:
		return 1
	case *Exists:
		return 1
	case *FreeText:
		return 1
	case *Quantified:
		return 1 + Count(t.Body)
	default:
		return 1
	}
}

// Depth returns the height of the subtree rooted at n (a leaf is 1).
// The M6 parser uses this to enforce the K-13 depth limit at parse time.
func Depth(n Node) int {
	if n == nil {
		return 0
	}
	switch t := n.(type) {
	case *Or:
		return 1 + max(Depth(t.L), Depth(t.R))
	case *And:
		return 1 + max(Depth(t.L), Depth(t.R))
	case *Not:
		return 1 + Depth(t.X)
	case *Quantified:
		return 1 + Depth(t.Body)
	default:
		return 1
	}
}
