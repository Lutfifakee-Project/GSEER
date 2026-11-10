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

package searchlite

// facets.go — the personal-mode faceted-search engine: the SAME GQL
// chain (parser → validator → plannersqlite WHERE compilation) so a
// facet's filtered row set is exactly the row set the search would
// return — the same semantics contract the PG facets engine keeps.
//
// Count semantics follow the query mode (service mode counts each row
// once; asset mode counts distinct (row_kind, asset_id)).
//
// Dialect notes: technology/source membership aggregates over
// json_each (the columns are JSON arrays); the freshness buckets bind
// their thresholds as parameters (SQLite has no interval arithmetic;
// the thresholds are the fixed-width UTC strings the SQ-02 format
// guarantees compare chronologically).

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/gql/parser"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/planner"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/plannersqlite"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/validator"
	"github.com/Lutfifakee-Project/GSEER/internal/search"
)

// FacetsResult is one faceted-search response.
type FacetsResult = search.FacetsResult

// FacetsInfo describes the executed faceted query.
type FacetsInfo = search.FacetsInfo

// Facets computes the requested facet dimensions over the same
// filtered row set the search would return.
func (s *Service) Facets(ctx context.Context, q search.FacetsQuery) (*FacetsResult, error) {
	start := time.Now()
	if strings.TrimSpace(q.GQL) == "" {
		return nil, &search.QueryError{
			Code:   "E_QUERY_EMPTY",
			Detail: "no query text",
			Why:    "facets always narrow — an empty filter is not allowed",
			Next:   `try: gseer search 'port==443' --facets`,
		}
	}
	mode := s.DefaultMode
	if q.Mode != "" {
		m, err := planner.ParseMode(q.Mode)
		if err != nil {
			return nil, &search.QueryError{Code: "E_QUERY_MODE", Detail: err.Error(), Next: "try: --mode service"}
		}
		mode = m
	}
	timeout := s.DefaultTimeout
	if q.Timeout > 0 {
		timeout = q.Timeout
	}
	maxValues := search.DefaultFacetValues
	if q.MaxValues > 0 {
		maxValues = q.MaxValues
	}
	if maxValues > search.MaxFacetValues {
		maxValues = search.MaxFacetValues
	}

	known := map[string]bool{}
	for _, n := range search.FacetNames {
		known[n] = true
	}
	dims := q.Facets
	if len(dims) == 0 {
		dims = search.FacetNames
	}
	for _, d := range dims {
		if !known[d] {
			return nil, &search.QueryError{
				Code:   "E_QUERY_FACET",
				Detail: fmt.Sprintf("facet %q is unknown", d),
				Next:   "known facets: " + strings.Join(search.FacetNames, ", "),
			}
		}
	}

	tree, err := parser.Parse(q.GQL)
	if err != nil {
		return nil, wrapGQLError(err)
	}
	if _, err := validator.Validate(tree); err != nil {
		return nil, wrapGQLError(err)
	}
	where, args, _, err := plannersqlite.CompileWhere(tree)
	if err != nil {
		return nil, wrapGQLError(err)
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	countExpr := "count(*)"
	if mode == planner.ModeAsset {
		countExpr = `count(DISTINCT search_index.asset_id || '-' || search_index.row_kind)`
	}

	result := &FacetsResult{Query: FacetsInfo{
		GQL: q.GQL, Mode: mode.String(), MaxValues: maxValues,
	}}
	for _, dim := range dims {
		facet, err := s.collectFacet(ctxTimeout, dim, where, args, countExpr, maxValues)
		if err != nil {
			return nil, err
		}
		result.Facets = append(result.Facets, *facet)
	}
	result.Query.TookMs = time.Since(start).Milliseconds()
	return result, nil
}

func (s *Service) freshnessArgs() []any {
	now := time.Now().UTC()
	day := now.Add(-24 * time.Hour)
	week := now.Add(-7 * 24 * time.Hour)
	month := now.Add(-30 * 24 * time.Hour)
	return []any{formatMicro(day), formatMicro(week), formatMicro(month)}
}

func formatMicro(t time.Time) string {
	return t.Format("2006-01-02T15:04:05.000000Z07:00")
}

// freshnessExpr binds: ?, ?, ? = now-24h, now-7d, now-30d (fixed-width
// UTC strings — chronological under the SQ-02 format contract).
const freshnessExpr = `CASE
        WHEN search_index."stale" <> 0 THEN 'stale'
        WHEN search_index."last_seen" >= ? THEN '24h'
        WHEN search_index."last_seen" >= ? THEN 'week'
        WHEN search_index."last_seen" >= ? THEN 'month'
        ELSE 'older' END`

// collectFacet runs one dimension's aggregate.
func (s *Service) collectFacet(ctx context.Context, dim, where string, whereArgs []any, countExpr string, maxValues int) (*search.Facet, error) {
	selectExpr, from, notNull, extraArgs := s.facetSQL(dim)
	facet := &search.Facet{Name: dim}
	if selectExpr == "" {
		return facet, nil
	}
	// BINDING ORDER: SQLite numbers every ? by SQL position.  The
	// freshness expression's three threshold placeholders live in the
	// SELECT — BEFORE the planner's WHERE placeholders — so its args
	// must be prepended, not appended.
	allArgs := append(append([]any{}, extraArgs...), whereArgs...)
	// GROUP BY 1 (the output ordinal) — the select expression may
	// contain ? placeholders (freshness thresholds); repeating it in
	// GROUP BY would count as additional placeholders.
	// The interpolated %s parts are internal fixed expressions
	// (facetSQL's whitelist), never user text; user values bind via
	// the ? placeholders in where/extraArgs.
	sqlText := fmt.Sprintf( //nolint:gosec // G201: identifiers are the fixed facetSQL whitelist; values bind via ? placeholders
		"SELECT %s AS value, %s AS n FROM %s WHERE (%s)%s GROUP BY 1 ORDER BY n DESC, value ASC LIMIT %d",
		selectExpr, countExpr, from, where, notNull, maxValues,
	)
	if os.Getenv("GSEER_DEBUG_FACETS") != "" {
		fmt.Fprintf(os.Stderr, "FACET %s SQL=%s ARGS=%v\n", dim, sqlText, allArgs)
	}
	rows, err := s.DB.QueryContext(ctx, sqlText, allArgs...)
	if err != nil {
		return nil, &search.QueryError{Code: "E_QUERY_EXEC", Detail: err.Error(), Next: "check the database; try: gseer doctor"}
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v sql.NullString
		var n int64
		if err := rows.Scan(&v, &n); err != nil {
			return nil, err
		}
		if v.Valid {
			facet.Values = append(facet.Values, search.FacetValue{Value: v.String, Count: n})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(facet.Values) == maxValues {
		facet.Truncated = true
	}
	return facet, nil
}

// facetSQL renders one dimension's aggregate (the SQLite spellings).
// Unknown dimensions return an empty selectExpr (the caller reports an
// empty facet), so the function is total — no error return.
func (s *Service) facetSQL(dim string) (selectExpr, from, notNull string, extraArgs []any) {
	switch dim {
	case search.FacetPort:
		return `CAST(search_index."port" AS TEXT)`, "search_index", ` AND search_index."port" IS NOT NULL`, nil
	case search.FacetProtocol:
		return `search_index."protocol"`, "search_index", ` AND search_index."protocol" IS NOT NULL`, nil
	case search.FacetService:
		return `search_index."name"`, "search_index", ` AND search_index."name" IS NOT NULL`, nil
	case search.FacetCountry:
		return `search_index."geo_country"`, "search_index", ` AND search_index."geo_country" IS NOT NULL`, nil
	case search.FacetTechnology:
		return `tech.value`,
			`search_index, json_each(CASE WHEN json_valid(search_index."technologies") THEN search_index."technologies" ELSE '[]' END) AS tech`,
			` AND tech.value <> ''`, nil
	case search.FacetSource:
		return `src.value`,
			`search_index, json_each(CASE WHEN json_valid(search_index."sources") THEN search_index."sources" ELSE '[]' END) AS src`,
			` AND src.value <> ''`, nil
	case search.FacetFreshness:
		return freshnessExpr, "search_index", "", s.freshnessArgs()
	}
	return "", "search_index", "", nil
}
