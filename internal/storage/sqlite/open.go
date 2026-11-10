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

package sqlite

// open.go — SQ-04 + SQ-05: opening a personal-mode database.
//
// SQ-04 permission policy:
//   - the parent directory is created 0700 (and forced to 0700 if it
//     already exists with wider bits — the index holds operator data);
//   - a NEWLY created database file is chmod'ed 0600;
//   - an EXISTING file keeps its mode (an operator may deliberately
//     share the file with a group) — the caller surfaces PermNote so
//     the decision is visible, never silent.
//   - Windows: chmod has no POSIX mode bits; the ACL follows the
//     parent directory — documented in docs/PERSONAL.md, no error.
//
// SQ-05 concurrency decision (owner directive):
//   SetMaxOpenConns(1) — the whole pool serializes through ONE
//   connection.  Rationale: (a) SQLite allows one writer; serializing
//   readers through the same connection cannot corrupt state and needs
//   no busy-retry logic inside the process; (b) personal-mode scale is
//   one operator; (c) cross-PROCESS contention (a second gseer
//   invocation on the same file) is handled by the busy_timeout pragma
//   instead.  WAL is still enabled for crash safety and for FUTURE
//   multi-connection tuning; the concurrency tests pin the invariant.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // the pure-Go driver (no CGO)
)

// OpenMode describes what Open did to the database file.
type OpenMode string

const (
	// OpenCreated — the file did not exist and was created 0600.
	OpenCreated OpenMode = "created"
	// OpenExisting — the file existed and was opened as-is.
	OpenExisting OpenMode = "existing"
)

// OpenResult reports the opened database plus the permission decisions.
type OpenResult struct {
	DB *sql.DB
	// Mode is created|existing.
	Mode OpenMode
	// PermNote is non-empty when an existing file has group/other read
	// or write bits set (the operator's choice, surfaced — not fixed).
	PermNote string
	// Path is the (unchanged) path that was opened.
	Path string
}

// Open opens (and if needed creates) the SQLite database at path with
// the SQ-04 permission policy and the SQ-05 pool settings, registers
// the gseer_* scalar functions, and returns the handle.
func Open(path string) (*OpenResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: cannot resolve path %q: %w", path, err)
	}
	dir := filepath.Dir(abs)
	if err := ensureDir0700(dir); err != nil {
		return nil, err
	}

	_, statErr := os.Stat(abs)
	fresh := os.IsNotExist(statErr)
	if statErr != nil && !fresh {
		return nil, fmt.Errorf("sqlite: cannot stat %q: %w", abs, statErr)
	}

	db, err := sql.Open("sqlite", BuildDSN(abs))
	if err != nil {
		return nil, fmt.Errorf("sqlite: cannot open %q: %w", abs, err)
	}
	// SQ-05: one serialized connection (see the file comment).  The
	// busy_timeout pragma still guards cross-process contention.
	db.SetMaxOpenConns(1)

	if fresh {
		// Force the file into existence NOW so the chmod lands before
		// any other opener races in (sql.Open is lazy).
		if err := db.Ping(); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("sqlite: cannot create %q: %w", abs, err)
		}
		if err := os.Chmod(abs, 0o600); err != nil && !isNoChmodOS(err) {
			_ = db.Close()
			return nil, fmt.Errorf("sqlite: cannot chmod 0600 %q: %w", abs, err)
		}
	}

	res := &OpenResult{DB: db, Path: abs, Mode: OpenExisting}
	if fresh {
		res.Mode = OpenCreated
	} else if note := permNote(abs); note != "" {
		res.PermNote = note
	}
	return res, nil
}

// ensureDir0700 creates the parent directory (recursively) and forces
// its mode to 0700 — also when the directory already exists with
// wider bits (MkdirAll keeps existing modes; MkdirAll first, chmod
// after).
func ensureDir0700(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("sqlite: cannot create directory %q: %w", dir, err)
	}
	// 0700 on a DIRECTORY is the SQ-04 least-privilege contract
	// (gosec G302 is written for files, where ≤0600 applies).
	if err := os.Chmod(dir, 0o700); err != nil && !isNoChmodOS(err) { //nolint:gosec // G302: directory mode 0700 is the SQ-04 contract (G302 targets files)
		return fmt.Errorf("sqlite: cannot chmod 0700 %q: %w", dir, err)
	}
	return nil
}

// permNote renders the warning for an existing file with group/other
// bits ("" when the mode is already ≤ 0600).
func permNote(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	perm := st.Mode().Perm()
	if perm&0o077 == 0 {
		return ""
	}
	return fmt.Sprintf("database file %q is mode %04o (group/other bits set) — tighten with chmod 0600 if this was not intentional", path, uint32(perm))
}

// isNoChmodOS reports whether a chmod error is the expected no-op on
// platforms without POSIX mode bits (Windows): the ACL follows the
// parent directory and the operator surfaces permissions differently.
func isNoChmodOS(err error) bool {
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
