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

package lexer

import (
	"errors"
	"strings"
	"testing"
)

// lexer_test.go — the M5 lexer acceptance tests (SPEC-04 §2–3): token
// shapes, 1-based positions, maximal munch, keyword case-insensitivity,
// escape decoding, and every GQL_E_LEX branch with its exact position.

// lexOK lexes and fails the test on any lexical error.
func lexOK(t *testing.T, src string) []Token {
	t.Helper()
	toks, err := Lex(src)
	if err != nil {
		t.Fatalf("Lex(%q) unexpected error: %v", src, err)
	}
	if n := len(toks); n == 0 || toks[n-1].Kind != EOF {
		t.Fatalf("Lex(%q) stream must end with EOF, got %v", src, toks)
	}
	return toks
}

// lexErr lexes and returns the *Error (failing the test when no error
// or a wrong error type comes back).
func lexErr(t *testing.T, src string) *Error {
	t.Helper()
	toks, err := Lex(src)
	if err == nil {
		t.Fatalf("Lex(%q) = %v, want GQL_E_LEX error", src, toks)
	}
	var le *Error
	if !errors.As(err, &le) {
		t.Fatalf("Lex(%q) error is %T (%v), want *lexer.Error", src, err, err)
	}
	if le.Code() != CodeLex {
		t.Errorf("error code = %q, want %q", le.Code(), CodeLex)
	}
	return le
}

// kinds extracts the token kinds of a stream (EOF included).
func kinds(toks []Token) []Kind {
	out := make([]Kind, 0, len(toks))
	for _, tok := range toks {
		out = append(out, tok.Kind)
	}
	return out
}

func kindEqual(a, b []Kind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestLexEmpty — empty and whitespace-only inputs produce exactly one
// EOF token.
func TestLexEmpty(t *testing.T) {
	for _, src := range []string{"", " ", "\t", "\n", "  \n\t \n"} {
		toks := lexOK(t, src)
		if len(toks) != 1 || toks[0].Kind != EOF {
			t.Errorf("Lex(%q) = %v, want [EOF]", src, toks)
		}
	}
}

// TestLexEOFPosition — the EOF token sits one past the last rune.
func TestLexEOFPosition(t *testing.T) {
	toks := lexOK(t, "ab")
	if eof := toks[1]; eof.Line != 1 || eof.Col != 3 {
		t.Errorf("EOF position = %d:%d, want 1:3 (1-based, one past `ab`)", eof.Line, eof.Col)
	}
	toks = lexOK(t, "ab\ncd")
	if eof := toks[2]; eof.Line != 2 || eof.Col != 3 {
		t.Errorf("EOF position = %d:%d, want 2:3", eof.Line, eof.Col)
	}
}

// TestLexTokenShapes — the canonical query from the quickstart lexes to
// the expected kind sequence with verbatim texts.
func TestLexTokenShapes(t *testing.T) {
	toks := lexOK(t, `port==443 && title CONTAINS "login"`)
	want := []Kind{IDENT, EQ, INT, AND, IDENT, KW_CONTAINS, STRING, EOF}
	if !kindEqual(kinds(toks), want) {
		t.Fatalf("kind stream = %v, want %v", kinds(toks), want)
	}
	checks := []struct {
		i    int
		kind Kind
		text string
		val  string
	}{
		{0, IDENT, "port", "port"},
		{1, EQ, "==", "=="},
		{2, INT, "443", "443"},
		{3, AND, "&&", "&&"},
		{4, IDENT, "title", "title"},
		{5, KW_CONTAINS, "CONTAINS", "CONTAINS"},
		{6, STRING, `"login"`, "login"},
	}
	for _, c := range checks {
		tok := toks[c.i]
		if tok.Kind != c.kind || tok.Text != c.text || tok.Value != c.val {
			t.Errorf("toks[%d] = %v, want %s text=%q value=%q", c.i, tok, c.kind, c.text, c.val)
		}
	}
}

// TestLexIdentifiersAndDotPaths — identifiers are dot-paths; keywords
// never fire on dotted lexemes or on identifiers that merely start with
// a keyword's letters.
func TestLexIdentifiersAndDotPaths(t *testing.T) {
	cases := []struct {
		src  string
		kind Kind
		text string
	}{
		{"port", IDENT, "port"},
		{"http.title", IDENT, "http.title"},
		{"tls.cert_subject_cn", IDENT, "tls.cert_subject_cn"},
		{"a.b.c", IDENT, "a.b.c"},
		{"_private", IDENT, "_private"},
		{"port80", IDENT, "port80"},
		{"android", IDENT, "android"},       // starts with `and`, still an IDENT
		{"and.x", IDENT, "and.x"},           // dotted lexemes are never keywords
		{"in_domainx", IDENT, "in_domainx"}, // maximal munch then tail
		{"true", IDENT, "true"},             // booleans are validator territory (M7)
	}
	for _, c := range cases {
		toks := lexOK(t, c.src)
		if toks[0].Kind != c.kind || toks[0].Text != c.text {
			t.Errorf("Lex(%q)[0] = %v, want %s %q", c.src, toks[0], c.kind, c.text)
		}
	}
}

// TestLexKeywordsCaseInsensitive — the keyword set matches any casing.
func TestLexKeywordsCaseInsensitive(t *testing.T) {
	cases := map[string]Kind{
		"and": KW_AND, "AND": KW_AND, "And": KW_AND,
		"or": KW_OR, "OR": KW_OR,
		"not": KW_NOT, "NOT": KW_NOT,
		"in": KW_IN, "IN": KW_IN,
		"any": KW_ANY, "ANY": KW_ANY,
		"all": KW_ALL, "ALL": KW_ALL,
		"contains": KW_CONTAINS, "Contains": KW_CONTAINS,
		"in_cidr": KW_IN_CIDR, "IN_CIDR": KW_IN_CIDR, "In_Cidr": KW_IN_CIDR,
		"in_domain": KW_IN_DOMAIN, "IN_DOMAIN": KW_IN_DOMAIN,
	}
	for src, want := range cases {
		toks := lexOK(t, src)
		if toks[0].Kind != want || toks[0].Value != src {
			t.Errorf("Lex(%q)[0] = %v, want %s", src, toks[0], want)
		}
	}
}

// TestLexSymbolicOperators — maximal munch for the two-character
// operators and the single-character fallbacks, including the sugar
// forms the parser owns (`=` → ASSIGN, lone `!` → NOT).
func TestLexSymbolicOperators(t *testing.T) {
	cases := []struct {
		src  string
		want []Kind
	}{
		{"&&", []Kind{AND, EOF}},
		{"||", []Kind{OR, EOF}},
		{"!", []Kind{NOT, EOF}},
		{"==", []Kind{EQ, EOF}},
		{"!=", []Kind{NEQ, EOF}},
		{"=", []Kind{ASSIGN, EOF}},
		{"<", []Kind{LT, EOF}},
		{"<=", []Kind{LE, EOF}},
		{">", []Kind{GT, EOF}},
		{">=", []Kind{GE, EOF}},
		{"===", []Kind{EQ, ASSIGN, EOF}},  // maximal munch then fallback
		{"!==", []Kind{NEQ, ASSIGN, EOF}}, // != then =
		{"<==", []Kind{LE, ASSIGN, EOF}},  // <= then =
	}
	for _, c := range cases[:7] {
		toks := lexOK(t, c.src)
		if !kindEqual(kinds(toks), c.want) {
			t.Errorf("Lex(%q) kinds = %v, want %v", c.src, kinds(toks), c.want)
		}
	}
	// The three concatenated-sugar forms.
	for _, c := range cases[7:10] {
		toks := lexOK(t, c.src)
		if !kindEqual(kinds(toks), c.want) {
			t.Errorf("Lex(%q) kinds = %v, want %v", c.src, kinds(toks), c.want)
		}
	}
	// A lone `&` is illegal (not part of any token).
	le := lexErr(t, "a&b")
	if le.Line != 1 || le.Col != 2 {
		t.Errorf("lone '&' error position = %d:%d, want 1:2", le.Line, le.Col)
	}
}

// TestLexPunctuationAndNumbers — parentheses, commas, digit runs, and
// maximal-munch boundaries between INT and IDENT.
func TestLexPunctuationAndNumbers(t *testing.T) {
	toks := lexOK(t, "port IN (80,443)")
	want := []Kind{IDENT, KW_IN, LPAREN, INT, COMMA, INT, RPAREN, EOF}
	if !kindEqual(kinds(toks), want) {
		t.Fatalf("kinds = %v, want %v", kinds(toks), want)
	}
	if toks[3].Text != "80" || toks[5].Text != "443" {
		t.Errorf("int texts = %q,%q, want 80,443", toks[3].Text, toks[5].Text)
	}

	toks = lexOK(t, "443abc")
	if !kindEqual(kinds(toks), []Kind{INT, IDENT, EOF}) {
		t.Errorf("443abc kinds = %v, want [INT IDENT EOF] (maximal munch)", kinds(toks))
	}
	if toks[0].Text != "443" || toks[1].Text != "abc" {
		t.Errorf("munch split = %q,%q, want 443,abc", toks[0].Text, toks[1].Text)
	}

	// Quantified-block braces tokenize as punctuation (M8 roundtrip
	// contract with ast.Quantified.String).
	toks = lexOK(t, "ANY(host){ port == 1 }")
	want = []Kind{KW_ANY, LPAREN, IDENT, RPAREN, LBRACE, IDENT, EQ, INT, RBRACE, EOF}
	if !kindEqual(kinds(toks), want) {
		t.Errorf("quantifier kinds = %v, want %v", kinds(toks), want)
	}
}

// TestLexStrings — escape decoding, empty strings, adjacency, and the
// Value vs Text distinction.
func TestLexStrings(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`"login"`, "login"},
		{`""`, ""},
		{`"a b"`, "a b"},
		{`"say \"hi\""`, `say "hi"`},
		{`"back\\slash"`, `back\slash`},
		{`"slash/slash"`, "slash/slash"},
		{`"nl\nrt\r"`, "nl\nrt\r"},
		{`"tab\tff\fbb\b"`, "tab\tff\fbb\b"},
		{`"caf\u00e9"`, "café"},
		{`"emoji \u2713"`, "emoji ✓"},
	}
	for _, c := range cases {
		toks := lexOK(t, c.src)
		if toks[0].Kind != STRING {
			t.Errorf("Lex(%q)[0] kind = %v, want STRING", c.src, toks[0].Kind)
			continue
		}
		if toks[0].Value != c.want {
			t.Errorf("Lex(%q)[0].Value = %q, want %q", c.src, toks[0].Value, c.want)
		}
		if toks[0].Text != c.src { // TEXT is verbatim for single-token sources
			t.Errorf("Lex(%q)[0].Text = %q, want verbatim source", c.src, toks[0].Text)
		}
	}

	// Adjacent literals lex as TWO tokens — the lexer never joins
	// them; juxtaposition is the parser's rejection (M6).
	toks := lexOK(t, `"a""b"`)
	if !kindEqual(kinds(toks), []Kind{STRING, STRING, EOF}) {
		t.Errorf(`adjacent literals kinds = %v, want [STRING STRING EOF]`, kinds(toks))
	} else if toks[0].Value != "a" || toks[1].Value != "b" {
		t.Errorf(`adjacent literal values = %q,%q, want a,b`, toks[0].Value, toks[1].Value)
	}
}

// TestLexPositions — 1-based line:col tracking across lines, tabs, and
// multi-byte runes (columns count RUNES, not bytes).
func TestLexPositions(t *testing.T) {
	toks := lexOK(t, "port==443 &&\n  title CONTAINS \"login\"")
	want := []struct {
		i         int
		line, col int
	}{
		{0, 1, 1},  // port
		{1, 1, 5},  // ==
		{2, 1, 7},  // 443
		{3, 1, 11}, // &&
		{4, 2, 3},  // title (after two leading spaces)
		{5, 2, 9},  // CONTAINS
		{6, 2, 18}, // "login"
	}
	for _, w := range want {
		tok := toks[w.i]
		if tok.Line != w.line || tok.Col != w.col {
			t.Errorf("toks[%d] %v position = %d:%d, want %d:%d", w.i, tok.Kind, tok.Line, tok.Col, w.line, w.col)
		}
	}

	// Tabs advance the column by one per rune; multi-byte runes count
	// as one column each.
	toks = lexOK(t, "\t\"é\" x")
	if toks[0].Col != 2 || toks[1].Col != 6 || toks[1].Line != 1 {
		t.Errorf("tab+unicode positions = %d:%d and %d:%d, want 2 and 6 on line 1",
			toks[0].Col, toks[0].Line, toks[1].Col, toks[1].Line)
	}
}

// TestLexFullQuery — the whole M5 surface in one stream: sugar ops,
// symbolic + keyword booleans, quantifier keywords, IN lists, dot-paths,
// strings with escapes.
func TestLexFullQuery(t *testing.T) {
	src := "!(http.title != \"Admin Login\") && port in (80, 443) ||\nANY(host) && ip IN_CIDR \"198.51.100.0/24\""
	toks := lexOK(t, src)
	want := []Kind{
		NOT, LPAREN, IDENT, NEQ, STRING, RPAREN, AND, IDENT, KW_IN,
		LPAREN, INT, COMMA, INT, RPAREN, OR, KW_ANY, LPAREN, IDENT, RPAREN,
		AND, IDENT, KW_IN_CIDR, STRING, EOF,
	}
	if !kindEqual(kinds(toks), want) {
		t.Fatalf("kinds = %v\nwant   = %v", kinds(toks), want)
	}
	if got := toks[4].Value; got != "Admin Login" {
		t.Errorf("string value = %q, want %q", got, "Admin Login")
	}
	if got := toks[22].Value; got != "198.51.100.0/24" {
		t.Errorf("CIDR string value = %q, want %q", got, "198.51.100.0/24")
	}
	// The second line starts at line 2.
	if toks[15].Line != 2 || toks[15].Col != 1 {
		t.Errorf("ANY position = %d:%d, want 2:1", toks[15].Line, toks[15].Col)
	}
}

// TestLexErrorIllegalChars — illegal characters abort with GQL_E_LEX,
// the exact position, and a concrete hint (single quotes point at
// double-quoted strings).
func TestLexErrorIllegalChars(t *testing.T) {
	cases := []struct {
		src     string
		line    int
		col     int
		hintHas string
	}{
		{`'single'`, 1, 1, "double quotes"},
		{`port==443;`, 1, 10, "not part of the GQL grammar"},
		{"port == -1", 1, 9, "negative numbers"},
		{"port == 1.2.3.4", 1, 10, "double-quoted strings"}, // the first '.' is the illegal char
		{"port == #", 1, 9, "double-quoted string"},
	}
	for _, c := range cases {
		le := lexErr(t, c.src)
		if le.Line != c.line || le.Col != c.col {
			t.Errorf("Lex(%q) error position = %d:%d, want %d:%d", c.src, le.Line, le.Col, c.line, c.col)
		}
		if !strings.Contains(le.Next, c.hintHas) {
			t.Errorf("Lex(%q) hint %q missing %q", c.src, le.Next, c.hintHas)
		}
		if !strings.HasPrefix(le.Error(), "GQL_E_LEX: ") {
			t.Errorf("Lex(%q) rendering = %q, want GQL_E_LEX prefix", c.src, le.Error())
		}
	}
}

// TestLexErrorUnterminatedString — EOF and raw newlines both end the
// scan; the error points at the OPENING quote.
func TestLexErrorUnterminatedString(t *testing.T) {
	le := lexErr(t, `port=="abc`)
	if le.Line != 1 || le.Col != 7 {
		t.Errorf("EOF-unterminated error position = %d:%d, want 1:7 (opening quote)", le.Line, le.Col)
	}
	if !strings.Contains(le.Detail, "unterminated string literal") {
		t.Errorf("detail = %q, want unterminated-string wording", le.Detail)
	}

	le = lexErr(t, "a == \"abc\ndef == 1")
	if le.Line != 1 || le.Col != 6 {
		t.Errorf("newline-unterminated error position = %d:%d, want 1:6", le.Line, le.Col)
	}
}

// TestLexErrorEscapes — invalid escapes point at the BACKSLASH.
func TestLexErrorEscapes(t *testing.T) {
	le := lexErr(t, `"abc\qdef"`)
	if le.Line != 1 || le.Col != 5 {
		t.Errorf("invalid-escape error position = %d:%d, want 1:5 (backslash)", le.Line, le.Col)
	}
	if !strings.Contains(le.Detail, "invalid escape sequence") {
		t.Errorf("detail = %q, want invalid-escape wording", le.Detail)
	}
	if !strings.Contains(le.Next, "\\uXXXX") {
		t.Errorf("hint = %q, want the escape-set list", le.Next)
	}

	// \u with a non-hex digit and \u cut off by EOF.
	le = lexErr(t, `"\u12g4"`)
	if le.Line != 1 || le.Col != 2 {
		t.Errorf("bad \\u error position = %d:%d, want 1:2", le.Line, le.Col)
	}
	le = lexErr(t, `"\u12`)
	if !strings.Contains(le.Detail, "\\u") {
		t.Errorf("truncated \\u detail = %q", le.Detail)
	}
}

// TestLexErrorRendering — the structured fields render as what → why →
// Try:, matching the project-wide error style.
func TestLexErrorRendering(t *testing.T) {
	le := lexErr(t, `x == 'y'`)
	rendered := le.Error()
	for _, want := range []string{"GQL_E_LEX:", "unexpected character", "\nWhy: ", "\nTry: ", "double quotes"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered error missing %q:\n%s", want, rendered)
		}
	}
	if le.Line != 1 || le.Col != 6 {
		t.Errorf("quote error position = %d:%d, want 1:6", le.Line, le.Col)
	}
}

// TestLexFirstErrorAborts — the lexer reports the FIRST lexical error
// and does not return a partial token stream.
func TestLexFirstErrorAborts(t *testing.T) {
	toks, err := Lex(`a b 'c d "e" @`)
	if err == nil {
		t.Fatalf("want error for the first illegal char, got tokens %v", toks)
	}
	if toks != nil {
		t.Errorf("error path must return a nil token stream, got %v", toks)
	}
	var le *Error
	if !errors.As(err, &le) || le.Col != 5 {
		t.Errorf("first error should point at the quote in col 5, got %v", err)
	}
}
