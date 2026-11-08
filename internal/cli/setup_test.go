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

	"github.com/spf13/cobra"
	"strings"
	"testing"
)

// setup_test.go — unit tests for the M4 fixture wiring on `gseer setup`
// (QS-6): flag mutual exclusion, the confirmation-gate reader branches,
// and the gate's ordering contract (consent BEFORE any database
// contact).  The full load/purge round trip against a live PostgreSQL
// lives in setup_fixtures_integration_test.go (build tag `integration`).

// writeSelfHostedConfigWithPolicy writes a self-hosted config file with
// an explicit policy.evaluation_fixtures value and returns its path.
func writeSelfHostedConfigWithPolicy(t *testing.T, dsn string, evalFixtures bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	policy := "false"
	if evalFixtures {
		policy = "true"
	}
	content := "profile: self-hosted\ndatabase:\n  dsn: " + dsn +
		"\npolicy:\n  evaluation_fixtures: " + policy + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// withConfigPath swaps flags.configPath for the duration of the test.
func withConfigPath(t *testing.T, path string) {
	t.Helper()
	old := flags.configPath
	flags.configPath = path
	t.Cleanup(func() { flags.configPath = old })
}

// findSetup returns the registered setup command.
func findSetup(t *testing.T) *cobra.Command {
	t.Helper()
	cmd, _, err := RootCmd.Find([]string{"setup"})
	if err != nil {
		t.Fatalf("setup not registered: %v", err)
	}
	return cmd
}

// resetSetupFlags clears the setup flags that tests set: the command is
// a global singleton, so cobra flag values leak across tests in one
// process.  Reset runs before AND after the test (after matters: a
// later test's reset alone cannot undo damage done by a test that
// returns early on a failed assertion).
func resetSetupFlags(t *testing.T) {
	t.Helper()
	clear := func() {
		cmd := findSetup(t)
		for _, f := range []string{"self-hosted", "with-compose", "full", "load-fixtures", "purge-fixtures"} {
			if err := cmd.Flags().Set(f, "false"); err != nil {
				t.Fatalf("reset --%s: %v", f, err)
			}
		}
		if err := cmd.Flags().Set("db-url", ""); err != nil {
			t.Fatalf("reset --db-url: %v", err)
		}
	}
	clear()
	t.Cleanup(clear)
}

// TestSetupFixtureFlagsMutuallyExclusive — loading and purging in one
// invocation is a usage error refused before any database contact.
func TestSetupFixtureFlagsMutuallyExclusive(t *testing.T) {
	resetSetupFlags(t)
	cfgFile := writeSelfHostedConfigWithPolicy(t, unreachablePortDSN(), true)
	withConfigPath(t, cfgFile)

	out := &bytes.Buffer{}
	cmd := findSetup(t)
	cmd.SetOut(out)
	if err := cmd.Flags().Set("load-fixtures", "true"); err != nil {
		t.Fatalf("set --load-fixtures: %v", err)
	}
	if err := cmd.Flags().Set("purge-fixtures", "true"); err != nil {
		t.Fatalf("set --purge-fixtures: %v", err)
	}

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected E_SETUP_USAGE, got nil")
	}
	if !strings.Contains(err.Error(), "E_SETUP_USAGE") {
		t.Errorf("want E_SETUP_USAGE, got: %v", err)
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("usage error should explain the conflict, got: %v", err)
	}
}

// TestSetupHelpExplainsFixtures — AC-8.x truthfulness: the setup help
// documents both fixture actions and the QS-6 gate semantics.
func TestSetupHelpExplainsFixtures(t *testing.T) {
	cmd := findSetup(t)
	for _, want := range []string{
		"--load-fixtures",
		"--purge-fixtures",
		"policy.evaluation_fixtures",
		"y/yes",
		"NOT real internet data",
	} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("setup help missing %q", want)
		}
	}
}

// TestReadFixtureConfirmationBranches — the QS-6 gate reader accepts
// y/yes case-insensitively, refuses everything else, and errors only on
// a truly empty stdin (mirrors readForceConfirmation's contract).
func TestReadFixtureConfirmationBranches(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantOK  bool
		wantErr bool
	}{
		{"y", "y\n", true, false},
		{"Y uppercase", "Y\n", true, false},
		{"yes", "yes\n", true, false},
		{"Yes with spaces", "  yes\r\n", true, false},
		{"n", "n\n", false, false},
		{"N", "N\n", false, false},
		{"bare newline", "\n", false, false},
		{"garbage", "delete everything\n", false, false},
		{"EOF no input", "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := readFixtureConfirmation(strings.NewReader(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want input error, got (ok=%v, nil)", ok)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.wantOK {
				t.Errorf("confirmed = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}

// TestSetupFixtureGateNoInputBeforeDBContact — the QS-6 ordering
// contract: with policy.evaluation_fixtures=false and a non-interactive
// stdin, --load-fixtures is refused with E_FIXTURE_CONFIRM and the
// database is NEVER contacted (the DSN points at a port nothing listens
// on, so any contact attempt would surface as E_DB_UNREACHABLE instead).
func TestSetupFixtureGateNoInputBeforeDBContact(t *testing.T) {
	resetSetupFlags(t)
	cfgFile := writeSelfHostedConfigWithPolicy(t, unreachablePortDSN(), false)
	withConfigPath(t, cfgFile)

	out := &bytes.Buffer{}
	cmd := findSetup(t)
	cmd.SetOut(out)
	cmd.SetIn(strings.NewReader("")) // non-interactive: EOF immediately
	if err := cmd.Flags().Set("load-fixtures", "true"); err != nil {
		t.Fatalf("set --load-fixtures: %v", err)
	}

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected E_FIXTURE_CONFIRM, got nil")
	}
	if !strings.Contains(err.Error(), "E_FIXTURE_CONFIRM") {
		t.Errorf("want E_FIXTURE_CONFIRM, got: %v", err)
	}
	if !strings.Contains(err.Error(), "policy.evaluation_fixtures") {
		t.Errorf("refusal must point at the config switch, got: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "QS-6 fixture gate") || !strings.Contains(text, "Proceed? [y/N]") {
		t.Errorf("gate prompt missing from output: %q", text)
	}
	if strings.Contains(text, "DB reachable") {
		t.Error("database was contacted BEFORE the gate consented — ordering contract broken")
	}
}

// TestSetupFixtureGatePolicyTrueSkipsPrompt — standing consent: with
// policy.evaluation_fixtures=true the gate proceeds WITHOUT reading
// stdin and the command continues to the database step (which fails on
// the unreachable DSN — proving the gate passed and the DB was reached
// after consent).
func TestSetupFixtureGatePolicyTrueSkipsPrompt(t *testing.T) {
	resetSetupFlags(t)
	cfgFile := writeSelfHostedConfigWithPolicy(t, unreachablePortDSN(), true)
	withConfigPath(t, cfgFile)

	out := &bytes.Buffer{}
	cmd := findSetup(t)
	cmd.SetOut(out)
	cmd.SetIn(strings.NewReader("")) // must not be needed
	if err := cmd.Flags().Set("purge-fixtures", "true"); err != nil {
		t.Fatalf("set --purge-fixtures: %v", err)
	}

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected E_DB_UNREACHABLE (gate passed, DB contact failed), got nil")
	}
	if !strings.Contains(err.Error(), "E_DB_UNREACHABLE") {
		t.Errorf("want E_DB_UNREACHABLE, got: %v", err)
	}
	if !strings.Contains(out.String(), "standing consent") {
		t.Errorf("standing-consent line missing from output: %q", out.String())
	}
}

// TestSetupSelfHostedFreshMachineContract — the v0.1.0 fresh-install
// regression pins (found by the release-readiness audit):
//
//  1. `setup --self-hosted --with-compose` on a machine with NO config
//     file and NO database environment writes the compose + .env files
//     and exits nil — provisioning CANNOT run in the same invocation
//     because the database only exists once compose starts it; the
//     printed Next block IS the continuation (the config layer's own
//     hint names this exact command as the bootstrap).
//  2. bare `setup --self-hosted` (no env, no config) implies the
//     self-hosted profile — the failure mode is the actionable
//     E_DB_UNREACHABLE, never the circular client-profile
//     E_CONFIG_REQUIRED (whose Try: hint recommends --self-hosted).
func TestSetupSelfHostedFreshMachineContract(t *testing.T) {
	// Simulate a fresh machine: ambient env must not leak in (empty
	// string is treated as UNSET by the env layer).
	t.Setenv("GSEER_PROFILE", "")
	t.Setenv("GSEER_DB_DSN", "")
	t.Setenv("GSEER_DB_PASSWORD", "")
	t.Setenv("GSEER_CONFIG", "")
	t.Setenv("GSEER_CLIENT_ENDPOINT", "")

	// --- 1. --self-hosted --with-compose writes the bootstrap files. ---
	resetSetupFlags(t)
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	out := &bytes.Buffer{}
	cmd := findSetup(t)
	cmd.SetOut(out)
	if err := cmd.Flags().Set("self-hosted", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("with-compose", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("fresh-machine setup --self-hosted --with-compose must succeed: %v\n%s", err, out.String())
	}
	for _, f := range []string{"docker-compose.yml", ".env"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("bootstrap file %s missing: %v", f, err)
		}
	}
	if !strings.Contains(out.String(), "Next (QS-2)") {
		t.Errorf("next-commands block missing:\n%s", out.String())
	}

	// --- 2. bare --self-hosted implies the self-hosted profile. ---
	// Hermeticity: the failure must come from an UNREACHABLE database,
	// never from the ambient machine.  Machines that run PostgreSQL on
	// the default port (developer laptops, CI services, this sandbox)
	// would otherwise "succeed" against an unrelated cluster and
	// break the contract being pinned.  Port 9 (discard) with a
	// bounded connect timeout is refused deterministically; the
	// profile-implication assertion is unaffected — the regression
	// this test exists for is the CIRCULAR client-profile error
	// (client.endpoint), which is decided before any DB contact.
	resetSetupFlags(t)
	cmd2 := findSetup(t)
	out2 := &bytes.Buffer{}
	cmd2.SetOut(out2)
	if err := cmd2.Flags().Set("self-hosted", "true"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GSEER_DB_DSN", "postgres://gseer:fresh@127.0.0.1:9/gseer?sslmode=disable&connect_timeout=2")
	err2 := cmd2.RunE(cmd2, nil)
	if err2 == nil {
		t.Fatal("expected the DB-reachability failure on a fresh machine, got nil")
	}
	if strings.Contains(err2.Error(), "client.endpoint") {
		t.Errorf("--self-hosted must imply the self-hosted profile; got the client-profile error: %v", err2)
	}
	if !strings.Contains(err2.Error(), "E_DB_UNREACHABLE") {
		t.Errorf("want the actionable E_DB_UNREACHABLE, got: %v", err2)
	}
}
