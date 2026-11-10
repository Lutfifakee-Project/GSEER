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
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
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
		selfHosted           bool
		personal             bool
		personalDBPath       string
		withCompose          bool
		withFull             bool
		dbURL                string
		loadFixtures         bool
		purgeFixtures        bool
		resetRuntimePassword bool
		runtimePasswordEnv   string
	)
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure an endpoint (client) or initialize self-hosted mode",
		Long: `Configure an endpoint (client) or initialize self-hosted mode.

Two profiles (SPEC-03 §5):
  - client:        configures a remote endpoint URL (no PostgreSQL needed).
  - personal:      provisions ONE embedded SQLite database file (SQ-01..06
                   enforced: 0600 file, WAL, migrations, suppression) —
                   run: gseer setup --personal [--db-path <file>] [--load-fixtures]
  - self-hosted:   verifies the database is reachable, checks the
                   PostgreSQL version (16+, OD-7), and runs the hardened
                   v0.2.0 deployment order — no manual SQL (QS-3):

                     1. role bootstrap      gseer_policy_owner + gseer_app + D-47
                     2. migrations to head  0001…0017
                     3. runtime grants      the least-privilege gseer_app matrix
                     4. privilege audit     fail-closed (OD-5); a failure
                                            aborts the deployment

                   When the bootstrap creates gseer_app, a random password
                   is generated and the runtime DSN is printed once —
                   store it in GSEER_DB_RUNTIME_DSN (the server requires
                   it; docs/DEPLOYMENT.md).  --reset-runtime-password
                   rotates the gseer_app password on an existing role.

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

			if personal {
				if selfHosted {
					return fmt.Errorf("E_SETUP_USAGE: --personal and --self-hosted are mutually exclusive in one invocation")
				}
				if purgeFixtures {
					return fmt.Errorf("E_SETUP_USAGE: --purge-fixtures is not a personal setup flag; load fixtures with --load-fixtures")
				}
				return runSetupPersonal(cmd, personalDBPath, loadFixtures)
			}

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
				if err := runSetupSelfHosted(cmd, cfg, dbURL, loadFixtures || purgeFixtures, resetRuntimePassword, runtimePasswordEnv); err != nil {
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
	cmd.Flags().BoolVar(&personal, "personal", false, "initialize PERSONAL mode (one embedded SQLite database file; no PostgreSQL)")
	cmd.Flags().StringVar(&personalDBPath, "db-path", "", "personal database file path (default: ~/.gseer/gseer.db)")
	cmd.Flags().BoolVar(&withCompose, "with-compose", false, "write a db-only docker-compose.yml + generated .env (QS-2)")
	cmd.Flags().BoolVar(&withFull, "full", false, "with --with-compose: write the FULL stack (db + server, healthchecks, token) as docker-compose.full.yml")
	cmd.Flags().StringVar(&dbURL, "db-url", "", "override the saved DSN (used with --self-hosted and the fixture actions)")
	cmd.Flags().BoolVar(&loadFixtures, "load-fixtures", false, "load the labeled synthetic fixture dataset (QS-6; idempotent)")
	cmd.Flags().BoolVar(&purgeFixtures, "purge-fixtures", false, "remove the fixture dataset and report the counts (QS-6; scoped to fixture-tagged rows)")
	cmd.Flags().BoolVar(&resetRuntimePassword, "reset-runtime-password", false, "rotate the gseer_app runtime password and print the runtime DSN again")
	cmd.Flags().StringVar(&runtimePasswordEnv, "runtime-password-env", "", "set the gseer_app password from this env var (deterministic provisioning, e.g. the compose full stack); implies a password set")
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

// provisionResult reports what the hardened provisioning did so the
// tail can hand off the runtime credential exactly once.
type provisionResult struct {
	// roleAppCreated is true when the bootstrap created gseer_app in
	// this run (its password is only then known here).
	roleAppCreated bool
	// runtimeDSN is the gseer_app DSN with the freshly generated
	// password (empty unless this run set the password).
	runtimeDSN string
}

// provisionSelfHosted runs the hardened v0.2.0 deployment order (Rev
// 6.7.2 §6.2; roadmap M-A/M-B/M-C/M-D) so both the plain --self-hosted
// path and the M4 fixture actions see the exact same steps:
//
//	reachability → PG floor (OD-7) → role bootstrap (step 1) →
//	migrations to head (step 2) → runtime grants (step 3) →
//	runtime password (when created/reset) → privilege audit (step 4,
//	fail-closed).
//
// Every step reports on out; the first failure stops before any later
// step.  A failed privilege audit refuses the deployment with exit 5
// (OD-5: no runtime traffic past a failed audit — there is no bypass).
// provisionRuntimeCredential sets gseer_app's password when the role was
// created here, on explicit rotation, or on deterministic
// --runtime-password-env provisioning, and records the derived runtime
// DSN on the result.  Extracted from provisionSelfHosted to keep that
// function under the gocyclo gate; the error messages, wrapping, and
// printf output are byte-identical to the inline original.
func provisionRuntimeCredential(ctx context.Context, dsn string, res *provisionResult, resetRuntimePassword bool, runtimePasswordEnv string, out io.Writer) error {
	if !res.roleAppCreated && !resetRuntimePassword && runtimePasswordEnv == "" {
		return nil
	}
	var pw string
	if runtimePasswordEnv != "" {
		pw = os.Getenv(runtimePasswordEnv)
		if pw == "" {
			return fmt.Errorf("E_RUNTIME_PASSWORD_ENV: --runtime-password-env %q is empty or unset\n"+
				"Why: deterministic provisioning needs the password present in the environment\n"+
				"Try: set %s (e.g. in the compose .env) and re-run", runtimePasswordEnv, runtimePasswordEnv)
		}
	} else {
		generated, gerr := generateRuntimePassword()
		if gerr != nil {
			return fmt.Errorf("E_RUNTIME_PASSWORD: cannot generate the gseer_app password: %s", gerr)
		}
		pw = generated
	}
	if serr := postgres.SetAppRolePassword(ctx, dsn, pw); serr != nil {
		return fmt.Errorf("E_RUNTIME_PASSWORD: cannot set the gseer_app password: %s\n"+
			"Why: the runtime credential needs a password before anything can log in as gseer_app\n"+
			"Try: run as a credential that may ALTER ROLE, or set the password manually, then re-run with --reset-runtime-password", serr)
	}
	rdsn, berr := runtimeDSNFromOperator(dsn, postgres.RoleApp, pw)
	if berr != nil {
		printf(out, "[ ok  ] runtime credential: password set for %s (build the runtime DSN yourself — the operator DSN is not URL-shaped)\n", postgres.RoleApp)
	} else {
		res.runtimeDSN = rdsn
	}
	printf(out, "[ ok  ] runtime credential: password set for %s (rotate anytime: gseer setup --self-hosted --reset-runtime-password)\n", postgres.RoleApp)
	return nil
}

func provisionSelfHosted(cmd *cobra.Command, cfg *config.Config, dsn string, resetRuntimePassword bool, runtimePasswordEnv string) (*provisionResult, error) {
	out := cmd.OutOrStdout()
	ctx := commandContext(cmd)
	res := &provisionResult{}

	// 1. Reachability BEFORE touching anything (SPEC-05 §1).
	if err := postgres.CheckReachable(ctx, dsn); err != nil {
		return nil, err
	}
	printf(out, "[ ok  ] DB reachable at %s\n", postgres.MustRedactDSN(dsn))

	// 2. PG version floor (OD-7 — PostgreSQL 16+ for the v0.2.0
	//    privilege model; PD-8's floor 15 was the v0.1.0 contract).
	major, err := postgres.CheckPGVersion(ctx, dsn, cfg.Defaults.PostgresMajor)
	if err != nil {
		return nil, err
	}
	printf(out, "[ ok  ] PG version: %d (minimum %d, OD-7)\n", major, cfg.Defaults.PostgresMajor)

	// 3. Role bootstrap (deployment step 1 — BEFORE migrations: the
	//    0016 refusal guard verifies the roles and the D-47 membership).
	//    Refuse-first: unsafe pre-existing roles fail loudly.
	notes, err := postgres.BootstrapRoles(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("E_BOOTSTRAP: role bootstrap failed: %s\n"+
			"Why: the bootstrap script creates/validates gseer_policy_owner, gseer_app, and the D-47 membership; "+
			"it refuses unsafe states instead of repairing them.\n"+
			"Try: run with the database-owner credential (the compose default); remediate the reported role state; re-run: gseer setup --self-hosted", err)
	}
	for _, n := range notes {
		printf(out, "[boot] %s\n", n)
	}
	for _, n := range notes {
		if strings.Contains(n, "created role "+postgres.RoleApp) {
			res.roleAppCreated = true
		}
	}

	// 4. Migrations to head (deployment step 2 — 0001…0017; one atomic
	//    transaction per migration; bounded advisory-lock wait).
	applied, head, err := postgres.RunMigrations(dsn, cfg.Database.MigrationLockTimeoutMs)
	if err != nil {
		return nil, err
	}
	printf(out, "[ ok  ] migrations: %d applied, schema at head v%d\n", applied, head)

	// 5. Runtime grants (deployment step 3 — the §5.2 matrix; additive-
	//    only, idempotent, never revokes or widens).
	notes, err = postgres.ApplyRuntimeGrants(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("E_RUNTIME_GRANTS: runtime grant setup failed: %s\n"+
			"Why: the grant script is additive-only and never repairs an unexpected state (fail-closed).\n"+
			"Try: inspect the error with the operator credential; docs/DEPLOYMENT.md §3.5 lists the safe repairs", err)
	}
	for _, n := range notes {
		printf(out, "[grnt] %s\n", n)
	}

	// 6. Runtime credential: set gseer_app's password when the role was
	//    created here, on explicit rotation, or on deterministic
	//    provisioning (--runtime-password-env) and hand off the DSN.
	if err := provisionRuntimeCredential(ctx, dsn, res, resetRuntimePassword, runtimePasswordEnv, out); err != nil {
		return nil, err
	}

	// 7. Privilege audit (deployment step 4 — read-only, fail-closed).
	report, aerr := postgres.RunPrivilegeAudit(ctx, dsn)
	if aerr != nil {
		return nil, fmt.Errorf("E_AUDIT_UNAVAILABLE: the privilege audit could not run: %s\n"+
			"Why: an audit that cannot run is not an audit that passed (fail-closed)\n"+
			"Try: check the database connection and re-run: gseer setup --self-hosted", aerr)
	}
	for _, n := range report.Notices {
		printf(out, "[audt] %s\n", n)
	}
	if !report.Passed {
		printf(out, "E_PRIVILEGE_AUDIT_FAILED: the fail-closed privilege audit refused this deployment (%d failure(s))\n", len(report.Failures))
		for _, f := range report.Failures {
			printf(out, "  - %s\n", f)
		}
		printf(out, "Why: OD-5 — no runtime traffic before a passed audit; there is no bypass, flag, or override.\n"+
			"Try: fix the deployment, never the check: docs/DEPLOYMENT.md §3.5 lists the safe repairs. Do NOT grant the missing privileges to gseer_app.")
		exitWith(ExitAuth) // exit 5: the deployment is refused
		return nil, nil
	}
	printf(out, "[ ok  ] privilege audit: PASSED (fail-closed gate, OD-5)\n")

	return res, nil
}

// runSetupSelfHosted performs the hardened v0.2.0 provisioning via the
// shared core, then prints the next-steps tail (including the one-time
// runtime-credential handoff).  Never runs fixtures and never binds a
// listener (that is M4/M10/M11 work); fixtureFollows suppresses the
// tail when a fixture action follows in the same run.
func runSetupSelfHosted(cmd *cobra.Command, cfg *config.Config, dbURL string, fixtureFollows, resetRuntimePassword bool, runtimePasswordEnv string) error {
	out := cmd.OutOrStdout()

	res, err := provisionSelfHosted(cmd, cfg, setupFixturesDSN(cfg, dbURL), resetRuntimePassword, runtimePasswordEnv)
	if err != nil {
		return err
	}

	if fixtureFollows {
		_, _ = fmt.Fprintln(out) // separator before the fixture report
		return nil
	}

	printf(out, "\nSelf-hosted mode ready (hardened model, Rev 6.7.2).\n")
	switch {
	case res.runtimeDSN != "":
		printf(out, "\nRuntime credential (gseer_app) — shown ONCE, store it now:\n")
		printf(out, "  export GSEER_DB_RUNTIME_DSN='%s'\n", res.runtimeDSN)
		printf(out, "  (the server requires this credential; it never serves traffic as the operator — docs/DEPLOYMENT.md)\n")
	case res.roleAppCreated:
		printf(out, "\nRuntime credential: gseer_app was created but the runtime DSN could not be derived from a non-URL operator DSN; construct it as postgres://%s:<password>@<host>:<port>/<db>?sslmode=…\n", postgres.RoleApp)
	default:
		printf(out, "\nRuntime credential: gseer_app already existed — its password lives in your secret channel.\n")
		printf(out, "  Lost it? gseer setup --self-hosted --reset-runtime-password  (rotates and prints a new DSN)\n")
	}
	printf(out, "\nNext:\n")
	printf(out, "  gseer doctor                    — verify the environment\n")
	printf(out, "  gseer server start              — start the API + dashboard (requires GSEER_DB_RUNTIME_DSN)\n")
	if cfg.Policy.EvaluationFixtures {
		printf(out, "  gseer setup --load-fixtures     — load the synthetic dataset (QS-6)\n")
	}
	return nil
}

// generateRuntimePassword returns a 24-byte random URL-safe password
// (192 bits — no dictionary, no reuse; generated here, stored only in
// the database and the printed handoff).
func generateRuntimePassword() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// runtimeDSNFromOperator derives the gseer_app runtime DSN from the
// operator DSN by swapping ONLY the userinfo (user + password) — host,
// port, database, and sslmode/parameters are preserved exactly.  Only
// URL-form DSNs are supported for the derivation; anything else returns
// an error so the operator constructs the DSN deliberately (never a
// guessed half-DSN).
func runtimeDSNFromOperator(operatorDSN, user, password string) (string, error) {
	u, err := url.Parse(operatorDSN)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("operator DSN is not URL-shaped")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("operator DSN scheme %q is not postgres", u.Scheme)
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}

// runSetupLoadFixtures loads the embedded fixture corpus behind the
// QS-6 gate: consent FIRST (before any database contact), then the
// store ingest and the honesty banner + counts report.  The action is
// profile-aware: the self-hosted path runs the shared provisioning core
// (idempotent at head) and the PostgreSQL store; the personal path
// opens the configured SQLite file (created by `gseer setup --personal`)
// and ingests through the personal store — same corpus, same gate,
// same honesty banner.
func runSetupLoadFixtures(cmd *cobra.Command, cfg *config.Config, dbURL string) error {
	out := cmd.OutOrStdout()
	ctx := commandContext(cmd)

	if err := confirmFixtureAction(cmd, cfg, "load", "loaded",
		"load the labeled synthetic fixture dataset into the database",
		"7 observations / 6 services / 4 assets (SPEC-04 §9) tagged provenance=fixture",
		"Re-running is idempotent (delivery-identity dedup); the data is NOT real internet data."); err != nil {
		return err
	}

	if cfg.Profile == config.ProfilePersonal {
		return runPersonalLoadFixtures(cmd, cfg)
	}

	dsn := setupFixturesDSN(cfg, dbURL)
	if _, err := provisionSelfHosted(cmd, cfg, dsn, false, ""); err != nil {
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

// runPersonalLoadFixtures is the personal-mode body of
// runSetupLoadFixtures: it opens the configured database FILE (never a
// postgres:// DSN), migrates idempotently (setup is a provisioning
// command), and ingests the same corpus through the personal store.
// The closing hints name only personal-surface commands — personal mode
// has no API server (`gseer server start` requires the self-hosted
// profile).
func runPersonalLoadFixtures(cmd *cobra.Command, cfg *config.Config) error {
	out := cmd.OutOrStdout()
	ctx := commandContext(cmd)

	store, res, err := personalStore(ctx, cfg)
	if err != nil {
		return fmt.Errorf("E_SETUP_DB: cannot open the personal database: %w\n"+
			"Try: gseer setup --personal  (provisions the database file)", err)
	}
	defer func() { _ = res.DB.Close() }()

	stats, err := fixture.Load(ctx, store)
	if err != nil {
		return err
	}

	printf(out, "[ ok  ] personal database: %s\n", res.Path)
	printf(out, "[ ok  ] fixture source registered (data_sources id=%d, connector=fixture, enabled=false)\n", stats.SourceID)
	printf(out, "[ ok  ] fixtures loaded: %d assets inserted, %d updated; %d services inserted, %d updated; %d observations inserted, %d deduped\n",
		stats.AssetsInserted, stats.AssetsUpdated,
		stats.ServicesInserted, stats.ServicesUpdated,
		stats.ObservationsInserted, stats.ObservationsDeduped)
	printf(out, "\n%s\n", fixture.Banner)
	printf(out, "Query them with: gseer search 'port=443'   — every surface labels this data as synthetic.\nPurge with: gseer setup --purge-fixtures\n")
	return nil
}

// runSetupPurgeFixtures removes every fixture-tagged row behind the
// QS-6 gate.  The purge is scoped by construction (store-side
// purge binds to provenance='fixture' — and, on PostgreSQL, the fixture
// source id), but the gate still asks: deletion is deletion, and the
// operator should never be surprised by either direction of the fixture
// lifecycle.  Profile-aware like the load path.
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

	if cfg.Profile == config.ProfilePersonal {
		return runPersonalPurgeFixtures(cmd, cfg)
	}

	dsn := setupFixturesDSN(cfg, dbURL)
	if _, err := provisionSelfHosted(cmd, cfg, dsn, false, ""); err != nil {
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

// runPersonalPurgeFixtures is the personal-mode body of
// runSetupPurgeFixtures: the SQLite purge is scoped by provenance
// ('fixture') inside one transaction; rows from real sources are
// unreachable by the predicates.  The fixture registry row stays
// (configuration, not data), exactly like the PostgreSQL purge.
func runPersonalPurgeFixtures(cmd *cobra.Command, cfg *config.Config) error {
	out := cmd.OutOrStdout()
	ctx := commandContext(cmd)

	store, res, err := personalStore(ctx, cfg)
	if err != nil {
		return fmt.Errorf("E_SETUP_DB: cannot open the personal database: %w\n"+
			"Try: gseer setup --personal  (provisions the database file)", err)
	}
	defer func() { _ = res.DB.Close() }()

	stats, err := store.PurgeFixtures(ctx)
	if err != nil {
		return err
	}

	printf(out, "[ ok  ] fixtures purged: %d observations, %d services, %d assets removed (one transaction)\n",
		stats.ObservationsRemoved, stats.ServicesRemoved, stats.AssetsRemoved)
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
