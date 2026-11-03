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

// Package dashboard implements the server-rendered pages (v0.0.1:
// /, /search, /assets/{id}, /about) calling the same SearchService
// server-side (AC-4.9 third surface).
//
// M1: package skeleton only (this doc.go).  M10 lands the pages and
// the three-surface parity (CLI -o json vs REST vs Dashboard server-side
// call return identical row identity sets + total across F1–F17).
//
// The dashboard package depends on internal/search and internal/config;
// it never imports any collector or egress package (ADR-0010; AC-3.1).
package dashboard
