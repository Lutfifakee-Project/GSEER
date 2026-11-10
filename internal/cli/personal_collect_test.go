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

// personal_collect_test.go — v0.3.0: `gseer source` and `gseer collect`
// work in PERSONAL mode through the same commands as self-hosted (the
// registry lives in the embedded SQLite file).  Nothing here touches
// the network: only the registry surface is exercised; the runner's
// personal-engine path is covered in internal/collect unit tests.

import (
        "bytes"
        "strings"
        "testing"

        "github.com/spf13/cobra"
)

func TestPersonalSourceRegistryCommands(t *testing.T) {
        dbPath := newMigratedPersonalDB(t)
        cfgPath := writePersonalConfigForTest(t, dbPath)

        restore := captureRootOutput(t)
        defer restore()

        run := func(t *testing.T, args ...string) string {
                t.Helper()
                // NOTE: no sourceAddReset() here — zeroing the shared flag struct
                // would also erase the registered defaults (`--seed-mode config`),
                // poisoning later tests that omit the flag.  Every source add in
                // this test passes its flags explicitly instead.
                var buf bytes.Buffer
                RootCmd.SetOut(&buf)
                RootCmd.SetErr(&buf)
                t.Cleanup(func() { RootCmd.SetOut(nil); RootCmd.SetErr(nil) })
                RootCmd.SetArgs(append([]string{"--config", cfgPath}, args...))
                t.Cleanup(func() { RootCmd.SetArgs([]string{}) })
                if err := RootCmd.Execute(); err != nil {
                        t.Fatalf("execute %v: %v\noutput: %s", args, err, buf.String())
                }
                return buf.String()
        }

        out := run(t, "source", "add", "my-ptr", "--type", "ptr",
                "--seed-mode", "config", "--seeds", "203.0.113.50")
        if !strings.Contains(out, "Registered source") || !strings.Contains(out, "ptr") {
                t.Errorf("source add output missing registration line:\n%s", out)
        }

        out = run(t, "source", "enable", "my-ptr")
        if !strings.Contains(out, "enabled") {
                t.Errorf("source enable output:\n%s", out)
        }

        out = run(t, "source", "list")
        if !strings.Contains(out, "my-ptr") || !strings.Contains(out, "ptr") {
                t.Errorf("source list missing the registered row:\n%s", out)
        }

        out = run(t, "collect", "status")
        if !strings.Contains(out, "my-ptr") || !strings.Contains(out, "JOBS") {
                t.Errorf("collect status missing the registry table:\n%s", out)
        }

        out = run(t, "source", "disable", "my-ptr")
        if !strings.Contains(out, "disabled") {
                t.Errorf("source disable output:\n%s", out)
        }
}

// captureRootOutput keeps tests from writing to the real stdout while
// letting each run() install its own buffer.
func captureRootOutput(t *testing.T) func() {
        t.Helper()
        oldCfg := flags.configPath
        oldQuiet := flags.quiet
        oldProfile := flags.profile
        return func() {
                flags.configPath = oldCfg
                flags.quiet = oldQuiet
                flags.profile = oldProfile
                RootCmd.SetArgs([]string{})
        }
}

var _ = cobra.Command{}
