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
	"fmt"
	"io"
	"strings"

	"github.com/Lutfifakee-Project/GSEER/internal/version"
	"github.com/spf13/cobra"
)

// bannerTemplate is the GSEER ASCII banner.  The final %s is replaced by
// the dynamic version string (v-prefixed).  Trailing backslashes on the
// first four lines are literal (raw string, no escape processing).
const bannerTemplate = `
   ___________ ________________ 
  / ____/ ___// ____/ ____/ __ \
 / / __ \__ \/ __/ / __/ / /_/ /
/ /_/ /___/ / /___/ /___/ _, _/ 
\____//____/_____/_____/_/ |_|%s`

// bannerVersion returns the display version string for the banner,
// prefixed with two spaces so the version sits at the right edge of the
// last banner line (e.g. "|_|  v0.1.0").  It uses build metadata Version;
// when empty (no ldflags injection), falls back to "dev".  When Version
// already carries a "v" prefix (e.g. "v0.1.0"), it is returned as-is to
// avoid doubling the prefix (no "vv0.1.0").
func bannerVersion() string {
	v := version.Current().Version
	if v == "" {
		v = "dev"
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return "  " + v
}

// printBanner renders the GSEER ASCII banner to w.  When the quiet flag
// is set, nothing is printed.
func printBanner(w io.Writer) {
	if flags.quiet {
		return
	}
	fmt.Fprintf(w, "%s\n", fmt.Sprintf(bannerTemplate, bannerVersion()))
}

// bannerHelpFunc wraps RootCmd's help rendering so the ASCII banner is
// printed before the help text for root-level invocations
// (`gseer`, `gseer --help`, `gseer help`).  Subcommand help
// (`gseer search --help`) uses the default cobra help func and prints
// no banner.
//
// cobra's HelpFunc() walks up the parent chain when a command has no
// helpFunc of its own, so subcommands inherit RootCmd's func.  We skip
// the banner when cmd has a parent (i.e. it is a subcommand).
//
// To render the subcommand's help without infinite recursion
// (cmd.Help() → HelpFunc() → parent → bannerHelpFunc → ...), we
// temporarily clear RootCmd's helpFunc so cmd.Help() falls through to
// cobra's default help rendering, then restore it.
func bannerHelpFunc(cmd *cobra.Command, args []string) {
	if !cmd.HasParent() {
		printBanner(cmd.OutOrStdout())
	}
	// Walk up to RootCmd.
	root := cmd
	for root.HasParent() {
		root = root.Parent()
	}
	origRootHelp := root.HelpFunc()
	root.SetHelpFunc(nil)
	defer root.SetHelpFunc(origRootHelp)
	cmd.Help()
}
