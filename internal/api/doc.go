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

// Package api implements the chi v5 HTTP routes for /api/v1 (SPEC-07)
// and the OpenAPI 3.1 source-of-truth with a CI route diff.
//
// M1: package skeleton only (this doc.go).  M10 lands the routes
// (health, version, search, explain, validate, assets/{id}[/history],
// sources, rules, server status/stop), token auth (hashed at rest),
// scopes, rate-limit headers, request IDs (UUIDv7), standard envelope
// + API_E_* codes.
//
// The api package depends on internal/search (the SearchService) and
// internal/config; it never imports any collector or egress package
// (ADR-0010; AC-3.1 import-graph test).
package api
