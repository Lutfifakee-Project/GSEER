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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// rule.go — the rule model, YAML loading, and static lint checks.
//
// A rule is one product identification over observed evidence.  The
// YAML format is the contribution format (docs/RULES.md §2): one rule
// per file, named <id>.yaml, so a pull request is a single new file
// plus, when the seed set changes, its diff on seed_test.go coverage
// pins.

// Family classifies the product.  The set is closed (lint rejects
// unknown families) so facet counts and docs stay meaningful.
type Family string

const (
	FamilyWebServer Family = "web-server"
	FamilyCMS       Family = "cms"
	FamilyLanguage  Family = "language"
	FamilyFramework Family = "framework"
	FamilyWebTech   Family = "web-tech"
	FamilyService   Family = "service"
)

// Families is the closed family set, in documentation order.
var Families = []Family{FamilyWebServer, FamilyCMS, FamilyLanguage, FamilyFramework, FamilyWebTech, FamilyService}

func (f Family) valid() bool {
	for _, ok := range Families {
		if f == ok {
			return true
		}
	}
	return false
}

// Field is an evidence path a rule may match.  Closed set (lint
// rejects unknown paths — a typo'd path would silently never fire,
// which is worse than a loud lint failure).  The set mirrors the
// observation evidence schema the storage layer actually stores:
// the http/tls payloads projected by latest_http/latest_tls and the
// ssh banner shape used by the fixture corpus (o3).
type Field string

const (
	FieldHTTPServer       Field = "http.server"
	FieldHTTPTitle        Field = "http.title"
	FieldTLSVersion       Field = "tls.version"
	FieldTLSCertSubjectCN Field = "tls.cert_subject_cn"
	FieldTLSCertIssuerCN  Field = "tls.cert_issuer_cn"
	FieldSSHBanner        Field = "ssh.banner"
)

// Fields is the closed matchable-field set, in documentation order.
var Fields = []Field{FieldHTTPServer, FieldHTTPTitle, FieldTLSVersion, FieldTLSCertSubjectCN, FieldTLSCertIssuerCN, FieldSSHBanner}

func (f Field) valid() bool {
	for _, ok := range Fields {
		if f == ok {
			return true
		}
	}
	return false
}

// resolve walks a dotted field path into the evidence map and returns
// the string value, ok=false for anything absent or non-string.
func (f Field) resolve(evidence map[string]any) (string, bool) {
	parts := strings.SplitN(string(f), ".", 2)
	v, ok := evidence[parts[0]]
	if !ok {
		return "", false
	}
	if len(parts) == 2 {
		m, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		v, ok = m[parts[1]]
		if !ok {
			return "", false
		}
	}
	s, ok := v.(string)
	return s, ok
}

// MatchSpec is one alternative signature inside a rule.  A rule fires
// when ANY of its specs matches (OR); the best hit (highest weight,
// spec order breaking ties) supplies the version and confidence.
type MatchSpec struct {
	// Field is the evidence path ("http.server").
	Field Field `yaml:"field"`
	// Pattern is a Go RE2 regexp applied to the field value.
	Pattern string `yaml:"pattern"`
	// Flags are optional RE2 flags, currently only "i" (case-insensitive).
	Flags string `yaml:"flags,omitempty"`
	// VersionGroup is the capture group (1-based) holding the product
	// version; 0 = the rule carries no version from this spec.
	VersionGroup int `yaml:"version_group,omitempty"`
	// Weight scales the rule confidence for THIS alternative
	// (0 < w ≤ 1).  Default 1.0.
	Weight float64 `yaml:"weight,omitempty"`

	re *regexpCompiled `yaml:"-"`
}

// Test is one false-positive regression case: the rule is applied to
// Evidence and must (not) match with the expected version.
type Test struct {
	Evidence map[string]any `yaml:"evidence"`
	// Match is the expected outcome.
	Match bool `yaml:"match"`
	// Version is the expected extracted version ("" = no assertion on
	// version; asserted only when the tag is present in the YAML via
	// a pointer, so an absent tag and an explicit empty differ).
	Version *string `yaml:"version,omitempty"`
}

// Rule is one identification signature.
type Rule struct {
	// ID is the stable kebab-case identifier ("<family-ish>-<product>"
	// convention, e.g. "web-server-nginx").
	ID string `yaml:"id"`
	// Family is one of the closed Family set.
	Family Family `yaml:"family"`
	// Product is the canonical technology name that lands in
	// technologies[] (plain name, no version — search CONTAINS is
	// element membership).
	Product string `yaml:"product"`
	// Version is a fixed version claim (rare; most rules extract from
	// evidence via VersionGroup).
	Version string `yaml:"version,omitempty"`
	// Confidence ∈ [0,1] per docs/RULES.md §4 tiers.
	Confidence float64 `yaml:"confidence"`
	// Provenance documents the signature's origin: which observable
	// the pattern encodes and why it is trustworthy.  Required — a
	// rule without provenance is a rule nobody can audit.
	Provenance string `yaml:"provenance"`
	// Matches ≥1 alternatives.
	Matches []MatchSpec `yaml:"matches"`
	// Tests ≥2 (≥1 positive, ≥1 negative).
	Tests []Test `yaml:"tests"`

	// source records where the rule was loaded from (lint report).
	source string
}

// Source is the load origin recorded for lint reports ("embedded seed"
// or the file path).
func (r *Rule) Source() string { return r.source }

// Problem is one lint finding.  RuleID is "" for registry-level
// findings (duplicate ids, exploitation text).
type Problem struct {
	RuleID  string `json:"rule_id"`
	File    string `json:"file,omitempty"`
	Field   string `json:"field,omitempty"`
	Detail  string `json:"detail"`
	Helpful string `json:"helpful,omitempty"`
}

func (p Problem) String() string {
	where := p.RuleID
	if p.File != "" {
		where = fmt.Sprintf("%s (%s)", p.RuleID, p.File)
	}
	out := where + ": " + p.Detail
	if p.Helpful != "" {
		out += " — " + p.Helpful
	}
	return out
}

// idPattern is the kebab-case rule id shape.
var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Lint statically validates rules (without applying tests — ApplyTests
// does that) and returns every finding.  An empty result means the
// registry is well-formed.
func Lint(rules []*Rule) []Problem {
	var problems []Problem
	seen := map[string]string{}
	for _, r := range rules {
		problems = append(problems, lintRule(r)...)
		if prev, dup := seen[r.ID]; dup {
			problems = append(problems, Problem{RuleID: r.ID, File: r.source,
				Detail:  fmt.Sprintf("duplicate rule id (first seen in %s)", prev),
				Helpful: "rule ids are the stable join key; rename the new rule"})
		} else {
			seen[r.ID] = r.source
		}
	}
	problems = append(problems, lintExploitationPatterns(rules)...)
	return problems
}

func lintRule(r *Rule) []Problem {
	var problems []Problem
	p := func(field, detail, helpful string) Problem {
		return Problem{RuleID: r.ID, File: r.source, Field: field, Detail: detail, Helpful: helpful}
	}
	if r.ID == "" {
		problems = append(problems, p("id", "rule id is empty", "add a kebab-case id, e.g. web-server-nginx"))
	} else if !idPattern.MatchString(r.ID) {
		problems = append(problems, p("id", fmt.Sprintf("id %q is not kebab-case", r.ID), "use lowercase letters/digits separated by single hyphens"))
	}
	if !r.Family.valid() {
		problems = append(problems, p("family", fmt.Sprintf("unknown family %q", r.Family),
			"one of: "+familyList()))
	}
	if strings.TrimSpace(r.Product) == "" {
		problems = append(problems, p("product", "product is empty", "the canonical technology name, e.g. nginx"))
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		problems = append(problems, p("confidence", fmt.Sprintf("confidence %v outside [0,1]", r.Confidence),
			"see docs/RULES.md §4 confidence tiers"))
	}
	if strings.TrimSpace(r.Provenance) == "" {
		problems = append(problems, p("provenance", "provenance is empty",
			"document the observable the signature encodes and its origin"))
	}
	if len(r.Matches) == 0 {
		problems = append(problems, p("matches", "no match specs", "add at least one field+pattern alternative"))
	}
	problems = append(problems, lintRuleMatches(r, p)...)
	if len(r.Tests) < 2 {
		problems = append(problems, p("tests", fmt.Sprintf("only %d test cases (need ≥2)", len(r.Tests)),
			"at least one positive and one false-positive guard"))
	}
	problems = append(problems, lintRuleTests(r, p)...)
	return problems
}

// lintRuleMatches validates every match spec of one rule (field names,
// pattern compilation, version_group bounds, weight range).
func lintRuleMatches(r *Rule, p func(field, detail, helpful string) Problem) []Problem {
	var problems []Problem
	for i, m := range r.Matches {
		where := fmt.Sprintf("matches[%d]", i)
		if !m.Field.valid() {
			problems = append(problems, p(where+".field", fmt.Sprintf("unknown field %q", m.Field),
				"one of: "+fieldList()))
		}
		re, err := compilePattern(m.Pattern, m.Flags)
		if err != nil {
			problems = append(problems, p(where+".pattern", fmt.Sprintf("pattern %q does not compile: %v", m.Pattern, err), ""))
		} else if m.VersionGroup > re.NumSubexp() {
			problems = append(problems, p(where+".version_group",
				fmt.Sprintf("version_group %d exceeds pattern's %d capture groups", m.VersionGroup, re.NumSubexp()),
				"version_group is 1-based over the pattern's () groups; 0 disables version extraction"))
		}
		if m.Weight != 0 && (m.Weight <= 0 || m.Weight > 1) {
			problems = append(problems, p(where+".weight", fmt.Sprintf("weight %v outside (0,1]", m.Weight), "omit the key for 1.0"))
		}
	}
	return problems
}

// lintRuleTests validates the false-positive regression block: every
// test carries evidence and the set has ≥1 positive and ≥1 negative.
func lintRuleTests(r *Rule, p func(field, detail, helpful string) Problem) []Problem {
	var problems []Problem
	var positives, negatives int
	for i, t := range r.Tests {
		if t.Match {
			positives++
		} else {
			negatives++
		}
		if len(t.Evidence) == 0 {
			problems = append(problems, p(fmt.Sprintf("tests[%d]", i), "empty evidence", "the evidence map the rule is applied to"))
		}
	}
	if positives == 0 {
		problems = append(problems, p("tests", "no positive test case", "the rule must be shown to fire at least once"))
	}
	if negatives == 0 {
		problems = append(problems, p("tests", "no negative (false-positive guard) test case",
			"a near-miss string that must NOT match, e.g. a different product sharing the prefix"))
	}
	return problems
}

// exploitationHints are fragments that indicate a signature exists to
// attack rather than identify.  Identification rules never need them.
var exploitationHints = []string{
	"union select", "drop table", "<script", "javascript:", "onerror=",
	"'; --", "../", "..\\", "%2e%2e", "etc/passwd", "cmd.exe", "/bin/sh",
	"password", "passwd:", "apikey", "api_key", "bearer ", "authorization:",
}

// lintExploitationPatterns rejects rule text that looks like an
// exploitation/credential signature rather than an identification
// signature (owner directive §2: no exploitation signatures).
func lintExploitationPatterns(rules []*Rule) []Problem {
	var problems []Problem
	for _, r := range rules {
		text := strings.ToLower(r.ID + " " + r.Product + " " + r.Provenance)
		for _, m := range r.Matches {
			text += " " + strings.ToLower(m.Pattern)
		}
		for _, hint := range exploitationHints {
			if strings.Contains(text, hint) {
				problems = append(problems, Problem{RuleID: r.ID, File: r.source,
					Detail:  fmt.Sprintf("rule text contains %q — GSEER rules identify software, they never encode exploitation or credential payloads", hint),
					Helpful: "see docs/RULES.md §1 honesty constraints"})
				break
			}
		}
	}
	return problems
}

func familyList() string {
	parts := make([]string, len(Families))
	for i, f := range Families {
		parts[i] = string(f)
	}
	return strings.Join(parts, " | ")
}

func fieldList() string {
	parts := make([]string, len(Fields))
	for i, f := range Fields {
		parts[i] = string(f)
	}
	return strings.Join(parts, " | ")
}

// ParseRule decodes one rule from YAML bytes.
func ParseRule(data []byte) (*Rule, error) {
	var r Rule
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	return &r, nil
}

// LoadDir loads every *.yaml file in dir (one rule per file, named
// <id>.yaml).  Unparseable files become registry-level problems
// returned alongside the successfully parsed rules — the caller
// decides whether to proceed.
func LoadDir(dir string) ([]*Rule, []Problem, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var (
		rules    []*Rule
		problems []Problem
	)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml")) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path) // #nosec G304 -- reads the operator-supplied rules directory (--dir); loading it is the feature
		if err != nil {
			return rules, problems, err
		}
		r, err := ParseRule(data)
		if err != nil {
			problems = append(problems, Problem{File: path, Detail: "parse failed: " + err.Error()})
			continue
		}
		r.source = path
		if r.ID != "" && !strings.HasPrefix(name, r.ID+".") {
			problems = append(problems, Problem{RuleID: r.ID, File: path,
				Detail:  fmt.Sprintf("file name %q does not match rule id %q", name, r.ID),
				Helpful: "one rule per file named <id>.yaml (contribution format, docs/RULES.md §2)"})
		}
		rules = append(rules, r)
	}
	return rules, problems, nil
}
