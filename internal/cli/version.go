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

	"github.com/Lutfifakee-Project/GSEER/internal/version"
	"github.com/spf13/cobra"
)

// registerVersionCmd attaches the `gseer version` command (SPEC-03 §2).
//
// Output is the multi-line block from version.Info.String(): version,
// commit, built_at, go version.  The fields are injected via -ldflags in
// the Makefile/CI; an un-injected binary reports "dev" / "unknown" rather
// than fabricating values (the honesty floor, AC-8.x).
func registerVersionCmd(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show version, commit, and build info",
		Long: `Show version, commit, and build info.

The version, commit, and built_at fields are injected at build time via
-ldflags; an un-injected binary (e.g. 'go run') reports 'dev' and
'unknown' rather than fabricating values.  'gseer doctor' prints the
running binary's SHA-256 for checksum verification (SPEC-03 §4).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprint(cmd.OutOrStdout(), version.Current().String())
			return err
		},
	}
	root.AddCommand(cmd)
}
