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

package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
	"unsafe"
)

// apply_windows_test.go — Windows durability coverage for the updater.
// The replacement primitive is MoveFileExW with
// MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH; the flag IS the
// durability claim, so it is pinned by a capturing seam.  API errors,
// rollback after a failed replacement and the dir-sync warning contract
// are exercised against the real copy/backup logic.  These tests run on
// Windows (the CI update-windows job); `GOOS=windows go test -c`
// compiles them everywhere.  The seams are package-global, so these
// tests stay sequential (no t.Parallel) and restore via t.Cleanup.

// utf16PtrToString decodes a NUL-terminated UTF-16 pointer for the flag
// capture assertions (the seam receives *uint16, the API's LPWSTR).
func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(p), uintptr(n)*unsafe.Sizeof(*p))) != 0 { // #nosec G103 -- test-only decode of a NUL-terminated LPWSTR
		n++
	}
	return string(utf16.Decode(unsafe.Slice(p, n)))
}

func TestWindowsReplacePassesWriteThroughFlags(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gseer")
	writeFileMode(t, target, "old", 0o755)

	var gotFlags uint32
	var gotFrom, gotTo string
	orig := moveFileEx
	moveFileEx = func(from, to *uint16, flags uint32) error {
		gotFlags, gotFrom, gotTo = flags, utf16PtrToString(from), utf16PtrToString(to)
		// Perform the real move so the rest of replaceBinary runs
		// honestly; remove the target first because this seam moves via
		// os.Rename (MoveFileW) instead of the replacing API.
		_ = os.Remove(utf16PtrToString(to))
		return os.Rename(utf16PtrToString(from), utf16PtrToString(to))
	}
	t.Cleanup(func() { moveFileEx = orig })

	if err := replaceBinary(target, []byte("new")); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	if want := uint32(movefileReplaceExisting | movefileWriteThrough); gotFlags != want {
		t.Errorf("MoveFileExW flags = %#x, want %#x (REPLACE_EXISTING|WRITE_THROUGH) — the flag is the durability claim", gotFlags, want)
	}
	if gotTo != target {
		t.Errorf("MoveFileExW target = %q, want %q", gotTo, target)
	}
	if !strings.HasPrefix(gotFrom, dir+string(os.PathSeparator)+".gseer-update-") {
		t.Errorf("MoveFileExW source = %q, want a .gseer-update-* temp in the target's own directory", gotFrom)
	}
	if got, rerr := os.ReadFile(target); rerr != nil || string(got) != "new" {
		t.Errorf("target = %q (%v), want the new payload", got, rerr)
	}
	if bak, rerr := os.ReadFile(target + ".bak"); rerr != nil || string(bak) != "old" {
		t.Errorf("backup = %q (%v), want the old binary", bak, rerr)
	}
}

func TestWindowsReplaceWithExistingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gseer")
	writeFileMode(t, target, "old-binary", 0o751)

	// Real MoveFileExW with the write-through flag — the actual API call
	// the updater makes in production.
	if err := replaceBinary(target, []byte("new-binary")); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	if got, rerr := os.ReadFile(target); rerr != nil || string(got) != "new-binary" {
		t.Fatalf("target = %q (%v), want the new payload", got, rerr)
	}
	if bak, rerr := os.ReadFile(target + ".bak"); rerr != nil || string(bak) != "old-binary" {
		t.Fatalf("backup = %q (%v), want the old binary", bak, rerr)
	}
	assertPerm(t, target, 0o751)
	assertNoTempFiles(t, dir)
}

func TestWindowsReplaceWithoutPriorTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gseer")

	if err := replaceBinary(target, []byte("fresh")); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	if got, rerr := os.ReadFile(target); rerr != nil || string(got) != "fresh" {
		t.Fatalf("target = %q (%v), want the payload", got, rerr)
	}
	if _, err := os.Stat(target + ".bak"); !os.IsNotExist(err) {
		t.Errorf("no backup should exist without a prior binary: %v", err)
	}
	assertNoTempFiles(t, dir)
}

func TestWindowsReplaceErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gseer")
	writeFileMode(t, target, "old-binary", 0o755)

	apiErr := errors.New("injected MoveFileExW failure")
	orig := moveFileEx
	moveFileEx = func(*uint16, *uint16, uint32) error { return apiErr }
	t.Cleanup(func() { moveFileEx = orig })

	err := replaceBinary(target, []byte("new-binary"))
	if err == nil || !strings.Contains(err.Error(), "E_UPDATE_REPLACE") {
		t.Fatalf("error = %v, want E_UPDATE_REPLACE", err)
	}
	var le *os.LinkError
	if !errors.As(err, &le) || le.Err != apiErr {
		t.Errorf("underlying error = %v, want the wrapped *os.LinkError carrying the API error", err)
	}
	// The target was never touched (the API call failed before any move).
	if got, rerr := os.ReadFile(target); rerr != nil || string(got) != "old-binary" {
		t.Errorf("target unexpectedly modified: %q (%v)", got, rerr)
	}
	assertNoTempFiles(t, dir)
}

func TestWindowsRollbackOnFailedReplace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gseer")
	writeFileMode(t, target, "old-binary", 0o755)

	orig := moveFileEx
	moveFileEx = func(*uint16, *uint16, uint32) error { return errors.New("injected MoveFileExW failure") }
	t.Cleanup(func() { moveFileEx = orig })

	err := replaceBinary(target, []byte("new-binary"))
	if err == nil {
		t.Fatal("the failed replacement must be reported")
	}
	// Rollback restored the original binary from the backup, and the
	// backup itself survived for manual recovery.
	if got, rerr := os.ReadFile(target); rerr != nil || string(got) != "old-binary" {
		t.Fatalf("target not rolled back: %q (%v)", got, rerr)
	}
	if bak, rerr := os.ReadFile(target + ".bak"); rerr != nil || string(bak) != "old-binary" {
		t.Fatalf("backup damaged: %q (%v)", bak, rerr)
	}
	assertNoTempFiles(t, dir)
}

func TestWindowsDirSyncWarningPropagates(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gseer")
	writeFileMode(t, target, "old-binary", 0o755)

	// On Windows syncDirDefault can only return nil (the barrier already
	// happened inside the write-through replacement), but the seam must
	// stay honored: an injected failure still surfaces as the
	// E_UPDATE_DIRSYNC warning with the binary replaced — never silently.
	injected := errors.New("injected dir-sync failure")
	orig := syncDir
	syncDir = func(string) error { return injected }
	t.Cleanup(func() { syncDir = orig })

	err := replaceBinary(target, []byte("new-binary"))
	if err == nil || !IsDirSyncWarning(err) {
		t.Fatalf("error = %v, want an E_UPDATE_DIRSYNC warning", err)
	}
	if !strings.Contains(err.Error(), "injected dir-sync failure") {
		t.Errorf("underlying error not surfaced: %v", err)
	}
	if got, rerr := os.ReadFile(target); rerr != nil || string(got) != "new-binary" {
		t.Errorf("target not replaced despite the warning: %q (%v)", got, rerr)
	}
}

func TestWindowsRenameDefaultInvalidPath(t *testing.T) {
	// A NUL byte cannot be converted to LPWSTR — the primitive must
	// return the *os.LinkError error shape instead of panicking.
	err := renameFileDefault("bad\x00path", filepath.Join(t.TempDir(), "out"))
	var le *os.LinkError
	if !errors.As(err, &le) {
		t.Fatalf("error = %v (%T), want *os.LinkError", err, err)
	}
}
