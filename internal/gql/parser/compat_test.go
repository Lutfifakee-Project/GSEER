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

// compat_test.go — parser coverage for the compatibility layer:
// `*=` wildcard comparisons and the free-text (bare keyword) form.
package parser

import (
	"testing"

	"github.com/Lutfifakee-Project/gseer/internal/gql/ast"
)

func TestParseWildcardMatch(t *testing.T) {
	n, err := Parse(`http.title *= "Port?l"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c, ok := n.(*ast.Comparison)
	if !ok {
		t.Fatalf("node = %T, want *ast.Comparison", n)
	}
	if c.Op != ast.OpMatch {
		t.Errorf("op = %s, want *=", c.Op)
	}
	if c.Field != "http.title" {
		t.Errorf("field = %q, want http.title", c.Field)
	}
	if c.Value.String() != `"Port?l"` {
		t.Errorf("value = %s, want \"Port?l\"", c.Value)
	}
}

func TestParseFreeTextSingleTerm(t *testing.T) {
	n, err := Parse(`"wordpress"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ft, ok := n.(*ast.FreeText)
	if !ok {
		t.Fatalf("node = %T, want *ast.FreeText", n)
	}
	if len(ft.Terms) != 1 || ft.Terms[0] != "wordpress" {
		t.Errorf("terms = %v, want [wordpress]", ft.Terms)
	}
}

func TestParseFreeTextMultipleTerms(t *testing.T) {
	n, err := Parse(`"apache" "login"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ft, ok := n.(*ast.FreeText)
	if !ok {
		t.Fatalf("node = %T, want *ast.FreeText", n)
	}
	if len(ft.Terms) != 2 || ft.Terms[0] != "apache" || ft.Terms[1] != "login" {
		t.Errorf("terms = %v, want [apache login]", ft.Terms)
	}
	// Canonical re-render must re-parse to the same node.
	n2, err := Parse(ft.String())
	if err != nil {
		t.Fatalf("re-parse of %q: %v", ft.String(), err)
	}
	if n2.String() != ft.String() {
		t.Errorf("canonical form is not stable: %q vs %q", n2.String(), ft.String())
	}
}

func TestParseFreeTextInsideGroup(t *testing.T) {
	// Free text composes with the full boolean grammar.
	n, err := Parse(`("wordpress" or "joomla") && port == 80`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	or, ok := n.(*ast.And)
	if !ok {
		t.Fatalf("root = %T, want *ast.And", n)
	}
	if _, ok := or.L.(*ast.Or); !ok {
		t.Errorf("left = %T, want *ast.Or (the free-text group)", or.L)
	}
}

func TestParseFreeTextEmptyTermError(t *testing.T) {
	if _, err := Parse(`""`); err == nil {
		t.Fatal("empty free-text term must be a parse error")
	}
}

func TestParseWildcardOnIntFieldParses(t *testing.T) {
	// The parser is grammar-complete: `port *= "44*"` parses; the
	// OPERATOR gate (int fields reject *=) belongs to the validator.
	if _, err := Parse(`port *= "44*"`); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}
