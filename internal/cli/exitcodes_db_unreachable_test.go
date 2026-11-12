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

// exitcodes_db_unreachable_test.go — regression pins for the DEPLOYMENT.md
// §3.4 exit-code table after the v0.3.1 audit of the DB-unreachable paths.
//
// The two surfaces really do exit differently, and the table must name
// the right one for each:
//
//   - `collect status` / `source` / `collect run` against a database that
//     is UNREACHABLE AT OPEN fail in openAppPool's pre-query reachability
//     gate (postgres.CheckReachable → E_DB_UNREACHABLE).  That error is
//     RETURNED from RunE, so the process exit is the generic error path
//     (1, via cmd/gseer/main.go) — NOT exit 4.
//   - once the store is OPEN, a query failure (schema not provisioned,
//     mid-session DB loss) surfaces through `E_DB: …` +
//     exitWith(ExitUnavailable) — exit 4.
//
// This file pins the unreachable-at-open path WITHOUT any PostgreSQL
// dependency (a DSN aimed at a just-closed loopback port).  The post-open
// exit-4 path needs a reachable database with no schema and is pinned in
// exitcodes_db_unreachable_integration_test.go (build tag `integration`).

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// selfHostedConfigForTest writes the minimal self-hosted config the
// data-plane commands load (both DSNs pointed at the same target so the
// runtime-credential preference has nothing to fall back from).
func selfHostedConfigForTest(t *testing.T, dsn string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "profile: self-hosted\ndatabase:\n  dsn: '" + dsn + "'\n  runtime_dsn: '" + dsn + "'\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write self-hosted config: %v", err)
	}
	return path
}

// deadPortDSN returns a DSN aimed at a loopback port that was reserved
// and immediately released — a deterministic connection refusal without
// a fixed magic port.  If some other process binds the port afterwards,
// CheckReachable still fails (the ping is not PostgreSQL), so the pin
// degrades to a loud test failure, never a false pass.
func deadPortDSN(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}
	return fmt.Sprintf("postgres://gseer:not-a-real-password@127.0.0.1:%d/gseer?sslmode=disable", port)
}

// exitCodeGuard records exitWith/exitFunc invocations instead of ending
// the test process.  A non-nil recorded code in the unreachable-at-open
// scenarios is the regression the docs fix depends on.
type exitCodeGuard struct{ called *int }

func pinExitFunc(t *testing.T) *exitCodeGuard {
	t.Helper()
	orig := exitFunc
	g := &exitCodeGuard{}
	exitFunc = func(code int) { g.called = &code }
	t.Cleanup(func() { exitFunc = orig })
	return g
}

// dataPlaneDeadDBCommands is every registry/collection surface the
// exit-code audit covered; each must fail through RunE's error return
// when the database is unreachable at open.
var dataPlaneDeadDBCommands = [][]string{
	{"collect", "status"},
	{"source", "list"},
	{"collect", "run", "--all"},
}

func TestDBUnreachableAtOpenExitsViaGenericErrorPath(t *testing.T) {
	cfg := selfHostedConfigForTest(t, deadPortDSN(t))
	restore := captureRootOutput(t)
	defer restore()

	guard := pinExitFunc(t)
	for _, args := range dataPlaneDeadDBCommands {
		out, err := executeRoot(t, append([]string{"--config", cfg}, args...)...)
		if err == nil {
			t.Fatalf("%v: expected the reachability-gate error, got success\noutput:\n%s", args, out)
		}
		if !strings.Contains(err.Error(), "E_DB_UNREACHABLE") {
			t.Fatalf("%v: expected E_DB_UNREACHABLE from the pre-query reachability gate, got: %v\noutput:\n%s", args, err, out)
		}
		// The error surfaces through the generic path in the error style
		// guide's shape (what failed → Why → Try) — DEPLOYMENT.md row 1.
		for _, frag := range []string{"Why:", "Try:"} {
			if !strings.Contains(err.Error(), frag) {
				t.Fatalf("%v: E_DB_UNREACHABLE is missing the style-guide %s line\nerror:\n%s", args, frag, err)
			}
		}
		if guard.called != nil {
			t.Fatalf("%v: exitWith(%d) was invoked — the unreachable-at-open path must stay on the generic error path (main exits 1), not exitWith", args, *guard.called)
		}
	}
}
