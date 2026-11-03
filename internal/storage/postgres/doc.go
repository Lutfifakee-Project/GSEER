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

// Package postgres is the PostgreSQL adapter under internal/storage.
//
// M1: package skeleton (doc.go only).  M3 lands the storage layer:
//
//   - migrations runner (golang-migrate v4, database/pgx/v5 driver,
//     source/iofs embedded migrations; one atomic transaction per
//     migration) — migrate.go, lock.go
//   - bounded advisory-lock waiting (session-level statement_timeout;
//     DB_MIGRATION_LOCK_TIMEOUT on expiry) — lock.go
//   - administrative migration commands backing `gseer db migrate
//     {status,diagnose,force}` — admin.go
//   - pgxpool lifecycle (created AFTER migrations succeed) — pool.go
//   - reachability + PG version checks (E_DB_UNREACHABLE, PD-8) —
//     reachability.go, version.go
//   - credential redaction for diagnostics (M3 discipline until SEC-6
//     lands in M10) — redact.go
//   - doctor migration-status checks (QS-4) — doctor.go
//
// M4 lands the repositories and the search_index union view.
//
// Search ≠ Scan (ADR-0010): this package contains no egress code and
// imports no collector package.  The M1 import-graph test pins that
// invariant; it must hold for every future change here.
package postgres
