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

// personal_restore_test.go — regression battery for the hardened
// personal-mode `db restore` (recoverable replacement).  Failure
// injection is deterministic through the package's I/O seams (see
// personal_restore.go) — no fake filesystems, no permission tricks
// that misbehave under root.  These tests mutate package-level seam
// variables, so they must never run in parallel.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/gseer/internal/fixture"
	"github.com/Lutfifakee-Project/gseer/internal/search"
	"github.com/Lutfifakee-Project/gseer/internal/searchlite"
	"github.com/Lutfifakee-Project/gseer/internal/storage/sqlite"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// runRestore drives runPersonalDBRestore the way the cobra command
// does: a personal config at flags.configPath, a captured output
// buffer, --yes consent.  Returns the output and the error.
func runRestore(t *testing.T, livePath, backupPath string) (string, error) {
	t.Helper()
	withConfigPath(t, writePersonalConfigForTest(t, livePath))
	cmd := &cobra.Command{Use: "restore"}
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	err := runPersonalDBRestore(cmd, backupPath, true)
	return out.String(), err
}

// newPopulatedPersonalDB provisions a migrated personal database and
// loads the labeled synthetic fixture corpus — the content a restore
// must bring back searchably.
func newPopulatedPersonalDB(t *testing.T, dir, name string) string {
	t.Helper()
	dbPath := filepath.Join(dir, name)
	res, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	if _, err := sqlite.Migrate(context.Background(), res.DB); err != nil {
		_ = res.DB.Close()
		t.Fatalf("migrate %s: %v", name, err)
	}
	if _, err := fixture.Load(context.Background(), sqlite.NewStore(res.DB)); err != nil {
		_ = res.DB.Close()
		t.Fatalf("load fixtures into %s: %v", name, err)
	}
	if err := res.DB.Close(); err != nil {
		t.Fatalf("close %s: %v", name, err)
	}
	return dbPath
}

// newEmptyPersonalDB provisions a migrated database with an EMPTY
// index (no fixtures) — the "old" live state a restore replaces.
func newEmptyPersonalDB(t *testing.T, dir, name string) string {
	t.Helper()
	dbPath := filepath.Join(dir, name)
	res, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	if _, err := sqlite.Migrate(context.Background(), res.DB); err != nil {
		_ = res.DB.Close()
		t.Fatalf("migrate %s: %v", name, err)
	}
	if err := res.DB.Close(); err != nil {
		t.Fatalf("close %s: %v", name, err)
	}
	return dbPath
}

// fixtureSearchHits runs the real GQL engine against the database at
// dbPath and returns how many rows the fixture query matches.
func fixtureSearchHits(t *testing.T, dbPath string) int {
	t.Helper()
	res, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open for search: %v", err)
	}
	defer func() { _ = res.DB.Close() }()
	out, err := searchlite.New(res.DB).Search(context.Background(), search.Query{
		GQL: `port==443 && title CONTAINS "login"`,
	})
	if err != nil {
		t.Fatalf("search after restore: %v", err)
	}
	return len(out.Rows)
}

func fileHash(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func assertNoStagingLeft(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			continue
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), ".restore-staging") {
				t.Errorf("staging leftover in %s: %s", dir, e.Name())
			}
		}
	}
}

// errReadCloser fails the copy after n delivered bytes — a
// deterministic mid-stream copy failure (disk-full / EIO analogue).
type errReadCloser struct {
	r io.Reader
	n int
}

func (e *errReadCloser) Read(p []byte) (int, error) {
	if e.n <= 0 {
		return 0, errors.New("injected read failure")
	}
	if len(p) > e.n {
		p = p[:e.n]
	}
	n, _ := e.r.Read(p)
	e.n -= n
	if e.n <= 0 {
		return n, errors.New("injected read failure")
	}
	return n, nil
}

func (e *errReadCloser) Close() error { return nil }

// ---------------------------------------------------------------------------
// 1. success — restore and REAL search after restore
// ---------------------------------------------------------------------------

func TestRestoreSuccessAndSearchAfterRestore(t *testing.T) {
	dir := t.TempDir()
	live := newEmptyPersonalDB(t, dir, "gseer.db")
	backup := newPopulatedPersonalDB(t, dir, "backup.db")

	if got := fixtureSearchHits(t, live); got != 0 {
		t.Fatalf("pre-restore search hits = %d, want 0 (empty index)", got)
	}

	out, err := runRestore(t, live, backup)
	if err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	for _, want := range []string{"restored", "recoverable replacement", "previous database is kept at"} {
		if !strings.Contains(out, want) {
			t.Errorf("restore output missing %q:\n%s", want, out)
		}
	}

	// The restored database is searchable through the real engine,
	// and carries the BACKUP's data, not the old live state.
	if got := fixtureSearchHits(t, live); got != 1 {
		t.Errorf("search after restore hits = %d, want 1", got)
	}

	// The previous live database is preserved aside.
	if _, serr := os.Stat(live + ".restore-old"); serr != nil {
		t.Errorf("recovery file missing: %v", serr)
	}
	// The restored file carries the restrictive permission policy.
	if runtime.GOOS != "windows" {
		if st, serr := os.Stat(live); serr != nil {
			t.Errorf("stat live: %v", serr)
		} else if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("restored live mode = %04o, want 0600", perm)
		}
	}
	assertNoStagingLeft(t, dir)
}

// ---------------------------------------------------------------------------
// 2/3. missing backup, invalid backup
// ---------------------------------------------------------------------------

func TestRestoreMissingBackupFile(t *testing.T) {
	dir := t.TempDir()
	live := newEmptyPersonalDB(t, dir, "gseer.db")
	before := fileHash(t, live)

	_, err := runRestore(t, live, filepath.Join(dir, "does-not-exist.db"))
	if err == nil || !strings.Contains(err.Error(), "E_DB_RESTORE") || !strings.Contains(err.Error(), "backup file not found") {
		t.Fatalf("error = %v, want E_DB_RESTORE backup file not found", err)
	}
	if after := fileHash(t, live); after != before {
		t.Errorf("live database changed after a refused restore")
	}
	assertNoStagingLeft(t, dir)
}

func TestRestoreInvalidBackupRejected(t *testing.T) {
	t.Run("not a database at all", func(t *testing.T) {
		dir := t.TempDir()
		live := newEmptyPersonalDB(t, dir, "gseer.db")
		backup := filepath.Join(dir, "backup.db")
		if werr := os.WriteFile(backup, []byte("this is definitely not a SQLite database"), 0o600); werr != nil {
			t.Fatalf("write garbage backup: %v", werr)
		}
		before := fileHash(t, live)

		_, err := runRestore(t, live, backup)
		if err == nil || !strings.Contains(err.Error(), "E_DB_RESTORE") {
			t.Fatalf("error = %v, want E_DB_RESTORE refusal", err)
		}
		if after := fileHash(t, live); after != before {
			t.Errorf("live database was touched by a refused restore")
		}
		assertNoStagingLeft(t, dir)
	})

	t.Run("truncated real database fails integrity check", func(t *testing.T) {
		dir := t.TempDir()
		live := newEmptyPersonalDB(t, dir, "gseer.db")
		full := newPopulatedPersonalDB(t, dir, "full.db")
		raw, rerr := os.ReadFile(full)
		if rerr != nil {
			t.Fatalf("read full db: %v", rerr)
		}
		if len(raw) < 8192 {
			t.Fatalf("fixture db unexpectedly small (%d bytes); cannot truncate", len(raw))
		}
		backup := filepath.Join(dir, "truncated.db")
		if werr := os.WriteFile(backup, raw[:len(raw)/2], 0o600); werr != nil {
			t.Fatalf("write truncated backup: %v", werr)
		}
		before := fileHash(t, live)

		_, err := runRestore(t, live, backup)
		if err == nil || !strings.Contains(err.Error(), "E_DB_RESTORE") {
			t.Fatalf("error = %v, want E_DB_RESTORE refusal", err)
		}
		if !strings.Contains(err.Error(), "integrity check") && !strings.Contains(err.Error(), "not a valid") {
			t.Errorf("error should name the integrity/validation stage, got: %v", err)
		}
		if after := fileHash(t, live); after != before {
			t.Errorf("live database was touched by a refused restore")
		}
		assertNoStagingLeft(t, dir)
	})
}

// ---------------------------------------------------------------------------
// 4/5/6. copy failure, sync failure, close failure (injected)
// ---------------------------------------------------------------------------

func TestRestoreCopyFailureInjection(t *testing.T) {
	t.Run("source open fails", func(t *testing.T) {
		dir := t.TempDir()
		live := newEmptyPersonalDB(t, dir, "gseer.db")
		backup := newPopulatedPersonalDB(t, dir, "backup.db")
		before := fileHash(t, live)

		orig := restoreOpenSrc
		restoreOpenSrc = func(string) (io.ReadCloser, error) { return nil, errors.New("injected open failure") }
		t.Cleanup(func() { restoreOpenSrc = orig })

		_, err := runRestore(t, live, backup)
		if err == nil || !strings.Contains(err.Error(), "cannot stage the backup copy") {
			t.Fatalf("error = %v, want a staging failure", err)
		}
		if !strings.Contains(err.Error(), "the live database was not touched") {
			t.Errorf("error must state the live database was untouched, got: %v", err)
		}
		if after := fileHash(t, live); after != before {
			t.Errorf("live database changed on a staging failure")
		}
		assertNoStagingLeft(t, dir)
	})

	t.Run("mid-copy read failure", func(t *testing.T) {
		dir := t.TempDir()
		live := newEmptyPersonalDB(t, dir, "gseer.db")
		backup := newPopulatedPersonalDB(t, dir, "backup.db")
		before := fileHash(t, live)
		raw, rerr := os.ReadFile(backup)
		if rerr != nil {
			t.Fatalf("read backup: %v", rerr)
		}

		orig := restoreOpenSrc
		restoreOpenSrc = func(string) (io.ReadCloser, error) {
			return &errReadCloser{r: bytes.NewReader(raw), n: 128}, nil
		}
		t.Cleanup(func() { restoreOpenSrc = orig })

		_, err := runRestore(t, live, backup)
		if err == nil || !strings.Contains(err.Error(), "cannot stage the backup copy") {
			t.Fatalf("error = %v, want a staging failure", err)
		}
		if after := fileHash(t, live); after != before {
			t.Errorf("live database changed on a staging failure")
		}
		assertNoStagingLeft(t, dir) // the partial copy must be cleaned up
	})
}

func TestRestoreSyncFailureInjection(t *testing.T) {
	dir := t.TempDir()
	live := newEmptyPersonalDB(t, dir, "gseer.db")
	backup := newPopulatedPersonalDB(t, dir, "backup.db")
	before := fileHash(t, live)

	orig := restoreSyncFile
	restoreSyncFile = func(*os.File) error { return errors.New("injected fsync failure") }
	t.Cleanup(func() { restoreSyncFile = orig })

	_, err := runRestore(t, live, backup)
	if err == nil || !strings.Contains(err.Error(), "cannot stage the backup copy") {
		t.Fatalf("error = %v, want a staging failure naming the fsync path", err)
	}
	if after := fileHash(t, live); after != before {
		t.Errorf("live database changed on a staging failure")
	}
	assertNoStagingLeft(t, dir)
}

func TestRestoreCloseFailureInjection(t *testing.T) {
	dir := t.TempDir()
	live := newEmptyPersonalDB(t, dir, "gseer.db")
	backup := newPopulatedPersonalDB(t, dir, "backup.db")
	before := fileHash(t, live)

	orig := restoreCloseFile
	restoreCloseFile = func(*os.File) error { return errors.New("injected close failure") }
	t.Cleanup(func() { restoreCloseFile = orig })

	_, err := runRestore(t, live, backup)
	if err == nil || !strings.Contains(err.Error(), "cannot stage the backup copy") {
		t.Fatalf("error = %v, want a staging failure naming the close path", err)
	}
	if after := fileHash(t, live); after != before {
		t.Errorf("live database changed on a staging failure")
	}
	assertNoStagingLeft(t, dir)
}

// ---------------------------------------------------------------------------
// 7/8. replacement failure and rollback failure (injected renames)
// ---------------------------------------------------------------------------

func TestRestoreReplacementFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	live := newEmptyPersonalDB(t, dir, "gseer.db")
	backup := newPopulatedPersonalDB(t, dir, "backup.db")
	before := fileHash(t, live)

	orig := restoreRename
	restoreRename = func(from, to string) error {
		if strings.Contains(from, ".restore-staging") {
			return errors.New("injected replacement failure")
		}
		return renameFileDefault(from, to)
	}
	t.Cleanup(func() { restoreRename = orig })

	out, err := runRestore(t, live, backup)
	if err == nil || !strings.Contains(err.Error(), "the replacement failed") {
		t.Fatalf("error = %v, want a replacement failure", err)
	}
	if !strings.Contains(err.Error(), "the previous database is back in place") {
		t.Errorf("error must state the rollback landed, got: %v", err)
	}
	if after := fileHash(t, live); after != before {
		t.Errorf("live database did not survive the replacement failure byte-for-byte")
	}
	if _, serr := os.Stat(live + ".restore-old"); !os.IsNotExist(serr) {
		t.Errorf("recovery file should be rolled back, stat err = %v", serr)
	}
	assertNoStagingLeft(t, dir)
	_ = out
}

func TestRestoreRollbackFailureReportedExplicitly(t *testing.T) {
	dir := t.TempDir()
	live := newEmptyPersonalDB(t, dir, "gseer.db")
	backup := newPopulatedPersonalDB(t, dir, "backup.db")
	before := fileHash(t, live) // the bytes that must still exist at .restore-old

	orig := restoreRename
	restoreRename = func(from, to string) error {
		if strings.Contains(from, ".restore-staging") {
			return errors.New("injected replacement failure")
		}
		if from == live+".restore-old" && to == live {
			return errors.New("injected rollback failure")
		}
		return renameFileDefault(from, to)
	}
	t.Cleanup(func() { restoreRename = orig })

	_, err := runRestore(t, live, backup)
	if err == nil {
		t.Fatalf("restore must fail, got nil")
	}
	if !strings.Contains(err.Error(), "E_DB_RESTORE_ROLLBACK") {
		t.Errorf("rollback failure must be reported explicitly, got: %v", err)
	}
	// The recoverable data must still exist — under the aside name.
	if _, serr := os.Stat(live + ".restore-old"); serr != nil {
		t.Fatalf("previous database missing from the recovery location: %v", serr)
	}
	if got := fileHash(t, live+".restore-old"); got != before {
		t.Errorf("recovery file content differs from the original live database")
	}
	if _, serr := os.Stat(live); !os.IsNotExist(serr) {
		t.Errorf("live path should be empty after a failed replacement + failed rollback, stat err = %v", serr)
	}
}

// ---------------------------------------------------------------------------
// 9. an existing .restore-old is archived, never discarded
// ---------------------------------------------------------------------------

func TestRestorePreservesPreviousRecoveryFile(t *testing.T) {
	dir := t.TempDir()
	live := newEmptyPersonalDB(t, dir, "gseer.db")
	backup := newPopulatedPersonalDB(t, dir, "backup.db")
	liveBefore := fileHash(t, live)

	// A leftover recovery set from an earlier restore attempt.
	old := live + ".restore-old"
	previousRecovery := []byte("PREVIOUS-RECOVERY-SET — must survive this restore")
	if werr := os.WriteFile(old, previousRecovery, 0o600); werr != nil {
		t.Fatalf("write previous recovery file: %v", werr)
	}
	if werr := os.WriteFile(old+"-wal", []byte("stale-wal"), 0o600); werr != nil {
		t.Fatalf("write previous recovery wal: %v", werr)
	}

	out, err := runRestore(t, live, backup)
	if err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if !strings.Contains(out, "preserved as") {
		t.Errorf("output must tell the operator where the old recovery file went:\n%s", out)
	}

	// The injected recovery set is gone from the aside names…
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatalf("readdir: %v", rerr)
	}
	archived, archivedWal := false, false
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, filepath.Base(live)+".restore-old-") {
			b, rerr2 := os.ReadFile(filepath.Join(dir, name))
			if rerr2 != nil {
				t.Fatalf("read archive %s: %v", name, rerr2)
			}
			switch {
			case strings.HasSuffix(name, "-wal"):
				archivedWal = string(b) == "stale-wal"
			default:
				archived = string(b) == string(previousRecovery)
			}
		}
	}
	if !archived {
		t.Errorf("the previous recovery file was not preserved with its content")
	}
	if !archivedWal {
		t.Errorf("the previous recovery WAL sidecar was not preserved with its content")
	}
	// …and the aside name now holds the PREVIOUS LIVE database.
	if got := fileHash(t, live+".restore-old"); got != liveBefore {
		t.Errorf(".restore-old should hold the pre-restore live database")
	}
}

// ---------------------------------------------------------------------------
// 10. backup and live database in different directories
// ---------------------------------------------------------------------------

func TestRestoreDifferentDirectories(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	live := newEmptyPersonalDB(t, dirA, "gseer.db")
	backup := newPopulatedPersonalDB(t, dirB, "backup.db")

	out, err := runRestore(t, live, backup)
	if err != nil {
		t.Fatalf("restore across directories: %v\n%s", err, out)
	}
	if got := fixtureSearchHits(t, live); got != 1 {
		t.Errorf("search after cross-directory restore hits = %d, want 1", got)
	}
	assertNoStagingLeft(t, dirA, dirB) // staging belongs next to the LIVE db, never next to the backup
}

// ---------------------------------------------------------------------------
// 11. first-run edge: the live file does not exist yet
// ---------------------------------------------------------------------------

func TestRestoreWhenLiveDatabaseMissing(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "gseer.db") // never created
	backup := newPopulatedPersonalDB(t, dir, "backup.db")

	out, err := runRestore(t, live, backup)
	if err != nil {
		t.Fatalf("restore onto a missing live path: %v\n%s", err, out)
	}
	if strings.Contains(out, "previous database is kept at") {
		t.Errorf("there is no previous database — the output must not claim one:\n%s", out)
	}
	if got := fixtureSearchHits(t, live); got != 1 {
		t.Errorf("search after restore hits = %d, want 1", got)
	}
	assertNoStagingLeft(t, dir)
}

// ---------------------------------------------------------------------------
// 12. backup equal to the live path is refused
// ---------------------------------------------------------------------------

func TestRestoreRefusesSelfRestore(t *testing.T) {
	dir := t.TempDir()
	live := newEmptyPersonalDB(t, dir, "gseer.db")
	before := fileHash(t, live)

	_, err := runRestore(t, live, live)
	if err == nil || !strings.Contains(err.Error(), "the backup path is the live database itself") {
		t.Fatalf("error = %v, want a self-restore refusal", err)
	}
	if after := fileHash(t, live); after != before {
		t.Errorf("live database changed on a refused self-restore")
	}
}
