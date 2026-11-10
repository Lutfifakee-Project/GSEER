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

import "testing"

// TestSearchFlagsResetSeam pins the searchFlagsReset test seam in the
// untagged build: the seam is a no-op closure in production (cobra flag
// values persist on the shared RootCmd by design) and is swapped for a
// real reset by the integration-tagged suites, which must clear sticky
// --facet/--max-values values between Execute calls.  Calling it here
// keeps the seam referenced outside the integration build tag so the
// linter sees the intended use, and fails if the wiring regresses.
func TestSearchFlagsResetSeam(t *testing.T) {
	if searchFlagsReset == nil {
		t.Fatal("searchFlagsReset seam is nil; the cli integration suites would inherit sticky search flags")
	}
	searchFlagsReset() // must be safe to call at any time (no-op by default)
}
