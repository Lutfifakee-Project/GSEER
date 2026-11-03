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

package postgres

import (
	"fmt"
	"strings"
)

// errors.go — structured storage-layer errors (SPEC-03 §7 style guide).
//
// Every error carries a stable Code so scripts and CI can match on it;
// the human text follows "what failed → why → exact next command".
// Error text is built from the REDACTED DSN only (redact.go) — the raw
// DSN never reaches any error, log, or diagnostic (D-M3-10).

// Stable storage-layer error codes.
const (
	// CodeDBUnreachable — PostgreSQL is not reachable (SPEC-03 §7).
	CodeDBUnreachable = "E_DB_UNREACHABLE"

	// CodeDBVersion — the PostgreSQL major version is below the
	// supported floor (PD-8: >= 15).
	CodeDBVersion = "E_DB_VERSION"

	// CodeDBMigrationFailed — a migration failed mid-execution; its
	// transaction was rolled back and the version is marked dirty.
	CodeDBMigrationFailed = "E_DB_MIGRATION_FAILED"

	// CodeDBMigrationDirty — the schema_migrations table is in a dirty
	// state from a previously failed migration; recovery is operator-
	// driven via `gseer db migrate diagnose` + `force`.
	CodeDBMigrationDirty = "E_DB_MIGRATION_DIRTY"

	// CodeDBMigrationLockTimeout — the migration advisory lock could not
	// be acquired within the configured timeout (owner directive §1,
	// M3 authorization round).  Named per the owner's exact identifier.
	CodeDBMigrationLockTimeout = "DB_MIGRATION_LOCK_TIMEOUT"

	// CodeDBUsage — an administrative command was invoked with invalid
	// arguments (e.g. force --version beyond head).
	CodeDBUsage = "E_DB_USAGE"

	// CodeDBSuppressed — a write was refused because the record matches
	// an enabled suppression rule (migration 0005; M13).  Surfaced by
	// the DB backstop trigger (custom ERRCODE GSUP1) when a write path
	// bypasses the Go gate; the gate itself skips suppressed facts
	// cleanly and reports them via IngestStats.Suppressed.
	CodeDBSuppressed = "E_DB_SUPPRESSED"
)

// DBError is the structured storage-layer error.  It mirrors the
// config.Error shape so the CLI renders one consistent style.
type DBError struct {
	Code   string
	Detail string
	Why    string
	Next   string
}

// Error renders per the SPEC-03 §7 style guide.
func (e *DBError) Error() string {
	var b strings.Builder
	if e.Code != "" {
		fmt.Fprintf(&b, "%s: ", e.Code)
	}
	b.WriteString(e.Detail)
	if e.Why != "" {
		fmt.Fprintf(&b, "\nWhy: %s", e.Why)
	}
	if e.Next != "" {
		fmt.Fprintf(&b, "\nTry: %s", e.Next)
	}
	return b.String()
}

// newDBError builds a DBError.
func newDBError(code, detail string) *DBError {
	return &DBError{Code: code, Detail: detail}
}

// withWhy attaches the "likely why" sentence.
func (e *DBError) withWhy(why string) *DBError {
	e.Why = why
	return e
}

// withNext attaches the "exact next command".
func (e *DBError) withNext(next string) *DBError {
	e.Next = next
	return e
}
