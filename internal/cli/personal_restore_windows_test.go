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

//go:build windows

package cli

// personal_restore_windows_test.go — Windows-specific restore
// regression.  These pin the platform replacement primitive
// (MoveFileExW with MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH):
// the swap must replace an EXISTING live database file in place, and
// the durability flags must never be dropped silently.  On Windows the
// POSIX 0600 permission assertions do not apply (the ACL follows the
// parent directory — SQ-04 note in the storage package).

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRestoreWindowsReplaceExisting(t *testing.T) {
	dir := t.TempDir()
	live := newEmptyPersonalDB(t, dir, "gseer.db")
	backup := newPopulatedPersonalDB(t, dir, "backup.db")

	withConfigPath(t, writePersonalConfigForTest(t, livePathForTest(live)))
	cmd := &cobra.Command{Use: "restore"}
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	if err := runPersonalDBRestore(cmd, backup, true); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if got := fixtureSearchHits(t, live); got != 1 {
		t.Errorf("search after restore hits = %d, want 1", got)
	}
	if _, serr := os.Stat(live + ".restore-old"); serr != nil {
		t.Errorf("recovery file missing: %v", serr)
	}
}

// livePathForTest keeps the intent explicit: the config DSN is the
// live database path itself.
func livePathForTest(live string) string { return live }

func TestRestoreWindowsMoveFileExFlags(t *testing.T) {
	var gotFlags uint32
	var called bool
	orig := moveFileEx
	moveFileEx = func(from, to *uint16, flags uint32) error {
		called = true
		gotFlags = flags
		return errors.New("injected MoveFileExW failure")
	}
	t.Cleanup(func() { moveFileEx = orig })

	dir := t.TempDir()
	from := filepath.Join(dir, "staging.db")
	to := filepath.Join(dir, "live.db")
	if werr := os.WriteFile(from, []byte("x"), 0o600); werr != nil {
		t.Fatalf("write staging: %v", werr)
	}

	err := renameFileDefault(from, to)
	if err == nil || !strings.Contains(err.Error(), "injected MoveFileExW failure") {
		t.Fatalf("err = %v, want the injected failure through the LinkError wrap", err)
	}
	if !called {
		t.Fatalf("MoveFileExW was not called")
	}
	const replaceExisting = 0x00000001
	const writeThrough = 0x00000008
	if gotFlags != replaceExisting|writeThrough {
		t.Errorf("MoveFileExW flags = %#x, want REPLACE_EXISTING|WRITE_THROUGH (%#x)", gotFlags, replaceExisting|writeThrough)
	}
}

func TestRestoreWindowsSyncDirIsWriteBarrierNoOp(t *testing.T) {
	if err := syncDirDefault("Z:\\does-not-matter"); err != nil {
		t.Errorf("syncDirDefault on Windows must report the write-through barrier (nil), got: %v", err)
	}
}
