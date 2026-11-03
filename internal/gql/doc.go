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

// Package gql contains the GSEER Query Language implementation:
// lexer → parser → ast → validator → planner (SPEC-04).
//
// M1: package skeleton only (this doc.go and the sub-package doc.go files).
// The execution order per the Phase 1 plan is M5 (lexer) → M8 (ast) →
// M6 (parser) → M7 (validator) → M9 (planner).  M1 lands the package
// layout per SPEC-02 §2 so the import-graph test (AC-3.1) can pin the
// absence of collector/egress dependencies from the very first commit.
//
// The gql package never imports any collector, egress, or P2 package
// (ADR-0010 Search ≠ Scan).
package gql
