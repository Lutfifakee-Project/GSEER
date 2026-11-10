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
)

// TestRootHelpListsCommands verifies the root command's help output
// contains every command registered in M1 (SPEC-03 §2).  This is the
// M1 stub for AC-5.1: the full no-phantom-commands CI test (parse help;
// every listed command registered AND runnable) lands in M11.  Here we
// pin the help text shape and the registered command set.
func TestRootHelpListsCommands(t *testing.T) {
	buf := &bytes.Buffer{}
	RootCmd.SetOut(buf)
	RootCmd.SetErr(buf)
	RootCmd.SetArgs([]string{"--help"})
	if err := RootCmd.Execute(); err != nil {
		// --help is not an error in cobra; if it returns one, the flag
		// handling is broken and we want to know immediately.
		t.Fatalf("RootCmd --help returned error: %v", err)
	}
	help := buf.String()

	// Every command listed in the root help's "Commands:" section must appear.
	// Cobra renders commands as single words (e.g. "search", "server", "source")
	// rather than the old static format ("search <query>", "server start", etc.).
	want := []string{
		"search",
		"setup",
		"doctor",
		"version",
		"server",
		"db",
		"source",
		"rules",
		"collect",
		"export",
		"completion",
	}
	for _, w := range want {
		if !strings.Contains(help, w) {
			t.Errorf("help missing %q\n--- help output ---\n%s", w, help)
		}
	}

	// The "Coming later" pointer line is normative (SPEC-03 §2).  The
	// v0.1.0 release promoted update/export to real commands; scan and
	// asset remain planned.
	for _, w := range []string{"Coming later", "scan", "asset", "docs/ROADMAP.md", "export"} {
		if !strings.Contains(help, w) {
			t.Errorf("help missing pointer-line element %q\n--- help output ---\n%s", w, help)
		}
	}

	// The AGPL license line.
	if !strings.Contains(help, "GNU AGPL-3.0") {
		t.Errorf("help missing AGPL line\n--- help output ---\n%s", help)
	}
}

// TestVersionCommandOutput verifies the `gseer version` command produces
// the stable multi-line block (version / commit / built_at / go).  An
// un-injected binary reports "dev" / "unknown" / "unknown" / runtime
// version (the honesty floor, AC-8.x).
func TestVersionCommandOutput(t *testing.T) {
	buf := &bytes.Buffer{}
	RootCmd.SetOut(buf)
	RootCmd.SetErr(buf)
	RootCmd.SetArgs([]string{"version"})
	if err := RootCmd.Execute(); err != nil {
		t.Fatalf("gseer version returned error: %v", err)
	}
	out := buf.String()

	for _, w := range []string{"gseer ", "commit:", "built_at:", "go:"} {
		if !strings.Contains(out, w) {
			t.Errorf("version output missing %q\n--- output ---\n%s", w, out)
		}
	}
	// An un-injected binary must say "dev", not a fabricated version.
	if !strings.Contains(out, "gseer dev") {
		t.Errorf("version output should report 'dev' when un-injected; got:\n%s", out)
	}
}
