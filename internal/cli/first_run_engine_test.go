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

// first_run_engine_test.go — the zero-configuration first run applied to
// the ENGINE commands (v0.3.1): a user's first GSEER invocation is not
// always bare `gseer`.  `gseer search 'port=443'`, `gseer source add …`,
// `gseer suppress list`, or `gseer doctor` must all end in a working
// personal engine (or an honest, pointed refusal for profiles the
// operator explicitly chose) — never the pre-v0.3.1 dead end where the
// first engine command failed with an unrelated client-profile error.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// engineRun executes the built binary in an isolated HOME and returns
// (output, exitErr) without failing on a non-zero exit — the caller
// asserts the expected outcome.
func engineRun(t *testing.T, bin, home string, env map[string]string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = home
	base := map[string]string{
		"HOME":   home,
		"PATH":   "/usr/bin:/bin",
		"TMPDIR": os.TempDir(),
	}
	for k, v := range env {
		base[k] = v
	}
	cmd.Env = mapToEnv(base)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestZeroConfigFirstEngineCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission assertions do not apply on windows")
	}
	if testing.Short() {
		t.Skip("builds and drives the real binary")
	}
	bin := buildFreshBinary(t)

	t.Run("source add is a valid first command", func(t *testing.T) {
		home := t.TempDir()
		out, err := engineRun(t, bin, home, nil, "source", "add", "my-dns", "--type", "dns", "--seeds", "example.net", "--enable", "--yes")
		if err != nil {
			t.Fatalf("first-command source add failed: %v\n%s", err, out)
		}
		for _, want := range []string{
			"First GSEER run — initializing PERSONAL MODE",
			"config written",
			`Registered source "my-dns"`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("first-command source add output missing %q:\n%s", want, out)
			}
		}
		// The registry row must be readable by the NEXT command in the
		// same home (the auto-init must not create a throwaway engine).
		out, err = engineRun(t, bin, home, nil, "source", "list")
		if err != nil || !strings.Contains(out, "my-dns") {
			t.Errorf("source list after first-command add: err=%v\n%s", err, out)
		}
		// The config the auto-init wrote must be the profile the store
		// commands expect — and the database file must exist on disk.
		cfg, rerr := os.ReadFile(filepath.Join(home, ".gseer", "config.yaml"))
		if rerr != nil || !strings.Contains(string(cfg), "profile: personal") {
			t.Errorf("auto-init config missing/invalid: err=%v\n%s", rerr, cfg)
		}
		if _, serr := os.Stat(filepath.Join(home, ".gseer", "gseer.db")); serr != nil {
			t.Errorf("personal database not created: %v", serr)
		}
	})

	t.Run("search is a valid first command", func(t *testing.T) {
		home := t.TempDir()
		out, err := engineRun(t, bin, home, nil, "search", "port=443")
		if err != nil {
			t.Fatalf("first-command search failed: %v\n%s", err, out)
		}
		if !strings.Contains(out, "0 result") {
			t.Errorf("first-command search should return an empty (honest) index:\n%s", out)
		}
	})

	t.Run("suppress list is a valid first command", func(t *testing.T) {
		home := t.TempDir()
		out, err := engineRun(t, bin, home, nil, "suppress", "list")
		if err != nil {
			t.Fatalf("first-command suppress list failed: %v\n%s", err, out)
		}
		if !strings.Contains(out, "No suppression rules") {
			t.Errorf("first-command suppress list unexpected:\n%s", out)
		}
	})

	t.Run("doctor on a fresh home reports the zero-config state without failing", func(t *testing.T) {
		home := t.TempDir()
		out, err := engineRun(t, bin, home, nil, "doctor")
		if err != nil {
			t.Fatalf("doctor on a fresh home must not fail: %v\n%s", err, out)
		}
		for _, want := range []string{
			"zero-config first run",
			"0 FAIL",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("fresh-home doctor output missing %q:\n%s", want, out)
			}
		}
		// Doctor is read-only: it must NOT provision anything.
		if _, serr := os.Stat(filepath.Join(home, ".gseer", "gseer.db")); serr == nil {
			t.Errorf("doctor created the personal database (read-only contract violated)")
		}
	})

	t.Run("explicit self-hosted choice keeps the honest refusal", func(t *testing.T) {
		home := t.TempDir()
		out, err := engineRun(t, bin, home, nil, "--profile", "self-hosted", "doctor")
		if err == nil {
			t.Fatalf("self-hosted doctor without any database must fail:\n%s", out)
		}
		if !strings.Contains(out, "FAIL") {
			t.Errorf("expected an honest FAIL report:\n%s", out)
		}
		if strings.Contains(out, "First GSEER run") {
			t.Errorf("an explicit --profile choice must never trigger the personal auto-init:\n%s", out)
		}
		// And nothing was provisioned behind the operator's back.
		if _, serr := os.Stat(filepath.Join(home, ".gseer", "gseer.db")); serr == nil {
			t.Errorf("self-hosted refusal created a personal database")
		}
	})

	t.Run("search --validate needs no configuration at all", func(t *testing.T) {
		home := t.TempDir()
		out, err := engineRun(t, bin, home, nil, "search", "--validate", "port=443")
		if err != nil {
			t.Fatalf("search --validate on a fresh home failed: %v\n%s", err, out)
		}
		if !strings.Contains(out, "valid GQL") {
			t.Errorf("search --validate output unexpected:\n%s", out)
		}
		// Compile-only: nothing is provisioned.
		if _, serr := os.Stat(filepath.Join(home, ".gseer", "gseer.db")); serr == nil {
			t.Errorf("search --validate must not create the personal database")
		}
	})
}
