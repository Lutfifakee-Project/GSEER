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

package lexer

import (
	"fmt"
	"strings"
)

// errors.go — the lexical error type (SPEC-03 §7 style guide).
//
// GQL lexical failures abort with the stable code GQL_E_LEX and carry
// the 1-based source position so the CLI can point at the exact
// character.  The text follows the project-wide "what failed → why →
// Try:" rendering, matching config.Error and postgres.DBError so every
// gseer error surface reads the same way.

// CodeLex is the stable GQL lexical-error code (SPEC-04 §2).
const CodeLex = "GQL_E_LEX"

// Error is a GQL lexical error with its source position.
type Error struct {
	Detail string // what failed, including "line L, column C"
	Why    string // likely why
	Next   string // concrete Try: hint
	Line   int    // 1-based line the error points at
	Col    int    // 1-based (rune) column the error points at
}

// Code is the stable error code (uniform access with the parser's and
// validator's error types from M6/M7 onward).
func (e *Error) Code() string { return CodeLex }

// Error renders per the SPEC-03 §7 style guide.
func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", CodeLex, e.Detail)
	if e.Why != "" {
		fmt.Fprintf(&b, "\nWhy: %s", e.Why)
	}
	if e.Next != "" {
		fmt.Fprintf(&b, "\nTry: %s", e.Next)
	}
	return b.String()
}

// newLexError builds a GQL_E_LEX error at an explicit 1-based position.
func newLexError(line, col int, detail, why, next string) *Error {
	return &Error{Detail: detail, Why: why, Next: next, Line: line, Col: col}
}
