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

//go:build windows

package export

import (
	"strings"
	"testing"
)

// filename_windows_test.go — pins the platform-honest clean-form
// contract on Windows where "/" and "\" are interchangeable OS separators.
//
// The production gate (filename.go ValidateOutPath) judges cleanliness on
// the platform-native form (filepath.FromSlash) so that:
//   - a forward-slash or mixed-separator path is ACCEPTED (separator style
//     is not uncleanliness on Windows);
//   - a ".." segment is REFUSED no matter which separator it hides behind
//     (the pre-fix implementation accepted "../escape.csv" on Windows
//     because the segment scan split on "\" only);
//   - control characters and leading flag-like names are REFUSED exactly
//     as on Unix (those checks are separator-independent).
//
// The separator-independent security invariants (traversal, control chars,
// leading dash) are ALSO covered platform-independently by TestValidateOutPath
// in export_test.go; this file adds the Windows-specific acceptance + the
// separator-hiding traversal regression.
func TestValidateOutPathWindowsSemantics(t *testing.T) {
	accepted := []string{
		"/tmp/seer-out/export.csv", // POSIX-style absolute path (forward slashes)
		"dir/sub/export.ndjson",    // nested path using "/"
		`dir\sub\export.ndjson`,    // native Windows separators
		`C:\Temp\001/out.json`,     // mixed separators (TempDir + "/name" join shape)
		"export.csv",
	}
	for _, p := range accepted {
		if err := ValidateOutPath(p); err != nil {
			t.Errorf("ValidateOutPath(%q) = %v, want nil (separator style is not uncleanliness)", p, err)
		}
	}

	refused := []struct {
		path string
		why  string
	}{
		{"../escape.csv", "traversal with forward separators"},
		{`..\escape.csv`, "traversal with native separators"},
		{`..\../evil.csv`, "mixed-separator traversal"},
		{"a/../b.csv", "mid-path traversal"},
		{"-flag.csv", "leading flag-like name"},
		{"out\n.csv", "control character (newline)"},
		{"out\t.csv", "control character (tab)"},
		{"\x00bad.csv", "control character (NUL)"},
	}
	for _, c := range refused {
		err := ValidateOutPath(c.path)
		if err == nil {
			t.Errorf("ValidateOutPath(%q) accepted — %s must be refused on Windows", c.path, c.why)
			continue
		}
		if !strings.Contains(err.Error(), "E_EXPORT_PATH") {
			t.Errorf("ValidateOutPath(%q) error lacks code: %v", c.path, err)
		}
	}
}
