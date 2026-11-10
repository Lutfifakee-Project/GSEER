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
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/GSEER/internal/config"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
)

// db.go — the `gseer db migrate` administrative commands (M3, owner
// directive §2 of the M3 authorization round):
//
//      gseer db migrate status      read-only migration state
//      gseer db migrate diagnose    read-only recovery guidance
//      gseer db migrate force       DESTRUCTIVE metadata-only version reset
//
// Invariants (owner directive; enforced by tests AC-M3-12..15):
//   - `setup --force-migrate-version` does NOT exist; force is only
//     reachable through this command tree.
//   - status/diagnose are read-only (SELECTs only).
//   - force changes ONLY the schema_migrations metadata row; it never
//     executes or repairs SQL migrations; it requires the exact
//     confirmation phrase unless --yes is passed (--yes still prints
//     the banner so scripted runs leave an audit trail).
//   - Nothing here runs automatically: no other command calls into
//     postgres.ForceMigrationVersion.

// confirmationPhrase is the exact string the operator must type to run
// the destructive force operation (owner directive §2.6).
const confirmationPhrase = "I have inspected the database and accept the consequences"

// loadSelfHostedConfig loads the configuration from the global flags +
// environment and verifies it is the self-hosted profile (the only
// profile with a database).  Returns the loaded config or a rendered
// error.
func loadSelfHostedConfig() (*config.Config, error) {
	cfg, cerr := config.Load(config.Flags{
		ConfigPath: flags.configPath,
		Profile:    flags.profile,
		Endpoint:   flags.server,
	})
	if cerr != nil {
		return nil, cerr
	}
	if cfg.Profile != config.ProfileSelfHosted {
		return nil, fmt.Errorf("E_CLIENT_ONLY: the database is only configured for the self-hosted profile (current: %s).\n"+
			"Why: the %s profile has no PostgreSQL settings to check.\n"+
			"Try: run with --profile self-hosted, or add database settings to the config", cfg.Profile, cfg.Profile)
	}
	return cfg, nil
}

// dbDSN resolves the effective OPERATOR connection DSN from the loaded
// config (migrations, diagnostics, setup).  The result embeds the
// password and must only be passed to connection APIs — every display
// path goes through postgres.MustRedactDSN.
func dbDSN(cfg *config.Config) string {
	return cfg.Database.EffectiveDSN()
}

// runtimeCommandDSN resolves the DSN a RUNTIME data-plane command
// (search/export/collect/suppress/source/host) uses: the least-privilege
// gseer_app credential when configured (runtime_dsn /
// GSEER_DB_RUNTIME_DSN), otherwise the operator credential WITH AN
// EXPLICIT notice — the fallback is never silent (AC-8.x honesty floor;
// Rev 6.7.2 §5.2 runs the runtime grant model only when the runtime
// credential is actually used).
func runtimeCommandDSN(w io.Writer, cfg *config.Config) string {
	dsn, explicit := cfg.Database.RuntimeCredentialDSN()
	if !explicit {
		printf(w, "[note ] GSEER_DB_RUNTIME_DSN is not set — this command runs under the operator credential\n"+
			"        Hardened path: gseer setup --self-hosted provisions gseer_app and prints the runtime DSN once (docs/DEPLOYMENT.md §2)\n")
	}
	return dsn
}

// registerDBCmd attaches `gseer db` with its `migrate` subtree.
func registerDBCmd(root *cobra.Command) {
	db := &cobra.Command{
		Use:   "db",
		Short: "Database administration (migrations)",
		Args:  cobra.NoArgs,
	}
	migrate := &cobra.Command{
		Use:   "migrate",
		Short: "Inspect and recover database migrations",
		Args:  cobra.NoArgs,
	}
	migrate.AddCommand(
		newDBMigrateStatusCmd(),
		newDBMigrateDiagnoseCmd(),
		newDBMigrateForceCmd(),
	)
	db.AddCommand(migrate)
	db.AddCommand(newDBBackupCmd(), newDBRestoreCmd())
	root.AddCommand(db)
}

func newDBMigrateStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the migration state (read-only)",
		Long: `Show the migration state (read-only).

Reports the binary's head migration version, the database's recorded
version + dirty flag, and the resulting state: AT_HEAD, BEHIND, AHEAD,
DIRTY, or UNINITIALIZED.  Issues SELECT queries only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadSelfHostedConfig()
			if err != nil {
				return err
			}
			st, err := postgres.GetMigrationStatus(cmd.Context(), dbDSN(cfg))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			printf(out, "Database:     %s   [redacted]\n", postgres.MustRedactDSN(dbDSN(cfg)))
			printf(out, "Binary head:  v%d\n", st.Head)
			printf(out, "Database:     version=%d, dirty=%v\n", st.Version, st.Dirty)
			printf(out, "Status:       %s\n", st.State)
			switch st.State {
			case postgres.StateAtHead:
				println(out, "\nNo action needed.")
			case postgres.StateBehind:
				println(out, "\nPending migrations:")
				for _, p := range st.Pending {
					printf(out, "  %s\n", p)
				}
				println(out, "\nNext: gseer setup --self-hosted  (or gseer server start)")
			case postgres.StateAhead:
				println(out, "\nNext: update the gseer binary to match the database.")
			case postgres.StateDirty:
				println(out, "\nThe last migration failed and its transaction was rolled back.")
				println(out, "Run: gseer db migrate diagnose")
			case postgres.StateUninitialized:
				println(out, "\nThe database has never been migrated.")
				println(out, "Next: gseer setup --self-hosted")
			}
			return nil
		},
	}
}

func newDBMigrateDiagnoseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diagnose",
		Short: "Explain the migration state and the recovery procedure (read-only)",
		Long: `Explain the migration state and the recovery procedure (read-only).

When the database is DIRTY, prints the failed migration file, the
pre-recovery database inspection queries, and the exact (confirmation-
gated) force command.  Issues SELECT queries only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadSelfHostedConfig()
			if err != nil {
				return err
			}
			d, err := postgres.DiagnoseMigration(cmd.Context(), dbDSN(cfg))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			printf(out, "Database:     %s   [redacted]\n", postgres.MustRedactDSN(dbDSN(cfg)))
			printf(out, "Binary head:  v%d\n", d.Status.Head)
			printf(out, "Database:     version=%d, dirty=%v\n", d.Status.Version, d.Status.Dirty)
			printf(out, "Status:       %s\n\n", d.Status.State)

			switch d.Status.State {
			case postgres.StateAtHead:
				println(out, "The schema is at head; no recovery needed.")
				return nil
			case postgres.StateUninitialized:
				println(out, "The database has never been migrated.")
				println(out, "Next: gseer setup --self-hosted")
				return nil
			case postgres.StateBehind:
				println(out, "Pending migrations:")
				for _, p := range d.Status.Pending {
					printf(out, "  %s\n", p)
				}
				println(out, "\nNext: gseer setup --self-hosted  (or gseer server start)")
				return nil
			case postgres.StateAhead:
				println(out, "The database is newer than this binary.")
				println(out, "Next: update the gseer binary.")
				return nil
			}

			// DIRTY — the recovery procedure (owner directive §2.4).
			println(out, "The last migration failed and its transaction was rolled back.")
			println(out, "The schema is effectively at the previous version, but")
			printf(out, "schema_migrations records v%d as dirty.\n\n", d.Status.Version)
			if d.FailedFile != "" {
				printf(out, "Failed migration:  %s  (embedded in this binary)\n\n", d.FailedFile)
			}
			println(out, "Pre-recovery database inspection — verify the schema is consistent")
			println(out, "with the previous version (the rolled-back migration left no DDL):")
			for _, q := range d.InspectionSQL {
				printf(out, "  %s\n", q)
			}
			println(out)
			if d.ForceHint != "" {
				println(out, "If the database is consistent with the previous version, reset the")
				println(out, "migration metadata to it:")
				printf(out, "  %s\n\n", d.ForceHint)
				println(out, "The force command:")
				println(out, "  - DELETES all rows from schema_migrations and INSERTS (version=N, dirty=false)")
				println(out, "  - Does NOT execute, roll back, or repair any SQL migration")
				println(out, "  - Does NOT fix any database corruption left by the failed migration")
				println(out, "  - Requires the explicit confirmation phrase (or --yes for scripting)")
				println(out)
				println(out, "After forcing, re-run migrations: gseer setup --self-hosted")
			}
			println(out)
			println(out, "If the database is NOT consistent with the previous version, manually")
			println(out, "repair it BEFORE forcing; forcing without repairing leaves the schema")
			println(out, "in an inconsistent state.")
			return nil
		},
	}
}

// readForceConfirmation reads the operator's reply to the exact-phrase
// request (the prompt itself is printed by the caller).  Returns
// (true, nil) only on the EXACT phrase; (false, nil) on a wrong
// phrase; (false, err) when no input is available at all.  Extracted
// so destructive-operation tests verify the gate without a subprocess
// (os.Exit cannot run inside a test binary).
func readForceConfirmation(in io.Reader) (confirmed bool, err error) {
	reader := bufio.NewReader(in)
	line, rerr := reader.ReadString('\n')
	if rerr != nil && line == "" {
		return false, fmt.Errorf("E_DB_USAGE: no confirmation input; no changes were made.\n" +
			"Try: re-run interactively, or pass --yes for scripted recovery")
	}
	return strings.TrimRight(line, "\r\n") == confirmationPhrase, nil
}

func newDBMigrateForceCmd() *cobra.Command {
	var (
		version int
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "force --version N",
		Short: "Reset migration version metadata (DESTRUCTIVE, confirmation-gated)",
		Long: `Reset migration version metadata (DESTRUCTIVE, confirmation-gated).

This command ONLY changes the schema_migrations metadata row:
  - DELETEs all rows from schema_migrations, then INSERTs (version=N, dirty=false)
It does NOT execute, roll back, or repair any SQL migration, and does
NOT fix database corruption left by a failed migration.

Prerequisites (verify BEFORE running; see ` + "`gseer db migrate diagnose`" + `):
  1. Reviewed ` + "`gseer db migrate diagnose`" + ` output.
  2. Inspected the database schema manually (tables/views/indexes).
  3. Manually repaired any partial DDL so the database is consistent
     with the target version.
  4. Documented the recovery in your operations log.

The command never runs automatically and is never invoked by any other
gseer command.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadSelfHostedConfig()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			// The pre-flight banner prints in BOTH modes (audit trail).
			println(out)
			println(out, "⚠  DESTRUCTIVE METADATA OPERATION")
			println(out)
			println(out, "This command will:")
			println(out, "  1. DELETE all rows from schema_migrations")
			printf(out, "  2. INSERT a single row: (version=%d, dirty=false)\n", version)
			println(out, "  3. It will NOT execute, roll back, or repair any SQL migration.")
			println(out, "  4. It will NOT fix any database corruption left by a failed migration.")
			println(out)

			dsn := dbDSN(cfg)
			head, herr := postgres.HeadVersion()
			if herr != nil {
				return herr
			}
			if version < 0 || version > int(head) {
				return fmt.Errorf("E_DB_USAGE: --version %d is out of range; the embedded migrations span 0..%d.\n"+
					"Try: re-run with a version between 0 and %d (see: gseer db migrate status)",
					version, head, head)
			}

			if !yes {
				println(out, "Before proceeding, you MUST have completed ALL of the following:")
				println(out, "  [1] Run `gseer db migrate diagnose` and reviewed the failed migration.")
				println(out, "  [2] Inspected the database schema manually (tables/views/indexes).")
				println(out, "  [3] Manually repaired any partial DDL so the database is consistent")
				printf(out, "      with version %d.\n", version)
				println(out, "  [4] Documented this recovery in your operations log.")
				println(out)
				println(out, "Type the following exact phrase to proceed:")
				printf(out, "  %s\n", confirmationPhrase)
				printf(out, "\n> ")

				confirmed, cerr := readForceConfirmation(cmd.InOrStdin())
				if cerr != nil {
					return cerr
				}
				if !confirmed {
					println(out)
					println(out, "Confirmation phrase did not match. No changes were made.")
					println(out, "schema_migrations was not modified. No SQL was executed.")
					os.Exit(ExitUsagePlanned)
				}
			} else {
				println(out, "--yes was passed. Proceeding WITHOUT interactive confirmation.")
				println(out, "Ensure the pre-recovery inspection (gseer db migrate diagnose) is complete.")
				println(out)
			}

			println(out, "Executing: DELETE FROM schema_migrations; INSERT (version=N, dirty=false);")
			if err := postgres.ForceMigrationVersion(dsn, cfg.Database.MigrationLockTimeoutMs, version); err != nil {
				return err
			}
			printf(out, "Result: schema_migrations now at (version=%d, dirty=false).\n", version)
			println(out, "Next: run `gseer setup --self-hosted` to apply remaining migrations.")
			return nil
		},
	}
	cmd.Flags().IntVar(&version, "version", -1, "the migration version to force to (0..head)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the interactive confirmation (scripted use; the banner still prints)")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

// newDBBackupCmd attaches `gseer db backup` — the personal-mode online
// backup (SQLite VACUUM INTO; one consistent snapshot).  The self-hosted
// profile backs up with PostgreSQL tooling (pg_dump) and is refused here
// honestly rather than half-supported.
func newDBBackupCmd() *cobra.Command {
	var outPath string
	cmd := &cobra.Command{
		Use:   "backup --out <file>",
		Short: "Backup the personal database (consistent online snapshot)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadEngineConfig()
			if err != nil {
				return err
			}
			if cfg.Profile != config.ProfilePersonal {
				return fmt.Errorf("E_DB_BACKUP: gseer db backup currently supports the personal profile; self-hosted PostgreSQL backups use pg_dump (docs/DEPLOYMENT.md)")
			}
			return runPersonalDBBackup(cmd, outPath)
		},
	}
	cmd.Flags().StringVar(&outPath, "out", "", "backup file path (default: <database>.backup-<UTC timestamp>)")
	return cmd
}

// newDBRestoreCmd attaches `gseer db restore <backup>` — replaces the
// personal database file with the backup (confirmation-gated; the old
// file is kept aside).
func newDBRestoreCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore <backup-file>",
		Short: "Restore the personal database from a backup snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadEngineConfig()
			if err != nil {
				return err
			}
			if cfg.Profile != config.ProfilePersonal {
				return fmt.Errorf("E_DB_RESTORE: gseer db restore currently supports the personal profile")
			}
			return runPersonalDBRestore(cmd, args[0], yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
