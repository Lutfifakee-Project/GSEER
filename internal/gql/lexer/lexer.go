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

// lexer.go — the scanning implementation (the package-level contract
// lives in doc.go; the error type in errors.go).
//
// Lexing policy (deliberate, M5-scoped):
//   - Maximal munch: `==` wins over `=`, `!=` over `!`, `&&` over … .
//   - `=` alone lexes as ASSIGN; the parser treats it as `==` sugar.
//   - Bare `-`/`.`/digits-followed-by-dots are NOT number syntax: v0.0.1
//     integer literals are plain digit runs, and IP/date values are
//     double-quoted strings — the illegal-character error points there.
//   - true/false lex as identifiers: the validator's typing (M7) owns
//     boolean-ish values; a field named `true` stays lexable.
//   - No input-length cap here: K-13 node/depth limits are parse-time
//     (M6) per the plan.
//
// The lexer never imports any collector, egress, or P2 package
// (ADR-0010; AC-3.1).
package lexer

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Kind enumerates the GQL token kinds (SPEC-04 §2).
type Kind int

// Token kinds.  The zero value is ILLEGAL and never appears in a
// successful token stream (lexical errors abort with *Error instead).
const (
	ILLEGAL Kind = iota

	EOF    // end of input
	IDENT  // field path: [A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*
	INT    // integer literal: [0-9]+
	STRING // double-quoted string literal, escapes decoded into Value

	// Punctuation.
	LPAREN // (
	RPAREN // )
	COMMA  // ,
	LBRACE // {  (quantified-block delimiters; the parser, M6, decides
	RBRACE // }   where they are legal — the lexer only tokenizes)

	// Symbolic boolean operators (parser maps keyword forms onto these).
	AND // &&
	OR  // ||
	NOT // !

	// Comparison operators.  ASSIGN is the `=` sugar; the parser (M6)
	// rewrites it to EQ.  `!=` is its own NEQ operator per SPEC-04 §3.
	EQ     // ==
	NEQ    // !=
	ASSIGN // = (sugar for ==)
	LT     // <
	LE     // <=
	GT     // >
	GE     // >=

	// Case-insensitive keyword forms (SPEC-04 §2).  The parser applies
	// the AND/OR/NOT → &&/||/! sugar; the operator keywords keep their
	// names (CONTAINS, IN_CIDR, IN_DOMAIN are real operators, not sugar).
	KwAnd      // and
	KwOr       // or
	KwNot      // not
	KwIn       // in   (IN lists; parser bounds list length ≤ 256)
	KwAny      // any  (quantified block; validator gates support)
	KwAll      // all  (parses; validator rejects in v0.0.1)
	KwContains // contains
	KwInCIDR   // in_cidr
	KwInDomain // in_domain

	// MATCH is the `*=` wildcard fuzzy-match operator (compatibility
	// syntax; `*` matches any run of characters, `?` exactly one).
	MATCH
)

// kindNames powers Kind.String and the "did you mean"-style hints.
var kindNames = map[Kind]string{
	ILLEGAL: "ILLEGAL", EOF: "EOF", IDENT: "IDENT", INT: "INT", STRING: "STRING",
	LPAREN: "LPAREN", RPAREN: "RPAREN", COMMA: "COMMA", LBRACE: "LBRACE", RBRACE: "RBRACE",
	AND: "AND", OR: "OR", NOT: "NOT",
	EQ: "EQ", NEQ: "NEQ", ASSIGN: "ASSIGN", LT: "LT", LE: "LE", GT: "GT", GE: "GE",
	KwAnd: "KwAnd", KwOr: "KwOr", KwNot: "KwNot", KwIn: "KwIn",
	KwAny: "KwAny", KwAll: "KwAll", KwContains: "KwContains",
	KwInCIDR: "KwInCIDR", KwInDomain: "KwInDomain", MATCH: "MATCH",
}

// String renders the kind name (test/explain ergonomics).
func (k Kind) String() string {
	if n, ok := kindNames[k]; ok {
		return n
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Token is one lexed GQL token with its 1-based source position.
type Token struct {
	Kind  Kind
	Text  string // verbatim lexeme as it appears in the query
	Value string // decoded value: string→unescaped contents, int→digits, else == Text
	Line  int    // 1-based line of the token's first rune
	Col   int    // 1-based (rune) column of the token's first rune
}

// String renders a debug-friendly one-line form.
func (t Token) String() string {
	return fmt.Sprintf("%s(%q) @%d:%d", t.Kind, t.Text, t.Line, t.Col)
}

// keywords maps the case-insensitive keyword forms to their kinds.  A
// single-segment identifier whose lowercase form hits this table lexes
// as the keyword; anything else (including every dotted path) stays an
// IDENT so field names can never collide with operators.
var keywords = map[string]Kind{
	"and":       KwAnd,
	"or":        KwOr,
	"not":       KwNot,
	"in":        KwIn,
	"any":       KwAny,
	"all":       KwAll,
	"contains":  KwContains,
	"in_cidr":   KwInCIDR,
	"in_domain": KwInDomain,
}

// Lex tokenizes the whole query.  On the first lexical error it returns
// (nil, *Error); a successful stream always ends with exactly one EOF
// token positioned one past the last rune.
func Lex(input string) ([]Token, error) {
	lx := &lexer{src: input, line: 1, col: 1}
	return lx.run()
}

// lexer holds the scanning state.  line/col track the position of the
// NEXT rune to read (1-based); startLine/startCol record the position
// of the token currently being scanned.
type lexer struct {
	src       string
	pos       int // byte offset of the next rune
	line, col int
	startLine int
	startCol  int
}

// run scans to EOF, token by token, via the dispatch table below.
func (lx *lexer) run() ([]Token, error) {
	var toks []Token
	for {
		tok, err := lx.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, tok)
		if tok.Kind == EOF {
			return toks, nil
		}
	}
}

// peek returns the next rune without consuming it (0 at EOF).
func (lx *lexer) peek() rune {
	if lx.pos >= len(lx.src) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(lx.src[lx.pos:])
	return r
}

// peek2 returns the rune after peek (0 if absent) — used for the
// two-character operators' maximal munch.
func (lx *lexer) peek2() rune {
	if lx.pos >= len(lx.src) {
		return 0
	}
	_, w := utf8.DecodeRuneInString(lx.src[lx.pos:])
	if lx.pos+w >= len(lx.src) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(lx.src[lx.pos+w:])
	return r
}

// advance consumes one rune, maintaining line/col.
func (lx *lexer) advance() rune {
	r, w := utf8.DecodeRuneInString(lx.src[lx.pos:])
	lx.pos += w
	if r == '\n' {
		lx.line++
		lx.col = 1
	} else {
		lx.col++
	}
	return r
}

// emit builds a token for the lexeme spanning [start, lx.pos) with the
// recorded start position.
func (lx *lexer) emit(kind Kind, start int, value string) Token {
	return Token{
		Kind:  kind,
		Text:  lx.src[start:lx.pos],
		Value: value,
		Line:  lx.startLine,
		Col:   lx.startCol,
	}
}

// twoCharOps drives maximal-munch scanning of the two-character
// operators: the leading rune either doubles ('&&', '||', '==') or
// pairs with '=' ('!=', '<=', '>=').
var twoCharOps = map[rune]struct {
	self rune // second rune when the operator doubles
	alt  rune // second rune for the pairwise form
	kind Kind
	text string
}{
	'&': {self: '&', kind: AND, text: "&&"},
	'|': {self: '|', kind: OR, text: "||"},
	'=': {self: '=', kind: EQ, text: "=="},
	'*': {alt: '=', kind: MATCH, text: "*="},
	'!': {alt: '=', kind: NEQ, text: "!="},
	'<': {alt: '=', kind: LE, text: "<="},
	'>': {alt: '=', kind: GE, text: ">="},
}

// singleCharOps maps the one-character punctuation tokens.
var singleCharOps = map[rune]struct {
	kind Kind
	text string
}{
	'(': {kind: LPAREN, text: "("},
	')': {kind: RPAREN, text: ")"},
	',': {kind: COMMA, text: ","},
	'{': {kind: LBRACE, text: "{"},
	'}': {kind: RBRACE, text: "}"},
	'!': {kind: NOT, text: "!"},
	'=': {kind: ASSIGN, text: "="},
	'<': {kind: LT, text: "<"},
	'>': {kind: GT, text: ">"},
}

// scanTwoChar consumes a two-character operator when the input has one
// at the current position (maximal munch).  matched=false means the
// leading rune does not start a two-char operator (or the second rune
// is missing) and the caller falls back to the single-char table.
func (lx *lexer) scanTwoChar(r rune, start int) (Token, bool) {
	op, ok := twoCharOps[r]
	if !ok {
		return Token{}, false
	}
	second := op.self
	if second == 0 {
		second = op.alt
	}
	if lx.peek2() != second {
		return Token{}, false
	}
	lx.advance()
	lx.advance()
	return lx.emit(op.kind, start, op.text), true
}

// next dispatches on the next rune through the tables above and returns
// the next token (EOF at end of input).
func (lx *lexer) next() (Token, error) {
	lx.skipSpace()
	lx.startLine, lx.startCol = lx.line, lx.col
	start := lx.pos

	r := lx.peek()
	switch {
	case r == 0:
		return lx.emit(EOF, start, ""), nil

	// Identifiers / keywords (table-driven keyword lookup inside).
	case isIdentStart(r):
		return lx.scanIdent(), nil

	// Integer literals.
	case isDigit(r):
		return lx.scanInt(), nil

	// String literals (the only path that can produce a lexical error
	// besides the illegal-character fallback).
	case r == '"':
		return lx.scanString()
	}
	if tok, matched := lx.scanTwoChar(r, start); matched {
		return tok, nil
	}
	if op, ok := singleCharOps[r]; ok {
		lx.advance()
		return lx.emit(op.kind, start, op.text), nil
	}
	lx.advance()
	return Token{}, newLexError(lx.startLine, lx.startCol,
		fmt.Sprintf("unexpected character %s at line %d, column %d", quoteRune(r), lx.startLine, lx.startCol),
		"the character does not start any GQL token",
		hintForChar(r))
}

// skipSpace consumes spaces, tabs and newlines (position tracking lives
// in advance).
func (lx *lexer) skipSpace() {
	for {
		switch lx.peek() {
		case ' ', '\t', '\n', '\r':
			lx.advance()
		default:
			return
		}
	}
}

// scanIdent consumes an identifier or dotted field path and returns a
// keyword token when the whole lexeme is a single undotted segment that
// matches the keyword table case-insensitively.
func (lx *lexer) scanIdent() Token {
	start := lx.pos
	lx.advance() // the isIdentStart rune is guaranteed by the dispatch
	for {
		r := lx.peek()
		if isIdentPart(r) {
			lx.advance()
			continue
		}
		if r == '.' && isIdentStart(lx.peek2()) {
			lx.advance() // dot
			lx.advance() // next segment head
			continue
		}
		break
	}
	text := lx.src[start:lx.pos]
	if !strings.ContainsRune(text, '.') {
		if kind, ok := keywords[strings.ToLower(text)]; ok {
			return lx.emit(kind, start, text)
		}
	}
	return lx.emit(IDENT, start, text)
}

// scanInt consumes a digit run.
func (lx *lexer) scanInt() Token {
	start := lx.pos
	for isDigit(lx.peek()) {
		lx.advance()
	}
	return lx.emit(INT, start, lx.src[start:lx.pos])
}

// scanString consumes a double-quoted string literal, decoding the
// escape set into the token's Value.  A raw newline or EOF before the
// closing quote is an unterminated-string error positioned at the
// literal's opening quote; an unknown escape is positioned at the
// backslash.
func (lx *lexer) scanString() (Token, error) {
	start := lx.pos
	openLine, openCol := lx.startLine, lx.startCol
	lx.advance() // opening quote

	var val strings.Builder
	for {
		r := lx.peek()
		switch r {
		case 0, '\n':
			// Position the error at the OPENING quote: that is where the
			// operator looks when fixing an unterminated literal.
			return Token{}, newLexError(openLine, openCol,
				fmt.Sprintf("unterminated string literal starting at line %d, column %d", openLine, openCol),
				"the closing double quote is missing (a raw line break also ends scanning)",
				"add the closing \" on the same line, or escape a literal line break as \\n")
		case '"':
			lx.advance()
			return lx.emit(STRING, start, val.String()), nil
		case '\\':
			escLine, escCol := lx.line, lx.col
			lx.advance() // backslash
			e := lx.peek()
			switch e {
			case '"':
				lx.advance()
				val.WriteByte('"')
			case '\\':
				lx.advance()
				val.WriteByte('\\')
			case '/':
				lx.advance()
				val.WriteByte('/')
			case 'b':
				lx.advance()
				val.WriteByte('\b')
			case 'f':
				lx.advance()
				val.WriteByte('\f')
			case 'n':
				lx.advance()
				val.WriteByte('\n')
			case 'r':
				lx.advance()
				val.WriteByte('\r')
			case 't':
				lx.advance()
				val.WriteByte('\t')
			case 'u':
				lx.advance()
				var cp rune
				for i := 0; i < 4; i++ {
					h := lx.peek()
					if !isHex(h) {
						return Token{}, newLexError(escLine, escCol,
							fmt.Sprintf("invalid \\u escape at line %d, column %d", escLine, escCol),
							"\\u must be followed by exactly four hexadecimal digits",
							"complete the four hex digits, e.g. \\u00e9")
					}
					cp = cp<<4 | hexNibble(h)
					lx.advance()
				}
				val.WriteRune(cp)
			default:
				return Token{}, newLexError(escLine, escCol,
					fmt.Sprintf("invalid escape sequence \\%s at line %d, column %d", quoteRune(e), escLine, escCol),
					"the escape is not part of the GQL string grammar",
					"valid escapes: \\\" \\\\ \\/ \\b \\f \\n \\r \\t \\uXXXX")
			}
		default:
			lx.advance()
			val.WriteRune(r)
		}
	}
}

// quoteRune renders r for an error message (visible for quote/space).
func quoteRune(r rune) string {
	switch r {
	case '"':
		return `'"'`
	case ' ':
		return "' '"
	case '\t':
		return "'\t'"
	case '\r':
		return "'\\r'"
	case 0:
		return "end of input"
	default:
		return fmt.Sprintf("'%c'", r)
	}
}

// hintForChar returns the concrete "Try:" hint for common illegal
// characters (Easy-to-Use: the error says what WOULD work).
func hintForChar(r rune) string {
	switch r {
	case '\'':
		return "GQL string literals use double quotes: \"text\", not 'text'"
	case '-':
		return "negative numbers are not GQL literals; quote the value as a string if it is text"
	case '.':
		return "a bare '.' appears in field paths (a.b); IP addresses and other dotted values must be double-quoted strings"
	case ':', ';', '[', ']':
		return "the character is not part of the GQL grammar (SPEC-04 §2)"
	default:
		return "remove the character or quote the value as a double-quoted string"
	}
}

// isIdentStart reports whether r may start an identifier segment.
func isIdentStart(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// isIdentPart reports whether r may continue an identifier segment.
func isIdentPart(r rune) bool {
	return isIdentStart(r) || isDigit(r)
}

// isDigit reports whether r is an ASCII digit.
func isDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

// isHex reports whether r is an ASCII hexadecimal digit.
func isHex(r rune) bool {
	return isDigit(r) || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// hexNibble converts an ASCII hex digit to its 0–15 value.  The caller
// has already verified isHex(r).
func hexNibble(r rune) rune {
	switch {
	case r >= '0' && r <= '9':
		return r - '0'
	case r >= 'a' && r <= 'f':
		return r - 'a' + 10
	default:
		return r - 'A' + 10
	}
}
