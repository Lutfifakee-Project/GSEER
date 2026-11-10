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

// Package search implements the SearchService: validate → plan → execute
// → shape, applying the mode filter (default: service, PD-1) with no mode
// semantics in any caller (RS-3, SPEC-04).
//
// M1: package skeleton only (this doc.go).  Full implementation lands in
// M10.  The SearchService depends only on the read-side SearchIndex
// interface; it has no dependency on any collector, egress, or P2
// package (ADR-0010 Search ≠ Scan; AC-3.1 import-graph test).
package search
