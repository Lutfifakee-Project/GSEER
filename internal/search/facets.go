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
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Lutfifakee-Project/GSEER/internal/gql/parser"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/planner"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/validator"
)

// facets.go — the v0.1.0 faceted-search engine (owner directive §3).
//
// Semantics contract: facets run the SAME GQL chain the search runs —
// parser → validator → planner WHERE compilation (planner.CompileWhere)
// — so a facet's filtered row set is exactly the row set the search
// would return for the same query (minus pagination).  Facets add only
// aggregation; they never introduce their own filtering, never touch
// the network, and never mutate anything (ADR-0010).
//
// Count semantics follow the query mode:
//   - service mode (default): each (asset, service) row counts once —
//     the same rows the service-mode search returns;
//   - asset mode: count(DISTINCT (row_kind, asset_id)) — each asset
//     counts once, mirroring the mode=asset projection.

// Facet dimension names.  The set is fixed (facet SQL is per-dimension
// by design); unknown names are rejected with E_QUERY_FACET.
const (
	FacetPort        = "port"
	FacetProtocol    = "protocol"
	FacetTechnology  = "technology"
	FacetService     = "service"
	FacetCountry     = "country"
	FacetSource      = "source"
	FacetFreshness   = "freshness"
	freshnessBuckets = `CASE
        WHEN search_index.stale THEN 'stale'
        WHEN search_index.last_seen >= now() - interval '24 hours' THEN '24h'
        WHEN search_index.last_seen >= now() - interval '7 days' THEN 'week'
        WHEN search_index.last_seen >= now() - interval '30 days' THEN 'month'
        ELSE 'older' END`
)

// FacetNames lists the facet dimensions in API order.
var FacetNames = []string{FacetPort, FacetProtocol, FacetTechnology, FacetService, FacetCountry, FacetSource, FacetFreshness}

// Defaults for facet cardinality (PD-consistent with the search caps).
const (
	DefaultFacetValues = 10
	MaxFacetValues     = 50
)

// FacetsQuery is one faceted-search request.
type FacetsQuery struct {
	// GQL is the raw filter expression (required; same language as
	// search — an empty filter is NOT allowed, facets always narrow).
	GQL string
	// Mode: "service" (default) or "asset" — mirrors search.
	Mode string
	// Timeout: 0 = the service default.
	Timeout time.Duration
	// MaxValues per facet: 0 = DefaultFacetValues; hard cap
	// MaxFacetValues.
	MaxValues int
	// Facets: which dimensions to compute; empty = all of them.
	Facets []string
}

// FacetValue is one bucket.
type FacetValue struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// Facet is one computed dimension.
type Facet struct {
	Name      string       `json:"facet"`
	Values    []FacetValue `json:"values"`
	Truncated bool         `json:"truncated"` // more distinct values exist than returned
}

// FacetsResult is the response of one Facets call.
type FacetsResult struct {
	Facets []Facet    `json:"facets"`
	Query  FacetsInfo `json:"query"`
}

// FacetsInfo describes the executed facet request.
type FacetsInfo struct {
	GQL       string `json:"gql"`
	Mode      string `json:"mode"`
	TookMs    int64  `json:"took_ms"`
	MaxValues int    `json:"max_values"`
}

// resolveFacetParams validates the facet query options and applies the
// engine defaults (mode, timeout, max-values cap).
func resolveFacetParams(q FacetsQuery, s *Service) (planner.Mode, time.Duration, int, error) {
	mode := s.DefaultMode
	if q.Mode != "" {
		m, err := planner.ParseMode(q.Mode)
		if err != nil {
			return 0, 0, 0, &QueryError{Code: "E_QUERY_MODE", Detail: err.Error(), Next: "try: --mode service"}
		}
		mode = m
	}
	timeout := s.DefaultTimeout
	if q.Timeout > 0 {
		timeout = q.Timeout
	}
	maxValues := q.MaxValues
	if maxValues == 0 {
		maxValues = DefaultFacetValues
	}
	if maxValues < 0 || maxValues > MaxFacetValues {
		return 0, 0, 0, &QueryError{
			Code:   "E_QUERY_FACET",
			Detail: fmt.Sprintf("facet max-values %d is out of range (0–%d)", maxValues, MaxFacetValues),
			Why:    "facet cardinality is capped to keep the aggregates cheap",
			Next:   fmt.Sprintf("try: --max-values %d", DefaultFacetValues),
		}
	}
	return mode, timeout, maxValues, nil
}

// facetDims resolves the requested facet dimensions: the full engine
// set when unspecified, else the deduplicated selection (order kept).
func facetDims(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return FacetNames, nil
	}
	seen := map[string]bool{}
	dims := make([]string, 0, len(requested))
	for _, name := range requested {
		if !facetNameKnown(name) {
			return nil, &QueryError{
				Code:   "E_QUERY_FACET",
				Detail: fmt.Sprintf("unknown facet %q", name),
				Why:    "the facet set is fixed per engine version",
				Next:   "known facets: " + joinFacets(FacetNames),
			}
		}
		if !seen[name] {
			seen[name] = true
			dims = append(dims, name)
		}
	}
	return dims, nil
}

// collectFacet runs one dimension's aggregate on the shared transaction
// and scans its (value, count) rows.
func collectFacet(ctx context.Context, tx pgx.Tx, dim, where, countExpr string, maxValues int, args []any) (Facet, error) {
	sqlText := facetSQL(dim, where, countExpr, maxValues)
	rows, err := tx.Query(ctx, sqlText, args...)
	if err != nil {
		return Facet{}, mapExecError(err)
	}
	defer func() { rows.Close() }()
	facet := Facet{Name: dim}
	for rows.Next() {
		var v string
		var n int64
		if err := rows.Scan(&v, &n); err != nil {
			return Facet{}, mapExecError(err)
		}
		facet.Values = append(facet.Values, FacetValue{Value: v, Count: n})
	}
	if err := rows.Err(); err != nil {
		return Facet{}, mapExecError(err)
	}
	facet.Truncated = len(facet.Values) == maxValues
	return facet, nil
}

// Facets computes the requested dimensions over the GQL-filtered row
// set.  Every dimension runs inside ONE transaction with the query
// timeout applied transaction-locally (same pattern as Search.exec —
// AC-2.8: the timeout never leaks into pooled session state).
func (s *Service) Facets(ctx context.Context, q FacetsQuery) (*FacetsResult, error) {
	start := time.Now()
	if len(strings.TrimSpace(q.GQL)) == 0 {
		return nil, &QueryError{
			Code:   "E_QUERY_EMPTY",
			Detail: "no filter expression",
			Why:    "facets aggregate over a filtered row set; there is nothing to filter",
			Next:   `try: gseer search 'port==443' --facets`,
		}
	}
	mode, timeout, maxValues, err := resolveFacetParams(q, s)
	if err != nil {
		return nil, err
	}
	dims, err := facetDims(q.Facets)
	if err != nil {
		return nil, err
	}

	// The same chain as Search: parse → validate → plan WHERE.
	tree, err := parser.Parse(q.GQL)
	if err != nil {
		return nil, wrapGQLError(err)
	}
	if _, err := validator.Validate(tree); err != nil {
		return nil, wrapGQLError(err)
	}
	where, args, _, err := planner.CompileWhere(tree)
	if err != nil {
		return nil, wrapGQLError(err)
	}

	countExpr := "count(*)"
	if mode == planner.ModeAsset {
		countExpr = `count(DISTINCT (search_index.row_kind, search_index.asset_id))`
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, mapExecError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Transaction-local timeout (same parameterized pattern as exec).
	if _, err := tx.Exec(ctx,
		`SELECT set_config('statement_timeout', $1::text, true)`,
		fmt.Sprintf("%d", timeout.Milliseconds())); err != nil {
		return nil, mapExecError(err)
	}

	result := &FacetsResult{Query: FacetsInfo{
		GQL: q.GQL, Mode: mode.String(), MaxValues: maxValues,
	}}
	for _, dim := range dims {
		facet, err := collectFacet(ctx, tx, dim, where, countExpr, maxValues, args)
		if err != nil {
			return nil, err
		}
		result.Facets = append(result.Facets, facet)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapExecError(err)
	}
	result.Query.TookMs = time.Since(start).Milliseconds()
	return result, nil
}

// facetSQL renders one dimension's aggregate.  The facet dimension is
// engine-owned (never user input); the WHERE clause is planner output
// over $N placeholders and carries all filter values, so no dimension
// needs extra query arguments today.
func facetSQL(dim, where, countExpr string, maxValues int) string {
	// placeholder must start at len(args)+1.  No dimension SQL needs
	// extra placeholders today (maxValues is engine-formatted, bounded).
	selectExpr := map[string]string{
		FacetPort:       `search_index.port::text`,
		FacetProtocol:   `search_index.protocol`,
		FacetService:    `search_index.name`,
		FacetCountry:    `search_index.geo_country`,
		FacetTechnology: `tech.v`,
		FacetSource:     `src.v`,
		FacetFreshness:  freshnessBuckets,
	}[dim]
	from := "search_index"
	switch dim {
	case FacetTechnology:
		from = `search_index, LATERAL unnest(search_index.technologies) AS tech(v)`
	case FacetSource:
		from = `search_index, LATERAL unnest(search_index.sources) AS src(v)`
	}
	notNull := ""
	switch dim {
	case FacetPort, FacetProtocol, FacetService, FacetCountry:
		notNull = " AND " + selectExpr + " IS NOT NULL"
	case FacetTechnology, FacetSource:
		notNull = " AND " + selectExpr + " <> ''"
	}
	groupBy := selectExpr
	if dim == FacetFreshness {
		// GROUP BY the whole CASE (PG allows grouping by the output
		// ordinal, but the explicit expression keeps it unambiguous).
		groupBy = freshnessBuckets
	}
	return fmt.Sprintf(
		"SELECT %s AS value, %s AS n FROM %s WHERE (%s)%s GROUP BY %s ORDER BY n DESC, value ASC LIMIT %d",
		selectExpr, countExpr, from, where, notNull, groupBy, maxValues,
	)
}

func facetNameKnown(name string) bool {
	for _, f := range FacetNames {
		if f == name {
			return true
		}
	}
	return false
}

func joinFacets(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
