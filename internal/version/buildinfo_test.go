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

package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

// TestApplyBuildInfoGoInstall pins the `go install ...@vX.Y.Z` fallback:
// an un-injected binary adopts the toolchain-recorded module version,
// VCS revision, and VCS commit time.  This is the fix for binaries that
// reported "gseer dev / commit unknown" when installed without ldflags
// (the only honest metadata a module-scoped install carries).
func TestApplyBuildInfoGoInstall(t *testing.T) {
	info := Info{Version: "dev", Commit: "unknown", BuiltAt: "unknown", GoVersion: "go1.26.0"}
	bi := &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/Lutfifakee-Project/GSEER", Version: "v0.2.1"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "ab55a38965f3088365e3aec3638065a10b4a9bd9"},
			{Key: "vcs.time", Value: "2026-11-08T10:00:00Z"},
		},
	}

	got := applyBuildInfo(info, bi)

	if got.Version != "v0.2.1" {
		t.Errorf("Version: want v0.2.1 (module version), got %q", got.Version)
	}
	if got.Commit != "ab55a38965f3088365e3aec3638065a10b4a9bd9" {
		t.Errorf("Commit: want the full vcs.revision, got %q", got.Commit)
	}
	if got.BuiltAt != "2026-11-08T10:00:00Z" {
		t.Errorf("BuiltAt: want the vcs.time stamp, got %q", got.BuiltAt)
	}
}

// TestApplyBuildInfoDevelStaysDev pins the honesty floor: a plain
// `go build`/`go run` reports "(devel)" — that is NOT a version, and the
// binary keeps saying "dev" rather than dressing development up as a
// release.  Empty settings change nothing either.
func TestApplyBuildInfoDevelStaysDev(t *testing.T) {
	info := Info{Version: "dev", Commit: "unknown", BuiltAt: "unknown", GoVersion: "go1.26.0"}
	bi := &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/Lutfifakee-Project/GSEER", Version: "(devel)"},
	}

	got := applyBuildInfo(info, bi)

	if got.Version != "dev" {
		t.Errorf("Version: want 'dev' for (devel), got %q", got.Version)
	}
	if got.Commit != "unknown" || got.BuiltAt != "unknown" {
		t.Errorf("Commit/BuiltAt: want 'unknown' with no vcs settings, got %q/%q", got.Commit, got.BuiltAt)
	}
}

// TestApplyBuildInfoInjectedWins pins precedence: ldflags-injected
// values are NEVER overwritten by the toolchain's stamps (the Makefile
// build remains the source of truth for release binaries).
func TestApplyBuildInfoInjectedWins(t *testing.T) {
	info := Info{Version: "v0.2.1", Commit: "cafe123", BuiltAt: "2026-11-09T00:00:00Z", GoVersion: "go1.26.0"}
	bi := &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/Lutfifakee-Project/GSEER", Version: "v0.0.1"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "deadbeef"},
			{Key: "vcs.time", Value: "1999-01-01T00:00:00Z"},
		},
	}

	got := applyBuildInfo(info, bi)

	if got.Version != "v0.2.1" || got.Commit != "cafe123" || got.BuiltAt != "2026-11-09T00:00:00Z" {
		t.Errorf("injected values must win, got %+v", got)
	}
}

// TestApplyBuildInfoNil pins the nil guard: a nil *BuildInfo changes
// nothing (defensive — ReadBuildInfo never returns (nil, true), but the
// helper is exported to tests and must be total).
func TestApplyBuildInfoNil(t *testing.T) {
	info := Info{Version: "dev", Commit: "unknown", BuiltAt: "unknown", GoVersion: "go1.26.0"}

	got := applyBuildInfo(info, nil)

	if got != info {
		t.Errorf("nil BuildInfo must be a no-op, got %+v", got)
	}
}

// TestCurrentWiresBuildInfoSeam pins the wiring of Current() through
// the readBuildInfo seam: with a stubbed toolchain report the returned
// Info carries the module version.  The stub is restored via t.Cleanup.
func TestCurrentWiresBuildInfoSeam(t *testing.T) {
	old := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Path: "github.com/Lutfifakee-Project/GSEER", Version: "v9.8.7"},
		}, true
	}
	t.Cleanup(func() { readBuildInfo = old })

	info := Current()

	if info.Version != "v9.8.7" {
		t.Errorf("Version: want v9.8.7 from the stubbed build info, got %q", info.Version)
	}
	if !strings.HasPrefix(info.GoVersion, "go1.") {
		t.Errorf("GoVersion: want the runtime version, got %q", info.GoVersion)
	}
}

// TestIsRealModuleVersion pins the acceptance predicate: only "v…"
// strings count as substantiated module versions.
func TestIsRealModuleVersion(t *testing.T) {
	for _, want := range []string{"v0.2.1", "v1.0.0-rc.1"} {
		if !isRealModuleVersion(want) {
			t.Errorf("isRealModuleVersion(%q) = false, want true", want)
		}
	}
	for _, no := range []string{"", "(devel)", "devel", "latest", "0.2.1"} {
		if isRealModuleVersion(no) {
			t.Errorf("isRealModuleVersion(%q) = true, want false", no)
		}
	}
}
