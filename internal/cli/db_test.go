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
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// db_test.go — destructive-operation and command-tree tests for
// `gseer db migrate` (owner directive §2 of the M3 authorization
// round; AC-M3-12, AC-M3-14, AC-M3-15).

// TestDBMigrateCommandTreeRegistered — AC-M3-12: the three admin
// commands are registered; `setup --force-migrate-version` does NOT
// exist.
func TestDBMigrateCommandTreeRegistered(t *testing.T) {
	for _, path := range []string{"db", "db migrate", "db migrate status", "db migrate diagnose", "db migrate force"} {
		if _, _, err := RootCmd.Find(strings.Split(path, " ")); err != nil {
			t.Errorf("command %q not registered: %v", path, err)
		}
	}
	cmd, _, err := RootCmd.Find([]string{"setup"})
	if err != nil {
		t.Fatalf("setup not registered: %v", err)
	}
	if cmd.Flags().Lookup("force-migrate-version") != nil {
		t.Error("setup must NOT expose --force-migrate-version (owner directive §2)")
	}
	force, _, err := RootCmd.Find([]string{"db", "migrate", "force"})
	if err != nil {
		t.Fatalf("force not registered: %v", err)
	}
	if force.Flags().Lookup("version") == nil {
		t.Error("force must require --version")
	}
	if force.Flags().Lookup("yes") == nil {
		t.Error("force must offer --yes for scripted recovery")
	}
}

// TestForceHelpExplainsMetadataOnly — the force help must state the
// metadata-only contract (owner directive §2.5 item 1).
func TestForceHelpExplainsMetadataOnly(t *testing.T) {
	force, _, err := RootCmd.Find([]string{"db", "migrate", "force"})
	if err != nil {
		t.Fatalf("force not registered: %v", err)
	}
	help := force.Long
	for _, want := range []string{
		"ONLY changes the schema_migrations metadata",
		"does NOT execute, roll back, or repair any SQL migration",
		"never runs automatically",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("force help missing %q", want)
		}
	}
}

// TestForceBannerRenders — the destructive banner prints BEFORE any
// database interaction (audit trail; owner directive §2.6).
func TestForceBannerRenders(t *testing.T) {
	cfgFile := writeSelfHostedConfig(t, unreachablePortDSN())
	t.Cleanup(func() { _ = os.Remove(cfgFile) })
	oldCfg := flags.configPath
	flags.configPath = cfgFile
	t.Cleanup(func() { flags.configPath = oldCfg })

	out := &bytes.Buffer{}
	force, _, err := RootCmd.Find([]string{"db", "migrate", "force"})
	if err != nil {
		t.Fatalf("find force: %v", err)
	}
	force.SetOut(out)
	force.SetIn(strings.NewReader(confirmationPhrase + "\n"))
	// RunE is invoked directly (not via Execute), so set the required
	// flag manually — cobra's flag parsing never runs.
	if err := force.Flags().Set("version", "1"); err != nil {
		t.Fatalf("set --version: %v", err)
	}

	// The correct phrase passes the gate; the command then fails at the
	// DB connection (unreachable DSN) — proving the DB is only touched
	// AFTER the confirmation gate.
	err = force.RunE(force, nil)
	if err == nil {
		t.Fatal("expected E_DB_UNREACHABLE (unreachable DSN), got nil")
	}
	if !strings.Contains(err.Error(), "E_DB_UNREACHABLE") {
		t.Errorf("want E_DB_UNREACHABLE, got: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"DESTRUCTIVE METADATA OPERATION",
		"DELETE all rows from schema_migrations",
		"NOT execute, roll back, or repair",
		confirmationPhrase,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("banner missing %q\noutput:\n%s", want, text)
		}
	}
}

// TestReadForceConfirmation — AC-M3-14 gate logic: exact phrase
// confirms, any other input refuses, empty stdin errors.
func TestReadForceConfirmation(t *testing.T) {
	t.Run("exact phrase confirms", func(t *testing.T) {
		ok, err := readForceConfirmation(strings.NewReader(confirmationPhrase + "\n"))
		if err != nil || !ok {
			t.Errorf("got (%v, %v), want (true, nil)", ok, err)
		}
	})
	t.Run("wrong phrase refuses", func(t *testing.T) {
		ok, err := readForceConfirmation(strings.NewReader("yes\n"))
		if err != nil || ok {
			t.Errorf("got (%v, %v), want (false, nil)", ok, err)
		}
	})
	t.Run("phrase with trailing spaces refuses", func(t *testing.T) {
		ok, err := readForceConfirmation(strings.NewReader(confirmationPhrase + "  \n"))
		if err != nil || ok {
			t.Errorf("trailing spaces must not confirm; got (%v, %v)", ok, err)
		}
	})
	t.Run("case differs refuses", func(t *testing.T) {
		ok, err := readForceConfirmation(strings.NewReader(strings.ToUpper(confirmationPhrase) + "\n"))
		if err != nil || ok {
			t.Errorf("case-insensitive match must not confirm; got (%v, %v)", ok, err)
		}
	})
	t.Run("no input errors", func(t *testing.T) {
		ok, err := readForceConfirmation(strings.NewReader(""))
		if err == nil || ok {
			t.Errorf("empty stdin must error; got (%v, %v)", ok, err)
		}
		if !strings.Contains(err.Error(), "E_DB_USAGE") {
			t.Errorf("error should carry E_DB_USAGE: %v", err)
		}
	})
}

// TestForceNotCallableFromOtherCommands — AC-M3-15 (static half): no
// file outside the force command and the storage admin primitive
// references ForceMigrationVersion or the raw migrate Force call.
func TestForceNotCallableFromOtherCommands(t *testing.T) {
	allowed := map[string]bool{
		"cli/db.go":                            true,
		"storage/postgres/admin.go":            true,
		"cli/db_test.go":                       true,
		"storage/postgres/integration_test.go": true,
		"storage/postgres/redact_test.go":      true,
	}
	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		rel, rerr := filepath.Rel("..", path)
		if rerr != nil {
			return rerr
		}
		// Normalize separators so the allowlist (slash-separated) matches
		// on every OS (Windows yields backslash paths from filepath.Rel).
		rel = filepath.ToSlash(rel)
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		text := string(data)
		if strings.Contains(text, "ForceMigrationVersion") && !allowed[rel] {
			t.Errorf("%s references ForceMigrationVersion — force must only be reachable via gseer db migrate force", rel)
		}
		if strings.Contains(text, "m.Force(") && rel != "storage/postgres/admin.go" && rel != "cli/db_test.go" {
			t.Errorf("%s calls migrate Force directly — route through postgres.ForceMigrationVersion", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// writeSelfHostedConfig writes a minimal self-hosted YAML config with
// the given DSN to a temp file and returns its path.
func writeSelfHostedConfig(t *testing.T, dsn string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "profile: self-hosted\ndatabase:\n  dsn: " + dsn + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// unreachablePortDSN returns a DSN bound to a reserved loopback port
// that no database listens on (fast, deterministic connect refusal).
func unreachablePortDSN() string {
	return "postgres://gseer@127.0.0.1:1/gseer_m3_test?sslmode=disable&connect_timeout=1"
}
