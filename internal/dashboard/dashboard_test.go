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

package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Lutfifakee-Project/GSEER/internal/search"
)

// dashboard_test.go — the M16 security review regression pins for the
// dashboard surface: hardening headers, the GET-only no-CSRF-surface
// invariant, and template-escaping sanity for attacker-controlled input.

// newTestDashboard builds the dashboard over search.New(nil): a real
// SearchService with no pool.  All tests below use queries that fail GQL
// validation BEFORE any database contact — the render path under test
// (headers, escaping, method guard) never needs rows.
func newTestDashboard(t *testing.T) http.Handler {
	t.Helper()
	return New(search.New(nil)).Handler()
}

func TestDashboardSecurityHeaders(t *testing.T) {
	h := newTestDashboard(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d", rec.Code)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff header")
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing frame-deny header")
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Error("missing referrer policy")
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Error("missing CSP")
	}
}

func TestDashboardGETOnlyNoCSRFState(t *testing.T) {
	h := newTestDashboard(t)
	// A state-changing method is refused (405): the surface stays
	// read-only by construction, and with no cookies or writes there is
	// no CSRF surface — this pin keeps it that way.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("q=x")))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / status = %d, want 405", rec.Code)
	}
	if rec.Header().Get("Allow") == "" {
		t.Error("405 must advertise Allow")
	}
	// No cookies are ever set (session-less by design).  The query here
	// fails GQL validation before any DB contact (nil service safe).
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/?q=port==", nil))
	if sc := rec2.Header().Values("Set-Cookie"); len(sc) != 0 {
		t.Errorf("dashboard set cookies: %v", sc)
	}
}

func TestDashboardEscapesAttackerInput(t *testing.T) {
	h := newTestDashboard(t)
	xss := `<script>alert(1)</script>`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?q="+xss, nil))
	body := rec.Body.String()
	if strings.Contains(body, "<script>") {
		t.Error("raw script tag leaked through the template")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected escaped script tag in output")
	}
}

func TestDashboardUnknownPathIs404(t *testing.T) {
	h := newTestDashboard(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown path status = %d, want 404", rec.Code)
	}
}
