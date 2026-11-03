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

package api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/Lutfifakee-Project/GSEER/internal/httpx"
)

// security_test.go — the M16 security review regression pins for the
// REST API surface: hardening headers on every response and the
// trusted-proxy client-IP resolution end to end (rate limiter sees the
// resolved IP, XFF spoofing is dead by default).

func TestAPISecurityHeaders(t *testing.T) {
	srv := New(nil, "", 0)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff")
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing frame-deny")
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Error("missing referrer policy")
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("missing CSP")
	}
}

func TestAPITrustedProxyEndToEnd(t *testing.T) {
	srv := New(nil, "", 0)
	trusted, err := parseTrustedForTest([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	srv.TrustedProxies = trusted

	// Two requests from the same spoofed XFF identity but different
	// UNTRUSTED peers must occupy two independent rate buckets (the
	// spoofed header is ignored).
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.RemoteAddr = "198.51.100." + itoaSlot(i) + ":1000"
		req.Header.Set("X-Forwarded-For", "8.8.8.8")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("untrusted-peer request %d: status %d (spoofed XFF must not merge buckets)", i, rec.Code)
		}
	}

	// A request THROUGH the trusted proxy resolves the last XFF hop.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "10.0.0.5:2000"
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.42")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("trusted-proxy request: status %d", rec.Code)
	}
}

func parseTrustedForTest(specs []string) ([]netip.Prefix, error) {
	return httpx.ParseTrustedProxies(specs)
}

func itoaSlot(i int) string { return string(rune('1' + i)) }

// The rate limiter must key on the RESOLVED client IP: through a trusted
// proxy, two real clients behind one proxy address do not share a bucket.
func TestAPIRateLimiterKeysOnResolvedIP(t *testing.T) {
	srv := New(nil, "", 3) // tiny window
	trusted, _ := parseTrustedForTest([]string{"127.0.0.1"})
	srv.TrustedProxies = trusted

	exhaust := func(client string) int {
		code := http.StatusOK
		for i := 0; i < 10; i++ {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
			req.RemoteAddr = "127.0.0.1:9000"
			req.Header.Set("X-Forwarded-For", client)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			code = rec.Code
		}
		return code
	}
	if got := exhaust("203.0.113.1"); got != http.StatusTooManyRequests {
		t.Errorf("client A should exhaust a 3-request window, got %d", got)
	}
	// Client B (different XFF identity through the same trusted proxy)
	// still has budget.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "127.0.0.1:9000"
	req.Header.Set("X-Forwarded-For", "203.0.113.2")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("client B must not inherit client A's exhausted bucket (got %d)", rec.Code)
	}
}
