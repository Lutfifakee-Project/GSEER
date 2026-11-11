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
	"fmt"
	"strings"

	"github.com/Lutfifakee-Project/gseer/internal/gql/ast"
	"github.com/Lutfifakee-Project/gseer/internal/gql/lexer"
)

// parser.go — the recursive-descent GQL parser (SPEC-04 §3 EBNF).
//
// Grammar (precedence `!` > `&&` > `||`; sugar normalized here):
//
//	query    := or EOF
//	or       := and ( ( "||" | KwOr ) and )*
//	and      := not ( ( "&&" | KwAnd ) not )*
//	primary  := "(" or ")" | quantified | comparison | exists
//	not      := ( "!" | KwNot ) not | primary
//	quantified := ( KwAny | KwAll ) "(" IDENT ")" "{" or "}"
//	predicate  := op value | KwIn "(" value ("," value)* ")"
//	comparison := IDENT predicate
//	exists     := IDENT            (no predicate follows)
//	op         := "==" | "!=" | "=" | "<" | "<=" | ">" | ">=" |
//	              KwContains | KwInCIDR | KwInDomain
//	value      := STRING | INT
//
// Sugar: `=` parses as `==` (ASSIGN → OpEq); the keyword boolean forms
// parse to the same nodes as their symbolic forms.  `field != "value"`
// is its own comparison operator (OpNeq), NOT a negated equality.
//
// Parse-time limits (K-13): node count and tree depth are enforced HERE
// while building, plus the IN-list length cap.  The normative K-13
// values live in SPEC-04 §7 (not vendored in-repo); the constants below
// enforce the requirement with generous headroom for real queries and
// are the tuning point if the spec numbers differ.
const (
	// maxNodes bounds the tree size (Count) — K-13 parse-time bound.
	maxNodes = 1024
	// maxDepth bounds the tree height (Depth) — K-13 parse-time bound.
	maxDepth = 32
	// maxInList bounds one IN list's element count (SPEC-04 §3).
	maxInList = 256
)

// Parse tokenizes src and parses it to an ast.Node tree.  Lexical
// errors surface as *lexer.Error (GQL_E_LEX); grammatical errors as
// *Error (GQL_E_PARSE) — both carry the 1-based position.
func Parse(src string) (ast.Node, error) {
	toks, err := lexer.Lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	if p.peek().Kind == lexer.EOF {
		return nil, p.errorf(p.peek(), "empty query",
			"a GQL query must compare at least one field",
			"try: port == 443   or   http.title CONTAINS \"login\"")
	}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	// The whole token stream must be consumed: a query is ONE expression.
	if tok := p.peek(); tok.Kind != lexer.EOF {
		return nil, p.unexpected(tok, "end of query")
	}
	return n, nil
}

// parser holds the token stream and the cursor.
type parser struct {
	toks []lexer.Token
	pos  int
}

// peek returns the current token (EOF-safe).
func (p *parser) peek() lexer.Token {
	if p.pos >= len(p.toks) {
		return p.toks[len(p.toks)-1] // the EOF token
	}
	return p.toks[p.pos]
}

// peek2 returns the token after peek (EOF-safe).
func (p *parser) peek2() lexer.Token {
	if p.pos+1 >= len(p.toks) {
		return p.toks[len(p.toks)-1]
	}
	return p.toks[p.pos+1]
}

// advance consumes and returns the current token.
func (p *parser) advance() lexer.Token {
	tok := p.peek()
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return tok
}

// errorf builds a GQL_E_PARSE error at the token's position.
func (p *parser) errorf(tok lexer.Token, detail, why, next string) *Error {
	return &Error{Detail: detail, Why: why, Next: next, Line: tok.Line, Col: tok.Col}
}

// unexpected renders the standard "unexpected X" error with a
// did-you-mean hint when tok is a plausible keyword misspelling.
func (p *parser) unexpected(tok lexer.Token, expected string) *Error {
	what := describe(tok)
	detail := fmt.Sprintf("unexpected %s at line %d, column %d", what, tok.Line, tok.Col)
	next := "expected " + expected
	if hint := didYouMean(tok); hint != "" {
		next += "; " + hint
	}
	return &Error{
		Detail: detail,
		Why:    "the GQL grammar (SPEC-04 §3) does not allow " + what + " here",
		Next:   next,
		Line:   tok.Line,
		Col:    tok.Col,
	}
}

// describe renders a token for an error message.
func describe(tok lexer.Token) string {
	switch tok.Kind {
	case lexer.EOF:
		return "end of query"
	case lexer.STRING:
		return "string literal " + tok.Text
	case lexer.INT:
		return "integer literal " + tok.Text
	case lexer.IDENT:
		return "field path " + tok.Text
	default:
		return fmt.Sprintf("%q", tok.Text)
	}
}

// keywordsForHints is the did-you-mean candidate set (lowercase).
var keywordsForHints = []string{
	"and", "or", "not", "in", "any", "all", "contains", "in_cidr", "in_domain", "exists",
}

// didYouMean returns a "did you mean 'x'?" clause when tok's text is
// within edit distance 1 of a keyword (case-insensitive).  Empty when
// nothing is close — a wrong guess is worse than none.
func didYouMean(tok lexer.Token) string {
	if tok.Kind != lexer.IDENT && tok.Kind != lexer.INT && tok.Kind != lexer.STRING {
		return ""
	}
	word := strings.ToLower(strings.Trim(tok.Text, `"`))
	const maxDist = 1
	best := ""
	for _, kw := range keywordsForHints {
		if word == kw {
			return "" // exact keyword would not have reached the error path
		}
		if editDistanceAtMost(word, kw, maxDist) {
			best = kw
			break
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("did you mean the keyword %q?", best)
}

// editDistanceAtMost reports whether a→b needs ≤ max edits (insert,
// delete, substitute) — early-exit Levenshtein bounded by max.
func editDistanceAtMost(a, b string, maxEdits int) bool {
	la, lb := len(a), len(b)
	if abs(la-lb) > maxEdits {
		return false
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if cur[j] < rowMin {
				rowMin = cur[j]
			}
		}
		if rowMin > maxEdits {
			return false // whole row already too far
		}
		prev, cur = cur, prev
	}
	return prev[lb] <= maxEdits
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// ---------------------------------------------------------------------------
// Precedence climbing: or → and → not → primary
// ---------------------------------------------------------------------------

// parseOr parses the `||` level (lowest precedence).
func (p *parser) parseOr() (ast.Node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		if tok.Kind != lexer.OR && tok.Kind != lexer.KwOr {
			return left, nil
		}
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &ast.Or{L: left, R: right, Line: tok.Line, Col: tok.Col}
		if err := p.checkLimits(left, tok); err != nil {
			return nil, err
		}
	}
}

// parseAnd parses the `&&` level.
func (p *parser) parseAnd() (ast.Node, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		if tok.Kind != lexer.AND && tok.Kind != lexer.KwAnd {
			return left, nil
		}
		p.advance()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &ast.And{L: left, R: right, Line: tok.Line, Col: tok.Col}
		if err := p.checkLimits(left, tok); err != nil {
			return nil, err
		}
	}
}

// parseNot parses prefix negation (binds tighter than && and ||).
func (p *parser) parseNot() (ast.Node, error) {
	tok := p.peek()
	if tok.Kind == lexer.NOT || tok.Kind == lexer.KwNot {
		p.advance()
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		n := &ast.Not{X: inner, Line: tok.Line, Col: tok.Col}
		if err := p.checkLimits(n, tok); err != nil {
			return nil, err
		}
		return n, nil
	}
	return p.parsePrimary()
}

// parsePrimary parses parenthesized groups, quantified blocks, and the
// leaf forms (comparison / exists).
func (p *parser) parsePrimary() (ast.Node, error) {
	tok := p.peek()
	switch tok.Kind {
	case lexer.LPAREN:
		p.advance()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if close := p.peek(); close.Kind != lexer.RPAREN {
			return nil, p.unexpected(close, "\")\" to close the group")
		}
		p.advance()
		return inner, nil

	case lexer.KwAny, lexer.KwAll:
		return p.parseQuantified()

	case lexer.STRING:
		// Free-text form: one or more quoted literals with no field
		// path — the bare-keyword search (`"wordpress"`, `"apache"
		// "login"`).  The validator expands the node over the free-text
		// field list.  A leading INT stays an error: a number without a
		// field has no honest target (port=443 is the field form).
		return p.parseFreeText()

	case lexer.IDENT:
		// `exists(field)` — the functional presence test (the canonical
		// rendering of ast.Exists).  Case-insensitive like every keyword;
		// a bare `exists` with no parentheses stays a plain field.
		if strings.EqualFold(tok.Text, "exists") && p.peek2().Kind == lexer.LPAREN {
			return p.parseExistsFunc()
		}
		return p.parseFieldTail()

	default:
		return nil, p.unexpected(tok, "a field path, \"(\", \"!\", or a quantifier (ANY/ALL)")
	}
}

// parseFreeText parses the free-text form: one or more consecutive
// string literals (already dispatched by parsePrimary).  Consecutive
// literals are an implicit AND across terms ("apache" "login" = both
// must match somewhere); the validator does the expansion.
func (p *parser) parseFreeText() (ast.Node, error) {
	first := p.advance() // the leading STRING, guaranteed by dispatch
	terms := []string{first.Value}
	for p.peek().Kind == lexer.STRING {
		terms = append(terms, p.advance().Value)
	}
	for _, term := range terms {
		if term == "" {
			return nil, p.errorf(first,
				"empty free-text term at line %d, column %d",
				"a free-text search needs a non-empty keyword",
				"try: \"nginx\"  or  port == 443")
		}
	}
	n := &ast.FreeText{Terms: terms, Line: first.Line, Col: first.Col}
	return p.checked(n, first)
}

// parseQuantified parses `ANY(field){ body }` (ALL parses too; the M7
// validator rejects it in v0.0.1 — the parser is grammar-complete).
func (p *parser) parseQuantified() (ast.Node, error) {
	qtok := p.advance() // ANY | ALL
	quant := ast.QuantAny
	if qtok.Kind == lexer.KwAll {
		quant = ast.QuantAll
	}
	if open := p.peek(); open.Kind != lexer.LPAREN {
		return nil, p.unexpected(open, "\"(\" after "+qtok.Text)
	}
	p.advance()
	field := p.peek()
	if field.Kind != lexer.IDENT {
		return nil, p.unexpected(field, "a field path inside "+qtok.Text+"(...)")
	}
	if strings.ContainsRune(field.Text, '.') {
		return nil, p.errorf(field,
			fmt.Sprintf("quantifier scope %q must be a simple name at line %d, column %d", field.Text, field.Line, field.Col),
			"the quantifier scopes a relation (one segment), not a dotted field path",
			"try: "+qtok.Text+"(service){ ... }")
	}
	p.advance()
	if close := p.peek(); close.Kind != lexer.RPAREN {
		return nil, p.unexpected(close, "\")\" after the "+qtok.Text+" scope")
	}
	p.advance()
	if open := p.peek(); open.Kind != lexer.LBRACE {
		return nil, p.unexpected(open, "\"{\" to open the "+qtok.Text+" block")
	}
	p.advance()
	body, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if close := p.peek(); close.Kind != lexer.RBRACE {
		return nil, p.unexpected(close, "\"}\" to close the "+qtok.Text+" block")
	}
	p.advance()
	n := &ast.Quantified{Q: quant, Field: field.Text, Body: body, Line: qtok.Line, Col: qtok.Col}
	if err := p.checkLimits(n, qtok); err != nil {
		return nil, err
	}
	return n, nil
}

// parseExistsFunc parses `exists(field)` — the functional form of the
// presence test (ast.Exists's canonical rendering must re-parse).
func (p *parser) parseExistsFunc() (ast.Node, error) {
	kw := p.advance() // the exists token
	p.advance()       // ( — guaranteed LPAREN by the caller's check
	field := p.peek()
	if field.Kind != lexer.IDENT {
		return nil, p.unexpected(field, "a field path inside exists(...)")
	}
	p.advance()
	if close := p.peek(); close.Kind != lexer.RPAREN {
		return nil, p.unexpected(close, "\")\" after the exists field")
	}
	p.advance()
	return &ast.Exists{Field: field.Text, Line: kw.Line, Col: kw.Col}, nil
}

// parseFieldTail parses the leaf forms starting at a field token: a
// comparison (operator + value) or a bare-field exists test.
func (p *parser) parseFieldTail() (ast.Node, error) {
	field := p.advance()
	op := p.peek()
	switch op.Kind {
	case lexer.EQ, lexer.ASSIGN:
		p.advance()
		return p.comparison(field, ast.OpEq)
	case lexer.NEQ:
		p.advance()
		return p.comparison(field, ast.OpNeq) // `!=` is its own operator (SPEC-04 §3)
	case lexer.LT:
		p.advance()
		return p.comparison(field, ast.OpLt)
	case lexer.LE:
		p.advance()
		return p.comparison(field, ast.OpLe)
	case lexer.GT:
		p.advance()
		return p.comparison(field, ast.OpGt)
	case lexer.GE:
		p.advance()
		return p.comparison(field, ast.OpGe)
	case lexer.KwContains:
		p.advance()
		return p.comparison(field, ast.OpContains)
	case lexer.KwInCIDR:
		p.advance()
		return p.comparison(field, ast.OpInCidr)
	case lexer.KwInDomain:
		p.advance()
		return p.comparison(field, ast.OpInDomain)
	case lexer.MATCH:
		p.advance()
		return p.comparison(field, ast.OpMatch)
	case lexer.KwIn:
		p.advance()
		return p.inList(field)
	case lexer.AND, lexer.KwAnd, lexer.OR, lexer.KwOr, lexer.RPAREN, lexer.RBRACE, lexer.EOF:
		// No predicate: a bare-field presence test.
		return &ast.Exists{Field: field.Text, Line: field.Line, Col: field.Col}, nil
	default:
		// An unknown token after a field.  When the FIELD itself looks
		// like a keyword misspelling, point the error at the field and
		// offer the suggestion — that is where the operator looks.
		if hint := didYouMean(field); hint != "" {
			return nil, &Error{
				Detail: fmt.Sprintf("unexpected %s at line %d, column %d", describe(field), field.Line, field.Col),
				Why:    "the GQL grammar (SPEC-04 §3) does not allow a field here",
				Next:   "expected an operator after the field (==, !=, <, <=, >, >=, CONTAINS, IN, IN_CIDR, IN_DOMAIN); " + hint,
				Line:   field.Line,
				Col:    field.Col,
			}
		}
		return nil, p.unexpected(op, "an operator after field "+field.Text+
			" (==, !=, <, <=, >, >=, CONTAINS, IN, IN_CIDR, IN_DOMAIN)")
	}
}

// comparison parses the value after an operator and builds the node.
func (p *parser) comparison(field lexer.Token, op ast.Op) (ast.Node, error) {
	vtok := p.peek()
	switch vtok.Kind {
	case lexer.STRING:
		p.advance()
		return p.checked(&ast.Comparison{
			Field: field.Text, Op: op,
			Value: &ast.StrValue{S: vtok.Value},
			Line:  field.Line, Col: field.Col,
		}, field)
	case lexer.INT:
		p.advance()
		var n int64
		if _, err := fmt.Sscanf(vtok.Text, "%d", &n); err != nil {
			return nil, p.errorf(vtok, fmt.Sprintf("integer literal %s is out of range at line %d, column %d", vtok.Text, vtok.Line, vtok.Col),
				"GQL integer literals are 64-bit", "reduce the literal's magnitude")
		}
		return p.checked(&ast.Comparison{
			Field: field.Text, Op: op,
			Value: &ast.IntValue{N: n},
			Line:  field.Line, Col: field.Col,
		}, field)
	case lexer.EOF:
		return nil, p.errorf(vtok, "missing value after "+op.String()+" at line "+itoa(field.Line)+", column "+itoa(field.Col),
			"the comparison needs a string or integer literal on the right-hand side",
			"try: "+field.Text+" "+op.String()+" 42")
	default:
		return nil, p.unexpected(vtok, "a string or integer literal after "+field.Text+" "+op.String())
	}
}

// inList parses `IN (v1, v2, …)` with the ≤ maxInList cap (SPEC-04 §3).
func (p *parser) inList(field lexer.Token) (ast.Node, error) {
	if open := p.peek(); open.Kind != lexer.LPAREN {
		return nil, p.unexpected(open, "\"(\" after IN")
	}
	p.advance()
	var items []ast.Value
	for {
		vtok := p.peek()
		switch vtok.Kind {
		case lexer.STRING:
			p.advance()
			items = append(items, &ast.StrValue{S: vtok.Value})
		case lexer.INT:
			p.advance()
			var n int64
			if _, err := fmt.Sscanf(vtok.Text, "%d", &n); err != nil {
				return nil, p.errorf(vtok, fmt.Sprintf("integer literal %s is out of range at line %d, column %d", vtok.Text, vtok.Line, vtok.Col),
					"GQL integer literals are 64-bit", "reduce the literal's magnitude")
			}
			items = append(items, &ast.IntValue{N: n})
		default:
			return nil, p.unexpected(vtok, "a string or integer literal inside the IN list")
		}
		if len(items) > maxInList {
			return nil, p.errorf(vtok,
				fmt.Sprintf("IN list exceeds %d elements at line %d, column %d", maxInList, vtok.Line, vtok.Col),
				"SPEC-04 §3 bounds IN lists to keep planning bounded",
				"split the query into multiple searches or narrow the list")
		}
		switch sep := p.peek(); sep.Kind {
		case lexer.COMMA:
			p.advance()
		case lexer.RPAREN:
			p.advance()
			return p.checked(&ast.Comparison{
				Field: field.Text, Op: ast.OpIn,
				Value: &ast.ListValue{Items: items},
				Line:  field.Line, Col: field.Col,
			}, field)
		default:
			return nil, p.unexpected(sep, "\",\" or \")\" in the IN list")
		}
	}
}

// checked applies the K-13 limits to a freshly built leaf node (leaves
// cannot grow the tree, but the count budget is checked uniformly).
func (p *parser) checked(n ast.Node, at lexer.Token) (ast.Node, error) {
	if err := p.checkLimits(n, at); err != nil {
		return nil, err
	}
	return n, nil
}

// checkLimits enforces the K-13 node-count and depth bounds at parse
// time (the whole point of building with accounting available).
func (p *parser) checkLimits(n ast.Node, at lexer.Token) error {
	if c := ast.Count(n); c > maxNodes {
		return p.errorf(at,
			fmt.Sprintf("query exceeds the %d-node limit at line %d, column %d (K-13)", maxNodes, at.Line, at.Col),
			"the parse-time node budget (K-13) keeps queries plannable",
			"simplify the query — fewer terms or shallower nesting")
	}
	if d := ast.Depth(n); d > maxDepth {
		return p.errorf(at,
			fmt.Sprintf("query exceeds the %d-level nesting limit at line %d, column %d (K-13)", maxDepth, at.Line, at.Col),
			"the parse-time depth budget (K-13) keeps queries plannable",
			"flatten the nested groups")
	}
	return nil
}

// itoa is strconv.Itoa without the extra import in this file's hot path.
func itoa(n int) string { return fmt.Sprintf("%d", n) }
