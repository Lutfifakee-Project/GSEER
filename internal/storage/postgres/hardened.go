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

// hardened.go — Go-side execution of the Rev 6.7.2 hardened-deployment
// artifacts (roadmap M-B and M-C; OD-1/OD-2/OD-5).
//
// The three pinned scripts live in deploy/sql (embedded via the deploy
// package).  This file is ONLY a runner: it never re-implements their
// SQL, never repairs a failed state, and never widens a grant.  The
// failure direction is always fail-closed — a failed bootstrap, grant,
// or audit stops the deployment and names what to fix.
//
// Runners:
//
//      BootstrapRoles    — step 1: create/validate gseer_policy_owner +
//                          gseer_app + the D-47 membership (refuse-first).
//      ApplyRuntimeGrants — step 3: the §5.2 least-privilege grant matrix
//                          for gseer_app (idempotent, additive-only).
//      RunPrivilegeAudit — step 4: the fail-closed effective-privilege
//                          audit (read-only; GSUPF on any failure).
//      SetAppRolePassword — bootstrap companion: set gseer_app's password
//                          at role-creation time (the bootstrap script
//                          itself never contains or generates credentials).
//
// The order is normative (docs/DEPLOYMENT.md §3.1): bootstrap → migrate
// → grants → audit → start the server with the gseer_app credential.

package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Lutfifakee-Project/GSEER/deploy"
)

// Hardened role names (pinned by the artifacts; not configurable).
const (
	RoleApp         = "gseer_app"
	RolePolicyOwner = "gseer_policy_owner"
)

// gsupfErrCode is the custom ERRCODE raised by the audit (and the 0016
// guard) with the complete failure list in the error message.
const gsupfErrCode = "GSUPF"

// AuditReport is the structured result of one privilege-audit run.
type AuditReport struct {
	// Passed is true when the audit completed without failures.
	Passed bool
	// Notices carries the audit's RAISE NOTICE lines (reachable-role
	// walk, TEMPORARY source, the final PASSED line) in order.
	Notices []string
	// Failures is the complete GSUPF failure list (empty when passed).
	Failures []string
}

// runDeploymentScript opens one dedicated connection as the configured
// (operator) credential, installs a NOTICE collector, and executes the
// given SQL as a single statement batch.  It returns the notices; any
// SQL error is returned as-is (the caller renders it fail-closed).
func runDeploymentScript(ctx context.Context, dsn, sql string) ([]string, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, unreachableError(err, dsn)
	}
	var notes []string
	cfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		notes = append(notes, n.Message)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return notes, unreachableError(err, dsn)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	if _, err := conn.Exec(ctx, sql); err != nil {
		return notes, err
	}
	return notes, nil
}

// BootstrapRoles executes the pinned role-bootstrap script (step 1).
// Idempotent; refuse-first on unsafe pre-existing roles.  Must run as a
// credential that may create roles (the database owner in the guided
// deployments).
func BootstrapRoles(ctx context.Context, dsn string) ([]string, error) {
	return runDeploymentScript(ctx, dsn, deploy.BootstrapRolesSQL)
}

// ApplyRuntimeGrants executes the pinned §5.2 grant script (step 3).
// The pinned artifact names the default database (`ON DATABASE gseer`);
// for a deployment database with another name, exactly that token is
// substituted with the actual (identifier-quoted) database name — the
// only transformation this runner performs, and it reports the
// substitution as a notice so the change is never silent.
func ApplyRuntimeGrants(ctx context.Context, dsn string) ([]string, error) {
	sql := deploy.RuntimeGrantsSQL
	dbName := databaseNameOf(dsn)
	if dbName != "" && dbName != "gseer" {
		sql = strings.Replace(sql,
			"ON DATABASE gseer TO",
			"ON DATABASE "+quoteIdent(dbName)+" TO", 1)
	}
	return runDeploymentScript(ctx, dsn, sql)
}

// RunPrivilegeAudit executes the pinned fail-closed audit (step 4).
// The audit is read-only.  A GSUPF failure is returned as a populated
// AuditReport (Passed=false, Failures=complete list) with a nil error —
// the CALLER decides the fail-closed action (refuse startup); a nil
// error with Passed=true means the gate is open.  Any other error
// (connectivity, syntax, unexpected SQLSTATE) is returned as a real
// error: an audit that could not RUN is not an audit that passed.
func RunPrivilegeAudit(ctx context.Context, dsn string) (AuditReport, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return AuditReport{}, unreachableError(err, dsn)
	}
	report := AuditReport{}
	cfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		report.Notices = append(report.Notices, n.Message)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return report, unreachableError(err, dsn)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	if _, err := conn.Exec(ctx, deploy.EffectivePrivilegeAuditSQL); err != nil {
		var pgErr *pgconn.PgError
		if isGSUPF(err, &pgErr) {
			report.Failures = parseGSUPFFailures(pgErr.Message)
			return report, nil
		}
		return report, err
	}
	report.Passed = true
	return report, nil
}

// SetAppRolePassword sets the gseer_app login password.  It is used by
// `gseer setup --self-hosted` at role-creation time (the bootstrap
// script deliberately never handles credentials).  The statement is a
// bare ALTER ROLE — the password lives only in this call and the
// caller's own secret channel.
func SetAppRolePassword(ctx context.Context, dsn, password string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return unreachableError(err, dsn)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	_, err = conn.Exec(ctx,
		fmt.Sprintf("ALTER ROLE %s WITH LOGIN PASSWORD %s",
			quoteIdent(RoleApp), quoteLiteral(password)))
	return err
}

// VerifyRuntimeIdentity asserts the pool's connecting user IS the
// gseer_app role (fail-closed).  The pinned audit evaluates gseer_app's
// effective privileges; this check closes the remaining gap — a
// superuser or operator DSN configured as the runtime credential would
// otherwise serve traffic with privileges the grant matrix never
// intended.  The identity check is cheap (one round trip) and runs on
// the same pool the server will serve from.
func VerifyRuntimeIdentity(ctx context.Context, pool *pgxpool.Pool) error {
	var user string
	if err := pool.QueryRow(ctx, `SELECT current_user`).Scan(&user); err != nil {
		return err
	}
	if user != RoleApp {
		return fmt.Errorf("connecting user %q is not %q", user, RoleApp)
	}
	return nil
}

// RoleExists reports whether a login role exists (used by doctor to
// keep its SKIP line honest when the hardened roles were never
// provisioned).
func RoleExists(ctx context.Context, dsn, role string) (bool, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return false, unreachableError(err, dsn)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).
		Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// isGSUPF reports whether err is the audit's GSUPF failure.
func isGSUPF(err error, pgErr **pgconn.PgError) bool {
	for _, e := range unwrapAll(err) {
		if pe, ok := e.(*pgconn.PgError); ok && pe.Code == gsupfErrCode {
			*pgErr = pe
			return true
		}
	}
	return false
}

// unwrapAll flattens an error chain (migrate-style wrappers included).
func unwrapAll(err error) []error {
	var out []error
	for e := err; e != nil; e = unwrapOnce(e) {
		out = append(out, e)
	}
	return out
}

func unwrapOnce(err error) error {
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		return u.Unwrap()
	}
	return nil
}

// parseGSUPFFailures extracts the failure list from the audit exception
// message: "EFFECTIVE-PRIVILEGE AUDIT FAILED with N failure(s): a | b | c".
func parseGSUPFFailures(message string) []string {
	const sep = "failure(s): "
	idx := strings.Index(message, sep)
	if idx < 0 {
		return []string{message}
	}
	list := message[idx+len(sep):]
	parts := strings.Split(list, " | ")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = append(out, message)
	}
	return out
}

// databaseNameOf extracts the database name from a DSN ("" when it
// cannot be determined; the caller then leaves the pinned script
// untouched and the grant fails loudly on the wrong database rather
// than silently guessing).
func databaseNameOf(dsn string) string {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return ""
	}
	return cfg.Database
}

// quoteIdent renders a PostgreSQL identifier safely.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// quoteLiteral renders a PostgreSQL string literal safely.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
