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

package search

import (
	"context"
	"time"
)

// stats.go — index freshness metadata (QS-4 honesty contract: never
// present the index as healthy/real when it is empty or fixture-labeled).
//
// Fixture observations carry provenance='fixture'; real observations
// (v0.0.2+) carry their source category.  FixtureOnly is true when the
// index holds observations and EVERY one of them is fixture-labeled —
// every surface MUST show the synthetic-data notice in that state.

// IndexMeta is the freshness snapshot attached to results (and served
// by the API's /api/v1/stats).
type IndexMeta struct {
	Assets       int64      `json:"assets"`
	Services     int64      `json:"services"`
	Observations int64      `json:"observations"`
	FixtureObs   int64      `json:"fixture_observations"`
	LastObserved *time.Time `json:"last_observed_at,omitempty"`
	FixtureOnly  bool       `json:"fixture_only"`
	Empty        bool       `json:"empty"`
}

// Notice renders the caller-facing freshness notice ("" when the index
// carries real data — which cannot happen in v0.0.1, but the contract
// is written for the v0.0.2 collectors too).
func (m *IndexMeta) Notice() string {
	if m == nil {
		return ""
	}
	if m.Empty {
		return "The index is empty — no assets have been ingested yet. " +
			"For evaluation data run: gseer setup --load-fixtures"
	}
	if m.FixtureOnly {
		return "SYNTHETIC EVALUATION DATA: every record in this index comes " +
			"from the labeled fixture corpus (provenance=fixture). " +
			"These are NOT real internet observations."
	}
	return ""
}

// Stats snapshots the index counts.  One round-trip, read-only.
func (s *Service) Stats(ctx context.Context) (*IndexMeta, error) {
	m := &IndexMeta{}
	var lastObs *time.Time
	err := s.pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM assets),
  (SELECT count(*) FROM services),
  (SELECT count(*) FROM observations),
  (SELECT count(*) FROM observations WHERE provenance = 'fixture'),
  (SELECT max(observed_at) FROM observations)
`).Scan(&m.Assets, &m.Services, &m.Observations, &m.FixtureObs, &lastObs)
	if err != nil {
		return nil, mapExecError(err)
	}
	m.LastObserved = lastObs
	m.Empty = m.Assets == 0 && m.Services == 0
	m.FixtureOnly = m.Observations > 0 && m.FixtureObs == m.Observations
	return m, nil
}
