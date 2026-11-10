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

package searchlite

// service.go — the personal-mode SearchService: the same Query/Result
// contract as internal/search.Service, lowered to database/sql + the
// plannersqlite dialect.  The PG service is untouched; the shared TYPES
// (search.Query/Result/Row/QueryInfo/IndexMeta/QueryError) come from the
// search package so the CLI, export, and API code stay backend-agnostic.

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/gql/parser"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/planner"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/plannersqlite"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/validator"
	"github.com/Lutfifakee-Project/GSEER/internal/search"
)

// Service executes GQL searches against the personal search_index view.
type Service struct {
	DB *sql.DB

	// Defaults (PD-1 and SPEC-03 §3); a Query's explicit values win.
	DefaultMode    planner.Mode
	DefaultLimit   int
	DefaultTimeout time.Duration
}

// New returns a Service over the personal database with the standard
// defaults.
func New(db *sql.DB) *Service {
	return &Service{
		DB:             db,
		DefaultMode:    planner.ModeService,
		DefaultLimit:   planner.DefaultLimit,
		DefaultTimeout: 5 * time.Second,
	}
}

// Search validates, plans, executes, and shapes one query.
func (s *Service) Search(ctx context.Context, q search.Query) (*search.Result, error) {
	start := time.Now()

	if strings.TrimSpace(q.GQL) == "" {
		return nil, &search.QueryError{
			Code:   "E_QUERY_EMPTY",
			Detail: "no query text",
			Why:    "the GQL expression is empty",
			Next:   `try: gseer search 'port==443'`,
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
	limit := q.Limit

	sorts, err := parseSortSpec(q.Sort)
	if err != nil {
		return nil, err
	}

	// validate → plan (the GQL chain; the SQLite dialect compiles here).
	tree, err := parser.Parse(q.GQL)
	if err != nil {
		return nil, wrapGQLError(err)
	}
	if _, err := validator.Validate(tree); err != nil {
		return nil, wrapGQLError(err)
	}
	plan, err := plannersqlite.Compile(tree, planner.Options{
		Mode:    mode,
		Sort:    sorts,
		Limit:   limit,
		Timeout: timeout,
		Cursor:  q.Cursor,
		Fields:  q.Fields,
	})
	if err != nil {
		return nil, wrapGQLError(err)
	}

	// The timeout is enforced through the context deadline (SQLite has
	// no statement_timeout).
	ctxTimeout, cancel := context.WithTimeout(ctx, plan.Timeout)
	defer cancel()

	rows, err := s.exec(ctxTimeout, plan)
	if err != nil {
		return nil, err
	}

	out := &search.Result{
		Rows: rows,
		Query: search.QueryInfo{
			GQL:       q.GQL,
			Mode:      plan.Mode.String(),
			Branch:    plan.Branch.String(),
			Limit:     plan.Limit,
			TimeoutMs: plan.Timeout.Milliseconds(),
			Sort:      plan.Sort,
			RowCount:  len(rows),
			TookMs:    time.Since(start).Milliseconds(),
		},
	}
	out.HasMore = len(rows) == plan.Limit && plan.Limit > 0
	if out.HasMore && len(rows) > 0 {
		out.NextCursor = cursorFromRow(plan, rows[len(rows)-1])
	}

	if q.WithStats {
		meta, serr := s.Stats(ctx)
		if serr != nil {
			// A stats failure must not fail the search itself — the
			// results are already fetched and valid.
			out.Notice = "index metadata unavailable: " + serr.Error()
		} else {
			out.Index = meta
			out.Query.Fixtures = meta.FixtureOnly
			out.Notice = meta.Notice()
		}
	}
	return out, nil
}

// exec runs the plan and scans the rows.
func (s *Service) exec(ctx context.Context, plan *Plan) ([]search.Row, error) {
	rows, err := s.DB.QueryContext(ctx, plan.SQL, plan.Args...)
	if err != nil {
		return nil, mapPlanError(err, plan)
	}
	defer func() { _ = rows.Close() }()
	out := make([]search.Row, 0, len(plan.ProjectionFields))
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, mapExecError(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, mapExecError(err)
	}
	return out, nil
}

// Stats snapshots the index counts (the same IndexMeta contract).
func (s *Service) Stats(ctx context.Context) (*search.IndexMeta, error) {
	m := &search.IndexMeta{}
	var lastObs string
	err := s.DB.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM assets),
  (SELECT count(*) FROM services),
  (SELECT count(*) FROM observations),
  (SELECT count(*) FROM observations WHERE provenance = 'fixture'),
  (SELECT COALESCE(max(observed_at), '') FROM observations)
`).Scan(&m.Assets, &m.Services, &m.Observations, &m.FixtureObs, &lastObs)
	if err != nil {
		return nil, mapExecError(err)
	}
	if lastObs != "" {
		if t, perr := parseTS(lastObs); perr == nil {
			m.LastObserved = &t
		}
	}
	m.Empty = m.Assets == 0 && m.Services == 0
	m.FixtureOnly = m.Observations > 0 && m.FixtureObs == m.Observations
	return m, nil
}

// Validate parses and validates one GQL expression (the API's
// /validate route; execution is NOT attempted).
func (s *Service) Validate(gql string) error {
	tree, err := parser.Parse(gql)
	if err != nil {
		return wrapGQLError(err)
	}
	if _, err := validator.Validate(tree); err != nil {
		return wrapGQLError(err)
	}
	return nil
}

// Explain compiles the query and renders the SQLite plan (the API's
// /explain route).
func (s *Service) Explain(gql string) (string, error) {
	tree, err := parser.Parse(gql)
	if err != nil {
		return "", wrapGQLError(err)
	}
	if _, err := validator.Validate(tree); err != nil {
		return "", wrapGQLError(err)
	}
	plan, err := plannersqlite.Compile(tree, planner.Options{})
	if err != nil {
		return "", wrapGQLError(err)
	}
	var b strings.Builder
	b.WriteString("GQL Plan (personal/SQLite dialect)\n")
	b.WriteString("-----------------------------------\n")
	b.WriteString("mode:     " + plan.Mode.String() + "\n")
	b.WriteString("branch:   " + plan.Branch.String() + "\n")
	fmt.Fprintf(&b, "limit:    %d\n", plan.Limit)
	b.WriteString("timeout:  " + plan.Timeout.String() + "\n")
	b.WriteString("sql:\n")
	b.WriteString(plan.SQL)
	return b.String(), nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// parseSortSpec mirrors the search package's parser ("field:dir",…).
func parseSortSpec(s string) ([]planner.SortSpec, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []planner.SortSpec
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		field, dir := part, ""
		if i := strings.LastIndex(part, ":"); i >= 0 {
			field, dir = part[:i], part[i+1:]
		}
		d, err := planner.ParseDirection(dir)
		if err != nil {
			return nil, &search.QueryError{
				Code:   "E_QUERY_SORT",
				Detail: fmt.Sprintf("sort %q: %v", part, err),
				Next:   "try: --sort last_seen:desc",
			}
		}
		out = append(out, planner.SortSpec{Field: field, Direction: d})
	}
	return out, nil
}

func wrapGQLError(err error) error {
	return &search.QueryError{
		Code:   "E_QUERY_INVALID",
		Detail: err.Error(),
		Next:   "see: gseer search --help",
	}
}

func mapExecError(err error) error {
	return &search.QueryError{
		Code:   "E_QUERY_EXEC",
		Detail: err.Error(),
		Next:   "check the database; try: gseer doctor",
	}
}

// mapPlanError wraps an execution failure with the plan's SQL for
// diagnosis (the SQL never carries user data — only ? placeholders).
func mapPlanError(err error, plan *Plan) error {
	return &search.QueryError{
		Code:   "E_QUERY_EXEC",
		Detail: err.Error(),
		Why:    plan.SQL,
		Next:   "check the database; try: gseer doctor",
	}
}

// cursorFromRow renders the next-page cursor from the last row (the
// same payload contract as the PG service; Row.sortValue is mirrored
// locally to keep the search package untouched).
func cursorFromRow(plan *Plan, r search.Row) string {
	primary := plan.Sort[0]
	val, isNull := rowSortValue(&r, primary.Field)
	return planner.CursorPayload{
		SortField: primary.Field,
		RowID:     r.RowID,
		RowKind:   r.RowKind,
		Value:     val,
		Null:      isNull,
	}.Encode()
}

// rowSortValue mirrors search.Row.sortValue (stringified, isNull flag).
func rowSortValue(r *search.Row, field string) (string, bool) {
	stringFields := map[string]*string{
		"ip":                  ptrStr(r.IP),
		"domain":              ptrStr(r.Domain),
		"hostname":            ptrStr(r.Hostname),
		"asn_org":             ptrStr(r.ASNOrg),
		"geo_country":         ptrStr(r.GeoCountry),
		"geo_region":          ptrStr(r.GeoRegion),
		"geo_city":            ptrStr(r.GeoCity),
		"transport":           ptrStr(r.Transport),
		"protocol":            ptrStr(r.Protocol),
		"name":                ptrStr(r.Name),
		"product":             ptrStr(r.Product),
		"version":             ptrStr(r.Version),
		"http.title":          ptrStr(r.HTTPTitle),
		"http.server":         ptrStr(r.HTTPServer),
		"tls.version":         ptrStr(r.TLSVersion),
		"tls.cert_subject_cn": ptrStr(r.TLSCertSubjectCN),
		"tls.cert_issuer_cn":  ptrStr(r.TLSCertIssuerCN),
	}
	if p, ok := stringFields[field]; ok {
		if p != nil {
			return *p, false
		}
		return "", true
	}
	switch field {
	case "port":
		return intOrNull(r.Port)
	case "asn":
		return intOrNull(r.ASN)
	case "http.status":
		return intOrNull(r.HTTPStatus)
	case "asset_value":
		return r.AssetValue, false
	case "asset_kind":
		return r.AssetKind, false
	// The cursor value MUST use the same fixed-width micro layout the
	// storage layer writes (SQ-02): RFC3339Nano trims trailing zeros,
	// which breaks the string equality/tiebreaker against stored rows
	// (a duplicated row across pages).  RFC3339Nano is correct for the
	// PG dialect (timestamptz compares chronologically); the personal
	// TEXT columns compare lexicographically and need the exact form.
	case "first_seen":
		return r.FirstSeen.UTC().Format(microLayout), false
	case "last_seen":
		return r.LastSeen.UTC().Format(microLayout), false
	case "tls.cert_expiry":
		if r.TLSCertExpiry == nil {
			return "", true
		}
		return r.TLSCertExpiry.UTC().Format(microLayout), false
	}
	return "", true
}

func ptrStr(p *string) *string { return p }

func intOrNull(p *int) (string, bool) {
	if p == nil {
		return "", true
	}
	return strconv.Itoa(*p), false
}

// microLayout is the SQ-02 storage layout (fixed-width micro UTC).
const microLayout = "2006-01-02T15:04:05.000000Z07:00"

// Plan aliases the SQLite planner's plan (keeps signatures tidy).
type Plan = plannersqlite.Plan
