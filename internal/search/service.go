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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Lutfifakee-Project/gseer/internal/gql/parser"
	"github.com/Lutfifakee-Project/gseer/internal/gql/planner"
	"github.com/Lutfifakee-Project/gseer/internal/gql/validator"
)

// service.go — the M10 SearchService (SPEC-04: validate → plan → execute
// → shape).  The single search implementation shared by every surface:
// `gseer search` (CLI), the REST API, and the dashboard all call THIS
// service, so GQL semantics and pagination are identical everywhere.
//
// Search never triggers scanning (ADR-0010): the service only reads the
// existing search_index projection; it holds no collector/egress
// dependency (AC-3.1 import-graph test).

// Service executes GQL searches against the search_index view.
type Service struct {
	pool *pgxpool.Pool

	// Defaults (PD-1 and SPEC-03 §3); a Query's explicit values win.
	DefaultMode    planner.Mode
	DefaultLimit   int
	DefaultTimeout time.Duration
}

// New returns a Service over pool with the standard defaults.
func New(pool *pgxpool.Pool) *Service {
	return &Service{
		pool:           pool,
		DefaultMode:    planner.ModeService,
		DefaultLimit:   planner.DefaultLimit,
		DefaultTimeout: 5 * time.Second,
	}
}

// Query is one search request.  Zero fields fall back to the service
// defaults; Mode/Sort strings are the user-facing spellings.
type Query struct {
	// GQL is the raw query text (required).
	GQL string
	// Mode: "service" (default, PD-1) or "asset".
	Mode string
	// Sort: comma-separated "field:dir" pairs (dir asc|desc, optional).
	// Empty = server default (last_seen DESC).
	Sort string
	// Limit: 0 = default.
	Limit int
	// Timeout: 0 = default.
	Timeout time.Duration
	// Cursor: opaque pagination cursor from a prior Result.
	Cursor string
	// Fields: optional projection (canonical paths or column names).
	Fields []string
	// WithStats: attach index freshness metadata to the Result.
	WithStats bool
}

// Result is one search response.
type Result struct {
	Rows []Row
	// NextCursor is the cursor for the next page, "" when exhausted.
	// Derived from the LAST row (sort value + composite row key), so
	// callers must not build it themselves.
	NextCursor string
	// HasMore is len(rows) == the effective limit (keyset pagination
	// heuristic; one extra round-trip on the tail page is avoided).
	HasMore bool
	// Query describes how the query was compiled (mode, branch, took).
	Query QueryInfo
	// Index carries freshness metadata when Query.WithStats was set.
	Index *IndexMeta
	// Explain carries the rendered plan when requested (Query flag via
	// the Explain result field on the CLI path; see Plan()).
	Explain string
	// Notice is a caller-facing notice (e.g. empty index guidance,
	// fixture-data label).  Never empty when the index needs context.
	Notice string
}

// QueryInfo describes the executed query.
type QueryInfo struct {
	GQL       string             `json:"gql"`
	Mode      string             `json:"mode"`
	Branch    string             `json:"branch"`
	Limit     int                `json:"limit"`
	TimeoutMs int64              `json:"timeout_ms"`
	Sort      []planner.SortSpec `json:"sort"`
	TookMs    int64              `json:"took_ms"`
	RowCount  int                `json:"row_count"`
	// Fixtures is true when the index the query ran against is
	// fixture-labeled (copied from Index when WithStats was set).
	Fixtures bool `json:"fixtures"`
}

// Search validates, plans, executes, and shapes one query.
func (s *Service) Search(ctx context.Context, q Query) (*Result, error) {
	start := time.Now()

	if strings.TrimSpace(q.GQL) == "" {
		return nil, &QueryError{
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
			return nil, &QueryError{Code: "E_QUERY_MODE", Detail: err.Error(), Next: "try: --mode service"}
		}
		mode = m
	}
	timeout := s.DefaultTimeout
	if q.Timeout > 0 {
		timeout = q.Timeout
	}
	limit := q.Limit

	// Sort parsing: "field:dir" comma-separated.
	sorts, err := parseSortSpec(q.Sort)
	if err != nil {
		return nil, err
	}

	// validate → plan (the GQL chain).
	tree, err := parser.Parse(q.GQL)
	if err != nil {
		return nil, wrapGQLError(err)
	}
	if _, err := validator.Validate(tree); err != nil {
		return nil, wrapGQLError(err)
	}
	plan, err := planner.Compile(tree, planner.Options{
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

	// Execute inside ONE transaction: the transaction-local
	// statement_timeout applies to exactly this SELECT and dies with the
	// transaction — set_config(is_local=true) never leaks into the
	// pooled connection's session state.
	rows, err := s.exec(ctx, plan)
	if err != nil {
		return nil, err
	}

	// Shape.
	out := &Result{
		Rows: rows,
		Query: QueryInfo{
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

// exec opens the transaction, applies the timeout, runs the plan, and
// scans the rows; commit (or rollback on any error).
func (s *Service) exec(ctx context.Context, plan *planner.Plan) ([]Row, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, mapExecError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	// Transaction-local timeout (parameterized set_config — AC-2.8).
	if toSQL, toArgs := plan.ApplyStatementTimeoutSQL(); toSQL != "" {
		if _, err := tx.Exec(ctx, toSQL, toArgs...); err != nil {
			return nil, errors.Join(errMap(mapExecError(err)), tx.Rollback(ctx))
		}
	}

	got, err := tx.Query(ctx, plan.SQL, plan.Args...)
	if err != nil {
		return nil, mapExecError(err)
	}
	collected, scanErr := collectRows(got)
	// pgx.Rows.Close returns no value; Err() carries the iteration error
	// and collectRows already surfaces it.  Close releases the connection
	// back to the tx.
	got.Close()
	if scanErr != nil {
		return nil, mapExecError(scanErr)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, mapExecError(err)
	}
	return collected, nil
}

// errMap flattens errors.Join output for the caller (first error wins).
func errMap(err error) error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if e != nil {
				return e
			}
		}
	}
	return err
}

// collectRows scans every result row by COLUMN NAME (the projection is
// caller-selected, so positions vary).
func collectRows(rows pgx.Rows) ([]Row, error) {
	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// parseSortSpec parses "field:dir,field2:dir2" into planner.SortSpec.
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
			return nil, &QueryError{
				Code:   "E_QUERY_SORT",
				Detail: fmt.Sprintf("sort %q: %v", part, err),
				Next:   "try: --sort last_seen:desc",
			}
		}
		out = append(out, planner.SortSpec{Field: field, Direction: d})
	}
	return out, nil
}

// wrapGQLError converts lexer/parser/validator/planner errors into the
// unified QueryError (they all render per the SPEC-03 §7 style guide
// already; the wrapper preserves the rendered text and code).
func wrapGQLError(err error) error {
	var qe *QueryError
	if errors.As(err, &qe) {
		return qe
	}
	// gql errors render Code/Detail/Why/Try via Error().
	return &QueryError{
		Code:   "E_QUERY_INVALID",
		Detail: err.Error(),
		Next:   "see: gseer search --help",
	}
}

// mapExecError classifies execution-layer failures:
//   - statement_timeout (SQLSTATE 57014) → TimeoutError
//   - pgconn connect/closed errors       → UnavailableError
//   - everything else                    → returned raw (tx rollback
//     already discards any partial state)
func mapExecError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "57014" { // query_canceled via statement_timeout
			return &TimeoutError{Err: err}
		}
		return err
	}
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return &UnavailableError{Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, pgx.ErrTxClosed) {
		return &UnavailableError{Err: err}
	}
	return err
}

// Plan compiles a query WITHOUT executing it — the `--validate` and
// `--explain` paths.  Returns the compiled plan (or the first
// lexer/parser/validator/planner error).
func (s *Service) Plan(q Query) (*planner.Plan, error) {
	mode := s.DefaultMode
	if q.Mode != "" {
		m, err := planner.ParseMode(q.Mode)
		if err != nil {
			return nil, &QueryError{Code: "E_QUERY_MODE", Detail: err.Error(), Next: "try: --mode service"}
		}
		mode = m
	}
	sorts, err := parseSortSpec(q.Sort)
	if err != nil {
		return nil, err
	}
	tree, err := parser.Parse(q.GQL)
	if err != nil {
		return nil, wrapGQLError(err)
	}
	if _, err := validator.Validate(tree); err != nil {
		return nil, wrapGQLError(err)
	}
	return planner.Compile(tree, planner.Options{
		Mode:    mode,
		Sort:    sorts,
		Limit:   q.Limit,
		Timeout: q.Timeout,
		Cursor:  q.Cursor,
		Fields:  q.Fields,
	})
}

// cursorFromRow derives the next-page cursor from the plan's sort and
// the last result row (the composite row key + the sort value, with the
// NULL flag when the sort column was NULL).
func cursorFromRow(plan *planner.Plan, r Row) string {
	primary := plan.Sort[0]
	val, isNull := r.sortValue(primary.Field)
	return planner.CursorPayload{
		SortField: primary.Field,
		RowID:     r.RowID,
		RowKind:   r.RowKind,
		Value:     val,
		Null:      isNull,
	}.Encode()
}
