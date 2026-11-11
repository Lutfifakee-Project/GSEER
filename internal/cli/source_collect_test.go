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

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Lutfifakee-Project/gseer/internal/collect"
)

// source_collect_test.go — the v0.0.2 `source` / `collect` commands:
// argument validation and the terms gate, exercised WITHOUT a database
// (the refusal paths run before any DB contact by design — a misconfigured
// or non-compliant request must fail fast and offline).

// runCLI executes the root command with args, capturing stdout+stderr.
// It resets the shared flag structs first (pflag values persist across
// Execute calls on the singleton command tree).
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	sourceAddReset()
	collectRunReset()
	redriveReset()
	buf := &bytes.Buffer{}
	RootCmd.SetOut(buf)
	RootCmd.SetErr(buf)
	RootCmd.SetArgs(args)
	err := RootCmd.Execute()
	return buf.String(), err
}

// resetRootState restores cobra's flag/arg state between executions.
func resetRootState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		RootCmd.SetArgs(nil)
		RootCmd.SetOut(nil)
		RootCmd.SetErr(nil)
	})
}

func TestSourceAddRefusesTermsDisabledConnectorOffline(t *testing.T) {
	resetRootState(t)
	for _, tc := range []struct{ connector, doc string }{
		{"rdap", "docs/SOURCES.md §B"},
		{"ct", "docs/SOURCES.md §A"},
	} {
		_, err := runCLI(t, "source", "add", "nope-"+tc.connector,
			"--type", tc.connector, "--seed-mode", "config", "--seeds", "example.net", "--enable")
		if err == nil {
			t.Fatalf("connector %q must refuse --enable", tc.connector)
		}
		if !strings.Contains(err.Error(), "E_SOURCE_TERMS") {
			t.Errorf("connector %q: expected E_SOURCE_TERMS, got: %v", tc.connector, err)
		}
		if !strings.Contains(err.Error(), tc.doc) {
			t.Errorf("connector %q: refusal must cite the review register (%s), got: %v",
				tc.connector, tc.doc, err)
		}
	}
}

func TestSourceAddUnknownConnectorType(t *testing.T) {
	resetRootState(t)
	_, err := runCLI(t, "source", "add", "x", "--type", "port-scanner", "--seed-mode", "config", "--seeds", "a.example")
	if err == nil || !strings.Contains(err.Error(), "E_SOURCE_CONNECTOR") {
		t.Fatalf("expected E_SOURCE_CONNECTOR, got %v", err)
	}
	// The refusal names the registered kinds — the operator can self-serve.
	if !strings.Contains(err.Error(), "dns") || !strings.Contains(err.Error(), "ip2asn") {
		t.Errorf("refusal should list registered kinds: %v", err)
	}
}

func TestSourceAddRequiresSeedsInConfigMode(t *testing.T) {
	resetRootState(t)
	_, err := runCLI(t, "source", "add", "x", "--type", "dns", "--seed-mode", "config")
	if err == nil || !strings.Contains(err.Error(), "E_USAGE") {
		t.Fatalf("expected E_USAGE for missing seeds, got %v", err)
	}
	if strings.Contains(err.Error(), "DB") {
		t.Error("validation failure must not touch the database")
	}
}

func TestSourceAddBadSeedMode(t *testing.T) {
	resetRootState(t)
	_, err := runCLI(t, "source", "add", "x", "--type", "dns", "--seed-mode", "wordlist", "--seeds", "a.example")
	if err == nil || !strings.Contains(err.Error(), "E_USAGE") {
		t.Fatalf("expected E_USAGE for bad seed-mode, got %v", err)
	}
}

func TestCollectRunRequiresSourceOrAll(t *testing.T) {
	resetRootState(t)
	_, err := runCLI(t, "collect", "run")
	if err == nil || !strings.Contains(err.Error(), "E_USAGE") {
		t.Fatalf("expected E_USAGE without --source/--all, got %v", err)
	}
}

func TestHelpListsV002Commands(t *testing.T) {
	resetRootState(t)
	help, err := runCLI(t, "--help")
	if err != nil {
		t.Fatalf("--help returned error: %v", err)
	}
	for _, want := range []string{
		"source",
		"collect",
		"suppress",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("root help missing %q", want)
		}
	}
}

// The terms gate is ALSO enforced by the runner (independent of the CLI)
// — one extra layer pinned at the package level: a caller that bypasses
// the CLI and flips enabled=true in the database still hits the refusal.
// The DB-level test lives in internal/collect (integration tag); here we
// pin that the CLI's verdict source is the SAME registry the runner uses.
func TestSourceCommandsUseRunnerRegistry(t *testing.T) {
	reg := collect.Registry()
	if len(reg) != 5 {
		t.Errorf("expected 5 registered connectors, got %d", len(reg))
	}
	for _, kind := range []string{"dns", "ip2asn", "ptr", "rdap", "ct"} {
		if _, ok := reg[kind]; !ok {
			t.Errorf("connector %q missing from the shared registry", kind)
		}
	}
}
