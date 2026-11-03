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

package search

import (
	"errors"
	"strings"
)

// errors.go — the SearchService error taxonomy (SPEC-03 §7 style).
//
// Every error carries a stable code, a plain Detail, an optional Why,
// and an actionable Next.  Surfaces map the codes to exit codes (CLI)
// and HTTP statuses (API) — the mapping tables live with the surfaces.

// QueryError is a user-query failure (bad GQL, bad options).  The CLI
// maps it to ExitInvalidInput (2); the API to 400.
type QueryError struct {
	Code   string
	Detail string
	Why    string
	Next   string
}

func (e *QueryError) Error() string {
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

// TimeoutError is a query that exceeded its statement_timeout.  The
// transaction was rolled back; the timeout applied only inside it.
// CLI → ExitUnavailable (4); API → 503 with API_E_QUERY_TIMEOUT.
type TimeoutError struct{ Err error }

func (e *TimeoutError) Error() string {
	return "E_QUERY_TIMEOUT: the query exceeded its time budget and was canceled\n" +
		"Why: statement_timeout is applied per query inside the search transaction\n" +
		"Try: narrow the query (add a field predicate) or raise --timeout (cap 30s)"
}
func (e *TimeoutError) Unwrap() error { return e.Err }

// UnavailableError is a search that could not reach the database (the
// pool is down, the connection broke).  CLI → ExitUnavailable (4) with
// a doctor pointer; API → 503 with API_E_DB_UNAVAILABLE.
type UnavailableError struct{ Err error }

func (e *UnavailableError) Error() string {
	return "E_DB_UNAVAILABLE: the search index is unreachable\n" +
		"Why: " + e.Err.Error() + "\n" +
		"Try: check the database is running and reachable — gseer doctor"
}
func (e *UnavailableError) Unwrap() error { return e.Err }

// AsTimeout / AsUnavailable are the classification helpers surfaces use.
func AsTimeout(err error) bool {
	var t *TimeoutError
	return errors.As(err, &t)
}

func AsUnavailable(err error) bool {
	var u *UnavailableError
	return errors.As(err, &u)
}

// AsQuery extracts a *QueryError (surfaces render Code/Detail/Next).
func AsQuery(err error) (*QueryError, bool) {
	var q *QueryError
	ok := errors.As(err, &q)
	return q, ok
}
