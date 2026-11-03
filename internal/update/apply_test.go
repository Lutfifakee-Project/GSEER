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

package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// apply_test.go — durability regression tests for the atomic replace:
// backup copy (success / failure / permission preservation), copy /
// sync / close error precedence, atomic replacement, rollback on rename
// failure, and the directory-sync warning contract (replaced ≠ rolled
// back).
//
// Failure injection uses only the package's one-line test seams
// (syncFile, closeFile, renameFile, syncDir) plus real filesystem
// effects (a directory as copy source fails io.Copy) — no fake
// filesystem abstractions.  The seams are package-global, so these
// tests must stay sequential (no t.Parallel) and restore via t.Cleanup.

func writeFileMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// chmod makes the mode exact even when the file already existed.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// assertPerm checks path's permission bits with platform-honest
// expectations.  On Unix the updater must preserve the exact POSIX
// mode.  On Windows os.Chmod can only toggle the single owner-write
// bit (mapped to the read-only attribute — Go reports 0666 for a
// writable file and 0444 for a read-only one); full POSIX modes are
// not representable there, so the Windows check verifies exactly
// that semantic and nothing more: a requested mode WITH owner-write
// must end up writable, a mode WITHOUT it must end up read-only.
// The Unix contract stays exact; the Windows limitation is
// documented, not silently ignored.
func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		wantWritable := want&0o200 != 0
		gotWritable := st.Mode().Perm()&0o200 != 0
		if gotWritable != wantWritable {
			t.Errorf("%s: writable = %v (perm %v), want writable = %v (requested %v) — Windows preserves only the read-only bit", path, gotWritable, st.Mode().Perm(), wantWritable, want)
		}
		return
	}
	if st.Mode().Perm() != want {
		t.Errorf("%s: mode = %v, want %v", path, st.Mode().Perm(), want)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".gseer-update-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestCopyFileSuccessAndModePreservation(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	// A mode with umask-strippable bits (group/other write) proves the
	// explicit chmod took effect, not just OpenFile's creation argument.
	writeFileMode(t, src, "binary-bytes", 0o776)
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "binary-bytes" {
		t.Fatalf("dst content = %q (%v), want the payload", got, err)
	}
	assertPerm(t, dst, 0o776)

	// Re-copy over an existing destination with a stale mode: the source
	// mode must win (the re-backup-over-old-.bak path).
	writeFileMode(t, dst, "stale", 0o600)
	writeFileMode(t, src, "binary-bytes", 0o755)
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("re-copy: %v", err)
	}
	assertPerm(t, dst, 0o755)
}

func TestCopyFileCopyFailure(t *testing.T) {
	dir := t.TempDir()
	// A directory as the copy source: os.Open succeeds, reading fails —
	// a real I/O failure at the io.Copy step, no injection needed.
	src := filepath.Join(dir, "src-dir")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")

	if err := copyFile(src, dst); err == nil {
		t.Fatal("copying from a directory must fail")
	}

	// A cleanup (Close) error must never overwrite the copy error.
	// The stub still REALLY closes the handle before reporting the
	// injected failure — a leaked handle would make t.TempDir cleanup
	// fail on Windows ("file in use by another process").
	origClose := closeFile
	closeFile = func(f *os.File) error {
		if rerr := f.Close(); rerr != nil {
			t.Logf("real close error (superseded by the injected one): %v", rerr)
		}
		return errors.New("injected close failure")
	}
	t.Cleanup(func() { closeFile = origClose })

	err := copyFile(src, dst)
	if err == nil {
		t.Fatal("copy failure swallowed")
	}
	if strings.Contains(err.Error(), "injected close failure") {
		t.Fatalf("cleanup error overwrote the primary copy error: %v", err)
	}
}

func TestCopyFileSyncFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeFileMode(t, src, "payload", 0o755)
	dst := filepath.Join(dir, "dst")

	injected := errors.New("injected fsync failure")
	origSync := syncFile
	syncFile = func(*os.File) error { return injected }
	t.Cleanup(func() { syncFile = origSync })

	if err := copyFile(src, dst); err != injected {
		t.Fatalf("sync error = %v, want the injected fsync failure", err)
	}
	// The bytes WERE written; only the durability barrier failed.
	if got, err := os.ReadFile(dst); err != nil || string(got) != "payload" {
		t.Fatalf("dst content = %q (%v), want the payload", got, err)
	}

	// A sync failure still takes precedence over a later Close failure.
	// The stub really closes the handle first (no leaked handles).
	origClose := closeFile
	closeFile = func(f *os.File) error {
		if rerr := f.Close(); rerr != nil {
			t.Logf("real close error (superseded by the injected one): %v", rerr)
		}
		return errors.New("injected close failure")
	}
	t.Cleanup(func() { closeFile = origClose })
	if err := copyFile(src, dst); err != injected {
		t.Fatalf("sync error masked by close error: %v", err)
	}
}

func TestCopyFileCloseFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeFileMode(t, src, "payload", 0o755)

	injected := errors.New("injected close failure")
	origClose := closeFile
	t.Cleanup(func() { closeFile = origClose })

	// Output close failure surfaces when no earlier error occurred.
	// The injected failure is reported AFTER the handle is really
	// released (t.TempDir cleanup must succeed on Windows).
	closeFile = func(f *os.File) error {
		if f.Name() == filepath.Join(dir, "dst") {
			if rerr := f.Close(); rerr != nil {
				t.Logf("real close error (superseded by the injected one): %v", rerr)
			}
			return injected
		}
		return f.Close()
	}
	if err := copyFile(src, filepath.Join(dir, "dst")); err != injected {
		t.Fatalf("output close error = %v, want the injected failure", err)
	}

	// Input close failure also surfaces when nothing earlier failed.
	closeFile = func(f *os.File) error {
		if f.Name() == src {
			if rerr := f.Close(); rerr != nil {
				t.Logf("real close error (superseded by the injected one): %v", rerr)
			}
			return injected
		}
		return f.Close()
	}
	if err := copyFile(src, filepath.Join(dir, "dst2")); err != injected {
		t.Fatalf("input close error = %v, want the injected failure", err)
	}
}

func TestReplaceBinaryAtomicReplaceAndBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gseer")
	writeFileMode(t, target, "old-binary", 0o751)

	if err := replaceBinary(target, []byte("new-binary")); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "new-binary" {
		t.Fatalf("target content = %q (%v), want the new payload", got, err)
	}
	if bak, err := os.ReadFile(target + ".bak"); err != nil || string(bak) != "old-binary" {
		t.Fatalf("backup content = %q (%v), want the old binary", bak, err)
	}
	// Mode preserved on BOTH the replaced binary and its backup.
	assertPerm(t, target, 0o751)
	assertPerm(t, target+".bak", 0o751)
	assertNoTempFiles(t, dir)
}

func TestReplaceBinaryNoPriorFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gseer")

	if err := replaceBinary(target, []byte("fresh")); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	if _, err := os.Stat(target + ".bak"); !os.IsNotExist(err) {
		t.Errorf("no backup should exist without a prior binary: %v", err)
	}
	assertPerm(t, target, 0o755)
	assertNoTempFiles(t, dir)
}

func TestReplaceBinaryRollbackOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gseer")
	writeFileMode(t, target, "old-binary", 0o755)

	injected := errors.New("injected rename failure")
	origRename := renameFile
	renameFile = func(string, string) error { return injected }
	t.Cleanup(func() { renameFile = origRename })

	err := replaceBinary(target, []byte("new-binary"))
	if err == nil || !strings.Contains(err.Error(), "E_UPDATE_REPLACE") {
		t.Fatalf("rename failure = %v, want E_UPDATE_REPLACE", err)
	}
	if !strings.Contains(err.Error(), "injected rename failure") {
		t.Errorf("underlying rename error not surfaced: %v", err)
	}
	// The live error chain: errors.Is reaches the injected cause
	// through *Error.Unwrap (the same chain errors.As walks for
	// *os.LinkError on Windows).
	if !errors.Is(err, injected) {
		t.Errorf("underlying rename error not chainable via errors.Is: %v", err)
	}
	// The original binary was restored from the backup, and the backup
	// itself survived for a later manual recovery.
	if got, rerr := os.ReadFile(target); rerr != nil || string(got) != "old-binary" {
		t.Fatalf("target not rolled back: %q (%v)", got, rerr)
	}
	if bak, rerr := os.ReadFile(target + ".bak"); rerr != nil || string(bak) != "old-binary" {
		t.Fatalf("backup damaged: %q (%v)", bak, rerr)
	}
	assertNoTempFiles(t, dir)
}

func TestReplaceBinaryRollbackFailureReported(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gseer")
	writeFileMode(t, target, "old-binary", 0o755)

	injected := errors.New("injected rename failure")
	origRename := renameFile
	// Worst case: the rename "fails" AND destroys the backup.  The error
	// must say the rollback failed instead of claiming a clean restore.
	renameFile = func(oldPath, newPath string) error {
		_ = os.Remove(newPath + ".bak") // newPath is the target; destroy its backup
		return injected
	}
	t.Cleanup(func() { renameFile = origRename })

	err := replaceBinary(target, []byte("new-binary"))
	if err == nil || !errors.Is(err, injected) {
		t.Fatalf("error = %v, want the wrapped injected rename failure", err)
	}
	if !strings.Contains(err.Error(), "rollback failed") {
		t.Errorf("error must state the rollback failed: %v", err)
	}
	// The target was never replaced (the rename failed before that).
	if got, rerr := os.ReadFile(target); rerr != nil || string(got) != "old-binary" {
		t.Fatalf("target unexpectedly modified: %q (%v)", got, rerr)
	}
}

func TestReplaceBinaryDirSyncFailureWarnsButReplaced(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gseer")
	writeFileMode(t, target, "old-binary", 0o755)

	injected := errors.New("injected directory fsync failure")
	origSyncDir := syncDir
	syncDir = func(string) error { return injected }
	t.Cleanup(func() { syncDir = origSyncDir })

	err := replaceBinary(target, []byte("new-binary"))
	if err == nil {
		t.Fatal("a directory-sync failure must not be silently swallowed")
	}
	if !IsDirSyncWarning(err) {
		t.Fatalf("dir-sync failure = %v, want an E_UPDATE_DIRSYNC warning", err)
	}
	if !strings.Contains(err.Error(), "injected directory fsync failure") {
		t.Errorf("underlying fsync error not surfaced: %v", err)
	}
	// The binary WAS replaced — the warning must not imply a rollback.
	if got, rerr := os.ReadFile(target); rerr != nil || string(got) != "new-binary" {
		t.Fatalf("target not replaced despite the warning: %q (%v)", got, rerr)
	}
	if bak, rerr := os.ReadFile(target + ".bak"); rerr != nil || string(bak) != "old-binary" {
		t.Fatalf("backup lost: %q (%v)", bak, rerr)
	}
	assertNoTempFiles(t, dir)
}

func TestApplyDirSyncWarningKeepsResult(t *testing.T) {
	pub, priv := testKeypair(t)
	SetTestPublicKey(pub)
	t.Cleanup(func() { SetTestPublicKey(nil) })

	payload := []byte("upgraded-binary")
	art := Artifact{
		OS: goos, Arch: goarch,
		URL:    "https://update-channel.test/a",
		SHA256: sha256Hex(payload),
		Size:   int64(len(payload)),
	}
	manifestBytes := signedManifest(t, priv, "v9.9.9", []Artifact{art})
	target := filepath.Join(t.TempDir(), "gseer")
	_ = os.WriteFile(target, []byte("old"), 0o755)
	fetcher := &Fetcher{Client: fakeHTTPClient(t, map[string]string{art.URL: string(payload)})}

	injected := errors.New("injected directory fsync failure")
	origSyncDir := syncDir
	syncDir = func(string) error { return injected }
	t.Cleanup(func() { syncDir = origSyncDir })

	res, err := Apply(context.Background(), manifestBytes, nil, "v0.1.0", target, false, fetcher)
	if err == nil || !IsDirSyncWarning(err) {
		t.Fatalf("apply error = %v, want a dir-sync warning", err)
	}
	if res == nil {
		t.Fatal("the apply result must accompany the dir-sync warning (the replacement happened)")
	}
	if res.NewVersion != "v9.9.9" || res.Replaced != target {
		t.Errorf("result = %+v", res)
	}
	if got, rerr := os.ReadFile(target); rerr != nil || string(got) != string(payload) {
		t.Errorf("target content = %q (%v), want the new payload", got, rerr)
	}
}
