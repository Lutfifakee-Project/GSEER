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

package cli

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestImportGraphSearchNotScan (AC-3.1) pins the Search ≠ Scan seam
// (ADR-0010) from the very first commit of the v0.0.1 module.
//
// It runs `go list -deps ./...` and asserts that no package in the
// dependency graph of the search/server/CLI surfaces matches any
// collector, egress, observation, or active-scan package.  In v0.0.1 no
// such package exists in the module at all; this test fails the moment
// one is introduced outside the gated Phase 2 framework.
//
// The forbidden substrings cover the SPEC-02 §2 collector subtree:
//
//	internal/collector/contract
//	internal/collector/pipeline
//	internal/collector/passive
//	internal/collector/observation
//	internal/collector/scope
//	internal/collector/audit
//	internal/collector/egress
//
// We match on the module-relative path so external dependencies whose
// names happen to contain "egress" or "scope" (e.g. a vendored utility)
// are not flagged.
func TestImportGraphSearchNotScan(t *testing.T) {
	if testing.Short() {
		t.Skip("import-graph test runs `go list -deps`; skip in -short mode")
	}
	goBin := goBinary(t)
	modroot := moduleRoot(t)

	// Enumerate the deps of every package that participates in search,
	// server, or CLI surfaces.  Adding a package here is a deliberate
	// architectural decision recorded in the milestone report.
	targets := []string{
		"./cmd/gseer",
		"./internal/cli",
		"./internal/search",
		"./internal/api",
		"./internal/dashboard",
		"./internal/gql/...",
	}

	args := []string{"list", "-deps"}
	args = append(args, targets...)
	cmd := exec.Command(goBin, args...)
	cmd.Dir = modroot
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps failed: %v\nstderr: %s", err, errOutput(err))
	}

	forbiddenSubstrings := []string{
		"github.com/Lutfifakee-Project/GSEER/internal/collector/",
	}
	// Additional in-module path fragments that would indicate a collector
	// sub-package was added outside the gated framework.  We match on the
	// module path prefix so external deps are not flagged.
	for _, frag := range []string{
		"/collector/contract",
		"/collector/pipeline",
		"/collector/passive",
		"/collector/observation",
		"/collector/scope",
		"/collector/audit",
		"/collector/egress",
	} {
		forbiddenSubstrings = append(forbiddenSubstrings, "github.com/Lutfifakee-Project/GSEER"+frag)
	}

	violations := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, frag := range forbiddenSubstrings {
			if strings.Contains(line, frag) {
				violations = append(violations, line+" (matches "+frag+")")
			}
		}
	}
	if len(violations) > 0 {
		t.Errorf("AC-3.1 violation: search/server/CLI surface depends on a collector/egress package:\n  %s\n"+
			"Search must never scan (ADR-0010).  In v0.0.1 no such package should exist.",
			strings.Join(violations, "\n  "))
	}
}

// TestNoCollectorPackageExists verifies the v0.0.1 module does not
// contain an internal/collector/ directory at all (AC-7.x active
// observation absence).  This is a stronger guarantee than "gated": the
// package is simply absent from the module until Phase 2.
func TestNoCollectorPackageExists(t *testing.T) {
	modroot := moduleRoot(t)
	collectorDir := filepath.Join(modroot, "internal", "collector")
	if _, err := exec.Command("test", "-d", collectorDir).CombinedOutput(); err == nil {
		t.Errorf("AC-7.x violation: %s exists — internal/collector/ must be ABSENT from v0.0.1", collectorDir)
	}
}

// TestNoPolicyActiveConfigKeyAccepted is deferred to M2.
//
// The M2 config package owns the AC-7.x enforcement: config validation
// rejects any `policy.active.*` key with a pointer to v0.0.2.  A source
// grep for the literal "policy.active" in M1 would fire on the very doc
// comments that describe the rejection (a false positive), and the
// acceptance path it would guard against does not exist until M2 lands
// the config schema.  M2 will add a runtime test that exercises the
// rejection handler directly.

// goBinary returns the path to the Go binary running this test.  runtime
// gives us GOROOT; we use $GOROOT/bin/go so the test always uses the
// same toolchain that compiled it.
func goBinary(t *testing.T) string {
	t.Helper()
	root := runtime.GOROOT()
	if root == "" {
		// Fall back to PATH lookup.
		bin, err := exec.LookPath("go")
		if err != nil {
			t.Fatalf("go binary not found (no GOROOT, not on PATH)")
		}
		return bin
	}
	return filepath.Join(root, "bin", "go")
}

// moduleRoot returns the directory of the test's source file, which is
// the cli package directory; `go list` run from there resolves the
// module root automatically.  We use runtime.Caller for portability.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// file is .../internal/cli/import_graph_test.go; the module root is
	// four directories up (internal/cli -> internal -> GSEER).
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// errOutput extracts stderr from an *exec.ExitError for diagnostics.
func errOutput(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		return string(ee.Stderr)
	}
	return ""
}
