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

// personal_fixtures_test.go — the personal-mode surfaces that v0.2.1
// shipped half-wired, pinned by tests so they cannot regress:
//
//   - `gseer setup --load-fixtures` / `--purge-fixtures` standalone
//     (saved personal config) route to the SQLite store instead of
//     trying to parse the database FILE as a postgres:// DSN;
//   - `gseer doctor` runs real personal checks (file, migrations,
//     index) instead of the client-profile skip line;
//   - `source`/`collect` refuse personal mode with an honest
//     E_PROFILE_UNSUPPORTED (the collection registry is PostgreSQL-only);
//   - `gseer export` streams through the personal search service.
//
// These tests are plain unit tests: no PostgreSQL, no network — the
// personal store is one embedded SQLite file in a temp directory.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/gseer/internal/config"
	"github.com/Lutfifakee-Project/gseer/internal/fixture"
	"github.com/Lutfifakee-Project/gseer/internal/search"
	"github.com/Lutfifakee-Project/gseer/internal/searchlite"
	"github.com/Lutfifakee-Project/gseer/internal/storage/sqlite"
)

// writePersonalConfigForTest writes the minimal personal config the
// loader expects (the same shape writePersonalConfig produces).
func writePersonalConfigForTest(t *testing.T, dbPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "profile: personal\ndatabase:\n  dsn: '" + dbPath + "'\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write personal config: %v", err)
	}
	return path
}

// newMigratedPersonalDB provisions an existing personal database (the
// state `gseer setup --personal` leaves behind) and returns its path.
func newMigratedPersonalDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "gseer.db")
	res, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open personal db: %v", err)
	}
	if _, err := sqlite.Migrate(context.Background(), res.DB); err != nil {
		_ = res.DB.Close()
		t.Fatalf("migrate personal db: %v", err)
	}
	if err := res.DB.Close(); err != nil {
		t.Fatalf("close personal db: %v", err)
	}
	return dbPath
}

// findSetupCmd returns the registered setup command.
func findSetupCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd, _, err := RootCmd.Find([]string{"setup"})
	if err != nil {
		t.Fatalf("setup not registered: %v", err)
	}
	return cmd
}

// personalSearchRows runs one GQL query against the personal store and
// returns the row count (the same surface the operator uses).
func personalSearchRows(t *testing.T, dbPath, gql string) int {
	t.Helper()
	res, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen personal db: %v", err)
	}
	defer func() { _ = res.DB.Close() }()
	out, err := searchlite.New(res.DB).Search(context.Background(), search.Query{GQL: gql})
	if err != nil {
		t.Fatalf("personal search %q: %v", gql, err)
	}
	return len(out.Rows)
}

// TestSetupLoadFixturesStandalonePersonal proves the standalone
// `gseer setup --load-fixtures` (no --personal flag in the invocation)
// follows the SAVED personal profile into the SQLite store, through the
// QS-6 gate, and that the data is searchable — the exact flow a
// personal-mode operator runs.
func TestSetupLoadFixturesStandalonePersonal(t *testing.T) {
	dbPath := newMigratedPersonalDB(t)
	withConfigPath(t, writePersonalConfigForTest(t, dbPath))

	setup := findSetupCmd(t)
	if err := setup.Flags().Set("load-fixtures", "true"); err != nil {
		t.Fatalf("set --load-fixtures: %v", err)
	}
	if err := setup.Flags().Set("purge-fixtures", "false"); err != nil {
		t.Fatalf("reset --purge-fixtures: %v", err)
	}
	out := &bytes.Buffer{}
	setup.SetOut(out)
	setup.SetIn(strings.NewReader("y\n")) // QS-6 interactive confirmation

	if err := setup.RunE(setup, nil); err != nil {
		t.Fatalf("standalone personal load-fixtures: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"personal database",
		"fixture source registered",
		"fixtures loaded",
		"Purge with: gseer setup --purge-fixtures",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q\noutput:\n%s", want, text)
		}
	}
	// Personal mode has no API server — the hint must not send the
	// operator to `gseer server start` (it refuses personal profiles).
	if strings.Contains(text, "server start") {
		t.Errorf("personal fixture output must not suggest `server start`\noutput:\n%s", text)
	}
	if got := personalSearchRows(t, dbPath, "port==443"); got == 0 {
		t.Errorf("port==443 returned 0 rows after fixture load — data did not land")
	}

	// Idempotency: a second load dedupes instead of duplicating.
	out.Reset()
	setup.SetIn(strings.NewReader("y\n"))
	if err := setup.RunE(setup, nil); err != nil {
		t.Fatalf("second load (idempotency): %v", err)
	}
	if !strings.Contains(out.String(), "deduped") {
		t.Errorf("second load should report dedupe counts\noutput:\n%s", out.String())
	}
}

// TestSetupPurgeFixturesStandalonePersonal proves the standalone purge
// removes every fixture-tagged row from the personal store and leaves a
// genuinely empty index behind.
func TestSetupPurgeFixturesStandalonePersonal(t *testing.T) {
	dbPath := newMigratedPersonalDB(t)
	withConfigPath(t, writePersonalConfigForTest(t, dbPath))

	// Seed fixtures directly (the load path is covered above).
	res, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := fixture.Load(context.Background(), sqlite.NewStore(res.DB)); err != nil {
		_ = res.DB.Close()
		t.Fatalf("seed fixtures: %v", err)
	}
	if err := res.DB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := personalSearchRows(t, dbPath, "port==443"); got == 0 {
		t.Fatalf("seed failed — port==443 empty before purge")
	}

	setup := findSetupCmd(t)
	if err := setup.Flags().Set("purge-fixtures", "true"); err != nil {
		t.Fatalf("set --purge-fixtures: %v", err)
	}
	if err := setup.Flags().Set("load-fixtures", "false"); err != nil {
		t.Fatalf("reset --load-fixtures: %v", err)
	}
	out := &bytes.Buffer{}
	setup.SetOut(out)
	setup.SetIn(strings.NewReader("y\n"))

	if err := setup.RunE(setup, nil); err != nil {
		t.Fatalf("standalone personal purge-fixtures: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "fixtures purged") {
		t.Errorf("output missing the purge report\noutput:\n%s", text)
	}
	if got := personalSearchRows(t, dbPath, "port==443"); got != 0 {
		t.Errorf("port==443 returned %d rows after purge — fixture data survived", got)
	}
}

// doctorCollector captures report() calls for assertions.
type doctorCollector struct {
	outcomes map[string]string
	details  map[string]string
}

func newDoctorCollector() *doctorCollector {
	return &doctorCollector{outcomes: map[string]string{}, details: map[string]string{}}
}

func (d *doctorCollector) report(name, outcome, detail, _ string, _ error) {
	d.outcomes[name] = outcome
	d.details[name] = detail
}

// TestDoctorPersonalChecks pins the personal doctor branch: file +
// permissions PASS, migration state PASS, and the same index honesty
// contract as the self-hosted path (EMPTY hint → fixture hint).
func TestDoctorPersonalChecks(t *testing.T) {
	t.Run("empty database at head", func(t *testing.T) {
		dbPath := newMigratedPersonalDB(t)
		cfg := &config.Config{
			Profile:  config.ProfilePersonal,
			Database: config.DatabaseConfig{DSN: dbPath},
		}
		col := newDoctorCollector()
		reportPersonalDBChecks(&cobra.Command{}, cfg, col.report)

		if col.outcomes["personal database"] != "PASS" {
			t.Errorf("personal database: want PASS, got %q (%s)", col.outcomes["personal database"], col.details["personal database"])
		}
		if col.outcomes["migrations"] != "PASS" {
			t.Errorf("migrations: want PASS, got %q (%s)", col.outcomes["migrations"], col.details["migrations"])
		}
		if col.outcomes["index"] != "HINT" || !strings.Contains(col.details["index"], "EMPTY") {
			t.Errorf("index: want EMPTY HINT, got %q (%s)", col.outcomes["index"], col.details["index"])
		}
	})

	t.Run("fixture data is labeled", func(t *testing.T) {
		dbPath := newMigratedPersonalDB(t)
		res, err := sqlite.Open(dbPath)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer func() { _ = res.DB.Close() }()
		if _, err := fixture.Load(context.Background(), sqlite.NewStore(res.DB)); err != nil {
			t.Fatalf("load fixtures: %v", err)
		}
		cfg := &config.Config{
			Profile:  config.ProfilePersonal,
			Database: config.DatabaseConfig{DSN: dbPath},
		}
		col := newDoctorCollector()
		reportPersonalDBChecks(&cobra.Command{}, cfg, col.report)

		if !strings.Contains(col.details["index"], "FIXTURE") {
			t.Errorf("index detail must carry the fixture label, got %q", col.details["index"])
		}
	})

	t.Run("missing file fails without creating it", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist.db")
		cfg := &config.Config{
			Profile:  config.ProfilePersonal,
			Database: config.DatabaseConfig{DSN: missing},
		}
		col := newDoctorCollector()
		reportPersonalDBChecks(&cobra.Command{}, cfg, col.report)

		if col.outcomes["personal database"] != "FAIL" {
			t.Errorf("missing file: want FAIL, got %q", col.outcomes["personal database"])
		}
		if _, serr := os.Stat(missing); !os.IsNotExist(serr) {
			t.Errorf("doctor must be read-only: the missing database file was created (%v)", serr)
		}
	})
}

// TestOpenSourceStoreProfileGates pins the profile gates: since v0.3.0
// personal mode SERVES the registry (the embedded SQLite file is the
// collection registry), so `source list` succeeds there — while the
// client profile is still refused as client-only.
func TestOpenSourceStoreProfileGates(t *testing.T) {
	oldProfile := flags.profile
	t.Cleanup(func() { flags.profile = oldProfile })
	flags.profile = ""

	t.Run("personal mode serves the registry", func(t *testing.T) {
		withConfigPath(t, writePersonalConfigForTest(t, newMigratedPersonalDB(t)))
		src, _, err := RootCmd.Find([]string{"source", "list"})
		if err != nil {
			t.Fatalf("source list not registered: %v", err)
		}
		var buf bytes.Buffer
		RootCmd.SetOut(&buf)
		t.Cleanup(func() { RootCmd.SetOut(nil) })
		if err := src.RunE(src, nil); err != nil {
			t.Fatalf("personal source list errored: %v", err)
		}
		if !strings.Contains(buf.String(), "No data sources registered") {
			t.Errorf("personal source list output missing the empty-registry hint:\n%s", buf.String())
		}
	})

	t.Run("client profile refuses as client-only", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "config.yaml")
		content := "profile: client\nclient:\n  endpoint: https://gseer.example.invalid\n"
		if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
			t.Fatalf("write client config: %v", err)
		}
		withConfigPath(t, cfgPath)
		src, _, err := RootCmd.Find([]string{"source", "list"})
		if err != nil {
			t.Fatalf("source list not registered: %v", err)
		}
		err = src.RunE(src, nil)
		if err == nil || !strings.Contains(err.Error(), "E_CLIENT_ONLY") {
			t.Errorf("want E_CLIENT_ONLY, got: %v", err)
		}
	})
}

// TestExportPersonalStdout pins the personal export path: the same
// streamed NDJSON contract through the embedded SQLite service.
func TestExportPersonalStdout(t *testing.T) {
	dbPath := newMigratedPersonalDB(t)
	res, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := fixture.Load(context.Background(), sqlite.NewStore(res.DB)); err != nil {
		_ = res.DB.Close()
		t.Fatalf("load fixtures: %v", err)
	}
	if err := res.DB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	withConfigPath(t, writePersonalConfigForTest(t, dbPath))

	exp, _, err := RootCmd.Find([]string{"export"})
	if err != nil {
		t.Fatalf("export not registered: %v", err)
	}
	for flag, val := range map[string]string{"format": "ndjson", "out": "-"} {
		if serr := exp.Flags().Set(flag, val); serr != nil {
			t.Fatalf("set --%s: %v", flag, serr)
		}
	}
	var stdout, stderr bytes.Buffer
	exp.SetOut(&stdout)
	exp.SetErr(&stderr)

	if err := exp.RunE(exp, []string{"port==443"}); err != nil {
		t.Fatalf("personal export: %v", err)
	}
	text := stderr.String()
	if !strings.Contains(text, "exported:") {
		t.Errorf("stats line missing\nstderr:\n%s", text)
	}
	// The data stream goes to the ORIGINAL stdout (dest is captured
	// before the stdout-export branch reroutes cmd's output to stderr);
	// the stats line goes to stderr — a piped stream stays parseable.
	stream := stdout.String()
	if !strings.Contains(stream, `"record":"footer"`) {
		t.Errorf("ndjson footer missing\nstdout:\n%s", stream)
	}
	if !strings.Contains(stream, `"row_id"`) {
		t.Errorf("no data rows in the stream\nstdout:\n%s", stream)
	}
}
