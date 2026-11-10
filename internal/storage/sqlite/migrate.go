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

package sqlite

// migrate.go — the personal-mode migration runner.
//
// The self-hosted profile runs golang-migrate over the PostgreSQL
// migrations; the personal engine mirrors the same CONTRACT with a
// small deterministic runner (the sqlite3 migrate driver requires CGO
// via mattn, which the personal build deliberately avoids):
//   - schema_migrations(version INTEGER PRIMARY KEY, dirty, applied_at);
//   - every migration runs inside ONE transaction (the personal schema
//     files are transaction-safe by the same rule the PG migrations
//     follow — no CONCURRENTLY, no VACUUM);
//   - a failure marks the row dirty and rolls back; recovery is an
//     explicit force (same operator semantics as gseer db migrate
//     force on PostgreSQL).
//
// Head reports the embedded head version; Status reports the database
// state (AT_HEAD / BEHIND / AHEAD / DIRTY / UNINITIALIZED) — the same
// vocabulary the CLI's migrate status prints for PostgreSQL.

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.up.sql
var migrationFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations parses the embedded migrations/*.up.sql files (named
// NNNN_*.up.sql) sorted by version.
func loadMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("sqlite: cannot read embedded migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		var version int
		if _, err := fmt.Sscanf(name, "%d_", &version); err != nil {
			return nil, fmt.Errorf("sqlite: migration file %q does not start with a version", name)
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("sqlite: cannot read migration %q: %w", name, err)
		}
		out = append(out, migration{version: version, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// MigrationHead returns the embedded head migration version.
func MigrationHead() (int, error) {
	ms, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	if len(ms) == 0 {
		return 0, fmt.Errorf("sqlite: no migrations embedded")
	}
	return ms[len(ms)-1].version, nil
}

// MigrationState is the database's migration position (the same
// vocabulary as the PostgreSQL CLI surface).
type MigrationState string

const (
	StateAtHead        MigrationState = "AT_HEAD"
	StateBehind        MigrationState = "BEHIND"
	StateAhead         MigrationState = "AHEAD"
	StateDirty         MigrationState = "DIRTY"
	StateUninitialized MigrationState = "UNINITIALIZED"
)

// MigrationStatus is a point-in-time snapshot.
type MigrationStatus struct {
	Head    int
	Version int64
	Dirty   bool
	State   MigrationState
}

// schemaMigrationsDDL creates the runner's bookkeeping table.
const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    dirty      INTEGER NOT NULL DEFAULT 0,
    applied_at TEXT    NOT NULL
)`

// Migrate applies every pending embedded migration inside its own
// transaction.  Idempotent: AT_HEAD is a no-op.
func Migrate(ctx context.Context, db *sql.DB) (MigrationStatus, error) {
	head, err := MigrationHead()
	if err != nil {
		return MigrationStatus{}, err
	}
	if _, err := db.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return MigrationStatus{}, fmt.Errorf("sqlite: cannot create schema_migrations: %w", err)
	}

	ms, err := loadMigrations()
	if err != nil {
		return MigrationStatus{}, err
	}
	for _, m := range ms {
		var (
			current int64
			dirty   bool
		)
		row := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0), COALESCE(MAX(dirty), 0) FROM schema_migrations`)
		if err := row.Scan(&current, &dirty); err != nil {
			return MigrationStatus{}, fmt.Errorf("sqlite: cannot read migration state: %w", err)
		}
		if dirty {
			return MigrationStatus{Head: head, Version: current, Dirty: true, State: StateDirty}, nil
		}
		if int64(m.version) <= current {
			continue
		}
		if err := applyOne(ctx, db, m); err != nil {
			_, _ = db.ExecContext(ctx, `INSERT INTO schema_migrations (version, dirty, applied_at) VALUES (?, 1, ?)
ON CONFLICT(version) DO UPDATE SET dirty = 1`, m.version, FormatTS(time.Now()))
			return MigrationStatus{Head: head, Version: int64(m.version) - 1, Dirty: true, State: StateDirty}, err
		}
	}
	st, err := GetMigrationStatus(ctx, db)
	if err != nil {
		return MigrationStatus{}, err
	}
	return st, nil
}

// applyOne runs one migration in a transaction and records the version.
func applyOne(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: cannot begin migration %d: %w", m.version, err)
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sqlite: migration %d (%s) failed: %w", m.version, m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, dirty, applied_at) VALUES (?, 0, ?)
ON CONFLICT(version) DO UPDATE SET dirty = 0, applied_at = excluded.applied_at`, m.version, FormatTS(time.Now())); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sqlite: cannot record migration %d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: cannot commit migration %d: %w", m.version, err)
	}
	return nil
}

// GetMigrationStatus reports the database's position without writing.
func GetMigrationStatus(ctx context.Context, db *sql.DB) (MigrationStatus, error) {
	head, err := MigrationHead()
	if err != nil {
		return MigrationStatus{}, err
	}
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&exists); err != nil {
		return MigrationStatus{}, fmt.Errorf("sqlite: cannot inspect schema_migrations: %w", err)
	}
	if exists == 0 {
		return MigrationStatus{Head: head, Version: 0, State: StateUninitialized}, nil
	}
	var (
		current int64
		dirty   bool
	)
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0), COALESCE(MAX(dirty), 0) FROM schema_migrations`).Scan(&current, &dirty); err != nil {
		return MigrationStatus{}, fmt.Errorf("sqlite: cannot read migration state: %w", err)
	}
	st := MigrationStatus{Head: head, Version: current, Dirty: dirty}
	switch {
	case dirty:
		st.State = StateDirty
	case current < int64(head):
		st.State = StateBehind
	case current > int64(head):
		st.State = StateAhead
	default:
		st.State = StateAtHead
	}
	return st, nil
}

// ForceVersion re-points the schema_migrations row (the recovery path
// for DIRTY after a manual fix — same semantics as the PG force).
func ForceVersion(ctx context.Context, db *sql.DB, version int64, dirty bool) error {
	if _, err := db.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("sqlite: cannot create schema_migrations: %w", err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version, dirty, applied_at) VALUES (?, ?, ?)
ON CONFLICT(version) DO UPDATE SET dirty = excluded.dirty`, version, boolToInt(dirty), FormatTS(time.Now()))
	if err != nil {
		return fmt.Errorf("sqlite: cannot force version %d: %w", version, err)
	}
	return nil
}
