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

package fprule

import (
	"fmt"
	"regexp"
	"sort"
)

// engine.go — the compiled registry and the evidence matcher.

// regexpCompiled wraps a compiled RE2 pattern.
type regexpCompiled struct {
	re *regexp.Regexp
}

func compilePattern(pattern, flags string) (*regexpCompiled, error) {
	var prefix string
	switch flags {
	case "":
	case "i":
		prefix = "(?i)"
	case "m":
		prefix = "(?m)"
	case "s":
		prefix = "(?s)"
	default:
		return nil, fmt.Errorf("unsupported flags %q (supported: i, m, s)", flags)
	}
	re, err := regexp.Compile(prefix + pattern)
	if err != nil {
		return nil, err
	}
	return &regexpCompiled{re: re}, nil
}

func (c *regexpCompiled) NumSubexp() int { return c.re.NumSubexp() }

// Match is one technology identification derived from evidence.
type Match struct {
	RuleID     string  `json:"rule_id"`
	Family     string  `json:"family"`
	Product    string  `json:"product"`
	Version    string  `json:"version,omitempty"`
	Confidence float64 `json:"confidence"`
}

// Registry is a compiled rule set: the embedded seed collection, the
// embedded set plus an operator directory, or an arbitrary set for
// tests.
type Registry struct {
	rules []*Rule
	// linted records whether compile() succeeded; Apply refuses to
	// run on a registry whose rules failed to compile (fail-closed:
	// a broken pattern must never silently no-op a classification).
	lintProblems []Problem
}

// NewRegistry compiles rules into a registry.  Compilation failures
// are surfaced as lint problems; a registry with any compile problem
// refuses to Apply (fail-closed).
func NewRegistry(rules []*Rule) *Registry {
	reg := &Registry{rules: rules}
	reg.compile()
	return reg
}

func (r *Registry) compile() {
	for _, rule := range r.rules {
		for i := range rule.Matches {
			m := &rule.Matches[i]
			re, err := compilePattern(m.Pattern, m.Flags)
			if err != nil {
				r.lintProblems = append(r.lintProblems, Problem{RuleID: rule.ID, File: rule.source,
					Field:  fmt.Sprintf("matches[%d].pattern", i),
					Detail: fmt.Sprintf("pattern %q does not compile: %v", m.Pattern, err)})
				continue
			}
			m.re = re
		}
	}
}

// Rules returns the registry's rules in load order.
func (r *Registry) Rules() []*Rule { return r.rules }

// LintProblems returns the compilation problems (empty = healthy).
func (r *Registry) LintProblems() []Problem { return r.lintProblems }

// Healthy reports whether the registry can Apply.
func (r *Registry) Healthy() bool { return len(r.lintProblems) == 0 }

// Apply matches the rule set against one evidence payload and returns
// the deduplicated hits sorted by confidence (desc) then product (asc).
//
// Semantics:
//   - a rule fires when ANY of its match specs hits (OR);
//   - within one rule the best hit (highest weight, spec order breaking
//     ties) supplies version and confidence;
//   - across rules, one product survives: the highest-confidence hit
//     (rule id asc breaking ties) — two rules claiming the same product
//     is a collection bug the linter/tests should catch, but Apply
//     stays deterministic regardless;
//   - evidence values that are absent or non-string never match;
//   - a registry with compile problems returns nil (fail-closed).
func (r *Registry) Apply(evidence map[string]any) []Match {
	if !r.Healthy() || len(evidence) == 0 {
		return nil
	}
	type best struct {
		m Match
	}
	byProduct := map[string]*best{}
	for _, rule := range r.rules {
		var hit *MatchSpec
		var version string
		var conf float64
		for i := range rule.Matches {
			spec := &rule.Matches[i]
			if spec.re == nil {
				continue
			}
			val, ok := spec.Field.resolve(evidence)
			if !ok {
				continue
			}
			sub := spec.re.re.FindStringSubmatch(val)
			if sub == nil {
				continue
			}
			w := spec.Weight
			if w == 0 {
				w = 1
			}
			c := rule.Confidence * w
			if hit == nil || c > conf {
				hit = spec
				conf = c
				version = rule.Version
				if spec.VersionGroup > 0 && spec.VersionGroup < len(sub) {
					version = sub[spec.VersionGroup]
				}
			}
		}
		if hit == nil {
			continue
		}
		m := Match{
			RuleID:     rule.ID,
			Family:     string(rule.Family),
			Product:    rule.Product,
			Version:    version,
			Confidence: round3(conf),
		}
		if prev, ok := byProduct[m.Product]; !ok {
			byProduct[m.Product] = &best{m: m}
		} else if m.Confidence > prev.m.Confidence ||
			(m.Confidence == prev.m.Confidence && m.RuleID < prev.m.RuleID) {
			prev.m = m
		}
	}
	out := make([]Match, 0, len(byProduct))
	for _, b := range byProduct {
		out = append(out, b.m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].Product < out[j].Product
	})
	return out
}

// Products reduces Apply's output to the technologies[] form: plain
// product names, confidence order preserved.
func Products(matches []Match) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.Product)
	}
	return out
}

// ApplyTests runs a rule's own regression tests and returns a problem
// per failure (used by lint and by `gseer rules validate`).
func ApplyTests(r *Rule) []Problem {
	var problems []Problem
	reg := NewRegistry([]*Rule{r})
	for i := range r.Matches {
		if r.Matches[i].re == nil {
			return []Problem{{RuleID: r.ID, File: r.source, Detail: "rule does not compile; tests not run"}}
		}
	}
	for i, t := range r.Tests {
		where := fmt.Sprintf("tests[%d]", i)
		hits := reg.Apply(t.Evidence)
		if !t.Match {
			if len(hits) > 0 {
				problems = append(problems, Problem{RuleID: r.ID, File: r.source, Field: where,
					Detail:  fmt.Sprintf("false-positive guard failed: matched %s (this evidence must NOT match)", matchSummary(hits)),
					Helpful: "tighten the pattern boundary or lower the confidence tier"})
			}
			continue
		}
		if len(hits) == 0 {
			problems = append(problems, Problem{RuleID: r.ID, File: r.source, Field: where,
				Detail: "positive test failed: no match"})
			continue
		}
		// The rule under test may share a product with another hit —
		// find THIS rule's hit.
		var mine *Match
		for k := range hits {
			if hits[k].RuleID == r.ID {
				mine = &hits[k]
				break
			}
		}
		if mine == nil {
			problems = append(problems, Problem{RuleID: r.ID, File: r.source, Field: where,
				Detail: "positive test failed: matched a different rule's product"})
			continue
		}
		if t.Version != nil && mine.Version != *t.Version {
			problems = append(problems, Problem{RuleID: r.ID, File: r.source, Field: where,
				Detail: fmt.Sprintf("version mismatch: got %q, want %q", mine.Version, *t.Version)})
		}
	}
	return problems
}

func matchSummary(hits []Match) string {
	out := ""
	for i, m := range hits {
		if i > 0 {
			out += ", "
		}
		out += m.Product
		if m.Version != "" {
			out += "@" + m.Version
		}
	}
	return out
}

// round3 keeps confidence to three decimals so YAML-declared weights
// compose predictably.
func round3(f float64) float64 {
	return float64(int(f*1000+0.5)) / 1000
}
