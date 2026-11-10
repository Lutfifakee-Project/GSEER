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

// personal.go — the Personal Mode backend selector (checkpoint B).
//
// Every personal command runs the SAME GQL chain and the SAME outputs
// as the self-hosted path — only the storage engine differs (one
// embedded SQLite file instead of PostgreSQL).  The self-hosted code
// paths are NOT modified; each command branches on the profile and
// dispatches here.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/GSEER/internal/config"
	"github.com/Lutfifakee-Project/GSEER/internal/fixture"
	"github.com/Lutfifakee-Project/GSEER/internal/search"
	"github.com/Lutfifakee-Project/GSEER/internal/searchlite"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/sqlite"
	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

// loadEngineConfig loads the config accepting the two engine profiles
// (self-hosted PostgreSQL and personal SQLite).  The client profile has
// no local engine and is rejected here.
func loadEngineConfig() (*config.Config, error) {
	cfg, cerr := config.Load(config.Flags{
		ConfigPath: flags.configPath,
		Profile:    flags.profile,
		Endpoint:   flags.server,
	})
	if cerr != nil {
		return nil, cerr
	}
	switch cfg.Profile {
	case config.ProfileSelfHosted, config.ProfilePersonal:
		return cfg, nil
	default:
		return nil, fmt.Errorf("E_CLIENT_ONLY: the engine commands need a local profile (self-hosted or personal; current: %s).\n"+
			"Try: gseer setup --personal  (single-file personal mode)", cfg.Profile)
	}
}

// openPersonal opens the personal database from the config's DSN (the
// database FILE path).
func openPersonal(cfg *config.Config) (*sqlite.OpenResult, error) {
	return sqlite.Open(cfg.Database.DSN)
}

// personalSearchService opens the database and returns the search
// service (per-invocation; the CLI is one-shot).
func personalSearchService(cfg *config.Config) (*searchlite.Service, func(), error) {
	res, err := openPersonal(cfg)
	if err != nil {
		return nil, nil, err
	}
	return searchlite.New(res.DB), func() { _ = res.DB.Close() }, nil
}

// personalStore opens + migrates the personal database and returns the
// store.
func personalStore(ctx context.Context, cfg *config.Config) (*sqlite.Store, *sqlite.OpenResult, error) {
	res, err := openPersonal(cfg)
	if err != nil {
		return nil, nil, err
	}
	if _, err := sqlite.Migrate(ctx, res.DB); err != nil {
		_ = res.DB.Close()
		return nil, nil, err
	}
	return sqlite.NewStore(res.DB), res, nil
}

// ---------------------------------------------------------------------------
// setup --personal
// ---------------------------------------------------------------------------

// defaultPersonalDBPath is the personal database location:
// <platform config dir>/gseer.db (~/.gseer/gseer.db on Linux).
func defaultPersonalDBPath() string {
	return filepath.Join(configDir(), "gseer.db")
}

// runSetupPersonal provisions the personal database: creates the file
// with the SQ-04 permission policy, migrates it, optionally loads the
// labeled fixture corpus, and writes the config.
func runSetupPersonal(cmd *cobra.Command, dbPath string, loadFixtures bool) error {
	out := cmd.OutOrStdout()
	ctx := commandContext(cmd)

	if dbPath == "" {
		dbPath = defaultPersonalDBPath()
	}
	if abs, err := filepath.Abs(dbPath); err == nil {
		dbPath = abs
	}

	if strings.HasPrefix(dbPath, "postgres://") {
		return fmt.Errorf("E_SETUP_USAGE: --db-path takes a FILE path for personal mode; postgres:// URLs belong to --self-hosted")
	}

	res, err := sqlite.Open(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = res.DB.Close() }()

	st, err := sqlite.Migrate(ctx, res.DB)
	if err != nil {
		return err
	}
	printf(out, "[ ok  ] personal database: %s\n", res.Path)
	printf(out, "[ ok  ] migrations: schema at head v%d (%s)\n", st.Head, st.State)
	switch res.Mode {
	case sqlite.OpenCreated:
		printf(out, "[ ok  ] new database file created with 0600 permissions\n")
	default:
		if res.PermNote != "" {
			printf(out, "[note ] %s\n", res.PermNote)
		}
	}

	if loadFixtures {
		if err := confirmFixtureAction(cmd, configPersonalScratch(dbPath), "load", "loaded",
			"load the labeled synthetic fixture dataset into the database",
			"7 observations / 6 services / 4 assets (SPEC-04 §9) tagged provenance=fixture",
			"Re-running is idempotent (delivery-identity dedup); the data is NOT real internet data."); err != nil {
			return err
		}
		store := sqlite.NewStore(res.DB)
		stats, err := fixture.Load(ctx, store)
		if err != nil {
			return err
		}
		printf(out, "[ ok  ] fixture source registered (data_sources id=%d, connector=fixture, enabled=false)\n", stats.SourceID)
		printf(out, "[ ok  ] fixtures loaded: %d assets inserted, %d updated; %d services inserted, %d updated; %d observations inserted, %d deduped\n",
			stats.AssetsInserted, stats.AssetsUpdated,
			stats.ServicesInserted, stats.ServicesUpdated,
			stats.ObservationsInserted, stats.ObservationsDeduped)
		printf(out, "\n%s\n", fixture.Banner)
	}

	// Write the config so every following command resolves the profile.
	cfgPath := flags.configPath
	if cfgPath == "" {
		cfgPath = defaultConfigPath()
	}
	if err := writePersonalConfig(cfgPath, dbPath); err != nil {
		return err
	}
	printf(out, "[ ok  ] config written: %s (profile=personal)\n", cfgPath)

	printf(out, "\nNext:\n")
	printf(out, "  gseer search 'port=443'         — query the index (personal mode)\n")
	printf(out, "  gseer setup --load-fixtures     — load the synthetic evaluation corpus (optional)\n")
	printf(out, "  gseer suppress add ip <ip> --reason \"...\"\n")
	printf(out, "  gseer db backup --out <file>    — consistent backup (VACUUM INTO)\n")
	return nil
}

// configPersonalScratch builds a minimal config for the fixture gate
// (the QS-6 confirmation reads policy.evaluation_fixtures from it).
func configPersonalScratch(dbPath string) *config.Config {
	return &config.Config{
		Profile:  config.ProfilePersonal,
		Database: config.DatabaseConfig{DSN: dbPath},
	}
}

// defaultConfigPath mirrors the loader's platform default.
func defaultConfigPath() string {
	return filepath.Join(configDir(), "config.yaml")
}

// configDir resolves the platform config directory (mirrors the config
// package's default resolution; kept here so the CLI writes where the
// loader reads).
func configDir() string {
	if v := os.Getenv("GSEER_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".gseer"
	}
	return filepath.Join(home, ".gseer")
}

// writePersonalConfig writes the minimal personal config (hand-rolled
// YAML so no unused sections are serialized).
func writePersonalConfig(path string, dbPath string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cannot create the config directory: %w", err)
	}
	body := fmt.Sprintf(`# GSEER personal mode — one embedded SQLite database file.
# Provisioned by: gseer setup --personal
profile: personal

database:
  dsn: %s
`, yamlQuote(dbPath))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("cannot write the config: %w", err)
	}
	return nil
}

// yamlQuote quotes a path for a YAML scalar (single-quote style).
func yamlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ---------------------------------------------------------------------------
// search
// ---------------------------------------------------------------------------

// runPersonalSearch mirrors localSearch with the SQLite service — the
// same renderers, the same facets contract.
func runPersonalSearch(out io.Writer, cfg *config.Config, q search.Query, fr facetsRequest) error {
	budget := 45 * time.Second
	if q.Timeout > 0 {
		budget = q.Timeout + 30*time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	svc, closeDB, err := personalSearchService(cfg)
	if err != nil {
		printf(out, "E_DB_UNAVAILABLE: cannot open the personal database\nWhy: %v\nTry: gseer setup --personal\n", err)
		exitWith(ExitUnavailable)
	}
	defer closeDB()

	res, err := svc.Search(ctx, q)
	if err != nil {
		renderSearchError(out, err)
		exitWith(searchExitCode(err))
	}
	var fres *search.FacetsResult
	if fr.Enabled {
		fres, err = svc.Facets(ctx, search.FacetsQuery{
			GQL: q.GQL, Mode: q.Mode, Timeout: q.Timeout,
			MaxValues: fr.MaxValues, Facets: fr.Dims,
		})
		if err != nil {
			renderSearchError(out, err)
			exitWith(searchExitCode(err))
		}
	}
	renderSearchWithFacets(out, res, fres, jsonOutput())
	return nil
}

// ---------------------------------------------------------------------------
// suppress
// ---------------------------------------------------------------------------

// runPersonalSuppressAdd mirrors the self-hosted add path (rule +
// retroactive retraction) against the personal store.
func runPersonalSuppressAdd(cmd *cobra.Command, kind suppress.Kind, value, reason, by, expires string, noRetract, jsonOut bool) error {
	cfg, err := loadEngineConfig()
	if err != nil {
		return err
	}
	if cfg.Profile != config.ProfilePersonal {
		return fmt.Errorf("E_PROFILE_MISMATCH: the personal suppress path requires profile=personal")
	}
	in := sqlite.SuppressInput{Kind: kind, Value: value, Reason: reason, CreatedBy: by}
	if expires != "" {
		exp, err := parseSuppressExpiry(expires, time.Now())
		if err != nil {
			return err
		}
		in.ExpiresAt = &exp
	}
	ctx := commandContext(cmd)
	store, res, err := personalStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = res.DB.Close() }()

	rule, existed, err := store.AddSuppression(ctx, in)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonOut {
		if existed {
			return fmt.Errorf("E_SUPPRESS_EXISTS: rule #%d (%s:%s) already exists", rule.ID, rule.Kind, rule.Value)
		}
		_, _ = fmt.Fprintf(out, "{\"id\":%d,\"kind\":%q,\"value\":%q,\"reason\":%q,\"expires_at\":%q}\n",
			rule.ID, rule.Kind, rule.Value, rule.Reason, expires)
		return nil
	}
	if existed {
		printf(out, "Rule already exists: #%d (%s: %s) — nothing changed.\n", rule.ID, rule.Kind, rule.Value)
		return nil
	}
	printf(out, "[ ok  ] suppression rule #%d added: %s = %s\n", rule.ID, rule.Kind, rule.Value)
	if in.ExpiresAt != nil {
		printf(out, "        expires: %s\n", in.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if noRetract {
		println(out, "        retraction skipped (--no-retract); matching records stay until the next retraction")
		return nil
	}
	stats, err := store.RetractSuppressed(ctx, rule)
	if err != nil {
		return fmt.Errorf("E_SUPPRESS_RETRACT: the rule is stored but the retroactive retraction failed.\n"+
			"Why: %v\n"+
			"Try: the rule is already enforcing on ingestion; retry the retraction by re-adding the rule (idempotent)", err)
	}
	printf(out, "[ ok  ] retroactive retraction: %d assets removed, %d services, %d observations (audit rows: %d)\n",
		stats.AssetsRetracted, stats.ServicesRemoved, stats.ObservationsRemoved, stats.AuditRows)
	return nil
}

// runPersonalSuppressList mirrors the self-hosted list path.
func runPersonalSuppressList(cmd *cobra.Command) error {
	cfg, err := loadEngineConfig()
	if err != nil {
		return err
	}
	store, res, err := personalStore(commandContext(cmd), cfg)
	if err != nil {
		return err
	}
	defer func() { _ = res.DB.Close() }()
	rules, err := store.ListSuppressions(cmd.Context())
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(rules) == 0 {
		println(out, "No suppression rules. Add one with: gseer suppress add <kind> <value> --reason \"...\"")
		return nil
	}
	printf(out, "%-6s %-14s %-40s %-10s %-22s %s\n", "ID", "KIND", "VALUE", "ENABLED", "EXPIRES", "REASON")
	for _, r := range rules {
		exp := "never"
		if r.ExpiresAt != nil {
			exp = r.ExpiresAt.UTC().Format(time.RFC3339)
		}
		printf(out, "%-6d %-14s %-40s %-10v %-22s %s\n",
			r.ID, r.Kind, r.Value, r.Enabled, exp, r.Reason)
	}
	return nil
}

// runPersonalSuppressRemove mirrors the self-hosted remove path.
func runPersonalSuppressRemove(cmd *cobra.Command, id int64) error {
	cfg, err := loadEngineConfig()
	if err != nil {
		return err
	}
	store, res, err := personalStore(commandContext(cmd), cfg)
	if err != nil {
		return err
	}
	defer func() { _ = res.DB.Close() }()
	removed, err := store.RemoveSuppression(cmd.Context(), id)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if !removed {
		return fmt.Errorf("E_SUPPRESS_NOT_FOUND: rule #%d does not exist — gseer suppress list", id)
	}
	printf(out, "[ ok  ] suppression rule #%d removed.  The audit history is kept; re-ingestion is allowed again.\n", id)
	return nil
}

// ---------------------------------------------------------------------------
// db backup / restore (personal)
// ---------------------------------------------------------------------------

// runPersonalDBBackup performs a consistent, online backup via SQLite's
// VACUUM INTO (one transaction snapshot; the live database is not
// locked out) and restores the 0600 permission policy on the copy.
func runPersonalDBBackup(cmd *cobra.Command, outPath string) error {
	cfg, err := loadEngineConfig()
	if err != nil {
		return err
	}
	if outPath == "" {
		stamp := time.Now().UTC().Format("20060102T150405Z")
		outPath = cfg.Database.DSN + ".backup-" + stamp
	}
	if abs, aerr := filepath.Abs(outPath); aerr == nil {
		outPath = abs
	}
	if outPath == cfg.Database.DSN {
		return fmt.Errorf("E_DB_BACKUP: the backup target must differ from the live database file")
	}

	store, res, err := personalStore(commandContext(cmd), cfg)
	if err != nil {
		return err
	}
	defer func() { _ = res.DB.Close() }()
	// VACUUM INTO produces a consistent snapshot; wal files are not needed.
	if _, err := store.DB.Exec(`VACUUM INTO ?`, sqlite.BuildDSN(outPath)); err != nil {
		// VACUUM INTO takes the raw (non-file:) path in most builds; retry
		// with the plain path when the DSN form is rejected.
		if _, err2 := store.DB.Exec(`VACUUM INTO ?`, outPath); err2 != nil {
			return fmt.Errorf("E_DB_BACKUP: cannot write the backup snapshot: %v / %v", err, err2)
		}
	}
	// Best-effort hardening: VACUUM INTO copies the source file's mode;
	// re-assert 0600 on the snapshot.  chmod is a best-effort no-op on
	// platforms without POSIX mode bits (see open.go isNoChmodOS).
	_ = os.Chmod(outPath, 0o600)
	st, serr := os.Stat(outPath)
	if serr != nil {
		return fmt.Errorf("E_DB_BACKUP: snapshot missing after VACUUM INTO: %w", serr)
	}
	printf(cmd.OutOrStdout(), "[ ok  ] backup written: %s (%d bytes, 0600)\n", outPath, st.Size())
	printf(cmd.OutOrStdout(), "Restore with: gseer db restore %s  (replaces the current database file)\n", outPath)
	return nil
}

// runPersonalDBRestore atomically replaces the live database file with
// the backup (the old file moves aside first — a failed restore is
// recoverable).
func runPersonalDBRestore(cmd *cobra.Command, backupPath string, yes bool) error {
	cfg, err := loadEngineConfig()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(backupPath)
	if err != nil {
		return err
	}
	if _, serr := os.Stat(abs); serr != nil {
		return fmt.Errorf("E_DB_RESTORE: backup file not found: %w", serr)
	}
	live := cfg.Database.DSN
	if !yes {
		out := cmd.OutOrStdout()
		printf(out, "This REPLACES the personal database %s with %s.\nProceed? [y/N] ", live, abs)
		var answer string
		if _, err := fmt.Fscanln(cmd.InOrStdin(), &answer); err != nil || (answer != "y" && answer != "yes") {
			println(out, "Restore cancelled.")
			return nil
		}
	}
	// Sanity: the backup must be a readable SQLite database.
	probe, err := sqlite.Open(abs)
	if err != nil {
		return fmt.Errorf("E_DB_RESTORE: the backup file is not a valid GSEER database: %w", err)
	}
	_ = probe.DB.Close()

	old := live + ".restore-old"
	_ = os.Remove(old)
	if err := os.Rename(live, old); err != nil {
		return fmt.Errorf("E_DB_RESTORE: cannot move the live database aside: %w", err)
	}
	if err := copyFile(abs, live, 0o600); err != nil {
		_ = os.Rename(old, live) // recover
		return fmt.Errorf("E_DB_RESTORE: copy failed (old database restored): %w", err)
	}
	printf(cmd.OutOrStdout(), "[ ok  ] restored %s from %s\n", live, abs)
	printf(cmd.OutOrStdout(), "The previous database is kept at %s (remove it when satisfied).\n", old)
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	// src/dst are operator-supplied paths — that IS the db
	// backup/restore contract (the operator names the file), not an
	// inclusion vulnerability.
	in, err := os.Open(src) //nolint:gosec // G304: operator-supplied backup/restore path is the feature
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) //nolint:gosec // G304: operator-supplied backup/restore path is the feature
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
