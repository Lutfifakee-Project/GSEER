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
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	migrate "github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Lutfifakee-Project/GSEER/internal/storage/migrations"
)

// migrate.go — the migration runner (M3).
//
//   - Source: internal/storage/migrations embedded via //go:embed and
//     served through the golang-migrate iofs driver — the binary is
//     self-contained (no runtime file lookup).
//   - Driver: GSEER's scoped-lock fork of golang-migrate's pgx/v5
//     driver (migratedriver.go, registered as "gseerpgx5"), the same
//     pgx major as the application pool (D-M3-4).
//   - Transactions: MultiStatementMode stays DISABLED so every
//     migration file runs in ONE atomic transaction (D-M3-5).
//   - schema_migrations: created and owned by the driver; never touched
//     by application SQL (owner directive §2).
//   - Locking: bounded via the session-scoped statement_timeout in
//     gseerMigrateDriver.Lock — the timeout covers ONLY the advisory-
//     lock acquisition and is reset (verified) before migration SQL
//     runs (lock.go + migratedriver.go; D-M3-4 as corrected in the M4
//     review round).

// migrationFileRe matches the golang-migrate naming convention
// NNNN_name.(up|down).sql inside the embedded FS.
var migrationFileRe = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)

// MigrationFiles returns the embedded up-migration file names in
// ascending version order (used by status/diagnose output).
func MigrationFiles() ([]string, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if migrationFileRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// HeadVersion returns the highest embedded migration version.  Doctor
// compares it against the database's recorded version to classify
// AT_HEAD / BEHIND / AHEAD (owner directive §2.5).
func HeadVersion() (uint, error) {
	names, err := MigrationFiles()
	if err != nil {
		return 0, err
	}
	if len(names) == 0 {
		return 0, fmt.Errorf("no migrations embedded")
	}
	last := names[len(names)-1]
	sub := migrationFileRe.FindStringSubmatch(last)
	if sub == nil {
		return 0, fmt.Errorf("cannot parse migration version from %q", last)
	}
	v, err := strconv.ParseUint(sub[1], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("cannot parse migration version from %q: %w", last, err)
	}
	return uint(v), nil
}

// newMigrate builds a migrate instance over the embedded iofs source
// and the pgx/v5 driver bound to effDSN.  Close() must be called by
// the caller (it releases the advisory lock and the connection).
func newMigrate(effDSN string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, err
	}
	// NewWithSourceInstance consumes src; m.Close() closes the source.
	m, err := migrate.NewWithSourceInstance("iofs", src, effDSN)
	return m, err
}

// RunMigrations applies pending embedded migrations to the database at
// rawDSN.  It returns the number of migrations applied (0 when the
// schema was already at head — idempotent re-run).
//
// Error translation (owner directives):
//   - advisory lock not acquired within the timeout →
//     DB_MIGRATION_LOCK_TIMEOUT (lock.go);
//   - a migration failed mid-execution → E_DB_MIGRATION_FAILED (the
//     failed migration's transaction was rolled back; the version is
//     dirty — recovery via `gseer db migrate diagnose`);
//   - the database was already dirty → E_DB_MIGRATION_DIRTY.
func RunMigrations(rawDSN string, lockTimeoutMs int) (applied int, head uint, err error) {
	head, err = HeadVersion()
	if err != nil {
		return 0, 0, err
	}
	effDSN, derr := effectiveMigrateDSN(rawDSN, lockTimeoutMs)
	if derr != nil {
		return 0, head, derr
	}

	// Current version BEFORE Up(): nil version means a fresh database.
	before, _, beforeErr := migrateVersion(effDSN)
	if beforeErr != nil {
		return 0, head, beforeErr
	}

	m, err := newMigrate(effDSN)
	if err != nil {
		return 0, head, translateMigrateError(err, lockTimeoutMs, rawDSN)
	}
	defer func() {
		if _, cerr := m.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	upErr := m.Up()
	if upErr != nil && !errors.Is(upErr, migrate.ErrNoChange) {
		// Already-at-head is the normal idempotent re-run (return value
		// below reports applied=0); any other Up failure is translated.
		return 0, head, translateMigrateError(upErr, lockTimeoutMs, rawDSN)
	}

	switch {
	case before < 0:
		// Fresh database: every embedded up-file applies.
		applied = countMigrationFiles(head)
	case uint(before) < head:
		// Upgrade: count the FILES between the recorded version and head —
		// NOT the version delta.  Versions 0007–0015 are permanently
		// reserved (OD-4), so 6 → 17 is TWO files (0016, 0017); reporting
		// "11 applied" would mislead the operator about what ran.
		applied = len(pendingFiles(uint(before), head))
	default:
		applied = 0
	}
	return applied, head, nil
}

// countMigrationFiles returns the number of embedded up-migrations
// (≤ head; under the reserved range this differs from head itself).
func countMigrationFiles(head uint) int {
	names, err := MigrationFiles()
	if err != nil {
		// HeadVersion already succeeded, so listing cannot fail; be honest
		// with the version delta as a last resort.
		return int(head)
	}
	n := 0
	for _, name := range names {
		if fileVersion(name) <= head {
			n++
		}
	}
	return n
}

// migrateVersion reads the recorded schema version by briefly opening
// the migrate driver (which also ensures the version table exists).
// Returns -1 when no version is recorded (fresh database).
func migrateVersion(effDSN string) (int, bool, error) {
	m, err := newMigrate(effDSN)
	if err != nil {
		return 0, false, translateMigrateError(err, 0, "")
	}
	defer func() {
		if _, cerr := m.Close(); cerr != nil {
			_ = cerr // read-only probe; close errors carry no recovery
		}
	}()

	v, dirty, verr := m.Version()
	if errors.Is(verr, migrate.ErrNilVersion) {
		return -1, false, nil
	}
	if verr != nil {
		return 0, false, translateMigrateError(verr, 0, "")
	}
	return int(v), dirty, nil
}

// translateMigrateError maps golang-migrate / PostgreSQL errors onto
// the stable GSEER codes (owner directives §1/§2 of the M3 round).
func translateMigrateError(err error, lockTimeoutMs int, rawDSN string) error {
	if err == nil {
		return nil
	}

	// Advisory-lock timeout (bounded wait, owner directive §1).
	if isStatementTimeoutErr(err) {
		return lockTimeoutError(lockTimeoutMs, MustRedactDSN(rawDSN))
	}

	// Connection failures (driver open or query transport) are
	// E_DB_UNREACHABLE, not migration failures — the actionable next
	// step is starting the database, not diagnosing migration state.
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return unreachableError(err, rawDSN)
	}

	// Dirty database: a previous migration failed; refuse to proceed.
	var dirty migrate.ErrDirty
	if errors.As(err, &dirty) {
		e := newDBError(CodeDBMigrationDirty,
			fmt.Sprintf("the database schema_migrations table is in a dirty state at version %d.",
				dirty.Version)).
			withWhy("a previous migration run failed mid-execution; PostgreSQL rolled back the " +
				"migration's transaction but schema_migrations records the failed version as dirty")
		e.Next = "run `gseer db migrate diagnose` for the recovery procedure; then, after manual " +
			"inspection, `gseer db migrate force --version <N>` (metadata-only, confirmation-gated)"
		return e
	}

	// Any other migration failure: the driver rolled the migration's
	// transaction back; surface a CONCISE message with recovery
	// guidance.  The driver's rendering embeds the full SQL text of
	// the failed file ("... in line 1: <entire file>") — that dump
	// is unusable in a terminal, so it is replaced by the underlying
	// PostgreSQL error message only.  The file itself is identified
	// by version via `gseer db migrate diagnose`.
	e := newDBError(CodeDBMigrationFailed,
		fmt.Sprintf("migration failed: %s", conciseCause(err))).
		withWhy("the failed migration's transaction was rolled back; the schema is unchanged " +
			"for the failing version, and schema_migrations records it as dirty")
	e.Next = "run `gseer db migrate diagnose` for the recovery procedure"
	return e
}

// conciseCause extracts the deepest PostgreSQL message from a migrate
// error chain, dropping the full SQL dump the driver embeds.  Falls
// back to the whole error string when no recognizable wrapper is found.
func conciseCause(err error) string {
	var dErr *migratedb.Error
	if errors.As(err, &dErr) {
		if dErr.OrigErr != nil {
			// Prefer the PG error message ("ERROR: ... (SQLSTATE X)")
			// over the driver's SQL-embedding format.
			var pgErr *pgconn.PgError
			if errors.As(dErr.OrigErr, &pgErr) {
				return fmt.Sprintf("%s (SQLSTATE %s)", pgErr.Message, pgErr.Code)
			}
			return dErr.OrigErr.Error()
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Sprintf("%s (SQLSTATE %s)", pgErr.Message, pgErr.Code)
	}
	return err.Error()
}
