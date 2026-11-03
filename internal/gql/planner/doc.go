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

// Package planner compiles the validated GQL AST to parameterized
// PostgreSQL SQL against the search_index union view (SPEC-04 §6, §8).
//
// M9 (landed): the three-branch MR-6 projection (witness | any-union |
// asset-all), the mode filter (ModeService default / ModeAsset DISTINCT
// ON asset_id), tokenized/substring/element-substring matching (==, IN,
// CONTAINS-ILIKE on strings, CONTAINS @> ARRAY on lists), IN_CIDR
// (ip::inet <<= cidr), IN_DOMAIN (domain = v OR domain ILIKE '%.' || v),
// sort rules (NULLS LAST/FIRST per direction; row_id ASC tiebreaker
// always appended), cursor pagination (disjunctive strict-advancement
// predicate correct for any primary direction + ASC row_id tiebreaker),
// limit/timeout wiring (Plan.Timeout carried as a Go duration and
// applied separately via Plan.ApplyStatementTimeoutSQL — PG's SET LOCAL
// does not accept a $N placeholder, so the timeout value never appears
// literally in any SQL text, AC-2.8), parameterized SQL only ($N
// placeholders, never interpolated user values, AC-2.8), and the
// explain output (Plan.Explain: mode, branch set, limit, timeout, sort,
// cursor, projection, SQL, args).
//
// The three branches classify a compiled Plan for EXPLAIN output:
//   - witness: a flat per-row predicate evaluated directly against
//     search_index (the common case — port == 443, http.status == 200);
//   - any-union: an ANY(host){ body } block compiled to a correlated
//     EXISTS subquery against a fresh alias of search_index (the host's
//     services) — the outer row matches if any inner service row of the
//     same asset_id satisfies body;
//   - any-service-inline: an ANY(service){ body } block folded into
//     the outer predicate (search_index is already per-service);
//   - asset-all: mode=asset wraps the projection in a WITH page AS (…)
//     DISTINCT ON (asset_id) subquery preferring last_seen DESC, so the
//     result is one row per asset.
//
// A single Plan can carry several branches at once (e.g. a witness
// predicate ANDed with an any-union, run in mode=asset).
//
// Layering: the planner is a pure compiler.  It imports internal/gql/ast
// only (plus the Go standard library).  It does NOT import pgx or any
// execution package — Plan.ApplyStatementTimeoutSQL returns the
// parameterized SQL the M10 SearchService runs; the planner itself
// executes nothing.  The planner never imports any collector or egress
// package (ADR-0010 Search ≠ Scan; AC-3.1 import-graph test).
package planner
