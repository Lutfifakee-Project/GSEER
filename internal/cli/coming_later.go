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
	"fmt"

	"github.com/spf13/cobra"
)

// registerComingLaterCmds registers the four planned-but-absent commands
// (SPEC-03 §2): update, scan, asset, export.  Each is registered so that
// `gseer <name>` does not fall through to cobra's generic "unknown
// command" suggestion; instead it prints the honest pointer line and
// exits 64 (AC-5.7–5.8).
//
// These commands are NOT listed in the top-level "Commands:" section of
// help (they appear only in the "Coming later" pointer line); they are
// hidden from cobra's auto-completion and help listing via Hidden=true.
//
// `gseer update --check` evaluation: the owner's Phase 1 directive
// requires evaluating whether v0.0.1 ships a check-only subcommand.  The
// evaluation outcome will be recorded in the M11 report.  Until then,
// `gseer update` (any invocation, including --check) prints the pointer
// line and exits 64.  We do not advertise unavailable functionality.
func registerComingLaterCmds(root *cobra.Command) {
	// v0.1.0 arrivals: `update` and `export` are real commands now
	// (export.go / update.go).  The remaining planned commands stay
	// hidden stubs with the honest pointer line + exit 64.
	for _, name := range []string{"scan", "asset"} {
		cmd := &cobra.Command{
			Use:    name,
			Short:  "Planned, not in this release — see docs/ROADMAP.md",
			Hidden: true, // not in the top-level Commands: listing
			// DisableFlagParsing so any flag (e.g. `gseer update --check`,
			// `gseer scan --target ...`) reaches RunE rather than tripping
			// cobra's "unknown flag" error.  The pointer line fires either
			// way; the user gets the honest message and exit 64.
			DisableFlagParsing: true,
			// Accept any args for the same reason.
			Args: cobra.ArbitraryArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				// We are about to exit(64); write errors are not
				// recoverable here, so we explicitly discard them.
				// exitFunc (the process-exit seam shared with search.go)
				// keeps this testable.
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"E_NOT_IMPLEMENTED: \"gseer %s\" is planned, not in this release.\n",
					cmd.Name())
				switch cmd.Name() {
				case "scan":
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Authorized service observation waits on the P2/PAC regime (D-9) — see docs/ROADMAP.md.")
				case "asset":
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Asset detail view is planned for a later release — see docs/ROADMAP.md.")
				}
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Exit code 64.")
				exitFunc(ExitUsagePlanned)
				return nil // unreachable; exitFunc terminates
			},
		}
		root.AddCommand(cmd)
	}
}
