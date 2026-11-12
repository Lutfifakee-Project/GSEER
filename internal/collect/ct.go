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

package collect

// ct.go — the Certificate Transparency connector (v0.0.2 P1), backed by
// the certificate-transparency aggregation service's JSON API (provider
// identity, endpoint and terms: docs/SOURCES.md §A).
//
// WHAT IT WOULD DO: for domain/hostname identifiers the operator or the
// index already names, query the aggregation service for logged
// certificates covering those names and record every distinct DNS name
// in the answers as a hostname/domain discovery fact (issuer, validity
// window, aggregation record id in evidence).
//
// TERMS VERDICT — DISABLED (docs/SOURCES.md §A).  The provider publishes
// no terms of use for automated access, and no statement granting
// permission to index or re-publish its aggregation.  The copyright
// notice shown on the provider's site is that provider's own claim over
// its aggregation — never a GSEER ownership claim; the underlying
// certificates are public data, but GSEER's directive is explicit:
// unclear permission for indexing/reuse keeps a connector disabled.
// The runner refuses it (E_SOURCE_TERMS) regardless
// of any database row.  The implementation stays complete and
// unit-tested; flipping Review() to reviewed-enabled is a one-line,
// owner-visible change after the terms question is settled with the
// operator (alternatives with explicit terms — e.g. an API with a
// written licence — are tracked in docs/SOURCES.md §A).

import (
	"fmt"
	"strings"
	"time"

	"github.com/Lutfifakee-Project/gseer/internal/suppress"
)

// Default CT-aggregation endpoint (overridden in tests).
const ctDefaultEndpoint = "https://crt.sh/"

// ctConnector implements the (disabled) CT discovery connector.
type ctConnector struct{}

// Kind implements Collector.
func (ctConnector) Kind() string { return "ct" }

// Review implements Collector (docs/SOURCES.md §A — verdict: DISABLED).
func (ctConnector) Review() Review {
	return Review{
		Verdict: ReviewDisabled,
		Why:     "the provider publishes no terms covering automated collection or indexing/reuse of its aggregation; needs an explicit licence before enablement",
		DocRef:  "docs/SOURCES.md §A (Certificate Transparency)",
	}
}

// ctEntry is the subset of the aggregation service's JSON record GSEER consumes.
type ctEntry struct {
	IssuerName   string `json:"issuer_name"`
	CommonName   string `json:"common_name"`
	NameValue    string `json:"name_value"`
	NotBefore    string `json:"not_before"`
	NotAfter     string `json:"not_after"`
	SerialNumber string `json:"serial_number"`
	ID           int64  `json:"id"`
}

// Collect implements Collector: per-seed aggregation queries, one batch.
// Unreachable in production while the verdict is "disabled"; tests drive
// it through httptest endpoints.
func (ctConnector) Collect(ctx Ctx) (*Batch, error) {
	if ctx.Client == nil {
		return nil, Fatal(fmt.Errorf("ct: no HTTP client configured"))
	}
	endpoint := ctx.Config.Endpoint
	if endpoint == "" {
		endpoint = ctDefaultEndpoint
	}

	names, err := seeds(ctx, "domain")
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	raws := make([]RawFact, 0, 16)
	asks, namesSeen := 0, 0

	for _, seed := range names {
		if err := ctx.Context.Err(); err != nil {
			return nil, err
		}
		seedCanonical, nerr := suppress.NormalizeDomain(seed)
		if nerr != nil {
			raws = append(raws, RawFact{AssetKind: "domain", Value: seed, ObservedAt: now})
			continue
		}
		asks++

		var entries []ctEntry
		url := strings.TrimRight(endpoint, "/") + "/?q=" + seedCanonical + "&output=json"
		if err := ctx.Client.GetJSON(ctx.Context, url, &entries); err != nil {
			return nil, err // transport failure: retryable
		}

		seenRaw, seenName := map[string]bool{}, map[string]bool{}
		for _, e := range entries {
			for _, raw := range ctNames(e) {
				if seenRaw[raw] {
					continue
				}
				seenRaw[raw] = true

				name, nerr := suppress.NormalizeDomain(raw)
				if nerr != nil {
					// Not a DNS name (IP entries, malformed SANs):
					// audit-only raw record, the pipeline dead-letters it.
					raws = append(raws, RawFact{AssetKind: "hostname", Value: raw, ObservedAt: now})
					continue
				}
				if seenName[name] {
					continue
				}
				seenName[name] = true
				namesSeen++

				raws = append(raws, RawFact{
					// Exactly the queried apex is a domain discovery; any
					// other name is a hostname discovery.  Both are
					// asset-level facts (P1 contract 3: no services).
					AssetKind:  ctAssetKind(name, seedCanonical),
					Value:      name,
					ObservedAt: now,
					Confidence: "high", // names come from cryptographically logged certificates
					Extra: map[string]any{"ct": map[string]any{
						"issuer":   e.IssuerName,
						"query":    seedCanonical,
						"crtsh_id": e.ID,
					}},
				})
			}
		}
	}

	return &Batch{
		Raws:  raws,
		Done:  true,
		Notes: map[string]int{"asks": asks, "names": namesSeen, "seeds": len(names)},
	}, nil
}

// ctNames extracts the DNS names one aggregation record covers (name_value is
// newline-separated and may include wildcards; common_name is a fallback).
func ctNames(e ctEntry) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		out = append(out, s)
	}
	for _, line := range strings.Split(e.NameValue, "\n") {
		add(strings.TrimPrefix(strings.TrimSpace(line), "*."))
	}
	if len(out) == 0 {
		add(strings.TrimPrefix(e.CommonName, "*."))
	}
	return out
}

// ctAssetKind classifies a discovered name: the registrable domain the
// operator asked about arrives as kind "domain", everything else as kind
// "hostname".  (GSEER does not run a Public Suffix List in v0.0.2; the
// seed comparison is the honest, deterministic rule.)
func ctAssetKind(name, seed string) string {
	if name == seed {
		return "domain"
	}
	return "hostname"
}

func init() { register(ctConnector{}) }
