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

// Package fixture implements the labeled synthetic dataset (SPEC-04 §9)
// and the banner constant shared by CLI/API/Dashboard.
//
// Every row is `provenance=fixture`; every surface marks fixture rows
// with the banner.  Fixture data can never be promoted to real (AC-9.7.x).
//
// M4: this package now carries the full fixture machinery — the
// embedded SPEC-04 §9 corpus (testdata/fixture/corpus.json), the Banner
// constant, and the Facts/Load loader that ingests it through the M4
// store (idempotent via L1/L2).  The QS-6 --load-fixtures /
// --purge-fixtures confirmation gate lives in the CLI layer; the
// provenance=fixture tag is stamped here and can never be promoted to
// real.
package fixture
