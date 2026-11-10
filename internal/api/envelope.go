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
	"encoding/json"
	"net/http"
	"time"
)

// envelope.go — the standard API response envelope (SPEC-07):
//
//      {"data": …, "error": null, "meta": {"request_id": …, "took_ms": …}}
//      {"data": null, "error": {"code","detail","why","next"}, "meta": {…}}
//
// Every response — success and failure — carries meta.request_id so
// operators can correlate a client report with server logs.

// apiError is the error object inside the envelope.
type apiError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Why    string `json:"why,omitempty"`
	Next   string `json:"next,omitempty"`
}

type meta struct {
	RequestID string `json:"request_id"`
	TookMs    int64  `json:"took_ms"`
}

type envelope struct {
	Data  any       `json:"data"`
	Error *apiError `json:"error"`
	Meta  meta      `json:"meta"`
}

// requestMeta injects the meta-carrier into the request context so the
// writers can stamp request_id / took_ms.  Implemented with a plain
// context value keyed by the unexported type.
type metaCarrier struct {
	id    string
	start time.Time
}

type metaKey struct{}

// writeData renders a success envelope (HTTP 200; errors use writeError
// with an explicit status).
func writeData(w http.ResponseWriter, r *http.Request, data any) {
	m := metaFrom(r)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(envelope{Data: data, Meta: m})
}

// writeError renders a failure envelope with the given status.
func writeError(w http.ResponseWriter, r *http.Request, status int, e *apiError) {
	m := metaFrom(r)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Error: e, Meta: m})
}

func metaFrom(r *http.Request) meta {
	if c, ok := r.Context().Value(metaKey{}).(*metaCarrier); ok && c != nil {
		return meta{RequestID: c.id, TookMs: time.Since(c.start).Milliseconds()}
	}
	return meta{}
}
