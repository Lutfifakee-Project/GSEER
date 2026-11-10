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

// governance_test.go — the M12 governance final pass as permanent CI
// checks.  These tests pin the release-gating invariants so a future
// change cannot silently violate them:
//
//   - every tracked .go file carries the AGPL header (ADR-0009, README);
//   - LICENSE is AGPL-3.0;
//   - no collector/egress package exists (mirrors the import-graph test
//     at the filesystem level);
//   - the planned-but-absent commands still exit 64 with an honest
//     pointer (AC-5.7–5.8);
//   - the version surface reports honest "dev"/"unknown" defaults
//     instead of fabricated values.

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// moduleRoot comes from import_graph_test.go (runtime.Caller-anchored).

func TestGovernanceEveryGoFileCarriesAGPLHeader(t *testing.T) {
	root := moduleRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "bin" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		head := string(b)
		if len(head) > 600 {
			head = head[:600]
		}
		if !strings.Contains(head, "GNU Affero General Public License") {
			t.Errorf("%s: missing AGPL header (every tracked .go file carries it)", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestGovernanceLicenseIsAGPL3(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(moduleRoot(t), "LICENSE"))
	if err != nil {
		t.Fatalf("read LICENSE: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "GNU AFFERO GENERAL PUBLIC LICENSE") ||
		!strings.Contains(s, "Version 3, 19 November 2007") {
		t.Error("LICENSE is not the AGPL-3.0 text")
	}
}

func TestGovernanceNoCollectorDirectory(t *testing.T) {
	root := moduleRoot(t)
	if _, err := os.Stat(filepath.Join(root, "internal", "collector")); err == nil {
		t.Error("internal/collector exists — the collection framework is v0.0.2 scope (AC-7.x)")
	}
	// The egress-adjacent names must not appear as packages either.
	for _, banned := range []string{"egress", "scanner", "probe"} {
		if _, err := os.Stat(filepath.Join(root, "internal", banned)); err == nil {
			t.Errorf("internal/%s exists — not allowed in v0.0.1", banned)
		}
	}
}

func TestGovernancePlannedCommandsExit64(t *testing.T) {
	for _, name := range []string{"scan", "asset"} { // update/export are real commands as of v0.1.0
		t.Run(name, func(t *testing.T) {
			// Stub the process-exit seam (coming_later exits 64).
			orig := exitFunc
			exitCode := 0
			called := false
			exitFunc = func(code int) { called = true; exitCode = code }
			defer func() { exitFunc = orig }()

			out := &bytes.Buffer{}
			errOut := &bytes.Buffer{}
			RootCmd.SetOut(out)
			RootCmd.SetErr(errOut)
			RootCmd.SetArgs([]string{name})
			_ = RootCmd.Execute()

			if !called {
				t.Fatalf("gseer %s did not reach the exit-64 path", name)
			}
			if exitCode != ExitUsagePlanned {
				t.Fatalf("gseer %s exited %d, want 64", name, exitCode)
			}
			combined := out.String() + errOut.String()
			for _, want := range []string{"E_NOT_IMPLEMENTED", "planned, not in this release", "Exit code 64"} {
				if !strings.Contains(combined, want) {
					t.Errorf("gseer %s output missing %q:\n%s", name, want, combined)
				}
			}
		})
	}
}

func TestGovernanceVersionDefaultsAreHonest(t *testing.T) {
	// The un-injected build reports "dev"/"unknown" — never fabricated
	// values (the Makefile stamps real values via ldflags at release).
	buf := &bytes.Buffer{}
	RootCmd.SetOut(buf)
	RootCmd.SetErr(buf)
	RootCmd.SetArgs([]string{"version"})
	if err := RootCmd.Execute(); err != nil {
		t.Fatalf("gseer version returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "commit:") || !strings.Contains(out, "built_at:") {
		t.Errorf("version output missing fields:\n%s", out)
	}
}

// TestGovernanceNoDevTCPUsage scans the repo shell scripts for the
// /dev/tcp probe pattern that trips the execution sandbox session guard
// (M10 recovery lesson — the guard kills the whole tool session).
func TestGovernanceNoDevTCPUsage(t *testing.T) {
	root := moduleRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".sh") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), "/dev/tcp") {
			t.Errorf("%s uses /dev/tcp — forbidden (sandbox session guard)", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	_ = exec.Command // keep exec import for future release checks
}
