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

	"github.com/spf13/cobra"
)

// TestNoPhantomCommands (AC-5.1 stub for M1) verifies that every command
// listed in the root help's "Commands:" section is in fact registered on
// the RootCmd command tree.  A "phantom command" is a name shown in help
// that has no backing command — the user types it and gets cobra's
// generic "unknown command" error.
//
// M1 scope: the test parses the help text and checks registration.  The
// full AC-5.1 CI test (parse help; every listed command registered AND
// runnable, with the actual command execution succeeding end-to-end)
// lands in M11 once the command implementations are complete.
//
// This stub already prevents the most common drift: a command is added
// to the help text but the registration call is missed (or vice versa).
func TestNoPhantomCommands(t *testing.T) {
	// Capture the registered command names from the cobra tree.  Force
	// cobra to materialize its auto-registered commands (completion, help)
	// first — they are added lazily and would otherwise not appear in
	// RootCmd.Commands() until first access.
	RootCmd.InitDefaultCompletionCmd()
	RootCmd.InitDefaultHelpCmd()
	registered := map[string]*cobra.Command{}
	for _, c := range RootCmd.Commands() {
		registered[c.Name()] = c
		// Subcommands (server start|status|stop, source list|add, etc.).
		for _, sc := range c.Commands() {
			registered[c.Name()+" "+sc.Name()] = sc
		}
	}

	// Parse the help text and extract the Commands: section.
	help := captureHelp(t)
	if !strings.Contains(help, "Commands:") {
		t.Fatal("help text has no Commands: section")
	}
	cmdsSection := substringAfter(help, "Commands:")
	cmdsSection = substringBefore(cmdsSection, "Quick start:")

	// Each line in the Commands: section that names a command must be
	// registered.  We extract the first token of each non-empty line.
	// Stop at the "Flags:" section which follows the command list.
	for _, line := range strings.Split(cmdsSection, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Commands:") {
			continue
		}
		if strings.HasPrefix(line, "Flags:") {
			break
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// The first field is the command name.  Cobra renders commands
		// as single words (e.g. "server", "source", "collect"), not
		// two-token pairs ("server start", "source list", etc.).
		name := fields[0]
		if _, ok := registered[name]; !ok {
			t.Errorf("phantom command %q listed in help but not registered", name)
		}
	}

	// The remaining planned commands (scan/asset) MUST be registered (as
	// hidden commands that exit 64) so that invocation does not fall
	// through to cobra's "unknown command" suggestion.  AC-5.7–5.8:
	// pointer line + exit 64.  (update/export became real commands in
	// v0.1.0 and are asserted by the Commands: scan above.)
	for _, name := range []string{"scan", "asset"} {
		c, ok := registered[name]
		if !ok {
			t.Errorf("planned command %q not registered (should be hidden + exit 64)", name)
			continue
		}
		if !c.Hidden {
			t.Errorf("planned command %q must be Hidden=true (only the Coming-later pointer line lists it)", name)
		}
	}
	// And the v0.1.0 promotions must NOT be hidden stubs anymore.
	for _, name := range []string{"update", "export"} {
		c, ok := registered[name]
		if !ok {
			t.Errorf("v0.1.0 command %q not registered", name)
			continue
		}
		if c.Hidden {
			t.Errorf("v0.1.0 command %q must be visible in help (it is real now)", name)
		}
	}
}

// captureHelp executes `gseer --help` against RootCmd and returns the
// output.  Resetting RootCmd args after Execute is required so subsequent
// tests don't inherit the --help arg.
func captureHelp(t *testing.T) string {
	t.Helper()
	buf := &bytes.Buffer{}
	RootCmd.SetOut(buf)
	RootCmd.SetErr(buf)
	RootCmd.SetArgs([]string{"--help"})
	defer RootCmd.SetArgs([]string{}) // reset for subsequent tests
	if err := RootCmd.Execute(); err != nil {
		t.Fatalf("RootCmd --help returned error: %v", err)
	}
	return buf.String()
}

func substringAfter(s, sep string) string {
	idx := strings.Index(s, sep)
	if idx < 0 {
		return s
	}
	return s[idx+len(sep):]
}

func substringBefore(s, sep string) string {
	idx := strings.Index(s, sep)
	if idx < 0 {
		return s
	}
	return s[:idx]
}
