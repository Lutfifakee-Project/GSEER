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

// fresh_install_test.go — the Fresh Install Acceptance Test (v0.3.0):
// simulate a user who has never seen GSEER.  Build the REAL binary,
// point HOME at an empty directory, and run the acceptance flow:
//
//      bare `gseer`   → auto-initializes personal mode + explains data
//      gseer doctor   → healthy install, honest EMPTY index
//      load-fixtures  → labeled synthetic corpus behind the consent gate
//      search         → real query engine returns the expected row
//      db backup      → consistent snapshot file (0600)
//      db restore     → staged, validated, recoverable replacement; data intact afterwards
//
// If this flow ever needs a developer standing next to the user, the
// Easy-to-Use principle is broken — this test is the guardrail.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// contextWithTimeout is a small seam so the file reads top-down.
func contextWithTimeout(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), d)
}

// mapToEnv converts a string map into os.Environ form (sorted for
// determinism).
func mapToEnv(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// buildFreshBinary compiles the canonical release entry point.
func buildFreshBinary(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain unavailable — cannot build the fresh-install binary")
	}
	bin := filepath.Join(t.TempDir(), "gseer")
	cmd := exec.Command(goBin, "build", "-o", bin, "./cmd/gseer")
	// The test binary runs inside internal/cli; the module root is two
	// levels up.
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fresh binary: %v\n%s", err, out)
	}
	return bin
}

// freshRun executes the built binary as a user would: isolated HOME,
// no inherited GSEER_* state, real stdout captured for assertions.
func freshRun(t *testing.T, bin string, home string, env map[string]string, args ...string) string {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = home
	base := map[string]string{
		"HOME":   home,
		"PATH":   "/usr/bin:/bin",
		"TMPDIR": os.TempDir(),
	}
	for k, v := range env {
		base[k] = v
	}
	cmd.Env = mapToEnv(base)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gseer %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestFreshInstallAcceptance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission assertions do not apply on windows")
	}
	if testing.Short() {
		t.Skip("builds and drives the real binary")
	}
	bin := buildFreshBinary(t)
	home := t.TempDir()
	fixtureEnv := map[string]string{"GSEER_POLICY_EVALUATION_FIXTURES": "true"}

	// 1. First run — bare `gseer` initializes personal mode and tells
	// the user how data enters the index.  Zero flags, zero config.
	out := freshRun(t, bin, home, nil)
	for _, want := range []string{
		"Welcome to GSEER — internet-asset search engine",
		"personal database",
		"config written",
		"Where does the data come from?",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("first run output missing %q:\n%s", want, out)
		}
	}

	// 2. Doctor — a healthy install with an honestly EMPTY index.
	out = freshRun(t, bin, home, nil, "doctor")
	if !strings.Contains(out, "4 PASS, 0 FAIL") || !strings.Contains(out, "EMPTY") {
		t.Errorf("doctor output unexpected:\n%s", out)
	}

	// 3. Load the labeled synthetic corpus (consent granted via policy).
	out = freshRun(t, bin, home, fixtureEnv, "setup", "--load-fixtures")
	if !strings.Contains(out, "fixtures loaded") {
		t.Errorf("load-fixtures output:\n%s", out)
	}

	// 4. Search — the query engine returns the expected synthetic row.
	out = freshRun(t, bin, home, nil, "search", `port==443 && title CONTAINS "login"`)
	if !strings.Contains(out, "SYNTHETIC") || !strings.Contains(out, "1 result") {
		t.Errorf("search output unexpected:\n%s", out)
	}

	// 5. Backup — a consistent snapshot file with 0600.
	backup := filepath.Join(home, "backup.db")
	out = freshRun(t, bin, home, nil, "db", "backup", "--out", backup)
	if !strings.Contains(out, "backup written") {
		t.Errorf("backup output:\n%s", out)
	}
	if st, err := os.Stat(backup); err != nil {
		t.Errorf("backup file missing: %v", err)
	} else if st.Size() == 0 {
		t.Errorf("backup file is empty")
	} else if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("backup mode = %04o, want 0600", perm)
	}

	// 6. Restore — atomic replace; the data survives.
	out = freshRun(t, bin, home, nil, "db", "restore", backup, "--yes")
	if !strings.Contains(out, "restored") {
		t.Errorf("restore output:\n%s", out)
	}
	out = freshRun(t, bin, home, nil, "search", `port==443`)
	if !strings.Contains(out, "2 result") {
		t.Errorf("search after restore unexpected:\n%s", out)
	}
}
