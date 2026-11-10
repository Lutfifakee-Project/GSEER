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

// Package ast holds the typed GQL tree + visitor + explain rendering.
//
// M1: package skeleton only (this doc.go).  M8 lands the node types
// (Or/And/Not/Comparison/Exists/Quantified{ANY,ALL}) carrying canonical
// field paths after alias rewrite, node-count/depth accounting (K-13
// limits enforced at parse), stable canonical String() used by --explain,
// and the visitor for validator/planner.
package ast
