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
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// doctor.go — the DB-side checks consumed by `gseer doctor` (QS-4).
// The storage layer assembles results; the CLI (M3 wiring in
// internal/cli/doctor.go) renders PASS/FAIL/HINT with fix lines.
// Doctor is strictly read-only: it never migrates and never forces.

// CheckOutcome classifies a doctor check line.
type CheckOutcome string

const (
	CheckPass CheckOutcome = "PASS"
	CheckFail CheckOutcome = "FAIL"
	CheckHint CheckOutcome = "HINT"
	CheckSkip CheckOutcome = "SKIP"
)

// CheckResult is one doctor line: what was checked, how it fared, and
// the fix instruction on failure.
type CheckResult struct {
	Name    string
	Outcome CheckOutcome
	Detail  string // human detail; built from redacted DSN only
	Fix     string // exact next command on FAIL/HINT
	// Err carries the structured DBError for FAIL outcomes (nil other-
	// wise) so the CLI can render the full Code/Why/Try shape.
	Err error
}

// DoctorDBChecks runs the M3 database checks in QS-4 order:
// reachable → PG version → migrations.  Each check that depends on a
// live connection SKIPs when the reachability check fails (doctor
// never hides the root cause behind a cascade of secondary failures).
func DoctorDBChecks(ctx context.Context, rawDSN string, minMajor int) []CheckResult {
	results := make([]CheckResult, 0, 3)

	// 1. Reachability.
	if err := CheckReachable(ctx, rawDSN); err != nil {
		results = append(results, CheckResult{
			Name:    "DB reachable",
			Outcome: CheckFail,
			Detail:  "cannot reach the database",
			Fix:     "start the database (compose stack: docker compose up -d db)",
			Err:     err,
		})
		// Everything below needs a live DB.
		results = append(results, CheckResult{
			Name: "PG version", Outcome: CheckSkip, Detail: "skipped (DB unreachable)",
		})
		results = append(results, CheckResult{
			Name: "migrations", Outcome: CheckSkip, Detail: "skipped (DB unreachable)",
		})
		return results
	}
	results = append(results, CheckResult{
		Name: "DB reachable", Outcome: CheckPass, Detail: MustRedactDSN(rawDSN),
	})

	// 2. PG version (PD-8).
	major, err := CheckPGVersion(ctx, rawDSN, minMajor)
	if err != nil {
		results = append(results, CheckResult{
			Name:    "PG version",
			Outcome: CheckFail,
			Detail:  fmt.Sprintf("server major %d", major),
			Fix:     "use PostgreSQL >= " + fmt.Sprint(minMajor) + " (the Compose stack ships postgres:17-alpine)",
			Err:     err,
		})
		results = append(results, CheckResult{
			Name: "migrations", Outcome: CheckSkip, Detail: "skipped (unsupported PG version)",
		})
		return results
	}
	results = append(results, CheckResult{
		Name: "PG version", Outcome: CheckPass, Detail: fmt.Sprintf("PostgreSQL %d (minimum %d)", major, minMajor),
	})

	// 3. Migration status.
	st, err := GetMigrationStatus(ctx, rawDSN)
	if err != nil {
		results = append(results, CheckResult{
			Name: "migrations", Outcome: CheckFail, Detail: "cannot read migration state", Err: err,
		})
		return results
	}
	switch st.State {
	case StateAtHead:
		results = append(results, CheckResult{
			Name: "migrations", Outcome: CheckPass,
			Detail: fmt.Sprintf("at head v%d", st.Head),
		})
	case StateBehind:
		results = append(results, CheckResult{
			Name: "migrations", Outcome: CheckFail,
			Detail: fmt.Sprintf("behind: database at v%d, binary head v%d", st.Version, st.Head),
			Fix:    "gseer setup --self-hosted",
		})
	case StateAhead:
		results = append(results, CheckResult{
			Name: "migrations", Outcome: CheckHint,
			Detail: fmt.Sprintf("database at v%d is NEWER than this binary's head v%d", st.Version, st.Head),
			Fix:    "update the gseer binary to match the database",
		})
	case StateDirty:
		results = append(results, CheckResult{
			Name: "migrations", Outcome: CheckFail,
			Detail: fmt.Sprintf("DIRTY at v%d", st.Version),
			Fix:    "gseer db migrate diagnose  (then, after inspection: gseer db migrate force --version N)",
		})
	case StateUninitialized:
		results = append(results, CheckResult{
			Name: "migrations", Outcome: CheckFail,
			Detail: "schema not initialized",
			Fix:    "gseer setup --self-hosted",
		})
	}
	return results
}

// PingPool is a helper for the server-start readiness path: the pool
// is created after migrations; this re-verifies liveness.
func PingPool(ctx context.Context, pool *pgxpool.Pool) error {
	pingCtx, cancel := context.WithTimeout(ctx, reachabilityTimeout)
	defer cancel()
	return pool.Ping(pingCtx)
}
