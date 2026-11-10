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
// MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package parser

import "strings"

// errors.go — the grammatical error type (SPEC-03 §7 style guide).
//
// Parse failures abort with the stable code GQL_E_PARSE and the 1-based
// position of the offending token; when the offending text is within
// edit distance 1 of a keyword, the "Try:" clause carries a
// did-you-mean hint.  Rendering matches lexer.Error and the project's
// other structured errors.

// CodeParse is the stable GQL grammar-error code (SPEC-04 §3).
const CodeParse = "GQL_E_PARSE"

// Error is a GQL parse error with its source position.
type Error struct {
	Detail string // what failed, including "line L, column C"
	Why    string // likely why
	Next   string // concrete Try: hint (may carry did-you-mean)
	Line   int    // 1-based line the error points at
	Col    int    // 1-based (rune) column the error points at
}

// Code is the stable error code (uniform access across the GQL chain).
func (e *Error) Code() string { return CodeParse }

// Error renders per the SPEC-03 §7 style guide.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(CodeParse + ": " + e.Detail)
	if e.Why != "" {
		b.WriteString("\nWhy: " + e.Why)
	}
	if e.Next != "" {
		b.WriteString("\nTry: " + e.Next)
	}
	return b.String()
}
