//go:build integration

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

package cli

// exitcodes_db_unreachable_integration_test.go — the post-open half of
// the exit-code pin (see exitcodes_db_unreachable_test.go): with the
// store OPEN (database reachable, schema NOT provisioned), `collect
// status` / `source list` / `collect run --all` print the `E_DB: …`
// failure surface and terminate through exitWith(ExitUnavailable) —
// exit 4.  Run under -tags integration (CI DB-integration jobs; needs
// GSEER_TEST_PG_ADMIN like the other cliTestDB suites).

import (
	"strings"
	"testing"
)

func TestDBQueryFailureAfterOpenExitsUnavailable(t *testing.T) {
	dsn, cleanup := cliTestDB(t)
	defer cleanup()
	cfg := selfHostedConfigForTest(t, dsn)
	restore := captureRootOutput(t)
	defer restore()

	guard := pinExitFunc(t)
	for _, args := range dataPlaneDeadDBCommands {
		out, err := executeRoot(t, append([]string{"--config", cfg}, args...)...)
		if err != nil {
			t.Fatalf("%v: post-open failures exit through exitWith — RunE must return nil, got: %v\noutput:\n%s", args, err, out)
		}
		if guard.called == nil {
			t.Fatalf("%v: expected exitWith(ExitUnavailable), but exitWith was never called\noutput:\n%s", args, out)
		}
		if *guard.called != ExitUnavailable {
			t.Fatalf("%v: expected exitWith(%d), got exitWith(%d)\noutput:\n%s", args, ExitUnavailable, *guard.called, out)
		}
		if !strings.Contains(out, "E_DB:") {
			t.Fatalf("%v: expected the E_DB failure surface after the store opened, got:\n%s", args, out)
		}
	}
}
