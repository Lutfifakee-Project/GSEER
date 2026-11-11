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

package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/Lutfifakee-Project/gseer/internal/export"
	"github.com/Lutfifakee-Project/gseer/internal/httpx"
	"github.com/Lutfifakee-Project/gseer/internal/search"
	"github.com/Lutfifakee-Project/gseer/internal/version"
)

// server.go — the REST API surface (SPEC-07): /api/v1 routes over the
// SAME SearchService the CLI and dashboard use, so GQL semantics,
// pagination, and error text are identical on every surface.
//
// Search never triggers scanning (ADR-0010): every handler here is
// read-only against the local index.  The api package imports
// internal/search and internal/gql/* — never any collector/egress
// package (AC-3.1 import-graph test).

// Server is the REST API.
type Server struct {
	search *search.Service
	// TokenHash is the SHA-256 hex digest of the configured API token;
	// "" means token auth is DISABLED — the server must then bind
	// loopback only (enforced by the cmd wiring before ListenAndServe).
	TokenHash string
	// RateLimit is requests per minute per client IP (0 = default 120).
	RateLimit int
	limiter   *rateLimiter
	// TrustedProxies are the CIDRs whose X-Forwarded-For header the
	// API may honor for client-IP resolution (M16).  Empty (the
	// default) = peer address only; the header is ignored.
	TrustedProxies []netip.Prefix
}

// New builds the API Server over the shared SearchService.
func New(svc *search.Service, tokenHash string, rateLimitPerMin int) *Server {
	if rateLimitPerMin <= 0 {
		rateLimitPerMin = 120
	}
	return &Server{
		search:    svc,
		TokenHash: tokenHash,
		RateLimit: rateLimitPerMin,
		limiter:   newRateLimiter(rateLimitPerMin, time.Minute),
	}
}

// Handler builds the http.Handler for the API.  Routes live under the
// /api/v1 prefix (SPEC-07), so the server command mounts this at root.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		// Middlewares: request ID → client-IP (trusted-proxy aware,
		// M16) → security headers → recovery → rate-limit → auth.
		r.Use(chimw.RequestID)
		r.Use(httpx.ResolveClientIP(s.TrustedProxies))
		r.Use(httpx.SecurityHeaders)
		r.Use(chimw.Recoverer)
		r.Use(s.rateLimit)
		r.Use(s.requestMeta)

		// Liveness — no auth, no DB (monitoring probes).
		r.Get("/health", s.handleHealth)
		// Readiness — DB reachable; unauthenticated for probes.
		r.Get("/ready", s.handleReady)
		// Version — unauthenticated (harmless build info).
		r.Get("/version", s.handleVersion)

		// Authenticated search surface.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/search", s.handleSearchGet)
			r.Post("/search", s.handleSearchPost)
			r.Get("/facets", s.handleFacets)
			r.Get("/export", s.handleExport)
			r.Post("/validate", s.handleValidate)
			r.Post("/explain", s.handleExplain)
			r.Get("/stats", s.handleStats)
		})
	})
	return r
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeData(w, r, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	meta, err := s.search.Stats(r.Context())
	if err != nil {
		// DB unreachable → not ready.
		writeError(w, r, http.StatusServiceUnavailable, &apiError{
			Code:   "API_E_DB_UNAVAILABLE",
			Detail: "the search index is unreachable",
			Next:   "check the database; run: gseer doctor",
		})
		return
	}
	writeData(w, r, map[string]any{
		"status": "ready",
		"index":  meta,
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeData(w, r, version.Current())
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	meta, err := s.search.Stats(r.Context())
	if err != nil {
		writeSearchError(w, r, err)
		return
	}
	writeData(w, r, meta)
}

// searchRequest is the shared shape for GET params and POST body.
type searchRequest struct {
	Query  string   `json:"query"`
	Mode   string   `json:"mode,omitempty"`
	Sort   string   `json:"sort,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Cursor string   `json:"cursor,omitempty"`
	Fields []string `json:"fields,omitempty"`
}

func (s *Server) runSearch(w http.ResponseWriter, r *http.Request, req searchRequest) {
	res, err := s.search.Search(r.Context(), search.Query{
		GQL:       req.Query,
		Mode:      req.Mode,
		Sort:      req.Sort,
		Limit:     req.Limit,
		Cursor:    req.Cursor,
		Fields:    req.Fields,
		WithStats: true, // freshness notice + fixture label ride on every response
	})
	if err != nil {
		writeSearchError(w, r, err)
		return
	}
	writeData(w, r, map[string]any{
		"rows":        res.Rows,
		"count":       res.Query.RowCount,
		"has_more":    res.HasMore,
		"next_cursor": res.NextCursor,
		"query":       res.Query,
		"notice":      res.Notice,
		"index":       res.Index,
	})
}

func (s *Server) handleSearchGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := searchRequest{
		Query:  q.Get("q"),
		Mode:   q.Get("mode"),
		Sort:   q.Get("sort"),
		Cursor: q.Get("cursor"),
	}
	if v := q.Get("limit"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &req.Limit); err != nil {
			writeError(w, r, http.StatusBadRequest, &apiError{
				Code: "API_E_INVALID_LIMIT", Detail: "limit must be an integer",
				Next: "try: limit=100",
			})
			return
		}
	}
	if f := q.Get("fields"); f != "" {
		req.Fields = splitCSV(f)
	}
	s.runSearch(w, r, req)
}

// handleFacets serves GET /api/v1/facets — facet aggregates over the
// SAME GQL filter and SearchService as /search (v0.1.0, owner directive
// §3).  Parameters: q (required), mode, facets (comma-separated
// dimensions), max_values.
func (s *Server) handleFacets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fq := search.FacetsQuery{
		GQL:  q.Get("q"),
		Mode: q.Get("mode"),
	}
	if v := q.Get("max_values"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &fq.MaxValues); err != nil {
			writeError(w, r, http.StatusBadRequest, &apiError{
				Code: "API_E_INVALID_MAX_VALUES", Detail: "max_values must be an integer",
				Next: "try: max_values=10",
			})
			return
		}
	}
	if f := q.Get("facets"); f != "" {
		fq.Facets = splitCSV(f)
	}
	res, err := s.search.Facets(r.Context(), fq)
	if err != nil {
		writeSearchError(w, r, err)
		return
	}
	writeData(w, r, map[string]any{
		"facets": res.Facets,
		"query":  res.Query,
	})
}

// handleExport serves GET /api/v1/export — the v0.1.0 streaming export
// (owner directive §4).  Parameters: q (required), format (json|csv|
// ndjson), mode, sort, fields, max (row cap; 0 = unlimited).  The
// response body streams per page; the Content-Disposition filename is
// server-generated (never client-controlled).
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := export.Request{
		Query: search.Query{
			GQL:  q.Get("q"),
			Mode: q.Get("mode"),
			Sort: q.Get("sort"),
		},
		Format: q.Get("format"),
	}
	if req.Format == "" {
		req.Format = export.FormatJSON
	}
	if v := q.Get("max"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &req.MaxRows); err != nil {
			writeError(w, r, http.StatusBadRequest, &apiError{
				Code: "API_E_INVALID_MAX", Detail: "max must be an integer",
				Next: "try: max=10000 (0 = unlimited)",
			})
			return
		}
	}
	if f := q.Get("fields"); f != "" {
		req.Query.Fields = splitCSV(f)
	}

	// Content-Type + a SERVER-generated filename (client input never
	// reaches the header — header-injection surface stays closed).
	var contentType, ext string
	switch req.Format {
	case export.FormatCSV:
		contentType, ext = "text/csv; charset=utf-8", "csv"
	case export.FormatNDJSON:
		contentType, ext = "application/x-ndjson", "ndjson"
	case export.FormatJSON:
		contentType, ext = "application/json", "json"
	default:
		writeError(w, r, http.StatusBadRequest, &apiError{
			Code: "API_E_INVALID_FORMAT",
			Detail: fmt.Sprintf("unsupported export format %q (supported: %s)",
				req.Format, strings.Join(export.Formats, ", ")),
			Next: "try: format=ndjson",
		})
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="gseer-export-%s.%s"`, time.Now().UTC().Format("20060102-150405"), ext))
	w.WriteHeader(http.StatusOK)

	if _, err := export.Stream(r.Context(), s.search, req, w); err != nil {
		// Headers are already written — the error rides the stream tail
		// as an NDJSON error record (parsers see it; the partial data is
		// honestly labeled by the footer contract).
		_ = json.NewEncoder(w).Encode(map[string]any{"record": "error", "error": err.Error()})
	}
}

func (s *Server) handleSearchPost(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, &apiError{
			Code:   "API_E_BAD_BODY",
			Detail: "the request body is not valid JSON: " + err.Error(),
			Next:   `send {"query": "port==443"}`,
		})
		return
	}
	s.runSearch(w, r, req)
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, &apiError{
			Code: "API_E_BAD_BODY", Detail: "the request body is not valid JSON: " + err.Error(),
			Next: `send {"query": "port==443"}`,
		})
		return
	}
	plan, err := s.search.Plan(search.Query{GQL: req.Query, Mode: req.Mode, Sort: req.Sort, Limit: req.Limit, Fields: req.Fields})
	if err != nil {
		writeSearchError(w, r, err)
		return
	}
	writeData(w, r, map[string]any{
		"valid":  true,
		"mode":   plan.Mode.String(),
		"branch": plan.Branch.String(),
	})
}

func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, &apiError{
			Code: "API_E_BAD_BODY", Detail: "the request body is not valid JSON: " + err.Error(),
			Next: `send {"query": "port==443"}`,
		})
		return
	}
	plan, err := s.search.Plan(search.Query{GQL: req.Query, Mode: req.Mode, Sort: req.Sort, Limit: req.Limit, Cursor: req.Cursor, Fields: req.Fields})
	if err != nil {
		writeSearchError(w, r, err)
		return
	}
	writeData(w, r, map[string]any{
		"mode":    plan.Mode.String(),
		"branch":  plan.Branch.String(),
		"sql":     plan.SQL,
		"args":    plan.Args,
		"limit":   plan.Limit,
		"sort":    plan.Sort,
		"explain": plan.Explain(),
	})
}

// writeSearchError maps a search.Service error to the API envelope:
//   - QueryError  → 400 API_E_QUERY (code from the GQL layer preserved in detail)
//   - TimeoutError → 503 API_E_QUERY_TIMEOUT
//   - UnavailableError → 503 API_E_DB_UNAVAILABLE
func writeSearchError(w http.ResponseWriter, r *http.Request, err error) {
	var qe *search.QueryError
	if ok := errors.As(err, &qe); ok {
		writeError(w, r, http.StatusBadRequest, &apiError{
			Code: "API_E_QUERY", Detail: qe.Error(),
		})
		return
	}
	if search.AsTimeout(err) {
		writeError(w, r, http.StatusServiceUnavailable, &apiError{
			Code:   "API_E_QUERY_TIMEOUT",
			Detail: "the query exceeded its time budget and was canceled",
			Next:   "narrow the query or raise the timeout (cap 30s)",
		})
		return
	}
	if search.AsUnavailable(err) {
		writeError(w, r, http.StatusServiceUnavailable, &apiError{
			Code:   "API_E_DB_UNAVAILABLE",
			Detail: "the search index is unreachable",
			Next:   "check the database; run: gseer doctor",
		})
		return
	}
	writeError(w, r, http.StatusInternalServerError, &apiError{
		Code:   "API_E_INTERNAL",
		Detail: "the search failed unexpectedly",
		Next:   "retry; if it persists run: gseer doctor",
	})
}

// splitCSV splits a comma-separated query parameter.
func splitCSV(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if part := s[start:i]; part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

// requireAuth enforces Bearer-token auth when a TokenHash is configured.
// Without a token the server is loopback-only (enforced at startup), so
// unauthenticated loopback access is the documented v0.0.1 default.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.TokenHash == "" {
			// No token configured: allow loopback only (defense in depth
			// on top of the startup bind check).
			if !isLoopbackRequest(r) {
				writeError(w, r, http.StatusUnauthorized, &apiError{
					Code:   "API_E_UNAUTHORIZED",
					Detail: "this server has no API token configured and therefore only accepts loopback requests",
					Next:   "configure server.token in the config file to enable remote access",
				})
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		tok := bearerToken(r)
		if tok == "" {
			writeError(w, r, http.StatusUnauthorized, &apiError{
				Code:   "API_E_UNAUTHORIZED",
				Detail: "missing Authorization: Bearer <token>",
				Next:   "send the API token from server.token in the Authorization header",
			})
			return
		}
		sum := sha256.Sum256([]byte(tok))
		got := hexEncode(sum[:])
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.TokenHash)) != 1 {
			writeError(w, r, http.StatusUnauthorized, &apiError{
				Code:   "API_E_UNAUTHORIZED",
				Detail: "invalid API token",
				Next:   "check the token against server.token in the operator's config",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && equalFold(h[:len(prefix)], prefix) {
		return h[len(prefix):]
	}
	return ""
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 32
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func isLoopbackRequest(r *http.Request) bool {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return ip.IsLoopback()
}

func hexEncode(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexDigits[v>>4]
		out[i*2+1] = hexDigits[v&0x0f]
	}
	return string(out)
}
