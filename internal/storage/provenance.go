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

package storage

// provenance.go — canonical provenance constants shared by storage,
// fixture loader and CLI (QS-6).
//
// Provenance is the honesty surface of the dataset: every observation is
// labeled with where it came from, and the labeled-synthetic fixture data
// can never be promoted to real internet data.  Keeping the literals in
// one place prevents the "fixture" tag from drifting between the loader,
// the purge paths and the search-side provenance filters.

// FixtureSourceName is the data_sources.name of the labeled synthetic
// fixture source (D-M3-1: the fixture source is itself a registry row).
const FixtureSourceName = "fixture"

// ProvenanceFixture is the observations.provenance value for every row
// written by the fixture loader; it can never be promoted to real (QS-6).
const ProvenanceFixture = "fixture"
