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

//go:build !windows

package update

import "os"

// renameFileDefault is the POSIX replacement primitive: plain
// os.Rename (rename(2) — same-directory, atomic, refuses cross-device
// instead of copying).  It passes no durability flags of its own; crash
// durability is provided afterwards by syncDirDefault's directory
// fsync, which is why a failed directory sync must surface as the
// E_UPDATE_DIRSYNC warning rather than being swallowed.
func renameFileDefault(from, to string) error {
	return os.Rename(from, to)
}

// syncDirDefault flushes a directory entry to stable storage so a
// just-completed rename survives a crash: open the directory read-only,
// fsync it, and close it.  Every step's error is surfaced explicitly —
// a silent failure here would falsely claim crash durability.
func syncDirDefault(dir string) error {
	d, err := os.Open(dir) // #nosec G304 -- dir is the target binary's own directory, opened read-only for the rename durability sync
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	// The sync result is the primary operation; the close error only
	// matters when the sync succeeded.
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
