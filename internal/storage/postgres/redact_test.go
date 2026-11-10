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
	"strings"
	"testing"

	migrate "github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestRedactDSN — AC-M3-7: the redacted DSN never carries userinfo.
func TestRedactDSN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"user+password", "postgres://admin:s3cret@127.0.0.1:5432/gseer?sslmode=disable",
			"postgres://127.0.0.1:5432/gseer?sslmode=disable"},
		{"user only", "postgres://admin@db.example.com/gseer",
			"postgres://db.example.com/gseer"},
		{"no userinfo", "postgres://127.0.0.1:5432/gseer?sslmode=require",
			"postgres://127.0.0.1:5432/gseer?sslmode=require"},
		{"special-char password", "postgres://u:p%40ss@127.0.0.1/gseer",
			"postgres://127.0.0.1/gseer"},
		{"ipv6 host", "postgres://u:p@[::1]:5433/gseer",
			"postgres://[::1]:5433/gseer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RedactDSN(tc.in)
			if err != nil {
				t.Fatalf("RedactDSN(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("RedactDSN(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// The zero-survivor invariant: no userinfo separator survives.
			if strings.Contains(got, "://") && strings.Contains(strings.SplitN(strings.SplitN(got, "://", 2)[1], "/", 1)[0], "@") {
				t.Errorf("RedactDSN(%q) leaked userinfo: %q", tc.in, got)
			}
		})
	}
}

func TestMustRedactDSN(t *testing.T) {
	if got := MustRedactDSN("postgres://u:p@h/db"); got != "postgres://h/db" {
		t.Errorf("MustRedactDSN valid = %q", got)
	}
	if got := MustRedactDSN("::not a url::"); got != "" {
		t.Errorf("MustRedactDSN invalid should be empty, got %q", got)
	}
}

// TestEffectiveMigrateDSN — the DSN handed to the migrate runner must
// use the gseerpgx5 scheme (GSEER's scoped-lock driver) and carry the
// lock timeout as x-lock-timeout-ms (consumed and stripped by the
// driver — it must NOT reach the server as a session GUC; that was the
// M3 stopgap removed in the M4 review round).
func TestEffectiveMigrateDSN(t *testing.T) {
	t.Run("injects lock timeout param", func(t *testing.T) {
		got, err := effectiveMigrateDSN("postgres://u:p@127.0.0.1:5432/gseer?sslmode=disable", 30000)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if !strings.HasPrefix(got, "gseerpgx5://") {
			t.Errorf("scheme not rewritten to gseerpgx5: %q", got)
		}
		if !strings.Contains(got, "x-lock-timeout-ms=30000") {
			t.Errorf("x-lock-timeout-ms not injected: %q", got)
		}
		if strings.Contains(got, "statement_timeout=") {
			t.Errorf("session-level statement_timeout must not be injected: %q", got)
		}
		if !strings.Contains(got, "sslmode=disable") {
			t.Errorf("existing params lost: %q", got)
		}
	})
	t.Run("operator x-lock-timeout-ms wins", func(t *testing.T) {
		got, err := effectiveMigrateDSN("postgres://u@h/db?x-lock-timeout-ms=5000", 30000)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if strings.Contains(got, "x-lock-timeout-ms=30000") {
			t.Errorf("operator timeout overridden: %q", got)
		}
		if !strings.Contains(got, "x-lock-timeout-ms=5000") {
			t.Errorf("operator timeout lost: %q", got)
		}
	})
	t.Run("rejects non-postgres scheme", func(t *testing.T) {
		_, err := effectiveMigrateDSN("mysql://u@h/db", 30000)
		if err == nil || !strings.Contains(err.Error(), "unsupported DSN scheme") {
			t.Errorf("want unsupported-scheme error, got %v", err)
		}
	})
	t.Run("postgresql:// alias accepted", func(t *testing.T) {
		got, err := effectiveMigrateDSN("postgresql://u@h/db", 30000)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if !strings.HasPrefix(got, "gseerpgx5://") {
			t.Errorf("postgresql:// not accepted: %q", got)
		}
	})
}

// TestIsStatementTimeoutErr — the server-side lock-timeout abort
// detection through the migrate driver's error wrapper chain.  Both the
// SQLSTATE (57014) and the message ("... statement timeout") must
// match: a statement canceled by an operator also carries 57014 but is
// NOT a lock timeout.
func TestIsStatementTimeoutErr(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:    pgerrcode.QueryCanceled,
		Message: "canceling statement due to statement timeout",
	}
	if !isStatementTimeoutErr(pgErr) {
		t.Error("57014 statement timeout not detected")
	}
	// Regression (M4 review): user-requested cancellation shares
	// SQLSTATE 57014 and must NOT be classified as a lock timeout.
	if isStatementTimeoutErr(&pgconn.PgError{
		Code:    pgerrcode.QueryCanceled,
		Message: "canceling statement due to user request",
	}) {
		t.Error("user-requested cancel wrongly detected as lock timeout")
	}
	if isStatementTimeoutErr(&pgconn.PgError{Code: pgerrcode.UndefinedTable}) {
		t.Error("42P01 wrongly detected as statement timeout")
	}
	if isStatementTimeoutErr(nil) || isStatementTimeoutErr(errTestPlain) {
		t.Error("nil/plain error wrongly detected")
	}
	// Regression (M3): the advisory-lock failure arrives wrapped in
	// migrate's *database.Error ("failed to open database: try lock
	// failed"), whose OrigErr is a field, not an Unwrap.  The detector
	// must traverse that wrapper to the 57014 inside.
	wrapped := fmt.Errorf("failed to open database: %w",
		&migratedb.Error{OrigErr: pgErr, Err: "try lock failed"})
	if !isStatementTimeoutErr(wrapped) {
		t.Error("57014 behind *database.Error wrapper not detected")
	}
}

var errTestPlain = &testErr{}

type testErr struct{}

func (*testErr) Error() string { return "plain" }

func TestHumanizeMillis(t *testing.T) {
	cases := map[int]string{30000: "30s", 60000: "60s", 2500: "2500ms"}
	for in, want := range cases {
		if got := humanizeMillis(in); got != want {
			t.Errorf("humanizeMillis(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestLockTimeoutErrorShape — AC-M3-11: the DB_MIGRATION_LOCK_TIMEOUT
// error carries the code, the timeout, the redacted target, and the
// actionable Try list; it NEVER carries userinfo.
func TestLockTimeoutErrorShape(t *testing.T) {
	err := lockTimeoutError(30000, "postgres://127.0.0.1:5432/gseer?sslmode=disable")
	msg := err.Error()
	for _, want := range []string{
		"DB_MIGRATION_LOCK_TIMEOUT", "30s",
		"postgres://127.0.0.1:5432/gseer", "pg_stat_activity", "GSEER_DB_MIGRATION_LOCK_TIMEOUT_MS",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error text missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "@") || strings.Contains(msg, "password") {
		t.Errorf("error text leaks credentials:\n%s", msg)
	}
}

// TestTranslateMigrateErrorConcise — regression (M3 smoke test): a
// failed migration must NOT dump the whole SQL file into the error
// text; the message carries the PostgreSQL cause only.  Mirrors the
// driver's real wrapper shape: OrigErr = *pgconn.PgError, Query = the
// full file text (database.Error.Error() renders Query — conciseCause
// must never touch it).
func TestTranslateMigrateErrorConcise(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:    "42P07",
		Message: `relation "latest_http" already exists`,
	}
	wrapped := fmt.Errorf("failed to open database: %w",
		&migratedb.Error{
			OrigErr: pgErr,
			Err:     "migration failed: " + pgErr.Message,
			Query:   []byte("CREATE VIEW latest_http AS SELECT secret_body_from_sql_file;"),
		})
	got := translateMigrateError(wrapped, 30000, "postgres://u:p@h/db").Error()
	if strings.Contains(got, "CREATE VIEW") || strings.Contains(got, "secret_body_from_sql_file") {
		t.Errorf("error text dumps SQL:\n%s", got)
	}
	if !strings.Contains(got, "relation \"latest_http\" already exists") {
		t.Errorf("missing PG cause:\n%s", got)
	}
	if !strings.Contains(got, "E_DB_MIGRATION_FAILED") {
		t.Errorf("missing code:\n%s", got)
	}
	if !strings.Contains(got, "gseer db migrate diagnose") {
		t.Errorf("missing recovery pointer:\n%s", got)
	}
}

// TestToVersionUint — range-proof conversion edge cases (gosec G115
// guard coverage).
func TestToVersionUint(t *testing.T) {
	if v, ok := toVersionUint(2); !ok || v != 2 {
		t.Errorf("toVersionUint(2) = %d, %v", v, ok)
	}
	if _, ok := toVersionUint(-1); ok {
		t.Error("negative version must be rejected")
	}
	if _, ok := toVersionUint(int64(math.MaxUint32) + 1); ok {
		t.Error("beyond uint32 must be rejected")
	}
	if v, ok := toVersionUint(int64(math.MaxUint32)); !ok || v != math.MaxUint32 {
		t.Errorf("MaxUint32 boundary: %d, %v", v, ok)
	}
}

// TestTranslateMigrateErrorBranches — dirty, connect-error, and
// fallback branches of the translator.
func TestTranslateMigrateErrorBranches(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if translateMigrateError(nil, 0, "") != nil {
			t.Error("nil must stay nil")
		}
	})
	t.Run("dirty carries version and recovery pointer", func(t *testing.T) {
		err := translateMigrateError(migrate.ErrDirty{Version: 3}, 0, "")
		var dbErr *DBError
		if !asDBError(err, &dbErr) || dbErr.Code != CodeDBMigrationDirty {
			t.Fatalf("want E_DB_MIGRATION_DIRTY, got %v", err)
		}
		if !strings.Contains(err.Error(), "version 3") || !strings.Contains(err.Error(), "gseer db migrate force") {
			t.Errorf("dirty text incomplete:\n%s", err)
		}
	})
	t.Run("connect error becomes unreachable", func(t *testing.T) {
		// Produce a GENUINE pgconn.ConnectError (synthetic construction
		// panics in Error()); a dead loopback port refuses fast.
		_, cerr := pgx.Connect(context.Background(),
			"postgres://gseer@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
		if cerr == nil {
			t.Fatal("expected a connect error")
		}
		var ce *pgconn.ConnectError
		if !errors.As(cerr, &ce) {
			t.Fatalf("want *pgconn.ConnectError, got %T: %v", cerr, cerr)
		}
		err := translateMigrateError(cerr, 0, "postgres://u:p@127.0.0.1:1/none")
		var dbErr *DBError
		if !asDBError(err, &dbErr) || dbErr.Code != CodeDBUnreachable {
			t.Fatalf("want E_DB_UNREACHABLE, got %v", err)
		}
		if strings.Contains(err.Error(), ":p@") || strings.Contains(err.Error(), "u:p") {
			t.Errorf("credentials leaked:\n%s", err)
		}
	})
	t.Run("plain error falls back with recovery pointer", func(t *testing.T) {
		err := translateMigrateError(errTestPlain, 0, "")
		var dbErr *DBError
		if !asDBError(err, &dbErr) || dbErr.Code != CodeDBMigrationFailed {
			t.Fatalf("want E_DB_MIGRATION_FAILED, got %v", err)
		}
	})
	t.Run("conciseCause non-pg OrigErr falls back", func(t *testing.T) {
		got := conciseCause(fmt.Errorf("wrapper: %w", &migratedb.Error{
			OrigErr: errTestPlain, Err: "driver text",
		}))
		if !strings.Contains(got, "plain") {
			t.Errorf("fallback should surface OrigErr text, got %q", got)
		}
	})
	t.Run("conciseCause direct pg error", func(t *testing.T) {
		got := conciseCause(&pgconn.PgError{Code: "42601", Message: "syntax error"})
		if got != "syntax error (SQLSTATE 42601)" {
			t.Errorf("conciseCause = %q", got)
		}
	})
}

// TestForceMigrationVersionRange — the argument contract fails before
// any database connection is attempted.
func TestForceMigrationVersionRange(t *testing.T) {
	if err := ForceMigrationVersion("postgres://gseer@127.0.0.1:1/none", 30000, -1); err == nil {
		t.Error("negative version must be rejected")
	} else if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("wrong error: %v", err)
	}
	if err := ForceMigrationVersion("postgres://gseer@127.0.0.1:1/none", 30000, 999); err == nil {
		t.Error("version beyond head must be rejected")
	}
}

// TestDoctorDBChecksUnreachable — the FAIL + SKIP cascade: with an
// unreachable DB, doctor reports the root cause and skips dependents
// (never a wall of secondary failures).
func TestDoctorDBChecksUnreachable(t *testing.T) {
	results := DoctorDBChecks(context.Background(),
		"postgres://gseer@127.0.0.1:1/none?sslmode=disable&connect_timeout=1", 15)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3 (fail + 2 skips)", len(results))
	}
	if results[0].Outcome != CheckFail {
		t.Errorf("first check = %s, want FAIL", results[0].Outcome)
	}
	if results[1].Outcome != CheckSkip || results[2].Outcome != CheckSkip {
		t.Errorf("dependents must SKIP: %s, %s", results[1].Outcome, results[2].Outcome)
	}
	if results[0].Err == nil {
		t.Error("FAIL result must carry the structured DBError")
	}
}

// asDBError is errors.As specialized for *DBError (mirrored by the
// integration suite; defined here so unit tests can use it without the
// integration build tag).
func asDBError(err error, target **DBError) bool {
	for err != nil {
		if e, ok := err.(*DBError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
