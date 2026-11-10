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

// compat_test.go — planner coverage for the compatibility layer:
// wildcard ILIKE lowering with literal escaping, empty-value
// semantics, derived boolean predicates, the defensive free-text
// lowering, and the cross-package field-list sync pin.
package planner

import (
	"strings"
	"testing"

	"github.com/Lutfifakee-Project/GSEER/internal/gql/ast"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/validator"
)

func TestCompileWildcardMatch(t *testing.T) {
	p := compile(t, `http.title *= "Port?l"`, Options{})
	if !strings.Contains(p.SQL, "ILIKE $1 ESCAPE '\\'") {
		t.Errorf("expected ILIKE fragment, got:\n%s", p.SQL)
	}
	// * → %, ? → _: "Port?l" becomes "Port_l" as the bound parameter.
	if got, ok := p.Args[0].(string); !ok || got != "Port_l" {
		t.Errorf("pattern arg = %v, want Port_l", p.Args[0])
	}
	assertNoLiteral(t, p.SQL, "Port", "?", "*")
}

func TestCompileWildcardMatchEscapesLikeMetacharacters(t *testing.T) {
	p := compile(t, `hostname *= "100%_reliabl*"`, Options{})
	got, ok := p.Args[0].(string)
	if !ok {
		t.Fatalf("pattern arg type = %T", p.Args[0])
	}
	// SQL metacharacters must be escaped so they match literally; the
	// user's * becomes the wildcard.
	if got != `100\%\_reliabl%` {
		t.Errorf("pattern arg = %q, want 100\\%%\\_reliabl%%", got)
	}
}

func TestCompileWildcardMatchIPCast(t *testing.T) {
	p := compile(t, `ip *= "203.0.113.*"`, Options{})
	// The ip column is inet-typed; the match must go through host(inet) text.
	if !strings.Contains(p.SQL, "host(search_index.\"ip\") ILIKE $1 ESCAPE '\\'") {
		t.Errorf("missing host(ip) text cast, got:\n%s", p.SQL)
	}
	if got, ok := p.Args[0].(string); !ok || got != "203.0.113.%" {
		t.Errorf("pattern arg = %v, want 203.0.13x.%%", p.Args[0])
	}
}

func TestCompileHostOrIP(t *testing.T) {
	// host="203.0.113.10" must match BOTH the hostname and the ip
	// columns (compat engines accept a name or an address in host).
	p := compile(t, `host == "203.0.113.10"`, Options{})
	if !strings.Contains(p.SQL, `"hostname" = $1`) || !strings.Contains(p.SQL, `host(search_index."ip") = $2`) {
		t.Errorf("expected (hostname = $1 OR host(ip) = $2), got:\n%s", p.SQL)
	}
	if !strings.Contains(p.SQL, " OR ") {
		t.Errorf("expected an OR over hostname/ip, got:\n%s", p.SQL)
	}

	// Fuzzy host matches lower the wildcard over both columns.
	p = compile(t, `host *= "203.0.113.*"`, Options{})
	if !strings.Contains(p.SQL, "\"hostname\" ILIKE $1 ESCAPE '\\'") ||
		!strings.Contains(p.SQL, "host(search_index.\"ip\") ILIKE $2 ESCAPE '\\'") {
		t.Errorf("expected ILIKE over hostname and host(ip), got:\n%s", p.SQL)
	}
	if got, ok := p.Args[0].(string); !ok || got != "203.0.113.%" {
		t.Errorf("pattern arg = %v, want 203.0.113.%%", p.Args[0])
	}

	// IN lists fan out over both columns.
	p = compile(t, `host IN ("portal.example", "203.0.113.10")`, Options{})
	if !strings.Contains(p.SQL, `"hostname" = ANY($1::text[])`) ||
		!strings.Contains(p.SQL, `host(search_index."ip") = ANY($2::text[])`) {
		t.Errorf("expected ANY(...) over hostname and host(ip), got:\n%s", p.SQL)
	}

	// The empty value keeps the hostname presence semantics.
	p = compile(t, `host == ""`, Options{})
	if !strings.Contains(p.SQL, `"hostname" IS NULL`) {
		t.Errorf("expected hostname-empty semantics, got:\n%s", p.SQL)
	}
}

func TestCompileEmptyValueSemantics(t *testing.T) {
	eq := compile(t, `http.title == ""`, Options{})
	if !strings.Contains(eq.SQL, `"http_title" IS NULL OR search_index."http_title" = ''`) {
		t.Errorf(`"" must mean missing-or-empty, got:\n%s`, eq.SQL)
	}
	neq := compile(t, `http.title != ""`, Options{})
	if !strings.Contains(neq.SQL, `"http_title" IS NOT NULL AND search_index."http_title" <> ''`) {
		t.Errorf(`!="" must mean present-and-non-empty, got:\n%s`, neq.SQL)
	}
}

func TestCompileDerivedBooleans(t *testing.T) {
	dom := compile(t, `is_domain == "true"`, Options{})
	if !strings.Contains(dom.SQL, `"domain" IS NOT NULL AND search_index."domain" <> ''`) {
		t.Errorf("is_domain=true lowering wrong:\n%s", dom.SQL)
	}
	v4 := compile(t, `is_ipv6 == "false"`, Options{})
	if !strings.Contains(v4.SQL, `"ip"::text`) || !strings.Contains(v4.SQL, "position(':' in") {
		t.Errorf("is_ipv6 lowering must test the ::text ip for a colon:\n%s", v4.SQL)
	}
	exp := compile(t, `cert.is_expired == "true"`, Options{})
	if !strings.Contains(exp.SQL, `"cert_expiry" IS NOT NULL AND search_index."cert_expiry" < now()`) {
		t.Errorf("cert.is_expired=true lowering wrong:\n%s", exp.SQL)
	}
}

func TestCompileTimeRewritePredicates(t *testing.T) {
	p := compile(t, `after == "2026-01-01"`, Options{})
	if !strings.Contains(p.SQL, `"last_seen" >= $1`) {
		t.Errorf("after must lower to last_seen >=, got:\n%s", p.SQL)
	}
	p2 := compile(t, `cert.not_after.before == "2026-01-01"`, Options{})
	if !strings.Contains(p2.SQL, `"cert_expiry" <= $1`) {
		t.Errorf("cert.not_after.before must lower to cert_expiry <=, got:\n%s", p2.SQL)
	}
}

func TestCompileFreeTextDefensive(t *testing.T) {
	// The validator normally expands FreeText before planning; compile
	// an UNEXPANDED node to prove the planner's defensive lowering
	// stays semantically identical (OR across fields per term).
	b := &builder{}
	sql := b.compileFreeText(&ast.FreeText{Terms: []string{"wordpress"}}, "search_index")
	if got := strings.Count(sql, " ILIKE "); got != len(freeTextFieldPaths) {
		t.Errorf("defensive free-text lowered %d arms, want %d:\n%s", got, len(freeTextFieldPaths), sql)
	}
}

func TestFreeTextFieldListsMatch(t *testing.T) {
	// The planner must not import the validator, so the free-text field
	// list is mirrored.  This pin fails when the two lists drift.
	if len(freeTextFieldPaths) != len(validator.FreeTextFields) {
		t.Fatalf("planner has %d free-text fields, validator has %d",
			len(freeTextFieldPaths), len(validator.FreeTextFields))
	}
	for i, f := range validator.FreeTextFields {
		if freeTextFieldPaths[i] != f {
			t.Errorf("list drift at [%d]: planner=%q validator=%q", i, freeTextFieldPaths[i], f)
		}
		if columnName(f) == "" {
			t.Errorf("free-text field %q has no search_index column", f)
		}
	}
}
