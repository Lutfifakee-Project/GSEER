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
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"
)

// seed.go — the embedded v0.1.0 fingerprint seed collection.
//
// The seed set is deliberately conservative: every signature encodes a
// well-documented default header/title format, carries provenance, and
// ships false-positive guards.  CMS/framework coverage is thin ON
// PURPOSE — reliable CMS/framework identification usually needs
// page-body or generator signals that GSEER's passive evidence model
// does not carry (Discovery ≠ Observation); contributions extend the
// set as observation-capable authorized sources land (docs/RULES.md §6).

//go:embed seed/*.yaml
var seedFS embed.FS

// LoadSeed parses the embedded seed collection.
func LoadSeed() ([]*Rule, []Problem, error) {
	entries, err := seedFS.ReadDir("seed")
	if err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	rules := make([]*Rule, 0, len(names))
	var problems []Problem
	for _, name := range names {
		data, err := seedFS.ReadFile(path.Join("seed", name))
		if err != nil {
			return rules, problems, err
		}
		r, err := ParseRule(data)
		if err != nil {
			problems = append(problems, Problem{File: "seed/" + name, Detail: "parse failed: " + err.Error()})
			continue
		}
		r.source = "embedded seed"
		if r.ID != "" && name != r.ID+".yaml" {
			problems = append(problems, Problem{RuleID: r.ID, File: "seed/" + name,
				Detail:  fmt.Sprintf("file name %q does not match rule id %q", name, r.ID),
				Helpful: "one rule per file named <id>.yaml"})
		}
		rules = append(rules, r)
	}
	return rules, problems, nil
}

// Seed returns the compiled embedded seed registry.
func Seed() *Registry {
	rules, problems, err := LoadSeed()
	if err != nil {
		// The seed set is compiled into the binary; a read failure is a
		// build error, not a runtime condition.
		reg := &Registry{}
		reg.lintProblems = append(reg.lintProblems, Problem{Detail: "seed load failed: " + err.Error()})
		return reg
	}
	reg := NewRegistry(rules)
	reg.lintProblems = append(reg.lintProblems, problems...)
	return reg
}

// SeedPlus returns the seed registry extended with operator rules from
// dir.  Operator problems are surfaced in LintProblems; a broken
// operator rule disables the registry (fail-closed) rather than
// silently running on the seed subset.
func SeedPlus(dir string) (*Registry, []Problem, error) {
	rules, problems, err := LoadSeed()
	if err != nil {
		return nil, problems, err
	}
	extra, dirProblems, err := LoadDir(dir)
	if err != nil {
		return nil, problems, err
	}
	problems = append(problems, dirProblems...)
	rules = append(rules, extra...)
	return NewRegistry(rules), problems, nil
}
