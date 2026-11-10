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

// Package storage contains the GSEER persistence layer.  The v0.0.1
// implementation is PostgreSQL-only (ADR-0016: no embedded DB).
//
// M1: package skeleton only (this doc.go).  M3 lands the pgx pool,
// golang-migrate runner (one transaction per migration), and the
// migration set v1 (assets, services, observations, helper views,
// data_sources, source_checkpoints, ingestion_dead_letter, scopes,
// collection_jobs, suppressions, schema_migrations).  M4 lands the
// repository types (Asset, Service, Observation with provenance) and
// the search_index union view.
//
// The storage package is read-side for search (the union view is queried
// by the planner) and write-side for ingestion (v0.0.2+).  No storage
// code imports any egress package.
package storage
