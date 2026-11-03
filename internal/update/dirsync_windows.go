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

//go:build windows

package update

import (
	"os"
	"syscall"
	"unsafe"
)

// Windows replacement + durability primitive.
//
// Go's os.Rename on Windows calls plain MoveFileW with NO flags
// (syscall.Rename → MoveFile, src/syscall/zsyscall_windows.go) — in
// particular it does NOT pass MOVEFILE_WRITE_THROUGH, so a rename via
// os.Rename is NOT guaranteed to survive a crash or power loss.  The
// updater therefore performs the replacement itself via MoveFileExW
// with explicit flags (Microsoft Learn, MoveFileExA/W):
//
//	MOVEFILE_REPLACE_EXISTING (0x1) — replace an existing target file.
//	MOVEFILE_WRITE_THROUGH    (0x8) — the function does not return until
//	                                  the moved file is actually on disk.
//
// Deliberately NOT passed: MOVEFILE_COPY_ALLOWED — a cross-volume
// replace must fail loudly (ERROR_NOT_SAME_DEVICE), never degrade into
// a non-atomic copy; replaceBinary already stages the temp file in the
// target's own directory, so both paths are always on one volume.
//
// Windows has no directory-handle flush (FlushFileBuffers rejects a
// directory handle), so there is no POSIX-style fsync(dir) to run after
// the rename: MOVEFILE_WRITE_THROUGH IS the post-rename durability
// barrier on this platform.  Sync errors are therefore impossible to
// ignore here — the barrier happens inside the replacement call itself,
// and its failure surfaces as a plain replacement error.
const (
	movefileReplaceExisting = 0x00000001
	movefileWriteThrough    = 0x00000008
)

var (
	modkernel32     = syscall.NewLazyDLL("kernel32.dll")
	procMoveFileExW = modkernel32.NewProc("MoveFileExW")
)

// moveFileEx is the MoveFileExW entry point used by renameFileDefault.
// Package-level purely as a test seam: the Windows tests assert that
// the replacement passes MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH
// and that API errors surface.  Production always calls the real
// kernel32 export.
var moveFileEx = func(from, to *uint16, flags uint32) error {
	r1, _, e1 := procMoveFileExW.Call(
		uintptr(unsafe.Pointer(from)), // #nosec G103 -- MoveFileExW's LPWSTR parameter; the pointers stay valid for the duration of the synchronous call
		uintptr(unsafe.Pointer(to)),   // #nosec G103 -- see above
		uintptr(flags),
	)
	if r1 == 0 {
		// GetLastError is only meaningful when the call failed, and e1
		// may be errno 0 even then — fall back to a generic error.
		if e1 == syscall.Errno(0) {
			return syscall.EINVAL
		}
		return e1
	}
	return nil
}

// renameFileDefault is the Windows replacement primitive: MoveFileExW
// with MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH.  Errors are
// wrapped in *os.LinkError with the "rename" op — the same shape
// os.Rename produces — so callers need no platform-specific handling.
func renameFileDefault(from, to string) error {
	fp, err := syscall.UTF16PtrFromString(from)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	tp, err := syscall.UTF16PtrFromString(to)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	if merr := moveFileEx(fp, tp, movefileReplaceExisting|movefileWriteThrough); merr != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: merr}
	}
	return nil
}

// syncDirDefault on Windows: no directory-handle flush exists on
// Windows (FlushFileBuffers rejects a directory handle), and none is
// needed — the replacement primitive itself ran MoveFileExW with
// MOVEFILE_WRITE_THROUGH, which does not return until the moved file
// is on disk.  Returning nil therefore reports a barrier that really
// happened (the write-through replacement), not an ignored sync.  If
// renameFileDefault is ever changed to a non-write-through call, this
// function MUST return an error instead — a silent nil would falsely
// claim crash durability.
func syncDirDefault(_ string) error {
	return nil
}
