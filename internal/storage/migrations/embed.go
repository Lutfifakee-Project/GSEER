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

// Package migrations embeds the GSEER SQL migration files so the
// compiled binary is self-contained (no runtime file-path lookup).
//
// The golang-migrate source driver `source/iofs` consumes the embed.FS
// directly: migrate.NewWithSourceInstance("iofs", migrations.FS, driver).
//
// Migration set (owner-approved M3/M4 scope; SPEC-05 §2/§3/§5 + v0.0.2
// Passive Intelligence milestone M13):
//
//	0001_core             assets, services, observations, data_sources
//	                      + L1/L2 uniqueness indexes + search indexes
//	0002_helper_views     latest_http, latest_tls, latest_fingerprints
//	0003_search_index     search_index union view (M4 planner target)
//	0004_search_indexes   evidence-driven supporting indexes (M4 STEP 2/3;
//	                      EXPLAIN ANALYZE baseline in
//	                      internal/storage/postgres/testdata/plan_baseline.md)
//	0005_suppressions     suppression policy rules + audit surface +
//	                      the DB backstop trigger (M13; fail-closed)
//	0006_collection       collector-side bookkeeping: collection_jobs,
//	                      source_checkpoints, ingestion_dead_letter,
//	                      scopes (M14; scopes itself stays P2-unused)
//	0016_suppression_policy_hardening
//	                      the pinned Rev 6.7.2 gseer_policy enforcement
//	                      layer (hash-pinned; versions 0007–0015 are
//	                      permanently RESERVED per OD-4 — they must
//	                      never be created)
//	0017_search_safe_casts
//	                      safe-cast helpers + rebuilt latest_http /
//	                      latest_tls views (GAP-1 mandate; malformed
//	                      evidence can no longer break search)
//
// The schema_migrations table is owned by the golang-migrate driver and
// is NEVER created or modified by an application migration.
package migrations

import (
	"embed"
)

// FS holds every *.sql file in this directory, embedded at build time.
//
//go:embed *.sql
var FS embed.FS
