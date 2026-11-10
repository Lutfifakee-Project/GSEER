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

// api_test.go — unit tests for the REST surface that need no database:
// routing, the response envelope, auth (token + loopback), rate-limit
// headers, and the validate/explain paths (pure GQL compilation).
// DB-backed handlers (search, ready, stats) are covered by the
// integration suite.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/search"
)

// testServer builds an API server whose search service has a nil pool —
// safe for the routes that never touch the database (health, version,
// auth, rate limit, validate/explain which only compile).  DB-backed
// handlers (search, ready, stats) are covered by the integration suite.
// rateLimitPerMin controls the limiter (use a big value to disable).
func testServer(tokenHash string) *Server {
	return testServerLimit(tokenHash, 3) // tiny limit so the limiter is exercisable
}

func testServerLimit(tokenHash string, limit int) *Server {
	return New(search.New(nil), tokenHash, limit)
}

func do(t *testing.T, h http.Handler, method, target, token string, body io.Reader) (*http.Response, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	// httptest defaults RemoteAddr to TEST-NET (192.0.2.1); a tokenless
	// server only serves loopback, so tests originate from loopback.
	req.RemoteAddr = "127.0.0.1:65535"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	var parsed map[string]any
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &parsed)
	return res, parsed
}

func TestHealthAndVersionNoAuth(t *testing.T) {
	h := testServer("").Handler()

	res, body := do(t, h, "GET", "/api/v1/health", "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("health: %d", res.StatusCode)
	}
	if body["data"].(map[string]any)["status"] != "ok" {
		t.Errorf("health data wrong: %v", body)
	}
	if body["meta"].(map[string]any)["request_id"] == "" {
		t.Errorf("meta.request_id missing: %v", body["meta"])
	}

	res, body = do(t, h, "GET", "/api/v1/version", "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("version: %d", res.StatusCode)
	}
	if body["data"].(map[string]any)["version"] == "" {
		t.Errorf("version data wrong: %v", body["data"])
	}
}

func TestAuthTokenRequiredAndVerified(t *testing.T) {
	tok := "correct-horse-battery-staple"
	sum := sha256.Sum256([]byte(tok))
	hash := hex.EncodeToString(sum[:])
	h := testServerLimit(hash, 1000).Handler()

	// No token → 401 with API_E_UNAUTHORIZED.
	res, body := do(t, h, "GET", "/api/v1/search?q=port==443", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.StatusCode)
	}
	if body["error"].(map[string]any)["code"] != "API_E_UNAUTHORIZED" {
		t.Errorf("wrong error code: %v", body["error"])
	}

	// Wrong token → 401.
	res, _ = do(t, h, "GET", "/api/v1/search?q=port==443", "wrong", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong token, got %d", res.StatusCode)
	}

	// Correct token passes auth — proven against /validate, which only
	// compiles (the DB-backed handlers need the integration suite).
	res, body = do(t, h, "POST", "/api/v1/validate", tok,
		strings.NewReader(`{"query": "port == 443"}`))
	if res.StatusCode != 200 {
		t.Fatalf("correct token rejected: %d (%v)", res.StatusCode, body)
	}
	if body["data"].(map[string]any)["valid"] != true {
		t.Errorf("validate under auth wrong: %v", body)
	}

	// Health/version stay unauthenticated even with a token configured.
	res, _ = do(t, h, "GET", "/api/v1/health", "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("health must stay unauthenticated, got %d", res.StatusCode)
	}
}

func TestAuthNoTokenLoopbackOnly(t *testing.T) {
	h := testServer("").Handler()
	// httptest requests originate from 127.0.0.1 → loopback allowed.
	res, _ := do(t, h, "GET", "/api/v1/search?q=port==443", "", nil)
	if res.StatusCode == http.StatusUnauthorized {
		t.Fatal("loopback request rejected on a tokenless server")
	}
}

func TestRateLimitHeadersAnd429(t *testing.T) {
	h := testServer("").Handler()

	var sawHeaders bool
	var got429 bool
	for i := 0; i < 10; i++ {
		res, body := do(t, h, "GET", "/api/v1/health", "", nil)
		if res.Header.Get("X-RateLimit-Limit") != "3" {
			t.Fatalf("missing/incorrect X-RateLimit-Limit: %q", res.Header.Get("X-RateLimit-Limit"))
		}
		if res.Header.Get("X-RateLimit-Remaining") == "" || res.Header.Get("X-RateLimit-Reset") == "" {
			t.Fatal("missing X-RateLimit-Remaining/Reset headers")
		}
		sawHeaders = true
		if res.StatusCode == http.StatusTooManyRequests {
			got429 = true
			if body["error"].(map[string]any)["code"] != "API_E_RATE_LIMITED" {
				t.Fatalf("429 body wrong: %v", body["error"])
			}
			if res.Header.Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
			break
		}
	}
	if !sawHeaders {
		t.Fatal("rate limit headers never seen")
	}
	if !got429 {
		t.Fatal("limiter never fired at limit=3 over 10 requests")
	}
}

func TestValidateHandler(t *testing.T) {
	h := testServer("").Handler()

	// Valid query → valid:true with mode/branch.
	res, body := do(t, h, "POST", "/api/v1/validate", "",
		strings.NewReader(`{"query": "port == 443 && exists(http.title)"}`))
	if res.StatusCode != 200 {
		t.Fatalf("validate: %d", res.StatusCode)
	}
	data := body["data"].(map[string]any)
	if data["valid"] != true || data["mode"] != "service" {
		t.Errorf("validate data wrong: %v", data)
	}

	// Invalid query → 400 API_E_QUERY with the GQL error text.
	res, body = do(t, h, "POST", "/api/v1/validate", "",
		strings.NewReader(`{"query": "bogusfield == 1"}`))
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", res.StatusCode)
	}
	if body["error"].(map[string]any)["code"] != "API_E_QUERY" {
		t.Errorf("expected API_E_QUERY, got %v", body["error"])
	}

	// Malformed body → 400 API_E_BAD_BODY.
	res, body = do(t, h, "POST", "/api/v1/validate", "", strings.NewReader(`{oops`))
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", res.StatusCode)
	}
	if body["error"].(map[string]any)["code"] != "API_E_BAD_BODY" {
		t.Errorf("expected API_E_BAD_BODY, got %v", body["error"])
	}
}

func TestExplainHandler(t *testing.T) {
	h := testServer("").Handler()
	res, body := do(t, h, "POST", "/api/v1/explain", "",
		strings.NewReader(`{"query": "port == 443", "mode": "asset"}`))
	if res.StatusCode != 200 {
		t.Fatalf("explain: %d", res.StatusCode)
	}
	data := body["data"].(map[string]any)
	if data["mode"] != "asset" {
		t.Errorf("explain mode wrong: %v", data["mode"])
	}
	if sql, ok := data["sql"].(string); !ok || sql == "" {
		t.Errorf("explain sql missing: %v", data)
	}
	if _, ok := data["explain"].(string); !ok {
		t.Errorf("explain text missing: %v", data)
	}
}

func TestUnknownRoute404Envelope(t *testing.T) {
	h := testServer("").Handler()
	res, _ := do(t, h, "GET", "/api/v1/nonexistent", "", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", res.StatusCode)
	}
}

func TestRateLimiterWindowReset(t *testing.T) {
	l := newRateLimiter(1, 10*time.Millisecond)
	ok1, rem1, _ := l.take("1.2.3.4", time.Now())
	if !ok1 || rem1 != 0 {
		t.Fatalf("first take failed: %v %v", ok1, rem1)
	}
	ok2, _, reset := l.take("1.2.3.4", time.Now())
	if ok2 {
		t.Fatal("second take inside the window must be denied")
	}
	time.Sleep(15 * time.Millisecond)
	ok3, rem3, reset3 := l.take("1.2.3.4", time.Now())
	if !ok3 || rem3 != 0 {
		t.Fatal("take after the window must be allowed")
	}
	if !reset3.After(reset) {
		t.Fatal("reset time did not advance to the new window")
	}
}

// TestAPIFacetsContract — the /api/v1/facets route: auth-gated like the
// search surface, parameter-validated before any database contact
// (these branches run against the nil-pool test server).
func TestAPIFacetsContract(t *testing.T) {
	tok := "facet-unit-token"
	sum := sha256.Sum256([]byte(tok))
	hash := hex.EncodeToString(sum[:])
	h := testServerLimit(hash, 1000).Handler()

	// No token → 401 (the facets surface is authenticated like search).
	res, body := do(t, h, "GET", "/api/v1/facets?q=port==443", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for facets without token, got %d", res.StatusCode)
	}
	if body["error"].(map[string]any)["code"] != "API_E_UNAUTHORIZED" {
		t.Errorf("wrong error code: %v", body["error"])
	}

	// bad max_values → 400 before the service is touched.
	res, body = do(t, h, "GET", "/api/v1/facets?q=port==443&max_values=abc", tok, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad max_values, got %d", res.StatusCode)
	}
	if body["error"].(map[string]any)["code"] != "API_E_INVALID_MAX_VALUES" {
		t.Errorf("wrong error code: %v", body["error"])
	}

	// Empty q → the SearchService's own E_QUERY_EMPTY (same semantics
	// as search — no separate facet validation path).
	res, body = do(t, h, "GET", "/api/v1/facets?q=%20%20", tok, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty filter, got %d", res.StatusCode)
	}
	if code := body["error"].(map[string]any)["code"]; code != "API_E_QUERY" && code != "E_QUERY_EMPTY" {
		t.Errorf("wrong error code: %v", body["error"])
	}
}
