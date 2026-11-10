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

package collect

import "github.com/Lutfifakee-Project/GSEER/internal/storage"

// Fact aliases the storage-level ingest unit so connector code and the
// runner read naturally ([]storage.Fact IS []Fact — identical alias).
type Fact = storage.Fact

// FatalError marks a connector error that retries cannot fix (a refused
// query, a malformed request).  Wrap at the point the failure is known
// permanent; the runner's retry loop passes it through unwrapped.
type FatalError struct{ Err error }

func (e FatalError) Error() string { return e.Err.Error() }

// Unwrap gives errors.Is/As access to the wrapped cause.
func (e FatalError) Unwrap() error { return e.Err }

// Fatal wraps err into a FatalError (nil passes through).
func Fatal(err error) error {
	if err == nil {
		return nil
	}
	return FatalError{Err: err}
}
