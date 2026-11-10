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

// first_run.go — the zero-configuration entry point (v0.3.0).
//
// Bare `gseer`:
//   - `--version`              → the version block (the flag existed
//     since M1 but was never consumed — fixed here);
//   - no config file           → auto-initialize personal mode (one
//     SQLite file; no database server, no Docker) and show the user
//     how data enters the index;
//   - a config file exists     → a short, honest status plus the most
//     useful next commands (read-only; never a panic).
//
// The goal is the Fresh Install path: `go install … && gseer` ends
// with a working engine and a user who knows what to do next.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/GSEER/internal/config"
	"github.com/Lutfifakee-Project/GSEER/internal/version"
)

// runFirstRun implements the bare `gseer` invocation.
func runFirstRun(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()

	if flags.showVer {
		_, err := fmt.Fprint(out, version.Current().String())
		return err
	}

	cfgPath := flags.configPath
	if cfgPath == "" {
		cfgPath = defaultConfigPath()
	}
	if _, err := os.Stat(cfgPath); err == nil {
		return runStatus(cmd)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("E_CONFIG: cannot inspect %s: %w", cfgPath, err)
	}

	// First run: provision personal mode through the standard setup path
	// (same permissions, same migrations, same config writer — no second
	// initialization code path to drift).
	if !flags.quiet {
		printf(out, "Welcome to GSEER — internet-asset search engine.\n")
		printf(out, "No config at %s — initializing PERSONAL MODE (one local SQLite file; no database server, no Docker).\n\n", cfgPath)
	}
	if err := runSetupPersonal(cmd, "", false); err != nil {
		return err
	}
	if !flags.quiet {
		printf(out, "\nWhere does the data come from? GSEER stores only what YOU collect or import.\n")
		printf(out, "  try it now:  gseer setup --load-fixtures   (labeled synthetic corpus — NOT real internet data)\n")
		printf(out, "  real data:   gseer source add my-dns --type dns --seeds <domains> --enable --yes\n")
		printf(out, "               gseer collect run --source my-dns\n")
		printf(out, "  full check:  gseer doctor\n")
	}
	return nil
}

// runStatus is the later-runs panel: what is configured, what the
// personal index holds, and the shortest path to the next useful
// action.  Read-only by contract.
func runStatus(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	if err := printBanner(out); err != nil {
		return err
	}
	cfg, err := config.Load(config.Flags{
		ConfigPath: flags.configPath,
		Profile:    flags.profile,
		Endpoint:   flags.server,
	})
	if err != nil {
		return fmt.Errorf("E_CONFIG: %s\nTry: gseer doctor", err.Error())
	}
	printf(out, "\n")
	switch cfg.Profile {
	case config.ProfilePersonal:
		printf(out, "profile: personal (database: %s)\n", cfg.Database.DSN)
		printPersonalIndex(cmd, cfg)
	case config.ProfileSelfHosted:
		printf(out, "profile: self-hosted (PostgreSQL)\n")
		printf(out, "  checks:    gseer doctor\n")
		printf(out, "  search:    gseer search 'port==443'\n")
		printf(out, "  collect:   gseer collect run --all\n")
	default:
		printf(out, "profile: client (server: %s)\n", cfg.Client.Endpoint)
		printf(out, "  search:    gseer search 'port==443'\n")
	}
	printf(out, "  all commands: gseer --help\n")
	return nil
}

// printPersonalIndex reports the personal index contents (read-only;
// a missing or unreadable database file is an honest hint, never a
// panic).
func printPersonalIndex(cmd *cobra.Command, cfg *config.Config) {
	out := cmd.OutOrStdout()
	svc, closeDB, err := personalSearchService(cfg)
	if err != nil {
		printf(out, "  index:    not readable (%v)\n  fix:      gseer setup --personal\n", err)
		return
	}
	defer closeDB()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	meta, err := svc.Stats(ctx)
	if err != nil {
		printf(out, "  index:    not readable (%v)\n", err)
		return
	}
	switch {
	case meta.Empty:
		printf(out, "  index:    EMPTY — nothing collected yet\n")
		printf(out, "  try:      gseer setup --load-fixtures   (labeled synthetic evaluation corpus)\n")
		printf(out, "  real:     gseer source add my-dns --type dns --seeds <domains> --enable --yes\n")
		printf(out, "            gseer collect run --source my-dns\n")
	case meta.FixtureOnly:
		printf(out, "  index:    %d assets, %d services, %d observations (SYNTHETIC fixture data — not real internet data)\n",
			meta.Assets, meta.Services, meta.Observations)
		printf(out, "  real:     gseer source add my-dns --type dns --seeds <domains> --enable --yes\n")
		printf(out, "            gseer collect run --source my-dns\n")
	default:
		printf(out, "  index:    %d assets, %d services, %d observations\n",
			meta.Assets, meta.Services, meta.Observations)
	}
}
