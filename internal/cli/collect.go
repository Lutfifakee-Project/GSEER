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

// collect.go — `gseer collect` (v0.0.2): run the registered passive
// connectors and inspect their bookkeeping.  Collection is ALWAYS an
// explicit operator action against ENABLED, TERMS-REVIEWED sources; no
// command here ever scans (P1 honesty contract 1) and nothing runs
// uninvoked (no daemon in v0.0.2 — scheduling is the operator's cron).

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/GSEER/internal/collect"
)

func registerCollectCmd(root *cobra.Command) {
	coll := &cobra.Command{
		Use:   "collect",
		Short: "Run passive data connectors (explicit; nothing scans, nothing auto-runs)",
		Args:  cobra.NoArgs,
		Long: `Run GSEER's passive data connectors.

Collection is one explicit command against sources YOU registered and
enabled.  Every run:
  - respects the connector's terms review (docs/SOURCES.md) — a connector
    whose provider terms are not clearly compatible refuses to run even
    when enabled (E_SOURCE_TERMS);
  - targets only identifiers you configured or that are already in your
    index (no third-party target lists, no enumeration);
  - writes asset-level facts through the same suppression-gated,
    idempotent ingestion path as every other writer;
  - records checkpoints, dead-lettered records and job stats in the
    database (gseer collect status).

Examples:
  gseer collect run --source my-dns
  gseer collect run --all
  gseer collect status`,
	}
	coll.AddCommand(newCollectRunCmd(), newCollectStatusCmd(), newCollectRedriveCmd())
	root.AddCommand(coll)
}

// collectRunReset and redriveReset are the test seams mirroring
// sourceAddReset (shared command-tree flag state; cobra flags persist
// across Execute calls on the shared RootCmd).
var (
	collectRunReset = func() {}
	redriveReset    = func() {}
)

func newCollectRunCmd() *cobra.Command {
	var source, all string
	var runAll bool
	collectRunReset = func() { source, all, runAll = "", "", false }
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run one source (--source NAME) or every enabled source (--all)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCollectRun(cmd, source, runAll, all)
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "registered source name to run")
	cmd.Flags().BoolVar(&runAll, "all", false, "run every enabled source once")
	cmd.MarkFlagsMutuallyExclusive("source", "all")
	return cmd
}

func newCollectRedriveCmd() *cobra.Command {
	var sourceName string
	var all, discard bool
	var limit int
	redriveReset = func() { sourceName, all, discard, limit = "", false, false, 0 }
	cmd := &cobra.Command{
		Use:   "redrive",
		Short: "Re-ingest dead-lettered records (explicit; the operator's second chance)",
		Long: `Re-ingest dead-lettered records.

Dead letters are PRESERVED records the pipeline could not apply (invalid
payloads, transient DB failures).  redrive rebuilds each record through
the same normalization pipeline, re-ingests it (suppression still
applies), and marks the row resolved — or bumps its attempts and keeps
it pending when it fails again.  --discard instead marks pending rows
discarded (operator judgment that they will never apply).

Nothing runs uninvoked: redrive is an explicit operator command.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCollectRedrive(cmd, sourceName, all, discard, limit)
		},
	}
	cmd.Flags().StringVar(&sourceName, "source", "", "re-drive only this source's dead letters")
	cmd.Flags().BoolVar(&all, "all", false, "re-drive dead letters for every source")
	cmd.Flags().BoolVar(&discard, "discard", false, "mark pending rows discarded instead of re-driving")
	cmd.Flags().IntVar(&limit, "limit", 500, "maximum rows per pass")
	cmd.MarkFlagsMutuallyExclusive("source", "all")
	cmd.MarkFlagsMutuallyExclusive("all", "discard")
	return cmd
}

func runCollectRedrive(cmd *cobra.Command, sourceName string, all, discard bool, limit int) error {
	out := cmd.OutOrStdout()
	store, closer, err := openSourceStore(cmd)
	if err != nil {
		return err
	}
	defer closer()

	var sourceID int64
	if sourceName != "" {
		src, gerr := store.GetSource(commandContext(cmd), sourceName)
		if gerr != nil {
			printf(out, "E_DB: %s\n", gerr.Error())
			exitWith(ExitUnavailable)
			return nil
		}
		sourceID = src.ID
	} else if !all && !discard {
		// Neither --source nor --all: with --discard, "every source" is
		// the sane reading (the operator is emptying the queue); for a
		// re-drive require an explicit scope.
		printf(out, "E_USAGE: pick --source <name>, --all, or --discard\n")
		exitWith(ExitUsagePlanned)
		return nil
	}

	stats, err := collect.Redrive(commandContext(cmd), store, collect.RedriveOptions{
		SourceID: sourceID,
		Limit:    limit,
		Discard:  discard,
	})
	if err != nil {
		printf(out, "E_DB: %s\n", err.Error())
		exitWith(ExitUnavailable)
		return nil
	}
	if discard {
		printf(out, "discarded: %d dead-letter row(s)\n", stats.Discarded)
		return nil
	}
	printf(out, "considered %d, re-driven %d, resolved %d, still-failing %d, suppressed %d\n",
		stats.Considered, stats.Redriven, stats.Resolved, stats.StillFailing, stats.Suppressed)
	if stats.StillFailing > 0 {
		printf(out, "still-failing rows stay pending (attempts bumped); inspect with: gseer collect status\n")
	}
	return nil
}

func newCollectStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show collection bookkeeping (last jobs, dead letters)",
		Args:  cobra.NoArgs,
		RunE:  runCollectStatus,
	}
}

func runCollectRun(cmd *cobra.Command, source string, runAll bool, _ string) error {
	out := cmd.OutOrStdout()
	if source == "" && !runAll {
		return fmt.Errorf("E_USAGE: pick --source <name> or --all\nTry: gseer collect run --source my-dns")
	}
	store, closer, cfg, err := openRegistryStore(cmd)
	if err != nil {
		return err
	}
	defer closer()
	if reg, problems := fingerprintEngineFromConfig(cmd, cfg); reg != nil {
		if setter, ok := store.(fingerprintSetter); ok {
			setter.SetFingerprintEngine(reg)
		}
	} else if len(problems) > 0 {
		for _, p := range problems {
			printf(out, "[warn] fingerprint rule problem: %s\n", p)
		}
		printf(out, "[warn] fingerprint enrichment disabled for this run (fail-closed)\n")
	}
	runner := collect.NewRunner(store, nil)

	if source != "" {
		return runOneSource(cmd, runner, source)
	}

	sources, err := store.ListSources(commandContext(cmd))
	if err != nil {
		printf(out, "E_DB: %s\n", err.Error())
		exitWith(ExitUnavailable)
		return nil
	}
	if len(sources) == 0 {
		printf(out, "No sources registered.\nTry: gseer source add my-dns --type dns --seed-mode index --enable\n")
		return nil
	}
	ranAny := false
	failed := 0
	for _, s := range sources {
		if !s.Enabled {
			continue
		}
		ranAny = true
		if err := runOneSource(cmd, runner, s.Name); err != nil {
			failed++
		}
	}
	if !ranAny {
		printf(out, "No enabled sources. Enable one: gseer source enable <name>\n")
		return nil
	}
	if failed > 0 {
		exitWith(ExitUnavailable)
	}
	return nil
}

func runOneSource(cmd *cobra.Command, runner *collect.Runner, name string) error {
	out := cmd.OutOrStdout()
	printf(out, "→ %s\n", name)
	stats, err := runner.Run(commandContext(cmd), name)
	switch {
	case err == nil:
	case sourceNotFound(err):
		printf(out, "E_SOURCE_NOT_FOUND: %q is not registered\nTry: gseer source list\n", name)
		exitWith(ExitInvalidInput)
		return err
	default:
		printf(out, "%s\n", err.Error())
		exitWith(ExitUnavailable)
		return err
	}
	if stats.Skipped != "" {
		printf(out, "  skipped: %s\n", stats.Skipped)
		return nil
	}
	raw, _ := json.MarshalIndent(stats, "  ", "  ")
	printf(out, "  %s\n", string(raw))
	return nil
}

func runCollectStatus(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	store, closer, err := openSourceStore(cmd)
	if err != nil {
		return err
	}
	defer closer()

	table, err := store.CollectionStatus(commandContext(cmd))
	if err != nil {
		printf(out, "E_DB: %s\n", err.Error())
		exitWith(ExitUnavailable)
		return nil
	}

	printf(out, "SOURCE  CONNECTOR  ENABLED  JOBS  DEAD-LETTERED\n")
	if len(table) == 0 {
		printf(out, "No sources registered.\nTry: gseer source add my-dns --type dns --seed-mode index --enable\n")
		return nil
	}
	for _, r := range table {
		_, _ = fmt.Fprintf(out, "%s  %s  %-7v  %-4d  %d\n",
			pad(r.Name, 24), pad(r.Connector, 10), r.Enabled, r.Jobs, r.Pending)
	}
	printf(out, "\nJob stats (per run): gseer collect run reports them; history lives in collection_jobs.\n")
	return nil
}
