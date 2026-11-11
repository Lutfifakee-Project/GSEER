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

// source.go — the real `gseer source` command (v0.0.2): the data_sources
// registry surface.  Registration records the connector's terms review
// verdict next to the row (terms_ref); enablement is an explicit,
// separate operator action — and even a hand-flipped enabled row cannot
// outrun the connector's code-level review gate (collect.Runner).

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/GSEER/internal/collect"
)

// registerSourceCmd attaches `gseer source` with list/add/enable/disable.
func registerSourceCmd(root *cobra.Command) {
	source := &cobra.Command{
		Use:   "source",
		Short: "Manage registered data sources (passive connectors; nothing collects until you run it)",
		Args:  cobra.NoArgs,
		Long: `Manage GSEER's registered data sources.

A source is one data provider feeding GSEER's index through a passive
connector (internal/collect).  Registration records the connector's
TERMS REVIEW verdict (docs/SOURCES.md) in the registry; enablement is a
separate, explicit action, and connectors whose provider terms do not
clearly permit automated collection and indexing refuse to run even
when enabled (E_SOURCE_TERMS).

Review verdicts live with the connectors:
  dns      reviewed-enabled  (operator-domain resolution)
  ip2asn   reviewed-enabled  (Team Cymru IP-to-ASN, DNS interface)
  ptr      reviewed-enabled  (reverse DNS via the operator's resolver)
  rdap     disabled          (RIR terms do not grant indexing/redistribution)
  ct       disabled          (crt.sh publishes no terms covering indexing/reuse)`,
	}
	source.AddCommand(newSourceListCmd(), newSourceAddCmd(), newSourceEnableCmd(true), newSourceEnableCmd(false))
	root.AddCommand(source)
}

func newSourceListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered data sources with their terms verdicts",
		Args:  cobra.NoArgs,
		RunE:  runSourceList,
	}
}

type sourceAddFlags struct {
	flagType string
	endpoint string
	seedMode string
	seeds    []string
	seedKind string
	resolver string
	rps      float64
	timeout  int
	limit    int
	enable   bool
	yes      bool
}

// sourceAddReset is the test seam that restores the shared flag struct:
// the root command tree is a package-level singleton, and pflag values
// persist across Execute() calls (a second parse appends to slices and
// keeps strings) — tests running multiple executions must reset.
var sourceAddReset = func() {}

func newSourceAddCmd() *cobra.Command {
	f := &sourceAddFlags{}
	sourceAddReset = func() { *f = sourceAddFlags{} }
	cmd := &cobra.Command{
		Use:   "add <name> --type <connector>",
		Short: "Register a data source in the GSEER database",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return runSourceAdd(cmd, args[0], f) },
	}
	cmd.Flags().StringVar(&f.flagType, "type", "", "connector kind: dns | ip2asn | ptr (reviewed-enabled), rdap | ct (disabled)")
	_ = cmd.MarkFlagRequired("type")
	cmd.Flags().StringVar(&f.endpoint, "endpoint", "", "provider API base URL (HTTP connectors)")
	cmd.Flags().StringVar(&f.seedMode, "seed-mode", "config", "where queries target: config (--seeds) or index (assets already in your index)")
	cmd.Flags().StringSliceVar(&f.seeds, "seeds", nil, "comma-separated seed identifiers (seed-mode=config)")
	cmd.Flags().StringVar(&f.seedKind, "seed-kind", "ip", "asset kind index seeds are drawn from (seed-mode=index)")
	cmd.Flags().StringVar(&f.resolver, "resolver", "", "DNS server host:port for dns/ip2asn (default: system resolver)")
	cmd.Flags().Float64Var(&f.rps, "requests-per-second", 0, "override the connector's default request rate")
	cmd.Flags().IntVar(&f.timeout, "timeout-ms", 0, "override the per-request timeout")
	cmd.Flags().IntVar(&f.limit, "limit-per-run", 0, "per-run record budget (default 500)")
	cmd.Flags().BoolVar(&f.enable, "enable", false, "enable the source after registration")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "accept the provider's documented terms reference non-interactively")
	return cmd
}

func newSourceEnableCmd(enable bool) *cobra.Command {
	use, short := "enable <name>", "Enable a registered data source"
	if !enable {
		use, short = "disable <name>", "Disable a registered data source"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSourceEnable(cmd, args[0], enable)
		},
	}
}

// openSourceStore loads config and opens the registry store for the
// profile the config selects (personal SQLite or self-hosted
// PostgreSQL; client is refused — see registry.go).  Since v0.3.0 the
// collection registry exists in BOTH engines: personal mode can
// register sources and collect for real.
func openSourceStore(cmd *cobra.Command) (collect.RegistryBackend, func(), error) {
	store, closer, _, err := openRegistryStore(cmd)
	return store, closer, err
}

func runSourceList(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	store, closer, err := openSourceStore(cmd)
	if err != nil {
		return err
	}
	defer closer()
	sources, err := store.ListSources(commandContext(cmd))
	if err != nil {
		printf(out, "E_DB: %s\n", err.Error())
		exitWith(ExitUnavailable)
		return nil
	}
	if len(sources) == 0 {
		printf(out, "No data sources registered.\nTry: gseer source add my-dns --type dns --seed-mode index --enable\n")
		return nil
	}
	_, _ = fmt.Fprintln(out, "NAME  CONNECTOR  ENABLED  TERMS  CONFIG")
	for _, s := range sources {
		terms := "—"
		if s.TermsRef != "" {
			terms = s.TermsRef
		}
		cfgStr := "-"
		if len(s.ConfigJSON) > 0 && string(s.ConfigJSON) != "{}" {
			cfgStr = string(s.ConfigJSON)
		}
		_, _ = fmt.Fprintf(out, "%s  %s  %-7v  %s  %s\n",
			pad(s.Name, 24), pad(s.Connector, 10), s.Enabled,
			truncate(terms, 60), truncate(cfgStr, 48))
	}
	printf(out, "\nTerms reviews: docs/SOURCES.md · run a source: gseer collect run --source <name>\n")
	return nil
}

func runSourceAdd(cmd *cobra.Command, name string, f *sourceAddFlags) error {
	out := cmd.OutOrStdout()
	picked, ok := collect.Registry()[f.typeOf()]
	if !ok {
		return fmt.Errorf("E_SOURCE_CONNECTOR: unknown connector type %q\nWhy: registered kinds are dns, ip2asn, ptr (reviewed-enabled), rdap, ct (disabled)\nTry: gseer source add <name> --type dns", f.typeOf())
	}

	if f.seedMode != "config" && f.seedMode != "index" {
		return fmt.Errorf("E_USAGE: --seed-mode must be config or index")
	}
	if f.seedMode == "config" && len(f.seeds) == 0 {
		return fmt.Errorf("E_USAGE: --seed-mode config needs --seeds (comma-separated identifiers)\nTry: gseer source add my-dns --type dns --seeds example.net,example.org")
	}

	cfg := map[string]any{
		"seed_mode": f.seedMode,
		"seed_kind": f.seedKind,
	}
	if f.endpoint != "" {
		cfg["endpoint"] = f.endpoint
	}
	if len(f.seeds) > 0 {
		cfg["seeds"] = f.seeds
	}
	if f.resolver != "" {
		cfg["resolver"] = f.resolver
	}
	if f.rps > 0 {
		cfg["requests_per_second"] = f.rps
	}
	if f.timeout > 0 {
		cfg["timeout_ms"] = f.timeout
	}
	if f.limit > 0 {
		cfg["limit_per_run"] = f.limit
	}

	rv := picked.Review()
	if f.enable && rv.Verdict != collect.ReviewEnabled {
		return fmt.Errorf("E_SOURCE_TERMS: connector %q is disabled by terms review and cannot be enabled\nWhy: %s\nSee: %s",
			picked.Kind(), rv.Why, rv.DocRef)
	}

	store, closer, err := openSourceStore(cmd)
	if err != nil {
		return err
	}
	defer closer()
	id, err := store.EnsureSource(commandContext(cmd), name, picked.Kind(), rv.DocRef+" — "+rv.Why, cfg)
	if err != nil {
		printf(out, "E_DB: %s\n", err.Error())
		exitWith(ExitUnavailable)
		return nil
	}
	if f.enable {
		if err := store.SetSourceEnabled(commandContext(cmd), name, true); err != nil {
			printf(out, "E_DB: %s\n", err.Error())
			exitWith(ExitUnavailable)
			return nil
		}
	}
	cfgJSON, _ := json.Marshal(cfg)
	printf(out, "Registered source %q (id=%d, connector=%s, enabled=%v)\n", name, id, picked.Kind(), f.enable)
	printf(out, "Config: %s\n", string(cfgJSON))
	if !f.enable {
		printf(out, "Next: gseer source enable %s   (nothing collects until enabled AND run)\n", name)
	} else {
		printf(out, "Next: gseer collect run --source %s\n", name)
	}
	return nil
}

// typeOf reads the --type flag.
func (f *sourceAddFlags) typeOf() string { return f.flagType }

func runSourceEnable(cmd *cobra.Command, name string, enable bool) error {
	out := cmd.OutOrStdout()
	store, closer, err := openSourceStore(cmd)
	if err != nil {
		return err
	}
	defer closer()
	if enable {
		src, err := store.GetSource(commandContext(cmd), name)
		if err != nil {
			printf(out, "E_DB: %s\n", err.Error())
			exitWith(ExitUnavailable)
			return nil
		}
		if c, ok := collect.Registry()[src.Connector]; ok && c.Review().Verdict != collect.ReviewEnabled {
			rv := c.Review()
			return fmt.Errorf("E_SOURCE_TERMS: connector %q is disabled by terms review and cannot be enabled\nWhy: %s\nSee: %s",
				src.Connector, rv.Why, rv.DocRef)
		}
	}
	if err := store.SetSourceEnabled(commandContext(cmd), name, enable); err != nil {
		if sourceNotFound(err) {
			return fmt.Errorf("E_SOURCE_NOT_FOUND: %q is not registered\nTry: gseer source list", name)
		}
		printf(out, "E_DB: %s\n", err.Error())
		exitWith(ExitUnavailable)
		return nil
	}
	state := "enabled"
	if !enable {
		state = "disabled"
	}
	printf(out, "Source %q %s.\n", name, state)
	return nil
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// pad left-aligns s in a field of n runes (plain table output).
func pad(s string, n int) string {
	for len([]rune(s)) < n {
		s += " "
	}
	return s
}
