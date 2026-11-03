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
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/GSEER/internal/config"
	"github.com/Lutfifakee-Project/GSEER/internal/fixture"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
)

// setup.go — `gseer setup` (SPEC-03 §2; QS-1–QS-3, QS-6).
//
// M3 wired the self-hosted provisioning path: reachability check → PG
// version check (PD-8) → migrations to head, with the "migrations: N
// applied, schema at head vX.Y" report line (QS-3).  M4 wires the
// fixture dataset actions:
//
//      --load-fixtures   ingest the labeled synthetic corpus (idempotent;
//                        L1/L2 dedup) after provisioning
//      --purge-fixtures  remove every fixture-tagged row in one scoped
//                        transaction (real observations are unreachable
//                        by the purge predicates)
//
// Both fixture actions pass the QS-6 gate (setupFixturesGate): the
// action proceeds without prompting when policy.evaluation_fixtures is
// true (the config IS the operator's standing consent), and otherwise
// requires an interactive y/yes.  Non-interactive stdin with the policy
// off is refused BEFORE any database contact — a piped script can never
// silently load or delete fixture rows.
// --db-url overrides the saved DSN for this invocation.
//
// --with-compose writes the db-only docker-compose.yml (the canonical
// template shipped since M1) and a generated .env with a random
// password, then prints the next commands verbatim (QS-2).

// registerSetupCmd attaches `gseer setup`.
func registerSetupCmd(root *cobra.Command) {
	var (
		selfHosted    bool
		withCompose   bool
		withFull      bool
		dbURL         string
		loadFixtures  bool
		purgeFixtures bool
	)
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure an endpoint (client) or initialize self-hosted mode",
		Long: `Configure an endpoint (client) or initialize self-hosted mode.

Two profiles (SPEC-03 §5):
  - client:        configures a remote endpoint URL (no PostgreSQL needed).
  - self-hosted:   verifies the database is reachable, checks the
                   PostgreSQL version (15+, PD-8), and runs migrations
                   to head — no manual SQL (QS-3).

Fixture dataset (QS-6, labeled synthetic evaluation data):
  --load-fixtures    ingest the embedded synthetic corpus (Assets A–D,
                     Services s1–s6, seven observations).  Idempotent:
                     re-loading dedupes on the delivery identity.
  --purge-fixtures   delete every fixture-tagged observation/service/asset
                     in ONE scoped transaction; rows not tagged by the
                     fixture source are never touched.
  Both act on the self-hosted database (use with --self-hosted, or after
  a completed setup).  When policy.evaluation_fixtures is false in the
  config, each action asks for an interactive y/yes confirmation; the
  result is always reported under the honesty banner:

    fixture dataset — synthetic observations for evaluation; NOT real internet data

The fresh-install workflow is documented in docs/QUICKSTART.md
(canonical) and pinned by the quickstart-linux CI job (AC-5.11).

Wired: --self-hosted provisioning (M3), --load-fixtures/--purge-fixtures
(M4), --with-compose compose + .env generation (M11; --full writes the
complete db + server stack, M22).

--with-compose always writes the files FIRST and stops: on a fresh
machine the database only exists once compose starts it, so the printed
next commands (docker compose up / gseer setup --self-hosted) are the
continuation.  --self-hosted implies the self-hosted profile; an
explicit --profile still wins.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			if loadFixtures && purgeFixtures {
				return fmt.Errorf("E_SETUP_USAGE: --load-fixtures and --purge-fixtures are mutually exclusive " +
					"in one invocation.\n" +
					"Why: loading and purging the same dataset in one run has no meaningful result.\n" +
					"Try: run them as two separate `gseer setup` invocations")
			}

			// Compose generation (QS-2) ALWAYS runs first and
			// terminates the invocation when requested: on a fresh
			// machine the database cannot be reachable before compose
			// starts it, so `--with-compose` (with or without
			// --self-hosted) means "write the files, print the next
			// commands verbatim".  Provisioning is the operator's
			// NEXT command (the Next block below prints it) —
			// attempting it here could only fail with
			// E_DB_UNREACHABLE on exactly the machine that needs
			// the bootstrap.  This is the contract the config
			// layer's own hint names: "run: gseer setup
			// --self-hosted --with-compose  (to provision one)".
			if withCompose {
				return runSetupWithCompose(cmd, ".", withFull)
			}

			// --self-hosted implies the self-hosted profile: the
			// flag exists so a new operator does not have to know
			// about --profile/GSEER_PROFILE first (the
			// E_CONFIG_REQUIRED hint itself recommends
			// `gseer setup --self-hosted`).  An explicit
			// --profile still wins.
			profile := flags.profile
			if selfHosted && profile == "" {
				profile = string(config.ProfileSelfHosted)
			}

			cfg, cerr := config.Load(config.Flags{
				ConfigPath: flags.configPath,
				Profile:    profile,
				Endpoint:   flags.server,
			})
			if cerr != nil {
				return cerr
			}

			if selfHosted {
				if err := runSetupSelfHosted(cmd, cfg, dbURL, loadFixtures || purgeFixtures); err != nil {
					return err
				}
				if loadFixtures || purgeFixtures {
					// fall through to the fixture action below
				} else {
					return nil
				}
			}

			// Fixture actions (M4): resolve the DSN exactly like the
			// self-hosted path (--db-url override wins), then run the
			// requested action behind the QS-6 gate.
			if loadFixtures {
				return runSetupLoadFixtures(cmd, cfg, dbURL)
			}
			if purgeFixtures {
				return runSetupPurgeFixtures(cmd, cfg, dbURL)
			}

			// Client-profile setup: persisting the chosen endpoint to the
			// config file is M11 wiring (it needs the profile wizard).
			printf(out, "gseer setup (client profile): endpoint %s\n", cfg.Client.Endpoint)
			if cfg.Client.Endpoint == "" {
				printf(out, "no endpoint configured yet.\n")
				printf(out, "Try: gseer setup --server https://gseer.example.com   (M11 wizard)\n")
				printf(out, "     or set GSEER_CLIENT_ENDPOINT and --profile client\n")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&selfHosted, "self-hosted", false, "initialize self-hosted mode (PostgreSQL + server)")
	cmd.Flags().BoolVar(&withCompose, "with-compose", false, "write a db-only docker-compose.yml + generated .env (QS-2)")
	cmd.Flags().BoolVar(&withFull, "full", false, "with --with-compose: write the FULL stack (db + server, healthchecks, token) as docker-compose.full.yml")
	cmd.Flags().StringVar(&dbURL, "db-url", "", "override the saved DSN (used with --self-hosted and the fixture actions)")
	cmd.Flags().BoolVar(&loadFixtures, "load-fixtures", false, "load the labeled synthetic fixture dataset (QS-6; idempotent)")
	cmd.Flags().BoolVar(&purgeFixtures, "purge-fixtures", false, "remove the fixture dataset and report the counts (QS-6; scoped to fixture-tagged rows)")
	root.AddCommand(cmd)
}

// commandContext returns the command's execution context, falling back
// to a live background context when RunE was invoked directly (cobra
// only sets the context on the Execute path; a nil context would panic
// deep inside the storage layer instead of failing with a real error).
func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// setupFixturesDSN resolves the effective DSN for the fixture actions:
// --db-url override first, then the configured database settings.  The
// result embeds the password and must only be passed to connection
// APIs — every display path goes through postgres.MustRedactDSN.
func setupFixturesDSN(cfg *config.Config, dbURL string) string {
	if dbURL != "" {
		return dbURL
	}
	return cfg.Database.EffectiveDSN()
}

// provisionSelfHosted runs the shared M3 provisioning core so both the
// plain --self-hosted path and the M4 fixture actions see the exact
// same checks: reachability → PG version floor (PD-8) → migrations to
// head.  Each step reports on out; the first failure returns the
// rendered error and stops before any later step.
func provisionSelfHosted(cmd *cobra.Command, cfg *config.Config, dsn string) error {
	out := cmd.OutOrStdout()
	ctx := commandContext(cmd)

	// 1. Reachability BEFORE migrating (SPEC-05 §1; owner directive §5.4).
	if err := postgres.CheckReachable(ctx, dsn); err != nil {
		return err
	}
	printf(out, "[ ok  ] DB reachable at %s\n", postgres.MustRedactDSN(dsn))

	// 2. PG version floor (PD-8).
	major, err := postgres.CheckPGVersion(ctx, dsn, cfg.Defaults.PostgresMajor)
	if err != nil {
		return err
	}
	printf(out, "[ ok  ] PG version: %d (minimum %d, PD-8)\n", major, cfg.Defaults.PostgresMajor)

	// 3. Migrations to head — one atomic transaction per migration;
	//    bounded advisory-lock wait (DB_MIGRATION_LOCK_TIMEOUT on
	//    contention); dirty state fails with recovery guidance.
	applied, head, err := postgres.RunMigrations(dsn, cfg.Database.MigrationLockTimeoutMs)
	if err != nil {
		return err
	}
	printf(out, "[ ok  ] migrations: %d applied, schema at head v%d\n", applied, head)
	return nil
}

// runSetupSelfHosted performs the M3 self-hosted provisioning via the
// shared core, then prints the next-steps tail.  Never runs fixtures
// and never binds a listener (that is M4/M10/M11 work); fixtureFollows
// suppresses the tail when a fixture action follows in the same run.
func runSetupSelfHosted(cmd *cobra.Command, cfg *config.Config, dbURL string, fixtureFollows bool) error {
	out := cmd.OutOrStdout()

	if err := provisionSelfHosted(cmd, cfg, setupFixturesDSN(cfg, dbURL)); err != nil {
		return err
	}

	if fixtureFollows {
		_, _ = fmt.Fprintln(out) // separator before the fixture report
		return nil
	}

	printf(out, "\nSelf-hosted mode ready. Next:\n")
	printf(out, "  gseer doctor                    — verify the environment\n")
	printf(out, "  gseer server start              — start the API + dashboard (M10)\n")
	if cfg.Policy.EvaluationFixtures {
		printf(out, "  gseer setup --load-fixtures     — load the synthetic dataset (QS-6)\n")
	}
	return nil
}

// runSetupLoadFixtures loads the embedded fixture corpus behind the
// QS-6 gate: consent FIRST (before any database contact), then the
// shared provisioning core (idempotent at head), then the store ingest
// and the honesty banner + counts report.
func runSetupLoadFixtures(cmd *cobra.Command, cfg *config.Config, dbURL string) error {
	out := cmd.OutOrStdout()
	ctx := commandContext(cmd)

	if err := confirmFixtureAction(cmd, cfg, "load", "loaded",
		"load the labeled synthetic fixture dataset into the database",
		"7 observations / 6 services / 4 assets (SPEC-04 §9) tagged provenance=fixture",
		"Re-running is idempotent (delivery-identity dedup); the data is NOT real internet data."); err != nil {
		return err
	}

	dsn := setupFixturesDSN(cfg, dbURL)
	if err := provisionSelfHosted(cmd, cfg, dsn); err != nil {
		return err
	}

	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	stats, err := fixture.Load(ctx, postgres.NewStore(pool))
	if err != nil {
		return err
	}

	printf(out, "[ ok  ] fixture source registered (data_sources id=%d, connector=fixture, enabled=false)\n", stats.SourceID)
	printf(out, "[ ok  ] fixtures loaded: %d assets inserted, %d updated; %d services inserted, %d updated; %d observations inserted, %d deduped\n",
		stats.AssetsInserted, stats.AssetsUpdated,
		stats.ServicesInserted, stats.ServicesUpdated,
		stats.ObservationsInserted, stats.ObservationsDeduped)
	printf(out, "\n%s\n", fixture.Banner)
	printf(out, "Query them with: gseer search 'port=443'   — every surface labels this data as synthetic.\nPurge with: gseer setup --purge-fixtures\nStart the API + dashboard: gseer server start\n")
	return nil
}

// runSetupPurgeFixtures removes every fixture-tagged row behind the
// QS-6 gate.  The purge is scoped by construction (store-side
// PurgeFixtureData binds to the fixture source id AND provenance), but
// the gate still asks: deletion is deletion, and the operator should
// never be surprised by either direction of the fixture lifecycle.
func runSetupPurgeFixtures(cmd *cobra.Command, cfg *config.Config, dbURL string) error {
	out := cmd.OutOrStdout()
	ctx := commandContext(cmd)

	if err := confirmFixtureAction(cmd, cfg, "purge", "purged",
		"purge the labeled synthetic fixture dataset from the database",
		"every observation carrying the fixture source id AND provenance=fixture, "+
			"plus services/assets left fully unreferenced after that deletion",
		"Rows from real sources are unreachable by the purge predicates and are never deleted."); err != nil {
		return err
	}

	dsn := setupFixturesDSN(cfg, dbURL)
	if err := provisionSelfHosted(cmd, cfg, dsn); err != nil {
		return err
	}

	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	stats, err := postgres.PurgeFixtureData(ctx, pool)
	if err != nil {
		return err
	}

	if !stats.FixtureSourcePresent {
		printf(out, "[ ok  ] no fixture source registered — nothing to purge (0 rows removed)\n")
		printf(out, "\n%s\n", fixture.Banner)
		return nil
	}
	printf(out, "[ ok  ] fixtures purged: %d observations, %d services, %d assets removed (one transaction)\n",
		stats.Observations, stats.Services, stats.Assets)
	printf(out, "\n%s\n", fixture.Banner)
	printf(out, "The fixture registry row remains (configuration, not data); re-load any time with `gseer setup --load-fixtures`.\n")
	return nil
}

// confirmFixtureAction implements the QS-6 fixture gate (owner-approved
// default: policy.evaluation_fixtures=false on non-dev installs).
//
//   - policy true  → the operator's standing consent; proceed silently.
//   - policy false → print what will happen and require an interactive
//     y/yes on the command's stdin.  An empty/EOF stdin is an error
//     BEFORE any database contact (piped scripts cannot accidentally
//     consent); any other reply refuses with the planned-usage exit so
//     `gseer setup --load-fixtures | cron` can never mutate the DB.
//
// action is "load" or "purge" (also the fixture flag name); done is the
// matching past participle ("loaded"/"purged") for the refusal line;
// what/count/assurance fill the prompt so both actions share one gate
// implementation without losing specificity.
func confirmFixtureAction(cmd *cobra.Command, cfg *config.Config, action, done, what, count, assurance string) error {
	out := cmd.OutOrStdout()
	if cfg.Policy.EvaluationFixtures {
		printf(out, "[ ok  ] QS-6 gate: policy.evaluation_fixtures=true — standing consent, proceeding\n")
		return nil
	}

	printf(out, "QS-6 fixture gate: you asked gseer to %s.\n", what)
	printf(out, "  Scope:     %s\n", count)
	printf(out, "  Assurance: %s\n", assurance)
	printf(out, "\npolicy.evaluation_fixtures is false, so this needs an explicit confirmation.\n")
	printf(out, "Proceed? [y/N] ")

	confirmed, cerr := readFixtureConfirmation(cmd.InOrStdin())
	if cerr != nil {
		return fmt.Errorf("E_FIXTURE_CONFIRM: no confirmation input; nothing was %s.\n"+
			"Why: policy.evaluation_fixtures is false, so `gseer setup --%s-fixtures` requires an "+
			"interactive confirmation before any database contact.\n"+
			"Try: re-run interactively, or set policy.evaluation_fixtures: true in the config "+
			"to record standing consent for the fixture lifecycle",
			done, action)
	}
	if !confirmed {
		printf(out, "\nRefused. Nothing was %s; no database connection was made.\n", done)
		os.Exit(ExitUsagePlanned)
	}
	printf(out, "\n")
	return nil
}

// readFixtureConfirmation reads the operator's reply to the QS-6
// y/yes prompt (the prompt itself is printed by the caller).  Returns
// (true, nil) on y/yes (case-insensitive); (false, nil) on any other
// reply; (false, err) when no input is available at all.  Extracted so
// gate tests verify every branch without a subprocess (os.Exit cannot
// run inside a test binary) — same shape as readForceConfirmation.
func readFixtureConfirmation(in io.Reader) (confirmed bool, err error) {
	reader := bufio.NewReader(in)
	line, rerr := reader.ReadString('\n')
	if rerr != nil && line == "" {
		return false, fmt.Errorf("no confirmation input")
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
