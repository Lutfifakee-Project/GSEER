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

package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// admin.go — storage primitives for the `gseer db migrate` commands
// (owner directive §2 of the M3 authorization round).
//
//      status     → GetMigrationStatus   (read-only)
//      diagnose   → DiagnoseMigration    (read-only)
//      force      → ForceMigrationVersion (DESTRUCTIVE, metadata-only)
//
// Invariants (enforced by tests, AC-M3-13..16):
//   - GetMigrationStatus / DiagnoseMigration issue SELECTs only.
//   - ForceMigrationVersion calls migrate.Force ONLY: it deletes and
//     re-inserts the schema_migrations row.  It never executes, rolls
//     back, or "repairs" any SQL migration, and never touches any other
//     table.
//   - No other GSEER code path calls ForceMigrationVersion (the CLI
//     db.go force command is the only caller; static analysis test).

// Migration states reported by GetMigrationStatus.
const (
	StateUninitialized = "UNINITIALIZED" // no schema_migrations table yet
	StateAtHead        = "AT_HEAD"
	StateBehind        = "BEHIND"
	StateAhead         = "AHEAD"
	StateDirty         = "DIRTY"
)

// MigrationStatus is the read-only migration state snapshot.
type MigrationStatus struct {
	State   string // one of the State* constants
	Version int64  // recorded version; -1 when uninitialized
	Dirty   bool
	Head    uint     // highest embedded migration version
	Pending []string // up-file names above the recorded version
}

// connectAdmin opens a dedicated pgx connection for the read-only
// administrative queries with a bounded connect timeout.
func connectAdmin(ctx context.Context, rawDSN string) (*pgx.Conn, error) {
	connCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(connCtx, rawDSN)
	if err != nil {
		return nil, unreachableError(err, rawDSN)
	}
	return conn, nil
}

// unreachableError builds E_DB_UNREACHABLE with the redacted DSN.
func unreachableError(err error, rawDSN string) *DBError {
	e := newDBError(CodeDBUnreachable, "cannot reach PostgreSQL.").
		withWhy(err.Error())
	if r := MustRedactDSN(rawDSN); r != "" {
		e.Detail += " Target: " + r
	}
	e.Next = "start the database (compose stack: docker compose up -d db); then verify: gseer doctor"
	return e
}

// GetMigrationStatus reads schema_migrations (if it exists) and
// classifies the state against the embedded head version.
// Read-only: SELECT queries only.
func GetMigrationStatus(ctx context.Context, rawDSN string) (*MigrationStatus, error) {
	head, err := HeadVersion()
	if err != nil {
		return nil, err
	}
	conn, err := connectAdmin(ctx, rawDSN)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(ctx) }()

	st := &MigrationStatus{Head: head, Version: -1}

	var version int64
	var dirty bool
	err = conn.QueryRow(ctx,
		`SELECT version, dirty FROM schema_migrations ORDER BY version DESC LIMIT 1`,
	).Scan(&version, &dirty)
	switch {
	case err == nil:
		st.Version = version
		st.Dirty = dirty
	case isUndefinedTable(err):
		st.State = StateUninitialized
		st.Pending = allPending(head)
		return st, nil
	default:
		return nil, unreachableError(err, rawDSN)
	}

	if dirty {
		st.State = StateDirty
		return st, nil
	}
	// The schema_migrations.version column is BIGINT NOT NULL and the
	// driver only writes migration numbers (>= 0); anything else is
	// treated as AHEAD.  Conversions go through toVersionUint which
	// proves the range to the overflow checker.
	v, ok := toVersionUint(version)
	if !ok {
		st.State = StateAhead
		return st, nil
	}
	switch {
	case v == head:
		st.State = StateAtHead
	case v < head:
		st.State = StateBehind
		st.Pending = pendingFiles(v, head)
	default:
		st.State = StateAhead
	}
	return st, nil
}

// toVersionUint converts a schema_migrations.version (BIGINT) into the
// uint domain of the migration file numbering, rejecting values that
// cannot be a real migration version (negative or beyond uint32).
func toVersionUint(v int64) (uint, bool) {
	if v < 0 || v > math.MaxUint32 {
		return 0, false
	}
	return uint(v), true
}

// isUndefinedTable reports SQLSTATE 42P01 (relation does not exist).
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UndefinedTable
}

// pendingFiles lists embedded up-files with version > from.
func pendingFiles(from uint, head uint) []string {
	names, _ := MigrationFiles()
	var out []string
	for _, n := range names {
		v := fileVersion(n)
		if v > from && v <= head {
			out = append(out, n)
		}
	}
	return out
}

// allPending lists every embedded up-file (fresh database case).
func allPending(head uint) []string {
	return pendingFiles(0, head)
}

// fileVersion parses the numeric prefix of a migration file name
// (0 when unparseable — callers only use it for ordering hints).
func fileVersion(name string) uint {
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			if i == 0 {
				return 0
			}
			v, err := strconv.ParseUint(name[:i], 10, 32)
			if err != nil {
				return 0
			}
			return uint(v)
		}
	}
	return 0
}

// Diagnosis is the read-only recovery guidance for a dirty database.
type Diagnosis struct {
	Status        *MigrationStatus
	FailedFile    string   // embedded up-file for the dirty version, if any
	InspectionSQL []string // suggested read-only inspection queries
	ForceHint     string   // the exact force command, when applicable
}

// DiagnoseMigration assembles the operator-facing recovery guidance
// for a dirty (or otherwise non-head) database.  Read-only.
func DiagnoseMigration(ctx context.Context, rawDSN string) (*Diagnosis, error) {
	st, err := GetMigrationStatus(ctx, rawDSN)
	if err != nil {
		return nil, err
	}
	d := &Diagnosis{Status: st}

	if st.State == StateDirty {
		names, _ := MigrationFiles()
		for _, n := range names {
			if fv, ok := toVersionUint(st.Version); ok && fileVersion(n) == fv {
				d.FailedFile = n
				break
			}
		}
	}
	d.InspectionSQL = []string{
		"SELECT tablename FROM pg_tables WHERE schemaname = 'public';",
		"SELECT viewname FROM pg_views WHERE schemaname = 'public';",
		"SELECT indexname FROM pg_indexes WHERE schemaname = 'public';",
		"SELECT version, dirty FROM schema_migrations;",
	}
	if st.State == StateDirty && st.Version > 0 {
		d.ForceHint = fmt.Sprintf("gseer db migrate force --version %d", st.Version-1)
	} else if st.State == StateDirty {
		d.ForceHint = "gseer db migrate force --version 0"
	}
	return d, nil
}

// ForceMigrationVersion sets schema_migrations to (version, dirty=false)
// WITHOUT executing any SQL migration (owner directive §2: the force
// operation "only changes migration version metadata", "never executes
// or repairs SQL migrations").
//
// The CLI layer (internal/cli/db.go) adds the confirmation gate; this
// primitive enforces the argument contract: 0 <= version <= head.
func ForceMigrationVersion(rawDSN string, lockTimeoutMs, version int) error {
	head, err := HeadVersion()
	if err != nil {
		return err
	}
	if version < 0 || version > int(head) {
		return newDBError(CodeDBUsage,
			fmt.Sprintf("--version %d is out of range; the embedded migrations span 0..%d",
				version, head)).
			withNext(fmt.Sprintf("re-run with a version between 0 and %d (see: gseer db migrate status)", head))
	}
	effDSN, derr := effectiveMigrateDSN(rawDSN, lockTimeoutMs)
	if derr != nil {
		return derr
	}
	m, err := newMigrate(effDSN)
	if err != nil {
		return translateMigrateError(err, lockTimeoutMs, rawDSN)
	}
	defer func() {
		if _, cerr := m.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	if err := m.Force(version); err != nil {
		return translateMigrateError(err, lockTimeoutMs, rawDSN)
	}
	return nil
}
