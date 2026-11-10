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

// Package fprule implements the v0.1.0 fingerprint rule engine: a
// versioned, lint-checked rule collection that classifies technology
// evidence ALREADY OBSERVED into products with confidence.
//
// Honesty contract (v0.1.0, owner directive §2):
//
//   - Rules match over evidence fields that a stored observation
//     actually carries (http.server/title, tls.*, ssh.banner).  The
//     engine never fabricates an observation, never contacts the
//     network, and never invents a port/service from a technology hit
//     (Discovery ≠ Observation — collect/doc.go).
//   - Signatures are IDENTIFICATION only.  No exploitation, no
//     vulnerability, no credential, no bypass signature is accepted —
//     the linter rejects rule ids/texts that smell like them (see
//     lintExploitationPatterns) and reviews are the second gate.
//   - Confidence is compositional and evidence-based: rule confidence
//     × per-match weight; the tiers (≥0.9 header-is-product, 0.7–0.89
//     header-embedded, 0.5–0.69 weak title signal) are documented in
//     docs/RULES.md and pinned by seed tests.
//   - Every rule ships ≥1 positive and ≥1 negative (false-positive
//     guard) test; `gseer rules validate` runs them and CI fails on a
//     regression.
//
// The embedded seed collection (seed/*.yaml) is the v0.1.0 "fingerprint
// seed set"; operators contribute additional rules through the format
// documented in docs/RULES.md and load them via config
// `fingerprints.rules_dir`.
package fprule
