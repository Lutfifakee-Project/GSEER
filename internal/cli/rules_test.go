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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rules_test.go — the `gseer rules` surface (v0.1.0 M18): list renders
// the loaded collection, validate lints and runs regression tests, and
// the exit-code contract holds (0 = clean, 2 = lint problems).

// captureExit swaps the process-exit seam for a recording stub, runs
// fn, and returns the exit code exitWith was called with (and whether
// it was called at all).  Restored via t.Cleanup.
func captureExit(t *testing.T, fn func()) (code int, called bool) {
	t.Helper()
	orig := exitFunc
	exitFunc = func(c int) { called = true; code = c }
	t.Cleanup(func() { exitFunc = orig })
	fn()
	return code, called
}

func TestRulesListShowsSeedCollection(t *testing.T) {
	out, err := executeRoot(t, "rules", "list")
	if err != nil {
		t.Fatalf("rules list: %v", err)
	}
	for _, want := range []string{"web-server-nginx", "cms-wordpress", "language-php", "framework-django-admin", "web-tech-cloudflare", "service-openssh", "docs/RULES.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("rules list output missing %q", want)
		}
	}
}

func TestRulesListJSON(t *testing.T) {
	t.Cleanup(func() { flags.output = "" }) // don't leak -o json into other tests
	out, err := executeRoot(t, "rules", "list", "-o", "json")
	if err != nil {
		t.Fatalf("rules list json: %v", err)
	}
	if !strings.Contains(out, `"product": "nginx"`) || !strings.Contains(out, `"version_capable": true`) {
		t.Errorf("json output incomplete: %s", out)
	}
}

func TestRulesValidateDefaultSeedClean(t *testing.T) {
	out, err := executeRoot(t, "rules", "validate")
	if err != nil {
		t.Fatalf("rules validate: %v — output:\n%s", err, out)
	}
	if !strings.Contains(out, "all ") || !strings.Contains(out, "pass lint and their regression tests") {
		t.Errorf("validate output lacks the pass line: %s", out)
	}
}

func TestRulesValidateDirContract(t *testing.T) {
	dir := t.TempDir()
	good := `
id: op-rule
family: web-server
product: operator-product
confidence: 0.9
provenance: operator-authored test rule used by the CLI contract test
matches:
  - field: http.server
    pattern: '^op-product/([\d.]+)$'
    version_group: 1
tests:
  - evidence: {http: {server: "op-product/1.0.0"}}
    match: true
    version: "1.0.0"
  - evidence: {http: {server: "other/1.0.0"}}
    match: false
`
	if err := os.WriteFile(filepath.Join(dir, "op-rule.yaml"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := executeRoot(t, "rules", "validate", "--dir", dir)
	if err != nil {
		t.Fatalf("validate good dir: %v — %s", err, out)
	}
	if !strings.Contains(out, "all 1 rules pass") {
		t.Errorf("good dir output: %s", out)
	}

	// A broken contribution (missing negative test) must fail with exit
	// code 2 (ExitInvalidInput) and a pointer to the docs.
	bad := strings.Replace(good, "  - evidence: {http: {server: \"other/1.0.0\"}}\n    match: false\n", "", 1)
	if err := os.WriteFile(filepath.Join(dir, "op-rule.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	out = "" // reset before the broken-rule runs
	code, called := captureExit(t, func() {
		out, _ = executeRoot(t, "rules", "validate", "--dir", dir)
	})
	if !called || code != ExitInvalidInput {
		t.Errorf("broken rule: want exit %d via seam, got called=%v code=%d", ExitInvalidInput, called, code)
	}
	if !strings.Contains(out, "no negative") {
		t.Errorf("broken rule output lacks the no-negative problem: %s", out)
	}

	// A failing false-positive guard is a validate failure too: the
	// unanchored pattern matches the negative evidence as a substring.
	fp := strings.Replace(good, `pattern: '^op-product/([\d.]+)$'`, `pattern: 'op-product'`, 1)
	fp = strings.Replace(fp, `"other/1.0.0"`, `"my-op-product/2.0"`, 1)
	if err := os.WriteFile(filepath.Join(dir, "op-rule.yaml"), []byte(fp), 0o600); err != nil {
		t.Fatal(err)
	}
	code, called = captureExit(t, func() {
		out, _ = executeRoot(t, "rules", "validate", "--dir", dir)
	})
	if !called || code != ExitInvalidInput || !strings.Contains(out, "false-positive guard failed") {
		t.Errorf("FP guard: want exit %d + guard failure, got called=%v code=%d out=%s", ExitInvalidInput, called, code, out)
	}

	// Missing dir is a usage error.
	if _, err := executeRoot(t, "rules", "validate", "--dir", filepath.Join(dir, "missing")); err == nil {
		t.Error("missing dir accepted")
	}
}

func TestRulesValidateJSONReportsProblems(t *testing.T) {
	t.Cleanup(func() { flags.output = "" }) // don't leak -o json into other tests
	dir := t.TempDir()
	bad := `
id: Bad_ID
family: web-server
product: x
confidence: 2.0
provenance: ""
matches: []
tests: []
`
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	var out string
	code, called := captureExit(t, func() {
		out, _ = executeRoot(t, "rules", "validate", "--dir", dir, "-o", "json")
	})
	if !called || code != ExitInvalidInput {
		t.Errorf("want exit %d via seam, got called=%v code=%d", ExitInvalidInput, called, code)
	}
	for _, want := range []string{`"rules": 1`, `"detail"`, "not kebab-case", "outside [0,1]", "no match specs"} {
		if !strings.Contains(out, want) {
			t.Errorf("json problems output missing %q", want)
		}
	}
}
