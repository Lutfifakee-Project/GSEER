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

// Package parser is the recursive-descent GQL parser (SPEC-04 §3 EBNF).
//
// M6 (landed): the grammar-complete parser.  Precedence `!` > `&&` >
// `||` (parseOr → parseAnd → parseNot → parsePrimary); sugar (`=` →
// `==`, AND/OR/NOT in any casing → the symbolic nodes); `field!="value"`
// as its own OpNeq comparison; IN lists capped at 256 elements; the
// quantified blocks ANY(f){ … } (accepted) and ALL(f){ … } (parses —
// the M7 validator rejects it in v0.0.1); the functional exists(field)
// form AND the bare-field presence test; positions from the source
// tokens on every node.  Parse-time K-13 enforcement: node count ≤
// 1024, depth ≤ 32 (constants at the top of parser.go — the normative
// spec values are not vendored in-repo; these bounds are the tuning
// point).  Grammar failures abort with GQL_E_PARSE at the offending
// token plus a did-you-mean keyword hint when the text is within edit
// distance 1 of a keyword (errors.go); the error points at the
// misspelled FIELD when the field is the misspelling.
//
// Consumes internal/gql/lexer, produces internal/gql/ast; never imports
// any collector or egress package (ADR-0010; AC-3.1).
package parser
