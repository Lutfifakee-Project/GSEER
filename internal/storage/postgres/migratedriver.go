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
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	nurl "net/url"
	"strconv"
	"strings"
	"sync/atomic"

	migrate "github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers database/sql driver "pgx/v5"
)

// migratedriver.go — GSEER's migration driver, a scoped-lock fork of
// golang-migrate's database/pgx/v5 driver (v4.20.1, MIT — structure and
// table bookkeeping follow the upstream file; the lock scoping below is
// GSEER-specific and is the reason the fork exists).
//
// WHY A FORK (owner directive, M3→M4 review round §1):
//
//   - The upstream driver acquires its advisory lock with
//     `SELECT pg_advisory_lock($1)` on context.Background() — the wait
//     is UNBOUNDED, so a contended `gseer setup` appears frozen.
//   - PostgreSQL's lock_timeout does not apply to advisory locks.
//   - The M3 stopgap injected a SESSION-level statement_timeout runtime
//     parameter into the DSN.  That bounds the lock wait, but the same
//     session also executes the migration SQL — so every migration
//     statement inherited the lock timeout too: a legitimate migration
//     statement (e.g. a future CREATE INDEX over a populated table)
//     running longer than the lock timeout would be killed with
//     SQLSTATE 57014 and misreported as DB_MIGRATION_LOCK_TIMEOUT.
//
// THE FIX — because the driver runs the lock AND the migrations on ONE
// dedicated connection (p.conn), the timeout can be scoped around the
// lock statement exactly:
//
//      Lock():  SET statement_timeout TO '<lockMs>ms'   (session-local)
//               SELECT pg_advisory_lock($1)             (bounded; 57014 on expiry)
//               SET statement_timeout TO 0              (migrations run unbounded)
//               SHOW statement_timeout  →  must be 0    (invariant, verified)
//
// What is PRESERVED relative to upstream:
//   - the advisory lock key: migratedb.GenerateAdvisoryLockId with the
//     same arguments (database name = URL path, schema, migrations
//     table) — a stock `migrate` CLI and GSEER serialize against each
//     other;
//   - schema_migrations bookkeeping (CREATE TABLE, TRUNCATE+INSERT in a
//     transaction, dirty flag semantics) — byte-for-byte upstream;
//   - one atomic implicit transaction per migration file
//     (MultiStatementMode disabled: the whole file is one ExecContext,
//     D-M3-5);
//   - per-file failure → transaction rolled back, version marked dirty.
//
// What is DELIBERATELY DIFFERENT:
//   - Lock waiting is bounded by the configured lock timeout
//     (DB_MIGRATION_LOCK_TIMEOUT on expiry);
//   - migration statement execution is UNBOUNDED (no statement timeout,
//     no context deadline) — slow-but-honest migrations are never
//     killed by the lock-wait limit; a hung migration is handled by the
//     dirty-state machinery and the operator (`gseer db migrate
//     diagnose`), not by a timer;
//   - the x-* DSN options are not parsed (GSEER passes exactly one:
//     x-lock-timeout-ms, see effectiveMigrateDSN in lock.go).

// gseerMigrateDriverName is the URL scheme this driver registers under.
// effectiveMigrateDSN rewrites postgres:// → gseerpgx5:// to route
// migrate.NewWithSourceInstance here.
const gseerMigrateDriverName = "gseerpgx5"

// lockTimeoutParam is the only x-* parameter GSEER injects into the
// migrate URL.  migrate.FilterCustomQuery strips all x-* parameters
// before the DSN reaches pgx, so it never becomes a runtime parameter.
const lockTimeoutParam = "x-lock-timeout-ms"

// migrationsTableName is the driver-owned bookkeeping table (upstream
// default; never referenced by application SQL).
const migrationsTableName = "schema_migrations"

func init() {
	migratedb.Register(gseerMigrateDriverName, &gseerMigrateDriver{})
}

type gseerMigrateDriver struct {
	// Locking, migration execution and version bookkeeping must use the
	// SAME connection (session-scoped GUCs and the session-scoped
	// advisory lock both depend on this).
	conn *sql.Conn
	db   *sql.DB
	// isLocked guards against double Lock/Unlock (upstream CAS pattern).
	isLocked atomic.Bool

	databaseName        string // URL path, e.g. "/gseer" (upstream parity)
	migrationsTableName string // unquoted identifier
	lockTimeoutMs       int    // bounds ONLY the advisory-lock statement
}

// Open implements migratedb.Driver.  The URL arrives as
// gseerpgx5://user:pass@host:port/db?sslmode=...&x-lock-timeout-ms=N
// (built by effectiveMigrateDSN).  The scheme is rewritten back to
// postgres:// for the pgx stdlib; x-lock-timeout-ms is consumed here and
// never reaches the server.
func (p *gseerMigrateDriver) Open(url string) (migratedb.Driver, error) {
	purl, err := nurl.Parse(url)
	if err != nil {
		return nil, err
	}

	lockTimeoutMs, lerr := parseLockTimeoutParam(purl)
	if lerr != nil {
		return nil, lerr
	}

	// Driver is registered as gseerpgx5, but the actual connection uses
	// the postgres scheme (upstream parity).  FilterCustomQuery drops
	// every x-* parameter, including x-lock-timeout-ms.
	purl.Scheme = "postgres"
	db, err := sql.Open("pgx/v5", migrate.FilterCustomQuery(purl).String())
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		// Return RAW (upstream parity): translateMigrateError classifies
		// *pgconn.ConnectError as E_DB_UNREACHABLE; wrapping it in
		// database.Error would hide it (OrigErr is a field, not Unwrap).
		return nil, err
	}

	databaseName := purl.Path
	if databaseName == "" {
		if err := db.QueryRow(`SELECT CURRENT_DATABASE()`).Scan(&databaseName); err != nil {
			_ = db.Close()
			return nil, &migratedb.Error{OrigErr: err, Query: []byte(`SELECT CURRENT_DATABASE()`)}
		}
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	drv := &gseerMigrateDriver{
		conn:                conn,
		db:                  db,
		databaseName:        databaseName,
		migrationsTableName: migrationsTableName,
		lockTimeoutMs:       lockTimeoutMs,
	}

	if err := drv.ensureVersionTable(); err != nil {
		_ = drv.Close()
		return nil, err
	}
	return drv, nil
}

// parseLockTimeoutParam extracts and validates x-lock-timeout-ms.
func parseLockTimeoutParam(purl *nurl.URL) (int, error) {
	raw := purl.Query().Get(lockTimeoutParam)
	if raw == "" {
		return 0, &migratedb.Error{Err: fmt.Sprintf(
			"internal error: the %s:// DSN is missing %s (effectiveMigrateDSN must inject it)",
			gseerMigrateDriverName, lockTimeoutParam)}
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1000 || n > 300000 {
		return 0, &migratedb.Error{Err: fmt.Sprintf(
			"internal error: %s must be an integer between 1000 and 300000, got %q",
			lockTimeoutParam, raw)}
	}
	return n, nil
}

func (p *gseerMigrateDriver) Close() error {
	connErr := p.conn.Close()
	dbErr := p.db.Close()
	if connErr != nil || dbErr != nil {
		return fmt.Errorf("conn: %v, db: %v", connErr, dbErr)
	}
	return nil
}

// Lock acquires the migration advisory lock with a BOUNDED wait.
//
// Exact timeout scope (owner directive §1, documented for review):
//
//  1. SET statement_timeout TO '<lockMs>ms' — session-local GUC;
//  2. SELECT pg_advisory_lock($1) — killed by the server with
//     SQLSTATE 57014 ("canceling statement due to statement timeout")
//     when the wait exceeds the configured limit; translated to
//     DB_MIGRATION_LOCK_TIMEOUT by translateMigrateError;
//  3. SET statement_timeout TO 0 — lifted IMMEDIATELY after
//     acquisition, on the same session, before any migration SQL runs;
//  4. SHOW statement_timeout verified == 0 (defense in depth: if the
//     reset silently failed, migrations would inherit the lock timeout
//     and a slow migration could be killed and misclassified — so this
//     path is a hard error, never a warning).
//
// Serialization is unchanged: the lock key is upstream's
// GenerateAdvisoryLockId("/<dbname>", schema, table) and the lock is
// held on p.conn until Unlock() or session teardown.
func (p *gseerMigrateDriver) Lock() error {
	return migratedb.CasRestoreOnErr(&p.isLocked, false, true, migratedb.ErrLocked, func() error {
		aid, err := migratedb.GenerateAdvisoryLockId(p.databaseName, "public", p.migrationsTableName)
		if err != nil {
			return err
		}

		// (1) Bound ONLY the upcoming lock statement.
		setBounded := fmt.Sprintf("SET statement_timeout TO '%dms'", p.lockTimeoutMs)
		if _, err := p.conn.ExecContext(context.Background(), setBounded); err != nil {
			return &migratedb.Error{OrigErr: err, Err: "try lock failed", Query: []byte(setBounded)}
		}

		// (2) Bounded advisory-lock acquisition.  This will wait until
		// the lock is free or the session statement_timeout fires.
		lockQuery := `SELECT pg_advisory_lock($1)`
		if _, err := p.conn.ExecContext(context.Background(), lockQuery, aid); err != nil {
			return &migratedb.Error{OrigErr: err, Err: "try lock failed", Query: []byte(lockQuery)}
		}

		// (3)+(4) Lift the bound before any migration runs.
		if err := p.unboundSessionTimeout(); err != nil {
			// The lock is held but the session is not fit for
			// migrations; release it and surface the failure.
			_, _ = p.conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, aid)
			return err
		}
		return nil
	})
}

// unboundSessionTimeout resets statement_timeout to 0 on the migrate
// session and VERIFIES the reset (Lock() step 3+4).
func (p *gseerMigrateDriver) unboundSessionTimeout() error {
	if _, err := p.conn.ExecContext(context.Background(), `SET statement_timeout TO 0`); err != nil {
		return &migratedb.Error{OrigErr: err,
			Err: "migration lock acquired but the statement_timeout reset failed; " +
				"migrations must not run under the lock-wait timeout"}
	}
	var shown string
	if err := p.conn.QueryRowContext(context.Background(), `SHOW statement_timeout`).Scan(&shown); err != nil {
		return &migratedb.Error{OrigErr: err,
			Err: "migration lock acquired but SHOW statement_timeout failed"}
	}
	if v, perr := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(shown), "ms"))); perr != nil || v != 0 {
		return &migratedb.Error{
			Err: fmt.Sprintf("migration lock acquired but statement_timeout is %q after reset; "+
				"migrations must not run under the lock-wait timeout", shown)}
	}
	return nil
}

func (p *gseerMigrateDriver) Unlock() error {
	return migratedb.CasRestoreOnErr(&p.isLocked, true, false, migratedb.ErrNotLocked, func() error {
		aid, err := migratedb.GenerateAdvisoryLockId(p.databaseName, "public", p.migrationsTableName)
		if err != nil {
			return err
		}
		query := `SELECT pg_advisory_unlock($1)`
		if _, err := p.conn.ExecContext(context.Background(), query, aid); err != nil {
			return &migratedb.Error{OrigErr: err, Query: []byte(query)}
		}
		return nil
	})
}

// Run applies one migration file.  Deliberately NO timeout: the lock
// wait is bounded, migration EXECUTION is not (see the package
// comment).  With MultiStatementMode disabled the whole file is sent as
// a single ExecContext — one implicit transaction per file (D-M3-5).
func (p *gseerMigrateDriver) Run(migration io.Reader) error {
	migr, err := io.ReadAll(migration)
	if err != nil {
		return err
	}
	return p.runStatement(migr)
}

func (p *gseerMigrateDriver) runStatement(statement []byte) error {
	query := string(statement)
	if strings.TrimSpace(query) == "" {
		return nil
	}
	// No context deadline here — that is the whole point of the fork.
	if _, err := p.conn.ExecContext(context.Background(), query); err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok {
			line, col, lineColOK := computeLineFromPos(query, int(pgErr.Position))
			message := fmt.Sprintf("migration failed: %s", pgErr.Message)
			if lineColOK {
				message = fmt.Sprintf("%s (column %d)", message, col)
			}
			if pgErr.Detail != "" {
				message = fmt.Sprintf("%s, %s", message, pgErr.Detail)
			}
			return migratedb.Error{OrigErr: err, Err: message, Query: statement, Line: line}
		}
		return migratedb.Error{OrigErr: err, Err: "migration failed", Query: statement}
	}
	return nil
}

// computeLineFromPos is copied from golang-migrate v4.20.1
// database/pgx/v5 (MIT) for error-message parity.
func computeLineFromPos(s string, pos int) (line uint, col uint, ok bool) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	runes := []rune(s)
	if pos > len(runes) {
		return 0, 0, false
	}
	sel := runes[:pos]
	line = uint(runesCount(sel, newLine) + 1)
	col = uint(pos - 1 - runesLastIndex(sel, newLine))
	return line, col, true
}

const newLine = '\n'

func runesCount(input []rune, target rune) int {
	var count int
	for _, r := range input {
		if r == target {
			count++
		}
	}
	return count
}

func runesLastIndex(input []rune, target rune) int {
	for i := len(input) - 1; i >= 0; i-- {
		if input[i] == target {
			return i
		}
	}
	return -1
}

// SetVersion is byte-for-byte upstream bookkeeping: TRUNCATE + INSERT
// in one transaction on the lock connection.
func (p *gseerMigrateDriver) SetVersion(version int, dirty bool) error {
	tx, err := p.conn.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		return &migratedb.Error{OrigErr: err, Err: "transaction start failed"}
	}

	query := `TRUNCATE ` + quoteIdentifier(p.migrationsTableName)
	if _, err := tx.Exec(query); err != nil {
		if errRollback := tx.Rollback(); errRollback != nil {
			err = errors.Join(err, errRollback)
		}
		return &migratedb.Error{OrigErr: err, Query: []byte(query)}
	}

	// Also re-write the schema version for nil dirty versions to prevent
	// an empty schema version after a failed first migration
	// (golang-migrate/migrate#330 — upstream parity).
	if version >= 0 || (version == migratedb.NilVersion && dirty) {
		query = `INSERT INTO ` + quoteIdentifier(p.migrationsTableName) + ` (version, dirty) VALUES ($1, $2)`
		if _, err := tx.Exec(query, version, dirty); err != nil {
			if errRollback := tx.Rollback(); errRollback != nil {
				err = errors.Join(err, errRollback)
			}
			return &migratedb.Error{OrigErr: err, Query: []byte(query)}
		}
	}

	if err := tx.Commit(); err != nil {
		return &migratedb.Error{OrigErr: err, Err: "transaction commit failed"}
	}
	return nil
}

func (p *gseerMigrateDriver) Version() (version int, dirty bool, err error) {
	query := `SELECT version, dirty FROM ` + quoteIdentifier(p.migrationsTableName) + ` LIMIT 1`
	err = p.conn.QueryRowContext(context.Background(), query).Scan(&version, &dirty)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return migratedb.NilVersion, false, nil
	case err != nil:
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UndefinedTable {
			return migratedb.NilVersion, false, nil
		}
		return 0, false, &migratedb.Error{OrigErr: err, Query: []byte(query)}
	default:
		return version, dirty, nil
	}
}

// Drop deletes every base table in the current schema (upstream parity;
// not used by any GSEER command in v0.0.1).
func (p *gseerMigrateDriver) Drop() (err error) {
	query := `SELECT table_name FROM information_schema.tables WHERE table_schema=(SELECT current_schema()) AND table_type='BASE TABLE'`
	tables, err := p.conn.QueryContext(context.Background(), query)
	if err != nil {
		return &migratedb.Error{OrigErr: err, Query: []byte(query)}
	}
	defer func() {
		if errClose := tables.Close(); errClose != nil {
			err = errors.Join(err, errClose)
		}
	}()

	var tableNames []string
	for tables.Next() {
		var tableName string
		if err := tables.Scan(&tableName); err != nil {
			return err
		}
		if len(tableName) > 0 {
			tableNames = append(tableNames, tableName)
		}
	}
	if err := tables.Err(); err != nil {
		return &migratedb.Error{OrigErr: err, Query: []byte(query)}
	}

	for _, t := range tableNames {
		query = `DROP TABLE IF EXISTS ` + quoteIdentifier(t) + ` CASCADE`
		if _, err := p.conn.ExecContext(context.Background(), query); err != nil {
			return &migratedb.Error{OrigErr: err, Query: []byte(query)}
		}
	}
	return nil
}

// ensureVersionTable creates the driver-owned bookkeeping table when
// missing (upstream parity, with the BOUNDED lock).
func (p *gseerMigrateDriver) ensureVersionTable() (err error) {
	if err = p.Lock(); err != nil {
		return err // bounded wait failed → DB_MIGRATION_LOCK_TIMEOUT path
	}

	defer func() {
		if e := p.Unlock(); e != nil {
			err = errors.Join(err, e)
		}
	}()

	query := `SELECT COUNT(1) FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2 LIMIT 1`
	row := p.conn.QueryRowContext(context.Background(), query, "public", p.migrationsTableName)

	var count int
	if err := row.Scan(&count); err != nil {
		return &migratedb.Error{OrigErr: err, Query: []byte(query)}
	}
	if count == 1 {
		return nil
	}

	query = `CREATE TABLE IF NOT EXISTS ` + quoteIdentifier(p.migrationsTableName) +
		` (version bigint not null primary key, dirty boolean not null)`
	if _, err := p.conn.ExecContext(context.Background(), query); err != nil {
		return &migratedb.Error{OrigErr: err, Query: []byte(query)}
	}
	return nil
}

// quoteIdentifier is copied from golang-migrate v4.20.1
// database/pgx/v5 (MIT, originates from lib/pq).
func quoteIdentifier(name string) string {
	end := strings.IndexRune(name, 0)
	if end > -1 {
		name = name[:end]
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
