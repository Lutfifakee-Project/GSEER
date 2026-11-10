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

// Package version exposes build-time metadata injected via -ldflags.
//
// The variables Version, Commit, and BuiltAt are normally set by the
// Makefile (or CI) using linker flags.  When they are not injected —
// most notably for `go install ...@vX.Y.Z` binaries, which cannot run
// ldflags — Current() falls back to the Go toolchain's own build
// information (debug.ReadBuildInfo): the module version, the VCS
// revision, and the VCS commit time.  That fallback is still honest: it
// only ever reports metadata the toolchain itself recorded, and it
// keeps the plain "dev"/"unknown" values when the binary carries no
// such evidence (a plain `go build`/`go run` inside a module reports
// "(devel)", which stays "dev").  GSEER never claims a version it
// cannot substantiate (SPEC-03 §4: checksums, not signatures, in
// v0.0.1; `gseer doctor` prints the running binary's SHA-256).
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Variables injected via -ldflags "-X ...".  Defaults are intentionally
// honest: when not injected, we say so plainly rather than fabricating
// a version (AC-8.x documentation honesty; the honesty floor).
var (
	// Version is the semantic version string, e.g. "v0.0.1".  When built
	// without ldflags it is "dev" — Current() then tries the module
	// version recorded by the toolchain (set for `go install ...@vX.Y.Z`)
	// and reports "dev" only when that yields nothing.
	Version = "dev"
	// Commit is the VCS commit hash the binary was built from.  Empty
	// when built without ldflags; Current() falls back to the toolchain's
	// vcs.revision stamp (full hash — unambiguous next to a release page)
	// and otherwise reports "unknown".
	Commit = ""
	// BuiltAt is the build timestamp in RFC3339 form.  Empty when built
	// without ldflags; Current() falls back to the toolchain's vcs.time
	// stamp (also RFC3339) and otherwise reports "unknown".
	BuiltAt = ""
)

// readBuildInfo is a seam for tests: production points at
// debug.ReadBuildInfo, which is environment-bound.
var readBuildInfo = debug.ReadBuildInfo

// Info is the structured form of the build metadata.  The CLI's `version`
// command renders this; future surfaces (doctor, /api/v1/version) reuse it.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuiltAt   string `json:"built_at"`
	GoVersion string `json:"go_version"`
}

// Current returns the build metadata for the running binary.  Go runtime
// version is always available; the other fields reflect ldflags injection
// with the toolchain's build information as the honest fallback (see the
// package comment).
func Current() Info {
	info := Info{
		Version:   Version,
		Commit:    orUnknown(Commit),
		BuiltAt:   orUnknown(BuiltAt),
		GoVersion: runtime.Version(),
	}
	if bi, ok := readBuildInfo(); ok {
		info = applyBuildInfo(info, bi)
	}
	return info
}

// applyBuildInfo fills the un-injected fields from the toolchain's build
// information.  Pure so tests can pin every branch: an injected value is
// NEVER overwritten, "(devel)" never becomes a version, and only the
// toolchain's own vcs.* stamps are trusted.
func applyBuildInfo(info Info, bi *debug.BuildInfo) Info {
	if bi == nil {
		return info
	}
	if info.Version == "dev" {
		if v := strings.TrimSpace(bi.Main.Version); isRealModuleVersion(v) {
			info.Version = v
		}
	}
	for _, s := range bi.Settings {
		val := strings.TrimSpace(s.Value)
		if val == "" {
			continue
		}
		switch {
		case info.Commit == "unknown" && s.Key == "vcs.revision":
			info.Commit = val
		case info.BuiltAt == "unknown" && s.Key == "vcs.time":
			info.BuiltAt = val // the toolchain stamps RFC3339 — the BuiltAt contract
		}
	}
	return info
}

// isRealModuleVersion reports whether v is a substantiated module
// version: a plain `go build`/`go test` reports "(devel)", an empty
// string means nothing was recorded, and only "v…" strings are release
// versions (the go command's canonical form).
func isRealModuleVersion(v string) bool {
	return v != "" && v != "(devel)" && strings.HasPrefix(v, "v")
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
