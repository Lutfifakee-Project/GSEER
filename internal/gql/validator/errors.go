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

package validator

import (
	"strings"
	"time"
)

// errors.go — the semantic error type (SPEC-03 §7 style guide).
//
// Validation failures carry one of the stable GQL_E_* codes below, the
// 1-based position, and a concrete Try: hint.  Rendering matches
// lexer.Error and parser.Error.

// Stable GQL semantic error codes (SPEC-04 §5).
const (
	CodeField       = "GQL_E_FIELD"                    // unknown field
	CodeType        = "GQL_E_TYPE"                     // strict-typing violation
	CodeOp          = "GQL_E_OP"                       // operator not valid for the field
	CodeScope       = "GQL_E_SCOPE"                    // unknown quantifier scope
	CodeUnsupported = "GQL_E_NOT_SUPPORTED_IN_VERSION" // parses, rejected in v0.0.1
)

// Error is a GQL semantic validation error with its source position.
type Error struct {
	Code   string // one of the GQL_E_* constants above
	Detail string // what failed, including "line L, column C"
	Why    string // likely why
	Next   string // concrete Try: hint
	Line   int    // 1-based line
	Col    int    // 1-based (rune) column
}

// Error renders per the SPEC-03 §7 style guide.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Code + ": " + e.Detail)
	if e.Why != "" {
		b.WriteString("\nWhy: " + e.Why)
	}
	if e.Next != "" {
		b.WriteString("\nTry: " + e.Next)
	}
	return b.String()
}

// parseRFC3339 validates the timestamp shape GQL time-typed fields
// accept (RFC 3339, per the GQL value grammar).
func parseRFC3339(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}

// editDistance computes the Levenshtein distance between a and b
// (insert/delete/substitute, all cost 1) — the did-you-mean metric for
// field names.
func editDistance(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			m := prev[j] + 1 // delete
			if cur[j-1]+1 < m {
				m = cur[j-1] + 1 // insert
			}
			if prev[j-1]+cost < m {
				m = prev[j-1] + cost // substitute
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}
