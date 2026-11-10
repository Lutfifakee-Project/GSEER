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

package validator

import (
	"errors"
	"strings"
	"testing"

	"github.com/Lutfifakee-Project/GSEER/internal/gql/ast"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/parser"
)

// validator_test.go — the M7 acceptance tests: alias rewrite, strict
// typing (port=="443" → GQL_E_TYPE + hint), operator/field
// compatibility, scope validation, the ALL gate, unknown fields with
// did-you-mean, and canonical-path rewriting visible in String().

// comparisonsByField collects every *ast.Comparison for the given field
// name (post-rewrite canonical path) via ast.Walk.
func comparisonsByField(n ast.Node, field string) []*ast.Comparison {
	var found []*ast.Comparison
	v := &collector{field: field, out: &found}
	ast.Walk(v, n)
	return found
}

type collector struct {
	field string
	out   *[]*ast.Comparison
}

func (c *collector) Visit(n ast.Node) ast.Visitor {
	if cmp, ok := n.(*ast.Comparison); ok && cmp.Field == c.field {
		*c.out = append(*c.out, cmp)
		return nil
	}
	return c
}

// validate parses and validates, returning the rewritten tree or the
// *Error.
func validate(t *testing.T, src string) (ast.Node, *Error) {
	t.Helper()
	n, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	out, err := Validate(n)
	if err == nil {
		return out, nil
	}
	var ve *Error
	if !errors.As(err, &ve) {
		t.Fatalf("Validate(%q) error is %T (%v), want *validator.Error", src, err, err)
	}
	return nil, ve
}

// mustValidate parses+validates or fails.
func mustValidate(t *testing.T, src string) ast.Node {
	t.Helper()
	n, ve := validate(t, src)
	if ve != nil {
		t.Fatalf("Validate(%q): %v", src, ve)
	}
	return n
}

// TestValidateAccepts — the canonical happy paths.
func TestValidateAccepts(t *testing.T) {
	srcs := []string{
		`port == 443`,
		`port = 443`, // sugar normalizes before validation
		`title CONTAINS "login"`,
		`ip IN_CIDR "198.51.100.0/24"`,
		`hostname IN_DOMAIN "example.net"`,
		`country == "US"`,                          // alias
		`status == 200 && server CONTAINS "nginx"`, // aliases
		`port in (80, 443)`,
		`stale == "false"`,
		`first_seen > "2026-01-01T00:00:00Z"`,
		`cert_expiry < "2027-01-01T00:00:00Z"`,
		`technologies CONTAINS "nginx"`,
		`ANY(host){ title CONTAINS "Admin" }`,
		`ANY(service){ exists(http.title) }`,
		`exists(http.title)`,
		`http.title`, // bare field
		`!(port == 80) && (title CONTAINS "login" || port > 8000)`,
	}
	for _, src := range srcs {
		mustValidate(t, src)
	}
}

// TestValidateAliasRewrite — aliases rewrite to canonical paths in
// place, visible through the canonical String().
func TestValidateAliasRewrite(t *testing.T) {
	cases := []struct{ src, want string }{
		{`country == "US"`, `geo_country == "US"`},
		{`title CONTAINS "login"`, `http.title CONTAINS "login"`},
		{`server == "nginx"`, `http.server == "nginx"`},
		{`status == 200`, `http.status == 200`},
		{`city == "Berlin"`, `geo_city == "Berlin"`},
		{`tech CONTAINS "nginx"`, `technologies CONTAINS "nginx"`},
		{`tls_version == "TLSv1.3"`, `tls.version == "TLSv1.3"`},
		{`cert_subject CONTAINS "example"`, `tls.cert_subject_cn CONTAINS "example"`},
	}
	for _, c := range cases {
		if got := mustValidate(t, c.src).String(); got != c.want {
			t.Errorf("Validate(%q).String() = %s, want %s", c.src, got, c.want)
		}
	}
}

// TestValidateUnknownField — GQL_E_FIELD with a did-you-mean hint.
func TestValidateUnknownField(t *testing.T) {
	n, _ := parser.Parse(`prot == 1`)
	_, err := Validate(n)
	if err == nil {
		t.Fatal("unknown field must fail")
	}
	var ve *Error
	if !errors.As(err, &ve) || ve.Code != CodeField {
		t.Fatalf("want GQL_E_FIELD, got %v", err)
	}
	if !strings.Contains(ve.Next, `did you mean the field "port"?`) {
		t.Errorf("hint = %q, want port suggestion", ve.Next)
	}
	if ve.Line != 1 || ve.Col != 1 {
		t.Errorf("position = %d:%d, want 1:1", ve.Line, ve.Col)
	}

	// A distant name gets no hint but still the registry pointer.
	n, _ = parser.Parse(`zzzzzz == 1`)
	_, err = Validate(n)
	var ve2 *Error
	if !errors.As(err, &ve2) || ve2.Code != CodeField {
		t.Fatalf("want GQL_E_FIELD, got %v", err)
	}
	if strings.Contains(ve2.Next, "did you mean") {
		t.Errorf("distant name must not hint: %q", ve2.Next)
	}
}

// TestValidateStrictTyping — quoted integers coerce on INT fields
// (compat layer: port=="443" validates as 443), non-numeric quoted
// strings stay GQL_E_TYPE; a string field rejects a bare integer.
func TestValidateStrictTyping(t *testing.T) {
	// Compat coercion: a quoted integer on an INT field parses in place.
	n, _ := parser.Parse(`port == "443"`)
	_, err := Validate(n)
	if err != nil {
		t.Fatalf("quoted integer must coerce, got %v", err)
	}
	cmps := comparisonsByField(n, "port")
	if len(cmps) != 1 {
		t.Fatalf("got %d port comparisons, want 1", len(cmps))
	}
	iv, ok := cmps[0].Value.(*ast.IntValue)
	if !ok || iv.N != 443 {
		t.Fatalf("coerced value = %#v, want IntValue(443)", cmps[0].Value)
	}

	// IN lists coerce their quoted-integer elements too.
	n, _ = parser.Parse(`port IN (80, "443")`)
	if _, err = Validate(n); err != nil {
		t.Fatalf("IN list coercion failed: %v", err)
	}
	cmps = comparisonsByField(n, "port")
	if len(cmps) != 1 {
		t.Fatalf("got %d port comparisons, want 1", len(cmps))
	}
	lv, ok := cmps[0].Value.(*ast.ListValue)
	if !ok || len(lv.Items) != 2 {
		t.Fatalf("list = %#v, want 2 coerced items", cmps[0].Value)
	}
	for i, item := range lv.Items {
		if _, ok := item.(*ast.IntValue); !ok {
			t.Errorf("item[%d] = %#v, want coerced IntValue", i, item)
		}
	}

	// Non-numeric quoted strings on an INT field stay strict.
	n, _ = parser.Parse(`port == "https"`)
	_, err = Validate(n)
	var ve *Error
	if !errors.As(err, &ve) || ve.Code != CodeType {
		t.Fatalf("want GQL_E_TYPE for non-numeric quoted value, got %v", err)
	}

	n, _ = parser.Parse(`country == 840`)
	_, err = Validate(n)
	if !errors.As(err, &ve) || ve.Code != CodeType {
		t.Fatalf("want GQL_E_TYPE for int-on-string, got %v", err)
	}
	if !strings.Contains(ve.Next, `geo_country == "840"`) {
		t.Errorf("hint = %q, want the quoted rewrite", ve.Next)
	}

	// Quoted integers inside IN lists still type-check the whole list:
	// a non-numeric item is an error even next to a valid one.
	n, _ = parser.Parse(`port IN (80, "abc")`)
	_, err = Validate(n)
	if !errors.As(err, &ve) || ve.Code != CodeType {
		t.Fatalf("want GQL_E_TYPE inside IN list, got %v", err)
	}

	// Timestamps must be RFC 3339.
	n, _ = parser.Parse(`first_seen > "yesterday"`)
	_, err = Validate(n)
	if !errors.As(err, &ve) || ve.Code != CodeType {
		t.Fatalf("want GQL_E_TYPE for bad timestamp, got %v", err)
	}

	// Booleans accept only the two literals.
	n, _ = parser.Parse(`stale == "maybe"`)
	_, err = Validate(n)
	if !errors.As(err, &ve) || ve.Code != CodeType {
		t.Fatalf("want GQL_E_TYPE for bad boolean, got %v", err)
	}
}

// TestValidateOperatorCompatibility — the op/field matrix.
func TestValidateOperatorCompatibility(t *testing.T) {
	// IN_CIDR only on ip.
	n, _ := parser.Parse(`port IN_CIDR "198.51.100.0/24"`)
	_, err := Validate(n)
	var ve *Error
	if !errors.As(err, &ve) || ve.Code != CodeOp {
		t.Fatalf("want GQL_E_OP, got %v", err)
	}
	if !strings.Contains(ve.Why, "port accepts:") {
		t.Errorf("why = %q, want the accepted-ops list", ve.Why)
	}

	// CONTAINS not on transport.
	n, _ = parser.Parse(`transport CONTAINS "tcp"`)
	_, err = Validate(n)
	if !errors.As(err, &ve) || ve.Code != CodeOp {
		t.Fatalf("want GQL_E_OP for transport CONTAINS, got %v", err)
	}

	// Range ops not on string fields.
	n, _ = parser.Parse(`country > "US"`)
	_, err = Validate(n)
	if !errors.As(err, &ve) || ve.Code != CodeOp {
		t.Fatalf("want GQL_E_OP for country >, got %v", err)
	}

	// CONTAINS IS on technologies; eq is not.
	n, _ = parser.Parse(`technologies == "nginx"`)
	_, err = Validate(n)
	if !errors.As(err, &ve) || ve.Code != CodeOp {
		t.Fatalf("want GQL_E_OP for technologies ==, got %v", err)
	}
}

// TestValidateAllGate — ALL parses but the validator rejects it with
// the version code and the ANY rewrite suggestion.
func TestValidateAllGate(t *testing.T) {
	n, _ := parser.Parse(`ALL(host){ port == 443 }`)
	_, err := Validate(n)
	var ve *Error
	if !errors.As(err, &ve) || ve.Code != CodeUnsupported {
		t.Fatalf("want GQL_E_NOT_SUPPORTED_IN_VERSION, got %v", err)
	}
	if !strings.Contains(ve.Next, "ANY(host){ port == 443 }") {
		t.Errorf("next = %q, want the ANY rewrite", ve.Next)
	}

	// ANY passes.
	mustValidate(t, `ANY(host){ port == 443 }`)
}

// TestValidateScope — unknown quantifier scopes are GQL_E_SCOPE.
func TestValidateScope(t *testing.T) {
	n, _ := parser.Parse(`ANY(service_row){ port == 443 }`)
	_, err := Validate(n)
	var ve *Error
	if !errors.As(err, &ve) || ve.Code != CodeScope {
		t.Fatalf("want GQL_E_SCOPE, got %v", err)
	}
	if !strings.Contains(ve.Next, "ANY(host)") {
		t.Errorf("next = %q, want the scope suggestion", ve.Next)
	}
}

// TestValidateNested — errors deep in the tree report the offending
// node's position.
func TestValidateNested(t *testing.T) {
	n, _ := parser.Parse(`port == 443 && (title CONTAINS "x" || bogus == 1)`)
	_, err := Validate(n)
	var ve *Error
	if !errors.As(err, &ve) || ve.Code != CodeField {
		t.Fatalf("want GQL_E_FIELD, got %v", err)
	}
	if ve.Line != 1 || ve.Col != 39 {
		t.Errorf("position = %d:%d, want 1:39 (the bogus field)", ve.Line, ve.Col)
	}
}

// TestFieldNames — the registry exposes sorted names covering the
// canonical paths and aliases.
func TestFieldNames(t *testing.T) {
	names := FieldNames()
	if len(names) == 0 {
		t.Fatal("registry is empty")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Fatalf("FieldNames not sorted: %v", names)
		}
	}
	for _, want := range []string{"port", "ip", "title", "country", "http.title", "geo_country"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("registry missing %q", want)
		}
	}
}
