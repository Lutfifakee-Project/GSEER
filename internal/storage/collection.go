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

package storage

import (
	"errors"
	"time"
)

// collection.go — the storage-level CANONICAL types for the source
// registry and collection bookkeeping (v0.3.0).
//
// Both store engines (postgres.Store for the self-hosted profile and
// sqlite.Store for personal mode) expose the SAME collection surface
// through collect.Backend.  To make that contract type-exact, the row
// types and the lookup-miss sentinel live HERE once, and each engine
// package aliases them.  Engine packages keep their own method sets —
// only the shared vocabulary is centralized.

// DataSourceRow is one data_sources registry row (same field set the
// engines carried as package-local structs before v0.3.0).
type DataSourceRow struct {
	ID         int64
	Name       string
	Connector  string
	TermsRef   string
	ConfigJSON []byte
	Enabled    bool
	CreatedAt  time.Time
}

// DeadLetter is the NORMALIZED dead-letter row the collect.Backend
// contract hands to the re-drive: the payload decoded into the map
// shape the rebuild path consumes, plus the delivery-identity fields.
//
// Engines may keep richer internal bookkeeping (per-engine status and
// audit columns); this is the shape the collector layer depends on.
type DeadLetter struct {
	ID        int64
	SourceID  int64
	Payload   map[string]any
	Reason    string
	Attempts  int
	FirstSeen time.Time
	LastSeen  time.Time
}

// CollectionStatusRow is one row of the `gseer collect status` table:
// the registry entry with its job and dead-letter counters.
type CollectionStatusRow struct {
	Name      string
	Connector string
	Enabled   bool
	Jobs      int64
	Pending   int64
}

// ErrSourceNotFound is the registry lookup miss.  Both engines return an
// error matching this sentinel so callers (the collect runner and the
// CLI) handle a missing source identically on either engine.
var ErrSourceNotFound = errors.New("data source not registered")
