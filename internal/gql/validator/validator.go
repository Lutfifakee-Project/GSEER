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

// Package validator is the GQL semantic validator (SPEC-04 §5, §7).
//
// M7 (landed): the field registry over the search_index contract
// (migration 0003 defines the planneable columns — the registry is
// derived from that schema, with dotted canonical paths and the
// user-facing aliases), alias rewrite to canonical paths in place,
// strict typing with one compat-mode coercion (a quoted integer on an
// INT field parses in place; anything else is rejected with a concrete
// hint), operator/field compatibility (IN_CIDR only on ip, IN lists on
// listable fields, range ops only on ordered fields), scope/binding
// validation for quantified blocks, the v0.0.1 feature gate (ALL →
// GQL_E_NOT_SUPPORTED_IN_VERSION with the !ANY suggestion), and the
// error codes GQL_E_FIELD (unknown field + did-you-mean), GQL_E_TYPE,
// GQL_E_OP, GQL_E_SCOPE, GQL_E_UNSUPPORTED.
//
// The exact registry membership is the search_index column set plus
// its aliases; if SPEC-04 §7's normative 37-entry list differs, the
// registry table below is the single point to reconcile.
//
// The validator never imports any collector, egress, or P2 package
// (ADR-0010; AC-3.1).
package validator

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/gql/ast"
)

// FieldType classifies a registry field for strict typing.
type FieldType int

// Field types (the value grammar each field accepts).
const (
	TypeString FieldType = iota // string values ("…"); no ranges
	TypeInt                     // integer values; ranges allowed
	TypeTime                    // RFC 3339 string values; ranges allowed
	TypeBool                    // "true"/"false" string values
	TypeList                    // text[] columns: CONTAINS matches elements
	TypeDate                    // date ("2006-01-02") or RFC 3339; ranges allowed
)

// String renders the type name for error messages.
func (t FieldType) String() string {
	switch t {
	case TypeInt:
		return "integer"
	case TypeTime:
		return "timestamp"
	case TypeBool:
		return "boolean"
	case TypeDate:
		return "date"
	case TypeList:
		return "list"
	default:
		return "string"
	}
}

// Field is one registry entry.
type Field struct {
	Canonical string    // dotted canonical path (planner target)
	Aliases   []string  // user-facing spellings (lowercase)
	Type      FieldType // value grammar
	Ops       ast.OpSet // operators this field accepts

	// RewriteTo/RewriteOp implement the compatibility rewrites
	// (after/before/cert.not_after.*): the comparison is re-targeted to
	// the canonical field with the direction-carrying operator before
	// planning.  An empty RewriteTo is a normal registry entry.
	RewriteTo string
	RewriteOp ast.Op
}

// Operator-set composites the registry reads as a table (SPEC-04 §7
// per-field compatibility, over the search_index column contract).
var (
	opEqNeq   = ast.OpSetEq | ast.OpSetNeq
	opEqNeqIn = opEqNeq | ast.OpSetIn
	opStrText = opEqNeqIn | ast.OpSetContains | ast.OpSetMatch
	opOrdered = opEqNeqIn | ast.OpSetLt | ast.OpSetLe | ast.OpSetGt | ast.OpSetGe
)

// fields is the registry: canonical path → metadata.  Derived from the
// search_index union view (migration 0003) — see the package comment.
var fields = map[string]*Field{
	"ip":                  {Canonical: "ip", Type: TypeString, Ops: opEqNeqIn | ast.OpSetInCidr | ast.OpSetMatch},
	"asset_value":         {Canonical: "asset_value", Type: TypeString, Ops: opStrText},
	"asset_kind":          {Canonical: "asset_kind", Type: TypeString, Ops: opEqNeqIn},
	"domain":              {Canonical: "domain", Type: TypeString, Ops: opStrText | ast.OpSetInDomain},
	"hostname":            {Canonical: "hostname", Type: TypeString, Ops: opStrText | ast.OpSetInDomain},
	"asn":                 {Canonical: "asn", Type: TypeInt, Ops: opOrdered},
	"asn_org":             {Canonical: "asn_org", Aliases: []string{"org"}, Type: TypeString, Ops: opStrText},
	"geo_country":         {Canonical: "geo_country", Aliases: []string{"country"}, Type: TypeString, Ops: opEqNeqIn},
	"geo_region":          {Canonical: "geo_region", Aliases: []string{"region"}, Type: TypeString, Ops: opEqNeqIn},
	"geo_city":            {Canonical: "geo_city", Aliases: []string{"city"}, Type: TypeString, Ops: opStrText},
	"port":                {Canonical: "port", Type: TypeInt, Ops: opOrdered},
	"transport":           {Canonical: "transport", Aliases: []string{"base_protocol"}, Type: TypeString, Ops: opEqNeqIn},
	"protocol":            {Canonical: "protocol", Type: TypeString, Ops: opEqNeqIn},
	"name":                {Canonical: "name", Type: TypeString, Ops: opStrText},
	"product":             {Canonical: "product", Type: TypeString, Ops: opStrText},
	"version":             {Canonical: "version", Type: TypeString, Ops: opStrText},
	"http.status":         {Canonical: "http.status", Aliases: []string{"status", "status_code"}, Type: TypeInt, Ops: opOrdered},
	"http.title":          {Canonical: "http.title", Aliases: []string{"title"}, Type: TypeString, Ops: opStrText},
	"http.server":         {Canonical: "http.server", Aliases: []string{"server"}, Type: TypeString, Ops: opStrText},
	"tls.version":         {Canonical: "tls.version", Aliases: []string{"tls_version"}, Type: TypeString, Ops: opEqNeqIn},
	"tls.cert_subject_cn": {Canonical: "tls.cert_subject_cn", Aliases: []string{"cert_subject_cn", "cert_subject", "cert.subject.cn"}, Type: TypeString, Ops: opStrText},
	"tls.cert_issuer_cn":  {Canonical: "tls.cert_issuer_cn", Aliases: []string{"cert_issuer_cn", "cert_issuer", "cert.issuer.cn"}, Type: TypeString, Ops: opStrText},
	"tls.cert_expiry":     {Canonical: "tls.cert_expiry", Aliases: []string{"cert_expiry"}, Type: TypeTime, Ops: opOrdered},
	"technologies":        {Canonical: "technologies", Aliases: []string{"tech", "fingerprint"}, Type: TypeList, Ops: ast.OpSetContains},
	"sources":             {Canonical: "sources", Type: TypeList, Ops: ast.OpSetContains},
	"first_seen":          {Canonical: "first_seen", Type: TypeTime, Ops: opOrdered},
	"last_seen":           {Canonical: "last_seen", Type: TypeTime, Ops: opOrdered},
	"stale":               {Canonical: "stale", Type: TypeBool, Ops: opEqNeq},
	// Derived boolean fields (compatibility syntax; the planner lowers
	// them to predicates over the underlying columns - no new column).
	"is_domain":       {Canonical: "is_domain", Type: TypeBool, Ops: opEqNeq},
	"is_ipv6":         {Canonical: "is_ipv6", Type: TypeBool, Ops: opEqNeq},
	"cert.is_expired": {Canonical: "cert.is_expired", Type: TypeBool, Ops: opEqNeq},
	// Derived string field: the user alias `host` matches the hostname
	// OR the ip column (compat engines accept both a domain name and
	// an address in `host`); the planner lowers it to an OR over the
	// two underlying columns - no new column.
	"host_or_ip": {Canonical: "host_or_ip", Aliases: []string{"host"}, Type: TypeString, Ops: opStrText},

	// Compatibility rewrites: the user-written name carries the
	// direction (after -> >=, before -> <=) over the canonical time field.
	"after":                 {Canonical: "after", Type: TypeDate, Ops: opOrdered, RewriteTo: "last_seen", RewriteOp: ast.OpGe},
	"before":                {Canonical: "before", Type: TypeDate, Ops: opOrdered, RewriteTo: "last_seen", RewriteOp: ast.OpLe},
	"cert.not_after.after":  {Canonical: "cert.not_after.after", Type: TypeDate, Ops: opOrdered, RewriteTo: "tls.cert_expiry", RewriteOp: ast.OpGe},
	"cert.not_after.before": {Canonical: "cert.not_after.before", Type: TypeDate, Ops: opOrdered, RewriteTo: "tls.cert_expiry", RewriteOp: ast.OpLe},
}

// aliases maps every alias (and every canonical path itself) to the
// canonical key, built once at init.
var aliases = buildAliases()

func buildAliases() map[string]string {
	m := make(map[string]string, len(fields)*2)
	for key, f := range fields {
		m[strings.ToLower(key)] = key
		for _, a := range f.Aliases {
			m[strings.ToLower(a)] = key
		}
	}
	return m
}

// FieldNames returns the registry's user-facing names (canonical paths
// plus aliases), sorted — used by did-you-mean and by help surfaces.
func FieldNames() []string {
	names := make([]string, 0, len(aliases))
	for name := range aliases {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// lookupField resolves a user-written field name to its registry entry.
// The second result is the canonical key ("", when unknown).
func lookupField(name string) (*Field, string) {
	key, ok := aliases[strings.ToLower(name)]
	if !ok {
		return nil, ""
	}
	return fields[key], key
}

// knownScopes are the quantifier relations v0.0.1 accepts: the
// service-granular search model quantifies over the services of a host
// (search_index row) — ANY(host), or over the service row itself
// (ANY(service)).
var knownScopes = map[string]bool{
	"host":    true,
	"service": true,
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// Validate walks the tree, rewriting field aliases to canonical paths in
// place and enforcing the semantic contract.  It returns the SAME tree
// (rewritten) on success.  The first semantic error aborts.
// FreeTextFields is the fixed field list the bare-keyword form
// ("wordpress") searches: the stored TEXT surfaces of the index that
// answer the question "where does this word appear?" - identity,
// HTTP metadata, product/name, organization and certificate names.
// Deliberately EXCLUDED: body/header/icon/js content (GSEER is
// Search-not-Scan: it indexes facts, not fetched page content) and
// ip (exact-value semantics; use ip *= "10.0.0.*" for patterns).
var FreeTextFields = []string{
	"asset_value",
	"hostname",
	"domain",
	"http.title",
	"http.server",
	"product",
	"name",
	"asn_org",
	"tls.cert_subject_cn",
	"tls.cert_issuer_cn",
}

// expandFreeText rewrites every ast.FreeText node into CONTAINS
// comparisons over FreeTextFields: one OR-chain per term (the term
// appears in ANY of the text fields), AND across terms (all terms
// must appear somewhere).  It runs before the semantic walk so the
// planner only ever sees comparisons.
func expandFreeText(n ast.Node) ast.Node {
	switch t := n.(type) {
	case *ast.Or:
		t.L = expandFreeText(t.L)
		t.R = expandFreeText(t.R)
		return t
	case *ast.And:
		t.L = expandFreeText(t.L)
		t.R = expandFreeText(t.R)
		return t
	case *ast.Not:
		t.X = expandFreeText(t.X)
		return t
	case *ast.Quantified:
		t.Body = expandFreeText(t.Body)
		return t
	case *ast.FreeText:
		return freeTextTree(t)
	default:
		return n
	}
}

// freeTextTree builds the AND-of-ORs comparison tree for one
// FreeText node.  Every leaf keeps the FreeText node's position so
// error messages and Explain still point at the user's input.
func freeTextTree(ft *ast.FreeText) ast.Node {
	line, col := ft.Pos()
	var out ast.Node
	for i, term := range ft.Terms {
		var termNode ast.Node
		for j, field := range FreeTextFields {
			leaf := &ast.Comparison{
				Field: field, Op: ast.OpContains,
				Value: &ast.StrValue{S: term},
				Line:  line, Col: col,
			}
			if j == 0 {
				termNode = leaf
			} else {
				termNode = &ast.Or{L: termNode, R: leaf, Line: line, Col: col}
			}
		}
		if i == 0 {
			out = termNode
		} else {
			out = &ast.And{L: out, R: termNode, Line: line, Col: col}
		}
	}
	return out
}

func Validate(n ast.Node) (ast.Node, error) {
	n = expandFreeText(n)
	v := &validator{}
	if err := v.visit(n); err != nil {
		return nil, err
	}
	return n, nil
}

type validator struct{}

// visit dispatches by node kind.
func (v *validator) visit(n ast.Node) error {
	switch t := n.(type) {
	case *ast.Or:
		return v.visitBool(t.L, t.R)
	case *ast.And:
		return v.visitBool(t.L, t.R)
	case *ast.Not:
		return v.visit(t.X)
	case *ast.Comparison:
		return v.visitComparison(t)
	case *ast.Exists:
		return v.visitFieldRef(&t.Field, n)
	case *ast.Quantified:
		return v.visitQuantified(t)
	}
	return nil
}

func (v *validator) visitBool(l, r ast.Node) error {
	if err := v.visit(l); err != nil {
		return err
	}
	return v.visit(r)
}

// visitFieldRef resolves and rewrites one field reference.
func (v *validator) visitFieldRef(field *string, at ast.Node) error {
	f, key := lookupField(*field)
	if f == nil {
		return unknownField(*field, at)
	}
	*field = key // alias rewrite to the canonical path, in place
	return nil
}

// visitComparison enforces field existence, operator compatibility, and
// strict typing.
func (v *validator) visitComparison(c *ast.Comparison) error {
	f, key := lookupField(c.Field)
	if f == nil {
		return unknownField(c.Field, c)
	}
	line, col := c.Pos()

	// Compatibility rewrite (after/before/cert.not_after.*): the
	// source entry validates the user-written value shape (a plain
	// date), then the comparison retargets to the canonical field
	// with the direction-carrying operator.
	typeChecked := false
	if f.RewriteTo != "" {
		if err := checkType(f, key, c); err != nil {
			return err
		}
		c.Op = f.RewriteOp
		key = f.RewriteTo
		f = fields[key]
		typeChecked = true
	}

	if !f.Ops.Has(c.Op) {
		return &Error{
			Code:   CodeOp,
			Detail: fmt.Sprintf("operator %s is not valid for %s at line %d, column %d", c.Op, key, line, col),
			Why:    fmt.Sprintf("%s accepts: %s", key, f.Ops.String()),
			Next:   opSuggestion(f, c),
			Line:   line, Col: col,
		}
	}
	if !typeChecked {
		if err := checkType(f, key, c); err != nil {
			return err
		}
	}
	c.Field = key // alias rewrite, in place
	return nil
}

// checkType enforces strict typing per the field's type: an INT field
// rejects a quoted value (GQL_E_TYPE + the unquoted rewrite), a STRING
// field rejects a bare integer when the field is not INT, timestamps
// check their RFC 3339 shape, lists require CONTAINS (already op-gated).
func checkType(f *Field, key string, c *ast.Comparison) error {
	line, col := c.Pos()
	switch v := c.Value.(type) {
	case *ast.IntValue:
		switch f.Type {
		case TypeInt:
			return nil
		case TypeString, TypeBool, TypeList:
			return &Error{
				Code: CodeType,
				Detail: fmt.Sprintf("field %s takes a %s value, got the integer %d at line %d, column %d",
					key, f.Type, v.N, line, col),
				Why:  "GQL typing is strict: quoted values are strings, bare numbers are integers",
				Next: fmt.Sprintf("try: %s %s \"%d\"", key, c.Op, v.N),
				Line: line, Col: col,
			}
		case TypeTime:
			return &Error{
				Code: CodeType,
				Detail: fmt.Sprintf("field %s takes an RFC 3339 timestamp, got the integer %d at line %d, column %d",
					key, v.N, line, col),
				Why:  "timestamps are quoted strings in GQL",
				Next: fmt.Sprintf("try: %s %s \"2026-01-01T00:00:00Z\"", key, c.Op),
				Line: line, Col: col,
			}
		}
	case *ast.StrValue:
		if c.Op == ast.OpMatch && v.S == "" {
			return &Error{
				Code:   CodeType,
				Detail: fmt.Sprintf("empty match pattern at line %d, column %d", line, col),
				Why:    "the wildcard pattern needs at least one character",
				Next:   "try: hostname *= \"*.example.\"  (* = any run, ? = one char)",
				Line:   line, Col: col,
			}
		}
		switch f.Type {
		case TypeInt:
			// Compat layer: a quoted value that parses as an
			// integer is coerced in place (compat engines quote
			// every value, so port=="443" must behave like
			// port==443).  Non-numeric quoted strings stay a
			// strict-typing error.
			if n, cerr := strconv.ParseInt(v.S, 10, 64); cerr == nil {
				c.Value = &ast.IntValue{N: n}
				return nil
			}
			return &Error{
				Code: CodeType,
				Detail: fmt.Sprintf("field %s takes an integer, got the string %s at line %d, column %d",
					key, quoteForMsg(v.S), line, col),
				Why:  "the quoted value is not a valid integer for this field",
				Next: fmt.Sprintf("try: %s %s 443   (or %s %s \"80,443\" with IN)", key, c.Op, key, c.Op),
				Line: line, Col: col,
			}
		case TypeTime:
			if _, err := parseRFC3339(v.S); err != nil {
				return &Error{
					Code: CodeType,
					Detail: fmt.Sprintf("field %s takes an RFC 3339 timestamp, got %s at line %d, column %d",
						key, quoteForMsg(v.S), line, col),
					Why:  "the value is not an RFC 3339 timestamp",
					Next: fmt.Sprintf("try: %s %s \"2026-01-01T00:00:00Z\"", key, c.Op),
					Line: line, Col: col,
				}
			}
		case TypeDate:
			if !isDateValue(v.S) {
				return &Error{
					Code: CodeType,
					Detail: fmt.Sprintf("field %s takes a date (\"2006-01-02\") or RFC 3339 timestamp, got %s at line %d, column %d",
						key, quoteForMsg(v.S), line, col),
					Why:  "the value is not a date",
					Next: fmt.Sprintf("try: %s %s \"2026-01-31\"", key, c.Op),
					Line: line, Col: col,
				}
			}
		case TypeBool:
			if v.S != "true" && v.S != "false" {
				return &Error{
					Code: CodeType,
					Detail: fmt.Sprintf("field %s takes \"true\" or \"false\", got %s at line %d, column %d",
						key, quoteForMsg(v.S), line, col),
					Why:  "boolean fields accept only the two quoted literals",
					Next: fmt.Sprintf("try: %s %s \"true\"", key, c.Op),
					Line: line, Col: col,
				}
			}
		}
	case *ast.ListValue:
		// Element typing mirrors the field type.  Quoted integers
		// coerce in place first, so `port IN (80, "443")` works like
		// the compat layer's single-value coercion.
		if f.Type == TypeInt {
			for i, item := range v.Items {
				if sv, ok := item.(*ast.StrValue); ok {
					if n, cerr := strconv.ParseInt(sv.S, 10, 64); cerr == nil {
						v.Items[i] = &ast.IntValue{N: n}
					}
				}
			}
		}
		for _, item := range v.Items {
			sub := &ast.Comparison{Field: c.Field, Op: c.Op, Value: item, Line: line, Col: col}
			if err := checkType(f, key, sub); err != nil {
				return err
			}
		}
	}
	return nil
}

// visitQuantified validates the scope name and the body.
func (v *validator) visitQuantified(q *ast.Quantified) error {
	line, col := q.Pos()
	if !knownScopes[strings.ToLower(q.Field)] {
		return &Error{
			Code:   CodeScope,
			Detail: fmt.Sprintf("unknown quantifier scope %q at line %d, column %d", q.Field, line, col),
			Why:    "v0.0.1 quantifies over host (the asset and its services) or service (the search row)",
			Next:   "try: ANY(host){ ... }   or   ANY(service){ ... }",
			Line:   line, Col: col,
		}
	}
	if q.Q == ast.QuantAll {
		return &Error{
			Code:   CodeUnsupported,
			Detail: fmt.Sprintf("ALL(...) is not supported in this version at line %d, column %d", line, col),
			Why:    "v0.0.1 ships the ANY quantifier only (ADR-0014; the ALL semantics need the v0.1.0 asset-granular mode)",
			Next:   "rewrite with ANY: ANY(" + q.Field + "){ " + q.Body.String() + " } — and negate if you meant 'no service matches'",
			Line:   line, Col: col,
		}
	}
	if err := v.visit(q.Body); err != nil {
		return err
	}
	// Scope/binding: fields the scope does not bind are a semantic error
	// for service scope when the body references host-only fields?  No —
	// the search_index row flattens both; v0.0.1 keeps binding permissive
	// and lets the planner decide projection.  Nothing further to check.
	return nil
}

// unknownField builds the GQL_E_FIELD error with a did-you-mean hint
// over the registry names.
func unknownField(name string, at ast.Node) error {
	line, col := at.Pos()
	next := "see the field registry: gseer search --help"
	if hint := didYouMeanField(name); hint != "" {
		next = hint + "; " + next
	}
	return &Error{
		Code:   CodeField,
		Detail: fmt.Sprintf("unknown field %q at line %d, column %d", name, line, col),
		Why:    "GQL validates field names against the search_index registry (SPEC-04 §7)",
		Next:   next,
		Line:   line, Col: col,
	}
}

// didYouMeanField suggests the closest registry name within edit
// distance 2 (field names are longer than keywords).
func didYouMeanField(name string) string {
	word := strings.ToLower(name)
	best, bestDist := "", 3
	for _, cand := range FieldNames() {
		if d := editDistance(word, cand); d < bestDist {
			best, bestDist = cand, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("did you mean the field %q?", best)
}

// opSuggestion renders the closest supported operator for a field.
func opSuggestion(f *Field, c *ast.Comparison) string {
	for _, op := range []ast.Op{ast.OpEq, ast.OpContains, ast.OpIn} {
		if f.Ops.Has(op) {
			return fmt.Sprintf("try: %s %s %s", f.Canonical, op, c.Value)
		}
	}
	return "check the field's supported operators"
}

// isDateValue accepts an RFC 3339 timestamp or a plain calendar date
// ("2006-01-02") — the compatibility time fields (after/before/
// cert.not_after.*) are written as dates in common usage.
func isDateValue(s string) bool {
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return true
	}
	_, err := parseRFC3339(s)
	return err == nil
}

// quoteForMsg renders s for an error message (truncate + quote).
func quoteForMsg(s string) string {
	if len(s) > 32 {
		s = s[:32] + "…"
	}
	return "\"" + s + "\""
}
