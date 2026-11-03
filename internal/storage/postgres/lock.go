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
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	migratedb "github.com/golang-migrate/migrate/v4/database"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
)

// lock.go — bounded migration advisory-lock waiting and its error
// translation (M3; scope corrected in the M4 review round, owner
// directive §1).
//
// The migration driver (migratedriver.go — a fork of golang-migrate's
// pgx/v5 driver) acquires its lock with a BLOCKING
// `SELECT pg_advisory_lock($1)`; PostgreSQL's lock_timeout does not
// apply to advisory locks and the driver accepts no context.  Without a
// bound, a contended `gseer setup` appears frozen indefinitely.
//
// TIMEOUT SCOPE (exact, per the owner's verification round):
//
//   - The configured lock timeout bounds ONLY the advisory-lock
//     ACQUISITION statement.  The driver sets a session-level
//     statement_timeout immediately before that statement and resets it
//     to 0 (verified via SHOW) immediately after the lock is acquired —
//     before any migration SQL runs (gseerMigrateDriver.Lock).
//   - Migration statements therefore run UNBOUNDED: a migration taking
//     longer than the lock timeout is normal and must succeed.
//   - When the lock wait exceeds the limit, the server cancels the
//     pg_advisory_lock statement with SQLSTATE 57014 ("canceling
//     statement due to statement timeout"), which isStatementTimeoutErr
//     recognises and translateMigrateError renders as
//     DB_MIGRATION_LOCK_TIMEOUT with actionable next steps.
//
// Locking correctness is unchanged: the timeout bounds waiting only.
// Once acquired, the runner holds the upstream advisory lock key until
// Unlock()/session teardown — serialization, dirty-state bookkeeping
// and crash release behave exactly as upstream.

// effectiveMigrateDSN returns the DSN used by the migrate runner:
//
//   - the URL scheme is rewritten postgres://|postgresql:// →
//     gseerpgx5:// so migrate.NewWithSourceInstance routes to GSEER's
//     scoped-lock driver (migratedriver.go);
//   - the lock timeout travels as the x-lock-timeout-ms parameter,
//     which the driver consumes and strips — it NEVER reaches the
//     server as a session GUC (that was the M3 stopgap, removed here);
//     an operator-supplied x-lock-timeout-ms in the DSN wins over the
//     config-layer default (validate.go bounds it to 1000..300000 ms).
//
// The returned DSN is for CONNECTING ONLY.  Diagnostics must use
// RedactDSN (the x-lock-timeout-ms parameter is not secret, but the
// userinfo is — effectiveMigrateDSN never logs anything itself).
func effectiveMigrateDSN(rawDSN string, lockTimeoutMs int) (string, error) {
	u, err := url.Parse(rawDSN)
	if err != nil {
		return "", newDBError(CodeDBUsage, "cannot parse the database DSN").
			withWhy(fmt.Sprintf("the DSN is not a valid URL: %v", err)).
			withNext("use the URL form: postgres://user:password@host:port/dbname?sslmode=disable")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", newDBError(CodeDBUsage, fmt.Sprintf("unsupported DSN scheme %q", u.Scheme)).
			withWhy("the migration runner connects with the pgx/v5 driver, which expects a postgres:// URL").
			withNext("use the URL form: postgres://user:password@host:port/dbname?sslmode=disable")
	}
	q := u.Query()
	if q.Get(lockTimeoutParam) == "" {
		q.Set(lockTimeoutParam, strconv.Itoa(lockTimeoutMs))
	}
	u.RawQuery = q.Encode()
	u.Scheme = gseerMigrateDriverName
	return u.String(), nil
}

// isStatementTimeoutErr reports whether err (or anything in its chain)
// is the server-side abort SQLSTATE 57014 with the statement-timeout
// message — the cancellation that bounds pg_advisory_lock.
//
// The SQLSTATE alone is NOT sufficient: a statement canceled by a user
// (pg_cancel_backend / Ctrl-C) also carries 57014 but with the message
// "canceling statement due to user request" — that is NOT a lock
// timeout and must fall through to the regular migration-failure
// classification.
//
// golang-migrate wraps driver errors in database.Error whose OrigErr
// field is not exposed via Unwrap, so the chain traversal also checks
// that wrapper explicitly.
func isStatementTimeoutErr(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgerrcode.QueryCanceled &&
			strings.Contains(pgErr.Message, "statement timeout")
	}
	var dErr *migratedb.Error
	if errors.As(err, &dErr) {
		return isStatementTimeoutErr(dErr.OrigErr)
	}
	return false
}

// lockTimeoutError builds the actionable DB_MIGRATION_LOCK_TIMEOUT
// error (owner directive §1).  redactedDSN is the display-safe form.
func lockTimeoutError(timeoutMs int, redactedDSN string) *DBError {
	e := newDBError(CodeDBMigrationLockTimeout,
		fmt.Sprintf("could not acquire the migration advisory lock within %s.",
			humanizeMillis(timeoutMs))).
		withWhy("the migration runner uses pg_advisory_lock to serialize concurrent " +
			"migrations; a lock held longer than the configured timeout usually means " +
			"another migrate process is stuck or the database is not responding")
	if redactedDSN != "" {
		e.Detail += " Target: " + redactedDSN
	}
	e.Next = "1) verify no other `gseer setup` / `gseer server start` is running: " +
		"ps aux | grep gseer; " +
		"2) find the lock holder: SELECT pid, state, query, query_start FROM pg_stat_activity " +
		"WHERE query LIKE '%pg_advisory_lock%'; " +
		"3) terminate a stuck holder after confirming it is not making progress: " +
		"SELECT pg_terminate_backend(<pid>); " +
		"4) re-run the command. To increase the timeout: " +
		"GSEER_DB_MIGRATION_LOCK_TIMEOUT_MS=60000"
	return e
}

// humanizeMillis renders a millisecond duration for error text.
func humanizeMillis(ms int) string {
	if ms%1000 == 0 {
		return fmt.Sprintf("%ds", ms/1000)
	}
	return fmt.Sprintf("%dms", ms)
}
