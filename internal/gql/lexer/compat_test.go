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

// compat_test.go — lexer coverage for the compatibility operators:
// the `*=` wildcard fuzzy-match token and the hard rule that a bare
// `*` still lexes as an error (it is not a GQL token).
package lexer

import "testing"

func TestLexWildcardMatch(t *testing.T) {
	toks, err := Lex(`hostname *= "*.example.*"`)
	if err != nil {
		t.Fatalf("Lex: %v", err)
	}
	wantKinds := []Kind{IDENT, MATCH, STRING, EOF}
	if len(toks) != len(wantKinds) {
		t.Fatalf("got %d tokens, want %d: %v", len(toks), len(wantKinds), toks)
	}
	for i, k := range wantKinds {
		if toks[i].Kind != k {
			t.Errorf("token[%d] = %s, want %s", i, toks[i].Kind, k)
		}
	}
	if got := toks[1].Text; got != "*=" {
		t.Errorf("MATCH token text = %q, want %q", got, "*=")
	}
}

func TestLexBareAsteriskIsIllegal(t *testing.T) {
	if _, err := Lex(`port * 443`); err == nil {
		t.Fatal("bare `*` must remain a lexical error (not a GQL token)")
	}
}

func TestLexSymbolicBooleans(t *testing.T) {
	// The symbolic boolean operators are compatibility-syntax anchors:
	// pin their tokenization so a lexer refactor cannot drop them.
	toks, err := Lex(`a == 1 && b == 2 || !c`)
	if err != nil {
		t.Fatalf("Lex: %v", err)
	}
	var kinds []Kind
	for _, tok := range toks {
		if tok.Kind == AND || tok.Kind == OR || tok.Kind == NOT {
			kinds = append(kinds, tok.Kind)
		}
	}
	want := []Kind{AND, OR, NOT}
	if len(kinds) != len(want) {
		t.Fatalf("operator tokens = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("operator[%d] = %s, want %s", i, kinds[i], want[i])
		}
	}
}
