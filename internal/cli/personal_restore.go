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

// personal_restore.go — the personal-mode `gseer db restore` backend.
//
// Guarantee model (honest terms): this is a RECOVERABLE REPLACEMENT,
// never claimed as "atomic".  The sequence:
//
//  1. the backup is probed (real SQLite open) and then fully STAGED
//     in a fresh temporary file inside the live database's own
//     directory — same volume, so the final rename stays a rename —
//     written with O_EXCL (a staging path can never clobber anything),
//     fsynced, closed with its error checked, chmod'ed 0600 after the
//     umask, and validated with a real SQLite open + PRAGMA
//     quick_check;
//  2. only after the staged copy is proven good, the live database
//     AND its -wal/-shm sidecars are moved aside to
//     <db>.restore-old* — the sidecars move WITH the database so a
//     stale WAL can never be crash-recovered against the new file
//     (moving the main file while leaving the WAL in place would be
//     exactly that corruption window);
//  3. the staged file is renamed onto the live path — same-directory
//     rename, atomic per step on POSIX, MoveFileExW with
//     REPLACE_EXISTING|WRITE_THROUGH on Windows (see
//     restorefs_windows.go);
//  4. any failure before or during the swap rolls the recorded
//     renames back in reverse order, and a FAILED ROLLBACK is
//     reported explicitly with the exact location of every artifact —
//     never silently ignored.
//
// Between the live file's rename aside (step 2) and the staged file's
// rename in (step 3) the live path briefly does not exist.  A crash
// in that window loses nothing — the previous database is intact at
// <db>.restore-old — and that is precisely why user-facing text says
// "recoverable replacement", not "atomic".
//
// A previous <db>.restore-old is NEVER discarded: it is archived to a
// unique <db>.restore-old-<timestamp> first and the operator is told
// where it went.
//
// Open-handle lifecycle: this command process never opens the live
// database (only the backup and the staged copy).  A concurrently
// running GSEER process keeps its POSIX file handles working across
// the renames (they follow the inode); on Windows an open live file
// or sidecar makes the rename fail — which aborts the restore cleanly
// with everything rolled back.  Either way the operator is asked to
// run the restore while no other GSEER process is using the database.

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/gseer/internal/storage/sqlite"
)

// Test seams: production always uses the real syscalls and the real
// sqlite opener; the regression tests swap these package-level
// variables to inject deterministic copy/sync/close/rename/sync-dir
// failures without fake filesystems, FUSE mounts, or unreliable
// permission tricks (which behave differently under root).  Mirrors
// the seam pattern of internal/update/apply.go.
var (
	restoreOpenSrc    = func(name string) (io.ReadCloser, error) { return os.Open(name) } //nolint:gosec // G304: the operator-supplied backup path IS the feature
	restoreSyncFile   = func(f *os.File) error { return f.Sync() }
	restoreCloseFile  = func(f *os.File) error { return f.Close() }
	restoreRename     = renameFileDefault
	restoreSyncDir    = syncDirDefault
	restoreSQLiteOpen = sqlite.Open
)

// restoreNoChmodErr reports whether a chmod error is the expected
// no-op on platforms without POSIX mode bits (mirrors the storage
// package's tolerance — the file ACL follows the parent directory
// there).
func restoreNoChmodErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, sub := range []string{"not supported", "not implemented", "ENOSYS", "EWINDOWS"} {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}

// uniqueRestorePath returns a path that does not exist right now:
// base itself when free, otherwise base.1, base.2, …  The O_EXCL in
// copyFile and the rename below stay the real guards against races;
// this loop only picks a sensible candidate (bounded, personal-mode
// scale).
func uniqueRestorePath(base string) (string, error) {
	if _, err := os.Lstat(base); os.IsNotExist(err) {
		return base, nil
	}
	for i := 1; i < 10000; i++ {
		cand := fmt.Sprintf("%s.%d", base, i)
		if _, err := os.Lstat(cand); os.IsNotExist(err) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("cannot find a free aside path next to %s", base)
}

// copyFile copies src to dst, creating dst exclusively with mode and
// returning only after dst is fully written, fsynced, closed AND
// chmod'ed — a nil return means the copy is really on disk with the
// requested permissions.  dst is ALWAYS opened with O_EXCL: this
// helper must never truncate or clobber an existing file (the live
// database is never a copyFile destination — the replacement happens
// exclusively through renames of a fully staged, validated copy).
// Error precedence: the first primary failure (open, copy, sync,
// chmod) wins; the close error surfaces only when nothing earlier
// failed, so cleanup never masks the operation's own error.  A failed
// copy releases the handle even when the close itself failed, and
// removes its own partial dst; a removal that still fails is REPORTED
// together with the primary error, never silently discarded.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := restoreOpenSrc(src) //nolint:gosec // G304: operator-supplied backup/restore path is the feature
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) //nolint:gosec // G304: a fresh staging path inside the database's own directory
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr == nil {
		copyErr = restoreSyncFile(out)
	}
	closeErr := restoreCloseFile(out)
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		// A FAILED close can leave the underlying handle OPEN (the
		// seam in tests simulates it; a real EINTR/EIO close or a
		// failed CloseHandle does it in production).  Removing an
		// open file fails on Windows with a sharing violation, which
		// used to silently leave a partial staging file behind.  The
		// cleanup therefore releases the REAL handle first —
		// out.Close(), deliberately NOT the seam, whose injected
		// failure is exactly what the cleanup must survive — and
		// only then removes the partial destination.  This second
		// close is safe to call on an already-closed *os.File (it
		// returns os.ErrClosed, an expected non-failure).  Its own
		// error carries no recoverable information beyond what the
		// Remove below surfaces: if the handle somehow survived, the
		// Remove fails on Windows and THAT error is reported next to
		// the primary one instead of being silently discarded.
		_ = out.Close()
		if rerr := os.Remove(dst); rerr != nil {
			return fmt.Errorf("%w (cleanup: the partial staging file could not be removed and is still at %s: %v — it can be deleted once no process uses it)", copyErr, dst, rerr)
		}
		return copyErr
	}
	// The umask can only clear bits on the O_CREATE mode — re-assert
	// the 0600 policy explicitly after the data is durable.
	if err := os.Chmod(dst, mode); err != nil && !restoreNoChmodErr(err) {
		_ = os.Remove(dst)
		return fmt.Errorf("cannot set %04o on the staged copy: %w", mode, err)
	}
	return nil
}

// quickCheck runs PRAGMA quick_check and requires the literal "ok".
func quickCheck(db *sql.DB) error {
	var out string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&out); err != nil {
		return err
	}
	if out != "ok" {
		return fmt.Errorf("quick_check reported %q", out)
	}
	return nil
}

// stageValidatedRestoreCopy probes the backup with a real SQLite open,
// stages a full copy of it in a fresh exclusive file next to the live
// database, and validates the STAGED copy (real SQLite open +
// quick_check + the 0600 policy) before anything is replaced.  The
// live database is not touched anywhere in this step; on failure no
// staging file is left behind.
func stageValidatedRestoreCopy(backup, live string) (string, error) {
	probe, err := restoreSQLiteOpen(backup)
	if err != nil {
		return "", fmt.Errorf("E_DB_RESTORE: the backup file is not a valid GSEER database: %w", err)
	}
	_ = probe.DB.Close()

	staging, err := uniqueRestorePath(live + ".restore-staging")
	if err != nil {
		return "", fmt.Errorf("E_DB_RESTORE: %w", err)
	}
	if err := copyFile(backup, staging, 0o600); err != nil {
		return "", fmt.Errorf("E_DB_RESTORE: cannot stage the backup copy (the live database was not touched): %w", err)
	}

	validated, err := restoreSQLiteOpen(staging)
	if err != nil {
		_ = os.Remove(staging)
		return "", fmt.Errorf("E_DB_RESTORE: the staged copy is not a valid database (the live database was not touched): %w", err)
	}
	if qerr := quickCheck(validated.DB); qerr != nil {
		_ = validated.DB.Close()
		_ = os.Remove(staging)
		return "", fmt.Errorf("E_DB_RESTORE: the staged copy failed the integrity check (the live database was not touched): %w", qerr)
	}
	_ = validated.DB.Close()

	// On Windows the POSIX mode bits do not exist (the ACL follows the
	// parent directory — see the storage package's SQ-04 note), so the
	// permissive-mode refusal below is POSIX-only by design.
	if runtime.GOOS != "windows" {
		if st, serr := os.Stat(staging); serr == nil {
			if perm := st.Mode().Perm(); perm&0o177 != 0 {
				_ = os.Remove(staging)
				return "", fmt.Errorf("E_DB_RESTORE: staged copy has permissive mode %04o, refusing to install it (the live database was not touched)", uint32(perm))
			}
		}
	}
	return staging, nil
}

// archivePreviousRecoveryFile never discards a previous <db>.restore-old:
// it renames the old recovery set (main file + sidecars) to a unique
// timestamped name and returns the operator-facing note, or an error.
func archivePreviousRecoveryFile(old string) (string, error) {
	if _, serr := os.Lstat(old); serr != nil {
		return "", nil // nothing to archive
	}
	archived, uerr := uniqueRestorePath(old + "-" + time.Now().UTC().Format("20060102T150405"))
	if uerr != nil {
		return "", fmt.Errorf("E_DB_RESTORE: cannot archive the previous recovery file: %w", uerr)
	}
	if rerr := restoreRename(old, archived); rerr != nil {
		return "", fmt.Errorf("E_DB_RESTORE: cannot set the previous recovery file aside (nothing was replaced): %w", rerr)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		side := old + suffix
		if _, serr := os.Lstat(side); serr == nil {
			_ = restoreRename(side, archived+suffix) // best effort: the main file is what matters
		}
	}
	return fmt.Sprintf("the previous recovery file was preserved as %s (never discarded)", archived), nil
}

// swapStagedRestore performs the recoverable replacement: move the
// live database AND its WAL/SHM sidecars aside, then rename the staged
// copy onto the live path.  Every completed rename is journaled for
// reverse-order rollback; a rollback that itself fails is reported
// explicitly (errors.Join) with the location of every artifact.  On a
// failed replacement the staging file is removed only when the
// rollback succeeded — after a failed rollback every recoverable
// artifact stays on disk.
func swapStagedRestore(staging, live, old string) error {
	type renameStep struct{ from, to string }
	var done []renameStep
	undo := func() error {
		var first error
		for i := len(done) - 1; i >= 0; i-- {
			if rerr := restoreRename(done[i].to, done[i].from); rerr != nil && first == nil {
				first = rerr
			}
		}
		return first
	}
	step := func(from, to string) error {
		if rerr := restoreRename(from, to); rerr != nil {
			return rerr
		}
		done = append(done, renameStep{from, to})
		return nil
	}

	liveExists := true
	if _, serr := os.Stat(live); serr != nil {
		if !os.IsNotExist(serr) {
			return fmt.Errorf("E_DB_RESTORE: cannot stat the live database: %w", serr)
		}
		liveExists = false // first-run edge: the config names a file that does not exist yet
	}

	if liveExists {
		if rerr := step(live, old); rerr != nil {
			if uerr := undo(); uerr != nil {
				return errors.Join(
					fmt.Errorf("E_DB_RESTORE: cannot move the live database aside: %w", rerr),
					fmt.Errorf("E_DB_RESTORE_ROLLBACK: the rollback ALSO failed — data is preserved, but files may be out of place; do not run GSEER until this is resolved: %w", uerr),
				)
			}
			_ = os.Remove(staging)
			return fmt.Errorf("E_DB_RESTORE: cannot move the live database aside (everything is back in place): %w", rerr)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			side := live + suffix
			if _, serr := os.Lstat(side); serr == nil {
				if rerr := step(side, old+suffix); rerr != nil {
					if uerr := undo(); uerr != nil {
						return errors.Join(
							fmt.Errorf("E_DB_RESTORE: cannot move the database sidecar %s aside: %w", side, rerr),
							fmt.Errorf("E_DB_RESTORE_ROLLBACK: the rollback ALSO failed — do not run GSEER; the previous database is at %s, its sidecars may need a manual move back", old),
						)
					}
					_ = os.Remove(staging)
					return fmt.Errorf("E_DB_RESTORE: cannot move the database sidecar %s aside (everything is back in place): %w", side, rerr)
				}
			}
		}
	}

	if rerr := step(staging, live); rerr != nil {
		if uerr := undo(); uerr != nil {
			return errors.Join(
				fmt.Errorf("E_DB_RESTORE: the replacement failed: %w", rerr),
				fmt.Errorf("E_DB_RESTORE_ROLLBACK: the rollback ALSO failed — do not run GSEER; the previous database (with its sidecars) is preserved next to %s under the .restore-old names; move them back manually", live),
			)
		}
		_ = os.Remove(staging)
		return fmt.Errorf("E_DB_RESTORE: the replacement failed (the previous database is back in place): %w", rerr)
	}
	return nil
}

// runPersonalDBRestore replaces the live personal database with the
// backup — staged, validated, recoverable (see the file comment for
// the full guarantee model; it is deliberately NOT documented as
// atomic).
func runPersonalDBRestore(cmd *cobra.Command, backupPath string, yes bool) error {
	cfg, err := loadEngineConfig()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(backupPath)
	if err != nil {
		return err
	}
	if _, serr := os.Stat(abs); serr != nil {
		return fmt.Errorf("E_DB_RESTORE: backup file not found: %w", serr)
	}
	live := filepath.Clean(cfg.Database.DSN)
	if live == filepath.Clean(abs) {
		return fmt.Errorf("E_DB_RESTORE: the backup path is the live database itself — nothing to restore")
	}
	if !yes {
		out := cmd.OutOrStdout()
		printf(out, "This REPLACES the personal database %s with %s (the current database is kept aside).\nProceed? [y/N] ", live, abs)
		var answer string
		if _, err := fmt.Fscanln(cmd.InOrStdin(), &answer); err != nil || (answer != "y" && answer != "yes") {
			println(out, "Restore cancelled.")
			return nil
		}
	}
	out := cmd.OutOrStdout()

	staging, err := stageValidatedRestoreCopy(abs, live)
	if err != nil {
		return err
	}

	old := live + ".restore-old"
	note, err := archivePreviousRecoveryFile(old)
	if err != nil {
		_ = os.Remove(staging)
		return err
	}
	if note != "" {
		printf(out, "[note ] %s\n", note)
	}

	if err := swapStagedRestore(staging, live, old); err != nil {
		return err
	}

	// Crash durability for the swap: fsync the directory on POSIX (a
	// no-op-with-barrier on Windows — see restorefs_windows.go).  The
	// replacement has ALREADY happened here: a failure must be
	// reported, never rolled back.
	if serr := restoreSyncDir(filepath.Dir(live)); serr != nil {
		return fmt.Errorf("E_DB_RESTORE_DIRSYNC: the restore IS in place, but flushing the directory entry to disk failed — after a crash or power loss the previous database file may reappear. Verify with `gseer search` and re-run the restore to make it durable: %w", serr)
	}

	// Done.  The recovery copy stays until the operator removes it.
	printf(out, "[ ok  ] restored %s from %s (recoverable replacement)\n", live, abs)
	if _, serr := os.Stat(old); serr == nil {
		printf(out, "The previous database is kept at %s (remove it when satisfied).\n", old)
		if _, werr := os.Lstat(old + "-wal"); werr == nil {
			printf(out, "Its WAL sidecar is kept at %s (kept together so the recovery set stays complete).\n", old+"-wal")
		}
		printf(out, "If anything looks wrong, recover with:\n  mv %s %s\n", old, live)
	}
	return nil
}
