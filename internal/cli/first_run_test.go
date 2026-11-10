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

// first_run_test.go — the zero-configuration entry point (v0.3.0):
// bare `gseer` auto-initializes personal mode on the first run, prints
// an honest status afterwards, and `--version` finally works (the flag
// existed since M1 but was never consumed).

import (
        "bytes"
        "os"
        "path/filepath"
        "strings"
        "testing"
)

// firstRun executes the root command with the given args against an
// isolated HOME (configDir() honors it) and returns the output.
func firstRun(t *testing.T, args ...string) string {
        t.Helper()
        return firstRunAt(t, t.TempDir(), args...)
}

func firstRunAt(t *testing.T, home string, args ...string) string {
        t.Helper()
        t.Setenv("HOME", home)
        t.Setenv("GSEER_CONFIG", "")
        oldCfg, oldQuiet, oldProfile := flags.configPath, flags.quiet, flags.profile
        t.Cleanup(func() {
                flags.configPath, flags.quiet, flags.profile = oldCfg, oldQuiet, oldProfile
                RootCmd.SetArgs([]string{})
                RootCmd.SetOut(nil)
                RootCmd.SetErr(nil)
        })
        flags.configPath, flags.quiet, flags.profile = "", false, ""

        var buf bytes.Buffer
        RootCmd.SetOut(&buf)
        RootCmd.SetErr(&buf)
        // Bare invocation MUST pass an EMPTY (non-nil) slice: SetArgs(nil)
        // makes cobra fall back to os.Args — the test binary's own flags.
        if args == nil {
                args = []string{}
        }
        RootCmd.SetArgs(args)
        // pflag values PERSIST across Execute calls on the shared command
        // tree: a previous test's parsed `--help` would force this bare run
        // into cobra's help path instead of RunE.  Reset it explicitly.
        _ = RootCmd.Flags().Set("help", "false")
        t.Logf("diag: RunE set=%v args=%q", RootCmd.RunE != nil, args)
        if err := RootCmd.Execute(); err != nil {
                t.Fatalf("execute %v: %v\noutput: %s", args, err, buf.String())
        }
        return buf.String()
}

func TestFirstRunAutoInitializesPersonalMode(t *testing.T) {
        home := t.TempDir()
        out := firstRunAt(t, home)

        for _, want := range []string{
                "Welcome to GSEER",
                "personal database",
                "migrations: schema at head",
                "config written",
                "Where does the data come from?",
                "gseer setup --load-fixtures",
        } {
                if !strings.Contains(out, want) {
                        t.Errorf("first-run output missing %q:\n%s", want, out)
                }
        }
        // The artifacts exist at the platform paths under the isolated HOME.
        if _, err := os.Stat(filepath.Join(home, ".gseer", "config.yaml")); err != nil {
                t.Errorf("config.yaml not created: %v", err)
        }
        if _, err := os.Stat(filepath.Join(home, ".gseer", "gseer.db")); err != nil {
                t.Errorf("gseer.db not created: %v", err)
        }

        // A second bare run is the STATUS panel — no re-initialization.
        out2 := firstRunAt(t, home)
        if strings.Contains(out2, "Welcome to GSEER") {
                t.Errorf("second run re-initialized (must be the status panel):\n%s", out2)
        }
        for _, want := range []string{"profile: personal", "index:", "EMPTY"} {
                if !strings.Contains(out2, want) {
                        t.Errorf("status output missing %q:\n%s", want, out2)
                }
        }
}

func TestFirstRunVersionFlagConsumed(t *testing.T) {
        out := firstRun(t, "--version")
        if !strings.Contains(out, "gseer ") {
                t.Errorf("--version output missing the version block:\n%s", out)
        }
        if strings.Contains(out, "Welcome") || strings.Contains(out, "profile:") {
                t.Errorf("--version must not run the first-run/status flow:\n%s", out)
        }
}
