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

package fprule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ruleYAML(t *testing.T, yml string) *Rule {
	t.Helper()
	r, err := ParseRule([]byte(yml))
	if err != nil {
		t.Fatalf("ParseRule: %v", err)
	}
	return r
}

func TestApplyBasics(t *testing.T) {
	r := ruleYAML(t, `
id: test-nginx
family: web-server
product: nginx
confidence: 0.9
provenance: test-only rule documenting the default Server header format
matches:
  - field: http.server
    pattern: '^nginx(?:/([\d.]+))?(\s*\(|$)'
    version_group: 1
tests:
  - evidence: {http: {server: "nginx/1.24.0"}}
    match: true
  - evidence: {http: {server: "not-nginx"}}
    match: false
`)
	reg := NewRegistry([]*Rule{r})

	t.Run("positive with version", func(t *testing.T) {
		hits := reg.Apply(map[string]any{"http": map[string]any{"server": "nginx/1.24.0"}})
		if len(hits) != 1 || hits[0].Product != "nginx" || hits[0].Version != "1.24.0" {
			t.Fatalf("hits = %+v", hits)
		}
		if hits[0].Confidence != 0.9 {
			t.Fatalf("confidence = %v, want 0.9", hits[0].Confidence)
		}
		if hits[0].Family != "web-server" || hits[0].RuleID != "test-nginx" {
			t.Fatalf("hit meta = %+v", hits[0])
		}
	})
	t.Run("no version capture", func(t *testing.T) {
		hits := reg.Apply(map[string]any{"http": map[string]any{"server": "nginx"}})
		if len(hits) != 1 || hits[0].Version != "" {
			t.Fatalf("hits = %+v", hits)
		}
	})
	t.Run("negative", func(t *testing.T) {
		if hits := reg.Apply(map[string]any{"http": map[string]any{"server": "not-nginx/1.0"}}); len(hits) != 0 {
			t.Fatalf("hits = %+v, want none", hits)
		}
	})
	t.Run("absent and non-string evidence never match", func(t *testing.T) {
		if hits := reg.Apply(map[string]any{"http": map[string]any{"server": 42}}); len(hits) != 0 {
			t.Fatalf("non-string matched: %+v", hits)
		}
		if hits := reg.Apply(map[string]any{}); len(hits) != 0 {
			t.Fatalf("empty evidence matched: %+v", hits)
		}
		if hits := reg.Apply(nil); len(hits) != 0 {
			t.Fatalf("nil evidence matched: %+v", hits)
		}
	})
}

func TestApplyWeightCompositing(t *testing.T) {
	r := ruleYAML(t, `
id: test-weighted
family: cms
product: test-cms
confidence: 0.8
provenance: test-only rule for weight compositing semantics
matches:
  - field: http.title
    pattern: 'exact title'
    weight: 0.5
  - field: http.server
    pattern: 'strong-header'
    weight: 1.0
tests:
  - evidence: {http: {title: "exact title"}}
    match: true
  - evidence: {http: {server: "strong-header"}}
    match: true
`)
	reg := NewRegistry([]*Rule{r})
	weak := reg.Apply(map[string]any{"http": map[string]any{"title": "exact title"}})
	if len(weak) != 1 || weak[0].Confidence != 0.4 {
		t.Fatalf("weak hit = %+v, want 0.8*0.5", weak)
	}
	strong := reg.Apply(map[string]any{"http": map[string]any{"server": "strong-header"}})
	if len(strong) != 1 || strong[0].Confidence != 0.8 {
		t.Fatalf("strong hit = %+v, want 0.8*1.0", strong)
	}
	// Both fields present: the best hit (higher weight) supplies confidence.
	both := reg.Apply(map[string]any{
		"http": map[string]any{"title": "exact title", "server": "strong-header"},
	})
	if len(both) != 1 || both[0].Confidence != 0.8 {
		t.Fatalf("both hit = %+v, want best 0.8", both)
	}
}

func TestApplyProductDedupDeterministic(t *testing.T) {
	a := ruleYAML(t, `
id: aaa-dup
family: web-server
product: dup-product
confidence: 0.9
provenance: test-only duplicate-product rule A
matches:
  - field: http.server
    pattern: 'aaa'
tests:
  - evidence: {http: {server: "aaa"}}
    match: true
`)
	b := ruleYAML(t, `
id: bbb-dup
family: web-server
product: dup-product
confidence: 0.7
provenance: test-only duplicate-product rule B
matches:
  - field: http.server
    pattern: 'bbb'
tests:
  - evidence: {http: {server: "bbb"}}
    match: true
`)
	reg := NewRegistry([]*Rule{a, b})
	hits := reg.Apply(map[string]any{"http": map[string]any{"server": "aaa bbb"}})
	if len(hits) != 1 {
		t.Fatalf("hits = %+v, want one deduped product", hits)
	}
	if hits[0].RuleID != "aaa-dup" || hits[0].Confidence != 0.9 {
		t.Fatalf("dedup kept %+v, want aaa-dup@0.9 (higher confidence wins)", hits[0])
	}
}

func TestApplySortedDeterministic(t *testing.T) {
	reg := Seed()
	hits := reg.Apply(map[string]any{"http": map[string]any{"server": "Apache/2.4.41 (Unix) PHP/7.4.12"}})
	if len(hits) < 2 {
		t.Fatalf("hits = %+v, want apache + php", hits)
	}
	if hits[0].Product != "apache-httpd" || hits[0].Confidence < hits[1].Confidence {
		t.Fatalf("sort order wrong: %+v", hits)
	}
	// language-php weight 0.9 * 0.85 = 0.765
	if hits[1].Product != "php" || hits[1].Confidence != 0.765 {
		t.Fatalf("php hit = %+v, want 0.765", hits[1])
	}
}

func TestApplyFailClosedOnBrokenPattern(t *testing.T) {
	broken := ruleYAML(t, `
id: broken-rule
family: web-server
product: broken
confidence: 0.9
provenance: test-only rule with an uncompilable pattern
matches:
  - field: http.server
    pattern: '(unclosed'
tests:
  - evidence: {http: {server: "x"}}
    match: true
`)
	reg := NewRegistry([]*Rule{broken})
	if reg.Healthy() {
		t.Fatal("registry with broken pattern reported healthy")
	}
	if hits := reg.Apply(map[string]any{"http": map[string]any{"server": "anything"}}); hits != nil {
		t.Fatalf("unhealthy registry applied: %+v", hits)
	}
	if len(reg.LintProblems()) != 1 {
		t.Fatalf("lint problems = %+v", reg.LintProblems())
	}
}

func TestLintAllBranches(t *testing.T) {
	base := `
id: ok-rule
family: web-server
product: ok
confidence: 0.9
provenance: test-only valid rule used as the lint baseline
matches:
  - field: http.server
    pattern: '^ok$'
tests:
  - evidence: {http: {server: "ok"}}
    match: true
  - evidence: {http: {server: "not-ok"}}
    match: false
`
	if problems := Lint([]*Rule{ruleYAML(t, base)}); len(problems) != 0 {
		t.Fatalf("baseline rule has lint problems: %v", problems)
	}

	cases := []struct {
		name string
		yml  string
		want string // substring expected in some problem detail
	}{
		{"empty id", strings.Replace(base, "id: ok-rule", "id: \"\"", 1), "id is empty"},
		{"bad id shape", strings.Replace(base, "id: ok-rule", "id: Bad_ID", 1), "not kebab-case"},
		{"unknown family", strings.Replace(base, "family: web-server", "family: scanner", 1), "unknown family"},
		{"empty product", strings.Replace(base, "product: ok", "product: \"\"", 1), "product is empty"},
		{"confidence high", strings.Replace(base, "confidence: 0.9", "confidence: 1.5", 1), "outside [0,1]"},
		{"confidence negative", strings.Replace(base, "confidence: 0.9", "confidence: -0.1", 1), "outside [0,1]"},
		{"no provenance", strings.Replace(base, "provenance: test-only valid rule used as the lint baseline", "provenance: \"\"", 1), "provenance is empty"},
		{"unknown field", strings.Replace(base, "field: http.server", "field: http.body", 1), "unknown field"},
		{"bad pattern", strings.Replace(base, "pattern: '^ok$'", "pattern: '(unclosed'", 1), "does not compile"},
		{"version group out of range", strings.Replace(base, "pattern: '^ok$'", "pattern: '^ok$'\n    version_group: 3", 1), "exceeds pattern's"},
		{"weight out of range", strings.Replace(base, "pattern: '^ok$'", "pattern: '^ok$'\n    weight: 1.5", 1), "weight 1.5 outside"},
		{"single test", strings.Replace(base, "  - evidence: {http: {server: \"not-ok\"}}\n    match: false\n", "", 1), "need ≥2"},
		{"no positive", strings.Replace(base, "  - evidence: {http: {server: \"ok\"}}\n    match: true\n", "", 1), "no positive test"},
		{"no negative", strings.Replace(base, "  - evidence: {http: {server: \"not-ok\"}}\n    match: false\n", "", 1), "no negative"},
		{"empty test evidence", strings.Replace(base, "evidence: {http: {server: \"not-ok\"}}", "evidence: {}", 1), "empty evidence"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := Lint([]*Rule{ruleYAML(t, tc.yml)})
			found := false
			for _, p := range problems {
				if strings.Contains(p.Detail, tc.want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("want problem containing %q, got %+v", tc.want, problems)
			}
		})
	}
}

func TestLintDuplicateIDs(t *testing.T) {
	yml := `
id: dup-rule
family: web-server
product: dup
confidence: 0.9
provenance: test-only duplicate id rule
matches:
  - field: http.server
    pattern: 'x'
tests:
  - evidence: {http: {server: "x"}}
    match: true
  - evidence: {http: {server: "y"}}
    match: false
`
	a := ruleYAML(t, yml)
	a.source = "a.yaml"
	b := ruleYAML(t, yml)
	b.source = "b.yaml"
	problems := Lint([]*Rule{a, b})
	found := false
	for _, p := range problems {
		if strings.Contains(p.Detail, "duplicate rule id") && strings.Contains(p.Detail, "a.yaml") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want duplicate-id problem, got %+v", problems)
	}
}

func TestLintExploitationHints(t *testing.T) {
	cases := map[string]string{
		"SQL injection shape":   "union select",
		"credential shape":      "bearer ",
		"password exfiltration": "password",
	}
	for name, hint := range cases {
		t.Run(name, func(t *testing.T) {
			r := ruleYAML(t, `
id: shaped-rule
family: web-tech
product: probe
confidence: 0.9
provenance: test-only rule whose pattern carries an exploitation-shaped hint
matches:
  - field: http.title
    pattern: '`+hint+`'
tests:
  - evidence: {http: {title: "generic"}}
    match: true
  - evidence: {http: {title: "other"}}
    match: false
`)
			problems := Lint([]*Rule{r})
			found := false
			for _, p := range problems {
				if strings.Contains(p.Detail, "never encode exploitation or credential payloads") {
					found = true
				}
			}
			if !found {
				t.Fatalf("want exploitation-hint problem for %q, got %+v", hint, problems)
			}
		})
	}
}

func TestApplyTestsFailures(t *testing.T) {
	t.Run("positive failure", func(t *testing.T) {
		r := ruleYAML(t, `
id: fail-pos
family: web-server
product: fail
confidence: 0.9
provenance: test-only rule with a failing positive test
matches:
  - field: http.server
    pattern: '^never$'
tests:
  - evidence: {http: {server: "always"}}
    match: true
  - evidence: {http: {server: "x"}}
    match: false
`)
		problems := ApplyTests(r)
		if len(problems) != 1 || !strings.Contains(problems[0].Detail, "positive test failed") {
			t.Fatalf("problems = %+v", problems)
		}
	})
	t.Run("false-positive guard failure", func(t *testing.T) {
		r := ruleYAML(t, `
id: fail-neg
family: web-server
product: fail
confidence: 0.9
provenance: test-only rule with a failing false-positive guard
matches:
  - field: http.server
    pattern: 'oops'
tests:
  - evidence: {http: {server: "oops"}}
    match: true
  - evidence: {http: {server: "oops again"}}
    match: false
`)
		problems := ApplyTests(r)
		if len(problems) != 1 || !strings.Contains(problems[0].Detail, "false-positive guard failed") {
			t.Fatalf("problems = %+v", problems)
		}
	})
	t.Run("version mismatch", func(t *testing.T) {
		r := ruleYAML(t, `
id: fail-ver
family: web-server
product: fail
confidence: 0.9
provenance: test-only rule with a version-mismatched test
matches:
  - field: http.server
    pattern: '^app/([\d.]+)$'
    version_group: 1
tests:
  - evidence: {http: {server: "app/1.2.3"}}
    match: true
    version: "9.9.9"
  - evidence: {http: {server: "x"}}
    match: false
`)
		problems := ApplyTests(r)
		if len(problems) != 1 || !strings.Contains(problems[0].Detail, "version mismatch") {
			t.Fatalf("problems = %+v", problems)
		}
	})
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	good := `
id: dir-rule
family: web-server
product: dir
confidence: 0.9
provenance: test-only operator rule loaded from a directory
matches:
  - field: http.server
    pattern: '^dir$'
tests:
  - evidence: {http: {server: "dir"}}
    match: true
  - evidence: {http: {server: "nodir"}}
    match: false
`
	if err := os.WriteFile(filepath.Join(dir, "dir-rule.yaml"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	rules, problems, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || len(problems) != 0 {
		t.Fatalf("rules=%d problems=%+v", len(rules), problems)
	}

	// Filename/id mismatch is a contribution-format problem.
	bad := strings.Replace(good, "id: dir-rule", "id: other-id", 1)
	if err := os.WriteFile(filepath.Join(dir, "misnamed.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	rules, problems, err = LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || len(problems) != 1 || !strings.Contains(problems[0].Detail, "does not match rule id") {
		t.Fatalf("rules=%d problems=%+v", len(rules), problems)
	}

	// Unparseable YAML becomes a problem, not a panic.
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("{unclosed: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	_, problems, err = LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range problems {
		if strings.Contains(p.Detail, "parse failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want parse-failed problem, got %+v", problems)
	}

	// Missing directory errors.
	if _, _, err := LoadDir(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing dir should error")
	}
}

func TestFieldResolve(t *testing.T) {
	ev := map[string]any{
		"http": map[string]any{"server": "s", "status": 200},
		"tls":  "not-a-map",
	}
	if v, ok := FieldHTTPServer.resolve(ev); !ok || v != "s" {
		t.Fatalf("http.server resolve = %q %v", v, ok)
	}
	if _, ok := FieldHTTPTitle.resolve(ev); ok {
		t.Fatal("absent field resolved")
	}
	if _, ok := Field("http.status").resolve(ev); ok {
		t.Fatal("non-string resolved")
	}
	if _, ok := FieldTLSVersion.resolve(ev); ok {
		t.Fatal("non-map parent resolved")
	}
	if _, ok := Field("ssh.banner").resolve(ev); ok {
		t.Fatal("absent parent resolved")
	}
}
