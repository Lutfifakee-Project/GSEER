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

// Package version exposes build-time metadata injected via -ldflags.
//
// The variables Version, Commit, and BuiltAt are normally set by the
// Makefile (or CI) using linker flags; their zero values are appropriate
// for `go run` and `go test` only.  GSEER never claims a version it
// cannot substantiate (SPEC-03 §4: checksums, not signatures, in v0.0.1;
// `gseer doctor` prints the running binary's SHA-256).
package version

import (
	"fmt"
	"runtime"
	"strings"
)

// Variables injected via -ldflags "-X ...".  Defaults are intentionally
// honest: when not injected, we say so plainly rather than fabricating
// a version (AC-8.x documentation honesty; the honesty floor).
var (
	// Version is the semantic version string, e.g. "v0.0.1".  When built
	// without ldflags it is "dev" — `gseer doctor` reports this honestly.
	Version = "dev"
	// Commit is the VCS commit hash the binary was built from.  Empty
	// when built without ldflags (e.g. `go run`); reported as "unknown".
	Commit = ""
	// BuiltAt is the build timestamp in RFC3339 form.  Empty when built
	// without ldflags; reported as "unknown".
	BuiltAt = ""
)

// Info is the structured form of the build metadata.  The CLI's `version`
// command renders this; future surfaces (doctor, /api/v1/version) reuse it.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuiltAt   string `json:"built_at"`
	GoVersion string `json:"go_version"`
}

// Current returns the build metadata for the running binary.  Go runtime
// version is always available; the other fields reflect ldflags injection.
func Current() Info {
	return Info{
		Version:   Version,
		Commit:    orUnknown(Commit),
		BuiltAt:   orUnknown(BuiltAt),
		GoVersion: runtime.Version(),
	}
}

// String renders the version block as a human-readable multi-line string.
// Format is stable so that tests and the QUICKSTART can pin it.
func (i Info) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "gseer %s\n", i.Version)
	fmt.Fprintf(&b, "  commit:   %s\n", i.Commit)
	fmt.Fprintf(&b, "  built_at: %s\n", i.BuiltAt)
	fmt.Fprintf(&b, "  go:       %s\n", i.GoVersion)
	return b.String()
}

// orUnknown replaces an empty string with the literal "unknown" so that
// `gseer version` never prints a blank field (which would imply the
// metadata was lost or hidden — the honesty floor).
func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
