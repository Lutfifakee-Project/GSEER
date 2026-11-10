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

// REL-01 regression: the module root gained a second main package so
// `go install github.com/Lutfifakee-Project/GSEER@latest` works.  The
// independent audit asked whether that introduces ambiguous or
// duplicated entry-point behavior.  These tests answer it with the
// actual binaries: both entry points are compiled from this module and
// must behave IDENTICALLY (same stdout, same exit code) for every
// representative invocation, because both contain no logic beyond the
// single cli.Execute dispatch call.
package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// entrypoints holds the canonical release entry point (built by
// GoReleaser, .goreleaser.yaml builds.main) and the module-root entry
// point (resolved by the short-form `go install ...@latest`).
func entrypoints(t *testing.T) (canonical, rootMain string) {
	t.Helper()
	dir := t.TempDir()
	canonical = filepath.Join(dir, "gseer-canon")
	rootMain = filepath.Join(dir, "gseer-root")
	build := func(out, pkg string) {
		t.Helper()
		cmd := exec.Command("go", "build", "-o", out, pkg)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("go build %s: %v\n%s", pkg, err, stderr.String())
		}
	}
	build(canonical, "./cmd/gseer")
	build(rootMain, ".")
	return canonical, rootMain
}

type runResult struct {
	stdout string
	stderr string
	code   int
}

func runBinary(t *testing.T, bin string, args ...string) runResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), "GSEER_NO_COLOR=1", "NO_COLOR=1", "TERM=dumb")
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s %v: %v", bin, args, err)
	}
	return runResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// TestEntrypointsAreBehaviorallyIdentical is the REL-01 proof: for each
// probe invocation the canonical cmd/gseer binary and the module-root
// binary must produce byte-identical stdout and the same exit code.
// The probes cover the three exit-code regimes of the AC-5.x matrix
// that are reachable without a database: success (version), usage help
// (--help), and unknown-command failure (exit 1 via the main dispatch
// contract).
func TestEntrypointsAreBehaviorallyIdentical(t *testing.T) {
	if testing.Short() {
		t.Skip("REL-01 equivalence build test skipped in -short mode (requires the Go toolchain to compile both entry points)")
	}
	canonical, rootMain := entrypoints(t)

	probes := [][]string{
		{"version"},
		{"--help"},
		{"definitely-not-a-gseer-command"},
	}
	for _, probe := range probes {
		probe := probe
		t.Run(probe[0], func(t *testing.T) {
			want := runBinary(t, canonical, probe...)
			got := runBinary(t, rootMain, probe...)
			if got.code != want.code {
				t.Errorf("entrypoint divergence (REL-01): exit code = %d, canonical = %d, args = %v", got.code, want.code, probe)
			}
			if got.stdout != want.stdout {
				t.Errorf("entrypoint divergence (REL-01): stdout differs for args %v\nroot:\n%s\ncanonical:\n%s", probe, got.stdout, want.stdout)
			}
			if got.stderr != want.stderr {
				t.Errorf("entrypoint divergence (REL-01): stderr differs for args %v\nroot:\n%s\ncanonical:\n%s", probe, got.stderr, want.stderr)
			}
		})
	}
}

// TestRootMainContainsNoSecondCommandTree guards the structural
// invariant that keeps the two entry points from diverging: the
// module-root main.go must not register, alias, or wrap any cobra
// command of its own.  Its import block must stay free of any command
// framework and its only cli-package reference must be the single
// cli.Execute dispatch call — everything else lives in internal/cli.
func TestRootMainContainsNoSecondCommandTree(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	// Inspect the import block (not comments) so documentation text
	// cannot trip the guard; the invariant is about code.
	start := bytes.Index(src, []byte("import ("))
	if start < 0 {
		t.Fatalf("cannot locate import block in main.go")
	}
	end := bytes.Index(src[start:], []byte("\n)"))
	if end < 0 {
		t.Fatalf("cannot locate import block terminator in main.go")
	}
	imports := src[start : start+end]
	for _, forbidden := range []string{"cobra", "internal/cli/"} {
		if bytes.Contains(imports, []byte(forbidden)) {
			t.Errorf("REL-01: root main.go imports %q; it must remain a pure cli.Execute delegate", forbidden)
		}
	}
	const dispatch = "cli.Execute()"
	if n := bytes.Count(src, []byte(dispatch)); n != 1 {
		t.Errorf("REL-01: root main.go must contain exactly one %s call, found %d", dispatch, n)
	}
}
