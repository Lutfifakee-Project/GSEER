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

// Package collect is the FIRST-PARTY PASSIVE data connector architecture
// (v0.0.2, milestones M14/M15, owner directive §2).
//
// # What a connector is
//
// A connector turns one data provider's records into GSEER facts —
// (asset, optional service, observation) triples — through the SAME
// ingestion path every other writer uses (store.IngestFacts).  There is
// no side door: the suppression gate (M13) and the assets backstop
// trigger see everything a collector produces.
//
// # The four honesty contracts
//
//  1. Search ≠ Scan.  A collector only QUERIES providers the operator
//     configured about identifiers the operator or the index already
//     named (seeds, or assets already in the index).  Nothing here port-
//     scans, banner-grabs, or enumerates the internet.  The P2 active
//     observation regime (gseer scan, PAC gates, scopes table) is a
//     different milestone with different gates.
//
//  2. Terms before traffic.  Every connector carries a Review verdict.
//     A connector whose provider terms do not clearly permit automated
//     collection and indexing is DISABLED — the runner refuses to run it
//     (E_SOURCE_TERMS) even if a database row claims enabled=true.
//     docs/SOURCES.md is the review register; each connector's Review.DocRef
//     points at its section.
//
//  3. Discovery ≠ Observation.  Passive data (DNS answers, certificate
//     log entries, registry objects) proves an identifier EXISTS or a
//     mapping was PUBLISHED — never that a service answered on a port.
//     P1 connectors therefore write ASSET-LEVEL facts only (Service ==
//     nil): no port, protocol, HTTP, TLS or banner information is ever
//     invented from passive sources.  In search_index every such record
//     appears as row_kind='asset-only'.  Service observations require a
//     service observation, which is P2 with its own gates.
//
//  4. Provenance on every record.  Facts carry the delivering source's
//     identity (observation.source_id), the connector category in
//     provenance, the observation timestamp (the provider's where it has
//     one; the lookup time otherwise) and evidence with a confidence
//     label.  Nothing a connector writes can be mistaken for the labeled
//     synthetic fixture corpus (provenance=fixture is reserved for it).
//
// # Data quality (owner directive §4)
//
//   - Normalization: every identifier passes the suppress package's
//     canonicalizers (lowercase IDNA, trailing-dot strip, netip form) —
//     the same functions the suppression gate uses, so normalization can
//     never diverge from suppression.
//   - Idempotency: L1 delivery identity (source, cursor) + L2 fact
//     identity (source, asset, service, observed_at, payload_hash) make
//     re-runs no-ops at the store level; connectors additionally dedupe
//     within a batch.
//   - First-seen / last-seen / freshness: the asset/service upserts keep
//     first_seen, GREATEST last_seen and clear stale on fresh sightings —
//     a re-observed identifier ages forward, a missing one ages stale.
//   - Retry + checkpoint: the Runner retries transport failures with
//     backoff, stores per-source cursors (source_checkpoints), and parks
//     poison records in ingestion_dead_letter instead of dropping them.
//
// # Layering
//
//	collect → storage/postgres → storage, suppress
//	collect → suppress (normalizers only)
//
// The package never imports internal/gql (search stays read-only over
// the index) and never opens listening sockets.  The governance tests
// pin both.
package collect

// Review is the terms-compliance verdict for one connector's provider.
type Review struct {
	// Verdict is "reviewed-enabled" (clear compatible terms: the
	// connector may be enabled by the operator) or "disabled"
	// (credentials/payment/unclear permission for indexing or reuse —
	// the runner refuses to run it, period).
	Verdict string `json:"verdict"`
	// Why is the one-line justification (what the terms say or lack).
	Why string `json:"why"`
	// DocRef anchors the full review in docs/SOURCES.md.
	DocRef string `json:"doc_ref"`
}

// Review verdicts.
const (
	ReviewEnabled  = "reviewed-enabled"
	ReviewDisabled = "disabled"
)
