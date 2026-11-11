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

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/gseer/internal/config"
	"github.com/Lutfifakee-Project/gseer/internal/fprule"
)

// rules.go — the `gseer rules` command pair (v0.1.0 fingerprint rule
// engine).  `rules list` shows the loaded collection (embedded seed
// plus, when configured, the operator's fingerprints.rules_dir);
// `rules validate` lints a rule set AND runs every rule's false-positive
// regression tests — the gate a contribution must pass.  Neither
// command touches the network or the collector framework.
func registerRulesCmd(root *cobra.Command) {
	rules := &cobra.Command{
		Use:   "rules",
		Short: "Manage fingerprint rules (technology classification over observed evidence)",
		Args:  cobra.NoArgs,
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List loaded fingerprint rules (embedded seed + configured rules dir)",
		Args:  cobra.NoArgs,
		RunE:  runRulesList,
	}
	validate := &cobra.Command{
		Use:   "validate",
		Short: "Lint fingerprint rules and run their false-positive regression tests",
		Args:  cobra.NoArgs,
		RunE:  runRulesValidate,
	}
	validate.Flags().String("dir", "", "lint a rule directory instead of the loaded collection (contribution workflow)")
	rules.AddCommand(list, validate)
	root.AddCommand(rules)
}

// rulesDirForLint resolves the operator rules dir without requiring a
// fully-valid profile config: the rules commands operate on files and
// embedded data, not on a database profile.  A valid config wins; a
// failing config falls back to the env var (a broken DSN section must
// not block rule linting); empty = seed only.
func rulesDirForLint() string {
	if cfg, err := config.Load(config.Flags{
		ConfigPath: flags.configPath,
		Profile:    flags.profile,
		Endpoint:   flags.server,
	}); err == nil {
		return cfg.Fingerprints.RulesDir
	}
	return os.Getenv("GSEER_FINGERPRINTS_RULES_DIR")
}

// loadRuleCollection loads the seed set plus the operator rules dir
// (both list and the default validate path share it).
func loadRuleCollection() (*fprule.Registry, []fprule.Problem, error) {
	dir := rulesDirForLint()
	if dir == "" {
		return fprule.Seed(), nil, nil
	}
	return fprule.SeedPlus(dir)
}

// registryFromConfig builds the rule registry for a config: the
// embedded seed plus the optional operator dir.  It returns the
// registry (possibly unhealthy — fail-closed) and every load problem.
func registryFromConfig(cfg *config.Config) (*fprule.Registry, []fprule.Problem, error) {
	if cfg.Fingerprints.RulesDir == "" {
		return fprule.Seed(), nil, nil
	}
	return fprule.SeedPlus(cfg.Fingerprints.RulesDir)
}

// fingerprintEngineFromConfig is the ingestion-side helper (collect
// run): an enabled, healthy registry is attached to the store; any
// problem disables enrichment for the run (fail-closed) and is
// reported by the caller.
func fingerprintEngineFromConfig(cmd *cobra.Command, cfg *config.Config) (*fprule.Registry, []fprule.Problem) {
	if cfg == nil || cfg.Fingerprints.Disabled {
		return nil, nil
	}
	reg, problems, err := registryFromConfig(cfg)
	if err != nil {
		printf(cmd.OutOrStdout(), "[warn] fingerprint rules dir unreadable: %s\n", err.Error())
		return nil, []fprule.Problem{{Detail: err.Error()}}
	}
	problems = append(problems, reg.LintProblems()...)
	if len(problems) > 0 || !reg.Healthy() {
		return nil, problems
	}
	if static := fprule.Lint(reg.Rules()); len(static) > 0 {
		return nil, static
	}
	return reg, nil
}

func runRulesList(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	reg, problems, err := loadRuleCollection()
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		for _, p := range problems {
			printf(out, "[problem] %s\n", p)
		}
	}
	rules := reg.Rules()
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })

	if flags.output == "json" {
		type ruleJSON struct {
			ID         string  `json:"id"`
			Family     string  `json:"family"`
			Product    string  `json:"product"`
			Confidence float64 `json:"confidence"`
			Versioned  bool    `json:"version_capable"`
			Source     string  `json:"source"`
			Provenance string  `json:"provenance"`
			Tests      int     `json:"tests"`
		}
		payload := make([]ruleJSON, 0, len(rules))
		for _, r := range rules {
			versioned := r.Version != ""
			for _, m := range r.Matches {
				if m.VersionGroup > 0 {
					versioned = true
				}
			}
			payload = append(payload, ruleJSON{r.ID, string(r.Family), r.Product, r.Confidence, versioned, r.Source(), strings.TrimSpace(r.Provenance), len(r.Tests)})
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	}

	printf(out, "Fingerprint rules (%d loaded; format: docs/RULES.md)\n\n", len(rules))
	for _, r := range rules {
		versioned := "product-only"
		for _, m := range r.Matches {
			if m.VersionGroup > 0 {
				versioned = "version-capable"
				break
			}
		}
		if r.Version != "" {
			versioned = "fixed-version"
		}
		printf(out, "%-34s %-11s %-14s conf=%.2f  %s\n", r.ID, r.Family, r.Product, r.Confidence, versioned)
		first := strings.TrimSpace(r.Provenance)
		if i := strings.IndexAny(first, "\r\n"); i >= 0 {
			first = first[:i]
		}
		printf(out, "    %s\n", first)
	}
	printf(out, "\nValidate a set: gseer rules validate [--dir RULES_DIR]\n")
	return nil
}

func runRulesValidate(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	dir, _ := cmd.Flags().GetString("dir")

	var (
		rules    []*fprule.Rule
		problems []fprule.Problem
		err      error
		label    string
	)
	switch {
	case dir != "":
		label = dir
		rules, problems, err = fprule.LoadDir(dir)
		if err != nil {
			return fmt.Errorf("E_USAGE: cannot read rule dir %q: %s", dir, err.Error())
		}
	default:
		label = "loaded collection (embedded seed"
		rules, problems, err = fprule.LoadSeed()
		if err != nil {
			return fmt.Errorf("E_INTERNAL: seed load failed: %s", err.Error())
		}
		if dir := rulesDirForLint(); dir != "" {
			label += " + " + dir
			extra, dirProblems, derr := fprule.LoadDir(dir)
			if derr != nil {
				return fmt.Errorf("E_USAGE: cannot read rule dir %q: %s", dir, derr.Error())
			}
			rules = append(rules, extra...)
			problems = append(problems, dirProblems...)
		}
		label += ")"
	}

	// Static lint + per-rule false-positive regression tests.
	problems = append(problems, fprule.Lint(rules)...)
	for _, r := range rules {
		problems = append(problems, fprule.ApplyTests(r)...)
	}

	if flags.output == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"rules": len(rules), "problems": problems}); err != nil {
			return err
		}
		if len(problems) > 0 {
			// The exit-code contract holds in machine-readable output too.
			exitWith(ExitInvalidInput)
		}
		return nil
	}
	printf(out, "Validating %s — %d rules\n", label, len(rules))
	if len(problems) == 0 {
		printf(out, "[ ok ] all %d rules pass lint and their regression tests\n", len(rules))
		return nil
	}
	printf(out, "FAILED: %d problem(s)\n", len(problems))
	for _, p := range problems {
		printf(out, "  - %s\n", p)
	}
	printf(out, "\nRule format and confidence tiers: docs/RULES.md\n")
	exitWith(ExitInvalidInput)
	return nil
}
