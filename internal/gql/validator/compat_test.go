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

// compat_test.go — validator coverage for the compatibility layer:
// alias rewrites, directional time rewrites, derived booleans, the *=
// operator gate, and the free-text expansion contract.
package validator

import (
	"testing"

	"github.com/Lutfifakee-Project/GSEER/internal/gql/ast"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/parser"
)

// mustParse parses q without validating (the validator under test).
func mustParse(t *testing.T, q string) ast.Node {
	t.Helper()
	n, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("Parse(%q): %v", q, err)
	}
	return n
}

// validateOne parses a single-comparison query, validates it, and
// returns the rewritten leaf.
func validateOne(t *testing.T, q string) *ast.Comparison {
	t.Helper()
	n, err := Validate(mustParse(t, q))
	if err != nil {
		t.Fatalf("Validate(%q): %v", q, err)
	}
	c, ok := n.(*ast.Comparison)
	if !ok {
		t.Fatalf("root = %T, want *ast.Comparison", n)
	}
	return c
}

func TestCompatAliasRewrites(t *testing.T) {
	cases := []struct{ q, wantField string }{
		{`host == "portal.example"`, "host_or_ip"}, // matches hostname OR ip in the planner
		{`org == "ACME"`, "asn_org"},
		{`base_protocol == "tcp"`, "transport"},
		{`status_code == 200`, "http.status"},
		{`cert.subject.cn == "example.com"`, "tls.cert_subject_cn"},
		{`cert.issuer.cn == "R3"`, "tls.cert_issuer_cn"},
	}
	for _, tc := range cases {
		c := validateOne(t, tc.q)
		if c.Field != tc.wantField {
			t.Errorf("%s: field = %q, want %q", tc.q, c.Field, tc.wantField)
		}
	}
}

func TestCompatTimeRewrites(t *testing.T) {
	cases := []struct {
		q         string
		wantField string
		wantOp    ast.Op
	}{
		{`after == "2026-01-01"`, "last_seen", ast.OpGe},
		{`before == "2026-01-01"`, "last_seen", ast.OpLe},
		{`after == "2026-01-01T00:00:00Z"`, "last_seen", ast.OpGe},
		{`cert.not_after.after == "2026-01-01"`, "tls.cert_expiry", ast.OpGe},
		{`cert.not_after.before == "2026-01-01"`, "tls.cert_expiry", ast.OpLe},
	}
	for _, tc := range cases {
		c := validateOne(t, tc.q)
		if c.Field != tc.wantField || c.Op != tc.wantOp {
			t.Errorf("%s: rewrote to (%s %s), want (%s %s)", tc.q, c.Field, c.Op, tc.wantField, tc.wantOp)
		}
	}
}

func TestCompatTimeRewriteRejectsNonDate(t *testing.T) {
	if _, err := Validate(mustParse(t, `after == "not-a-date"`)); err == nil {
		t.Fatal("after must reject a non-date value")
	}
}

func TestCompatDerivedBooleans(t *testing.T) {
	for _, q := range []string{
		`is_domain == "true"`, `is_domain == "false"`,
		`is_ipv6 == "true"`, `is_ipv6 == "false"`,
		`cert.is_expired == "true"`, `cert.is_expired == "false"`,
	} {
		if _, err := Validate(mustParse(t, q)); err != nil {
			t.Errorf("%s: unexpected error: %v", q, err)
		}
	}
	if _, err := Validate(mustParse(t, `is_domain == "maybe"`)); err == nil {
		t.Error(`is_domain == "maybe" must be a type error`)
	}
}

func TestCompatWildcardOpGate(t *testing.T) {
	if _, err := Validate(mustParse(t, `hostname *= "*.example.*"`)); err != nil {
		t.Errorf("string field *= rejected: %v", err)
	}
	if _, err := Validate(mustParse(t, `ip *= "10.0.0.*"`)); err != nil {
		t.Errorf("ip *= rejected: %v", err)
	}
	if _, err := Validate(mustParse(t, `port *= "44*"`)); err == nil {
		t.Error("int field must reject the *= operator")
	}
	if _, err := Validate(mustParse(t, `hostname *= ""`)); err == nil {
		t.Error("empty wildcard pattern must be rejected")
	}
}

func TestCompatFreeTextFieldList(t *testing.T) {
	if len(FreeTextFields) != 10 {
		t.Errorf("FreeTextFields has %d entries, want 10", len(FreeTextFields))
	}
	seen := map[string]bool{}
	for _, f := range FreeTextFields {
		if seen[f] {
			t.Errorf("duplicate free-text field %q", f)
		}
		seen[f] = true
	}
}

// collectComparisons gathers every Comparison leaf of a tree.
func collectComparisons(n ast.Node) []*ast.Comparison {
	var out []*ast.Comparison
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		switch t := n.(type) {
		case *ast.Or:
			walk(t.L)
			walk(t.R)
		case *ast.And:
			walk(t.L)
			walk(t.R)
		case *ast.Not:
			walk(t.X)
		case *ast.Comparison:
			out = append(out, t)
		}
	}
	walk(n)
	return out
}

func TestCompatFreeTextExpansion(t *testing.T) {
	n, err := Validate(mustParse(t, `"wordpress"`))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	leaves := collectComparisons(n)
	if len(leaves) != len(FreeTextFields) {
		t.Fatalf("expansion produced %d leaves, want %d", len(leaves), len(FreeTextFields))
	}
	for _, leaf := range leaves {
		if leaf.Op != ast.OpContains {
			t.Errorf("leaf %s %s: op = %s, want CONTAINS", leaf.Field, leaf.Op, ast.OpContains)
		}
		if v, ok := leaf.Value.(*ast.StrValue); !ok || v.S != "wordpress" {
			t.Errorf("leaf %s: value = %v, want wordpress", leaf.Field, leaf.Value)
		}
	}
	// The expansion must cover the identity field: a hostname-only
	// corpus row must be reachable from a bare keyword.
	fields := map[string]bool{}
	for _, leaf := range leaves {
		fields[leaf.Field] = true
	}
	for _, want := range []string{"hostname", "asset_value", "http.title"} {
		if !fields[want] {
			t.Errorf("free-text expansion is missing field %q", want)
		}
	}
}

func TestCompatFreeTextMultiTermIsAnd(t *testing.T) {
	n, err := Validate(mustParse(t, `"apache" "login"`))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	root, ok := n.(*ast.And)
	if !ok {
		t.Fatalf("root = %T, want *ast.And (AND across terms)", n)
	}
	left := collectComparisons(root.L)
	right := collectComparisons(root.R)
	if len(left) != len(FreeTextFields) || len(right) != len(FreeTextFields) {
		t.Errorf("term chains: left=%d right=%d, want %d each", len(left), len(right), len(FreeTextFields))
	}
	if v, ok := left[0].Value.(*ast.StrValue); !ok || v.S != "apache" {
		t.Errorf("left term value = %v, want apache", left[0].Value)
	}
	if v, ok := right[0].Value.(*ast.StrValue); !ok || v.S != "login" {
		t.Errorf("right term value = %v, want login", right[0].Value)
	}
}

func TestCompatFreeTextNotConfusedWithExists(t *testing.T) {
	// A bare IDENT stays the exists-form (GQL semantics); only the
	// QUOTED literal is free text.  Pin the distinction.
	n, err := Validate(mustParse(t, `hostname`))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, ok := n.(*ast.Exists); !ok {
		t.Fatalf("bare field = %T, want *ast.Exists", n)
	}
}
